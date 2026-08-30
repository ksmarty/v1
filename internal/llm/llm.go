// Package llm is a minimal OpenAI-compatible chat completions client
// with streaming and tool-call support.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"v1/internal/sanitize"
)

// Message is a chat message in the OpenAI schema. Content is either a plain
// string or an array of content parts ({type:"text"} / {type:"image_url"}).
type Message struct {
	Role             string     `json:"role"`
	Content          any        `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

// ToolCall is a function call requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall carries the tool name and its JSON arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is an OpenAI tool definition.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a callable tool.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Client talks to an OpenAI-compatible API.
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	// ReasoningEffort is sent as reasoning_effort when set (thinking level).
	ReasoningEffort string
	HTTP            *http.Client
}

// NewClient creates a client for the given base URL, key and model.
func NewClient(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		// Generous so long reasoning streams are never cut off; the chat
		// handler's own context timeout is the real backstop.
		HTTP: &http.Client{Timeout: 15 * time.Minute},
	}
}

// Usage is the token accounting reported in the final chunk of a stream.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	// Cost is the price this turn cost, when the provider includes it in the
	// usage payload (e.g. gateways that bill metered usage). Nil/absent means
	// the provider surfaced no cost, so callers show tokens only.
	Cost *float64
}

// StreamResult is the accumulated output of a streamed completion.
type StreamResult struct {
	Text      string
	Reasoning string
	ToolCalls []ToolCall
	Usage     *Usage
	// StopReason is the provider's finish_reason (e.g. "length" when the
	// output window was exhausted mid-reply). It lets the agent tell a genuine
	// end-of-turn apart from a stream that was truncated.
	StopReason string
	// GatewayError is a provider error delivered inside an otherwise-200
	// stream (some gateways reply "data: {\"error\":{...}}" — rate limits,
	// "no active credentials for provider", quota walls — instead of an HTTP
	// error status). When set, ChatStream reports it instead of letting the
	// stream look like a successful empty reply.
	GatewayError string
}

// chatRetries is how many times the initial HTTP request is attempted
// (network errors, 429/408 and 5xx are retried with backoff).
const chatRetries = 3

// defaultMaxTokens is the output ceiling requested from providers that don't
// publish an output limit. Without an explicit max_tokens many providers apply
// a small default and cut long replies off mid-stream (finish_reason
// "length"); requesting a generous ceiling avoids that.
const defaultMaxTokens = 8192

// chatBackoffBase is the first retry delay, doubled per attempt.
const chatBackoffBase = 500 * time.Millisecond

// Complete requests a non-streaming chat completion and returns its text.
// It is intentionally small so agent features can inject an equivalent client.
// sanitizeToolCallArgs guarantees every assistant tool call inside the request
// carries valid JSON arguments. Providers hard-reject assistant tool calls
// whose arguments are not valid JSON ("function.arguments must be valid JSON",
// HTTP 400). A stream that hit the length cap mid-arguments — or compaction
// that sliced an arguments string — can otherwise leave corrupt JSON in
// history and poison every later request with the same 400. This runs on every
// request, so histories corrupted earlier are auto-recovered too.
func sanitizeToolCallArgs(messages []Message) []Message {
	changed := false
	cp := make([]Message, len(messages))
	copy(cp, messages)
	for i := range cp {
		if cp[i].Role == "assistant" && len(cp[i].ToolCalls) > 0 {
			tcs := make([]ToolCall, len(cp[i].ToolCalls))
			copy(tcs, cp[i].ToolCalls)
			cp[i].ToolCalls = tcs
		}
	}
	for i := range messages {
		m := &cp[i]
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		for j := range m.ToolCalls {
			args := m.ToolCalls[j].Function.Arguments
			if args == "" {
				// A zero-argument call; some gateways reject an empty string.
				m.ToolCalls[j].Function.Arguments = "{}"
				changed = true
			} else if !json.Valid([]byte(args)) {
				m.ToolCalls[j].Function.Arguments = `{"note":"tool call arguments were truncated and could not be parsed"}`
				changed = true
			} else if len(strings.TrimSpace(args)) == 0 || strings.TrimSpace(args)[0] != '{' {
				// Valid JSON but not an object (e.g. a bare string, number or
				// array) — several gateways reject those as arguments too.
				m.ToolCalls[j].Function.Arguments = `{"note":"tool call arguments were malformed and could not be parsed"}`
				changed = true
			}
		}
	}
	if !changed {
		return messages
	}
	return cp
}

// sanitizeMessagesForAPI scrubs content that could make a provider reject the
// payload ("The string did not match the expected pattern"). It runs on every
// request at the API boundary, so it also retroactively repairs chats whose
// stored history already contains raw control bytes: whatever loaded the
// messages — a fresh session, a client retry replaying its own copy, or a
// pre-fix transcript in the database — they all pass through here before the
// JSON body is marshaled.
func sanitizeMessagesForAPI(messages []Message) []Message {
	changed := false
	cp := make([]Message, len(messages))
	copy(cp, messages)
	for i, m := range cp {
		mc, cChanged := scrubContent(m.Content)
		rc := sanitize.Text(m.ReasoningContent)
		tid := sanitize.Text(m.ToolCallID)
		n := sanitize.Text(m.Name)
		if cChanged || rc != m.ReasoningContent || tid != m.ToolCallID || n != m.Name {
			changed = true
		}
		m.Content = mc
		m.ReasoningContent = rc
		m.ToolCallID = tid
		m.Name = n
		cp[i] = m
	}
	if !changed {
		return messages
	}
	return cp
}

func scrubContent(c any) (any, bool) {
	switch v := c.(type) {
	case string:
		clean := sanitize.Text(v)
		return clean, clean != v
	case []any:
		out := make([]any, len(v))
		anyChanged := false
		for i, part := range v {
			pm, ok := part.(map[string]any)
			if !ok {
				out[i] = part
				continue
			}
			txt, has := pm["text"].(string)
			if !has {
				out[i] = part
				continue
			}
			if clean := sanitize.Text(txt); clean != txt {
				nm := cloneMap(pm)
				nm["text"] = clean
				out[i] = nm
				anyChanged = true
			} else {
				out[i] = part
			}
		}
		if anyChanged {
			return out, true
		}
		return c, false
	default:
		return c, false
	}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	messages = sanitizeToolCallArgs(messages)
	messages = sanitizeMessagesForAPI(messages)
	body := map[string]any{"model": c.Model, "messages": messages}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", llmError(resp.StatusCode, data)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("LLM response contained no choices")
	}
	return out.Choices[0].Message.Content, nil
}

func (c *Client) ChatStream(ctx context.Context, messages []Message, tools []Tool, onDelta func(string), onReasoning func(string)) (*StreamResult, error) {
	messages = sanitizeToolCallArgs(messages)
	messages = sanitizeMessagesForAPI(messages)
	res := &StreamResult{}
	streamOptions := true
	maxTokens := true
	attempt := 0
	var lastErr error
	for {
		attempt++
		if attempt > chatRetries {
			return nil, lastErr
		}
		resp, retryAfter, err := c.postStream(ctx, messages, tools, streamOptions, maxTokens)
		if err != nil {
			lastErr = fmt.Errorf("LLM request failed: %w", err)
			if err := waitBackoff(ctx, chatBackoffBase*time.Duration(1<<(attempt-1))); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode == http.StatusBadRequest && streamOptions {
			// Older providers reject stream_options; fall back once without it.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			streamOptions = false
			attempt--
			lastErr = fmt.Errorf("LLM request failed (HTTP 400)")
			continue
		}
		if resp.StatusCode == http.StatusBadRequest && maxTokens {
			// Some gateways cap max_tokens below the default; fall back once
			// without it so their own default applies.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			maxTokens = false
			attempt--
			lastErr = fmt.Errorf("LLM request failed (HTTP 400)")
			continue
		}
		if !(resp.StatusCode >= 200 && resp.StatusCode < 300) {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = llmError(resp.StatusCode, body)
			if !isRetryableStatus(resp.StatusCode) {
				return nil, lastErr
			}
			if err := waitBackoff(ctx, retryAfterDelay(retryAfter, chatBackoffBase*time.Duration(1<<(attempt-1)))); err != nil {
				return nil, err
			}
			continue
		}
		defer resp.Body.Close()
		if err := scanStream(resp.Body, res, onDelta, onReasoning); err != nil {
			// Return what streamed so far — a mid-response failure (token
			// limit, network drop) still leaves a partial reply the caller
			// can persist and resume from.
			return res, err
		}
		// A 200 stream can carry the provider's real error as a data chunk
		// (rate limit, missing provider credentials, quota). Surface it
		// instead of letting the caller treat an empty stream as a reply.
		if res.Text == "" && res.Reasoning == "" && len(res.ToolCalls) == 0 && res.GatewayError != "" {
			return res, fmt.Errorf("LLM returned an empty response: %s", res.GatewayError)
		}
		return res, nil
	}
}

// postStream issues one chat completions request and returns the response and
// any Retry-After duration for rate-limited responses.
func (c *Client) postStream(ctx context.Context, messages []Message, tools []Tool, streamOptions, maxTokens bool) (*http.Response, time.Duration, error) {
	body := map[string]any{
		"model":    c.Model,
		"messages": messages,
		"stream":   true,
	}
	if c.ReasoningEffort != "" {
		body["reasoning_effort"] = c.ReasoningEffort
	}
	if maxTokens {
		body["max_tokens"] = defaultMaxTokens
	}
	if streamOptions {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	return resp, retryAfterDuration(resp), nil
}

// scanStream reads an SSE body, accumulating text, reasoning and tool calls
// and capturing usage from the final chunk (present when include_usage is on).
func scanStream(r io.Reader, res *StreamResult, onDelta func(string), onReasoning func(string)) error {
	toolCalls := map[int]*ToolCall{}
	var order []int

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			// Some gateways stream their error as a data chunk with a non-empty
			// "error" object even when the HTTP status is 200.
			Error *struct {
				Message string `json:"message"`
				Code    string `json:"code"`
				Type    string `json:"type"`
			} `json:"error"`
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
				Delta        struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					// OpenRouter (and some other gateways) stream thinking in a
					// plain `reasoning` field instead of reasoning_content.
					Reasoning string `json:"reasoning"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int64    `json:"prompt_tokens"`
				CompletionTokens int64    `json:"completion_tokens"`
				Cost             *float64 `json:"cost"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			res.Usage = &Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				Cost:             chunk.Usage.Cost,
			}
		}
		if chunk.Error != nil && chunk.Error.Message != "" && res.GatewayError == "" {
			res.GatewayError = chunk.Error.Message
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if chunk.Choices[0].FinishReason != nil {
			res.StopReason = *chunk.Choices[0].FinishReason
		}
		d := chunk.Choices[0].Delta
		if d.Content != "" {
			res.Text += d.Content
			if onDelta != nil {
				onDelta(d.Content)
			}
		}
		if d.ReasoningContent != "" {
			res.Reasoning += d.ReasoningContent
			if onReasoning != nil {
				onReasoning(d.ReasoningContent)
			}
		} else if d.Reasoning != "" {
			res.Reasoning += d.Reasoning
			if onReasoning != nil {
				onReasoning(d.Reasoning)
			}
		}
		for _, tc := range d.ToolCalls {
			slot, ok := toolCalls[tc.Index]
			if !ok {
				slot = &ToolCall{Type: "function"}
				toolCalls[tc.Index] = slot
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				slot.ID = tc.ID
			}
			if tc.Type != "" {
				slot.Type = tc.Type
			}
			if tc.Function.Name != "" {
				slot.Function.Name += tc.Function.Name
			}
			slot.Function.Arguments += tc.Function.Arguments
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading LLM stream: %w", err)
	}
	for _, i := range order {
		res.ToolCalls = append(res.ToolCalls, *toolCalls[i])
	}
	return nil
}

// isRetryableStatus reports whether the HTTP status warrants a retry.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return code >= 500 && code < 600
}

// retryAfterDuration parses a Retry-After header (seconds), capped at 2m.
func retryAfterDuration(resp *http.Response) time.Duration {
	ra := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if ra == "" {
		return 0
	}
	if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
		if secs > 120 {
			secs = 120
		}
		return time.Duration(secs) * time.Second
	}
	return 0
}

// retryAfterDelay picks the retry delay, preferring Retry-After.
func retryAfterDelay(after, fallback time.Duration) time.Duration {
	if after > 0 && after <= 10*time.Second {
		return after
	}
	return fallback
}

// waitBackoff sleeps for d, honoring context cancellation.
func waitBackoff(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// llmError builds an error from a non-2xx response, extracting the
// provider's human-readable message when the body is JSON — OpenAI-style
// {"error":{"message":...}} or wrapped shapes like
// {"type":"error","error":{"type":"...","message":"..."}}.
func llmError(code int, body []byte) error {
	msg := extractErrorMessage(string(body))
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if msg == "" {
		msg = http.StatusText(code)
	}
	if r := []rune(msg); len(r) > 500 {
		msg = string(r[:500]) + "…"
	}
	return fmt.Errorf("LLM request failed (HTTP %d): %s", code, msg)
}

// extractErrorMessage pulls the human message out of a provider error body,
// or "" when the body is not a recognized JSON error shape. An SSE-style
// "data:" prefix is tolerated.
func extractErrorMessage(body string) string {
	body = strings.TrimPrefix(strings.TrimSpace(body), "data:")
	var v struct {
		Message string `json:"message"`
		Error   *struct {
			Message string          `json:"message"`
			Error   json.RawMessage `json:"error"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &v); err != nil {
		return ""
	}
	if v.Error != nil {
		if v.Error.Message != "" {
			return v.Error.Message
		}
		var inner struct {
			Message string `json:"message"`
		}
		if len(v.Error.Error) > 0 && json.Unmarshal(v.Error.Error, &inner) == nil {
			return inner.Message
		}
	}
	return v.Message
}

// TestModels performs GET {baseURL}/models with the configured key.
func (c *Client) TestModels(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s/models", resp.StatusCode, c.BaseURL)
	}
	return nil
}
