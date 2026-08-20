package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"v1/internal/gitops"
)

// ghErrBody extracts GitHub's "message" from an error body so the UI can show
// something readable (rate limit, not found, permissions) instead of raw JSON.
func ghErrBody(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return e.Message
	}
	return truncateStr(string(body), 200)
}

// githubOwnerRepo splits "owner/name" into its two parts.
func githubOwnerRepo(s string) (owner, repo string) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "git@")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.Index(s, "github.com/"); i >= 0 {
		s = s[i+len("github.com/"):]
	}
	parts := strings.Split(s, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2], parts[len(parts)-1]
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return "", ""
}

// githubActionsClient returns a GitHub client using the caller's token if one
// is configured, otherwise an anonymous client (works for public repos).
func (s *Server) githubActionsClient(userID string) *gitops.GHClient {
	return gitops.NewGHClient(s.githubToken(userID))
}

// handleGitHubWorkflows lists recent GitHub Actions workflow runs for a repo.
// Works for any public repo; private repos require a configured token.
func (s *Server) handleGitHubWorkflows(w http.ResponseWriter, r *http.Request) {
	c := s.githubActionsClient(s.currentUser(r).ID)
	owner, repo := githubOwnerRepo(r.URL.Query().Get("repo"))
	if owner == "" || repo == "" {
		writeError(w, http.StatusBadRequest, "missing or invalid repo (expected owner/name)")
		return
	}
	u := "https://api.github.com/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/actions/runs?per_page=20"
	status, body, err := c.Request(r.Context(), "GET", u, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if status >= 400 {
		writeError(w, status, "GitHub API "+strconv.Itoa(status)+" for "+owner+"/"+repo+": "+ghErrBody(body))
		return
	}
	var resp struct {
		TotalCount int `json:"total_count"`
		Runs       []struct {
			ID           int64  `json:"id"`
			Name         string `json:"name"`
			DisplayTitle string `json:"display_title"`
			HeadBranch   string `json:"head_branch"`
			Event        string `json:"event"`
			Status       string `json:"status"`
			Conclusion   string `json:"conclusion"`
			CreatedAt    string `json:"created_at"`
			HTMLURL      string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		writeError(w, http.StatusInternalServerError, "bad response: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"owner":     owner,
		"repo":      repo,
		"count":     resp.TotalCount,
		"workflows": resp.Runs,
	})
}

// handleGitHubImages lists the container image published for a single repo
// (owner/name) on ghcr.io, with its most recent tag versions. GitHub's
// packages API requires authentication (anonymous requests 404 even for
// public packages), and the token needs the read:packages scope — the OAuth
// device flow requests it. An optional `limit` query param (default 10, 0 =
// unlimited) trims the tags to the N most recent.
func (s *Server) handleGitHubImages(w http.ResponseWriter, r *http.Request) {
	c := s.githubActionsClient(s.currentUser(r).ID)
	owner, repo := githubOwnerRepo(r.URL.Query().Get("repo"))
	if owner == "" || repo == "" {
		writeError(w, http.StatusBadRequest, "missing or invalid repo (expected owner/name)")
		return
	}
	limit := 10
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			limit = n
		}
	}
	imageName := strings.ToLower(repo)
	var status int
	var body []byte
	var err error
	// Try user packages first, then org packages.
	for _, kind := range []string{"users", "orgs"} {
		u := "https://api.github.com/" + kind + "/" + url.PathEscape(owner) + "/packages/container/" + url.PathEscape(imageName)
		status, body, err = c.Request(r.Context(), "GET", u, nil)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if status < 400 {
			break
		}
	}
	if status >= 400 {
		writeError(w, status, "GitHub API "+strconv.Itoa(status)+" for "+owner+"/"+repo+": "+ghErrBody(body))
		return
	}
	var pkg struct {
		Name       string `json:"name"`
		Visibility string `json:"visibility"`
		HTMLURL    string `json:"html_url"`
		CreatedAt  string `json:"created_at"`
		UpdatedAt  string `json:"updated_at"`
	}
	if err := json.Unmarshal(body, &pkg); err != nil {
		writeError(w, http.StatusInternalServerError, "bad response: "+err.Error())
		return
	}
	// Fetch the versions (tags) for this package. GitHub returns them newest
	// first, so the first `limit` unique tags are the most recent.
	var tags []string
	allSeen := map[string]bool{}
	vu := "https://api.github.com/" + strings.ToLower(owner) + "/packages/container/" + url.PathEscape(imageName) + "/versions?per_page=100"
	vstatus, vbody, verr := c.Request(r.Context(), "GET", vu, nil)
	if verr == nil && vstatus < 400 {
		var versions []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Metadata struct {
				Container struct {
					Tags []string `json:"tags"`
				} `json:"container"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(vbody, &versions); err == nil {
			for _, v := range versions {
				for _, t := range v.Metadata.Container.Tags {
					if t == "" || allSeen[t] {
						continue
					}
					allSeen[t] = true
					if limit <= 0 || len(tags) < limit {
						tags = append(tags, t)
					}
				}
			}
		}
	}
	// Keep the newest-first order (drop the old alphabetical sort).
	writeJSON(w, http.StatusOK, map[string]any{
		"owner":     owner,
		"count":     1,
		"limit":     limit,
		"totalTags": len(allSeen),
		"images": []map[string]any{{
			"name":       pkg.Name,
			"visibility": pkg.Visibility,
			"url":        pkg.HTMLURL,
			"created_at": pkg.CreatedAt,
			"updated_at": pkg.UpdatedAt,
			"full":       "ghcr.io/" + owner + "/" + pkg.Name,
			"tags":       tags,
		}},
	})
}
