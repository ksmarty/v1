package llm

import (
	"encoding/json"
	"fmt"
	"testing"
)

// A provider with more models than the old 40-model cap used to lose everything
// past the fortieth id alphabetically — including its vision flag, which the
// screenshot_app gate reads. The list must survive intact.
func TestModelSubsetKeepsEveryToolModel(t *testing.T) {
	const n = 120
	models := make(map[string]any, n)
	for i := 0; i < n; i++ {
		models[fmt.Sprintf("vendor-%03d/model-%03d", i, i)] = map[string]any{
			"name":      fmt.Sprintf("Model %03d", i),
			"tool_call": true,
			"modalities": map[string]any{
				"input": []string{"text", "image"},
			},
			"limit": map[string]any{"context": 128000, "output": 8192},
		}
	}
	raw, err := json.Marshal(map[string]any{"models": models})
	if err != nil {
		t.Fatal(err)
	}
	var doc modelsDevDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	got := modelSubset(doc)
	if len(got) != n {
		t.Fatalf("modelSubset kept %d of %d models", len(got), n)
	}
	last := got[len(got)-1]
	if last.ID != "vendor-119/model-119" || !last.ImageInput || last.Context != 128000 {
		t.Fatalf("last entry = %+v, want the final model with its metadata", last)
	}
}

// A non-tool-calling model stays out, and the cap still bounds a runaway
// response.
func TestModelSubsetFiltersAndCaps(t *testing.T) {
	models := map[string]any{
		"text-only":  map[string]any{"tool_call": false},
		"tool-model": map[string]any{"tool_call": true},
	}
	raw, err := json.Marshal(map[string]any{"models": models})
	if err != nil {
		t.Fatal(err)
	}
	var doc modelsDevDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if got := modelSubset(doc); len(got) != 1 || got[0].ID != "tool-model" {
		t.Fatalf("modelSubset = %+v, want only the tool-calling model", got)
	}
	if maxModelsPerProvider < 400 {
		t.Fatalf("maxModelsPerProvider = %d, too low to hold a real provider's list", maxModelsPerProvider)
	}
}
