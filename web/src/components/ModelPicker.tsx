import { useEffect, useMemo, useState } from 'react';
import type { ProviderModel, SavedProvider } from '../types';
import { fuzzyScore } from '../utils';
import { Dialog, Input } from './ui';
import { IconCheck, IconPaperclip, IconPencil, IconStar, IconX } from './icons';

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
  const [draftModel, setDraftModel] = useState(model);
  // When true, the selected provider shows a free-text field so you can enter
  // an arbitrary model id without switching to the separate Custom provider.
  const [customModel, setCustomModel] = useState(false);
  // Favourite model ids per provider, kept on this device.
  const favKey = `v1-fav-models:${providerId || 'custom'}`;
  const [favs, setFavs] = useState<string[]>(() => {
    try {
      return JSON.parse(localStorage.getItem(favKey) ?? '[]');
    } catch {
      return [];
    }
  });

  useEffect(() => {
    if (open) {
      setQuery('');
      setDraftModel(model);
      setCustomModel(false);
    }
  }, [open, model]);

  useEffect(() => {
    try {
      setFavs(JSON.parse(localStorage.getItem(favKey) ?? '[]'));
    } catch {
      setFavs([]);
    }
  }, [favKey]);

  const toggleFav = (id: string) => {
    setFavs((prev) => {
      const next = prev.includes(id) ? prev.filter((f) => f !== id) : [...prev, id];
      localStorage.setItem(favKey, JSON.stringify(next));
      return next;
    });
  };

  // Previously used custom model ids, most recent first, kept on this device.
  const [usedCustom, setUsedCustom] = useState<string[]>(() => {
    try {
      return JSON.parse(localStorage.getItem('v1-custom-models') ?? '[]');
    } catch {
      return [];
    }
  });
  const persistCustom = (next: string[]) => {
    localStorage.setItem('v1-custom-models', JSON.stringify(next));
    setUsedCustom(next);
  };
  const saveCustomModel = (id: string) => {
    persistCustom([id, ...usedCustom.filter((m) => m !== id)].slice(0, 20));
    onModelChange(id);
  };
  const editCustomModel = (id: string) => {
    setDraftModel(id);
    persistCustom(usedCustom.filter((m) => m !== id));
  };
  const deleteCustomModel = (id: string) => {
    persistCustom(usedCustom.filter((m) => m !== id));
  };

  const filtered = useMemo(() => {
    const favFirst = (a: ProviderModel, b: ProviderModel) => {
      const fa = favs.includes(a.id) ? 0 : 1;
      const fb = favs.includes(b.id) ? 0 : 1;
      return fa - fb;
    };
    const q = query.trim();
    if (!q) return [...models].sort(favFirst).slice(0, 200);
    return models
      .map((m) => ({
        m,
        s: Math.min(fuzzyScore(q, m.name) ?? Infinity, fuzzyScore(q, m.id) ?? Infinity),
      }))
      .filter((x) => x.s !== Infinity)
      .sort((a, b) => favFirst(a.m, b.m) || a.s - b.s)
      .slice(0, 200)
      .map((x) => x.m);
  }, [models, query, favs]);

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
          <section className="flex min-h-0 flex-1 flex-col">
            <h3 className="mb-2 shrink-0 text-xs font-medium text-subtle">Custom model</h3>
            <div className="flex shrink-0 items-center gap-2">
              <Input
                value={draftModel}
                onChange={(e) => setDraftModel(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    if (draftModel.trim()) saveCustomModel(draftModel.trim());
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
                onClick={() => saveCustomModel(draftModel.trim())}
                className="shrink-0 rounded-lg border border-border bg-surface px-3 py-2 text-xs font-medium text-text transition-colors hover:border-border-strong disabled:opacity-40"
              >
                Save
              </button>
            </div>
            <p className="mt-1.5 shrink-0 text-xs text-subtle">
              Your draft survives switching providers — it is applied when you
              press Save.
            </p>
            {usedCustom.length > 0 && (
              <div className="mt-3 min-h-0 flex-1 overflow-y-auto overscroll-contain">
                <h4 className="mb-1.5 text-xs font-medium text-subtle">Previously used</h4>
                <div className="flex flex-col gap-1">
                  {usedCustom.map((id) => (
                    <div
                      key={id}
                      className={`flex min-h-[36px] w-full items-center gap-1 rounded-lg border px-2.5 py-1.5 transition-colors ${
                        id === model ? 'border-accent bg-surface' : 'border-border'
                      }`}
                    >
                      <button
                        type="button"
                        onClick={() => onModelChange(id)}
                        className="min-w-0 flex-1 truncate text-left font-mono text-xs text-text"
                        title={id}
                      >
                        {id}
                      </button>
                      {id === model && <IconCheck className="h-3.5 w-3.5 shrink-0 text-accent" />}
                      <button
                        type="button"
                        onClick={() => editCustomModel(id)}
                        title="Edit"
                        className="shrink-0 rounded p-1 text-faint transition-colors hover:text-text"
                      >
                        <IconPencil className="h-3.5 w-3.5" />
                      </button>
                      <button
                        type="button"
                        onClick={() => deleteCustomModel(id)}
                        title="Delete"
                        className="shrink-0 rounded p-1 text-faint transition-colors hover:text-red-300"
                      >
                        <IconX className="h-3.5 w-3.5" />
                      </button>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </section>
        ) : (
          <>
            <section className="shrink-0">
              <div className="mb-2 flex items-center justify-between gap-2">
                <h3 className="text-xs font-medium text-subtle">Model</h3>
                <button
                  type="button"
                  onClick={() => setCustomModel((v) => !v)}
                  className="inline-flex items-center gap-1 rounded-full border border-border-strong px-2.5 py-1 text-xs text-text transition-colors hover:bg-surface"
                >
                  {customModel ? 'Browse models' : 'Custom model…'}
                </button>
              </div>
              {customModel ? (
                <div className="flex items-center gap-2">
                  <Input
                    value={draftModel}
                    onChange={(e) => setDraftModel(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' && draftModel.trim()) {
                        e.preventDefault();
                        onModelChange(draftModel.trim());
                        onClose();
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
                    onClick={() => {
                      onModelChange(draftModel.trim());
                      onClose();
                    }}
                    className="shrink-0 rounded-lg border border-border bg-surface px-3 py-2 text-xs font-medium text-text transition-colors hover:border-border-strong disabled:opacity-40"
                  >
                    Save
                  </button>
                </div>
              ) : (
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Search models"
                  spellCheck={false}
                  autoComplete="off"
                />
              )}
            </section>
            {!customModel && (
              <div className="fade-y min-h-0 min-w-0 flex-1 overflow-y-auto overscroll-contain">
                <div className="flex flex-col gap-1">
                  {filtered.length === 0 && (
                    <p className="px-1 py-2 text-xs text-faint">No models match.</p>
                  )}
                  {filtered.map((m) => (
                    <div
                      key={m.id}
                      role="button"
                      tabIndex={0}
                      onClick={() => {
                        onModelChange(m.id);
                        onClose();
                      }}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          onModelChange(m.id);
                          onClose();
                        }
                      }}
                      className={`flex min-h-[44px] w-full cursor-pointer items-center gap-2 overflow-hidden rounded-lg border px-3 py-2 text-left transition-colors ${
                        m.id === model
                          ? 'border-accent bg-surface'
                          : 'border-border hover:border-border-strong'
                      }`}
                    >
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm text-text" title={m.name || m.id}>
                          {m.name || m.id}
                        </span>
                        {m.name && m.name !== m.id && (
                          <span className="block truncate font-mono text-[11px] text-faint" title={m.id}>
                            {m.id}
                          </span>
                        )}
                      </span>
                      {m.imageInput && (
                        <span title="Supports image uploads" className="shrink-0 text-accent">
                          <IconPaperclip className="h-3.5 w-3.5" />
                        </span>
                      )}
                      <button
                        type="button"
                        onClick={(e) => {
                          e.stopPropagation();
                          toggleFav(m.id);
                        }}
                        title={favs.includes(m.id) ? 'Remove from favourites' : 'Favourite'}
                        className={`shrink-0 rounded p-0.5 transition-colors ${
                          favs.includes(m.id)
                            ? 'text-amber-400'
                            : 'text-faint hover:text-text'
                        }`}
                      >
                        <IconStar
                          className="h-3.5 w-3.5"
                          fill={favs.includes(m.id) ? 'currentColor' : 'none'}
                        />
                      </button>
                      {m.id === model && <IconCheck className="h-4 w-4 shrink-0 text-accent" />}
                    </div>
                  ))}
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </Dialog>
  );
}
