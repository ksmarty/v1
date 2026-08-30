// Package sanitize scrubs text that crosses process boundaries into LLM
// provider requests. It is a leaf package: agent, llm and store all import it,
// so every path into a provider — fresh turns, retries, and histories stored
// before the scrubber existed — passes through the same cleaning rules.
package sanitize

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	// ansiCSIRe matches ANSI CSI escape sequences (\x1b[...letter), common in
	// raw terminal output.
	ansiCSIRe = regexp.MustCompile(`\x1b\[[0-9;:?]*[ -/]*[@-~]`)
	// ansiOSCRe matches ANSI OSC sequences (\x1b]...ST) used for titles, etc.
	ansiOSCRe = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
)

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
	// would be left as a stray control byte otherwise.
	return strings.ReplaceAll(b.String(), "\r", "")
}

// Scrub returns the cleaned text plus whether anything changed.
func Scrub(s string) (string, bool) {
	clean := Text(s)
	return clean, clean != s
}