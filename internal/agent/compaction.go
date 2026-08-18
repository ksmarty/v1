package agent

import (
	"context"
	"fmt"
	"strings"

	"v1/internal/llm"
	"v1/internal/store"
)

const (
	defaultContextBudget    = 12000
	defaultContextThreshold = 0.80
)

// Summarizer replaces an old, non-user-visible history prefix with a compact summary.
type Summarizer interface {
	Summarize(context.Context, []llm.Message) (string, error)
}

// summarizeSystem instructs the model how to compress a conversation chunk.
const summarizeSystem = "Summarize the following conversation history for an AI coding agent. Preserve decisions, requirements, files changed, tool outcomes, errors, and unresolved work. Be concise and factual; do not address the user. Keep the summary tight (under 800 words)."

type clientSummarizer struct{ client *llm.Client }

// safeBudget returns the per-call input budget for summarization: as much of
// the model's context window as we can use (minus room for the response),
// clamped conservatively. Falls back to 24k tokens when the window is unknown.
func (s clientSummarizer) safeBudget(ctx context.Context) int {
	if n := llm.ModelContextLength(ctx, s.client.BaseURL, s.client.APIKey, s.client.Model); n > 0 {
		b := n - 8192
		if b < 8000 {
			b = 8000
		}
		if b > 60000 {
			b = 60000
		}
		return b
	}
	return 24000
}

// Summarize compresses the messages. Histories larger than the model's window
// are summarized in bounded chunks and folded together, so compaction works
// even when the transcript is far larger than a single request could carry.
func (s clientSummarizer) Summarize(ctx context.Context, messages []llm.Message) (string, error) {
	budget := s.safeBudget(ctx)
	if EstimateTokens(messages) <= budget {
		return s.client.Complete(ctx, summarizeRequest(messages))
	}
	summary := ""
	for _, chunk := range chunkMessages(messages, budget) {
		var (
			out string
			err error
		)
		if summary == "" {
			out, err = s.client.Complete(ctx, summarizeRequest(chunk))
		} else {
			out, err = s.client.Complete(ctx, mergeSummaries(summary, chunk))
		}
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) == "" {
			continue
		}
		summary = out
	}
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("summarizer returned an empty summary")
	}
	return summary, nil
}

func summarizeRequest(messages []llm.Message) []llm.Message {
	return []llm.Message{
		{Role: "system", Content: summarizeSystem},
		{Role: "user", Content: "Conversation to summarize:\n\n" + transcriptText(messages)},
	}
}

func mergeSummaries(prev string, chunk []llm.Message) []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "You are merging conversation summaries. Keep every fact from the previous summary and the new chunk: decisions, requirements, files changed, tool outcomes, errors, unresolved work. Remove duplication. Stay under 800 words."},
		{Role: "user", Content: "Previous summary:\n\n" + prev + "\n\n---\n\nAdditional conversation:\n\n" + transcriptText(chunk)},
	}
}

// transcriptText renders messages as plain text for the summarizer.
func transcriptText(messages []llm.Message) string {
	var b strings.Builder
	for _, m := range messages {
		role := m.Role
		if role == "" {
			role = "message"
		}
		content := contentString(m.Content)
		if content != "" {
			b.WriteString("[" + role + "] " + content + "\n")
		}
		for _, tc := range m.ToolCalls {
			b.WriteString("[tool] " + tc.Function.Name + " " + tc.Function.Arguments + "\n")
		}
	}
	return b.String()
}

// chunkMessages splits the history into pieces that each fit the token budget,
// truncating any single oversized message so no call exceeds the window.
func chunkMessages(messages []llm.Message, budget int) [][]llm.Message {
	var (
		chunks    [][]llm.Message
		cur       []llm.Message
		curTokens int
	)
	for _, m := range messages {
		mt := EstimateTokens([]llm.Message{m})
		if len(cur) > 0 && curTokens+mt > budget {
			chunks = append(chunks, cur)
			cur, curTokens = nil, 0
		}
		if mt > budget {
			m = truncateMessage(m, budget)
			mt = EstimateTokens([]llm.Message{m})
		}
		cur = append(cur, m)
		curTokens += mt
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// truncateMessage trims a message's content (and tool arguments) to fit the
// budget so a single giant message cannot overflow the window. The room is
// shared between the content and each tool call so the total stays bounded.
func truncateMessage(m llm.Message, budget int) llm.Message {
	overhead := 8 + len(m.Role) + len(m.Name) + len(m.ToolCallID)
	for _, tc := range m.ToolCalls {
		overhead += 12 + len(tc.ID) + len(tc.Type) + len(tc.Function.Name)
	}
	room := budget*4 - overhead - 512 // slack for estimate overage
	if room < 64 {
		room = 64
	}
	contentRoom := room / 2
	if s, ok := m.Content.(string); ok {
		if len(s) > contentRoom {
			m.Content = s[:contentRoom] + "\n…(truncated)"
		}
	} else if contentString(m.Content) != "" {
		s := contentString(m.Content)
		if len(s) > contentRoom {
			m.Content = s[:contentRoom] + "\n…(truncated)"
		}
	}
	if n := len(m.ToolCalls); n > 0 {
		argRoom := room / (2 * n)
		for i := range m.ToolCalls {
			if len(m.ToolCalls[i].Function.Arguments) > argRoom {
				m.ToolCalls[i].Function.Arguments = m.ToolCalls[i].Function.Arguments[:argRoom]
			}
		}
	}
	return m
}

// EstimateTokens is a deterministic provider-neutral estimate. It deliberately
// uses UTF-8 bytes rather than a provider tokenizer and includes message shape.
func EstimateTokens(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += 8 + len(m.Role) + len(m.Name) + len(m.ToolCallID) + len(contentString(m.Content))
		for _, tc := range m.ToolCalls {
			total += 12 + len(tc.ID) + len(tc.Type) + len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	return (total + 3) / 4
}

func contentString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func messageContent(m llm.Message) string { return contentString(m.Content) }

// compactHistory compresses only the request copy. Persisted transcript remains intact.
func compactHistory(ctx context.Context, history []llm.Message, budget int, threshold float64, summarizer Summarizer) []llm.Message {
	if budget <= 0 {
		budget = defaultContextBudget
	}
	if threshold <= 0 || threshold > 1 {
		threshold = defaultContextThreshold
	}
	limit := int(float64(budget) * threshold)
	out := protocolSafeHistory(history)
	if EstimateTokens(out) <= limit {
		return out
	}

	out = append([]llm.Message(nil), out...)
	// First remove bulk from tool results, retaining enough head/tail for useful context.
	for i := range out {
		if out[i].Role == "tool" {
			content := messageContent(out[i])
			if len(content) > 2400 {
				out[i].Content = trimHeadTail(content, 900)
			}
		}
	}
	if EstimateTokens(out) <= limit {
		return out
	}

	// Select an old prefix ending on a complete message boundary. Keep the
	// original system message and first user request verbatim, and never split
	// an assistant tool call from its following tool results.
	firstUser := -1
	for i := 1; i < len(out); i++ {
		if out[i].Role == "user" {
			firstUser = i
			break
		}
	}
	prefixStart := 1
	if firstUser >= 0 {
		prefixStart = firstUser + 1
	}
	if prefixStart >= len(out)-1 {
		return out
	}
	end := prefixStart
	for end < len(out)-1 && EstimateTokens(out[:end+1]) < limit/2 {
		end++
	}
	if end == prefixStart {
		end++
	}
	for end < len(out) && out[end].Role == "tool" {
		end++
	}
	if end < len(out) && out[end].Role == "assistant" && len(out[end].ToolCalls) > 0 {
		end++
		for end < len(out) && out[end].Role == "tool" {
			end++
		}
	}
	if end >= len(out) {
		end = len(out) - 1
	}
	prefix := out[prefixStart:end]
	if len(prefix) == 0 {
		return out
	}
	summary := ""
	if summarizer != nil {
		summary, _ = summarizer.Summarize(ctx, prefix)
	}
	if strings.TrimSpace(summary) == "" {
		summary = fallbackSummary(prefix)
	}
	result := append([]llm.Message(nil), out[:prefixStart]...)
	result = append(result, llm.Message{Role: "system", Content: "Conversation summary (historical, not user-visible):\n" + summary})
	result = append(result, out[end:]...)
	return result
}

func protocolSafeHistory(history []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(history))
	for i := 0; i < len(history); i++ {
		m := history[i]
		if m.Role == "tool" {
			// A parallel tool call produces several consecutive tool results —
			// walk back past any already-accepted results to their assistant
			// message before checking for a matching call.
			j := len(out) - 1
			for j >= 0 && out[j].Role == "tool" {
				j--
			}
			if j < 0 || out[j].Role != "assistant" {
				continue
			}
			valid := false
			for _, tc := range out[j].ToolCalls {
				if tc.ID == m.ToolCallID {
					valid = true
					break
				}
			}
			if !valid {
				continue
			}
			out = append(out, m)
			continue
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			j := i + 1
			seen := map[string]bool{}
			for j < len(history) && history[j].Role == "tool" {
				seen[history[j].ToolCallID] = true
				j++
			}
			complete := true
			for _, tc := range m.ToolCalls {
				if !seen[tc.ID] {
					complete = false
					break
				}
			}
			if !complete {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func fallbackSummary(messages []llm.Message) string {
	var b strings.Builder
	for _, m := range messages {
		content := messageContent(m)
		if len(content) > 320 {
			content = content[:320] + "..."
		}
		if content == "" && len(m.ToolCalls) > 0 {
			content = "requested tool calls: " + m.ToolCalls[0].Function.Name
		}
		if content != "" {
			fmt.Fprintf(&b, "%s: %s\n", m.Role, content)
		}
	}
	return b.String()
}

// CompactProject summarizes the persisted transcript without adding a visible message.
func CompactProject(ctx context.Context, st *store.Store, projectID, sessionID string, client *llm.Client) (int64, error) {
	messages, err := st.ListMessages(projectID, sessionID)
	if err != nil {
		return 0, err
	}
	if len(messages) == 0 {
		return 0, fmt.Errorf("no messages to compact")
	}
	history := make([]llm.Message, 0, len(messages))
	for _, m := range messages {
		history = append(history, llm.Message{Role: m.Role, Content: m.Content})
	}
	summary, err := (clientSummarizer{client: client}).Summarize(ctx, history)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(summary) == "" {
		summary = fallbackSummary(history)
	}
	lastID := messages[len(messages)-1].ID
	if err := st.SaveCompactionSnapshot(projectID, sessionID, summary, lastID); err != nil {
		return 0, err
	}
	return lastID, nil
}

func compactForModel(ctx context.Context, history []llm.Message, p ChatParams) []llm.Message {
	var s Summarizer = p.Summarizer
	if s == nil && p.Client != nil {
		s = clientSummarizer{client: p.Client}
	}
	return compactHistory(ctx, history, p.ContextBudget, p.ContextThreshold, s)
}
