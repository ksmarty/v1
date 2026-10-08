package memory

import (
	"regexp"
	"strings"
)

var (
	privateSpan = regexp.MustCompile(`(?is)<private>.*?</private>`)
	// An opening tag with no closing tag must not leak the rest of the text, so
	// it is stripped to the end rather than left in place.
	privateOpen = regexp.MustCompile(`(?is)<private>.*$`)
)

// StripPrivate removes every <private>...</private> span from s and trims what
// is left.
//
// This is the guarantee that does not depend on the model obeying an
// instruction. The agent can be told not to store secrets and still try — and a
// memory is injected into later prompts, so a leaked credential would be
// repeated to the model for as long as the memory lives. The text is removed on
// the way in instead, and a memory that is entirely private becomes empty, which
// callers refuse.
func StripPrivate(s string) string {
	if !strings.Contains(strings.ToLower(s), "<private") {
		return strings.TrimSpace(s)
	}
	s = privateSpan.ReplaceAllString(s, "")
	s = privateOpen.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}
