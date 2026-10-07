// Package mcp implements a minimal Model Context Protocol client that spawns
// MCP servers as subprocesses and talks JSON-RPC (newline-delimited) over
// stdio, plus a Manager that keeps configured servers connected.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"v1/internal/llm"
)

// ServerConfig describes one MCP server the user configured. A config with a
// URL is reached over the streamable HTTP transport; otherwise Command is
// spawned as a subprocess (a bare executable or an npx-style launcher).
type ServerConfig struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// URL selects the streamable HTTP transport. When set, Command and Args
	// are ignored.
	URL string `json:"url,omitempty"`
	// Headers are sent with every HTTP request, for servers that authenticate
	// with a static key.
	Headers map[string]string `json:"headers,omitempty"`
	// Enabled is nil when the field predates the toggle; nil means enabled.
	Enabled *bool `json:"enabled,omitempty"`
}

// sameConnection reports whether two configs describe the same connection, so
// that a running server can be reused. The name and the enabled flag do not
// change what is on the other end.
func (c ServerConfig) sameConnection(other ServerConfig) bool {
	return c.Command == other.Command &&
		c.URL == other.URL &&
		equalStrings(c.Args, other.Args) &&
		equalHeaders(c.Headers, other.Headers)
}

// IsEnabled reports whether the server should be connected. Missing means
// enabled — configs saved before the toggle existed keep working.
func (c ServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// Tool is one tool exposed by an MCP server.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

const protocolVersion = "2024-11-05"

// rpcMessage is the JSON-RPC wire shape.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  map[string]any  `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// transport carries JSON-RPC messages to one MCP server.
type transport interface {
	// request sends a request and returns its raw result.
	request(ctx context.Context, method string, params map[string]any) (json.RawMessage, error)
	// notify sends a fire-and-forget notification.
	notify(method string, params map[string]any) error
	// close releases the transport.
	close() error
}

// stderrReporter is implemented by transports that can explain a failed
// handshake with what the server printed.
type stderrReporter interface {
	stderr() string
}

// Client is a connected MCP server, over either transport.
type Client struct {
	cfg ServerConfig
	tr  transport
}

// TokenSource supplies a bearer token for a remote server, refreshing it when
// it has expired. The server implements this against its stored grants; a nil
// source means the server needs no OAuth.
type TokenSource interface {
	AccessToken(ctx context.Context, serverID string) (string, error)
	// Invalidate drops the cached token so the next call refreshes it.
	Invalidate(serverID string)
}

// Option configures Connect.
type Option func(*connectOptions)

type connectOptions struct {
	tokens TokenSource
}

// WithTokenSource authenticates a remote connection with a stored OAuth grant.
func WithTokenSource(ts TokenSource) Option {
	return func(o *connectOptions) { o.tokens = ts }
}

// Connect opens the configured transport and performs the MCP handshake.
func Connect(ctx context.Context, cfg ServerConfig, opts ...Option) (*Client, error) {
	var o connectOptions
	for _, opt := range opts {
		opt(&o)
	}
	tr, err := dial(ctx, cfg, o.tokens)
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, tr: tr}
	if err := c.handshake(ctx); err != nil {
		_ = tr.close()
		msg := err.Error()
		if sr, ok := tr.(stderrReporter); ok {
			if e := sr.stderr(); e != "" {
				msg = fmt.Sprintf("%s (stderr: %s)", msg, strings.TrimSpace(e))
			}
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return c, nil
}

// dial picks the transport the config describes: a URL means the streamable
// HTTP transport, otherwise the server is spawned as a subprocess.
func dial(ctx context.Context, cfg ServerConfig, tokens TokenSource) (transport, error) {
	if strings.TrimSpace(cfg.URL) != "" {
		return dialHTTP(cfg, tokens)
	}
	return dialStdio(ctx, cfg)
}

func (c *Client) handshake(ctx context.Context) error {
	raw, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "v1", "version": "0.1.0"},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	// The server may negotiate a different version, and the HTTP transport has
	// to echo the negotiated one back on every later request.
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &res) == nil && res.ProtocolVersion != "" {
		if vs, ok := c.tr.(protocolVersionSetter); ok {
			vs.setProtocolVersion(res.ProtocolVersion)
		}
	}
	// Server notifications are fire-and-forget; the initialized one tells the
	// server we are ready for requests.
	c.notify("notifications/initialized", nil)
	return nil
}

// ListTools returns the server's advertised tools.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.request(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

// CallTool invokes a tool and returns a plain-text rendering of the result.
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (string, error) {
	raw, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return "", err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", err
	}
	var parts []string
	for _, c := range res.Content {
		parts = append(parts, c.Text)
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if res.IsError {
		if text == "" {
			text = "MCP server returned an error"
		}
		return "", fmt.Errorf("%s", text)
	}
	if text == "" {
		text = fmt.Sprintf("ok (no text content, %d blocks)", len(res.Content))
	}
	return text, nil
}

// Close releases the connection.
func (c *Client) Close() error {
	return c.tr.close()
}

// notify sends a notification (no response expected).
func (c *Client) notify(method string, params map[string]any) {
	_ = c.tr.notify(method, params)
}

// request sends a request and waits for its response.
func (c *Client) request(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return c.tr.request(ctx, method, params)
}

// entry is one connected server in the Manager.
type entry struct {
	cfg    ServerConfig
	cl     *Client
	tools  []Tool
	failed bool
	failAt time.Time
}

// Manager keeps MCP servers connected and exposes their tools to the agent.
type Manager struct {
	load   func() []ServerConfig
	tokens TokenSource

	mu      sync.Mutex
	clients map[string]*entry
}

// NewManager creates a manager that loads its server list from load on each
// Sync call (so config changes are picked up without a restart).
func NewManager(load func() []ServerConfig, opts ...Option) *Manager {
	var o connectOptions
	for _, opt := range opts {
		opt(&o)
	}
	return &Manager{load: load, tokens: o.tokens, clients: map[string]*entry{}}
}

// Sync connects to every configured server (skipping recent failures and
// already-connected ones), disconnects removed servers and returns the union
// of live tools as agent-ready tools namespaced mcp_<server>_<tool>.
func (m *Manager) Sync(ctx context.Context) ([]llm.Tool, error) {
	cfgList := []ServerConfig{}
	if m.load != nil {
		cfgList = m.load()
	}
	m.mu.Lock()
	seen := map[string]bool{}
	var tools []llm.Tool
	for _, cfg := range cfgList {
		seen[cfg.ID] = true
		if !cfg.IsEnabled() {
			// Disabled: never connect, and tear down a leftover connection.
			if e := m.clients[cfg.ID]; e != nil && e.cl != nil {
				_ = e.cl.Close()
			}
			delete(m.clients, cfg.ID)
			continue
		}
		e := m.clients[cfg.ID]
		if e != nil && e.cfg.sameConnection(cfg) && e.cl != nil {
			for _, t := range e.tools {
				tools = append(tools, t.ToLLMTool(cfg.ID))
			}
			continue
		}
		if e != nil && e.failed && time.Since(e.failAt) < 30*time.Second {
			continue
		}
		connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cl, err := Connect(connectCtx, cfg, WithTokenSource(m.tokens))
		cancel()
		if err != nil {
			m.clients[cfg.ID] = &entry{cfg: cfg, failed: true, failAt: time.Now()}
			continue
		}
		toolsList, err := cl.ListTools(ctx)
		if err != nil {
			_ = cl.Close()
			m.clients[cfg.ID] = &entry{cfg: cfg, failed: true, failAt: time.Now()}
			continue
		}
		m.clients[cfg.ID] = &entry{cfg: cfg, cl: cl, tools: toolsList}
		for _, t := range toolsList {
			tools = append(tools, t.ToLLMTool(cfg.ID))
		}
	}
	for id, e := range m.clients {
		if !seen[id] {
			if e.cl != nil {
				_ = e.cl.Close()
			}
			delete(m.clients, id)
		}
	}
	m.mu.Unlock()
	return tools, nil
}

// CallTool invokes toolName on the server with the given id.
func (m *Manager) CallTool(ctx context.Context, serverID, toolName string, arguments map[string]any) (string, error) {
	m.mu.Lock()
	e := m.clients[serverID]
	m.mu.Unlock()
	if e == nil || e.cl == nil {
		return "", fmt.Errorf("MCP server %q is not connected", serverID)
	}
	return e.cl.CallTool(ctx, toolName, arguments)
}

// Status returns the connection state of each configured server without
// attempting any connection.
func (m *Manager) Status() []map[string]any {
	out := []map[string]any{}
	cfgList := []ServerConfig{}
	if m.load != nil {
		cfgList = m.load()
	}
	m.mu.Lock()
	for _, cfg := range cfgList {
		e := m.clients[cfg.ID]
		st := map[string]any{"id": cfg.ID, "name": cfg.Name, "enabled": cfg.IsEnabled(), "connected": false, "toolCount": 0}
		if e != nil {
			st["connected"] = e.cl != nil
			st["toolCount"] = len(e.tools)
		}
		out = append(out, st)
	}
	m.mu.Unlock()
	return out
}

// Shutdown disconnects every server.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	for _, e := range m.clients {
		if e.cl != nil {
			_ = e.cl.Close()
		}
	}
	m.clients = map[string]*entry{}
	m.mu.Unlock()
}

// ToLLMTool adapts an MCP tool into the agent's tool schema, namespaced so it
// cannot collide with the built-in tools.
func (t Tool) ToLLMTool(serverID string) llm.Tool {
	params := t.InputSchema
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	desc := t.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool %s from server %s", t.Name, serverID)
	} else {
		desc += fmt.Sprintf(" (MCP server %s)", serverID)
	}
	return llm.Tool{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "mcp_" + serverID + "_" + t.Name,
			Description: desc,
			Parameters:  params,
		},
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalHeaders(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
