package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"v1/internal/store"
)

// handleListSessions returns the project's chat sessions, creating the
// default one on first access so every project has at least one thread.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	if _, err := s.st.EnsureDefaultSession(p.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sessions, err := s.st.ListSessions(p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// handleCreateSession starts a new chat session for the project. Empty names
// are auto-numbered ("Session 2", …).
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	cs, err := s.st.CreateChatSession(p.ID, body.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": cs})
}

// handleForkSession copies a chat session's transcript into a new session, so
// the user can take the conversation in another direction without disturbing
// the original. The fork inherits the model's view too: the durable harness
// conversation is forked in the sidecar and the copy is recorded on the new
// session, otherwise its first turn would reach the model with no history.
func (s *Server) handleForkSession(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	src := r.PathValue("sessionId")
	// A fork while a turn runs would copy a transcript the sidecar is still
	// writing, and the durable fork could land before the turn's answer does.
	if s.turns.get(p.ID, src) != nil {
		writeError(w, http.StatusConflict, "run_active")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	forked, err := s.st.ForkChatSession(p.ID, src, strings.TrimSpace(body.Name))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bridge := s.harnessBridge(); bridge != nil {
		if srcConv, err := s.st.HarnessConversationID(p.ID, src); err == nil && srcConv != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			if convID, err := bridge.ForkConversation(ctx, srcConv); err != nil {
				// The v1 transcript is still a correct fork; the model would only
				// start its durable history over, so log rather than fail the request.
				log.Printf("fork session: copying the durable conversation failed: %v", err)
			} else if err := s.st.SetHarnessConversationID(p.ID, forked.ID, convID); err != nil {
				log.Printf("fork session: persisting the forked conversation id failed: %v", err)
			}
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": forked})
}

// handleRenameSession renames one of the project's chat sessions.
func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.st.RenameChatSession(p.ID, r.PathValue("sessionId"), name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleArchiveSession hides a chat session from the switcher (its history is
// kept and it can be restored).
func (s *Server) handleArchiveSession(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	if err := s.st.ArchiveChatSession(p.ID, r.PathValue("sessionId")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleUnarchiveSession restores an archived chat session to the active list.
func (s *Server) handleUnarchiveSession(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	if err := s.st.UnarchiveChatSession(p.ID, r.PathValue("sessionId")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteSession deletes a chat session and its messages.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	sessionID := r.PathValue("sessionId")
	// ifEmpty=1 is the cleanup path: a chat that was opened and left without
	// anything being said in it. The check lives here rather than in the client
	// because only the store knows whether a message landed in the meantime — a
	// background command reports into its session whether or not anyone is
	// looking at it.
	if r.URL.Query().Get("ifEmpty") == "1" {
		n, err := s.st.SessionMessageCount(p.ID, sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if n > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": false})
			return
		}
	}
	if err := s.st.DeleteChatSession(p.ID, sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": true})
}
