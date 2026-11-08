package server

import (
	"testing"
)

// The Appearance page is account-scoped, so a user who never opened it must get
// the SPA's own defaults rather than zero values (a zero ChatSide would render
// the chat on neither side, and a nil ChatTabs order as `null`).
func TestAppearanceDefaultsWhenUnset(t *testing.T) {
	s, _, _ := newHarnessTestServer(t)
	a := s.appearance("user-1")
	if a.ChatSide != "left" || !a.JSONPretty || !a.ToolCallsCollapsed || a.TurnFold || a.ThinkingCollapsed {
		t.Fatalf("unexpected defaults: %+v", a)
	}
	if a.ChatTabs.Order == nil || a.ChatTabs.Hidden == nil {
		t.Fatalf("tab lists must never be nil: %+v", a.ChatTabs)
	}
}

// One control saving must not push the other knobs back to their old values:
// patches merge into the stored object, they do not replace it.
func TestAppearancePatchMergesKnobs(t *testing.T) {
	s, _, _ := newHarnessTestServer(t)
	if err := s.applyAppearancePatch("user-1", appearancePatch{ChatSide: strPtr("right")}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyAppearancePatch("user-1", appearancePatch{TurnFold: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	a := s.appearance("user-1")
	if a.ChatSide != "right" {
		t.Fatalf("chat side lost by a later patch: %+v", a)
	}
	if !a.TurnFold {
		t.Fatalf("turn fold not stored: %+v", a)
	}
	// Per user: another account is untouched.
	if other := s.appearance("user-2"); other.ChatSide != "left" || other.TurnFold {
		t.Fatalf("leaked to another user: %+v", other)
	}
}

func TestAppearanceNormalizesStoredValues(t *testing.T) {
	s, _, _ := newHarnessTestServer(t)
	if err := s.st.SetUserSetting("user-1", keyAppearance, `{"chatSide":"sideways","theme":"  v1 Dark  "}`); err != nil {
		t.Fatal(err)
	}
	a := s.appearance("user-1")
	if a.ChatSide != "left" {
		t.Fatalf("unknown chat side not clamped: %q", a.ChatSide)
	}
	if a.Theme != "v1 Dark" {
		t.Fatalf("theme not trimmed: %q", a.Theme)
	}
	// A row v1 cannot read at all falls back to the defaults instead of an
	// empty appearance.
	if err := s.st.SetUserSetting("user-1", keyAppearance, "{nope"); err != nil {
		t.Fatal(err)
	}
	if a := s.appearance("user-1"); !a.ToolCallsCollapsed {
		t.Fatalf("unreadable row did not fall back: %+v", a)
	}
}

func strPtr(v string) *string { return &v }
func boolPtr(v bool) *bool    { return &v }
