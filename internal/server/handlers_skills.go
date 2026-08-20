package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"v1/internal/gitops"
	"v1/internal/skills"
)

// ---- installed skills (skillsmp) ----

// skillsRoot is the directory where installed skills live.
func (s *Server) skillsRoot() string {
	return filepath.Join(s.cfg.DataDir, "skills")
}

// installedSkills returns the persisted list of installed skills.
func (s *Server) installedSkills() []skills.Skill {
	v, ok, _ := s.st.GetSetting(keySkills)
	if !ok || v == "" {
		return nil
	}
	var out []skills.Skill
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil
	}
	return out
}

func (s *Server) saveSkills(list []skills.Skill) error {
	if list == nil {
		list = []skills.Skill{}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.st.SetSetting(keySkills, string(raw))
}

// skillsSystemPrompt renders the SKILL.md contents of enabled installed skills.
func (s *Server) skillsSystemPrompt() string {
	return skills.SystemPrompt(s.skillsRoot(), s.installedSkills())
}

// handleSkillsSearch queries the SkillsMP marketplace.
func (s *Server) handleSkillsSearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	q := strings.ToLower(strings.TrimSpace(body.Query))
	// Built-in skills ship with v1; surface them when they match (or always
	// for an empty query — the "Suggested" group needs no marketplace call, so
	// it still works offline).
	builtin := make([]skills.Skill, 0, len(skills.Builtins()))
	for _, b := range skills.Builtins() {
		if q != "" &&
			!strings.Contains(strings.ToLower(b.Skill.Name), q) &&
			!strings.Contains(strings.ToLower(b.Skill.Description), q) {
			continue
		}
		b.Skill.Builtin = true
		builtin = append(builtin, b.Skill)
	}
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"skills": builtin})
		return
	}
	results, err := skills.Search(r.Context(), body.Query, 20)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": append(builtin, results...)})
}

// handleSkillsInstall downloads a marketplace skill into the skills dir and
// adds it to the persisted list (enabled by default).
func (s *Server) handleSkillsInstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Skill skills.Skill `json:"skill"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// Built-in skills are materialized from the binary instead of downloaded.
	if b := skills.FindBuiltin(body.Skill.ID); b != nil {
		if err := s.installBuiltinSkill(*b); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sk := b.Skill
		sk.Builtin = true // persist the flag so the UI can hide marketplace links
		installed := append(s.installedSkills(), sk)
		if err := s.saveSkills(installed); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"installed": installed, "builtin": true})
		return
	}
	if body.Skill.Name == "" || body.Skill.Owner == "" {
		writeError(w, http.StatusBadRequest, "skill is missing name or source")
		return
	}
	installed, err := skills.Install(r.Context(), s.ghForSkills(s.currentUser(r).ID), body.Skill, s.skillsRoot())
	if err != nil {
		writeError(w, http.StatusBadGateway, "install failed: "+err.Error())
		return
	}
	cur := s.installedSkills()
	out := make([]skills.Skill, 0, len(cur)+1)
	for _, sk := range cur {
		if sk.ID != installed.ID && sk.Dir != installed.Dir {
			out = append(out, sk)
		}
	}
	out = append(out, installed)
	if err := s.saveSkills(out); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": out, "installed": installed})
}

// handleSkillReadme returns an installed skill's SKILL.md so the UI can show
// a detail preview without leaving the app.
func (s *Server) handleSkillReadme(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, sk := range s.installedSkills() {
		if sk.ID != id && sk.Dir != id {
			continue
		}
		// Dir must be a plain directory name under the skills root.
		if sk.Dir == "" || sk.Dir != filepath.Base(sk.Dir) {
			break
		}
		data, err := os.ReadFile(filepath.Join(s.skillsRoot(), sk.Dir, "SKILL.md"))
		if err != nil {
			writeError(w, http.StatusNotFound, "no SKILL.md for this skill")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"content": string(data)})
		return
	}
	writeError(w, http.StatusNotFound, "skill not found")
}

// handleSkillsRemove deletes an installed skill and its files.
func (s *Server) handleSkillsRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	cur := s.installedSkills()
	out := make([]skills.Skill, 0, len(cur))
	removed := false
	for _, sk := range cur {
		if sk.ID == body.ID || sk.Dir == body.ID {
			_ = os.RemoveAll(filepath.Join(s.skillsRoot(), sk.Dir))
			removed = true
			continue
		}
		out = append(out, sk)
	}
	if !removed {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	if err := s.saveSkills(out); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": out})
}

// handleSkillsToggle enables or disables an installed skill.
func (s *Server) handleSkillsToggle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	cur := s.installedSkills()
	found := false
	for i := range cur {
		if cur[i].ID == body.ID || cur[i].Dir == body.ID {
			cur[i].Enabled = body.Enabled
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	if err := s.saveSkills(cur); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": cur})
}

// ghForSkills returns a GitHub client (token optional) for public-repo raw
// downloads during skill install.
func (s *Server) ghForSkills(userID string) *gitops.GHClient {
	return gitops.NewGHClient(s.githubToken(userID))
}

// installBuiltinSkill writes a bundled skill's files (SKILL.md + templates)
// into the skills directory. The directory name comes from the skill's Dir
// and must be a plain single component.
func (s *Server) installBuiltinSkill(b skills.Builtin) error {
	dir := b.Skill.Dir
	if dir == "" || dir != filepath.Base(dir) || strings.ContainsAny(dir, `/\`) {
		return os.ErrInvalid
	}
	dest := filepath.Join(s.skillsRoot(), dir)
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	for rel, content := range b.Files {
		full := filepath.Join(dest, filepath.FromSlash(rel))
		if !strings.HasPrefix(full, dest+string(filepath.Separator)) {
			return os.ErrInvalid
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
