package packages

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	body string
}

func buildTarball(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
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

func TestExtractSkills(t *testing.T) {
	md := "---\nname: Alpha Skill\ndescription: \"Does alpha things\"\n---\n\n# Alpha\n\nBody.\n"
	tarball := buildTarball(t, []entry{
		{"package/package.json", `{"name":"p"}`},
		{"package/README.md", "readme"},
		{"package/src/index.js", "code"},
		{"package/skills/alpha/SKILL.md", md},
		{"package/skills/alpha/templates/thing.md", "template"},
		{"package/skills/beta/SKILL.md", "---\nname: Beta\n---\n"},
		// A directory under skills/ with no SKILL.md is not a skill.
		{"package/skills/gamma/notes.txt", "not a skill"},
	})

	got, err := extractSkills(tarball)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("skills = %d (%v), want 2", len(got), got)
	}
	alpha := got[0]
	if alpha.Dir != "alpha" || alpha.Name != "Alpha Skill" || alpha.Description != "Does alpha things" {
		t.Fatalf("alpha = %+v", alpha)
	}
	if alpha.SkillMD != md {
		t.Fatalf("SKILL.md was not preserved verbatim")
	}
	// Files are keyed relative to the skill directory, so a nested template
	// keeps its path.
	if string(alpha.Files["SKILL.md"]) != md || string(alpha.Files["templates/thing.md"]) != "template" {
		t.Fatalf("alpha files = %v", keys(alpha.Files))
	}
	if got[1].Name != "Beta" {
		t.Fatalf("beta = %+v", got[1])
	}
}

func TestExtractSkillsNameFallsBackToDirectory(t *testing.T) {
	got, err := extractSkills(buildTarball(t, []entry{
		{"package/skills/no-frontmatter/SKILL.md", "# Just a heading\n"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "no-frontmatter" {
		t.Fatalf("got %+v, want the directory name as the fallback", got)
	}
}

func TestExtractSkillsRejectsUnsafePaths(t *testing.T) {
	got, err := extractSkills(buildTarball(t, []entry{
		{"package/skills/../../etc/SKILL.md", "escaped"},
		{"package/skills/ok/SKILL.md", "---\nname: ok\n---\n"},
		{"package/skills/ok/../../../evil.md", "escaped"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Dir != "ok" {
		t.Fatalf("got %+v, want only the safe skill", got)
	}
	if _, bad := got[0].Files["../../../evil.md"]; bad {
		t.Fatal("a traversal file was kept")
	}
}

func TestSkillWrite(t *testing.T) {
	root := t.TempDir()
	s := Skill{Dir: "alpha", Files: map[string][]byte{
		"SKILL.md":           []byte("---\nname: a\n---\n"),
		"templates/thing.md": []byte("template"),
	}}
	if err := s.Write(root); err != nil {
		t.Fatalf("write: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "alpha", "templates", "thing.md")); err != nil || string(b) != "template" {
		t.Fatalf("nested file = %q, %v", b, err)
	}

	for name, bad := range map[string]Skill{
		"traversal dir": {Dir: "../escape", Files: map[string][]byte{"a": {}}},
		"nested dir":    {Dir: "a/b", Files: map[string][]byte{"a": {}}},
		"absolute file": {Dir: "ok", Files: map[string][]byte{"/tmp/evil": {}}},
		"escaping file": {Dir: "ok", Files: map[string][]byte{"../../evil": {}}},
	} {
		if err := bad.Write(root); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); !os.IsNotExist(err) {
		t.Fatal("a traversal directory was created")
	}
}

func TestFrontmatter(t *testing.T) {
	cases := []struct {
		name, md, wantName, wantDesc string
	}{
		{"plain", "---\nname: A\ndescription: B\n---\nbody", "A", "B"},
		{"quoted", "---\nname: \"A B\"\ndescription: 'C: D'\n---\n", "A B", "C: D"},
		{"crlf", "---\r\nname: A\r\ndescription: B\r\n---\r\n", "A", "B"},
		{"leading blank", "\n\n---\nname: A\n---\n", "A", ""},
		{"none", "# Title\n", "", ""},
		{"unterminated", "---\nname: A\n", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, desc := frontmatter(tc.md)
			if name != tc.wantName || desc != tc.wantDesc {
				t.Fatalf("frontmatter(%q) = %q, %q; want %q, %q", tc.md, name, desc, tc.wantName, tc.wantDesc)
			}
		})
	}
}

func TestTarballURL(t *testing.T) {
	if got := tarballURL("bigpowers", "1.2.3"); got != RegistryBase+"/bigpowers/-/bigpowers-1.2.3.tgz" {
		t.Fatalf("url = %q", got)
	}
	if got := tarballURL("@scope/pkg", "1.0.0"); got != RegistryBase+"/@scope/pkg/-/pkg-1.0.0.tgz" {
		t.Fatalf("scoped url = %q", got)
	}
}

func TestSearch(t *testing.T) {
	body := `{"objects":[
	 {"package":{"name":"bigpowers","version":"1.0.0","description":"big","keywords":["pi-package","tools"],
	   "author":{"name":"Ann"},"date":"2026-01-01T00:00:00.000Z",
	   "links":{"npm":"https://www.npmjs.com/package/bigpowers","repository":"https://github.com/x/y"}},
	  "downloads":{"monthly":1234},"updated":"2026-02-01T00:00:00.000Z"},
	 {"package":{"name":"mentions-it","version":"2.0.0","description":"a pi-package for things","keywords":["other"],
	   "author":"Bob","links":{}}},
	 {"package":{"name":"no-keywords","version":"3.0.0","keywords":[],"links":{}}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("text"), "keywords:pi-package") {
			t.Errorf("query %q does not filter by keyword", r.URL.Query().Get("text"))
		}
		io.WriteString(w, body)
	}))
	defer srv.Close()
	withRegistry(t, srv.URL)

	got, err := Search(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("results = %d (%v), want the keyworded package and the one with no keyword list", len(got), got)
	}
	first := got[0]
	if first.Name != "bigpowers" || first.Author != "Ann" || first.Downloads != 1234 {
		t.Fatalf("first = %+v", first)
	}
	if first.NPMURL == "" || first.Repository == "" || first.Updated == "" {
		t.Fatalf("links/updated were dropped: %+v", first)
	}
	// A result the registry returned without a keyword list is kept, and gets a
	// usable npm link.
	if got[1].Name != "no-keywords" || got[1].NPMURL != "https://www.npmjs.com/package/no-keywords" {
		t.Fatalf("second = %+v", got[1])
	}
}

func TestSearchRegistryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	withRegistry(t, srv.URL)

	if _, err := Search(context.Background(), "x", 10); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("error = %q", err)
	}
}

func TestFetchResolvesLatestAndReadsSkills(t *testing.T) {
	tarball := buildTarball(t, []entry{
		{"package/skills/alpha/SKILL.md", "---\nname: Alpha\ndescription: A\n---\n"},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/bigpowers":
			io.WriteString(w, `{"dist-tags":{"latest":"1.4.0"}}`)
		case r.URL.Path == "/bigpowers/-/bigpowers-1.4.0.tgz":
			w.Write(tarball)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	withRegistry(t, srv.URL)

	got, err := Fetch(context.Background(), "bigpowers", "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Alpha" {
		t.Fatalf("skills = %+v", got)
	}
}

func TestFetchPackageWithoutSkills(t *testing.T) {
	tarball := buildTarball(t, []entry{{"package/index.js", "code"}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarball)
	}))
	defer srv.Close()
	withRegistry(t, srv.URL)

	got, err := Fetch(context.Background(), "plain", "1.0.0")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("skills = %+v, want none", got)
	}
}

func TestFetchRequiresName(t *testing.T) {
	if _, err := Fetch(context.Background(), "  ", "1.0.0"); err == nil {
		t.Fatal("expected an error")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// withRegistry points the package at a test registry for the duration of a test.
func withRegistry(t *testing.T, base string) {
	t.Helper()
	old := RegistryBase
	RegistryBase = base
	t.Cleanup(func() { RegistryBase = old })
}
