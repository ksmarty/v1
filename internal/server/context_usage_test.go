package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"v1/internal/agent"
	"v1/internal/harness"
	"v1/internal/store"
)

// contextTokens must count a round's window fill the way pi does (pi-ai's
// calculateContextTokens). `input` is only the uncached prompt, so summing
// input+output omits the prompt the provider served from its cache — which on a
// long conversation is most of what the model read.
func TestContextTokensCountsTheCachedPrompt(t *testing.T) {
	cases := []struct {
		name string
		u    *harness.Usage
		want int64
	}{
		{"nil", nil, 0},
		{"uncached", &harness.Usage{Input: 100, Output: 20}, 120},
		{"cached", &harness.Usage{Input: 100, Output: 20, CacheRead: 7000, CacheWrite: 300}, 7420},
		{"provider total wins", &harness.Usage{Input: 100, Output: 20, TotalTokens: 9999}, 9999},
	}
	for _, tc := range cases {
		if got := contextTokens(tc.u); got != tc.want {
			t.Errorf("%s: contextTokens = %d, want %d", tc.name, got, tc.want)
		}
	}
	if got := harnessUsageJSON(nil, "m"); got != "" {
		t.Errorf("a round with no usage should store nothing, got %q", got)
	}
}

// The context meter and the client's token line both read the newest assistant
// row's usage, so the durable path has to record it the way the built-in loop
// does. It did not: every row was written with an empty usage, so the meter fell
// back to counting the whole stored transcript.
func TestHarnessTurnPersistsRoundUsage(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	q := harness.NewEventQueue()
	runner := &harnessToolRunner{results: map[string]harness.ToolResult{}}

	q.Push([]harness.Event{
		{Type: "message_update", Usage: &harness.Usage{Input: 100, Output: 20, CacheRead: 7000, CacheWrite: 300},
			Changes: []harness.Change{delta("text_delta", "answer")}},
		{Type: "message_end", Entry: json.RawMessage(`{"id":1,"kind":"assistant"}`)},
	})
	q.Push([]harness.Event{{Type: "run_end"}})

	if _, err := s.consumeHarnessTurn(context.Background(), nil, "conv-1", q, runner,
		agent.ChatParams{Project: p, SessionID: sessionID}, "test-model", func(agent.ChatEvent) {}); err != nil {
		t.Fatalf("consumeHarnessTurn: %v", err)
	}

	msgs, err := s.st.ListMessages(p.ID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var row *store.Message
	for i := range msgs {
		if msgs[i].Role == "assistant" {
			row = msgs[i]
		}
	}
	if row == nil {
		t.Fatal("no assistant row was persisted")
	}
	if row.Usage == "" {
		t.Fatal("the assistant row carries no usage, so the context meter cannot see the real fill")
	}
	var u struct {
		Input   int64  `json:"input"`
		Output  int64  `json:"output"`
		Cached  int64  `json:"cached"`
		Context int64  `json:"context"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal([]byte(row.Usage), &u); err != nil {
		t.Fatalf("usage %q is not JSON: %v", row.Usage, err)
	}
	if u.Context != 7420 {
		t.Fatalf("context = %d, want 7420 (input+output+cacheRead+cacheWrite, as pi counts it)", u.Context)
	}
	if u.Cached != 7000 || u.Model != "test-model" {
		t.Fatalf("usage = %+v, want the cached share and the model", u)
	}
}

// contextUsageFor drives the endpoint the ring polls, through the auth
// middleware — which is what attaches the request's user, as it does in the app.
func contextUsageFor(t *testing.T, s *Server, projectID string) (used, budget int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/context", nil)
	req.SetPathValue("id", projectID)
	rec := httptest.NewRecorder()
	s.auth.Middleware(http.HandlerFunc(s.handleContextUsage)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("context usage: HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Used   int `json:"used"`
		Budget int `json:"budget"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response %s: %v", rec.Body.String(), err)
	}
	return got.Used, got.Budget
}

// The meter reports what the provider last saw, not the size of everything v1
// ever stored. The durable transcript folds older entries into a summary inside
// pi-durable while v1 keeps every row it wrote, so the byte estimate over the
// stored rows grows without bound while the real context does not.
func TestContextUsagePrefersTheProviderReport(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	// 40K of characters the durable transcript has already folded away.
	if _, err := s.st.AddMessage(p.ID, sessionID, "user", strings.Repeat("x", 40000), "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.AddMessage(p.ID, sessionID, "assistant", "answer", "", "m", "", `{"context":900}`, ""); err != nil {
		t.Fatal(err)
	}

	used, budget := contextUsageFor(t, s, p.ID)
	if used != 900 {
		t.Fatalf("used = %d, want the recorded context 900 (the stored 40K is folded away); budget = %d", used, budget)
	}
}

// Whatever was appended after the last response is estimated on top of it, the
// same shape pi's estimateContext uses.
func TestContextUsageEstimatesTheTail(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	if _, err := s.st.AddMessage(p.ID, sessionID, "assistant", "answer", "", "m", "", `{"context":900}`, ""); err != nil {
		t.Fatal(err)
	}
	// 400 characters ≈ 100 tokens by v1's bytes/4 estimate.
	if _, err := s.st.AddMessage(p.ID, sessionID, "user", strings.Repeat("y", 400), "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}

	used, _ := contextUsageFor(t, s, p.ID)
	if used < 900 || used > 1100 {
		t.Fatalf("used = %d, want the recorded 900 plus roughly 100 tokens of tail", used)
	}
}

// With no usage recorded yet the byte estimate is all there is.
func TestContextUsageFallsBackToTheEstimate(t *testing.T) {
	s, p, sessionID := newHarnessTestServer(t)
	if _, err := s.st.AddMessage(p.ID, sessionID, "user", strings.Repeat("z", 4000), "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}

	used, _ := contextUsageFor(t, s, p.ID)
	if used < 900 || used > 1100 {
		t.Fatalf("used = %d, want roughly 1000 tokens from the byte estimate", used)
	}
}
