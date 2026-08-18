package agent

import (
	"context"
	"strings"
	"testing"

	"v1/internal/llm"
)

type fakeSummarizer struct{ calls int }

func (f *fakeSummarizer) Summarize(context.Context, []llm.Message) (string, error) {
	f.calls++
	return "kept facts", nil
}

func TestEstimateTokensDeterministic(t *testing.T) {
	m := []llm.Message{{Role: "user", Content: "hello"}, {Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: "{}"}}}}}
	if got := EstimateTokens(m); got != EstimateTokens(m) || got <= 0 {
		t.Fatalf("estimate = %d", got)
	}
}

func TestChunkMessagesFitsBudget(t *testing.T) {
	big := make([]byte, 8000)
	for i := range big {
		big[i] = 'a'
	}
	messages := make([]llm.Message, 30)
	for i := range messages {
		messages[i] = llm.Message{Role: "user", Content: string(big)}
	}
	chunks := chunkMessages(messages, 5000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if EstimateTokens(c) > 5000 {
			t.Fatalf("chunk exceeds budget: %d", EstimateTokens(c))
		}
	}
}

func TestChunkMessagesTruncatesGiantMessage(t *testing.T) {
	huge := string(make([]byte, 40000)) // 40k bytes ≈ 10k tokens, over budget
	m := llm.Message{Role: "assistant", Content: huge, ToolCalls: []llm.ToolCall{{ID: "1", Type: "function", Function: llm.FunctionCall{Name: "run_command", Arguments: string(make([]byte, 20000))}}}}
	chunks := chunkMessages([]llm.Message{m}, 2000)
	if len(chunks) != 1 {
		t.Fatalf("expected one chunk, got %d", len(chunks))
	}
	if EstimateTokens(chunks[0]) > 2000 {
		t.Fatalf("giant message still exceeds budget: %d", EstimateTokens(chunks[0]))
	}
}

func TestSummarizeRequestIsPlainText(t *testing.T) {
	m := []llm.Message{
		{Role: "user", Content: "fix the build"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "tc1", Type: "function", Function: llm.FunctionCall{Name: "run_command", Arguments: `{"command":"go build"}`}}}},
		{Role: "tool", ToolCallID: "tc1", Content: "ok"},
	}
	out := summarizeRequest(m)
	joined := ""
	for _, msg := range out {
		if s, ok := msg.Content.(string); ok {
			joined += s
		}
	}
	if !strings.Contains(joined, "fix the build") || !strings.Contains(joined, "run_command") || !strings.Contains(joined, "ok") {
		t.Fatalf("transcript lost details: %q", joined)
	}
}

func TestCompactHistoryPreservesMultiToolPairs(t *testing.T) {
	old := "old context " + string(make([]byte, 3000))
	h := []llm.Message{{Role: "system", Content: "system"}, {Role: "user", Content: old}, {Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "a", Type: "function", Function: llm.FunctionCall{Name: "read_file"}}, {ID: "b", Type: "function", Function: llm.FunctionCall{Name: "list_files"}}}}, {Role: "tool", ToolCallID: "a", Content: old}, {Role: "tool", ToolCallID: "b", Content: old}, {Role: "user", Content: "latest"}}
	f := &fakeSummarizer{}
	got := compactHistory(context.Background(), h, 100, 1, f)
	if f.calls == 0 {
		t.Fatal("summarizer was not called")
	}
	if len(got) < 4 || got[0].Role != "system" || got[1].Role != "user" || got[2].Role != "system" || got[len(got)-1].Content != "latest" {
		t.Fatalf("unexpected compacted history: %#v", got)
	}
	assertProtocolSafe(t, got)
}

func TestCompactHistoryDropsOrphanToolMessages(t *testing.T) {
	h := []llm.Message{{Role: "system", Content: "s"}, {Role: "tool", ToolCallID: "missing", Content: "bad"}, {Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call", Type: "function"}}}, {Role: "tool", ToolCallID: "other", Content: "bad"}, {Role: "user", Content: "latest"}}
	got := compactHistory(context.Background(), h, 100000, 1, nil)
	assertProtocolSafe(t, got)
	if got[len(got)-1].Content != "latest" {
		t.Fatalf("lost latest message: %#v", got)
	}
}

func TestCompactHistoryFallback(t *testing.T) {
	h := []llm.Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "initial request"},
		{Role: "assistant", Content: string(make([]byte, 5000))},
		{Role: "user", Content: "latest"},
	}
	got := compactHistory(context.Background(), h, 100, 1, nil)
	if len(got) < 4 || got[2].Role != "system" {
		t.Fatalf("missing fallback summary: %#v", got)
	}
}

func TestCompactHistoryKeepsParallelToolResults(t *testing.T) {
	h := []llm.Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "u"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "a", Type: "function", Function: llm.FunctionCall{Name: "read_file"}},
			{ID: "b", Type: "function", Function: llm.FunctionCall{Name: "read_file"}},
			{ID: "c", Type: "function", Function: llm.FunctionCall{Name: "read_file"}},
		}},
		{Role: "tool", ToolCallID: "a", Content: "ra"},
		{Role: "tool", ToolCallID: "b", Content: "rb"},
		{Role: "tool", ToolCallID: "c", Content: "rc"},
		{Role: "user", Content: "latest"},
	}
	got := compactHistory(context.Background(), h, 100000, 1, nil)
	tools := 0
	for _, m := range got {
		if m.Role == "tool" {
			tools++
		}
	}
	if tools != 3 {
		t.Fatalf("expected 3 parallel tool results, got %d: %#v", tools, got)
	}
	assertProtocolSafe(t, got)
}

func assertProtocolSafe(t *testing.T, history []llm.Message) {
	t.Helper()
	for i, m := range history {
		if m.Role != "tool" {
			continue
		}
		j := i - 1
		for j >= 0 && history[j].Role == "tool" {
			j--
		}
		if j < 0 || history[j].Role != "assistant" {
			t.Fatalf("orphan tool result at %d", i)
		}
		calls := map[string]bool{}
		for _, tc := range history[j].ToolCalls {
			calls[tc.ID] = true
		}
		if !calls[m.ToolCallID] {
			t.Fatalf("tool result %q has no matching call", m.ToolCallID)
		}
	}
	for i, m := range history {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		seen := map[string]bool{}
		for j := i + 1; j < len(history) && history[j].Role == "tool"; j++ {
			seen[history[j].ToolCallID] = true
		}
		for _, tc := range m.ToolCalls {
			if !seen[tc.ID] {
				t.Fatalf("orphan assistant tool call %q", tc.ID)
			}
		}
	}
}
