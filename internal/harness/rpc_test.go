package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestCallRoundTrip(t *testing.T) {
	peer := make(chan json.RawMessage, 1)
	_, client := NewConnPair(context.Background(),
		DialOptions{Handler: func(_ context.Context, method string, params json.RawMessage) (any, error) {
			peer <- params
			return map[string]any{"echo": method}, nil
		}},
		DialOptions{},
	)
	defer client.Close()

	var out map[string]any
	if err := client.Call(context.Background(), "session.prompt", map[string]any{"text": "hi"}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out["echo"] != "session.prompt" {
		t.Fatalf("echo = %v", out["echo"])
	}
	select {
	case params := <-peer:
		if !strings.Contains(string(params), `"text":"hi"`) {
			t.Fatalf("handler params = %s", params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not invoked")
	}
}

// Concurrent calls must be matched by id, not by arrival order.
func TestConcurrentCallsMatchIDs(t *testing.T) {
	_, client := NewConnPair(context.Background(),
		DialOptions{Handler: func(_ context.Context, _ string, params json.RawMessage) (any, error) {
			var p struct {
				N int `json:"n"`
			}
			_ = json.Unmarshal(params, &p)
			// Reply out of order on purpose.
			time.Sleep(time.Duration(20-p.N%20) * time.Millisecond)
			return map[string]any{"n": p.N}, nil
		}},
		DialOptions{},
	)
	defer client.Close()

	const n = 40
	errCh := make(chan error, n)
	for i := range n {
		go func() {
			var out struct {
				N int `json:"n"`
			}
			if err := client.Call(context.Background(), "tick", map[string]any{"n": i}, &out); err != nil {
				errCh <- err
				return
			}
			if out.N != i {
				errCh <- errors.New("mismatched reply id")
				return
			}
			errCh <- nil
		}()
	}
	for range n {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestHandlerErrorPropagatesCode(t *testing.T) {
	_, client := NewConnPair(context.Background(),
		DialOptions{Handler: func(context.Context, string, json.RawMessage) (any, error) {
			return nil, &HandlerError{Code: CodeMethodNotFound, Message: "no such tool"}
		}},
		DialOptions{},
	)
	defer client.Close()

	err := client.Call(context.Background(), "host.call", nil, nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want *RPCError", err)
	}
	if rpcErr.Code != CodeMethodNotFound || rpcErr.Message != "no such tool" {
		t.Fatalf("rpcErr = %+v", rpcErr)
	}
}

// An approval can block for minutes; the caller's context must still win.
func TestCallRespectsContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	_, client := NewConnPair(context.Background(),
		DialOptions{Handler: func(context.Context, string, json.RawMessage) (any, error) {
			<-release
			return nil, nil
		}},
		DialOptions{},
	)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := client.Call(ctx, "approval.wait", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestCloseIsIdempotentAndReleasesWaiters(t *testing.T) {
	blocked := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, server := NewConnPair(ctx, DialOptions{}, DialOptions{Handler: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		close(blocked)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	done := make(chan error, 1)
	go func() {
		var out any
		done <- client.Call(context.Background(), "never.answers", nil, &out)
	}()
	<-blocked

	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("pending call err = %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not release the pending call")
	}
	if err := client.Call(context.Background(), "any", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after close = %v, want ErrClosed", err)
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("Done is not closed after Close")
	}
	server.Close()
}

// A handler failure on a notification must not tear the connection down.
func TestNotificationErrorIsIsolated(t *testing.T) {
	var got []string
	_, client := NewConnPair(context.Background(),
		DialOptions{Handler: func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			got = append(got, method)
			if method == "event" {
				return nil, errors.New("renderer exploded")
			}
			return "ok", nil
		}},
		DialOptions{},
	)
	defer client.Close()

	if err := client.Notify("event", map[string]any{"kind": "text"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	var out string
	if err := client.Call(context.Background(), "ping", nil, &out); err != nil {
		t.Fatalf("Call after notification error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("out = %q", out)
	}
	if len(got) != 2 || got[0] != "event" {
		t.Fatalf("handler saw %v", got)
	}
}

// The wire format is newline-delimited JSON-RPC 2.0; verify it against a
// hand-rolled peer so the Node sidecar's framing assumptions stay honest.
func TestWireFormat(t *testing.T) {
	clientRaw, peerRaw := net.Pipe()
	client := newConn(context.Background(), clientRaw, func(context.Context, string, json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	}, nil)
	defer client.Close()
	defer peerRaw.Close()

	reader := bufio.NewReader(peerRaw)

	// net.Pipe is synchronous: every write blocks until the other side reads,
	// so the peer must be drained concurrently while we write our requests.
	lines := make(chan map[string]any, 8)
	go func() {
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				var msg map[string]any
				if err := json.Unmarshal(line, &msg); err == nil {
					lines <- msg
				}
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()

	// A malformed line is reported and does not kill the connection.
	if _, err := peerRaw.Write([]byte("not json\n")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	// A request from the peer is answered with the caller's id and jsonrpc 2.0.
	if _, err := peerRaw.Write([]byte(`{"jsonrpc":"2.0","id":"7","method":"harness.ready","params":{}}` + "\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}

	sawParseError := false
	deadline := time.After(5 * time.Second)
	for {
		var msg map[string]any
		select {
		case m, ok := <-lines:
			if !ok {
				t.Fatal("peer connection closed before the reply arrived")
			}
			msg = m
		case <-deadline:
			t.Fatal("timed out waiting for replies")
		}
		if msg["jsonrpc"] != "2.0" {
			t.Fatalf("jsonrpc = %v", msg["jsonrpc"])
		}
		if msg["id"] == "7" {
			result, ok := msg["result"].(map[string]any)
			if !ok || result["ok"] != true {
				t.Fatalf("result = %v", msg["result"])
			}
			if !sawParseError {
				t.Fatal("connection survived garbage without reporting a parse error")
			}
			return
		}
		if rpcErr, ok := msg["error"].(map[string]any); ok {
			if code, _ := rpcErr["code"].(float64); int(code) == CodeParseError {
				sawParseError = true
			}
		}
	}
}

// A peer that hangs up makes pending calls fail rather than hang forever.
func TestPeerCloseFailsPendingCalls(t *testing.T) {
	blocked := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, server := NewConnPair(ctx, DialOptions{}, DialOptions{Handler: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		close(blocked)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	done := make(chan error, 1)
	go func() {
		var out any
		done <- client.Call(context.Background(), "never.answers", nil, &out)
	}()
	<-blocked
	server.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after the peer hung up")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peer close did not release the pending call")
	}
	client.Close()
}

func TestDialMissingSocketFails(t *testing.T) {
	if _, err := Dial(context.Background(), "/nonexistent/harness.sock", DialOptions{}); err == nil {
		t.Fatal("expected dial to fail")
	}
}
