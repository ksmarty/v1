package server

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"v1/internal/agent"
	"v1/internal/embed"
	"v1/internal/llm"
	"v1/internal/memory"
)

// captureTimeout bounds the extra model call a turn's extraction makes.
const captureTimeout = 90 * time.Second

// knownMemoryLimit bounds how many existing memories are listed to the
// extraction. Enough to suppress the common repeats, not enough to bury the
// exchange it is supposed to read.
const knownMemoryLimit = 60

// captureMemories asks the model what is worth remembering from the turn that
// just finished, and stores each fact through the same path the remember tool
// uses — so the <private> stripping, category validation, exact and semantic
// deduplication, capacity limit and embedding all apply identically.
//
// It runs detached from the request, after the done event: the user is not
// waiting on an extra model call, and a failure is worth a log line rather than
// a broken turn.
func (s *Server) captureMemories(projectID, sessionID string, fromID int64, embedCfg embed.Config, userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
	defer cancel()

	msgs, err := s.st.ListMessages(projectID, sessionID)
	if err != nil {
		log.Printf("memory capture: reading the turn: %v", err)
		return
	}
	exchange := memory.TurnExchange(msgs, fromID)
	if exchange == "" {
		return
	}
	reply, err := s.llmClient(userID).Complete(ctx, []llm.Message{
		{Role: "system", Content: memory.CaptureSystem},
		{Role: "user", Content: memory.CapturePrompt(s.knownMemories(projectID), exchange)},
	})
	if err != nil {
		log.Printf("memory capture: %v", err)
		return
	}
	facts := memory.ParseCaptured(reply)
	if len(facts) == 0 {
		return
	}
	// A fresh executor, not the turn's: the turn's carries the SSE emit
	// callbacks, and writing to a stream that has already been closed for a
	// finished turn is not something a background task should be doing.
	exec := &agent.Executor{Store: s.st, ProjectID: projectID, EmbedConfig: embedCfg}
	for _, f := range facts {
		args, err := json.Marshal(map[string]any{
			"content":    f.Content,
			"category":   f.Category,
			"tags":       f.Tags,
			"importance": f.Importance,
		})
		if err != nil {
			continue
		}
		if _, err := exec.Execute(ctx, "remember", string(args)); err != nil {
			log.Printf("memory capture: storing %q: %v", f.Content, err)
		}
	}
}

// knownMemories lists what the project already remembers, newest first, for the
// extraction prompt. Disabled memories are skipped: they are not injected, so
// the model should not treat them as covered.
func (s *Server) knownMemories(projectID string) []string {
	mems, err := s.st.ListMemories(projectID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(mems))
	for i := len(mems) - 1; i >= 0 && len(out) < knownMemoryLimit; i-- {
		m := mems[i]
		if !m.Enabled {
			continue
		}
		out = append(out, strings.TrimSpace(m.Content))
	}
	return out
}
