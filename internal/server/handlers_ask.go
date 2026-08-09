package server

import (
	"context"
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

// turnAsk implements the ask_user tool for one chat stream: emits the question
// as an SSE event and waits for the answer endpoint.
func (s *Server) turnAsk(emit func(agent.ChatEvent)) func(ctx context.Context, question string, options []string) (string, error) {
	return func(ctx context.Context, question string, options []string) (string, error) {
		id := store.NewID()
		ar := s.ask.register(id)
		if emit != nil {
			emit(agent.ChatEvent{Type: "question_request", RequestID: id, Text: question, Options: options})
		}
		select {
		case answer := <-ar.ch:
			return answer, nil
		case <-time.After(10 * time.Minute):
			return "", fmt.Errorf("question timed out")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// handleAskRespond answers a pending question from the ask_user tool.
func (s *Server) handleAskRespond(w http.ResponseWriter, r *http.Request) {
	if s.projectOr404(w, r) == nil {
		return
	}
	var body struct {
		RequestID string `json:"requestId"`
		Answer    string `json:"answer"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeError(w, http.StatusBadRequest, "requestId is required")
		return
	}
	ar := s.ask.resolve(body.RequestID, body.Answer)
	if ar == nil {
		writeError(w, http.StatusNotFound, "no pending question with that id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
