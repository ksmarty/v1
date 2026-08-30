package sanitize

import "testing"

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