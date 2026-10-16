package agent

import (
	"context"
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

// A model that can read images is offered screenshot_app and told that seeing
// the app is part of done; a text-only model gets neither.
func TestVisionGatesScreenshotAndVisualVerification(t *testing.T) {
	base := ChatParams{Project: &store.Project{}, SkipCompactionSnapshot: true}
	if hasToolName(base.ToolSet(), "screenshot_app") {
		t.Error("a text-only model must not be offered screenshot_app")
	}
	if strings.Contains(BuildSystemPrompt(&base), "screenshot_app") {
		t.Error("a text-only model must not be told about screenshot_app")
	}

	vision := base
	vision.Vision = true
	if !hasToolName(vision.ToolSet(), "screenshot_app") {
		t.Error("a vision model should be offered screenshot_app")
	}
	prompt := BuildSystemPrompt(&vision)
	if !strings.Contains(prompt, "screenshot_app") || !strings.Contains(prompt, "Visual verification is part of done") {
		t.Errorf("the vision prompt does not demand visual verification:\n%s", prompt)
	}
}

// The screenshot tool must hand the model a token it can put in its reply, so
// the chat can render the image inline in the summary.
func TestScreenshotAppAdvertisesEmbedToken(t *testing.T) {
	e := &Executor{Screenshot: func(context.Context, string) ([]byte, error) {
		return []byte("\x89PNG"), nil
	}}
	out, err := e.screenshotApp(context.Background(), `{"path":"/"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "v1-screenshot") {
		t.Errorf("screenshot result does not advertise the embed token: %s", out)
	}
	if len(e.PendingImage) == 0 {
		t.Error("screenshot did not set PendingImage")
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
