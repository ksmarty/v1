package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"v1/internal/agent"
	"v1/internal/mcp"
	"v1/internal/store"
)

// ---- MCP server settings ----

func (s *Server) mcpServers() []mcp.ServerConfig {
	v, ok, _ := s.st.GetSetting(keyMCP)
	if !ok || v == "" {
		return nil
	}
	var out []mcp.ServerConfig
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil
	}
	return out
}

func (s *Server) saveMCPServers(servers []mcp.ServerConfig) error {
	if servers == nil {
		servers = []mcp.ServerConfig{}
	}
	raw, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	return s.st.SetSetting(keyMCP, string(raw))
}

// handleMCPTest connects to a candidate server config and reports its tools,
// without saving anything.
func (s *Server) handleMCPTest(w http.ResponseWriter, r *http.Request) {
	var body mcp.ServerConfig
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ID == "" || body.Command == "" {
		writeError(w, http.StatusBadRequest, "id and command are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	cl, err := mcp.Connect(ctx, body)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer cl.Close()
	tools, err := cl.ListTools(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tools": out})
}

// handleMCPStatus reports the connection state of configured servers.
func (s *Server) handleMCPStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"servers": s.mcp.Status()})
}

// ---- permission policy (allow / deny / ask) ----

// toolPolicy returns the tool permission policy map. Keys are exact tool
// names ("run_command", "mcp.<server>.<tool>"), with "mcp.*" and "*" as
// wildcards. The built-in default is "allow".
func (s *Server) toolPolicy() map[string]string {
	v, ok, _ := s.st.GetSetting(keyToolPolicy)
	if !ok || v == "" {
		return map[string]string{}
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return map[string]string{}
	}
	return out
}

func (s *Server) setToolPolicy(tool, policy string) error {
	p := s.toolPolicy()
	switch policy {
	case "allow", "deny", "ask":
		p[tool] = policy
	case "":
		delete(p, tool)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.st.SetSetting(keyToolPolicy, string(raw))
}

func (s *Server) toolPolicyFor(tool string) string {
	p := s.toolPolicy()
	if v, ok := p[tool]; ok && v != "" {
		return v
	}
	if strings.HasPrefix(tool, "mcp.") {
		if v, ok := p["mcp.*"]; ok && v != "" {
			return v
		}
	}
	if v, ok := p["*"]; ok && v != "" {
		return v
	}
	return "allow"
}

// permRequest is a pending "ask" decision.
type permRequest struct {
	ch   chan bool
	tool string
}

// permRegistry tracks in-flight permission requests by id so the chat UI can
// answer them.
type permRegistry struct {
	mu   sync.Mutex
	reqs map[string]*permRequest
}

func (r *permRegistry) register(id, tool string) *permRequest {
	pr := &permRequest{ch: make(chan bool, 1), tool: tool}
	r.mu.Lock()
	r.reqs[id] = pr
	r.mu.Unlock()
	return pr
}

func (r *permRegistry) resolve(id string, allow bool) *permRequest {
	r.mu.Lock()
	pr := r.reqs[id]
	delete(r.reqs, id)
	r.mu.Unlock()
	if pr != nil {
		pr.ch <- allow
	}
	return pr
}

// turnPerm implements agent.Resolver for one chat stream: it reads the
// policy from settings, and surfaces "ask" decisions as SSE events answered
// through the permission endpoint.
type turnPerm struct {
	s    *Server
	emit func(agent.ChatEvent)
}

func (t *turnPerm) Request(ctx context.Context, tool, detail string) (bool, error) {
	switch t.s.toolPolicyFor(tool) {
	case "deny":
		return false, fmt.Errorf("permission denied: %s is blocked by policy", tool)
	case "ask":
		id := store.NewID()
		pr := t.s.perm.register(id, tool)
		if t.emit != nil {
			t.emit(agent.ChatEvent{Type: "permission_request", RequestID: id, Tool: tool, Detail: detail})
		}
		select {
		case allow := <-pr.ch:
			if !allow {
				return false, fmt.Errorf("permission denied: %s was declined", tool)
			}
			return true, nil
		case <-time.After(2 * time.Minute):
			return false, fmt.Errorf("permission request for %s timed out", tool)
		case <-ctx.Done():
			return false, ctx.Err()
		}
	default:
		return true, nil
	}
}

// handlePermission answers a pending permission request. When remember is
// set, the decision is persisted as the tool's policy.
func (s *Server) handlePermission(w http.ResponseWriter, r *http.Request) {
	if s.projectOr404(w, r) == nil {
		return
	}
	var body struct {
		RequestID string `json:"requestId"`
		Allow     bool   `json:"allow"`
		Remember  bool   `json:"remember"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeError(w, http.StatusBadRequest, "requestId is required")
		return
	}
	pr := s.perm.resolve(body.RequestID, body.Allow)
	if pr == nil {
		writeError(w, http.StatusNotFound, "no pending permission request with that id")
		return
	}
	if body.Remember {
		policy := "deny"
		if body.Allow {
			policy = "allow"
		}
		if err := s.setToolPolicy(pr.tool, policy); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
