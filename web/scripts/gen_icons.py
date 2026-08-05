#!/usr/bin/env python3
"""Regenerates the PWA icons in web/public. Run from the repo root:
   python3 web/scripts/gen_icons.py
Produces icon-192.png, icon-512.png, icon-maskable-512.png, apple-touch-icon.png
on a dark rounded square with a bold "v1" and an accent cursor bar."""
from PIL import Image, ImageDraw, ImageFont
import os

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "public")
BG = "#0a0a0a"
ACCENT = "#3b82f6"
FONT = "/System/Library/Fonts/Supplemental/Arial Bold.ttf"


def font_for(target_width: int) -> ImageFont.FreeTypeFont:
    size = 10
    while True:
        font = ImageFont.truetype(FONT, size)
        bbox = font.getbbox("v1")
        if (bbox[2] - bbox[0]) >= target_width or size > 4000:
            return font
        size += 4


def render(path: str, size: int, radius: int, text_frac: float) -> None:
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    if radius:
        d.rounded_rectangle([0, 0, size - 1, size - 1], radius=radius, fill=BG)
    else:
        d.rectangle([0, 0, size - 1, size - 1], fill=BG)
    font = font_for(int(size * text_frac))
    bbox = font.getbbox("v1")
    tw = bbox[2] - bbox[0]
    th = bbox[3] - bbox[1]
    cx, cy = size / 2, size / 2
    d.text((cx, cy), "v1", font=font, fill="white", anchor="mm")
    bar_w = size * 0.05
    bar_h = th
    x0 = cx + tw / 2 + size * 0.035
    y0 = cy - bar_h / 2
    d.rounded_rectangle([x0, y0, x0 + bar_w, y0 + bar_h], radius=bar_w / 2, fill=ACCENT)
    img.save(path)


def main() -> None:
    os.makedirs(OUT, exist_ok=True)
    render(os.path.join(OUT, "icon-192.png"), 192, 42, 0.55)
    render(os.path.join(OUT, "icon-512.png"), 512, 112, 0.55)
    render(os.path.join(OUT, "icon-maskable-512.png"), 512, 0, 0.5)
    render(os.path.join(OUT, "apple-touch-icon.png"), 180, 0, 0.55)
    print("wrote", ", ".join(sorted(os.listdir(OUT))))


if __name__ == "__main__":
    main()
