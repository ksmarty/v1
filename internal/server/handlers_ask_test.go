package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"v1/internal/agent"
	"v1/internal/store"
)

// TestAskLifecycle covers the ask_user persistence: the question lands in the
// durable record and the pending endpoint serves it, answering resolves the
// live channel and clears the record, and a dead run (ctx cancel) clears it
// too.
func TestAskLifecycle(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	// Create a project through the API (auth-disabled default user aside, the
	// handler path is what the UI uses).
	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"ask test"}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	pid := created.ID
	session, err := s.st.EnsureDefaultSession(pid)
	if err != nil {
		t.Fatal(err)
	}
	sid := session.ID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gotEvent *agent.ChatEvent
	emit := func(ev agent.ChatEvent) { gotEvent = &ev }
	ask := s.turnAsk(pid, sid, emit)

	answered := make(chan string, 1)
	go func() {
		ans, err := ask(ctx, []agent.AskQuestion{{Question: "Cats or dogs?", Options: []agent.AskOption{{Label: "Cats", Description: "quieter"}, {Label: "Dogs", Description: "needs walks"}}}})
		if err != nil {
			answered <- "err:" + err.Error()
			return
		}
		if len(ans.Answers) != 1 || ans.Answers[0].Answer != "Cats" {
			answered <- "wrong:" + fmt.Sprint(ans)
			return
		}
		answered <- ans.Answers[0].Answer
	}()

	// The question is emitted and persisted.
	var eventText string
	deadline := time.Now().Add(5 * time.Second)
	for gotEvent == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if gotEvent == nil || gotEvent.Type != "question_request" || gotEvent.Text != "Cats or dogs?" {
		t.Fatalf("emit: %+v", gotEvent)
	}
	eventText = gotEvent.Text
	pa, err := s.st.GetPendingAsk(pid, sid)
	if err != nil || pa.Question != eventText || len(pa.Options) != 2 {
		t.Fatalf("pending: %+v err=%v", pa, err)
	}

	// The pending endpoint serves it.
	getReq := httptest.NewRequest("GET", "/api/projects/"+pid+"/ask/pending", nil)
	getReq.Header.Set("Cookie", cookieHeader(adminCookie))
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, getReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("pending: %d %s", rr.Code, rr.Body.String())
	}
	var pend struct {
		Pending   bool     `json:"pending"`
		RequestID string   `json:"requestId"`
		Question  string   `json:"question"`
		Options   []string `json:"options"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &pend); err != nil {
		t.Fatal(err)
	}
	if !pend.Pending || pend.Question != "Cats or dogs?" || len(pend.Options) != 2 {
		t.Fatalf("pending response: %+v", pend)
	}

	// Answering resolves the channel and clears the record.
	postReq := httptest.NewRequest("POST", "/api/projects/"+pid+"/ask/respond",
		strings.NewReader(`{"requestId":"`+pend.RequestID+`","answer":"Cats"}`))
	postReq.Header.Set("Cookie", cookieHeader(adminCookie))
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, postReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("respond: %d %s", rr.Code, rr.Body.String())
	}
	select {
	case ans := <-answered:
		if ans != "Cats" {
			t.Fatalf("answer = %q", ans)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask never resolved")
	}
	if _, err := s.st.GetPendingAsk(pid, sid); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("pending after answer: %v", err)
	}

	// The pending endpoint reports none.
	getReq = httptest.NewRequest("GET", "/api/projects/"+pid+"/ask/pending", nil)
	getReq.Header.Set("Cookie", cookieHeader(adminCookie))
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, getReq)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"pending":false`) {
		t.Fatalf("pending after answer: %d %s", rr.Code, rr.Body.String())
	}

	// A dead run clears the record too.
	ctx2, cancel2 := context.WithCancel(context.Background())
	ask2 := s.turnAsk(pid, sid, func(agent.ChatEvent) {})
	go func() {
		_, _ = ask2(ctx2, []agent.AskQuestion{{Question: "Will you finish?"}})
	}()
	deadline = time.Now().Add(5 * time.Second)
	for {
		pa, err = s.st.GetPendingAsk(pid, sid)
		if err == nil {
			if time.Now().After(deadline) {
				t.Fatalf("second ask never persisted: %+v", pa)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel2()
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, err := s.st.GetPendingAsk(pid, sid); errors.Is(err, store.ErrNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending ask not cleared after ctx cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Answering an unknown id still clears any lingering record and 404s.
	postReq = httptest.NewRequest("POST", "/api/projects/"+pid+"/ask/respond",
		strings.NewReader(`{"requestId":"nope","answer":"x"}`))
	postReq.Header.Set("Cookie", cookieHeader(adminCookie))
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, postReq)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("respond unknown: %d %s", rr.Code, rr.Body.String())
	}
}

// parseAskAnswers must accept the {answers:[{question,answer}]} envelope the UI
// posts for a single-question ask, not just the plain string the older client
// sent — otherwise the whole JSON payload is stored as the answer and shown in
// the chat. Notes and structured picks ride along with it.
func TestParseAskAnswers(t *testing.T) {
	one := []agent.AskQuestion{{Question: "Which days work best for you?"}}
	got := parseAskAnswers(`{"answers":[{"question":"Which days work best for you?","answer":"Wednesday"}]}`, one)
	if len(got.Answers) != 1 || got.Answers[0].Answer != "Wednesday" || got.Answers[0].Question != one[0].Question {
		t.Fatalf("envelope payload: %+v", got)
	}

	// A bare array (the older UI's shape) still parses.
	got = parseAskAnswers(`[{"question":"Which days work best for you?","answer":"Wednesday"}]`, one)
	if len(got.Answers) != 1 || got.Answers[0].Answer != "Wednesday" {
		t.Fatalf("bare array payload: %+v", got)
	}

	// Picks, typed text and the global note survive.
	got = parseAskAnswers(`{"answers":[{"question":"Which days work best for you?","answer":"Wed, Fri","options":["Wed","Fri"],"text":"after 5"}],"globalNote":"ship friday"}`, one)
	if len(got.Answers) != 1 || got.Answers[0].Answer != "Wed, Fri" || len(got.Answers[0].Options) != 2 || got.Answers[0].Text != "after 5" || got.GlobalNote != "ship friday" {
		t.Fatalf("rich payload: %+v", got)
	}

	// A plain answer string still works (backward compatible).
	got = parseAskAnswers("Cats", one)
	if len(got.Answers) != 1 || got.Answers[0].Answer != "Cats" || got.Answers[0].Question != one[0].Question {
		t.Fatalf("plain payload: %+v", got)
	}

	// A literal JSON answer without an "answer" key is not the envelope.
	got = parseAskAnswers(`[{"x":1}]`, one)
	if len(got.Answers) != 1 || got.Answers[0].Answer != `[{"x":1}]` {
		t.Fatalf("literal json: %+v", got)
	}

	// Multi-question: the array maps positionally.
	two := []agent.AskQuestion{{Question: "a"}, {Question: "b"}}
	got = parseAskAnswers(`{"answers":[{"question":"a","answer":"1"},{"question":"b","answer":"2"}]}`, two)
	if len(got.Answers) != 2 || got.Answers[0].Answer != "1" || got.Answers[1].Answer != "2" {
		t.Fatalf("multi payload: %+v", got)
	}

	// A malformed multi-question payload yields empty answers, not a JSON blob.
	got = parseAskAnswers("garbage", two)
	if len(got.Answers) != 2 || got.Answers[0].Answer != "" || got.Answers[1].Question != "b" {
		t.Fatalf("fallback: %+v", got)
	}
}
