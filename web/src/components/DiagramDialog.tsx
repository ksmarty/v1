import { useEffect, useRef, useState } from 'react';
import { IconX } from './icons';
import { diagramName, exportPng, exportSvg } from '../mermaid';

// Viewport-fixed overlays sit under the notch / Dynamic Island unless they add
// the safe-area inset themselves (an absolutely positioned child ignores its
// parent's padding), so the offsets carry the inset on the element.
const SAFE_TOP = 'calc(env(safe-area-inset-top, 0px) + 0.75rem)';
const SAFE_BOTTOM = 'calc(env(safe-area-inset-bottom, 0px) + 0.75rem)';

// DiagramDialog shows a rendered mermaid diagram full size, the way
// ImageLightbox does for images, and exports it as SVG or a 3x PNG.
export default function DiagramDialog({
  svg,
  code,
  onClose,
}: {
  svg: string;
  code: string;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState<'png' | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const name = diagramName(code);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeRef.current();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const onPng = () => {
    setError(null);
    setBusy('png');
    exportPng(svg, name, 3)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(null));
  };

  const onCopy = () => {
    void navigator.clipboard
      .writeText(code)
      .then(() => {
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => setError('The clipboard is not available.'));
  };

  return (
    <div
      className="v1-safe-top fixed inset-0 z-50 flex flex-col bg-bg/90 backdrop-blur-sm"
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label="Diagram"
    >
      <div
        style={{ paddingTop: SAFE_TOP, paddingRight: '0.75rem', paddingLeft: '0.75rem' }}
        className="flex items-center justify-between gap-2"
      >
        <span className="truncate font-mono text-xs text-faint">{name}</span>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close diagram"
          style={{ top: SAFE_TOP, right: '0.75rem' }}
          className="absolute inline-flex h-10 w-10 items-center justify-center rounded-lg bg-surface/80 text-dim transition-colors hover:bg-border hover:text-text"
        >
          <IconX className="h-5 w-5" />
        </button>
      </div>

      <div className="flex min-h-0 flex-1 items-center justify-center overflow-auto p-4">
        {/* The SVG comes from mermaid's own renderer, already sanitised by
            securityLevel: 'strict'. */}
        <div
          className="v1-diagram-full"
          onClick={(e) => e.stopPropagation()}
          dangerouslySetInnerHTML={{ __html: svg }}
        />
      </div>

      <div
        style={{ paddingBottom: SAFE_BOTTOM }}
        className="flex flex-wrap items-center justify-center gap-2 px-4 pt-2"
        onClick={(e) => e.stopPropagation()}
      >
        <button
          type="button"
          onClick={() => exportSvg(svg, name)}
          className="rounded-lg border border-border bg-surface px-3 py-1.5 text-xs text-text transition-colors hover:border-border-strong"
        >
          Export SVG
        </button>
        <button
          type="button"
          onClick={onPng}
          disabled={busy === 'png'}
          className="rounded-lg border border-border bg-surface px-3 py-1.5 text-xs text-text transition-colors hover:border-border-strong disabled:opacity-60"
        >
          {busy === 'png' ? 'Exporting…' : 'Export PNG (3×)'}
        </button>
        <button
          type="button"
          onClick={onCopy}
          className="rounded-lg border border-border bg-surface px-3 py-1.5 text-xs text-dim transition-colors hover:border-border-strong hover:text-text"
        >
          {copied ? 'Copied' : 'Copy source'}
        </button>
        {error && <span className="w-full text-center text-xs text-red-400">{error}</span>}
      </div>
    </div>
  );
}
