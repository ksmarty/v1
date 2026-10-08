// Package sanitize scrubs text that crosses process boundaries into LLM
// provider requests. It is a leaf package: agent, llm and store all import it,
// so every path into a provider — fresh turns, retries, and histories stored
// before the scrubber existed — passes through the same cleaning rules.
package sanitize

import (
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	// ansiCSIRe matches ANSI CSI escape sequences (\x1b[...letter), common in
	// raw terminal output.
	ansiCSIRe = regexp.MustCompile(`\x1b\[[0-9;:?]*[ -/]*[@-~]`)
	// ansiOSCRe matches ANSI OSC sequences (\x1b]...ST) used for titles, etc.
	ansiOSCRe = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
)

// Redacted is what a credential is replaced with on its way to a provider.
const Redacted = "[redacted]"

// CredentialPattern is a high-precision, low-false-positive credential format.
// One list serves both purposes: redacting text on its way to a provider and
// flagging secrets committed into a project's sources (agent.scanForSecrets),
// so the two can never drift apart.
type CredentialPattern struct {
	Label string
	Re    *regexp.Regexp
}

var credentialPatterns = []CredentialPattern{
	{"OpenAI API key", regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`)},
	{"Anthropic API key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9]{20,}\b`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`)},
	{"GitHub fine-grained PAT", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{"Stripe key", regexp.MustCompile(`\b(?:sk|pk)_(?:live|test)_[A-Za-z0-9]{16,}\b`)},
	{"Private key block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
}

// CredentialPatterns returns the shared credential formats.
func CredentialPatterns() []CredentialPattern {
	out := make([]CredentialPattern, len(credentialPatterns))
	copy(out, credentialPatterns)
	return out
}

// privateKeyBlockRe matches a whole PEM private key, header through footer. It
// is applied BEFORE the header-only pattern in credentialPatterns, which would
// otherwise replace just the header and leave the key material in the text.
var privateKeyBlockRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`)

// minSecretLen is the shortest literal secret worth redacting: replacing a
// shorter string would mangle ordinary prose for no real gain.
const minSecretLen = 8

var (
	secretMu     sync.RWMutex
	secretValues []string
)

// SetSecrets registers the literal secret values this process holds — its own
// configuration secrets. Text replaces exact occurrences of them.
//
// This is the only reliable defence. Pattern matching cannot catch an arbitrary
// secret, but knowing the value can: an agent may read one from anywhere (a
// project's .env, /proc/1/environ) and whatever it prints is persisted and sent
// to a provider. Values shorter than minSecretLen are ignored.
func SetSecrets(values ...string) {
	kept := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) < minSecretLen {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		kept = append(kept, v)
	}
	secretMu.Lock()
	secretValues = kept
	secretMu.Unlock()
}

// AddSecret registers one more literal secret, for credentials learned after
// startup — a per-user API key read from settings, say. SetSecrets replaces the
// whole set; this adds to it.
func AddSecret(v string) {
	v = strings.TrimSpace(v)
	if len(v) < minSecretLen {
		return
	}
	secretMu.Lock()
	defer secretMu.Unlock()
	for _, existing := range secretValues {
		if existing == v {
			return
		}
	}
	secretValues = append(secretValues, v)
}

// redact replaces credentials: first the literal values this process knows,
// then anything matching a known credential format (which covers secrets v1
// never sees, such as a key sitting in a project's .env).
func redact(s string) string {
	secretMu.RLock()
	values := secretValues
	secretMu.RUnlock()
	for _, v := range values {
		s = strings.ReplaceAll(s, v, Redacted)
	}
	s = privateKeyBlockRe.ReplaceAllString(s, Redacted)
	for _, p := range credentialPatterns {
		s = p.Re.ReplaceAllString(s, Redacted)
	}
	return s
}

// Text removes bytes that OpenAI-compatible providers reject ("The string did
// not match the expected pattern"): ANSI escapes, C0/C1 control characters
// (newlines and tabs survive), DEL, and invalid UTF-8. It is safe to run on
// already-clean text (no-op) or on any mix of raw bytes.
func Text(s string) string {
	s = ansiOSCRe.ReplaceAllString(s, "")
	s = ansiCSIRe.ReplaceAllString(s, "")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\n', '\t', '\r':
			b.WriteRune(r)
		default:
			if r < 0x20 || r == 0x7f {
				continue
			}
			b.WriteRune(r)
		}
	}
	// \r only survives as part of \r\n; a lone \r is a legacy line ending and
	// would be left as a stray control byte otherwise. Credentials are stripped
	// last, so the redaction sees the final text.
	return redact(strings.ReplaceAll(b.String(), "\r", ""))
}

// Scrub returns the cleaned text plus whether anything changed.
func Scrub(s string) (string, bool) {
	clean := Text(s)
	return clean, clean != s
}
