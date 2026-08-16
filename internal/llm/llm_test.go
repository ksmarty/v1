package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != "test-model" {
			t.Errorf("model = %q, want %q", request.Model, "test-model")
		}
		if len(request.Messages) != 1 || request.Messages[0].Role != "user" || request.Messages[0].Content != "hello" {
			t.Errorf("messages = %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"world"}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "test-model")
	got, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if got != "world" {
		t.Errorf("Complete() = %q, want %q", got, "world")
	}
}

func TestExtractErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "wrapped provider error",
			body: `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Error from provider (Console): Rate limit exceeded. Please try again later."}}`,
			want: "Error from provider (Console): Rate limit exceeded. Please try again later.",
		},
		{
			name: "openai shape",
			body: `{"error":{"message":"Invalid API key","type":"invalid_request_error"}}`,
			want: "Invalid API key",
		},
		{
			name: "top-level message",
			body: `{"message":"model not found"}`,
			want: "model not found",
		},
		{
			name: "sse prefixed",
			body: `data: {"error":{"message":"overloaded"}}`,
			want: "overloaded",
		},
		{
			name: "non-json",
			body: `upstream connect error`,
			want: "",
		},
	}
	for _, c := range cases {
		if got := extractErrorMessage(c.body); got != c.want {
			t.Errorf("%s: extractErrorMessage = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLLMErrorFormat(t *testing.T) {
	err := llmError(429, []byte(`{"error":{"message":"Rate limit exceeded."}}`))
	if err.Error() != "LLM request failed (HTTP 429): Rate limit exceeded." {
		t.Fatalf("llmError = %q", err.Error())
	}
	// Long bodies get capped.
	long := strings.Repeat("x", 1000)
	err = llmError(500, []byte(long))
	if len(err.Error()) > 540 {
		t.Fatalf("llmError not capped: %d chars", len(err.Error()))
	}
}
