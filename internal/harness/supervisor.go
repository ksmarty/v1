package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrAlreadyRunning is returned by Start when another live sidecar already
// answers on the socket. Only one process may own the durable store, so a
// second one is refused rather than allowed to corrupt it.
var ErrAlreadyRunning = errors.New("harness: another sidecar is already listening")

// Ready is the sidecar's startup handshake payload.
type Ready struct {
	ProtocolVersion  int    `json:"protocolVersion"`
	SchemaVersion    int    `json:"schemaVersion"`
	NodeVersion      string `json:"nodeVersion"`
	PiDurableVersion string `json:"piDurableVersion"`
	Database         string `json:"database"`
	PID              int    `json:"pid"`
}

// Options configures a Supervisor.
type Options struct {
	// Command is the runtime executable (V1_SIDECAR_CMD), e.g. "node".
	Command string
	// Script is the sidecar entrypoint passed as the first argument.
	Script string
	// Args are extra arguments appended after Script.
	Args []string
	// Socket is the Unix socket the sidecar listens on.
	Socket string
	// DBPath is the pi-durable store path, passed to the sidecar.
	DBPath string
	// Env adds to the inherited environment for the child process.
	Env []string
	// Handler answers the sidecar's tool and approval calls.
	Handler Handler
	// Logf receives lifecycle logging (exits, restarts, handshakes).
	Logf func(format string, args ...any)

	// ClientVersion is reported to the sidecar in the handshake.
	ClientVersion string
	// DialTimeout bounds waiting for the socket to appear after spawn.
	DialTimeout time.Duration
	// HandshakeTimeout bounds the harness.ready round trip.
	HandshakeTimeout time.Duration
	// ExitTimeout bounds each graceful-shutdown wait before escalating.
	ExitTimeout time.Duration
	// RestartBackoff is the delay before the first restart attempt; it
	// doubles per attempt up to maxRestartBackoff.
	RestartBackoff time.Duration
	// MaxRestarts bounds consecutive restarts of a crashing sidecar. Zero
	// means a single attempt with no restarts. A sidecar that stayed up for
	// RestartHealthyAfter before dying gets a fresh budget.
	MaxRestarts int
	// RestartHealthyAfter is the uptime after which a crash is treated as an
	// isolated failure rather than part of a crash loop.
	RestartHealthyAfter time.Duration
}

const (
	defaultDialTimeout         = 10 * time.Second
	defaultHandshakeTimeout    = 10 * time.Second
	defaultExitTimeout         = 5 * time.Second
	defaultRestartBackoff      = 500 * time.Millisecond
	defaultRestartHealthyAfter = 60 * time.Second
	maxRestartBackoff          = 30 * time.Second
	probeTimeout               = 3 * time.Second
)

// proc tracks one sidecar process. Its exitCh is closed by the single
// reaper goroutine that calls Wait, so Close and watch can both wait on it
// without racing over Wait.
type proc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	started time.Time
	exitCh  chan struct{}
	exitErr error
}

// Supervisor owns the sidecar process: it spawns it, waits for the socket,
// performs the handshake, restarts it after a crash, and shuts it down.
type Supervisor struct {
	opts Options
	logf func(string, ...any)

	mu          sync.Mutex
	proc        *proc
	conn        *Conn
	ready       Ready
	restarts    int
	consecutive int
	stopping    bool
	stopped     chan struct{}
}

// New builds a Supervisor. Start launches the sidecar.
func New(opts Options) *Supervisor {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = defaultDialTimeout
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = defaultHandshakeTimeout
	}
	if opts.ExitTimeout <= 0 {
		opts.ExitTimeout = defaultExitTimeout
	}
	if opts.RestartBackoff <= 0 {
		opts.RestartBackoff = defaultRestartBackoff
	}
	if opts.RestartHealthyAfter <= 0 {
		opts.RestartHealthyAfter = defaultRestartHealthyAfter
	}
	if opts.ClientVersion == "" {
		opts.ClientVersion = "v1"
	}
	return &Supervisor{opts: opts, logf: opts.Logf, stopped: make(chan struct{})}
}

// Start launches the sidecar and completes the handshake. It fails loudly: a
// sidecar that cannot start, handshake, or agree on the protocol is a startup
// error, not something to discover at the first chat turn.
func (s *Supervisor) Start(ctx context.Context) error {
	if s.opts.Socket == "" {
		return errors.New("harness: no sidecar socket configured")
	}
	if err := os.MkdirAll(filepath.Dir(s.opts.Socket), 0o755); err != nil {
		return fmt.Errorf("harness: create socket dir: %w", err)
	}
	if err := s.refuseIfRunning(ctx); err != nil {
		return err
	}
	// A socket file with no live listener is a leftover from a crash.
	if err := os.Remove(s.opts.Socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("harness: remove stale socket: %w", err)
	}
	if err := s.spawn(ctx); err != nil {
		return err
	}
	go s.watch(ctx)
	return nil
}

// refuseIfRunning probes the socket and reports an error if a live sidecar
// answers. An orphan whose parent died closes its own stdin and exits, so a
// short window distinguishes "orphan still shutting down" from "another
// server owns this socket".
func (s *Supervisor) refuseIfRunning(ctx context.Context) error {
	if _, err := os.Stat(s.opts.Socket); err != nil {
		return nil
	}
	deadline := time.Now().Add(probeTimeout)
	for {
		conn, err := Dial(ctx, s.opts.Socket, DialOptions{Logf: s.logf, Timeout: 500 * time.Millisecond})
		if err != nil {
			return nil
		}
		ready, err := s.handshake(ctx, conn, 2*time.Second)
		conn.Close()
		if err == nil {
			return fmt.Errorf("%w (pid %d, database %s)", ErrAlreadyRunning, ready.PID, ready.Database)
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// spawn starts the process, waits for its socket, and handshakes.
func (s *Supervisor) spawn(ctx context.Context) error {
	args := make([]string, 0, len(s.opts.Args)+1)
	if s.opts.Script != "" {
		args = append(args, s.opts.Script)
	}
	args = append(args, s.opts.Args...)

	// A previous child leaves its socket file behind when it is killed, so
	// clear the path before each spawn; otherwise a restart would dial the
	// dead socket and burn an attempt.
	if err := os.Remove(s.opts.Socket); err != nil && !os.IsNotExist(err) {
		s.logf("harness: remove stale socket: %v", err)
	}

	cmd := exec.Command(s.opts.Command, args...)
	cmd.Env = append(os.Environ(), append(s.opts.Env,
		"V1_SIDECAR_SOCKET="+s.opts.Socket,
		"V1_HARNESS_DB="+s.opts.DBPath,
	)...)
	// The child watches its stdin: when this process dies the pipe closes and
	// the sidecar exits instead of lingering with the store open.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("harness: stdin pipe: %w", err)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return fmt.Errorf("harness: start %s: %w", s.opts.Command, err)
	}

	p := &proc{cmd: cmd, stdin: stdin, started: time.Now(), exitCh: make(chan struct{})}
	go func() {
		p.exitErr = cmd.Wait()
		close(p.exitCh)
	}()
	s.mu.Lock()
	s.proc = p
	s.mu.Unlock()
	s.logf("harness: sidecar started (pid %d): %s %s", cmd.Process.Pid, s.opts.Command, strings.Join(args, " "))

	if err := s.waitForSocket(ctx, p); err != nil {
		s.terminate(p)
		return err
	}
	conn, err := Dial(ctx, s.opts.Socket, DialOptions{Handler: s.opts.Handler, Logf: s.logf})
	if err != nil {
		s.terminate(p)
		return fmt.Errorf("harness: connect %s: %w", s.opts.Socket, err)
	}
	ready, err := s.handshake(ctx, conn, s.opts.HandshakeTimeout)
	if err != nil {
		conn.Close()
		s.terminate(p)
		return err
	}
	conn.SetOnClose(func(err error) {
		s.mu.Lock()
		current := s.conn
		s.mu.Unlock()
		if current == conn {
			s.logf("harness: connection lost: %v", err)
		}
	})

	s.mu.Lock()
	s.conn = conn
	s.ready = ready
	s.mu.Unlock()
	s.logf("harness: sidecar ready (protocol %d, schema %d, node %s, pi-durable %s, db %s)",
		ready.ProtocolVersion, ready.SchemaVersion, ready.NodeVersion, ready.PiDurableVersion, ready.Database)
	return nil
}

// handshake performs the harness.ready round trip and rejects a protocol
// mismatch, which can only happen after a partially upgraded install.
func (s *Supervisor) handshake(ctx context.Context, conn *Conn, timeout time.Duration) (Ready, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req := map[string]any{"protocolVersion": ProtocolVersion, "client": s.opts.ClientVersion}
	var ready Ready
	if err := conn.Call(callCtx, "harness.ready", req, &ready); err != nil {
		return Ready{}, fmt.Errorf("harness: handshake failed: %w", err)
	}
	if ready.ProtocolVersion != ProtocolVersion {
		return Ready{}, fmt.Errorf("harness: protocol mismatch: sidecar speaks %d, server speaks %d",
			ready.ProtocolVersion, ProtocolVersion)
	}
	return ready, nil
}

func (s *Supervisor) waitForSocket(ctx context.Context, p *proc) error {
	deadline := time.Now().Add(s.opts.DialTimeout)
	for {
		if _, err := os.Stat(s.opts.Socket); err == nil {
			return nil
		}
		select {
		case <-p.exitCh:
			return fmt.Errorf("harness: sidecar exited before listening: %v", p.exitErr)
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("harness: socket %s did not appear within %s", s.opts.Socket, s.opts.DialTimeout)
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// watch restarts the sidecar after an unexpected exit, backing off between
// attempts and giving up (loudly) once the consecutive-crash budget is spent.
// A sidecar that ran for RestartHealthyAfter gets a fresh budget, so a rare
// crash after hours of work is not treated as a crash loop.
func (s *Supervisor) watch(ctx context.Context) {
	for {
		s.mu.Lock()
		p := s.proc
		s.mu.Unlock()
		if p == nil {
			return
		}
		<-p.exitCh
		if s.isStopping() {
			return
		}
		uptime := time.Since(p.started)

		s.mu.Lock()
		s.conn = nil
		if uptime >= s.opts.RestartHealthyAfter {
			s.consecutive = 0
		}
		consecutive := s.consecutive
		s.mu.Unlock()
		s.logf("harness: sidecar exited after %s: %v", uptime.Round(time.Millisecond), p.exitErr)

		restarted := false
		for consecutive < s.opts.MaxRestarts {
			consecutive++
			backoff := s.opts.RestartBackoff << (consecutive - 1)
			if backoff > maxRestartBackoff {
				backoff = maxRestartBackoff
			}
			s.logf("harness: restarting sidecar in %s (attempt %d/%d)", backoff, consecutive, s.opts.MaxRestarts)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if s.isStopping() {
				return
			}
			if err := s.spawn(ctx); err != nil {
				s.logf("harness: restart failed: %v", err)
				s.setConsecutive(consecutive)
				continue
			}
			s.mu.Lock()
			s.restarts++
			s.consecutive = consecutive
			s.mu.Unlock()
			restarted = true
			break
		}
		if !restarted {
			s.setConsecutive(consecutive)
			s.logf("harness: sidecar crashed %d times in a row; giving up (chat turns will fail)", consecutive)
			close(s.stopped)
			return
		}
	}
}

func (s *Supervisor) setConsecutive(n int) {
	s.mu.Lock()
	s.consecutive = n
	s.mu.Unlock()
}

func (s *Supervisor) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

// Call proxies a request to the sidecar.
func (s *Supervisor) Call(ctx context.Context, method string, params, out any) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return ErrNotRunning
	}
	return conn.Call(ctx, method, params, out)
}

// Notify proxies a notification to the sidecar.
func (s *Supervisor) Notify(method string, params any) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return ErrNotRunning
	}
	return conn.Notify(method, params)
}

// Ready reports the handshake payload and whether the sidecar is connected.
func (s *Supervisor) Ready() (Ready, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready, s.conn != nil
}

// Restarts reports how many times the sidecar was restarted after a crash.
func (s *Supervisor) Restarts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts
}

// Done is closed when the supervisor gives up on the sidecar.
func (s *Supervisor) Done() <-chan struct{} { return s.stopped }

// Close shuts the sidecar down gracefully: ask it to stop, close its stdin
// (the child's own watchdog), then escalate to SIGTERM and finally SIGKILL.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	conn := s.conn
	s.conn = nil
	p := s.proc
	s.mu.Unlock()

	if conn != nil {
		callCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		var ignored struct{}
		_ = conn.Call(callCtx, "harness.shutdown", map[string]any{"reason": "server shutdown"}, &ignored)
		cancel()
		conn.Close()
	}
	if p == nil {
		return nil
	}
	p.stdin.Close()
	if p.wait(s.opts.ExitTimeout) {
		return nil
	}
	s.logf("harness: sidecar did not exit in %s; sending SIGTERM", s.opts.ExitTimeout)
	_ = p.cmd.Process.Signal(os.Interrupt)
	if p.wait(s.opts.ExitTimeout) {
		return nil
	}
	s.logf("harness: sidecar ignored SIGTERM; killing")
	_ = p.cmd.Process.Kill()
	p.wait(s.opts.ExitTimeout)
	return nil
}

// wait reports whether the process exited within timeout.
func (p *proc) wait(timeout time.Duration) bool {
	select {
	case <-p.exitCh:
		return true
	case <-time.After(timeout):
		return false
	}
}

// terminate kills a process whose startup failed.
func (s *Supervisor) terminate(p *proc) {
	p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	p.wait(2 * time.Second)
	s.mu.Lock()
	if s.proc == p {
		s.proc = nil
		s.conn = nil
	}
	s.mu.Unlock()
}
