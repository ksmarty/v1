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

// sseChunk renders one SSE data line from a JSON payload.
func sseChunk(payload string) string { return "data: " + payload + "\n\n" }

func TestChatStreamSendsMaxTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens *int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.MaxTokens == nil || *request.MaxTokens != defaultMaxTokens {
			t.Errorf("max_tokens = %v, want %d", request.MaxTokens, defaultMaxTokens)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			sseChunk(`{"choices":[{"delta":{"content":"hel"}}]}`) +
				sseChunk(`{"choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`) +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "test-model")
	res, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if res.Text != "hello" {
		t.Errorf("Text = %q, want %q", res.Text, "hello")
	}
	if res.StopReason != "stop" {
		t.Errorf("StopReason = %q, want %q", res.StopReason, "stop")
	}
}

func TestChatStreamFallsBackWithoutMaxTokens(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			MaxTokens     *int `json:"max_tokens"`
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls {
		case 1:
			if request.MaxTokens == nil || request.StreamOptions == nil {
				t.Fatal("first call should carry max_tokens and stream_options")
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported option"}}`))
		case 2:
			if request.MaxTokens == nil || request.StreamOptions != nil {
				t.Errorf("second call should keep max_tokens, drop stream_options: %+v", request)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"max_tokens too large"}}`))
		case 3:
			if request.MaxTokens != nil || request.StreamOptions != nil {
				t.Errorf("third call should drop both: %+v", request)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				sseChunk(`{"choices":[{"delta":{"content":"ok"}}]}`) +
					"data: [DONE]\n\n",
			))
		default:
			t.Fatalf("too many calls: %d", calls)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "test-model")
	res, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if res.Text != "ok" {
		t.Errorf("Text = %q, want %q", res.Text, "ok")
	}
	if calls != 3 {
		t.Errorf("server calls = %d, want 3", calls)
	}
}

func TestChatStreamOpenRouterReasoningField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			sseChunk(`{"choices":[{"delta":{"content":"","reasoning":"Think"}}]}`) +
				sseChunk(`{"choices":[{"delta":{"content":"","reasoning":"ing…"}}]}`) +
				sseChunk(`{"choices":[{"delta":{"content":"Answer."},"finish_reason":"stop"}]}`) +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "test-model")
	var reasoning string
	res, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil, func(d string) { reasoning += d })
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if reasoning != "Thinking…" {
		t.Errorf("reasoning deltas = %q, want %q", reasoning, "Thinking…")
	}
	if res.Reasoning != "Thinking…" {
		t.Errorf("res.Reasoning = %q, want %q", res.Reasoning, "Thinking…")
	}
	if res.Text != "Answer." {
		t.Errorf("res.Text = %q, want %q", res.Text, "Answer.")
	}
}
