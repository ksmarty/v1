package extensions

import (
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0", "1.0.0", 0},
		{"1.10", "1.9", 1},
		{"1.9", "1.10", -1},
		{"2.0.0", "1.99.99", 1},
		{"1.0.0", "1.0.1", -1},
		{"v1.2.0", "1.1.0", 1},
		{"", "1.0.0", -1},
		{"1.0.0", "", 1},
		{"1.0", "1.0-beta", 1},
		{"1.0-beta", "1.0", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestUnifiedDiff(t *testing.T) {
	if got := UnifiedDiff("a\nb\n", "a\nb\n"); got != "" {
		t.Fatalf("equal sources produced a diff: %q", got)
	}
	diff := UnifiedDiff("a\nb\nc\n", "a\nB\nc\n")
	if !strings.Contains(diff, "1 line(s) added, 1 removed") {
		t.Fatalf("counts missing: %q", diff)
	}
	if !strings.Contains(diff, "- b") || !strings.Contains(diff, "+ B") {
		t.Fatalf("changed lines missing: %q", diff)
	}
	// Context lines survive around a change, and long unchanged runs collapse.
	long := strings.Repeat("same\n", 40) + "old\n" + strings.Repeat("tail\n", 40)
	updated := strings.Repeat("same\n", 40) + "new\n" + strings.Repeat("tail\n", 40)
	collapsed := UnifiedDiff(long, updated)
	if !strings.Contains(collapsed, "…") {
		t.Fatalf("long unchanged runs were not collapsed: %q", collapsed)
	}
	if !strings.Contains(collapsed, "+ new") {
		t.Fatalf("the change is missing: %q", collapsed)
	}
}
