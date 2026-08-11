package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/html"

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
	// RenderPage renders a URL in headless Chrome and returns the rendered
	// HTML — the fetch_url tool falls back to it for JS-rendered pages whose
	// static response carries no readable text.
	RenderPage func(ctx context.Context, url string) (string, error)
	// FetchGuard validates URLs before fetch_url touches them. When nil, the
	// built-in guard applies: http/https only, loopback and link-local
	// addresses rejected (the fetch runs server-side, so a project must not
	// reach the v1 instance or other previews through it).
	FetchGuard func(rawURL string) error
}

// Execute runs one tool call and returns the result string fed back to the LLM.
func (e *Executor) Execute(ctx context.Context, name, argsJSON string) (string, error) {
	switch name {
	case "list_files":
		return e.listFiles(argsJSON)
	case "search_files":
		return e.searchFiles(ctx, argsJSON)
	case "read_file":
		return e.readFile(argsJSON)
	case "write_file":
		return e.writeFile(argsJSON)
	case "edit_file":
		return e.editFile(argsJSON)
	case "delete_file":
		return e.deleteFile(argsJSON)
	case "move_file":
		return e.moveFile(argsJSON)
	case "fetch_url":
		return e.fetchURL(ctx, argsJSON)
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
// anything that escapes the workspace root — lexically or through symlinks.
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
	// The lexical check misses symlinks: a link inside the workspace can
	// point anywhere. Walk up to the deepest existing ancestor, resolve it,
	// and confirm the target still sits under the workspace root.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolvedRoot = root
	}
	anc := full
	for {
		if _, err := os.Lstat(anc); err == nil {
			break
		}
		parent := filepath.Dir(anc)
		if parent == anc {
			break
		}
		anc = parent
	}
	resolved, err := filepath.EvalSymlinks(anc)
	if err != nil {
		resolved = anc
	}
	resolved = filepath.Join(resolved, strings.TrimPrefix(full, anc))
	if resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace", rel)
	}
	// Return the original spelling so relative paths and displays stay stable.
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

// searchFiles searches the workspace for files: semantic search via `semble`
// when the binary is available, otherwise filename (fd) and content (rg)
// matching. Paths in the result are workspace-relative.
func (e *Executor) searchFiles(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Query string `json:"query"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("query is required")
	}
	root, err := e.resolve(args.Path)
	if err != nil {
		return "", err
	}
	if out, ok := runSemble(ctx, args.Query, root); ok {
		return out, nil
	}
	return e.searchFilesRgFd(args.Query, root)
}

// runSemble runs `semble search` in hybrid mode and returns its markdown
// output (ranked paths with line ranges, snippets and scores) verbatim.
func runSemble(ctx context.Context, query, root string) (string, bool) {
	if _, err := exec.LookPath("semble"); err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "semble", "search", "-k", "5", query, root)
	cmd.Dir = root
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return "", false
	}
	return out.String(), true
}

// searchFilesRgFd is the fallback when semble is unavailable: filename
// matches via fd plus content matches via rg (both literal, case-insensitive).
func (e *Executor) searchFilesRgFd(query, root string) (string, error) {
	res := map[string]any{"engine": "fd+rg"}
	fdOK := false
	rgOK := false
	if files, ok := runFd(query, root); ok {
		fdOK = true
		rel := make([]string, 0, len(files))
		for _, f := range files {
			if p := e.workspacePath(root, f); p != "" {
				rel = append(rel, p)
			}
		}
		res["files"] = rel
	}
	if matches, ok := runRg(query, root); ok {
		rgOK = true
		m := make([]map[string]any, 0, len(matches))
		for _, mm := range matches {
			m = append(m, map[string]any{
				"path": e.workspacePath(root, mm.Path),
				"line": mm.Line,
				"text": mm.Text,
			})
		}
		res["matches"] = m
	}
	if !fdOK && !rgOK {
		return "", fmt.Errorf("no search tool available: semble, rg and fd are all missing from PATH")
	}
	return toolResult(res), nil
}

// runFd lists files whose names match the query (literal, case-insensitive),
// capped at 50. Paths are relative to the search root.
func runFd(query, root string) ([]string, bool) {
	if _, err := exec.LookPath("fd"); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "fd", "-t", "f", "-i", "-F", "--max-results", "50", query, root)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return []string{}, true // no matches
		}
		return nil, false
	}
	files := []string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files, true
}

// searchMatch is one rg content match.
type searchMatch struct {
	Path string
	Line int
	Text string
}

// runRg returns up to 50 content matches for the query (literal,
// case-insensitive, line numbers). Paths are relative to the search root;
// node_modules, dist and .git are skipped.
func runRg(query, root string) ([]searchMatch, bool) {
	if _, err := exec.LookPath("rg"); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rg",
		"--line-number", "--no-heading", "-i", "-F", "-m", "5",
		"-g", "!**/node_modules/**", "-g", "!**/dist/**", "-g", "!**/.git/**",
		query, root)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return []searchMatch{}, true // no matches
		}
		return nil, false
	}
	matches := []searchMatch{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l == "" {
			continue
		}
		path, rest, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		lineStr, text, _ := strings.Cut(rest, ":")
		line, _ := strconv.Atoi(lineStr)
		if len(text) > 200 {
			text = text[:200] + "…"
		}
		matches = append(matches, searchMatch{Path: path, Line: line, Text: text})
		if len(matches) >= 50 {
			break
		}
	}
	return matches, true
}

// workspacePath converts a search command's output path (relative to the
// search root) to a workspace-relative one.
func (e *Executor) workspacePath(root, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	rel, err := filepath.Rel(e.Root, p)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
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

// deleteFile removes a file from the workspace (files only, never directories).
func (e *Executor) deleteFile(argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
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
	info, err := os.Lstat(full)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory — delete files only", args.Path)
	}
	if err := os.Remove(full); err != nil {
		return "", err
	}
	if e.OnFileChange != nil {
		e.OnFileChange()
	}
	return toolResult(map[string]any{"ok": true, "path": args.Path}), nil
}

// moveFile renames a file or directory within the workspace; destination
// parent directories are created as needed.
func (e *Executor) moveFile(argsJSON string) (string, error) {
	var args struct {
		Path    string `json:"path"`
		NewPath string `json:"newPath"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Path == "" || args.NewPath == "" {
		return "", fmt.Errorf("path and newPath are required")
	}
	src, err := e.resolve(args.Path)
	if err != nil {
		return "", err
	}
	dst, err := e.resolve(args.NewPath)
	if err != nil {
		return "", err
	}
	if src == dst {
		return "", fmt.Errorf("source and destination are the same")
	}
	if _, err := os.Lstat(src); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	if e.OnFileChange != nil {
		e.OnFileChange()
	}
	return toolResult(map[string]any{"ok": true, "path": args.Path, "newPath": args.NewPath}), nil
}

// fetchURL retrieves a web page's readable text (docs, READMEs, API
// references). HTML is stripped to text; responses are size-capped. Pages
// whose static response carries no readable text (JS-rendered SPAs) are
// rendered in headless Chrome when one is available.
func (e *Executor) fetchURL(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	u, err := url.Parse(strings.TrimSpace(args.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url must be a full http(s) URL")
	}
	text, err := e.fetchPageText(ctx, u.String())
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", fmt.Errorf("page returned no readable content")
	}
	if len(text) > 400*1024 {
		text = text[:400*1024] + "\n…(truncated)"
	}
	return toolResult(map[string]any{"ok": true, "url": u.String(), "text": text}), nil
}

// validateFetchURL is the default FetchGuard: only http(s) is allowed, and
// the host must not resolve to a loopback, link-local, multicast or
// unspecified address. The fetch runs server-side, so without this a project
// could reach the v1 instance itself or other previews through fetch_url.
func validateFetchURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL has no host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		// Resolution failure: the fetch itself will report it.
		return nil
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("refusing to fetch non-routable address %s (%s)", ip, host)
		}
	}
	return nil
}

// fetchPageText fetches the page and extracts readable text. When the static
// response is empty, too thin, or fails outright, the page is rendered in
// headless Chrome (via RenderPage) and the text is extracted from the
// rendered DOM — JS frameworks produce no static content.
func (e *Executor) fetchPageText(ctx context.Context, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	guard := e.FetchGuard
	if guard == nil {
		guard = validateFetchURL
	}
	if err := guard(rawURL); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "v1-agent/1.0")
	text := ""
	var fetchErr error
	if resp, err := http.DefaultClient.Do(req); err == nil {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			body, rerr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			resp.Body.Close()
			if rerr == nil {
				src := string(body)
				if strings.Contains(resp.Header.Get("Content-Type"), "html") {
					src = htmlToText(src)
				}
				text = strings.TrimSpace(src)
			}
		} else {
			fetchErr = fmt.Errorf("fetch failed: %s", resp.Status)
			resp.Body.Close()
		}
	} else {
		fetchErr = err
	}
	// SPA shells carry no static text; render when the response was thin or
	// failed outright.
	if (len(text) < 40 || fetchErr != nil) && e.RenderPage != nil {
		if rendered, rerr := e.RenderPage(ctx, rawURL); rerr == nil {
			if rt := strings.TrimSpace(htmlToText(rendered)); rt != "" {
				return rt, nil
			}
		}
	}
	if fetchErr != nil {
		return "", fetchErr
	}
	return text, nil
}

var htmlBlockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "main": true,
	"header": true, "footer": true, "aside": true, "figure": true, "figcaption": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "dl": true, "dt": true, "dd": true,
	"table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true,
	"blockquote": true, "details": true, "summary": true, "address": true,
}

func isHTMLBlock(tag string) bool {
	return htmlBlockTags[tag]
}

func htmlAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// htmlToText extracts the readable text of an HTML document with a proper
// DOM walk: the page title first, then structure-preserving text — headings,
// paragraphs and list items on their own lines, code blocks fenced, table
// cells separated. Scripts, styles, navigation and inline markup are dropped.
func htmlToText(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return ""
	}
	var out strings.Builder
	var title string
	last := byte(0)
	preserve := false // inside <pre>: whitespace kept verbatim
	prefix := ""      // written after the block newline (list markers)

	ensureNL := func() {
		if out.Len() > 0 && last != '\n' {
			out.WriteByte('\n')
			last = '\n'
		}
	}
	write := func(s string) {
		if s == "" {
			return
		}
		out.WriteString(s)
		last = s[len(s)-1]
	}
	var textContent func(n *html.Node) string
	textContent = func(n *html.Node) string {
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
			} else if c.Type == html.ElementNode {
				b.WriteString(textContent(c))
			}
		}
		return b.String()
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			if preserve {
				write(n.Data)
				return
			}
			t := strings.TrimSpace(n.Data)
			if t == "" {
				return
			}
			if out.Len() > 0 && last != ' ' && last != '\n' {
				write(" ")
			}
			write(t)
			return
		case html.ElementNode:
		case html.DocumentNode:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			return
		default:
			return
		}
		switch n.Data {
		case "script", "style", "noscript", "template", "svg", "iframe", "nav", "form":
			return
		case "head":
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && c.Data == "title" {
					title = strings.TrimSpace(textContent(c))
				}
			}
			return
		case "br", "hr":
			ensureNL()
			return
		case "pre":
			ensureNL()
			write("```")
			ensureNL()
			preserve = true
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			preserve = false
			ensureNL()
			write("```")
			ensureNL()
			return
		case "li":
			prefix = "- "
		case "td", "th":
			write("  ")
		case "img":
			if alt := htmlAttr(n, "alt"); alt != "" {
				write(alt)
			}
			return
		}
		if isHTMLBlock(n.Data) {
			ensureNL()
		}
		if prefix != "" {
			write(prefix)
			prefix = ""
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if isHTMLBlock(n.Data) {
			ensureNL()
		}
	}
	walk(doc)
	text := strings.TrimSpace(out.String())
	if title != "" {
		text = title + "\n\n" + text
	}
	return text
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
