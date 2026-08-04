import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api';
import type { Provider, ProviderModel } from '../types';
import { errMsg } from '../utils';
import { Button, ErrorBox, Input, Select, Spinner } from './ui';
import { IconExternalLink, IconRefresh } from './icons';

const CUSTOM_ID = 'custom';

function ModelCombobox({
  models,
  value,
  onChange,
}: {
  models: ProviderModel[];
  value: string;
  onChange: (v: string) => void;
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

export default function ProviderSelector({
  baseURL,
  model,
  onBaseURLChange,
  onModelChange,
}: {
  baseURL: string;
  model: string;
  onBaseURLChange: (v: string) => void;
  onModelChange: (v: string) => void;
}) {
  const [providers, setProviders] = useState<Provider[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<string>(CUSTOM_ID);
  const [refreshing, setRefreshing] = useState(false);
  const [refreshMsg, setRefreshMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const matchedOnce = useRef(false);

  const load = useCallback(async () => {
    try {
      const res = await api.getProviders();
      setProviders(res.providers);
      setLoadError(null);
    } catch (e) {
      setLoadError(errMsg(e));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Preselect the provider matching the configured base URL — once, on first load.
  useEffect(() => {
    if (!providers || matchedOnce.current) return;
    matchedOnce.current = true;
    const match = providers.find((p) => p.id !== CUSTOM_ID && p.baseURL && p.baseURL === baseURL);
    setSelectedId(match ? match.id : CUSTOM_ID);
  }, [providers, baseURL]);

  const selected = providers?.find((p) => p.id === selectedId) ?? null;
  const hasModels = (selected?.models.length ?? 0) > 0;

  const choose = (id: string) => {
    if (id === selectedId) return;
    setSelectedId(id);
    const p = providers?.find((x) => x.id === id);
    if (p && p.baseURL) onBaseURLChange(p.baseURL);
    onModelChange('');
  };

  const refresh = async () => {
    setRefreshing(true);
    setRefreshMsg(null);
    try {
      const r = await api.refreshProviders();
      if (r.ok) {
        setRefreshMsg({
          ok: true,
          text: `Model lists updated${typeof r.count === 'number' ? ` (${r.count})` : ''}.`,
        });
        await load();
      } else {
        setRefreshMsg({ ok: false, text: r.error || 'Refresh failed.' });
      }
    } catch (e) {
      setRefreshMsg({ ok: false, text: errMsg(e) });
    } finally {
      setRefreshing(false);
    }
  };

  return (
    <div className="flex flex-col gap-3">
      <div>
        <div className="mb-1 flex items-center justify-between gap-2">
          <span className="text-xs text-subtle">Provider</span>
          <span className="flex items-center gap-1.5">
            {refreshMsg && (
              <span className={`text-xs ${refreshMsg.ok ? 'text-emerald-500' : 'text-red-400'}`}>
                {refreshMsg.text}
              </span>
            )}
            <button
              type="button"
              onClick={() => void refresh()}
              disabled={refreshing}
              title="Refresh model lists from models.dev"
              aria-label="Refresh model lists"
              className="inline-flex h-6 w-6 items-center justify-center rounded-md text-faint transition-colors hover:bg-border hover:text-text disabled:opacity-40"
            >
              {refreshing ? (
                <Spinner className="h-3.5 w-3.5" />
              ) : (
                <IconRefresh className="h-3.5 w-3.5" />
              )}
            </button>
          </span>
        </div>
        {providers === null && !loadError && (
          <div className="flex h-[38px] items-center rounded-lg border border-border bg-surface px-3">
            <Spinner className="h-4 w-4" />
          </div>
        )}
        {loadError && (
          <div className="flex items-center gap-2">
            <ErrorBox message={loadError} className="flex-1" />
            <Button variant="outline" onClick={() => void load()}>
              Retry
            </Button>
          </div>
        )}
        {providers !== null && (
          <Select value={selectedId} onChange={(e) => choose(e.target.value)}>
            {providers.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </Select>
        )}
        {selected && (selected.keyHint || selected.doc) && (
          <p className="mt-1.5 text-xs text-faint">
            {selected.keyHint}
            {selected.keyHint && selected.doc ? ' · ' : ''}
            {selected.doc && (
              <a
                href={selected.doc}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-0.5 text-accent hover:underline"
              >
                docs <IconExternalLink className="h-3 w-3" />
              </a>
            )}
          </p>
        )}
      </div>

      <label className="block">
        <span className="mb-1 block text-xs text-subtle">Base URL</span>
        <Input
          value={baseURL}
          onChange={(e) => onBaseURLChange(e.target.value)}
          placeholder="https://api.openai.com/v1"
        />
      </label>

      <label className="block">
        <span className="mb-1 block text-xs text-subtle">Model</span>
        {hasModels && selected ? (
          <ModelCombobox models={selected.models} value={model} onChange={onModelChange} />
        ) : (
          <Input
            value={model}
            onChange={(e) => onModelChange(e.target.value)}
            placeholder="gpt-4o"
          />
        )}
      </label>
    </div>
  );
}
