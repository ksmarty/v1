import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { api } from '../api';
import type { GitStatus, PushResult } from '../types';
import { errMsg } from '../utils';
import { Button, Dialog, ErrorBox, IconButton, Input, Spinner, Textarea } from './ui';
import { IconExternalLink, IconGitBranch, IconGitHub } from './icons';

function sanitizeRepoName(name: string): string {
  const cleaned = name
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return cleaned || 'v1-project';
}

function PushDialog({
  open,
  onClose,
  projectId,
  onDone,
}: {
  open: boolean;
  onClose: () => void;
  projectId: string;
  onDone: () => void;
}) {
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<PushResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setMessage('');
      setResult(null);
      setError(null);
      setBusy(false);
    }
  }, [open]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!message.trim()) {
      setError('Commit message is required.');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const r = await api.githubPush(projectId, message.trim());
      setResult(r);
      onDone();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title="Push changes">
      {result ? (
        <div>
          <div className="mb-2 flex gap-2">
            <span
              className={`rounded-full px-2 py-0.5 text-xs ${
                result.committed
                  ? 'bg-emerald-950 text-emerald-400'
                  : 'bg-border text-dim'
              }`}
            >
              {result.committed ? 'Committed' : 'Nothing to commit'}
            </span>
            <span
              className={`rounded-full px-2 py-0.5 text-xs ${
                result.pushed
                  ? 'bg-emerald-950 text-emerald-400'
                  : 'bg-border text-dim'
              }`}
            >
              {result.pushed ? 'Pushed' : 'Not pushed'}
            </span>
          </div>
          {result.summary && (
            <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-surface p-3 font-mono text-xs text-dim">
              {result.summary}
            </pre>
          )}
          <div className="mt-4 flex justify-end">
            <Button onClick={onClose}>Done</Button>
          </div>
        </div>
      ) : (
        <form onSubmit={(e) => void submit(e)}>
          <Textarea
            autoFocus
            rows={3}
            placeholder="Commit message"
            value={message}
            onChange={(e) => setMessage(e.target.value)}
          />
          {error && <ErrorBox message={error} className="mt-3" />}
          <div className="mt-4 flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy}>
              {busy ? <Spinner className="h-4 w-4" /> : 'Commit & push'}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  );
}

function CreateRepoDialog({
  open,
  onClose,
  projectId,
  projectName,
  onDone,
}: {
  open: boolean;
  onClose: () => void;
  projectId: string;
  projectName: string;
  onDone: () => void;
}) {
  const [name, setName] = useState('');
  const [isPrivate, setIsPrivate] = useState(true);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setName(sanitizeRepoName(projectName));
      setIsPrivate(true);
      setBusy(false);
      setDone(false);
      setError(null);
    }
  }, [open, projectName]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setError('Repository name is required.');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.githubCreate(projectId, name.trim(), isPrivate);
      setDone(true);
      onDone();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title="Create GitHub repo & push">
      {done ? (
        <div>
          <p className="text-sm text-text">Repository created and code pushed.</p>
          <div className="mt-4 flex justify-end">
            <Button onClick={onClose}>Done</Button>
          </div>
        </div>
      ) : (
        <form onSubmit={(e) => void submit(e)}>
          <label className="mb-1 block text-xs text-subtle">Repository name</label>
          <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
          <label className="mt-3 flex cursor-pointer items-center gap-2 text-sm text-text">
            <input
              type="checkbox"
              checked={isPrivate}
              onChange={(e) => setIsPrivate(e.target.checked)}
              className="h-4 w-4 accent-accent"
            />
            Private repository
          </label>
          {error && <ErrorBox message={error} className="mt-3" />}
          <div className="mt-4 flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy}>
              {busy ? <Spinner className="h-4 w-4" /> : 'Create & push'}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  );
}

export default function GitHubMenu({
  projectId,
  projectName,
  repoUrl,
}: {
  projectId: string;
  projectName: string;
  repoUrl: string;
}) {
  const [open, setOpen] = useState(false);
  const [status, setStatus] = useState<GitStatus | null>(null);
  const [statusError, setStatusError] = useState<string | null>(null);
  const [dialog, setDialog] = useState<'push' | 'create' | null>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  const loadStatus = useCallback(() => {
    api
      .gitStatus(projectId)
      .then((s) => {
        setStatus(s);
        setStatusError(null);
      })
      .catch((e) => setStatusError(errMsg(e)));
  }, [projectId]);

  useEffect(() => {
    if (!open) return;
    loadStatus();
    const onDoc = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open, loadStatus]);

  const effectiveRepoUrl = status?.repoUrl || repoUrl;

  return (
    <div className="relative" ref={menuRef}>
      <IconButton aria-label="GitHub" title="GitHub" onClick={() => setOpen((o) => !o)}>
        <IconGitHub className="h-5 w-5" />
      </IconButton>

      {open && (
        <div className="absolute right-0 top-full z-40 mt-1 w-72 rounded-xl border border-border bg-bg p-3 shadow-2xl">
          {status === null && !statusError && (
            <div className="flex justify-center py-4">
              <Spinner className="h-4 w-4" />
            </div>
          )}
          {statusError && <ErrorBox message={statusError} />}
          {status !== null && (
            <div className="mb-3 flex flex-col gap-1.5 text-xs text-dim">
              {status.isRepo ? (
                <div className="flex items-center gap-1.5">
                  <IconGitBranch className="h-3.5 w-3.5 shrink-0 text-subtle" />
                  <span className="font-mono text-text">{status.branch ?? 'HEAD'}</span>
                  <span className="text-faint">·</span>
                  <span>{status.modified} modified</span>
                  <span className="text-faint">·</span>
                  <span>{status.untracked} untracked</span>
                </div>
              ) : (
                <p className="text-subtle">Not a git repository yet.</p>
              )}
              {effectiveRepoUrl && (
                <a
                  href={effectiveRepoUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="flex items-center gap-1 truncate text-accent hover:underline"
                >
                  <span className="truncate">
                    {effectiveRepoUrl.replace(/^https?:\/\/github\.com\//, '')}
                  </span>
                  <IconExternalLink className="h-3 w-3 shrink-0" />
                </a>
              )}
            </div>
          )}
          <div className="flex flex-col gap-1.5">
            <Button
              variant="outline"
              className="w-full justify-start"
              onClick={() => {
                setOpen(false);
                setDialog('push');
              }}
            >
              Push changes
            </Button>
            {!effectiveRepoUrl && (
              <Button
                variant="outline"
                className="w-full justify-start"
                onClick={() => {
                  setOpen(false);
                  setDialog('create');
                }}
              >
                Create repo & push
              </Button>
            )}
          </div>
        </div>
      )}

      <PushDialog
        open={dialog === 'push'}
        onClose={() => setDialog(null)}
        projectId={projectId}
        onDone={loadStatus}
      />
      <CreateRepoDialog
        open={dialog === 'create'}
        onClose={() => setDialog(null)}
        projectId={projectId}
        projectName={projectName}
        onDone={loadStatus}
      />
    </div>
  );
}
