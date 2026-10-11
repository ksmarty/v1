package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBuiltinFile(t *testing.T, root, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(root, dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readBuiltinFile(t *testing.T, root, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// An untouched copy of a bundled skill must pick up a shipped SKILL.md change:
// the on-disk file still matches the hash v1 recorded last time, so it is not a
// user edit.
func TestRefreshBuiltinsUpgradesUntouched(t *testing.T) {
	root := t.TempDir()
	b := FindBuiltin("session-naming")
	if b == nil {
		t.Fatal("session-naming builtin is missing")
	}
	old := "# Session naming\n\nold copy\n"
	writeBuiltinFile(t, root, b.Skill.Dir, "SKILL.md", old)
	// Pretend v1 wrote the old copy, so the manifest records its hash.
	if err := saveBuiltinManifest(root, map[string]string{
		b.Skill.ID: hashBuiltinFiles(map[string]string{"SKILL.md": old}),
	}); err != nil {
		t.Fatal(err)
	}
	if err := RefreshBuiltins(root, map[string]bool{b.Skill.ID: true}); err != nil {
		t.Fatal(err)
	}
	if got := readBuiltinFile(t, root, b.Skill.Dir, "SKILL.md"); got != b.Files["SKILL.md"] {
		t.Fatal("an untouched bundled skill was not upgraded to the shipped source")
	}
}

// A copy the user edited must be left alone, even when the shipped source moved
// on, so a local change is never clobbered.
func TestRefreshBuiltinsLeavesEditedCopy(t *testing.T) {
	root := t.TempDir()
	b := FindBuiltin("session-naming")
	if b == nil {
		t.Fatal("session-naming builtin is missing")
	}
	if err := InstallBuiltin(root, *b); err != nil {
		t.Fatal(err)
	}
	edited := b.Files["SKILL.md"] + "\nmy local edit\n"
	writeBuiltinFile(t, root, b.Skill.Dir, "SKILL.md", edited)
	if err := RefreshBuiltins(root, map[string]bool{b.Skill.ID: true}); err != nil {
		t.Fatal(err)
	}
	if got := readBuiltinFile(t, root, b.Skill.Dir, "SKILL.md"); got != edited {
		t.Fatal("an edited bundled skill was overwritten")
	}
}

// A builtin that is not in the installed list must not be materialized, so
// removing a bundled skill keeps it removed.
func TestRefreshBuiltinsIgnoresUninstalled(t *testing.T) {
	root := t.TempDir()
	if err := RefreshBuiltins(root, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "v1-extensions")); !os.IsNotExist(err) {
		t.Fatal("an uninstalled bundled skill was materialized")
	}
}

// A copy that predates the manifest is upgraded once, keeping the old file as a
// .bak so an edit made before tracking began is not lost.
func TestRefreshBuiltinsBacksUpUntracked(t *testing.T) {
	root := t.TempDir()
	b := FindBuiltin("session-naming")
	if b == nil {
		t.Fatal("session-naming builtin is missing")
	}
	old := "# Session naming\n\npre-tracking copy\n"
	writeBuiltinFile(t, root, b.Skill.Dir, "SKILL.md", old)
	if err := RefreshBuiltins(root, map[string]bool{b.Skill.ID: true}); err != nil {
		t.Fatal(err)
	}
	if got := readBuiltinFile(t, root, b.Skill.Dir, "SKILL.md"); got != b.Files["SKILL.md"] {
		t.Fatal("a pre-manifest copy was not upgraded")
	}
	if got := readBuiltinFile(t, root, b.Skill.Dir, "SKILL.md.bak"); got != old {
		t.Fatal("the pre-manifest copy was not backed up")
	}
}
