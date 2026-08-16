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
