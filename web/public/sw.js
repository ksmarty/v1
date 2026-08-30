// v1 service worker: installable + offline app shell.
// API, preview, and WebSocket traffic is never cached or intercepted.
//
// INVARIANT: every respondWith() must settle with a real Response — never
// undefined and never a rejected promise. A rejected respondWith surfaces as
// "FetchEvent ... resulted in a network error response: the promise was
// rejected" / "Failed to convert value to 'Response'" and kills the load.
const VERSION = 'v1-cache-v4';
const SHELL_KEY = '/index.html';
const APP_SHELL = ['/', '/index.html', '/manifest.json?v=3', '/icon-192.png?v=3', '/icon-512.png?v=3'];

function offlineResponse() {
  return new Response(
    '<!doctype html><meta charset="utf-8"><title>Offline</title>' +
      '<body style="font-family:sans-serif;text-align:center;padding-top:20vh">' +
      'Offline — check your connection and reload.</body>',
    { status: 503, headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' } },
  );
}

// Best-effort cache write. Never throws, never leaves an unhandled rejection
// (caches.put rejects on streaming/opaque/206 responses).
function cachePut(cacheName, req, res) {
  if (!res || !res.ok) return;
  caches
    .open(cacheName)
    .then((c) => c.put(req, res.clone()))
    .catch(() => {});
}

// Turn-finished notifications: open/focus the app and navigate to the exact
// chat the notification is about (from the notification's data.url).
self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const url = e.notification.data?.url;
  e.waitUntil(
    clients.matchAll({ type: 'window', includeUncontrolled: true }).then((cs) => {
      for (const c of cs) {
        if (c.focus) c.focus();
        if (url && 'navigate' in c) {
          c.navigate(url);
          return;
        }
        if (url) {
          c.postMessage({ type: 'navigate', url });
        }
        return;
      }
      return clients.openWindow(url || '/');
    }),
  );
});

self.addEventListener('install', (e) => {
  e.waitUntil(
    caches
      .open(VERSION)
      // Pre-cache is best-effort: a single failing asset must not kill the
      // install (which would leave no cache and no skipWaiting).
      .then((c) => c.addAll(APP_SHELL).catch(() => {}))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== VERSION).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (e) => {
  const req = e.request;
  if (req.method !== 'GET' || req.headers.get('upgrade')) return;
  if (!req.url.startsWith('http')) return;
  let url;
  try {
    url = new URL(req.url);
  } catch {
    return;
  }
  if (url.origin !== self.location.origin) return;
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/preview/')) return;

  // Navigations: serve the cached app shell immediately (the SPA routes
  // client-side) and refresh the shell in the background. The page load can
  // never be sunk by a flaky network leg, and the shell is always present
  // after the first successful load.
  if (req.mode === 'navigate') {
    e.respondWith(
      caches.match(SHELL_KEY).then((cached) => {
        if (cached) {
          fetch(req)
            .then((res) => cachePut(VERSION, SHELL_KEY, res))
            .catch(() => {});
          return cached;
        }
        // First visit (nothing cached yet): network, then cache the shell.
        // On failure return a real Response instead of letting respondWith
        // reject with an undefined value.
        return fetch(req)
          .then((res) => {
            cachePut(VERSION, SHELL_KEY, res);
            return res;
          })
          .catch(() => offlineResponse());
      }),
    );
    return;
  }

  // Static assets: stale-while-revalidate.
  e.respondWith(
    caches.match(req).then((cached) => {
      const refresh = fetch(req)
        .then((res) => {
          cachePut(VERSION, req, res);
          return res;
        })
        .catch(() => cached || offlineResponse());
      return cached || refresh;
    }),
  );
});