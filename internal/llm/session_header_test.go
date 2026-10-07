package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
)

// opencode's zen endpoint refuses a request without its routing header, so the
// header name has to follow the endpoint rather than being opt-in per call
// site: every caller that builds a client for that endpoint must send it.
//
// The matching is by hostname, mirroring pi's own provider layer: opencode
// serves the same API under /zen/v1 and /zen/go/v1, and a path-keyed table
// missed the second one — which is how a deployment that worked under pi failed
// on v1 with a 400 naming no header.
func TestSessionHeaderForBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://opencode.ai/zen/v1":    "x-opencode-session",
		"https://opencode.ai/zen/go/v1": "x-opencode-session",
		"https://opencode.ai/zen/v1/":   "x-opencode-session",
		"https://opencode.ai":           "x-opencode-session",
		"https://OPENCODE.AI/zen/v1":    "x-opencode-session",
		"https://api.openai.com/v1":     "",
		"https://openrouter.ai/api/v1":  "",
		// A hostname is matched whole, not as a suffix: an unrelated domain
		// that merely ends in the same letters must not get the header.
		"https://notopencode.ai/zen/v1": "",
		"https://opencode.ai.evil.test": "",
		// A subdomain is a different hostname, and pi's matching agrees.
		"https://go.opencode.ai/zen/v1": "",
		"":                              "",
		"not a url":                     "",
	}
	for in, want := range cases {
		if got := SessionHeaderForBaseURL(in); got != want {
			t.Errorf("SessionHeaderForBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// Static headers travel with every request to the endpoint, independent of the
// session: opencode wants its client attribution alongside the routing header.
// The descriptor has to be able to express more than one header — a single-name
// field could not, which is exactly how the second header went missing.
func TestStaticHeadersForBaseURL(t *testing.T) {
	cases := map[string]map[string]string{
		"https://opencode.ai/zen/v1":    {"x-opencode-client": "v1"},
		"https://opencode.ai/zen/go/v1": {"x-opencode-client": "v1"},
		"https://OPENCODE.AI":           {"x-opencode-client": "v1"},
		"https://api.openai.com/v1":     nil,
		"https://openrouter.ai/api/v1":  nil,
		"":                              nil,
	}
	for in, want := range cases {
		got := StaticHeadersForBaseURL(in)
		if len(got) != len(want) {
			t.Errorf("StaticHeadersForBaseURL(%q) = %v, want %v", in, got, want)
			continue
		}
		for name, value := range want {
			if got[name] != value {
				t.Errorf("StaticHeadersForBaseURL(%q)[%q] = %q, want %q", in, name, got[name], value)
			}
		}
	}
}

// v1 sends its own client name, never pi's. pi's attribution headers assert
// pi's identity — one is a billing origin — so copying them would misattribute
// v1's traffic to another project. This guards that decision against a
// well-meaning "match pi exactly" edit.
func TestStaticHeadersDoNotImpersonatePi(t *testing.T) {
	hosts := []string{
		"https://opencode.ai/zen/v1",
		"https://opencode.ai/zen/go/v1",
		"https://openrouter.ai/api/v1",
		"https://integrate.api.nvidia.com/v1",
		"https://api.cloudflare.com/client/v4/accounts/x/ai/v1",
		"https://gateway.ai.cloudflare.com/v1/acc/gw",
	}
	piValues := []string{"pi", "Pi", "pi-coding-agent", "https://pi.dev"}
	forbidden := []string{"HTTP-Referer", "X-BILLING-INVOKE-ORIGIN", "X-OpenRouter-Title", "X-OpenRouter-Categories"}
	for _, host := range hosts {
		headers := StaticHeadersForBaseURL(host)
		for name, value := range headers {
			for _, piValue := range piValues {
				if value == piValue {
					t.Errorf("StaticHeadersForBaseURL(%q) sends %s: %q, which claims pi's identity", host, name, value)
				}
			}
		}
		for _, name := range forbidden {
			if _, ok := headers[name]; ok {
				t.Errorf("StaticHeadersForBaseURL(%q) sends pi's attribution header %s", host, name)
			}
		}
	}
}

// pi encodes endpoint quirks v1 cannot import: pi-coding-agent's package
// "exports" map publishes only ".", "./extensions" and "./package.json", so
// dist/core/provider-attribution.js is unreachable (ERR_PACKAGE_PATH_NOT_EXPORTED).
// The table above is therefore a mirror and can drift, so this fails when pi
// starts handling a host v1 has not considered — turning a silently missing
// header into a test failure. It skips when pi is not installed.
func TestEndpointHeaderHostsCoverPiAttributionHosts(t *testing.T) {
	const path = "/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/dist/core/provider-attribution.js"
	source, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("pi not installed at %s: %v", path, err)
	}
	piHosts := map[string]bool{}
	for _, m := range regexp.MustCompile(`const [A-Z_]+_HOST = "([^"]+)"`).FindAllStringSubmatch(string(source), -1) {
		piHosts[m[1]] = true
	}
	if len(piHosts) == 0 {
		t.Fatalf("parsed no hosts from %s — has the file moved or changed shape?", path)
	}

	// Hosts pi handles that v1 deliberately does not, each with its reason.
	documentedSkips := map[string]string{
		"openrouter.ai":             "pi sends identity attribution only (HTTP-Referer, X-OpenRouter-Title, X-OpenRouter-Categories)",
		"integrate.api.nvidia.com":  "pi sends X-BILLING-INVOKE-ORIGIN: Pi",
		"api.cloudflare.com":        "pi sends User-Agent: pi-coding-agent",
		"gateway.ai.cloudflare.com": "pi sends User-Agent: pi-coding-agent",
	}

	// v1 must never invent an endpoint quirk pi does not implement.
	for host := range endpointHeadersByHost {
		if !piHosts[host] {
			t.Errorf("v1 attaches headers to %q, which pi's provider-attribution.js does not know", host)
		}
	}
	// And every host pi knows must be a deliberate decision here.
	for host := range piHosts {
		_, mirrored := endpointHeadersByHost[host]
		_, skipped := documentedSkips[host]
		if !mirrored && !skipped {
			t.Errorf("pi handles %q but v1 neither mirrors it nor documents why it is skipped", host)
		}
	}
	// A skip that pi has dropped is stale documentation.
	for host := range documentedSkips {
		if !piHosts[host] {
			t.Errorf("v1 documents skipping %q, which pi no longer handles", host)
		}
	}
}

// NewClient is the single construction point, so an endpoint that needs the
// header gets it without its callers knowing.
func TestNewClientTakesSessionHeaderFromBaseURL(t *testing.T) {
	c := NewClient("https://opencode.ai/zen/v1/", "k", "m")
	if c.SessionHeader != "x-opencode-session" {
		t.Fatalf("SessionHeader = %q", c.SessionHeader)
	}
	if c.BaseURL != "https://opencode.ai/zen/v1" {
		t.Fatalf("BaseURL = %q", c.BaseURL)
	}
	if other := NewClient("https://api.openai.com/v1", "k", "m"); other.SessionHeader != "" {
		t.Fatalf("an endpoint that needs no header got %q", other.SessionHeader)
	}
}

// The header has to reach the wire on the streaming path the chat turn uses,
// with the conversation's own session as the value.
func TestChatStreamSendsSessionHeader(t *testing.T) {
	var got string
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("x-opencode-session")
		seen = true
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "k", "m")
	client.SessionHeader = "x-opencode-session"
	client.SessionID = "sess-42"
	if _, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil, nil); err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if !seen {
		t.Fatal("the endpoint was never called")
	}
	if got != "sess-42" {
		t.Fatalf("x-opencode-session = %q, want the session id", got)
	}
}

// With no session id the model stands in, so the request is still routable;
// and an endpoint that needs no header must not receive one.
func TestChatStreamSessionHeaderFallback(t *testing.T) {
	var got string
	var present bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, present = r.Header.Get("x-opencode-session"), r.Header.Values("x-opencode-session") != nil
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "k", "the-model")
	client.SessionHeader = "x-opencode-session"
	if _, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil, nil); err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if got != "the-model" {
		t.Fatalf("fallback session = %q, want the model", got)
	}

	plain := NewClient(srv.URL, "k", "m")
	if _, err := plain.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil, nil); err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if present {
		t.Fatal("an endpoint that needs no routing header must not get one")
	}
}

// The static headers have to reach the wire on the streaming path the chat turn
// uses, not just be carried on the client.
func TestChatStreamSendsStaticHeaders(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("x-opencode-client")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "k", "m")
	client.StaticHeaders = StaticHeadersForBaseURL("https://opencode.ai/zen/go/v1")
	if _, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got != "v1" {
		t.Fatalf("x-opencode-client = %q, want v1", got)
	}
}
