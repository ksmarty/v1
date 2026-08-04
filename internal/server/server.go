// Package server wires together the v1 HTTP API, preview proxy, terminal
// and embedded SPA static serving.
package server

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"v1/internal/auth"
	"v1/internal/config"
	"v1/internal/llm"
	"v1/internal/preview"
	"v1/internal/store"
	"v1/internal/terminal"
)

//go:embed all:dist
var distFS embed.FS

// Server is the v1 HTTP server.
type Server struct {
	cfg       config.Config
	st        *store.Store
	auth      *auth.Manager
	previews  *preview.Manager
	terminals *terminal.Manager
	handler   http.Handler

	oauthMu    sync.Mutex
	oauthFlows map[string]*oauthFlow
}

// New builds the server, its routes and middleware.
func New(cfg config.Config, st *store.Store) *Server {
	s := &Server{
		cfg:        cfg,
		st:         st,
		auth:       auth.NewManager(st, cfg.AuthDisabled, cfg.Password),
		previews:   preview.NewManager(cfg.MaxPreviews),
		terminals:  terminal.NewManager(),
		oauthFlows: map[string]*oauthFlow{},
	}
	s.auth.EnsureEnvPassword()
	mux := http.NewServeMux()
	s.routes(mux)
	s.handler = s.auth.Middleware(mux)
	return s
}

// Handler returns the root HTTP handler (with auth middleware applied).
func (s *Server) Handler() http.Handler { return s.handler }

// Shutdown stops all previews and terminals.
func (s *Server) Shutdown() {
	s.previews.StopAll()
	s.terminals.KillAll()
}

func (s *Server) routes(m *http.ServeMux) {
	m.HandleFunc("GET /api/healthz", s.handleHealth)

	m.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/auth/setup", s.handleSetup)
	m.HandleFunc("POST /api/auth/logout", s.handleLogout)

	m.HandleFunc("GET /api/settings", s.handleGetSettings)
	m.HandleFunc("PUT /api/settings", s.handlePutSettings)
	m.HandleFunc("POST /api/settings/test-llm", s.handleTestLLM)

	m.HandleFunc("GET /api/providers", s.handleListProviders)
	m.HandleFunc("POST /api/providers/refresh", s.handleRefreshProviders)

	m.HandleFunc("GET /api/projects", s.handleListProjects)
	m.HandleFunc("POST /api/projects", s.handleCreateProject)
	m.HandleFunc("POST /api/projects/import", s.handleImportProject)
	m.HandleFunc("GET /api/projects/{id}", s.handleGetProject)
	m.HandleFunc("DELETE /api/projects/{id}", s.handleDeleteProject)

	m.HandleFunc("GET /api/projects/{id}/files", s.handleListFiles)
	m.HandleFunc("GET /api/projects/{id}/file", s.handleReadFile)
	m.HandleFunc("PUT /api/projects/{id}/file", s.handleWriteFile)
	m.HandleFunc("DELETE /api/projects/{id}/file", s.handleDeleteFile)

	m.HandleFunc("GET /api/projects/{id}/messages", s.handleListMessages)
	m.HandleFunc("POST /api/projects/{id}/chat", s.handleChat)

	m.HandleFunc("GET /api/projects/{id}/preview/status", s.handlePreviewStatus)
	m.HandleFunc("POST /api/projects/{id}/preview/start", s.handlePreviewStart)
	m.HandleFunc("POST /api/projects/{id}/preview/stop", s.handlePreviewStop)

	m.HandleFunc("GET /api/projects/{id}/terminal", s.handleTerminal)

	m.HandleFunc("GET /api/github/repos", s.handleGitHubRepos)
	m.HandleFunc("GET /api/github/user", s.handleGitHubUser)
	m.HandleFunc("POST /api/github/oauth/device/start", s.handleOAuthDeviceStart)
	m.HandleFunc("POST /api/github/oauth/device/poll", s.handleOAuthDevicePoll)
	m.HandleFunc("POST /api/projects/{id}/github/create", s.handleGitHubCreate)
	m.HandleFunc("POST /api/projects/{id}/github/push", s.handleGitHubPush)
	m.HandleFunc("GET /api/projects/{id}/git/status", s.handleGitStatus)

	// The preview proxy handles all common HTTP methods (incl. WS upgrades
	// via GET). Methods are enumerated so the patterns don't conflict with
	// the "GET /" SPA catch-all.
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		m.HandleFunc(method+" /preview/{id}/", s.handlePreviewProxy)
	}

	m.HandleFunc("GET /", s.handleSPA)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) projectOr404(w http.ResponseWriter, r *http.Request) *store.Project {
	p, err := s.st.GetProject(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return nil
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	return p
}

// Settings keys stored in the settings table. Values set via the API take
// precedence over environment fallbacks.
const (
	keyLLMBaseURL          = "llm_base_url"
	keyLLMAPIKey           = "llm_api_key"
	keyLLMModel            = "llm_model"
	keyGitHubToken         = "github_token"
	keyGitHubTokenSource   = "github_token_source"
	keyGitHubOAuthClientID = "github_oauth_client_id"
	keyProvidersCache      = "providers_cache"
)

// llmConfig resolves the effective LLM settings (sqlite overrides env).
func (s *Server) llmConfig() (baseURL, apiKey, model string) {
	baseURL, apiKey, model = s.cfg.OpenAIBase, s.cfg.OpenAIKey, s.cfg.Model
	if v, ok, _ := s.st.GetSetting(keyLLMBaseURL); ok && v != "" {
		baseURL = v
	}
	if v, ok, _ := s.st.GetSetting(keyLLMAPIKey); ok && v != "" {
		apiKey = v
	}
	if v, ok, _ := s.st.GetSetting(keyLLMModel); ok && v != "" {
		model = v
	}
	return baseURL, apiKey, model
}

func (s *Server) llmClient() *llm.Client {
	baseURL, apiKey, model := s.llmConfig()
	return llm.NewClient(baseURL, apiKey, model)
}

// githubToken resolves the effective GitHub token (sqlite overrides env).
func (s *Server) githubToken() string {
	if v, ok, _ := s.st.GetSetting(keyGitHubToken); ok && v != "" {
		return v
	}
	return s.cfg.GitHubToken
}

// githubTokenSource reports how the current token got configured:
// "oauth"/"pat" for a settings-stored token, "env" for an env-only token,
// or nil when no token exists.
func (s *Server) githubTokenSource() *string {
	if v, ok, _ := s.st.GetSetting(keyGitHubToken); ok && v != "" {
		src, ok, _ := s.st.GetSetting(keyGitHubTokenSource)
		if !ok || (src != "oauth" && src != "pat") {
			src = "pat"
		}
		return &src
	}
	if s.cfg.GitHubToken != "" {
		src := "env"
		return &src
	}
	return nil
}

// githubOAuthClientID resolves the effective OAuth App client ID
// (sqlite overrides env). It is not a secret.
func (s *Server) githubOAuthClientID() string {
	if v, ok, _ := s.st.GetSetting(keyGitHubOAuthClientID); ok && v != "" {
		return v
	}
	return s.cfg.GitHubOAuthClientID
}

// ---- health ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.cfg.Version})
}

// ---- preview proxy ----

func (s *Server) handlePreviewProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.st.GetProject(id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pv := s.previews.Get(id)
	if pv == nil {
		writeError(w, http.StatusServiceUnavailable, "preview is not running")
		return
	}

	if pv.Mode == "static" {
		rest := strings.TrimPrefix(r.URL.Path, "/preview/"+id+"/")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + rest
		r2.URL.RawPath = ""
		http.FileServer(http.Dir(p.Path)).ServeHTTP(w, r2)
		return
	}

	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(pv.Port))}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			if pv.Vite {
				// vite serves at its base path; forward the path unchanged.
				pr.Out.URL.Path = pr.In.URL.Path
			} else {
				out := strings.TrimPrefix(pr.In.URL.Path, "/preview/"+id)
				if out == "" {
					out = "/"
				}
				pr.Out.URL.Path = out
			}
			pr.Out.URL.RawPath = ""
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeError(w, http.StatusBadGateway, "preview unavailable: "+err.Error())
		},
	}
	proxy.ServeHTTP(w, r)
}

// ---- SPA ----

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		http.Error(w, "frontend not built", http.StatusInternalServerError)
		return
	}
	clean := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	name := strings.TrimPrefix(clean, "/")
	if name == "" {
		name = "index.html"
	}
	if f, err := sub.Open(name); err == nil {
		if stat, serr := f.Stat(); serr == nil && !stat.IsDir() {
			defer f.Close()
			if rs, ok := f.(io.ReadSeeker); ok {
				w.Header().Set("Cache-Control", "no-cache")
				http.ServeContent(w, r, stat.Name(), stat.ModTime(), rs)
				return
			}
		} else {
			f.Close()
		}
	}
	// SPA fallback: serve index.html for any unknown non-API GET.
	idx, err := sub.Open("index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "frontend not built")
		return
	}
	defer idx.Close()
	if rs, ok := idx.(io.ReadSeeker); ok {
		stat, _ := idx.Stat()
		var mod time.Time
		if stat != nil {
			mod = stat.ModTime()
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", mod, rs)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "frontend not built")
}
