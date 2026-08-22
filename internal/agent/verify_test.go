package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scaffoldNodeProject writes a minimal node project with scripted scripts.
func scaffoldNodeProject(t *testing.T, dir string, scripts map[string]string) {
	t.Helper()
	pkg := map[string]any{"name": "verify-fixture", "scripts": scripts}
	data, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Fake an installed dependency tree so the install step is skipped
	// (offline-friendly test) and npm run <script> still works.
	marker := filepath.Join(dir, "node_modules")
	if err := os.MkdirAll(marker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marker, ".package-lock.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(filepath.Join(marker, ".package-lock.json"), future, future)
	// An old lockfile keeps npm from treating the project as uncommitted.
	_ = os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o644)
	past := time.Now().Add(-time.Minute)
	_ = os.Chtimes(filepath.Join(dir, "package-lock.json"), past, past)
}

func TestVerifyProjectPipeline(t *testing.T) {
	e := newTestExecutor(t)
	scaffoldNodeProject(t, e.Root, map[string]string{
		"lint":      "echo lint-ok",
		"build":     "node -e \"console.log('build-ok')\"",
		"typecheck": "echo type-ok",
		"test":      "echo test-ok",
	})
	res := executeContract(t, e, context.Background(), "verify_project", `{}`)
	if res["ok"] != true {
		t.Fatalf("expected ok, got %v (%v)", res["ok"], res["errors"])
	}
	steps, _ := res["steps"].([]any)
	seen := map[string]bool{}
	for _, s := range steps {
		m := s.(map[string]any)
		seen[m["name"].(string)] = true
	}
	for _, want := range []string{"install", "lint", "typecheck", "build", "test", "secrets-scan"} {
		if !seen[want] {
			t.Fatalf("missing step %q in %v", want, steps)
		}
	}
	if res["projectType"] != "node" {
		t.Fatalf("projectType = %v, want node", res["projectType"])
	}
}

func TestVerifyProjectFailsOnBuildError(t *testing.T) {
	e := newTestExecutor(t)
	scaffoldNodeProject(t, e.Root, map[string]string{
		"build": "node -e \"process.exit(1)\"",
	})
	res := executeContract(t, e, context.Background(), "verify_project", `{}`)
	if res["ok"] != false {
		t.Fatalf("expected failure, got %v", res)
	}
	errs, _ := res["errors"].([]any)
	joined := ""
	for _, er := range errs {
		if s, ok := er.(string); ok {
			joined += s + "\n"
		}
	}
	if !strings.Contains(joined, "build") {
		t.Fatalf("errors = %v, want the build failure listed", errs)
	}
}

// TestVerifyProjectSecretScan verifies leaked credentials fail the pipeline
// and are masked in the report. The scan skips .env files (the sanctioned
// location) but flags them when .gitignore doesn't cover them.
func TestVerifyProjectSecretScan(t *testing.T) {
	e := newTestExecutor(t)
	scaffoldNodeProject(t, e.Root, map[string]string{"build": "echo build-ok"})
	if err := os.WriteFile(filepath.Join(e.Root, "api.ts"), []byte("const key = 'sk-1234567890abcdef1234567890abcdef';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := executeContract(t, e, context.Background(), "verify_project", `{}`)
	if res["ok"] != false {
		t.Fatalf("expected failure due to leaked secret, got %v", res)
	}
	steps, _ := res["steps"].([]any)
	var scanOut string
	for _, s := range steps {
		m := s.(map[string]any)
		if m["name"] == "secrets-scan" {
			scanOut, _ = m["output"].(string)
		}
	}
	if !strings.Contains(scanOut, "OpenAI API key") {
		t.Fatalf("scan output = %q, want the OpenAI key finding", scanOut)
	}
	if strings.Contains(scanOut, "sk-1234567890abcdef1234567890abcdef") {
		t.Fatalf("scan leaked the full secret instead of masking: %q", scanOut)
	}
	// .env not gitignored -> suggestion.
	if err := os.WriteFile(filepath.Join(e.Root, ".env"), []byte("FOO=bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = executeContract(t, e, context.Background(), "verify_project", `{}`)
	sugs, _ := res["suggestions"].([]any)
	found := false
	for _, s := range sugs {
		if str, _ := s.(string); strings.Contains(str, ".gitignore") {
			found = true
		}
	}
	if !found {
		t.Fatalf("suggestions = %v, want a .gitignore/.env suggestion", sugs)
	}
	// Once .env is covered, the suggestion disappears.
	_ = os.WriteFile(filepath.Join(e.Root, ".gitignore"), []byte(".env*\n"), 0o644)
	res = executeContract(t, e, context.Background(), "verify_project", `{}`)
	sugs, _ = res["suggestions"].([]any)
	for _, s := range sugs {
		if str, _ := s.(string); strings.Contains(str, ".gitignore") {
			t.Fatalf("suggestion should vanish after .gitignore covers .env: %v", sugs)
		}
	}
}

// TestVerifyProjectPlainProject verifies a non-code project passes cleanly.
func TestVerifyProjectPlainProject(t *testing.T) {
	e := newTestExecutor(t)
	res := executeContract(t, e, context.Background(), "verify_project", `{}`)
	if res["ok"] != true {
		t.Fatalf("plain project should pass, got %v", res)
	}
	if res["projectType"] != "plain" {
		t.Fatalf("projectType = %v, want plain", res["projectType"])
	}
}

// TestVerifyProjectPreviewHealth checks the preview step: 2xx passes, 5xx fails.
func TestVerifyProjectPreviewHealth(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()
	e := newTestExecutor(t)
	scaffoldNodeProject(t, e.Root, map[string]string{"build": "echo build-ok"})
	e.PreviewURL = func() string { return okSrv.URL }
	res := executeContract(t, e, context.Background(), "verify_project", `{}`)
	if res["ok"] != true {
		t.Fatalf("healthy preview should pass, got %v", res)
	}

	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer badSrv.Close()
	e.PreviewURL = func() string { return badSrv.URL }
	res = executeContract(t, e, context.Background(), "verify_project", `{}`)
	if res["ok"] != false {
		t.Fatalf("unhealthy preview should fail, got %v", res)
	}
	steps, _ := res["steps"].([]any)
	for _, s := range steps {
		m := s.(map[string]any)
		if m["name"] == "preview" && m["success"] != false {
			t.Fatalf("preview step should be unsuccessful: %v", m)
		}
	}
	// No live preview -> skipped, still ok.
	e.PreviewURL = func() string { return "" }
	res = executeContract(t, e, context.Background(), "verify_project", `{}`)
	if res["ok"] != true {
		t.Fatalf("skipped preview should pass, got %v", res)
	}
}
