package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postChatJSON(t *testing.T, s *Server, cookie, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Cookie", cookieHeader(cookie))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

func TestEditMessageRejectsActiveRunWithoutMutating(t *testing.T) {
	s, cookie, _ := newAuthServer(t)
	pid := newTestProject(t, s, cookie, "edit-active")
	session, err := s.st.EnsureDefaultSession(pid)
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := s.st.AddUserMessage(pid, session.ID, "original", "m", "")
	if err != nil {
		t.Fatal(err)
	}
	saveProviders(t, s, cookie, `{"llm":{"providers":[{"id":"p1","name":"fake","baseURL":"https://api.example.com/v1","model":"m","apiKey":"k"}]}}`)
	if _, started, _, err := s.turns.beginOrQueue(pid, session.ID, "running", nil, "", 0); err != nil || !started {
		t.Fatalf("start run: started=%v err=%v", started, err)
	}
	t.Cleanup(func() { s.turns.end(pid, session.ID) })

	rr := postChatJSON(t, s, cookie, "/api/projects/"+pid+"/chat", map[string]any{
		"sessionId": session.ID, "message": "changed", "editMessageId": messageID,
		"providerId": "p1", "model": "m",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("edit during run = %d, want 409; body %s", rr.Code, rr.Body.String())
	}
	got, err := s.st.GetMessage(pid, messageID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "original" {
		t.Fatalf("rejected edit changed content to %q", got.Content)
	}
}

func TestEditMessageRejectsAnotherSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	s, cookie, _ := newAuthServer(t)
	pid := newTestProject(t, s, cookie, "edit-session")
	first, err := s.st.EnsureDefaultSession(pid)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.st.CreateChatSession(pid, "second")
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := s.st.AddUserMessage(pid, second.ID, "other session", "m", "")
	if err != nil {
		t.Fatal(err)
	}
	saveProviders(t, s, cookie, fmt.Sprintf(
		`{"llm":{"providers":[{"id":"p1","name":"fake","baseURL":%q,"model":"m","apiKey":"k"}]}}`, upstream.URL))

	rr := postChatJSON(t, s, cookie, "/api/projects/"+pid+"/chat", map[string]any{
		"sessionId": first.ID, "message": "overwritten", "editMessageId": messageID,
		"providerId": "p1", "model": "m",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-session edit = %d, want 404; body %s", rr.Code, rr.Body.String())
	}
	got, err := s.st.GetMessage(pid, messageID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "other session" {
		t.Fatalf("cross-session edit changed content to %q", got.Content)
	}
}

func TestTruncateMessagesRejectsActiveRun(t *testing.T) {
	s, cookie, _ := newAuthServer(t)
	pid := newTestProject(t, s, cookie, "truncate-active")
	session, err := s.st.EnsureDefaultSession(pid)
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := s.st.AddUserMessage(pid, session.ID, "keep me", "m", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, started, _, err := s.turns.beginOrQueue(pid, session.ID, "running", nil, "", 0); err != nil || !started {
		t.Fatalf("start run: started=%v err=%v", started, err)
	}
	t.Cleanup(func() { s.turns.end(pid, session.ID) })

	rr := postChatJSON(t, s, cookie, "/api/projects/"+pid+"/messages/truncate", map[string]any{
		"sessionId": session.ID, "id": 0,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("truncate during run = %d, want 409; body %s", rr.Code, rr.Body.String())
	}
	got, err := s.st.GetMessage(pid, messageID)
	if err != nil || got.Content != "keep me" {
		t.Fatalf("rejected truncate changed history: message=%+v err=%v", got, err)
	}
}

func TestAskRespondPreservesOtherPendingAsks(t *testing.T) {
	s, cookie, _ := newAuthServer(t)
	pid := newTestProject(t, s, cookie, "ask-scope")
	first, err := s.st.EnsureDefaultSession(pid)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.st.CreateChatSession(pid, "second")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetPendingAsk(pid, first.ID, "ask-1", "Which mode?", []string{"Fast"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetPendingAsk(pid, second.ID, "ask-2", "Which theme?", []string{"Dark"}, ""); err != nil {
		t.Fatal(err)
	}
	ar1 := s.ask.register(pid, first.ID, "ask-1")
	ar2 := s.ask.register(pid, second.ID, "ask-2")
	t.Cleanup(func() {
		s.ask.unregister("ask-1")
		s.ask.unregister("ask-2")
	})

	// An unknown requestId must not erase the session's durable pending ask.
	rr := postChatJSON(t, s, cookie, "/api/projects/"+pid+"/ask/respond", map[string]any{
		"sessionId": first.ID, "requestId": "stale-ask", "answer": "Fast",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("stale ask status = %d, want 404", rr.Code)
	}
	if pending, err := s.st.GetPendingAsk(pid, first.ID); err != nil || pending.ID != "ask-1" {
		t.Fatalf("stale response cleared pending ask: pending=%+v err=%v", pending, err)
	}

	// Session 1 must not resolve or clear session 2's ask.
	rr = postChatJSON(t, s, cookie, "/api/projects/"+pid+"/ask/respond", map[string]any{
		"sessionId": first.ID, "requestId": "ask-2", "answer": "Dark",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-session ask status = %d, want 404", rr.Code)
	}
	if pending, err := s.st.GetPendingAsk(pid, first.ID); err != nil || pending.ID != "ask-1" {
		t.Fatalf("cross-session response cleared first session ask: pending=%+v err=%v", pending, err)
	}
	if pending, err := s.st.GetPendingAsk(pid, second.ID); err != nil || pending.ID != "ask-2" {
		t.Fatalf("cross-session response cleared second session ask: pending=%+v err=%v", pending, err)
	}
	select {
	case got := <-ar2.ch:
		t.Fatalf("cross-session response resolved second session ask with %q", got)
	default:
	}

	// Answering the matching session and requestId resolves and clears only that ask.
	rr = postChatJSON(t, s, cookie, "/api/projects/"+pid+"/ask/respond", map[string]any{
		"sessionId": first.ID, "requestId": "ask-1", "answer": "Fast",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("matching ask status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := <-ar1.ch; got != "Fast" {
		t.Fatalf("resolved payload = %q, want Fast", got)
	}
	if _, err := s.st.GetPendingAsk(pid, first.ID); err == nil {
		t.Fatal("matching ask was not cleared")
	}
	if pending, err := s.st.GetPendingAsk(pid, second.ID); err != nil || pending.ID != "ask-2" {
		t.Fatalf("matching ask disturbed second session ask: pending=%+v err=%v", pending, err)
	}
}
