package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// piModelsFile is the catalog the sidecar writes from pi's own model data (see
// sidecar/src/modeldata.js). v1's own catalog does not know every model a user
// can select, and a provider's /models endpoint does not always report a
// context window, so this is the tiebreaker before the fixed server budget.
const piModelsFile = "pi-models.json"

// piModelInfo is one catalog entry, in the shape the sidecar writes.
type piModelInfo struct {
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
	MaxTokens     int    `json:"maxTokens"`
	Reasoning     bool   `json:"reasoning"`
}

type piModelCatalog struct {
	GeneratedAt int64                  `json:"generatedAt"`
	Source      string                 `json:"source"`
	Models      map[string]piModelInfo `json:"models"`
}

// piModelCache holds the parsed catalog together with the file state it was read
// from, so a sidecar that writes a fresh catalog (a new image, a new pi-ai
// release) is picked up without restarting v1.
type piModelCache struct {
	mu      sync.Mutex
	modTime time.Time
	catalog *piModelCatalog
}

func newPiModelCache() *piModelCache { return &piModelCache{} }

// load returns the catalog, re-reading it only when the file changed. A missing
// or unreadable file returns the last good catalog, or nil when there never was
// one — the sidecar may simply not have run yet, which is not an error.
func (c *piModelCache) load(path string) *piModelCatalog {
	info, err := os.Stat(path)
	if err != nil {
		return c.last()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.catalog != nil && info.ModTime().Equal(c.modTime) {
		return c.catalog
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c.catalog
	}
	var cat piModelCatalog
	if json.Unmarshal(data, &cat) != nil || len(cat.Models) == 0 {
		return c.catalog
	}
	c.catalog, c.modTime = &cat, info.ModTime()
	return c.catalog
}

func (c *piModelCache) last() *piModelCatalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.catalog
}

// piModelContext is pi's context window for a model, or 0 when pi has none.
func (s *Server) piModelContext(model string) int {
	if model == "" || s.piModels == nil {
		return 0
	}
	cat := s.piModels.load(filepath.Join(s.cfg.DataDir, piModelsFile))
	if cat == nil {
		return 0
	}
	if m, ok := cat.Models[model]; ok && m.ContextWindow > 0 {
		return m.ContextWindow
	}
	return 0
}
