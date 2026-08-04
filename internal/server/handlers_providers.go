package server

import (
	"encoding/json"
	"net/http"

	"v1/internal/llm"
)

// handleListProviders serves the provider catalog: the settings-cached copy
// when present, otherwise the embedded snapshot; always with the custom
// entry appended.
func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	cat := s.providerCatalog()
	if cat == nil {
		writeError(w, http.StatusInternalServerError, "provider catalog unavailable")
		return
	}
	writeJSON(w, http.StatusOK, cat.WithCustom())
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
