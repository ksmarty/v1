package agent

import (
	"context"
	"strings"
	"testing"

	"v1/internal/store"
)

// The tool is advertised only when a key is configured, so the model is never
// offered a search it cannot run.
func TestWebSearchIsOfferedOnlyWithAKey(t *testing.T) {
	params := ChatParams{Project: &store.Project{}, SkipCompactionSnapshot: true}
	if hasToolName(params.ToolSet(), "web_search") {
		t.Fatal("web_search should not be offered without a LangSearch key")
	}
	params.WebSearchKey = "   "
	if hasToolName(params.ToolSet(), "web_search") {
		t.Fatal("a blank key should not enable web_search")
	}
	params.WebSearchKey = "ls-key"
	if !hasToolName(params.ToolSet(), "web_search") {
		t.Fatal("web_search should be offered once a key is set")
	}
}

// Calling it anyway — a stale transcript, or a key cleared mid-session — must
// fail with something the user can act on, and must not touch the network.
func TestWebSearchWithoutAKeyExplainsItself(t *testing.T) {
	e := &Executor{}
	res, err := e.Execute(context.Background(), "web_search", `{"query":"golang"}`)
	if err == nil {
		t.Fatalf("expected an error, got %q", res)
	}
	if !strings.Contains(err.Error(), "LangSearch API key") {
		t.Fatalf("the error should name the missing key, got %v", err)
	}
}

// An empty query is refused before any request is made.
func TestWebSearchRejectsAnEmptyQuery(t *testing.T) {
	e := &Executor{WebSearchKey: "ls-key"}
	if _, err := e.Execute(context.Background(), "web_search", `{"query":"  "}`); err == nil {
		t.Fatal("an empty query should be refused")
	}
}
