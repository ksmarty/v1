package agent

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"v1/internal/store"
)

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

	ExitCode int
	TimedOut bool
	Err      error
	Output   string
	done     chan struct{}

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
	mu   sync.Mutex
	jobs map[string]*BackgroundJob
}

func NewBackgroundManager() *BackgroundManager {
	return &BackgroundManager{jobs: map[string]*BackgroundJob{}}
}

// Start launches the command detached in dir. notify runs on completion
// (typically to persist the result message). Returns the job id.
func (m *BackgroundManager) Start(dir, command string, timeout time.Duration, sessionID string, notify func(*BackgroundJob)) (string, error) {
	job := &BackgroundJob{
		ID:        store.NewID(),
		Command:   command,
		SessionID: sessionID,
		done:      make(chan struct{}),
		notify:    notify,
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &limitWriter{max: backgroundOutputCap}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start: %w", err)
	}
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
// conversation: a bracketed notice followed by the (capped) output.
func BackgroundResultText(j *BackgroundJob) string {
	status := fmt.Sprintf("exit %d", j.ExitCode)
	if j.TimedOut {
		status = "timed out"
	} else if j.Err != nil {
		status = "failed to start"
	}
	out := strings.TrimSpace(j.Output)
	if len(out) > backgroundOutputCap {
		out = out[:backgroundOutputCap] + "\n…truncated"
	}
	return fmt.Sprintf("[Background #%s: %s] finished (%s):\n\n%s", j.ID[:8], j.Command, status, out)
}
