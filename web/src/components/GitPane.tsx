import { useCallback, useEffect, useState } from 'react';
import { api } from '../api';
import type { GitCommit, GitInfo } from '../types';
import { errMsg } from '../utils';
import { Button, Dialog, ErrorBox, Input, Spinner } from './ui';
import { IconGitBranch, IconRewind, IconPlus, IconCheck } from './icons';

function timeAgo(unix: number): string {
  const secs = Math.floor(Date.now() / 1000 - unix);
  if (secs < 60) return 'just now';
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

export default function GitPane({
  projectId,
  onPreviewRestart,
}: {
  projectId: string;
  onPreviewRestart: () => void;
}) {
  const [info, setInfo] = useState<GitInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [newBranch, setNewBranch] = useState('');
  const [confirmRevert, setConfirmRevert] = useState<GitCommit | null>(null);
  const [reverting, setReverting] = useState(false);

  const load = useCallback(async () => {
    try {
      const g = await api.gitInfo(projectId);
      setInfo(g);
      setError(null);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setLoading(false);
    }
  }, [projectId]);

  useEffect(() => {
    void load();
  }, [load]);

  const act = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      await load();
      onPreviewRestart();
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const initRepo = () => act(() => api.gitInit(projectId));

  const createBranch = () => {
    const name = newBranch.trim();
    if (!name) return;
    setNewBranch('');
    void act(() => api.gitBranch(projectId, name));
  };

  const checkoutBranch = (branch: string) => {
    if (!branch || branch === info?.branch) return;
    void act(() => api.gitCheckout(projectId, branch));
  };

  const revert = () => {
    if (!confirmRevert) return;
    setReverting(true);
    setError(null);
    api
      .gitRevert(projectId, confirmRevert.hash)
      .then(async () => {
        setConfirmRevert(null);
        await load();
        onPreviewRestart();
      })
      .catch((e) => setError(errMsg(e)))
      .finally(() => setReverting(false));
  };

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner className="h-5 w-5" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="space-y-3 p-4">
        <ErrorBox message={error} />
        <Button variant="outline" onClick={() => void load()}>
          Retry
        </Button>
      </div>
    );
  }

  if (!info?.isRepo) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
        <IconGitBranch className="h-10 w-10 text-border-strong" />
        <p className="text-sm text-subtle">Not a git repository yet</p>
        <p className="max-w-xs text-xs text-faint">
          Initialize git to snapshot the project after every chat turn, so you can
          branch and rewind the repo to any point in time.
        </p>
        <Button onClick={() => void initRepo()} disabled={busy}>
          {busy ? <Spinner className="h-4 w-4" /> : <IconPlus className="h-4 w-4" />}
          Initialize git repo
        </Button>
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-3 p-3">
      <div className="rounded-xl border border-border bg-surface/50 p-3">
        <div className="flex items-center gap-2">
          <IconGitBranch className="h-4 w-4 shrink-0 text-dim" />
          <select
            value={info.branch}
            onChange={(e) => checkoutBranch(e.target.value)}
            aria-label="Branch"
            className="min-w-0 flex-1 rounded-md border border-border bg-surface px-2 py-1.5 text-sm text-text outline-none transition-colors focus:border-subtle"
          >
            {(info.branches ?? []).map((b) => (
              <option key={b} value={b}>
                {b}
              </option>
            ))}
          </select>
          <span className="shrink-0 rounded-full bg-border px-2 py-0.5 text-[10px] text-subtle">
            {info.commits?.length ?? 0} commits
          </span>
        </div>
        <div className="mt-2 flex items-center gap-2">
          <Input
            value={newBranch}
            onChange={(e) => setNewBranch(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') createBranch();
            }}
            placeholder="New branch name"
            className="min-w-0 flex-1"
          />
          <Button
            variant="outline"
            onClick={createBranch}
            disabled={busy || !newBranch.trim()}
            className="shrink-0 px-2.5 text-xs"
          >
            Create
          </Button>
        </div>
        <p className="mt-2 text-[11px] leading-relaxed text-faint">
          Every finished chat turn is committed automatically, so each step of a
          conversation is a checkpoint you can return to.
        </p>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="flex flex-col gap-1.5">
          {(info.commits ?? []).map((c) => (
            <div
              key={c.hash}
              className="flex items-center gap-2.5 rounded-lg border border-border/80 bg-surface/50 px-2.5 py-2"
            >
              <IconCheck className="h-3.5 w-3.5 shrink-0 text-faint" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm text-text">{c.message || '(empty message)'}</div>
                <div className="mt-0.5 flex items-center gap-2 font-mono text-[10px] text-faint">
                  <span>{c.short}</span>
                  <span className="text-dim">{c.author}</span>
                  <span>{timeAgo(c.time)}</span>
                </div>
              </div>
              <button
                type="button"
                onClick={() => setConfirmRevert(c)}
                aria-label="Revert to this commit"
                title="Revert the repo to this commit (drops later changes)"
                className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-text"
              >
                <IconRewind className="h-3.5 w-3.5" />
              </button>
            </div>
          ))}
          {info.commits?.length === 0 && (
            <p className="py-6 text-center text-xs text-faint">No commits yet.</p>
          )}
        </div>
      </div>

      <Dialog
        open={confirmRevert !== null}
        onClose={() => setConfirmRevert(null)}
        title="Revert repo to this commit?"
      >
        <p className="text-sm text-subtle">
          Hard-resets the working tree to{' '}
          <span className="font-mono text-text">{confirmRevert?.short}</span> —{' '}
          {confirmRevert?.message ?? ''}. Every change made after this commit will
          be discarded.
        </p>
        <div className="mt-4 flex justify-end gap-2">
          <Button variant="ghost" onClick={() => setConfirmRevert(null)} disabled={reverting}>
            Cancel
          </Button>
          <Button variant="danger" onClick={() => void revert()} disabled={reverting}>
            {reverting && <Spinner className="h-4 w-4" />}
            Revert
          </Button>
        </div>
      </Dialog>
    </div>
  );
}
