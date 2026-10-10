// Package embed turns text into vectors using a configured provider.
//
// Three providers are supported:
//
//   - native: a BERT-family encoder run inside v1, with the weights fetched
//     from Hugging Face on first use and cached on disk. This is the only
//     provider that needs no external service, and the only one that works
//     offline once its model is cached.
//   - openai: any OpenAI-compatible /embeddings endpoint.
//   - huggingface: the HF inference feature-extraction API.
//
// The native encoder is pure Go. v1 builds with CGO_ENABLED=0 (Dockerfile), so
// an ONNX runtime is not an option — see bert.go for the forward pass and
// native.go for the supported model set.
//
// Nothing here is required for memory to work. When no provider is configured
// the caller ranks lexically, which is what v1 did before embeddings existed.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Provider identifiers.
const (
	ProviderNative      = "native"      // in-process BERT encoder (see native.go)
	ProviderOpenAI      = "openai"      // any OpenAI-compatible /embeddings
	ProviderHuggingFace = "huggingface" // HF inference feature-extraction
)

// DefaultHuggingFaceURL is the HF inference router. The legacy
// api-inference.huggingface.co host still resolves for some models, so BaseURL
// is overridable rather than hardcoded.
const DefaultHuggingFaceURL = "https://router.huggingface.co/hf-inference/models"

// Config describes where embeddings come from.
type Config struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"baseUrl"`
	APIKey   string `json:"apiKey"`
}

// Enabled reports whether the config is complete enough to attempt an embed.
// A missing key is not fatal for a local endpoint (Ollama, LM Studio, llama.cpp
// all serve /embeddings without one), so the key is only required for the hosted
// providers.
func (c Config) Enabled() bool {
	if strings.TrimSpace(c.Model) == "" {
		return false
	}
	switch c.Provider {
	case ProviderNative:
		// The model is optional: DefaultNativeModel is used when none is named,
		// and the weights are fetched on first use.
		return true
	case ProviderOpenAI:
		// A base URL is the minimum: this provider is defined by its endpoint.
		return strings.TrimSpace(c.BaseURL) != ""
	case ProviderHuggingFace:
		return strings.TrimSpace(c.APIKey) != ""
	default:
		return false
	}
}

// dims maps known model ids to their vector width. It is used to size the
// storage column and to catch a model swap that would silently mix vector
// spaces — comparing a 768-wide memory against a 1536-wide query is meaningless,
// and Cosine returns 0 for a length mismatch rather than a wrong number.
var dims = map[string]int{
	"nomic-embed-text-v1":         768,
	"nomic-embed-text-v1.5":       768,
	"nomic-embed-text-v2-moe":     768,
	"embeddinggemma-300m":         768,
	"embeddinggemma-2":            768,
	"embeddinggemma-2-300m":       768,
	"all-minilm-l6-v2":            384,
	"all-minilm-l12-v2":           384,
	"all-mpnet-base-v2":           768,
	"bge-small-en-v1.5":           384,
	"bge-base-en-v1.5":            768,
	"bge-large-en-v1.5":           1024,
	"gte-small":                   384,
	"gte-base":                    768,
	"jina-embeddings-v2-base-en":  768,
	"jina-embeddings-v2-small-en": 512,
	"text-embedding-ada-002":      1536,
	"text-embedding-3-small":      1536,
	"text-embedding-3-large":      3072,
	"voyage-3-lite":               512,
	"voyage-3":                    1024,
}

// Dims returns the known width of a model id, or 0 when unknown.
//
// Matching ignores any org prefix and case, so `Xenova/nomic-embed-text-v1` and
// `nomic-embed-text-v1` agree.
func Dims(model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if d, ok := dims[m]; ok {
		return d
	}
	// Fall back to a suffix match so dated revisions still resolve.
	for id, d := range dims {
		if strings.HasPrefix(m, id) {
			return d
		}
	}
	return 0
}

// DimsFor returns the width the configured provider produces.
//
// For native it reads the width out of the cached model, because an arbitrary
// Hugging Face repository is not in the table above. Until that model has been
// downloaded the answer is 0, which the settings page shows as unknown rather
// than guessing a width that would then be wrong.
func DimsFor(provider, model string) int {
	if provider != ProviderNative {
		return Dims(model)
	}
	repo, dir, err := parseModelRef(model)
	if err != nil {
		return 0
	}
	if dir == "" {
		if dir, err = modelCacheDir(repo); err != nil {
			return 0
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return 0
	}
	var cfg bertConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return 0
	}
	return cfg.HiddenSize
}

// Downloaded reports whether a native model's files are already on disk. The
// settings page uses it to say "downloaded" and offer a test instead of a fresh
// download, so the answer has to reflect the files the encoder actually needs
// (config, a tokenizer, and the weights), not merely that the directory exists.
func Downloaded(provider, model string) bool {
	if provider != ProviderNative {
		return false
	}
	repo, dir, err := parseModelRef(model)
	if err != nil {
		return false
	}
	if dir == "" {
		if dir, err = modelCacheDir(repo); err != nil {
			return false
		}
	}
	if !fileExists(filepath.Join(dir, "config.json")) {
		return false
	}
	if !fileExists(filepath.Join(dir, "tokenizer.json")) && !fileExists(filepath.Join(dir, "vocab.txt")) {
		return false
	}
	return fileExists(filepath.Join(dir, "model.safetensors")) ||
		fileExists(filepath.Join(dir, "model.safetensors.index.json"))
}

// usesTaskPrefixes reports whether a model expects the retrieval instruction
// prefixes from its model card. Nomic's embedding models are trained with them
// and retrieve noticeably worse without: the prefix tells the model whether the
// text is a document to be stored or a query to be matched against them.
//
// opencode-mem defaults this off and lets the user opt in; here it is on for the
// models that document it, because the failure mode is silent — retrieval just
// gets quietly worse.
func usesTaskPrefixes(model string) bool {
	return strings.Contains(strings.ToLower(model), "nomic")
}

// Client embeds text. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a client for the config. It does not validate the provider; call
// Config.Enabled first (the caller falls back to lexical ranking when false).
func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

// Embed returns one vector per input, in the same order. An empty input returns
// no vectors and no error.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if !c.cfg.Enabled() {
		return nil, fmt.Errorf("no embedding provider is configured")
	}
	switch c.cfg.Provider {
	case ProviderNative:
		return nativeEmbed(ctx, c.cfg, texts)
	case ProviderOpenAI:
		return c.embedOpenAI(ctx, texts)
	case ProviderHuggingFace:
		return c.embedHuggingFace(ctx, texts)
	default:
		return nil, fmt.Errorf("unknown embedding provider %q", c.cfg.Provider)
	}
}

// EmbedOne is the single-text convenience used on the write path.
func (c *Client) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	vs, err := c.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, fmt.Errorf("embedding provider returned no vector")
	}
	return vs[0], nil
}

// EmbedQuery embeds a search query, applying the model's query prefix.
func (c *Client) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return c.EmbedOne(ctx, c.queryText(text))
}

// EmbedDocument embeds text destined for storage, applying the document prefix.
func (c *Client) EmbedDocument(ctx context.Context, text string) ([]float32, error) {
	return c.EmbedOne(ctx, c.documentText(text))
}

func (c *Client) queryText(s string) string {
	if usesTaskPrefixes(c.cfg.Model) {
		return "search_query: " + s
	}
	return s
}

func (c *Client) documentText(s string) string {
	if usesTaskPrefixes(c.cfg.Model) {
		return "search_document: " + s
	}
	return s
}

// embedOpenAI speaks the OpenAI /embeddings shape, which Ollama, LM Studio,
// llama.cpp, Voyage, Jina and most hosted providers also implement.
func (c *Client) embedOpenAI(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": c.cfg.Model, "input": texts})
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	raw, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("embedding response is not JSON: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embedding provider returned no vectors")
	}
	// The API does not promise ordering when it returns an index, so honour it.
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		idx := d.Index
		if idx < 0 || idx >= len(vecs) {
			idx = i
		}
		vecs[idx] = d.Embedding
	}
	return vecs, nil
}

// embedHuggingFace uses the inference feature-extraction pipeline. The response
// is a nested array for a batch, and for a single input some models return a
// flat vector instead, so both shapes are accepted.
func (c *Client) embedHuggingFace(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{
		"inputs":  texts,
		"options": map[string]any{"wait_for_model": true},
	})
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if base == "" {
		base = DefaultHuggingFaceURL
	}
	endpoint := base + "/" + c.cfg.Model + "/pipeline/feature-extraction"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	raw, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var nested [][]float32
	if err := json.Unmarshal(raw, &nested); err == nil && len(nested) > 0 {
		return nested, nil
	}
	var flat []float32
	if err := json.Unmarshal(raw, &flat); err == nil && len(flat) > 0 {
		return [][]float32{flat}, nil
	}
	return nil, fmt.Errorf("embedding response is not a vector: %s", snippet(raw))
}

// do performs the request and turns a non-2xx into an error that names the
// provider's own message, which is the difference between "401" and "your key
// does not have access to this model".
func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("reading embedding response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding provider returned %d: %s", resp.StatusCode, snippet(raw))
	}
	return raw, nil
}

// snippet trims a response body for an error message. HF returns a JSON error
// object; OpenAI too. Showing the raw prefix is more useful than guessing at
// their schemas.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
