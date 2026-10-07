package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"v1/internal/agent"
	"v1/internal/harness"
	"v1/internal/llm"
	"v1/internal/store"
)

// This file is the v1 half of the pi-durable harness: it hands one chat turn to
// the sidecar and translates the sidecar's agent events back into the SSE
// events the chat client already understands. Tools, permissions and
// transcripts stay here — the sidecar only owns the model call and the turn
// loop.

// harnessToolDefs are the tool definitions the sidecar advertises to the
// model: exactly the set the built-in loop would send for this turn, taken
// from the same source (agent.ChatParams.ToolSet) so the two paths cannot
// drift. Every one is executed by Go (agent.Executor), so path guards,
// approvals, background jobs and MCP tools behave identically.
func harnessToolDefs(params agent.ChatParams) []harness.ToolDef {
	set := params.ToolSet()
	out := make([]harness.ToolDef, 0, len(set))
	for _, t := range set {
		out = append(out, harness.ToolDef{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}
	return out
}

// SetHarness attaches the pi-durable bridge. Without one — or with
// V1_HARNESS=go — chat turns run on the built-in Go agent loop.
func (s *Server) SetHarness(b *harness.Bridge) {
	s.harnessMu.Lock()
	s.harness = b
	s.harnessMu.Unlock()
}

func (s *Server) harnessBridge() *harness.Bridge {
	s.harnessMu.RLock()
	defer s.harnessMu.RUnlock()
	return s.harness
}

// harnessEnabled reports whether chat turns should run on the sidecar.
func (s *Server) harnessEnabled() bool {
	return s.cfg.HarnessEnabled() && s.harnessBridge() != nil
}

// harnessConversationID is the pi-durable conversation behind one v1 chat
// session. It is derived, not stored: the same session always maps to the same
// conversation, so a restart or a retry resumes the durable transcript.
// harnessUserContent turns a v1 user turn into pi-durable content parts. With
// no attachments it stays a plain string; with them, images become image parts
// and text files are inlined the same way agent.userMessage inlines them, so
// both harnesses put the same thing in front of the model.
func harnessUserContent(text string, atts []agent.Attachment) any {
	if len(atts) == 0 {
		return text
	}
	parts := []harness.InputPart{{Type: "text", Text: text}}
	for _, a := range atts {
		if a.Kind == "image" {
			parts = append(parts, harness.InputPart{Type: "image", Data: a.Content, MimeType: a.MIME})
			continue
		}
		parts = append(parts, harness.InputPart{Type: "text", Text: "Attached file: " + a.Name + "\n```\n" + a.Content + "\n```"})
	}
	return parts
}

// harnessEnsureRequest builds the conversation.ensure call for one turn. The
// instructions and the tool definitions both come from the functions the
// built-in loop uses — agent.BuildSystemPrompt and ChatParams.ToolSet — so a
// turn's prompt and its tool set cannot depend on which harness runs it.
func harnessEnsureRequest(params agent.ChatParams, model string) harness.EnsureRequest {
	provider := harnessProviderSpec(params.Client, model)
	return harness.EnsureRequest{
		V1SessionID:   harnessConversationID(params.Project.ID, params.SessionID),
		Cwd:           params.Project.Path,
		Instructions:  agent.BuildSystemPrompt(&params),
		Provider:      provider,
		Model:         harness.ModelRef{Provider: provider.ID, ModelID: model},
		ThinkingLevel: params.ReasoningEffort,
		ToolDefs:      harnessToolDefs(params),
	}
}

func harnessConversationID(projectID, sessionID string) string {
	return projectID + ":" + sessionID
}

// harnessProviderID derives a stable provider id from the endpoint. The
// sidecar keys stored API keys and usage by provider id, so it must not drift
// between turns for the same base URL.
func harnessProviderID(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return "v1"
	}
	return u.Hostname()
}

// harnessProviderSpec describes v1's configured endpoint in pi-ai's shape.
//
// The turn's own model is registered first, and it is not necessarily the
// client's: the user can pick any model v1 offers for the endpoint, including
// ones the static catalog does not know (a newer model from the provider's
// live list, or a custom endpoint's). pi-durable refuses a model missing from
// the supplied catalog, so omitting it fails the turn before the model is ever
// called.
func harnessProviderSpec(c *llm.Client, turnModel string) harness.ProviderSpec {
	id := harnessProviderID(c.BaseURL)
	// v1's own client only ever speaks OpenAI chat completions, so the
	// sidecar must run the turn on the same protocol.
	spec := harness.ProviderSpec{ID: id, Name: id, BaseURL: c.BaseURL, APIKey: c.APIKey, API: "openai-completions"}
	seen := map[string]bool{}
	add := func(m harness.ModelSpec) {
		if m.ID == "" || seen[m.ID] {
			return
		}
		seen[m.ID] = true
		spec.Models = append(spec.Models, m)
	}
	add(harness.ModelSpec{ID: turnModel, Name: turnModel, Input: []string{"text", "image"}})
	add(harness.ModelSpec{ID: c.Model, Name: c.Model, Input: []string{"text", "image"}})
	for _, m := range llm.ModelsForBaseURL(c.BaseURL) {
		// Image input is advertised unconditionally, deliberately.
		//
		// pi-ai drops image parts for a model whose input list omits "image",
		// silently: no error, no note, the model just answers about an image it
		// never saw. v1's catalog cannot be trusted for this — the openrouter
		// entry for google/gemini-2.5-flash carries no imageInput while every
		// other provider's entry for the same model does — and the built-in loop
		// never consulted it anyway: it sends images and only strips them after
		// the provider rejects them (agent.go:458). Advertising the capability
		// keeps the two harnesses putting the same thing in front of the model,
		// and a model that truly cannot take images now fails loudly instead of
		// answering about an image it was never shown.
		ms := harness.ModelSpec{
			ID:            m.ID,
			Name:          m.Name,
			ContextWindow: m.Context,
			Reasoning:     m.Reasoning != nil,
			Input:         []string{"text", "image"},
		}
		add(ms)
	}
	return spec
}

// harnessToolDetail summarizes a tool call for the UI: the file path, command
// or question the call is about.
func harnessToolDetail(args json.RawMessage) string {
	var a struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Command  string `json:"command"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &a)
	}
	for _, s := range []string{a.Path, a.FilePath, a.Command} {
		if s != "" {
			return s
		}
	}
	return ""
}

// harnessToolRunner executes the sidecar's host-tool calls on the turn's
// executor, remembers each result so the UI event for a finished tool can
// report success exactly as the built-in loop does, and persists the result as
// a "tool" row.
//
// The durable transcript lives in the sidecar, but v1's store is what the UI
// reloads from, and it renders tool cards from these rows: without them a
// reloaded pi session showed the assistant's prose and no tool history at all.
type harnessToolRunner struct {
	exec *agent.Executor

	store     *store.Store
	projectID string
	sessionID string

	mu      sync.Mutex
	results map[string]harness.ToolResult
}

func (r *harnessToolRunner) RunTool(ctx context.Context, call harness.ToolCall) (harness.ToolResult, error) {
	if r.exec == nil {
		return harness.ToolResult{Text: "no tool executor is available for this turn", IsError: true}, nil
	}
	out, err := r.exec.Execute(ctx, call.Tool, string(call.Args))
	res := harness.ToolResult{Text: out}
	if err != nil {
		res.IsError = true
		if out == "" {
			res.Text = err.Error()
		} else {
			res.Text = out + "\n" + err.Error()
		}
	}
	// screenshot_app hands its PNG to the agent loop through PendingImage. The
	// built-in loop injects it as a follow-up user message; here it rides on the
	// tool result instead, which is where a vision model expects it.
	r.mu.Lock()
	if png := r.exec.PendingImage; len(png) > 0 {
		r.exec.PendingImage = nil
		res.Images = []harness.ToolImage{{Data: base64.StdEncoding.EncodeToString(png), MimeType: "image/png"}}
	}
	if r.results != nil {
		r.results[call.CallID] = res
	}
	r.mu.Unlock()

	// The built-in loop writes one "tool" row per call, tagged with the call id
	// and tool name (agent.go:631). The UI parses that tag to label the card, so
	// the shape has to match.
	if r.store != nil {
		meta, _ := json.Marshal(map[string]any{"tool_call_id": call.CallID, "name": call.Tool})
		if _, err := r.store.AddMessage(r.projectID, r.sessionID, "tool", res.Text, string(meta), "", "", "", ""); err != nil {
			log.Printf("harness: persisting tool result failed: %v", err)
		}
	}
	return res, nil
}

// Authorize is a no-op by design: v1's approval gate lives inside the tool
// implementations (Executor.Perm), so a call needing the user's consent blocks
// in RunTool and asks over the existing SSE permission flow. The sidecar asks
// before the call so a denial is recorded durably, but the answer here is
// always "ask the tool".
func (r *harnessToolRunner) Authorize(context.Context, harness.ToolCall) (string, error) {
	return "", nil
}

func (r *harnessToolRunner) result(callID string) harness.ToolResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.results[callID]
}

// runHarnessTurn runs one chat turn on the pi-durable sidecar. It mirrors
// RunChat's contract — persist the user message, stream ChatEvents, persist
// each assistant message as it lands, return the turn's usage — so the code
// around it (queueing, retries, checkpoints, the done event) is unchanged.
//
// The durable transcript lives in the sidecar's store; v1's store remains the
// UI's source of truth, which is why assistant messages are persisted here as
// they complete rather than being read back from the sidecar.
func (s *Server) runHarnessTurn(ctx context.Context, p *store.Project, params agent.ChatParams, emit func(agent.ChatEvent)) (*agent.TurnResult, error) {
	bridge := s.harnessBridge()
	if bridge == nil {
		return nil, errors.New("chat: the pi-durable sidecar is not running")
	}
	model := params.Model
	if model == "" {
		model = params.Client.Model
	}
	// The same executor wiring RunChat does, so a tool that depends on per-turn
	// state (plan mode, the background manager, the session id) cannot work on
	// one harness and fail on the other.
	// The pi path owns compaction in the durable transcript, so v1's own snapshot
	// is not injected: the sidecar would otherwise carry the same history
	// verbatim *and* as a summary.
	params.SkipCompactionSnapshot = true
	params.PrepareExecutor()
	// The same transcript bookkeeping RunChat does: a retry re-runs an existing
	// user message, a continue resumes a partial one, a fresh turn appends.
	if params.LastUserID > 0 {
		if err := s.st.DeleteMessagesAfter(p.ID, params.SessionID, params.LastUserID); err != nil {
			return nil, err
		}
	} else if params.ContinueFromID <= 0 {
		if _, err := s.st.AddMessage(p.ID, params.SessionID, "user", params.Message, "", model, "", "", agent.MarshalAttachments(params.Attachments)); err != nil {
			return nil, err
		}
	}
	if params.Exec != nil {
		params.Exec.SessionID = params.SessionID
	}

	convID := harnessConversationID(p.ID, params.SessionID)
	req := harnessEnsureRequest(params, model)
	// Rejoin the conversation this session already has, if any. pi-durable mints
	// the id, so it has to be remembered across turns — and across a sidecar
	// restart, where the sidecar's own v1SessionId map is gone and a fresh
	// conversation would silently lose the history.
	if stored, err := params.Store.HarnessConversationID(p.ID, params.SessionID); err == nil && stored != "" {
		req.ConversationID = stored
	}
	ens, err := bridge.Ensure(ctx, req)
	if err != nil {
		return nil, err
	}
	// pi-durable mints the conversation id; the sidecar addresses every later
	// call — and every inbound host-tool call — by that id, not by v1's session
	// id. v1SessionId stays the stable identity used to find the conversation
	// again.
	sidecarID := ens.ConversationID
	if sidecarID == "" {
		sidecarID = convID
	}
	if sidecarID != req.ConversationID {
		if err := params.Store.SetHarnessConversationID(p.ID, params.SessionID, sidecarID); err != nil {
			log.Printf("harness: persisting conversation id failed: %v", err)
		}
	}
	// v1's store is the transcript retry, edit and delete trim; the durable
	// transcript is a second copy of the same conversation. Rewinding it back to
	// where v1's store ends keeps the two from drifting, so a retried question
	// reaches the model once instead of twice. This has to run before the watch:
	// a rewind forks a new conversation, and a watch is per conversation.
	if keep := harnessKeepUserTurns(params.Store, p.ID, params.SessionID); keep >= 0 {
		rewound, err := bridge.Rewind(ctx, sidecarID, keep)
		if err != nil {
			return nil, err
		}
		if rewound != "" && rewound != sidecarID {
			sidecarID = rewound
			if err := params.Store.SetHarnessConversationID(p.ID, params.SessionID, sidecarID); err != nil {
				log.Printf("harness: persisting rewound conversation id failed: %v", err)
			}
		}
	}
	subID := fmt.Sprintf("%s:%d", convID, time.Now().UnixNano())
	q, stop, err := bridge.Watch(ctx, subID, sidecarID)
	if err != nil {
		return nil, err
	}
	defer stop()

	runner := &harnessToolRunner{
		exec:      params.Exec,
		store:     params.Store,
		projectID: params.Project.ID,
		sessionID: params.SessionID,
		results:   map[string]harness.ToolResult{},
	}
	unregister := bridge.Register(sidecarID, runner)
	defer unregister()

	// The request id is what makes a resubmit idempotent: the sidecar keeps the
	// submission record under it, so a duplicate never runs the turn twice.
	requestID := fmt.Sprintf("v1-%d", time.Now().UnixNano())
	if _, err := bridge.Submit(ctx, sidecarID, requestID, harnessUserContent(params.Message, params.Attachments), "queue"); err != nil {
		return nil, err
	}
	return s.consumeHarnessTurn(ctx, bridge, sidecarID, q, runner, params, model, emit)
}

// harnessKeepUserTurns is how many of the sidecar's user turns must survive the
// submission: v1's own user turns, less the turn being submitted, which is
// already in the store by now (the bookkeeping above either appended it or
// trimmed back to it).
//
// A store that does not end on a user message is left unadjusted, and an
// unreadable store reports -1, meaning "leave the durable transcript alone".
// Both fall the same way on purpose: the sidecar only rewinds when it has more
// user turns than v1 does, so a miscount can skip a rewind but never force one.
func harnessKeepUserTurns(st *store.Store, projectID, sessionID string) int {
	if st == nil {
		return -1
	}
	msgs, err := st.ListMessages(projectID, sessionID)
	if err != nil {
		return -1
	}
	users := 0
	for _, m := range msgs {
		if m.Role == "user" {
			users++
		}
	}
	if len(msgs) > 0 && msgs[len(msgs)-1].Role == "user" {
		users--
	}
	if users < 0 {
		users = 0
	}
	return users
}

// toolSummary is the one-line tool result shown in the transcript, matching
// the built-in loop's cap.
func toolSummary(result string) string {
	if len(result) > 300 {
		return result[:300] + "..."
	}
	return result
}

// reconcile brings the client's view in line with the text pi-durable actually
// committed, and adopts the committed value for persistence. Deltas are the
// live view of a throttled stream, so the committed text is usually a strict
// extension of what streamed; only the missing tail is sent.
func reconcile(streamed *strings.Builder, committed, typ string, emit func(agent.ChatEvent)) {
	if committed == "" {
		return
	}
	prefix := streamed.String()
	switch {
	case committed == prefix:
		// The stream already showed all of it.
	case strings.HasPrefix(committed, prefix):
		emit(agent.ChatEvent{Type: typ, Text: committed[len(prefix):]})
	default:
		// The committed text is not an extension of what streamed: the round was
		// rewritten (a retry or an abort). Send it in full so the answer is
		// visible; the live view may show a stale prefix until the next reload.
		emit(agent.ChatEvent{Type: typ, Text: committed})
	}
	streamed.Reset()
	streamed.WriteString(committed)
}

// entryToolJSON renders a committed assistant entry's tool calls in the shape
// the UI reads back: messages.tool_json holding an OpenAI-style tool_calls
// array, identical to what the built-in loop stores for the same round.
func entryToolJSON(entry json.RawMessage) string {
	calls := entryToolCalls(entry)
	if len(calls) == 0 {
		return ""
	}
	b, err := json.Marshal(map[string]any{"tool_calls": calls})
	if err != nil {
		return ""
	}
	return string(b)
}

// entryToolCalls extracts the tool calls pi-durable committed on an assistant
// entry. Arguments arrive as a parsed object (pi-ai's shape) and are
// re-serialized to the JSON string v1's ToolCall carries.
func entryToolCalls(entry json.RawMessage) []llm.ToolCall {
	if len(entry) == 0 {
		return nil
	}
	var rec struct {
		Model []struct {
			Content []struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"content"`
		} `json:"model"`
	}
	if err := json.Unmarshal(entry, &rec); err != nil {
		return nil
	}
	var out []llm.ToolCall
	for _, m := range rec.Model {
		for _, c := range m.Content {
			if c.Type != "toolCall" || c.Name == "" {
				continue
			}
			args := "{}"
			if len(c.Arguments) > 0 {
				args = string(c.Arguments)
			}
			out = append(out, llm.ToolCall{
				ID:       c.ID,
				Type:     "function",
				Function: llm.FunctionCall{Name: c.Name, Arguments: args},
			})
		}
	}
	return out
}

// entryFailure reports why a committed assistant entry is a failure. A model
// call that ends in an error (bad request, exhausted retries) still commits an
// entry, with stopReason "error" and the reason in errorMessage; a call the
// client aborted commits one with stopReason "aborted".
func entryFailure(entry json.RawMessage) (msg string, aborted bool) {
	if len(entry) == 0 {
		return "", false
	}
	var rec struct {
		Model []struct {
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"model"`
	}
	if err := json.Unmarshal(entry, &rec); err != nil {
		return "", false
	}
	for _, m := range rec.Model {
		switch m.StopReason {
		case "error":
			if m.ErrorMessage != "" {
				return m.ErrorMessage, false
			}
			return "the model call failed", false
		case "aborted":
			return "", true
		}
	}
	return "", false
}

// entryText extracts the assistant text and reasoning pi-durable committed on
// an entry. Reasoning lives under "thinking" (pi-ai's ThinkingContent), not
// "text".
func entryText(entry json.RawMessage) (text, reasoning string) {
	if len(entry) == 0 {
		return "", ""
	}
	var rec struct {
		Model []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"model"`
	}
	if err := json.Unmarshal(entry, &rec); err != nil {
		return "", ""
	}
	var t, r strings.Builder
	for _, m := range rec.Model {
		if m.Role != "assistant" {
			continue
		}
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		}
		if err := json.Unmarshal(m.Content, &parts); err != nil {
			continue
		}
		for _, p := range parts {
			switch p.Type {
			case "text":
				t.WriteString(p.Text)
			case "thinking":
				r.WriteString(p.Thinking)
			}
		}
	}
	return t.String(), r.String()
}

// entryUsage extracts the token usage the sidecar committed on an entry. This
// is the reliable per-round source: pi-durable throttles the in-flight partial
// (progress.partialIntervalMs, 100 ms by default), so a fast round may never
// emit a message_update. Tool-result entries carry no usage.
func entryUsage(entry json.RawMessage) *harness.Usage {
	if len(entry) == 0 {
		return nil
	}
	var rec struct {
		Model []struct {
			Usage *harness.Usage `json:"usage"`
		} `json:"model"`
	}
	if err := json.Unmarshal(entry, &rec); err != nil {
		return nil
	}
	var out *harness.Usage
	for _, m := range rec.Model {
		if m.Usage == nil {
			continue
		}
		if out == nil {
			out = &harness.Usage{}
		}
		out.Input += m.Usage.Input
		out.Output += m.Usage.Output
		out.CacheRead += m.Usage.CacheRead
		out.CacheWrite += m.Usage.CacheWrite
		out.TotalTokens += m.Usage.TotalTokens
		if m.Usage.Cost != nil && m.Usage.Cost.Total != nil {
			if out.Cost == nil || out.Cost.Total == nil {
				out.Cost = &harness.Cost{}
			}
			total := *m.Usage.Cost.Total
			if out.Cost.Total != nil {
				total += *out.Cost.Total
			}
			out.Cost.Total = &total
		}
	}
	return out
}

// consumeHarnessTurn translates the sidecar's agent events into v1's SSE
// events until the run ends.
func (s *Server) consumeHarnessTurn(ctx context.Context, bridge *harness.Bridge, sidecarID string, q *harness.EventQueue, runner *harnessToolRunner, params agent.ChatParams, model string, emit func(agent.ChatEvent)) (*agent.TurnResult, error) {
	turn := &agent.TurnResult{Model: model}
	var text, reasoning strings.Builder
	var in, out int64
	var cost float64
	var hasCost bool
	var lastContext int64

	// The assistant's tool calls, as the UI reads them back from the row's
	// tool_json; set when a round commits and consumed by persist.
	var toolJSON string

	persist := func() error {
		if text.Len() == 0 && reasoning.Len() == 0 && toolJSON == "" {
			return nil
		}
		_, err := s.st.AddMessage(params.Project.ID, params.SessionID, "assistant", text.String(), toolJSON, model, reasoning.String(), "", "")
		text.Reset()
		reasoning.Reset()
		toolJSON = ""
		return err
	}
	// One model call's usage is that round's own totals, so rounds are summed as
	// they complete: Input/Output cover the whole turn for billing, Context is
	// the last round's prompt size, as in the built-in loop.
	addUsage := func(u *harness.Usage) {
		if u == nil {
			return
		}
		in += u.Input
		out += u.Output
		if u.Cost != nil && u.Cost.Total != nil {
			cost += *u.Cost.Total
			hasCost = true
		}
		lastContext = u.Input + u.Output
	}
	// The partial's usage, held until the round commits. pi-durable commits the
	// in-flight partial at most once per progress.partialIntervalMs, so a fast
	// round never produces a message_update at all.
	var partial *harness.Usage

	// Mid-run steering: the built-in loop drains params.Steer between rounds, so
	// the pi path polls it here and hands each message to the sidecar as a
	// steer, which joins the running turn after the current tool round.
	pollSteer := func() {
		if params.Steer == nil || bridge == nil {
			return
		}
		for _, msg := range params.Steer() {
			if msg == "" {
				continue
			}
			if err := bridge.Steer(ctx, sidecarID, fmt.Sprintf("v1-steer-%d", time.Now().UnixNano()), msg); err != nil {
				log.Printf("harness: steer failed: %v", err)
				continue
			}
			// The client shows it as a user message, exactly as the built-in
			// loop's injected_message does.
			emit(agent.ChatEvent{Type: "injected_message", Text: msg})
		}
	}

	// Finished background commands are the other thing the built-in loop folds
	// into the conversation mid-turn (agent.go:431): their output is already
	// persisted by the completion callback, so this only has to reach the model,
	// which a steer does. Without it the model would never learn how a background
	// command it started ended.
	pollBackground := func() {
		if params.PollBackground == nil || bridge == nil {
			return
		}
		for _, r := range params.PollBackground() {
			if r.Text == "" {
				continue
			}
			if err := bridge.Steer(ctx, sidecarID, fmt.Sprintf("v1-background-%d", time.Now().UnixNano()), r.Text); err != nil {
				log.Printf("harness: background result injection failed: %v", err)
				continue
			}
			emit(agent.ChatEvent{Type: "injected_message", MessageID: r.MessageID, Text: r.Text})
		}
	}

	// Aborted is set when the turn was cancelled, so a second cancellation is
	// not mistaken for a new stop request.
	var aborted bool

	for {
		// Polled on every batch, not only when the stream idles: a turn that
		// streams continuously would otherwise never notice a steer. The drain is
		// only there to guarantee the poll happens when events stop arriving.
		pollSteer()
		pollBackground()
		// A short drain timeout is what lets steering be polled mid-turn: Drain
		// blocks until an event arrives, so a bounded wait keeps the loop
		// responsive to the queue without spinning.
		drainCtx, cancelDrain := context.WithTimeout(ctx, 250*time.Millisecond)
		events, err := q.Drain(drainCtx)
		cancelDrain()
		if err != nil {
			if ctx.Err() != nil && !aborted {
				// The user stopped the turn. Tell the sidecar, or it keeps
				// generating into a transcript nobody is watching — burning tokens
				// and durable entries. The abort needs its own context, since this
				// one is already cancelled.
				aborted = true
				abortCtx, cancelAbort := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				if bridge != nil {
					if err := bridge.Abort(abortCtx, sidecarID); err != nil {
						log.Printf("harness: abort failed: %v", err)
					}
				}
				cancelAbort()
				// Keep what the model already produced; the rest is abandoned with
				// the generation, exactly as the built-in loop drops its in-flight
				// partial on a stop.
				_ = persist()
				return turn, context.Canceled
			}
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				continue
			}
			// The stream ended — the sidecar died, or the aborted turn finished
			// winding down. Keep whatever the model already produced.
			_ = persist()
			return turn, err
		}
		done := false
		for _, ev := range events {
			if s.cfg.HarnessDebug {
				log.Printf("harness event: type=%s usage=%v changes=%d", ev.Type, ev.Usage != nil, len(ev.Changes))
			}
			switch ev.Type {
			case "message_update":
				for _, ch := range ev.Changes {
					switch ch.Type {
					case "text_delta":
						if ch.Delta != "" {
							text.WriteString(ch.Delta)
							emit(agent.ChatEvent{Type: "delta", Text: ch.Delta})
						}
					case "thinking_delta":
						if ch.Delta != "" {
							reasoning.WriteString(ch.Delta)
							emit(agent.ChatEvent{Type: "reasoning", Text: ch.Delta})
						}
					}
				}
				if ev.Usage != nil {
					partial = ev.Usage
				}
			case "message_end":
				// Fires for tool-result entries too. The committed entry is the
				// authority for both text and usage: pi-durable throttles the live
				// partial (progress.partialIntervalMs, 100 ms), so a fast round may
				// stream no deltas at all and only this event carries the answer.
				if t, r := entryText(ev.Entry); t != "" || r != "" {
					reconcile(&text, t, "delta", emit)
					reconcile(&reasoning, r, "reasoning", emit)
				}
				// A round that called tools carries them on the row, the way the
				// built-in loop stores res.ToolCalls (agent.go:508).
				toolJSON = entryToolJSON(ev.Entry)
				if err := persist(); err != nil {
					return turn, err
				}
				u := entryUsage(ev.Entry)
				if u == nil {
					// The partial is a fallback for a round that never committed
					// (aborted or faulted after streaming).
					u = partial
				}
				addUsage(u)
				partial = nil
				// A model call that failed is still committed as an entry, with
				// stopReason "error"; pi-durable emits no task_failed for it, so this
				// is the only place the failure surfaces. Without this check the turn
				// ends in a silent, empty done.
				if msg, aborted := entryFailure(ev.Entry); msg != "" || aborted {
					if aborted {
						return turn, context.Canceled
					}
					return turn, errors.New(msg)
				}
			case "tool_execution_start":
				emit(agent.ChatEvent{Type: "tool_start", Name: ev.ToolName, Detail: harnessToolDetail(ev.Args)})
			case "tool_execution_end":
				// An absent entry means the tool task faulted or was orphaned;
				// a tool that ran and failed is reported by its own runner.
				res := runner.result(ev.ToolCallID)
				emit(agent.ChatEvent{Type: "tool_end", Name: ev.ToolName, OK: ev.Entry != nil && !res.IsError, Detail: toolSummary(res.Text)})
			case "compaction_start":
				// pi-durable compacts its own transcript (threshold, overflow, or a
				// manual request). The built-in loop compacts in memory without a
				// word; surfacing it matters here because the durable transcript is
				// what the model's context is built from, so the context meter is
				// about to drop and the user deserves to know why.
				if ev.Blocking {
					emit(agent.ChatEvent{Type: "info", Text: "Compacting the conversation to stay within the model's context window."})
				}
			case "compaction_end":
				emit(agent.ChatEvent{Type: "info", Text: "Compacted the conversation; older turns are now a summary."})
			case "auto_retry_start":
				// The sidecar retries a failed model call on its own; say so, the
				// way the built-in loop announces a mid-reply resume.
				emit(agent.ChatEvent{Type: "info", Text: fmt.Sprintf("The model call failed; retrying automatically (attempt %d).", ev.Attempt)})
			case "task_failed":
				// The generation faulted (provider error, retries exhausted).
				_ = persist()
				return turn, errors.New(ev.ErrorMessage())
			case "run_end":
				done = true
			}
		}
		if done {
			break
		}
	}
	if err := persist(); err != nil {
		return turn, err
	}
	if in > 0 || out > 0 {
		u := &agent.Usage{Input: in, Output: out, Model: model, Context: lastContext}
		if hasCost {
			u.Cost = &cost
		}
		turn.Usage = u
	}
	return turn, nil
}
