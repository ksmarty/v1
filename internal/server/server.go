// Package server wires together the v1 HTTP API, preview proxy, terminal
// and embedded SPA static serving.
package server

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"v1/internal/agent"
	"v1/internal/auth"
	"v1/internal/config"
	"v1/internal/llm"
	"v1/internal/mcp"
	"v1/internal/preview"
	"v1/internal/store"
	"v1/internal/terminal"
	"v1/internal/vercel"
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
	turns     *turnManager
	handler   http.Handler

	background *agent.BackgroundManager

	mcp  *mcp.Manager
	perm permRegistry
	ask  askRegistry

	oauthMu    sync.Mutex
	oauthFlows map[string]*oauthFlow
	// Redirect (authorization-code) flow states, keyed by the OAuth state.
	oauthCodeFlows map[string]*githubOAuthFlow

	oidcMu    sync.Mutex
	oidcFlows map[string]*oidcFlow
	oidc      *auth.OIDC

	vercelMu      sync.Mutex
	vercelFlows   map[string]*vercelFlow
	vercelDeploys map[string]*deployState
}

// New builds the server, its routes and middleware.
func New(cfg config.Config, st *store.Store) *Server {
	s := &Server{
		cfg:            cfg,
		st:             st,
		auth:           auth.NewManager(st, cfg.AuthDisabled, cfg.Password),
		previews:       preview.NewManager(cfg.MaxPreviews),
		terminals:      terminal.NewManager(),
		turns:          newTurnManager(),
		background:     agent.NewBackgroundManager(),
		oauthFlows:     map[string]*oauthFlow{},
		oauthCodeFlows: map[string]*githubOAuthFlow{},
		oidcFlows:      map[string]*oidcFlow{},
		vercelFlows:    map[string]*vercelFlow{},
		vercelDeploys:  map[string]*deployState{},
		oidc:           auth.NewOIDC(auth.OIDCConfig{}),
		perm:           permRegistry{reqs: map[string]*permRequest{}},
		ask:            askRegistry{reqs: map[string]*askRequest{}},
	}
	s.mcp = mcp.NewManager(s.mcpServers)
	s.auth.BootstrapAdmin()
	s.rebuildOIDC()
	s.pruneOIDCFlows()
	s.pruneVercelFlows()
	s.failInterruptedVercelDeploys()
	mux := http.NewServeMux()
	s.routes(mux)
	s.handler = s.auth.Middleware(mux)
	return s
}

// Handler returns the root HTTP handler (with auth middleware applied).
func (s *Server) Handler() http.Handler { return s.handler }

// Shutdown stops all previews, terminals, background jobs and MCP servers.
func (s *Server) Shutdown() {
	s.previews.StopAll()
	s.terminals.KillAll()
	if s.background != nil {
		s.background.KillAll()
	}
	s.mcp.Shutdown()
}

func (s *Server) routes(m *http.ServeMux) {
	m.HandleFunc("GET /api/healthz", s.handleHealth)

	m.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/auth/setup", s.handleSetup)
	m.HandleFunc("POST /api/auth/signup", s.handleSignup)
	m.HandleFunc("POST /api/auth/logout", s.handleLogout)
	m.HandleFunc("GET /api/auth/oidc/start", s.handleOIDCStart)
	m.HandleFunc("GET /api/auth/oidc/callback", s.handleOIDCCallback)
	m.HandleFunc("GET /api/settings/oidc", s.handleOIDCSettings)
	m.HandleFunc("POST /api/settings/oidc", s.handleOIDCSettingsSave)
	m.HandleFunc("GET /api/auth/vercel/oauth/start", s.handleVercelOAuthStart)
	m.HandleFunc("GET /api/auth/vercel/oauth/callback", s.handleVercelOAuthCallback)
	m.HandleFunc("GET /api/auth/github/oauth/start", s.handleGitHubOAuthStart)
	m.HandleFunc("GET /api/auth/github/oauth/callback", s.handleGitHubOAuthCallback)

	m.HandleFunc("GET /api/users", s.handleListUsers)
	m.HandleFunc("POST /api/users", s.handleCreateUser)
	m.HandleFunc("PATCH /api/users/{id}", s.handleUpdateUser)
	m.HandleFunc("DELETE /api/users/{id}", s.handleDeleteUser)

	m.HandleFunc("GET /api/settings", s.handleGetSettings)
	m.HandleFunc("PUT /api/settings", s.handlePutSettings)
	m.HandleFunc("POST /api/settings/test-llm", s.handleTestLLM)

	m.HandleFunc("GET /api/providers", s.handleListProviders)
	m.HandleFunc("POST /api/providers/refresh", s.handleRefreshProviders)
	m.HandleFunc("GET /api/providers/thinking", s.handleProviderThinking)
	m.HandleFunc("GET /api/providers/search", s.handleSearchProviders)
	m.HandleFunc("POST /api/providers/add", s.handleAddProvider)
	m.HandleFunc("POST /api/providers/remove", s.handleRemoveProvider)

	m.HandleFunc("GET /api/projects", s.handleListProjects)
	m.HandleFunc("GET /api/projects/active", s.handleActiveRuns)
	m.HandleFunc("POST /api/projects", s.handleCreateProject)
	m.HandleFunc("POST /api/projects/import", s.handleImportProject)
	m.HandleFunc("GET /api/projects/{id}", s.handleGetProject)
	m.HandleFunc("PATCH /api/projects/{id}", s.handleUpdateProject)
	m.HandleFunc("DELETE /api/projects/{id}", s.handleDeleteProject)

	m.HandleFunc("GET /api/projects/{id}/files", s.handleListFiles)
	m.HandleFunc("GET /api/projects/{id}/file", s.handleReadFile)
	m.HandleFunc("PUT /api/projects/{id}/file", s.handleWriteFile)
	m.HandleFunc("DELETE /api/projects/{id}/file", s.handleDeleteFile)

	m.HandleFunc("GET /api/projects/{id}/messages", s.handleListMessages)
	m.HandleFunc("GET /api/projects/{id}/context", s.handleContextUsage)
	m.HandleFunc("GET /api/projects/{id}/messages/{msgId}/attachments/{idx}", s.handleMessageAttachment)
	m.HandleFunc("POST /api/projects/{id}/messages/truncate", s.handleTruncateMessages)
	m.HandleFunc("GET /api/projects/{id}/memories", s.handleListMemories)
	m.HandleFunc("POST /api/projects/{id}/memories", s.handleCreateMemory)
	m.HandleFunc("POST /api/projects/{id}/memories/{memId}/toggle", s.handleToggleMemory)
	m.HandleFunc("PUT /api/projects/{id}/memories/{memId}", s.handleUpdateMemory)
	m.HandleFunc("DELETE /api/projects/{id}/memories/{memId}", s.handleDeleteMemory)
	m.HandleFunc("POST /api/projects/{id}/ask/respond", s.handleAskRespond)
	m.HandleFunc("GET /api/projects/{id}/ask/pending", s.handleAskPending)
	m.HandleFunc("GET /api/projects/{id}/chat/queue", s.handleChatQueue)
	m.HandleFunc("GET /api/projects/{id}/diagnostics", s.handleDiagnostics)
	m.HandleFunc("POST /api/projects/{id}/chat/queue/reorder", s.handleChatQueueReorder)
	m.HandleFunc("POST /api/projects/{id}/chat/queue/steer", s.handleChatQueueSteer)
	m.HandleFunc("POST /api/projects/{id}/chat/queue/edit", s.handleChatQueueEdit)
	m.HandleFunc("POST /api/projects/{id}/chat/queue/hold", s.handleChatQueueHold)
	m.HandleFunc("POST /api/projects/{id}/chat/queue/delete", s.handleChatQueueDelete)
	m.HandleFunc("GET /api/projects/{id}/sessions", s.handleListSessions)
	m.HandleFunc("POST /api/projects/{id}/sessions", s.handleCreateSession)
	m.HandleFunc("POST /api/projects/{id}/sessions/{sessionId}/rename", s.handleRenameSession)
	m.HandleFunc("POST /api/projects/{id}/sessions/{sessionId}/archive", s.handleArchiveSession)
	m.HandleFunc("POST /api/projects/{id}/sessions/{sessionId}/unarchive", s.handleUnarchiveSession)
	m.HandleFunc("DELETE /api/projects/{id}/sessions/{sessionId}", s.handleDeleteSession)
	m.HandleFunc("POST /api/projects/{id}/compact", s.handleCompact)
	m.HandleFunc("GET /api/projects/{id}/todos", s.handleGetTodos)
	m.HandleFunc("POST /api/projects/{id}/chat", s.handleChat)
	m.HandleFunc("POST /api/projects/{id}/chat/retry", s.handleChatRetry)
	m.HandleFunc("POST /api/projects/{id}/chat/stop", s.handleChatStop)
	m.HandleFunc("GET /api/projects/{id}/chat/status", s.handleChatStatus)
	m.HandleFunc("GET /api/projects/{id}/chat/watch", s.handleChatWatch)

	m.HandleFunc("GET /api/projects/{id}/preview/status", s.handlePreviewStatus)
	m.HandleFunc("POST /api/projects/{id}/preview/start", s.handlePreviewStart)
	m.HandleFunc("POST /api/projects/{id}/preview/stop", s.handlePreviewStop)

	m.HandleFunc("GET /api/projects/{id}/terminal", s.handleTerminal)

	m.HandleFunc("GET /api/github/repos", s.handleGitHubRepos)
	m.HandleFunc("GET /api/github/user", s.handleGitHubUser)
	m.HandleFunc("GET /api/github/workflows", s.handleGitHubWorkflows)
	m.HandleFunc("GET /api/github/images", s.handleGitHubImages)
	m.HandleFunc("POST /api/github/oauth/device/start", s.handleOAuthDeviceStart)
	m.HandleFunc("POST /api/github/oauth/device/poll", s.handleOAuthDevicePoll)
	m.HandleFunc("POST /api/projects/{id}/github/create", s.handleGitHubCreate)
	m.HandleFunc("POST /api/projects/{id}/github/link", s.handleGitHubLink)
	m.HandleFunc("POST /api/projects/{id}/github/push", s.handleGitHubPush)
	m.HandleFunc("GET /api/projects/{id}/git/status", s.handleGitStatus)
	m.HandleFunc("GET /api/projects/{id}/git/info", s.handleGitInfo)
	m.HandleFunc("POST /api/projects/{id}/git/init", s.handleGitInit)
	m.HandleFunc("POST /api/projects/{id}/git/branch", s.handleGitBranch)
	m.HandleFunc("POST /api/projects/{id}/git/checkout", s.handleGitCheckout)
	m.HandleFunc("POST /api/projects/{id}/git/revert", s.handleGitRevert)
	m.HandleFunc("POST /api/projects/{id}/revert", s.handleGitRevert)
	m.HandleFunc("POST /api/projects/{id}/git/commit", s.handleGitCommit)
	m.HandleFunc("GET /api/projects/{id}/git/changes", s.handleGitChanges)
	m.HandleFunc("POST /api/projects/{id}/git/fetch", s.handleGitFetch)
	m.HandleFunc("POST /api/projects/{id}/git/pull", s.handleGitPull)
	m.HandleFunc("POST /api/projects/{id}/git/discard", s.handleGitDiscard)
	m.HandleFunc("POST /api/projects/{id}/git/stage", s.handleGitStage)
	m.HandleFunc("POST /api/projects/{id}/chat/permission", s.handlePermission)

	m.HandleFunc("GET /api/mcp/status", s.handleMCPStatus)
	m.HandleFunc("POST /api/mcp/test", s.handleMCPTest)

	m.HandleFunc("GET /api/vercel/user", s.handleVercelUser)
	m.HandleFunc("POST /api/projects/{id}/vercel/deploy", s.handleVercelDeploy)
	m.HandleFunc("GET /api/projects/{id}/vercel/deployments", s.handleVercelDeployments)

	m.HandleFunc("POST /api/skills/search", s.handleSkillsSearch)
	m.HandleFunc("POST /api/skills/install", s.handleSkillsInstall)
	m.HandleFunc("GET /api/skills/{id}/readme", s.handleSkillReadme)
	m.HandleFunc("POST /api/skills/remove", s.handleSkillsRemove)
	m.HandleFunc("POST /api/skills/toggle", s.handleSkillsToggle)

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

// currentUser returns the authenticated user of the request. The middleware
// guarantees one for protected routes (and the local dev user when auth is
// disabled), so callers can use it directly.
func (s *Server) currentUser(r *http.Request) *store.User {
	u, _ := auth.UserFromContext(r.Context())
	return u
}

// requireAdmin writes a 403 and returns false when the caller is not an admin.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if u := s.currentUser(r); u != nil && u.IsAdmin {
		return true
	}
	writeError(w, http.StatusForbidden, "admin required")
	return false
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
	// Strict isolation when auth is on: every user only sees their own
	// projects. Auth-disabled (dev) mode stays open, as before.
	if !s.cfg.AuthDisabled {
		if u := s.currentUser(r); u == nil || p.OwnerID != u.ID {
			writeError(w, http.StatusNotFound, "project not found")
			return nil
		}
	}
	return p
}

// Settings keys stored in the settings table. Values set via the API take
// precedence over environment fallbacks.
const (
	keyLLMBaseURL              = "llm_base_url"
	keyLLMAPIKey               = "llm_api_key"
	keyLLMModel                = "llm_model"
	keyLLMDefaultModel         = "llm_default_model"
	keyLLMProviders            = "llm_providers"
	keyLLMCurrency             = "llm_currency"
	keyGitHubToken             = "github_token"
	keyGitHubTokenSource       = "github_token_source"
	keyGitHubOAuthClientID     = "github_oauth_client_id"
	keyGitHubOAuthClientSecret = "github_oauth_client_secret"
	keyVercelToken             = "vercel_token"
	keyVercelTokenSource       = "vercel_token_source"
	keyVercelRefreshToken      = "vercel_refresh_token"
	keyVercelOAuthClientID     = "vercel_oauth_client_id"
	keyVercelClientSecret      = "vercel_oauth_client_secret"
	keyProvidersCache          = "providers_cache"
	keyProvidersCustom         = "providers_custom"
	keyMCP                     = "mcp_servers"
	keySkills                  = "skills_installed"
	keyPermissionMode          = "permission_mode"
	keyRewindApproval          = "rewind_approval"
	keyThinkingDefault         = "thinking_default"
	keyToonEnabled             = "toon_enabled"
	keyDisabledTools           = "disabled_tools"
	keyCaveman                 = "caveman"
	keySoftTimeout             = "turn_soft_timeout" // minutes; 0 = disabled
	keyHardTimeout             = "turn_hard_timeout" // minutes; 0 = disabled
	keyTerminalFontSize        = "terminal_font_size" // px
	keyTerminalWrap            = "terminal_wrap"      // "1" = wrap, "0" = off
	keyAutoPushDefault         = "auto_push_default"
	keySystemPrompt            = "system_prompt"
	keyContextThreshold        = "context_threshold"
)

// oidcEnabled reports whether the OIDC flow is active: it needs auth enabled
// and the flow configured (forced via V1_AUTH_OIDC_ENABLED, or fully
// specified via the V1_OIDC_* values).
func (s *Server) oidcEnabled() bool {
	return !s.cfg.AuthDisabled && s.oidc != nil && s.oidc.Enabled()
}

// Instance-level OIDC settings (admin-savable in Settings → Auth → OIDC).
// Saved settings override the environment; empty values fall back to env, and
// an empty callback URI is derived per request.
const (
	keyOIDCIssuer        = "oidc_issuer"
	keyOIDCClientID      = "oidc_client_id"
	keyOIDCClientSecret  = "oidc_client_secret"
	keyOIDCCallbackURI   = "oidc_callback_uri"
	keyOIDCAllowedEmails = "oidc_allowed_emails"
)

// oidcConfig merges the admin-saved OIDC settings over the environment.
func (s *Server) oidcConfig() auth.OIDCConfig {
	cfg := auth.OIDCConfig{
		Enabled:       s.cfg.AuthOIDCEnabled,
		Issuer:        s.cfg.OIDCIssuer,
		ClientID:      s.cfg.OIDCClientID,
		ClientSecret:  s.cfg.OIDCClientSecret,
		RedirectURI:   s.cfg.OIDCRedirectURI,
		AllowedEmails: s.cfg.OIDCAllowedEmails,
		AdminEmails:   s.cfg.OIDCAdminEmails,
	}
	if v, _, _ := s.st.GetSetting(keyOIDCIssuer); v != "" {
		cfg.Issuer = v
	}
	if v, _, _ := s.st.GetSetting(keyOIDCClientID); v != "" {
		cfg.ClientID = v
	}
	if v, _, _ := s.st.GetSetting(keyOIDCClientSecret); v != "" {
		cfg.ClientSecret = v
	}
	if v, _, _ := s.st.GetSetting(keyOIDCCallbackURI); v != "" {
		cfg.RedirectURI = v
	}
	if v, _, _ := s.st.GetSetting(keyOIDCAllowedEmails); v != "" {
		cfg.AllowedEmails = splitCSV(v)
	}
	return cfg
}

// isOIDCAdmin reports whether an OIDC email is granted the admin role
// (V1_OIDC_ADMIN_EMAILS in the effective config, case-insensitive).
func (s *Server) isOIDCAdmin(email string) bool {
	for _, e := range s.oidcConfig().AdminEmails {
		if strings.EqualFold(e, email) {
			return true
		}
	}
	return false
}

// rebuildOIDC swaps in a fresh OIDC client from the effective config — called
// at startup and whenever an admin saves the OIDC settings. The provider is
// resolved lazily, so a misconfigured issuer doesn't break anything until a
// login is attempted.
func (s *Server) rebuildOIDC() {
	s.oidc = auth.NewOIDC(s.oidcConfig())
	s.auth.SetOIDCEnabled(s.oidcEnabled())
}

// userSetting resolves a setting for a user: the user's own value wins, then
// the shared/global value (legacy pre-multi-user data or admin-set values).
func (s *Server) userSetting(userID, key string) (string, bool) {
	if v, ok, err := s.st.GetUserSetting(userID, key); err == nil && ok && v != "" {
		return v, true
	}
	v, ok, _ := s.st.GetSetting(key)
	return v, ok
}

// llmConfig resolves the effective LLM settings for a user (user settings
// override global settings, which override env).
func (s *Server) llmConfig(userID string) (baseURL, apiKey, model string) {
	baseURL, apiKey, model = s.cfg.OpenAIBase, s.cfg.OpenAIKey, s.cfg.Model
	if v, ok := s.userSetting(userID, keyLLMBaseURL); ok && v != "" {
		baseURL = v
	}
	if v, ok := s.userSetting(userID, keyLLMAPIKey); ok && v != "" {
		apiKey = v
	}
	if v, ok := s.userSetting(userID, keyLLMModel); ok && v != "" {
		model = v
	}
	return baseURL, apiKey, model
}

func (s *Server) llmClient(userID string) *llm.Client {
	baseURL, apiKey, model := s.llmConfig(userID)
	return llm.NewClient(baseURL, apiKey, model)
}

// defaultLLMModel is the model new sessions fall back to when nothing is
// selected yet — set automatically from the first saved provider (see
// handlePutSettings) and editable in Settings → LLM.
func (s *Server) defaultLLMModel(userID string) string {
	v, _ := s.userSetting(userID, keyLLMDefaultModel)
	return v
}

// llmProviderRecord is one saved LLM provider. APIKey is only ever written
// and used server-side; responses expose just apiKeySet.
type llmProviderRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey,omitempty"`
	Model   string `json:"model"`
}

// llmProviders loads the user's saved provider list (nil when none saved).
func (s *Server) llmProviders(userID string) []llmProviderRecord {
	v, ok := s.userSetting(userID, keyLLMProviders)
	if !ok || v == "" {
		return nil
	}
	var out []llmProviderRecord
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil
	}
	return out
}

func (s *Server) findLLMProvider(userID, id string) *llmProviderRecord {
	if id == "" {
		return nil
	}
	for _, p := range s.llmProviders(userID) {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

// llmClientFor builds a client for a saved provider id, falling back to the
// legacy single-provider settings when the id is empty or unknown.
func (s *Server) llmClientFor(userID, providerID string) *llm.Client {
	if p := s.findLLMProvider(userID, providerID); p != nil {
		return llm.NewClient(p.BaseURL, p.APIKey, p.Model)
	}
	return s.llmClient(userID)
}

// githubToken resolves the user's effective GitHub token (user settings
// override global settings, which override env).
func (s *Server) githubToken(userID string) string {
	if v, ok := s.userSetting(userID, keyGitHubToken); ok && v != "" {
		return v
	}
	return s.cfg.GitHubToken
}

// systemPromptFor resolves the system prompt override a user has set (their
// own override wins, then V1_SYSTEM_PROMPT); empty means "use the built-in
// base".
func (s *Server) systemPromptFor(userID string) string {
	if v, ok := s.userSetting(userID, keySystemPrompt); ok && v != "" {
		return v
	}
	return s.cfg.SystemPrompt
}

// effectiveSystemPrompt is the system prompt the agent will actually run for a
// user: their override, else the env value, else the built-in base. Settings
// uses this so the editor starts pre-filled with what runs today.
func (s *Server) effectiveSystemPrompt(userID string) string {
	v := s.systemPromptFor(userID)
	if v == "" {
		v = agent.SystemPrompt()
	}
	return v
}

// rewindApproval reports whether rewinding chat history requires approval.
func (s *Server) rewindApproval(userID string) bool {
	v, ok := s.userSetting(userID, keyRewindApproval)
	return ok && v == "1"
}

// defaultThinking resolves the user's default thinking level (” = the
// model's lowest level).
func (s *Server) defaultThinking(userID string) string {
	v, ok := s.userSetting(userID, keyThinkingDefault)
	if !ok || v == "" {
		return ""
	}
	return v
}

// toonEnabled reports whether tool results are TOON-encoded for the model.
// Enabled by default; only an explicit "0" disables it.
func (s *Server) toonEnabled(userID string) bool {
	v, ok := s.userSetting(userID, keyToonEnabled)
	if !ok || v == "" {
		return true
	}
	return v == "1"
}

// disabledTools resolves the user's disabled builtin tool names (JSON array).
func (s *Server) disabledTools(userID string) map[string]bool {
	v, ok := s.userSetting(userID, keyDisabledTools)
	if !ok || v == "" {
		return nil
	}
	var names []string
	if json.Unmarshal([]byte(v), &names) != nil {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// cavemanEnabled reports whether the user wants the terse caveman reply style.
func (s *Server) cavemanEnabled(userID string) bool {
	v, ok := s.userSetting(userID, keyCaveman)
	return ok && v == "1"
}

// turnTimeouts resolves the user's soft/hard turn timeouts in minutes. Both
// default to the classic 5/10 when unset; 0 disables that side entirely.
func (s *Server) turnTimeouts(userID string) (soft, hard time.Duration) {
	softMin := s.turnTimeoutMinutes(userID, keySoftTimeout, 5)
	hardMin := s.turnTimeoutMinutes(userID, keyHardTimeout, 10)
	return time.Duration(softMin) * time.Minute, time.Duration(hardMin) * time.Minute
}

func (s *Server) turnTimeoutMinutes(userID, key string, def int) int {
	v, ok := s.userSetting(userID, key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// turnTimeoutMinutesForUI reports the effective soft/hard timeouts in minutes
// for the settings UI (defaults 5/10, 0 = disabled).
func (s *Server) turnTimeoutMinutesForUI(userID string) map[string]int {
	return map[string]int{
		"soft": s.turnTimeoutMinutes(userID, keySoftTimeout, 5),
		"hard": s.turnTimeoutMinutes(userID, keyHardTimeout, 10),
	}
}

// terminalFontSize returns the user's terminal font size in px (default 13).
func (s *Server) terminalFontSize(userID string) int {
	v, ok := s.userSetting(userID, keyTerminalFontSize)
	if !ok || v == "" {
		return 13
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 8 || n > 28 {
		return 13
	}
	return n
}

// terminalWrap reports whether terminal output wraps (default on).
func (s *Server) terminalWrap(userID string) bool {
	v, ok := s.userSetting(userID, keyTerminalWrap)
	if !ok || v == "" {
		return true
	}
	return v == "1"
}

// autoPushDefault resolves the global default for new projects' auto-push
// toggle (defaults to off; only projects created after a change pick it up).
func (s *Server) autoPushDefault(userID string) bool {
	v, ok := s.userSetting(userID, keyAutoPushDefault)
	if !ok || v == "" {
		return false
	}
	return v == "1"
}

// contextThreshold resolves the user's compaction threshold — the share of
// the context budget at which compaction triggers — as a fraction. The user
// setting stores a percent; env is the fallback.
func (s *Server) contextThreshold(userID string) float64 {
	if v, ok := s.userSetting(userID, keyContextThreshold); ok {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 && n <= 100 {
			return n / 100
		}
	}
	t := s.cfg.ContextThreshold
	if t <= 0 || t > 1 {
		t = 0.8
	}
	return t
}

// currency resolves the currency used to display provider-supplied cost.
// Defaults to USD when unset.
func (s *Server) currency(userID string) string {
	if v, ok := s.userSetting(userID, keyLLMCurrency); ok && v != "" {
		return v
	}
	return "USD"
}

// githubTokenSource reports how the current token got configured:
// "oauth"/"pat" for a settings-stored token, "env" for an env-only token,
// or nil when no token exists.
func (s *Server) githubTokenSource(userID string) *string {
	if v, ok := s.userSetting(userID, keyGitHubToken); ok && v != "" {
		src, ok := s.userSetting(userID, keyGitHubTokenSource)
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

// githubOAuthClientSecret resolves the effective OAuth App client secret
// (sqlite overrides env). Needed for the redirect (authorization-code) flow;
// device flow only needs the client ID.
func (s *Server) githubOAuthClientSecret() string {
	if v, ok, _ := s.st.GetSetting(keyGitHubOAuthClientSecret); ok && v != "" {
		return v
	}
	return s.cfg.GitHubOAuthClientSecret
}

// vercelToken resolves the user's effective Vercel token (user settings
// override env).
func (s *Server) vercelToken(userID string) string {
	if v, ok := s.userSetting(userID, keyVercelToken); ok && v != "" {
		return v
	}
	return s.cfg.VercelToken
}

// failInterruptedVercelDeploys marks deployments that were in flight when the
// process went down as ERROR, so after a restart the UI shows what happened
// instead of pretending the deploy never existed.
func (s *Server) failInterruptedVercelDeploys() {
	if err := s.st.FailInterruptedVercelDeploys("deploy interrupted by a server restart"); err != nil {
		log.Printf("vercel: marking interrupted deploys: %v", err)
	}
}

// vercelTokenSource reports how the current token got configured:
// "oauth"/"pat" for a settings-stored token, "env" for an env-only token,
// or nil when no token exists.
func (s *Server) vercelTokenSource(userID string) *string {
	if v, ok := s.userSetting(userID, keyVercelToken); ok && v != "" {
		src, ok := s.userSetting(userID, keyVercelTokenSource)
		if !ok || (src != "oauth" && src != "pat") {
			src = "pat"
		}
		return &src
	}
	if s.cfg.VercelToken != "" {
		src := "env"
		return &src
	}
	return nil
}

func (s *Server) vercelRefreshToken(userID string) string {
	if v, ok := s.userSetting(userID, keyVercelRefreshToken); ok && v != "" {
		return v
	}
	return s.cfg.VercelRefreshToken
}

// vercelOAuthClientID resolves the OAuth app client id (sqlite overrides env).
func (s *Server) vercelOAuthClientID() string {
	if v, ok, _ := s.st.GetSetting(keyVercelOAuthClientID); ok && v != "" {
		return v
	}
	return s.cfg.VercelClientID
}

// vercelOAuthClientSecret resolves the OAuth app client secret (sqlite
// overrides env).
func (s *Server) vercelOAuthClientSecret() string {
	if v, ok, _ := s.st.GetSetting(keyVercelClientSecret); ok && v != "" {
		return v
	}
	return s.cfg.VercelClientSecret
}

// vercelClient builds a Vercel API client from the user's effective settings.
func (s *Server) vercelClient(userID string) *vercel.Client {
	return &vercel.Client{
		Token:        s.vercelToken(userID),
		RefreshToken: s.vercelRefreshToken(userID),
		ClientID:     s.vercelOAuthClientID(),
		ClientSecret: s.vercelOAuthClientSecret(),
	}
}

// customProviders returns the providers added at runtime from models.dev
// (persisted in settings, never part of the catalog cache).
func (s *Server) customProviders() []llm.Provider {
	v, ok, _ := s.st.GetSetting(keyProvidersCustom)
	if !ok || v == "" {
		return nil
	}
	var out []llm.Provider
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil
	}
	return out
}

func (s *Server) saveCustomProviders(providers []llm.Provider) error {
	invalidateCustomModelsCache() // a new/removed provider must refetch models now, not in 10 min
	data, err := json.Marshal(providers)
	if err != nil {
		return err
	}
	return s.st.SetSetting(keyProvidersCustom, string(data))
}

func (s *Server) addCustomProvider(p llm.Provider) error {
	cur := s.customProviders()
	for _, cp := range cur {
		if cp.ID == p.ID {
			return nil
		}
	}
	return s.saveCustomProviders(append(cur, p))
}

func (s *Server) removeCustomProvider(id string) error {
	cur := s.customProviders()
	out := cur[:0]
	for _, cp := range cur {
		if cp.ID != id {
			out = append(out, cp)
		}
	}
	if len(out) == len(cur) {
		return nil
	}
	return s.saveCustomProviders(out)
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
	if u := s.currentUser(r); !s.cfg.AuthDisabled && (u == nil || p.OwnerID != u.ID) {
		writeError(w, http.StatusNotFound, "project not found")
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
