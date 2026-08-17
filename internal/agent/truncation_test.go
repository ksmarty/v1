package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"v1/internal/llm"
	"v1/internal/store"
)

func sseLine(payload string) string { return "data: " + payload + "\n\n" }

// TestRunChatContinuesTruncatedOutput verifies that a stream truncated by the
// provider's output window (finish_reason "length") is continued on the next
// round and persisted as a single folded reply, not silently dropped.
func TestRunChatContinuesTruncatedOutput(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	projDir := filepath.Join(dir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &store.Project{ID: store.NewID(), Name: "t", Path: projDir}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}

	attempt := 0
	var infos []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = w.Write([]byte(
				sseLine(`{"choices":[{"delta":{"content":"Part one. "}}]}`) +
					sseLine(`{"choices":[{"delta":{},"finish_reason":"length"}]}`) +
					"data: [DONE]\n\n",
			))
			return
		}
		_, _ = w.Write([]byte(
			sseLine(`{"choices":[{"delta":{"content":"Part two."}}]}`) +
				sseLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	_, err = RunChat(context.Background(), ChatParams{
		Store:     s,
		Project:   p,
		Client:    llm.NewClient(server.URL, "", "test-model"),
		Exec:      &Executor{},
		Message:   "hi",
		SessionID: "s1",
		Emit: func(ev ChatEvent) {
			if ev.Type == "info" {
				infos = append(infos, ev.Text)
			}
		},
	})
	if err != nil {
		t.Fatalf("RunChat() error = %v", err)
	}
	if attempt != 2 {
		t.Errorf("provider calls = %d, want 2 (one truncated, one continuation)", attempt)
	}
	if len(infos) != 1 {
		t.Errorf("info events = %d, want 1", len(infos))
	}

	msgs, err := s.ListMessages(p.ID, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[1].Role != "assistant" {
		t.Fatalf("messages = %d, want 2 with an assistant reply (got %d)", len(msgs), len(msgs))
	}
	got := msgs[1].Content
	if got != "Part one. \n\nPart two." {
		t.Errorf("assistant content = %q, want %q", got, "Part one. \n\nPart two.")
	}
}

// TestRunChatEmptyResponseErrors verifies that a provider stream that ends
// without producing anything (a gateway error body, a quota wall) fails the
// turn loudly instead of "completing" it with an empty reply.
func TestRunChatEmptyResponseErrors(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	projDir := filepath.Join(dir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &store.Project{ID: store.NewID(), Name: "t", Path: projDir}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	_, err = RunChat(context.Background(), ChatParams{
		Store:     s,
		Project:   p,
		Client:    llm.NewClient(server.URL, "", "test-model"),
		Exec:      &Executor{},
		Message:   "hi",
		SessionID: "s1",
		Emit:      func(ChatEvent) {},
	})
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("RunChat() error = %v, want an empty-response error", err)
	}
}

// TestRunChatSurvivesManyToolRounds verifies a turn is not cut off at the
// basic round cap while the model is still working: 20 tool rounds with
// interleaved output-window truncations (which previously burned the same
// budget) must all execute and end with the model's final text reply.
func TestRunChatSurvivesManyToolRounds(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	projDir := filepath.Join(dir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &store.Project{ID: store.NewID(), Name: "t", Path: projDir}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}

	toolResults := 0 // tool-role messages seen in the most recent request
	truncatedOnce := false
	infos := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		toolResults = 0
		for _, m := range body.Messages {
			if m.Role == "tool" {
				toolResults++
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if toolResults >= 20 {
			_, _ = w.Write([]byte(
				sseLine(`{"choices":[{"delta":{"content":"All done."},"finish_reason":"stop"}]}`) +
					"data: [DONE]\n\n",
			))
			return
		}
		if toolResults == 7 && !truncatedOnce {
			// Output window fills mid-reply — no tool call to continue from.
			truncatedOnce = true
			_, _ = w.Write([]byte(
				sseLine(`{"choices":[{"delta":{"content":"Writing… "}}]}`) +
					sseLine(`{"choices":[{"delta":{},"finish_reason":"length"}]}`) +
					"data: [DONE]\n\n",
			))
			return
		}
		id := "call_" + strconv.Itoa(toolResults)
		_, _ = w.Write([]byte(
			sseLine(`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"`+id+`","type":"function","function":{"name":"probe_tool","arguments":""}}]}}]}`) +
				sseLine(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"n\":1}"}}]}}]}`) +
				sseLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	_, err = RunChat(context.Background(), ChatParams{
		Store:     s,
		Project:   p,
		Client:    llm.NewClient(server.URL, "", "test-model"),
		Exec:      &Executor{},
		Message:   "do the work",
		SessionID: "s1",
		Emit: func(ev ChatEvent) {
			if ev.Type == "info" {
				infos++
			}
		},
	})
	if err != nil {
		t.Fatalf("RunChat() error = %v", err)
	}
	if toolResults != 20 {
		t.Errorf("tool rounds executed = %d, want 20 (turn was cut off early)", toolResults)
	}
	if infos == 0 {
		t.Errorf("no truncation info events — the length responses weren't exercised")
	}
	msgs, err := s.ListMessages(p.ID, "s1")
	if err != nil {
		t.Fatal(err)
	}
	var final string
	for _, m := range msgs {
		if m.Role == "assistant" && m.Content == "All done." {
			final = m.Content
		}
	}
	if final == "" {
		t.Fatalf("final assistant reply missing — turn ended before the model finished")
	}
}
