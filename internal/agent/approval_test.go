package agent

import (
	"strings"
	"testing"

	"v1/internal/llm"
	"v1/internal/store"
)

// The three permission modes used to differ only in whether tool calls were
// gated, which left "don't ask for approvals" and "yolo" behaving identically.
// The difference the user asked for is about asking *questions*: auto keeps
// ask_user, yolo drops it.
func TestApprovalModeControlsAskingQuestions(t *testing.T) {
	base := ChatParams{Project: &store.Project{}, SkipCompactionSnapshot: true}

	auto := base
	auto.ApprovalMode = "auto"
	if !hasToolName(auto.ToolSet(), "ask_user") {
		t.Error("auto mode should still offer ask_user")
	}
	if !strings.Contains(BuildSystemPrompt(&auto), "ask_user still works") {
		t.Error("auto mode should tell the model that questions are still welcome")
	}
	if strings.Contains(BuildSystemPrompt(&auto), "Do not ask questions") {
		t.Error("auto mode should not tell the model to stop asking")
	}

	yolo := base
	yolo.ApprovalMode = "yolo"
	if hasToolName(yolo.ToolSet(), "ask_user") {
		t.Error("yolo mode should not offer ask_user at all")
	}
	if !strings.Contains(BuildSystemPrompt(&yolo), "Do not ask questions") {
		t.Error("yolo mode should tell the model not to ask")
	}

	// The default (ask) is untouched: the tool is offered and neither note is
	// injected.
	ask := base
	prompt := BuildSystemPrompt(&ask)
	if !hasToolName(ask.ToolSet(), "ask_user") {
		t.Error("ask mode should offer ask_user")
	}
	if strings.Contains(prompt, "ask_user still works") || strings.Contains(prompt, "Do not ask questions") {
		t.Error("ask mode should inject no approval note")
	}
}

// yolo must not loosen anything else: the other tools stay available.
func TestYoloKeepsEveryOtherTool(t *testing.T) {
	base := ChatParams{Project: &store.Project{}, SkipCompactionSnapshot: true}
	plain := len(base.ToolSet())
	base.ApprovalMode = "yolo"
	if got := len(base.ToolSet()); got != plain-1 {
		t.Fatalf("yolo removed %d tools, want exactly 1 (ask_user)", plain-got)
	}
	for _, name := range []string{"run_command", "write_file", "edit_file"} {
		if !hasToolName(base.ToolSet(), name) {
			t.Errorf("yolo should still offer %s", name)
		}
	}
}

func hasToolName(tools []llm.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
