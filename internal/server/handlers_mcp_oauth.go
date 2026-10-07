package server

// OAuth for remote MCP servers. A remote server that answers 401 is unusable
// without a token, so v1 runs the authorization-code flow with PKCE against the
// server's published metadata, stores the grant, and refreshes it silently.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"v1/internal/mcp"
)

// mcpOAuthKey is the settings key holding one server's grant.
func mcpOAuthKey(serverID string) string { return "mcp_oauth_" + serverID }

// oauthStore persists grants and implements mcp.TokenSource.
type oauthStore struct{ s *Server }

func (o *oauthStore) load(serverID string) (mcp.OAuthTokens, bool) {
	v, ok, err := o.s.st.GetSetting(mcpOAuthKey(serverID))
	if err != nil || !ok || strings.TrimSpace(v) == "" {
		return mcp.OAuthTokens{}, false
	}
	var t mcp.OAuthTokens
	if err := json.Unmarshal([]byte(v), &t); err != nil {
		return mcp.OAuthTokens{}, false
	}
	return t, true
}

func (o *oauthStore) save(serverID string, t mcp.OAuthTokens) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return o.s.st.SetSetting(mcpOAuthKey(serverID), string(raw))
}

// AccessToken returns a bearer token, refreshing an expired grant. No grant
// means an empty token: the request goes out unauthenticated and the resulting
// 401 is what tells the user to connect.
func (o *oauthStore) AccessToken(ctx context.Context, serverID string) (string, error) {
	t, ok := o.load(serverID)
	if !ok {
		return "", nil
	}
	if t.Valid(time.Now()) {
		return t.AccessToken, nil
	}
	if !t.CanRefresh() {
		return "", nil
	}
	refreshed, err := mcp.RefreshAccessToken(ctx, nil, t)
	if err != nil {
		return "", err
	}
	if err := o.save(serverID, *refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

// Invalidate expires the stored token so the next call refreshes it, keeping
// the refresh token and the client registration.
func (o *oauthStore) Invalidate(serverID string) {
	t, ok := o.load(serverID)
	if !ok {
		return
	}
	t.Expiry = time.Now().Add(-time.Hour)
	_ = o.save(serverID, t)
}

// mcpOAuthFlow is one authorization attempt awaiting its redirect back.
type mcpOAuthFlow struct {
	serverID     string
	verifier     string
	clientID     string
	clientSecret string
	redirectURI  string
	cfg          mcp.OAuthConfig
	createdAt    time.Time
}

// oauthFlowTTL bounds how long a started flow stays valid.
const oauthFlowTTL = 15 * time.Minute

// pendingOAuth holds in-flight flows, keyed by their state parameter.
type pendingOAuth struct {
	mu    sync.Mutex
	flows map[string]mcpOAuthFlow
}

func newPendingOAuth() *pendingOAuth {
	return &pendingOAuth{flows: map[string]mcpOAuthFlow{}}
}

func (p *pendingOAuth) put(state string, f mcpOAuthFlow) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Opportunistically drop anything that has gone stale.
	for k, v := range p.flows {
		if time.Since(v.createdAt) > oauthFlowTTL {
			delete(p.flows, k)
		}
	}
	p.flows[state] = f
}

func (p *pendingOAuth) take(state string) (mcpOAuthFlow, bool) {
	if state == "" {
		return mcpOAuthFlow{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f, ok := p.flows[state]
	delete(p.flows, state) // single use
	if !ok || time.Since(f.createdAt) > oauthFlowTTL {
		return mcpOAuthFlow{}, false
	}
	return f, true
}

// oauthRedirectURI is where the authorization server sends the browser back.
// It must match the URI registered with the client, so it is derived from the
// request, honouring a reverse proxy's forwarded headers.
func (s *Server) oauthRedirectURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); p != "" {
		scheme = strings.Split(p, ",")[0]
	}
	host := r.Host
	if h := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); h != "" {
		host = strings.Split(h, ",")[0]
	}
	return scheme + "://" + strings.TrimSpace(host) + "/api/mcp/oauth/callback"
}

// mcpRemoteServer finds a configured remote server by id.
func (s *Server) mcpRemoteServer(id string) (mcp.ServerConfig, bool) {
	for _, cfg := range s.mcpServers() {
		if cfg.ID == id {
			return cfg, strings.TrimSpace(cfg.URL) != ""
		}
	}
	return mcp.ServerConfig{}, false
}

// handleMCPOAuthStart discovers the server's authorization endpoints, registers
// a client if needed, and returns the URL to send the browser to.
func (s *Server) handleMCPOAuthStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg, ok := s.mcpRemoteServer(body.ID)
	if !ok {
		writeError(w, http.StatusBadRequest, "that server has no url to authorize against")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	discovered, err := mcp.DiscoverOAuth(ctx, nil, cfg.URL)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	store := &oauthStore{s: s}
	tokens, _ := store.load(body.ID)
	redirectURI := s.oauthRedirectURI(r)
	if tokens.ClientID == "" {
		clientID, secret, err := mcp.RegisterClient(ctx, nil, discovered, redirectURI, "v1")
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		tokens.ClientID, tokens.ClientSecret = clientID, secret
		if err := store.save(body.ID, tokens); err != nil {
			writeError(w, http.StatusInternalServerError, "could not store the client registration")
			return
		}
	}

	pkce, err := mcp.NewPKCE()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate the PKCE challenge")
		return
	}
	state, err := mcp.RandomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate a state parameter")
		return
	}
	s.mcpOAuthFlows.put(state, mcpOAuthFlow{
		serverID:     body.ID,
		verifier:     pkce.Verifier,
		clientID:     tokens.ClientID,
		clientSecret: tokens.ClientSecret,
		redirectURI:  redirectURI,
		cfg:          *discovered,
		createdAt:    time.Now(),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"url": mcp.AuthorizeURL(discovered, tokens.ClientID, redirectURI, state, pkce.Challenge),
	})
}

// handleMCPOAuthCallback exchanges the authorization code and stores the grant,
// then returns the browser to the settings page.
func (s *Server) handleMCPOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back := func(status, reason string) {
		target := "/settings?mcp=" + status
		if reason != "" {
			target += "&reason=" + url.QueryEscape(reason)
		}
		http.Redirect(w, r, target, http.StatusFound)
	}
	if e := q.Get("error"); e != "" {
		back("error", e+": "+q.Get("error_description"))
		return
	}
	flow, ok := s.mcpOAuthFlows.take(q.Get("state"))
	if !ok {
		back("error", "this authorization link is no longer valid — start again")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	tokens, err := mcp.ExchangeCode(ctx, nil, &flow.cfg, q.Get("code"), flow.verifier, flow.clientID, flow.clientSecret, flow.redirectURI)
	if err != nil {
		back("error", err.Error())
		return
	}
	if err := (&oauthStore{s: s}).save(flow.serverID, *tokens); err != nil {
		back("error", "the server authorized but v1 could not store the token")
		return
	}
	back("connected", "")
}

// handleMCPOAuthDisconnect forgets a server's grant.
func (s *Server) handleMCPOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.st.SetSetting(mcpOAuthKey(body.ID), ""); err != nil {
		writeError(w, http.StatusInternalServerError, "could not clear the stored token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mcpOAuthStatus reports, per server id, whether a usable grant exists. It is
// merged into the MCP status response so the UI can label the button.
func (s *Server) mcpOAuthStatus() map[string]string {
	out := map[string]string{}
	store := &oauthStore{s: s}
	for _, cfg := range s.mcpServers() {
		if strings.TrimSpace(cfg.URL) == "" {
			continue
		}
		t, ok := store.load(cfg.ID)
		switch {
		case !ok || t.AccessToken == "":
			// No grant yet. A stored client registration alone is not a
			// connection.
			out[cfg.ID] = "none"
		case t.Valid(time.Now()):
			out[cfg.ID] = "connected"
		case t.CanRefresh():
			out[cfg.ID] = "refreshable"
		default:
			out[cfg.ID] = "expired"
		}
	}
	return out
}
