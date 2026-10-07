package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"v1/internal/agent"
)

// TestBackgroundEndpoints covers what the UI's background-tasks modal needs:
// a running job is listed for its own session only, and cancelling it by the
// short id the UI was shown actually stops it. Only running jobs are served, so
// a cancelled one leaves the list.
func TestBackgroundEndpoints(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"bg test"}`))
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

	// A job in another session must never appear in this one's list.
	if _, err := s.background.Start(t.TempDir(), "sleep 30", 60*time.Second, "someone-else", nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan *agent.BackgroundJob, 1)
	if _, err := s.background.Start(t.TempDir(), "sleep 30", 60*time.Second, sid, func(j *agent.BackgroundJob) { done <- j }); err != nil {
		t.Fatal(err)
	}

	list := func() []backgroundJobJSON {
		r := httptest.NewRequest("GET", "/api/projects/"+pid+"/background?sessionId="+sid, nil)
		r.Header.Set("Cookie", cookieHeader(adminCookie))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Jobs []backgroundJobJSON `json:"jobs"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Jobs
	}
	cancel := func(sessionID, jobID string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/projects/"+pid+"/background/cancel",
			strings.NewReader(`{"sessionId":"`+sessionID+`","jobId":"`+jobID+`"}`))
		r.Header.Set("Cookie", cookieHeader(adminCookie))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		return rec
	}

	jobs := list()
	if len(jobs) != 1 || jobs[0].Command != "sleep 30" {
		t.Fatalf("list = %+v, want just this session's job", jobs)
	}
	short := jobs[0].ID
	if len(short) != 8 {
		t.Fatalf("job id = %q, want the 8-char short id", short)
	}
	if jobs[0].StartedAt == 0 {
		t.Fatal("startedAt was not reported")
	}

	// Another session cannot stop this job, and an unknown id is a 404.
	if rec := cancel("someone-else", short); rec.Code != http.StatusNotFound {
		t.Fatalf("cancel from another session: %d %s", rec.Code, rec.Body.String())
	}
	if rec := cancel(sid, "deadbeef"); rec.Code != http.StatusNotFound {
		t.Fatalf("cancel with an unknown id: %d %s", rec.Code, rec.Body.String())
	}
	if rec := cancel(sid, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("cancel with no id: %d %s", rec.Code, rec.Body.String())
	}
	if got := list(); len(got) != 1 {
		t.Fatalf("a rejected cancel changed the list: %+v", got)
	}

	rec := cancel(sid, short)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case job := <-done:
		if !job.Cancelled {
			t.Fatal("the cancelled job did not record Cancelled")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled job never finished")
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("list after the cancel = %+v, want empty", got)
	}
}
