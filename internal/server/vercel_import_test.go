package server

import (
	"testing"

	"v1/internal/store"
)

func TestRepoSlug(t *testing.T) {
	cases := map[string]string{
		"https://github.com/ksmarty/v1":     "ksmarty/v1",
		"https://github.com/ksmarty/v1.git": "ksmarty/v1",
		"https://github.com/ksmarty/v1/":    "ksmarty/v1",
		"git@github.com:ksmarty/v1.git":     "ksmarty/v1",
		"  https://github.com/ksmarty/v1 ":  "ksmarty/v1",
		// Anything that is not a GitHub remote must not be sent to Vercel as a
		// repository slug.
		"https://gitlab.com/ksmarty/v1": "",
		"https://github.com/ksmarty":    "",
		"":                              "",
	}
	for in, want := range cases {
		if got := repoSlug(in); got != want {
			t.Errorf("repoSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVercelProjectNamePrefersTheLinkedProject(t *testing.T) {
	// Once a project has been imported, deploys and the deployment list have to
	// target the linked Vercel project rather than one named after the v1 project.
	linked := &store.Project{Name: "My App", VercelProject: "linked-app"}
	if got := vercelProjectName(linked); got != "linked-app" {
		t.Errorf("linked project name = %q", got)
	}
	plain := &store.Project{Name: "My App"}
	if got := vercelProjectName(plain); got != "my-app" {
		t.Errorf("fallback project name = %q", got)
	}
}
