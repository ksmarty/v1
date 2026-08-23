package config

import (
	"reflect"
	"testing"
)

func TestLoadContextSettings(t *testing.T) {
	t.Setenv("V1_CONTEXT_BUDGET", "24000")
	t.Setenv("V1_CONTEXT_THRESHOLD", "0.7")
	c := Load("v", "c")
	if c.ContextBudget != 24000 || c.ContextThreshold != 0.7 {
		t.Fatalf("context settings = %d, %v", c.ContextBudget, c.ContextThreshold)
	}
}

func TestLoadOIDCAdminEmails(t *testing.T) {
	t.Setenv("V1_OIDC_ADMIN_EMAILS", "admin@example.com, boss@example.com, ")
	c := Load("v", "c")
	want := []string{"admin@example.com", "boss@example.com"}
	if !reflect.DeepEqual(c.OIDCAdminEmails, want) {
		t.Fatalf("OIDCAdminEmails = %v, want %v", c.OIDCAdminEmails, want)
	}
}

func TestLoadSystemPrompt(t *testing.T) {
	// No V1_SYSTEM_PROMPT -> empty; the agent falls back to its built-in base.
	t.Setenv("V1_SYSTEM_PROMPT", "")
	c := Load("v", "c")
	if c.SystemPrompt != "" {
		t.Fatalf("SystemPrompt = %q, want empty when env is unset", c.SystemPrompt)
	}
	// Explicit env overrides the built-in.
	t.Setenv("V1_SYSTEM_PROMPT", "custom prompt")
	c = Load("v", "c")
	if c.SystemPrompt != "custom prompt" {
		t.Fatalf("SystemPrompt = %q, want custom prompt", c.SystemPrompt)
	}
}
