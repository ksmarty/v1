package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"v1/internal/auth"
	"v1/internal/vercel"
)

// Vercel OAuth authorization-code flow. Flow state lives in memory only,
// keyed by the random state value (same pattern as the OIDC flow).

type vercelFlow struct {
	expiresAt time.Time
}

const vercelFlowTTL = 10 * time.Minute

// vercelRedirectURI resolves the OAuth callback URI: the configured
// V1_VERCEL_REDIRECT_URI wins, otherwise it is derived from the request
// (X-Forwarded-Proto when present, else the TLS state).
func (s *Server) vercelRedirectURI(r *http.Request) string {
	if s.cfg.VercelRedirectURI != "" {
		return s.cfg.VercelRedirectURI
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/auth/vercel/oauth/callback"
}

func (s *Server) pruneVercelFlows() {
	now := time.Now()
	s.vercelMu.Lock()
	for state, f := range s.vercelFlows {
		if now.After(f.expiresAt) {
			delete(s.vercelFlows, state)
		}
	}
	s.vercelMu.Unlock()
}

// handleVercelOAuthStart begins the flow: stores a random state and redirects
// the browser to Vercel's authorization endpoint. Both the start and callback
// live under /api/auth/ so they are exempt from the auth middleware.
func (s *Server) handleVercelOAuthStart(w http.ResponseWriter, r *http.Request) {
	clientID, clientSecret := s.vercelOAuthClientID(), s.vercelOAuthClientSecret()
	if clientID == "" || clientSecret == "" {
		writeError(w, http.StatusBadRequest, "Vercel OAuth client id and secret are not configured (add them in Settings)")
		return
	}
	state, err := auth.RandomHex(32)
	if err != nil {
		log.Printf("vercel: generating state: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.pruneVercelFlows()
	s.vercelMu.Lock()
	s.vercelFlows[state] = &vercelFlow{expiresAt: time.Now().Add(vercelFlowTTL)}
	s.vercelMu.Unlock()
	http.Redirect(w, r, vercel.AuthorizeURL(clientID, s.vercelRedirectURI(r), state), http.StatusFound)
}

// handleVercelOAuthCallback exchanges the authorization code for a token and
// stores it. Failures redirect to /settings with a marker instead of showing
// an error page.
func (s *Server) handleVercelOAuthCallback(w http.ResponseWriter, r *http.Request) {
	fail := func() {
		http.Redirect(w, r, "/settings?page=vercel&vercel=error", http.StatusFound)
	}
	q := r.URL.Query()
	if q.Get("error") != "" || q.Get("state") == "" || q.Get("code") == "" {
		fail()
		return
	}
	state := q.Get("state")
	s.vercelMu.Lock()
	flow, ok := s.vercelFlows[state]
	if ok && time.Now().After(flow.expiresAt) {
		delete(s.vercelFlows, state)
		ok = false
	}
	if ok {
		delete(s.vercelFlows, state)
	}
	s.vercelMu.Unlock()
	if !ok {
		fail()
		return
	}

	clientID, clientSecret := s.vercelOAuthClientID(), s.vercelOAuthClientSecret()
	tok, err := vercel.ExchangeCode(r.Context(), clientID, clientSecret, q.Get("code"), s.vercelRedirectURI(r))
	if err != nil {
		log.Printf("vercel: exchanging code: %v", err)
		fail()
		return
	}
	if err := s.st.SetSetting(keyVercelToken, tok.AccessToken); err != nil {
		log.Printf("vercel: storing token: %v", err)
		fail()
		return
	}
	if tok.RefreshToken != "" {
		if err := s.st.SetSetting(keyVercelRefreshToken, tok.RefreshToken); err != nil {
			log.Printf("vercel: storing refresh token: %v", err)
		}
	}
	if err := s.st.SetSetting(keyVercelTokenSource, "oauth"); err != nil {
		log.Printf("vercel: storing token source: %v", err)
	}
	http.Redirect(w, r, "/settings?page=vercel", http.StatusFound)
}

// handleVercelUser reports whether a token is configured and, when possible,
// the connected Vercel username.
func (s *Server) handleVercelUser(w http.ResponseWriter, r *http.Request) {
	if s.vercelToken() == "" {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	login, err := s.vercelClient().User(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "login": login})
}

// deployState tracks one in-flight (or finished) deploy per project.
type deployState struct {
	mu         sync.Mutex
	startedAt  time.Time
	done       bool
	deployment *vercel.Deployment
	err        string
}

// maxDeployBytes caps the base64'd upload (Vercel's deployment API body limit
// is ~50 MB; base64 inflates 33%, so 35 MB of raw files stays under it).
const maxDeployBytes = 35 << 20

var deploySkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".vercel":      true,
	".next":        true,
	".cache":       true,
}

var errDeployTooLarge = errors.New("project too large for a direct deploy")

// collectProjectFiles walks the project directory, skipping build/dependency
// dirs, and base64-ready byte slices with a total size cap.
func collectProjectFiles(root string) ([]vercel.DeployFile, error) {
	var files []vercel.DeployFile
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && deploySkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > maxDeployBytes {
			return errDeployTooLarge
		}
		files = append(files, vercel.DeployFile{File: filepath.ToSlash(rel), Data: data})
		return nil
	})
	if errors.Is(err, errDeployTooLarge) {
		return nil, errDeployTooLarge
	}
	return files, err
}

var deployNameRe = regexp.MustCompile(`[^a-z0-9._-]+`)

// slugify turns a project name into a valid Vercel project name.
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = deployNameRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "v1-project"
	}
	return s
}

// handleVercelDeploy starts a deploy of the project's files in the background
// and returns immediately; status is polled via handleVercelDeployments.
func (s *Server) handleVercelDeploy(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	if s.vercelToken() == "" {
		writeError(w, http.StatusBadRequest, "no Vercel token configured (connect Vercel in Settings)")
		return
	}
	var body struct {
		Target string `json:"target"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Target != "" && body.Target != "production" {
		writeError(w, http.StatusBadRequest, `target must be "production" or omitted`)
		return
	}
	files, err := collectProjectFiles(p.Path)
	if err != nil {
		if errors.Is(err, errDeployTooLarge) {
			writeError(w, http.StatusBadRequest, "project is too large for a direct Vercel deploy (35 MB cap — exclude build artifacts and retry)")
			return
		}
		writeError(w, http.StatusInternalServerError, "reading project files: "+err.Error())
		return
	}

	state := &deployState{startedAt: time.Now()}
	s.vercelMu.Lock()
	s.vercelDeploys[p.ID] = state
	s.vercelMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		dep, derr := s.vercelClient().Deploy(ctx, slugify(p.Name), files, body.Target)
		state.mu.Lock()
		state.done = true
		if derr != nil {
			state.err = derr.Error()
		} else {
			state.deployment = dep
		}
		state.mu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// handleVercelDeployments reports the active deploy (in-memory) plus the
// recent deployments from the Vercel API.
func (s *Server) handleVercelDeployments(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	s.vercelMu.Lock()
	st := s.vercelDeploys[p.ID]
	s.vercelMu.Unlock()

	var active any
	if st != nil {
		st.mu.Lock()
		switch {
		case !st.done:
			active = map[string]any{"state": "BUILDING", "startedAt": st.startedAt}
		case st.err != "":
			active = map[string]any{"state": "ERROR", "error": st.err}
		default:
			active = st.deployment
		}
		st.mu.Unlock()
	}

	recent := []vercel.Deployment{}
	if s.vercelToken() != "" {
		deps, err := s.vercelClient().ListDeployments(r.Context(), slugify(p.Name), 10)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"connected": true,
				"active":    active,
				"recent":    recent,
				"error":     err.Error(),
			})
			return
		}
		recent = deps
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"connected": s.vercelToken() != "",
		"active":    active,
		"recent":    recent,
	})
}
