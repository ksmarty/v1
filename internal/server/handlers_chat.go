package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"v1/internal/agent"
	"v1/internal/store"
)

// chatTimeout bounds one full chat turn (all tool rounds). Reasoning streams
// can run long, so it is generous.
const chatTimeout = 15 * time.Minute

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
	type usageJSON struct {
		Input  int64  `json:"input"`
		Output int64  `json:"output"`
		Model  string `json:"model"`
	}
	type messageJSON struct {
		ID        int64           `json:"id"`
		Role      string          `json:"role"`
		Content   string          `json:"content"`
		Tool      json.RawMessage `json:"tool,omitempty"`
		Model     string          `json:"model,omitempty"`
		Reasoning string          `json:"reasoning,omitempty"`
		Usage     *usageJSON      `json:"usage,omitempty"`
		CreatedAt int64           `json:"createdAt"`
	}
	out := []messageJSON{}
	for _, m := range msgs {
		mj := messageJSON{ID: m.ID, Role: m.Role, Content: m.Content, Model: m.Model, Reasoning: m.Reasoning, CreatedAt: m.CreatedAt}
		if m.ToolJSON != "" {
			mj.Tool = json.RawMessage(m.ToolJSON)
		}
		if m.Usage != "" {
			var u usageJSON
			if json.Unmarshal([]byte(m.Usage), &u) == nil {
				mj.Usage = &u
			}
		}
		out = append(out, mj)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetTodos returns the project's agent-maintained todo list.
func (s *Server) handleGetTodos(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	todos, err := s.st.GetTodos(p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"todos": todos})
}

// ---- chat (SSE agent loop) ----

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Message        string `json:"message"`
		Model          string `json:"model"`
		EditMessageID  int64  `json:"editMessageId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	if _, apiKey, _ := s.llmConfig(); apiKey == "" {
		writeError(w, http.StatusBadRequest, "no_api_key")
		return
	}

	params := agent.ChatParams{
		Store:   s.st,
		Project: p,
		Client:  s.llmClient(),
		Message: body.Message,
		Model:   body.Model,
	}
	// Editing an existing user message rewinds the thread to it and re-runs
	// from the edited text: update its content, then run with LastUserID set so
	// history is truncated at it and the message is not re-added.
	if body.EditMessageID > 0 {
		msg, err := s.st.GetMessage(p.ID, body.EditMessageID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "message not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if msg.Role != "user" {
			writeError(w, http.StatusBadRequest, "can only edit a user message")
			return
		}
		if err := s.st.UpdateMessageContent(p.ID, msg.ID, body.Message); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		params.LastUserID = msg.ID
	}

	s.streamChatTurn(w, r, p, params)
}

// handleTruncateMessages deletes every message after the given id — the
// "revert"/rewind action that cuts the thread back to a chosen point.
func (s *Server) handleTruncateMessages(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ID <= 0 {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.st.DeleteMessagesAfter(p.ID, body.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleChatRetry(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	if _, apiKey, _ := s.llmConfig(); apiKey == "" {
		writeError(w, http.StatusBadRequest, "no_api_key")
		return
	}
	last, err := s.st.LastUserMessage(p.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "no_user_turn")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Re-run the last user turn with its stored model, falling back to the
	// current client model when the stored message has none.
	s.streamChatTurn(w, r, p, agent.ChatParams{
		Store:      s.st,
		Project:    p,
		Client:     s.llmClient(),
		Message:    last.Content,
		Model:      last.Model,
		LastUserID: last.ID,
	})
}

// streamChatTurn runs one chat turn and streams it as SSE: reasoning and text
// deltas, tool events, then a done event carrying usage.
func (s *Server) streamChatTurn(w http.ResponseWriter, r *http.Request, p *store.Project, params agent.ChatParams) {
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

	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()

	root, err := filepath.Abs(p.Path)
	if err != nil {
		emit(agent.ChatEvent{Type: "error", Error: err.Error()})
		return
	}
	params.Emit = emit
	params.Exec = &agent.Executor{
		Root:           root,
		ProjectID:      p.ID,
		PreviewCommand: p.PreviewCommand,
		Previews:       s.previews,
		Store:          s.st,
		OnTodos: func(t []store.Todo) {
			emit(agent.ChatEvent{Type: "todos", Todos: t})
		},
	}
	turn, err := agent.RunChat(ctx, params)
	if err != nil {
		emit(agent.ChatEvent{Type: "error", Error: err.Error()})
		return
	}
	emit(agent.ChatEvent{Type: "done", Usage: turn.Usage})
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