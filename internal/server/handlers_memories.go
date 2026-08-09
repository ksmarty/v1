package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"v1/internal/store"
)

// memoryBudgetChars caps the memories section injected into the system
// prompt (re-sent with every request of every round, so it stays small).
const memoryBudgetChars = 4000

// memoryPrompt renders the memories section for the system prompt. When the
// total exceeds the budget, the newest memories win and the count of hidden
// older ones is noted so the model knows they exist.
func memoryPrompt(mems []store.Memory) string {
	if len(mems) == 0 {
		return ""
	}
	kept := make([]store.Memory, 0, len(mems))
	total := 0
	for i := len(mems) - 1; i >= 0; i-- {
		line := len(mems[i].Content) + 8 // id, dash and newline
		if total+line > memoryBudgetChars && len(kept) > 0 {
			break
		}
		total += line
		kept = append([]store.Memory{mems[i]}, kept...)
	}
	var b strings.Builder
	b.WriteString("Project memories (facts you saved in earlier turns; use the forget tool with an id to delete one):")
	if hidden := len(mems) - len(kept); hidden > 0 {
		fmt.Fprintf(&b, "\n(%d older memories omitted — they still exist.)", hidden)
	}
	for _, m := range kept {
		fmt.Fprintf(&b, "\n- [%d] %s", m.ID, m.Content)
	}
	return b.String()
}

// memoryContent trims and caps memory writes the same way the remember tool
// does; writes the HTTP error and reports false when invalid.
func memoryContent(w http.ResponseWriter, content string) (string, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return "", false
	}
	if len(content) > 300 {
		writeError(w, http.StatusBadRequest, "memory entries must be 300 characters or fewer")
		return "", false
	}
	return content, true
}

// handleCreateMemory adds a memory manually (same caps/dedup as remember).
func (s *Server) handleCreateMemory(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	content, ok := memoryContent(w, body.Content)
	if !ok {
		return
	}
	mems, err := s.st.ListMemories(p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(content), " "))
	exists := false
	for _, m := range mems {
		if strings.ToLower(strings.Join(strings.Fields(m.Content), " ")) == normalized {
			exists = true
			break
		}
	}
	if !exists {
		if len(mems) >= 200 {
			writeError(w, http.StatusBadRequest, "memory is full (200 entries)")
			return
		}
		if _, err := s.st.AddMemory(p.ID, content); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.respondMemories(w, p.ID)
}

// handleUpdateMemory rewrites a memory's content manually.
func (s *Server) handleUpdateMemory(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("memId"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid memory id")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	content, ok := memoryContent(w, body.Content)
	if !ok {
		return
	}
	if err := s.st.UpdateMemory(p.ID, id, content); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondMemories(w, p.ID)
}

// respondMemories writes the project's refreshed memory list.
func (s *Server) respondMemories(w http.ResponseWriter, projectID string) {
	mems, err := s.st.ListMemories(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if mems == nil {
		mems = []store.Memory{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"memories": mems})
}

// handleListMemories serves the project's saved memories.
func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	s.respondMemories(w, p.ID)
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
