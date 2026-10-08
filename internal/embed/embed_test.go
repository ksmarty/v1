package embed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigEnabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"nothing set", Config{}, false},
		{"model only", Config{Model: "x"}, false},
		{"openai without a base url", Config{Provider: ProviderOpenAI, Model: "x", APIKey: "k"}, false},
		// A local endpoint (Ollama, LM Studio) needs no key.
		{"openai with a base url and no key", Config{Provider: ProviderOpenAI, Model: "x", BaseURL: "http://localhost:11434/v1"}, true},
		{"huggingface needs a key", Config{Provider: ProviderHuggingFace, Model: "x"}, false},
		{"huggingface with a key", Config{Provider: ProviderHuggingFace, Model: "x", APIKey: "hf_x"}, true},
		{"unknown provider", Config{Provider: "other", Model: "x", APIKey: "k"}, false},
	}
	for _, c := range cases {
		if got := c.cfg.Enabled(); got != c.want {
			t.Errorf("%s: Enabled = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDims(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"nomic-embed-text-v1.5", 768},
		// An org prefix and different case must resolve to the same model.
		{"Xenova/nomic-embed-text-v1", 768},
		{"text-embedding-3-small", 1536},
		{"text-embedding-3-large", 3072},
		{"all-MiniLM-L6-v2", 384},
		{"google/embeddinggemma-2", 768},
		{"some-unknown-model", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := Dims(c.model); got != c.want {
			t.Errorf("Dims(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}

func TestTaskPrefixesOnlyForNomic(t *testing.T) {
	if !usesTaskPrefixes("Xenova/nomic-embed-text-v1") {
		t.Error("nomic models need the retrieval prefixes")
	}
	if usesTaskPrefixes("text-embedding-3-small") {
		t.Error("non-nomic models must not get prefixes")
	}
}

func TestQueryAndDocumentTextDifferForNomic(t *testing.T) {
	c := New(Config{Provider: ProviderOpenAI, Model: "nomic-embed-text-v1.5", BaseURL: "http://x"})
	if got := c.queryText("cache keys"); got != "search_query: cache keys" {
		t.Errorf("queryText = %q", got)
	}
	if got := c.documentText("cache keys"); got != "search_document: cache keys" {
		t.Errorf("documentText = %q", got)
	}
	// A model that documents no prefixes must pass text through untouched.
	plain := New(Config{Provider: ProviderOpenAI, Model: "text-embedding-3-small", BaseURL: "http://x"})
	if got := plain.queryText("cache keys"); got != "cache keys" {
		t.Errorf("queryText for a non-nomic model = %q", got)
	}
}

func TestEmbedOpenAISendsAndParses(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		// Deliberately out of order with explicit indexes, as a real provider
		// may return: the client must restore the input order.
		json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"index": 1, "embedding": []float32{0, 1}},
			map[string]any{"index": 0, "embedding": []float32{1, 0}},
		}})
	}))
	defer srv.Close()

	c := New(Config{Provider: ProviderOpenAI, Model: "text-embedding-3-small", BaseURL: srv.URL, APIKey: "sk-test"})
	vecs, err := c.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/embeddings" {
		t.Errorf("path = %q, want /embeddings", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody["model"] != "text-embedding-3-small" {
		t.Errorf("model sent = %v", gotBody["model"])
	}
	if len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("vectors were not restored to input order: %v", vecs)
	}
}

func TestEmbedOpenAISurfacesProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Incorrect API key provided"}}`)
	}))
	defer srv.Close()

	c := New(Config{Provider: ProviderOpenAI, Model: "m", BaseURL: srv.URL, APIKey: "bad"})
	_, err := c.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	// The provider's own message is the actionable part.
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Incorrect API key") {
		t.Errorf("error = %v; want the status and the provider's message", err)
	}
}

func TestEmbedHuggingFaceNestedAndFlat(t *testing.T) {
	// Nested: the normal batch shape.
	nested := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nomic-embed-text-v1.5/pipeline/feature-extraction" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer hf_x" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode([][]float32{{1, 0}, {0, 1}})
	}))
	defer nested.Close()

	c := New(Config{Provider: ProviderHuggingFace, Model: "nomic-embed-text-v1.5", BaseURL: nested.URL, APIKey: "hf_x"})
	vecs, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("nested parse = %v", vecs)
	}

	// Flat: some models return a bare vector for a single input.
	flat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]float32{0.5, 0.5})
	}))
	defer flat.Close()
	c2 := New(Config{Provider: ProviderHuggingFace, Model: "m", BaseURL: flat.URL, APIKey: "hf_x"})
	vecs, err = c2.Embed(context.Background(), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 2 {
		t.Fatalf("flat parse = %v", vecs)
	}
}

func TestEmbedWithoutConfigFails(t *testing.T) {
	c := New(Config{})
	if _, err := c.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected an error when no provider is configured")
	}
	// An empty input is not an error; there is simply nothing to embed.
	if vs, err := c.Embed(context.Background(), nil); err != nil || vs != nil {
		t.Fatalf("empty input = %v, %v", vs, err)
	}
}
