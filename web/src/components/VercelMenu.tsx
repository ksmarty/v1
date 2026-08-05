import { useCallback, useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { SiVercel } from 'react-icons/si';
import { api } from '../api';
import type { VercelDeployment, VercelDeploymentsResponse } from '../types';
import { errMsg } from '../utils';
import { Button, ErrorBox, IconButton, Spinner } from './ui';
import { IconExternalLink } from './icons';

const TERMINAL_STATES = new Set([
  'READY',
  'ERROR',
  'ERROR_BUILDING',
  'ERROR_INSTALLING',
  'ERROR_DEPLOYING',
  'CANCELED',
]);

function isTerminal(state?: string): boolean {
  return !!state && TERMINAL_STATES.has(state);
}

function relTime(ms: number): string {
  const s = Math.floor((Date.now() - ms) / 1000);
  if (s < 60) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function StateBadge({ state }: { state: string }) {
  if (state === 'READY') {
    return (
      <span className="shrink-0 rounded-full bg-emerald-950 px-1.5 py-0.5 text-[10px] text-emerald-400">
        ready
      </span>
    );
  }
  if (state === 'BUILDING' || state === 'QUEUED' || state === 'INITIALIZING') {
    return (
      <span className="flex shrink-0 items-center gap-1 rounded-full bg-border px-1.5 py-0.5 text-[10px] text-dim">
        <Spinner className="h-2.5 w-2.5" /> building
      </span>
    );
  }
  if (state.startsWith('ERROR') || state === 'CANCELED') {
    return (
      <span className="shrink-0 rounded-full bg-red-950 px-1.5 py-0.5 text-[10px] text-red-400">
        failed
      </span>
    );
  }
  return (
    <span className="shrink-0 rounded-full bg-border px-1.5 py-0.5 text-[10px] text-dim">
      {state.toLowerCase()}
    </span>
  );
}

function DeploymentUrl({ url }: { url: string }) {
  const href = /^https?:\/\//i.test(url) ? url : `https://${url}`;
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className="min-w-0 flex-1 truncate text-accent hover:underline"
    >
      {url.replace(/^https?:\/\//, '')}
    </a>
  );
}

/**
 * Vercel deploy menu in the project header: deploy preview/production, watch
 * the active deployment, and browse recent ones. Mirrors GitHubMenu.
 */
export default function VercelMenu({
  projectId,
  projectName,
}: {
  projectId: string;
  projectName: string;
}) {
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<VercelDeploymentsResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<'preview' | 'production' | null>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const buildingRef = useRef(false);
  const navigate = useNavigate();

  const load = useCallback(() => {
    api
      .vercelDeployments(projectId)
      .then((d) => {
        setData(d);
        setErr(null);
      })
      .catch((e) => setErr(errMsg(e)));
  }, [projectId]);

  useEffect(() => {
    buildingRef.current = !!data?.active && !isTerminal(data.active.state);
  }, [data]);

  useEffect(() => {
    if (!open) return;
    load();
    const t = setInterval(() => {
      if (buildingRef.current) void load();
    }, 2500);
    const onDoc = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => {
      clearInterval(t);
      document.removeEventListener('mousedown', onDoc);
    };
  }, [open, load]);

  const deploy = async (target: 'preview' | 'production') => {
    setBusy(target);
    setErr(null);
    try {
      await api.vercelDeploy(projectId, target === 'production' ? 'production' : undefined);
      await load();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(null);
    }
  };

  const active = data?.active;
  const activeState = active ? active.state : undefined;
  const activeError = active && 'error' in active ? active.error : undefined;
  const recent = (data?.recent ?? []).slice(0, 5);

  return (
    <div className="relative" ref={menuRef}>
      <IconButton aria-label="Vercel" title="Vercel deploy" onClick={() => setOpen((o) => !o)}>
        <SiVercel className="h-5 w-5" />
      </IconButton>

      {open && (
        <div className="absolute right-0 top-full z-40 mt-1 w-80 rounded-xl border border-border bg-bg p-3 shadow-2xl">
          <div className="mb-2 flex items-center justify-between">
            <span className="text-xs font-medium uppercase tracking-wide text-faint">Vercel</span>
            {data?.connected ? (
              <a
                href="https://vercel.com/dashboard"
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center gap-1 text-xs text-accent hover:underline"
              >
                Dashboard <IconExternalLink className="h-3 w-3" />
              </a>
            ) : null}
          </div>

          {err && (
            <div className="mb-2">
              <ErrorBox message={err} />
            </div>
          )}

          {data && !data.connected && !err && (
            <div className="mb-2 flex flex-col items-center gap-2 rounded-lg border border-border p-3 text-center">
              <p className="text-xs text-dim">
                Connect your Vercel account in Settings to deploy this project.
              </p>
              <Button
                variant="outline"
                className="min-h-[30px] px-2.5 text-xs"
                onClick={() => navigate('/settings', { state: { from: `/project/${projectId}` } })}
              >
                Connect Vercel
              </Button>
            </div>
          )}

          {data?.connected && (
            <>
              <div className="flex flex-col gap-1.5">
                <Button
                  variant="outline"
                  className="w-full justify-start"
                  disabled={busy !== null || !isTerminal(activeState)}
                  onClick={() => void deploy('preview')}
                >
                  {busy === 'preview' ? <Spinner className="h-4 w-4" /> : null}
                  Deploy preview
                </Button>
                <Button
                  variant="outline"
                  className="w-full justify-start"
                  disabled={busy !== null || !isTerminal(activeState)}
                  onClick={() => void deploy('production')}
                >
                  {busy === 'production' ? <Spinner className="h-4 w-4" /> : null}
                  Deploy to production
                </Button>
              </div>

              {activeState && (
                <div className="mt-2.5 flex items-center gap-2 rounded-lg border border-border px-3 py-2">
                  {isTerminal(activeState) ? (
                    <StateBadge state={activeState} />
                  ) : (
                    <span className="flex shrink-0 items-center gap-1 rounded-full bg-border px-1.5 py-0.5 text-[10px] text-dim">
                      <Spinner className="h-2.5 w-2.5" /> building
                    </span>
                  )}
                  {activeState === 'READY' && active && 'url' in active && active.url ? (
                    <DeploymentUrl url={active.url} />
                  ) : (
                    <span className="min-w-0 flex-1 truncate text-xs text-dim">
                      {activeState === 'READY'
                        ? projectName
                        : activeError
                          ? activeError
                          : 'Deploying…'}
                    </span>
                  )}
                </div>
              )}

              {recent.length > 0 && (
                <div className="mt-3">
                  <div className="mb-1.5 flex items-center justify-between">
                    <span className="text-[11px] text-subtle">Recent deployments</span>
                    <span className="text-[11px] text-faint">{recent.length}</span>
                  </div>
                  <ul className="flex flex-col gap-1">
                    {recent.map((d: VercelDeployment) => (
                      <li
                        key={d.id}
                        className="flex items-center gap-2 rounded-lg px-2 py-1.5 transition-colors hover:bg-surface"
                      >
                        <StateBadge state={d.state} />
                        <DeploymentUrl url={d.url} />
                        <span className="flex shrink-0 items-center gap-1.5">
                          {d.production && (
                            <span className="rounded-full bg-border px-1.5 py-0.5 text-[10px] text-dim">
                              prod
                            </span>
                          )}
                          <span className="text-[10px] text-faint">
                            {d.createdAt ? relTime(d.createdAt) : ''}
                          </span>
                        </span>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
}
