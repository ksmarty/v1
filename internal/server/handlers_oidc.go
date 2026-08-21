package server

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"v1/internal/auth"
	"v1/internal/store"
)

// OIDC authorization-code flow with PKCE (Authentik and other providers).
// Flow state lives in memory only, keyed by the random state value.

type oidcFlow struct {
	codeVerifier string
	nonce        string
	expiresAt    time.Time
}

const oidcFlowTTL = 10 * time.Minute

// oidcRedirectURI resolves the callback URI: the configured (env or
// admin-saved) callback URI wins, otherwise it is derived from the request
// (X-Forwarded-Proto when present, else the TLS state).
func (s *Server) oidcRedirectURI(r *http.Request) string {
	if uri := s.oidcConfig().RedirectURI; uri != "" {
		return uri
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/auth/oidc/callback"
}

// splitCSV splits a comma-separated list, trimming whitespace and dropping
// empties.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// handleOIDCSettings returns the effective OIDC configuration for the
// settings UI (admin only). The client secret is only reported as a boolean;
// an empty callbackUri means the per-request default is used.
func (s *Server) handleOIDCSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	cfg := s.oidcConfig()
	// A field is read-only in the UI when its env var is configured — the env
	// is the deployment-level source of truth, and editing those in the UI
	// would just be overridden at the next restart anyway.
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":               cfg.Issuer,
		"clientId":             cfg.ClientID,
		"clientSecretSet":      cfg.ClientSecret != "",
		"callbackUri":          cfg.RedirectURI,
		"allowedEmails":        strings.Join(cfg.AllowedEmails, ", "),
		"enabled":              s.oidcEnabled(),
		"issuerFromEnv":        s.cfg.OIDCIssuer != "",
		"clientIdFromEnv":      s.cfg.OIDCClientID != "",
		"clientSecretFromEnv":  s.cfg.OIDCClientSecret != "",
		"callbackUriFromEnv":   s.cfg.OIDCRedirectURI != "",
		"allowedEmailsFromEnv": len(s.cfg.OIDCAllowedEmails) > 0,
	})
}

// handleOIDCSettingsSave stores the admin-saved OIDC configuration and
// rebuilds the OIDC client. Empty fields fall back to the environment; an
// empty client secret keeps the previously saved one.
func (s *Server) handleOIDCSettingsSave(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var body struct {
		Issuer        string `json:"issuer"`
		ClientID      string `json:"clientId"`
		ClientSecret  string `json:"clientSecret"`
		CallbackURI   string `json:"callbackUri"`
		AllowedEmails string `json:"allowedEmails"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	set := func(key, val string) {
		_ = s.st.SetSetting(key, strings.TrimSpace(val))
	}
	set(keyOIDCIssuer, body.Issuer)
	set(keyOIDCClientID, body.ClientID)
	if body.ClientSecret != "" {
		set(keyOIDCClientSecret, body.ClientSecret)
	}
	set(keyOIDCCallbackURI, body.CallbackURI)
	set(keyOIDCAllowedEmails, body.AllowedEmails)
	s.rebuildOIDC()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": s.oidcEnabled()})
}

// pruneOIDCFlows drops expired flows so the in-memory map cannot grow without
// bound. Called on start and before each new flow.
func (s *Server) pruneOIDCFlows() {
	now := time.Now()
	s.oidcMu.Lock()
	for state, f := range s.oidcFlows {
		if now.After(f.expiresAt) {
			delete(s.oidcFlows, state)
		}
	}
	s.oidcMu.Unlock()
}

// handleOIDCStart begins an authorization-code flow: PKCE challenge, state
// and nonce are generated, the verifier is stored, and the browser is
// redirected to the issuer's authorization endpoint.
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if !s.oidcEnabled() {
		writeError(w, http.StatusNotFound, "oidc_disabled")
		return
	}
	state, err := auth.RandomHex(32)
	if err != nil {
		log.Printf("oidc: generating state: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	verifier, _, err := auth.PKCEVerifier()
	if err != nil {
		log.Printf("oidc: generating PKCE verifier: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nonce, err := auth.RandomHex(16)
	if err != nil {
		log.Printf("oidc: generating nonce: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.pruneOIDCFlows()
	s.oidcMu.Lock()
	s.oidcFlows[state] = &oidcFlow{
		codeVerifier: verifier,
		nonce:        nonce,
		expiresAt:    time.Now().Add(oidcFlowTTL),
	}
	s.oidcMu.Unlock()

	redirect := s.oidcRedirectURI(r)
	authURL, err := s.oidc.AuthCodeURL(r.Context(), state, verifier, nonce, redirect)
	if err != nil {
		// The browser sees this as a 502 (Cloudflare passes it through).
		// Log the full error with the issuer so the operator can diagnose
		// (unreachable issuer, wrong discovery URL, TLS from the container…).
		cfg := s.oidcConfig()
		log.Printf("oidc: building auth URL (issuer %q, redirect %q): %v", cfg.Issuer, redirect, err)
		writeError(w, http.StatusBadGateway, "OIDC discovery failed for issuer: "+cfg.Issuer+" — "+err.Error())
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleOIDCCallback exchanges the code, verifies the ID token and the email
// allowlist, then creates a session and redirects into the app. Failures
// redirect to /login with an oidc marker instead of showing an error page.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(why string) {
		http.Redirect(w, r, "/login?oidc="+why, http.StatusFound)
	}
	q := r.URL.Query()
	if q.Get("error") != "" || q.Get("state") == "" || q.Get("code") == "" {
		fail("error")
		return
	}
	state := q.Get("state")
	s.oidcMu.Lock()
	flow, ok := s.oidcFlows[state]
	if ok && time.Now().After(flow.expiresAt) {
		delete(s.oidcFlows, state)
		ok = false
	}
	if ok {
		delete(s.oidcFlows, state)
	}
	s.oidcMu.Unlock()
	if !ok {
		fail("error")
		return
	}

	redirect := s.oidcRedirectURI(r)
	email, err := s.oidc.VerifyCode(r.Context(), q.Get("code"), flow.codeVerifier, flow.nonce, redirect)
	if err != nil {
		log.Printf("oidc: verifying code: %v", err)
		fail("error")
		return
	}
	if !s.oidc.Allowed(email) {
		log.Printf("oidc: sign-in denied for %s", email)
		fail("denied")
		return
	}
	u, err := s.st.GetUserByUsername(email)
	if errors.Is(err, store.ErrNotFound) {
		// Auto-provision: the first OIDC sign-in for an email creates the
		// account. The random hash keeps the row valid for users that never
		// use a password. Emails listed in V1_OIDC_ADMIN_EMAILS become admins.
		admin := s.isOIDCAdmin(email)
		u, err = s.auth.CreateUser(email, store.NewID(), admin)
		if err == nil {
			u.OIDC = true
			_ = s.st.SetUserOIDC(u.ID, true)
		}
	} else if err == nil && !u.IsAdmin && s.isOIDCAdmin(email) {
		// A listed email signing in later gets promoted to admin.
		if ua := s.st.SetUserAdmin(u.ID, true); ua == nil {
			u.IsAdmin = true
		}
	} else if err == nil && !u.OIDC {
		u.OIDC = true
		_ = s.st.SetUserOIDC(u.ID, true)
	}
	if err != nil {
		log.Printf("oidc: resolving user %s: %v", email, err)
		fail("error")
		return
	}
	if err := s.auth.CreateSession(w, r, u.ID); err != nil {
		log.Printf("oidc: creating session: %v", err)
		fail("error")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}
