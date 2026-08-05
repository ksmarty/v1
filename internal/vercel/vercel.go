// Package vercel is a minimal client for the Vercel REST API: account OAuth,
// file deployments and deployment status polling.
package vercel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	apiBase  = "https://api.vercel.com"
	authBase = "https://vercel.com"
)

// Deployment is a Vercel deployment in its terminal or building state.
type Deployment struct {
	ID         string `json:"id"`
	State      string `json:"state"` // readyState, e.g. READY / BUILDING / ERROR
	URL        string `json:"url"`
	CreatedAt  int64  `json:"createdAt"`
	Production bool   `json:"production"`
}

// DeployFile is one project file to upload (raw bytes; base64'd on the wire).
type DeployFile struct {
	File string
	Data []byte
}

// OAuthToken is the response of Vercel's token endpoints.
type OAuthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
}

// Client talks to the Vercel API with a bearer token. RefreshToken/ClientID/
// ClientSecret are optional; when set, a 401 triggers a single token refresh.
type Client struct {
	Token        string
	RefreshToken string
	ClientID     string
	ClientSecret string
	HTTP         *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) doOnce(ctx context.Context, method, path string, query url.Values, body any) (int, []byte, error) {
	u := apiBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	var contentType string
	switch b := body.(type) {
	case nil:
	case []byte:
		rdr = bytes.NewReader(b)
		contentType = "application/octet-stream"
	case url.Values:
		rdr = strings.NewReader(b.Encode())
		contentType = "application/x-www-form-urlencoded"
	default:
		data, err := json.Marshal(b)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(data)
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return 0, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, data, apiError(resp.StatusCode, data)
	}
	return resp.StatusCode, data, nil
}

// do performs a request, transparently refreshing the token once on a 401.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (int, []byte, error) {
	status, data, err := c.doOnce(ctx, method, path, query, body)
	if status == http.StatusUnauthorized && c.RefreshToken != "" && c.ClientID != "" && c.ClientSecret != "" {
		tok, terr := exchangeToken(ctx, c.ClientID, c.ClientSecret, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {c.RefreshToken},
		})
		if terr == nil && tok != nil && tok.AccessToken != "" {
			c.Token = tok.AccessToken
			if tok.RefreshToken != "" {
				c.RefreshToken = tok.RefreshToken
			}
			return c.doOnce(ctx, method, path, query, body)
		}
	}
	return status, data, err
}

// apiError converts a non-2xx response into a readable error, preferring the
// Vercel `{"error": {"message": ...}}` shape.
func apiError(status int, data []byte) error {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &e); err == nil && e.Error.Message != "" {
		msg := e.Error.Message
		if e.Error.Code != "" {
			msg += " (" + e.Error.Code + ")"
		}
		return fmt.Errorf("vercel: HTTP %d: %s", status, msg)
	}
	msg := strings.TrimSpace(string(data))
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("vercel: HTTP %d: %s", status, msg)
}

// ---- OAuth ----

// AuthorizeURL builds the Vercel authorization URL for an OAuth app.
func AuthorizeURL(clientID, redirectURI, state string) string {
	q := url.Values{
		"client_id":    {clientID},
		"redirect_uri": {redirectURI},
		"state":        {state},
	}
	return authBase + "/oauth/authorize?" + q.Encode()
}

// exchangeToken posts credentials to Vercel's token endpoint (code or refresh
// grant, plus client id/secret).
func exchangeToken(ctx context.Context, clientID, clientSecret string, form url.Values) (*OAuthToken, error) {
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v2/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiError(resp.StatusCode, data)
	}
	var tok OAuthToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("vercel: decoding token response: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("vercel: token endpoint returned no access token")
	}
	return &tok, nil
}

// ExchangeCode trades an authorization code for an access token.
func ExchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (*OAuthToken, error) {
	return exchangeToken(ctx, clientID, clientSecret, url.Values{
		"code":         {code},
		"redirect_uri": {redirectURI},
	})
}

// ---- account ----

// User returns the username of the authenticated account.
func (c *Client) User(ctx context.Context) (string, error) {
	_, data, err := c.do(ctx, http.MethodGet, "/www/user", nil, nil)
	if err != nil {
		return "", err
	}
	var u struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := json.Unmarshal(data, &u); err != nil {
		return "", err
	}
	if u.User.Username == "" {
		return "", fmt.Errorf("vercel: user response missing username")
	}
	return u.User.Username, nil
}

// ---- deployments ----

// deployResponse is the shape of POST /v13/deployments and the per-deployment
// GET detail response (which uses `id`).
type deployResponse struct {
	ID         string  `json:"id"`
	URL        string  `json:"url"`
	ReadyState string  `json:"readyState"`
	Created    int64   `json:"created"`
	Target     *string `json:"target"`
}

// Deploy uploads the given files and waits (polling every 2s, up to 10
// minutes) for the deployment to reach a terminal state.
func (c *Client) Deploy(ctx context.Context, name string, files []DeployFile, target string) (*Deployment, error) {
	payload := make([]map[string]string, 0, len(files))
	for _, f := range files {
		payload = append(payload, map[string]string{
			"file": f.File,
			"data": base64.StdEncoding.EncodeToString(f.Data),
		})
	}
	body := map[string]any{
		"name":  name,
		"files": payload,
		"projectSettings": map[string]any{
			"framework":                   nil,
			"buildCommand":                nil,
			"installCommand":              nil,
			"devCommand":                  nil,
			"outputDirectory":             nil,
			"commandForIgnoringBuildStep": nil,
		},
	}
	if target != "" {
		body["target"] = target
	}
	_, data, err := c.do(ctx, http.MethodPost, "/v13/deployments", nil, body)
	if err != nil {
		return nil, err
	}
	var created deployResponse
	if err := json.Unmarshal(data, &created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, fmt.Errorf("vercel: deployment response missing id")
	}
	return c.poll(ctx, created.ID)
}

// poll waits for a deployment to leave BUILDING/QUEUED/INITIALIZING.
func (c *Client) poll(ctx context.Context, id string) (*Deployment, error) {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		_, data, err := c.do(ctx, http.MethodGet, "/v13/deployments/"+id, nil, nil)
		if err != nil {
			return nil, err
		}
		var d deployResponse
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		dep := &Deployment{
			ID:         d.ID,
			State:      d.ReadyState,
			URL:        d.URL,
			CreatedAt:  d.Created,
			Production: d.Target != nil && *d.Target == "production",
		}
		switch d.ReadyState {
		case "READY", "ERROR", "ERROR_BUILDING", "ERROR_INSTALLING", "ERROR_DEPLOYING", "CANCELED":
			return dep, nil
		case "":
			// transient — keep polling
		}
		if time.Now().After(deadline) {
			return dep, fmt.Errorf("vercel: timed out waiting for deployment to finish")
		}
		select {
		case <-ctx.Done():
			return dep, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ListDeployments returns the most recent deployments of a project.
func (c *Client) ListDeployments(ctx context.Context, project string, limit int) ([]Deployment, error) {
	q := url.Values{"project": {project}, "limit": {strconv.Itoa(limit)}}
	_, data, err := c.do(ctx, http.MethodGet, "/v13/deployments", q, nil)
	if err != nil {
		return nil, err
	}
	var list struct {
		Deployments []struct {
			UID        string  `json:"uid"`
			URL        string  `json:"url"`
			ReadyState string  `json:"readyState"`
			Created    int64   `json:"created"`
			Target     *string `json:"target"`
		} `json:"deployments"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]Deployment, 0, len(list.Deployments))
	for _, d := range list.Deployments {
		out = append(out, Deployment{
			ID:         d.UID,
			State:      d.ReadyState,
			URL:        d.URL,
			CreatedAt:  d.Created,
			Production: d.Target != nil && *d.Target == "production",
		})
	}
	return out, nil
}
