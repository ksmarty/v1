import { useEffect, useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { api } from '../api';
import type { AuthStatus } from '../types';
import { errMsg } from '../utils';
import { Button, Center, ErrorBox, Input, Spinner } from '../components/ui';
import { IconLock } from '../components/icons';

export default function Login({ mode }: { mode: 'login' | 'setup' }) {
  const navigate = useNavigate();
  const [authStatus, setAuthStatus] = useState<AuthStatus | null>(null);
  const [checking, setChecking] = useState(true);
  const [checkError, setCheckError] = useState<string | null>(null);
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    api
      .getAuthStatus()
      .then((s) => {
        if (cancelled) return;
        setAuthStatus(s);
        if (mode === 'login') {
          if (s.setupRequired) {
            navigate('/setup', { replace: true });
            return;
          }
          if (!s.authRequired || s.authenticated) {
            navigate('/', { replace: true });
            return;
          }
        } else {
          // setup mode
          if (!s.setupRequired) {
            if (!s.authRequired || s.authenticated) navigate('/', { replace: true });
            else navigate('/login', { replace: true });
            return;
          }
        }
        setChecking(false);
      })
      .catch((e) => {
        if (cancelled) return;
        setCheckError(errMsg(e));
        setChecking(false);
      });
    return () => {
      cancelled = true;
    };
  }, [mode, navigate]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (!password) {
      setError('Password is required.');
      return;
    }
    if (mode === 'setup' && password !== confirm) {
      setError('Passwords do not match.');
      return;
    }
    setBusy(true);
    try {
      if (mode === 'setup') await api.setup(password);
      else await api.login(password);
      navigate('/', { replace: true });
    } catch (err) {
      setError(errMsg(err));
      setBusy(false);
    }
  };

  if (checking) {
    return (
      <Center>
        <Spinner className="h-6 w-6" />
      </Center>
    );
  }

  if (checkError) {
    return (
      <Center>
        <p className="text-sm text-dim">Cannot reach the server: {checkError}</p>
        <Button variant="outline" onClick={() => window.location.reload()}>
          Retry
        </Button>
      </Center>
    );
  }

  const isSetup = mode === 'setup';

  return (
    <div className="flex min-h-dvh items-center justify-center p-4">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex flex-col items-center text-center">
          <div className="mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-primary text-lg font-bold tracking-tight text-primary-text">
            v1
          </div>
          <h1 className="text-lg font-semibold text-text">
            {isSetup ? 'Create your password' : 'Sign in to v1'}
          </h1>
          <p className="mt-1 text-sm text-subtle">
            {isSetup
              ? 'This password protects your v1 instance.'
              : 'Enter your password to continue.'}
          </p>
        </div>

        {!isSetup && authStatus?.oidcEnabled && (
          <>
            <Button
              variant="outline"
              className="w-full"
              onClick={() => {
                window.location.href = '/api/auth/oidc/start';
              }}
            >
              Sign in with Authentik
            </Button>
            <div className="my-4 flex items-center gap-3 text-xs text-faint">
              <div className="h-px flex-1 bg-border" />
              or with password
              <div className="h-px flex-1 bg-border" />
            </div>
          </>
        )}

        <form
          onSubmit={submit}
          className="rounded-2xl border border-border bg-bg p-5"
        >
          <div className="flex flex-col gap-3">
            <div className="relative">
              <IconLock className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-faint" />
              <Input
                type="password"
                autoFocus
                autoComplete={isSetup ? 'new-password' : 'current-password'}
                placeholder="Password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="pl-9"
              />
            </div>
            {isSetup && (
              <div className="relative">
                <IconLock className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-faint" />
                <Input
                  type="password"
                  autoComplete="new-password"
                  placeholder="Confirm password"
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                  className="pl-9"
                />
              </div>
            )}
            {error && <ErrorBox message={error} />}
            <Button type="submit" disabled={busy} className="w-full">
              {busy ? <Spinner className="h-4 w-4" /> : isSetup ? 'Create password' : 'Sign in'}
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
