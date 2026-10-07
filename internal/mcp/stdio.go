package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// maxStderr caps how much of a server's stderr is kept for diagnostics, so a
// chatty server cannot grow the process's memory.
const maxStderr = 4 << 10

// stderrBuffer collects a bounded prefix of a subprocess's stderr. The
// subprocess writes from another goroutine, so it locks.
type stderrBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *stderrBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := maxStderr - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return n, nil
}

func (b *stderrBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// stdioTransport spawns the server as a subprocess and talks
// newline-delimited JSON-RPC over its stdin/stdout.
type stdioTransport struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	errBuf  *stderrBuffer
	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage
	done    chan struct{}
}

func dialStdio(ctx context.Context, cfg ServerConfig) (*stdioTransport, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errBuf := &stderrBuffer{}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", cfg.Command, err)
	}
	t := &stdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		errBuf:  errBuf,
		pending: map[int]chan json.RawMessage{},
		done:    make(chan struct{}),
	}
	go t.readLoop(stdout)
	return t, nil
}

// stderr reports what the subprocess wrote before it failed.
func (t *stdioTransport) stderr() string { return t.errBuf.String() }

func (t *stdioTransport) request(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	t.mu.Lock()
	t.nextID++
	id := t.nextID
	ch := make(chan json.RawMessage, 1)
	t.pending[id] = ch
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}()

	msg := rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", id)), Method: method}
	if params != nil {
		msg.Params = params
	}
	if err := t.writeLine(msg); err != nil {
		return nil, err
	}
	select {
	case raw := <-ch:
		if len(raw) == 0 {
			return nil, fmt.Errorf("connection closed by server")
		}
		var rpc rpcMessage
		if err := json.Unmarshal(raw, &rpc); err != nil {
			return nil, err
		}
		if rpc.Error != nil {
			return nil, fmt.Errorf("%s (%d)", rpc.Error.Message, rpc.Error.Code)
		}
		return rpc.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.done:
		return nil, fmt.Errorf("connection closed by server")
	}
}

func (t *stdioTransport) notify(method string, params map[string]any) error {
	msg := rpcMessage{JSONRPC: "2.0", Method: method, Params: params}
	return t.writeLine(msg)
}

func (t *stdioTransport) close() error {
	select {
	case <-t.done:
		return nil
	default:
	}
	close(t.done)
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
	return nil
}

func (t *stdioTransport) writeLine(msg rpcMessage) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := t.stdin.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (t *stdioTransport) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // ignore malformed frames
		}
		if len(msg.ID) == 0 {
			continue // notification — ignore
		}
		var id int
		if err := json.Unmarshal(msg.ID, &id); err != nil {
			continue
		}
		t.mu.Lock()
		ch := t.pending[id]
		t.mu.Unlock()
		if ch != nil {
			select {
			case ch <- []byte(line):
			default:
			}
		}
	}
	// Stream ended: fail all pending requests.
	t.mu.Lock()
	for _, ch := range t.pending {
		ch <- nil
	}
	t.pending = map[int]chan json.RawMessage{}
	t.mu.Unlock()
}
