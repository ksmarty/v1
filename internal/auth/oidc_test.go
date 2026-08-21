package auth

import "testing"

func TestOIDCEnabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  OIDCConfig
		want bool
	}{
		{"empty config", OIDCConfig{}, false},
		{"forced on", OIDCConfig{Enabled: true}, true},
		{"forced on even when incomplete", OIDCConfig{Enabled: true, Issuer: "issuer"}, true},
		{"partial config (no issuer)", OIDCConfig{ClientID: "id", ClientSecret: "secret"}, false},
		{"missing secret", OIDCConfig{Issuer: "issuer", ClientID: "id", RedirectURI: "uri"}, false},
		{"minimal config, no redirect", OIDCConfig{Issuer: "issuer", ClientID: "id", ClientSecret: "secret"}, true},
		{"complete config", OIDCConfig{Issuer: "issuer", ClientID: "id", ClientSecret: "secret", RedirectURI: "uri"}, true},
	}
	for _, c := range cases {
		if got := NewOIDC(c.cfg).Enabled(); got != c.want {
			t.Errorf("%s: Enabled() = %v, want %v", c.name, got, c.want)
		}
	}
}

// The issuer must be passed to go-oidc verbatim. Authentik (and other
// providers) publish a trailing-slash issuer in their discovery document
// (https://…/application/o/v1/); NewOIDC must not trim it, or go-oidc's
// "issuer did not match" check fails and /oidc/start 502s.
func TestOIDCIssuerKeptVerbatim(t *testing.T) {
	issuer := "https://auth.example.com/application/o/v1/"
	o := NewOIDC(OIDCConfig{Issuer: issuer, ClientID: "id", ClientSecret: "secret"})
	if o.cfg.Issuer != issuer {
		t.Fatalf("issuer was rewritten: got %q, want %q", o.cfg.Issuer, issuer)
	}
}

func TestOIDCAllowed(t *testing.T) {
	o := NewOIDC(OIDCConfig{AllowedEmails: []string{"Alice@Example.com"}})
	cases := []struct {
		email string
		want  bool
	}{
		{"alice@example.com", true},
		{"ALICE@EXAMPLE.COM", true},
		{"bob@example.com", false},
		{"", false},
	}
	for _, c := range cases {
		if got := o.Allowed(c.email); got != c.want {
			t.Errorf("Allowed(%q) = %v, want %v", c.email, got, c.want)
		}
	}
	open := NewOIDC(OIDCConfig{})
	if !open.Allowed("anyone@example.com") {
		t.Errorf("empty allowlist must allow everyone")
	}
}
