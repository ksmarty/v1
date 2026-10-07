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
	if _, err := s.createExtension(context.Background(), "../escape", "d", "export default () => ({});"); err == nil {
		t.Fatal("a traversing id was accepted")
	}
	if source, _ := extensions.Read(s.extensionsRoot(), "escape"); source != "" {
		t.Fatalf("a rejected id wrote a file:\n%s", source)
	}
}

// A syntax error must be reported to the agent (node's message) and leave
// nothing on disk.
func TestCreateExtensionRejectsSyntaxError(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	s, _, _ := newAuthServer(t)
	_, err := s.createExtension(context.Background(), "broken", "d", "export default (pi) => ({")
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
	message, err := s.createExtension(context.Background(), "word-stats", "counts words", src)
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
