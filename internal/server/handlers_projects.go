package server

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"v1/internal/gitops"
	"v1/internal/llm"
	"v1/internal/preview"
	"v1/internal/scaffold"
	"v1/internal/store"
)

type projectJSON struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	RepoURL               string `json:"repoUrl,omitempty"`
	PreviewCommand        string `json:"previewCommand,omitempty"`
	DefaultPreviewCommand string `json:"defaultPreviewCommand"`
	Instructions          string `json:"instructions,omitempty"`
	AutoPush              bool   `json:"autoPush"`
	PreviewDisabled       bool   `json:"previewDisabled"`
	VercelEnabled         bool   `json:"vercelEnabled"`
	GitHubTab             string `json:"githubTab"`
	Ephemeral             bool   `json:"ephemeral"`
	CreatedAt             int64  `json:"createdAt"`
	UpdatedAt             int64  `json:"updatedAt"`
}

func toProjectJSON(p *store.Project) projectJSON {
	return projectJSON{
		ID:                    p.ID,
		Name:                  p.Name,
		RepoURL:               p.RepoURL,
		PreviewCommand:        p.PreviewCommand,
		DefaultPreviewCommand: preview.DefaultPreviewCommand(p.Path),
		Instructions:          p.Instructions,
		AutoPush:              p.AutoPush,
		PreviewDisabled:       p.PreviewDisabled,
		VercelEnabled:         p.VercelEnabled,
		GitHubTab:             p.GitHubTab,
		Ephemeral:             p.Ephemeral,
		CreatedAt:             p.CreatedAt,
		UpdatedAt:             p.UpdatedAt,
	}
}

// handleUpdateProject rewrites the user-editable project settings.
func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Name            *string `json:"name"`
		PreviewCommand  *string `json:"previewCommand"`
		Instructions    *string `json:"instructions"`
		AutoPush        *bool   `json:"autoPush"`
		PreviewDisabled *bool   `json:"previewDisabled"`
		VercelEnabled   *bool   `json:"vercelEnabled"`
		// "auto", "on" or "off"; empty leaves it unchanged.
		GitHubTab *string `json:"githubTab"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// Text fields are only written when the request carries at least one, so a
	// toggle-only PATCH never wipes the custom instructions or preview command.
	if body.Name != nil || body.PreviewCommand != nil || body.Instructions != nil {
		name := p.Name
		if body.Name != nil {
			if trimmed := strings.TrimSpace(*body.Name); trimmed != "" {
				name = trimmed
			}
		}
		previewCommand := p.PreviewCommand
		if body.PreviewCommand != nil {
			previewCommand = strings.TrimSpace(*body.PreviewCommand)
		}
		instructions := p.Instructions
		if body.Instructions != nil {
			instructions = strings.TrimSpace(*body.Instructions)
		}
		if err := s.st.UpdateProjectSettings(p.ID, name, previewCommand, instructions); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.AutoPush != nil {
		if err := s.st.UpdateProjectAutoPush(p.ID, *body.AutoPush); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.PreviewDisabled != nil {
		if err := s.st.UpdateProjectPreviewDisabled(p.ID, *body.PreviewDisabled); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Stop any running preview so a disabled preview doesn't linger.
		if *body.PreviewDisabled {
			s.previews.Stop(p.ID)
		}
	}
	if body.VercelEnabled != nil {
		if err := s.st.UpdateProjectVercelEnabled(p.ID, *body.VercelEnabled); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.GitHubTab != nil {
		mode := strings.TrimSpace(*body.GitHubTab)
		switch mode {
		case "", "auto", "on", "off":
		default:
			writeError(w, http.StatusBadRequest, `githubTab must be "auto", "on" or "off"`)
			return
		}
		if mode == "auto" {
			mode = ""
		}
		if err := s.st.UpdateProjectGitHubTab(p.ID, mode); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	updated, err := s.st.GetProject(p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toProjectJSON(updated))
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	// Ephemeral projects disappear 24h after their last activity. Sweeping on
	// every list keeps the dashboard honest without a background job.
	_, _ = s.st.ArchiveExpiredEphemeralProjects(time.Now().Unix() - 24*60*60)
	var projects []*store.Project
	var err error
	if s.cfg.AuthDisabled {
		projects, err = s.st.ListProjects()
	} else {
		projects, err = s.st.ListProjectsByOwner(s.currentUser(r).ID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type previewInfo struct {
		Running bool    `json:"running"`
		URL     *string `json:"url"`
	}
	type item struct {
		ID        string      `json:"id"`
		Name      string      `json:"name"`
		RepoURL   string      `json:"repoUrl,omitempty"`
		Ephemeral bool        `json:"ephemeral"`
		Preview   previewInfo `json:"preview"`
		UpdatedAt int64       `json:"updatedAt"`
	}
	out := []item{}
	for _, p := range projects {
		running, url, _ := s.previews.Status(p.ID)
		out = append(out, item{
			ID:        p.ID,
			Name:      p.Name,
			RepoURL:   p.RepoURL,
			Ephemeral: p.Ephemeral,
			Preview:   previewInfo{Running: running, URL: url},
			UpdatedAt: p.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Template    string `json:"template"`
		Description string `json:"description"`
		Ephemeral   bool   `json:"ephemeral"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		// Let the LLM name it from the description, falling back to the
		// description's first line when the call fails or is unusable.
		name = s.llmProjectName(r.Context(), s.currentUser(r).ID, body.Description)
	}
	if name == "" {
		name = deriveName(body.Description)
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, "name or description is required")
		return
	}
	template := body.Template
	if template == "" {
		if body.Description != "" {
			template = "empty"
		} else {
			template = "vite-react"
		}
	}
	id := store.NewID()
	dir := filepath.Join(s.cfg.DataDir, "projects", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := scaffold.Apply(dir, template, name); err != nil {
		os.RemoveAll(dir)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Instructions are only what the user adds in the project settings — the
	// initial description lives as the first chat message, not the system
	// prompt.
	p := &store.Project{ID: id, Name: name, Path: dir, OwnerID: s.currentUser(r).ID, AutoPush: s.autoPushDefault(s.currentUser(r).ID), Ephemeral: body.Ephemeral}
	if err := s.st.CreateProject(p); err != nil {
		os.RemoveAll(dir)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toProjectJSON(p))
}

// llmProjectName asks the LLM for a short project name for the description.
// Any failure — no key, timeout, unusable reply — yields "" so the caller
// falls back to the description-based derivation.
func (s *Server) llmProjectName(ctx context.Context, userID, description string) string {
	if description == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	text, err := s.llmClient(userID).Complete(ctx, []llm.Message{
		{Role: "system", Content: "You name software projects. Reply with ONLY a short project name (3-40 characters). No quotes, no explanation, no markdown."},
		{Role: "user", Content: "Name this project: " + description},
	})
	if err != nil {
		return ""
	}
	// First line only, quotes/backticks stripped, length-capped like deriveName.
	line := text
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "\"'`"))
	r := []rune(line)
	if len(r) > 48 {
		line = string(r[:48])
	}
	return strings.TrimSpace(line)
}

// deriveName turns a "what do you want to create?" description into a short
// project name: the first line, capped at 48 runes.
func deriveName(description string) string {
	line := description
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	r := []rune(line)
	if len(r) > 48 {
		return string(r[:48]) + "…"
	}
	return line
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	// Detect the repository from the working tree's origin remote so the GitHub
	// tab can decide on its own. Never overwrite a user-linked URL.
	if p.RepoURL == "" {
		if remote := gitops.RemoteURL(p.Path); repoSlug(remote) != "" {
			if err := s.st.SetProjectRepoURL(p.ID, remote); err == nil {
				p.RepoURL = remote
			}
		}
	}
	writeJSON(w, http.StatusOK, toProjectJSON(p))
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	s.previews.Stop(p.ID)
	s.terminals.KillProject(p.ID)
	if err := s.st.DeleteProject(p.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.RemoveAll(p.Path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleImportProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RepoURL string `json:"repoUrl"`
		Name    string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	repoURL := strings.TrimSpace(body.RepoURL)
	if repoURL == "" {
		writeError(w, http.StatusBadRequest, "repoUrl is required")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = repoNameFromURL(repoURL)
	}
	id := store.NewID()
	dir := filepath.Join(s.cfg.DataDir, "projects", id)
	if err := gitops.Clone(r.Context(), repoURL, s.githubToken(s.currentUser(r).ID), dir); err != nil {
		os.RemoveAll(dir)
		writeError(w, http.StatusBadRequest, "clone failed: "+err.Error())
		return
	}
	p := &store.Project{ID: id, Name: name, Path: dir, RepoURL: repoURL, OwnerID: s.currentUser(r).ID}
	if err := s.st.CreateProject(p); err != nil {
		os.RemoveAll(dir)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Time-travel needs a repository: initialize one (with an initial commit)
	// right away — the git-activate endpoint is then only needed for repos
	// that want a GitHub remote or explicit control.
	go func() {
		_ = gitops.InitRepo(dir, s.githubLogin(s.currentUser(r).ID))
	}()
	writeJSON(w, http.StatusCreated, toProjectJSON(p))
}

func repoNameFromURL(repoURL string) string {
	repoURL = strings.TrimSuffix(strings.TrimRight(repoURL, "/"), ".git")
	if i := strings.LastIndex(repoURL, "/"); i >= 0 {
		repoURL = repoURL[i+1:]
	}
	if repoURL == "" {
		return "imported-project"
	}
	return repoURL
}

// ---- project files ----

var errPathTraversal = errors.New("path escapes the project directory")

// safeJoin resolves rel against root, rejecting paths that escape root.
func safeJoin(root, rel string) (string, error) {
	root = filepath.Clean(root)
	if rel == "" || rel == "." || rel == "/" {
		return root, nil
	}
	clean := filepath.Clean("/" + rel)
	full := filepath.Join(root, clean)
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", errPathTraversal
	}
	return full, nil
}

var skipListNames = map[string]bool{"node_modules": true, ".git": true, "dist": true}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
		Size int64  `json:"size"`
	}
	// ?recursive=true: every file in the project (for chat @-completion).
	if r.URL.Query().Get("recursive") == "true" {
		out := []entry{}
		root := p.Path
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != root && skipListNames[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			out = append(out, entry{Name: d.Name(), Path: filepath.ToSlash(rel), Type: "file"})
			if len(out) >= 5000 {
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot walk directory: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": out})
		return
	}
	rel := r.URL.Query().Get("path")
	full, err := safeJoin(p.Path, rel)
	if errors.Is(err, errPathTraversal) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dirents, err := os.ReadDir(full)
	if err != nil {
		writeError(w, http.StatusNotFound, "cannot read directory: "+err.Error())
		return
	}
	out := []entry{}
	base := strings.Trim(filepath.ToSlash(filepath.Clean("/"+rel)), "/")
	for _, de := range dirents {
		if skipListNames[de.Name()] {
			continue
		}
		e := entry{Name: de.Name(), Type: "file"}
		if base != "" {
			e.Path = base + "/" + de.Name()
		} else {
			e.Path = de.Name()
		}
		if de.IsDir() {
			e.Type = "dir"
		} else if info, err := de.Info(); err == nil {
			e.Size = info.Size()
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	full, err := safeJoin(p.Path, r.URL.Query().Get("path"))
	if errors.Is(err, errPathTraversal) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}
	if info.Size() > 1<<20 {
		writeError(w, http.StatusBadRequest, "file too large (>1MB)")
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if strings.Contains(string(data), "\x00") {
		writeError(w, http.StatusBadRequest, "binary file")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": string(data)})
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	full, err := safeJoin(p.Path, body.Path)
	if errors.Is(err, errPathTraversal) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(full, []byte(body.Content), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.previews.TouchRevision(p.ID)
	_ = s.st.TouchProject(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleActiveRuns reports which turns are running, for the dashboard's live
// "generating" indicator. It answers with the sessions as well as the projects,
// because a project with several sessions needs to show which one is running.
func (s *Server) handleActiveRuns(w http.ResponseWriter, r *http.Request) {
	runs := s.turns.activeRuns()
	projects := make([]string, 0, len(runs))
	sessions := make([]string, 0, len(runs))
	seen := make(map[string]bool, len(runs))
	for _, run := range runs {
		if !seen[run.ProjectID] {
			seen[run.ProjectID] = true
			projects = append(projects, run.ProjectID)
		}
		sessions = append(sessions, run.SessionID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": projects, "sessions": sessions})
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	full, err := safeJoin(p.Path, rel)
	if errors.Is(err, errPathTraversal) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if full == filepath.Clean(p.Path) {
		writeError(w, http.StatusBadRequest, "cannot delete the project root")
		return
	}
	if err := os.RemoveAll(full); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.previews.TouchRevision(p.ID)
	_ = s.st.TouchProject(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
