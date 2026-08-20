import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { api } from '../api';
import type { GitHubContainerImage, GitHubWorkflowRun } from '../types';
import { errMsg } from '../utils';
import { Button, ErrorBox, Input, Spinner } from './ui';
import { IconCheck, IconGitHub, IconRefresh } from './icons';

// GitHub Actions runs + published container-image repos for any public repo
// (private repos need a token configured in Settings). The repo is prefilled
// from the linked project repo, but can point at any GitHub repository.
export default function GitHubPane({ repoUrl }: { repoUrl?: string }) {
  const suggested = useMemo(() => {
    const s = (repoUrl ?? '').trim();
    const m = s.match(/github\.com\/([^\/]+\/[^\/]+)/);
    if (!m) return '';
    return m[1].replace(/\.git$/, '');
  }, [repoUrl]);

  const [query, setQuery] = useState(suggested);
  const [owner, setOwner] = useState(suggested.split('/')[0] ?? '');
  const [workflows, setWorkflows] = useState<GitHubWorkflowRun[] | null>(null);
  const [images, setImages] = useState<GitHubContainerImage[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const autoLoaded = useRef('');

  useEffect(() => {
    if (suggested) {
      setQuery(suggested);
      setOwner(suggested.split('/')[0] ?? '');
      if (autoLoaded.current !== suggested) {
        autoLoaded.current = suggested;
        void loadRepo(suggested);
      }
    }
  }, [suggested]);

  const loadRepo = async (repo: string) => {
    if (!repo) {
      setError('Enter a repository as owner/name, e.g. octocat/Hello-World.');
      return;
    }
    setLoading(true);
    setError(null);
    const parts = repo.split('/');
    const own = parts[0] ?? '';
    setOwner(own);
    setWorkflows(null);
    setImages(null);
    try {
      const [w, im] = await Promise.allSettled([
        api.githubWorkflows(repo),
        api.githubImages(repo),
      ]);
      if (w.status === 'fulfilled') setWorkflows(w.value.workflows);
      else setWorkflows([]);
      if (im.status === 'fulfilled') setImages(im.value.images);
      else setImages([]);
      const e1 = w.status === 'rejected' ? errMsg(w.reason) : null;
      const e2 = im.status === 'rejected' ? errMsg(im.reason) : null;
      if (e1 || e2) setError([e1, e2].filter(Boolean).join(' · '));
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setLoading(false);
    }
  };

  const load = (e?: FormEvent) => {
    e?.preventDefault();
    void loadRepo(query.trim());
  };

  const badge = (s: string, c: string | null) => {
    let color = 'bg-accent/15 text-accent';
    let label = s;
    if (s === 'completed') {
      color = c === 'success' ? 'bg-emerald-500/15 text-emerald-300' : 'bg-red-500/15 text-red-300';
      label = c === 'success' ? 'success' : 'failed';
    } else if (s === 'in_progress') {
      color = 'bg-amber-500/15 text-amber-300';
      label = 'running';
    }
    return (
      <span className={`shrink-0 rounded-full px-2 py-0.5 text-[10px] font-medium ${color}`}>{label}</span>
    );
  };

  return (
    <div className="flex h-full min-h-0 flex-col overflow-y-auto v1-no-scrollbar">
      <div className="border-b border-border p-3">
        <form onSubmit={load} className="flex items-center gap-2">
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="owner/name"
            className="flex-1"
          />
          <Button type="submit" disabled={loading}>
            {loading ? <Spinner /> : <IconGitHub className="h-4 w-4" />}
            <span>Load</span>
          </Button>
        </form>
        <p className="mt-2 text-xs text-subtle">
          Actions runs and published <span className="font-mono">ghcr.io</span> images for any public
          GitHub repo. Private repos require a GitHub token in Settings.
        </p>
      </div>

      {error && <div className="p-3"><ErrorBox message={error} /></div>}

      <div className="p-3">
        <h3 className="mb-2 flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-dim">
          <IconRefresh className="h-3.5 w-3.5" /> GitHub Actions
        </h3>
        {workflows === null ? (
          <p className="text-sm text-subtle">No repo loaded yet.</p>
        ) : workflows.length === 0 ? (
          <p className="text-sm text-subtle">No workflow runs found.</p>
        ) : (
          <ul className="space-y-1.5">
            {workflows.map((w) => (
              <li
                key={w.id}
                className="rounded-md border border-border bg-surface/50 p-2 text-sm"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate font-medium text-text">
                    {w.display_title || w.name || `run #${w.id}`}
                  </span>
                  {badge(w.status, w.conclusion)}
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-dim">
                  <span className="font-mono">{w.head_branch || '—'}</span>
                  <span className="font-mono">{w.event}</span>
                  <span>{new Date(w.created_at).toLocaleString()}</span>
                  {w.html_url && (
                    <a
                      href={w.html_url}
                      target="_blank"
                      rel="noreferrer"
                      className="text-accent hover:underline"
                    >
                      view run
                    </a>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="p-3 pt-0">
        <h3 className="mb-2 flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-dim">
          <IconCheck className="h-3.5 w-3.5" /> Container images · <span className="font-mono">{owner || '—'}</span>
        </h3>
        {images === null ? (
          <p className="text-sm text-subtle">Load a repo to see its published images.</p>
        ) : images.length === 0 ? (
          <p className="text-sm text-subtle">No container images published for {owner}.</p>
        ) : (
          <ul className="space-y-1.5">
            {images.map((img) => (
              <li key={img.full} className="rounded-md border border-border bg-surface/50 p-2 text-sm">
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate font-mono text-text">{img.full}</span>
                  <span className="shrink-0 rounded-full bg-accent/15 px-2 py-0.5 text-[10px] font-medium text-accent">
                    {img.visibility}
                  </span>
                </div>
                {img.tags.length > 0 && (
                  <div className="mt-1.5 flex flex-wrap gap-1">
                    {img.tags.map((t) => (
                      <code
                        key={t}
                        className="rounded bg-border/60 px-1.5 py-0.5 font-mono text-[10px] text-dim"
                      >
                        {t}
                      </code>
                    ))}
                  </div>
                )}
                <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-dim">
                  <span>updated {img.updated_at ? new Date(img.updated_at).toLocaleString() : '—'}</span>
                  {img.url && (
                    <a href={img.url} target="_blank" rel="noreferrer" className="text-accent hover:underline">
                      open
                    </a>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
