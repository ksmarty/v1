package agent

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	// Register the decoders for the image formats accepted as attachments.
	_ "image/gif"
)

// Inline-image limits for the copy of an image placed in front of the model,
// mirroring pi's defaults: a longest edge and an encoded (base64) size. The
// attachment's bytes are the ones served to the chat UI, so scaling the stored
// copy keeps both the display and the model payload bounded no matter how large
// the file the user attached was.
const (
	maxInlineImageDim   = 2000
	maxInlineImageBytes = 45 * 1024 * 1024 / 10 // 4.5 MB of base64
	// maxDecodePixels bounds how large an image we are willing to decode to
	// scale it. A bigger source passes through untouched rather than risk an
	// out-of-memory decode; browsers scale before upload, so this only guards a
	// direct API client.
	maxDecodePixels = 80_000_000
)

// NormalizeImageAttachments scales every image attachment down to the inline
// limits. It is applied once, when a turn is accepted, so the stored copy and
// the model copy are already bounded. Non-image and undecodable attachments
// pass through untouched.
func NormalizeImageAttachments(atts []Attachment) []Attachment {
	for i := range atts {
		if atts[i].Kind == "image" {
			atts[i] = normalizeImageAttachment(atts[i])
		}
	}
	return atts
}

func normalizeImageAttachment(a Attachment) Attachment {
	raw, err := base64.StdEncoding.DecodeString(a.Content)
	if err != nil || len(raw) == 0 {
		return a
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		// No stdlib decoder (e.g. webp/bmp): leave the bytes for the provider to
		// judge rather than dropping the image.
		return a
	}
	if cfg.Width*cfg.Height > maxDecodePixels {
		return a
	}
	if cfg.Width <= maxInlineImageDim && cfg.Height <= maxInlineImageDim && len(a.Content) <= maxInlineImageBytes {
		return a
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return a
	}
	out, mime, ok := encodeImageWithin(img, format)
	if !ok {
		return a
	}
	a.Content = base64.StdEncoding.EncodeToString(out)
	a.MIME = mime
	return a
}

// encodeImageWithin encodes img under the inline byte cap, halving the longest
// edge until it fits. A JPEG source stays JPEG (lossy, small); everything else
// is re-encoded as PNG so screenshots and transparency survive.
func encodeImageWithin(img image.Image, format string) ([]byte, string, bool) {
	cur := img
	if b := cur.Bounds(); b.Dx() > maxInlineImageDim || b.Dy() > maxInlineImageDim {
		cur = downscaleImage(cur, maxInlineImageDim)
	}
	for attempt := 0; attempt < 6; attempt++ {
		var buf bytes.Buffer
		mime := "image/png"
		if format == "jpeg" {
			mime = "image/jpeg"
			if err := jpeg.Encode(&buf, cur, &jpeg.Options{Quality: 82}); err != nil {
				return nil, "", false
			}
		} else if err := png.Encode(&buf, cur); err != nil {
			return nil, "", false
		}
		// buf holds the raw bytes; the stored content is base64, which is ~4/3
		// the size, so compare against the base64 budget in those terms.
		if int64(buf.Len())*4/3 <= maxInlineImageBytes || cur.Bounds().Dx() <= 64 {
			return buf.Bytes(), mime, true
		}
		b := cur.Bounds()
		longest := b.Dx()
		if b.Dy() > longest {
			longest = b.Dy()
		}
		cur = downscaleImage(cur, longest/2)
	}
	return nil, "", false
}

// downscaleImage box-filters src down so its longest edge is maxDim. A box
// filter averages each destination pixel's source block, which is cheap and
// good enough for the model copy.
func downscaleImage(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxDim || longest == 0 {
		return src
	}
	if maxDim < 1 {
		maxDim = 1
	}
	scale := float64(maxDim) / float64(longest)
	nw := int(float64(w)*scale + 0.5)
	nh := int(float64(h)*scale + 0.5)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy0 := y * h / nh
		sy1 := (y + 1) * h / nh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < nw; x++ {
			sx0 := x * w / nw
			sx1 := (x + 1) * w / nw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, bl, al, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					bl += uint64(cb)
					al += uint64(ca)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(r / n >> 8),
				G: uint8(g / n >> 8),
				B: uint8(bl / n >> 8),
				A: uint8(al / n >> 8),
			})
		}
	}
	return dst
}
