package server

import (
	"encoding/json"
	"testing"

	"v1/internal/store"
)

// A tool the sidecar runs itself — an extension's, or anything else Go did not
// execute — leaves the runner empty, so its result has to be read off the entry
// pi-durable committed. Without that the call renders with nothing behind it,
// which is exactly what an extension's tool calls looked like.
func TestEntryToolResult(t *testing.T) {
	entry := json.RawMessage(`{"model":[
		{"role":"assistant","content":[{"type":"text","text":"calling"}]},
		{"role":"toolResult","toolCallId":"call_1","toolName":"word_count","isError":false,
		 "content":[{"type":"text","text":"42 words"}]}
	]}`)
	text, isError := entryToolResult(entry)
	if text != "42 words" {
		t.Fatalf("text = %q, want %q", text, "42 words")
	}
	if isError {
		t.Fatal("isError = true, want false")
	}
}

func TestEntryToolResultJoinsBlocksAndFlagsErrors(t *testing.T) {
	entry := json.RawMessage(`{"model":[
		{"role":"toolResult","isError":true,"content":[
			{"type":"text","text":"boom"},
			{"type":"text","text":" and bust"}
		]}
	]}`)
	text, isError := entryToolResult(entry)
	if text != "boom and bust" {
		t.Fatalf("text = %q, want %q", text, "boom and bust")
	}
	if !isError {
		t.Fatal("isError = false, want true")
	}
}

func TestEntryToolResultIgnoresOtherRoles(t *testing.T) {
	entry := json.RawMessage(`{"model":[{"role":"assistant","content":[{"type":"text","text":"hi"}]}]}`)
	if text, isError := entryToolResult(entry); text != "" || isError {
		t.Fatalf("text = %q, isError = %v; want empty and false", text, isError)
	}
	if text, isError := entryToolResult(nil); text != "" || isError {
		t.Fatalf("nil entry: text = %q, isError = %v", text, isError)
	}
	if text, isError := entryToolResult(json.RawMessage("not json")); text != "" || isError {
		t.Fatalf("bad json: text = %q, isError = %v", text, isError)
	}
}

// recordToolResult has to write the same shape the built-in loop writes, because
// the UI pairs a call with its result through the row's tool_call_id.
func TestRecordToolResultPairsWithItsCall(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	s := &Server{st: st}

	projectID, sessionID := store.NewID(), store.NewID()
	if err := s.recordToolResult(projectID, sessionID, "call_1", "word_count", "42 words"); err != nil {
		t.Fatalf("recordToolResult: %v", err)
	}

	msgs, err := st.ListMessages(projectID, sessionID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d rows, want 1", len(msgs))
	}
	got := msgs[0]
	if got.Role != "tool" {
		t.Fatalf("role = %q, want tool", got.Role)
	}
	if got.Content != "42 words" {
		t.Fatalf("content = %q", got.Content)
	}
	var meta struct {
		ToolCallID string `json:"tool_call_id"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal([]byte(got.ToolJSON), &meta); err != nil {
		t.Fatalf("tool_json = %q: %v", got.ToolJSON, err)
	}
	if meta.ToolCallID != "call_1" || meta.Name != "word_count" {
		t.Fatalf("tool_json = %q, want call_1/word_count", got.ToolJSON)
	}
}
