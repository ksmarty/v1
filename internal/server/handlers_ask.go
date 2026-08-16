package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"v1/internal/agent"
	"v1/internal/store"
)

// askRequest is a pending question from the agent's ask_user tool.
type askRequest struct {
	ch chan string
}

// askRegistry tracks in-flight questions by id so the chat UI can answer them.
type askRegistry struct {
	mu   sync.Mutex
	reqs map[string]*askRequest
}

func (r *askRegistry) register(id string) *askRequest {
	ar := &askRequest{ch: make(chan string, 1)}
	r.mu.Lock()
	r.reqs[id] = ar
	r.mu.Unlock()
	return ar
}

func (r *askRegistry) resolve(id, answer string) *askRequest {
	r.mu.Lock()
	ar := r.reqs[id]
	delete(r.reqs, id)
	r.mu.Unlock()
	if ar != nil {
		ar.ch <- answer
	}
	return ar
}

// turnAsk implements the ask_user tool for one chat stream: persists the
// question(s) (so they survive reloads and reconnects), emits them as an SSE
// event, and waits for the answer endpoint. Multi-question asks resolve with
// the full answers array once the user confirms.
func (s *Server) turnAsk(projectID, sessionID string, emit func(agent.ChatEvent)) func(ctx context.Context, questions []agent.AskQuestion) ([]agent.AskAnswer, error) {
	return func(ctx context.Context, questions []agent.AskQuestion) ([]agent.AskAnswer, error) {
		id := store.NewID()
		first := questions[0]
		qsJSON := ""
		if len(questions) > 1 {
			if b, err := json.Marshal(questions); err == nil {
				qsJSON = string(b)
			}
		}
		// The durable record first — the UI answers through it even when the
		// live stream is gone.
		if err := s.st.SetPendingAsk(projectID, sessionID, id, first.Question, first.Options, qsJSON); err != nil {
			return nil, fmt.Errorf("failed to persist question: %w", err)
		}
		ar := s.ask.register(id)
		if emit != nil {
			emit(agent.ChatEvent{Type: "question_request", RequestID: id, Text: first.Question, Options: first.Options, Questions: questions})
		}
		select {
		case answer := <-ar.ch:
			_ = s.st.ClearPendingAsk(projectID, sessionID)
			return parseAskAnswers(answer, questions), nil
		case <-time.After(10 * time.Minute):
			_ = s.st.ClearPendingAsk(projectID, sessionID)
			return nil, fmt.Errorf("question timed out")
		case <-ctx.Done():
			_ = s.st.ClearPendingAsk(projectID, sessionID)
			return nil, ctx.Err()
		}
	}
}

// parseAskAnswers turns the response payload into the answers array. The UI
// posts {"answers":[{question,answer},…]} for multi-question asks and a plain
// "answer" string for single ones (backward compatible).
func parseAskAnswers(payload string, questions []agent.AskQuestion) []agent.AskAnswer {
	if len(questions) == 1 {
		return []agent.AskAnswer{{Question: questions[0].Question, Answer: payload}}
	}
	var out []agent.AskAnswer
	if err := json.Unmarshal([]byte(payload), &out); err != nil || len(out) != len(questions) {
		out = make([]agent.AskAnswer, len(questions))
		for i, q := range questions {
			out[i].Question = q.Question
		}
	}
	return out
}

// handleAskRespond answers a pending question from the ask_user tool. The
// durable record is cleared either way — a stale card must not linger after
// the answer or timeout landed.
func (s *Server) handleAskRespond(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		RequestID string `json:"requestId"`
		Answer    string `json:"answer"`
		Answers   []struct {
			Question string `json:"question"`
			Answer   string `json:"answer"`
		} `json:"answers"`
		SessionID string `json:"sessionId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeError(w, http.StatusBadRequest, "requestId is required")
		return
	}
	payload := body.Answer
	if len(body.Answers) > 0 {
		if b, err := json.Marshal(body.Answers); err == nil {
			payload = string(b)
		}
	}
	_ = s.st.ClearPendingAsk(p.ID, s.chatSessionID(p, body.SessionID))
	ar := s.ask.resolve(body.RequestID, payload)
	if ar == nil {
		writeError(w, http.StatusNotFound, "no pending question with that id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAskPending returns the project's pending ask_user question, or
// {"pending":false} when there is none. The UI checks this on load and after
// reconnects so a question asked while the app was closed is still answerable.
func (s *Server) handleAskPending(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	a, err := s.st.GetPendingAsk(p.ID, s.chatSessionID(p, r.URL.Query().Get("sessionId")))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"pending": false})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Multi-question asks carry the full questions array.
	var questions []agent.AskQuestion
	if a.QuestionsJSON != "" {
		_ = json.Unmarshal([]byte(a.QuestionsJSON), &questions)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pending":   true,
		"requestId": a.ID,
		"question":  a.Question,
		"options":   a.Options,
		"questions": questions,
	})
}
