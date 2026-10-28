package agent

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"v1/internal/llm"
)

// maxInlineTextAttachment bounds how much of a text attachment is inlined into
// the model's context. The full text stays on the message row for the chat UI
// and the attachment endpoint; only the copy sent to the model is truncated,
// matching pi's 50 KB read limit, so one huge log cannot blow the context.
const maxInlineTextAttachment = 51200

// InlineTextAttachment returns the text to place in front of the model,
// truncated with a note when the attachment is larger than the inline budget.
func InlineTextAttachment(a Attachment) string {
	if len(a.Content) <= maxInlineTextAttachment {
		return a.Content
	}
	cut := maxInlineTextAttachment
	for cut > 0 && !utf8.RuneStart(a.Content[cut]) {
		cut--
	}
	return a.Content[:cut] + fmt.Sprintf("\n\n[truncated: showing %d of %d bytes — the full file is attached to the message]", cut, len(a.Content))
}

// Attachment is a file attached to a user chat message. Kind is "text"
// (Content holds the file's text) or "image" (Content holds base64 data and
// MIME identifies the format). Persisted as JSON on the message row so
// history replays (retry/edit/follow-up turns) re-attach the files.
type Attachment struct {
	Name    string `json:"name"`
	MIME    string `json:"mime"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// MarshalAttachments encodes attachments for storage ("" when none).
func MarshalAttachments(atts []Attachment) string {
	if len(atts) == 0 {
		return ""
	}
	b, err := json.Marshal(atts)
	if err != nil {
		return ""
	}
	return string(b)
}

// ParseAttachments decodes stored attachments (nil when empty/invalid).
func ParseAttachments(raw string) []Attachment {
	if raw == "" {
		return nil
	}
	var out []Attachment
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

// AttachmentMeta describes an attachment without its content (for SSE events;
// the content is served separately by the message-attachment endpoint).
type AttachmentMeta struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Kind string `json:"kind"`
	Size int    `json:"size"`
}

// AttachmentMetas summarises attachments for an SSE event: the transcript
// renders a chip from this and fetches the content only when the file is
// opened, so a large image is not re-sent down the stream.
func AttachmentMetas(atts []Attachment) []AttachmentMeta {
	if len(atts) == 0 {
		return nil
	}
	out := make([]AttachmentMeta, 0, len(atts))
	for _, a := range atts {
		size := len(a.Content)
		if a.Kind == "image" {
			// Content is base64, so the decoded size is 3/4 of the encoded one.
			size = len(a.Content) / 4 * 3
		}
		out = append(out, AttachmentMeta{Name: a.Name, MIME: a.MIME, Kind: a.Kind, Size: size})
	}
	return out
}

// SteerMessage is one mid-run user message: a follow-up that joined a run that
// was already in flight. It carries attachments so a queued or steered message
// can bring files along exactly like a fresh turn can.
type SteerMessage struct {
	Text        string
	Attachments []Attachment
}

func textPart(t string) map[string]any {
	return map[string]any{"type": "text", "text": t}
}

// imagePart returns an OpenAI image_url content part (data URL).
func imagePart(a Attachment) map[string]any {
	return map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "data:" + a.MIME + ";base64," + a.Content,
		},
	}
}

// userMessage builds the LLM message for a user turn. Attachments become
// content parts: images as vision parts, text files inlined as code blocks.
func userMessage(content string, atts []Attachment) llm.Message {
	if len(atts) == 0 {
		return llm.Message{Role: "user", Content: content}
	}
	parts := []any{}
	// An empty text part is a rejected content block, not a harmless no-op, so a
	// turn carrying only an attachment omits it (see harnessUserContent).
	if content != "" {
		parts = append(parts, textPart(content))
	}
	for _, a := range atts {
		switch a.Kind {
		case "image":
			parts = append(parts, imagePart(a))
		default:
			parts = append(parts, textPart("Attached file: "+a.Name+"\n```\n"+InlineTextAttachment(a)+"\n```"))
		}
	}
	return llm.Message{Role: "user", Content: parts}
}

// hasImageParts reports whether any history message carries vision parts.
func hasImageParts(history []llm.Message) bool {
	for _, m := range history {
		parts, ok := m.Content.([]any)
		if !ok {
			continue
		}
		for _, p := range parts {
			if pm, ok := p.(map[string]any); ok && pm["type"] == "image_url" {
				return true
			}
		}
	}
	return false
}

// stripImageParts rebuilds history with image parts replaced by a text note,
// for models that reject vision input.
func stripImageParts(history []llm.Message) []llm.Message {
	out := make([]llm.Message, len(history))
	copy(out, history)
	for i, m := range out {
		parts, ok := m.Content.([]any)
		if !ok {
			continue
		}
		rebuilt := []any{}
		hadImage := false
		for _, p := range parts {
			pm, ok := p.(map[string]any)
			if ok && pm["type"] == "image_url" {
				hadImage = true
				continue
			}
			rebuilt = append(rebuilt, p)
		}
		if hadImage {
			rebuilt = append(rebuilt, textPart("[An image was attached to this message, but this model does not support images.]"))
			out[i].Content = rebuilt
		}
	}
	return out
}
