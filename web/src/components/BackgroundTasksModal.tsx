import { useCallback, useEffect, useState } from 'react';
import { api } from '../api';
import type { BackgroundJob } from '../types';
import { Button, Dialog, Spinner } from './ui';
import { IconTerminal } from './icons';

/**
 * Background tasks, in the two places they exist.
 *
 * Running jobs are not in the transcript — they only appear there once they
 * finish — so this fetches them from the server and offers to stop one. A
 * command started by mistake, or one that will never finish on its own, would
 * otherwise run until its timeout with no way out.
 *
 * A finished job's output is already in the transcript as the
 * "[Background #…] finished" row, so that view renders the row's own text
 * rather than fetching a second copy that could disagree with it.
 */

/** Seconds → "45s" / "3m 20s" / "1h 02m". */
function elapsed(startedAt: number, now: number): string {
  const secs = Math.max(0, now - startedAt);
  if (secs < 60) return `${secs}s`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ${String(secs % 60).padStart(2, '0')}s`;
  return `${Math.floor(mins / 60)}h ${String(mins % 60).padStart(2, '0')}m`;
}

/** Split "[Background #id: cmd] finished (exit 0):\n\noutput" into its parts. */
export function splitBackgroundResult(text: string): { header: string; body: string } {
  const idx = text.indexOf('\n\n');
  if (idx === -1) return { header: text, body: '' };
  return { header: text.slice(0, idx), body: text.slice(idx + 2) };
}

export default function BackgroundTasksModal({
  open,
  onClose,
  projectId,
  sessionId,
  output,
}: {
  open: boolean;
  onClose: () => void;
  projectId: string;
  sessionId: string;
  /** A finished job's full result text; null shows the running jobs instead. */
  output: { title: string; text: string } | null;
}) {
  const [jobs, setJobs] = useState<BackgroundJob[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [cancelling, setCancelling] = useState<string | null>(null);
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000));

  const refresh = useCallback(async () => {
    if (!sessionId) return;
    setLoading(true);
    try {
      const res = await api.listBackground(projectId, sessionId);
      setJobs(res.jobs ?? []);
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : 'could not load background tasks');
    } finally {
      setLoading(false);
    }
  }, [projectId, sessionId]);

  // Fetch when opened in the running view, and keep the elapsed times ticking.
  useEffect(() => {
    if (!open || output) return;
    void refresh();
    setNow(Math.floor(Date.now() / 1000));
    const timer = window.setInterval(() => setNow(Math.floor(Date.now() / 1000)), 1000);
    return () => window.clearInterval(timer);
  }, [open, output, refresh]);

  const cancel = async (jobId: string) => {
    setCancelling(jobId);
    try {
      await api.cancelBackground(projectId, sessionId, jobId);
      // The job still reports its own result row, marked cancelled; drop it from
      // the running list now so the button cannot be pressed twice.
      setJobs((prev) => prev.filter((j) => j.id !== jobId));
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : 'could not cancel that task');
      void refresh();
    } finally {
      setCancelling(null);
    }
  };

  if (output) {
    const { header, body } = splitBackgroundResult(output.text);
    return (
      <Dialog open={open} onClose={onClose} title={output.title} wide fixedBody>
        <div className="flex min-h-0 flex-col gap-2">
          <div className="shrink-0 font-mono text-[11px] leading-relaxed text-amber-200/80">
            {header}
          </div>
          <pre className="min-h-0 flex-1 overflow-auto overscroll-contain rounded-lg border border-border bg-surface p-3 font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-words text-text">
            {body.trim() ? body : '(no output)'}
          </pre>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog open={open} onClose={onClose} title="Background tasks" wide fixedBody>
      <div className="flex min-h-0 flex-col gap-3">
        {error && <div className="shrink-0 text-xs text-red-400">{error}</div>}
        {loading && jobs.length === 0 ? (
          <div className="flex items-center gap-2 py-6 text-xs text-subtle">
            <Spinner className="h-3.5 w-3.5" /> Checking…
          </div>
        ) : jobs.length === 0 ? (
          <div className="py-6 text-xs text-subtle">
            No background tasks are running. Finished ones appear in the chat with their
            output.
          </div>
        ) : (
          <ul className="flex min-h-0 flex-col gap-2 overflow-y-auto overscroll-contain">
            {jobs.map((job) => (
              <li
                key={job.id}
                className="flex items-start gap-3 rounded-lg border border-amber-300/20 bg-amber-300/5 px-3 py-2"
              >
                <IconTerminal className="mt-0.5 h-3.5 w-3.5 shrink-0 animate-pulse text-amber-300/90" />
                <div className="min-w-0 flex-1">
                  <div className="break-words font-mono text-[12px] leading-relaxed text-amber-100/90">
                    {job.command}
                  </div>
                  <div className="mt-0.5 text-[10px] uppercase tracking-wide text-amber-300/70">
                    #{job.id} · running {elapsed(job.startedAt, now)}
                  </div>
                </div>
                <Button
                  variant="outline"
                  onClick={() => void cancel(job.id)}
                  disabled={cancelling === job.id}
                  className="shrink-0"
                >
                  {cancelling === job.id ? 'Stopping…' : 'Cancel'}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Dialog>
  );
}
