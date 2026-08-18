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
