import { useMemo, useState } from 'react';
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
    <div className="relative">
      <Input
        className={className}
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') setOpen(false);
        }}
        placeholder="Select or type a model id"
        autoComplete="off"
      />
      {open && filtered.length > 0 && (
        <div className="absolute z-30 mt-1 max-h-56 w-full overflow-y-auto rounded-lg border border-border bg-bg py-1 shadow-xl">
          {filtered.map((m) => (
            <button
              key={m.id}
              type="button"
              onMouseDown={(e) => {
                e.preventDefault(); // keep focus; select before blur closes the list
                onChange(m.id);
                setOpen(false);
              }}
              className="flex w-full items-baseline gap-2 px-3 py-2 text-left transition-colors hover:bg-surface"
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
