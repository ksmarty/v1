package sanitize

import (
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain output", "plain output"},
		{"\x1b[31mred\x1b[0m text", "red text"},
		{"\x1b]0;title\x07osc", "osc"},
		{"line1\r\nline2", "line1\nline2"},
		{"tab\there", "tab\there"},
		{"bell\x07gone", "bellgone"},
		{"nul\x00byte", "nulbyte"},
		{"del\x7fgone", "delgone"},
		{"lone\rreturn", "lonereturn"},
		{"\xc2\xa0nbsp", "\u00a0nbsp"}, // valid UTF-8 (U+00A0) survives
	}
	for _, tc := range cases {
		if got := Text(tc.in); got != tc.want {
			t.Errorf("Text(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Invalid UTF-8 is replaced (one U+FFFD per run of invalid bytes).
	if got := Text("bad\xff\xfebytes"); got != "bad\uFFFDbytes" {
		t.Errorf("invalid utf8: %q", got)
	}
	// Clean text round-trips exactly (idempotent).
	if got, changed := Scrub("already clean"); got != "already clean" || changed {
		t.Errorf("Scrub on clean text should be a no-op: %q %v", got, changed)
	}
	if _, changed := Scrub("dirty\x1b[1mtext"); !changed {
		t.Error("Scrub should report changes on dirty text")
	}
}

// A credential learned after startup — a per-user API key read from settings —
// must be redacted too, and registering it must not drop the ones SetSecrets
// installed. SetSecrets replaces the whole set, so the two have to compose.
func TestAddSecretRedactsLaterValues(t *testing.T) {
	SetSecrets("startup-secret-value")
	t.Cleanup(func() { SetSecrets() })

	AddSecret("sk-livekey000000000000000000000000")
	AddSecret("  sk-livekey000000000000000000000000  ") // trimmed, and a no-op duplicate

	got := Text("key sk-livekey000000000000000000000000 and startup-secret-value")
	if strings.Contains(got, "livekey") {
		t.Fatalf("a secret added after startup survived: %q", got)
	}
	if strings.Contains(got, "startup-secret-value") {
		t.Fatalf("AddSecret dropped the startup secrets: %q", got)
	}

	// A short value is not a secret worth mangling prose over.
	AddSecret("short")
	if got := Text("this short text stays"); got != "this short text stays" {
		t.Fatalf("a too-short value should be ignored: %q", got)
	}
}
