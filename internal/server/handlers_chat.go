package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"v1/internal/agent"
)

// ---- messages ----

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	msgs, err := s.st.ListMessages(p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type messageJSON struct {
		ID        int64           `json:"id"`
		Role      string          `json:"role"`
		Content   string          `json:"content"`
		Tool      json.RawMessage `json:"tool,omitempty"`
		CreatedAt int64           `json:"createdAt"`
	}
	out := []messageJSON{}
	for _, m := range msgs {
		mj := messageJSON{ID: m.ID, Role: m.Role, Content: m.Content, CreatedAt: m.CreatedAt}
		if m.ToolJSON != "" {
			mj.Tool = json.RawMessage(m.ToolJSON)
		}
		out = append(out, mj)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- chat (SSE agent loop) ----

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emit := func(ev agent.ChatEvent) {
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	root, err := filepath.Abs(p.Path)
	if err != nil {
		emit(agent.ChatEvent{Type: "error", Error: err.Error()})
		return
	}
	exec := &agent.Executor{
		Root:           root,
		ProjectID:      p.ID,
		PreviewCommand: p.PreviewCommand,
		Previews:       s.previews,
	}
	err = agent.RunChat(r.Context(), agent.ChatParams{
		Store:   s.st,
		Project: p,
		Client:  s.llmClient(),
		Exec:    exec,
		Message: body.Message,
		Emit:    emit,
	})
	if err != nil {
		emit(agent.ChatEvent{Type: "error", Error: err.Error()})
		return
	}
	emit(agent.ChatEvent{Type: "done"})
}

// ---- preview control ----

func (s *Server) handlePreviewStatus(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	running, url, logs := s.previews.Status(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"running": running,
		"url":     url,
		"logs":    logs,
	})
}

func (s *Server) handlePreviewStart(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	url, err := s.previews.Start(p.ID, p.Path, p.PreviewCommand)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url})
}

func (s *Server) handlePreviewStop(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	s.previews.Stop(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- terminal ----

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	s.terminals.ServeWS(w, r, p.ID, p.Path)
}
