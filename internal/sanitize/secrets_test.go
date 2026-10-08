package sanitize

import (
	"strings"
	"testing"
)

// TestTextRedactsRegisteredSecretValues covers the guarantee: a value v1 knows
// is replaced wherever it appears, no matter how the agent obtained it (a
// project's .env, /proc/1/environ, a database row).
func TestTextRedactsRegisteredSecretValues(t *testing.T) {
	SetSecrets("s3cret-value-that-is-long", "short")
	defer SetSecrets()

	in := "V1_OIDC_CLIENT_SECRET=s3cret-value-that-is-long\nnote: short\n"
	got := Text(in)
	if strings.Contains(got, "s3cret-value-that-is-long") {
		t.Fatalf("registered secret survived redaction: %q", got)
	}
	if !strings.Contains(got, Redacted) {
		t.Fatalf("expected %q in %q", Redacted, got)
	}
	// A value below minSecretLen is deliberately left alone: redacting it would
	// mangle ordinary prose for no real gain.
	if !strings.Contains(got, "short") {
		t.Fatalf("short value should not be redacted: %q", got)
	}
}

func TestTextRedactsCredentialFormats(t *testing.T) {
	SetSecrets()
	cases := []struct{ name, leak string }{
		{"openai", "sk-abcdefghijklmnopqrstuvwxyz012345"},
		{"github", "ghp_" + strings.Repeat("a", 36)},
		{"github pat", "github_pat_" + strings.Repeat("b", 22)},
		{"aws", "AKIAIOSFODNN7EXAMPLE"},
		{"slack", "xoxb-1234567890-abcdefghij"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Text("found: " + tc.leak + " in the tree")
			if strings.Contains(got, tc.leak) {
				t.Fatalf("credential survived redaction: %q", got)
			}
			if !strings.Contains(got, Redacted) {
				t.Fatalf("expected %q in %q", Redacted, got)
			}
		})
	}
}

// TestTextRedactsWholePrivateKeyBlock is the case the header-only pattern would
// get wrong: replacing just the BEGIN line would leave the key material itself
// sitting in the transcript.
func TestTextRedactsWholePrivateKeyBlock(t *testing.T) {
	SetSecrets()
	in := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA1234\nmore-key-material\n-----END RSA PRIVATE KEY-----\n"
	got := Text(in)
	for _, leak := range []string{"MIIEowIBAAKCAQEA1234", "more-key-material", "BEGIN RSA PRIVATE KEY"} {
		if strings.Contains(got, leak) {
			t.Fatalf("private key material survived redaction (%q): %q", leak, got)
		}
	}
}

func TestTextLeavesOrdinaryTextAlone(t *testing.T) {
	SetSecrets()
	in := "the quick brown fox jumps over the lazy dog"
	if got := Text(in); got != in {
		t.Fatalf("ordinary text changed: %q", got)
	}
}

func TestCredentialPatternsIsACopy(t *testing.T) {
	p := CredentialPatterns()
	if len(p) == 0 {
		t.Fatal("expected credential patterns")
	}
	p[0].Label = "mutated"
	if CredentialPatterns()[0].Label == "mutated" {
		t.Fatal("CredentialPatterns returned the shared slice")
	}
}
