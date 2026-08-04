package server

import (
	"log"
	"net/http"
	"strings"
	"time"

	"v1/internal/auth"
)

// OIDC authorization-code flow with PKCE (Authentik and other providers).
// Flow state lives in memory only, keyed by the random state value.

type oidcFlow struct {
	codeVerifier string
	nonce        string
	expiresAt    time.Time
}

const oidcFlowTTL = 10 * time.Minute

// oidcRedirectURI resolves the callback URI: the configured
// V1_OIDC_REDIRECT_URI wins, otherwise it is derived from the request
// (X-Forwarded-Proto when present, else the TLS state).
func (s *Server) oidcRedirectURI(r *http.Request) string {
	if s.cfg.OIDCRedirectURI != "" {
		return s.cfg.OIDCRedirectURI
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/auth/oidc/callback"
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
		log.Printf("oidc: discovering issuer %s: %v", s.cfg.OIDCIssuer, err)
		writeError(w, http.StatusBadGateway, err.Error())
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
	if err := s.auth.CreateSession(w, r); err != nil {
		log.Printf("oidc: creating session: %v", err)
		fail("error")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}
