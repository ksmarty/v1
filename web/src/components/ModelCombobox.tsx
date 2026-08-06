import { useEffect, useMemo, useRef, useState } from 'react';
import type { ProviderModel } from '../types';
import { Input } from './ui';

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

  // Close on outside interaction instead of input blur — blur fires before a
  // tap lands on touch devices, which made the list vanish on iOS.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent | TouchEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    document.addEventListener('touchstart', onDoc, { passive: true });
    return () => {
      document.removeEventListener('mousedown', onDoc);
      document.removeEventListener('touchstart', onDoc);
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
      {open && filtered.length > 0 && (
        <div className="absolute z-30 mt-1 max-h-64 w-full overflow-y-auto rounded-lg border border-border bg-bg py-1 shadow-xl">
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
              <span className="shrink-0 font-mono text-xs text-faint">{m.id}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
