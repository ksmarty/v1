import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { api } from '../api';
import type { PreviewStatus } from '../types';
import { errMsg } from '../utils';
import { Button, IconButton, Spinner } from './ui';
import {
  IconChevronDown,
  IconChevronRight,
  IconExternalLink,
  IconMonitor,
  IconPlay,
  IconRefresh,
  IconSquare,
} from './icons';

const BREAKPOINTS: { label: string; width: string }[] = [
  { label: 'Full', width: '' },
  { label: 'Desktop', width: '1280px' },
  { label: 'Laptop', width: '1024px' },
  { label: 'Tablet', width: '768px' },
  { label: 'Mobile', width: '390px' },
];

export default function PreviewPane({
  projectId,
  refreshKey,
}: {
  projectId: string;
  refreshKey: number;
}) {
  const [status, setStatus] = useState<PreviewStatus | null>(null);
  const [initialLoading, setInitialLoading] = useState(true);
  const [starting, setStarting] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [logsOpen, setLogsOpen] = useState(false);
  const [iframeKey, setIframeKey] = useState(0);
  const [bp, setBp] = useState('');
  const lastRevRef = useRef(0);
  const frameRef = useRef<HTMLIFrameElement>(null);

  const previewUrl = `/preview/${projectId}/`;
  const [barUrl, setBarUrl] = useState(previewUrl);
  const [iframeUrl, setIframeUrl] = useState(previewUrl);

  const fetchStatus = useCallback(async () => {
    try {
      const s = await api.getPreviewStatus(projectId);
      setStatus(s);
      if (s.running) setStarting(false);
      lastRevRef.current = s.revision;
      setError(null);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setInitialLoading(false);
    }
  }, [projectId]);

  useEffect(() => {
    void fetchStatus();
  }, [fetchStatus]);

  // Poll only while the preview is starting up; afterwards refresh on demand.
  useEffect(() => {
    if (!starting) return;
    const began = Date.now();
    const t = setInterval(() => {
      if (Date.now() - began > 120_000) {
        setStarting(false);
        setError('Preview did not start within 2 minutes. Check the logs for details.');
        return;
      }
      void fetchStatus();
    }, 2000);
    return () => clearInterval(t);
  }, [starting, fetchStatus]);

  // While running, poll status and auto-reload the iframe whenever the
  // project's file revision changes (the agent wrote/edited files).
  useEffect(() => {
    if (!status?.running) return;
    const t = setInterval(async () => {
      let s: PreviewStatus | null = null;
      try {
        s = await api.getPreviewStatus(projectId);
      } catch {
        return; // transient — try again next tick
      }
      setStatus(s);
      if (!s.running) return;
      if (s.revision !== lastRevRef.current) {
        lastRevRef.current = s.revision;
        setIframeKey((k) => k + 1);
      }
    }, 2000);
    return () => clearInterval(t);
  }, [projectId, status?.running]);

  // External refresh request (e.g. the restart_preview tool finished ok).
  const firstRefresh = useRef(true);
  useEffect(() => {
    if (firstRefresh.current) {
      firstRefresh.current = false;
      return;
    }
    void fetchStatus().then(() => setIframeKey((k) => k + 1));
  }, [refreshKey, fetchStatus]);

  const start = async () => {
    setStarting(true);
    setError(null);
    try {
      await api.startPreview(projectId);
      await fetchStatus();
    } catch (e) {
      setError(errMsg(e));
      setStarting(false);
    }
  };

  const stop = async () => {
    setStopping(true);
    setError(null);
    try {
      await api.stopPreview(projectId);
      await fetchStatus();
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setStopping(false);
    }
  };

  const refresh = () => {
    setIframeKey((k) => k + 1);
    void fetchStatus();
  };

  // Navigate the iframe from the URL bar. Relative paths resolve against the
  // current frame URL (kept inside the preview proxy), absolute URLs go out.
  const go = (e?: FormEvent) => {
    e?.preventDefault();
    const raw = barUrl.trim();
    if (!raw) return;
    let next = raw;
    try {
      next = new URL(raw, window.location.origin + iframeUrl).href;
    } catch {
      // keep as typed
    }
    setIframeUrl(next);
    setBarUrl(next);
  };

  // Keep the URL bar in sync with in-frame navigation (same-origin through
  // the proxy, so the location is readable; external pages are skipped).
  const onFrameLoad = () => {
    try {
      const href = frameRef.current?.contentWindow?.location.href;
      if (href && href.startsWith(window.location.origin)) setBarUrl(href);
    } catch {
      // cross-origin — ignore
    }
  };

  const running = status?.running ?? false;

  const frame = (className: string) => (
    <iframe
      key={iframeKey}
      ref={frameRef}
      src={iframeUrl}
      onLoad={onFrameLoad}
      title="App preview"
      className={className}
    />
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex h-11 shrink-0 items-center gap-1 border-b border-border px-2">
        {running ? (
          <Button
            variant="outline"
            onClick={() => void stop()}
            disabled={stopping}
            className="min-h-[32px] px-2.5 py-1 text-xs"
          >
            {stopping ? <Spinner className="h-3.5 w-3.5" /> : <IconSquare className="h-3.5 w-3.5" />}
            Stop
          </Button>
        ) : (
          <Button
            variant="outline"
            onClick={() => void start()}
            disabled={starting}
            className="min-h-[32px] px-2.5 py-1 text-xs"
          >
            {starting ? <Spinner className="h-3.5 w-3.5" /> : <IconPlay className="h-3.5 w-3.5" />}
            {starting ? 'Starting…' : 'Start'}
          </Button>
        )}
        <span className="ml-1 flex items-center gap-1.5 text-xs text-subtle">
          <span
            className={`h-1.5 w-1.5 rounded-full ${
              running ? 'bg-emerald-500' : starting ? 'animate-pulse bg-amber-500' : 'bg-border-strong'
            }`}
          />
          {running ? 'Running' : starting ? 'Starting' : 'Stopped'}
        </span>
        <div className="flex-1" />
        <select
          value={bp}
          onChange={(e) => setBp(e.target.value)}
          aria-label="Preview width"
          title="Preview width"
          className="shrink-0 rounded-md border border-border bg-surface px-2 py-1 text-xs text-subtle outline-none transition-colors focus:border-subtle"
        >
          {BREAKPOINTS.map((b) => (
            <option key={b.label} value={b.width}>
              {b.label}
            </option>
          ))}
        </select>
        <button
          type="button"
          onClick={() => setLogsOpen((o) => !o)}
          className="flex min-h-[36px] items-center gap-1 rounded-lg px-2 text-xs text-subtle transition-colors hover:bg-border hover:text-text"
        >
          {logsOpen ? (
            <IconChevronDown className="h-3.5 w-3.5" />
          ) : (
            <IconChevronRight className="h-3.5 w-3.5" />
          )}
          Logs
        </button>
        <IconButton
          aria-label="Refresh preview"
          onClick={refresh}
          disabled={!running}
          className="h-9 w-9 md:h-8 md:w-8"
        >
          <IconRefresh className="h-4 w-4" />
        </IconButton>
        <IconButton
          aria-label="Open preview in new tab"
          onClick={() => window.open(iframeUrl, '_blank', 'noopener')}
          disabled={!running}
          className="h-9 w-9 md:h-8 md:w-8"
        >
          <IconExternalLink className="h-4 w-4" />
        </IconButton>
      </div>

      <form
        onSubmit={go}
        className="flex h-10 shrink-0 items-center gap-2 border-b border-border px-2"
      >
        <IconMonitor className="h-3.5 w-3.5 shrink-0 text-faint" />
        <input
          value={barUrl}
          onChange={(e) => setBarUrl(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') go();
          }}
          spellCheck={false}
          aria-label="Preview URL"
          className="h-7 min-w-0 flex-1 rounded-md border border-border bg-surface px-2 font-mono text-xs text-text outline-none transition-colors focus:border-subtle"
        />
        <button
          type="submit"
          className="shrink-0 rounded-md px-2 py-1 text-xs text-subtle transition-colors hover:bg-border hover:text-text"
        >
          Go
        </button>
      </form>

      {logsOpen && (
        <div className="max-h-44 shrink-0 overflow-auto border-b border-border bg-bg p-3">
          <pre className="whitespace-pre-wrap break-words font-mono text-xs leading-relaxed text-dim">
            {status?.logs?.trim() ? status.logs : 'No logs yet.'}
          </pre>
        </div>
      )}

      <div className="min-h-0 flex-1">
        {initialLoading ? (
          <div className="flex h-full items-center justify-center">
            <Spinner className="h-5 w-5" />
          </div>
        ) : running ? (
          bp ? (
            <div className="flex h-full min-h-0 justify-center overflow-auto bg-border/40">
              <div className="h-full shrink-0 border-x border-border bg-bg" style={{ width: bp }}>
                {frame('h-full w-full border-0')}
              </div>
            </div>
          ) : (
            frame('h-full w-full border-0 bg-bg')
          )
        ) : (
          <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
            <IconMonitor className="h-10 w-10 text-border-strong" />
            <p className="text-sm text-subtle">
              {starting ? 'Starting the dev server…' : 'Preview is not running'}
            </p>
            {starting ? (
              <Spinner className="h-5 w-5" />
            ) : (
              <Button onClick={() => void start()}>
                <IconPlay className="h-4 w-4" /> Start preview
              </Button>
            )}
            {error && <p className="max-w-sm text-xs text-red-400">{error}</p>}
          </div>
        )}
      </div>
    </div>
  );
}
