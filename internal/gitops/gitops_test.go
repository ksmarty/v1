package gitops

import (
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
)

// setupTestRepo creates a throwaway repo with one committed file and returns
// its path.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "a.txt"), "base\n")
	if _, err := w.Add("a.txt"); err != nil {
		t.Fatal(err)
	}
	_, err = w.Commit("initial", &git.CommitOptions{Author: authorSignature("")})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardFile(t *testing.T) {
	dir := setupTestRepo(t)

	// Modify a tracked file.
	writeFile(t, filepath.Join(dir, "a.txt"), "changed\n")
	if err := StageFile(dir, "a.txt"); err != nil {
		t.Fatal(err)
	}
	// Also add an untracked file.
	writeFile(t, filepath.Join(dir, "new.txt"), "uno\n")

	if err := DiscardFile(dir, "a.txt"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "base\n" {
		t.Fatalf("a.txt = %q, want %q", string(b), "base\n")
	}
	if err := DiscardFile(dir, "new.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file should be removed after discard")
	}
	// Clean worktree after both discards.
	st, err := repoStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsClean() {
		t.Fatalf("worktree not clean after discards: %v", st)
	}
}

func TestDiscardDeleted(t *testing.T) {
	dir := setupTestRepo(t)
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := DiscardFile(dir, "a.txt"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "base\n" {
		t.Fatalf("a.txt = %q, want %q", string(b), "base\n")
	}
}

func TestDiscardAll(t *testing.T) {
	dir := setupTestRepo(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "changed\n")
	writeFile(t, filepath.Join(dir, "keep.txt"), "kept\n")
	if err := StageFile(dir, "keep.txt"); err != nil {
		t.Fatal(err)
	}
	if err := DiscardAll(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "base\n" {
		t.Fatalf("a.txt = %q after discard-all", string(b))
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked keep.txt should be removed")
	}
	st, err := repoStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsClean() {
		t.Fatalf("worktree not clean after discard-all: %v", st)
	}
}

func TestStageFile(t *testing.T) {
	dir := setupTestRepo(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "changed\n")
	if err := StageFile(dir, "a.txt"); err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	st, err := w.Status()
	if err != nil {
		t.Fatal(err)
	}
	if fs := st.File("a.txt"); fs == nil {
		t.Fatalf("a.txt should be staged, got nil")
	} else if fs.Staging != git.Modified {
		t.Fatalf("a.txt should be staged as modified, got %+v", fs)
	}
}

// repoStatus is a helper returning the worktree status map.
func repoStatus(dir string) (git.Status, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return nil, err
	}
	w, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	return w.Status()
}