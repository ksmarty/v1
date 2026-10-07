package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newFakeMCPAuthServer stands in for a remote MCP server's authorization
// server: metadata, dynamic client registration and a token endpoint.
func newFakeMCPAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"resource":%q,"authorization_servers":[%q],"scopes_supported":["mcp.read"]}`, srv.URL+"/mcp", srv.URL)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"registration_endpoint":%q}`,
			srv.URL, srv.URL+"/authorize", srv.URL+"/token", srv.URL+"/register")
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"client_id":"cid-1","client_secret":"secret-1"}`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code") == "" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		io.WriteString(w, `{"access_token":"tok-1","refresh_token":"ref-1","expires_in":3600}`)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestMCPOAuthEndToEnd(t *testing.T) {
	as := newFakeMCPAuthServer(t)
	s, adminCookie, _ := newAuthServer(t)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, path, nil)
		} else {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
		}
		r.Header.Set("Cookie", cookieHeader(adminCookie))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, r)
		return rr
	}

	// Configure a remote server pointing at the fake authorization server.
	rr := do("PUT", "/api/settings", fmt.Sprintf(`{"mcp":[{"id":"r1","name":"Remote","url":%q}]}`, as.URL+"/mcp"))
	if rr.Code != http.StatusOK {
		t.Fatalf("save settings = %d: %s", rr.Code, rr.Body)
	}

	// Start the flow: the response carries the authorization URL.
	rr = do("POST", "/api/mcp/oauth/start", `{"id":"r1"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("start = %d: %s", rr.Code, rr.Body)
	}
	var start struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	if authorize.Path != "/authorize" {
		t.Fatalf("authorize path = %q", authorize.Path)
	}
	q := authorize.Query()
	if q.Get("state") == "" {
		t.Fatal("the authorization URL must carry a state")
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatal("the authorization URL must carry a PKCE challenge")
	}
	if q.Get("client_id") != "cid-1" {
		t.Fatalf("client_id = %q, want the registered client", q.Get("client_id"))
	}
	if !strings.Contains(q.Get("redirect_uri"), "/api/mcp/oauth/callback") {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}

	// Before authorizing, the status says there is no grant.
	rr = do("GET", "/api/mcp/status", "")
	var status struct {
		OAuth map[string]string `json:"oauth"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.OAuth["r1"] != "none" {
		t.Fatalf("oauth status before connecting = %v", status.OAuth)
	}

	// The authorization server redirects the browser back.
	rr = do("GET", "/api/mcp/oauth/callback?code=the-code&state="+url.QueryEscape(q.Get("state")), "")
	if rr.Code != http.StatusFound {
		t.Fatalf("callback = %d: %s", rr.Code, rr.Body)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "mcp=connected") {
		t.Fatalf("callback redirect = %q, want a connected result", loc)
	}

	rr = do("GET", "/api/mcp/status", "")
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.OAuth["r1"] != "connected" {
		t.Fatalf("oauth status after connecting = %v", status.OAuth)
	}

	// Disconnecting forgets the grant.
	if rr = do("POST", "/api/mcp/oauth/disconnect", `{"id":"r1"}`); rr.Code != http.StatusNoContent {
		t.Fatalf("disconnect = %d: %s", rr.Code, rr.Body)
	}
	rr = do("GET", "/api/mcp/status", "")
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.OAuth["r1"] != "none" {
		t.Fatalf("oauth status after disconnecting = %v", status.OAuth)
	}
}

func TestMCPOAuthCallbackRejectsUnknownState(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	req := httptest.NewRequest("GET", "/api/mcp/oauth/callback?code=x&state=never-issued", nil)
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("callback = %d, want a redirect back to settings", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "mcp=error") {
		t.Fatalf("redirect = %q, want an error result", loc)
	}
}

func TestMCPOAuthStartRejectsNonRemoteServer(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"mcp":[{"id":"c1","name":"Local","command":"npx","args":["-y","server"]}]}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rr.Code, rr.Body)
	}

	req = httptest.NewRequest("POST", "/api/mcp/oauth/start", strings.NewReader(`{"id":"c1"}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("start for a stdio server = %d, want 400: %s", rr.Code, rr.Body)
	}
}

func TestMCPOAuthStartReportsDiscoveryFailure(t *testing.T) {
	// A server that publishes no metadata must produce a readable error rather
	// than an empty authorization URL.
	dead := httptest.NewServer(http.NotFoundHandler())
	defer dead.Close()

	s, adminCookie, _ := newAuthServer(t)
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(fmt.Sprintf(`{"mcp":[{"id":"r1","name":"Remote","url":%q}]}`, dead.URL+"/mcp")))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	req = httptest.NewRequest("POST", "/api/mcp/oauth/start", strings.NewReader(`{"id":"r1"}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("start = %d, want 502: %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "OAuth metadata") {
		t.Fatalf("error = %s, want it to mention the missing metadata", rr.Body)
	}
}
