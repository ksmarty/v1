package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"v1/internal/llm"
)

// providerUsageView is the reply of GET /api/providers/usage. It is always a
// 200: a provider that publishes no usage is a normal state (supported=false,
// the button renders disabled), and a failed read carries its reason in Error
// without hiding the button the user chose to put in the composer.
type providerUsageView struct {
	Supported  bool              `json:"supported"`
	ProviderID string            `json:"providerId,omitempty"`
	Provider   string            `json:"provider,omitempty"`
	Windows    []llm.UsageWindow `json:"windows,omitempty"`
	FetchedAt  time.Time         `json:"fetchedAt,omitzero"`
	Error      string            `json:"error,omitempty"`
}

const (
	usageCacheTTL     = 60 * time.Second
	usageUnsupported  = 10 * time.Minute
	usageFetchTimeout = 15 * time.Second
)

// providerUsageCache keeps the last reading per user+endpoint, so a composer
// that re-renders while a turn streams does not turn into a stream of requests
// to the provider's rate-limit endpoint (which is itself rate limited).
var providerUsageCache = struct {
	sync.Mutex
	entries map[string]providerUsageEntry
}{entries: map[string]providerUsageEntry{}}

type providerUsageEntry struct {
	at   time.Time
	ttl  time.Duration
	view providerUsageView
}

func usageCacheGet(key string) (providerUsageView, bool) {
	providerUsageCache.Lock()
	defer providerUsageCache.Unlock()
	e, ok := providerUsageCache.entries[key]
	if !ok || time.Since(e.at) > e.ttl {
		return providerUsageView{}, false
	}
	return e.view, true
}

func usageCachePut(key string, view providerUsageView, ttl time.Duration) {
	providerUsageCache.Lock()
	defer providerUsageCache.Unlock()
	// Bound the map: one entry per endpoint a user has configured is tiny, but
	// the short TTL would otherwise leave expired keys behind forever.
	if len(providerUsageCache.entries) > 64 {
		for k, e := range providerUsageCache.entries {
			if time.Since(e.at) > e.ttl {
				delete(providerUsageCache.entries, k)
			}
		}
	}
	providerUsageCache.entries[key] = providerUsageEntry{at: time.Now(), ttl: ttl, view: view}
}

// handleProviderUsage reports how much of the selected provider's plan is
// spent. The endpoint is discovered from the base URL (internal/llm's usage
// driver registry), so supporting another provider needs no change here.
func (s *Server) handleProviderUsage(w http.ResponseWriter, r *http.Request) {
	userID := s.currentUser(r).ID
	providerID := r.URL.Query().Get("providerId")
	force := r.URL.Query().Get("force") == "1"

	baseURL, apiKey, _ := s.llmConfig(userID)
	// The saved provider's own name is what the reader picked in the model
	// picker, so prefer it over the driver's family label.
	providerName := ""
	if p := s.findLLMProvider(userID, providerID); p != nil {
		baseURL, apiKey = p.BaseURL, p.APIKey
		providerName = p.Name
	}
	if baseURL == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}

	key := userID + "|" + baseURL
	if !force {
		if view, ok := usageCacheGet(key); ok {
			writeJSON(w, http.StatusOK, view)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), usageFetchTimeout)
	defer cancel()
	usage, err := llm.NewClient(baseURL, apiKey, "").FetchUsage(ctx)
	if err != nil {
		if errors.Is(err, llm.ErrUsageUnsupported) {
			view := providerUsageView{Supported: false, ProviderID: providerID}
			usageCachePut(key, view, usageUnsupported)
			writeJSON(w, http.StatusOK, view)
			return
		}
		// The provider does publish usage; this read just failed. Keep the
		// button live and let the client show the reason, never cache it.
		writeJSON(w, http.StatusOK, providerUsageView{
			Supported:  true,
			ProviderID: providerID,
			Error:      err.Error(),
		})
		return
	}
	if providerName == "" {
		providerName = usage.Provider
	}
	view := providerUsageView{
		Supported:  true,
		ProviderID: providerID,
		Provider:   providerName,
		Windows:    usage.Windows,
		FetchedAt:  usage.FetchedAt,
	}
	usageCachePut(key, view, usageCacheTTL)
	writeJSON(w, http.StatusOK, view)
}
