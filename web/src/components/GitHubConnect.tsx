import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError } from '../api';
import type { DeviceFlowStart, GitHubScope } from '../types';
import { errMsg } from '../utils';
import { Button, Dialog, Spinner } from './ui';
import { IconCheck, IconCopy, IconExternalLink, IconGitHub, IconLock } from './icons';

type Phase =
  | { kind: 'starting' }
  | { kind: 'waiting'; flow: DeviceFlowStart }
  | { kind: 'complete'; login?: string }
  | { kind: 'failed'; message: string };

/**
 * GitHub OAuth connect button + modal. `enabled` should be true once an OAuth
 * Client ID has been saved in settings. When `secretEnabled` (a client secret
 * is configured), the connect button uses the redirect (authorization-code)
 * flow — bounce to GitHub and back, no code entry. Otherwise it uses the
 * device flow (enter a code).
 */
export default function GitHubConnect({
  enabled,
  secretEnabled = false,
  onConnected,
}: {
  enabled: boolean;
  secretEnabled?: boolean;
  onConnected: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [phase, setPhase] = useState<Phase | null>(null);
  const [copied, setCopied] = useState(false);
  const [scopesOpen, setScopesOpen] = useState(false);
  const [scopes, setScopes] = useState<GitHubScope[] | null>(null);
  const [selectedScopes, setSelectedScopes] = useState<Set<string>>(new Set());
  const [scopesLoading, setScopesLoading] = useState(false);
  const [scopesError, setScopesError] = useState<string | null>(null);
  const [scopesSaving, setScopesSaving] = useState(false);
  const timerRef = useRef<number | null>(null);
  const cancelledRef = useRef(false);

  const clearTimer = useCallback(() => {
    if (timerRef.current !== null) {
      window.clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  useEffect(() => clearTimer, [clearTimer]);

  const poll = useCallback(
    (flowId: string, intervalSec: number) => {
      clearTimer();
      timerRef.current = window.setTimeout(() => {
        void (async () => {
          if (cancelledRef.current) return;
          try {
            const r = await api.oauthDevicePoll(flowId);
            if (cancelledRef.current) return;
            switch (r.status) {
              case 'pending':
                poll(flowId, intervalSec);
                break;
              case 'slow_down':
                poll(flowId, intervalSec + 2);
                break;
              case 'complete':
                setPhase({ kind: 'complete', login: r.login });
                onConnected();
                break;
              case 'denied':
                setPhase({ kind: 'failed', message: 'Authorization was denied on GitHub.' });
                break;
              case 'expired':
                setPhase({ kind: 'failed', message: 'The code expired. Start a new attempt.' });
                break;
              case 'error':
                setPhase({ kind: 'failed', message: r.error || 'Authorization failed.' });
                break;
            }
          } catch (e) {
            if (!cancelledRef.current) setPhase({ kind: 'failed', message: errMsg(e) });
          }
        })();
      }, (intervalSec + 1) * 1000);
    },
    [clearTimer, onConnected],
  );

  const start = async () => {
    cancelledRef.current = false;
    setCopied(false);
    // Redirect (authorization-code) flow: bounce to GitHub and back to the
    // app — no code entry. Requires a saved client secret.
    if (secretEnabled) {
      window.location.href = '/api/auth/github/oauth/start';
      return;
    }
    setOpen(true);
    setPhase({ kind: 'starting' });
    try {
      const flow = await api.oauthDeviceStart();
      if (cancelledRef.current) return;
      setPhase({ kind: 'waiting', flow });
      poll(flow.flowId, flow.interval);
    } catch (e) {
      const friendly =
        e instanceof ApiError && e.message === 'no_client_id'
          ? 'Save an OAuth Client ID below first.'
          : errMsg(e);
      setPhase({ kind: 'failed', message: friendly });
    }
  };

  const close = useCallback(() => {
    cancelledRef.current = true;
    clearTimer();
    setOpen(false);
    setPhase(null);
  }, [clearTimer]);

  // Auto-close shortly after a successful connect.
  useEffect(() => {
    if (phase?.kind !== 'complete') return;
    const t = window.setTimeout(close, 2000);
    return () => window.clearTimeout(t);
  }, [phase, close]);

  const copyCode = (code: string) => {
    void navigator.clipboard?.writeText(code).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    });
  };

  const openScopes = async () => {
    setScopesOpen(true);
    setScopesLoading(true);
    setScopesError(null);
    try {
      const r = await api.githubScopes();
      setScopes(r.scopes);
      setSelectedScopes(new Set(r.selected));
    } catch (e) {
      setScopesError(errMsg(e));
    } finally {
      setScopesLoading(false);
    }
  };

  const toggleScope = (name: string) => {
    setSelectedScopes((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  };

  const saveScopes = async () => {
    setScopesSaving(true);
    setScopesError(null);
    try {
      const r = await api.saveGitHubScopes([...selectedScopes]);
      setSelectedScopes(new Set(r.selected));
      setScopesOpen(false);
    } catch (e) {
      setScopesError(errMsg(e));
    } finally {
      setScopesSaving(false);
    }
  };

  return (
    <>
      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          onClick={() => void start()}
          disabled={!enabled}
          title={enabled ? 'Connect your GitHub account' : 'Save an OAuth Client ID first'}
        >
          <IconGitHub className="h-4 w-4" /> Connect with GitHub
        </Button>
        <Button
          variant="ghost"
          onClick={() => void openScopes()}
          title="Choose which GitHub permissions v1 requests"
        >
          <IconLock className="h-4 w-4" /> Scopes
        </Button>
      </div>

      <Dialog open={open} onClose={close} title="Connect with GitHub">
        {phase?.kind === 'starting' && (
          <div className="flex items-center justify-center gap-2 py-8 text-sm text-dim">
            <Spinner className="h-4 w-4" /> Requesting a device code…
          </div>
        )}

        {phase?.kind === 'waiting' && (
          <div className="flex flex-col items-center gap-4 py-2">
            <p className="text-center text-sm text-dim">
              Enter this code on GitHub to authorize v1:
            </p>
            <div className="flex items-center gap-2">
              <code className="rounded-lg border border-border bg-surface px-4 py-2.5 font-mono text-2xl font-semibold tracking-[0.2em] text-text">
                {phase.flow.userCode}
              </code>
              <Button
                variant="outline"
                className="min-h-[44px] px-3"
                onClick={() => copyCode(phase.flow.userCode)}
              >
                {copied ? <IconCheck className="h-4 w-4 text-emerald-500" /> : <IconCopy className="h-4 w-4" />}
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </div>
            <Button
              onClick={() => window.open(phase.flow.verificationUri, '_blank', 'noopener')}
            >
              <IconExternalLink className="h-4 w-4" /> Open GitHub
            </Button>
            <p className="flex items-center gap-2 text-xs text-subtle">
              <Spinner className="h-3.5 w-3.5" /> Waiting for authorization…
            </p>
          </div>
        )}

        {phase?.kind === 'complete' && (
          <div className="flex flex-col items-center gap-2 py-6 text-center">
            <span className="flex h-10 w-10 items-center justify-center rounded-full bg-emerald-950">
              <IconCheck className="h-5 w-5 text-emerald-400" />
            </span>
            <p className="text-sm text-text">
              Connected{phase.login ? ` as @${phase.login}` : ''}.
            </p>
          </div>
        )}

        {phase?.kind === 'failed' && (
          <div className="flex flex-col items-center gap-3 py-4 text-center">
            <p className="text-sm text-red-400">{phase.message}</p>
            <div className="flex gap-2">
              <Button variant="ghost" onClick={close}>
                Close
              </Button>
              <Button variant="outline" onClick={() => void start()}>
                Try again
              </Button>
            </div>
          </div>
        )}
      </Dialog>
      <Dialog
        open={scopesOpen}
        onClose={() => setScopesOpen(false)}
        title="GitHub scopes"
        fixedBody
      >
        <div className="flex h-full min-h-0 flex-col gap-3">
          <p className="shrink-0 text-xs text-dim">
            Choose which permissions v1 requests. These are sent the next time you connect.
          </p>
          {scopesLoading && (
            <div className="flex items-center justify-center gap-2 py-8 text-sm text-dim">
              <Spinner className="h-4 w-4" /> Loading scopes…
            </div>
          )}
          {scopesError && <p className="shrink-0 text-sm text-red-400">{scopesError}</p>}
          {scopes && (
            <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">
              <div className="flex flex-col gap-1">
                {scopes.map((sc) => (
                  <label
                    key={sc.name}
                    className="flex cursor-pointer items-start gap-3 rounded-lg border border-border px-3 py-2 hover:bg-surface"
                  >
                    <input
                      type="checkbox"
                      className="mt-0.5 h-4 w-4 shrink-0 accent-indigo-500"
                      checked={selectedScopes.has(sc.name)}
                      onChange={() => toggleScope(sc.name)}
                    />
                    <span className="min-w-0">
                      <code className="font-mono text-sm text-text">{sc.name}</code>
                      {sc.description && (
                        <span className="mt-0.5 block text-xs text-dim">{sc.description}</span>
                      )}
                    </span>
                  </label>
                ))}
              </div>
            </div>
          )}
          <div className="flex shrink-0 justify-end gap-2">
            <Button variant="ghost" onClick={() => setScopesOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={() => void saveScopes()}
              disabled={scopesSaving || selectedScopes.size === 0}
            >
              {scopesSaving ? 'Saving…' : 'Save'}
            </Button>
          </div>
        </div>
      </Dialog>
    </>
  );
}
