package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAS is a stand-in authorization server: it publishes protected-resource
// and authorization-server metadata, registers clients and issues tokens.
type fakeAS struct {
	mu           sync.Mutex
	tokenForm    url.Values
	tokenCalls   int
	registerBody map[string]any
	// requireSecret makes the token endpoint reject a missing client_secret.
	requireSecret bool
}

func newFakeAS(t *testing.T) (*httptest.Server, *fakeAS) {
	t.Helper()
	as := &fakeAS{}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"resource":%q,"authorization_servers":[%q],"scopes_supported":["mcp.read"]}`, srv.URL+"/mcp", srv.URL)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"registration_endpoint":%q,"scopes_supported":["mcp.read"]}`,
			srv.URL, srv.URL+"/authorize", srv.URL+"/token", srv.URL+"/register")
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		as.mu.Lock()
		as.registerBody = body
		as.mu.Unlock()
		io.WriteString(w, `{"client_id":"cid-1","client_secret":"secret-1"}`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		as.mu.Lock()
		as.tokenForm = r.PostForm
		as.tokenCalls++
		needSecret := as.requireSecret
		as.mu.Unlock()
		if needSecret && r.PostForm.Get("client_secret") == "" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_client"}`)
			return
		}
		if r.PostForm.Get("grant_type") == "refresh_token" {
			io.WriteString(w, `{"access_token":"tok-2","expires_in":3600}`)
			return
		}
		io.WriteString(w, `{"access_token":"tok-1","refresh_token":"ref-1","expires_in":3600,"scope":"mcp.read"}`)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, as
}

func TestDiscoverOAuth(t *testing.T) {
	srv, _ := newFakeAS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := DiscoverOAuth(ctx, nil, srv.URL+"/mcp")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if cfg.AuthorizationEndpoint != srv.URL+"/authorize" {
		t.Fatalf("authorization endpoint = %q", cfg.AuthorizationEndpoint)
	}
	if cfg.TokenEndpoint != srv.URL+"/token" {
		t.Fatalf("token endpoint = %q", cfg.TokenEndpoint)
	}
	if cfg.RegistrationEndpoint != srv.URL+"/register" {
		t.Fatalf("registration endpoint = %q", cfg.RegistrationEndpoint)
	}
	if cfg.Resource != srv.URL+"/mcp" {
		t.Fatalf("resource = %q", cfg.Resource)
	}
	if len(cfg.Scopes) != 1 || cfg.Scopes[0] != "mcp.read" {
		t.Fatalf("scopes = %v", cfg.Scopes)
	}
}

func TestDiscoverOAuthWithoutMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if _, err := DiscoverOAuth(context.Background(), nil, srv.URL); err == nil {
		t.Fatal("a server with no metadata must fail with a clear error")
	}
}

func TestPKCEChallengeIsS256(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if p.Verifier == "" || p.Challenge == "" {
		t.Fatal("both values are required")
	}
	if p.Challenge != challengeFor(p.Verifier) {
		t.Fatal("the challenge must be the S256 hash of the verifier")
	}
	if strings.ContainsAny(p.Verifier, "+/=") {
		t.Fatalf("verifier %q must be base64url without padding", p.Verifier)
	}
}

func TestAuthorizeURL(t *testing.T) {
	cfg := &OAuthConfig{
		AuthorizationEndpoint: "https://as.example.com/authorize",
		Resource:              "https://mcp.example.com/mcp",
		Scopes:                []string{"a", "b"},
	}
	got := AuthorizeURL(cfg, "cid", "https://v1.example.com/api/mcp/oauth/callback", "st-1", "ch-1")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for key, want := range map[string]string{
		"response_type":         "code",
		"client_id":             "cid",
		"redirect_uri":          "https://v1.example.com/api/mcp/oauth/callback",
		"state":                 "st-1",
		"code_challenge":        "ch-1",
		"code_challenge_method": "S256",
		"scope":                 "a b",
		"resource":              "https://mcp.example.com/mcp",
	} {
		if q.Get(key) != want {
			t.Fatalf("%s = %q, want %q", key, q.Get(key), want)
		}
	}
}

func TestExchangeCode(t *testing.T) {
	srv, as := newFakeAS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := DiscoverOAuth(ctx, nil, srv.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := ExchangeCode(ctx, nil, cfg, "the-code", "the-verifier", "cid-1", "secret-1", "https://v1.example.com/cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if tokens.AccessToken != "tok-1" || tokens.RefreshToken != "ref-1" {
		t.Fatalf("tokens = %+v", tokens)
	}
	if !tokens.Valid(time.Now()) {
		t.Fatal("a token with an expiry in the future must be valid")
	}
	if tokens.Expiry.IsZero() {
		t.Fatal("expires_in must be turned into an expiry")
	}
	if tokens.ClientID != "cid-1" || tokens.ClientSecret != "secret-1" {
		t.Fatal("the client credentials must be kept for the refresh")
	}
	if tokens.Resource != cfg.Resource {
		t.Fatal("the resource must be kept for the refresh")
	}

	as.mu.Lock()
	form := as.tokenForm
	as.mu.Unlock()
	for key, want := range map[string]string{
		"grant_type":    "authorization_code",
		"code":          "the-code",
		"code_verifier": "the-verifier",
		"client_id":     "cid-1",
		"client_secret": "secret-1",
		"redirect_uri":  "https://v1.example.com/cb",
		"resource":      cfg.Resource,
	} {
		if form.Get(key) != want {
			t.Fatalf("token request %s = %q, want %q", key, form.Get(key), want)
		}
	}
}

func TestRefreshAccessTokenKeepsGrantDetails(t *testing.T) {
	srv, as := newFakeAS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The fake refresh response omits the refresh token and the scope, which a
	// server is allowed to do; both must survive.
	prev := OAuthTokens{
		AccessToken:  "expired",
		RefreshToken: "ref-1",
		TokenURL:     srv.URL + "/token",
		Resource:     "https://mcp.example.com/mcp",
		ClientID:     "cid-1",
		ClientSecret: "secret-1",
		Scope:        "mcp.read",
		Expiry:       time.Now().Add(-time.Hour),
	}
	if prev.Valid(time.Now()) {
		t.Fatal("the fixture must be expired")
	}
	got, err := RefreshAccessToken(ctx, nil, prev)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got.AccessToken != "tok-2" {
		t.Fatalf("access token = %q", got.AccessToken)
	}
	if got.RefreshToken != "ref-1" || got.Scope != "mcp.read" {
		t.Fatalf("refresh token/scope must be preserved: %+v", got)
	}
	if got.ClientID != "cid-1" || got.Resource != prev.Resource {
		t.Fatalf("client/resource must be preserved: %+v", got)
	}
	as.mu.Lock()
	form := as.tokenForm
	as.mu.Unlock()
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "ref-1" {
		t.Fatalf("refresh request = %v", form)
	}
}

func TestRefreshAccessTokenRejected(t *testing.T) {
	srv, as := newFakeAS(t)
	as.mu.Lock()
	as.requireSecret = true
	as.mu.Unlock()
	_, err := RefreshAccessToken(context.Background(), nil, OAuthTokens{
		RefreshToken: "ref-1",
		TokenURL:     srv.URL + "/token",
	})
	if err == nil {
		t.Fatal("a rejected refresh must be an error")
	}
	if !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("error = %q, want the server's reason", err)
	}
}

func TestOAuthTokensValidity(t *testing.T) {
	if (OAuthTokens{}).Valid(time.Now()) {
		t.Fatal("an empty grant is not valid")
	}
	if !(OAuthTokens{AccessToken: "t"}).Valid(time.Now()) {
		t.Fatal("a token with no expiry is usable")
	}
	// A token expiring within the minute is treated as already expired.
	if (OAuthTokens{AccessToken: "t", Expiry: time.Now().Add(30 * time.Second)}).Valid(time.Now()) {
		t.Fatal("a token about to expire must not be used")
	}
	if (OAuthTokens{AccessToken: "t"}).CanRefresh() {
		t.Fatal("a grant without a refresh token cannot be refreshed")
	}
	if !(OAuthTokens{RefreshToken: "r", TokenURL: "https://as/token"}).CanRefresh() {
		t.Fatal("a full grant can be refreshed")
	}
}

// countingTokens is a TokenSource that hands out one token and records
// invalidations.
type countingTokens struct {
	mu        sync.Mutex
	token     string
	calls     int
	invalid   int
	refreshTo string
}

func (c *countingTokens) AccessToken(ctx context.Context, serverID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.token == "" {
		return "", nil
	}
	return c.token, nil
}

func (c *countingTokens) Invalidate(serverID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalid++
	if c.refreshTo != "" {
		c.token = c.refreshTo
	}
}

// TestTransportRefreshesOnUnauthorized proves the transport asks for a token,
// and that a 401 makes it drop the token and retry exactly once.
func TestTransportRefreshesOnUnauthorized(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		auths = append(auths, auth)
		mu.Unlock()
		if auth != "Bearer fresh" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://as.example.com/.well-known/oauth-protected-resource"`)
			http.Error(w, "expired token", http.StatusUnauthorized)
			return
		}
		var msg rpcMessage
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &msg)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"fake"}}}`, msg.ID)
	}))
	defer srv.Close()

	tokens := &countingTokens{token: "stale", refreshTo: "fresh"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := Connect(ctx, ServerConfig{ID: "remote", Name: "Remote", URL: srv.URL}, WithTokenSource(tokens))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	if tokens.invalid != 1 {
		t.Fatalf("invalidations = %d, want exactly 1", tokens.invalid)
	}
	mu.Lock()
	defer mu.Unlock()
	// The handshake is initialize (stale, rejected) + initialize (retry, fresh)
	// + notifications/initialized (fresh).
	if len(auths) < 2 {
		t.Fatalf("requests = %d, want at least the rejected attempt and a retry: %v", len(auths), auths)
	}
	if auths[0] != "Bearer stale" {
		t.Fatalf("the first attempt sent %q, want the stored token", auths[0])
	}
	for i, a := range auths[1:] {
		if a != "Bearer fresh" {
			t.Fatalf("request %d sent %q, want the refreshed token", i+1, a)
		}
	}
}

// TestConfiguredHeaderBeatsStoredGrant keeps a hand-entered token working.
func TestConfiguredHeaderBeatsStoredGrant(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	tokens := &countingTokens{token: "from-grant"}
	_, err := Connect(context.Background(),
		ServerConfig{ID: "r", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer pasted"}},
		WithTokenSource(tokens))
	if err == nil {
		t.Fatal("expected the 401 to surface")
	}
	if got != "Bearer pasted" {
		t.Fatalf("authorization = %q, want the configured header", got)
	}
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	if tokens.calls != 0 {
		t.Fatalf("the stored grant must not be consulted when a header is configured (calls=%d)", tokens.calls)
	}
}
