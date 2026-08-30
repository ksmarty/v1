import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App';
import { initTheme } from './themes';
import './index.css';

initTheme();

// Register the service worker only in production builds so dev never serves
// stale cached assets. updateViaCache 'none' + an explicit update() on every
// load means a fixed sw.js replaces a broken one on the next visit.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    void navigator.serviceWorker
      .register('/sw.js', { updateViaCache: 'none' })
      .then((reg) => {
        void reg.update();
      })
      .catch(() => {
        // SW registration is best-effort (e.g. unsupported or private mode).
      });
  });
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
