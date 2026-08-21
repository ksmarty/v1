package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"v1/internal/gitops"
	"v1/internal/store"
)

// GitHub OAuth device flow. No client secret and no redirect URI are needed,
// so it works behind any reverse proxy. Flow state lives in memory only.

type oauthFlow struct {
	deviceCode string
	interval   int
	expiresAt  time.Time
}

const (
	deviceCodeURL  = "https://github.com/login/device/code"
	accessTokenURL = "https://github.com/login/oauth/access_token"
	deviceScope    = "repo read:user read:packages"
)

// githubFormPost posts a form to a github.com endpoint and decodes the JSON
// response body.
func githubFormPost(ctx context.Context, endpoint string, form url.Values) (int, map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "v1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return resp.StatusCode, nil, fmt.Errorf("invalid response from GitHub (HTTP %d): %s", resp.StatusCode, truncateStr(string(body), 200))
	}
	return resp.StatusCode, out, nil
}

func truncateStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func jsonStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func jsonNum(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// handleOAuthDeviceStart begins a device flow: asks GitHub for a device code
// and returns the user code + verification URI to show the user.
func (s *Server) handleOAuthDeviceStart(w http.ResponseWriter, r *http.Request) {
	clientID := s.githubOAuthClientID()
	if clientID == "" {
		writeError(w, http.StatusBadRequest, "no_client_id")
		return
	}
	status, resp, err := githubFormPost(r.Context(), deviceCodeURL, url.Values{
		"client_id": {clientID},
		"scope":     {deviceScope},
	})
	if err != nil {
		// The container couldn't reach GitHub (DNS/egress/TLS). Log the exact
		// error so the operator can see whether it's transport vs GitHub.
		log.Printf("github oauth: device code request failed (client_id %q): %v", clientID, err)
		writeError(w, http.StatusBadGateway, "GitHub device flow start failed: "+err.Error())
		return
	}
	if status != http.StatusOK {
		// GitHub returns 404 with {"error":"Not Found"} when the client_id
		// doesn't match a real OAuth App — tell the user that instead of a
		// mysterious 502.
		if status == http.StatusNotFound {
			log.Printf("github oauth: GitHub rejected client_id %q (404)", clientID)
			writeError(w, http.StatusBadGateway, "GitHub rejected the OAuth Client ID (404 Not Found) — check the Client ID saved in Settings matches your OAuth App at github.com/settings/developers")
			return
		}
		log.Printf("github oauth: device code request returned HTTP %d (client_id %q)", status, clientID)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("GitHub device flow start failed (HTTP %d)", status))
		return
	}
	if e := jsonStr(resp, "error"); e != "" {
		desc := jsonStr(resp, "error_description")
		if desc == "" {
			desc = e
		}
		writeError(w, http.StatusBadGateway, "GitHub: "+desc)
		return
	}
	deviceCode := jsonStr(resp, "device_code")
	if deviceCode == "" {
		writeError(w, http.StatusBadGateway, "GitHub did not return a device code")
		return
	}
	expiresIn := jsonNum(resp, "expires_in")
	if expiresIn <= 0 {
		expiresIn = 900
	}
	interval := jsonNum(resp, "interval")
	if interval <= 0 {
		interval = 5
	}
	flowID := store.NewID()
	s.oauthMu.Lock()
	s.oauthFlows[flowID] = &oauthFlow{
		deviceCode: deviceCode,
		interval:   interval,
		expiresAt:  time.Now().Add(time.Duration(expiresIn) * time.Second),
	}
	s.oauthMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"flowId":          flowID,
		"userCode":        jsonStr(resp, "user_code"),
		"verificationUri": jsonStr(resp, "verification_uri"),
		"expiresIn":       expiresIn,
		"interval":        interval,
	})
}

// handleOAuthDevicePoll makes one token-poll attempt for an in-flight flow.
func (s *Server) handleOAuthDevicePoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FlowID string `json:"flowId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	s.oauthMu.Lock()
	flow, ok := s.oauthFlows[body.FlowID]
	if ok && time.Now().After(flow.expiresAt) {
		delete(s.oauthFlows, body.FlowID)
		ok = false
	}
	s.oauthMu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"status": "expired"})
		return
	}

	clientID := s.githubOAuthClientID()
	if clientID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": "no OAuth client ID configured"})
		return
	}
	status, resp, err := githubFormPost(r.Context(), accessTokenURL, url.Values{
		"client_id":   {clientID},
		"device_code": {flow.deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	if status != http.StatusOK {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "error",
			"error":  fmt.Sprintf("GitHub token endpoint returned HTTP %d", status),
		})
		return
	}

	flowErr := jsonStr(resp, "error")
	switch flowErr {
	case "":
		// success, handled below
	case "authorization_pending":
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
		return
	case "slow_down":
		writeJSON(w, http.StatusOK, map[string]any{"status": "slow_down"})
		return
	case "access_denied":
		s.deleteOAuthFlow(body.FlowID)
		writeJSON(w, http.StatusOK, map[string]any{"status": "denied"})
		return
	case "expired_token":
		s.deleteOAuthFlow(body.FlowID)
		writeJSON(w, http.StatusOK, map[string]any{"status": "expired"})
		return
	default:
		desc := jsonStr(resp, "error_description")
		if desc == "" {
			desc = flowErr
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": desc})
		return
	}

	token := jsonStr(resp, "access_token")
	if token == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": "GitHub returned no access token"})
		return
	}
	userID := s.currentUser(r).ID
	if err := s.st.SetUserSetting(userID, keyGitHubToken, token); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := s.st.SetUserSetting(userID, keyGitHubTokenSource, "oauth"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.deleteOAuthFlow(body.FlowID)
	login := ""
	if user, err := gitops.NewGHClient(token).GetUser(r.Context()); err == nil {
		login = user.Login
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "complete", "login": login})
}

func (s *Server) deleteOAuthFlow(flowID string) {
	s.oauthMu.Lock()
	delete(s.oauthFlows, flowID)
	s.oauthMu.Unlock()
}
