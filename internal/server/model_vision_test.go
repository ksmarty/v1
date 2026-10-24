package server

import (
	"encoding/json"
	"testing"

	"v1/internal/llm"
)

// visionTestServer installs a catalog cache holding one provider whose models
// exercise the id spellings and capability flags the vision gate has to tell
// apart.
func visionTestServer(t *testing.T) *Server {
	t.Helper()
	s, _, _ := newHarnessTestServer(t)
	cat := &llm.Catalog{Providers: []llm.Provider{
		{ID: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Models: []llm.ProviderModel{
			{ID: "moonshotai/kimi-k3", Name: "Kimi K3", ImageInput: true},
			{ID: "deepseek/deepseek-chat", Name: "DeepSeek Chat"},
		}},
		{ID: "opencode-go", BaseURL: "https://opencode.ai/zen/go/v1", Models: []llm.ProviderModel{
			{ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", ImageInput: true},
			{ID: "glm-5.2", Name: "GLM 5.2"},
		}},
	}}
	raw, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetSetting(keyProvidersCache, string(raw)); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestModelSupportsImages(t *testing.T) {
	s := visionTestServer(t)
	cases := []struct {
		name       string
		providerID string
		model      string
		want       bool
	}{
		{"namespaced id on its own provider", "openrouter", "moonshotai/kimi-k3", true},
		{"bare id for a namespaced catalog entry", "openrouter", "kimi-k3", true},
		{"exact id on its own provider", "opencode-go", "deepseek-v4.1-flash", true},
		{"known text-only model", "opencode-go", "glm-5.2", false},
		{"known text-only namespaced model", "openrouter", "deepseek/deepseek-chat", false},
		// A configured provider whose picker list came from its live /models
		// endpoint, so no catalog entry carries its modalities.
		{"uncatalogued model", "opencode-go", "brand-new-vision-model", true},
		{"unknown provider", "", "kimi-k3", true},
		// A user-added provider carries a generated id, never a catalog id; the
		// model's capability still comes from the entry naming it.
		{"user-added provider", "0ed6d90a-f9df-497c-9b9f-36da52728d7a", "kimi-k3", true},
		{"user-added provider, text-only model", "0ed6d90a-f9df-497c-9b9f-36da52728d7a", "glm-5.2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.modelSupportsImages("test-user", tc.providerID, tc.model); got != tc.want {
				t.Fatalf("modelSupportsImages(%q, %q) = %v, want %v", tc.providerID, tc.model, got, tc.want)
			}
		})
	}
}

// The provider-scoped answer wins: a bare id may name a different model under
// another provider, and that provider's entry is the one that describes it.
func TestModelSupportsImagesPrefersTheTurnsProvider(t *testing.T) {
	s := visionTestServer(t)
	if !s.modelSupportsImages("test-user", "opencode-go", "deepseek-v4.1-flash") {
		t.Fatal("the turn's provider knows the model as vision-capable")
	}
	// Unknown within a known provider: the catalog has no entry there, so the
	// capability is unknown rather than borrowed from a same-named model.
	if !s.modelSupportsImages("test-user", "openrouter", "deepseek-v4.1-flash") {
		t.Fatal("an unknown id must not read as text-only")
	}
}
