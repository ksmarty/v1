package server

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"v1/internal/store"
)

// memTestServer opens a scratch store and a Server wired to it.
func memTestServer(t *testing.T) (*store.Store, *Server, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, &Server{st: st}, store.NewID()
}

func mustAdd(t *testing.T, st *store.Store, projectID, content, category string, importance float64) {
	t.Helper()
	if _, err := st.AddMemory(projectID, content, category, importance); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryPromptRanksByRelevance(t *testing.T) {
	st, s, pid := memTestServer(t)
	mustAdd(t, st, pid, "project uses pnpm", "fact", 1)
	mustAdd(t, st, pid, "user prefers dark themes", "preference", 1)
	got := s.memoryPrompt(pid, "", "please switch the app to a dark theme")
	// The message is about themes, so the dark-theme memory is injected and the
	// unrelated pnpm one is dropped rather than padding the section out to a
	// fixed size with whatever happens to be most important.
	if !strings.Contains(got, "dark themes") {
		t.Fatalf("the relevant memory was not injected: %q", got)
	}
	if strings.Contains(got, "pnpm") {
		t.Fatalf("an unrelated memory was injected: %q", got)
	}
}

func TestMemoryPromptEmptyAndDisabled(t *testing.T) {
	st, s, pid := memTestServer(t)
	if got := s.memoryPrompt(pid, "", "hi"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	mustAdd(t, st, pid, "stale fact", "fact", 1)
	mems, err := st.ListMemories(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMemoryEnabled(pid, mems[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if got := s.memoryPrompt(pid, "", "hi"); got != "" {
		t.Fatalf("disabled memory leaked: %q", got)
	}
}

func TestMemoryPromptTokensAndTouches(t *testing.T) {
	st, s, pid := memTestServer(t)
	for i := 1; i <= 12; i++ {
		mustAdd(t, st, pid, string(rune('A'+i-1))+"-fact-"+strings.Repeat("x", 120), "fact", 1)
	}
	got := s.memoryPrompt(pid, "", "fact")
	if len(got) > memoryBudgetChars+400 {
		t.Fatalf("prompt too large: %d chars", len(got))
	}
	// At most memoryTopK are injected.
	if n := strings.Count(got, "\n- ["); n > memoryTopK {
		t.Fatalf("injected %d memories, cap is %d", n, memoryTopK)
	}
	// Injected memories were touched (access_count bumped above 0).
	mems, err := st.ListMemories(pid)
	if err != nil {
		t.Fatal(err)
	}
	touched := 0
	for _, m := range mems {
		if m.AccessCount > 0 {
			touched++
		}
	}
	if touched == 0 || touched > memoryTopK {
		t.Fatalf("touched %d memories, want 1..%d", touched, memoryTopK)
	}
}

func TestPlanPromptRoundtrip(t *testing.T) {
	st, s, pid := memTestServer(t)
	if got := s.planPrompt(pid); got != "" {
		t.Fatalf("expected no plan, got %q", got)
	}
	plan := `{"goal":"build the app","features":[{"id":"f1","description":"scaffold"}],"invariants":[],"checkpoints":[],"estimated_turns":3}`
	if err := st.SetPlan(pid, plan); err != nil {
		t.Fatal(err)
	}
	got := s.planPrompt(pid)
	if !strings.Contains(got, "## Active Plan") || !strings.Contains(got, "build the app") {
		t.Fatalf("plan prompt = %q", got)
	}
	// Replacing the plan works.
	plan2 := `{"goal":"changed scope","features":[{"id":"f1","description":"x"}],"estimated_turns":1}`
	if err := st.SetPlan(pid, plan2); err != nil {
		t.Fatal(err)
	}
	if got := s.planPrompt(pid); strings.Contains(got, "build the app") {
		t.Fatalf("plan was not replaced: %q", got)
	}
}

func TestMemoryDecayAndPin(t *testing.T) {
	st, s, pid := memTestServer(t)
	// Pinned memory (importance 2) survives any amount of time.
	mustAdd(t, st, pid, "pinned preference", "preference", 2)
	// Normal memory, then simulate 40 days of disuse.
	mustAdd(t, st, pid, "fading fact", "fact", 1)
	mems, err := st.ListMemories(pid)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mems {
		if m.Content == "fading fact" {
			if err := st.TouchMemory(m.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, m := range mems {
		if m.Content == "fading fact" {
			s.st.SetLastAccessed(m.ID, time.Now().Add(-40*24*time.Hour).Unix())
		}
	}
	got := s.memoryPrompt(pid, "", "anything")
	if !strings.Contains(got, "pinned preference") {
		t.Fatalf("pinned memory vanished: %q", got)
	}
	if strings.Contains(got, "fading fact") {
		t.Fatalf("decayed memory should have been dropped: %q", got)
	}
}
