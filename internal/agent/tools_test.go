package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestWriteReadEditFile(t *testing.T) {
	e := newTestExecutor(t)
	if _, err := e.Execute("write_file", `{"path":"sub/dir/a.txt","content":"hello world"}`); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(e.Root, "sub/dir/a.txt"))
	if err != nil || string(data) != "hello world" {
		t.Fatalf("file content = %q, err=%v", data, err)
	}

	res, err := e.Execute("read_file", `{"path":"sub/dir/a.txt"}`)
	if err != nil || !strings.Contains(res, "hello world") {
		t.Fatalf("read_file = %q, err=%v", res, err)
	}

	if _, err := e.Execute("edit_file", `{"path":"sub/dir/a.txt","old_string":"world","new_string":"v1"}`); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(e.Root, "sub/dir/a.txt"))
	if string(data) != "hello v1" {
		t.Fatalf("after edit content = %q", data)
	}

	if _, err := e.Execute("edit_file", `{"path":"sub/dir/a.txt","old_string":"nope","new_string":"x"}`); err == nil {
		t.Fatal("edit_file with missing old_string should fail")
	}
}

func TestReadFileTruncates(t *testing.T) {
	e := newTestExecutor(t)
	big := strings.Repeat("x", 60*1024)
	if _, err := e.Execute("write_file", `{"path":"big.txt","content":`+jsonString(big)+`}`); err != nil {
		t.Fatal(err)
	}
	res, err := e.Execute("read_file", `{"path":"big.txt"}`)
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
	res, err := e.Execute("run_command", `{"command":"echo out && echo err >&2 && exit 3"}`)
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
	res, err := e.Execute("run_command", `{"command":"sleep 30","timeout_seconds":1}`)
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

func TestListFilesSkipsAndCaps(t *testing.T) {
	e := newTestExecutor(t)
	os.MkdirAll(filepath.Join(e.Root, "node_modules/dep"), 0o755)
	os.MkdirAll(filepath.Join(e.Root, "src"), 0o755)
	os.WriteFile(filepath.Join(e.Root, "node_modules/dep/x.js"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(e.Root, "src/a.ts"), []byte("x"), 0o644)
	res, err := e.Execute("list_files", `{}`)
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
