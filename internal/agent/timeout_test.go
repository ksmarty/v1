package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"v1/internal/llm"
	"v1/internal/store"
)

func newRunChatStore(t *testing.T) (*store.Store, *store.Project) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	projDir := filepath.Join(dir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &store.Project{ID: store.NewID(), Name: "t", Path: projDir}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	return s, p
}

// TestHardTimeoutAbortsTurn verifies that a turn that outlives its hard
// timeout is aborted with a friendly error instead of hanging forever.
func TestHardTimeoutAbortsTurn(t *testing.T) {
	s, p := newRunChatStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // far beyond the turn's hard timeout
	}))
	defer server.Close()

	_, err := RunChat(context.Background(), ChatParams{
		Store:       s,
		Project:     p,
		Client:      llm.NewClient(server.URL, "", "test-model"),
		Exec:        &Executor{},
		Message:     "hi",
		SessionID:   "s1",
		HardTimeout: 300 * time.Millisecond,
		Emit:        func(ChatEvent) {},
	})
	if err == nil {
		t.Fatal("expected the turn to be aborted by the hard timeout")
	}
	if !strings.Contains(err.Error(), "hard timeout") {
		t.Fatalf("error = %q, want a hard-timeout message", err.Error())
	}
}

// TestSoftTimeoutWarnsBetweenRounds verifies that once a turn runs past its
// soft timeout, a "you have been working" system message is injected before
// the next round.
func TestSoftTimeoutWarnsBetweenRounds(t *testing.T) {
	s, p := newRunChatStore(t)
	round := 0
	var secondRequest []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round++
		w.Header().Set("Content-Type", "text/event-stream")
		switch round {
		case 1:
			// Delay past the soft timeout, then request a tool call so the
			// turn enters a second round.
			time.Sleep(250 * time.Millisecond)
			_, _ = w.Write([]byte(
				sseLine(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"run_command","arguments":"{\"command\":\"true\"}"}}]}}]}`) +
					sseLine(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`) +
					"data: [DONE]\n\n",
			))
		case 2:
			var body struct {
				Messages []llm.Message `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			secondRequest, _ = json.Marshal(body)
			_, _ = w.Write([]byte(
				sseLine(`{"choices":[{"delta":{"content":"done"}}]}`) +
					sseLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
					"data: [DONE]\n\n",
			))
		}
	}))
	defer server.Close()

	_, err := RunChat(context.Background(), ChatParams{
		Store:       s,
		Project:     p,
		Client:      llm.NewClient(server.URL, "", "test-model"),
		Exec:        &Executor{},
		Message:     "hi",
		SessionID:   "s1",
		SoftTimeout: 150 * time.Millisecond,
		HardTimeout: 10 * time.Second,
		Emit:        func(ChatEvent) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondRequest == nil {
		t.Fatal("second round never happened")
	}
	var body struct {
		Messages []llm.Message `json:"messages"`
	}
	if err := json.Unmarshal(secondRequest, &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range body.Messages {
		if m.Role == "system" && strings.Contains(contentString(m.Content), "soft time limit") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("soft-timeout warning was not injected into the second round")
	}
}
