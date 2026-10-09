package server

import "testing"

// The schema comes from the loaded module, so these helpers have to survive
// whatever the harness reports — including nothing at all, when it is not
// running.
func TestExtensionSchemaReadsTheHarnessState(t *testing.T) {
	state := map[string]any{"loaded": []any{
		map[string]any{"id": "word-count", "settings": []any{map[string]any{"key": "limit"}}},
		map[string]any{"id": "other"},
	}}
	if got := extensionSchema(state, "word-count"); len(got) != 1 {
		t.Fatalf("schema = %#v", got)
	}
	if got := extensionSchema(state, "other"); len(got) != 0 {
		t.Fatalf("extension with no fields = %#v", got)
	}
	if got := extensionSchema(map[string]any{"available": false}, "word-count"); got != nil {
		t.Fatalf("harness not running = %#v", got)
	}
}

func TestExtensionSettingValuesMergesDefaultsUnderSaved(t *testing.T) {
	schema := []any{
		map[string]any{"key": "limit", "type": "text", "default": "500"},
		map[string]any{"key": "strict", "type": "checkbox", "default": false},
		map[string]any{"key": "note", "type": "text"},
	}
	stored := map[string]any{"limit": "10"}
	got := extensionSettingValues(schema, stored)
	if got["limit"] != "10" {
		t.Fatalf("saved value did not win: %#v", got)
	}
	// A declared default applies before the user has saved anything, so a field
	// added by a later version of an extension is never undefined.
	if got["strict"] != false {
		t.Fatalf("checkbox default missing: %#v", got)
	}
	// A field with no declared default stays absent rather than becoming an
	// empty string, which the extension can tell apart from "unset".
	if _, ok := got["note"]; ok {
		t.Fatalf("a field with no default appeared: %#v", got)
	}
	if _, ok := stored["strict"]; ok {
		t.Fatal("the merge wrote into the stored map")
	}
}

func TestCleanExtensionSettingsKeepsDeclaredKeysAndTypes(t *testing.T) {
	schema := []any{
		map[string]any{"key": "limit", "type": "text"},
		map[string]any{"key": "strict", "type": "checkbox"},
	}
	clean := cleanExtensionSettings(schema, map[string]any{
		"limit":  "20",
		"strict": true,
		"gone":   "x",
	})
	if len(clean) != 2 || clean["limit"] != "20" || clean["strict"] != true {
		t.Fatalf("cleaned values = %#v", clean)
	}
	// An undeclared key would otherwise sit in the form forever.
	if _, ok := clean["gone"]; ok {
		t.Fatalf("an undeclared key survived: %#v", clean)
	}
	// A value of the wrong type would reach the extension as a surprise.
	wrong := cleanExtensionSettings(schema, map[string]any{"limit": 5, "strict": "yes"})
	if len(wrong) != 0 {
		t.Fatalf("wrongly typed values survived: %#v", wrong)
	}
}
