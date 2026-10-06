// Package harness supervises the pi-durable sidecar that will run v1's chat
// turns, and speaks its newline-delimited JSON-RPC 2.0 protocol over a Unix
// socket. Go stays the owner of the HTTP API, auth, the store and the tools:
// it starts the sidecar, keeps it alive, and answers the sidecar's tool and
// approval calls.
package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ProtocolVersion is the bridge protocol revision. Both sides report it in
// the handshake; a mismatch is a startup failure (the Go server and the
// sidecar ship in the same image, so they can only disagree after a bad
// upgrade).
const ProtocolVersion = 1

// maxLine caps a single protocol line. Tool results and event batches are the
// largest payloads; the sidecar caps tool output well below this.
const maxLine = 32 << 20

var (
	// ErrNotRunning is returned by Call when the sidecar is not connected.
	ErrNotRunning = errors.New("harness: sidecar is not running")
	// ErrClosed is returned when the peer closes the connection.
	ErrClosed = errors.New("harness: connection closed")
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object, also usable as a Go error.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// Handler answers inbound messages from the sidecar. Requests (“host.call“,
// approval prompts) may block on the user; notifications (“event“ batches)
// must return quickly — they are delivered in order on the read loop, so a
// slow notification handler stalls every later message.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// HandlerError lets a handler choose the JSON-RPC error code it returns.
type HandlerError struct {
	Code    int
	Message string
}

func (e *HandlerError) Error() string { return e.Message }

// Conn is a JSON-RPC 2.0 connection over a stream (a Unix socket). Writes are
// serialized, one reader goroutine dispatches responses to waiting callers
// and inbound messages to the handler.
type Conn struct {
	ctx     context.Context
	conn    net.Conn
	r       *bufio.Reader
	handler Handler
	logf    func(format string, args ...any)

	writeMu sync.Mutex
	w       *bufio.Writer

	mu       sync.Mutex
	nextID   uint64
	pending  map[uint64]chan *rpcMessage
	closed   bool
	closeCh  chan struct{}
	closeErr error
	onClose  func(error)
}

// DialOptions configures a Conn.
type DialOptions struct {
	// Handler answers inbound messages; may be nil for a write-only client.
	Handler Handler
	// Logf receives protocol warnings; nil discards them.
	Logf func(format string, args ...any)
	// Timeout bounds the dial itself (0 uses ctx alone).
	Timeout time.Duration
}

// Dial connects to the sidecar's Unix socket and starts reading. ctx is the
// base context handed to the Handler, so cancelling it stops in-flight tool
// and approval handlers.
func Dial(ctx context.Context, address string, opts DialOptions) (*Conn, error) {
	dialCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "unix", address)
	if err != nil {
		return nil, err
	}
	return newConn(ctx, conn, opts.Handler, opts.Logf), nil
}

func newConn(ctx context.Context, conn net.Conn, handler Handler, logf func(string, ...any)) *Conn {
	if ctx == nil {
		ctx = context.Background()
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	c := &Conn{
		ctx:     ctx,
		conn:    conn,
		r:       bufio.NewReaderSize(conn, 64<<10),
		w:       bufio.NewWriterSize(conn, 64<<10),
		handler: handler,
		logf:    logf,
		pending: map[uint64]chan *rpcMessage{},
		closeCh: make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// SetOnClose registers a callback for the connection's termination. It fires
// exactly once, from the close path.
func (c *Conn) SetOnClose(fn func(error)) {
	c.mu.Lock()
	c.onClose = fn
	c.mu.Unlock()
}

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.closeCh }

// Err reports why the connection ended, if it did.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

// Call sends a request and decodes the result into out (which may be nil).
func (c *Conn) Call(ctx context.Context, method string, params any, out any) error {
	id, ch, err := c.register()
	if err != nil {
		return err
	}
	msg := rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(strconv.FormatUint(id, 10)), Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			c.unregister(id)
			return fmt.Errorf("marshal %s params: %w", method, err)
		}
		msg.Params = raw
	}
	if err := c.write(msg); err != nil {
		c.unregister(id)
		return err
	}
	select {
	case <-ctx.Done():
		c.unregister(id)
		return ctx.Err()
	case <-c.closeCh:
		c.unregister(id)
		if err := c.Err(); err != nil {
			return err
		}
		return ErrClosed
	case res := <-ch:
		if res == nil {
			return ErrClosed
		}
		if res.Error != nil {
			return res.Error
		}
		if out != nil && len(res.Result) > 0 {
			if err := json.Unmarshal(res.Result, out); err != nil {
				return fmt.Errorf("decode %s result: %w", method, err)
			}
		}
		return nil
	}
}

// Notify sends a notification (no reply expected).
func (c *Conn) Notify(method string, params any) error {
	msg := rpcMessage{JSONRPC: "2.0", Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal %s params: %w", method, err)
		}
		msg.Params = raw
	}
	return c.write(msg)
}

// Close closes the connection. It is safe to call more than once.
func (c *Conn) Close() error {
	c.shutdown(ErrClosed)
	return c.conn.Close()
}

// CloseWithError closes the connection, reporting err to callers and OnClose.
func (c *Conn) CloseWithError(err error) {
	c.shutdown(err)
	c.conn.Close()
}

func (c *Conn) register() (uint64, chan *rpcMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		if c.closeErr != nil {
			return 0, nil, c.closeErr
		}
		return 0, nil, ErrClosed
	}
	c.nextID++
	id := c.nextID
	ch := make(chan *rpcMessage, 1)
	c.pending[id] = ch
	return id, ch, nil
}

func (c *Conn) unregister(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Conn) write(msg rpcMessage) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(raw) > maxLine {
		return fmt.Errorf("harness: outgoing %s exceeds %d bytes", msg.Method, maxLine)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.w.Write(raw); err != nil {
		return err
	}
	if err := c.w.WriteByte('\n'); err != nil {
		return err
	}
	return c.w.Flush()
}

// shutdown records the terminal error (first one wins) and releases waiters.
func (c *Conn) shutdown(err error) {
	if err == nil {
		err = ErrClosed
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = err
	c.pending = map[uint64]chan *rpcMessage{}
	onClose := c.onClose
	c.mu.Unlock()

	// Waiters select on closeCh, so closing it releases every Call.
	close(c.closeCh)
	if onClose != nil {
		onClose(err)
	}
}

// readLoop reads newline-delimited messages until the peer closes or a read
// fails, then tears the connection down.
func (c *Conn) readLoop() {
	for {
		line, err := c.r.ReadBytes('\n')
		if len(line) > 0 {
			if len(line) > maxLine {
				c.CloseWithError(fmt.Errorf("harness: incoming message exceeds %d bytes", maxLine))
				return
			}
			c.handleLine(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.CloseWithError(ErrClosed)
			} else {
				c.CloseWithError(fmt.Errorf("harness: read: %w", err))
			}
			return
		}
	}
}

func (c *Conn) handleLine(line []byte) {
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		c.logf("harness: malformed message from sidecar: %v", err)
		c.write(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &RPCError{Code: CodeParseError, Message: "malformed JSON"}})
		return
	}
	// A reply to one of our requests.
	if msg.Method == "" {
		id, err := strconv.ParseUint(string(msg.ID), 10, 64)
		if err != nil {
			c.logf("harness: reply with unusable id %s", msg.ID)
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch == nil {
			c.logf("harness: reply for unknown request %d", id)
			return
		}
		ch <- &msg
		return
	}
	// A request from the sidecar: answer off the read loop, because a handler
	// may block for a long time (an approval waiting on the user).
	if len(msg.ID) > 0 {
		go c.handleRequest(msg)
		return
	}
	c.handleNotification(msg)
}

func (c *Conn) handleRequest(msg rpcMessage) {
	result, err := c.invoke(msg)
	resp := rpcMessage{JSONRPC: "2.0", ID: msg.ID}
	if err != nil {
		code := CodeInternalError
		var he *HandlerError
		if errors.As(err, &he) {
			code = he.Code
		}
		resp.Error = &RPCError{Code: code, Message: err.Error()}
	} else {
		raw, err := json.Marshal(result)
		if err != nil {
			resp.Error = &RPCError{Code: CodeInternalError, Message: err.Error()}
		} else {
			resp.Result = raw
		}
	}
	if err := c.write(resp); err != nil {
		c.logf("harness: reply %s failed: %v", msg.Method, err)
	}
}

// handleNotification delivers unsolicited messages in order. Handler errors
// are logged; there is no one to report them to.
func (c *Conn) handleNotification(msg rpcMessage) {
	if _, err := c.invoke(msg); err != nil {
		c.logf("harness: notification %s failed: %v", msg.Method, err)
	}
}

func (c *Conn) invoke(msg rpcMessage) (any, error) {
	if c.handler == nil {
		return nil, &HandlerError{Code: CodeMethodNotFound, Message: "no handler registered"}
	}
	return c.handler(c.ctx, msg.Method, msg.Params)
}

// NewConnPair returns two connected in-memory Conns, for tests.
func NewConnPair(ctx context.Context, a, b DialOptions) (*Conn, *Conn) {
	server, client := net.Pipe()
	return newConn(ctx, client, a.Handler, a.Logf), newConn(ctx, server, b.Handler, b.Logf)
}
