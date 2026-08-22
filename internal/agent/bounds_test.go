package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// parseToolResult parses a tool result so tests can assert on the contract.
func parseToolResult(t *testing.T, res string) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("result %q is not JSON: %v", res, err)
	}
	return out
}

// executeContract runs a tool the way the agent loop does: a returned error
// is converted into the structured {success:false, error:{…}} contract.
func executeContract(t *testing.T, e *Executor, ctx context.Context, name, argsJSON string) map[string]any {
	t.Helper()
	res, err := e.Execute(ctx, name, argsJSON)
	if err != nil {
		res = formatToolError(err)
	}
	return parseToolResult(t, res)
}

// plainDial lets fetch_url tests reach httptest's loopback servers while the
// hardened (validating) dialer stays the production default.
func plainDial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// toolErrorOf extracts the error envelope {type,message,recoverable,suggestion}.
func toolErrorOf(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	if res["success"] != false {
		t.Fatalf("expected success=false, got %v", res["success"])
	}
	env, _ := res["error"].(map[string]any)
	if env == nil {
		t.Fatalf("missing error envelope in %v", res)
	}
	return env
}

// TestToolErrorContract verifies a tool failure surfaces as the structured
// {success:false, error:{type,message,recoverable,suggestion}} contract.
func TestToolErrorContract(t *testing.T) {
	e := newTestExecutor(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.Root, "evil")); err != nil {
		t.Fatal(err)
	}
	res := executeContract(t, e, context.Background(), "write_file", `{"path":"evil/secret.txt","content":"x"}`)
	env := toolErrorOf(t, res)
	if env["type"] != "PATH_ESCAPE" {
		t.Fatalf("type = %v, want PATH_ESCAPE", env["type"])
	}
	if env["recoverable"] != true {
		t.Fatalf("recoverable = %v, want true", env["recoverable"])
	}
	if env["suggestion"] == "" {
		t.Fatal("missing suggestion")
	}
	// The file outside the workspace must be untouched.
	if got, _ := os.ReadFile(target); string(got) != "secret" {
		t.Fatalf("file outside workspace was modified: %q", got)
	}
}

// TestFetchURLBlockedLoopback verifies the default guard refuses loopback
// hosts before any request is made.
func TestFetchURLBlockedLoopback(t *testing.T) {
	e := newTestExecutor(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached a loopback server; it should have been blocked")
	}))
	defer srv.Close()
	res := executeContract(t, e, context.Background(), "fetch_url", `{"url":"`+srv.URL+`"}`)
	env := toolErrorOf(t, res)
	if env["type"] != "BLOCKED_HOST" {
		t.Fatalf("type = %v, want BLOCKED_HOST", env["type"])
	}
}

// TestFetchURLContentTypeAllowList verifies non-text responses are refused.
func TestFetchURLContentTypeAllowList(t *testing.T) {
	e := newTestExecutor(t)
	e.FetchGuard = func(rawURL string) error { return nil } // allow loopback for the test
	e.DialContext = plainDial
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4"))
	}))
	defer srv.Close()
	res := executeContract(t, e, context.Background(), "fetch_url", `{"url":"`+srv.URL+`"}`)
	env := toolErrorOf(t, res)
	if env["type"] != "NOT_ALLOWED" {
		t.Fatalf("type = %v, want NOT_ALLOWED", env["type"])
	}
}

// TestFetchURLAllowsText verifies text/html passes the allow-list.
func TestFetchURLAllowsText(t *testing.T) {
	e := newTestExecutor(t)
	e.FetchGuard = func(rawURL string) error { return nil }
	e.DialContext = plainDial
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><h1>Hello docs</h1></body></html>"))
	}))
	defer srv.Close()
	res := executeContract(t, e, context.Background(), "fetch_url", `{"url":"`+srv.URL+`"}`)
	if res["ok"] != true {
		t.Fatalf("expected ok, got %v", res)
	}
	if !strings.Contains(res["text"].(string), "Hello docs") {
		t.Fatalf("text = %q, want the page title", res["text"])
	}
}

// TestBannedIP covers the blocklist helper directly.
func TestBannedIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "::1", "169.254.10.10", "fe80::1", "0.0.0.0", "224.0.0.1"}
	for _, s := range blocked {
		if !bannedIP(net.ParseIP(s)) {
			t.Fatalf("%s should be banned", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "142.250.72.100"}
	for _, s := range allowed {
		if bannedIP(net.ParseIP(s)) {
			t.Fatalf("%s should be allowed", s)
		}
	}
}

// TestAllowedContentType covers the fetch_url content-type allow-list.
func TestAllowedContentType(t *testing.T) {
	ok := []string{"text/html", "text/plain; charset=utf-8", "application/json", "application/xml", ""}
	for _, ct := range ok {
		if !allowedContentType(ct) {
			t.Fatalf("%q should pass the allow-list", ct)
		}
	}
	bad := []string{"application/pdf", "image/png", "application/octet-stream", "application/zip"}
	for _, ct := range bad {
		if allowedContentType(ct) {
			t.Fatalf("%q should be refused", ct)
		}
	}
}

// TestWriteFileCaps verifies write_file refuses oversized and binary content.
func TestWriteFileCaps(t *testing.T) {
	e := newTestExecutor(t)
	res := executeContract(t, e, context.Background(), "write_file", `{"path":"big.txt","content":"`+strings.Repeat("a", 600*1024)+`"}`)
	env := toolErrorOf(t, res)
	if env["type"] != "TOO_LARGE" {
		t.Fatalf("type = %v, want TOO_LARGE", env["type"])
	}
	res = executeContract(t, e, context.Background(), "write_file", `{"path":"bin.dat","content":"abc\u0000def"}`)
	env = toolErrorOf(t, res)
	if env["type"] != "BINARY_REJECTED" {
		t.Fatalf("type = %v, want BINARY_REJECTED", env["type"])
	}
	if _, statErr := os.Stat(filepath.Join(e.Root, "bin.dat")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("binary file should not have been created")
	}
}

// TestSudoRefused verifies privilege escalation commands are refused at the
// tool boundary, both foreground and background.
func TestSudoRefused(t *testing.T) {
	e := newTestExecutor(t)
	for _, tool := range []string{"run_command", "run_command_background"} {
		res := executeContract(t, e, context.Background(), tool, `{"command":"sudo whoami"}`)
		env := toolErrorOf(t, res)
		if env["type"] != "PRIVILEGE_ESCALATION" {
			t.Fatalf("%s: type = %v, want PRIVILEGE_ESCALATION", tool, env["type"])
		}
	}
}

// TestAskUserRepeatGuard verifies asking the same question twice reuses the
// first answer instead of blocking on the user again.
func TestAskUserRepeatGuard(t *testing.T) {
	e := newTestExecutor(t)
	var calls int32
	e.OnAsk = func(ctx context.Context, qs []AskQuestion) ([]AskAnswer, error) {
		atomic.AddInt32(&calls, 1)
		return []AskAnswer{{Question: qs[0].Question, Answer: "yes"}}, nil
	}
	ask := `{"question":"Should I use Tailwind?"}`
	res := executeContract(t, e, context.Background(), "ask_user", ask)
	if res["answer"] != "yes" {
		t.Fatalf("answer = %v", res["answer"])
	}
	res = executeContract(t, e, context.Background(), "ask_user", ask)
	if res["answer"] != "yes" {
		t.Fatalf("cached answer = %v", res["answer"])
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("OnAsk called %d times, want 1", n)
	}
}

// TestAskUserTimeout verifies ask_user gives up after AskTimeout and tells
// the model to proceed with its best judgment.
func TestAskUserTimeout(t *testing.T) {
	e := newTestExecutor(t)
	e.AskTimeout = 150 * time.Millisecond
	e.OnAsk = func(ctx context.Context, qs []AskQuestion) ([]AskAnswer, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	res := executeContract(t, e, context.Background(), "ask_user", `{"question":"Pick a color?"}`)
	env := toolErrorOf(t, res)
	if env["type"] != "ASK_TIMEOUT" {
		t.Fatalf("type = %v, want ASK_TIMEOUT", env["type"])
	}
	if env["recoverable"] != false {
		t.Fatalf("recoverable = %v, want false", env["recoverable"])
	}
}

// TestContainerResourceCaps verifies `run` gets CPU/memory caps injected and
// explicit flags are left alone.
func TestContainerResourceCaps(t *testing.T) {
	got := containerResourceCaps([]string{"run", "--rm", "node:22", "sh"})
	if len(got) != 8 || got[1] != "--cpus" || got[2] != "1" || got[3] != "--memory" || got[4] != "1g" {
		t.Fatalf("capped = %v", got)
	}
	explicit := containerResourceCaps([]string{"run", "--memory", "8g", "postgres"})
	if strings.Join(explicit, " ") != "run --memory 8g postgres" {
		t.Fatalf("explicit flags were overridden: %v", explicit)
	}
	other := containerResourceCaps([]string{"images"})
	if strings.Join(other, " ") != "images" {
		t.Fatalf("non-run subcommand changed: %v", other)
	}
}
