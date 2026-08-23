package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHeadCommit verifies HeadCommit returns a short hash once the repo has
// commits and errors on a directory that isn't a repo at all.
func TestHeadCommit(t *testing.T) {
	dir := t.TempDir()
	if _, err := HeadCommit(dir); err == nil {
		t.Fatal("non-repo directory should error")
	}
	if err := InitRepo(dir, "tester"); err != nil {
		t.Fatal(err)
	}
	h, err := HeadCommit(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 12 {
		t.Fatalf("hash = %q, want 12 chars", h)
	}
}

// TestInitRepoSkipsExisting verifies auto-init is a no-op on an existing repo
// (no second initial commit, HEAD unchanged).
func TestInitRepoSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	if err := InitRepo(dir, "tester"); err != nil {
		t.Fatal(err)
	}
	h1, _ := HeadCommit(dir)
	if err := InitRepo(dir, "tester"); err != nil {
		t.Fatal(err)
	}
	h2, _ := HeadCommit(dir)
	if h1 != h2 {
		t.Fatalf("HEAD changed across InitRepo on existing repo: %s -> %s", h1, h2)
	}
}

// TestInitRepoCreatesIgnore verifies brand-new repos get the project
// .gitignore (so .env etc. are never committed by checkpoints).
func TestInitRepoCreatesIgnore(t *testing.T) {
	dir := t.TempDir()
	if err := InitRepo(dir, "tester"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf(".gitignore not written: %v", err)
	}
	if !strings.Contains(string(data), ".env") {
		t.Fatalf(".gitignore = %q, want .env coverage", data)
	}
}
