package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Disabling a provider must not delete its key: the point of the flag is a
// temporary switch-off, so the credential has to survive a round trip.
func TestProviderDisabledSurvivesSaveAndSettings(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	saveProviders(t, s, adminCookie, `{"llm":{"providers":[`+
		`{"id":"p1","name":"off","baseURL":"https://api.example.com/v1","model":"m","apiKey":"secret-key"},`+
		`{"id":"p2","name":"on","baseURL":"https://other.example.com/v1","model":"m","apiKey":"other-key"}]}}`)

	// Disable p1 the way the UI does: resend the list with the flag set on one
	// entry and no key for it (the form never echoes stored keys back).
	saveProviders(t, s, adminCookie, `{"llm":{"providers":[`+
		`{"id":"p1","name":"off","baseURL":"https://api.example.com/v1","model":"m","disabled":true},`+
		`{"id":"p2","name":"on","baseURL":"https://other.example.com/v1","model":"m"}]}}`)

	req := httptest.NewRequest("GET", "/api/settings", nil)
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get settings = %d", rr.Code)
	}
	var got struct {
		LLM struct {
			Providers []struct {
				ID        string `json:"id"`
				Disabled  bool   `json:"disabled"`
				APIKeySet bool   `json:"apiKeySet"`
			} `json:"providers"`
		} `json:"llm"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.LLM.Providers) != 2 {
		t.Fatalf("providers = %+v", got.LLM.Providers)
	}
	byID := map[string]struct {
		Disabled  bool
		APIKeySet bool
	}{}
	for _, p := range got.LLM.Providers {
		byID[p.ID] = struct {
			Disabled  bool
			APIKeySet bool
		}{p.Disabled, p.APIKeySet}
	}
	if !byID["p1"].Disabled {
		t.Fatal("p1 should report disabled")
	}
	if !byID["p1"].APIKeySet {
		t.Fatal("disabling p1 must keep its stored key")
	}
	if byID["p2"].Disabled {
		t.Fatal("p2 should stay enabled")
	}
}

// A disabled provider is not offered by the picker catalog: the model picker
// lists providers from GET /api/providers, and offering one the server will
// refuse is how a chat gets sent into an error.
func TestDisabledProviderIsHiddenFromProviderCatalog(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	saveProviders(t, s, adminCookie, `{"llm":{"providers":[`+
		`{"id":"off1","name":"off","baseURL":"https://off.example.com/v1","model":"m","apiKey":"k","disabled":true},`+
		`{"id":"on1","name":"on","baseURL":"https://on.example.com/v1","model":"m","apiKey":"k"}]}}`)

	req := httptest.NewRequest("GET", "/api/providers", nil)
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get providers = %d", rr.Code)
	}
	var got struct {
		Providers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var haveOn, haveOff bool
	for _, p := range got.Providers {
		switch p.ID {
		case "on1":
			haveOn = true
		case "off1":
			haveOff = true
		}
	}
	if !haveOn {
		t.Fatal("the enabled saved provider must be listed")
	}
	if haveOff {
		t.Fatal("a disabled saved provider must not be listed")
	}
}

// A turn asking for a disabled provider is refused with a message naming it,
// rather than silently falling back to another endpoint.
func TestChatRefusesDisabledProvider(t *testing.T) {
	var hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	s, adminCookie, _ := newAuthServer(t)
	saveProviders(t, s, adminCookie, `{"llm":{"providers":[{"id":"p1","name":"off","baseURL":"`+upstream.URL+
		`","model":"m","apiKey":"k","disabled":true}]}}`)

	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"disabled-provider"}`))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	var created struct{ ID string }
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	body := `{"message":"hi","providerId":"p1","model":"m"}`
	req = httptest.NewRequest("POST", "/api/projects/"+created.ID+"/chat", strings.NewReader(body))
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("chat with a disabled provider = %d (%s), want 400", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "disabled") || !strings.Contains(rr.Body.String(), "off") {
		t.Fatalf("error should name the disabled provider, got %s", rr.Body.String())
	}
	if hits != 0 {
		t.Fatalf("a disabled provider reached the upstream %d times", hits)
	}
}

// A saved provider whose endpoint publishes no model list keeps a nil slice,
// which serializes as `null` — and every client read of provider.models then
// throws (the picker crashed with "models is not iterable"). The catalog must
// always carry a JSON array.
func TestProviderCatalogAlwaysCarriesModelArrays(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	// Port 1 refuses immediately, so the live-model fetch fails fast.
	saveProviders(t, s, adminCookie, `{"llm":{"providers":[{"id":"dead1","name":"dead","baseURL":"http://127.0.0.1:1/v1","model":"m","apiKey":"k"}]}}`)

	req := httptest.NewRequest("GET", "/api/providers", nil)
	req.Header.Set("Cookie", cookieHeader(adminCookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get providers = %d", rr.Code)
	}
	var got struct {
		Providers []struct {
			ID     string          `json:"id"`
			Models json.RawMessage `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) == 0 {
		t.Fatal("no providers listed")
	}
	for _, p := range got.Providers {
		if len(p.Models) == 0 || string(p.Models) == "null" {
			t.Fatalf("provider %q serialized models=%s, want a JSON array", p.ID, string(p.Models))
		}
	}
}
