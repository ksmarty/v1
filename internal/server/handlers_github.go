package server

import (
	"net/http"
	"strings"

	"v1/internal/gitops"
)

// ghClient returns a GitHub client or writes a 400 error when no token is set.
func (s *Server) ghClient(w http.ResponseWriter, userID string) *gitops.GHClient {
	token := s.githubToken(userID)
	if token == "" {
		writeError(w, http.StatusBadRequest, "no GitHub token configured (set one in settings)")
		return nil
	}
	return gitops.NewGHClient(token)
}

func (s *Server) handleGitHubRepos(w http.ResponseWriter, r *http.Request) {
	c := s.ghClient(w, s.currentUser(r).ID)
	if c == nil {
		return
	}
	repos, err := c.ListRepos(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

func (s *Server) handleGitHubUser(w http.ResponseWriter, r *http.Request) {
	c := s.ghClient(w, s.currentUser(r).ID)
	if c == nil {
		return
	}
	user, err := c.GetUser(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleGitHubCreate(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	c := s.ghClient(w, s.currentUser(r).ID)
	if c == nil {
		return
	}
	var body struct {
		Name    string `json:"name"`
		Private bool   `json:"private"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	cloneURL, err := c.CreateRepo(r.Context(), body.Name, body.Private)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	login := ""
	if user, err := c.GetUser(r.Context()); err == nil {
		login = user.Login
	}
	committed, pushed, summary, err := gitops.InitAndPush(r.Context(), p.Path, cloneURL, c.Token, "Initial commit from v1", login)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	repoURL := cloneURL
	_ = s.st.SetProjectRepoURL(p.ID, repoURL)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"repoUrl":   repoURL,
		"committed": committed,
		"pushed":    pushed,
		"summary":   summary,
	})
}

func (s *Server) handleGitHubPush(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	c := s.ghClient(w, s.currentUser(r).ID)
	if c == nil {
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	login := ""
	if user, err := c.GetUser(r.Context()); err == nil {
		login = user.Login
	}
	committed, pushed, summary, err := gitops.CommitAndPush(r.Context(), p.Path, c.Token, body.Message, login)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"committed": committed,
		"pushed":    pushed,
		"summary":   summary,
	})
}

func (s *Server) handleGitHubLink(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		RepoURL string `json:"repoUrl"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	repoURL := strings.TrimSpace(body.RepoURL)
	if repoURL == "" {
		writeError(w, http.StatusBadRequest, "repoUrl is required")
		return
	}
	if err := gitops.LinkRepo(r.Context(), repoURL, s.githubToken(s.currentUser(r).ID), p.Path); err != nil {
		writeError(w, http.StatusBadGateway, "link failed: "+err.Error())
		return
	}
	if err := s.st.SetProjectRepoURL(p.ID, repoURL); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.previews.TouchRevision(p.ID)
	_ = s.st.TouchProject(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "repoUrl": repoURL})
}

func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	st := gitops.GetStatus(p.Path)
	resp := map[string]any{
		"isRepo":    st.IsRepo,
		"modified":  st.Modified,
		"untracked": st.Untracked,
	}
	if st.Branch != "" {
		resp["branch"] = st.Branch
	}
	if p.RepoURL != "" {
		resp["repoUrl"] = p.RepoURL
	}
	writeJSON(w, http.StatusOK, resp)
}
