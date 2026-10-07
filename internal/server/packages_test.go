package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"

	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"v1/internal/packages"
)

type tarEntry struct {
	name string
	body string
}

func tarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeRegistry serves the three npm endpoints the package installer uses.
func fakeRegistry(t *testing.T, tarball []byte, version string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/-/v1/search":
			io.WriteString(w, `{"objects":[{"package":{"name":"bigpowers","version":"`+version+
				`","description":"handy skills","keywords":["pi-package"],"author":{"name":"Ann"},`+
				`"links":{"npm":"https://www.npmjs.com/package/bigpowers"}},"downloads":{"monthly":42}}]}`)
		case r.URL.Path == "/bigpowers":
			io.WriteString(w, `{"dist-tags":{"latest":"`+version+`"}}`)
		case r.URL.Path == "/bigpowers/-/bigpowers-"+version+".tgz":
			w.Write(tarball)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := packages.RegistryBase
	packages.RegistryBase = srv.URL
	t.Cleanup(func() { packages.RegistryBase = old })
	return srv
}

func TestPackageInstallReachesTheAgent(t *testing.T) {
	tarball := tarGz(t, []tarEntry{
		{"package/index.js", "extension code that v1 cannot run"},
		{"package/skills/alpha/SKILL.md", "---\nname: Alpha\ndescription: Does alpha things\n---\n\nAlpha body.\n"},
		{"package/skills/alpha/templates/note.md", "a template"},
	})
	fakeRegistry(t, tarball, "2.1.0")
	s, adminCookie, _ := newAuthServer(t)

	call := func(method, path, body string) *httptest.ResponseRecorder {
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, path, nil)
		} else {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
		}
		r.Header.Set("Cookie", cookieHeader(adminCookie))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, r)
		return rr
	}

	// Search finds the package.
	rr := call("POST", "/api/packages/search", `{"query":"powers"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rr.Code, rr.Body)
	}
	var found struct {
		Packages []packages.Package `json:"packages"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &found); err != nil {
		t.Fatal(err)
	}
	if len(found.Packages) != 1 || found.Packages[0].Name != "bigpowers" {
		t.Fatalf("search = %+v", found.Packages)
	}

	// Preview shows the skill and its SKILL.md before anything is installed.
	rr = call("GET", "/api/packages/preview?name=bigpowers", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", rr.Code, rr.Body)
	}
	var preview struct {
		Skills []packages.Skill `json:"skills"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Skills) != 1 || !strings.Contains(preview.Skills[0].SkillMD, "Alpha body.") {
		t.Fatalf("preview = %+v", preview.Skills)
	}
	if _, err := os.Stat(filepath.Join(s.skillsRoot(), "alpha")); !os.IsNotExist(err) {
		t.Fatal("preview must not install anything")
	}

	// Install writes the skill and registers it.
	rr = call("POST", "/api/packages/install", `{"name":"bigpowers"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("install = %d: %s", rr.Code, rr.Body)
	}
	installed := s.installedSkills()
	if len(installed) != 1 {
		t.Fatalf("installed = %+v", installed)
	}
	sk := installed[0]
	if sk.Package != "bigpowers" || sk.PackageVersion != "2.1.0" || sk.Name != "Alpha" || !sk.Enabled {
		t.Fatalf("skill = %+v", sk)
	}
	// The files, including nested ones, are on disk.
	if b, err := os.ReadFile(filepath.Join(s.skillsRoot(), "alpha", "templates", "note.md")); err != nil || string(b) != "a template" {
		t.Fatalf("template = %q, %v", b, err)
	}

	// The whole point: the agent's prompt now carries the skill.
	prompt := s.skillsSystemPrompt()
	if !strings.Contains(prompt, "Alpha body.") {
		t.Fatalf("the installed skill is missing from the agent prompt:\n%s", prompt)
	}

	// The extensions tab lists it as an installed package.
	rr = call("GET", "/api/packages/installed", "")
	var list struct {
		Packages []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Skills  []struct {
				Name string `json:"name"`
			} `json:"skills"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Packages) != 1 || list.Packages[0].Name != "bigpowers" ||
		list.Packages[0].Version != "2.1.0" || len(list.Packages[0].Skills) != 1 {
		t.Fatalf("installed packages = %+v", list.Packages)
	}

	// Removing the package removes its files and its registration.
	if rr = call("POST", "/api/packages/remove", `{"name":"bigpowers"}`); rr.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rr.Code, rr.Body)
	}
	if got := s.installedSkills(); len(got) != 0 {
		t.Fatalf("after remove: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(s.skillsRoot(), "alpha")); !os.IsNotExist(err) {
		t.Fatal("the skill directory survived removal")
	}
	if rr = call("POST", "/api/packages/remove", `{"name":"bigpowers"}`); rr.Code != http.StatusNotFound {
		t.Fatalf("second remove = %d, want 404", rr.Code)
	}
}

func TestPackageInstallWithoutSkillsExplainsItself(t *testing.T) {
	// A package that is only extension code: v1 can import nothing from it, and
	// must say so rather than reporting a successful no-op.
	fakeRegistry(t, tarGz(t, []tarEntry{{"package/index.js", "code"}}), "1.0.0")
	s, adminCookie, _ := newAuthServer(t)

	req := httptest.NewRequest("POST", "/api/packages/install", strings.NewReader(`{"name":"bigpowers"}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("install = %d, want 422: %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "Pi CLI") {
		t.Fatalf("error = %s, want it to explain that extension code cannot run here", rr.Body)
	}
}

func TestPackageInstallRequiresAName(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	req := httptest.NewRequest("POST", "/api/packages/install", strings.NewReader(`{"name":"  "}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("install = %d, want 400", rr.Code)
	}
}

func TestPackageSearchReportsRegistryFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	old := packages.RegistryBase
	packages.RegistryBase = srv.URL
	defer func() { packages.RegistryBase = old }()

	s, adminCookie, _ := newAuthServer(t)
	req := httptest.NewRequest("POST", "/api/packages/search", strings.NewReader(`{"query":"x"}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("search = %d, want 502: %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "502") {
		t.Fatalf("error = %s, want the registry status", rr.Body)
	}
}

func TestPackageInstalledIgnoresHandInstalledSkills(t *testing.T) {
	s, _, _ := newAuthServer(t)
	if err := s.saveSkills(nil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/packages/installed", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	var list struct {
		Packages []any `json:"packages"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Packages) != 0 {
		t.Fatalf("packages = %+v", list.Packages)
	}
}
