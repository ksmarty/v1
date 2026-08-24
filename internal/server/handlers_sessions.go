package server

import (
	"net/http"
	"strings"
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
	if err := s.st.DeleteChatSession(p.ID, r.PathValue("sessionId")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
