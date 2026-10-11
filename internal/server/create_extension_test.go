package server

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"v1/internal/extensions"
)

// The create_extension tool's backend must refuse an unsafe id before it writes
// anything.
func TestCreateExtensionRejectsBadID(t *testing.T) {
	s, _, _ := newAuthServer(t)
	if _, err := s.createExtension(context.Background(), "../escape", "d", "1.0.0", "first", "export default () => ({});"); err == nil {
		t.Fatal("a traversing id was accepted")
	}
	if source, _ := extensions.Read(s.extensionsRoot(), "escape"); source != "" {
		t.Fatalf("a rejected id wrote a file:\n%s", source)
	}
}

// The harness reports cross-extension name conflicts; create_extension must
// turn the ones involving the new extension into a message, so the agent learns
// its tool will be shadowed instead of believing it loaded.
func TestConflictsForExtension(t *testing.T) {
	raw := []any{
		map[string]any{"kind": "tool", "name": "shared", "ids": []any{"alpha", "beta"}},
		map[string]any{"kind": "tool", "name": "read_file", "ids": []any{"alpha", "builtin"}},
		map[string]any{"kind": "section", "name": "notes", "ids": []any{"beta", "gamma"}},
	}
	got := conflictsForExtension(raw, "alpha")
	if len(got) != 2 {
		t.Fatalf("conflicts = %v, want two for alpha", got)
	}
	if !strings.Contains(got[0], `"shared"`) || !strings.Contains(got[0], "beta") {
		t.Fatalf("first conflict = %q", got[0])
	}
	if !strings.Contains(got[1], "builtin") {
		t.Fatalf("host-tool conflict = %q", got[1])
	}
	if other := conflictsForExtension(raw, "delta"); len(other) != 0 {
		t.Fatalf("an unrelated extension got conflicts: %v", other)
	}
}

// A syntax error must be reported to the agent (node's message) and leave
// nothing on disk.
func TestCreateExtensionRejectsSyntaxError(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	s, _, _ := newAuthServer(t)
	_, err := s.createExtension(context.Background(), "broken", "d", "1.0.0", "first", "export default (pi) => ({")
	if err == nil {
		t.Fatal("a broken module was accepted")
	}
	if !strings.Contains(err.Error(), "SyntaxError") {
		t.Fatalf("the rejection does not carry node's error: %v", err)
	}
	if source, _ := extensions.Read(s.extensionsRoot(), "broken"); source != "" {
		t.Fatalf("a rejected extension was written:\n%s", source)
	}
}

// A good extension is written to disk, recorded enabled, and named in the
// result so the agent can confirm what loaded.
func TestCreateExtensionInstallsEnabled(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	s, _, _ := newAuthServer(t)
	const src = `export default (pi) => ({ name: "word-stats", tools: [] });`
	message, err := s.createExtension(context.Background(), "word-stats", "counts words", "1.0.0", "first", src)
	if err != nil {
		t.Fatalf("createExtension: %v", err)
	}
	if !strings.Contains(message, "word-stats") {
		t.Fatalf("the result does not name the extension: %q", message)
	}
	onDisk, err := extensions.Read(s.extensionsRoot(), "word-stats")
	if err != nil {
		t.Fatal(err)
	}
	if onDisk != src {
		t.Fatalf("the source on disk is %q, want %q", onDisk, src)
	}
	var enabled bool
	for _, ext := range s.installedExtensions() {
		if ext.ID == "word-stats" {
			enabled = ext.Enabled
		}
	}
	if !enabled {
		t.Fatal("the installed extension is not enabled")
	}
}

// An update must move the version forward, and the result must show what
// changed so the agent (and the transcript) can see it without re-reading the
// source.
func TestCreateExtensionRequiresVersionIncrement(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	s, _, _ := newAuthServer(t)
	ctx := context.Background()
	const first = `export default (pi) => ({ name: "word-stats", tools: [] });`
	if _, err := s.createExtension(ctx, "word-stats", "counts words", "1.0.0", "first", first); err != nil {
		t.Fatalf("first install: %v", err)
	}
	const second = `export default (pi) => ({ name: "word-stats", tools: [], note: "two" });`
	// The same version, or an older one, is refused before writing.
	if _, err := s.createExtension(ctx, "word-stats", "", "1.0.0", "same", second); err == nil || !strings.Contains(err.Error(), "greater than") {
		t.Fatalf("a non-incremented version was accepted: %v", err)
	}
	if got, _ := extensions.Read(s.extensionsRoot(), "word-stats"); got != first {
		t.Fatalf("a rejected update changed the source: %q", got)
	}
	// A greater version is accepted and the result carries the diff.
	message, err := s.createExtension(ctx, "word-stats", "", "1.1.0", "second", second)
	if err != nil {
		t.Fatalf("incremented update: %v", err)
	}
	if !strings.Contains(message, "1 line(s) added") || !strings.Contains(message, "+ ") || !strings.Contains(message, "note") {
		t.Fatalf("the result does not show the diff: %q", message)
	}
}

// A missing version or changelog is refused, so an extension always carries
// both.
func TestCreateExtensionRequiresVersionAndChangelog(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	s, _, _ := newAuthServer(t)
	const src = `export default (pi) => ({ name: "word-stats", tools: [] });`
	if _, err := s.createExtension(context.Background(), "word-stats", "d", "", "first", src); err == nil {
		t.Fatal("a missing version was accepted")
	}
	if _, err := s.createExtension(context.Background(), "word-stats", "d", "1.0.0", "", src); err == nil {
		t.Fatal("a missing changelog was accepted")
	}
}
