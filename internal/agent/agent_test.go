package agent

import (
	"os"
	"path/filepath"
	"testing"

	"v1/internal/llm"
)

func TestFreshProject(t *testing.T) {
	dir := t.TempDir()
	if !freshProject(dir) {
		t.Fatal("empty dir should be fresh")
	}
	// The scaffold "empty" template writes only a README.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !freshProject(dir) {
		t.Fatal("README-only dir should be fresh")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if freshProject(dir) {
		t.Fatal("dir with real files should not be fresh")
	}
	if freshProject(filepath.Join(dir, "missing")) {
		t.Fatal("missing dir should not be fresh")
	}
}

func TestStripBrokenToolCalls(t *testing.T) {
	tcs := []llm.ToolCall{
		{ID: "a", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"ok"}`}},
		{ID: "b", Function: llm.FunctionCall{Name: "write_file", Arguments: `{"path":"web/src/main.t`}}, // cut mid-JSON
		{ID: "c", Function: llm.FunctionCall{Name: "no_args", Arguments: ""}},                           // never started
	}
	got := stripBrokenToolCalls(tcs)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %d calls, want only the complete one: %+v", len(got), got)
	}
}
