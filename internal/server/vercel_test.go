package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectFrameworkSignalFiles verifies config-file detection for the
// common frameworks.
func TestDetectFrameworkSignalFiles(t *testing.T) {
	cases := []struct{ file, fw string }{
		{"next.config.js", "nextjs"},
		{"next.config.ts", "nextjs"},
		{"vite.config.ts", "vite"},
		{"vue.config.js", ""}, // not in the signal list; falls through to package.json
	}
	for _, tc := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, tc.file), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := detectFramework(dir); got != tc.fw {
			t.Fatalf("%s: detectFramework = %q, want %q", tc.file, got, tc.fw)
		}
	}
}

// TestDetectFrameworkFromPackageJSON verifies dependency-based detection
// (dependencies and devDependencies) and that unknown stacks yield "".
func TestDetectFrameworkFromPackageJSON(t *testing.T) {
	cases := []struct {
		json string
		fw   string
	}{
		{`{"dependencies":{"next":"14.0.0"}}`, "nextjs"},
		{`{"dependencies":{"@angular/core":"17.0.0"}}`, "angular"},
		{`{"devDependencies":{"svelte":"4.0.0"}}`, "svelte"},
		{`{"devDependencies":{"@sveltejs/kit":"^1.0.0"}}`, "sveltekit"},
		{`{"dependencies":{"astro":"^3.0.0"}}`, "astro"},
		{`{"dependencies":{"vue":"3.0.0"}}`, "vuejs"},
		{`{"dependencies":{"react":"18.0.0"},"devDependencies":{"vite":"5.0.0"}}`, "create-react-app"},
		{`{"dependencies":{"express":"4.0.0"}}`, ""},
		{`not json`, ""},
		{``, ""},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		if tc.json != "" {
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(tc.json), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := detectFramework(dir); got != tc.fw {
			t.Fatalf("pkg %q: detectFramework = %q, want %q", tc.json, got, tc.fw)
		}
	}
}

// TestSlugifyVercelName verifies project names become valid Vercel names.
func TestSlugifyVercelName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"My Cool App!", "my-cool-app"},
		{"---trailing---", "trailing"},
		{"", "v1-project"},
		{"Already_fine.123", "already_fine.123"},
	}
	for _, tc := range cases {
		if got := slugify(tc.in); got != tc.want {
			t.Fatalf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
