import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { api } from '../api';
import type { Provider } from '../types';
import { errMsg } from '../utils';
import { Button, ErrorBox, Input, Spinner } from './ui';
import { IconExternalLink, IconRefresh } from './icons';
import { ModelCombobox } from './ModelCombobox';

export default function ProviderSelector({
  baseURL,
  model,
  onBaseURLChange,
  onModelChange,
  hideModel = false,
  onPickProvider,
  children,
}: {
  baseURL: string;
  model: string;
  onBaseURLChange: (v: string) => void;
  onModelChange: (v: string) => void;
  hideModel?: boolean;
  /** Called with the provider picked from the models.dev search, so the form
   * can auto-fill the provider name. */
  onPickProvider?: (p: Provider) => void;
  children?: ReactNode;
}) {
  const [refreshing, setRefreshing] = useState(false);
  const [refreshMsg, setRefreshMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [browseQ, setBrowseQ] = useState('');
  const [browseResults, setBrowseResults] = useState<Provider[] | null>(null);
  const [browseBusy, setBrowseBusy] = useState(false);
  const [browseError, setBrowseError] = useState<string | null>(null);
  const [browsed, setBrowsed] = useState<Provider | null>(null);
  const browseSeq = useRef(0);

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

  // Debounced search (250 ms) — blank lists all providers.
  useEffect(() => {
    const t = setTimeout(() => void runBrowse(), 250);
    return () => clearTimeout(t);
  }, [runBrowse]);

  const pick = (r: Provider) => {
    setBrowsed(r);
    onBaseURLChange(r.baseURL);
    onModelChange('');
    onPickProvider?.(r);
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
        runBrowse();
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
        <Input
          value={browseQ}
          onChange={(e) => setBrowseQ(e.target.value)}
          placeholder="Search providers…"
          autoComplete="off"
        />
        <div className="mt-2">
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
                  className={`block w-full border-b border-border/60 px-3 py-2 text-left last:border-0 transition-colors hover:bg-surface ${
                    browsed?.id === r.id ? 'bg-surface' : ''
                  }`}
                >
                  <span className="flex items-baseline gap-2">
                    <span className="min-w-0 flex-1 truncate text-sm text-text">{r.name}</span>
                    <span className="shrink-0 text-[11px] text-faint">
                      {r.models.length} {r.models.length === 1 ? 'model' : 'models'}
                    </span>
                  </span>
                  <span className="mt-0.5 block truncate font-mono text-[11px] text-faint">
                    {r.baseURL}
                  </span>
                </button>
              ))}
            </div>
          )}
        </div>
        {browsed && (browsed.keyHint || browsed.doc) && (
          <p className="mt-1.5 text-xs text-faint">
            {browsed.keyHint}
            {browsed.keyHint && browsed.doc ? ' · ' : ''}
            {browsed.doc && (
              <a
                href={browsed.doc}
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

      {children}

      {!hideModel && (
        <label className="block">
          <span className="mb-1 block text-xs text-subtle">Model</span>
          {browsed && browsed.models.length > 0 ? (
            <ModelCombobox models={browsed.models} value={model} onChange={onModelChange} />
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
