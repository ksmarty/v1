import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App';
import ErrorBoundary from './components/ErrorBoundary';
import { installDebugLog } from './debuglog';
import { initTheme } from './themes';
import './index.css';

initTheme();
import { registerPush } from './push';

installDebugLog();

// Register the service worker only in production builds so dev never serves
// stale cached assets. updateViaCache 'none' + an explicit update() on every
// load means a fixed sw.js replaces a broken one on the next visit.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  // A new worker taking control means this page is running an older build.
  // Reload once so the app matches the worker: without this, a PWA that is left
  // open and resumed keeps executing the bundle it was loaded with, and only a
  // full close fixes it. The controller check skips the very first install,
  // where claiming control is not an update.
  let reloaded = false;
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (reloaded || !navigator.serviceWorker.controller) return;
    reloaded = true;
    window.location.reload();
  });

  window.addEventListener('load', () => {
    void navigator.serviceWorker
      .register('/sw.js', { updateViaCache: 'none' })
      .then((reg) => {
        void reg.update();
        // Resuming an app that was left open is the case that stayed stale:
        // no navigation happens, so nothing ever checked for a new build.
        document.addEventListener('visibilitychange', () => {
          if (document.visibilityState === 'visible') void reg.update();
        });
        // Register for Web Push once the worker exists. Idempotent, and every
        // failure is swallowed: this only decides whether a notification can
        // reach the device with the app closed.
        void registerPush();
      })
      .catch(() => {
        // SW registration is best-effort (e.g. unsupported or private mode).
      });
  });
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </React.StrictMode>,
);
