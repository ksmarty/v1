package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The fork endpoint copies a session's transcript into a new thread. The user
// lands in the fork and the original keeps its messages, so both directions can
// go on independently.
func TestForkSessionCopiesMessages(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	pid := newTestProject(t, s, adminCookie, "fork")
	src, err := s.st.EnsureDefaultSession(pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.AddMessage(pid, src.ID, "user", "hello", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.AddMessage(pid, src.ID, "assistant", "hi", "", "m", "", "", ""); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/projects/"+pid+"/sessions/"+src.ID+"/fork", strings.NewReader("{}"))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("fork = %d, body %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Session struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"session"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Session.ID == "" || body.Session.ID == src.ID {
		t.Fatalf("fork id = %q (source %q)", body.Session.ID, src.ID)
	}
	if body.Session.Name != src.Name+" (fork)" {
		t.Fatalf("fork name = %q", body.Session.Name)
	}

	msgs, err := s.st.ListMessages(pid, body.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Content != "hello" || msgs[1].Content != "hi" {
		t.Fatalf("fork transcript = %+v", msgs)
	}
	orig, err := s.st.ListMessages(pid, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(orig) != 2 {
		t.Fatalf("source transcript changed: %d messages", len(orig))
	}
}

// Forking a session with a live run would copy a transcript the sidecar is
// still writing, so it is refused like an edit is.
func TestForkSessionRejectsActiveRun(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	pid := newTestProject(t, s, adminCookie, "fork-busy")
	src, err := s.st.EnsureDefaultSession(pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.turns.beginOrQueue(pid, src.ID, "", "", 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.turns.end(pid, src.ID) })

	req := httptest.NewRequest("POST", "/api/projects/"+pid+"/sessions/"+src.ID+"/fork", strings.NewReader("{}"))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("fork during a run = %d, want 409", rr.Code)
	}
}

// An unknown session is a 404 rather than a fresh empty fork.
func TestForkSessionUnknownSession(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	pid := newTestProject(t, s, adminCookie, "fork-missing")
	req := httptest.NewRequest("POST", "/api/projects/"+pid+"/sessions/missing/fork", strings.NewReader("{}"))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("fork unknown session = %d, want 404", rr.Code)
	}
}
