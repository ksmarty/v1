import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { api } from '../api';
import type { GitHubRepo, Project, ProjectTemplate } from '../types';
import { errMsg, timeAgo } from '../utils';
import { Button, Dialog, ErrorBox, IconButton, Input, Spinner } from '../components/ui';
import {
  IconDots,
  IconGitHub,
  IconLogout,
  IconPlus,
  IconSettings,
  IconTrash,
} from '../components/icons';

const TEMPLATES: { id: ProjectTemplate; label: string; desc: string }[] = [
  { id: 'vite-react', label: 'Vite + React', desc: 'React app with Vite dev server' },
  { id: 'static', label: 'Static HTML', desc: 'Plain HTML/CSS/JS site' },
  { id: 'empty', label: 'Empty', desc: 'Start from scratch' },
];

function CardMenu({ onDelete }: { onDelete: () => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  return (
    <div ref={ref} className="relative">
      <IconButton
        aria-label="Project menu"
        className="h-9 w-9 md:h-8 md:w-8"
        onClick={(e) => {
          e.preventDefault();
          setOpen((o) => !o);
        }}
      >
        <IconDots className="h-4 w-4" />
      </IconButton>
      {open && (
        <div className="absolute right-0 top-full z-30 mt-1 w-36 overflow-hidden rounded-lg border border-border bg-bg py-1 shadow-xl">
          <button
            className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-red-400 hover:bg-border"
            onClick={(e) => {
              e.preventDefault();
              setOpen(false);
              onDelete();
            }}
          >
            <IconTrash className="h-4 w-4" />
            Delete
          </button>
        </div>
      )}
    </div>
  );
}

function NewProjectDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const [name, setName] = useState('');
  const [template, setTemplate] = useState<ProjectTemplate>('vite-react');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setName('');
      setTemplate('vite-react');
      setError(null);
      setBusy(false);
    }
  }, [open]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setError('Name is required.');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const p = await api.createProject(name.trim(), template);
      navigate(`/project/${p.id}`);
    } catch (err) {
      setError(errMsg(err));
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title="New project">
      <form onSubmit={submit}>
        <Input
          autoFocus
          placeholder="Project name"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <div className="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-3">
          {TEMPLATES.map((t) => (
            <button
              key={t.id}
              type="button"
              onClick={() => setTemplate(t.id)}
              className={`rounded-lg border p-3 text-left transition-colors ${
                template === t.id
                  ? 'border-accent bg-accent/10'
                  : 'border-border hover:border-faint'
              }`}
            >
              <div className="text-sm font-medium text-text">{t.label}</div>
              <div className="mt-0.5 text-xs text-subtle">{t.desc}</div>
            </button>
          ))}
        </div>
        {error && <ErrorBox message={error} className="mt-3" />}
        <div className="mt-4 flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={busy}>
            {busy ? <Spinner className="h-4 w-4" /> : 'Create project'}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function ImportDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate();
  const [url, setUrl] = useState('');
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [repos, setRepos] = useState<GitHubRepo[] | null>(null);
  const [reposError, setReposError] = useState<string | null>(null);
  const [importingRepo, setImportingRepo] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setUrl('');
    setName('');
    setError(null);
    setBusy(false);
    setRepos(null);
    setReposError(null);
    setImportingRepo(null);
    api
      .listGitHubRepos()
      .then(setRepos)
      .catch((e) => setReposError(errMsg(e)));
  }, [open]);

  const doImport = async (repoUrl: string, repoName?: string) => {
    setBusy(true);
    setError(null);
    try {
      const p = await api.importProject(repoUrl, repoName);
      navigate(`/project/${p.id}`);
    } catch (err) {
      setError(errMsg(err));
      setBusy(false);
      setImportingRepo(null);
    }
  };

  const submitManual = (e: FormEvent) => {
    e.preventDefault();
    if (!url.trim()) {
      setError('Repository URL is required.');
      return;
    }
    void doImport(url.trim(), name.trim() || undefined);
  };

  return (
    <Dialog open={open} onClose={onClose} title="Import from GitHub">
      <form onSubmit={submitManual} className="flex flex-col gap-2">
        <Input
          placeholder="https://github.com/owner/repo"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
        />
        <Input
          placeholder="Project name (optional)"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        {error && <ErrorBox message={error} />}
        <div className="flex justify-end">
          <Button type="submit" variant="outline" disabled={busy}>
            {busy && !importingRepo ? <Spinner className="h-4 w-4" /> : 'Import'}
          </Button>
        </div>
      </form>

      <div className="my-4 flex items-center gap-3 text-xs text-faint">
        <div className="h-px flex-1 bg-border" />
        or pick a repository
        <div className="h-px flex-1 bg-border" />
      </div>

      {repos === null && !reposError && (
        <div className="flex justify-center py-6">
          <Spinner className="h-5 w-5" />
        </div>
      )}
      {reposError && (
        <div className="flex flex-col items-center gap-2 py-4 text-center">
          <p className="text-sm text-dim">{reposError}</p>
          <Link to="/settings" className="text-sm text-accent hover:underline" onClick={onClose}>
            Configure GitHub token in Settings
          </Link>
        </div>
      )}
      {repos !== null && repos.length === 0 && (
        <p className="py-4 text-center text-sm text-subtle">No repositories found.</p>
      )}
      {repos !== null && repos.length > 0 && (
        <div className="max-h-72 overflow-y-auto rounded-lg border border-border">
          {repos.map((r) => (
            <button
              key={r.fullName}
              type="button"
              disabled={busy}
              onClick={() => {
                setImportingRepo(r.fullName);
                void doImport(r.url, r.name);
              }}
              className="flex w-full items-center gap-2.5 border-b border-border/60 px-3 py-2.5 text-left transition-colors last:border-0 hover:bg-surface disabled:opacity-60"
            >
              <IconGitHub className="h-4 w-4 shrink-0 text-subtle" />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm text-text">{r.fullName}</span>
                <span className="block text-xs text-faint">
                  {r.private ? 'Private' : 'Public'} · {timeAgo(r.updatedAt)}
                </span>
              </span>
              {importingRepo === r.fullName && <Spinner className="h-4 w-4 shrink-0" />}
            </button>
          ))}
        </div>
      )}
    </Dialog>
  );
}

export default function Projects() {
  const navigate = useNavigate();
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [newOpen, setNewOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [deleting, setDeleting] = useState<Project | null>(null);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  const load = useCallback(() => {
    api
      .listProjects()
      .then((list) => {
        setProjects(list);
        setError(null);
      })
      .catch((e) => setError(errMsg(e)));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const logout = async () => {
    try {
      await api.logout();
    } catch {
      // even if it fails, send the user to login
    }
    window.location.href = '/login';
  };

  const confirmDelete = async () => {
    if (!deleting) return;
    setDeleteBusy(true);
    setDeleteError(null);
    try {
      await api.deleteProject(deleting.id);
      setProjects((prev) => (prev ? prev.filter((p) => p.id !== deleting.id) : prev));
      setDeleting(null);
    } catch (e) {
      setDeleteError(errMsg(e));
    } finally {
      setDeleteBusy(false);
    }
  };

  return (
    <div className="v1-safe-top flex min-h-dvh flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border px-3 md:h-12 md:px-5">
        <Link to="/" className="text-base font-semibold tracking-tight text-text">
          v1
        </Link>
        <div className="flex-1" />
        <Button variant="outline" onClick={() => setImportOpen(true)}>
          <IconGitHub className="h-4 w-4" />
          <span className="hidden sm:inline">Import</span>
        </Button>
        <Button onClick={() => setNewOpen(true)}>
          <IconPlus className="h-4 w-4" />
          <span className="hidden sm:inline">New project</span>
        </Button>
        <IconButton aria-label="Settings" onClick={() => navigate('/settings')}>
          <IconSettings className="h-5 w-5" />
        </IconButton>
        <IconButton aria-label="Sign out" onClick={() => void logout()}>
          <IconLogout className="h-5 w-5" />
        </IconButton>
      </header>

      <main className="flex-1 p-4 md:p-6">
        {projects === null && !error && (
          <div className="flex justify-center py-16">
            <Spinner className="h-6 w-6" />
          </div>
        )}
        {error && (
          <div className="mx-auto max-w-md">
            <ErrorBox message={error} />
            <div className="mt-3 flex justify-center">
              <Button variant="outline" onClick={load}>
                Retry
              </Button>
            </div>
          </div>
        )}
        {projects !== null && projects.length === 0 && !error && (
          <div className="flex flex-col items-center gap-3 py-20 text-center">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary text-xl font-bold text-primary-text">
              v1
            </div>
            <h2 className="text-lg font-semibold text-text">No projects yet</h2>
            <p className="max-w-xs text-sm text-subtle">
              Create a new project or import an existing repository from GitHub.
            </p>
            <div className="mt-2 flex gap-2">
              <Button onClick={() => setNewOpen(true)}>
                <IconPlus className="h-4 w-4" /> New project
              </Button>
              <Button variant="outline" onClick={() => setImportOpen(true)}>
                <IconGitHub className="h-4 w-4" /> Import
              </Button>
            </div>
          </div>
        )}
        {projects !== null && projects.length > 0 && (
          <div className="mx-auto grid max-w-6xl grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {projects.map((p) => (
              <div
                key={p.id}
                className="relative rounded-xl border border-border bg-bg transition-colors hover:border-faint"
              >
                <Link to={`/project/${p.id}`} className="block p-4 pr-12">
                  <div className="flex items-center gap-2">
                    <span
                      className={`h-2 w-2 shrink-0 rounded-full ${
                        p.preview.running ? 'bg-emerald-500' : 'bg-border-strong'
                      }`}
                      title={p.preview.running ? 'Preview running' : 'Preview stopped'}
                    />
                    <span className="truncate font-medium text-text">{p.name}</span>
                  </div>
                  <div className="mt-1.5 text-xs text-subtle">
                    {p.preview.running ? 'Preview running' : 'Preview stopped'}
                    {p.updatedAt ? ` · ${timeAgo(p.updatedAt)}` : ''}
                  </div>
                </Link>
                <div className="absolute right-1.5 top-1.5">
                  <CardMenu
                    onDelete={() => {
                      setDeleteError(null);
                      setDeleting(p);
                    }}
                  />
                </div>
              </div>
            ))}
          </div>
        )}
      </main>

      <NewProjectDialog open={newOpen} onClose={() => setNewOpen(false)} />
      <ImportDialog open={importOpen} onClose={() => setImportOpen(false)} />

      <Dialog open={deleting !== null} onClose={() => setDeleting(null)} title="Delete project">
        <p className="text-sm text-dim">
          Delete <span className="font-medium text-text">{deleting?.name}</span>? This
          removes the project and all its files. This cannot be undone.
        </p>
        {deleteError && <ErrorBox message={deleteError} className="mt-3" />}
        <div className="mt-4 flex justify-end gap-2">
          <Button variant="ghost" onClick={() => setDeleting(null)}>
            Cancel
          </Button>
          <Button variant="danger" onClick={() => void confirmDelete()} disabled={deleteBusy}>
            {deleteBusy ? <Spinner className="h-4 w-4" /> : 'Delete'}
          </Button>
        </div>
      </Dialog>
    </div>
  );
}
