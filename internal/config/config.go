// Package config reads environment configuration once at startup.
package config

import (
	"os"
	"strconv"
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
	if v := os.Getenv("V1_MAX_PREVIEWS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxPreviews = n
		}
	}
	return c
}
