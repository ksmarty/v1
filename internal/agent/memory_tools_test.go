package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"v1/internal/store"
)

// memoryExecutor returns an Executor backed by a fresh store.
func memoryExecutor(t *testing.T) (*Executor, *store.Store) {
	t.Helper()
	e := newTestExecutor(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.Store = st
	return e, st
}

func TestRememberStripsPrivateSections(t *testing.T) {
	e, st := memoryExecutor(t)
	res := executeContract(t, e, context.Background(), "remember",
		`{"content":"the API key is <private>sk-live-abc123</private> and it lives in .env","tags":"env, keys"}`)
	if res["ok"] != true {
		t.Fatalf("remember failed: %v", res)
	}
	mems, err := st.ListMemories("testproj")
	if err != nil || len(mems) != 1 {
		t.Fatalf("memories = %v err = %v", mems, err)
	}
	content := mems[0].Content
	if strings.Contains(content, "sk-live-abc123") {
		t.Fatalf("the credential reached the store: %q", content)
	}
	if !strings.Contains(content, "lives in .env") {
		t.Fatalf("the rest of the memory was lost: %q", content)
	}
}

func TestRememberRefusesAFullyPrivateMemory(t *testing.T) {
	e, st := memoryExecutor(t)
	res := executeContract(t, e, context.Background(), "remember", `{"content":"<private>sk-live-abc123</private>"}`)
	env := toolErrorOf(t, res)
	if env["type"] != "BAD_ARGUMENT" {
		t.Fatalf("type = %v, want BAD_ARGUMENT", env["type"])
	}
	if mems, _ := st.ListMemories("testproj"); len(mems) != 0 {
		t.Fatalf("a fully private memory was stored: %v", mems)
	}
}

func TestSearchMemoriesFindsBeyondInjection(t *testing.T) {
	e, st := memoryExecutor(t)
	for _, m := range []struct{ content, category string }{
		{"the webhook retry policy backs off to five minutes after the third failure", "fact"},
		{"the user prefers Tailwind over CSS modules", "preference"},
		{"the deploy script lives in scripts/deploy.sh", "fact"},
	} {
		if _, err := st.AddMemory("testproj", m.content, m.category, 1); err != nil {
			t.Fatal(err)
		}
	}
	res := executeContract(t, e, context.Background(), "search_memories", `{"query":"webhook retry policy"}`)
	rows, ok := res["results"].([]any)
	if !ok || len(rows) == 0 {
		t.Fatalf("no results: %v", res)
	}
	first, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %v", rows[0])
	}
	if !strings.Contains(first["content"].(string), "webhook retry policy") {
		t.Fatalf("the best match was not first: %v", first)
	}
	if first["relevance"] == nil {
		t.Fatalf("no relevance score in %v", first)
	}
	// A retrieved memory feeds its recency and frequency signal.
	mems, _ := st.ListMemories("testproj")
	for _, m := range mems {
		if m.ID == int64(first["id"].(float64)) && m.AccessCount == 0 {
			t.Fatalf("a returned memory was not touched: %+v", m)
		}
	}
}

func TestSearchMemoriesFiltersByCategory(t *testing.T) {
	e, st := memoryExecutor(t)
	if _, err := st.AddMemory("testproj", "the deploy script lives in scripts/deploy.sh", "fact", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMemory("testproj", "the user prefers dark mode for deploys", "preference", 1); err != nil {
		t.Fatal(err)
	}
	res := executeContract(t, e, context.Background(), "search_memories", `{"query":"deploy","category":"preference"}`)
	rows, _ := res["results"].([]any)
	for _, r := range rows {
		if row, ok := r.(map[string]any); ok && row["category"] != "preference" {
			t.Fatalf("the category filter leaked %v", row)
		}
	}
}

func TestSearchMemoriesExplainsAnEmptyResult(t *testing.T) {
	e, st := memoryExecutor(t)
	if _, err := st.AddMemory("testproj", "the deploy script lives in scripts/deploy.sh", "fact", 1); err != nil {
		t.Fatal(err)
	}
	res := executeContract(t, e, context.Background(), "search_memories", `{"query":"quantum chromodynamics"}`)
	if rows, _ := res["results"].([]any); len(rows) != 0 {
		t.Fatalf("expected no results, got %v", rows)
	}
	if res["note"] == nil {
		t.Fatalf("an empty result carried no explanation: %v", res)
	}
}

func TestSearchMemoriesRequiresAQuery(t *testing.T) {
	e, _ := memoryExecutor(t)
	if _, err := e.Execute(context.Background(), "search_memories", `{}`); err == nil {
		t.Fatal("an empty query was accepted")
	}
	if _, err := e.Execute(context.Background(), "search_memories", `{"query":"   "}`); err == nil {
		t.Fatal("a whitespace query was accepted")
	}
}
