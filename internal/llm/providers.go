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
	"time"
)

//go:embed providers.json
var embeddedCatalog []byte

// ProviderModel is one model entry of a provider.
type ProviderModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Provider is one LLM provider in the catalog.
type Provider struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	BaseURL string          `json:"baseURL"`
	KeyHint *string         `json:"keyHint"`
	Doc     *string         `json:"doc"`
	Models  []ProviderModel `json:"models"`
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
	out := &Catalog{Source: c.Source, Providers: make([]Provider, 0, len(c.Providers)+1)}
	out.Providers = append(out.Providers, c.Providers...)
	out.Providers = append(out.Providers, customProvider)
	return out
}

const modelsDevURL = "https://models.dev/api.json"
const maxModelsPerProvider = 40

// modelsDevDoc mirrors the relevant part of the models.dev API response.
type modelsDevDoc struct {
	Name   string   `json:"name"`
	Env    []string `json:"env"`
	Doc    string   `json:"doc"`
	Models map[string]struct {
		Name     string `json:"name"`
		ToolCall bool   `json:"tool_call"`
	} `json:"models"`
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

	out := &Catalog{Source: snapshot.Source, Providers: make([]Provider, 0, len(snapshot.Providers))}
	for _, sp := range snapshot.Providers {
		p := sp // keep id and baseURL from the snapshot
		fp, ok := fresh[sp.ID]
		if !ok {
			out.Providers = append(out.Providers, p)
			continue
		}
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
			models = append(models, ProviderModel{ID: id, Name: name})
		}
		p.Models = models
		out.Providers = append(out.Providers, p)
	}
	return out, nil
}
