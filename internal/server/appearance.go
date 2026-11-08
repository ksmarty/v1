package server

import (
	"encoding/json"
	"log"
	"strings"
)

// keyAppearance is the per-user Appearance page: the theme, which side the chat
// sits on, the tab bar, and how the chat renders itself. One JSON object rather
// than a key per knob — they are read together, saved a knob at a time, and only
// the SPA interprets them.
const keyAppearance = "appearance"

// chatTabsSetting is the project view's tab bar: the tabs in display order, and
// the ones the user hid. An empty order means "the built-in order", which keeps
// a tab added in a later release from being invisible to an existing account.
type chatTabsSetting struct {
	Order  []string `json:"order"`
	Hidden []string `json:"hidden"`
}

// appearanceSettings is the resolved Appearance state. Every field is filled,
// because the API never hands the SPA a value it would have to default itself.
type appearanceSettings struct {
	// Theme is a theme name from the SPA's catalog ("" = its default).
	Theme    string          `json:"theme"`
	ChatSide string          `json:"chatSide"`
	ChatTabs chatTabsSetting `json:"chatTabs"`
	// JSONPretty pretty-prints tool-call arguments and results.
	JSONPretty bool `json:"jsonPretty"`
	// ToolCallsCollapsed folds a message's tool calls into one line.
	ToolCallsCollapsed bool `json:"toolCallsCollapsed"`
	// TurnFold folds a finished turn to its last comment.
	TurnFold bool `json:"turnFold"`
	// ThinkingCollapsed starts reasoning blocks closed.
	ThinkingCollapsed bool `json:"thinkingCollapsed"`
}

// appearancePatch is what a PUT may change: a nil field is left alone, so one
// control saving does not push the other knobs back from a stale client copy.
type appearancePatch struct {
	Theme              *string          `json:"theme"`
	ChatSide           *string          `json:"chatSide"`
	ChatTabs           *chatTabsSetting `json:"chatTabs"`
	JSONPretty         *bool            `json:"jsonPretty"`
	ToolCallsCollapsed *bool            `json:"toolCallsCollapsed"`
	TurnFold           *bool            `json:"turnFold"`
	ThinkingCollapsed  *bool            `json:"thinkingCollapsed"`
}

// defaultAppearance matches the SPA's own defaults, so a fresh account renders
// the same before and after the settings fetch lands.
func defaultAppearance() appearanceSettings {
	return appearanceSettings{
		ChatSide:           "left",
		ChatTabs:           chatTabsSetting{Order: []string{}, Hidden: []string{}},
		JSONPretty:         true,
		ToolCallsCollapsed: true,
	}
}

// appearance resolves the user's Appearance settings over the defaults. It is
// stored whole, so a newer v1 that adds a knob reads it as its default rather
// than as a zero value.
func (s *Server) appearance(userID string) appearanceSettings {
	out := defaultAppearance()
	raw, ok := s.userSetting(userID, keyAppearance)
	if !ok || raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		log.Printf("appearance: ignoring an unreadable setting for user %q: %v", userID, err)
		return defaultAppearance()
	}
	return out.normalized()
}

// normalized clamps whatever the row (or an older release) holds to values the
// SPA can render, and never leaves a nil slice to become `null` in JSON.
func (a appearanceSettings) normalized() appearanceSettings {
	a.Theme = strings.TrimSpace(a.Theme)
	if a.ChatSide != "right" {
		a.ChatSide = "left"
	}
	if a.ChatTabs.Order == nil {
		a.ChatTabs.Order = []string{}
	}
	if a.ChatTabs.Hidden == nil {
		a.ChatTabs.Hidden = []string{}
	}
	return a
}

// applyAppearancePatch merges a patch into the stored settings. A patch that
// changes nothing back to the defaults still writes the resolved object: it is
// one row, and reading it back always yields the same values the SPA just got.
func (s *Server) applyAppearancePatch(userID string, patch appearancePatch) error {
	cur := s.appearance(userID)
	if patch.Theme != nil {
		cur.Theme = strings.TrimSpace(*patch.Theme)
	}
	if patch.ChatSide != nil {
		cur.ChatSide = *patch.ChatSide
	}
	if patch.ChatTabs != nil {
		cur.ChatTabs = *patch.ChatTabs
	}
	if patch.JSONPretty != nil {
		cur.JSONPretty = *patch.JSONPretty
	}
	if patch.ToolCallsCollapsed != nil {
		cur.ToolCallsCollapsed = *patch.ToolCallsCollapsed
	}
	if patch.TurnFold != nil {
		cur.TurnFold = *patch.TurnFold
	}
	if patch.ThinkingCollapsed != nil {
		cur.ThinkingCollapsed = *patch.ThinkingCollapsed
	}
	raw, err := json.Marshal(cur.normalized())
	if err != nil {
		return err
	}
	return s.st.SetUserSetting(userID, keyAppearance, string(raw))
}
