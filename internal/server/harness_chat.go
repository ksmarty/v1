package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"v1/internal/agent"
	"v1/internal/harness"
	"v1/internal/llm"
	"v1/internal/sanitize"
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
	// Tell the sidecar which extensions are enabled straight away. Until it is
	// told, it loads everything on disk, and a turn must never run an extension
	// the user disabled.
	if state := s.reloadExtensions(context.Background()); state["reloaded"] != true {
		log.Printf("extensions: the initial reload failed: %v", state["reason"])
	}
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

// handleDelegateMessages returns a delegated sub-agent's durable transcript.
// The child is a sidecar conversation v1 never opens as a chat session, so the
// UI fetches it on demand by the id carried in the delegate tool result.
func (s *Server) handleDelegateMessages(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	b := s.harnessBridge()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "the harness is not running")
		return
	}
	conversationID := r.PathValue("conversationId")
	if conversationID == "" {
		writeError(w, http.StatusBadRequest, "a conversation id is required")
		return
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	page, err := b.Entries(r.Context(), conversationID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// harnessConversationID is the pi-durable conversation behind one v1 chat
// session. It is derived, not stored: the same session always maps to the same
// conversation, so a restart or a retry resumes the durable transcript.
// scrubHarnessText cleans text on its way out to the sidecar. The built-in loop
// scrubs at the LLM API boundary (llm.sanitizeMessagesForAPI); the harness path
// has no boundary of its own, so the text v1 hands over — the user turn, the
// system prompt, steering messages and tool results — is scrubbed where it is
// produced. Without this, one ANSI escape or control byte (a command's raw
// output, an attached file, a pasted message) reaches the provider untouched,
// and an endpoint that enforces a character pattern rejects the whole request
// with "The string did not match the expected pattern". Background results are
// already cleaned where they are formatted (agent.sanitizeBackgroundText).
func scrubHarnessText(s string) string { return sanitize.Text(s) }

// harnessUserContent turns a v1 user turn into pi-durable content parts. With
// no attachments it stays a plain string; with them, images become image parts
// and text files are inlined the same way agent.userMessage inlines them, so
// both harnesses put the same thing in front of the model.
func harnessUserContent(text string, atts []agent.Attachment) any {
	text = scrubHarnessText(text)
	if len(atts) == 0 {
		return text
	}
	// An empty text part is omitted rather than sent as "": providers reject an
	// empty text block (Anthropic is explicit that they must be non-empty), and a
	// turn carrying only an attachment has nothing to put in it.
	parts := []harness.InputPart{}
	if text != "" {
		parts = append(parts, harness.InputPart{Type: "text", Text: text})
	}
	for _, a := range atts {
		if a.Kind == "image" {
			// Image bytes are base64 and hold no control characters, so the
			// scrubber could only risk corrupting the payload.
			parts = append(parts, harness.InputPart{Type: "image", Data: a.Content, MimeType: a.MIME})
			continue
		}
		parts = append(parts, harness.InputPart{Type: "text", Text: scrubHarnessText("Attached file: " + a.Name + "\n```\n" + agent.InlineTextAttachment(a) + "\n```")})
	}
	return parts
}

// harnessEnsureRequest builds the conversation.ensure call for one turn. The
// instructions and the tool definitions both come from the functions the
// built-in loop uses — agent.BuildSystemPrompt and ChatParams.ToolSet — so a
// turn's prompt and its tool set cannot depend on which harness runs it.
func harnessEnsureRequest(params agent.ChatParams, model, exposure string, meta func(string) (harnessModelMeta, bool)) harness.EnsureRequest {
	provider := harnessProviderSpec(params.Client, model, meta)
	req := harness.EnsureRequest{
		V1SessionID:   harnessConversationID(params.Project.ID, params.SessionID),
		Cwd:           params.Project.Path,
		Instructions:  scrubHarnessText(agent.BuildSystemPrompt(&params)),
		Provider:      provider,
		Model:         harness.ModelRef{Provider: provider.ID, ModelID: model},
		ThinkingLevel: params.ReasoningEffort,
		ToolDefs:      harnessToolDefs(params),
		ToolExposure:  exposure,
	}
	// MCP tools are the per-turn additions (ExtraTools); in search mode they stay
	// hidden until search_tools reveals them. A name that is not in the advertised
	// set is harmless — the sidecar simply never hides it.
	if exposure == "search" {
		deferred := make([]string, 0, len(params.ExtraTools))
		for _, t := range params.ExtraTools {
			deferred = append(deferred, t.Function.Name)
		}
		req.DeferredTools = deferred
	}
	return req
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

// harnessModelMeta is what v1 knows about a model beyond its id, in the shape
// the harness spec needs. A lookup returns false when it knows nothing.
type harnessModelMeta struct {
	Name          string
	ContextWindow int
	MaxTokens     int
	Reasoning     bool
}

// harnessModelMeta reports what v1 knows about a model. The catalog served to
// the client comes first — it is the one models.dev filled in, which is where
// reasoning support and the output ceiling live — and pi's own catalog is the
// tiebreaker for a model models.dev does not know (`deepseek-v4.1-flash`).
// Without either, pi-ai is told the model cannot think and has no ceiling, and
// falls back to a flat 8192 output budget a reasoning model can spend entirely
// on thinking before it calls a tool or answers.
func (s *Server) harnessModelMeta(model string) (harnessModelMeta, bool) {
	var meta harnessModelMeta
	known := false
	fill := func(name string, contextWindow, maxTokens int, reasoning bool) {
		known = true
		if meta.Name == "" {
			meta.Name = name
		}
		if meta.ContextWindow == 0 {
			meta.ContextWindow = contextWindow
		}
		if meta.MaxTokens == 0 {
			meta.MaxTokens = maxTokens
		}
		if reasoning {
			meta.Reasoning = true
		}
	}
	if cat := s.providerCatalog(); cat != nil {
		for _, p := range cat.Providers {
			for _, m := range p.Models {
				if !modelMatchesID(m.ID, model) && !modelMatchesID(model, m.ID) {
					continue
				}
				fill(m.Name, m.Context, m.Output, m.Reasoning != nil)
			}
		}
	}
	if info, ok := s.piModel(model); ok {
		fill(info.Name, info.ContextWindow, info.MaxTokens, info.Reasoning)
	}
	return meta, known
}

// harnessProviderSpec describes v1's configured endpoint in pi-ai's shape.
//
// The turn's own model is registered first, and it is not necessarily the
// client's: the user can pick any model v1 offers for the endpoint, including
// ones the static catalog does not know (a newer model from the provider's
// live list, or a custom endpoint's). pi-durable refuses a model missing from
// the supplied catalog, so omitting it fails the turn before the model is ever
// called.
//
// meta supplies the model metadata the endpoint's own catalog does not publish
// (see harnessModelMeta); nil means "id only".
func harnessProviderSpec(c *llm.Client, turnModel string, meta func(string) (harnessModelMeta, bool)) harness.ProviderSpec {
	id := harnessProviderID(c.BaseURL)
	// v1's own client only ever speaks OpenAI chat completions, so the
	// sidecar must run the turn on the same protocol.
	spec := harness.ProviderSpec{ID: id, Name: id, BaseURL: c.BaseURL, APIKey: c.APIKey, API: "openai-completions", SessionHeader: llm.SessionHeaderForBaseURL(c.BaseURL), Headers: llm.StaticHeadersForBaseURL(c.BaseURL)}
	seen := map[string]bool{}
	add := func(m harness.ModelSpec) {
		if m.ID == "" || seen[m.ID] {
			return
		}
		seen[m.ID] = true
		spec.Models = append(spec.Models, m)
	}
	catalog := llm.ModelsForBaseURL(c.BaseURL)
	// catalogModel finds the catalog entry naming a model, accepting the
	// namespaced and bare spellings in either direction — the turn's model comes
	// from the picker, which may list "moonshotai/kimi-k3" for a catalog entry
	// spelled "kimi-k3", or the other way around.
	catalogModel := func(model string) *llm.ProviderModel {
		for i := range catalog {
			if modelMatchesID(catalog[i].ID, model) || modelMatchesID(model, catalog[i].ID) {
				return &catalog[i]
			}
		}
		return nil
	}
	// The turn's own model and the client's configured one are registered first,
	// because pi-durable refuses a model that is missing from the supplied
	// catalog — but never as a bare entry. De-duplication is by id, so a bare
	// entry shadows the catalog one and the turn would run on a model pi-ai
	// believes cannot think and has no output ceiling: no reasoning request (and
	// no thinking in the UI) for a reasoning model, and pi-ai's flat 8192-token
	// fallback, which a reasoning model can spend entirely on thinking before it
	// calls a tool or answers. That reads as "the agent runs but nothing shows".
	//
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
	addModel := func(modelID string) {
		if modelID == "" {
			return
		}
		ms := harness.ModelSpec{ID: modelID, Name: modelID, Input: []string{"text", "image"}}
		if m := catalogModel(modelID); m != nil {
			if m.Name != "" {
				ms.Name = m.Name
			}
			ms.ContextWindow = m.Context
			ms.MaxTokens = m.Output
			ms.Reasoning = m.Reasoning != nil
		}
		if meta != nil {
			if info, ok := meta(modelID); ok {
				if info.Name != "" {
					ms.Name = info.Name
				}
				if info.ContextWindow > 0 {
					ms.ContextWindow = info.ContextWindow
				}
				if info.MaxTokens > 0 {
					ms.MaxTokens = info.MaxTokens
				}
				if info.Reasoning {
					ms.Reasoning = true
				}
			}
		}
		add(ms)
	}
	addModel(turnModel)
	addModel(c.Model)
	// Every model the endpoint publishes is registered too, so picking another
	// one mid-conversation does not fail for a model the sidecar never saw.
	for _, m := range catalog {
		addModel(m.ID)
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
// executor and remembers each result so the UI event for a finished tool can
// report success exactly as the built-in loop does.
//
// The durable transcript lives in the sidecar, but v1's store is what the UI
// reloads from, and it renders tool cards from "tool" rows. Those rows are
// written once, from the `tool_execution_end` event, which fires for host tools
// and sidecar-run tools alike (see recordToolResult); writing here too would
// duplicate every host-tool result.
type harnessToolRunner struct {
	exec *agent.Executor
	// emit, when set, is the turn's event sink. Attached uses it to tell the UI
	// the child conversation id while a delegate is still running.
	emit func(agent.ChatEvent)

	mu       sync.Mutex
	results  map[string]harness.ToolResult
	inFlight int
}

// busy reports whether a tool is executing in this process. The sidecar stays
// silent while it waits for the result, so the turn's stall watchdog reads this
// before treating the quiet as a dead run.
func (r *harnessToolRunner) busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inFlight > 0
}

// Attached is called when a delegated child conversation is linked to this
// turn. It emits the child id so the UI can open the sub-agent's transcript
// live, rather than only once the delegate tool returns its marker.
func (r *harnessToolRunner) Attached(childID, _ string) {
	if r.emit == nil || childID == "" {
		return
	}
	r.emit(agent.ChatEvent{Type: "delegate_started", Detail: childID})
}

func (r *harnessToolRunner) RunTool(ctx context.Context, call harness.ToolCall) (harness.ToolResult, error) {
	// A tool that runs here is invisible to the turn's event loop, so the stall
	// watchdog has to know one is in flight (see harnessStallTimeout).
	r.mu.Lock()
	r.inFlight++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.inFlight--
		r.mu.Unlock()
	}()
	if r.exec == nil {
		return harness.ToolResult{Text: "no tool executor is available for this turn", IsError: true}, nil
	}
	out, display, err := r.exec.ExecuteWithDisplay(ctx, call.Tool, string(call.Args))
	res := harness.ToolResult{Text: out, Details: display}
	if err != nil {
		res.IsError = true
		if out == "" {
			res.Text = err.Error()
		} else {
			res.Text = out + "\n" + err.Error()
		}
	}
	// Tool output is raw by nature: command output carries ANSI escapes and a
	// file read can hold any byte. Scrub it once, before it is both handed back
	// to the sidecar (which puts it in front of the model) and persisted, so the
	// request and the row the UI reloads can never disagree.
	res.Text = scrubHarnessText(res.Text)
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
		if _, err := s.st.AddUserMessage(p.ID, params.SessionID, params.Message, model, agent.MarshalAttachments(params.Attachments)); err != nil {
			return nil, err
		}
	}
	if params.Exec != nil {
		params.Exec.SessionID = params.SessionID
	}

	convID := harnessConversationID(p.ID, params.SessionID)
	req := harnessEnsureRequest(params, model, s.toolExposure(p.OwnerID), s.harnessModelMeta)
	// Rejoin the conversation this session already has, if any. pi-durable mints
	// the id, so it has to be remembered across turns — and across a sidecar
	// restart, where the sidecar's own v1SessionId map is gone and a fresh
	// conversation would silently lose the history.
	if stored, err := params.Store.HarnessConversationID(p.ID, params.SessionID); err == nil && stored != "" {
		req.ConversationID = stored
	}
	// A poisoned sidecar session (a failed storage commit) answers every later
	// call with "reopen it", and only a fresh process can reopen it. When the
	// supervisor cannot get one, Ensure waits forever — and the run then never
	// ends: the chat sits on "Generation still running", every later message
	// queues behind a run that will never do anything, and only a server restart
	// clears it. Bound the wait so the turn fails visibly and the next message
	// runs instead.
	ensureCtx, cancelEnsure := context.WithTimeout(ctx, harnessEnsureTimeout)
	ens, err := bridge.Ensure(ensureCtx, req)
	cancelEnsure()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("the agent runtime stopped answering (%s) — try the message again", harnessEnsureTimeout)
		}
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
		exec:    params.Exec,
		emit:    emit,
		results: map[string]harness.ToolResult{},
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
		// rewritten (a retry or an abort). Replace the live value rather than
		// appending, or the stale fragment would be glued to the full text.
		emit(agent.ChatEvent{Type: typ, Text: committed, Replace: true})
	}
	streamed.Reset()
	streamed.WriteString(committed)
}

// unsavedText is the part of a committed round's text that a mid-round flush has
// not persisted yet: the flush wrote the prefix into its own row, so only the
// remainder belongs to the row that commits.
func unsavedText(committed, flushed string) string {
	if flushed == "" {
		return committed
	}
	if rest, ok := strings.CutPrefix(committed, flushed); ok {
		return rest
	}
	return committed
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
// recordToolResult persists one tool result as its own "tool" row, tagged with
// the id of the call it answers. The built-in loop writes these as it goes and
// the durable harness has to as well: the UI pairs a call with its result by
// that id and renders the result only when it finds the pairing.
func (s *Server) recordToolResult(projectID, sessionID, callID, name, text string, display json.RawMessage) error {
	meta := map[string]any{"tool_call_id": callID, "name": name}
	if len(display) > 0 {
		meta["display"] = display
	}
	mb, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = s.st.AddMessage(projectID, sessionID, "tool", text, string(mb), "", "", "", "")
	return err
}

// entryToolResult extracts the text and error flag of the tool result
// pi-durable committed on an entry. A result arrives as its own "toolResult"
// message rather than as assistant content, so entryText does not see it.
func entryToolResult(entry json.RawMessage) (string, bool) {
	if len(entry) == 0 {
		return "", false
	}
	var rec struct {
		Model []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			IsError bool            `json:"isError"`
		} `json:"model"`
	}
	if err := json.Unmarshal(entry, &rec); err != nil {
		return "", false
	}
	var text strings.Builder
	isError := false
	for _, m := range rec.Model {
		if m.Role != "toolResult" {
			continue
		}
		if m.IsError {
			isError = true
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(m.Content, &parts); err != nil {
			continue
		}
		for _, part := range parts {
			if part.Type == "text" {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String(), isError
}

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

// contextTokens is a round's context-window fill, counted exactly as pi counts
// it (pi-ai's calculateContextTokens): the provider's own total when it reports
// one, otherwise input + output + both cache figures. `input` is only the
// uncached prompt, so summing Input+Output omits the prompt served from cache —
// on a long conversation, most of what the model actually read.
func contextTokens(u *harness.Usage) int64 {
	if u == nil {
		return 0
	}
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.Input + u.Output + u.CacheRead + u.CacheWrite
}

// harnessUsageJSON renders a round's usage in the shape the built-in loop stores
// (agent.Usage), so the client's token and cost line and the context meter read
// both paths identically.
func harnessUsageJSON(u *harness.Usage, model string) string {
	if u == nil {
		return ""
	}
	out := map[string]any{
		// Input is the whole prompt, cached tokens included, matching the
		// built-in loop's PromptTokens. pi-ai reports only the uncached part as
		// Input, so adding the cache figures keeps "N in" the same number on both
		// paths — and correct when a proxy serves most of the prompt from cache.
		"input":   u.Input + u.CacheRead + u.CacheWrite,
		"output":  u.Output,
		"model":   model,
		"context": contextTokens(u),
	}
	if u.CacheRead > 0 {
		out["cached"] = u.CacheRead
	}
	if u.Cost != nil && u.Cost.Total != nil {
		out["cost"] = *u.Cost.Total
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// consumeHarnessTurn translates the sidecar's agent events into v1's SSE
// events until the run ends.
// harnessEnsureTimeout bounds the wait for the sidecar to accept a conversation.
// Without it a session poisoned by a failed commit, or a supervisor that cannot
// respawn the sidecar, leaves the turn blocked with no timeout at all.
const harnessEnsureTimeout = 90 * time.Second

// harnessStallTimeout is how long a turn may run with nothing in flight and no
// event from the sidecar before the runtime is asked whether it is still there.
// Only reached when no host tool is executing, so a long build or install is
// never mistaken for a stall.
const harnessStallTimeout = 60 * time.Second

// harnessStallCeiling caps how long a runtime that is alive but silent may hold a
// turn. A quiet turn is usually legitimate — a reasoning model on a large context
// can think for minutes before its first token, and some providers stream nothing
// at all until the round is done — but an unbounded wait is what leaves the chat
// on "Generation still running" forever.
const harnessStallCeiling = 10 * time.Minute

// harnessPingTimeout bounds the liveness probe: a runtime that cannot answer this
// quickly is treated as dead, which is the case the stall guard is for.
const harnessPingTimeout = 5 * time.Second

// stallWatch is a turn's silence watchdog. It cannot treat silence as death on its
// own: the sidecar says nothing while Go runs a tool, while a slow provider is
// still thinking, and while a model request is in flight before its first token.
// Two clocks keep those apart — lastActivity ends the wait as soon as the runtime
// proves it is alive (a ping), quietSince bounds the whole quiet stretch.
type stallWatch struct {
	// lastActivity is the last event, or the last time the runtime answered.
	lastActivity time.Time
	// quietSince is when the runtime last said something itself. A liveness probe
	// never moves it, or a wedged model call could hold a turn forever.
	quietSince time.Time
	// announced keeps the "still waiting" log to one line per quiet stretch.
	announced bool
}

func newStallWatch(now time.Time) *stallWatch {
	return &stallWatch{lastActivity: now, quietSince: now}
}

// spoke records a real event from the runtime.
func (w *stallWatch) spoke(now time.Time) {
	w.lastActivity, w.quietSince, w.announced = now, now, false
}

// busy records that a host tool is executing here, which the sidecar is waiting on.
func (w *stallWatch) busy(now time.Time) { w.lastActivity = now }

// due reports whether the silence has lasted long enough to ask the runtime.
func (w *stallWatch) due(now time.Time) bool {
	return now.Sub(w.lastActivity) >= harnessStallTimeout
}

// alive records a runtime that answered the probe. It returns false when the wait
// has already run past the ceiling, so the caller gives up instead of extending it
// again.
func (w *stallWatch) alive(now time.Time) bool {
	w.lastActivity = now
	if now.Sub(w.quietSince) > harnessStallCeiling {
		return false
	}
	return true
}

func (s *Server) consumeHarnessTurn(ctx context.Context, bridge *harness.Bridge, sidecarID string, q *harness.EventQueue, runner *harnessToolRunner, params agent.ChatParams, model string, emit func(agent.ChatEvent)) (*agent.TurnResult, error) {
	turn := &agent.TurnResult{Model: model}
	var text, reasoning strings.Builder
	// The part of the round in flight that a mid-round flush already persisted.
	// The round's remainder is written when it commits, so this prefix is what
	// keeps the two rows from covering the same words.
	var flushed, flushedReasoning string
	var in, out int64
	var cost float64
	var hasCost bool
	var cached int64
	var lastContext int64

	// The assistant's tool calls, as the UI reads them back from the row's
	// tool_json; set when a round commits and consumed by persist.
	var toolJSON string
	// Across the whole turn: a run that never wrote visible text and never
	// called a tool is a failure, not an answer (see the guard after the loop).
	var sawText, sawTool bool

	persist := func(round *harness.Usage) error {
		// Whatever a flush already wrote belongs to the round that is committing
		// now; the next round starts from nothing.
		flushed, flushedReasoning = "", ""
		if text.Len() == 0 && reasoning.Len() == 0 && toolJSON == "" {
			return nil
		}
		// The turn produced something the user can see if this round wrote text
		// or called a tool. Tracked here because persist is the one place that
		// sees both streamed deltas and the committed entry.
		if text.Len() > 0 {
			sawText = true
		}
		if toolJSON != "" {
			sawTool = true
		}
		// The row carries its own round's usage, as the built-in loop's does. The
		// context meter reads the newest assistant row's recorded context to report
		// the window fill, and the client reads the same row for its token and cost
		// line. Without it both fall back to counting the stored transcript, which
		// on this path never shrinks when pi-durable folds older entries into a
		// summary — so the meter reported a fill the provider never saw.
		_, err := s.st.AddMessage(params.Project.ID, params.SessionID, "assistant", text.String(), toolJSON, model, reasoning.String(), harnessUsageJSON(round, model), "")
		text.Reset()
		reasoning.Reset()
		toolJSON = ""
		return err
	}
	// flushRound persists the text and reasoning the round has streamed so far as
	// their own assistant row, so a message injected mid-round lands AFTER what
	// the model had already said. Ids are write order — the client sorts by them —
	// so without this the injected row took an id before the round it interrupted
	// and a reload put the user's message above the reply text that preceded it.
	flushRound := func() {
		if text.Len() == 0 && reasoning.Len() == 0 {
			return
		}
		said, thought := text.String(), reasoning.String()
		if said != "" {
			sawText = true
		}
		flushed += said
		flushedReasoning += thought
		if _, err := s.st.AddMessage(params.Project.ID, params.SessionID, "assistant", said, "", model, thought, "", ""); err != nil {
			log.Printf("chat: flushing a round before an injected message: %v", err)
		}
		text.Reset()
		reasoning.Reset()
	}
	// One model call's usage is that round's own totals, so rounds are summed as
	// they complete: Input/Output cover the whole turn for billing, Context is
	// the last round's prompt size, as in the built-in loop.
	addUsage := func(u *harness.Usage) {
		if u == nil {
			return
		}
		// pi-ai's Input is the uncached prompt only; the cache figures are the
		// rest of it. Sum the whole prompt so "N in" matches the built-in loop's
		// PromptTokens and does not collapse to a handful of tokens when a
		// compression proxy serves the history from its cache.
		in += u.Input + u.CacheRead + u.CacheWrite
		out += u.Output
		cached += u.CacheRead
		if u.Cost != nil && u.Cost.Total != nil {
			cost += *u.Cost.Total
			hasCost = true
		}
		lastContext = contextTokens(u)
	}
	// The partial's usage, held until the round commits. pi-durable commits the
	// in-flight partial at most once per progress.partialIntervalMs, so a fast
	// round never produces a message_update at all.
	var partial *harness.Usage

	// Mid-run steering: the built-in loop drains params.Steer between rounds, so
	// the pi path polls it here and hands each message to the sidecar as a
	// steer, which joins the running turn after the current tool round.
	pollSteer := func() {
		if params.Steer == nil {
			return
		}
		// No bridge means nowhere to deliver the steer, but the transcript still
		// records it — the same thing the built-in loop does with a nil client.
		// Production only reaches this path with a live bridge.
		deliver := func(text string) error {
			if bridge == nil {
				return nil
			}
			return bridge.Steer(ctx, sidecarID, fmt.Sprintf("v1-steer-%d", time.Now().UnixNano()), text)
		}
		for _, msg := range params.Steer() {
			if msg.Text == "" && len(msg.Attachments) == 0 {
				continue
			}
			// The sidecar takes text only: the attachments ride along as a note
			// so the model at least knows files were sent (the built-in loop
			// delivers them as real parts).
			steerText := scrubHarnessText(msg.Text)
			if len(msg.Attachments) > 0 {
				names := make([]string, 0, len(msg.Attachments))
				for _, a := range msg.Attachments {
					names = append(names, a.Name)
				}
				steerText = strings.TrimSpace(
					steerText + "\n\n[attached files: " + strings.Join(names, ", ") + "]",
				)
			}
			if err := deliver(steerText); err != nil {
				log.Printf("harness: steer failed: %v", err)
				// The steer was already drained from the queue, so put it back. A
				// message the user sent must not vanish because the running turn could
				// not take it; it runs as a follow-up turn when this one ends.
				if params.Requeue != nil {
					params.Requeue(msg)
				}
				continue
			}
			// The text the round already streamed is written first: the store's
			// ids are the transcript's order, so the steer has to land behind it.
			flushRound()
			// The client shows it as a user message, exactly as the built-in
			// loop's injected_message does — and v1's store has to hold it too, or
			// the steered turn disappears from the transcript on the next reload.
			msgID, addErr := s.st.AddInjectedMessage(
				params.Project.ID, params.SessionID, msg.Text, agent.SteerToolJSON, model,
				agent.MarshalAttachments(msg.Attachments),
			)
			if addErr != nil {
				log.Printf("chat: recording a steered message: %v", addErr)
			}
			emit(agent.ChatEvent{
				Type:        "injected_message",
				MessageID:   msgID,
				Text:        msg.Text,
				Tool:        "steer",
				ToolJSON:    agent.MarkerName(agent.SteerToolJSON),
				Attachments: agent.AttachmentMetas(msg.Attachments),
			})
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
			emit(agent.ChatEvent{Type: "injected_message", MessageID: r.MessageID, Text: r.Text, Tool: "background", ToolJSON: agent.MarkerName(agent.BackgroundToolJSON)})
		}
	}

	// Aborted is set when the turn was cancelled, so a second cancellation is
	// not mistaken for a new stop request.
	var aborted bool
	// The run can go quiet for a long time legitimately — a tool Go is executing,
	// a slow provider — so silence is probed rather than read as death (below).
	watch := newStallWatch(time.Now())
	// giveUp ends a turn that produced nothing more, and takes the generation down
	// with it. A turn v1 abandons but the sidecar keeps running is worse than the
	// stall itself: the model's every later tool call reaches a turn that no longer
	// exists, reads "no active turn for tool", and the chat shows it failing the
	// same way over and over.
	giveUp := func(msg string) (*agent.TurnResult, error) {
		_ = persist(partial)
		abortCtx, cancelAbort := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancelAbort()
		if bridge != nil {
			if err := bridge.Abort(abortCtx, sidecarID); err != nil {
				log.Printf("harness: aborting an abandoned turn failed: %v", err)
			}
		}
		return turn, errors.New(msg)
	}

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
		if len(events) > 0 {
			watch.spoke(time.Now())
		}
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
				_ = persist(partial)
				return turn, context.Canceled
			}
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				// A sidecar that stopped answering must not pin the session: the run
				// would stay active forever, so the chat shows "Generation still
				// running" for a turn that produced nothing and every later message
				// queues behind it until the container is restarted. Silence alone is
				// not that, though: a tool running in Go (a build, an install) and a
				// provider that has not produced a token yet both keep the sidecar
				// quiet for minutes. So past the timeout the runtime is asked.
				if runner.busy() {
					watch.busy(time.Now())
				} else if watch.due(time.Now()) {
					probeCtx, cancelProbe := context.WithTimeout(ctx, harnessPingTimeout)
					pingErr := bridge.Ping(probeCtx)
					cancelProbe()
					if pingErr != nil {
						log.Printf("harness: the runtime stopped answering (%v)", pingErr)
						return giveUp(fmt.Sprintf("the agent stopped responding (no activity for %s)", harnessStallTimeout))
					}
					now := time.Now()
					if !watch.alive(now) {
						return giveUp(fmt.Sprintf("the agent produced nothing for %s", harnessStallCeiling))
					}
					if !watch.announced {
						watch.announced = true
						log.Printf("harness: the runtime is alive but has said nothing for %s — still waiting on the model", now.Sub(watch.quietSince).Round(time.Second))
					}
				}
				continue
			}
			// The stream ended — the sidecar died, or the aborted turn finished
			// winding down. Keep whatever the model already produced.
			_ = persist(partial)
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
					// Only the part no flush has written yet: the committed entry
					// carries the whole round, and re-streaming its flushed prefix
					// would put the same words in two rows.
					reconcile(&text, unsavedText(t, flushed), "delta", emit)
					reconcile(&reasoning, unsavedText(r, flushedReasoning), "reasoning", emit)
				}
				// A round that called tools carries them on the row, the way the
				// built-in loop stores res.ToolCalls (agent.go:508).
				toolJSON = entryToolJSON(ev.Entry)
				u := entryUsage(ev.Entry)
				if u == nil {
					// The partial is a fallback for a round that never committed
					// (aborted or faulted after streaming).
					u = partial
				}
				// Persist with the usage in hand, so the row records it.
				if err := persist(u); err != nil {
					return turn, err
				}
				addUsage(u)
				// The context meter follows the turn as it runs, not only when it
				// ends: every round re-reads the conversation, so the round's prompt
				// size is the live fill.
				if lastContext > 0 {
					emit(agent.ChatEvent{Type: "context", Usage: &agent.Usage{Context: lastContext}})
				}
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
				// The full arguments, not a summary: the client renders the live card
				// from the same JSON the reload path parses (edit diff, write content,
				// todo/plan previews), so a call looks right during the turn and not
				// only after a reload. The summary is only a fallback for empty args.
				detail := string(ev.Args)
				if detail == "" || detail == "null" {
					detail = harnessToolDetail(ev.Args)
				}
				emit(agent.ChatEvent{Type: "tool_start", Name: ev.ToolName, Detail: detail})
			case "tool_execution_end":
				// An absent entry means the tool task faulted or was orphaned;
				// a tool that ran and failed is reported by its own runner.
				res := runner.result(ev.ToolCallID)
				text, isError := res.Text, res.IsError
				if text == "" {
					// A tool the sidecar ran itself — an extension's, or anything else
					// Go did not execute — leaves the runner empty. Its result lives
					// only on the entry pi-durable committed, and without it the call
					// renders in the transcript with nothing behind it.
					if t, e := entryToolResult(ev.Entry); t != "" || e {
						text, isError = t, e
					}
				}
				// Record the result the way the built-in loop does, so the UI can pair
				// it with its call and still show it after a reload.
				if err := s.recordToolResult(params.Project.ID, params.SessionID, ev.ToolCallID, ev.ToolName, text, res.Details); err != nil {
					log.Printf("chat: recording a tool result: %v", err)
				}
				// The result goes out whole, as the persisted row does: the client
				// parses it to render the command output, the edit diff and the
				// failure reason, so a truncated envelope shows as unparseable text
				// until the next reload reads the full row back.
				emit(agent.ChatEvent{Type: "tool_end", Name: ev.ToolName, OK: ev.Entry != nil && !isError, Detail: text, Display: res.Details})
				// A screenshot tool result carries a PNG for the model (res.Images).
				// Mirror the built-in loop: persist it as a user attachment and emit an
				// injected_message, so the image is in the chat log live and after a
				// reload instead of only in the model's context.
				for _, img := range res.Images {
					raw, decErr := base64.StdEncoding.DecodeString(img.Data)
					if decErr != nil {
						continue
					}
					mime := img.MimeType
					if mime == "" {
						mime = "image/png"
					}
					att := []agent.Attachment{{Name: "screenshot.png", MIME: mime, Kind: "image", Content: img.Data}}
					// No caption: the image is the message (see the built-in loop).
					flushRound()
					msgID, addErr := s.st.AddInjectedMessage(params.Project.ID, params.SessionID, "", agent.AgentToolJSON, "", agent.MarshalAttachments(att))
					if addErr != nil {
						log.Printf("chat: recording a screenshot: %v", addErr)
						continue
					}
					emit(agent.ChatEvent{
						Type:        "injected_message",
						MessageID:   msgID,
						Text:        "",
						Tool:        "agent",
						ToolJSON:    agent.MarkerName(agent.AgentToolJSON),
						Attachments: []agent.AttachmentMeta{{Name: "screenshot.png", MIME: mime, Kind: "image", Size: len(raw)}},
					})
				}
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
				// Persisted, not announced: a notice row is part of the
				// transcript, so it is still there after a reload and the reader
				// can see why the model stopped remembering earlier turns.
				notice := agent.CompactionNotice(ev.Reason)
				flushRound()
				msgID, addErr := s.st.AddMessage(
					params.Project.ID, params.SessionID, store.RoleNotice,
					notice, "", "", "", "", "",
				)
				if addErr != nil {
					log.Printf("chat: recording a compaction notice: %v", addErr)
				}
				emit(agent.ChatEvent{Type: "notice", MessageID: msgID, Text: notice})
			case "auto_retry_start":
				// The sidecar retries a failed model call on its own; say so, the
				// way the built-in loop announces a mid-reply resume.
				emit(agent.ChatEvent{Type: "info", Text: fmt.Sprintf("The model call failed; retrying automatically (attempt %d).", ev.Attempt)})
			case "task_failed":
				// The generation faulted (provider error, retries exhausted).
				_ = persist(partial)
				return turn, errors.New(ev.ErrorMessage())
			case "run_end":
				done = true
			}
		}
		if done {
			break
		}
	}
	if err := persist(partial); err != nil {
		return turn, err
	}
	// The built-in loop errors on an answer with no text, reasoning or tool
	// calls (agent.go:662). The harness path had no such guard: a reasoning
	// model that spent its whole output window thinking (stopReason "length")
	// committed a reasoning-only entry and the turn ended in a silent done,
	// so the user saw a thinking block and then nothing. Reasoning alone is
	// never a user-visible answer; surface it instead of swallowing it.
	if !sawText && !sawTool {
		return turn, errors.New("the model produced no answer — it may have run out of output tokens while thinking; send a follow-up to continue")
	}
	if in > 0 || out > 0 {
		u := &agent.Usage{Input: in, Output: out, Model: model, Context: lastContext, Cached: cached}
		if hasCost {
			u.Cost = &cost
		}
		turn.Usage = u
	}
	return turn, nil
}
