package agent

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"v1/internal/sanitize"
	"v1/internal/store"
)

// BackgroundToolJSON is the tool_json marker stored on a finished background
// command's result row. It must be VALID JSON: the chat API hands tool_json to
// the browser verbatim as json.RawMessage, and a bare marker made the whole
// message list fail to marshal — the handler had already written its 200
// header, so the client got an empty body and reported a JSON syntax error
// (Safari: "The string did not match the expected pattern") instead of the
// chat history. It is a JSON string so the UI can compare tool === 'background'
// and style the row as a background result rather than something the user said.
const BackgroundToolJSON = `"background"`

// legacyBackgroundToolJSON is the unquoted marker written by earlier builds.
// Rows still carrying it must keep working, so readers accept both encodings.
const legacyBackgroundToolJSON = "background"

// IsBackgroundRow reports whether a stored tool_json marks a background result
// row, in either the current (JSON string) or the legacy (bare word) encoding.
func IsBackgroundRow(toolJSON string) bool {
	return toolJSON == BackgroundToolJSON || toolJSON == legacyBackgroundToolJSON
}

// sanitizeBackgroundText makes raw command output safe for chat messages and
// LLM requests. ANSI escapes and other control characters are removed — the
// OpenAI-compatible providers reject strings that fail their character
// pattern ("The string did not match the expected pattern") — and invalid
// UTF-8 is replaced so the text survives JSON/SSE transport intact.
func sanitizeBackgroundText(s string) string {
	return sanitize.Text(s)
}

// BackgroundResult is a finished background command handed to the agent loop
// for injection into the running turn.
type BackgroundResult struct {
	ID        string
	Text      string
	MessageID int64
}

// BackgroundJob is one detached command. The command runs without any turn
// context; when it finishes, the completion callback (wired by the server)
// persists the result into the chat transcript.
type BackgroundJob struct {
	ID        string
	Command   string
	SessionID string
	StartedAt time.Time

	ExitCode int
	TimedOut bool
	// Cancelled is set when the user cancelled the job from the UI, so the
	// result the model reads says so instead of reporting the signal's exit code.
	Cancelled bool
	Err       error
	Output    string
	done      chan struct{}
	cmd       *exec.Cmd

	// Filled by the completion callback once the result is persisted.
	Text   string
	MsgID  int64
	notify func(*BackgroundJob)
}

// backgroundOutputCap caps what a background result carries into the chat.
const backgroundOutputCap = 32 * 1024

// BackgroundManager tracks detached commands started by the agent. Jobs are
// scoped to the chat session that started them, so results land in the right
// transcript.
type BackgroundManager struct {
	mu    sync.Mutex
	jobs  map[string]*BackgroundJob
	token string // GitHub token, for a background command that runs gh
}

func NewBackgroundManager(token string) *BackgroundManager {
	return &BackgroundManager{jobs: map[string]*BackgroundJob{}, token: token}
}

// Start launches the command detached in dir. notify runs on completion
// (typically to persist the result message). Returns the job id.
func (m *BackgroundManager) Start(dir, command string, timeout time.Duration, sessionID string, notify func(*BackgroundJob)) (string, error) {
	job := &BackgroundJob{
		ID:        store.NewID(),
		Command:   command,
		SessionID: sessionID,
		StartedAt: time.Now(),
		done:      make(chan struct{}),
		notify:    notify,
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = commandEnv(command, m.token)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &limitWriter{max: backgroundOutputCap}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start: %w", err)
	}
	job.cmd = cmd
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	go func() {
		waitCh := make(chan error, 1)
		go func() { waitCh <- cmd.Wait() }()
		select {
		case err := <-waitCh:
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					job.ExitCode = ee.ExitCode()
				} else {
					job.Err = err
				}
			}
		case <-time.After(timeout):
			job.TimedOut = true
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			<-waitCh
		}
		job.Output = out.String()
		close(job.done)
		if job.notify != nil {
			job.notify(job)
		}
	}()
	return job.ID, nil
}

// CancelSession terminates every still-running job started by the chat
// session: SIGTERM to the process group, SIGKILL to whatever survives after a
// 5-second grace. Finished jobs are unaffected. Used when the user stops a
// turn so detached commands don't outlive it.
func (m *BackgroundManager) CancelSession(sessionID string) {
	m.mu.Lock()
	var procs []*os.Process
	for _, j := range m.jobs {
		if j.SessionID != sessionID {
			continue
		}
		select {
		case <-j.done:
			continue
		default:
		}
		if j.cmd != nil && j.cmd.Process != nil {
			procs = append(procs, j.cmd.Process)
		}
	}
	m.mu.Unlock()
	killProcessGroups(procs)
}

// ShortID is the id the agent and the UI use for a job: the first eight
// characters, which is what run_command_background reports back to the model.
func (j *BackgroundJob) ShortID() string {
	if len(j.ID) > 8 {
		return j.ID[:8]
	}
	return j.ID
}

// Running returns the session's still-running jobs, oldest first, so the UI can
// list what is in flight and offer to cancel one.
func (m *BackgroundManager) Running(sessionID string) []*BackgroundJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*BackgroundJob
	for _, j := range m.jobs {
		if j.SessionID != sessionID {
			continue
		}
		select {
		case <-j.done:
			continue
		default:
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt.Before(out[k].StartedAt) })
	return out
}

// Cancel terminates one still-running job of the session, matched by short id
// (or full id). It returns the job it cancelled, or nil when nothing running
// matched: a finished job cannot be cancelled, and another session's job is not
// reachable this way.
func (m *BackgroundManager) Cancel(sessionID, id string) *BackgroundJob {
	m.mu.Lock()
	var target *BackgroundJob
	for _, j := range m.jobs {
		if j.SessionID != sessionID || (j.ID != id && j.ShortID() != id) {
			continue
		}
		select {
		case <-j.done:
			continue
		default:
		}
		target = j
		break
	}
	if target != nil {
		// Set before the kill so the result row the model reads says the user
		// stopped it rather than reporting a signal's exit code.
		target.Cancelled = true
	}
	var procs []*os.Process
	if target != nil && target.cmd != nil && target.cmd.Process != nil {
		procs = append(procs, target.cmd.Process)
	}
	m.mu.Unlock()
	if target == nil {
		return nil
	}
	killProcessGroups(procs)
	return target
}

// KillAll terminates every still-running job across all sessions — used on
// server shutdown so detached commands can't outlive the process.
func (m *BackgroundManager) KillAll() {
	m.mu.Lock()
	var procs []*os.Process
	for _, j := range m.jobs {
		select {
		case <-j.done:
			continue
		default:
		}
		if j.cmd != nil && j.cmd.Process != nil {
			procs = append(procs, j.cmd.Process)
		}
	}
	m.mu.Unlock()
	killProcessGroups(procs)
}

// killProcessGroups SIGTERMs the given process groups and SIGKILLs whatever
// survives after a 5-second grace.
func killProcessGroups(procs []*os.Process) {
	if len(procs) == 0 {
		return
	}
	for _, p := range procs {
		_ = syscall.Kill(-p.Pid, syscall.SIGTERM)
	}
	time.AfterFunc(5*time.Second, func() {
		for _, p := range procs {
			_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
		}
	})
}

// Wait blocks until the job finishes (used by tests and status checks).
func (j *BackgroundJob) Wait() {
	<-j.done
}

// Completed returns and removes the session's finished jobs, oldest first.
func (m *BackgroundManager) Completed(sessionID string) []*BackgroundJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*BackgroundJob
	for _, j := range m.jobs {
		if j.SessionID != sessionID {
			continue
		}
		select {
		case <-j.done:
			out = append(out, j)
			delete(m.jobs, j.ID)
		default:
		}
	}
	return out
}

// BackgroundResultText formats a finished job the way it is injected into the
// conversation: a bracketed notice followed by the (capped) output. Output
// and command are sanitized so raw terminal bytes (ANSI escapes, control
// characters) can never leak into provider-bound messages.
func BackgroundResultText(j *BackgroundJob) string {
	status := fmt.Sprintf("exit %d", j.ExitCode)
	if j.Cancelled {
		status = "cancelled"
	} else if j.TimedOut {
		status = "timed out"
	} else if j.Err != nil {
		status = "failed to start"
	}
	out := strings.TrimSpace(sanitizeBackgroundText(j.Output))
	if len(out) > backgroundOutputCap {
		out = out[:backgroundOutputCap] + "\n…truncated"
	}
	return fmt.Sprintf("[Background #%s: %s] finished (%s):\n\n%s", j.ID[:8], sanitizeBackgroundText(j.Command), status, out)
}
