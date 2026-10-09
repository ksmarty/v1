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

// commandEnv is childEnv plus the GitHub token, for a command that runs the
// GitHub CLI. gh reads its credentials from GH_TOKEN (GITHUB_TOKEN as a
// fallback), so handing it the token is what makes `gh pr create` work.
//
// The token is added only when the command line actually invokes gh. Injecting
// it into every command would undo the point of keeping credentials out of
// child processes: any project script, or an `env` the agent runs while
// debugging, would print a live token. Value-level redaction in the sanitize
// package would still stop it reaching a transcript, but there is no reason to
// hand it out. The check is a word match, so `cd app && gh pr list` works; a gh
// invoked indirectly, through a script of the user's own, is not authenticated.
func commandEnv(cmdline, token string) []string {
	env := childEnv()
	if token == "" || !invokesGH(cmdline) {
		return env
	}
	return append(env, "GH_TOKEN="+token, "GITHUB_TOKEN="+token)
}

func invokesGH(cmdline string) bool {
	for _, f := range strings.Fields(cmdline) {
		if f == "gh" {
			return true
		}
	}
	return false
}
