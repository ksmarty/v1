package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
