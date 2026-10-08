package agent

import "testing"

// Models quote numbers unpredictably — `"step": 1` one turn and `"step": "1"`
// the next. Rejecting the whole plan over that costs a round trip and loses the
// plan, which is how a real make_plan call failed.
func TestValidatePlanAcceptsQuotedNumbers(t *testing.T) {
	plan := `{
		"goal": "ship the thing",
		"features": [
			{"id": "f1", "description": "do the thing", "depends_on": [], "status": "done"},
			{"id": "f2", "description": "do the other thing", "depends_on": ["f1"]}
		],
		"invariants": ["no secrets"],
		"checkpoints": [
			{"step": "1", "action": "build", "verification": "build passes"},
			{"step": "Step 2", "action": "test", "verification": "tests pass"},
			{"step": 3, "action": "ship", "verification": "released"}
		],
		"estimated_turns": "4"
	}`
	if err := validatePlan(plan); err != nil {
		t.Fatalf("a plan with quoted numbers should validate, got: %v", err)
	}
}

// Being lenient about quoting must not make the check toothless: a checkpoint
// with no action, and one whose step cannot be read as a number, are still bad.
func TestValidatePlanStillRejectsBadCheckpoints(t *testing.T) {
	for name, plan := range map[string]string{
		"no action":  `{"goal":"g","features":[{"id":"f1","description":"d"}],"checkpoints":[{"step":1,"action":"","verification":"v"}],"estimated_turns":2}`,
		"no verify":  `{"goal":"g","features":[{"id":"f1","description":"d"}],"checkpoints":[{"step":1,"action":"a","verification":""}],"estimated_turns":2}`,
		"step words": `{"goal":"g","features":[{"id":"f1","description":"d"}],"checkpoints":[{"step":"later","action":"a","verification":"v"}],"estimated_turns":2}`,
	} {
		if err := validatePlan(plan); err == nil {
			t.Errorf("%s: expected the plan to be rejected", name)
		}
	}
}

func TestFirstIntIn(t *testing.T) {
	for in, want := range map[string]int{
		"1": 1, "Step 2": 2, "step-12": 12, "3.": 3, "": 0, "later": 0, " 007 ": 7,
	} {
		if got := firstIntIn(in); got != want {
			t.Errorf("firstIntIn(%q) = %d, want %d", in, got, want)
		}
	}
}
