package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tool must degrade clearly when the turn has no way to install an
// extension, rather than silently doing nothing.
func TestCreateExtensionToolUnavailable(t *testing.T) {
	e := newTestExecutor(t)
	out, err := e.createExtension(context.Background(), `{"id":"x","source":"export default () => ({});"}`)
	if out != "" {
		t.Fatalf("an unavailable tool returned content: %q", out)
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("want a ToolError, got %v", err)
	}
	if te.Type != "UNAVAILABLE" {
		t.Fatalf("type = %q, want UNAVAILABLE", te.Type)
	}
	if got := formatToolError(err); !strings.Contains(got, `"success":false`) || !strings.Contains(got, "extensions are not available") {
		t.Fatalf("formatToolError did not build the contract: %s", got)
	}
}

// Missing arguments must be refused before the callback runs, as a ToolError
// rather than a bare error that aborts the turn.
func TestCreateExtensionToolRequiresIDAndSource(t *testing.T) {
	e := newTestExecutor(t)
	e.CreateExtension = func(context.Context, string, string, string) (string, error) {
		t.Fatal("the callback must not run for invalid arguments")
		return "", nil
	}
	for _, args := range []string{`{}`, `{"id":"x"}`, `{"source":"x"}`} {
		_, err := e.createExtension(context.Background(), args)
		var te *ToolError
		if !errors.As(err, &te) {
			t.Fatalf("%s: want a ToolError, got %v", args, err)
		}
		if te.Type != "BAD_ARGUMENT" {
			t.Fatalf("%s: type = %q, want BAD_ARGUMENT", args, te.Type)
		}
		if !strings.Contains(te.Message, "required") {
			t.Fatalf("%s: message = %q", args, te.Message)
		}
	}
}

// A validation or load failure from the server must come back as a ToolError
// the model can read, carrying the reported message.
func TestCreateExtensionToolReportsCallbackError(t *testing.T) {
	e := newTestExecutor(t)
	e.CreateExtension = func(_ context.Context, id, description, source string) (string, error) {
		if id != "word-stats" || description != "counts words" || source != "src" {
			t.Fatalf("callback got id=%q description=%q source=%q", id, description, source)
		}
		return "", errors.New("SyntaxError: unexpected end of input")
	}
	_, err := e.createExtension(context.Background(),
		`{"id":" word-stats ","description":"counts words","source":" src "}`)
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("want a ToolError, got %v", err)
	}
	if te.Type != "EXTENSION_REJECTED" || !strings.Contains(te.Message, "SyntaxError") {
		t.Fatalf("the callback failure was not wrapped: %+v", te)
	}
}

// A successful install passes the callback's human-readable result through.
func TestCreateExtensionToolSucceeds(t *testing.T) {
	e := newTestExecutor(t)
	e.CreateExtension = func(_ context.Context, id, description, source string) (string, error) {
		return "installed extension " + id, nil
	}
	out, err := e.createExtension(context.Background(),
		`{"id":"word-stats","description":"counts words","source":"src"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "installed extension word-stats") {
		t.Fatalf("the message was not passed through: %s", out)
	}
}

// The source can be read from a workspace file instead of passed inline, which
// is what keeps a large extension from being truncated inside the tool call.
func TestCreateExtensionToolReadsSourcePath(t *testing.T) {
	e := newTestExecutor(t)
	dir := filepath.Join(e.Root, "extensions", "word-stats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const source = "export default () => ({ name: \"word-stats\" });\n"
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	e.CreateExtension = func(_ context.Context, id, description, got string) (string, error) {
		if id != "word-stats" {
			t.Fatalf("id = %q", id)
		}
		if got != strings.TrimSpace(source) {
			t.Fatalf("source = %q, want the file contents", got)
		}
		return "installed extension word-stats", nil
	}
	out, err := e.createExtension(context.Background(),
		`{"id":"word-stats","description":"counts words","path":"extensions/word-stats/index.js"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "installed extension word-stats") {
		t.Fatalf("the message was not passed through: %s", out)
	}
}

// A path that does not resolve to a readable workspace file must be refused as
// a BAD_ARGUMENT before the callback runs.
func TestCreateExtensionToolRejectsBadPath(t *testing.T) {
	e := newTestExecutor(t)
	e.CreateExtension = func(context.Context, string, string, string) (string, error) {
		t.Fatal("the callback must not run for a bad path")
		return "", nil
	}
	for _, args := range []string{
		`{"id":"x","path":"extensions/missing/index.js"}`,
		`{"id":"x","path":"../../etc/passwd"}`,
	} {
		_, err := e.createExtension(context.Background(), args)
		var te *ToolError
		if !errors.As(err, &te) {
			t.Fatalf("%s: want a ToolError, got %v", args, err)
		}
		if te.Type != "BAD_ARGUMENT" {
			t.Fatalf("%s: type = %q, want BAD_ARGUMENT", args, te.Type)
		}
	}
}
