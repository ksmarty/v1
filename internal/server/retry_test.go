package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContinuedPartialDetection(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"cp"}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	var created struct{ ID string }
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	pid := created.ID
	session, err := s.st.EnsureDefaultSession(pid)
	if err != nil {
		t.Fatal(err)
	}
	sid := session.ID

	uid, _ := s.st.AddMessage(pid, sid, "user", "hi", "", "", "", "", "")
	// no error yet -> 0
	if got := s.continuedPartial(pid, sid, uid); got != 0 {
		t.Fatalf("expected 0 before failure, got %d", got)
	}
	partial, _ := s.st.AddMessage(pid, sid, "assistant", "PART", "", "", "th", "", "")
	_, _ = s.st.AddMessage(pid, sid, "error", "boom", "", "", "", "", "")
	if got := s.continuedPartial(pid, sid, uid); got != partial {
		t.Fatalf("expected %d, got %d", partial, got)
	}
	// an empty partial is not continuable — normal re-run instead
	_, _ = s.st.AddMessage(pid, sid, "assistant", "", "", "", "", "", "")
	_, _ = s.st.AddMessage(pid, sid, "error", "boom2", "", "", "", "", "")
	if got := s.continuedPartial(pid, sid, uid); got != 0 {
		t.Fatalf("expected 0 for empty partial, got %d", got)
	}
}

func TestChatStatus(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"st"}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	var created struct{ ID string }
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	pid := created.ID
	session, _ := s.st.EnsureDefaultSession(pid)

	// Idle -> not running.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/projects/"+pid+"/chat/status?sessionId="+session.ID, nil)
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	s.Handler().ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), `"running":false`) {
		t.Fatalf("idle status = %s", rr.Body.String())
	}

	// Active run -> running.
	q, started, _ := s.turns.beginOrQueue(pid, session.ID, "")
	if !started {
		t.Fatal("should start a run")
	}
	defer s.turns.end(pid, session.ID)
	_ = q
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/projects/"+pid+"/chat/status?sessionId="+session.ID, nil)
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	s.Handler().ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), `"running":true`) {
		t.Fatalf("active status = %s", rr.Body.String())
	}
	_ = http.StatusOK
}
