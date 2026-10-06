package server

import (
	"context"
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

// harnessProviderSpec describes v1's configured endpoint in pi-ai's shape. The
// turn's own model is always registered: the catalog may not know a custom
// endpoint's model, and pi-ai must be able to resolve every model it is asked
// for.
func harnessProviderSpec(c *llm.Client) harness.ProviderSpec {
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
	add(harness.ModelSpec{ID: c.Model, Name: c.Model})
	for _, m := range llm.ModelsForBaseURL(c.BaseURL) {
		ms := harness.ModelSpec{ID: m.ID, Name: m.Name, ContextWindow: m.Context, Reasoning: m.Reasoning != nil}
		if m.ImageInput {
			ms.Input = []string{"text", "image"}
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
// executor, and remembers each result so the UI event for a finished tool can
// report success exactly as the built-in loop does.
type harnessToolRunner struct {
	exec *agent.Executor

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
	r.mu.Lock()
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
	provider := harnessProviderSpec(params.Client)
	ens, err := bridge.Ensure(ctx, harness.EnsureRequest{
		V1SessionID:   convID,
		Cwd:           p.Path,
		Instructions:  agent.BuildSystemPrompt(&params),
		Provider:      provider,
		Model:         harness.ModelRef{Provider: provider.ID, ModelID: model},
		ThinkingLevel: params.ReasoningEffort,
		ToolDefs:      harnessToolDefs(params),
	})
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
	subID := fmt.Sprintf("%s:%d", convID, time.Now().UnixNano())
	q, stop, err := bridge.Watch(ctx, subID, sidecarID)
	if err != nil {
		return nil, err
	}
	defer stop()

	runner := &harnessToolRunner{exec: params.Exec, results: map[string]harness.ToolResult{}}
	unregister := bridge.Register(sidecarID, runner)
	defer unregister()

	// The request id is what makes a resubmit idempotent: the sidecar keeps the
	// submission record under it, so a duplicate never runs the turn twice.
	requestID := fmt.Sprintf("v1-%d", time.Now().UnixNano())
	if _, err := bridge.Submit(ctx, sidecarID, requestID, params.Message, "queue"); err != nil {
		return nil, err
	}
	return s.consumeHarnessTurn(ctx, bridge, sidecarID, q, runner, params, model, emit)
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

	persist := func() error {
		if text.Len() == 0 && reasoning.Len() == 0 {
			return nil
		}
		_, err := s.st.AddMessage(params.Project.ID, params.SessionID, "assistant", text.String(), "", model, reasoning.String(), "", "")
		text.Reset()
		reasoning.Reset()
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

	// Aborted is set when the turn was cancelled, so a second cancellation is
	// not mistaken for a new stop request.
	var aborted bool

	for {
		// Polled on every batch, not only when the stream idles: a turn that
		// streams continuously would otherwise never notice a steer. The drain is
		// only there to guarantee the poll happens when events stop arriving.
		pollSteer()
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
