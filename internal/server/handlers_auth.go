package server

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"v1/internal/llm"
	"v1/internal/mcp"
	"v1/internal/store"
)

// ---- auth ----

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,32}$`)

// validateUsername returns an error message for invalid usernames, or "".
func validateUsername(name string) string {
	if !usernameRe.MatchString(name) {
		return "username must be 3–32 characters: letters, digits, . _ -"
	}
	return ""
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	var user *struct {
		Username string `json:"username"`
		IsAdmin  bool   `json:"isAdmin"`
	}
	u, ok := s.auth.User(r)
	if ok && u != nil {
		user = &struct {
			Username string `json:"username"`
			IsAdmin  bool   `json:"isAdmin"`
		}{Username: u.Username, IsAdmin: u.IsAdmin}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authRequired":  !s.cfg.AuthDisabled,
		"authenticated": ok,
		"setupRequired": s.auth.SetupRequired(),
		"oidcEnabled":   s.oidcEnabled(),
		"signupEnabled": s.cfg.AllowSignup,
		"user":          user,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if s.cfg.AuthDisabled {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	u, ok := s.auth.Login(strings.TrimSpace(body.Username), body.Password)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err := s.auth.CreateSession(w, r, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthDisabled || !s.auth.SetupRequired() {
		writeError(w, http.StatusBadRequest, "setup not required")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	username := strings.TrimSpace(body.Username)
	if msg := validateUsername(username); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if body.Password == "" {
		writeError(w, http.StatusBadRequest, "password must not be empty")
		return
	}
	// The first account is the admin — it also claims any legacy ownerless
	// projects (created before multi-user, or while auth was disabled).
	u, err := s.auth.CreateUser(username, body.Password, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.st.ClaimOwnerlessProjects(u.ID)
	if err := s.auth.CreateSession(w, r, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthDisabled || !s.cfg.AllowSignup {
		writeError(w, http.StatusForbidden, "signup is disabled")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	username := strings.TrimSpace(body.Username)
	if msg := validateUsername(username); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if body.Password == "" {
		writeError(w, http.StatusBadRequest, "password must not be empty")
		return
	}
	// Open registration creates a plain user — unless no account exists yet:
	// the first account is always an admin (mirrors the setup flow and the
	// "can't demote the last admin" guard).
	isAdmin := false
	if n, err := s.st.UserCount(); err == nil && n == 0 {
		isAdmin = true
	}
	u, err := s.auth.CreateUser(username, body.Password, isAdmin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.auth.CreateSession(w, r, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.DestroySession(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- settings ----

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	userID := s.currentUser(r).ID
	baseURL, apiKey, model := s.llmConfig(userID)
	models := llm.ModelsForBaseURL(baseURL)
	if models == nil {
		models = []llm.ProviderModel{}
	}
	providers := s.llmProviders(userID)
	providerJSON := make([]map[string]any, 0, len(providers))
	for _, p := range providers {
		providerJSON = append(providerJSON, map[string]any{
			"id":        p.ID,
			"name":      p.Name,
			"baseURL":   p.BaseURL,
			"model":     p.Model,
			"apiKeySet": p.APIKey != "",
		})
	}
	activeProviderID, _, _ := s.st.GetUserSetting(userID, keyLLMActiveProvider)
	writeJSON(w, http.StatusOK, map[string]any{
		"llm": map[string]any{
			"baseURL":          baseURL,
			"model":            model,
			"apiKeySet":        apiKey != "",
			"models":           models,
			"providers":        providerJSON,
			"activeProviderId": activeProviderID,
		},
		"github": map[string]any{
			"tokenSet":      s.githubToken(userID) != "",
			"oauthClientId": s.githubOAuthClientID(),
			"source":        s.githubTokenSource(userID),
		},
		"vercel": map[string]any{
			"tokenSet":        s.vercelToken(userID) != "",
			"oauthClientId":   s.vercelOAuthClientID(),
			"clientSecretSet": s.vercelOAuthClientSecret() != "",
			"source":          s.vercelTokenSource(userID),
		},
		"auth":             map[string]any{"disabled": s.cfg.AuthDisabled},
		"mcp":              s.mcpServers(),
		"skills":           s.installedSkills(),
		"permissionMode":   s.permissionMode(userID),
		"rewindApproval":   s.rewindApproval(userID),
		"defaultThinking":  s.defaultThinking(userID),
		"toonEnabled":      s.toonEnabled(userID),
		"autoPushDefault":  s.autoPushDefault(userID),
		"contextThreshold": int(s.contextThreshold(userID) * 100),
		"systemPrompt":     s.globalSystemPrompt(userID),
		"version":          s.cfg.Version,
		"commit":           s.cfg.Commit,
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	userID := s.currentUser(r).ID
	var body struct {
		LLM *struct {
			BaseURL          *string              `json:"baseURL"`
			APIKey           *string              `json:"apiKey"`
			Model            *string              `json:"model"`
			Providers        *[]llmProviderRecord `json:"providers"`
			ActiveProviderID *string              `json:"activeProviderId"`
		} `json:"llm"`
		GitHubToken         *string             `json:"githubToken"`
		GitHubOAuthClientID *string             `json:"githubOAuthClientId"`
		VercelToken         *string             `json:"vercelToken"`
		VercelOAuthClientID *string             `json:"vercelOAuthClientId"`
		VercelClientSecret  *string             `json:"vercelOAuthClientSecret"`
		Password            *string             `json:"password"`
		MCP                 *[]mcp.ServerConfig `json:"mcp"`
		PermissionMode      *string             `json:"permissionMode"`
		RewindApproval      *bool               `json:"rewindApproval"`
		DefaultThinking     *string             `json:"defaultThinking"`
		ToonEnabled         *bool               `json:"toonEnabled"`
		AutoPushDefault     *bool               `json:"autoPushDefault"`
		ContextThreshold    *float64            `json:"contextThreshold"`
		SystemPrompt        *string             `json:"systemPrompt"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// set applies a nullable string to a user setting; empty string clears it
	// back to the shared/global fallback.
	set := func(key string, v *string) error {
		if v == nil {
			return nil
		}
		if *v == "" {
			return s.st.DeleteUserSetting(userID, key)
		}
		return s.st.SetUserSetting(userID, key, *v)
	}
	// setGlobal writes an instance-level setting (shared by all users).
	setGlobal := func(key string, v *string) error {
		if v == nil {
			return nil
		}
		if *v == "" {
			return s.st.DeleteSetting(key)
		}
		return s.st.SetSetting(key, *v)
	}
	if body.LLM != nil {
		// 1. Merge the saved provider list. Incoming records without an id get a
		// fresh one; an empty apiKey keeps the stored key of an existing record,
		// or inherits the legacy key for a brand-new record (the settings form
		// never shows the stored key back).
		if body.LLM.Providers != nil {
			_, legacyKey, _ := s.llmConfig(userID)
			merged := make([]llmProviderRecord, 0, len(*body.LLM.Providers))
			for _, inc := range *body.LLM.Providers {
				if inc.ID == "" {
					inc.ID = store.NewID()
				}
				if inc.APIKey == "" {
					if cur := s.findLLMProvider(userID, inc.ID); cur != nil {
						inc.APIKey = cur.APIKey
					} else if legacyKey != "" {
						inc.APIKey = legacyKey
					}
				}
				merged = append(merged, inc)
			}
			raw, err := json.Marshal(merged)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := s.st.SetUserSetting(userID, keyLLMProviders, string(raw)); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			// The saved provider set changed (new base URL/key/removed entry):
			// drop cached live model lists so the next providers request
			// repopulates them instead of serving stale or missing models.
			invalidateCustomModelsCache()
		}
		// 2. Activate a provider: mirrors it into the legacy single-provider
		// keys so every existing code path (chat gating, retries, test) works.
		if body.LLM.ActiveProviderID != nil {
			if *body.LLM.ActiveProviderID == "" {
				if err := s.st.DeleteUserSetting(userID, keyLLMActiveProvider); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
			} else {
				p := s.findLLMProvider(userID, *body.LLM.ActiveProviderID)
				if p == nil {
					writeError(w, http.StatusBadRequest, "unknown provider id")
					return
				}
				for key, v := range map[string]string{
					keyLLMBaseURL: p.BaseURL,
					keyLLMAPIKey:  p.APIKey,
					keyLLMModel:   p.Model,
				} {
					if err := s.st.SetUserSetting(userID, key, v); err != nil {
						writeError(w, http.StatusInternalServerError, err.Error())
						return
					}
				}
				if err := s.st.SetUserSetting(userID, keyLLMActiveProvider, p.ID); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
		}
		// 3. Legacy single-provider fields (empty clears).
		for key, v := range map[string]*string{
			keyLLMBaseURL: body.LLM.BaseURL,
			keyLLMAPIKey:  body.LLM.APIKey,
			keyLLMModel:   body.LLM.Model,
		} {
			if err := set(key, v); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if body.GitHubToken != nil {
		if *body.GitHubToken == "" {
			// Clearing the token also clears its source.
			if err := s.st.DeleteUserSetting(userID, keyGitHubToken); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := s.st.DeleteUserSetting(userID, keyGitHubTokenSource); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			if err := s.st.SetUserSetting(userID, keyGitHubToken, *body.GitHubToken); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := s.st.SetUserSetting(userID, keyGitHubTokenSource, "pat"); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	// OAuth app credentials are instance-level (one OAuth app per install):
	// they stay in the shared settings table.
	if body.GitHubOAuthClientID != nil {
		if err := setGlobal(keyGitHubOAuthClientID, body.GitHubOAuthClientID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.VercelToken != nil {
		if *body.VercelToken == "" {
			// Clearing the token also clears its refresh token and source.
			for _, key := range []string{keyVercelToken, keyVercelRefreshToken, keyVercelTokenSource} {
				if err := s.st.DeleteUserSetting(userID, key); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
		} else {
			if err := s.st.SetUserSetting(userID, keyVercelToken, *body.VercelToken); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := s.st.SetUserSetting(userID, keyVercelTokenSource, "pat"); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if body.VercelOAuthClientID != nil {
		if err := setGlobal(keyVercelOAuthClientID, body.VercelOAuthClientID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.VercelClientSecret != nil {
		if err := setGlobal(keyVercelClientSecret, body.VercelClientSecret); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.MCP != nil {
		if err := s.saveMCPServers(*body.MCP); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.PermissionMode != nil {
		if err := set(keyPermissionMode, body.PermissionMode); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.SystemPrompt != nil {
		if err := set(keySystemPrompt, body.SystemPrompt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.RewindApproval != nil {
		val := "0"
		if *body.RewindApproval {
			val = "1"
		}
		if err := s.st.SetUserSetting(userID, keyRewindApproval, val); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.DefaultThinking != nil {
		if err := set(keyThinkingDefault, body.DefaultThinking); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.ToonEnabled != nil {
		val := "0"
		if *body.ToonEnabled {
			val = "1"
		}
		if err := s.st.SetUserSetting(userID, keyToonEnabled, val); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.AutoPushDefault != nil {
		val := "0"
		if *body.AutoPushDefault {
			val = "1"
		}
		if err := s.st.SetUserSetting(userID, keyAutoPushDefault, val); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.ContextThreshold != nil {
		// Stored as a percent; 0 or out-of-range clears back to the default.
		if *body.ContextThreshold <= 0 || *body.ContextThreshold > 100 {
			if err := s.st.DeleteUserSetting(userID, keyContextThreshold); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			val := strconv.FormatFloat(*body.ContextThreshold, 'f', -1, 64)
			if err := s.st.SetUserSetting(userID, keyContextThreshold, val); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if body.Password != nil && *body.Password != "" {
		if err := s.auth.SetPassword(userID, *body.Password); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTestLLM(w http.ResponseWriter, r *http.Request) {
	userID := s.currentUser(r).ID
	// Optional body: non-empty fields override the stored/env configuration.
	var body struct {
		BaseURL string `json:"baseURL"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	}
	if r.Body != nil {
		data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, "reading body: "+err.Error())
			return
		}
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &body); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
				return
			}
		}
	}
	baseURL, apiKey, model := s.llmConfig(userID)
	if body.BaseURL != "" {
		baseURL = body.BaseURL
	}
	if body.APIKey != "" {
		apiKey = body.APIKey
	}
	if body.Model != "" {
		model = body.Model
	}
	if err := llm.NewClient(baseURL, apiKey, model).TestModels(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
