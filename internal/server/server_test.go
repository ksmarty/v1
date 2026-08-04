package server

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	good := map[string]string{
		"":           root,
		".":          root,
		"a.txt":      filepath.Join(root, "a.txt"),
		"sub/b.txt":  filepath.Join(root, "sub", "b.txt"),
		"a/../../b":  filepath.Join(root, "b"),
		"/abs/p.txt": filepath.Join(root, "abs", "p.txt"),
	}
	for rel, want := range good {
		got, err := safeJoin(root, rel)
		if err != nil {
			t.Fatalf("safeJoin(%q) error: %v", rel, err)
		}
		if got != want {
			t.Fatalf("safeJoin(%q) = %q, want %q", rel, got, want)
		}
	}
	// Anything resolved must stay inside root even when not rejected.
	for _, rel := range []string{"../x", "../../etc/passwd", "a/../../../b"} {
		got, err := safeJoin(root, rel)
		if err != nil {
			continue
		}
		if got != root && !strings.HasPrefix(got, root+string(filepath.Separator)) {
			t.Fatalf("safeJoin(%q) escaped root: %q", rel, got)
		}
	}
}
