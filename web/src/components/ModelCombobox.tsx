import { useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import type { ProviderModel } from '../types';
import { Input } from './ui';
import { IconPaperclip } from './icons';

export function ModelCombobox({
  models,
  value,
  onChange,
  className = '',
}: {
  models: ProviderModel[];
  value: string;
  onChange: (v: string) => void;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  // The dropdown is portaled to document.body so it escapes scroll containers
  // and stacking contexts (e.g. the Settings page's sticky header clipping it).
  const listRef = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{
    top?: number;
    bottom?: number;
    left: number;
    width: number;
  } | null>(null);

  // Close on outside interaction instead of input blur — blur fires before a
  // tap lands on touch devices, which made the list vanish on iOS. The portaled
  // list counts as inside so scrollbar drags don't close it.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent | TouchEvent) => {
      const t = e.target as Node;
      const inside =
        (rootRef.current && rootRef.current.contains(t)) ||
        (listRef.current && listRef.current.contains(t));
      if (!inside) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    document.addEventListener('touchstart', onDoc, { passive: true });
    return () => {
      document.removeEventListener('mousedown', onDoc);
      document.removeEventListener('touchstart', onDoc);
    };
  }, [open]);

  // Pin the portaled list to the input and follow it while scrolling/resizing.
  const measure = () => {
    const el = rootRef.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    // Flip above the input when there's no room below (max-h-64 + 8px margin).
    const flip = r.bottom + 272 > window.innerHeight;
    setPos({
      top: flip ? undefined : r.bottom + 4,
      bottom: flip ? window.innerHeight - r.top + 4 : undefined,
      left: r.left,
      width: r.width,
    });
  };
  useEffect(() => {
    if (!open) return;
    measure();
    window.addEventListener('scroll', measure, true);
    window.addEventListener('resize', measure);
    return () => {
      window.removeEventListener('scroll', measure, true);
      window.removeEventListener('resize', measure);
    };
  }, [open]);

  const filtered = useMemo(() => {
    const q = value.trim().toLowerCase();
    const list = q
      ? models.filter(
          (m) => m.name.toLowerCase().includes(q) || m.id.toLowerCase().includes(q),
        )
      : models;
    return list.slice(0, 100);
  }, [models, value]);

  return (
    <div className="relative" ref={rootRef}>
      <Input
        className={className}
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') setOpen(false);
        }}
        placeholder="Select or type a model id"
        autoComplete="off"
      />
      {open &&
        filtered.length > 0 &&
        pos &&
        createPortal(
          <div
            ref={listRef}
            className="fixed z-[100] max-h-64 overflow-y-auto rounded-lg border border-border bg-bg py-1 shadow-xl"
            style={{ top: pos.top, bottom: pos.bottom, left: pos.left, width: pos.width }}
          >
            {filtered.map((m) => (
              <button
                key={m.id}
                type="button"
                onPointerDown={(e) => {
                  e.preventDefault(); // keep focus; select before the list closes
                  onChange(m.id);
                  setOpen(false);
                }}
                className="flex w-full min-h-[44px] items-baseline gap-2 px-3 py-2.5 text-left transition-colors hover:bg-surface md:min-h-0 md:py-2"
              >
                <span className="min-w-0 flex-1 truncate text-sm text-text">{m.name}</span>
                {m.imageInput && (
                  <span
                    title="Supports image uploads"
                    className="shrink-0 self-center text-accent"
                  >
                    <IconPaperclip className="h-3.5 w-3.5" />
                  </span>
                )}
                <span className="shrink-0 font-mono text-xs text-faint">{m.id}</span>
              </button>
            ))}
          </div>,
          document.body,
        )}
    </div>
  );
}