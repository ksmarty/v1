package config

import (
	"path/filepath"
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

func TestHarnessDefaultsToSidecar(t *testing.T) {
	// The pi-durable sidecar is the chat harness now; V1_HARNESS=go remains the
	// escape hatch back to the built-in loop.
	c := Load("v", "c")
	if c.HarnessMode != HarnessPi || !c.HarnessEnabled() {
		t.Fatalf("HarnessMode = %q, enabled = %v; want the pi-durable sidecar", c.HarnessMode, c.HarnessEnabled())
	}
	if c.SidecarCmd != "node" {
		t.Fatalf("SidecarCmd = %q, want node", c.SidecarCmd)
	}
	if c.SidecarSocket == "" || c.HarnessDB == "" {
		t.Fatalf("socket/db paths must have defaults: %q %q", c.SidecarSocket, c.HarnessDB)
	}
	if c.SidecarSocket != filepath.Join(c.DataDir, "harness.sock") {
		t.Fatalf("SidecarSocket = %q, want it under the data dir", c.SidecarSocket)
	}
	if c.HarnessDB != filepath.Join(c.DataDir, "harness.sqlite") {
		t.Fatalf("HarnessDB = %q, want it under the data dir", c.HarnessDB)
	}
	if c.MaxSidecarRestarts != 3 {
		t.Fatalf("MaxSidecarRestarts = %d, want 3", c.MaxSidecarRestarts)
	}
}

func TestHarnessEnvOverrides(t *testing.T) {
	t.Setenv("V1_HARNESS", "PI") // case-insensitive
	t.Setenv("V1_SIDECAR_CMD", "bun")
	t.Setenv("V1_SIDECAR_SCRIPT", "/opt/v1/sidecar/host.js")
	t.Setenv("V1_SIDECAR_SOCKET", "/run/v1/harness.sock")
	t.Setenv("V1_HARNESS_DB", "/var/lib/v1/harness.sqlite")
	t.Setenv("V1_SIDECAR_MAX_RESTARTS", "7")
	c := Load("v", "c")
	if !c.HarnessEnabled() {
		t.Fatalf("HarnessMode = %q, want pi", c.HarnessMode)
	}
	if c.SidecarCmd != "bun" || c.SidecarScript != "/opt/v1/sidecar/host.js" {
		t.Fatalf("sidecar command = %q %q", c.SidecarCmd, c.SidecarScript)
	}
	if c.SidecarSocket != "/run/v1/harness.sock" || c.HarnessDB != "/var/lib/v1/harness.sqlite" {
		t.Fatalf("sidecar paths = %q %q", c.SidecarSocket, c.HarnessDB)
	}
	if c.MaxSidecarRestarts != 7 {
		t.Fatalf("MaxSidecarRestarts = %d, want 7", c.MaxSidecarRestarts)
	}
}

func TestHarnessUnknownModeFallsBackToTheDefault(t *testing.T) {
	t.Setenv("V1_HARNESS", "rust")
	c := Load("v", "c")
	if c.HarnessMode != HarnessPi {
		t.Fatalf("HarnessMode = %q, want the default for an unknown value", c.HarnessMode)
	}
}

func TestHarnessGoEscapeHatch(t *testing.T) {
	t.Setenv("V1_HARNESS", "go")
	c := Load("v", "c")
	if c.HarnessMode != HarnessGo || c.HarnessEnabled() {
		t.Fatalf("HarnessMode = %q, enabled = %v; want the built-in loop", c.HarnessMode, c.HarnessEnabled())
	}
}
