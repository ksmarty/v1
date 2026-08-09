package server

import (
	"errors"
	"net/http"
	"strconv"

	"v1/internal/store"
)

// handleListMemories serves the project's saved memories.
func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	mems, err := s.st.ListMemories(p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if mems == nil {
		mems = []store.Memory{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"memories": mems})
}

// handleDeleteMemory removes one of the project's memories.
func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("memId"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid memory id")
		return
	}
	if err := s.st.DeleteMemory(p.ID, id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
