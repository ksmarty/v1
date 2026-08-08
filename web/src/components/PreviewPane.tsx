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
} from './icons';

// Device presets render the iframe at exact device dimensions so media
// queries and layout match the real thing; the stage scales the frame down
// when the pane is smaller than the device.
const DEVICES: { label: string; w: number; h: number }[] = [
  { label: 'Full', w: 0, h: 0 },
  { label: 'Desktop', w: 1280, h: 800 },
  { label: 'Laptop', w: 1024, h: 640 },
  { label: 'Tablet', w: 768, h: 1024 },
  { label: 'Mobile', w: 390, h: 844 },
];

// Renders a log line with the filter query highlighted.
function LogLine({ line, query }: { line: string; query: string }) {
  if (!query) return <>{line + '\n'}</>;
  const parts = line.split(new RegExp(`(${query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'ig'));
  return (
    <>
      {parts.map((p, i) =>
        i % 2 === 1 ? (
          <mark key={i} className="rounded-sm bg-accent/30 text-text">
            {p}
          </mark>
        ) : (
          p
        ),
      )}
      {'\n'}
    </>
  );
}

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
  const [logQuery, setLogQuery] = useState('');
  const [iframeKey, setIframeKey] = useState(0);
  const [deviceIdx, setDeviceIdx] = useState(0);
  const [scale, setScale] = useState(1);
  const lastRevRef = useRef(0);
  const frameRef = useRef<HTMLIFrameElement>(null);
  const stageRef = useRef<HTMLDivElement>(null);

  const device = DEVICES[deviceIdx] ?? DEVICES[0];

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

  const filteredLogs = (status?.logs ?? '')
    .split('\n')
    .filter((l) => l.trim() !== '')
    .filter((l) => !logQuery.trim() || l.toLowerCase().includes(logQuery.trim().toLowerCase()));

  // Fit the device frame into the stage: scale down (never up) when the pane
  // is smaller than the device, so the preview keeps true device pixels.
  useEffect(() => {
    const el = stageRef.current;
    if (!el || device.w === 0) {
      setScale(1);
      return;
    }
    const fit = () => {
      // 32 = stage padding (p-4), rest = the dimension label row below.
      setScale(
        Math.min(1, (el.clientWidth - 32) / device.w, (el.clientHeight - 56) / device.h),
      );
    };
    fit();
    const ro = new ResizeObserver(fit);
    ro.observe(el);
    return () => ro.disconnect();
  }, [device, running]);

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
        <Button
          variant="outline"
          onClick={() => void (running ? stop() : start())}
          disabled={starting || stopping}
          title={
            running
              ? 'Preview is running — click to stop'
              : starting
                ? 'Preview is starting…'
                : 'Preview is stopped — click to start'
          }
          className="min-h-[32px] gap-1.5 px-2.5 py-1 text-xs"
        >
          {starting || stopping ? (
            <Spinner className="h-3.5 w-3.5" />
          ) : (
            <span
              className={`h-1.5 w-1.5 shrink-0 rounded-full ${
                running ? 'bg-emerald-500' : 'bg-border-strong'
              }`}
            />
          )}
          {running ? 'Stop' : starting ? 'Starting…' : 'Start'}
        </Button>
        <div className="flex-1" />
        <select
          value={deviceIdx}
          onChange={(e) => setDeviceIdx(Number(e.target.value))}
          aria-label="Preview device size"
          title="Preview device size"
          className="shrink-0 rounded-md border border-border bg-surface px-1.5 py-1 text-xs text-subtle outline-none transition-colors focus:border-subtle md:px-2"
        >
          {DEVICES.map((d, i) => (
            <option key={d.label} value={i}>
              {d.label}
            </option>
          ))}
        </select>
        <button
          type="button"
          onClick={() => setLogsOpen((o) => !o)}
          aria-label="Toggle preview logs"
          title="Preview logs"
          className="flex min-h-[36px] items-center gap-1 rounded-lg px-2 text-xs text-subtle transition-colors hover:bg-border hover:text-text"
        >
          {logsOpen ? (
            <IconChevronDown className="h-3.5 w-3.5" />
          ) : (
            <IconChevronRight className="h-3.5 w-3.5" />
          )}
          <span className="hidden md:inline">Logs</span>
        </button>
        <IconButton
          aria-label="Refresh preview"
          onClick={refresh}
          disabled={!running}
          className="h-9 w-9 md:h-8 md:w-8"
        >
          <IconRefresh className="h-4 w-4" />
        </IconButton>
        {/* An anchor, not window.open: iOS PWAs ignore window.open but follow
            target=_blank links into Safari. */}
        <a
          href={running ? iframeUrl : undefined}
          target="_blank"
          rel="noopener noreferrer"
          aria-label="Open preview in new tab"
          aria-disabled={!running}
          className={`inline-flex h-9 w-9 items-center justify-center rounded-lg text-dim transition-colors md:h-8 md:w-8 ${
            running
              ? 'hover:bg-border hover:text-text'
              : 'pointer-events-none opacity-40'
          }`}
        >
          <IconExternalLink className="h-4 w-4" />
        </a>
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
          autoCorrect="off"
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
        <div className="shrink-0 border-b border-border bg-bg">
          <div className="flex items-center gap-2 px-3 pt-2">
            <input
              value={logQuery}
              onChange={(e) => setLogQuery(e.target.value)}
              placeholder="Filter logs…"
              spellCheck={false}
              autoCorrect="off"
              aria-label="Filter logs"
              className="h-7 min-w-0 flex-1 rounded-md border border-border bg-surface px-2 font-mono text-xs text-text outline-none transition-colors placeholder:text-faint focus:border-subtle"
            />
            {logQuery.trim() && (
              <span className="shrink-0 text-[10px] text-faint">
                {filteredLogs.length} line{filteredLogs.length === 1 ? '' : 's'}
              </span>
            )}
          </div>
          <div className="fade-y mt-2 max-h-44 overflow-auto px-3 pb-3">
            <pre className="whitespace-pre-wrap break-words font-mono text-xs leading-relaxed text-dim">
              {filteredLogs.length > 0
                ? filteredLogs.map((line, i) => <LogLine key={i} line={line} query={logQuery.trim()} />)
                : status?.logs?.trim()
                  ? 'No lines match.'
                  : 'No logs yet.'}
            </pre>
          </div>
        </div>
      )}

      {/* relative + overflow-hidden: iOS expands in-flow iframes to their
          content height, which would stretch the whole page — an absolutely
          positioned frame inside a clipped stage keeps its bounds. */}
      <div className="relative min-h-0 flex-1 overflow-hidden">
        {initialLoading ? (
          <div className="flex h-full items-center justify-center">
            <Spinner className="h-5 w-5" />
          </div>
        ) : running ? (
          device.w > 0 ? (
            <div
              ref={stageRef}
              className="flex h-full min-h-0 flex-col items-center justify-center gap-2 overflow-hidden bg-border/40 p-4"
            >
              <div
                className="shrink-0"
                style={{ width: device.w * scale, height: device.h * scale }}
              >
                <div
                  className="origin-top-left overflow-hidden rounded-xl border border-border-strong bg-bg shadow-2xl"
                  style={{ width: device.w, height: device.h, transform: `scale(${scale})` }}
                >
                  {frame('h-full w-full border-0')}
                </div>
              </div>
              <span className="shrink-0 text-[11px] text-faint">
                {device.label} · {device.w}×{device.h}
                {scale < 1 && ` · scaled to ${Math.round(scale * 100)}%`}
              </span>
            </div>
          ) : (
            frame('absolute inset-0 h-full w-full border-0 bg-bg')
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
