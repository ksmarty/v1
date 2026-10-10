package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"v1/internal/agent"
	"v1/internal/harness"
	"v1/internal/llm"
)

// rawControlBytes is a fixture shaped like the text that breaks a provider:
// visible content a user needs to keep, wrapped in the ANSI escapes a command's
// output carries and a BEL byte a terminal capture can leave behind.
const rawControlBytes = "before\x1b[31mred\x1b[0mafter\x07"

func hasControlBytes(s string) bool {
	return strings.ContainsRune(s, 0x1b) || strings.ContainsRune(s, 0x07)
}

// The harness path has no LLM API boundary of its own, so the text v1 hands the
// sidecar has to be scrubbed where it is produced — otherwise an endpoint that
// enforces a character pattern rejects the whole request with "The string did
// not match the expected pattern".
func TestHarnessUserContentScrubsControlBytes(t *testing.T) {
	// A turn without attachments stays a plain string.
	plain, ok := harnessUserContent(rawControlBytes, nil).(string)
	if !ok {
		t.Fatalf("a turn with no attachments must stay a plain string, got %T", harnessUserContent(rawControlBytes, nil))
	}
	if hasControlBytes(plain) {
		t.Fatalf("user text still carries control bytes: %q", plain)
	}
	if !strings.Contains(plain, "before") || !strings.Contains(plain, "after") {
		t.Fatalf("the text lost its visible content: %q", plain)
	}

	// With attachments it becomes content parts, and the inlined file is
	// scrubbed too — including the file name it is announced with.
	atts := []agent.Attachment{
		{Kind: "text", Name: "log\x1b[0m.txt", Content: rawControlBytes},
		{Kind: "image", Name: "shot.png", MIME: "image/png", Content: "iVBORw0KGgo="},
	}
	parts, ok := harnessUserContent(rawControlBytes, atts).([]harness.InputPart)
	if !ok {
		t.Fatalf("a turn with attachments must become parts, got %T", harnessUserContent(rawControlBytes, atts))
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %+v, want the user text, the inlined file and the image", parts)
	}
	if hasControlBytes(parts[1].Text) {
		t.Fatalf("the inlined attachment still carries control bytes: %q", parts[1].Text)
	}
	if !strings.Contains(parts[1].Text, "before") {
		t.Fatalf("the inlined attachment lost its content: %q", parts[1].Text)
	}
	// Image payloads are base64: scrubbing them could only corrupt the data.
	if parts[2].Data != "iVBORw0KGgo=" || parts[2].MimeType != "image/png" {
		t.Fatalf("image part = %+v, want the payload untouched", parts[2])
	}
}

// The system prompt is built from stored project state, so it can carry the
// same bytes. The compaction snapshot is the realistic carrier: it is inlined
// into the prompt verbatim.
func TestHarnessEnsureRequestScrubsInstructions(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	if err := s.st.SaveCompactionSnapshot(p.ID, sessionID, rawControlBytes, 7); err != nil {
		t.Fatal(err)
	}
	params := agent.ChatParams{
		Project:   p,
		SessionID: sessionID,
		Store:     s.st,
		Client:    &llm.Client{BaseURL: "https://opencode.ai/zen/go/v1", APIKey: "k", Model: "m"},
	}
	// Guard the fixture: if the prompt did not carry the bytes, this test would
	// pass without proving anything.
	if !hasControlBytes(agent.BuildSystemPrompt(&params)) {
		t.Fatal("fixture: the built prompt must carry the control bytes")
	}

	req := harnessEnsureRequest(params, "m", "all")
	if hasControlBytes(req.Instructions) {
		t.Fatalf("instructions still carry control bytes: %q", req.Instructions)
	}
	if !strings.Contains(req.Instructions, "before") || !strings.Contains(req.Instructions, "after") {
		t.Fatalf("instructions lost their visible content")
	}
}

// A tool result goes straight back to the sidecar as the tool call's answer, so
// it is the path most likely to carry raw terminal bytes into a request.
func TestHarnessToolResultIsScrubbed(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	if err := os.WriteFile(filepath.Join(p.Path, "raw.log"), []byte(rawControlBytes+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &harnessToolRunner{
		exec:    &agent.Executor{Root: p.Path, ProjectID: p.ID, SessionID: sessionID, Store: s.st},
		results: map[string]harness.ToolResult{},
	}
	args, _ := json.Marshal(map[string]string{"path": "raw.log"})
	res, err := runner.RunTool(context.Background(), harness.ToolCall{Tool: "read_file", Args: args, CallID: "call_raw"})
	if err != nil {
		t.Fatalf("RunTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("read_file failed: %+v", res)
	}
	if !strings.Contains(res.Text, "before") || !strings.Contains(res.Text, "after") {
		t.Fatalf("the tool result lost its content: %q", res.Text)
	}
	if hasControlBytes(res.Text) {
		t.Fatalf("tool result still carries control bytes: %q", res.Text)
	}

	// The row the UI reloads from and a retry rebuilds the request from must
	// match what the model was shown. RunTool no longer writes it — the
	// tool_execution_end event does — so persist it the way the handler does.
	if err := s.recordToolResult(p.ID, sessionID, "call_raw", "read_file", res.Text); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.st.ListMessages(p.ID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("rows = %+v, want one tool row", msgs)
	}
	if hasControlBytes(msgs[0].Content) {
		t.Fatalf("persisted tool row still carries control bytes: %q", msgs[0].Content)
	}
}
