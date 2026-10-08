package skills

import (
	"reflect"
	"testing"
)

func TestPreviewPaths(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   []string
	}{
		{"empty", "", []string{"SKILL.md"}},
		{"dot", ".", []string{"SKILL.md"}},
		{"directory", "skills/pdf", []string{"skills/pdf/SKILL.md", "SKILL.md"}},
		{"leading dot slash", "./skills/pdf", []string{"skills/pdf/SKILL.md", "SKILL.md"}},
		{"leading slash", "/skills/pdf", []string{"skills/pdf/SKILL.md", "SKILL.md"}},
		{"trailing slash", "skills/pdf/", []string{"skills/pdf/SKILL.md", "SKILL.md"}},
		{"the file itself", "skills/pdf/SKILL.md", []string{"skills/pdf/SKILL.md", "SKILL.md"}},
		{"the file at the root", "SKILL.md", []string{"SKILL.md"}},
		{"lowercase file", "skills/pdf/skill.md", []string{"skills/pdf/SKILL.md", "SKILL.md"}},
		{"padded", "  skills/pdf  ", []string{"skills/pdf/SKILL.md", "SKILL.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := previewPaths(tc.source)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("previewPaths(%q) = %v, want %v", tc.source, got, tc.want)
			}
		})
	}
}
