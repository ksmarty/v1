package memory

import (
	"encoding/json"
	"strings"

	"v1/internal/store"
)

// CaptureSystem instructs the model to turn one exchange into durable memories.
//
// It is deliberately narrow. The failure mode of automatic capture is not
// missing a fact, it is filling the store with restatements of the current task
// — every one of which is then injected into every later turn. So the rules
// spend more words on what not to record than on what to.
const CaptureSystem = `You extract durable memories from one exchange of a coding session.

Reply with ONLY a JSON array. Each element:
{"content": "...", "category": "preference|episodic|fact|plan", "tags": "comma,separated", "importance": 1-3}

Write content as one self-contained sentence that still makes sense in a future session that cannot see this conversation. Name things concretely — file paths, symbols, endpoints, versions — instead of saying "it" or "the change".

Record only what will still matter later:
- preference: a choice or correction the user made about how they want things done
- episodic: what was tried and what happened, especially a failure and its cause
- fact: stable project knowledge — architecture, conventions, endpoints, gotchas, where things live
- plan: a plan that is still in progress

Never record:
- what was just edited, or any progress report about the current task
- anything already obvious from the project's own code
- a restatement of what the user asked for
- anything the user asked to keep out of memory
- anything already listed under "Known memories" below

importance: 3 for a decision that constrains future work, 2 for a durable fact, 1 for useful context.
tags: short lowercase technical terms someone would search for — file names, symbols, technologies.

Return [] when nothing is worth remembering. At most 5 entries.`

// Captured is one memory the extraction returned.
type Captured struct {
	Content    string  `json:"content"`
	Category   string  `json:"category"`
	Tags       string  `json:"tags"`
	Importance float64 `json:"importance"`
}

// CaptureLimit caps what one turn may contribute, so a chatty exchange cannot
// flood the store in a single step.
const CaptureLimit = 5

// ParseCaptured reads an extraction reply into facts, dropping anything it
// cannot use. A reply it cannot parse yields nothing rather than a partial
// guess: the next turn will extract again anyway.
func ParseCaptured(reply string) []Captured {
	// Models fence the JSON about as often as not.
	if i := strings.Index(reply, "["); i >= 0 {
		if j := strings.LastIndex(reply, "]"); j > i {
			reply = reply[i : j+1]
		}
	}
	var raw []Captured
	if err := json.Unmarshal([]byte(reply), &raw); err != nil {
		return nil
	}
	out := make([]Captured, 0, len(raw))
	for _, c := range raw {
		// The same <private> contract as the remember tool: a fact the model
		// wrapped in tags must not reach the store through this path either.
		c.Content = oneLine(StripPrivate(c.Content))
		if c.Content == "" {
			continue
		}
		switch c.Category {
		case "preference", "episodic", "fact", "plan":
		default:
			c.Category = "fact"
		}
		if c.Importance < 1 {
			c.Importance = 1
		}
		if c.Importance > 3 {
			c.Importance = 3
		}
		c.Tags = oneLine(c.Tags)
		out = append(out, c)
	}
	if len(out) > CaptureLimit {
		out = out[:CaptureLimit]
	}
	return out
}

// CapturePrompt frames the exchange for the extraction, listing what the
// project already remembers so the model does not hand back what is already
// stored — the cheap half of deduplication, before anything is embedded.
func CapturePrompt(known []string, exchange string) string {
	var b strings.Builder
	if len(known) > 0 {
		b.WriteString("Known memories (do not repeat these):\n")
		for _, k := range known {
			b.WriteString("- ")
			b.WriteString(oneLine(k))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Exchange:\n")
	b.WriteString(exchange)
	return b.String()
}

// oneLine folds whitespace so a memory is always a single line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

const (
	captureUserLimit  = 2000
	captureReplyLimit = 4000
)

// TurnExchange renders the part of a turn the extraction reads: the user's
// messages and the assistant's final reply.
//
// The transcript is deliberately excluded. Memories are injected into the
// system prompt of every turn, so feeding the transcript back would let the
// model re-derive its own earlier memories and compound paraphrase on
// paraphrase until retrieval degrades. Tool output is excluded too: it is most
// of a turn's bulk and almost none of its durable content.
//
// fromID is the id of the turn's first user message; anything before it belongs
// to an earlier turn and is already accounted for.
func TurnExchange(msgs []*store.Message, fromID int64) string {
	var user []string
	var reply string
	for _, m := range msgs {
		if m == nil || m.ID < fromID {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}
		switch m.Role {
		case "user":
			user = append(user, text)
		case "assistant":
			// The last one wins: it is the turn's conclusion.
			reply = text
		}
	}
	if len(user) == 0 && reply == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("User:\n")
	b.WriteString(clipRunes(strings.Join(user, "\n\n"), captureUserLimit))
	if reply != "" {
		b.WriteString("\n\nAssistant:\n")
		b.WriteString(clipRunes(reply, captureReplyLimit))
	}
	return b.String()
}

// clipRunes truncates on a rune boundary and says so, so the model does not
// read a cut-off sentence as complete.
func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n[truncated]"
}
