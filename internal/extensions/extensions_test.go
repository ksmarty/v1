package extensions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
}

// An id becomes a directory name, so anything that could escape the root has to
// be refused before it reaches the filesystem.
func TestValidateRejectsUnsafeIDs(t *testing.T) {
	for _, id := range []string{"", "Upper", "has space", "../escape", "a/b", "-lead", "dot.dot", "."} {
		if err := Validate(id, "export default () => ({});"); err == nil {
			t.Fatalf("Validate(%q) accepted an invalid id", id)
		}
	}
}

func TestValidateRejectsEmptyAndOversizedSource(t *testing.T) {
	if err := Validate("ok", "   \n\t "); err == nil {
		t.Fatal("an empty source was accepted")
	}
	if err := Validate("ok", strings.Repeat("x", maxSource+1)); err == nil {
		t.Fatal("an oversized source was accepted")
	}
}

// The syntax check must be the parser that will actually run the file, so a
// module Go cannot judge is accepted and a real syntax error is reported with
// node's own message.
func TestValidateChecksSyntaxWithNode(t *testing.T) {
	requireNode(t)
	if err := Validate("good", "export default (pi) => ({ name: \"good\", tools: [] });"); err != nil {
		t.Fatalf("a valid module was rejected: %v", err)
	}
	err := Validate("bad", "export default (pi) => ({")
	if err == nil {
		t.Fatal("a syntax error was accepted")
	}
	if !strings.Contains(err.Error(), "SyntaxError") {
		t.Fatalf("the error does not come from node's parser: %v", err)
	}
}

func TestWriteReadRemoveRoundTrip(t *testing.T) {
	root := t.TempDir()
	const source = "export default (pi) => ({ name: \"sample\", tools: [] });\n"
	if err := Write(root, "sample", source); err != nil {
		t.Fatal(err)
	}
	got, err := Read(root, "sample")
	if err != nil {
		t.Fatal(err)
	}
	if got != source {
		t.Fatalf("Read returned %q, want the written source", got)
	}
	if _, err := os.Stat(filepath.Join(Dir(root, "sample"), "index.js")); err != nil {
		t.Fatalf("the source is not at the expected path: %v", err)
	}
	ids, err := List(root)
	if err != nil || len(ids) != 1 || ids[0] != "sample" {
		t.Fatalf("List = %v (%v), want [sample]", ids, err)
	}

	if err := Remove(root, "sample"); err != nil {
		t.Fatal(err)
	}
	if ids, _ := List(root); len(ids) != 0 {
		t.Fatalf("List after Remove = %v, want empty", ids)
	}
	// Reading a removed extension is not an error: the editor shows empty.
	if got, err := Read(root, "sample"); err != nil || got != "" {
		t.Fatalf("Read after Remove = %q, %v; want empty and no error", got, err)
	}
	// Removing something absent is also not an error.
	if err := Remove(root, "never-existed"); err != nil {
		t.Fatalf("Remove of a missing extension failed: %v", err)
	}
}

func TestReconcileFollowsDisk(t *testing.T) {
	root := t.TempDir()
	if err := Write(root, "on-disk", "export default () => ({});"); err != nil {
		t.Fatal(err)
	}
	if err := Write(root, "discovered", "export default () => ({});"); err != nil {
		t.Fatal(err)
	}

	known := []Extension{
		{ID: "on-disk", Name: "kept name", Description: "kept", Enabled: true},
		{ID: "stale", Name: "no directory"},
	}
	out := Reconcile(root, known)
	if len(out) != 2 {
		t.Fatalf("Reconcile returned %d extensions, want 2: %+v", len(out), out)
	}
	byID := map[string]Extension{}
	for _, ext := range out {
		byID[ext.ID] = ext
	}
	// Metadata whose directory is gone is dropped.
	if _, ok := byID["stale"]; ok {
		t.Fatal("metadata for a missing directory survived")
	}
	// Existing metadata is preserved, including the enabled flag.
	if ext := byID["on-disk"]; ext.Name != "kept name" || !ext.Enabled {
		t.Fatalf("existing metadata was not preserved: %+v", ext)
	}
	// A directory with no metadata is discovered, and defaults to disabled.
	if ext, ok := byID["discovered"]; !ok {
		t.Fatal("a directory with no metadata was not discovered")
	} else if ext.Enabled {
		t.Fatalf("a discovered extension defaulted to enabled: %+v", ext)
	}
}

func TestMaterializeWritesBuiltinOnce(t *testing.T) {
	root := t.TempDir()
	if err := Materialize(root, nil); err != nil {
		t.Fatal(err)
	}
	source, err := Read(root, "delegate")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, "pi.defineTool") {
		t.Fatalf("the materialized delegate does not define a tool:\n%s", source)
	}

	// A builtin that is already present is left alone, so a user's edit is not
	// silently reverted on the next start.
	const edited = "// edited by the user\n"
	if err := Write(root, "delegate", edited); err != nil {
		t.Fatal(err)
	}
	if err := Materialize(root, nil); err != nil {
		t.Fatal(err)
	}
	got, err := Read(root, "delegate")
	if err != nil {
		t.Fatal(err)
	}
	if got != edited {
		t.Fatalf("Materialize overwrote an edited builtin:\n%s", got)
	}
}

// Every bundled extension must be a module node can parse, since a broken one
// would fail at load time on a user's machine rather than here.
func TestBuiltinSourcesAreValidModules(t *testing.T) {
	requireNode(t)
	builtins := Builtins()
	if len(builtins) == 0 {
		t.Fatal("no builtin extensions are registered")
	}
	for _, builtin := range builtins {
		if err := Validate(builtin.Extension.ID, builtin.Source); err != nil {
			t.Fatalf("builtin %q has an invalid source: %v", builtin.Extension.ID, err)
		}
		if FindBuiltin(builtin.Extension.ID) == nil {
			t.Fatalf("builtin %q is not findable", builtin.Extension.ID)
		}
	}
	if FindBuiltin("no-such-builtin") != nil {
		t.Fatal("FindBuiltin returned something for an unknown id")
	}
}
