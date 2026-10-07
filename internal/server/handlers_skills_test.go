package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuiltinSkillInstallResponseShape guards the response contract of the
// built-in branch of POST /api/skills/install.
//
// That branch used to answer with `installed` only, while the marketplace
// branch answers with `skills` — the full list the client renders. The UI does
// `setSkills(r.skills)`, so installing a built-in stored undefined and the next
// render crashed on `undefined.some(...)`. The install had in fact succeeded,
// which is why reloading showed the skill present.
func TestBuiltinSkillInstallResponseShape(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)

	req := httptest.NewRequest("POST", "/api/skills/install",
		strings.NewReader(`{"skill":{"id":"persistent-tool-install"}}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("install builtin: %d %s", rr.Code, rr.Body.String())
	}

	var body struct {
		Skills []struct {
			ID      string `json:"id"`
			Dir     string `json:"dir"`
			Builtin bool   `json:"builtin"`
		} `json:"skills"`
		Installed struct {
			ID string `json:"id"`
		} `json:"installed"`
		Builtin bool `json:"builtin"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Skills) == 0 {
		t.Fatalf("response carried no `skills` list, but the UI renders r.skills: %s", rr.Body.String())
	}
	var dir string
	for _, sk := range body.Skills {
		if sk.ID == "persistent-tool-install" {
			dir = sk.Dir
		}
	}
	if dir == "" {
		t.Fatalf("installed skill missing from `skills`: %s", rr.Body.String())
	}
	if body.Installed.ID != "persistent-tool-install" {
		t.Fatalf("`installed` should name the skill just added, got %q", body.Installed.ID)
	}
	if !body.Builtin {
		t.Fatal("builtin flag not set")
	}

	// A built-in is materialized from the binary rather than downloaded.
	if _, err := os.Stat(filepath.Join(s.skillsRoot(), dir, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md not written: %v", err)
	}
}
