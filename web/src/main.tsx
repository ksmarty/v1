import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App';
import { initTheme } from './themes';
import './index.css';

initTheme();

// Register the service worker only in production builds so dev never serves
// stale cached assets.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    void navigator.serviceWorker.register('/sw.js').catch(() => {
      // SW registration is best-effort (e.g. unsupported or private mode).
    });
  });
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
