package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// PreviewStarter starts (or restarts) a project's preview and returns its URL.
type PreviewStarter interface {
	Start(projectID, dir, previewCommand string) (string, error)
}

// Executor executes agent tool calls against a project workspace.
type Executor struct {
	Root           string
	ProjectID      string
	PreviewCommand string
	Previews       PreviewStarter
}

// Execute runs one tool call and returns the result string fed back to the LLM.
func (e *Executor) Execute(name, argsJSON string) (string, error) {
	switch name {
	case "list_files":
		return e.listFiles(argsJSON)
	case "read_file":
		return e.readFile(argsJSON)
	case "write_file":
		return e.writeFile(argsJSON)
	case "edit_file":
		return e.editFile(argsJSON)
	case "run_command":
		return e.runCommand(argsJSON)
	case "restart_preview":
		return e.restartPreview()
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// resolve maps a workspace-relative path to an absolute path, rejecting
// anything that escapes the workspace root.
func (e *Executor) resolve(rel string) (string, error) {
	root := e.Root
	if rel == "" || rel == "." {
		return root, nil
	}
	clean := filepath.Clean("/" + rel)
	full := filepath.Join(root, clean)
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace", rel)
	}
	return full, nil
}

var skippedDirs = map[string]bool{"node_modules": true, ".git": true, "dist": true}

func (e *Executor) listFiles(argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	root, err := e.resolve(args.Path)
	if err != nil {
		return "", err
	}
	const maxDepth = 3
	const maxEntries = 300
	var entries []string
	truncated := false
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(e.Root, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if d.IsDir() {
			if skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			if depth >= maxDepth {
				entries = append(entries, filepath.ToSlash(rel)+"/")
				return filepath.SkipDir
			}
			entries = append(entries, filepath.ToSlash(rel)+"/")
			return nil
		}
		entries = append(entries, filepath.ToSlash(rel))
		if len(entries) >= maxEntries {
			truncated = true
			return fmt.Errorf("cap reached")
		}
		return nil
	})
	if walkErr != nil && walkErr.Error() != "cap reached" {
		return "", walkErr
	}
	return toolResult(map[string]any{
		"files":     entries,
		"truncated": truncated,
	}), nil
}

func (e *Executor) readFile(argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	full, err := e.resolve(args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", args.Path)
	}
	const cap = 50 * 1024
	f, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, cap+1))
	if err != nil {
		return "", err
	}
	if looksBinary(data) {
		return "", fmt.Errorf("%s is a binary file", args.Path)
	}
	content := string(data)
	if len(data) > cap {
		content = string(data[:cap]) + fmt.Sprintf("\n...[truncated, file is %d bytes]", info.Size())
	}
	return toolResult(map[string]any{"content": content, "size": info.Size()}), nil
}

func (e *Executor) writeFile(argsJSON string) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	full, err := e.resolve(args.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(args.Content), 0o644); err != nil {
		return "", err
	}
	return toolResult(map[string]any{"ok": true, "path": args.Path, "bytes": len(args.Content)}), nil
}

func (e *Executor) editFile(argsJSON string) (string, error) {
	var args struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Path == "" || args.OldString == "" {
		return "", fmt.Errorf("path and old_string are required")
	}
	full, err := e.resolve(args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	content := string(data)
	n := strings.Count(content, args.OldString)
	if n == 0 {
		return "", fmt.Errorf("old_string not found in %s", args.Path)
	}
	content = strings.Replace(content, args.OldString, args.NewString, 1)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", err
	}
	res := map[string]any{"ok": true, "path": args.Path}
	if n > 1 {
		res["note"] = fmt.Sprintf("old_string occurred %d times; only the first occurrence was replaced", n)
	}
	return toolResult(res), nil
}

// limitWriter caps the amount of captured output.
type limitWriter struct {
	mu      sync.Mutex
	buf     []byte
	max     int
	dropped int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.max - len(w.buf)
	if remaining > 0 {
		if len(p) > remaining {
			w.buf = append(w.buf, p[:remaining]...)
			w.dropped += len(p) - remaining
		} else {
			w.buf = append(w.buf, p...)
		}
	} else {
		w.dropped += len(p)
	}
	return len(p), nil
}

func (w *limitWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

func (e *Executor) runCommand(argsJSON string) (string, error) {
	var args struct {
		Command        string   `json:"command"`
		TimeoutSeconds *float64 `json:"timeout_seconds"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Command == "" {
		return "", fmt.Errorf("command is required")
	}
	timeout := 120 * time.Second
	if args.TimeoutSeconds != nil && *args.TimeoutSeconds > 0 {
		timeout = time.Duration(*args.TimeoutSeconds * float64(time.Second))
		if timeout > 600*time.Second {
			timeout = 600 * time.Second
		}
	}

	cmd := exec.Command("sh", "-c", args.Command)
	cmd.Dir = e.Root
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &limitWriter{max: 512 * 1024}
	cmd.Stdout = out
	cmd.Stderr = out

	if err := cmd.Start(); err != nil {
		return "", err
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	timedOut := false
	var runErr error
	select {
	case runErr = <-waitCh:
	case <-time.After(timeout):
		timedOut = true
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		runErr = <-waitCh
	}

	exitCode := 0
	if runErr != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}

	output := trimHeadTail(out.String(), 5*1024)
	res := map[string]any{
		"exitCode": exitCode,
		"output":   output,
	}
	if timedOut {
		res["timedOut"] = true
		note := fmt.Sprintf("command exceeded timeout of %s and was killed", timeout)
		if strings.Contains(args.Command, "dev") || strings.Contains(args.Command, "serve") {
			note = fmt.Sprintf("command exceeded timeout of %s; it looked like a long-running dev server and was killed", timeout)
		}
		res["note"] = note
	}
	return toolResult(res), nil
}

func (e *Executor) restartPreview() (string, error) {
	if e.Previews == nil {
		return "", fmt.Errorf("preview manager unavailable")
	}
	url, err := e.Previews.Start(e.ProjectID, e.Root, e.PreviewCommand)
	if err != nil {
		return "", err
	}
	return toolResult(map[string]any{"ok": true, "url": url}), nil
}
