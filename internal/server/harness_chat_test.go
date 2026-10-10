package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"v1/internal/agent"
	"v1/internal/config"
	"v1/internal/harness"
	"v1/internal/llm"
	"v1/internal/store"
)

func newHarnessTestServer(t *testing.T) (*Server, *store.Project, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(config.Config{DataDir: t.TempDir(), AuthDisabled: true}, st)
	p := &store.Project{ID: store.NewID(), Name: "harness", Path: t.TempDir()}
	if err := s.st.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	session, err := s.st.EnsureDefaultSession(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, p, session.ID
}

// result records a tool result the way RunTool does, so the translation sees
// it on tool_execution_end.
func (r *harnessToolRunner) record(callID string, res harness.ToolResult) {
	r.mu.Lock()
	r.results[callID] = res
	r.mu.Unlock()
}

func delta(typ, text string) harness.Change {
	return harness.Change{Type: typ, Delta: text}
}

func TestConsumeHarnessTurnTranslatesEvents(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	q := harness.NewEventQueue()
	runner := &harnessToolRunner{results: map[string]harness.ToolResult{}}
	runner.record("call-ok", harness.ToolResult{Text: "file contents"})
	runner.record("call-fail", harness.ToolResult{Text: "boom", IsError: true})

	var events []agent.ChatEvent
	emit := func(ev agent.ChatEvent) { events = append(events, ev) }

	cost := 0.5
	q.Push([]harness.Event{
		// First model round: text, reasoning and a tool call.
		{Type: "message_update", Usage: &harness.Usage{Input: 100, Output: 20, Cost: &harness.Cost{Total: &cost}},
			Changes: []harness.Change{delta("thinking_delta", "why"), delta("text_delta", "Hel"), delta("text_delta", "lo")}},
		{Type: "message_end", Entry: json.RawMessage(`{"id":1,"kind":"assistant"}`)},
		{Type: "tool_execution_start", ToolCallID: "call-ok", ToolName: "read_file", Args: []byte(`{"path":"a.txt"}`)},
		{Type: "tool_execution_end", ToolCallID: "call-ok", ToolName: "read_file", Entry: json.RawMessage(`{"id":2,"kind":"toolResult"}`)},
		// A tool whose task faulted: no entry at all.
		{Type: "tool_execution_end", ToolCallID: "call-gone", ToolName: "run_command"},
		// A tool that ran and failed: an entry, but its runner reported an error.
		{Type: "tool_execution_start", ToolCallID: "call-fail", ToolName: "run_command", Args: []byte(`{"command":"false"}`)},
		{Type: "tool_execution_end", ToolCallID: "call-fail", ToolName: "run_command", Entry: json.RawMessage(`{"id":3,"kind":"toolResult"}`)},
		// Second model round after the tools.
		{Type: "message_update", Usage: &harness.Usage{Input: 150, Output: 10},
			Changes: []harness.Change{delta("text_delta", "second")}},
		{Type: "message_end", Entry: json.RawMessage(`{"id":4,"kind":"assistant"}`)},
	})
	q.Push([]harness.Event{{Type: "run_end"}})

	turn, err := s.consumeHarnessTurn(context.Background(), nil, "conv-1", q, runner,
		agent.ChatParams{Project: p, SessionID: sessionID}, "test-model", emit)
	if err != nil {
		t.Fatalf("consumeHarnessTurn: %v", err)
	}

	var texts, reasonings, toolStarts, toolEnds []agent.ChatEvent
	for _, ev := range events {
		switch ev.Type {
		case "delta":
			texts = append(texts, ev)
		case "reasoning":
			reasonings = append(reasonings, ev)
		case "tool_start":
			toolStarts = append(toolStarts, ev)
		case "tool_end":
			toolEnds = append(toolEnds, ev)
		}
	}
	if len(texts) != 3 || texts[0].Text != "Hel" || texts[1].Text != "lo" || texts[2].Text != "second" {
		t.Fatalf("text events = %+v", texts)
	}
	if len(reasonings) != 1 || reasonings[0].Text != "why" {
		t.Fatalf("reasoning events = %+v", reasonings)
	}
	if len(toolStarts) != 2 || toolStarts[0].Name != "read_file" || toolStarts[0].Detail != "a.txt" {
		t.Fatalf("tool_start events = %+v", toolStarts)
	}
	if len(toolEnds) != 3 {
		t.Fatalf("tool_end events = %+v", toolEnds)
	}
	// A finished tool is ok; a faulted or erroring one is not.
	if !toolEnds[0].OK {
		t.Fatalf("successful tool reported not ok: %+v", toolEnds[0])
	}
	if toolEnds[1].OK {
		t.Fatalf("faulted tool reported ok: %+v", toolEnds[1])
	}
	if toolEnds[2].OK {
		t.Fatalf("erroring tool reported ok: %+v", toolEnds[2])
	}

	// Each completed assistant message is persisted as its own row.
	msgs, err := s.st.ListMessages(p.ID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var assistants []string
	for _, m := range msgs {
		if m.Role == "assistant" {
			assistants = append(assistants, m.Content)
		}
	}
	if len(assistants) != 2 || assistants[0] != "Hello" || assistants[1] != "second" {
		t.Fatalf("persisted assistant rows = %q", assistants)
	}

	// Input/Output sum every round; Context is the last round's prompt size.
	if turn.Usage == nil {
		t.Fatal("no usage reported")
	}
	if turn.Usage.Input != 250 || turn.Usage.Output != 30 || turn.Usage.Context != 160 {
		t.Fatalf("usage = %+v", turn.Usage)
	}
	if turn.Usage.Cost == nil || *turn.Usage.Cost != 0.5 {
		t.Fatalf("cost = %+v", turn.Usage.Cost)
	}
	if turn.Usage.Model != "test-model" || turn.Model != "test-model" {
		t.Fatalf("model = %q / %q", turn.Usage.Model, turn.Model)
	}
}

func TestConsumeHarnessTurnReadsTextAndUsageFromCommittedEntry(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	q := harness.NewEventQueue()
	runner := &harnessToolRunner{results: map[string]harness.ToolResult{}}
	// A fast round never emits a message_update carrying usage or text:
	// pi-durable throttles the live partial, so the committed entry is the only
	// source for both.
	q.Push([]harness.Event{
		{Type: "message_start"},
		{Type: "message_end", Entry: json.RawMessage(`{"id":1,"kind":"assistant","model":[{"role":"assistant",` +
			`"content":[{"type":"thinking","thinking":"pondering"},{"type":"text","text":"the answer"}],` +
			`"usage":{"input":8000,"output":50,"cacheRead":100,"totalTokens":8050,"cost":{"total":0.002}}}]}`)},
	})
	q.Push([]harness.Event{{Type: "run_end"}})

	var events []agent.ChatEvent
	turn, err := s.consumeHarnessTurn(context.Background(), nil, "conv-1", q, runner,
		agent.ChatParams{Project: p, SessionID: sessionID}, "test-model", func(ev agent.ChatEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}

	// The answer still reaches the client, as the missing tail of the stream.
	var streamed, thought string
	for _, ev := range events {
		switch ev.Type {
		case "delta":
			streamed += ev.Text
		case "reasoning":
			thought += ev.Text
		}
	}
	if streamed != "the answer" || thought != "pondering" {
		t.Fatalf("streamed text = %q / reasoning = %q", streamed, thought)
	}

	msgs, err := s.st.ListMessages(p.ID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "the answer" || msgs[0].Reasoning != "pondering" {
		t.Fatalf("persisted rows = %+v", msgs)
	}

	if turn.Usage == nil {
		t.Fatal("no usage reported")
	}
	if turn.Usage.Input != 8000 || turn.Usage.Output != 50 || turn.Usage.Context != 8050 {
		t.Fatalf("usage = %+v", turn.Usage)
	}
	if turn.Usage.Cost == nil || *turn.Usage.Cost != 0.002 {
		t.Fatalf("cost = %v", turn.Usage.Cost)
	}
}

// A round that streamed part of its text must not have the committed text
// appended twice.
func TestConsumeHarnessTurnDoesNotDuplicateStreamedText(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	q := harness.NewEventQueue()
	runner := &harnessToolRunner{results: map[string]harness.ToolResult{}}
	q.Push([]harness.Event{
		{Type: "message_update", Changes: []harness.Change{delta("text_delta", "Hel"), delta("text_delta", "lo")}},
		{Type: "message_end", Entry: json.RawMessage(`{"id":1,"kind":"assistant","model":[{"role":"assistant",` +
			`"content":[{"type":"text","text":"Hello there"}]}]}`)},
	})
	q.Push([]harness.Event{{Type: "run_end"}})

	var streamed string
	_, err := s.consumeHarnessTurn(context.Background(), nil, "conv-1", q, runner,
		agent.ChatParams{Project: p, SessionID: sessionID}, "test-model", func(ev agent.ChatEvent) {
			if ev.Type == "delta" {
				streamed += ev.Text
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if streamed != "Hello there" {
		t.Fatalf("streamed text = %q, want %q", streamed, "Hello there")
	}
	msgs, err := s.st.ListMessages(p.ID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "Hello there" {
		t.Fatalf("persisted rows = %+v", msgs)
	}
}

// The durable transcript must be rewound to match v1's store, which is the one
// retry, edit and delete trim. The count is of the user turns the sidecar must
// keep, so it excludes the turn being submitted: that turn is already in the
// store, either appended or trimmed back to.
func TestHarnessKeepUserTurns(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	add := func(role string) {
		t.Helper()
		if _, err := s.st.AddMessage(p.ID, sessionID, role, role+" text", "", "", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}

	// An empty store has no turn to exclude.
	if got := harnessKeepUserTurns(s.st, p.ID, sessionID); got != 0 {
		t.Fatalf("empty store = %d, want 0", got)
	}

	// A fresh turn: the store ends on the submitted user message, which the
	// sidecar must not count as one of its own.
	add("user")
	if got := harnessKeepUserTurns(s.st, p.ID, sessionID); got != 0 {
		t.Fatalf("first turn = %d, want 0", got)
	}

	// A completed turn, then a second question.
	add("assistant")
	add("user")
	if got := harnessKeepUserTurns(s.st, p.ID, sessionID); got != 1 {
		t.Fatalf("second turn = %d, want 1", got)
	}

	// A retry trims back to a user message, so the turns before it survive and
	// the re-run turn itself is excluded.
	if err := s.st.DeleteMessagesAfter(p.ID, sessionID, 3); err != nil {
		t.Fatal(err)
	}
	if got := harnessKeepUserTurns(s.st, p.ID, sessionID); got != 1 {
		t.Fatalf("retry of the second turn = %d, want 1", got)
	}

	// Retrying the only turn leaves nothing for the sidecar to keep, which is
	// the case that must rewind the durable transcript to empty.
	if err := s.st.DeleteMessagesAfter(p.ID, sessionID, 1); err != nil {
		t.Fatal(err)
	}
	if got := harnessKeepUserTurns(s.st, p.ID, sessionID); got != 0 {
		t.Fatalf("retry of the first turn = %d, want 0", got)
	}

	// An unreadable store must not force a rewind.
	if got := harnessKeepUserTurns(nil, p.ID, sessionID); got != -1 {
		t.Fatalf("nil store = %d, want -1", got)
	}
}

// A model call that fails commits an entry with stopReason "error" instead of
// emitting task_failed; the turn must still report the failure rather than
// ending in a silent done.
func TestConsumeHarnessTurnReportsCommittedModelError(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	q := harness.NewEventQueue()
	runner := &harnessToolRunner{results: map[string]harness.ToolResult{}}
	q.Push([]harness.Event{
		{Type: "message_start"},
		{Type: "message_end", Entry: json.RawMessage(`{"id":1,"kind":"assistant","model":[{"role":"assistant","content":[],` +
			`"stopReason":"error","errorMessage":"400: {\"type\":\"MissingSessionID\"}"}]}`)},
		{Type: "run_end"},
	})

	_, err := s.consumeHarnessTurn(context.Background(), nil, "conv-1", q, runner,
		agent.ChatParams{Project: p, SessionID: sessionID}, "test-model", func(agent.ChatEvent) {})
	if err == nil || !strings.Contains(err.Error(), "MissingSessionID") {
		t.Fatalf("err = %v, want the committed model failure", err)
	}
}

// A client-requested abort is not an error worth persisting, but it must still
// stop the turn rather than report success.
func TestConsumeHarnessTurnReportsAbortedGeneration(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	q := harness.NewEventQueue()
	runner := &harnessToolRunner{results: map[string]harness.ToolResult{}}
	q.Push([]harness.Event{
		{Type: "message_end", Entry: json.RawMessage(`{"id":1,"kind":"assistant","model":[{"role":"assistant","content":[{"type":"text","text":"half"}],"stopReason":"aborted"}]}`)},
		{Type: "run_end"},
	})

	_, err := s.consumeHarnessTurn(context.Background(), nil, "conv-1", q, runner,
		agent.ChatParams{Project: p, SessionID: sessionID}, "test-model", func(agent.ChatEvent) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// The partial answer is kept.
	msgs, lerr := s.st.ListMessages(p.ID, sessionID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(msgs) != 1 || msgs[0].Content != "half" {
		t.Fatalf("persisted rows = %+v", msgs)
	}
}

// The tool definitions the sidecar advertises must be exactly the ones the
// built-in loop would send: same names, same schemas, same order. This is the
// property that keeps the two harnesses from drifting apart, and it is easy to
// break by assembling tools in two places.
func TestHarnessToolDefsMatchTheBuiltInLoop(t *testing.T) {
	params := agent.ChatParams{
		Vision:        true,
		PlanMode:      false,
		ExtraTools:    []llm.Tool{mcpEchoTool()},
		DisabledTools: map[string]bool{"delete_file": true},
	}
	defs := harnessToolDefs(params)

	want := params.ToolSet()
	if len(defs) != len(want) {
		t.Fatalf("defs = %d tools, built-in loop = %d", len(defs), len(want))
	}
	for i, def := range defs {
		if def.Name != want[i].Function.Name {
			t.Fatalf("def[%d] = %q, want %q (order must match too)", i, def.Name, want[i].Function.Name)
		}
		if def.Description != want[i].Function.Description {
			t.Fatalf("def %q description differs from the built-in loop", def.Name)
		}
		got, err := json.Marshal(def.Parameters)
		if err != nil {
			t.Fatal(err)
		}
		expect, err := json.Marshal(want[i].Function.Parameters)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(expect) {
			t.Fatalf("def %q schema = %s, want %s", def.Name, got, expect)
		}
	}

	// The per-turn filters must survive the trip: a disabled tool is gone and
	// the vision and MCP tools are present.
	names := map[string]bool{}
	for _, def := range defs {
		names[def.Name] = true
	}
	if names["delete_file"] {
		t.Error("a tool the user disabled is still advertised")
	}
	if !names["screenshot_app"] {
		t.Error("screenshot_app missing for a vision model")
	}
	if !names["mcp_echo"] {
		t.Error("MCP tool missing")
	}
}

// Plan mode restricts the set to plan-safe tools on both harnesses.
func TestHarnessToolDefsRespectPlanMode(t *testing.T) {
	defs := harnessToolDefs(agent.ChatParams{PlanMode: true})
	names := map[string]bool{}
	for _, def := range defs {
		names[def.Name] = true
	}
	if names["write_file"] || names["run_command"] {
		t.Errorf("plan mode advertised a mutating tool: %v", names)
	}
	if !names["read_file"] || !names["make_plan"] {
		t.Errorf("plan mode is missing a read-only tool: %v", names)
	}
}

// The sidecar must receive the same system prompt the built-in loop would use,
// skills, memories, plan and project instructions included.
func TestHarnessInstructionsMatchTheBuiltInPrompt(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	p.Instructions = "Always answer in rhyme."
	params := agent.ChatParams{
		Project:        p,
		SessionID:      sessionID,
		Store:          s.st,
		Client:         &llm.Client{BaseURL: "https://example.test/v1", APIKey: "k", Model: "test-model"},
		SkillsPrompt:   "SKILLS-MARKER",
		MemoriesPrompt: "MEMORIES-MARKER",
		ToonEnabled:    true,
	}
	got := harnessEnsureRequest(params, "test-model").Instructions
	want := agent.BuildSystemPrompt(&params)
	if got != want {
		t.Fatalf("instructions differ from the built-in prompt:\n got %q\nwant %q", got, want)
	}
	for _, marker := range []string{"SKILLS-MARKER", "MEMORIES-MARKER", "Always answer in rhyme."} {
		if !strings.Contains(got, marker) {
			t.Errorf("instructions are missing %q", marker)
		}
	}
}

func mcpEchoTool() llm.Tool {
	return llm.Tool{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "mcp_echo",
			Description: "Echo a value back (from an MCP server).",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string"}},
			},
		},
	}
}

// A screenshot must reach the model. The built-in loop hands the PNG to the
// agent loop through Executor.PendingImage; the harness attaches it to the tool
// result instead, which is where a vision model expects it (pi-ai then splits
// it into a follow-up user message for APIs that reject images in tool
// results).
func TestHarnessToolRunnerCarriesScreenshotImage(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	exec := &agent.Executor{
		Screenshot: func(context.Context, string) ([]byte, error) { return png, nil },
	}
	r := &harnessToolRunner{exec: exec, results: map[string]harness.ToolResult{}}
	res, err := r.RunTool(context.Background(), harness.ToolCall{
		Tool:   "screenshot_app",
		Args:   json.RawMessage(`{"path":"/"}`),
		CallID: "call-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Images) != 1 {
		t.Fatalf("images = %+v, want exactly one", res.Images)
	}
	if res.Images[0].MimeType != "image/png" {
		t.Errorf("mimeType = %q, want image/png", res.Images[0].MimeType)
	}
	got, err := base64.StdEncoding.DecodeString(res.Images[0].Data)
	if err != nil {
		t.Fatalf("image data is not base64: %v", err)
	}
	if !bytes.Equal(got, png) {
		t.Errorf("image data = %q, want %q", got, png)
	}
	if len(exec.PendingImage) != 0 {
		t.Error("PendingImage was left set, so a later tool would resend the same screenshot")
	}
}

// Attachments must reach the model on the pi path too: a turn with no
// attachments stays a plain string, and one with them becomes content parts in
// the shape pi-durable hands the model.
// v1's stored summary must not reach the model on the pi path: the sidecar
// compacts its own durable transcript, so injecting v1's snapshot as well would
// hand the model the same history verbatim and summarised.
func TestHarnessPromptOmitsCompactionSnapshot(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	if err := s.st.SaveCompactionSnapshot(p.ID, sessionID, "SUMMARY-MARKER", 7); err != nil {
		t.Fatal(err)
	}

	params := agent.ChatParams{Project: p, SessionID: sessionID, Store: s.st}
	if got := agent.BuildSystemPrompt(&params); !strings.Contains(got, "SUMMARY-MARKER") {
		t.Fatal("the built-in prompt must still carry the snapshot")
	}
	params.SkipCompactionSnapshot = true
	if got := agent.BuildSystemPrompt(&params); strings.Contains(got, "SUMMARY-MARKER") {
		t.Fatal("the pi path must not inject v1's snapshot")
	}
}

// A round that called tools must persist the calls on the assistant row and
// the results as "tool" rows, exactly as the built-in loop does: v1's store is
// what the UI reloads from, and it renders tool cards from these rows. Without
// them a reloaded pi session showed prose and no tool history.
func TestHarnessPersistsToolCallsAndResults(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)

	entry := json.RawMessage(`{"id":1,"kind":"assistant","model":[{"role":"assistant","content":[` +
		`{"type":"toolCall","id":"call_a","name":"write_file","arguments":{"path":"a.txt","content":"hi"}}],` +
		`"stopReason":"toolUse"}]}`)
	toolJSON := entryToolJSON(entry)
	if toolJSON == "" {
		t.Fatal("no tool_json for an assistant entry with a tool call")
	}
	var calls struct {
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(toolJSON), &calls); err != nil {
		t.Fatalf("tool_json is not the UI's shape: %v (%s)", err, toolJSON)
	}
	if len(calls.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v, want one", calls.ToolCalls)
	}
	if calls.ToolCalls[0].ID != "call_a" || calls.ToolCalls[0].Function.Name != "write_file" {
		t.Fatalf("tool_call = %+v", calls.ToolCalls[0])
	}
	// The UI reads arguments as the raw JSON string the model produced.
	if !strings.Contains(calls.ToolCalls[0].Function.Arguments, `"a.txt"`) {
		t.Fatalf("arguments = %q", calls.ToolCalls[0].Function.Arguments)
	}

	// A plain text round carries no tool calls.
	if got := entryToolJSON(json.RawMessage(`{"id":2,"kind":"assistant","model":[{"role":"assistant","content":[{"type":"text","text":"hi"}],"stopReason":"stop"}]}`)); got != "" {
		t.Fatalf("tool_json = %q, want empty for a text round", got)
	}

	runner := &harnessToolRunner{
		exec:      &agent.Executor{Root: p.Path, ProjectID: p.ID, SessionID: sessionID, Store: s.st},
		store:     s.st,
		projectID: p.ID,
		sessionID: sessionID,
		results:   map[string]harness.ToolResult{},
	}
	args, _ := json.Marshal(map[string]string{"path": "a.txt", "content": "hi"})
	if _, err := runner.RunTool(context.Background(), harness.ToolCall{
		ConversationID: harnessConversationID(p.ID, sessionID),
		Tool:           "write_file",
		Args:           args,
		CallID:         "call_a",
	}); err != nil {
		t.Fatalf("RunTool: %v", err)
	}

	msgs, err := s.st.ListMessages(p.ID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Role != "tool" {
		t.Fatalf("rows = %+v, want one tool row", msgs)
	}
	if !strings.Contains(msgs[0].ToolJSON, `"tool_call_id":"call_a"`) || !strings.Contains(msgs[0].ToolJSON, `"name":"write_file"`) {
		t.Fatalf("tool row tag = %s, want the call id and tool name", msgs[0].ToolJSON)
	}
}

// pi-ai silently drops image parts for a model whose input list omits "image",
// so the pi path must never narrow that list from v1's catalog: the openrouter
// entry for google/gemini-2.5-flash carries no imageInput while the built-in
// loop sends images for it regardless, which is how a red image reached the
// model as "Green" on the pi path and "Red" on the Go path.
func TestHarnessProviderSpecAdvertisesImageInput(t *testing.T) {
	client := &llm.Client{BaseURL: "https://example.test/v1", APIKey: "k", Model: "test-model"}
	spec := harnessProviderSpec(client, "test-model")
	if len(spec.Models) == 0 {
		t.Fatal("no models in the spec")
	}
	for _, m := range spec.Models {
		found := false
		for _, in := range m.Input {
			if in == "image" {
				found = true
			}
		}
		if !found {
			t.Errorf("model %s advertises input %v, want image included", m.ID, m.Input)
		}
	}
}

func TestHarnessUserContent(t *testing.T) {
	if got := harnessUserContent("hello", nil); got != "hello" {
		t.Fatalf("no attachments: got %#v, want the plain string", got)
	}

	parts, ok := harnessUserContent("look at this", []agent.Attachment{
		{Name: "shot.png", MIME: "image/png", Kind: "image", Content: "aGk="},
		{Name: "notes.txt", MIME: "text/plain", Kind: "text", Content: "hi"},
	}).([]harness.InputPart)
	if !ok {
		t.Fatalf("with attachments: got %T, want []harness.InputPart", parts)
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %+v, want text + image + text", parts)
	}
	if parts[0].Type != "text" || parts[0].Text != "look at this" {
		t.Fatalf("parts[0] = %+v", parts[0])
	}
	if parts[1].Type != "image" || parts[1].Data != "aGk=" || parts[1].MimeType != "image/png" {
		t.Fatalf("parts[1] = %+v, want an image part", parts[1])
	}
	if parts[2].Type != "text" || !strings.Contains(parts[2].Text, "Attached file: notes.txt") {
		t.Fatalf("parts[2] = %+v, want the inlined text file", parts[2])
	}
}

func TestConsumeHarnessTurnReportsFaultedGeneration(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	q := harness.NewEventQueue()
	runner := &harnessToolRunner{results: map[string]harness.ToolResult{}}
	q.Push([]harness.Event{
		{Type: "message_update", Changes: []harness.Change{delta("text_delta", "partial")}},
		{Type: "task_failed", Message: json.RawMessage(`"provider error: 429"`)},
	})

	_, err := s.consumeHarnessTurn(context.Background(), nil, "conv-1", q, runner,
		agent.ChatParams{Project: p, SessionID: sessionID}, "test-model", func(agent.ChatEvent) {})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want the provider failure", err)
	}
	// The partial answer is kept rather than lost with the failed turn.
	msgs, err := s.st.ListMessages(p.ID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "partial" {
		t.Fatalf("persisted rows = %+v", msgs)
	}
}

// The provider's static headers must reach the sidecar too: it is the side that
// calls the endpoint, and opencode's client attribution travels with the
// routing header rather than instead of it.
func TestHarnessProviderSpecSendsStaticHeaders(t *testing.T) {
	opencode := harnessProviderSpec(llm.NewClient("https://opencode.ai/zen/go/v1", "k", "m"), "m")
	if got := opencode.Headers["x-opencode-client"]; got != "v1" {
		t.Fatalf("headers = %v, want x-opencode-client: v1", opencode.Headers)
	}
	if opencode.SessionHeader != "x-opencode-session" {
		t.Fatalf("sessionHeader = %q", opencode.SessionHeader)
	}

	openai := harnessProviderSpec(llm.NewClient("https://api.openai.com/v1", "k", "gpt-x"), "gpt-x")
	if len(openai.Headers) != 0 {
		t.Fatalf("an endpoint needing no static headers got %v", openai.Headers)
	}
}

// The provider's routing header must reach the sidecar too: it is the side that
// actually calls the endpoint, and without this the pi path (the default) sends
// no header at all and opencode's zen endpoint refuses the turn.
func TestHarnessProviderSpecSendsSessionHeader(t *testing.T) {
	opencode := harnessProviderSpec(llm.NewClient("https://opencode.ai/zen/v1", "k", "deepseek-v4-flash"), "deepseek-v4-flash")
	if opencode.SessionHeader != "x-opencode-session" {
		t.Fatalf("sessionHeader = %q, want x-opencode-session", opencode.SessionHeader)
	}

	openai := harnessProviderSpec(llm.NewClient("https://api.openai.com/v1", "k", "gpt-x"), "gpt-x")
	if openai.SessionHeader != "" {
		t.Fatalf("an endpoint needing no routing header got %q", openai.SessionHeader)
	}
}

// The model the turn runs on must be in the catalog even when it is neither
// the client's configured model nor a model in v1's static catalog: a user
// picking a newer model from the provider's live list (deepseek-v4.1-flash
// against a catalog holding only deepseek-v4-flash) used to fail the turn with
// "model ... is not in the supplied catalog" before the model was called.
func TestHarnessProviderSpecRegistersTurnModel(t *testing.T) {
	client := llm.NewClient("https://api.example.com/v1/", "sk-secret", "configured-model")
	spec := harnessProviderSpec(client, "deepseek-v4.1-flash")
	if spec.ID != "api.example.com" {
		t.Fatalf("provider id = %q", spec.ID)
	}
	if spec.BaseURL != "https://api.example.com/v1" || spec.APIKey != "sk-secret" {
		t.Fatalf("spec = %+v", spec)
	}
	if len(spec.Models) == 0 || spec.Models[0].ID != "deepseek-v4.1-flash" {
		t.Fatalf("the turn's own model must be registered first: %+v", spec.Models)
	}
	seen := map[string]int{}
	for _, m := range spec.Models {
		seen[m.ID]++
		if len(m.Input) == 0 {
			t.Fatalf("model %q advertises no input", m.ID)
		}
	}
	for _, id := range []string{"deepseek-v4.1-flash", "configured-model"} {
		if seen[id] != 1 {
			t.Fatalf("model %q registered %d times", id, seen[id])
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("model %q registered %d times", id, n)
		}
	}
}

// A turn with no explicit model falls back to the client's, which must not be
// registered twice.
func TestHarnessProviderSpecHandlesEmptyTurnModel(t *testing.T) {
	spec := harnessProviderSpec(llm.NewClient("https://api.example.com/v1/", "sk", "configured-model"), "")
	seen := map[string]int{}
	for _, m := range spec.Models {
		seen[m.ID]++
	}
	if seen["configured-model"] != 1 {
		t.Fatalf("configured model registered %d times: %+v", seen["configured-model"], spec.Models)
	}
	if seen[""] != 0 {
		t.Fatal("an empty model id must not be registered")
	}
}

func TestHarnessProviderIDFallsBack(t *testing.T) {
	if got := harnessProviderID("not a url"); got != "v1" {
		t.Fatalf("provider id = %q, want v1", got)
	}
}

func TestHarnessToolDefsMirrorTheBuiltinSet(t *testing.T) {
	defs := harnessToolDefs(agent.ChatParams{})
	byName := make(map[string]harness.ToolDef, len(defs))
	for _, d := range defs {
		byName[d.Name] = d
	}
	// The sidecar advertises what the built-in loop sends, not a subset of it.
	for _, name := range []string{
		"read_file", "write_file", "edit_file", "list_files", "search_files",
		"delete_file", "move_file", "fetch_url", "run_command", "run_command_background",
		"restart_preview", "set_project_name", "set_session_name", "set_todos",
		"remember", "forget", "search_memories", "ask_user", "git", "run_container", "verify_project",
		"make_plan", "update_plan",
	} {
		d, ok := byName[name]
		if !ok {
			t.Fatalf("tool %q missing from the sidecar's definitions", name)
		}
		if d.Description == "" || d.Parameters["type"] != "object" {
			t.Fatalf("tool %q has an incomplete definition: %+v", name, d)
		}
	}
}

func TestHarnessToolDefsRespectDisabledAndPlanMode(t *testing.T) {
	disabled := harnessToolDefs(agent.ChatParams{DisabledTools: map[string]bool{"run_command": true}})
	for _, d := range disabled {
		if d.Name == "run_command" {
			t.Fatal("a disabled tool must not be advertised")
		}
	}

	// Plan mode is read-only: no writes, no commands, no MCP tools.
	plan := map[string]bool{}
	for _, d := range harnessToolDefs(agent.ChatParams{PlanMode: true}) {
		plan[d.Name] = true
	}
	for _, name := range []string{"write_file", "edit_file", "run_command", "git"} {
		if plan[name] {
			t.Fatalf("plan mode advertised %q", name)
		}
	}
	for _, name := range []string{"read_file", "list_files", "search_files"} {
		if !plan[name] {
			t.Fatalf("plan mode dropped %q", name)
		}
	}
}

func TestHarnessToolDefsIncludeVisionAndMCP(t *testing.T) {
	defs := harnessToolDefs(agent.ChatParams{
		Vision: true,
		ExtraTools: []llm.Tool{{Type: "function", Function: llm.ToolFunction{
			Name: "mcp_search", Description: "search", Parameters: map[string]any{"type": "object"},
		}}},
	})
	seen := map[string]bool{}
	for _, d := range defs {
		seen[d.Name] = true
	}
	if !seen["screenshot_app"] {
		t.Fatal("a vision model must be offered screenshot_app")
	}
	if !seen["mcp_search"] {
		t.Fatal("dynamic MCP tools must reach the sidecar")
	}
}

func TestHarnessToolDetail(t *testing.T) {
	cases := map[string]string{
		`{"path":"src/a.ts"}`:         "src/a.ts",
		`{"file_path":"/abs/b.md"}`:   "/abs/b.md",
		`{"command":"go test ./..."}`: "go test ./...",
		`{}`:                          "",
		``:                            "",
	}
	for args, want := range cases {
		if got := harnessToolDetail([]byte(args)); got != want {
			t.Fatalf("harnessToolDetail(%s) = %q, want %q", args, got, want)
		}
	}
}

// A turn that only ever produced reasoning (a reasoning model that spent its
// output window thinking) must not end in a silent done: the harness path used
// to accept it, so the user saw a thinking block and then nothing.
func TestHarnessReasoningOnlyTurnErrors(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	q := harness.NewEventQueue()
	runner := &harnessToolRunner{results: map[string]harness.ToolResult{}}
	q.Push([]harness.Event{
		{Type: "message_update", Changes: []harness.Change{delta("thinking_delta", "thinking hard")}},
		{Type: "message_end", Entry: json.RawMessage(`{"id":1,"kind":"assistant"}`)},
	})
	q.Push([]harness.Event{{Type: "run_end"}})

	_, err := s.consumeHarnessTurn(context.Background(), nil, "conv-1", q, runner,
		agent.ChatParams{Project: p, SessionID: sessionID}, "test-model", func(agent.ChatEvent) {})
	if err == nil {
		t.Fatal("a reasoning-only turn must return an error, not a silent done")
	}
	if !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("error = %v, want a no-answer message", err)
	}
}
