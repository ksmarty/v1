package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProviderModelsEndpoint(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[{"id":"z-model"},{"id":"a-model"},{"id":"b-model"}]}`))
	}))
	defer ts.Close()

	models, err := ProviderModelsEndpoint(context.Background(), ts.URL+"/v1", "secret")
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}
	if models[0].ID != "a-model" || models[2].ID != "z-model" {
		t.Fatalf("models not sorted: %v", models)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("auth header = %q", gotAuth)
	}
}

func TestProviderModelsEndpointBaseWithoutV1(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"raw-model"}]}`))
	}))
	defer ts.Close()
	models, err := ProviderModelsEndpoint(context.Background(), ts.URL, "")
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if len(models) != 1 || models[0].ID != "raw-model" {
		t.Fatalf("unexpected: %+v", models)
	}
}

func TestProviderModelsEndpointUnauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer ts.Close()
	if _, err := ProviderModelsEndpoint(context.Background(), ts.URL, ""); err == nil {
		t.Fatal("expected an error for 401")
	}
}

// TestPrettifyModelName covers the fallback used when an endpoint lists an id
// with no display name and the catalog has no entry for it — the opencode-go
// models whose models.dev counterparts are named.
func TestPrettifyModelName(t *testing.T) {
	cases := map[string]string{
		"deepseek-v4.1-flash": "DeepSeek V4.1 Flash",
		"glm-5.3-flash":       "GLM 5.3 Flash",
		"minimax-m2.7":        "MiniMax M2.7",
		"mimo-v2.5-pro":       "MiMo V2.5 Pro",
		"qwen3.7-max":         "Qwen3.7 Max",
		"gpt-5.6-luna":        "GPT 5.6 Luna",
		"space-bunny":         "Space Bunny",
		"kimi_k2.7_code":      "Kimi K2.7 Code",
		"":                    "",
	}
	for id, want := range cases {
		if got := PrettifyModelName(id); got != want {
			t.Errorf("PrettifyModelName(%q) = %q, want %q", id, got, want)
		}
	}
}
