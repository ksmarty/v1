package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tool must degrade clearly when the turn has no way to install a skill.
func TestCreateSkillToolUnavailable(t *testing.T) {
	e := newTestExecutor(t)
	out, err := e.createSkill(context.Background(), `{"id":"x","version":"1.0.0","changelog":"c","content":"# A\n"}`)
	if out != "" {
		t.Fatalf("an unavailable tool returned content: %q", out)
	}
	var te *ToolError
	if !errors.As(err, &te) || te.Type != "UNAVAILABLE" {
		t.Fatalf("want an UNAVAILABLE ToolError, got %v", err)
	}
}

// Version, changelog and content must be refused before the callback runs, as a
// ToolError rather than a bare error that aborts the turn.
func TestCreateSkillToolRequiresVersionAndChangelog(t *testing.T) {
	e := newTestExecutor(t)
	e.CreateSkill = func(context.Context, string, string, string, string, string) (string, error) {
		t.Fatal("the callback must not run for invalid arguments")
		return "", nil
	}
	for _, args := range []string{
		`{}`,
		`{"id":"x","changelog":"c","content":"# A\n"}`,
		`{"id":"x","version":"1.0.0","content":"# A\n"}`,
		`{"id":"x","version":"1.0.0","changelog":"c"}`,
	} {
		_, err := e.createSkill(context.Background(), args)
		var te *ToolError
		if !errors.As(err, &te) || te.Type != "BAD_ARGUMENT" {
			t.Fatalf("%s: want a BAD_ARGUMENT ToolError, got %v", args, err)
		}
	}
}

// The body can be read from a workspace file instead of passed inline, which
// keeps a large SKILL.md from being truncated inside the tool call.
func TestCreateSkillToolReadsContentPath(t *testing.T) {
	e := newTestExecutor(t)
	dir := filepath.Join(e.Root, "skills", "release-checklist")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const body = "# Release checklist\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	e.CreateSkill = func(_ context.Context, id, description, version, changelog, got string) (string, error) {
		if id != "release-checklist" || got != strings.TrimSpace(body) {
			t.Fatalf("callback got id=%q content=%q", id, got)
		}
		return "installed skill release-checklist", nil
	}
	if _, err := e.createSkill(context.Background(),
		`{"id":"release-checklist","version":"1.0.0","changelog":"first","path":"skills/release-checklist/SKILL.md"}`); err != nil {
		t.Fatal(err)
	}
}

// The chat shows a skill card, not the raw message: the display must carry the
// version, changelog and diff lifted out of the JSON-wrapped install summary.
func TestCreateSkillDisplayLiftsSummary(t *testing.T) {
	msg := "installed skill \"release-checklist\" version 1.0.0 and enabled it (20 bytes)\nchangelog: first\nchanges:\n--- a\n+++ b\n"
	out := toolResult(map[string]any{"ok": true, "message": msg})
	raw := toolDisplay("create_skill", out)
	if raw == nil {
		t.Fatal("toolDisplay returned nil")
	}
	var got struct {
		Kind      string `json:"kind"`
		Version   string `json:"version"`
		Changelog string `json:"changelog"`
		Diff      string `json:"diff"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != "skill" || got.Version != "1.0.0" || got.Changelog != "first" || !strings.Contains(got.Diff, "+++ b") {
		t.Fatalf("display = %+v", got)
	}
}

// The same parsing must work for create_extension now that it unwraps the JSON
// envelope: the changelog and diff were previously read from the escaped JSON,
// so they came back empty.
func TestCreateExtensionDisplayLiftsSummary(t *testing.T) {
	msg := "installed extension \"word-stats\" version 2.0.0 and enabled it (10 bytes)\nchangelog: second\nchanges:\n--- a\n+++ b\n"
	out := toolResult(map[string]any{"ok": true, "message": msg})
	raw := toolDisplay("create_extension", out)
	var got struct {
		Kind      string `json:"kind"`
		Version   string `json:"version"`
		Changelog string `json:"changelog"`
		Diff      string `json:"diff"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != "extension" || got.Version != "2.0.0" || got.Changelog != "second" || !strings.Contains(got.Diff, "+++ b") {
		t.Fatalf("display = %+v", got)
	}
}
