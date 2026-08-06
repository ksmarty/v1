package agent

import (
	"encoding/json"

	"v1/internal/llm"
)

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
	parts := []any{textPart(content)}
	for _, a := range atts {
		switch a.Kind {
		case "image":
			parts = append(parts, imagePart(a))
		default:
			parts = append(parts, textPart("Attached file: "+a.Name+"\n```\n"+a.Content+"\n```"))
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
