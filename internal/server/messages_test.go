package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"v1/internal/agent"
)

// newTestProject creates a project through the API and returns its id.
func newTestProject(t *testing.T, s *Server, cookie, name string) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"`+name+`"}`))
	req.Header.Set("Cookie", cookieHeader(cookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("create project = %d, body %s", rr.Code, rr.Body.String())
	}
	var created struct{ ID string }
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("project id is empty")
	}
	return created.ID
}

// A background command's result row is stored with a marker in tool_json, and
// the chat API hands tool_json to the browser as json.RawMessage. When that
// marker was the bare word "background" the whole message list failed to
// encode: the handler had already written its 200 header, so the client got an
// empty body and reported a JSON syntax error (Safari: "The string did not
// match the expected pattern") instead of the history. Every chat containing a
// finished background command was unreadable.
func TestListMessagesSurvivesBackgroundMarker(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	pid := newTestProject(t, s, adminCookie, "messages")
	session, err := s.st.EnsureDefaultSession(pid)
	if err != nil {
		t.Fatal(err)
	}

	add := func(role, content, toolJSON string) {
		t.Helper()
		if _, err := s.st.AddMessage(pid, session.ID, role, content, toolJSON, "m", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	add("user", "hi", "")
	// The marker as stored by earlier builds — the rows still in real databases.
	add("user", "[Background #abc] finished (exit 0):\nok", "background")
	// The marker as written now.
	add("user", "[Background #def] finished (exit 0):\nok", agent.BackgroundToolJSON)
	// Anything else malformed must not be able to hide a whole chat either.
	add("assistant", "bad row", "{not json")

	req := httptest.NewRequest("GET", "/api/projects/"+pid+"/messages?sessionId="+session.ID, nil)
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("list messages = %d, body %s", rr.Code, rr.Body.String())
	}
	if rr.Body.Len() == 0 {
		t.Fatal("empty body: browsers report this as a JSON syntax error, not as an empty chat")
	}
	var got []struct {
		Role    string          `json:"role"`
		Content string          `json:"content"`
		Tool    json.RawMessage `json:"tool"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("messages = %d, want 4", len(got))
	}
	// Both encodings reach the UI as the JSON string the frontend compares
	// against (ChatPane: m.tool === 'background').
	for _, i := range []int{1, 2} {
		if string(got[i].Tool) != agent.BackgroundToolJSON {
			t.Fatalf("message %d tool = %s, want %s", i, got[i].Tool, agent.BackgroundToolJSON)
		}
	}
	// A normal user message carries no marker.
	if len(got[0].Tool) != 0 {
		t.Fatalf("plain user message tool = %s, want empty", got[0].Tool)
	}
	// The malformed row is dropped rather than taking the chat down with it.
	if len(got[3].Tool) != 0 {
		t.Fatalf("malformed row tool = %s, want empty", got[3].Tool)
	}
}

// writeJSON must never answer 200 with an empty body: that is exactly what
// turned an encoding failure into a bogus client-side syntax error.
func TestWriteJSONNeverReturnsEmptyBody(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusOK, make(chan int))

	if rr.Body.Len() == 0 {
		t.Fatal("writeJSON wrote an empty body")
	}
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, rr.Body.String())
	}
	if body["error"] == "" {
		t.Fatalf("error body = %v", body)
	}
}

// A successful response keeps its exact shape (trailing newline included).
func TestWriteJSONSuccess(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusOK, []string{"a"})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Body.String(); got != "[\"a\"]\n" {
		t.Fatalf("body = %q", got)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
}
