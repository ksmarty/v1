package server

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// estSecondsPerTurn is the rough per-turn duration used to estimate how long
// a queued message will wait before its follow-up turn starts.
const estSecondsPerTurn = 60

// handleChatQueue returns the run's queued messages in processing order, or
// an empty list when the session is idle. Each entry carries its position and
// an estimated wait (position * estSecondsPerTurn) so the UI can show the
// user how long their queued message may take. `steering` lists the messages
// that have been steered into the run and are waiting to be injected.
func (s *Server) handleChatQueue(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	messages := []map[string]any{}
	steering := []map[string]any{}
	if q := s.turns.get(p.ID, s.chatSessionID(p, r.URL.Query().Get("sessionId"))); q != nil {
		for i, m := range q.list() {
			messages = append(messages, map[string]any{
				"id":                   m.ID,
				"text":                 m.Text,
				"position":             i + 1,
				"estimatedWaitSeconds": (i + 1) * estSecondsPerTurn,
				"queuedAt":             m.QueuedAt.Format(time.RFC3339),
			})
		}
		for _, m := range q.listSteers() {
			steering = append(steering, map[string]any{"id": m.ID, "text": m.Text})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages, "steering": steering})
}

// handleChatQueueReorder reorders the pending queue to match the given id
// list (a permutation of the current ids).
func (s *Server) handleChatQueueReorder(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		IDs       []string `json:"ids"`
		SessionID string   `json:"sessionId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	q := s.turns.get(p.ID, s.chatSessionID(p, body.SessionID))
	if q == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if err := q.reorder(body.IDs); err != nil {
		if errors.Is(err, errNotQueued) {
			writeError(w, http.StatusBadRequest, "ids must be a permutation of the queued messages")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleChatQueueEdit replaces one queued message's text in place.
func (s *Server) handleChatQueueEdit(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		SessionID string `json:"sessionId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	q := s.turns.get(p.ID, s.chatSessionID(p, body.SessionID))
	if q == nil {
		writeError(w, http.StatusNotFound, "no active run")
		return
	}
	if err := q.edit(body.ID, body.Text); err != nil {
		if errors.Is(err, errNotQueued) {
			writeError(w, http.StatusNotFound, "no such queued message")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleChatQueueHold marks a queued message as being edited (held=true) or
// released (held=false). Held messages are skipped by the follow-up drain, so
// an in-progress edit is never sent.
func (s *Server) handleChatQueueHold(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		ID        string `json:"id"`
		Held      bool   `json:"held"`
		SessionID string `json:"sessionId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	q := s.turns.get(p.ID, s.chatSessionID(p, body.SessionID))
	if q == nil {
		writeError(w, http.StatusNotFound, "no active run")
		return
	}
	if err := q.hold(body.ID, body.Held); err != nil {
		if errors.Is(err, errNotQueued) {
			writeError(w, http.StatusNotFound, "no such queued message")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleChatQueueSteer moves one queued message onto the run's steer stream:
// it is injected at the next round boundary instead of waiting for the queue.
func (s *Server) handleChatQueueSteer(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	q := s.turns.get(p.ID, s.chatSessionID(p, body.SessionID))
	if q == nil {
		writeError(w, http.StatusNotFound, "no active run")
		return
	}
	if err := q.steer(body.ID); err != nil {
		if errors.Is(err, errNotQueued) {
			writeError(w, http.StatusNotFound, "no such queued message")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleChatQueueDelete removes a queued follow-up message so it never gets
// sent. Already-injected steering messages are unaffected.
func (s *Server) handleChatQueueDelete(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	q := s.turns.get(p.ID, s.chatSessionID(p, body.SessionID))
	if q == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if err := q.remove(body.ID); err != nil {
		if errors.Is(err, errNotQueued) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
