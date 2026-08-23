package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"v1/internal/llm"
)

// PlanDocument is the strict JSON shape the planner produces and make_plan /
// update_plan accept. It is validated before storage and rendered as the
// "## Active Plan" section of the system prompt on every subsequent turn.
type PlanDocument struct {
	Goal           string           `json:"goal"`
	Features       []PlanFeature    `json:"features"`
	Invariants     []string         `json:"invariants"`
	Checkpoints    []PlanCheckpoint `json:"checkpoints"`
	EstimatedTurns int              `json:"estimated_turns"`
}

type PlanFeature struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	DependsOn   []string `json:"depends_on,omitempty"`
}

type PlanCheckpoint struct {
	Step         int    `json:"step"`
	Action       string `json:"action"`
	Verification string `json:"verification"`
}

// planPrompt is the lightweight planner's system prompt: minimal context,
// strict JSON output, no tool use. The planner is only asked when there is
// no active plan yet, so the main thread of every turn stays untouched.
const planPrompt = `You are a planning assistant for an AI coding agent. Given the user's request and the project context, produce a short structured plan the agent will execute.

Output STRICT JSON with this exact schema, nothing else:

{
  "goal": "one-sentence task description",
  "features": [
    {"id": "f1", "description": "...", "depends_on": []}
  ],
  "invariants": ["rules the agent must never break"],
  "checkpoints": [
    {"step": 1, "action": "install dependencies", "verification": "npm install succeeds"}
  ],
  "estimated_turns": 5
}

Rules:
- 3-7 features, each with a unique id like f1, f2, and one-line description.
- depends_on lists the feature ids that must complete first (empty for none).
- 2-5 invariants: hard rules (e.g. "never expose secrets in client code").
- 2-5 checkpoints: when to run install/build/preview/tests and what proves each step worked.
- estimated_turns: your guess at how many agent turns this needs.

No markdown, no commentary — only the JSON object.`

// GeneratePlan runs the lightweight planner over the user's request and
// returns a canonical (validated, compact) plan JSON string. It never fails
// the turn: callers treat any error as "no plan".
func GeneratePlan(ctx context.Context, client *llm.Client, userMessage string) (string, error) {
	planCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	messages := []llm.Message{
		{Role: "system", Content: planPrompt},
		{Role: "user", Content: userMessage},
	}
	res, err := client.ChatStream(planCtx, messages, nil, func(string) {}, func(string) {})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Text) == "" {
		return "", fmt.Errorf("planner returned an empty response")
	}
	return canonicalPlan(res.Text)
}

// canonicalPlan parses a raw model response into a PlanDocument, validates
// the required structure, and re-marshals it compactly so whatever is stored
// in the plans table is normalized (and the rendered prompt stays readable).
func canonicalPlan(raw string) (string, error) {
	// Tolerate fenced output: some models wrap JSON in ```json ... ```.
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}
	var doc PlanDocument
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", fmt.Errorf("planner output is not JSON: %w", err)
	}
	if strings.TrimSpace(doc.Goal) == "" {
		return "", fmt.Errorf("plan missing goal")
	}
	if len(doc.Features) == 0 {
		return "", fmt.Errorf("plan has no features")
	}
	if len(doc.Invariants) == 0 {
		return "", fmt.Errorf("plan has no invariants")
	}
	// Normalize the features the agent will actually key on.
	seen := map[string]bool{}
	for i := range doc.Features {
		f := &doc.Features[i]
		f.ID = strings.TrimSpace(f.ID)
		f.Description = strings.TrimSpace(f.Description)
		if f.ID == "" {
			f.ID = fmt.Sprintf("f%d", i+1)
		}
		if f.Description == "" {
			return "", fmt.Errorf("feature %q has no description", f.ID)
		}
		if seen[f.ID] {
			return "", fmt.Errorf("duplicate feature id %q", f.ID)
		}
		seen[f.ID] = true
	}
	for i := range doc.Checkpoints {
		if doc.Checkpoints[i].Step == 0 {
			doc.Checkpoints[i].Step = i + 1
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// looksLikePlanTask decides whether a first message warrants the auto-planner:
// substantive (multi-word, not a slash command) and not already a planning
// turn. Cheap and conservative — when unsure, we skip rather than burn a
// model round trip.
func looksLikePlanTask(msg string) bool {
	msg = strings.TrimSpace(msg)
	if msg == "" || strings.HasPrefix(msg, "/") {
		return false
	}
	return len(strings.Fields(msg)) >= 8
}

// runAutoPlan generates and stores a plan for a message when the turn has no
// active plan yet (AutoPlan enabled), returning the "## Active Plan" prompt
// section to inject, or "". Any failure is silent: the main loop just runs
// without a plan, exactly as before.
func runAutoPlan(ctx context.Context, p *ChatParams) string {
	if p == nil || !p.AutoPlan || p.PlanPrompt != "" || p.Store == nil || p.Client == nil {
		return ""
	}
	if !looksLikePlanTask(p.Message) {
		return ""
	}
	plan, err := GeneratePlan(ctx, p.Client, p.Message)
	if err != nil {
		return ""
	}
	if err := p.Store.SetPlan(p.Project.ID, plan); err != nil {
		return ""
	}
	if p.Emit != nil {
		p.Emit(ChatEvent{Type: "info", Text: "planned the task before starting"})
	}
	return "## Active Plan\n" + plan
}
