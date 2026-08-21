package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig is the static OIDC (Authentik) configuration, read from the
// environment once at startup.
type OIDCConfig struct {
	// Enabled forces the flow on even when the other fields are unset.
	Enabled bool
	// Issuer, ClientID, ClientSecret and RedirectURI enable the flow when
	// all four are set; RedirectURI may be empty and derived per-request.
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURI  string
	// AllowedEmails, when non-empty, restricts sign-in to these addresses
	// (case-insensitive). An empty list allows everyone.
	AllowedEmails []string
}

// OIDC implements the authorization-code flow with PKCE and ID-token
// verification against the issuer's discovery document. The provider is
// resolved lazily so a misconfigured issuer does not fail startup.
type OIDC struct {
	cfg      OIDCConfig
	mu       sync.Mutex
	provider *oidc.Provider
}

// NewOIDC builds an OIDC client from static configuration. The issuer is
// kept verbatim (no trailing-slash trimming): go-oidc compares the passed
// issuer against the discovery document's `issuer` field, and providers
// (Authentik included) commonly publish a trailing-slash issuer, so trimming
// here would produce a false mismatch.
func NewOIDC(cfg OIDCConfig) *OIDC {
	return &OIDC{cfg: cfg}
}

// Enabled reports whether the flow is configured: either forced on via
// V1_AUTH_OIDC_ENABLED, or fully specified via issuer + client ID + client
// secret (the callback URI may be empty — it is derived per request). The
// caller is still responsible for its own auth-disabled policy.
func (o *OIDC) Enabled() bool {
	return o.cfg.Enabled ||
		(o.cfg.Issuer != "" && o.cfg.ClientID != "" && o.cfg.ClientSecret != "")
}

// Allowed reports whether the authenticated email may sign in. An empty
// allowlist admits everyone; otherwise the comparison is case-insensitive.
func (o *OIDC) Allowed(email string) bool {
	if len(o.cfg.AllowedEmails) == 0 {
		return true
	}
	for _, e := range o.cfg.AllowedEmails {
		if strings.EqualFold(e, email) {
			return true
		}
	}
	return false
}

// providerCached resolves the OIDC provider (discovery document + keys) on
// first use and caches it. Failures are returned to the caller and retried on
// the next request.
func (o *OIDC) providerCached(ctx context.Context) (*oidc.Provider, error) {
	o.mu.Lock()
	if o.provider != nil {
		p := o.provider
		o.mu.Unlock()
		return p, nil
	}
	o.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, o.cfg.Issuer)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.provider = p
	o.mu.Unlock()
	return p, nil
}

func (o *OIDC) oauthConfig(p *oidc.Provider, redirect string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     o.cfg.ClientID,
		ClientSecret: o.cfg.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  redirect,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

// AuthCodeURL builds the authorization URL for a fresh flow: PKCE S256
// challenge and a nonce are embedded so the callback can be verified.
func (o *OIDC) AuthCodeURL(ctx context.Context, state, verifier, nonce, redirect string) (string, error) {
	p, err := o.providerCached(ctx)
	if err != nil {
		return "", err
	}
	return o.oauthConfig(p, redirect).AuthCodeURL(
		state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("nonce", nonce),
	), nil
}

// VerifyCode exchanges the authorization code, verifies the returned ID
// token (signature, issuer, audience, expiry, nonce) and returns the
// authenticated email (claims.email, falling back to preferred_username).
func (o *OIDC) VerifyCode(ctx context.Context, code, verifier, nonce, redirect string) (string, error) {
	p, err := o.providerCached(ctx)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	token, err := o.oauthConfig(p, redirect).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", err
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return "", errors.New("id_token missing from token response")
	}
	idToken, err := p.Verifier(&oidc.Config{ClientID: o.cfg.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return "", err
	}
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Nonce             string `json:"nonce"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return "", err
	}
	if nonce != "" && claims.Nonce != nonce {
		return "", errors.New("id_token nonce mismatch")
	}
	email := claims.Email
	if email == "" {
		email = claims.PreferredUsername
	}
	if email == "" {
		return "", errors.New("no email claim in id_token")
	}
	return email, nil
}

// RandomHex returns n random bytes hex-encoded, for state/nonce values.
func RandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// PKCEVerifier generates a code verifier and its S256 challenge.
func PKCEVerifier() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}
