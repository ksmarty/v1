package mcp

// OAuth support for remote MCP servers: protected-resource metadata (RFC 9728),
// authorization-server metadata (RFC 8414), PKCE (RFC 7636), dynamic client
// registration (RFC 7591) and the token/refresh grants (RFC 6749). Remote MCP
// servers routinely require this — without it a server that answers 401 is
// simply unusable, because there is no way to obtain a token.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuthConfig is what discovery learned about a server's authorization.
type OAuthConfig struct {
	// ResourceMetadata is the URL the metadata was read from.
	ResourceMetadata string
	// Resource is the protected resource identifier, echoed on the token
	// requests (RFC 8707) so a token cannot be replayed at another server.
	Resource              string
	AuthorizationServer   string
	AuthorizationEndpoint string
	TokenEndpoint         string
	RegistrationEndpoint  string
	Scopes                []string
}

// OAuthTokens is a stored grant for one server.
type OAuthTokens struct {
	ClientID     string    `json:"clientId,omitempty"`
	ClientSecret string    `json:"clientSecret,omitempty"`
	AccessToken  string    `json:"accessToken,omitempty"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	TokenURL     string    `json:"tokenUrl,omitempty"`
	Resource     string    `json:"resource,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

// Valid reports whether the access token can still be used. Tokens are treated
// as expired a minute early so a call cannot start with one that dies mid-flight.
func (t OAuthTokens) Valid(now time.Time) bool {
	if t.AccessToken == "" {
		return false
	}
	if t.Expiry.IsZero() {
		return true
	}
	return now.Before(t.Expiry.Add(-time.Minute))
}

// CanRefresh reports whether an expired token can be renewed without the user.
func (t OAuthTokens) CanRefresh() bool {
	return t.RefreshToken != "" && t.TokenURL != ""
}

// PKCE is a code verifier and its challenge.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE generates a fresh verifier and its S256 challenge.
func NewPKCE() (PKCE, error) {
	verifier, err := RandomToken(32)
	if err != nil {
		return PKCE{}, err
	}
	return PKCE{Verifier: verifier, Challenge: challengeFor(verifier)}, nil
}

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RandomToken returns n random bytes as base64url without padding.
func RandomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// oauthHTTPClient is used for the metadata and token calls. These are quick
// request/response exchanges, unlike the MCP calls themselves.
func oauthHTTPClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// getJSON fetches a URL and decodes a JSON object, bounded so a hostile or
// broken endpoint cannot stream unbounded data into memory.
func getJSON(ctx context.Context, client *http.Client, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %s", rawURL, res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}

// DiscoverOAuth finds the authorization server for a protected MCP endpoint.
// It tries the well-known locations in the order RFC 9728 recommends: the
// resource's own path first, then the origin.
func DiscoverOAuth(ctx context.Context, client *http.Client, serverURL string) (*OAuthConfig, error) {
	client = oauthHTTPClient(client)
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid server url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	origin := u.Scheme + "://" + u.Host
	path := strings.TrimSuffix(u.Path, "/")

	var meta struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	candidates := []string{origin + "/.well-known/oauth-protected-resource" + path}
	if path != "" {
		candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")
	}
	var lastErr error
	found := false
	for _, c := range candidates {
		if err := getJSON(ctx, client, c, &meta); err != nil {
			lastErr = err
			continue
		}
		found = true
		break
	}
	if !found {
		if lastErr == nil {
			lastErr = errors.New("no protected resource metadata")
		}
		return nil, fmt.Errorf("this server did not publish OAuth metadata (%w)", lastErr)
	}
	if len(meta.AuthorizationServers) == 0 {
		return nil, errors.New("the OAuth metadata lists no authorization server")
	}

	cfg := &OAuthConfig{
		ResourceMetadata:    candidates[0],
		Resource:            meta.Resource,
		AuthorizationServer: meta.AuthorizationServers[0],
		Scopes:              meta.ScopesSupported,
	}
	if cfg.Resource == "" {
		cfg.Resource = serverURL
	}

	as := strings.TrimSuffix(cfg.AuthorizationServer, "/")
	var asMeta struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		ScopesSupported       []string `json:"scopes_supported"`
	}
	asCandidates := []string{
		as + "/.well-known/oauth-authorization-server",
		as + "/.well-known/openid-configuration",
	}
	found = false
	for _, c := range asCandidates {
		if err := getJSON(ctx, client, c, &asMeta); err != nil {
			lastErr = err
			continue
		}
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("could not read the authorization server metadata (%w)", lastErr)
	}
	cfg.AuthorizationEndpoint = asMeta.AuthorizationEndpoint
	cfg.TokenEndpoint = asMeta.TokenEndpoint
	cfg.RegistrationEndpoint = asMeta.RegistrationEndpoint
	if len(asMeta.ScopesSupported) > 0 && len(cfg.Scopes) == 0 {
		cfg.Scopes = asMeta.ScopesSupported
	}
	if cfg.AuthorizationEndpoint == "" || cfg.TokenEndpoint == "" {
		return nil, errors.New("the authorization server metadata is missing its endpoints")
	}
	return cfg, nil
}

// RegisterClient performs dynamic client registration, returning credentials
// the flow can use. Servers that do not support registration return an error.
func RegisterClient(ctx context.Context, client *http.Client, cfg *OAuthConfig, redirectURI, clientName string) (string, string, error) {
	if cfg.RegistrationEndpoint == "" {
		return "", "", errors.New("this server does not support automatic client registration; configure a client id manually")
	}
	body := map[string]any{
		"client_name":                clientName,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "client_secret_post",
	}
	if len(cfg.Scopes) > 0 {
		body["scope"] = strings.Join(cfg.Scopes, " ")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.RegistrationEndpoint, strings.NewReader(string(payload)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := oauthHTTPClient(client).Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", "", fmt.Errorf("client registration failed: %s: %s", res.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", fmt.Errorf("client registration returned invalid JSON: %w", err)
	}
	if out.ClientID == "" {
		return "", "", errors.New("client registration returned no client id")
	}
	return out.ClientID, out.ClientSecret, nil
}

// AuthorizeURL builds the URL the browser is sent to.
func AuthorizeURL(cfg *OAuthConfig, clientID, redirectURI, state, challenge string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if len(cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	if cfg.Resource != "" {
		q.Set("resource", cfg.Resource)
	}
	sep := "?"
	if strings.Contains(cfg.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return cfg.AuthorizationEndpoint + sep + q.Encode()
}

// postToken performs a form-encoded token request and decodes the grant.
func postToken(ctx context.Context, client *http.Client, tokenURL string, form url.Values) (*OAuthTokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := oauthHTTPClient(client).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("token request failed: %s: %s", res.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		TokenType        string `json:"token_type"`
		Scope            string `json:"scope"`
		ExpiresIn        int64  `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("token response was not JSON: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("token request rejected: %s %s", out.Error, out.ErrorDescription)
	}
	if out.AccessToken == "" {
		return nil, errors.New("token response contained no access token")
	}
	tokens := &OAuthTokens{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		TokenURL:     tokenURL,
		Scope:        out.Scope,
	}
	if out.ExpiresIn > 0 {
		tokens.Expiry = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return tokens, nil
}

// ExchangeCode trades the authorization code for tokens.
func ExchangeCode(ctx context.Context, client *http.Client, cfg *OAuthConfig, code, verifier, clientID, clientSecret, redirectURI string) (*OAuthTokens, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	if cfg.Resource != "" {
		form.Set("resource", cfg.Resource)
	}
	tokens, err := postToken(ctx, client, cfg.TokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	tokens.ClientID = clientID
	tokens.ClientSecret = clientSecret
	tokens.Resource = cfg.Resource
	return tokens, nil
}

// RefreshAccessToken renews a grant without user interaction.
func RefreshAccessToken(ctx context.Context, client *http.Client, prev OAuthTokens) (*OAuthTokens, error) {
	if !prev.CanRefresh() {
		return nil, errors.New("this grant has no refresh token")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {prev.RefreshToken},
	}
	if prev.ClientID != "" {
		form.Set("client_id", prev.ClientID)
	}
	if prev.ClientSecret != "" {
		form.Set("client_secret", prev.ClientSecret)
	}
	if prev.Resource != "" {
		form.Set("resource", prev.Resource)
	}
	tokens, err := postToken(ctx, client, prev.TokenURL, form)
	if err != nil {
		return nil, err
	}
	// Servers are not required to resend the refresh token or the scope.
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = prev.RefreshToken
	}
	if tokens.Scope == "" {
		tokens.Scope = prev.Scope
	}
	tokens.ClientID = prev.ClientID
	tokens.ClientSecret = prev.ClientSecret
	tokens.Resource = prev.Resource
	return tokens, nil
}
