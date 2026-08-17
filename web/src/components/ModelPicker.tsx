import { useEffect, useMemo, useState } from 'react';
import type { ProviderModel, SavedProvider } from '../types';
import { Dialog, Input } from './ui';
import { IconCheck, IconPaperclip } from './icons';

/**
 * Fullscreen (on mobile) model picker: provider pills on top, searchable
 * model list below. Used on small screens where the desktop combobox row is
 * too cramped; the provider switcher lives here instead of in the chat bar.
 */
// VSCode-style fuzzy match: subsequence with penalties for gaps and
// mid-word hits. Returns a score (lower = better), or null when the query
// is not a subsequence of the text.
function fuzzyScore(query: string, text: string): number | null {
  const q = query.toLowerCase();
  const t = text.toLowerCase();
  let qi = 0;
  let score = 0;
  let prev = -2;
  for (let i = 0; i < t.length && qi < q.length; i++) {
    if (t[i] === q[qi]) {
      if (i !== prev + 1) score += 3;
      if (i > 0 && /[a-z0-9]/.test(t[i - 1])) score += 2;
      prev = i;
      qi++;
    }
  }
  return qi === q.length ? score : null;
}

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
  const [draftModel, setDraftModel] = useState(model);

  useEffect(() => {
    if (open) {
      setQuery('');
      setDraftModel(model);
    }
  }, [open, model]);

  const filtered = useMemo(() => {
    const q = query.trim();
    if (!q) return models.slice(0, 200);
    return models
      .map((m) => ({
        m,
        s: Math.min(fuzzyScore(q, m.name) ?? Infinity, fuzzyScore(q, m.id) ?? Infinity),
      }))
      .filter((x) => x.s !== Infinity)
      .sort((a, b) => a.s - b.s)
      .slice(0, 200)
      .map((x) => x.m);
  }, [models, query]);

  return (
    <Dialog open={open} onClose={onClose} title="Model" wide fullScreen fixedBody align="top">
      <div className="flex h-full min-h-0 flex-col gap-4">
        <section className="shrink-0">
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

        {providerId === '' ? (
          <section className="shrink-0">
            <h3 className="mb-2 text-xs font-medium text-subtle">Custom model</h3>
            <div className="flex items-center gap-2">
              <Input
                value={draftModel}
                onChange={(e) => setDraftModel(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    onModelChange(draftModel.trim());
                  }
                }}
                placeholder="model id"
                spellCheck={false}
                autoComplete="off"
                className="min-w-0 flex-1 font-mono text-xs"
              />
              <button
                type="button"
                disabled={!draftModel.trim()}
                onClick={() => onModelChange(draftModel.trim())}
                className="shrink-0 rounded-lg border border-border bg-surface px-3 py-2 text-xs font-medium text-text transition-colors hover:border-border-strong disabled:opacity-40"
              >
                Save
              </button>
            </div>
            <p className="mt-1.5 text-xs text-subtle">
              Your draft survives switching providers — it is applied when you
              press Save.
            </p>
          </section>
        ) : (
          <>
            <section className="shrink-0">
              <h3 className="mb-2 text-xs font-medium text-subtle">Model</h3>
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Search models"
                spellCheck={false}
                autoComplete="off"
              />
            </section>
            <div className="fade-y min-h-0 min-w-0 flex-1 overflow-y-auto overscroll-contain">
              <div className="flex flex-col gap-1">
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
            </div>
          </>
        )}
      </div>
    </Dialog>
  );
}
