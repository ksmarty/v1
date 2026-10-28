import { useMemo, type MouseEvent } from 'react';
import { renderMarkdown } from '../markdown';

export default function Markdown({
  text,
  streaming,
  validTag,
  resolveImage,
  onImageClick,
}: {
  text: string;
  streaming?: boolean;
  validTag?: (tag: string) => boolean;
  /** Resolves a markdown image src (e.g. the `v1-screenshot` token) to a URL. */
  resolveImage?: (src: string) => string | null;
  /** Opens an image the reply embedded (a screenshot, a diagram) full size. */
  onImageClick?: (url: string, name: string) => void;
}) {
  const html = useMemo(
    () => renderMarkdown(text, streaming, validTag, resolveImage),
    [text, streaming, validTag, resolveImage],
  );
  const onCopy = (e: MouseEvent<HTMLDivElement>) => {
    const target = e.target as HTMLElement;
    // An image in the reply opens in the lightbox, like a thumbnail attachment
    // does — model-written screenshots were dead to clicks before.
    const img = target.closest('img');
    if (img && onImageClick) {
      const src = img.getAttribute('src');
      if (src) {
        onImageClick(src, img.getAttribute('alt') || 'Image');
        return;
      }
    }
    const btn = target.closest('button[data-copy]');
    if (!(btn instanceof HTMLButtonElement)) return;
    const code = btn.parentElement?.querySelector('code');
    const content = code?.textContent ?? '';
    if (!content) return;
    void navigator.clipboard
      .writeText(content)
      .then(() => {
        const label = btn.textContent;
        btn.textContent = 'Copied';
        window.setTimeout(() => {
          btn.textContent = label;
        }, 1500);
      })
      .catch(() => {
        // clipboard unavailable — ignore
      });
  };
  return (
    <div
      className={`md${onImageClick ? ' md-clickable-images' : ''}`}
      onClick={onCopy}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}
