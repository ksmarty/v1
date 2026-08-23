package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"v1/internal/store"
)

// memoryBudgetChars caps the memories section injected into the system
// prompt (re-sent with every request of every round, so it stays small).
const memoryBudgetChars = 4000

// memoryPrompt ranks the project's memories against the incoming user
// message and renders the top entries for the system prompt. Ranking score:
// effective importance (2x), retrieval frequency, and keyword overlap with
// the current message — so what the user is asking about now shows up first.
// Injected memories get touched (last_accessed/access_count bumped) so
// frequently-used entries outrank stale ones over time. Returns "" when no
// memory is worth injecting.
func (s *Server) memoryPrompt(projectID, userMessage string) string {
	mems, err := s.st.ListMemories(projectID)
	if err != nil {
		return ""
	}
	active := make([]store.Memory, 0, len(mems))
	for _, m := range mems {
		if m.Enabled {
			active = append(active, m)
		}
	}
	if len(active) == 0 {
		return ""
	}
	userWords := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(userMessage)) {
		if len(w) > 3 {
			userWords[w] = true
		}
	}
	type scored struct {
		m     store.Memory
		score float64
	}
	scoreds := make([]scored, 0, len(active))
	for _, m := range active {
		score := 2*m.Importance + 0.5*float64(m.AccessCount)
		content := strings.ToLower(m.Content)
		for w := range userWords {
			if strings.Contains(content, w) {
				score += 2
			}
		}
		scoreds = append(scoreds, scored{m, score})
	}
	sort.SliceStable(scoreds, func(i, j int) bool { return scoreds[i].score > scoreds[j].score })
	// Inject at most the top 5, within the character budget.
	var sb strings.Builder
	sb.WriteString("Project memories (facts you saved in earlier turns; use the forget tool with an id to delete one):")
	total := sb.Len()
	kept := 0
	hidden := 0
	for _, sc := range scoreds {
		line := fmt.Sprintf("\n- [%d] %s: %s", sc.m.ID, sc.m.Category, sc.m.Content)
		if kept >= 5 || total+len(line) > memoryBudgetChars {
			hidden++
			continue
		}
		sb.WriteString(line)
		total += len(line)
		kept++
		_ = s.st.TouchMemory(sc.m.ID)
	}
	if kept == 0 {
		return ""
	}
	return sb.String()
}

// planPrompt renders the active plan section for the system prompt.
func (s *Server) planPrompt(projectID string) string {
	plan, ok, err := s.st.GetPlan(projectID)
	if err != nil || !ok {
		return ""
	}
	return "## Active Plan\n" + plan
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
		if _, err := s.st.AddMemory(p.ID, content, "fact", 1); err != nil {
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

// handleToggleMemory enables/disables a memory without deleting it.
func (s *Server) handleToggleMemory(w http.ResponseWriter, r *http.Request) {
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
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.st.SetMemoryEnabled(p.ID, id, body.Enabled); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.respondMemories(w, p.ID)
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
