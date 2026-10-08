package memory

import "testing"

func TestStripPrivate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no tags is unchanged", "the deploy script is scripts/deploy.sh", "the deploy script is scripts/deploy.sh"},
		{"a span is removed", "key is <private>sk-abc123</private> ok", "key is  ok"},
		{"multiple spans are removed", "<private>a</private>keep<private>b</private>", "keep"},
		{"spans across newlines are removed", "before\n<private>line one\nline two</private>\nafter", "before\n\nafter"},
		{"case is ignored", "x <PRIVATE>secret</PRIVATE> y", "x  y"},
		{"a missing closing tag is removed to the end", "public part <private>sk-abc123", "public part"},
		{"an unclosed tag first wins", "<private>sk-abc", ""},
		{"whitespace is trimmed", "  <private>x</private>  ", ""},
		// Fail closed, deliberately. An opening tag with no close hides the rest
		// even when the text was only mentioning the tag: mangling a memory is a
		// far better outcome than storing a credential, and the agent is told
		// not to mention the tag in prose.
		{"a bare mention hides the rest", "the <private> tag hides secrets", "the"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripPrivate(tc.in); got != tc.want {
				t.Errorf("StripPrivate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
