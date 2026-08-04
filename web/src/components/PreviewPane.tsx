import { useCallback, useEffect, useRef, useState } from 'react';
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

  const fetchStatus = useCallback(async () => {
    try {
      const s = await api.getPreviewStatus(projectId);
      setStatus(s);
      if (s.running) setStarting(false);
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

  const running = status?.running ?? false;
  const previewUrl = `/preview/${projectId}/`;

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
          onClick={() => window.open(previewUrl, '_blank', 'noopener')}
          disabled={!running}
          className="h-9 w-9 md:h-8 md:w-8"
        >
          <IconExternalLink className="h-4 w-4" />
        </IconButton>
      </div>

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
          <iframe
            key={iframeKey}
            src={previewUrl}
            title="App preview"
            className="h-full w-full border-0 bg-bg"
          />
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
