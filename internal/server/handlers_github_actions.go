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
// Every page of the (paginated) tag list is walked and the 15 newest tags are
// returned, newest first, plus the total tag count.
func (s *Server) handleGitHubImages(w http.ResponseWriter, r *http.Request) {
	owner, repo := githubOwnerRepo(r.URL.Query().Get("repo"))
	if owner == "" || repo == "" {
		writeError(w, http.StatusBadRequest, "missing or invalid repo (expected owner/name)")
		return
	}
	imageName := strings.ToLower(repo)
	list, err := registryTags(r.Context(), "https://ghcr.io", owner+"/"+imageName, s.githubToken(s.currentUser(r).ID))
	if err != nil {
		writeError(w, http.StatusBadGateway, "ghcr.io "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"owner":     owner,
		"repo":      repo,
		"count":     1,
		"limit":     maxRegistryTags,
		"totalTags": list.Total,
		"images": []map[string]any{{
			"name":       imageName,
			"visibility": "public",
			"url":        "https://github.com/" + owner + "/" + imageName + "/pkgs/container/" + imageName,
			"created_at": "",
			"updated_at": "",
			"full":       "ghcr.io/" + owner + "/" + imageName,
			"tags":       list.Newest,
		}},
	})
}

const (
	// maxRegistryTags is how many newest tags the UI shows.
	maxRegistryTags = 15
	// maxRegistryTagPages bounds the walk of a registry's paginated tag list
	// (100 tags per page at ghcr.io, so this covers 100k tags).
	maxRegistryTagPages = 1000
)

// registryTagList is the result of listing a published image's tags: Newest
// holds the maxRegistryTags most recent ones (newest first), Total is how many
// tags the registry actually has, before the cap.
type registryTagList struct {
	Newest []string
	Total  int
}

// registryTags lists the most recent tags for image on the Docker Registry
// HTTP API v2 (registry/v2/<image>/tags/list). For ghcr.io, anonymous pulls
// work for public images; when the repo is private the registry returns a
// bearer challenge which we satisfy with the configured GitHub token (ghcr
// accepts PATs/OAuth tokens via the token endpoint).
//
// The registry paginates the tag list (100 tags per page, newest page linked
// via the "Link: <...>; rel=next" header) and orders each page
// alphabetically, NOT by recency. Reading only the first page therefore
// returns the alphabetically smallest tags — for v1, v0.0.1..v0.56.4 while
// the image is at v0.11x — so the UI showed "some versions from the middle"
// instead of the newest. Every page is walked, then all tags are sorted
// semver-descending and capped at maxRegistryTags.
func registryTags(ctx context.Context, registry, image, token string) (registryTagList, error) {
	var out registryTagList
	pageURL := registry + "/v2/" + image + "/tags/list"
	bearer := ""
	seenPages := map[string]bool{}
	all := make([]string, 0, 256)
	for page := 0; pageURL != ""; page++ {
		if page >= maxRegistryTagPages || seenPages[pageURL] {
			break
		}
		seenPages[pageURL] = true
		tags, next, tok, err := registryTagsPage(ctx, pageURL, bearer, registry, token)
		if err != nil {
			return out, err
		}
		bearer = tok
		all = append(all, tags...)
		if next == "" {
			break
		}
		if strings.HasPrefix(next, "http://") || strings.HasPrefix(next, "https://") {
			pageURL = next
		} else if strings.HasPrefix(next, "/") {
			pageURL = registry + next
		} else {
			pageURL = registry + "/" + next
		}
	}
	// Sort ALL tags semver-descending, THEN cap — the registry's own order is
	// alphabetical per page and meaningless across pages.
	tags := make([]string, 0, len(all))
	for _, t := range all {
		if t == "" || t == "latest" {
			continue
		}
		tags = append(tags, t)
	}
	out.Total = len(tags)
	sortTags(tags)
	if len(tags) > maxRegistryTags {
		tags = tags[:maxRegistryTags]
	}
	out.Newest = tags
	return out, nil
}

// registryTagsPage fetches one page of a tag list, transparently completing
// the bearer-token handshake on a 401. It returns the page's tags, the next
// page URL from the Link header ("" on the last page) and the bearer token it
// used, so a multi-page walk authenticates once.
func registryTagsPage(ctx context.Context, pageURL, bearer, registry, token string) (tags []string, next, usedToken string, err error) {
	do := func(auth string) (*http.Response, error) {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if rerr != nil {
			return nil, rerr
		}
		req.Header.Set("Accept", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		return registryClient.Do(req)
	}
	resp, err := do(bearer)
	if err != nil {
		return nil, "", bearer, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// Bearer token flow: ghcr.io's WWW-Authenticate gives the token
		// realm/service/scope. Fetch a token (optionally using the GitHub
		// token as the username) and retry the same page with it.
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		tok, terr := fetchRegistryToken(ctx, registry, challenge, token)
		if terr != nil {
			return nil, "", bearer, terr
		}
		bearer = tok
		resp, err = do(bearer)
		if err != nil {
			return nil, "", bearer, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, "", bearer, fmt.Errorf("tags list (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, "", bearer, err
	}
	return body.Tags, nextTagPage(resp.Header.Get("Link")), bearer, nil
}

// nextTagPage extracts the rel="next" URL from a registry Link header, e.g.
// `</v2/owner/image/tags/list?last=v0.56.4&n=0>; rel="next"`.
func nextTagPage(link string) string {
	for _, part := range strings.Split(link, ",") {
		segs := strings.Split(part, ";")
		if len(segs) < 2 {
			continue
		}
		isNext := false
		for _, attr := range segs[1:] {
			attr = strings.TrimSpace(attr)
			if attr == `rel="next"` || attr == "rel=next" {
				isNext = true
			}
		}
		if !isNext {
			continue
		}
		u := strings.TrimSpace(segs[0])
		if start := strings.IndexByte(u, '<'); start >= 0 {
			if end := strings.IndexByte(u[start:], '>'); end > 0 {
				return strings.TrimSpace(u[start+1 : start+end])
			}
		}
	}
	return ""
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

// sortTags orders semver-like tags descending (v0.0.17 before v0.0.10), so
// the first N are the most recent. Handles any number of numeric parts and
// treats pre-release suffixes (v1.2.0-beta) as older than the release.
func sortTags(tags []string) {
	sort.SliceStable(tags, func(i, j int) bool {
		ai, apre := tagParts(tags[i])
		bi, bpre := tagParts(tags[j])
		n := min(len(ai), len(bi))
		for k := 0; k < n; k++ {
			if ai[k] != bi[k] {
				return ai[k] > bi[k]
			}
		}
		if len(ai) != len(bi) {
			return len(ai) > len(bi)
		}
		// Same core version: a pre-release is older than the final release.
		if apre != bpre {
			return bpre != ""
		}
		return tags[i] > tags[j]
	})
}

// tagParts splits a tag like "v1.2.3-beta" into its numeric parts and the
// pre-release suffix ("" for a final release).
func tagParts(t string) (nums []int, pre string) {
	t = strings.TrimPrefix(strings.TrimPrefix(t, "v"), "v")
	if i := strings.IndexByte(t, '-'); i >= 0 {
		pre = t[i+1:]
		t = t[:i]
	}
	for _, s := range strings.Split(t, ".") {
		n, err := strconv.Atoi(s)
		if err != nil {
			break
		}
		nums = append(nums, n)
	}
	return nums, pre
}

var registryClient = &http.Client{Timeout: 30 * time.Second}
