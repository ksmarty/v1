package agent

import (
	"strings"
	"testing"
)

func envHas(env []string, key string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return true
		}
	}
	return false
}

// The GitHub token is handed to a command only when that command runs gh.
func TestCommandEnvAddsGitHubTokenOnlyForGH(t *testing.T) {
	const token = "ghp_example"
	for _, cmd := range []string{
		"gh pr list",
		"cd app && gh issue view 1",
		"git push && gh pr create --fill",
	} {
		if !envHas(commandEnv(cmd, token), "GH_TOKEN") {
			t.Fatalf("%q did not get GH_TOKEN", cmd)
		}
	}
	// Handing it to every command would print a live token from any `env` the
	// agent runs while debugging, or from any project script.
	for _, cmd := range []string{"npm test", "git status", "echo ghp", "which ghi"} {
		if envHas(commandEnv(cmd, token), "GH_TOKEN") {
			t.Fatalf("%q was given GH_TOKEN", cmd)
		}
	}
	// With no token configured nothing is added, so gh reports itself as
	// unauthenticated instead of being handed an empty credential.
	if envHas(commandEnv("gh pr list", ""), "GH_TOKEN") {
		t.Fatal("an empty token was injected")
	}
}

// Defence in depth: v1's own credentials stay out of child processes, so a
// project script that prints its environment prints nothing useful.
func TestChildEnvDropsV1Credentials(t *testing.T) {
	t.Setenv("V1_GITHUB_TOKEN", "secret")
	t.Setenv("V1_PASSWORD", "secret")
	t.Setenv("V1_KEEP_ME", "1")
	env := childEnv()
	if envHas(env, "V1_GITHUB_TOKEN") || envHas(env, "V1_PASSWORD") {
		t.Fatal("a credential survived into the child environment")
	}
	if !envHas(env, "V1_KEEP_ME") {
		t.Fatal("an unrelated variable was dropped")
	}
}
