package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The supervisor tests exercise a real child process, so the test binary
// doubles as a fake sidecar when V1_FAKE_SIDECAR is set. This keeps the tests
// free of a Node dependency while still covering spawn, socket discovery,
// handshake, crash recovery, and shutdown.
func TestMain(m *testing.M) {
	// Presence, not value: the normal fake mode is the empty string, and a
	// child that re-runs the suite would fork bomb the machine.
	if mode, ok := os.LookupEnv("V1_FAKE_SIDECAR"); ok {
		os.Exit(runFakeSidecar(mode))
	}
	os.Exit(m.Run())
}

// runFakeSidecar is a minimal stand-in for the Node sidecar: it listens on
// the socket the supervisor hands it and answers the handful of methods the
// supervisor uses.
func runFakeSidecar(mode string) int {
	if mode == "crash" {
		return 3
	}
	socket := os.Getenv("V1_SIDECAR_SOCKET")
	if socket == "" {
		return 4
	}
	// The child's stdin closes when the server dies; exit with it so an
	// orphan cannot hold the durable store open.
	go func() {
		_, _ = bufio.NewReader(os.Stdin).ReadByte()
		_ = os.Remove(socket)
		os.Exit(0)
	}()
	if mode == "no-listen" {
		time.Sleep(30 * time.Second)
		return 0
	}
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return 5
	}
	defer ln.Close()
	readyProtocol := ProtocolVersion
	if mode == "bad-protocol" {
		readyProtocol = 99
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return 6
		}
		go serveFakeSidecar(conn, socket, readyProtocol, mode)
	}
}

// fakePeer speaks the newline-delimited JSON-RPC framing by hand, so it does
// not lean on the implementation under test.
type fakePeer struct {
	r *bufio.Reader
	w *bufio.Writer
}

func serveFakeSidecar(conn net.Conn, socket string, readyProtocol int, mode string) {
	peer := &fakePeer{r: bufio.NewReader(conn), w: bufio.NewWriter(conn)}
	for {
		line, err := peer.r.ReadBytes('\n')
		if err != nil {
			return
		}
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		method, _ := msg["method"].(string)
		id, hasID := msg["id"]
		if !hasID {
			continue // notification, nothing to answer
		}
		switch method {
		case "harness.ready":
			peer.reply(id, map[string]any{
				"protocolVersion":  readyProtocol,
				"schemaVersion":    1,
				"nodeVersion":      "v0.0.0-fake",
				"piDurableVersion": "0.0.0-fake",
				"database":         os.Getenv("V1_HARNESS_DB"),
				"pid":              os.Getpid(),
			})
			if mode == "flappy" {
				// Handshake done, then die: exercises the restart budget.
				peer.w.Flush()
				os.Exit(1)
			}
		case "harness.shutdown":
			peer.reply(id, map[string]any{"ok": true})
			peer.w.Flush()
			_ = os.Remove(socket)
			os.Exit(0)
		case "session.prompt":
			result := map[string]any{"echo": method}
			if mode == "host-call" {
				raw, err := peer.request("host.call", map[string]any{"tool": "read_file"})
				if err != nil {
					peer.replyError(id, err.Error())
					continue
				}
				var decoded struct {
					Output string `json:"output"`
				}
				_ = json.Unmarshal(raw, &decoded)
				result["hostResult"] = decoded.Output
			}
			peer.reply(id, result)
		default:
			peer.replyError(id, "unknown method "+method)
		}
	}
}

func (p *fakePeer) send(msg map[string]any) {
	raw, _ := json.Marshal(msg)
	_, _ = p.w.Write(raw)
	_ = p.w.WriteByte('\n')
	_ = p.w.Flush()
}

func (p *fakePeer) reply(id any, result any) {
	p.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (p *fakePeer) replyError(id any, message string) {
	p.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": CodeInternalError, "message": message}})
}

// request sends a call of its own and waits for the reply.
func (p *fakePeer) request(method string, params any) (json.RawMessage, error) {
	const id = "fake-1"
	p.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	for {
		line, err := p.r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if got, _ := msg["id"].(string); got != id {
			// A request the supervisor sent while we waited: answer it so the
			// connection cannot stall.
			if m, _ := msg["method"].(string); m == "harness.shutdown" {
				p.reply(msg["id"], map[string]any{"ok": true})
			}
			continue
		}
		if errObj, ok := msg["error"].(map[string]any); ok {
			return nil, errors.New(errObj["message"].(string))
		}
		raw, _ := json.Marshal(msg["result"])
		return raw, nil
	}
}

func newTestSupervisor(t *testing.T, mode string, handler Handler) *Supervisor {
	t.Helper()
	dir := t.TempDir()
	sup := New(Options{
		Command:        os.Args[0],
		Socket:         filepath.Join(dir, "harness.sock"),
		DBPath:         filepath.Join(dir, "harness.sqlite"),
		Env:            []string{"V1_FAKE_SIDECAR=" + mode},
		Handler:        handler,
		Logf:           t.Logf,
		DialTimeout:    3 * time.Second,
		ExitTimeout:    2 * time.Second,
		RestartBackoff: 20 * time.Millisecond,
		MaxRestarts:    3,
	})
	t.Cleanup(func() { _ = sup.Close() })
	return sup
}

func TestSupervisorStartHandshakeCallAndClose(t *testing.T) {
	sup := newTestSupervisor(t, "", nil)
	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ready, ok := sup.Ready()
	if !ok {
		t.Fatal("Ready reported not connected")
	}
	if ready.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocolVersion = %d", ready.ProtocolVersion)
	}
	if ready.PID <= 0 || ready.NodeVersion == "" || ready.Database == "" {
		t.Fatalf("ready payload incomplete: %+v", ready)
	}
	if ready.Database != sup.opts.DBPath {
		t.Fatalf("database = %q, want %q", ready.Database, sup.opts.DBPath)
	}

	var out struct {
		Echo string `json:"echo"`
	}
	if err := sup.Call(ctx, "session.prompt", map[string]any{"text": "hi"}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Echo != "session.prompt" {
		t.Fatalf("echo = %q", out.Echo)
	}

	pid := ready.PID
	if err := sup.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitFor(t, 3*time.Second, "child process to exit", func() bool {
		return syscall.Kill(pid, 0) != nil
	})
	if err := sup.Call(ctx, "session.prompt", nil, nil); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Call after Close = %v, want ErrNotRunning", err)
	}
	if err := sup.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestSupervisorRefusesSecondInstance(t *testing.T) {
	first := newTestSupervisor(t, "", nil)
	if err := first.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	// A second supervisor on the same socket would open the durable store
	// twice; it must refuse instead.
	second := New(Options{
		Command:     os.Args[0],
		Socket:      first.opts.Socket,
		DBPath:      first.opts.DBPath,
		Env:         []string{"V1_FAKE_SIDECAR="},
		Logf:        t.Logf,
		DialTimeout: 2 * time.Second,
	})
	if err := second.Start(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start = %v, want ErrAlreadyRunning", err)
	}
}

func TestSupervisorRejectsProtocolMismatch(t *testing.T) {
	sup := newTestSupervisor(t, "bad-protocol", nil)
	err := sup.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "protocol mismatch") {
		t.Fatalf("Start = %v, want a protocol mismatch error", err)
	}
}

func TestSupervisorFailsWhenChildDiesBeforeListening(t *testing.T) {
	sup := newTestSupervisor(t, "crash", nil)
	start := time.Now()
	err := sup.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exited before listening") {
		t.Fatalf("Start = %v, want an 'exited before listening' error", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Start waited %s, want a fast failure", elapsed)
	}
}

func TestSupervisorDoesNotWaitForeverForSocket(t *testing.T) {
	sup := newTestSupervisor(t, "no-listen", nil)
	sup.opts.DialTimeout = 300 * time.Millisecond
	err := sup.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not appear") {
		t.Fatalf("Start = %v, want a socket timeout error", err)
	}
}

// A killed sidecar must come back without operator intervention.
func TestSupervisorRestartsAfterCrash(t *testing.T) {
	sup := newTestSupervisor(t, "", nil)
	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first, _ := sup.Ready()
	if err := syscall.Kill(first.PID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill sidecar: %v", err)
	}

	waitFor(t, 5*time.Second, "sidecar to be restarted", func() bool {
		_, ok := sup.Ready()
		return ok && sup.Restarts() >= 1
	})
	var out struct {
		Echo string `json:"echo"`
	}
	if err := sup.Call(ctx, "session.prompt", map[string]any{"text": "again"}, &out); err != nil {
		t.Fatalf("Call after restart: %v", err)
	}
	second, _ := sup.Ready()
	if second.PID == first.PID {
		t.Fatalf("restart reused pid %d", first.PID)
	}
}

// Once the restart budget is spent the supervisor gives up loudly instead of
// respawning forever.
func TestSupervisorGivesUpAfterMaxRestarts(t *testing.T) {
	sup := newTestSupervisor(t, "flappy", nil)
	sup.opts.MaxRestarts = 2
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 10*time.Second, "supervisor to give up", func() bool {
		select {
		case <-sup.Done():
			return true
		default:
			return false
		}
	})
	if err := sup.Call(context.Background(), "session.prompt", nil, nil); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Call after give-up = %v, want ErrNotRunning", err)
	}
}

// The sidecar calls back into Go for tools and approvals; the handler must
// receive those calls over the same connection and the result must reach the
// sidecar.
func TestSupervisorAnswersHostCalls(t *testing.T) {
	hostCalls := make(chan string, 4)
	sup := newTestSupervisor(t, "host-call", func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "host.call" {
			return nil, &HandlerError{Code: CodeMethodNotFound, Message: "unknown method " + method}
		}
		var p struct {
			Tool string `json:"tool"`
		}
		_ = json.Unmarshal(params, &p)
		hostCalls <- p.Tool
		return map[string]any{"output": "file contents"}, nil
	})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var out struct {
		HostResult string `json:"hostResult"`
	}
	if err := sup.Call(context.Background(), "session.prompt", map[string]any{"text": "read"}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	select {
	case tool := <-hostCalls:
		if tool != "read_file" {
			t.Fatalf("tool = %q", tool)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler never received the host call")
	}
	if out.HostResult != "file contents" {
		t.Fatalf("hostResult = %q", out.HostResult)
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
