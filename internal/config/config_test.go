package config

import "testing"

func TestLoadContextSettings(t *testing.T) {
	t.Setenv("V1_CONTEXT_BUDGET", "24000")
	t.Setenv("V1_CONTEXT_THRESHOLD", "0.7")
	c := Load("v", "c")
	if c.ContextBudget != 24000 || c.ContextThreshold != 0.7 {
		t.Fatalf("context settings = %d, %v", c.ContextBudget, c.ContextThreshold)
	}
}
