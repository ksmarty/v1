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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/html"

	"v1/internal/llm"
	"v1/internal/mcp"
	"v1/internal/store"
)

// PreviewStarter starts (or restarts) a project's preview and returns its URL.
type PreviewStarter interface {
	Start(projectID, dir, previewCommand string) (string, error)
}

// maxVerifyOutput caps each verification step's captured output (the model
// gets the interesting tail, not megabytes of logs).
const maxVerifyOutput = 32 << 10

// verifyStep is one pipeline stage: install, lint, typecheck, build, test,
// secrets-scan, preview. Skipped stages don't count against ok.
type verifyStep struct {
	Name    string `json:"name"`
	Success bool   `json:"success"`
	Skipped bool   `json:"skipped"`
	Output  string `json:"output,omitempty"`
	DurMS   int64  `json:"durationMs"`
}

// verifyReport is the structured result of the verify_project tool.
type verifyReport struct {
	OK          bool         `json:"ok"`
	ProjectType string       `json:"projectType"`
	Steps       []verifyStep `json:"steps"`
	Errors      []string     `json:"errors"`
	Suggestions []string     `json:"suggestions"`
}

// Executor executes agent tool calls against a project workspace.
type Executor struct {
	Root           string
	ProjectID      string
	SessionID      string // chat session the turn belongs to (background jobs)
	PreviewCommand string
	Previews       PreviewStarter
	// PreviewURL, when non-nil, resolves the live preview URL for the
	// verify_project health check. Nil skips the preview step.
	PreviewURL func() string
	Store      *store.Store
	Background    *BackgroundManager // detached commands (run_command_background)
		// BackgroundNotify persists a finished background command's result into
		// the chat transcript (wired by the server).
		BackgroundNotify func(*BackgroundJob)
		// OnBackgroundStarted notifies the UI when a background command has been
		// dispatched (single arg: the job's short id), so it can show a live
		// "running" indicator until the result lands.
		OnBackgroundStarted func(string)
	OnTodos          func([]store.Todo)
	OnMemories       func([]store.Memory)
	OnFileChange     func()
	// OnProjectRename notifies the UI when set_project_name renames the
	// project (nil when the turn cannot rename).
	OnProjectRename func(string)
	// OnSessionRename notifies the UI when set_session_name renames the
	// current chat session (nil when the turn cannot rename).
	OnSessionRename func(string)
	MCP             *mcp.Manager // optional: namespaced mcp_<server>_<tool> tools
	Perm            Resolver     // optional: gates tool calls via allow/deny/ask
	PlanMode        bool         // read-only planning turn: state-changing tools refused
	// DisabledTools are builtin tool names turned off in Settings; calls are
	// refused even if the model somehow sends one.
	DisabledTools map[string]bool
	GithubToken   string // user's GitHub token for the git tool's remote ops
	// OnAsk asks the user one or more questions and waits for the answers
	// (the ask_user tool); nil when the turn cannot prompt.
	OnAsk func(ctx context.Context, questions []AskQuestion) ([]AskAnswer, error)
	// AskTimeout bounds ask_user's wait for an answer; 0 uses the default of
	// 5 minutes. The user can always answer sooner.
	AskTimeout time.Duration
	// askCache remembers answered questions during the turn so the agent
	// can't pester the user with the same question twice.
	askCache map[string]string
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
	// DialContext, when set, is used for fetch_url connections (tests, custom
	// proxies). Nil uses the hardened dialer that re-resolves and validates
	// the host at connect time.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// planBlockedTools: state-changing tools that are refused in plan mode.
var planBlockedTools = map[string]bool{
	"write_file":             true,
	"edit_file":              true,
	"delete_file":            true,
	"move_file":              true,
	"run_command":            true,
	"git":                    true,
	"run_container":          true,
	"restart_preview":        true,
	"screenshot_app":         true,
	"set_todos":              true,
	"set_project_name":       true,
	"set_session_name":       true,
	"run_command_background": true,
	"remember":               true,
	"forget":                 true,
}

// planSafeTools filters a tool list down to the read-only ones for plan mode.
func planSafeTools(in []llm.Tool) []llm.Tool {
	out := make([]llm.Tool, 0, len(in))
	for _, t := range in {
		if planBlockedTools[t.Function.Name] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Execute runs one tool call and returns the result string fed back to the LLM.
func (e *Executor) Execute(ctx context.Context, name, argsJSON string) (string, error) {
	if e.DisabledTools[name] {
		return "", fmt.Errorf("%s is disabled in Settings → Tools & permissions → Tools", name)
	}
	if e.PlanMode && (planBlockedTools[name] || strings.HasPrefix(name, "mcp_")) {
		return "", fmt.Errorf("%s is not available in plan mode — planning only reads files and pages", name)
	}
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
	case "git":
		return e.gitOp(ctx, argsJSON)
	case "run_container":
		return e.runContainer(ctx, argsJSON)
	case "restart_preview":
		return e.restartPreview()
	case "screenshot_app":
		return e.screenshotApp(ctx, argsJSON)
	case "set_project_name":
		return e.setProjectName(argsJSON)
	case "set_session_name":
		return e.setSessionName(argsJSON)
	case "run_command_background":
		return e.runCommandBackground(ctx, argsJSON)
	case "set_todos":
		return e.setTodos(argsJSON)
	case "remember":
		return e.remember(argsJSON)
	case "forget":
		return e.forget(argsJSON)
	case "ask_user":
		return e.askUser(ctx, argsJSON)
	case "verify_project":
		return e.verifyProject(ctx, argsJSON)
	case "make_plan":
		return e.makePlan(argsJSON)
	case "update_plan":
		return e.updatePlan(argsJSON)
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
		Content    string   `json:"content"`
		Category   string   `json:"category"`
		Importance *float64 `json:"importance"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	args.Content = strings.TrimSpace(args.Content)
	if args.Content == "" {
		return "", fmt.Errorf("content is required")
	}
	args.Category = strings.ToLower(strings.TrimSpace(args.Category))
	if args.Category != "" && args.Category != "preference" && args.Category != "episodic" && args.Category != "fact" && args.Category != "plan" {
		return "", toolFail("BAD_ARGUMENT", fmt.Sprintf("category %q is not allowed — use preference, episodic, fact or plan", args.Category), true, "use one of preference, episodic, fact or plan")
	}
	importance := 1.0
	if args.Importance != nil {
		importance = *args.Importance
	}
	if importance < 0 || importance > 3 {
		return "", fmt.Errorf("importance must be between 0 and 3")
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
	id, err := e.Store.AddMemory(e.ProjectID, args.Content, args.Category, importance)
	if err != nil {
		return "", err
	}
	e.emitMemories()
	return toolResult(map[string]any{"ok": true, "id": id, "category": args.Category, "importance": importance}), nil
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

// ToolError carries a structured, model-readable error contract. Every tool
// failure reaches the LLM as {success:false, error:{type, message,
// recoverable, suggestion}} so the model can decide whether to retry, adjust
// its approach, or ask the user — instead of guessing from a bare message.
type ToolError struct {
	Type        string
	Message     string
	Recoverable bool
	Suggestion  string
}

func (e *ToolError) Error() string { return e.Message }

// toolFail builds a ToolError; recoverable=true tells the model "fix the
// cause and retry", recoverable=false means "ask the user or change course".
func toolFail(t, msg string, recoverable bool, suggestion string) error {
	return &ToolError{Type: t, Message: msg, Recoverable: recoverable, Suggestion: suggestion}
}

// AskQuestion is one question for the ask_user tool; AskAnswer pairs it with
// the user's response.
type AskQuestion struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

type AskAnswer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// askUser blocks the turn until the user answers through the ask endpoint. A
// single question can be passed as "question" (with optional "options"); pass
// "questions" as an array to ask several in sequence — the user steps through
// them and confirms all answers at once. Questions are bounded by AskTimeout
// (default 5 minutes) and remembered for the rest of the turn: asking the
// same question again returns the earlier answer instead of pestering the
// user.
func (e *Executor) askUser(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Question  string        `json:"question"`
		Options   []string      `json:"options"`
		Questions []AskQuestion `json:"questions"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	var qs []AskQuestion
	if len(args.Questions) > 0 {
		qs = args.Questions
	} else if strings.TrimSpace(args.Question) != "" {
		qs = []AskQuestion{{Question: args.Question, Options: args.Options}}
	} else {
		return "", fmt.Errorf("question is required")
	}
	if len(qs) > 8 {
		qs = qs[:8]
	}
	for i := range qs {
		qs[i].Question = strings.TrimSpace(qs[i].Question)
		if qs[i].Question == "" {
			return "", fmt.Errorf("every question needs a question")
		}
		if len(qs[i].Options) > 4 {
			qs[i].Options = qs[i].Options[:4]
		}
	}
	if e.OnAsk == nil {
		return "", toolFail("ASK_UNAVAILABLE", "asking the user is unavailable in this context", false, "proceed with your best judgment")
	}
	// Repeat guard: the same question was already answered this turn — reuse
	// the answer instead of blocking on the user again.
	if e.askCache == nil {
		e.askCache = map[string]string{}
	}
	parts := make([]string, len(qs))
	for i, q := range qs {
		parts[i] = strings.ToLower(q.Question)
	}
	key := strings.Join(parts, "|")
	if prev, ok := e.askCache[key]; ok && len(qs) == 1 {
		return toolResult(map[string]any{"answer": prev, "note": "this question was already answered earlier in the turn; reusing that answer"}), nil
	}
	timeout := e.AskTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	askCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answers, err := e.OnAsk(askCtx, qs)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || askCtx.Err() == context.DeadlineExceeded {
			label := timeout.Round(time.Second)
			if label < time.Second {
				label = time.Second
			}
			return "", toolFail("ASK_TIMEOUT", fmt.Sprintf("the user did not answer within %s; stop waiting and continue with your best judgment or a reasonable default", label), false, "stop asking and proceed with your best guess")
		}
		return "", err
	}
	if len(answers) == 1 {
		e.askCache[key] = answers[0].Answer
		return toolResult(map[string]any{"answer": answers[0].Answer}), nil
	}
	return toolResult(map[string]any{"answers": answers}), nil
}

// runCommandBackground starts a command detached from the turn: the tool
// returns immediately with the job id, and the result is injected into the
// conversation as a "[Background #id: …]" user message when the command
// finishes. The agent can keep working instead of blocking on long commands.
func (e *Executor) runCommandBackground(ctx context.Context, argsJSON string) (string, error) {
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
	if fields := strings.Fields(args.Command); len(fields) > 0 && fields[0] == "sudo" {
		return "", toolFail("PRIVILEGE_ESCALATION", "sudo (and other privilege escalation) is not allowed", false, "run the command without sudo; the workspace already has your permissions")
	}
	if e.Perm != nil {
		ok, err := e.Perm.Request(ctx, "run_command_background", args.Command)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("command was not allowed")
		}
	}
	if e.Background == nil {
		return "", fmt.Errorf("background commands are unavailable in this context")
	}
	timeout := 600 * time.Second
	if args.TimeoutSeconds != nil && *args.TimeoutSeconds > 0 {
		timeout = time.Duration(*args.TimeoutSeconds * float64(time.Second))
		if timeout > 3600*time.Second {
			timeout = 3600 * time.Second
		}
	}
	id, err := e.Background.Start(e.Root, args.Command, timeout, e.SessionID, e.BackgroundNotify)
	if err != nil {
		return "", err
	}
	if e.OnBackgroundStarted != nil {
		e.OnBackgroundStarted(id[:8])
	}
	return toolResult(map[string]any{"id": id[:8], "status": "running", "note": "the result arrives in the chat when the command finishes; continue with other work meanwhile"}), nil
}

// setProjectName renames the project — the agent uses it at the start of a
// new project so the display name comes from the work, not the first prompt.
// Only the project's first session may rename it; later sessions get a
// refusal so they don't clobber the established name.
func (e *Executor) setProjectName(argsJSON string) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	name := strings.TrimSpace(args.Name)
	r := []rune(name)
	if r == nil || len(r) == 0 {
		return "", fmt.Errorf("name is required")
	}
	if len(r) > 80 {
		name = string(r[:80])
	}
	if e.Store == nil {
		return "", fmt.Errorf("project store unavailable")
	}
	def, err := e.Store.EnsureDefaultSession(e.ProjectID)
	if err != nil {
		return "", err
	}
	if e.SessionID != "" && def.ID != e.SessionID {
		return "", fmt.Errorf("only the project's first session can set the project name")
	}
	if err := e.Store.RenameProject(e.ProjectID, name); err != nil {
		return "", err
	}
	if e.OnProjectRename != nil {
		e.OnProjectRename(name)
	}
	return toolResult(map[string]any{"ok": true, "name": name}), nil
}

// setSessionName renames the current chat session — mainly useful at the
// start of a new session, so the session list shows what the thread is about
// instead of "Session 3". Only the session the turn runs in can be renamed.
func (e *Executor) setSessionName(argsJSON string) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	name := strings.TrimSpace(args.Name)
	r := []rune(name)
	if r == nil || len(r) == 0 {
		return "", fmt.Errorf("name is required")
	}
	if len(r) > 80 {
		name = string(r[:80])
	}
	if e.SessionID == "" {
		return "", fmt.Errorf("no active chat session")
	}
	if e.Store == nil {
		return "", fmt.Errorf("session store unavailable")
	}
	if err := e.Store.RenameChatSession(e.ProjectID, e.SessionID, name); err != nil {
		return "", err
	}
	if e.OnSessionRename != nil {
		e.OnSessionRename(name)
	}
	return toolResult(map[string]any{"ok": true, "name": name}), nil
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
		return "", toolFail("PATH_ESCAPE", fmt.Sprintf("path %q escapes the workspace", rel), true, "use a relative path inside the project directory")
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
		return "", toolFail("PATH_ESCAPE", fmt.Sprintf("path %q escapes the workspace (symlink resolves outside)", rel), true, "use a relative path inside the project directory")
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
	// writeFile's contract: text files only, capped at 500KB, so a runaway
	// write can neither fill the disk nor slip a binary blob into the tree.
	if len(args.Content) > 500*1024 {
		return "", toolFail("TOO_LARGE", fmt.Sprintf("file content is %d bytes; the write cap is 500KB", len(args.Content)), true, "write the file in smaller pieces or use run_command to generate it")
	}
	if strings.IndexByte(args.Content, 0) >= 0 {
		return "", toolFail("BINARY_REJECTED", "refusing to write a binary file (contains NUL bytes)", true, "write text content only")
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
		if bannedIP(ip) {
			return toolFail("BLOCKED_HOST", fmt.Sprintf("host %q resolves to blocked address %s", host, ip), false, "fetch a public URL instead of an internal one")
		}
	}
	return nil
}

// bannedIP implements the fetch_url address blocklist: loopback, link-local
// (both unicast and multicast), multicast, and unspecified (0.0.0.0/::)
// addresses are never reachable from fetch_url — the fetch runs server-side,
// so these would otherwise reach the v1 instance itself or other tenants.
func bannedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified()
}

// allowedContentType implements the fetch_url content-type allow-list:
// text/* and common text-ish application types pass; anything else (PDFs,
// images, archives, binaries) is refused because it can't be meaningfully fed
// to the model. A missing header is allowed — the body is then sniffed.
func allowedContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	if ct == "" {
		return true
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json", "application/xml", "application/javascript",
		"application/x-javascript", "application/xhtml+xml", "application/markdown":
		return true
	}
	return false
}

// fetchPageText fetches the page and extracts readable text. When the static
// response is empty, too thin, or fails outright, the page is rendered in
// headless Chrome (via RenderPage) and the text is extracted from the
// rendered DOM — JS frameworks produce no static content.
func (e *Executor) fetchPageText(ctx context.Context, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
	// The default dialer re-resolves the host at connect time and validates
	// every IP, which closes the DNS-rebinding window: a hostname that
	// resolves to a public address for the pre-check but to a private one at
	// dial time is refused before a connection is made. A custom DialContext
	// (tests, custom proxies) replaces it wholesale.
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if e.DialContext != nil {
		transport.DialContext = e.DialContext
	} else {
		var dialer net.Dialer
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.LookupIP(host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if bannedIP(ip) {
					return nil, toolFail("BLOCKED_HOST", fmt.Sprintf("host %q resolves to blocked address %s", host, ip), false, "fetch a public URL instead of an internal one")
				}
			}
			var lastErr error
			for _, ip := range ips {
				if conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port)); err == nil {
					return conn, nil
				} else {
					lastErr = err
				}
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("no usable addresses for %s", host)
			}
			return nil, lastErr
		}
	}
	client := &http.Client{Transport: transport}
	text := ""
	var fetchErr error
	if resp, err := client.Do(req); err == nil {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if !allowedContentType(resp.Header.Get("Content-Type")) {
				ct := resp.Header.Get("Content-Type")
				_ = resp.Body.Close()
				return "", toolFail("NOT_ALLOWED", fmt.Sprintf("content type %q is not allowed for fetch_url (text and JSON only)", ct), false, "fetch a plain-text, HTML or JSON URL instead")
			}
			body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
	if fields := strings.Fields(args.Command); len(fields) > 0 && fields[0] == "sudo" {
		return "", toolFail("PRIVILEGE_ESCALATION", "sudo (and other privilege escalation) is not allowed", false, "run the command without sudo; the workspace already has your permissions")
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

	cmdLine := args.Command
	cmd := exec.Command("sh", "-c", cmdLine)
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
	cancelled := false
	var runErr error
	select {
	case runErr = <-waitCh:
	case <-ctx.Done():
		// The turn was stopped (stop button, hard timeout, session end): kill
		// the whole process group so grandchildren don't outlive the turn.
		cancelled = true
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case runErr = <-waitCh:
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				runErr = <-waitCh
			}
		}
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
	if cancelled {
		res["cancelled"] = true
		res["note"] = "command was cancelled (the turn was stopped or the session ended)"
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

// gitOp runs any git command against the project workspace. The whole git
// CLI is exposed (status, add, commit, push, pull, log, diff, branch,
// checkout, revert, merge, remote, ...) so the model can drive every phase of
// a repo's lifecycle. Commands run with the workspace as the working
// directory (equivalent to `git -C <root>`). Remote operations (push, pull,
// fetch, clone) authenticate automatically with the user's linked GitHub
// token, so no host git credentials are required.
func (e *Executor) gitOp(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	cmdline := strings.TrimSpace(args.Command)
	if cmdline == "" {
		return "", fmt.Errorf("git: command is required, e.g. \"status\", \"add .\", \"commit -m …\"")
	}
	fields := strings.Fields(cmdline)
	if fields[0] == "git" {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return "exitCode: 0, output: (no command)", nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git: the git binary is not installed on this host — install git to use this tool")
	}
	full := []string{"-C", e.Root}
	// Credential injection for remote operations: a one-shot credential
	// helper that answers with the user's GitHub token. It is only attached
	// to the subcommands that hit the network, and only when a token exists.
	if e.GithubToken != "" && isGitRemoteOp(fields[0]) {
		helper := `!f() { printf "username=x-access-token\npassword=$V1_GIT_TOKEN\n"; }; f`
		full = append(full, "-c", "credential.helper="+helper)
	}
	full = append(full, fields...)
	execCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(execCtx, "git", full...)
	if e.GithubToken != "" && isGitRemoteOp(fields[0]) {
		cmd.Env = append(os.Environ(), "V1_GIT_TOKEN="+e.GithubToken)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if execCtx.Err() != nil {
			return "", fmt.Errorf("git: timed out")
		}
		return toolResult(map[string]any{
			"exitCode": 1,
			"output":   trimHeadTail(out.String(), 5*1024),
			"error":    "git command failed: " + err.Error(),
		}), nil
	}
	return toolResult(map[string]any{"exitCode": 0, "output": out.String()}), nil
}

// isGitRemoteOp reports whether the subcommand talks to a remote, where the
// injected credential helper applies.
func isGitRemoteOp(sub string) bool {
	switch sub {
	case "push", "pull", "fetch", "clone", "ls-remote", "submodule":
		return true
	}
	return false
}

// runContainer lets the model test container/docker functionality. It runs
// podman when available (a lighter-weight, daemonless Docker-compatible
// runtime) and falls back to docker. Container image repo tests can go through
// whatever is installed — e.g. `run_container` with "images", "ps", or build
// & run commands.
func (e *Executor) verifyProject(ctx context.Context, argsJSON string) (string, error) {
	report := verifyReport{ProjectType: "plain"}
	steps := func(s verifyStep) { report.Steps = append(report.Steps, s) }

	// Detect the project type from the files that actually drive its build.
	var pkg map[string]any
	var scripts map[string]any
	hasTS := false
	if data, err := os.ReadFile(filepath.Join(e.Root, "package.json")); err == nil {
		_ = json.Unmarshal(data, &pkg)
		if s, ok := pkg["scripts"].(map[string]any); ok {
			scripts = s
		}
		report.ProjectType = "node"
		if _, err := os.Stat(filepath.Join(e.Root, "tsconfig.json")); err == nil {
			hasTS = true
		}
	} else if _, err := os.Stat(filepath.Join(e.Root, "pyproject.toml")); err == nil {
		report.ProjectType = "python"
	} else if _, err := os.Stat(filepath.Join(e.Root, "requirements.txt")); err == nil {
		report.ProjectType = "python"
	} else if _, err := os.Stat(filepath.Join(e.Root, "go.mod")); err == nil {
		report.ProjectType = "go"
	}
	if report.ProjectType == "plain" {
		report.OK = true
		return reportResult(report)
	}

	// 1. Install — only when the manifest is newer than the installed tree,
	//    so a cold checkout installs but unchanged projects skip the network.
	if report.ProjectType == "node" {
		need := true
		marker := filepath.Join(e.Root, "node_modules", ".package-lock.json")
		if st, err := os.Stat(marker); err == nil {
			mtime := st.ModTime()
			newer := false
			for _, f := range []string{"package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml"} {
				if mst, err := os.Stat(filepath.Join(e.Root, f)); err == nil && mst.ModTime().After(mtime) {
					newer = true
				}
			}
			need = newer
		}
		if need {
			var cmdLine string
			if _, err := os.Stat(filepath.Join(e.Root, "yarn.lock")); err == nil {
				cmdLine = "yarn install --frozen-lockfile"
			} else if _, err := os.Stat(filepath.Join(e.Root, "pnpm-lock.yaml")); err == nil {
				cmdLine = "pnpm install --frozen-lockfile"
			} else {
				cmdLine = "npm install"
			}
			steps(e.runVerifyCmd(ctx, "install", cmdLine, 10*time.Minute))
		} else {
			steps(verifyStep{Name: "install", Success: true, Skipped: true, Output: "dependencies are up to date"})
		}
		steps(e.runVerifyScript(ctx, scripts, "lint", "lint", 5*time.Minute))
		if scripts["typecheck"] != nil {
			steps(e.runVerifyScript(ctx, scripts, "typecheck", "typecheck", 5*time.Minute))
		} else if hasTS {
			steps(e.runVerifyCmd(ctx, "typecheck", "npx tsc --noEmit", 5*time.Minute))
		} else {
			steps(verifyStep{Name: "typecheck", Success: true, Skipped: true, Output: "no typecheck script or TypeScript config"})
		}
		steps(e.runVerifyScript(ctx, scripts, "build", "build", 10*time.Minute))
		steps(e.runVerifyScript(ctx, scripts, "test", "test", 10*time.Minute))
	} else if report.ProjectType == "go" {
		steps(e.runVerifyCmd(ctx, "vet", "go vet ./...", 5*time.Minute))
		steps(e.runVerifyCmd(ctx, "build", "go build ./...", 10*time.Minute))
	} else if report.ProjectType == "python" {
		if _, err := os.Stat(filepath.Join(e.Root, "pyproject.toml")); err == nil {
			steps(e.runVerifyCmd(ctx, "build", "python -m build", 5*time.Minute))
		}
	}

	// Static analysis: leaked secrets + unsafe eval in sources.
	secretStep, suggestions := e.scanForSecrets()
	steps(secretStep)
	report.Suggestions = append(report.Suggestions, suggestions...)

	// Preview health: the running dev server must answer 2xx.
	if e.PreviewURL != nil {
		if url := e.PreviewURL(); url != "" {
			start := time.Now()
			ok := false
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
			if err == nil {
				resp, herr := http.DefaultClient.Do(req)
				if herr == nil {
					ok = resp.StatusCode >= 200 && resp.StatusCode < 400
					resp.Body.Close()
				}
			}
			cancel()
			out := ""
			if !ok {
				out = "the preview did not answer with 2xx/3xx — the dev server may be down or still starting"
			}
			steps(verifyStep{Name: "preview", Success: ok, Output: out, DurMS: time.Since(start).Milliseconds()})
		} else {
			steps(verifyStep{Name: "preview", Success: true, Skipped: true, Output: "no live preview"})
		}
	}

	report.OK = true
	for _, s := range report.Steps {
		if s.Skipped {
			continue
		}
		if !s.Success {
			report.OK = false
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %s", s.Name, s.Output))
		}
	}
	return reportResult(report)
}

// reportResult flattens a struct report into the flat JSON tool-result
// contract ({ok, steps, errors, …}) the model consumes.
func reportResult(r verifyReport) (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return "", err
	}
	return toolResult(m), nil
}

// runVerifyScript runs a package.json script when it exists (and isn't a
// placeholder like "echo ok"), skipping it otherwise.
func (e *Executor) runVerifyScript(ctx context.Context, scripts map[string]any, key, label string, timeout time.Duration) verifyStep {
	raw, ok := scripts[key]
	if !ok {
		return verifyStep{Name: label, Success: true, Skipped: true, Output: "no " + key + " script"}
	}
	cmd, _ := raw.(string)
	if cmd == "" || strings.HasPrefix(cmd, "echo ") {
		return verifyStep{Name: label, Success: true, Skipped: true, Output: key + " script is a placeholder"}
	}
	return e.runVerifyCmd(ctx, label, "npm run "+key, timeout)
}

// runVerifyCmd runs one pipeline command against the project root, capturing
// capped output and a duration.
func (e *Executor) runVerifyCmd(ctx context.Context, name, cmdline string, timeout time.Duration) verifyStep {
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", cmdline)
	cmd.Dir = e.Root
	cmd.Env = os.Environ()
	out := &limitWriter{max: maxVerifyOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	output := strings.TrimSpace(trimHeadTail(out.String(), 12000))
	// Cap what the model must read: first and last lines with elision.
	if len(output) > 4000 {
		output = output[:2000] + "\n... (" + fmt.Sprint(len(output)-4000) + " bytes elided) ...\n" + output[len(output)-2000:]
	}
	return verifyStep{Name: name, Success: err == nil, Output: output, DurMS: time.Since(start).Milliseconds()}
}

// secretPatterns are high-precision, low-false-positive credentials. Anything
// they match in project sources means a real secret is probably in the tree.
var secretPatterns = []struct{ label, re string }{
	{"OpenAI API key", `\bsk-[A-Za-z0-9]{20,}\b`},
	{"Anthropic API key", `\bsk-ant-[A-Za-z0-9]{20,}\b`},
	{"Google API key", `\bAIza[0-9A-Za-z_-]{35}\b`},
	{"AWS access key", `\bAKIA[0-9A-Z]{16}\b`},
	{"GitHub token", `\bgh[pousr]_[A-Za-z0-9]{36,}\b`},
	{"GitHub fine-grained PAT", `\bgithub_pat_[A-Za-z0-9_]{20,}\b`},
	{"Slack token", `\bxox[baprs]-[A-Za-z0-9-]{10,}\b`},
	{"Stripe key", `\b(?:sk|pk)_(?:live|test)_[A-Za-z0-9]{16,}\b`},
	{"Private key block", `-----BEGIN [A-Z ]*PRIVATE KEY-----`},
}

// scanForSecrets walks the source tree (skipping vendors/builds/git) looking
// for high-signal credential patterns, and checks .env is gitignored.
func (e *Executor) scanForSecrets() (verifyStep, []string) {
	start := time.Now()
	var findings []string
	skipDirs := map[string]bool{
		"node_modules": true, ".git": true, "dist": true, "build": true,
		".next": true, "out": true, "target": true, "vendor": true,
		"__pycache__": true, ".cache": true, ".venv": true, "venv": true,
	}
	regexes := make([]*regexp.Regexp, 0, len(secretPatterns))
	for _, p := range secretPatterns {
		regexes = append(regexes, regexp.MustCompile(p.re))
	}
	visit := func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != e.Root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		base := d.Name()
		if strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".lock") ||
			strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".map") ||
			strings.HasSuffix(base, ".png") || strings.HasSuffix(base, ".jpg") ||
			strings.HasSuffix(base, ".jpeg") || strings.HasSuffix(base, ".gif") ||
			strings.HasSuffix(base, ".woff") || strings.HasSuffix(base, ".woff2") ||
			strings.HasSuffix(base, ".ico") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 512<<10 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(e.Root, path)
		lines := strings.Split(string(data), "\n")
		for li, line := range lines {
			for i, re := range regexes {
				if m := re.FindString(line); m != "" {
					findings = append(findings, fmt.Sprintf("%s (%s:%d): %s", secretPatterns[i].label, rel, li+1, maskSecret(m)))
				}
			}
		}
		return nil
	}
	_ = filepath.WalkDir(e.Root, visit)
	step := verifyStep{Name: "secrets-scan", Success: len(findings) == 0, DurMS: time.Since(start).Milliseconds()}
	if len(findings) > 0 {
		step.Output = "possible secrets in the source tree (rotate them and move to environment variables):\n" + strings.Join(findings, "\n")
	}
	// .env must never be committed: missing or non-covering .gitignore both
	// count as a red flag worth surfacing.
	var sug []string
	envFiles, _ := filepath.Glob(filepath.Join(e.Root, ".env*"))
	gi, _ := os.ReadFile(filepath.Join(e.Root, ".gitignore"))
	if len(envFiles) > 0 {
		covered := len(gi) > 0 && strings.Contains(string(gi), ".env")
		if !covered {
			sug = append(sug, ".env files exist but .gitignore does not cover them — add .env* before committing")
		}
	}
	return step, sug
}

// maskSecret shows just enough of a leaked credential to identify it without
// echoing the full value back into the transcript.
func maskSecret(s string) string {
	if len(s) <= 8 {
		return s[:1] + "…"
	}
	return s[:4] + "…" + s[len(s)-2:]
}

// planDocument is the schema a plan must satisfy before it is stored. The
// model produces this JSON on the first turn of a multi-step task and updates
// it (make_plan/update_plan) as work progresses.
type planDocument struct {
	Goal           string      `json:"goal"`
	Features       []planFeat  `json:"features"`
	Invariants     []string    `json:"invariants"`
	Checkpoints    []planCheck `json:"checkpoints"`
	EstimatedTurns int         `json:"estimated_turns"`
}

type planFeat struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	DependsOn   []string `json:"depends_on"`
}

type planCheck struct {
	Step         int    `json:"step"`
	Action       string `json:"action"`
	Verification string `json:"verification"`
}

// validatePlan checks a plan against its schema and returns a structured
// error the model can fix on the next call.
func validatePlan(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return toolFail("PLAN_INVALID", "plan is empty", true, "pass a valid plan JSON: {goal, features, invariants, checkpoints, estimated_turns}")
	}
	var doc planDocument
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return toolFail("PLAN_INVALID", fmt.Sprintf("plan is not valid JSON: %v", err), true, "fix the JSON and try again")
	}
	if strings.TrimSpace(doc.Goal) == "" {
		return toolFail("PLAN_INVALID", "plan.goal must be a non-empty sentence describing the task", true, "add a goal string")
	}
	if doc.EstimatedTurns <= 0 {
		return toolFail("PLAN_INVALID", "plan.estimated_turns must be a positive integer", true, "set an estimated_turns count")
	}
	if len(doc.Features) == 0 {
		return toolFail("PLAN_INVALID", "plan.features must list at least one feature", true, "break the task into features with ids")
	}
	ids := map[string]bool{}
	for _, f := range doc.Features {
		if strings.TrimSpace(f.ID) == "" || strings.TrimSpace(f.Description) == "" {
			return toolFail("PLAN_INVALID", "every feature needs an id and a description", true, "give each feature an id and a description")
		}
		ids[f.ID] = true
	}
	for _, f := range doc.Features {
		for _, dep := range f.DependsOn {
			if !ids[dep] {
				return toolFail("PLAN_INVALID", fmt.Sprintf("feature %q depends on unknown feature %q", f.ID, dep), true, "only reference feature ids you listed")
			}
		}
	}
	for _, c := range doc.Checkpoints {
		if c.Step <= 0 || strings.TrimSpace(c.Action) == "" || strings.TrimSpace(c.Verification) == "" {
			return toolFail("PLAN_INVALID", "every checkpoint needs a positive step number, an action and a verification", true, "fix the checkpoint with step/action/verification")
		}
	}
	return nil
}

// setPlan is the shared core of make_plan and update_plan.
func (e *Executor) setPlan(argsJSON string) (string, error) {
	var args struct {
		Plan string `json:"plan"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if err := validatePlan(args.Plan); err != nil {
		return "", err
	}
	if e.Store == nil {
		return "", fmt.Errorf("memory store unavailable")
	}
	if err := e.Store.SetPlan(e.ProjectID, args.Plan); err != nil {
		return "", err
	}
	return toolResult(map[string]any{"ok": true, "note": "plan stored — it is injected into the system prompt of subsequent turns"}), nil
}

// makePlan starts (or replaces) the project's active plan. The plan JSON is
// validated before storage so malformed plans never reach the prompt.
func (e *Executor) makePlan(argsJSON string) (string, error) {
	return e.setPlan(argsJSON)
}

// updatePlan replaces the active plan — the model should send the full,
// updated document with progress notes reflected (features marked done,
// checkpoints checked off), not a diff.
func (e *Executor) updatePlan(argsJSON string) (string, error) {
	return e.setPlan(argsJSON)
}

// containerResourceCaps bounds every `run` container the agent starts:
// default 1 CPU and 1GB of memory keep a runaway container from starving the
// host. Commands that already set their own --memory/--cpus/--cpuset flags
// keep them untouched.
func containerResourceCaps(fields []string) []string {
	if len(fields) == 0 || fields[0] != "run" {
		return fields
	}
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "--memory") || strings.HasPrefix(f, "--cpus") || f == "-m" {
			return fields
		}
	}
	out := make([]string, 0, len(fields)+3)
	out = append(out, fields[0], "--cpus", "1", "--memory", "1g")
	return append(out, fields[1:]...)
}

func (e *Executor) runContainer(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	cli := ""
	for _, c := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(c); err == nil {
			cli = c
			break
		}
	}
	if cli == "" {
		return "", fmt.Errorf("no container runtime found: install podman or docker on the host to run container commands")
	}
	cmdline := strings.TrimSpace(args.Command)
	if cmdline == "" {
		return "", fmt.Errorf("run_container: command is required, e.g. \"images\", \"ps -a\", \"build -t myapp .\"")
	}
	if e.Perm != nil {
		ok, err := e.Perm.Request(ctx, "run_container", cmdline)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("container command was not allowed")
		}
	}
	fields := strings.Fields(cmdline)
	if fields[0] == cli {
		fields = fields[1:]
	}
	fields = containerResourceCaps(fields)
	execCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(execCtx, cli, fields...)
	cmd.Dir = e.Root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if execCtx.Err() != nil {
			return "", fmt.Errorf("run_container: timed out")
		}
		return toolResult(map[string]any{
			"runtime":  cli,
			"exitCode": 1,
			"output":   trimHeadTail(out.String(), 5*1024),
			"error":    cli + " command failed: " + err.Error(),
		}), nil
	}
	return toolResult(map[string]any{"runtime": cli, "exitCode": 0, "output": out.String()}), nil
}
