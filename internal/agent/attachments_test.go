package agent

import "testing"

// An attachment-only turn is allowed (no caption), and must not carry an empty
// text part: providers reject an empty text block, so the turn would fail for a
// message the user is permitted to send.
func TestUserMessageOmitsEmptyTextPart(t *testing.T) {
	msg := userMessage("", []Attachment{{Name: "shot.png", MIME: "image/png", Kind: "image", Content: "aGk="}})
	parts, ok := msg.Content.([]any)
	if !ok {
		t.Fatalf("content is %T, want parts", msg.Content)
	}
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want just the image: %#v", len(parts), parts)
	}
	for _, p := range parts {
		if pm, ok := p.(map[string]any); ok && pm["type"] == "text" && pm["text"] == "" {
			t.Fatalf("empty text part present: %#v", parts)
		}
	}
}

// A caption still leads the parts, so the text is not dropped for having an
// attachment alongside it.
func TestUserMessageKeepsCaption(t *testing.T) {
	msg := userMessage("what is this?", []Attachment{{Name: "shot.png", MIME: "image/png", Kind: "image", Content: "aGk="}})
	parts, ok := msg.Content.([]any)
	if !ok {
		t.Fatalf("content is %T, want parts", msg.Content)
	}
	first, ok := parts[0].(map[string]any)
	if !ok || first["text"] != "what is this?" {
		t.Fatalf("first part = %#v, want the caption", parts[0])
	}
}

// No attachments keeps the plain-string message every other turn uses.
func TestUserMessagePlainTextStaysString(t *testing.T) {
	if msg := userMessage("hello", nil); msg.Content != "hello" {
		t.Fatalf("content = %#v, want the plain string", msg.Content)
	}
}
