package server

import (
	"net/http"
)

// ---- auth ----

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"authRequired":  !s.cfg.AuthDisabled,
		"authenticated": s.auth.Authenticated(r),
		"setupRequired": s.auth.SetupRequired(),
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
	writeJSON(w, http.StatusOK, map[string]any{
		"llm": map[string]any{
			"baseURL":   baseURL,
			"model":     model,
			"apiKeySet": apiKey != "",
		},
		"github": map[string]any{
			"tokenSet":      s.githubToken() != "",
			"oauthClientId": s.githubOAuthClientID(),
			"source":        s.githubTokenSource(),
		},
		"auth":    map[string]any{"disabled": s.cfg.AuthDisabled},
		"version": s.cfg.Version,
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LLM *struct {
			BaseURL *string `json:"baseURL"`
			APIKey  *string `json:"apiKey"`
			Model   *string `json:"model"`
		} `json:"llm"`
		GitHubToken         *string `json:"githubToken"`
		GitHubOAuthClientID *string `json:"githubOAuthClientId"`
		Password            *string `json:"password"`
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
	if body.Password != nil && *body.Password != "" {
		if err := s.auth.SetPassword(*body.Password); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTestLLM(w http.ResponseWriter, r *http.Request) {
	if err := s.llmClient().TestModels(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
