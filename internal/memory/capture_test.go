package memory

import (
	"strings"
	"testing"

	"v1/internal/store"
)

func TestParseCapturedReadsAFencedArray(t *testing.T) {
	got := ParseCaptured("```json\n[{\"content\":\"the deploy script lives in scripts/deploy.sh\",\"category\":\"fact\",\"tags\":\"deploy, scripts\",\"importance\":2}]\n```")
	if len(got) != 1 {
		t.Fatalf("got %d facts, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Content != "the deploy script lives in scripts/deploy.sh" {
		t.Fatalf("content = %q", f.Content)
	}
	if f.Category != "fact" || f.Tags != "deploy, scripts" || f.Importance != 2 {
		t.Fatalf("fields = %+v", f)
	}
}

func TestParseCapturedDefaultsAndClamps(t *testing.T) {
	got := ParseCaptured(`[
		{"content":"a preference","category":"banana","importance":9},
		{"content":"a plan","category":"plan","importance":-4},
		{"content":"a fact","category":"fact"}
	]`)
	if len(got) != 3 {
		t.Fatalf("got %d facts, want 3: %+v", len(got), got)
	}
	// An unknown category becomes a fact rather than a rejection: the model's
	// intent is clear even when the label is not.
	if got[0].Category != "fact" {
		t.Fatalf("unknown category = %q, want fact", got[0].Category)
	}
	if got[0].Importance != 3 {
		t.Fatalf("importance = %v, want clamped to 3", got[0].Importance)
	}
	if got[1].Importance != 1 {
		t.Fatalf("importance = %v, want clamped up to 1", got[1].Importance)
	}
	if got[1].Category != "plan" {
		t.Fatalf("category = %q, want plan", got[1].Category)
	}
	// A missing importance is a normal default, not an error.
	if got[2].Importance != 1 {
		t.Fatalf("default importance = %v, want 1", got[2].Importance)
	}
}

func TestParseCapturedStripsPrivateSections(t *testing.T) {
	got := ParseCaptured(`[{"content":"the key is <private>sk-live-abc</private> and it lives in .env"}]`)
	if len(got) != 1 {
		t.Fatalf("got %d facts: %+v", len(got), got)
	}
	if strings.Contains(got[0].Content, "sk-live-abc") {
		t.Fatalf("a credential reached the capture: %q", got[0].Content)
	}
	if !strings.Contains(got[0].Content, "lives in .env") {
		t.Fatalf("the rest of the fact was lost: %q", got[0].Content)
	}
}

func TestParseCapturedDropsUnusableEntries(t *testing.T) {
	got := ParseCaptured(`[{"content":"   "},{"content":"<private>only a secret</private>"},{"content":"kept"}]`)
	if len(got) != 1 || got[0].Content != "kept" {
		t.Fatalf("got %+v, want only the usable entry", got)
	}
}

func TestParseCapturedCollapsesWhitespace(t *testing.T) {
	got := ParseCaptured(`[{"content":"a fact\n  spread\nover lines"}]`)
	if len(got) != 1 || got[0].Content != "a fact spread over lines" {
		t.Fatalf("content = %q", got[0].Content)
	}
}

func TestParseCapturedRejectsUnparseableReplies(t *testing.T) {
	for _, reply := range []string{
		"",
		"I could not find anything to remember.",
		`{"content":"not an array"}`,
		`[{"content":}]`,
	} {
		if got := ParseCaptured(reply); len(got) != 0 {
			t.Fatalf("ParseCaptured(%q) = %+v, want nothing", reply, got)
		}
	}
}

func TestParseCapturedCapsOneTurn(t *testing.T) {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < CaptureLimit+4; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"content":"fact number `)
		b.WriteString(string(rune('a' + i)))
		b.WriteString(`"}`)
	}
	b.WriteString("]")
	if got := ParseCaptured(b.String()); len(got) != CaptureLimit {
		t.Fatalf("got %d facts, want the cap of %d", len(got), CaptureLimit)
	}
}

func msgs(pairs ...any) []*store.Message {
	var out []*store.Message
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, &store.Message{
			ID:      int64(i/2 + 1),
			Role:    pairs[i].(string),
			Content: pairs[i+1].(string),
		})
	}
	return out
}

func TestTurnExchangeUsesTheTurnOnly(t *testing.T) {
	list := msgs(
		"user", "an earlier question",
		"assistant", "an earlier answer",
		"user", "what does the deploy script do?",
		"assistant", "intermediate narration",
		"tool", `{"output":"a huge tool result"}`,
		"assistant", "it runs scripts/deploy.sh and then restarts the service",
	)
	got := TurnExchange(list, 3)
	if strings.Contains(got, "an earlier question") || strings.Contains(got, "an earlier answer") {
		t.Fatalf("an earlier turn leaked into the exchange:\n%s", got)
	}
	if strings.Contains(got, "huge tool result") {
		t.Fatalf("tool output leaked into the exchange:\n%s", got)
	}
	if !strings.Contains(got, "what does the deploy script do?") {
		t.Fatalf("the user's message is missing:\n%s", got)
	}
	// The final assistant message is the turn's conclusion; earlier narration
	// from the same turn is superseded by it.
	if strings.Contains(got, "intermediate narration") {
		t.Fatalf("intermediate narration was kept:\n%s", got)
	}
	if !strings.Contains(got, "it runs scripts/deploy.sh") {
		t.Fatalf("the final reply is missing:\n%s", got)
	}
}

func TestTurnExchangeKeepsEveryUserMessage(t *testing.T) {
	list := msgs(
		"user", "add the retry",
		"assistant", "done",
		"user", "actually make it five minutes",
	)
	got := TurnExchange(list, 1)
	if !strings.Contains(got, "add the retry") || !strings.Contains(got, "actually make it five minutes") {
		t.Fatalf("a mid-turn correction was dropped:\n%s", got)
	}
}

func TestTurnExchangeTruncatesAndSaysSo(t *testing.T) {
	long := strings.Repeat("x", captureUserLimit+500)
	got := TurnExchange(msgs("user", long, "assistant", "short"), 1)
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("a truncated exchange did not say so:\n%s", got[:80])
	}
	if len([]rune(got)) > captureUserLimit+captureReplyLimit+100 {
		t.Fatalf("the exchange was not bounded: %d runes", len([]rune(got)))
	}
}

func TestTurnExchangeIsEmptyWithoutATurn(t *testing.T) {
	if got := TurnExchange(msgs("user", "old", "assistant", "old"), 99); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := TurnExchange(nil, 1); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestCapturePromptListsKnownMemories(t *testing.T) {
	got := CapturePrompt([]string{"the user prefers Tailwind", "deploys go through scripts/deploy.sh"}, "User:\nhi")
	if !strings.Contains(got, "Known memories") || !strings.Contains(got, "prefers Tailwind") {
		t.Fatalf("known memories are missing:\n%s", got)
	}
	if !strings.HasSuffix(got, "User:\nhi") {
		t.Fatalf("the exchange is not last:\n%s", got)
	}
	// With nothing known the section is omitted rather than left empty.
	bare := CapturePrompt(nil, "User:\nhi")
	if strings.Contains(bare, "Known memories") {
		t.Fatalf("an empty known list still produced a section:\n%s", bare)
	}
}
