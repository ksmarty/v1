package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeState records what the fake server saw, so tests can assert on the
// session and protocol-version headers.
type fakeState struct {
	mu       sync.Mutex
	methods  []string
	sessions []string
	versions []string
	auths    []string
}

func (s *fakeState) record(r *http.Request, method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods = append(s.methods, method)
	s.sessions = append(s.sessions, r.Header.Get("Mcp-Session-Id"))
	s.versions = append(s.versions, r.Header.Get("MCP-Protocol-Version"))
	s.auths = append(s.auths, r.Header.Get("Authorization"))
}

func (s *fakeState) header(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch name {
	case "session":
		return append([]string(nil), s.sessions...)
	case "version":
		return append([]string(nil), s.versions...)
	case "auth":
		return append([]string(nil), s.auths...)
	}
	return nil
}

// newFakeServer is a minimal streamable-HTTP MCP server: it hands out a session
// id on initialize, answers tools/list with a JSON body and tools/call with an
// SSE stream, and requires the session header on every later request.
func newFakeServer(t *testing.T) (*httptest.Server, *fakeState) {
	t.Helper()
	st := &fakeState{}
	const sessionID = "sess-123"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			st.record(r, "delete")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		st.record(r, msg.Method)

		// Every request after initialize must carry the session id back.
		if msg.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != sessionID {
			http.Error(w, "missing session", http.StatusBadRequest)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			http.Error(w, "must accept event stream", http.StatusNotAcceptable)
			return
		}

		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", sessionID)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18",`+
				`"capabilities":{},"serverInfo":{"name":"fake","version":"1"}}}`, msg.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo",`+
				`"description":"echoes","inputSchema":{"type":"object"}}]}}`, msg.ID)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			// An unrelated notification arrives first and must be skipped.
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":"+
				`{"content":[{"type":"text","text":"hello from sse"}]}}`+"\n\n", msg.ID)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"no such method"}}`, msg.ID)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

func TestConnectOverHTTP(t *testing.T) {
	srv, st := newFakeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := Connect(ctx, ServerConfig{ID: "remote", Name: "remote", URL: srv.URL})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}

	text, err := c.CallTool(ctx, "echo", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if text != "hello from sse" {
		t.Fatalf("tool text = %q, want %q", text, "hello from sse")
	}

	// The session id must come back on every request AFTER initialize; the
	// initialize call itself is what creates the session.
	sessions := st.header("session")
	if len(sessions) == 0 || sessions[0] != "" {
		t.Fatalf("initialize sent a session header (%q); the session does not exist yet", sessions)
	}
	for i, sid := range sessions[1:] {
		if sid != "sess-123" {
			t.Fatalf("request %d sent session %q, want sess-123", i+1, sid)
		}
	}
	// The negotiated version must be echoed, not the client's own.
	versions := st.header("version")
	if len(versions) == 0 {
		t.Fatal("no protocol version header was sent")
	}
	if versions[0] != "" {
		t.Fatalf("initialize sent a protocol version header (%q); it belongs only on later requests", versions[0])
	}
	for i, v := range versions[1:] {
		if v != "2025-06-18" {
			t.Fatalf("request %d sent version %q, want the negotiated 2025-06-18", i+1, v)
		}
	}
}

func TestConnectOverHTTPRejectsMissingURL(t *testing.T) {
	if _, err := Connect(context.Background(), ServerConfig{ID: "x"}); err == nil {
		t.Fatal("a config with neither url nor command must fail")
	}
	if _, err := Connect(context.Background(), ServerConfig{ID: "x", URL: "ftp://example.com"}); err == nil {
		t.Fatal("a non-http url must fail")
	}
}

func TestHTTPUnauthorizedIsActionable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://example.com/.well-known/oauth-protected-resource"`)
		http.Error(w, "no token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := Connect(context.Background(), ServerConfig{ID: "remote", Name: "Remote", URL: srv.URL})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "requires authorization") {
		t.Fatalf("error = %q, want it to mention authorization", err)
	}
}

func TestHTTPConfiguredHeadersAreSent(t *testing.T) {
	srv, st := newFakeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := Connect(ctx, ServerConfig{ID: "remote", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer static-key"}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	if _, err := c.ListTools(ctx); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for i, a := range st.header("auth") {
		if a != "Bearer static-key" {
			t.Fatalf("request %d sent auth %q, want the configured header", i, a)
		}
	}
}

func TestHTTPNotificationNeedsNoResponse(t *testing.T) {
	srv, _ := newFakeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Connect(ctx, ServerConfig{ID: "remote", URL: srv.URL})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	if err := c.tr.notify("notifications/initialized", nil); err != nil {
		t.Fatalf("notify: %v", err)
	}
}

func TestReadSSEResponseSkipsUnrelatedFrames(t *testing.T) {
	stream := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n" +
		": keep-alive comment\n\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"ok\":true}}\n\n"
	raw, err := readSSEResponse(strings.NewReader(stream), json.RawMessage("7"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var rpc rpcMessage
	if err := json.Unmarshal(raw, &rpc); err != nil {
		t.Fatal(err)
	}
	if string(rpc.Result) != `{"ok":true}` {
		t.Fatalf("result = %s", rpc.Result)
	}
}

func TestReadSSEResponseUnterminatedEvent(t *testing.T) {
	// The last event is not followed by a blank line, which is legal.
	stream := "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":1}}"
	raw, err := readSSEResponse(strings.NewReader(stream), json.RawMessage("1"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), `"ok":1`) {
		t.Fatalf("raw = %s", raw)
	}
}

func TestReadSSEResponseMissing(t *testing.T) {
	stream := "data: {\"jsonrpc\":\"2.0\",\"id\":9,\"result\":{}}\n\n"
	if _, err := readSSEResponse(strings.NewReader(stream), json.RawMessage("1")); err == nil {
		t.Fatal("a stream without the matching response must be an error")
	}
}

func TestServerConfigSameConnection(t *testing.T) {
	base := ServerConfig{ID: "a", Name: "A", Command: "npx", Args: []string{"-y", "s"}, Headers: map[string]string{"X": "1"}}
	same := base
	same.ID = "b"   // identity and label do not change the wire
	same.Name = "B" // nor does the display name
	if !base.sameConnection(same) {
		t.Fatal("a renamed server must reuse its connection")
	}
	for name, other := range map[string]ServerConfig{
		"different command": {Command: "uvx", Args: base.Args, Headers: base.Headers},
		"different args":    {Command: "npx", Args: []string{"-y", "other"}, Headers: base.Headers},
		"extra arg":         {Command: "npx", Args: []string{"-y", "s", "extra"}, Headers: base.Headers},
		"different headers": {Command: "npx", Args: base.Args, Headers: map[string]string{"X": "2"}},
		"no headers":        {Command: "npx", Args: base.Args},
		"url instead":       {URL: "https://example.com/mcp", Headers: base.Headers},
	} {
		if base.sameConnection(other) {
			t.Fatalf("%s: must not reuse the connection", name)
		}
	}
}
