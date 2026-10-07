package agent

import (
	"encoding/json"
	"testing"
)

// The background marker is stored in the messages.tool_json column, which the
// chat API serialises as json.RawMessage. If it stops being valid JSON the
// whole message list fails to encode and the chat renders as an error instead
// of a transcript, so the encoding is part of the contract.
func TestBackgroundToolJSONIsValidJSON(t *testing.T) {
	if !json.Valid([]byte(BackgroundToolJSON)) {
		t.Fatalf("BackgroundToolJSON %q is not valid JSON", BackgroundToolJSON)
	}
	var s string
	if err := json.Unmarshal([]byte(BackgroundToolJSON), &s); err != nil {
		t.Fatalf("BackgroundToolJSON is not a JSON string: %v", err)
	}
	// The frontend compares the decoded value against this literal.
	if s != "background" {
		t.Fatalf("BackgroundToolJSON decodes to %q, want background", s)
	}
}

func TestIsBackgroundRow(t *testing.T) {
	cases := []struct {
		toolJSON string
		want     bool
	}{
		{BackgroundToolJSON, true},
		{"background", true}, // legacy rows written by earlier builds
		{"", false},
		{`{"name":"run_command"}`, false},
		{`"something-else"`, false},
	}
	for _, tc := range cases {
		if got := IsBackgroundRow(tc.toolJSON); got != tc.want {
			t.Fatalf("IsBackgroundRow(%q) = %v, want %v", tc.toolJSON, got, tc.want)
		}
	}
}
