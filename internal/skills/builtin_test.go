package skills

import (
	"strings"
	"testing"
)

// The bundled extension-authoring skill must be listed so the agent can load
// it, and its SKILL.md must be a real document. The source is a Go raw string
// literal, so a stray backtick would break the build; assert the prose stays
// free of one.
func TestBuiltinsIncludesV1Extensions(t *testing.T) {
	var found Builtin
	var ok bool
	for _, b := range Builtins() {
		if b.Skill.ID == "v1-extensions" {
			found, ok = b, true
			break
		}
	}
	if !ok {
		t.Fatal("the v1-extensions builtin is not listed by Builtins()")
	}
	if !found.Skill.Enabled {
		t.Fatal("the v1-extensions builtin is not enabled by default")
	}
	if found.Skill.Dir != "v1-extensions" {
		t.Fatalf("v1-extensions dir = %q, want %q", found.Skill.Dir, "v1-extensions")
	}
	body, ok := found.Files["SKILL.md"]
	if !ok {
		t.Fatal("v1-extensions has no SKILL.md")
	}
	if strings.TrimSpace(body) == "" {
		t.Fatal("v1-extensions SKILL.md is empty")
	}
	if strings.Contains(body, "`") {
		t.Fatal("v1-extensions SKILL.md contains a backtick, which cannot live in the Go raw string literal")
	}
	for _, want := range []string{"create_extension", "pi.defineTool", "index.js"} {
		if !strings.Contains(body, want) {
			t.Fatalf("v1-extensions SKILL.md does not mention %q", want)
		}
	}
}
