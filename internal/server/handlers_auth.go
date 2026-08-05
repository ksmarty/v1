package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"v1/internal/llm"
	"v1/internal/mcp"
	"v1/internal/store"
)

// ---- auth ----

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"authRequired":  !s.cfg.AuthDisabled,
		"authenticated": s.auth.Authenticated(r),
		"setupRequired": s.auth.SetupRequired(),
		"oidcEnabled":   s.oidcEnabled(),
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if s.cfg.AuthDisabled {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if !s.auth.Verify(body.Password) {
		writeError(w, http.StatusUnauthorized, "invalid password")
		return
	}
	if err := s.auth.CreateSession(w, r); err != nil {
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
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Password == "" {
		writeError(w, http.StatusBadRequest, "password must not be empty")
		return
	}
	if err := s.auth.SetPassword(body.Password); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.auth.CreateSession(w, r); err != nil {
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
	baseURL, apiKey, model := s.llmConfig()
	models := llm.ModelsForBaseURL(baseURL)
	if models == nil {
		models = []llm.ProviderModel{}
	}
	providers := s.llmProviders()
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
	activeProviderID, _, _ := s.st.GetSetting(keyLLMActiveProvider)
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
			"tokenSet":      s.githubToken() != "",
			"oauthClientId": s.githubOAuthClientID(),
			"source":        s.githubTokenSource(),
		},
		"vercel": map[string]any{
			"tokenSet":        s.vercelToken() != "",
			"oauthClientId":   s.vercelOAuthClientID(),
			"clientSecretSet": s.vercelOAuthClientSecret() != "",
			"source":          s.vercelTokenSource(),
		},
		"auth":           map[string]any{"disabled": s.cfg.AuthDisabled},
		"mcp":            s.mcpServers(),
		"skills":         s.installedSkills(),
		"permissionMode": s.permissionMode(),
		"version":        s.cfg.Version,
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
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
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// set applies a nullable string to a settings key; empty string clears it.
	set := func(key string, v *string) error {
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
			_, legacyKey, _ := s.llmConfig()
			merged := make([]llmProviderRecord, 0, len(*body.LLM.Providers))
			for _, inc := range *body.LLM.Providers {
				if inc.ID == "" {
					inc.ID = store.NewID()
				}
				if inc.APIKey == "" {
					if cur := s.findLLMProvider(inc.ID); cur != nil {
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
			if err := s.st.SetSetting(keyLLMProviders, string(raw)); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		// 2. Activate a provider: mirrors it into the legacy single-provider
		// keys so every existing code path (chat gating, retries, test) works.
		if body.LLM.ActiveProviderID != nil {
			if *body.LLM.ActiveProviderID == "" {
				if err := s.st.DeleteSetting(keyLLMActiveProvider); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
			} else {
				p := s.findLLMProvider(*body.LLM.ActiveProviderID)
				if p == nil {
					writeError(w, http.StatusBadRequest, "unknown provider id")
					return
				}
				for key, v := range map[string]string{
					keyLLMBaseURL: p.BaseURL,
					keyLLMAPIKey:  p.APIKey,
					keyLLMModel:   p.Model,
				} {
					if err := s.st.SetSetting(key, v); err != nil {
						writeError(w, http.StatusInternalServerError, err.Error())
						return
					}
				}
				if err := s.st.SetSetting(keyLLMActiveProvider, p.ID); err != nil {
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
			if err := s.st.DeleteSetting(keyGitHubToken); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := s.st.DeleteSetting(keyGitHubTokenSource); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			if err := s.st.SetSetting(keyGitHubToken, *body.GitHubToken); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := s.st.SetSetting(keyGitHubTokenSource, "pat"); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if err := set(keyGitHubOAuthClientID, body.GitHubOAuthClientID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.VercelToken != nil {
		if *body.VercelToken == "" {
			// Clearing the token also clears its refresh token and source.
			for _, key := range []string{keyVercelToken, keyVercelRefreshToken, keyVercelTokenSource} {
				if err := s.st.DeleteSetting(key); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
		} else {
			if err := s.st.SetSetting(keyVercelToken, *body.VercelToken); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := s.st.SetSetting(keyVercelTokenSource, "pat"); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if err := set(keyVercelOAuthClientID, body.VercelOAuthClientID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := set(keyVercelClientSecret, body.VercelClientSecret); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
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
	if body.Password != nil && *body.Password != "" {
		if err := s.auth.SetPassword(*body.Password); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTestLLM(w http.ResponseWriter, r *http.Request) {
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
	baseURL, apiKey, model := s.llmConfig()
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
