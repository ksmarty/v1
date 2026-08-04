import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError } from '../api';
import type { DeviceFlowStart } from '../types';
import { errMsg } from '../utils';
import { Button, Dialog, Spinner } from './ui';
import { IconCheck, IconCopy, IconExternalLink, IconGitHub } from './icons';

type Phase =
  | { kind: 'starting' }
  | { kind: 'waiting'; flow: DeviceFlowStart }
  | { kind: 'complete'; login?: string }
  | { kind: 'failed'; message: string };

/**
 * GitHub OAuth device-flow connect button + modal. `enabled` should be true
 * only once an OAuth Client ID has been saved in settings.
 */
export default function GitHubConnect({
  enabled,
  onConnected,
}: {
  enabled: boolean;
  onConnected: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [phase, setPhase] = useState<Phase | null>(null);
  const [copied, setCopied] = useState(false);
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

  return (
    <>
      <Button
        variant="outline"
        onClick={() => void start()}
        disabled={!enabled}
        title={enabled ? 'Connect your GitHub account' : 'Save an OAuth Client ID first'}
      >
        <IconGitHub className="h-4 w-4" /> Connect with GitHub
      </Button>

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
    </>
  );
}
