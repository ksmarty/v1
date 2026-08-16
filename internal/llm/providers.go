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

// reasoningOption is one entry of a model's reasoning_options array:
// {"type":"toggle"} means thinking is on/off, {"type":"effort","values":[…]}
// publishes the supported effort levels.
type reasoningOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

// ReasoningInfo is a model's thinking configuration per models.dev: Effort
// reports that thinking can be toggled, Levels the published effort levels
// (low/high/max etc.).
type ReasoningInfo struct {
	Effort bool     `json:"effort,omitempty"`
	Levels []string `json:"levels,omitempty"`
}

// ProviderModel is one model entry of a provider. ImageInput reports whether
// the model accepts image input (vision) according to models.dev; Reasoning
// reports thinking support.
type ProviderModel struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	ImageInput bool           `json:"imageInput,omitempty"`
	Reasoning  *ReasoningInfo `json:"reasoning,omitempty"`
	// Context is the model's context window in tokens, when published
	// (models.dev limit.context).
	Context int `json:"context,omitempty"`
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
	"anthropic":  "https://api.anthropic.com/v1",
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
		Name             string           `json:"name"`
		ToolCall         bool             `json:"tool_call"`
		Modalities       struct {
			Input []string `json:"input"`
		} `json:"modalities"`
		Reasoning        json.RawMessage  `json:"reasoning"`
		ReasoningOptions []reasoningOption `json:"reasoning_options"`
		Limit            struct {
			Context int `json:"context"`
		} `json:"limit"`
	} `json:"models"`
}

// parseReasoning interprets a models.dev `reasoning` value (a boolean, or an
// {effort, levels} object) together with `reasoning_options`. nil when the
// model does not reason.
func parseReasoning(reasoning json.RawMessage, options []reasoningOption) *ReasoningInfo {
	if len(reasoning) == 0 || string(reasoning) == "null" {
		return nil
	}
	var b bool
	reasons := false
	if err := json.Unmarshal(reasoning, &b); err == nil {
		reasons = b
	} else {
		var r ReasoningInfo
		if err := json.Unmarshal(reasoning, &r); err == nil {
			reasons = r.Effort || len(r.Levels) > 0
		}
	}
	if !reasons {
		return nil
	}
	info := &ReasoningInfo{Effort: true}
	for _, o := range options {
		if o.Type != "effort" || len(o.Values) == 0 {
			continue
		}
		info.Levels = o.Values
		break
	}
	return info
}

// ThinkingOptions is a model's thinking configuration resolved from the
// provider's own /models endpoint. Off reports that thinking can be disabled.
type ThinkingOptions struct {
	Levels []string `json:"levels,omitempty"`
	Off    bool     `json:"off,omitempty"`
}

// thinkingCache caches resolved options per provider+model for a short TTL.
type thinkingEntry struct {
	opts      *ThinkingOptions
	fetchedAt time.Time
}

var thinkingCache struct {
	mu      sync.Mutex
	entries map[string]thinkingEntry
}

const thinkingCacheTTL = 10 * time.Minute

// ProviderThinking resolves a model's thinking options from the provider's
// /models endpoint (the standard OpenAI-compatible model list). The metadata
// shape varies by provider — reasoning_options (models.dev-style),
// reasoning {effort,levels} or {mandatory} (OpenRouter) — or is absent
// entirely (opencode, OpenAI, Anthropic publish none). When the endpoint
// publishes nothing, the model family determines the levels (the same
// convention Kimi Code applies to Claude), and unknown models get nil.
func ProviderThinking(ctx context.Context, baseURL, apiKey, model string) *ThinkingOptions {
	key := baseURL + "|" + model
	thinkingCache.mu.Lock()
	if e, ok := thinkingCache.entries[key]; ok && time.Since(e.fetchedAt) < thinkingCacheTTL {
		opts := e.opts
		thinkingCache.mu.Unlock()
		return opts
	}
	thinkingCache.mu.Unlock()

	opts := fetchThinking(ctx, baseURL, apiKey, model)

	thinkingCache.mu.Lock()
	if thinkingCache.entries == nil {
		thinkingCache.entries = map[string]thinkingEntry{}
	}
	thinkingCache.entries[key] = thinkingEntry{opts: opts, fetchedAt: time.Now()}
	thinkingCache.mu.Unlock()
	return opts
}

// modelContextCache caches per-model context windows from the provider's
// /models endpoint, keyed by baseURL|model.
var modelContextCache = struct {
	sync.Mutex
	entries map[string]modelContextEntry
}{entries: map[string]modelContextEntry{}}

type modelContextEntry struct {
	context   int
	fetchedAt time.Time
}

// ModelContextLength returns the model's context window in tokens from the
// provider's /models endpoint, or 0 when the provider publishes none.
func ModelContextLength(ctx context.Context, baseURL, apiKey, model string) int {
	key := baseURL + "|" + model
	modelContextCache.Lock()
	if e, ok := modelContextCache.entries[key]; ok && time.Since(e.fetchedAt) < thinkingCacheTTL {
		n := e.context
		modelContextCache.Unlock()
		return n
	}
	modelContextCache.Unlock()

	n := fetchModelContext(ctx, baseURL, apiKey, model)

	modelContextCache.Lock()
	if modelContextCache.entries == nil {
		modelContextCache.entries = map[string]modelContextEntry{}
	}
	modelContextCache.entries[key] = modelContextEntry{context: n, fetchedAt: time.Now()}
	modelContextCache.Unlock()
	return n
}

// fetchModelContext reads the model's context window from the provider's
// /models response. OpenAI-compatible providers vary in the field name —
// context_length, max_context_length and context_window are all common.
func fetchModelContext(ctx context.Context, baseURL, apiKey, model string) int {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/models", nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return 0
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID               string `json:"id"`
			ContextLength    int    `json:"context_length"`
			MaxContextLength int    `json:"max_context_length"`
			ContextWindow    int    `json:"context_window"`
			Context          int    `json:"context"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out) != nil {
		return 0
	}
	for _, m := range out.Data {
		if m.ID != model {
			continue
		}
		switch {
		case m.ContextLength > 0:
			return m.ContextLength
		case m.MaxContextLength > 0:
			return m.MaxContextLength
		case m.ContextWindow > 0:
			return m.ContextWindow
		case m.Context > 0:
			return m.Context
		}
	}
	return 0
}

// thinkingModelEntry mirrors one entry of a /models response.
type thinkingModelEntry struct {
	ID               string `json:"id"`
	Reasoning        json.RawMessage `json:"reasoning"`
	ReasoningOptions []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
	} `json:"reasoning_options"`
}

func fetchThinking(ctx context.Context, baseURL, apiKey, model string) *ThinkingOptions {
	var list struct {
		Data []thinkingModelEntry `json:"data"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return nil
	}
	for i := range list.Data {
		if list.Data[i].ID != model && !strings.HasSuffix(list.Data[i].ID, "/"+model) {
			continue
		}
		if opts := parseEndpointThinking(&list.Data[i]); opts != nil {
			return opts
		}
	}
	// The provider's endpoint was silent — OpenRouter's public model list
	// publishes reasoning metadata for the models it serves (supported_efforts
	// etc.), which describes the model itself, not the router.
	if opts := openRouterThinking(ctx, model); opts != nil {
		return opts
	}
	return familyThinking(model)
}

// openRouterCache caches the public OpenRouter model list briefly.
var openRouterCache struct {
	mu        sync.Mutex
	data      map[string]*ThinkingOptions
	fetchedAt time.Time
}

// openRouterThinking looks up a model in OpenRouter's public /api/v1/models
// and returns its thinking options, or nil when absent.
func openRouterThinking(ctx context.Context, model string) *ThinkingOptions {
	openRouterCache.mu.Lock()
	if openRouterCache.data != nil && time.Since(openRouterCache.fetchedAt) < thinkingCacheTTL {
		opts := openRouterCache.data[model]
		openRouterCache.mu.Unlock()
		return opts
	}
	openRouterCache.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var list struct {
		Data []thinkingModelEntry `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&list); err != nil {
		return nil
	}
	byModel := map[string]*ThinkingOptions{}
	for i := range list.Data {
		entry := &list.Data[i]
		if opts := parseEndpointThinking(entry); opts != nil {
			byModel[entry.ID] = opts
			// Also index by the bare model name (anthropic/claude-fable-5
			// → claude-fable-5) so provider-local ids resolve.
			if j := strings.LastIndex(entry.ID, "/"); j >= 0 && j+1 < len(entry.ID) {
				byModel[entry.ID[j+1:]] = opts
			}
		}
	}

	openRouterCache.mu.Lock()
	openRouterCache.data = byModel
	openRouterCache.fetchedAt = time.Now()
	opts := byModel[model]
	openRouterCache.mu.Unlock()
	return opts
}

// parseEndpointThinking extracts thinking options from one /models entry,
// handling the shapes providers actually publish: reasoning_options
// (models.dev-style), reasoning {effort,levels}, and OpenRouter's
// {mandatory, supported_efforts, default_effort}. nil when the entry carries
// no thinking metadata.
func parseEndpointThinking(e *thinkingModelEntry) *ThinkingOptions {
	opts := &ThinkingOptions{}
	found := false
	if len(e.Reasoning) > 0 && string(e.Reasoning) != "null" {
		var b bool
		if err := json.Unmarshal(e.Reasoning, &b); err == nil {
			found = b
		} else {
			var r struct {
				Effort           *bool    `json:"effort"`
				Levels           []string `json:"levels"`
				Mandatory        *bool    `json:"mandatory"`
				SupportedEfforts []string `json:"supported_efforts"`
				DefaultEffort    string   `json:"default_effort"`
			}
			if err := json.Unmarshal(e.Reasoning, &r); err == nil {
				found = (r.Effort != nil && *r.Effort) || len(r.Levels) > 0 ||
					r.Mandatory != nil || len(r.SupportedEfforts) > 0
				opts.Levels = r.Levels
				if len(r.SupportedEfforts) > 0 {
					// OpenRouter lists them high→low; present low→high.
					opts.Levels = append([]string(nil), r.SupportedEfforts...)
					for i, j := 0, len(opts.Levels)-1; i < j; i, j = i+1, j-1 {
						opts.Levels[i], opts.Levels[j] = opts.Levels[j], opts.Levels[i]
					}
				}
				if r.Mandatory != nil && !*r.Mandatory {
					opts.Off = true
				}
			}
		}
	}
	for _, o := range e.ReasoningOptions {
		switch o.Type {
		case "toggle", "budget_tokens":
			opts.Off = true
			found = true
		case "effort":
			if len(o.Values) > 0 {
				opts.Levels = o.Values
			}
			found = true
		}
	}
	if !found {
		return nil
	}
	return opts
}

// familyThinking is the fallback when a provider's /models endpoint publishes
// no reasoning metadata: known reasoning families map to concrete levels
// (the same convention Kimi Code applies to Claude). Unknown models get nil.
func familyThinking(model string) *ThinkingOptions {
	id := strings.ToLower(model)
	switch {
	case strings.HasPrefix(id, "claude-"):
		return &ThinkingOptions{Levels: []string{"low", "medium", "high"}, Off: true}
	case strings.HasPrefix(id, "deepseek-reasoner"), strings.Contains(id, "deepseek-v4"):
		return &ThinkingOptions{Levels: []string{"low", "high", "max"}, Off: true}
	case strings.HasPrefix(id, "o3"), strings.HasPrefix(id, "o4"), strings.HasPrefix(id, "gpt-5"):
		return &ThinkingOptions{Levels: []string{"low", "medium", "high"}, Off: true}
	}
	return nil
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
		models = append(models, ProviderModel{
			ID:         id,
			Name:       name,
			ImageInput: image,
			Reasoning:  parseReasoning(m.Reasoning, m.ReasoningOptions),
			Context:    m.Limit.Context,
		})
	}
	return models
}

// CatalogHasReasoning reports whether any model in the catalog carries
// reasoning metadata — used to detect stale caches built before the field
// existed.
func CatalogHasReasoning(c *Catalog) bool {
	for _, p := range c.Providers {
		for _, m := range p.Models {
			if m.Reasoning != nil {
				return true
			}
		}
	}
	return false
}

// CatalogHasContext reports whether any model carries a context window —
// stale caches built before the field existed fail this.
func CatalogHasContext(c *Catalog) bool {
	for _, p := range c.Providers {
		for _, m := range p.Models {
			if m.Context > 0 {
				return true
			}
		}
	}
	return false
}

// CatalogHasReasoningLevels reports whether any model publishes effort
// levels — used to detect caches built before reasoning_options was parsed.
func CatalogHasReasoningLevels(c *Catalog) bool {
	for _, p := range c.Providers {
		for _, m := range p.Models {
			if m.Reasoning != nil && len(m.Reasoning.Levels) > 0 {
				return true
			}
		}
	}
	return false
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

// RefreshCatalog fetches the current models.dev data and rebuilds the
// catalog: the embedded snapshot pins a curated core (ids and baseURLs stay
// as pinned there), and every other OpenAI-compatible provider on models.dev
// joins dynamically — so new providers appear after a refresh without a code
// change. Entries without a usable endpoint are skipped.
func RefreshCatalog(ctx context.Context) (*Catalog, error) {
	snapshot, err := EmbeddedCatalog()
	if err != nil {
		return nil, fmt.Errorf("reading embedded catalog: %w", err)
	}
	fresh, err := LiveModelsDev(ctx)
	if err != nil {
		return nil, err
	}

	out := &Catalog{Source: snapshot.Source, Providers: make([]Provider, 0, len(fresh))}
	seen := make(map[string]bool, len(snapshot.Providers))
	for _, sp := range snapshot.Providers {
		p := sp // keep id and baseURL from the snapshot
		if fp, ok := fresh[sp.ID]; ok {
			applyFresh(&p, fp)
		}
		seen[sp.ID] = true
		out.Providers = append(out.Providers, p)
	}
	for fid, fp := range fresh {
		if seen[fid] {
			continue
		}
		base := fp.Api
		if base == "" {
			base = knownBaseURLs[fid]
		}
		if base == "" {
			continue // no known OpenAI-compatible endpoint
		}
		p := Provider{ID: fid, BaseURL: strings.TrimSuffix(base, "/")}
		applyFresh(&p, fp)
		out.Providers = append(out.Providers, p)
	}
	sort.Slice(out.Providers, func(i, j int) bool {
		return out.Providers[i].Name < out.Providers[j].Name
	})
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

// providerRank gives well-known majors a popularity head start in search
// results. models.dev publishes no popularity signal, so this small curated
// ranking (order only — availability is unaffected) pushes the majors to the
// top; the model-count proxy and then name follow.
var providerRank = map[string]int{
	"openai": 0, "anthropic": 1, "google": 2, "deepseek": 3, "openrouter": 4,
	"mistral": 5, "meta": 6, "xai": 7, "groq": 8, "cohere": 9, "zhipuai": 10,
	"perplexity": 11,
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
		rankOf := func(id string) int {
			if r, ok := providerRank[id]; ok {
				return r
			}
			return 1000 // unranked providers sort after the majors
		}
		ri, mi, ni := rankOf(out[i].ID), len(out[i].Models), out[i].Name
		rj, mj, nj := rankOf(out[j].ID), len(out[j].Models), out[j].Name
		if ri != rj {
			return ri < rj
		}
		if mi != mj {
			return mi > mj
		}
		return ni < nj
	})
	return out, nil
}
