package agent

import (
	"os"
	"strings"
)

// secretEnvNames are the environment variables v1 reads its own credentials
// from (see config.Load). They are removed from the environment handed to
// commands the agent runs.
//
// This is defence in depth, not the guarantee. An agent that dumps
// /proc/self/environ of its own shell now sees nothing, but it can still read
// /proc/1/environ of the v1 server, so the value-level redaction in the
// sanitize package remains what actually stops a secret reaching a transcript.
// Keeping credentials out of child processes removes the common accident: a
// project script or a debugging command that prints its environment.
var secretEnvNames = map[string]bool{
	"V1_PASSWORD":                   true,
	"V1_GITHUB_TOKEN":               true,
	"GITHUB_TOKEN":                  true,
	"V1_GITHUB_OAUTH_CLIENT_SECRET": true,
	"V1_VERCEL_TOKEN":               true,
	"V1_VERCEL_CLIENT_SECRET":       true,
	"V1_VERCEL_REFRESH_TOKEN":       true,
	"V1_OIDC_CLIENT_SECRET":         true,
	"OPENAI_API_KEY":                true,
}

// childEnv is the environment for a command the agent runs: the server's
// environment minus v1's own credentials.
func childEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 && secretEnvNames[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
