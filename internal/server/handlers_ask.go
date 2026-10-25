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
// event, and waits for the answer endpoint. A multi-question ask resolves with
// the full answers array (plus any global note) once the user confirms.
func (s *Server) turnAsk(projectID, sessionID string, emit func(agent.ChatEvent)) func(ctx context.Context, questions []agent.AskQuestion) (agent.AskResponse, error) {
	return func(ctx context.Context, questions []agent.AskQuestion) (agent.AskResponse, error) {
		id := store.NewID()
		first := questions[0]
		// The full questions array always goes into the durable record: options
		// carry descriptions and previews now, and a single question is just a
		// questionnaire of one.
		var qsJSON string
		if b, err := json.Marshal(questions); err == nil {
			qsJSON = string(b)
		}
		if err := s.st.SetPendingAsk(projectID, sessionID, id, first.Question, agent.Labels(first.Options), qsJSON); err != nil {
			return agent.AskResponse{}, fmt.Errorf("failed to persist question: %w", err)
		}
		ar := s.ask.register(id)
		if emit != nil {
			emit(agent.ChatEvent{Type: "question_request", RequestID: id, Text: first.Question, Options: agent.Labels(first.Options), Questions: questions})
		}
		select {
		case payload := <-ar.ch:
			_ = s.st.ClearPendingAsk(projectID, sessionID)
			return parseAskAnswers(payload, questions), nil
		case <-time.After(agent.DefaultAskTimeout):
			_ = s.st.ClearPendingAsk(projectID, sessionID)
			return agent.AskResponse{}, fmt.Errorf("question timed out")
		case <-ctx.Done():
			_ = s.st.ClearPendingAsk(projectID, sessionID)
			return agent.AskResponse{}, ctx.Err()
		}
	}
}

// parseAskAnswers turns the response payload into the questionnaire's answers.
// The UI answers through {"answers":[{question,answer,options,text}…],
// "globalNote":…} for single- and multi-question asks alike; a bare array (what
// older clients posted) and a plain answer string are accepted too, so neither
// an old tab nor a typed JSON answer is mistaken for the envelope. The array
// form is only trusted when an element actually carries an "answer" key.
func parseAskAnswers(payload string, questions []agent.AskQuestion) agent.AskResponse {
	env := parseAskEnvelope(payload, questions, false)
	if env == nil {
		env = parseAskEnvelope(payload, questions, true)
	}
	if env != nil {
		return *env
	}
	if len(questions) == 1 {
		return agent.AskResponse{Answers: []agent.AskAnswer{{Question: questions[0].Question, Answer: payload}}}
	}
	out := make([]agent.AskAnswer, len(questions))
	for i, q := range questions {
		out[i].Question = q.Question
	}
	return agent.AskResponse{Answers: out}
}

// parseAskEnvelope decodes the answers out of payload, either from the
// {"answers":…} envelope or — with bare set — directly from an array. Nil when
// the payload is not this shape.
func parseAskEnvelope(payload string, questions []agent.AskQuestion, bare bool) *agent.AskResponse {
	type answerJSON struct {
		Question string   `json:"question"`
		Answer   *string  `json:"answer"`
		Options  []string `json:"options"`
		Text     string   `json:"text"`
	}
	var answers []answerJSON
	globalNote := ""
	if bare {
		if json.Unmarshal([]byte(payload), &answers) != nil {
			return nil
		}
	} else {
		var env struct {
			Answers    []answerJSON `json:"answers"`
			GlobalNote string       `json:"globalNote"`
		}
		if json.Unmarshal([]byte(payload), &env) != nil {
			return nil
		}
		answers, globalNote = env.Answers, env.GlobalNote
	}
	if len(answers) == 0 || answers[0].Answer == nil {
		return nil
	}
	out := make([]agent.AskAnswer, len(answers))
	for i, a := range answers {
		out[i] = agent.AskAnswer{Question: a.Question, Answer: *a.Answer, Options: a.Options, Text: a.Text}
	}
	resp := agent.AskResponse{Answers: out, GlobalNote: globalNote}
	if len(questions) == 1 {
		if resp.Answers[0].Question == "" {
			resp.Answers[0].Question = questions[0].Question
		}
		resp.Answers = resp.Answers[:1]
		return &resp
	}
	if len(resp.Answers) != len(questions) {
		return nil
	}
	return &resp
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
			Question string   `json:"question"`
			Answer   string   `json:"answer"`
			Options  []string `json:"options"`
			Text     string   `json:"text"`
		} `json:"answers"`
		// GlobalNote is the user's remark on the questionnaire as a whole.
		GlobalNote string `json:"globalNote"`
		SessionID  string `json:"sessionId"`
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
		if b, err := json.Marshal(map[string]any{"answers": body.Answers, "globalNote": body.GlobalNote}); err == nil {
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
