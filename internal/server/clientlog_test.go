package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientLogEndpointAndDiagnostics(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)

	// A batch of browser-side errors, including a stack and breadcrumbs.
	body := `{"client":{"url":"https://v1.example/chat/abc","userAgent":"Mozilla/5.0 Safari","platform":"MacIntel","viewport":"1440x900"},` +
		`"entries":[` +
		`{"at":"2026-01-01T00:00:00Z","kind":"error","msg":"SyntaxError: The string did not match the expected pattern.","stack":"SyntaxError: ...\n    at connect (TerminalPane.tsx:88)","extra":{"breadcrumbs":[{"kind":"boot","msg":"debug log installed"}]}},` +
		`{"at":"2026-01-01T00:00:01Z","kind":"fetch","msg":"TypeError: Load failed","extra":{"path":"/api/projects/p1/messages"}}` +
		`]}`

	req := httptest.NewRequest("POST", "/api/client-log", strings.NewReader(body))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("post client-log = %d, body %s", rr.Code, rr.Body.String())
	}

	// Malformed JSON must be rejected, not panic.
	bad := httptest.NewRequest("POST", "/api/client-log", strings.NewReader("{not json"))
	bad.Header.Set("Cookie", cookieHeader(adminCookie))
	badRR := httptest.NewRecorder()
	s.Handler().ServeHTTP(badRR, bad)
	if badRR.Code != http.StatusBadRequest {
		t.Fatalf("malformed client-log = %d, want 400", badRR.Code)
	}

	// An empty batch is accepted and stored as nothing.
	empty := httptest.NewRequest("POST", "/api/client-log", strings.NewReader(`{}`))
	empty.Header.Set("Cookie", cookieHeader(adminCookie))
	emptyRR := httptest.NewRecorder()
	s.Handler().ServeHTTP(emptyRR, empty)
	if emptyRR.Code != http.StatusNoContent {
		t.Fatalf("empty client-log = %d, want 204", emptyRR.Code)
	}

	// The diagnostics export carries the client log so a user's report is
	// self-contained.
	projReq := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"diag"}`))
	projReq.Header.Set("Cookie", cookieHeader(adminCookie))
	projRR := httptest.NewRecorder()
	s.Handler().ServeHTTP(projRR, projReq)
	var created struct{ ID string }
	if err := json.Unmarshal(projRR.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	diag := httptest.NewRequest("GET", "/api/projects/"+created.ID+"/diagnostics", nil)
	diag.Header.Set("Cookie", cookieHeader(adminCookie))
	diagRR := httptest.NewRecorder()
	s.Handler().ServeHTTP(diagRR, diag)
	if diagRR.Code != http.StatusOK {
		t.Fatalf("diagnostics = %d", diagRR.Code)
	}
	var dump struct {
		ClientLog struct {
			Client struct {
				URL string `json:"url"`
			} `json:"client"`
			Entries []struct {
				Kind string `json:"kind"`
				Msg  string `json:"msg"`
			} `json:"entries"`
		} `json:"clientLog"`
	}
	if err := json.Unmarshal(diagRR.Body.Bytes(), &dump); err != nil {
		t.Fatal(err)
	}
	if dump.ClientLog.Client.URL != "https://v1.example/chat/abc" {
		t.Fatalf("client info missing from the dump: %+v", dump.ClientLog.Client)
	}
	if len(dump.ClientLog.Entries) != 2 {
		t.Fatalf("client log entries = %d, want 2", len(dump.ClientLog.Entries))
	}
	if dump.ClientLog.Entries[0].Kind != "error" ||
		!strings.Contains(dump.ClientLog.Entries[0].Msg, "did not match the expected pattern") {
		t.Fatalf("first entry = %+v", dump.ClientLog.Entries[0])
	}
}
