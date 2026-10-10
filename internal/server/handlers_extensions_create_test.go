package server

import (
	"context"
	"encoding/json"
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

// The reload outcome must report hooks as well as tools and sections, so the
// agent can confirm a hook-only extension loaded.
func TestExtensionReloadOutcomeReportsHooks(t *testing.T) {
	reload := map[string]any{
		"reloaded": true,
		"state": map[string]any{
			"loaded": []any{
				map[string]any{
					"id":       "auto-session-name",
					"tools":    []any{"session_namer"},
					"sections": []any{"auto-session-name"},
					"hooks":    []any{"pi.generation:beforeRequest+onYield", "pi.tool:beforeTool"},
				},
			},
		},
	}
	out := extensionReloadOutcome(reload, "auto-session-name")
	if !out.reloaded {
		t.Fatal("reloaded = false")
	}
	if len(out.hooks) != 2 || out.hooks[0] != "pi.generation:beforeRequest+onYield" {
		t.Fatalf("hooks = %v", out.hooks)
	}
	if len(out.tools) != 1 || out.tools[0] != "session_namer" {
		t.Fatalf("tools = %v", out.tools)
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

// list_extensions must work with the harness stopped: it reports the installed
// extensions and says the harness is not running, so the agent can still see
// what is installed without shelling into the host.
func TestListExtensionsReportsInstalled(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	s, _, _ := newAuthServer(t)
	ctx := context.Background()
	if _, err := s.createExtension(ctx, "word-stats", "counts words",
		`export default (pi) => ({ name: "word-stats", tools: [] });`); err != nil {
		t.Fatalf("createExtension: %v", err)
	}
	out, err := s.listExtensions(ctx)
	if err != nil {
		t.Fatalf("listExtensions: %v", err)
	}
	var got struct {
		Extensions []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		} `json:"extensions"`
		HarnessAvailable bool `json:"harnessAvailable"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("listExtensions returned invalid JSON: %v\n%s", err, out)
	}
	found := false
	for _, ext := range got.Extensions {
		if ext.ID == "word-stats" && ext.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatalf("word-stats missing from %s", out)
	}
	if got.HarnessAvailable {
		t.Fatalf("harnessAvailable = true with the harness stopped: %s", out)
	}
}
