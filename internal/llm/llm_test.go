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

func TestSanitizeToolCallArgs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" means expect a valid JSON object replacement
	}{
		{name: "valid object passes through", in: `{"path":"web/index.html"}`, want: `{"path":"web/index.html"}`},
		{name: "empty string replaced", in: "", want: "{}"},
		{name: "truncated JSON replaced", in: `{"path":"web/src/main.t`, want: ""},
		{name: "bare string replaced", in: `hello`, want: ""},
		{name: "array replaced", in: `[1,2,3]`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := []Message{{
				Role:      "assistant",
				Content:   "call",
				ToolCalls: []ToolCall{{ID: "t1", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: tt.in}}},
			}}
			got := sanitizeToolCallArgs(msgs)
			gotArgs := got[0].ToolCalls[0].Function.Arguments
			if tt.want != "" {
				if gotArgs != tt.want {
					t.Fatalf("arguments = %q, want %q", gotArgs, tt.want)
				}
			} else if !json.Valid([]byte(gotArgs)) || strings.TrimSpace(gotArgs)[0] != '{' {
				t.Fatalf("arguments = %q, want valid JSON object", gotArgs)
			}
			// The caller's slice must not be mutated.
			if msgs[0].ToolCalls[0].Function.Arguments != tt.in {
				t.Fatalf("input slice mutated: %q", msgs[0].ToolCalls[0].Function.Arguments)
			}
		})
	}
}

func TestSanitizeMessagesForAPI(t *testing.T) {
	// Provider-hostile bytes in a plain string.
	msgs := []Message{
		{Role: "user", Content: "out\x1b[31mput\x07\x00"},
		{Role: "assistant", Content: "ok", ReasoningContent: "think\x1b[0m"},
		{Role: "tool", ToolCallID: "call_1\u0000", Name: "run\u0001", Content: "{\"output\":\"\x1b[1mx\"}"},
		{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "part\x1b[0m"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:x"}},
		}},
	}
	got := sanitizeMessagesForAPI(msgs)
	if g := got[0].Content.(string); g != "output" {
		t.Fatalf("plain content not scrubbed: %q", g)
	}
	if g := got[1].ReasoningContent; g != "think" {
		t.Fatalf("reasoning not scrubbed: %q", g)
	}
	if g := got[2].ToolCallID; g != "call_1" {
		t.Fatalf("tool_call_id not scrubbed: %q", g)
	}
	if g := got[2].Name; g != "run" {
		t.Fatalf("name not scrubbed: %q", g)
	}
	if g := got[2].Content.(string); g != "{\"output\":\"x\"}" {
		t.Fatalf("tool content not scrubbed: %q", g)
	}
	parts := got[3].Content.([]any)
	textPart := parts[0].(map[string]any)
	if g := textPart["text"].(string); g != "part" {
		t.Fatalf("content-part text not scrubbed: %q", g)
	}
	// The image part must be untouched.
	imgPart := parts[1].(map[string]any)
	if imgPart["type"] != "image_url" {
		t.Fatalf("image part mangled: %#v", imgPart)
	}
	// Clean input returns the same slice (no copy). Safe to assert because the
	// implementation returns the input when nothing changed.
	clean := []Message{{Role: "user", Content: "fine"}}
	if got := sanitizeMessagesForAPI(clean); &got[0] != &clean[0] {
		t.Fatalf("clean input should be returned unchanged, got a copy")
	}
}
