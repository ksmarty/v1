package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// GitHubScope is one OAuth scope the user can request, as documented by
// GitHub. The list is fetched rather than hardcoded so it tracks GitHub's own
// docs.
type GitHubScope struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// githubScopesURL is the raw source of GitHub's "Scopes for OAuth apps" docs
// page — a markdown table of scope names and descriptions. It is the only
// machine-readable list GitHub publishes; there is no REST endpoint for it.
const githubScopesURL = "https://raw.githubusercontent.com/github/docs/main/content/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps.md"

// defaultGitHubScopes are the scopes v1 needs for its own features: `repo`
// (clone/push/create), `workflow` (commit .github/workflows files),
// `read:user` (identify the account) and `read:packages` (list ghcr.io
// images). Only the *default selection* is fixed; the catalog is fetched.
var defaultGitHubScopes = []string{"repo", "workflow", "read:user", "read:packages"}

// validScopeName guards the saved selection: GitHub scope names are letters,
// digits, colon, underscore and hyphen.
var validScopeName = regexp.MustCompile(`^[A-Za-z0-9:_-]+$`)

// scopeLineRe matches one table row: an optional bold/indent marker, a
// backticked scope name, then the description after the pipe.
var scopeLineRe = regexp.MustCompile("^\\s*(?:\\*\\*|&emsp;)?`([^`]+)`(?:\\*\\*)?\\s*\\|\\s*(.*)$")

var (
	liquidBranchRe = regexp.MustCompile(`(?s)\{%-?\s*(?:elsif|else)\b.*?%\}.*?\{%-?\s*endif\b.*?%\}`)
	liquidRe       = regexp.MustCompile(`\{[%{][^}]*[%}]\}`)
	dataVarRe      = regexp.MustCompile(`\{%-?\s*data\s+variables\.product\.prodname_([a-z0-9_]+)\s*-?%\}`)
	mdLinkRe       = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	autotitleRe    = regexp.MustCompile(`[^.]*AUTOTITLE[^.]*\.`)
	mdEmphasisRe   = regexp.MustCompile(`_([^_]+)_`)
)

// LoadGitHubScopes fetches the scope catalog in the background at startup so
// the picker is ready by the time it is opened. Failures are logged and
// retried lazily by the API, never fatal.
func (s *Server) LoadGitHubScopes(ctx context.Context) {
	go func() {
		fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		list, err := fetchGitHubScopes(fctx)
		if err != nil {
			log.Printf("github: fetching the OAuth scope catalog: %v", err)
			return
		}
		s.scopesMu.Lock()
		s.scopes = list
		s.scopesLoaded = true
		s.scopesMu.Unlock()
		log.Printf("github: loaded %d OAuth scopes", len(list))
	}()
}

// githubScopeCatalog returns the available scopes, fetching them on demand if
// the startup fetch has not succeeded. The current selection is always merged
// in so the picker can show what is chosen even when GitHub is unreachable.
func (s *Server) githubScopeCatalog(ctx context.Context) []GitHubScope {
	s.scopesMu.Lock()
	list := s.scopes
	loaded := s.scopesLoaded
	s.scopesMu.Unlock()
	if !loaded {
		fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if fetched, err := fetchGitHubScopes(fctx); err == nil {
			s.scopesMu.Lock()
			s.scopes = fetched
			s.scopesLoaded = true
			s.scopesMu.Unlock()
			list = fetched
		}
	}
	return mergeSelectedScopes(list, s.githubScopeSelection())
}

// mergeSelectedScopes appends any selected scope the catalog is missing, so a
// saved selection always has a row in the picker.
func mergeSelectedScopes(list []GitHubScope, selected []string) []GitHubScope {
	have := make(map[string]bool, len(list))
	for _, sc := range list {
		have[sc.Name] = true
	}
	out := append([]GitHubScope(nil), list...)
	for _, name := range selected {
		if !have[name] {
			out = append(out, GitHubScope{Name: name})
			have[name] = true
		}
	}
	return out
}

// githubScopeSelection is the saved scope list, or v1's defaults.
func (s *Server) githubScopeSelection() []string {
	if v, ok, _ := s.st.GetSetting(keyGitHubOAuthScopes); ok && strings.TrimSpace(v) != "" {
		return strings.Fields(v)
	}
	return append([]string(nil), defaultGitHubScopes...)
}

// githubOAuthScopeString is the space-separated scope sent to GitHub.
func (s *Server) githubOAuthScopeString() string {
	return strings.Join(s.githubScopeSelection(), " ")
}

// handleGitHubScopes serves the catalog and the current selection.
func (s *Server) handleGitHubScopes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"scopes":   s.githubScopeCatalog(r.Context()),
		"selected": s.githubScopeSelection(),
	})
}

// handleGitHubScopesSave stores the scopes the user picked. The values are
// validated by shape, not against the catalog, so a selection can be saved
// even if the docs fetch is unavailable.
func (s *Server) handleGitHubScopesSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scopes []string `json:"scopes"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	clean := make([]string, 0, len(body.Scopes))
	seen := map[string]bool{}
	for _, sc := range body.Scopes {
		sc = strings.TrimSpace(sc)
		if sc == "" || seen[sc] || !validScopeName.MatchString(sc) {
			continue
		}
		seen[sc] = true
		clean = append(clean, sc)
	}
	if len(clean) == 0 {
		writeError(w, http.StatusBadRequest, "select at least one scope")
		return
	}
	if err := s.st.SetSetting(keyGitHubOAuthScopes, strings.Join(clean, " ")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"selected": clean})
}

// fetchGitHubScopes downloads and parses GitHub's scope docs.
func fetchGitHubScopes(ctx context.Context) ([]GitHubScope, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubScopesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "v1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github scope docs returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	list := parseGitHubScopes(string(body))
	if len(list) == 0 {
		return nil, fmt.Errorf("no scopes found in the GitHub docs")
	}
	return list, nil
}

// parseGitHubScopes reads the "Available scopes" table out of the docs
// markdown.
func parseGitHubScopes(md string) []GitHubScope {
	var out []GitHubScope
	inSection := false
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "## Available scopes") {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		// The table is followed by a note block and then other sections.
		if strings.HasPrefix(line, ">") || strings.HasPrefix(line, "## ") {
			break
		}
		m := scopeLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" || name == "(no scope)" {
			continue
		}
		out = append(out, GitHubScope{Name: name, Description: cleanScopeDescription(m[2])})
	}
	return out
}

// productNames maps the docs' Liquid product variables to readable names.
var productNames = map[string]string{
	"actions":         "GitHub Actions",
	"registry":        "GitHub Packages",
	"code_scanning":   "code scanning",
	"secret_scanning": "secret scanning",
	"dependabot":      "Dependabot",
	"copilot":         "GitHub Copilot",
	"pages":           "GitHub Pages",
}

// cleanScopeDescription strips the docs' Liquid templating and markdown so the
// UI shows plain prose. Conditional branches (`{% elsif %}`/`{% else %}`) are
// dropped, keeping the primary branch, so the alternatives don't run together.
func cleanScopeDescription(s string) string {
	s = liquidBranchRe.ReplaceAllString(s, "")
	// Resolve the product-name variables the docs embed before the generic
	// Liquid strip would delete them (e.g. "code scanning API").
	s = dataVarRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := dataVarRe.FindStringSubmatch(m)
		if name, ok := productNames[parts[1]]; ok {
			return name
		}
		return strings.ReplaceAll(parts[1], "_", " ")
	})
	s = liquidRe.ReplaceAllString(s, "")
	s = mdLinkRe.ReplaceAllString(s, "$1")
	// The docs use `[AUTOTITLE](url)` as link text; drop the sentence that
	// carried it rather than leaving a dangling "see AUTOTITLE".
	s = autotitleRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "AUTOTITLE", "")
	s = strings.ReplaceAll(s, "<br/>", " ")
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "&emsp;", " ")
	s = strings.ReplaceAll(s, "`", "")
	s = mdEmphasisRe.ReplaceAllString(s, "$1")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}
