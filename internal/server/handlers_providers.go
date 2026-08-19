package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
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
	writeJSON(w, http.StatusOK, s.finalizeProviders(cat, r))
}

// customModelsCache holds short-lived /v1/models listings keyed by base URL,
// each with its own fetch time so one provider's refresh never expiries
// another's. Entries are dropped via invalidateCustomModelsCache whenever
// provider configuration changes (see handlers_auth/server), so a newly added
// provider's model list is fetched on the very next request.
var customModelsCache struct {
	sync.Mutex
	byBase map[string]customModelsEntry
}

type customModelsEntry struct {
	at   time.Time
	list []llm.ProviderModel
}

// invalidateCustomModelsCache drops all cached live model lists so the next
// providers request re-hits every endpoint. Called whenever provider
// configuration changes (save/add/remove), so newly configured providers
// populate immediately without any manual cache clearing.
func invalidateCustomModelsCache() {
	customModelsCache.Lock()
	customModelsCache.byBase = nil
	customModelsCache.Unlock()
}

// customProviderModels fetches (and briefly caches) the model list for a
// custom base URL via its OpenAI-compatible /models endpoint. Reports nil when
// unreachable or unparsable (callers keep whatever the catalog already had).
func (s *Server) customProviderModels(ctx context.Context, baseURL, apiKey string) []llm.ProviderModel {
	key := strings.ToLower(strings.TrimRight(baseURL, "/"))
	customModelsCache.Lock()
	ent, ok := customModelsCache.byBase[key]
	customModelsCache.Unlock()
	if ok && time.Since(ent.at) < 10*time.Minute {
		return ent.list
	}
	ms, err := llm.ProviderModelsEndpoint(ctx, baseURL, apiKey)
	if err != nil {
		return nil
	}
	customModelsCache.Lock()
	if customModelsCache.byBase == nil {
		customModelsCache.byBase = map[string]customModelsEntry{}
	}
	customModelsCache.byBase[key] = customModelsEntry{at: time.Now(), list: ms}
	customModelsCache.Unlock()
	return ms
}

// normalizeBase lowercases and trims a base URL for stable matching & cache keys.
func normalizeBase(baseURL string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
}

// finalizeProviders appends the user's saved providers to the catalog (so the
// chat picker, which unions catalog entries by base URL, sees them) and fills
// in live model lists for any provider entry that has a base URL but no
// catalog models, authenticated with the matching saved provider's API key.
func (s *Server) finalizeProviders(cat *llm.Catalog, r *http.Request) *llm.Catalog {
	if cat == nil {
		return cat
	}
	userID := s.currentUser(r).ID
	out := *cat
	out.Providers = append([]llm.Provider(nil), cat.Providers...)

	seen := map[string]bool{}
	for _, p := range out.Providers {
		if p.BaseURL != "" {
			seen[normalizeBase(p.BaseURL)] = true
		}
	}
	keys := map[string]string{}
	for _, lp := range s.llmProviders(userID) {
		if lp.BaseURL == "" {
			continue
		}
		nb := normalizeBase(lp.BaseURL)
		keys[nb] = lp.APIKey
		if seen[nb] {
			continue
		}
		out.Providers = append(out.Providers, llm.Provider{ID: lp.ID, Name: lp.Name, BaseURL: lp.BaseURL, Added: true})
		seen[nb] = true
	}

	// Live model lists are the default for any provider the user has actually
	// configured (a saved provider), explicitly added as a custom provider, or
	// whose /models endpoint is public (NVIDIA). For those, the endpoint is
	// authoritative and the pinned catalog list is only a fallback: live
	// entries replace the static ones, but static metadata (context size,
	// vision, reasoning levels) is re-applied when the id matches so the
	// picker keeps its rich flags. Everything else in the catalog keeps its
	// curated static models.
	live := map[string]bool{}
	for _, lp := range s.llmProviders(userID) {
		if lp.BaseURL != "" {
			live[normalizeBase(lp.BaseURL)] = true
		}
	}
	for _, cp := range s.customProviders() {
		if cp.BaseURL != "" {
			live[normalizeBase(cp.BaseURL)] = true
		}
	}
	live[normalizeBase(llm.NVIDIABaseURL)] = true

	merge := func(static, liveModels []llm.ProviderModel) []llm.ProviderModel {
		byID := map[string]llm.ProviderModel{}
		for _, m := range liveModels {
			byID[m.ID] = m
		}
		for _, m := range static {
			if cur, ok := byID[m.ID]; ok {
				if cur.Name == "" {
					cur.Name = m.Name
				}
				if !cur.ImageInput && m.ImageInput {
					cur.ImageInput = true
				}
				if cur.Reasoning == nil && m.Reasoning != nil {
					cur.Reasoning = m.Reasoning
				}
				byID[m.ID] = cur
			}
		}
		out := make([]llm.ProviderModel, 0, len(byID))
		for _, m := range byID {
			out = append(out, m)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out
	}

	for i := range out.Providers {
		p := &out.Providers[i]
		if p.BaseURL == "" {
			continue
		}
		nb := normalizeBase(p.BaseURL)
		if !live[nb] {
			continue
		}
		if ms := s.customProviderModels(r.Context(), p.BaseURL, keys[nb]); len(ms) > 0 {
			// Endpoint reached: its list wins, enriched with any known static
			// metadata for matching ids. Unreachable => keep catalog list.
			p.Models = merge(p.Models, ms)
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
