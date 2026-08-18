package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"v1/internal/llm"
)

// routerModelsCache holds a short-lived copy of OpenRouter's live model list.
var routerModelsCache struct {
	sync.Mutex
	at   time.Time
	list []llm.ProviderModel
}

// routerModels returns the cached OpenRouter model directory, refetching it
// at most every 15 minutes. It reports ok=false when unavailable.
func (s *Server) routerModels() ([]llm.ProviderModel, bool) {
	routerModelsCache.Lock()
	defer routerModelsCache.Unlock()
	if routerModelsCache.list != nil && time.Since(routerModelsCache.at) < 15*time.Minute {
		return routerModelsCache.list, true
	}
	data := llm.RouterLiveModels(context.Background())
	if data != nil && len(data) > 0 {
		routerModelsCache.list = data
		routerModelsCache.at = time.Now()
		return data, true
	}
	return nil, false
}

// handleListProviders serves the provider catalog: the settings-cached copy
// when present, otherwise the embedded snapshot; runtime-added providers and
// the custom entry are appended. Caches built before image-input metadata
// existed are refreshed lazily so the model lists carry vision flags.
func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	cat := s.providerCatalog()
	if cat == nil || !llm.CatalogHasVision(cat) || !llm.CatalogHasReasoning(cat) || !llm.CatalogHasReasoningLevels(cat) || !llm.CatalogHasContext(cat) {
		if fresh, err := llm.RefreshCatalog(r.Context()); err == nil {
			cat = fresh
			if data, err := json.Marshal(fresh); err == nil {
				_ = s.st.SetSetting(keyProvidersCache, string(data))
			}
		}
	}
	if cat == nil {
		writeError(w, http.StatusInternalServerError, "provider catalog unavailable")
		return
	}
	cat = s.CatalogWithRouter(cat).WithAdded(s.customProviders())
	writeJSON(w, http.StatusOK, s.catalogWithCustomModels(cat, r))
}

// customModelsCache holds short-lived /v1/models listings keyed by base URL.
var customModelsCache struct {
	sync.Mutex
	at     time.Time
	byBase map[string][]llm.ProviderModel
}

// customProviderModels fetches (and briefly caches) the model list for a
// custom base URL via its OpenAI-compatible /models endpoint. Reports nil when
// unreachable or unparsable (callers keep whatever the catalog already had).
func (s *Server) customProviderModels(ctx context.Context, baseURL, apiKey string) []llm.ProviderModel {
	key := strings.ToLower(strings.TrimRight(baseURL, "/"))
	customModelsCache.Lock()
	cached, ok := customModelsCache.byBase[key]
	age := customModelsCache.at
	customModelsCache.Unlock()
	if ok && time.Since(age) < 10*time.Minute {
		return cached
	}
	ms, err := llm.ProviderModelsEndpoint(ctx, baseURL, apiKey)
	if err != nil {
		return nil
	}
	customModelsCache.Lock()
	if customModelsCache.byBase == nil {
		customModelsCache.byBase = map[string][]llm.ProviderModel{}
	}
	customModelsCache.byBase[key] = ms
	customModelsCache.at = time.Now()
	customModelsCache.Unlock()
	return ms
}

// catalogWithCustomModels fills in live model listings for provider entries
// that have a base URL but no catalog models (i.e. user-defined providers),
// authenticated with the matching saved provider's API key when one exists.
func (s *Server) catalogWithCustomModels(cat *llm.Catalog, r *http.Request) *llm.Catalog {
	if cat == nil {
		return cat
	}
	keys := map[string]string{}
	for _, lp := range s.llmProviders(s.currentUser(r).ID) {
		if lp.BaseURL != "" {
			keys[strings.ToLower(strings.TrimRight(lp.BaseURL, "/"))] = lp.APIKey
		}
	}
	out := *cat
	out.Providers = append([]llm.Provider(nil), cat.Providers...)
	for i := range out.Providers {
		p := &out.Providers[i]
		if p.BaseURL == "" || len(p.Models) > 0 {
			continue
		}
		key := strings.ToLower(strings.TrimRight(p.BaseURL, "/"))
		if ms := s.customProviderModels(r.Context(), p.BaseURL, keys[key]); len(ms) > 0 {
			p.Models = ms
		}
	}
	return &out
}

// CatalogWithRouter returns a catalog with OpenRouter's live model directory
// merged in (cached briefly; silent when unreachable).
func (s *Server) CatalogWithRouter(cat *llm.Catalog) *llm.Catalog {
	data, ok := s.routerModels()
	if !ok || len(data) == 0 {
		return cat
	}
	return llm.MergeRouterModels(cat, data)
}

func (s *Server) providerCatalog() *llm.Catalog {
	if v, ok, _ := s.st.GetSetting(keyProvidersCache); ok && v != "" {
		if cat, err := llm.ParseCatalog([]byte(v)); err == nil {
			return cat
		}
	}
	cat, err := llm.EmbeddedCatalog()
	if err != nil {
		return nil
	}
	return cat
}

// handleRefreshProviders re-fetches models.dev, rebuilds the curated catalog
// and caches it. Failures keep the old data and return ok:false.
func (s *Server) handleRefreshProviders(w http.ResponseWriter, r *http.Request) {
	cat, err := llm.RefreshCatalog(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	data, err := json.Marshal(cat)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := s.st.SetSetting(keyProvidersCache, string(data)); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(cat.Providers)})
}

// handleProviderThinking resolves a model's thinking options from the
// provider's own /models endpoint (family fallback when it publishes none).
func (s *Server) handleProviderThinking(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	if model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	userID := s.currentUser(r).ID
	baseURL, apiKey, _ := s.llmConfig(userID)
	if pid := r.URL.Query().Get("providerId"); pid != "" {
		if p := s.findLLMProvider(userID, pid); p != nil {
			baseURL, apiKey = p.BaseURL, p.APIKey
		}
	}
	if baseURL == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}
	opts := llm.ProviderThinking(r.Context(), baseURL, apiKey, model)
	if opts == nil {
		opts = &llm.ThinkingOptions{}
	}
	writeJSON(w, http.StatusOK, opts)
}

// handleSearchProviders lists OpenAI-compatible providers from the live
// models.dev catalog whose id or name matches the q query parameter. Network
// failures are a 502 without any curated fallback.
func (s *Server) handleSearchProviders(w http.ResponseWriter, r *http.Request) {
	matches, err := llm.SearchProviders(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": matches})
}

// handleAddProvider persists a provider found on models.dev so it appears in
// the dropdown. The id and baseURL are never client-supplied.
func (s *Server) handleAddProvider(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ID == "" {
		writeError(w, http.StatusBadRequest, "provider id is required")
		return
	}
	found, err := llm.FindProvider(r.Context(), body.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if found == nil {
		writeError(w, http.StatusBadRequest, "provider not found on models.dev")
		return
	}
	if s.providerInCatalog(found.ID) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "existing": true, "provider": found})
		return
	}
	if err := s.addCustomProvider(*found); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": found})
}

// handleRemoveProvider drops a runtime-added provider.
func (s *Server) handleRemoveProvider(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.removeCustomProvider(body.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// providerInCatalog reports whether id is a built-in cached provider (but not
// the custom entry or a runtime-added one).
func (s *Server) providerInCatalog(id string) bool {
	if id == "custom" {
		return true
	}
	for _, p := range s.customProviders() {
		if p.ID == id {
			return true
		}
	}
	cat := s.providerCatalog()
	if cat == nil {
		return false
	}
	for _, p := range cat.Providers {
		if p.ID == id {
			return true
		}
	}
	return false
}
