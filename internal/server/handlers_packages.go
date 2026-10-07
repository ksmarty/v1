package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"v1/internal/packages"
	"v1/internal/skills"
)

// handlePackagesSearch searches the npm registry for packages tagged as pi
// packages.
func (s *Server) handlePackagesSearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	results, err := packages.Search(r.Context(), body.Query, body.Limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": results})
}

// handlePackagePreview downloads a package and returns the skills it bundles,
// so the UI can show what installing it would actually add.
func (s *Server) handlePackagePreview(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	version := r.URL.Query().Get("version")
	found, err := packages.Fetch(r.Context(), name, version)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": strings.TrimSpace(name), "skills": found})
}

// handlePackageInstall imports a package's skills into the skills directory and
// registers them, enabled, as installed skills.
//
// A pi package's extension code targets the Pi CLI runtime, which v1 does not
// embed, so only its skills can be used here. A package that bundles none is
// rejected with an explanation rather than silently doing nothing.
func (s *Server) handlePackageInstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "a package name is required")
		return
	}
	version := strings.TrimSpace(body.Version)
	if version == "" {
		v, err := packages.LatestVersion(r.Context(), body.Name)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		version = v
	}
	found, err := packages.Fetch(r.Context(), body.Name, version)
	if err != nil {
		writeError(w, http.StatusBadGateway, "install failed: "+err.Error())
		return
	}
	if len(found) == 0 {
		writeError(w, http.StatusUnprocessableEntity,
			"this package bundles no skills — its extension code is written for the Pi CLI, which v1 does not run")
		return
	}

	// Re-installing replaces the package's previous skills and any skill that
	// occupies the same directory.
	dirs := make(map[string]bool, len(found))
	for _, pkgSkill := range found {
		dirs[pkgSkill.Dir] = true
	}
	root := s.skillsRoot()
	merged := make([]skills.Skill, 0, len(s.installedSkills())+len(found))
	for _, sk := range s.installedSkills() {
		if sk.Package == body.Name || dirs[sk.Dir] {
			continue
		}
		merged = append(merged, sk)
	}
	added := make([]skills.Skill, 0, len(found))
	for _, pkgSkill := range found {
		if err := pkgSkill.Write(root); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sk := skills.Skill{
			ID:             body.Name + "/" + pkgSkill.Dir,
			Name:           pkgSkill.Name,
			Description:    pkgSkill.Description,
			Dir:            pkgSkill.Dir,
			Enabled:        true,
			Package:        body.Name,
			PackageVersion: version,
		}
		merged = append(merged, sk)
		added = append(added, sk)
	}
	if err := s.saveSkills(merged); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installed": added,
		"version":   version,
		"skills":    merged,
	})
}

// handlePackagesInstalled groups the installed skills by the package they were
// imported from.
func (s *Server) handlePackagesInstalled(w http.ResponseWriter, r *http.Request) {
	type installedPackage struct {
		Name    string         `json:"name"`
		Version string         `json:"version"`
		Skills  []skills.Skill `json:"skills"`
	}
	byName := map[string]*installedPackage{}
	var order []string
	for _, sk := range s.installedSkills() {
		if sk.Package == "" {
			continue
		}
		p := byName[sk.Package]
		if p == nil {
			p = &installedPackage{Name: sk.Package, Version: sk.PackageVersion}
			byName[sk.Package] = p
			order = append(order, sk.Package)
		}
		p.Skills = append(p.Skills, sk)
	}
	out := make([]installedPackage, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": out})
}

// handlePackageRemove deletes every skill a package imported and forgets them.
func (s *Server) handlePackageRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	cur := s.installedSkills()
	out := make([]skills.Skill, 0, len(cur))
	removed := 0
	for _, sk := range cur {
		if sk.Package == body.Name {
			_ = os.RemoveAll(filepath.Join(s.skillsRoot(), sk.Dir))
			removed++
			continue
		}
		out = append(out, sk)
	}
	if removed == 0 {
		writeError(w, http.StatusNotFound, "that package is not installed")
		return
	}
	if err := s.saveSkills(out); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": out})
}
