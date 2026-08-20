package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

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
// (owner/name) on ghcr.io, with its most recent tag versions. Tags come from
// the Docker Registry HTTP API v2 (ghcr.io/v2/<owner>/<name>/tags/list),
// which works anonymously for public images — no GitHub token or
// read:packages scope needed. Auth still applies to private repos via the
// configured GitHub token through the standard registry bearer-token flow.
// A fixed 15 most-recent tags are returned.
func (s *Server) handleGitHubImages(w http.ResponseWriter, r *http.Request) {
	owner, repo := githubOwnerRepo(r.URL.Query().Get("repo"))
	if owner == "" || repo == "" {
		writeError(w, http.StatusBadRequest, "missing or invalid repo (expected owner/name)")
		return
	}
	imageName := strings.ToLower(repo)
	tags, err := registryTags(r.Context(), "https://ghcr.io", owner+"/"+imageName, s.githubToken(s.currentUser(r).ID))
	if err != nil {
		writeError(w, http.StatusBadGateway, "ghcr.io "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"owner":     owner,
		"count":     1,
		"limit":     15,
		"totalTags": len(tags),
		"images": []map[string]any{{
			"name":       imageName,
			"visibility": "public",
			"url":        "https://github.com/" + owner + "/" + imageName + "/pkgs/container/" + imageName,
			"created_at": "",
			"updated_at": "",
			"full":       "ghcr.io/" + owner + "/" + imageName,
			"tags":       tags,
		}},
	})
}

const maxRegistryTags = 15

// registryTags lists the most recent tags for image on the Docker Registry
// HTTP API v2 (registry/v2/<image>/tags/list). For ghcr.io, anonymous pulls
// work for public images; when the repo is private the registry returns a
// bearer challenge which we satisfy with the configured GitHub token (ghcr
// accepts PATs/OAuth tokens via the token endpoint). Returns newest first,
// capped at maxRegistryTags.
func registryTags(ctx context.Context, registry, image, token string) ([]string, error) {
	tagsURL := registry + "/v2/" + image + "/tags/list"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tagsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.list.v2+json")
	resp, err := registryClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// Bearer token flow: ghcr.io's WWW-Authenticate gives the token
		// realm/service/scope. Fetch a token (optionally using the GitHub
		// token as the username) and retry the tags list with it.
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		tok, terr := fetchRegistryToken(ctx, registry, challenge, token)
		if terr != nil {
			return nil, terr
		}
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, tagsURL, nil)
		req2.Header.Set("Authorization", "Bearer "+tok)
		req2.Header.Set("Accept", "application/vnd.docker.distribution.manifest.list.v2+json")
		resp, err = registryClient.Do(req2)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("tags list (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]string, 0, min(len(body.Tags), maxRegistryTags))
	for _, t := range body.Tags {
		if t == "" || t == "latest" {
			continue
		}
		out = append(out, t)
		if len(out) >= maxRegistryTags {
			break
		}
	}
	// Registry tags are unordered; sort descending (v0.0.17 before v0.0.10)
	// and take the first 15 as "most recent".
	sortTags(out)
	return out, nil
}

// fetchRegistryToken performs the Docker registry bearer-token flow. When a
// GitHub token is configured it is used as the basic-auth «oauth2:token»
// credential, which lets private repos be listed too.
func fetchRegistryToken(ctx context.Context, registry, challenge, token string) (string, error) {
	// Parse the WWW-Authenticate: Bearer realm=...,service=...,scope=... header.
	realm, service, scope := "", "", ""
	for _, part := range strings.Split(challenge, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "realm=") {
			realm = strings.Trim(strings.TrimSpace(part[len("realm="):]), `"`)
		} else if strings.HasPrefix(part, "service=") {
			service = strings.Trim(strings.TrimSpace(part[len("service="):]), `"`)
		} else if strings.HasPrefix(part, "scope=") {
			scope = strings.Trim(strings.TrimSpace(part[len("scope="):]), `"`)
		}
	}
	if realm == "" {
		realm = "https://" + strings.TrimPrefix(registry, "https://") + "/token"
	}
	u := realm + "?service=" + url.QueryEscape(service) + "&scope=" + url.QueryEscape(scope)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if token != "" {
		req.SetBasicAuth("oauth2", token)
	}
	resp, err := registryClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("token endpoint (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.Token == "" {
		return "", fmt.Errorf("empty token from registry")
	}
	return body.Token, nil
}

// sortTags orders semver-like tags descending (v0.0.17 before v0.0.10, then
// lexical descending as a tiebreak), so the first N are the most recent.
func sortTags(tags []string) {
	sort.SliceStable(tags, func(i, j int) bool {
		ai, bi := tagParts(tags[i]), tagParts(tags[j])
		for k := 0; k < 3; k++ {
			if ai[k] != bi[k] {
				return ai[k] > bi[k]
			}
		}
		return tags[i] > tags[j]
	})
}

func tagParts(t string) [3]int {
	var out [3]int
	t = strings.TrimPrefix(strings.TrimPrefix(t, "v"), "v")
	nums := strings.Split(strings.Split(t, "-")[0], ".")
	for i := 0; i < 3 && i < len(nums); i++ {
		n, err := strconv.Atoi(nums[i])
		if err == nil {
			out[i] = n
		}
	}
	return out
}

var registryClient = &http.Client{Timeout: 30 * time.Second}
