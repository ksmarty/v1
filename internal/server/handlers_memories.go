package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	v1embed "v1/internal/embed"
	"v1/internal/memory"
	"v1/internal/store"
)

// memoryBudgetChars caps the memories section injected into the system prompt
// (re-sent with every request of every round, so it stays bounded).
//
// It bounds what is *injected*, not what is *stored*: since retrieval is ranked,
// a memory only spends this budget in the turns where it is actually relevant.
// That is why memories no longer have a length limit.
const memoryBudgetChars = 4000

// memoryTopK is how many memories the ranker may inject in one turn.
// opencode-mem injects 3, ordered by recency; v1 ranks by relevance against the
// current message instead, so a slightly larger K is both affordable and useful.
const memoryTopK = 8

// embeddingBackfillBatch bounds how many old memories one turn will embed.
const embeddingBackfillBatch = 25

// embeddingBackfillRunning guards the background backfill so two concurrent
// turns do not duplicate the same work.
var embeddingBackfillRunning atomic.Bool

// backfillEmbeddings embeds the project's memories that have no vector yet, or
// whose vector came from a different model than the one configured now.
//
// It runs in the background, because it is an upgrade rather than a requirement:
// until a memory has a vector it is still found lexically, so nothing breaks
// while this catches up. Bounded per call, so a project with hundreds of old
// memories does not spend an embedding budget in a single turn.
func (s *Server) backfillEmbeddings(projectID, userID string) {
	cfg := s.embedConfigFor(userID)
	if !cfg.Enabled() || !embeddingBackfillRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer embeddingBackfillRunning.Store(false)
		ids, err := s.st.MemoriesMissingEmbeddings(projectID, cfg.Model)
		if err != nil || len(ids) == 0 {
			return
		}
		if len(ids) > embeddingBackfillBatch {
			ids = ids[:embeddingBackfillBatch]
		}
		client := v1embed.New(cfg)
		for _, id := range ids {
			content, err := s.st.MemoryContent(id)
			if err != nil || strings.TrimSpace(content) == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			vec, err := client.EmbedDocument(ctx, content)
			cancel()
			if err != nil {
				// Stop the whole batch rather than hammering a provider that is
				// refusing: the next turn retries from the same place.
				log.Printf("memory: embedding memory %d failed: %v", id, err)
				return
			}
			if err := s.st.SetMemoryEmbedding(id, cfg.Model, vec); err != nil {
				log.Printf("memory: storing the embedding for memory %d failed: %v", id, err)
				return
			}
		}
	}()
}

// memoryPrompt ranks the project's memories against the incoming user message
// and renders the best ones for the system prompt.
//
// Ranking is semantic when the user has configured an embedding provider: the
// message is embedded and compared against each memory's stored vector, blended
// with a tag match and a lexical term (see internal/memory). With no provider it
// falls back to those lexical signals alone — which is what v1 did before
// embeddings existed, minus the part where the most important memories were
// injected whether or not they had anything to do with the message.
//
// Injected memories are touched so their access counts keep feeding the recency
// and frequency terms. Returns "" when nothing clears the relevance floor.
func (s *Server) memoryPrompt(projectID, userID, userMessage string) string {
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

	q := memory.Query{Text: userMessage, Now: time.Now()}
	if cfg := s.embedConfigFor(userID); cfg.Enabled() {
		// A query embedding failure degrades to lexical ranking rather than
		// dropping the section: the memories are still worth injecting, and a
		// provider outage should not silently empty the model's memory.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if v, err := v1embed.New(cfg).EmbedQuery(ctx, userMessage); err == nil {
			q.Vector = v
		}
	}

	// Vectors are fetched separately from the list so the memories page, which
	// renders every row, never pays for kilobytes of floats it cannot use.
	vectors, err := s.st.MemoryVectors(projectID)
	if err != nil {
		vectors = nil
	}
	cands := make([]memory.Candidate, 0, len(active))
	for _, m := range active {
		cands = append(cands, memory.Candidate{
			ID:          m.ID,
			Content:     m.Content,
			Tags:        m.Tags,
			Category:    m.Category,
			Importance:  m.Importance,
			LastAccess:  m.LastAccessed,
			AccessCount: m.AccessCount,
			Vector:      vectors[m.ID],
		})
	}
	hits := memory.Rank(cands, q, memoryTopK, memory.MinRelevance)
	if len(hits) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Project memories (the best matches for this message, not the whole store — search_memories looks at the rest; forget with an id deletes one):")
	total := sb.Len()
	kept := 0
	for _, h := range hits {
		line := fmt.Sprintf("\n- [%d] %s: %s", h.ID, h.Category, h.Content)
		if total+len(line) > memoryBudgetChars {
			break
		}
		sb.WriteString(line)
		total += len(line)
		kept++
		_ = s.st.TouchMemory(h.ID)
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

// memoryContent trims and validates a memory written through the UI.
//
// There is no length limit. The prompt carries only the memories the ranker
// selects for the current message, so a long memory costs context in the turns
// where it is genuinely relevant and nothing otherwise — and a fact written out
// properly retrieves better than the same fact compressed into shorthand.
func memoryContent(w http.ResponseWriter, content string) (string, bool) {
	if strings.TrimSpace(content) == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return "", false
	}
	// <private> sections never reach the store, so a memory written through the
	// UI cannot carry a secret into the system prompt of every later turn.
	stripped := memory.StripPrivate(content)
	if stripped == "" {
		writeError(w, http.StatusBadRequest, "the memory was entirely inside <private> tags, so nothing was saved")
		return "", false
	}
	return stripped, true
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
