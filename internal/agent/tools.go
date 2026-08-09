package agent

import (
	"context"
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

	"v1/internal/mcp"
	"v1/internal/store"
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
	Store          *store.Store
	OnTodos        func([]store.Todo)
	OnMemories     func([]store.Memory)
	OnFileChange   func()
	MCP            *mcp.Manager // optional: namespaced mcp_<server>_<tool> tools
	Perm           Resolver     // optional: gates tool calls via allow/deny/ask
	// OnAsk asks the user a question and waits for their answer (the ask_user
	// tool); nil when the turn cannot prompt.
	OnAsk func(ctx context.Context, question string, options []string) (string, error)
	// Screenshot captures the app preview as a PNG (nil when the model cannot
	// read images). PendingImage carries the PNG from a screenshot_app call to
	// the agent loop, which attaches it to the conversation.
	Screenshot   func(ctx context.Context, path string) ([]byte, error)
	PendingImage []byte
}

// Execute runs one tool call and returns the result string fed back to the LLM.
func (e *Executor) Execute(ctx context.Context, name, argsJSON string) (string, error) {
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
		return e.runCommand(ctx, argsJSON)
	case "restart_preview":
		return e.restartPreview()
	case "screenshot_app":
		return e.screenshotApp(ctx, argsJSON)
	case "set_todos":
		return e.setTodos(argsJSON)
	case "remember":
		return e.remember(argsJSON)
	case "forget":
		return e.forget(argsJSON)
	case "ask_user":
		return e.askUser(ctx, argsJSON)
	default:
		if strings.HasPrefix(name, "mcp_") {
			return e.mcpCall(ctx, name, argsJSON)
		}
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// screenshotApp captures the current app preview; the image travels to the
// agent loop via PendingImage and reaches the model as an injected image
// message (tool results are text-only on many OpenAI-compatible APIs).
func (e *Executor) screenshotApp(ctx context.Context, argsJSON string) (string, error) {
	if e.Screenshot == nil {
		return "", fmt.Errorf("screenshots are not available with this model")
	}
	var args struct {
		Path string `json:"path"`
	}
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	png, err := e.Screenshot(ctx, args.Path)
	if err != nil {
		return "", err
	}
	e.PendingImage = png
	return toolResult(map[string]any{
		"ok":    true,
		"path":  args.Path,
		"bytes": len(png),
		"note":  "the screenshot image follows in the next message",
	}), nil
}

// mcpCall routes a namespaced MCP tool (mcp_<server>_<tool>) to the matching
// server, gated by the permission policy.
func (e *Executor) mcpCall(ctx context.Context, name, argsJSON string) (string, error) {
	rest := strings.TrimPrefix(name, "mcp_")
	idx := strings.Index(rest, "_")
	if idx <= 0 || idx == len(rest)-1 {
		return "", fmt.Errorf("malformed MCP tool name %q", name)
	}
	serverID, toolName := rest[:idx], rest[idx+1:]
	detail := toolName
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if e.Perm != nil {
		ok, err := e.Perm.Request(ctx, "mcp."+serverID+"."+toolName, detail)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("tool %q was not allowed", name)
		}
	}
	if e.MCP == nil {
		return "", fmt.Errorf("MCP support is unavailable")
	}
	result, err := e.MCP.CallTool(ctx, serverID, toolName, args)
	if err != nil {
		return "", err
	}
	return result, nil
}

// remember saves a project-scoped memory; forget deletes one by id. Entries
// are short, deduped, and capped so the system prompt's memories section
// stays small (it is re-sent with every request of every round).
func (e *Executor) remember(argsJSON string) (string, error) {
	var args struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	args.Content = strings.TrimSpace(args.Content)
	if args.Content == "" {
		return "", fmt.Errorf("content is required")
	}
	if e.Store == nil {
		return "", fmt.Errorf("memory store unavailable")
	}
	if len(args.Content) > 300 {
		return "", fmt.Errorf("memory entries must be 300 characters or fewer — save a shorter fact")
	}
	mems, err := e.Store.ListMemories(e.ProjectID)
	if err != nil {
		return "", err
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(args.Content), " "))
	for _, m := range mems {
		if strings.ToLower(strings.Join(strings.Fields(m.Content), " ")) == normalized {
			return toolResult(map[string]any{"ok": true, "id": m.ID, "note": "already remembered"}), nil
		}
	}
	if len(mems) >= 200 {
		return "", fmt.Errorf("memory is full (200 entries) — use the forget tool to delete one first")
	}
	id, err := e.Store.AddMemory(e.ProjectID, args.Content)
	if err != nil {
		return "", err
	}
	e.emitMemories()
	return toolResult(map[string]any{"ok": true, "id": id}), nil
}

func (e *Executor) forget(argsJSON string) (string, error) {
	var args struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.ID <= 0 {
		return "", fmt.Errorf("id is required")
	}
	if e.Store == nil {
		return "", fmt.Errorf("memory store unavailable")
	}
	if err := e.Store.DeleteMemory(e.ProjectID, args.ID); err != nil {
		return "", err
	}
	e.emitMemories()
	return toolResult(map[string]any{"ok": true}), nil
}

// emitMemories pushes the refreshed memory list to the UI after a change.
func (e *Executor) emitMemories() {
	if e.OnMemories == nil || e.Store == nil {
		return
	}
	if mems, err := e.Store.ListMemories(e.ProjectID); err == nil {
		e.OnMemories(mems)
	}
}

// askUser blocks the turn until the user answers through the ask endpoint.
func (e *Executor) askUser(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Question) == "" {
		return "", fmt.Errorf("question is required")
	}
	if len(args.Options) > 4 {
		args.Options = args.Options[:4]
	}
	if e.OnAsk == nil {
		return "", fmt.Errorf("asking the user is unavailable in this context")
	}
	answer, err := e.OnAsk(ctx, args.Question, args.Options)
	if err != nil {
		return "", err
	}
	return toolResult(map[string]any{"answer": answer}), nil
}

// setTodos replaces the project's task list. The agent is expected to pass the
// full desired list (not a delta) so items keep their order and done state.
func (e *Executor) setTodos(argsJSON string) (string, error) {
	var args struct {
		Todos []store.Todo `json:"todos"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if e.Store == nil {
		return "", fmt.Errorf("todo store unavailable")
	}
	if args.Todos == nil {
		args.Todos = []store.Todo{}
	}
	if err := e.Store.SetTodos(e.ProjectID, args.Todos); err != nil {
		return "", err
	}
	if e.OnTodos != nil {
		e.OnTodos(args.Todos)
	}
	return toolResult(map[string]any{"ok": true, "count": len(args.Todos)}), nil
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
	if e.OnFileChange != nil {
		e.OnFileChange()
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
	if e.OnFileChange != nil {
		e.OnFileChange()
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

func (e *Executor) runCommand(ctx context.Context, argsJSON string) (string, error) {
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
	if e.Perm != nil {
		ok, err := e.Perm.Request(ctx, "run_command", args.Command)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("command was not allowed")
		}
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
