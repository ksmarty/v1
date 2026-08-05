package server

import (
	"context"
	"net/http"
	"strings"

	"v1/internal/gitops"
)

// githubLogin returns the authenticated GitHub login for commit authorship,
// or "" when no token is configured (commits then use the v1 identity).
func (s *Server) githubLogin() string {
	tok := s.githubToken()
	if tok == "" {
		return ""
	}
	user, err := gitops.NewGHClient(tok).GetUser(context.Background())
	if err != nil {
		return ""
	}
	return user.Login
}

// handleGitInfo reports whether the project is a git repo, and if so its
// current branch, branch list and commit history.
func (s *Server) handleGitInfo(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	resp := map[string]any{"isRepo": false}
	branch, branches, err := gitops.Branches(p.Path)
	if err == nil {
		history, herr := gitops.History(p.Path)
		if herr != nil {
			writeError(w, http.StatusInternalServerError, herr.Error())
			return
		}
		resp["isRepo"] = true
		resp["branch"] = branch
		resp["branches"] = branches
		resp["commits"] = history
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGitInit turns the project directory into a git repository (with an
// initial commit) so time-travel and per-turn auto-commits can work.
func (s *Server) handleGitInit(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	login := s.githubLogin()
	if err := gitops.InitRepo(p.Path, login); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.previews.TouchRevision(p.ID)
	_ = s.st.TouchProject(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleGitBranch creates and checks out a new branch.
func (s *Server) handleGitBranch(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := gitops.CreateBranch(p.Path, name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.previews.TouchRevision(p.ID)
	_ = s.st.TouchProject(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleGitCheckout switches the project to an existing branch.
func (s *Server) handleGitCheckout(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Branch string `json:"branch"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Branch == "" {
		writeError(w, http.StatusBadRequest, "branch is required")
		return
	}
	if err := gitops.CheckoutBranch(p.Path, body.Branch); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.previews.TouchRevision(p.ID)
	_ = s.st.TouchProject(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleGitRevert hard-resets the project to a past commit, dropping every
// change made after it.
func (s *Server) handleGitRevert(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Commit string `json:"commit"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Commit == "" {
		writeError(w, http.StatusBadRequest, "commit is required")
		return
	}
	if err := gitops.RevertTo(p.Path, body.Commit); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.previews.TouchRevision(p.ID)
	_ = s.st.TouchProject(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
