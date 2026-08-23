package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"v1/internal/llm"
)

const validPlanResponse = `{
  "goal": "Build a todo app",
  "features": [
    {"id": "f1", "description": "scaffold the vite project", "depends_on": []},
    {"id": "f2", "description": "add todo CRUD", "depends_on": ["f1"]}
  ],
  "invariants": ["never expose secrets in client code"],
  "checkpoints": [
    {"step": 1, "action": "install dependencies", "verification": "npm install succeeds"},
    {"step": 2, "action": "run build", "verification": "npm run build passes"}
  ],
  "estimated_turns": 4
}`

// TestCanonicalPlanNormalizes verifies a valid plan response is normalized
// (fenced JSON tolerated, missing ids filled, compact output).
func TestCanonicalPlanNormalizes(t *testing.T) {
	out, err := canonicalPlan("```json\n" + validPlanResponse + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\n") || strings.HasPrefix(out, "```") {
		t.Fatalf("output should be compact JSON without fences, got %q", out)
	}
	var doc PlanDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Goal != "Build a todo app" || len(doc.Features) != 2 || len(doc.Invariants) != 1 {
		t.Fatalf("plan lost data: %v", doc)
	}
	if len(doc.Features[1].DependsOn) != 1 || doc.Features[1].DependsOn[0] != "f1" {
		t.Fatalf("depends_on lost: %v", doc.Features[1].DependsOn)
	}
}

// TestCanonicalPlanMissingIDs verifies features without ids get generated
// ones (f1, f2 …).
func TestCanonicalPlanMissingIDs(t *testing.T) {
	raw := `{"goal":"g","features":[{"description":"first"},{"description":"second"}],"invariants":["i"]}`
	out, err := canonicalPlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	var doc PlanDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Features[0].ID != "f1" || doc.Features[1].ID != "f2" {
		t.Fatalf("ids = %q, %q; want f1, f2", doc.Features[0].ID, doc.Features[1].ID)
	}
}

// TestCanonicalPlanRejectsInvalid verifies malformed plans (no goal, no
// features, no invariants, duplicate ids) are refused.
func TestCanonicalPlanRejectsInvalid(t *testing.T) {
	cases := []string{
		`{"features":[{"id":"f1","description":"d"}],"invariants":["i"]}`,
		`{"goal":"g","invariants":["i"]}`,
		`{"goal":"g","features":[{"id":"f1","description":"d"}]}`,
		`{"goal":"g","features":[{"id":"f1","description":"d"},{"id":"f1","description":"e"}],"invariants":["i"]}`,
		`not json at all`,
		``,
	}
	for _, c := range cases {
		if _, err := canonicalPlan(c); err == nil {
			t.Fatalf("case %q should be rejected", c)
		}
	}
}

// TestGeneratePlanParsesModelOutput verifies the planner round trip against a
// fake model that answers with a fenced plan JSON.
func TestGeneratePlanParsesModelOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		content := escapeJSON("```json\n" + validPlanResponse + "\n```")
		_, _ = w.Write([]byte(
			sseLine(`{"choices":[{"delta":{"content":"`+content+`"}}]}`) +
				sseLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	plan, err := GeneratePlan(context.Background(), llm.NewClient(srv.URL, "", "planner"), "Plan a thing with several steps please")
	if err != nil {
		t.Fatal(err)
	}
	var doc PlanDocument
	if err := json.Unmarshal([]byte(plan), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Goal != "Build a todo app" || len(doc.Features) != 2 {
		t.Fatalf("plan = %v", doc)
	}
}

// TestGeneratePlanEmptyFails verifies an empty model reply becomes an error.
func TestGeneratePlanEmptyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()
	if _, err := GeneratePlan(context.Background(), llm.NewClient(srv.URL, "", "planner"), "Plan something"); err == nil {
		t.Fatal("expected an error for an empty planner reply")
	}
}

// TestLooksLikePlanTask covers the task-detection heuristic.
func TestLooksLikePlanTask(t *testing.T) {
	if !looksLikePlanTask("Build me a full todo app with auth, storage, and a nice dark mode") {
		t.Fatal("a substantive request should trigger planning")
	}
	for _, msg := range []string{"", "/plan build a todo app", "hi", "yes", "?"} {
		if looksLikePlanTask(msg) {
			t.Fatalf("%q should not trigger planning", msg)
		}
	}
}

// TestAutoPlanWiresPlanIntoTurn runs a full turn with AutoPlan: the first
// model call is the planner, the second the main loop. The resulting plan
// must be stored and present in the main loop's system prompt.
func TestAutoPlanWiresPlanIntoTurn(t *testing.T) {
	s, p := newRunChatStore(t)
	calls := 0
	var mainRequest []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			mainRequest, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1: // planner call
			content := escapeJSON(validPlanResponse)
			_, _ = w.Write([]byte(
				sseLine(`{"choices":[{"delta":{"content":"`+content+`"}}]}`) +
					sseLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
					"data: [DONE]\n\n",
			))
		default: // main loop turn
			_, _ = w.Write([]byte(
				sseLine(`{"choices":[{"delta":{"content":"starting the build"}}]}`) +
					sseLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
					"data: [DONE]\n\n",
			))
		}
	}))
	defer srv.Close()

	chat := ChatParams{
		Store:       s,
		Project:     p,
		Client:      llm.NewClient(srv.URL, "", "main"),
		Exec:        &Executor{},
		Message:     "Build a todo app with auth and a settings page and a profile view",
		SessionID:   "plan-sess",
		AutoPlan:    true,
		HardTimeout: 30 * time.Second,
		Emit:        func(ChatEvent) {},
	}
	if _, err := RunChat(context.Background(), chat); err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("expected planner + main calls, got %d", calls)
	}
	plan, ok, err := s.GetPlan(p.ID)
	if err != nil || !ok {
		t.Fatalf("plan not stored: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(plan, "Build a todo app") {
		t.Fatalf("stored plan = %q, want the goal", plan)
	}
	if mainRequest == nil {
		t.Fatal("main loop request was not captured")
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(mainRequest, &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range body.Messages {
		var text string
		switch v := m.Content.(type) {
		case string:
			text = v
		case []any:
			for _, part := range v {
				if pm, ok := part.(map[string]any); ok {
					if s, ok := pm["text"].(string); ok {
						text += s
					}
				}
			}
		}
		if strings.Contains(text, "## Active Plan") && strings.Contains(text, "Build a todo app") {
			found = true
		}
	}
	if !found {
		t.Fatal("the plan was not injected into the main loop's system prompt")
	}
}

// TestAutoPlanSkippedForChattyMessages verifies a trivial message never calls
// the planner (no store write, no PlanPrompt).
func TestAutoPlanSkippedForChattyMessages(t *testing.T) {
	s, p := newRunChatStore(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			sseLine(`{"choices":[{"delta":{"content":"hi"}}]}`) +
				sseLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	chat := &ChatParams{
		Store:       s,
		Project:     p,
		Client:      llm.NewClient(srv.URL, "", "main"),
		Exec:        &Executor{},
		Message:     "hi",
		SessionID:   "chat-sess",
		AutoPlan:    true,
		HardTimeout: 30 * time.Second,
		Emit:        func(ChatEvent) {},
	}
	if _, err := RunChat(context.Background(), *chat); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected only the main call, got %d (planner should be skipped)", calls)
	}
	if _, ok, _ := s.GetPlan(p.ID); ok {
		t.Fatal("no plan should be stored for a trivial message")
	}
}

// escapeJSON makes a JSON document safe to embed in a single SSE content
// delta (escapes quotes/backslashes; no newlines since content arrives raw).
func escapeJSON(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n', '\r', '\t':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
