import { useEffect, useState, type ReactNode } from 'react';
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { api } from './api';
import type { AuthStatus } from './types';
import { errMsg } from './utils';
import { useAppHeight } from './hooks/useAppHeight';
import { Button, Center, Spinner } from './components/ui';
import Login from './pages/Login';
import Projects from './pages/Projects';
import Project from './pages/Project';
import Settings from './pages/Settings';

function RequireAuth({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<AuthStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let cancelled = false;
    api
      .getAuthStatus()
      .then((s) => {
        if (cancelled) return;
        setStatus(s);
        setError(null);
      })
      .catch((e) => {
        if (!cancelled) setError(errMsg(e));
      });
    return () => {
      cancelled = true;
    };
  }, [attempt]);

  if (error) {
    return (
      <Center>
        <p className="text-sm text-dim">Cannot reach the server: {error}</p>
        <Button
          variant="outline"
          onClick={() => {
            setError(null);
            setStatus(null);
            setAttempt((a) => a + 1);
          }}
        >
          Retry
        </Button>
      </Center>
    );
  }
  if (!status) {
    return (
      <Center>
        <Spinner className="h-6 w-6" />
      </Center>
    );
  }
  if (status.setupRequired) return <Navigate to="/setup" replace />;
  if (status.authRequired && !status.authenticated) return <Navigate to="/login" replace />;
  return <>{children}</>;
}

export default function App() {
  useAppHeight();
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login mode="login" />} />
        <Route path="/setup" element={<Login mode="setup" />} />
        <Route
          path="/"
          element={
            <RequireAuth>
              <Projects />
            </RequireAuth>
          }
        />
        <Route
          path="/project/:id"
          element={
            <RequireAuth>
              <Project />
            </RequireAuth>
          }
        />
        <Route
          path="/settings"
          element={
            <RequireAuth>
              <Settings />
            </RequireAuth>
          }
        />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  );
}
