import { useLayoutEffect, useMemo, useRef, type KeyboardEvent, type MouseEvent } from 'react';
import {
  DIAGRAM_ATTR,
  decodeDiagramSrc,
  injectCachedDiagrams,
  renderMarkdown,
} from '../markdown';
import { cachedDiagram, renderDiagram } from '../mermaid';

function diagramError(e: unknown): string {
  const message = e instanceof Error ? e.message : String(e);
  // mermaid throws the parser's multi-line diagnostic; the first line is the
  // useful one and the rest is a caret diagram of the offending line.
  return message.split('\n')[0].replace(/^Error:\s*/, '');
}

export default function Markdown({
  text,
  streaming,
  validTag,
  resolveImage,
  onImageClick,
  onDiagramClick,
}: {
  text: string;
  streaming?: boolean;
  validTag?: (tag: string) => boolean;
  /** Resolves a markdown image src (e.g. the `v1-screenshot` token) to a URL. */
  resolveImage?: (src: string) => string | null;
  /** Opens an image the reply embedded (a screenshot, a diagram) full size. */
  onImageClick?: (url: string, name: string) => void;
  /** Opens a rendered mermaid diagram in the diagram dialog (SVG + code). */
  onDiagramClick?: (svg: string, code: string) => void;
}) {
  const html = useMemo(() => {
    const raw = renderMarkdown(text, streaming, validTag, resolveImage);
    // Diagrams already rendered in this session are inlined while rendering,
    // so a streaming reply never blinks them back to a placeholder.
    return injectCachedDiagrams(raw, cachedDiagram);
  }, [text, streaming, validTag, resolveImage]);

  const ref = useRef<HTMLDivElement>(null);
  const openRef = useRef(onDiagramClick);
  openRef.current = onDiagramClick;

  // A ```mermaid fence renders as an empty placeholder; fill each one with the
  // SVG mermaid produces. This runs before paint so a warm cache shows the
  // diagram on the first frame of a re-render.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const nodes = Array.from(
      el.querySelectorAll<HTMLElement>(`[${DIAGRAM_ATTR}]:not(.is-ready):not(.is-failed)`),
    );
    if (nodes.length === 0) return;
    let cancelled = false;
    for (const node of nodes) {
      const code = decodeDiagramSrc(node.getAttribute(DIAGRAM_ATTR) ?? '');
      renderDiagram(code)
        .then((svg) => {
          if (cancelled || !node.isConnected) return;
          node.innerHTML = svg;
          node.classList.add('is-ready');
          if (openRef.current) {
            node.tabIndex = 0;
            node.setAttribute('role', 'button');
            node.setAttribute('aria-label', 'Open diagram');
          }
        })
        .catch((e: unknown) => {
          if (cancelled || !node.isConnected) return;
          node.classList.add('is-failed');
          node.textContent = `This diagram could not be rendered: ${diagramError(e)}`;
        });
    }
    return () => {
      cancelled = true;
    };
  }, [html]);

  const onClick = (e: MouseEvent<HTMLDivElement>) => {
    const target = e.target as HTMLElement;
    const diagram = target.closest(`[${DIAGRAM_ATTR}]`);
    if (diagram instanceof HTMLElement && diagram.classList.contains('is-ready')) {
      if (!openRef.current) return;
      const code = decodeDiagramSrc(diagram.getAttribute(DIAGRAM_ATTR) ?? '');
      openRef.current(cachedDiagram(code) ?? diagram.innerHTML, code);
      return;
    }
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

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    const target = e.target as HTMLElement;
    const diagram = target.closest(`[${DIAGRAM_ATTR}]`);
    if (!(diagram instanceof HTMLElement) || !diagram.classList.contains('is-ready')) return;
    if (!openRef.current) return;
    e.preventDefault();
    const code = decodeDiagramSrc(diagram.getAttribute(DIAGRAM_ATTR) ?? '');
    openRef.current(cachedDiagram(code) ?? diagram.innerHTML, code);
  };

  return (
    <div
      ref={ref}
      className={`md${onImageClick ? ' md-clickable-images' : ''}${
        onDiagramClick ? ' md-clickable-diagrams' : ''
      }`}
      onClick={onClick}
      onKeyDown={onKeyDown}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}
