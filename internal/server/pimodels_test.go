package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"v1/internal/config"
)

func writeCatalog(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, piModelsFile)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// touch moves a file's mtime, so a reload is observed even on a filesystem with
// coarse timestamp granularity.
func touch(t *testing.T, path string, d time.Duration) {
	t.Helper()
	at := time.Now().Add(d)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestPiModelCacheLoadsAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := writeCatalog(t, dir, `{"source":"test","models":{"deepseek-v4.1-flash":{"name":"DeepSeek V4.1 Flash","contextWindow":1000000,"maxTokens":384000,"reasoning":true}}}`)

	c := newPiModelCache()
	cat := c.load(path)
	if cat == nil {
		t.Fatal("catalog not loaded")
	}
	if got := cat.Models["deepseek-v4.1-flash"].ContextWindow; got != 1000000 {
		t.Fatalf("contextWindow = %d, want 1000000", got)
	}

	// A rewritten catalog is picked up without a restart.
	writeCatalog(t, dir, `{"models":{"deepseek-v4.1-flash":{"contextWindow":123}}}`)
	touch(t, path, 2*time.Second)
	cat2 := c.load(path)
	if cat2 == nil || cat2.Models["deepseek-v4.1-flash"].ContextWindow != 123 {
		t.Fatalf("rewritten catalog not picked up: %+v", cat2)
	}

	// Corrupt input never replaces the last good catalog.
	writeCatalog(t, dir, `{not json`)
	touch(t, path, 4*time.Second)
	if got := c.load(path); got != cat2 {
		t.Fatal("a corrupt catalog must not replace the last good one")
	}
}

func TestPiModelCacheToleratesMissingAndEmpty(t *testing.T) {
	dir := t.TempDir()
	c := newPiModelCache()
	if cat := c.load(filepath.Join(dir, piModelsFile)); cat != nil {
		t.Fatal("a missing file must load as nil")
	}
	// The sidecar may not have run yet, so a missing file must never be fatal
	// and must not be remembered as an error.
	if cat := c.load(filepath.Join(dir, piModelsFile)); cat != nil {
		t.Fatal("a missing file must stay nil")
	}
	path := writeCatalog(t, dir, `{"models":{}}`)
	if cat := c.load(path); cat != nil {
		t.Fatal("an empty catalog must load as nil")
	}
}

func TestPiModelContextResolvesKnownModelOnly(t *testing.T) {
	dir := t.TempDir()
	writeCatalog(t, dir, `{"models":{
		"deepseek-v4.1-flash":{"name":"DeepSeek V4.1 Flash","contextWindow":1000000},
		"no-window":{"name":"No Window"}
	}}`)
	s := &Server{cfg: config.Config{DataDir: dir}, piModels: newPiModelCache()}

	if got := s.piModelContext("deepseek-v4.1-flash"); got != 1000000 {
		t.Fatalf("piModelContext = %d, want 1000000", got)
	}
	if got := s.piModelContext("no-window"); got != 0 {
		t.Fatalf("a zero window must report 0, got %d", got)
	}
	if got := s.piModelContext("unknown-model"); got != 0 {
		t.Fatalf("an unknown model must report 0, got %d", got)
	}
	if got := s.piModelContext(""); got != 0 {
		t.Fatalf("an empty model must report 0, got %d", got)
	}
}
