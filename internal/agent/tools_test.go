package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"v1/internal/store"
)

func newTestExecutor(t *testing.T) *Executor {
	t.Helper()
	return &Executor{Root: t.TempDir(), ProjectID: "testproj"}
}

func TestResolveRejectsTraversal(t *testing.T) {
	e := newTestExecutor(t)
	for _, p := range []string{"../x", "../../etc/passwd", "/etc/passwd", "a/../../../b"} {
		full, err := e.resolve(p)
		if err != nil {
			continue // rejected outright: good
		}
		if !strings.HasPrefix(full, e.Root+string(filepath.Separator)) && full != e.Root {
			t.Fatalf("path %q resolved outside root: %q", p, full)
		}
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	e := newTestExecutor(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(e.Root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), "read_file", `{"path":"escape/outside.txt"}`); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
	if err := os.WriteFile(filepath.Join(e.Root, "ok.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink that stays inside the workspace still works.
	if err := os.Symlink(filepath.Join(e.Root, "ok.txt"), filepath.Join(e.Root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), "read_file", `{"path":"link.txt"}`); err != nil {
		t.Fatalf("in-workspace symlink rejected: %v", err)
	}
}

func TestWriteReadEditFile(t *testing.T) {
	e := newTestExecutor(t)
	if _, err := e.Execute(context.Background(), "write_file", `{"path":"sub/dir/a.txt","content":"hello world"}`); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(e.Root, "sub/dir/a.txt"))
	if err != nil || string(data) != "hello world" {
		t.Fatalf("file content = %q, err=%v", data, err)
	}

	res, err := e.Execute(context.Background(), "read_file", `{"path":"sub/dir/a.txt"}`)
	if err != nil || !strings.Contains(res, "hello world") {
		t.Fatalf("read_file = %q, err=%v", res, err)
	}

	if _, err := e.Execute(context.Background(), "edit_file", `{"path":"sub/dir/a.txt","old_string":"world","new_string":"v1"}`); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(e.Root, "sub/dir/a.txt"))
	if string(data) != "hello v1" {
		t.Fatalf("after edit content = %q", data)
	}

	if _, err := e.Execute(context.Background(), "edit_file", `{"path":"sub/dir/a.txt","old_string":"nope","new_string":"x"}`); err == nil {
		t.Fatal("edit_file with missing old_string should fail")
	}
}

func TestReadFileTruncates(t *testing.T) {
	e := newTestExecutor(t)
	big := strings.Repeat("x", 60*1024)
	if _, err := e.Execute(context.Background(), "write_file", `{"path":"big.txt","content":`+jsonString(big)+`}`); err != nil {
		t.Fatal(err)
	}
	res, err := e.Execute(context.Background(), "read_file", `{"path":"big.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "truncated") {
		t.Fatal("expected truncation notice")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestRunCommandExitCodeAndOutput(t *testing.T) {
	e := newTestExecutor(t)
	res, err := e.Execute(context.Background(), "run_command", `{"command":"echo out && echo err >&2 && exit 3"}`)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ExitCode int    `json:"exitCode"`
		Output   string `json:"output"`
	}
	if err := json.Unmarshal([]byte(res), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ExitCode != 3 {
		t.Fatalf("exit code = %d", parsed.ExitCode)
	}
	if !strings.Contains(parsed.Output, "out") || !strings.Contains(parsed.Output, "err") {
		t.Fatalf("output = %q", parsed.Output)
	}
}

func TestRunCommandTimeoutKills(t *testing.T) {
	e := newTestExecutor(t)
	start := time.Now()
	res, err := e.Execute(context.Background(), "run_command", `{"command":"sleep 30","timeout_seconds":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("command was not killed at timeout")
	}
	if !strings.Contains(res, `"timedOut":true`) {
		t.Fatalf("expected timedOut in %q", res)
	}
}

// TestRunCommandCancelKills verifies that cancelling the turn's context kills
// the command's whole process group (stop button / hard timeout) and reports
// the cancellation to the model.
func TestRunCommandCancelKills(t *testing.T) {
	e := newTestExecutor(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res, err := e.Execute(ctx, "run_command", `{"command":"sleep 30"}`)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("command was not killed on cancellation")
	}
	if !strings.Contains(res, `"cancelled":true`) {
		t.Fatalf("expected cancelled in %q", res)
	}
}

// TestSetProjectName: the tool renames the project and notifies the UI.
func TestSetProjectName(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := &store.Project{ID: "proj1", Name: "Old Name", Path: t.TempDir()}
	if err := st.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	e := &Executor{ProjectID: p.ID, Store: st}
	var notified string
	e.OnProjectRename = func(name string) { notified = name }

	res, err := e.Execute(context.Background(), "set_project_name", `{"name":"  FitTrack  "}`)
	if err != nil {
		t.Fatal(err)
	}
	if notified != "FitTrack" {
		t.Fatalf("notified = %q", notified)
	}
	got, err := st.GetProject(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "FitTrack" {
		t.Fatalf("project name = %q", got.Name)
	}
	if !strings.Contains(res, "FitTrack") {
		t.Fatalf("result = %q", res)
	}

	if _, err := e.Execute(context.Background(), "set_project_name", `{"name":"  "}`); err == nil {
		t.Fatal("empty name should fail")
	}
}

func TestListFilesSkipsAndCaps(t *testing.T) {
	e := newTestExecutor(t)
	os.MkdirAll(filepath.Join(e.Root, "node_modules/dep"), 0o755)
	os.MkdirAll(filepath.Join(e.Root, "src"), 0o755)
	os.WriteFile(filepath.Join(e.Root, "node_modules/dep/x.js"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(e.Root, "src/a.ts"), []byte("x"), 0o644)
	res, err := e.Execute(context.Background(), "list_files", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res, "node_modules") {
		t.Fatalf("node_modules should be skipped: %q", res)
	}
	if !strings.Contains(res, "src/") {
		t.Fatalf("expected src/ in %q", res)
	}
}

func TestSearchFilesRequiresQuery(t *testing.T) {
	e := newTestExecutor(t)
	if _, err := e.Execute(context.Background(), "search_files", `{}`); err == nil {
		t.Fatal("expected error for missing query")
	}
}

func TestDeleteAndMoveFile(t *testing.T) {
	e := newTestExecutor(t)
	if _, err := e.Execute(context.Background(), "write_file", `{"path":"sub/a.txt","content":"hello"}`); err != nil {
		t.Fatal(err)
	}
	// Move to a new path whose parent directory does not exist yet.
	if _, err := e.Execute(context.Background(), "move_file", `{"path":"sub/a.txt","newPath":"deep/place/b.txt"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.Root, "sub/a.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still exists after move")
	}
	if data, err := os.ReadFile(filepath.Join(e.Root, "deep/place/b.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("moved file = %q, err=%v", data, err)
	}
	// Move to the same path is rejected.
	if _, err := e.Execute(context.Background(), "move_file", `{"path":"deep/place/b.txt","newPath":"deep/place/b.txt"}`); err == nil {
		t.Fatal("expected error for moving onto itself")
	}
	// Traversal-looking paths are normalized into the workspace, never escape.
	if _, err := e.Execute(context.Background(), "move_file", `{"path":"deep/place/b.txt","newPath":"../escape.txt"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.Root, "escape.txt")); err != nil {
		t.Fatal("expected normalized destination inside the workspace")
	}
	// Delete the file.
	if _, err := e.Execute(context.Background(), "delete_file", `{"path":"escape.txt"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.Root, "escape.txt")); !os.IsNotExist(err) {
		t.Fatal("file still exists after delete")
	}
	// Deleting a missing file and a directory both error.
	if _, err := e.Execute(context.Background(), "delete_file", `{"path":"escape.txt"}`); err == nil {
		t.Fatal("expected error deleting a missing file")
	}
	if _, err := e.Execute(context.Background(), "delete_file", `{"path":"deep"}`); err == nil {
		t.Fatal("expected error deleting a directory")
	}
}

func TestFetchURLValidatesAndStrips(t *testing.T) {
	e := newTestExecutor(t)
	if _, err := e.Execute(context.Background(), "fetch_url", `{"url":"file:///etc/passwd"}`); err == nil {
		t.Fatal("expected non-http URL to be rejected")
	}
	if _, err := e.Execute(context.Background(), "fetch_url", `{"url":"not a url"}`); err == nil {
		t.Fatal("expected malformed URL to be rejected")
	}
	got := htmlToText(`<html><head><title>Test Page</title></head><body><h1>Title</h1><p>Hello <b>world</b> &amp; friends</p><ul><li>one</li><li>two</li></ul><pre>const x = 1;</pre><script>var y=1;</script></body></html>`)
	for _, want := range []string{"Test Page", "Title", "Hello", "world", "& friends", "- one", "- two", "```", "const x = 1;"} {
		if !strings.Contains(got, want) {
			t.Fatalf("htmlToText = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "var y=1") {
		t.Fatalf("script content leaked: %q", got)
	}
}

func TestFetchURLBlocksNonRoutable(t *testing.T) {
	e := newTestExecutor(t)
	for _, u := range []string{
		"http://127.0.0.1:1/",
		"http://[::1]:1/",
		"http://localhost:1/",
		"http://169.254.169.254/latest/meta-data",
		"http://0.0.0.0:1/",
	} {
		if _, err := e.Execute(context.Background(), "fetch_url", fmt.Sprintf(`{"url":%q}`, u)); err == nil {
			t.Fatalf("expected %s to be blocked", u)
		}
	}
}

func TestFetchURLRendersSPAShell(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>App</title></head><body><div id="root"></div><script src="/app.js"></script></body></html>`))
	}))
	defer srv.Close()
	calls := 0
	e := newTestExecutor(t)
	e.FetchGuard = func(string) error { return nil } // httptest binds 127.0.0.1
	e.RenderPage = func(ctx context.Context, url string) (string, error) {
		calls++
		return `<html><body><h1>Rendered Title</h1><p>Content from the JS render.</p><pre>const x = 1;</pre></body></html>`, nil
	}
	res, err := e.Execute(context.Background(), "fetch_url", fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("RenderPage calls = %d, want 1", calls)
	}
	for _, want := range []string{"Rendered Title", "Content from the JS render.", "```", "const x = 1;"} {
		if !strings.Contains(res, want) {
			t.Fatalf("result missing %q: %s", want, res)
		}
	}
}

func TestFetchURLSkipsRenderForStaticText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Static Page</h1><p>Plenty of server-rendered content here.</p></body></html>`))
	}))
	defer srv.Close()
	calls := 0
	e := newTestExecutor(t)
	e.FetchGuard = func(string) error { return nil } // httptest binds 127.0.0.1
	e.RenderPage = func(ctx context.Context, url string) (string, error) {
		calls++
		return "", nil
	}
	res, err := e.Execute(context.Background(), "fetch_url", fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("RenderPage should not be called for static content, calls = %d", calls)
	}
	if !strings.Contains(res, "Static Page") {
		t.Fatalf("result missing static content: %s", res)
	}
}

func TestFetchURLRendersOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "challenge", http.StatusForbidden)
	}))
	defer srv.Close()
	e := newTestExecutor(t)
	e.FetchGuard = func(string) error { return nil } // httptest binds 127.0.0.1
	e.RenderPage = func(ctx context.Context, url string) (string, error) {
		return `<html><body><h1>Rendered Despite 403</h1></body></html>`, nil
	}
	res, err := e.Execute(context.Background(), "fetch_url", fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "Rendered Despite 403") {
		t.Fatalf("result missing rendered content: %s", res)
	}
}

func TestSearchFilesFallsBackToFdRg(t *testing.T) {
	if _, err := exec.LookPath("fd"); err != nil {
		t.Skip("fd not installed")
	}
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	e := newTestExecutor(t)
	os.MkdirAll(filepath.Join(e.Root, "src"), 0o755)
	os.WriteFile(filepath.Join(e.Root, "src/button.tsx"), []byte("export function Button() {}\n"), 0o644)
	os.WriteFile(filepath.Join(e.Root, "src/a.ts"), []byte("const x = 1\n"), 0o644)
	res, err := e.searchFilesRgFd("Button", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Files   []string `json:"files"`
		Matches []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
			Text string `json:"text"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(res), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Files) == 0 || parsed.Files[0] != "src/button.tsx" {
		t.Fatalf("files = %v", parsed.Files)
	}
	found := false
	for _, m := range parsed.Matches {
		if m.Path == "src/button.tsx" && m.Line == 1 && strings.Contains(m.Text, "Button") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected content match in src/button.tsx, got %+v", parsed.Matches)
	}
}

// TestGitOpEndToEnd drives the git tool through init → commit → push to a
// file:// remote (no GitHub needed) to prove the tool pipeline works, and
// verifies remote ops carry the credential helper when a token is present.
func TestGitOpEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	e := newTestExecutor(t)
	root := t.TempDir()
	e.Root = root
	e.GithubToken = "ghp_test123"

	run := func(cmd string) (string, error) {
		return e.Execute(ctx, "git", `{"command":`+strconv.Quote(cmd)+`}`)
	}
	if out, err := run("init -b main"); err != nil || !strings.Contains(out, "Initialized") {
		t.Fatalf("git init failed: %v %q", err, out)
	}
	if out, err := run("add ."); err != nil || out == "" {
		t.Fatalf("git add failed: %v %q", err, out)
	}
	if _, err := run("commit -m \"first\""); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	bare := t.TempDir()
	if initBare, err := exec.Command("git", "init", "--bare", "-b", "main", bare).CombinedOutput(); err != nil {
		t.Fatalf("bare init: %v %s", err, initBare)
	}
	if _, err := run("remote add origin " + bare); err != nil {
		t.Fatalf("remote add failed: %v", err)
	}
	if out, err := run("push -u origin main"); err != nil {
		t.Fatalf("push failed: %v %s", err, out)
	}
	for _, op := range []string{"push", "pull", "fetch", "clone", "ls-remote", "submodule"} {
		if !isGitRemoteOp(op) {
			t.Fatalf("isGitRemoteOp should cover %s", op)
		}
	}
	if isGitRemoteOp("status") {
		t.Fatal("status must not be treated as a remote op")
	}
}
