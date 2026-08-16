package agent

import (
	"os"
	"path/filepath"
	"testing"
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
