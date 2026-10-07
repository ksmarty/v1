package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIKeyHint(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want string
	}{
		{"empty", "", ""},
		{"too short to hint safely", "short", ""},
		{"short key exposes at most half", "abcdefgh", "abcd"},
		{"ten chars is the cap", "abcdefghijklmnopqrstuvwxyz", "abcdefghij"},
		{"never more than half", "abcdefghijkl", "abcdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := apiKeyHint(tc.key)
			if got != tc.want {
				t.Fatalf("apiKeyHint(%q) = %q, want %q", tc.key, got, tc.want)
			}
			if len(got) > 10 {
				t.Fatalf("hint %q exceeds 10 chars", got)
			}
			if len(tc.key) > 0 && len(got) >= len(tc.key) {
				t.Fatalf("hint %q exposes the whole key %q", got, tc.key)
			}
		})
	}
}

func TestSameBaseURL(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://api.example.com/v1", "https://api.example.com/v1/", true},
		{"https://API.example.com/v1", "https://api.example.com/v1", true},
		{" https://api.example.com/v1 ", "https://api.example.com/v1", true},
		{"https://api.example.com/v1", "https://api.example.com/v2", false},
		{"https://api.example.com/v1", "https://other.example.com/v1", false},
		{"", "https://api.example.com/v1", false},
	}
	for _, tc := range cases {
		if got := sameBaseURL(tc.a, tc.b); got != tc.want {
			t.Fatalf("sameBaseURL(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// saveProviders PUTs an llm.providers list and returns the decoded body.
func saveProviders(t *testing.T, s *Server, cookie, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body))
	req.Header.Set("Cookie", cookieHeader(cookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("put settings = %d, body %s", rr.Code, rr.Body.String())
	}
	return rr
}

// Replacing the key of the provider the effective config mirrors must also
// refresh the effective key. The first-run mirror only fires once, so without
// this the effective key keeps the replaced value — and every path that
// resolves without a provider id (retry, compact, the Custom provider) would
// keep sending the old key, which is indistinguishable from the replacement
// being ignored.
func TestProviderEditRefreshesEffectiveKey(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	admin, err := s.st.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}

	// First save: the provider is mirrored into the effective config.
	saveProviders(t, s, adminCookie, `{"llm":{"providers":[`+
		`{"id":"p1","name":"fake","baseURL":"https://api.example.com/v1","model":"m","apiKey":"first-key"}]}}`)
	if v, _, _ := s.st.GetUserSetting(admin.ID, keyLLMAPIKey); v != "first-key" {
		t.Fatalf("effective key after first save = %q, want first-key", v)
	}

	// Edit the same provider with a replacement key.
	saveProviders(t, s, adminCookie, `{"llm":{"providers":[`+
		`{"id":"p1","name":"fake","baseURL":"https://api.example.com/v1","model":"m","apiKey":"second-key"}]}}`)
	if v, _, _ := s.st.GetUserSetting(admin.ID, keyLLMAPIKey); v != "second-key" {
		t.Fatalf("effective key after edit = %q, want second-key", v)
	}

	// An empty incoming key still keeps the stored one.
	saveProviders(t, s, adminCookie, `{"llm":{"providers":[`+
		`{"id":"p1","name":"fake","baseURL":"https://api.example.com/v1","model":"m"}]}}`)
	if v, _, _ := s.st.GetUserSetting(admin.ID, keyLLMAPIKey); v != "second-key" {
		t.Fatalf("effective key after keyless edit = %q, want second-key", v)
	}

	// A provider on a different endpoint must not overwrite the effective key.
	saveProviders(t, s, adminCookie, `{"llm":{"providers":[`+
		`{"id":"p1","name":"fake","baseURL":"https://api.example.com/v1","model":"m","apiKey":"second-key"},`+
		`{"id":"p2","name":"other","baseURL":"https://other.example.com/v1","model":"m","apiKey":"other-key"}]}}`)
	if v, _, _ := s.st.GetUserSetting(admin.ID, keyLLMAPIKey); v != "second-key" {
		t.Fatalf("effective key after adding a second provider = %q, want second-key", v)
	}
}

// GET /api/settings must expose a prefix of each stored key so the UI can show
// which key is in use, without ever sending the key itself.
func TestSettingsExposeAPIKeyHint(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	const key = "sk-opencode-abcdefghijklmnop"
	saveProviders(t, s, adminCookie, fmt.Sprintf(
		`{"llm":{"providers":[{"id":"p1","name":"fake","baseURL":"https://api.example.com/v1","model":"m","apiKey":%q}]}}`, key))

	req := httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get settings = %d", rr.Code)
	}
	var got struct {
		LLM struct {
			APIKeySet  bool   `json:"apiKeySet"`
			APIKeyHint string `json:"apiKeyHint"`
			Providers  []struct {
				ID         string `json:"id"`
				APIKeySet  bool   `json:"apiKeySet"`
				APIKeyHint string `json:"apiKeyHint"`
			} `json:"providers"`
		} `json:"llm"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.LLM.Providers) != 1 {
		t.Fatalf("providers = %+v", got.LLM.Providers)
	}
	p := got.LLM.Providers[0]
	wantHint := key[:10] // the cap is 10 characters
	if !p.APIKeySet || p.APIKeyHint != wantHint {
		t.Fatalf("provider hint = %q (set=%v), want %q", p.APIKeyHint, p.APIKeySet, wantHint)
	}
	if !got.LLM.APIKeySet || got.LLM.APIKeyHint != wantHint {
		t.Fatalf("effective hint = %q (set=%v), want %q", got.LLM.APIKeyHint, got.LLM.APIKeySet, wantHint)
	}
	if strings.Contains(rr.Body.String(), key) {
		t.Fatal("the full key must never be serialized")
	}
}

// A retry must run on the provider the turn was sent with. Without a provider
// id the server falls back to the effective single-provider key, so a provider
// whose key was just replaced would keep being called with the old one — the
// upstream then rejects it (opencode answers "requires an active subscription")
// and the replacement looks like it never took effect.
func TestChatRetryUsesRequestedProviderKey(t *testing.T) {
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	s, adminCookie, _ := newAuthServer(t)

	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"retry-key"}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	var created struct{ ID string }
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	session, err := s.st.EnsureDefaultSession(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.AddMessage(created.ID, session.ID, "user", "hi", "", "m", "", "", ""); err != nil {
		t.Fatal(err)
	}

	// A saved provider holding the replacement key.
	saveProviders(t, s, adminCookie, fmt.Sprintf(
		`{"llm":{"providers":[{"id":"p1","name":"fake","baseURL":%q,"model":"m","apiKey":"new-key"}]}}`, upstream.URL))
	// Then make the effective single-provider key stale, so a fallback is visible.
	saveProviders(t, s, adminCookie, `{"llm":{"apiKey":"old-key"}}`)

	retry := func(query string) {
		seen = nil
		req := httptest.NewRequest("POST",
			"/api/projects/"+created.ID+"/chat/retry?sessionId="+session.ID+query, nil)
		req.Header.Set("Cookie", cookieHeader(adminCookie))
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	retry("&providerId=p1")
	if len(seen) == 0 {
		t.Fatal("the turn never reached the upstream server")
	}
	for _, a := range seen {
		if a != "Bearer new-key" {
			t.Fatalf("retry with providerId sent %q, want Bearer new-key", a)
		}
	}

	// Control: without a provider id the effective key is used, which is the
	// behaviour the UI must avoid by always sending the provider.
	retry("")
	if len(seen) == 0 {
		t.Fatal("the control turn never reached the upstream server")
	}
	for _, a := range seen {
		if a != "Bearer old-key" {
			t.Fatalf("retry without providerId sent %q, want Bearer old-key", a)
		}
	}
}
