package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"v1/internal/skills"
)

// create_skill writes a SKILL.md, enables it and records its version/changelog.
func TestCreateSkillInstalls(t *testing.T) {
	s, _, _ := newAuthServer(t)
	ctx := context.Background()
	const body = "# Release checklist\n\n- [ ] bump the version\n"
	msg, err := s.createSkill(ctx, "release-checklist", "how to release", "1.0.0", "first", body)
	if err != nil {
		t.Fatalf("createSkill: %v", err)
	}
	if !strings.Contains(msg, "version 1.0.0") || !strings.Contains(msg, "changelog: first") {
		t.Fatalf("message missing version/changelog: %q", msg)
	}
	data, err := os.ReadFile(filepath.Join(s.skillsRoot(), "release-checklist", "SKILL.md"))
	if err != nil {
		t.Fatalf("SKILL.md not written: %v", err)
	}
	if string(data) != strings.TrimSpace(body) {
		t.Fatalf("SKILL.md = %q, want %q", data, strings.TrimSpace(body))
	}
	list := s.installedSkills()
	var found *skills.Skill
	for i := range list {
		if list[i].ID == "release-checklist" {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatal("skill not in the installed list")
	}
	if found.Version != "1.0.0" || found.Changelog != "first" || !found.Enabled || found.Dir != "release-checklist" {
		t.Fatalf("installed skill = %+v", *found)
	}
}

// A re-install must replace the SKILL.md in place and refuse a version that
// does not move forward, so an update is never mistaken for the install it
// replaces.
func TestCreateSkillRequiresNewerVersion(t *testing.T) {
	s, _, _ := newAuthServer(t)
	ctx := context.Background()
	if _, err := s.createSkill(ctx, "release-checklist", "", "1.0.0", "first", "# A\n"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if _, err := s.createSkill(ctx, "release-checklist", "", "1.0.0", "again", "# B\n"); err == nil {
		t.Fatal("a same-version update should be refused")
	}
	if _, err := s.createSkill(ctx, "release-checklist", "", "0.9.0", "older", "# B\n"); err == nil {
		t.Fatal("an older-version update should be refused")
	}
	if _, err := s.createSkill(ctx, "release-checklist", "", "1.1.0", "second", "# B\n"); err != nil {
		t.Fatalf("newer install: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(s.skillsRoot(), "release-checklist", "SKILL.md"))
	if string(data) != "# B" {
		t.Fatalf("SKILL.md was not replaced: %q", data)
	}
	count := 0
	for _, sk := range s.installedSkills() {
		if sk.ID == "release-checklist" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("re-install left %d entries, want 1", count)
	}
}

// A body that is not Markdown with a heading, or an id that escapes the root,
// must be refused.
func TestCreateSkillRejectsBadInput(t *testing.T) {
	s, _, _ := newAuthServer(t)
	ctx := context.Background()
	if _, err := s.createSkill(ctx, "x", "", "1.0.0", "c", "no heading"); err == nil {
		t.Fatal("a body without a heading should be refused")
	}
	if _, err := s.createSkill(ctx, "../escape", "", "1.0.0", "c", "# A\n"); err == nil {
		t.Fatal("an escaping id should be refused")
	}
}
