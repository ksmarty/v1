package server

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"v1/internal/agent"
	"v1/internal/store"
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
	if q := s.turns.queueFor(p.ID, s.chatSessionID(p, r.URL.Query().Get("sessionId"))); q != nil {
		for i, m := range q.list() {
			entry := map[string]any{
				"id":                   m.ID,
				"text":                 m.Text,
				"position":             i + 1,
				"estimatedWaitSeconds": (i + 1) * estSecondsPerTurn,
				"queuedAt":             m.QueuedAt.Format(time.RFC3339),
			}
			// A queued message can carry files; the UI shows them as chips so the
			// queue reads like the message the user actually composed.
			if metas := agent.AttachmentMetas(m.Attachments); len(metas) > 0 {
				entry["attachments"] = metas
			}
			messages = append(messages, entry)
		}
		for _, m := range q.listSteers() {
			entry := map[string]any{"id": m.ID, "text": m.Text}
			if metas := agent.AttachmentMetas(m.Attachments); len(metas) > 0 {
				entry["attachments"] = metas
			}
			steering = append(steering, entry)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages, "steering": steering})
}

// handleChatErrorsClear drops the persisted failure rows of a session. Sending
// anything after an error means the user ignored it, and a transcript that
// keeps the failure in place reads as if the chat stopped working there: every
// later turn sits below a red box that no longer describes the conversation.
func (s *Server) handleChatErrorsClear(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	sessionID := s.chatSessionID(p, r.URL.Query().Get("sessionId"))
	n, err := s.st.DeleteErrorMessages(p.ID, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleared": n})
}

// handleChatQueueResume starts the oldest stranded queued message as a turn.
// A queue can outlive the run it was queued on — the provider failed, the
// server restarted, the client was away for hours — and it is persisted
// precisely so that this can pick it up again. The message is taken out of the
// queue before the turn starts (so it can never be sent twice), and the rest of
// the queue is hydrated into the new run, which runs it as follow-up turns.
func (s *Server) handleChatQueueResume(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	userID := s.currentUser(r).ID
	sessionID := s.chatSessionID(p, r.URL.Query().Get("sessionId"))
	if s.turns.running(p.ID, sessionID) {
		// Something is already running: the queue is that run's business.
		writeJSON(w, http.StatusAccepted, map[string]any{"running": true})
		return
	}
	rows, err := s.st.ListQueuedMessages(p.ID, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Steered rows are injected into a run, and a held row is being edited in
	// the composer: neither is a turn to start here.
	var pick *store.QueuedMessage
	rest := make([]store.QueuedMessage, 0, len(rows))
	for i := range rows {
		if pick == nil && !rows[i].Steer && !rows[i].Held {
			pick = &rows[i]
			continue
		}
		rest = append(rest, rows[i])
	}
	if pick == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": false})
		return
	}
	providerID := r.URL.Query().Get("providerId")
	model := r.URL.Query().Get("model")
	if !s.requireLLMKey(w, userID, providerID) {
		return
	}
	atts := agent.ParseAttachments(pick.Attachments)
	params := agent.ChatParams{
		Store:           s.st,
		Project:         p,
		Client:          s.llmClientFor(userID, providerID),
		Message:         pick.Text,
		SessionID:       sessionID,
		Attachments:     atts,
		Model:           model,
		Vision:          s.modelSupportsImages(userID, providerID, model),
		SoftTimeout:     s.cfg.TurnSoftTimeout,
		HardTimeout:     s.cfg.TurnHardTimeout,
		AutoPlan:        s.cfg.AutoPlan,
	}
	q, started, _, qerr := s.turns.beginOrQueue(p.ID, sessionID, pick.Text, atts, userID, s.cfg.MaxConcurrentRunsPerUser)
	if qerr != nil || !started {
		// Nothing lost: put the row back where it was and let the next attempt
		// pick it up. The client reports queued, not an error.
		if serr := s.st.SaveQueuedMessages(p.ID, sessionID, append([]store.QueuedMessage{*pick}, rest...)); serr != nil {
			log.Printf("queue: restoring %s: %v", pick.ID, serr)
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
		return
	}
	// The turn starts from the row itself, so the persisted copy must go before
	// any of it runs.
	if _, derr := s.st.DeleteQueuedMessage(p.ID, sessionID, pick.ID); derr != nil {
		log.Printf("queue: dropping resumed %s: %v", pick.ID, derr)
	}
	s.streamChatTurn(w, r, p, userID, params, q)
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
	q := s.turns.queueFor(p.ID, s.chatSessionID(p, body.SessionID))
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
	q := s.turns.queueFor(p.ID, s.chatSessionID(p, body.SessionID))
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
	q := s.turns.queueFor(p.ID, s.chatSessionID(p, body.SessionID))
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
	q := s.turns.queueFor(p.ID, s.chatSessionID(p, body.SessionID))
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
	q := s.turns.queueFor(p.ID, s.chatSessionID(p, body.SessionID))
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
