package server

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"v1/internal/extensions"
)

// Installing an id that already exists must replace its source in place: the
// agent iterating on an extension calls create_extension repeatedly, and a
// second entry (or a stale source) would leave the agent editing one file while
// another loads.
func TestCreateExtensionReinstallReplaces(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	s, _, _ := newAuthServer(t)
	ctx := context.Background()
	const first = `export default (pi) => ({ name: "word-stats", tools: [] });`
	const second = `export default (pi) => ({ name: "word-stats", tools: [], note: "v2" });`

	if _, err := s.createExtension(ctx, "word-stats", "counts words", first); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if _, err := s.createExtension(ctx, "word-stats", "", second); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	if got, _ := extensions.Read(s.extensionsRoot(), "word-stats"); got != second {
		t.Fatalf("source on disk = %q, want the replacement", got)
	}
	count := 0
	for _, ext := range s.installedExtensions() {
		if ext.ID == "word-stats" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("re-install left %d entries for word-stats, want 1", count)
	}
}

// With the harness stopped the extension is still written and enabled, but the
// result must say it has not loaded yet rather than implying it is live.
func TestCreateExtensionReportsStoppedHarness(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	s, _, _ := newAuthServer(t)
	message, err := s.createExtension(context.Background(), "word-stats", "counts words",
		`export default (pi) => ({ name: "word-stats", tools: [] });`)
	if err != nil {
		t.Fatalf("createExtension: %v", err)
	}
	if !strings.Contains(message, "harness") {
		t.Fatalf("the result does not mention the harness: %q", message)
	}
	if !strings.Contains(message, "word-stats") {
		t.Fatalf("the result does not name the extension: %q", message)
	}
}
