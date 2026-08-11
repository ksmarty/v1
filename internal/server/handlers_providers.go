package server

import (
	"encoding/json"
	"net/http"

	"v1/internal/llm"
)

// handleListProviders serves the provider catalog: the settings-cached copy
// when present, otherwise the embedded snapshot; runtime-added providers and
// the custom entry are appended. Caches built before image-input metadata
// existed are refreshed lazily so the model lists carry vision flags.
func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	cat := s.providerCatalog()
	if cat == nil || !llm.CatalogHasVision(cat) || !llm.CatalogHasReasoning(cat) || !llm.CatalogHasReasoningLevels(cat) {
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
	writeJSON(w, http.StatusOK, cat.WithAdded(s.customProviders()))
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
