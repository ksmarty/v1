package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Vercel is opt-in per project: a fresh project must not expose the button, and
// its endpoints must refuse until the toggle is switched on. The Go zero value
// is off, so this also guards against a code path forgetting to set the field.
func TestProjectVercelIsOptIn(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)

	create := func(name string) string {
		req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"`+name+`"}`))
		req.Header.Set("Cookie", cookieHeader(adminCookie))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
			t.Fatalf("create project = %d, body %s", rr.Code, rr.Body.String())
		}
		var created struct {
			ID            string `json:"id"`
			VercelEnabled bool   `json:"vercelEnabled"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		if created.VercelEnabled {
			t.Fatalf("new project %s must start with Vercel disabled", name)
		}
		return created.ID
	}
	id := create("vercel-opt-in")

	deploy := func() int {
		req := httptest.NewRequest("POST", "/api/projects/"+id+"/vercel/deploy", strings.NewReader(`{}`))
		req.Header.Set("Cookie", cookieHeader(adminCookie))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		return rr.Code
	}
	if code := deploy(); code != http.StatusForbidden {
		t.Fatalf("deploy while disabled = %d, want 403", code)
	}

	req := httptest.NewRequest("PATCH", "/api/projects/"+id, strings.NewReader(`{"vercelEnabled":true}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update project = %d, body %s", rr.Code, rr.Body.String())
	}
	var updated struct {
		VercelEnabled bool `json:"vercelEnabled"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if !updated.VercelEnabled {
		t.Fatal("vercelEnabled did not persist through the API")
	}
	p, err := s.st.GetProject(id)
	if err != nil {
		t.Fatal(err)
	}
	if !p.VercelEnabled {
		t.Fatal("store did not persist vercel_enabled")
	}

	// Enabled: the request now gets past the disabled guard and fails later on
	// the missing token instead (a different status).
	if code := deploy(); code == http.StatusForbidden {
		t.Fatal("deploy still refused after enabling Vercel")
	}

	// The next project is unaffected by the previous one.
	create("vercel-opt-in-2")
}
