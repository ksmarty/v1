package llm

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed providers.json
var embeddedCatalog []byte

// ProviderModel is one model entry of a provider. ImageInput reports whether
// the model accepts image input (vision) according to models.dev.
type ProviderModel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ImageInput bool   `json:"imageInput,omitempty"`
}

// Provider is one LLM provider in the catalog.
type Provider struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	BaseURL string          `json:"baseURL"`
	KeyHint *string         `json:"keyHint"`
	Doc     *string         `json:"doc"`
	Models  []ProviderModel `json:"models"`
	Added   bool            `json:"added,omitempty"`
}

// Catalog is the provider catalog served by GET /api/providers.
type Catalog struct {
	Source    string     `json:"source"`
	Providers []Provider `json:"providers"`
}

// customProvider is appended to every served catalog; it is not part of the
// snapshot and is never stored in the cache.
var customProvider = Provider{
	ID:      "custom",
	Name:    "Custom endpoint",
	BaseURL: "",
	KeyHint: nil,
	Doc:     nil,
	Models:  []ProviderModel{},
}

// ParseCatalog parses catalog JSON of the snapshot shape.
func ParseCatalog(data []byte) (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// EmbeddedCatalog returns the catalog snapshot compiled into the binary.
func EmbeddedCatalog() (*Catalog, error) {
	return ParseCatalog(embeddedCatalog)
}

// WithCustom returns a copy of the catalog with the custom entry appended.
func (c *Catalog) WithCustom() *Catalog {
	return c.WithAdded(nil)
}

// WithAdded returns a copy of the catalog with runtime-added providers
// inserted before the trailing custom entry, marked with Added=true.
func (c *Catalog) WithAdded(added []Provider) *Catalog {
	out := &Catalog{Source: c.Source, Providers: make([]Provider, 0, len(c.Providers)+len(added)+1)}
	out.Providers = append(out.Providers, c.Providers...)
	for _, p := range added {
		p.Added = true
		out.Providers = append(out.Providers, p)
	}
	out.Providers = append(out.Providers, customProvider)
	return out
}

const modelsDevURL = "https://models.dev/api.json"
const maxModelsPerProvider = 40

// ModelsForBaseURL returns the catalog models for the provider whose base URL
// matches baseURL (case-insensitive, ignoring a trailing slash), looking up
// knownBaseURLs first and falling back to the embedded catalog provider list.
// It returns nil when no known provider matches so callers can render [].
func ModelsForBaseURL(baseURL string) []ProviderModel {
	if baseURL == "" {
		return nil
	}
	catalog, err := EmbeddedCatalog()
	if err != nil {
		return nil
	}
	want := canonicalBaseURL(baseURL)
	for id := range knownBaseURLs {
		if canonicalBaseURL(knownBaseURLs[id]) == want {
			return catalogModelsByID(catalog, id)
		}
	}
	for _, p := range catalog.Providers {
		if canonicalBaseURL(p.BaseURL) == want {
			return p.Models
		}
	}
	return nil
}

// catalogModelsByID returns the models of the provider with the given id.
func catalogModelsByID(catalog *Catalog, id string) []ProviderModel {
	for _, p := range catalog.Providers {
		if p.ID == id {
			return p.Models
		}
	}
	return nil
}

// canonicalBaseURL normalizes a base URL for comparison.
func canonicalBaseURL(u string) string {
	return strings.ToLower(strings.TrimSuffix(u, "/"))
}

// knownBaseURLs pins OpenAI-compatible base URLs for providers that models.dev
// does not publish an `api` field for (it only lists one for a subset of
// providers). Used by SearchProviders so major providers like OpenAI, Groq,
// xAI, Mistral, Together, Cerebras and Gemini still appear in the "browse all
// models.dev providers" UI with a usable base URL.
var knownBaseURLs = map[string]string{
	"openai":     "https://api.openai.com/v1",
	"opencode":   "https://opencode.ai/zen/v1",
	"google":     "https://generativelanguage.googleapis.com/v1beta/openai/",
	"groq":       "https://api.groq.com/openai/v1",
	"mistral":    "https://api.mistral.ai/v1",
	"xai":        "https://api.x.ai/v1",
	"moonshotai": "https://api.moonshot.ai/v1",
	"zhipuai":    "https://open.bigmodel.cn/api/paas/v4",
	"togetherai": "https://api.together.xyz/v1",
	"cerebras":   "https://api.cerebras.ai/v1",
}

// liveCacheTTL is how long the in-memory models.dev cache stays fresh. It is
// shared by RefreshCatalog and SearchProviders so both hit the network at
// most once per window.
const liveCacheTTL = 15 * time.Minute

// modelsDevDoc mirrors the relevant part of the models.dev API response. The
// Api field is the OpenAI-compatible base URL (when the provider is one);
// entries without it are not OpenAI-compatible.
type modelsDevDoc struct {
	Name   string   `json:"name"`
	Api    string   `json:"api"`
	Env    []string `json:"env"`
	Doc    string   `json:"doc"`
	Models map[string]struct {
		Name       string `json:"name"`
		ToolCall   bool   `json:"tool_call"`
		Modalities struct {
			Input []string `json:"input"`
		} `json:"modalities"`
	} `json:"models"`
}

// liveCache holds the last successful models.dev fetch.
var liveCache struct {
	mu        sync.Mutex
	data      map[string]modelsDevDoc
	fetchedAt time.Time
}

// LiveModelsDev returns the models.dev catalog, fetching it when the in-memory
// cache is empty or older than liveCacheTTL. A failed fetch is returned as an
// error; stale data is never served in its place.
func LiveModelsDev(ctx context.Context) (map[string]modelsDevDoc, error) {
	liveCache.mu.Lock()
	if liveCache.data != nil && time.Since(liveCache.fetchedAt) < liveCacheTTL {
		data := liveCache.data
		liveCache.mu.Unlock()
		return data, nil
	}
	liveCache.mu.Unlock()

	fresh, err := fetchModelsDev(ctx)
	if err != nil {
		return nil, err
	}
	liveCache.mu.Lock()
	liveCache.data = fresh
	liveCache.fetchedAt = time.Now()
	liveCache.mu.Unlock()
	return fresh, nil
}

// fetchModelsDev downloads the full models.dev catalog.
func fetchModelsDev(ctx context.Context) (map[string]modelsDevDoc, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "v1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var fresh map[string]modelsDevDoc
	if err := json.Unmarshal(body, &fresh); err != nil {
		return nil, fmt.Errorf("parsing models.dev response: %w", err)
	}
	return fresh, nil
}

// modelSubset builds the curated, sorted model list for a models.dev entry:
// only tool-calling models, capped for size.
func modelSubset(fp modelsDevDoc) []ProviderModel {
	ids := make([]string, 0, len(fp.Models))
	for id := range fp.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	models := make([]ProviderModel, 0, maxModelsPerProvider)
	for _, id := range ids {
		if len(models) >= maxModelsPerProvider {
			break
		}
		m := fp.Models[id]
		if strings.HasPrefix(id, "~") || !m.ToolCall {
			continue
		}
		name := m.Name
		if name == "" {
			name = id
		}
		image := false
		for _, in := range m.Modalities.Input {
			if in == "image" {
				image = true
				break
			}
		}
		models = append(models, ProviderModel{ID: id, Name: name, ImageInput: image})
	}
	return models
}

// CatalogHasVision reports whether any model in the catalog carries image-input
// metadata — used to detect stale caches built before the field existed.
func CatalogHasVision(c *Catalog) bool {
	for _, p := range c.Providers {
		for _, m := range p.Models {
			if m.ImageInput {
				return true
			}
		}
	}
	return false
}

// applyFresh overlays name/keyHint/doc/models from a models.dev entry onto a
// provider whose id and baseURL come from the pinned snapshot.
func applyFresh(p *Provider, fp modelsDevDoc) {
	if fp.Name != "" {
		p.Name = fp.Name
	}
	if len(fp.Env) > 0 && fp.Env[0] != "" {
		hint := fp.Env[0]
		p.KeyHint = &hint
	} else {
		p.KeyHint = nil
	}
	if fp.Doc != "" {
		doc := fp.Doc
		p.Doc = &doc
	} else {
		p.Doc = nil
	}
	p.Models = modelSubset(fp)
}

// RefreshCatalog fetches the current models.dev data and rebuilds the curated
// catalog: provider ids and baseURLs stay exactly as in the embedded snapshot
// (the source of truth for base URLs); name/keyHint/doc/models are updated
// from the fresh data.
func RefreshCatalog(ctx context.Context) (*Catalog, error) {
	snapshot, err := EmbeddedCatalog()
	if err != nil {
		return nil, fmt.Errorf("reading embedded catalog: %w", err)
	}
	fresh, err := LiveModelsDev(ctx)
	if err != nil {
		return nil, err
	}

	out := &Catalog{Source: snapshot.Source, Providers: make([]Provider, 0, len(snapshot.Providers))}
	for _, sp := range snapshot.Providers {
		p := sp // keep id and baseURL from the snapshot
		if fp, ok := fresh[sp.ID]; ok {
			applyFresh(&p, fp)
		}
		out.Providers = append(out.Providers, p)
	}
	return out, nil
}

// FindProvider fetches models.dev and returns the provider whose id matches
// (case-insensitive), or nil when absent. BaseURL is left empty (models.dev
// does not publish base URLs).
func FindProvider(ctx context.Context, id string) (*Provider, error) {
	fresh, err := LiveModelsDev(ctx)
	if err != nil {
		return nil, err
	}
	for fid, fp := range fresh {
		if !strings.EqualFold(fid, id) {
			continue
		}
		p := Provider{ID: fid, BaseURL: ""}
		applyFresh(&p, fp)
		return &p, nil
	}
	return nil, nil
}

// SearchProviders looks up OpenAI-compatible providers on models.dev whose id
// or name contains query (case-insensitive; an empty query returns every
// compatible provider). A provider counts as OpenAI-compatible when models.dev
// publishes an `api` base URL for it, or when its id is in knownBaseURLs. The
// base URL comes from the models.dev `api` field, falling back to knownBaseURLs
// when that field is absent. Tool-calling models are capped per provider.
func SearchProviders(ctx context.Context, query string) ([]Provider, error) {
	fresh, err := LiveModelsDev(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]Provider, 0)
	for id, fp := range fresh {
		base := fp.Api
		if base == "" {
			base = knownBaseURLs[id]
		}
		if base == "" {
			continue // no known OpenAI-compatible endpoint
		}
		if q != "" && !strings.Contains(strings.ToLower(id), q) && !strings.Contains(strings.ToLower(fp.Name), q) {
			continue
		}
		p := Provider{ID: id, BaseURL: base}
		applyFresh(&p, fp)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}
