package agent

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func pngAttachment(t *testing.T, w, h int) Attachment {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return Attachment{Name: "big.png", MIME: "image/png", Kind: "image", Content: base64.StdEncoding.EncodeToString(buf.Bytes())}
}

// A large image is scaled down to the inline longest edge and stays decodable.
func TestNormalizeImageAttachmentScalesDown(t *testing.T) {
	a := NormalizeImageAttachments([]Attachment{pngAttachment(t, 4000, 2500)})[0]
	raw, err := base64.StdEncoding.DecodeString(a.Content)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width > maxInlineImageDim || cfg.Height > maxInlineImageDim {
		t.Fatalf("scaled image is %dx%d, over %d", cfg.Width, cfg.Height, maxInlineImageDim)
	}
	if cfg.Width < 2 || cfg.Height < 2 {
		t.Fatalf("scaled image collapsed: %dx%d", cfg.Width, cfg.Height)
	}
}

// A small image is left byte-for-byte unchanged.
func TestNormalizeImageAttachmentKeepsSmall(t *testing.T) {
	orig := pngAttachment(t, 100, 80)
	got := NormalizeImageAttachments([]Attachment{orig})[0]
	if got.Content != orig.Content || got.MIME != orig.MIME {
		t.Fatal("a small image should pass through unchanged")
	}
}

// Non-image attachments are untouched.
func TestNormalizeImageAttachmentsIgnoresText(t *testing.T) {
	got := NormalizeImageAttachments([]Attachment{{Name: "a.txt", Kind: "text", Content: "hi"}})[0]
	if got.Content != "hi" {
		t.Fatal("text attachment was modified")
	}
}
