package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A retry must run on the model the user has selected NOW, not the one stored
// on the message. Retrying is usually a reaction to whatever the last attempt
// produced, so switching model and hitting retry has to actually switch —
// otherwise the same failing model is called again and the switch looks like it
// did nothing. An absent model keeps the stored one, which is what the silent
// resume path relies on: it continues the same turn rather than starting a new
// attempt.
func TestChatRetryUsesRequestedModel(t *testing.T) {
	var bodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	s, adminCookie, _ := newAuthServer(t)

	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"retry-model"}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	var created struct{ ID string }
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	session, err := s.st.EnsureDefaultSession(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The turn being retried was sent with stored-model.
	if _, err := s.st.AddMessage(created.ID, session.ID, "user", "hi", "", "stored-model", "", "", ""); err != nil {
		t.Fatal(err)
	}
	saveProviders(t, s, adminCookie, fmt.Sprintf(
		`{"llm":{"providers":[{"id":"p1","name":"fake","baseURL":%q,"model":"m","apiKey":"k"}]}}`, upstream.URL))

	retry := func(query string) []string {
		bodies = nil
		req := httptest.NewRequest("POST",
			"/api/projects/"+created.ID+"/chat/retry?sessionId="+session.ID+query, nil)
		req.Header.Set("Cookie", cookieHeader(adminCookie))
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
		return bodies
	}

	got := retry("&providerId=p1&model=chosen-model")
	if len(got) == 0 {
		t.Fatal("the turn never reached the upstream server")
	}
	for _, b := range got {
		if !strings.Contains(b, "chosen-model") {
			t.Fatalf("retry with model=chosen-model sent %s, want the chosen model", b)
		}
	}

	got = retry("&providerId=p1")
	if len(got) == 0 {
		t.Fatal("the stored-model retry never reached the upstream server")
	}
	for _, b := range got {
		if !strings.Contains(b, "stored-model") {
			t.Fatalf("retry without a model sent %s, want the stored model", b)
		}
	}
}
