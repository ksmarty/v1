package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxHTTPBody caps a single response body. MCP results are text, and the
// largest legitimate one is far below this.
const maxHTTPBody = 8 << 20

// protocolVersionSetter is implemented by transports that must echo the
// protocol version the server negotiated. The streamable HTTP transport
// requires it on every request after initialize; stdio has no such header.
type protocolVersionSetter interface {
	setProtocolVersion(string)
}

// httpTransport speaks the streamable HTTP transport: one endpoint that accepts
// JSON-RPC POSTs and answers either with a JSON body or with an SSE stream
// carrying the response.
type httpTransport struct {
	cfg    ServerConfig
	client *http.Client
	// tokens supplies a bearer token from a stored OAuth grant, when configured.
	tokens TokenSource

	mu      sync.Mutex
	nextID  int
	session string
	// version is the protocol version the server negotiated, echoed back in
	// MCP-Protocol-Version on later requests.
	version string
	// token is the current OAuth access token, set by the OAuth flow.
	token string
}

func dialHTTP(cfg ServerConfig, tokens TokenSource) (*httpTransport, error) {
	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		return nil, fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("url must be http or https")
	}
	return &httpTransport{
		cfg:    cfg,
		client: &http.Client{Timeout: 5 * time.Minute},
		tokens: tokens,
	}, nil
}

func (t *httpTransport) setProtocolVersion(v string) {
	t.mu.Lock()
	t.version = v
	t.mu.Unlock()
}

// setToken installs the OAuth access token used for later requests.
func (t *httpTransport) setToken(token string) {
	t.mu.Lock()
	t.token = token
	t.mu.Unlock()
}

func (t *httpTransport) request(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	t.mu.Lock()
	t.nextID++
	id := t.nextID
	t.mu.Unlock()

	msg := rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", id)), Method: method}
	if params != nil {
		msg.Params = params
	}
	raw, err := t.post(ctx, msg)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("server returned no response to %s", method)
	}
	var rpc rpcMessage
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return nil, err
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("%s (%d)", rpc.Error.Message, rpc.Error.Code)
	}
	return rpc.Result, nil
}

func (t *httpTransport) notify(method string, params map[string]any) error {
	msg := rpcMessage{JSONRPC: "2.0", Method: method}
	if params != nil {
		msg.Params = params
	}
	// A notification has no response, but the request still has to go out; a
	// short deadline keeps a hung server from blocking the turn.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := t.post(ctx, msg)
	return err
}

// close terminates the session, which tells the server it can free any state.
func (t *httpTransport) close() error {
	t.mu.Lock()
	session := t.session
	t.session = ""
	t.mu.Unlock()
	if session == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.cfg.URL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Mcp-Session-Id", session)
	if err := t.authorize(ctx, req); err != nil {
		return nil
	}
	res, err := t.client.Do(req)
	if err != nil {
		return nil // best effort: the session is going away regardless
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	_ = res.Body.Close()
	return nil
}

// authorize applies the configured headers and any OAuth token. An explicitly
// configured Authorization header wins, so a pasted token is never overridden.
func (t *httpTransport) authorize(ctx context.Context, req *http.Request) error {
	for k, v := range t.cfg.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Authorization") != "" {
		return nil
	}
	t.mu.Lock()
	token := t.token
	t.mu.Unlock()
	if token == "" && t.tokens != nil {
		// A grant that cannot be read must not block the request: the server
		// may well accept it anonymously, and the 401 path reports the reason.
		if tok, err := t.tokens.AccessToken(ctx, t.cfg.ID); err == nil {
			token = tok
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return nil
}

// post sends one JSON-RPC message and returns the response that matches it, or
// nil for a notification.
func (t *httpTransport) post(ctx context.Context, msg rpcMessage) (json.RawMessage, error) {
	return t.postAttempt(ctx, msg, false)
}

func (t *httpTransport) postAttempt(ctx context.Context, msg rpcMessage, retried bool) (json.RawMessage, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The transport requires the client to accept both forms: the server picks.
	req.Header.Set("Accept", "application/json, text/event-stream")
	t.mu.Lock()
	session := t.session
	version := t.version
	t.mu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	// The header belongs on every request after initialize.
	if version != "" && msg.Method != "initialize" {
		req.Header.Set("MCP-Protocol-Version", version)
	}
	if err := t.authorize(ctx, req); err != nil {
		return nil, err
	}

	res, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if sid := res.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.session = sid
		t.mu.Unlock()
	}

	if res.StatusCode == http.StatusUnauthorized {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		// A stored grant may simply be stale (revoked, or the token rotated),
		// so drop it and try once more before giving up.
		if t.tokens != nil && !retried {
			t.tokens.Invalidate(t.cfg.ID)
			_ = res.Body.Close()
			return t.postAttempt(ctx, msg, true)
		}
		return nil, t.unauthorized(res.Header.Get("WWW-Authenticate"), snippet)
	}
	// A notification is answered with 202 and no body.
	if res.StatusCode == http.StatusAccepted || res.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return nil, fmt.Errorf("server returned %s: %s", res.Status, strings.TrimSpace(string(snippet)))
	}

	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return readSSEResponse(res.Body, msg.ID)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxHTTPBody))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	return json.RawMessage(raw), nil
}

// label names the server in errors.
func (t *httpTransport) label() string {
	if t.cfg.Name != "" {
		return t.cfg.Name
	}
	if t.cfg.ID != "" {
		return t.cfg.ID
	}
	return t.cfg.URL
}

// unauthorized turns a 401 into an error that says what to do about it.
func (t *httpTransport) unauthorized(challenge string, body []byte) error {
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		detail = strings.TrimSpace(challenge)
	}
	if detail != "" {
		return fmt.Errorf("server %q requires authorization: %s", t.label(), detail)
	}
	return fmt.Errorf("server %q requires authorization", t.label())
}

// readSSEResponse reads an SSE stream until the response matching id arrives.
// A server may interleave unrelated notifications, which are skipped.
func readSSEResponse(r io.Reader, id json.RawMessage) (json.RawMessage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxHTTPBody)
	var data []string

	dispatch := func() (json.RawMessage, bool) {
		if len(data) == 0 {
			return nil, false
		}
		payload := strings.Join(data, "\n")
		data = nil
		var rpc rpcMessage
		if err := json.Unmarshal([]byte(payload), &rpc); err != nil {
			return nil, false // a keep-alive or a non-JSON frame
		}
		if len(rpc.ID) == 0 || string(rpc.ID) != string(id) {
			return nil, false
		}
		return json.RawMessage(payload), true
	}

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if raw, ok := dispatch(); ok {
				return raw, nil
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(rest, " "))
		}
	}
	// A final event may not be terminated by a blank line.
	if raw, ok := dispatch(); ok {
		return raw, nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("the event stream ended without a response")
}
