// Package config reads environment configuration once at startup.
package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds all process configuration, read once from the environment.
type Config struct {
	Port                int
	DataDir             string
	AuthDisabled        bool
	Password            string
	OpenAIKey           string
	OpenAIBase          string
	Model               string
	GitHubToken         string
	GitHubOAuthClientID string
	VercelToken         string
	VercelClientID      string
	VercelClientSecret  string
	VercelRedirectURI   string
	AuthOIDCEnabled     bool
	OIDCIssuer          string
	OIDCClientID        string
	OIDCClientSecret    string
	OIDCRedirectURI     string
	OIDCAllowedEmails   []string
	MaxPreviews         int
	Version             string
}

// Load reads configuration from environment variables.
func Load(version string) Config {
	c := Config{
		Port:        8080,
		DataDir:     "./data",
		OpenAIBase:  "https://api.openai.com/v1",
		Model:       "gpt-4o",
		MaxPreviews: 3,
		Version:     version,
	}
	if v := os.Getenv("V1_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
			c.Port = n
		}
	}
	if v := os.Getenv("V1_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("V1_AUTH_DISABLED"); v == "true" || v == "1" || v == "yes" {
		c.AuthDisabled = true
	}
	c.Password = os.Getenv("V1_PASSWORD")
	c.OpenAIKey = os.Getenv("OPENAI_API_KEY")
	if v := os.Getenv("OPENAI_BASE_URL"); v != "" {
		c.OpenAIBase = v
	}
	if v := os.Getenv("V1_MODEL"); v != "" {
		c.Model = v
	}
	if v := os.Getenv("V1_GITHUB_TOKEN"); v != "" {
		c.GitHubToken = v
	} else {
		c.GitHubToken = os.Getenv("GITHUB_TOKEN")
	}
	c.GitHubOAuthClientID = os.Getenv("V1_GITHUB_OAUTH_CLIENT_ID")
	c.VercelToken = os.Getenv("V1_VERCEL_TOKEN")
	c.VercelClientID = os.Getenv("V1_VERCEL_CLIENT_ID")
	c.VercelClientSecret = os.Getenv("V1_VERCEL_CLIENT_SECRET")
	c.VercelRedirectURI = os.Getenv("V1_VERCEL_REDIRECT_URI")
	if v := os.Getenv("V1_AUTH_OIDC_ENABLED"); v == "true" || v == "1" || v == "yes" {
		c.AuthOIDCEnabled = true
	}
	c.OIDCIssuer = os.Getenv("V1_OIDC_ISSUER")
	c.OIDCClientID = os.Getenv("V1_OIDC_CLIENT_ID")
	c.OIDCClientSecret = os.Getenv("V1_OIDC_CLIENT_SECRET")
	c.OIDCRedirectURI = os.Getenv("V1_OIDC_REDIRECT_URI")
	if v := os.Getenv("V1_OIDC_ALLOWED_EMAILS"); v != "" {
		for _, part := range strings.Split(v, ",") {
			if e := strings.TrimSpace(part); e != "" {
				c.OIDCAllowedEmails = append(c.OIDCAllowedEmails, e)
			}
		}
	}
	if v := os.Getenv("V1_MAX_PREVIEWS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxPreviews = n
		}
	}
	return c
}
