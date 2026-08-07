import { useEffect, useMemo, useState } from 'react';
import type { ProviderModel, SavedProvider } from '../types';
import { Dialog, Input } from './ui';
import { IconCheck, IconPaperclip } from './icons';

/**
 * Fullscreen (on mobile) model picker: provider pills on top, searchable
 * model list below. Used on small screens where the desktop combobox row is
 * too cramped; the provider switcher lives here instead of in the chat bar.
 */
export default function ModelPicker({
  open,
  onClose,
  providers,
  providerId,
  model,
  models,
  onProviderChange,
  onModelChange,
}: {
  open: boolean;
  onClose: () => void;
  providers: SavedProvider[];
  providerId: string; // '' = custom (no saved provider)
  model: string;
  models: ProviderModel[];
  onProviderChange: (id: string) => void;
  onModelChange: (id: string) => void;
}) {
  const [query, setQuery] = useState('');

  useEffect(() => {
    if (!open) setQuery('');
  }, [open]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = q
      ? models.filter(
          (m) => m.name.toLowerCase().includes(q) || m.id.toLowerCase().includes(q),
        )
      : models;
    return list.slice(0, 200);
  }, [models, query]);

  return (
    <Dialog open={open} onClose={onClose} title="Model" fullScreen fixedBody align="top">
      <div className="flex flex-col gap-5">
        <section>
          <h3 className="mb-2 text-xs font-medium text-subtle">Provider</h3>
          <div className="flex flex-wrap gap-1.5">
            {providers.map((p) => (
              <button
                key={p.id}
                type="button"
                onClick={() => onProviderChange(p.id)}
                className={`flex min-h-[36px] items-center gap-1.5 rounded-full border px-3 text-sm transition-colors ${
                  p.id === providerId
                    ? 'border-accent bg-surface text-text'
                    : 'border-border text-subtle hover:border-border-strong hover:text-text'
                }`}
              >
                {p.name}
                {p.id === providerId && <IconCheck className="h-3.5 w-3.5 text-accent" />}
              </button>
            ))}
            <button
              type="button"
              onClick={() => onProviderChange('')}
              className={`flex min-h-[36px] items-center gap-1.5 rounded-full border px-3 text-sm transition-colors ${
                providerId === ''
                  ? 'border-accent bg-surface text-text'
                  : 'border-border text-subtle hover:border-border-strong hover:text-text'
              }`}
            >
              Custom
              {providerId === '' && <IconCheck className="h-3.5 w-3.5 text-accent" />}
            </button>
          </div>
        </section>

        <section>
          <h3 className="mb-2 text-xs font-medium text-subtle">Model</h3>
          {providerId === '' ? (
            <Input
              value={model}
              onChange={(e) => onModelChange(e.target.value)}
              placeholder="model id"
              spellCheck={false}
              autoComplete="off"
              className="font-mono text-xs"
            />
          ) : (
            <>
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Search models"
                spellCheck={false}
                autoComplete="off"
              />
              <div className="mt-2 flex flex-col gap-1">
                {filtered.length === 0 && (
                  <p className="px-1 py-2 text-xs text-faint">No models match.</p>
                )}
                {filtered.map((m) => (
                  <button
                    key={m.id}
                    type="button"
                    onClick={() => {
                      onModelChange(m.id);
                      onClose();
                    }}
                    className={`flex min-h-[44px] items-center gap-2 rounded-lg border px-3 py-2 text-left transition-colors ${
                      m.id === model
                        ? 'border-accent bg-surface'
                        : 'border-border hover:border-border-strong'
                    }`}
                  >
                    <span className="min-w-0 flex-1 truncate text-sm text-text">{m.name}</span>
                    {m.imageInput && (
                      <span title="Supports image uploads" className="shrink-0 text-accent">
                        <IconPaperclip className="h-3.5 w-3.5" />
                      </span>
                    )}
                    <span className="shrink-0 font-mono text-[11px] text-faint">{m.id}</span>
                    {m.id === model && <IconCheck className="h-4 w-4 shrink-0 text-accent" />}
                  </button>
                ))}
              </div>
            </>
          )}
        </section>
      </div>
    </Dialog>
  );
}
