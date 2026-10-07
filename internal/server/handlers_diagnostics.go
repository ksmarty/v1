package server

import (
	"encoding/json"
	"net/http"

	"v1/internal/agent"
)

// handleDiagnostics returns a debug dump for one project's chat session —
// build version, the effective LLM provider (API key masked), run state, and
// the session's messages — so a chat that ended unexpectedly can be diagnosed
// without shelling into the host. Attachment bytes are omitted; only names and
// sizes are reported.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	userID := s.currentUser(r).ID
	requestedSessionID := r.URL.Query().Get("sessionId")
	sessionID := s.chatSessionID(p, requestedSessionID)

	out := map[string]any{
		"project": map[string]any{"id": p.ID, "name": p.Name},
		"session": sessionID,
		// What the client asked for. When this differs from "session" the pane
		// is being shown a different session than the one it requested, which is
		// itself a diagnosis.
		"sessionRequested": requestedSessionID,
		"version":          s.cfg.Version,
		"commit":           s.cfg.Commit,
		"provider":         s.diagnosticsProvider(userID),
	}
	if q := s.turns.get(p.ID, sessionID); q != nil {
		queued := []map[string]any{}
		for _, m := range q.list() {
			queued = append(queued, map[string]any{"id": m.ID, "text": m.Text})
		}
		steering := []map[string]any{}
		for _, m := range q.listSteers() {
			steering = append(steering, map[string]any{"id": m.ID, "text": m.Text})
		}
		out["run"] = map[string]any{"running": true, "queued": queued, "steering": steering}
	} else {
		out["run"] = map[string]any{"running": false}
	}

	msgs, err := s.st.ListMessages(p.ID, sessionID)
	if err != nil {
		// Never drop this. An empty history and a failed read look identical in
		// the UI — the pane shows an error either way — and which one it was
		// decides the diagnosis.
		out["messagesError"] = err.Error()
	} else {
		list := make([]map[string]any, 0, len(msgs))
		roles := map[string]int{}
		for _, m := range msgs {
			roles[m.Role]++
			list = append(list, map[string]any{
				"id":          m.ID,
				"role":        m.Role,
				"content":     m.Content,
				"toolJson":    m.ToolJSON,
				"model":       m.Model,
				"usage":       m.Usage,
				"reasoning":   m.Reasoning,
				"attachments": diagnosticsAttachments(m.Attachments),
			})
		}
		out["messages"] = list
		out["messageCount"] = len(msgs)
		// The role histogram makes an unexpected row visible at a glance: the
		// chat view renders a row whose role it does not know — a persisted
		// failure, for instance — as its raw content.
		out["messageRoles"] = roles
	}

	// What the browser saw. Without this the export is server-only, and a
	// failure that happened while loading or rendering the session leaves no
	// trace in it at all.
	if client, entries := s.clientLogs.get(userID); client != nil || len(entries) > 0 {
		out["clientLog"] = map[string]any{"client": client, "entries": entries}
	}
	writeJSON(w, http.StatusOK, out)
}

// diagnosticsProvider reports the effective LLM provider: the first saved
// provider when the user has any, otherwise the legacy keyed settings. The
// API key is masked to its first and last four characters.
func (s *Server) diagnosticsProvider(userID string) map[string]any {
	if ps := s.llmProviders(userID); len(ps) > 0 {
		p := ps[0]
		return map[string]any{
			"id": p.ID, "name": p.Name, "baseURL": p.BaseURL, "model": p.Model,
			"apiKey": maskKey(p.APIKey),
		}
	}
	baseURL, apiKey, model := s.llmConfig(userID)
	return map[string]any{
		"name": "legacy settings", "baseURL": baseURL, "model": model, "apiKey": maskKey(apiKey),
	}
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return ""
	}
	return key[:4] + "…" + key[len(key)-4:]
}

// diagnosticsAttachments summarizes stored attachments without their bytes.
func diagnosticsAttachments(raw string) []map[string]any {
	if raw == "" {
		return nil
	}
	var atts []agent.Attachment
	if err := json.Unmarshal([]byte(raw), &atts); err != nil {
		return []map[string]any{{"name": "<unparseable>"}}
	}
	out := make([]map[string]any, 0, len(atts))
	for _, a := range atts {
		out = append(out, map[string]any{
			"name": a.Name, "mime": a.MIME, "kind": a.Kind, "size": len(a.Content),
		})
	}
	return out
}
