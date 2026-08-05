import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { api } from '../api';
import type { Provider } from '../types';
import { errMsg } from '../utils';
import { Button, ErrorBox, Input, Select, Spinner } from './ui';
import { IconExternalLink, IconRefresh, IconX } from './icons';
import { ModelCombobox } from './ModelCombobox';

const CUSTOM_ID = 'custom';
const BROWSED_VALUE = '__browsed__';

export default function ProviderSelector({
  baseURL,
  model,
  onBaseURLChange,
  onModelChange,
  hideModel = false,
  children,
}: {
  baseURL: string;
  model: string;
  onBaseURLChange: (v: string) => void;
  onModelChange: (v: string) => void;
  hideModel?: boolean;
  children?: ReactNode;
}) {
  const [providers, setProviders] = useState<Provider[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<string>(CUSTOM_ID);
  const [refreshing, setRefreshing] = useState(false);
  const [refreshMsg, setRefreshMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [browseOpen, setBrowseOpen] = useState(false);
  const [browseQ, setBrowseQ] = useState('');
  const [browseResults, setBrowseResults] = useState<Provider[] | null>(null);
  const [browseBusy, setBrowseBusy] = useState(false);
  const [browseError, setBrowseError] = useState<string | null>(null);
  const [browsed, setBrowsed] = useState<Provider | null>(null);
  const browseSeq = useRef(0);
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
  // The provider currently driving the form: a browsed models.dev result wins
  // over the curated dropdown selection until the user picks again.
  const active = browsed ?? selected;

  const choose = (id: string) => {
    setBrowsed(null);
    if (id === selectedId) return;
    setSelectedId(id);
    const p = providers?.find((x) => x.id === id);
    if (p && p.baseURL) onBaseURLChange(p.baseURL);
    onModelChange('');
  };

  const runBrowse = useCallback(() => {
    const q = browseQ.trim();
    const seq = ++browseSeq.current;
    setBrowseBusy(true);
    setBrowseError(null);
    api
      .searchProviders(q)
      .then((r) => {
        if (seq !== browseSeq.current) return;
        setBrowseResults(r.providers);
      })
      .catch((e) => {
        if (seq !== browseSeq.current) return;
        setBrowseResults(null);
        setBrowseError(errMsg(e));
      })
      .finally(() => {
        if (seq === browseSeq.current) setBrowseBusy(false);
      });
  }, [browseQ]);

  // Debounced search (250 ms) — also fires for an empty query to list everything.
  useEffect(() => {
    if (!browseOpen) return;
    const t = setTimeout(() => void runBrowse(), 250);
    return () => clearTimeout(t);
  }, [browseOpen, runBrowse]);

  const toggleBrowse = () => {
    if (browseOpen) {
      browseSeq.current++;
      setBrowseResults(null);
      setBrowseError(null);
      setBrowseBusy(false);
      setBrowseOpen(false);
    } else {
      setBrowseOpen(true);
    }
  };

  const pick = (r: Provider) => {
    setBrowsed(r);
    onBaseURLChange(r.baseURL);
    onModelChange('');
    browseSeq.current++;
    setBrowseOpen(false);
    setBrowseResults(null);
    setBrowseError(null);
    setBrowseBusy(false);
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
          <>
            <Select
              value={browsed ? BROWSED_VALUE : selectedId}
              onChange={(e) => choose(e.target.value)}
            >
              {browsed && <option value={BROWSED_VALUE}>{browsed.name}</option>}
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
            <div className="mt-2">
              <button
                type="button"
                onClick={toggleBrowse}
                className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
              >
                {browseOpen && <IconX className="h-3 w-3" />}
                {browseOpen ? 'Hide provider search' : 'Browse all models.dev providers…'}
              </button>
            </div>
          </>
        )}
        {active && (active.keyHint || active.doc) && (
          <p className="mt-1.5 text-xs text-faint">
            {active.keyHint}
            {active.keyHint && active.doc ? ' · ' : ''}
            {active.doc && (
              <a
                href={active.doc}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-0.5 text-accent hover:underline"
              >
                docs <IconExternalLink className="h-3 w-3" />
              </a>
            )}
          </p>
        )}

        {browseOpen && (
          <div className="mt-2 flex flex-col gap-2">
            <Input
              value={browseQ}
              onChange={(e) => setBrowseQ(e.target.value)}
              placeholder="Search models.dev — blank lists all providers"
              autoComplete="off"
            />
            {browseBusy && <Spinner className="h-4 w-4" />}
            {browseError && (
              <div className="flex items-center gap-2">
                <ErrorBox message={browseError} className="flex-1" />
                <Button variant="outline" onClick={() => void runBrowse()}>
                  Retry
                </Button>
              </div>
            )}
            {browseResults !== null && !browseBusy && (
              <div className="max-h-56 overflow-y-auto rounded-lg border border-border">
                {browseResults.length === 0 && (
                  <p className="px-3 py-3 text-center text-xs text-subtle">
                    No providers found on models.dev.
                  </p>
                )}
                {browseResults.map((r) => (
                  <button
                    key={r.id}
                    type="button"
                    onClick={() => pick(r)}
                    className="block w-full border-b border-border/60 px-3 py-2 text-left last:border-0 transition-colors hover:bg-surface"
                  >
                    <span className="flex items-baseline gap-2">
                      <span className="min-w-0 flex-1 truncate text-sm text-text">{r.name}</span>
                      <span className="shrink-0 text-[11px] text-faint">
                        {r.models.length} {r.models.length === 1 ? 'model' : 'models'}
                      </span>
                    </span>
                    {r.baseURL && (
                      <span className="mt-0.5 block truncate font-mono text-[11px] text-faint">
                        {r.baseURL}
                      </span>
                    )}
                  </button>
                ))}
              </div>
            )}
          </div>
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

      {children}

      {!hideModel && (
        <label className="block">
          <span className="mb-1 block text-xs text-subtle">Model</span>
          {active && active.models.length > 0 ? (
            <ModelCombobox models={active.models} value={model} onChange={onModelChange} />
          ) : (
            <Input
              value={model}
              onChange={(e) => onModelChange(e.target.value)}
              placeholder="gpt-4o"
            />
          )}
        </label>
      )}
    </div>
  );
}