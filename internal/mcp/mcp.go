// Package mcp implements a minimal Model Context Protocol client that spawns
// MCP servers as subprocesses and talks JSON-RPC (newline-delimited) over
// stdio, plus a Manager that keeps configured servers connected.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"v1/internal/llm"
)

// ServerConfig describes one MCP server the user configured. Command may be a
// bare executable or an npx-style launcher (e.g. npx -y some-server).
type ServerConfig struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// Enabled is nil when the field predates the toggle; nil means enabled.
	Enabled *bool `json:"enabled,omitempty"`
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

// Client is a connected MCP server subprocess.
type Client struct {
	cfg     ServerConfig
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage
	done    chan struct{}
}

// Connect starts the server subprocess and performs the MCP handshake.
func Connect(ctx context.Context, cfg ServerConfig) (*Client, error) {
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
	// Keep stderr in a small buffer for diagnostics instead of surfacing it.
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", cfg.Command, err)
	}
	c := &Client{
		cfg:     cfg,
		cmd:     cmd,
		stdin:   stdin,
		pending: map[int]chan json.RawMessage{},
		done:    make(chan struct{}),
	}
	go c.readLoop(stdout)
	if err := c.handshake(ctx); err != nil {
		_ = c.Close()
		msg := err.Error()
		if e := errBuf.String(); e != "" {
			msg = fmt.Sprintf("%s (stderr: %s)", msg, strings.TrimSpace(e))
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return c, nil
}

func (c *Client) handshake(ctx context.Context) error {
	if _, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "v1", "version": "0.1.0"},
	}); err != nil {
		return fmt.Errorf("initialize: %w", err)
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

// Close stops the server subprocess.
func (c *Client) Close() error {
	select {
	case <-c.done:
		return nil
	default:
	}
	close(c.done)
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
	return nil
}

// notify sends a notification (no response expected).
func (c *Client) notify(method string, params map[string]any) {
	_ = c.writeLine(rpcMessage{JSONRPC: "2.0", Method: method, Params: params})
}

// request sends a request and waits for its response.
func (c *Client) request(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	msg := rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", id)), Method: method}
	if params != nil {
		msg.Params = params
	}
	if err := c.writeLine(msg); err != nil {
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
	case <-c.done:
		return nil, fmt.Errorf("connection closed by server")
	}
}

func (c *Client) writeLine(msg rpcMessage) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (c *Client) readLoop(r io.Reader) {
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
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- []byte(line):
			default:
			}
		}
	}
	// Stream ended: fail all pending requests.
	c.mu.Lock()
	for _, ch := range c.pending {
		ch <- nil
	}
	c.pending = map[int]chan json.RawMessage{}
	c.mu.Unlock()
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
	load func() []ServerConfig

	mu      sync.Mutex
	clients map[string]*entry
}

// NewManager creates a manager that loads its server list from load on each
// Sync call (so config changes are picked up without a restart).
func NewManager(load func() []ServerConfig) *Manager {
	return &Manager{load: load, clients: map[string]*entry{}}
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
		if e != nil && e.cfg.Command == cfg.Command && equalStrings(e.cfg.Args, cfg.Args) && e.cl != nil {
			for _, t := range e.tools {
				tools = append(tools, t.ToLLMTool(cfg.ID))
			}
			continue
		}
		if e != nil && e.failed && time.Since(e.failAt) < 30*time.Second {
			continue
		}
		connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cl, err := Connect(connectCtx, cfg)
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
