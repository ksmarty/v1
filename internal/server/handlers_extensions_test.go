package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"v1/internal/extensions"
)

// extensionsRequest runs one request against the API with an admin session.
func extensionsRequest(t *testing.T, s *Server, cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Cookie", cookieHeader(cookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

type extensionListBody struct {
	Extensions []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Builtin bool   `json:"builtin"`
	} `json:"extensions"`
	Harness struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	} `json:"harness"`
}

// The sub-agent tool ships with v1, so a fresh install must already list it,
// enabled, with its source on disk.
func TestExtensionsListIncludesBundledDelegate(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	rr := extensionsRequest(t, s, adminCookie, "GET", "/api/extensions", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/extensions = %d: %s", rr.Code, rr.Body.String())
	}
	var body extensionListBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ext := range body.Extensions {
		if ext.ID != "delegate" {
			continue
		}
		found = true
		if !ext.Enabled {
			t.Fatal("the bundled delegate is not enabled by default")
		}
		if !ext.Builtin {
			t.Fatal("the bundled delegate is not marked as a builtin")
		}
	}
	if !found {
		t.Fatalf("the bundled delegate is missing from %+v", body.Extensions)
	}
	// The source must have been materialized, or the sidecar would load nothing.
	source, err := extensions.Read(s.extensionsRoot(), "delegate")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, "pi.defineTool") {
		t.Fatalf("the delegate source was not written:\n%s", source)
	}
	// With no harness attached the API still answers, and says so.
	if body.Harness.Available {
		t.Fatal("the harness is reported as available in a test server")
	}
	if body.Harness.Reason == "" {
		t.Fatal("an unavailable harness has no reason")
	}
}

func TestExtensionsSaveRejectsUnsafeID(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	rr := extensionsRequest(t, s, adminCookie, "POST", "/api/extensions",
		`{"id":"../escape","source":"export default () => ({});"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("saving a traversing id = %d, want 400: %s", rr.Code, rr.Body.String())
	}
}

func TestExtensionsSaveRejectsSyntaxError(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	s, adminCookie, _ := newAuthServer(t)
	rr := extensionsRequest(t, s, adminCookie, "POST", "/api/extensions",
		`{"id":"broken","source":"export default (pi) => ({"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("saving a broken module = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	// The message must be node's, so the editor can show something actionable.
	if !strings.Contains(rr.Body.String(), "SyntaxError") {
		t.Fatalf("the rejection does not carry node's error: %s", rr.Body.String())
	}
	// And nothing may have been written.
	if source, _ := extensions.Read(s.extensionsRoot(), "broken"); source != "" {
		t.Fatalf("a rejected extension was written anyway:\n%s", source)
	}
}

func TestExtensionsSaveToggleAndRemove(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	s, adminCookie, _ := newAuthServer(t)

	// A new extension starts disabled: loading code nobody asked for is the
	// wrong default.
	const sampleSource = `export default (pi) => ({ name: "sample", tools: [] });`
	rr := extensionsRequest(t, s, adminCookie, "POST", "/api/extensions",
		`{"id":"sample","name":"Sample","description":"a test","source":`+
			strconv.Quote(sampleSource)+`}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rr.Code, rr.Body.String())
	}
	var body extensionListBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !hasExtension(body, "sample", false) {
		t.Fatalf("the new extension is not present and disabled: %+v", body.Extensions)
	}

	// The editor reads back what is on disk.
	rr = extensionsRequest(t, s, adminCookie, "GET", "/api/extensions/sample", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rr.Code, rr.Body.String())
	}
	var one struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if one.Source != sampleSource {
		t.Fatalf("the read-back source is %q, want %q", one.Source, sampleSource)
	}

	// Toggling flips it without touching the source.
	rr = extensionsRequest(t, s, adminCookie, "POST", "/api/extensions/toggle", `{"id":"sample","enabled":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle = %d: %s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !hasExtension(body, "sample", true) {
		t.Fatalf("the extension was not enabled: %+v", body.Extensions)
	}

	// Removing it takes it off the list and deletes the source.
	rr = extensionsRequest(t, s, adminCookie, "POST", "/api/extensions/remove", `{"id":"sample"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, ext := range body.Extensions {
		if ext.ID == "sample" {
			t.Fatal("the removed extension is still listed")
		}
	}
	if source, _ := extensions.Read(s.extensionsRoot(), "sample"); source != "" {
		t.Fatalf("the removed extension's source survived:\n%s", source)
	}

	// A bundled extension cannot be removed, because it would come back on the
	// next start; disabling is the supported way to turn it off.
	rr = extensionsRequest(t, s, adminCookie, "POST", "/api/extensions/remove", `{"id":"delegate"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("removing the bundled delegate = %d, want 400: %s", rr.Code, rr.Body.String())
	}
}

func hasExtension(body extensionListBody, id string, enabled bool) bool {
	for _, ext := range body.Extensions {
		if ext.ID == id {
			return ext.Enabled == enabled
		}
	}
	return false
}
