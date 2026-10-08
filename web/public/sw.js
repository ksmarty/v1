// v1 service worker: installable + offline app shell.
// API, preview, and WebSocket traffic is never cached or intercepted.
//
// INVARIANT: every respondWith() must settle with a real Response — never
// undefined and never a rejected promise. A rejected respondWith surfaces as
// "FetchEvent ... resulted in a network error response: the promise was
// rejected" / "Failed to convert value to 'Response'" and kills the load.
const VERSION = 'v1-cache-v6';
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

function b64urlToBytes(base64) {
  const padded = base64.replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(padded + '='.repeat((4 - (padded.length % 4)) % 4));
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

function subscriptionBody(sub) {
  const json = sub.toJSON();
  return {
    endpoint: sub.endpoint,
    p256dh: (json.keys && json.keys.p256dh) || '',
    auth: (json.keys && json.keys.auth) || '',
    userAgent: (self.navigator && self.navigator.userAgent) || '',
  };
}

// Web Push: the notification path that still works with the app closed. iOS
// suspends the page's JavaScript within seconds of backgrounding, so a
// notification built by the page can never fire while the app is away — only
// this handler can.
//
// waitUntil is called synchronously (the async body runs inside it): the push
// event is only kept alive by a waitUntil registered during dispatch.
self.addEventListener('push', (e) => {
  e.waitUntil(
    (async () => {
      let msg = {};
      try {
        msg = e.data ? await e.data.json() : {};
      } catch {
        msg = {};
      }
      await self.registration.showNotification(msg.title || 'v1', {
        body: msg.body || '',
        icon: '/icon-192.png?v=3',
        badge: '/icon-192.png?v=3',
        // One tag per chat: a newer notification replaces the older one for the
        // same chat instead of stacking, and renotify still re-alerts.
        tag: msg.tag || 'v1',
        renotify: true,
        data: { url: msg.url || '/' },
      });
    })().catch(() => {}),
  );
});

// The browser rotates a subscription while the app is closed. Without this the
// server keeps a dead endpoint and notifications stop with no visible error.
self.addEventListener('pushsubscriptionchange', (e) => {
  e.waitUntil(
    (async () => {
      let key = (e.oldSubscription && e.oldSubscription.options && e.oldSubscription.options.applicationServerKey) || null;
      if (!key) {
        const res = await fetch('/api/push/vapid');
        const body = await res.json();
        key = b64urlToBytes(body.publicKey);
      }
      const sub = await self.registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: key,
      });
      await fetch('/api/push/subscribe', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(subscriptionBody(sub)),
      });
    })().catch(() => {}),
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

  // Navigations: network first, so a reload picks up a newly deployed build
  // immediately. This used to serve the cached shell first and refresh it in
  // the background, which meant the page you just loaded was always one build
  // behind — and an app left open never navigated again, so it ran the old
  // bundle indefinitely while the version in Settings (fetched live from
  // /api) already showed the new one. The cached shell is still the offline
  // fallback, and `no-store` stops the browser's own HTTP cache from serving
  // the stale copy we are trying to get past.
  if (req.mode === 'navigate') {
    e.respondWith(
      fetch(req, { cache: 'no-store' })
        .then((res) => {
          cachePut(VERSION, SHELL_KEY, res);
          return res;
        })
        .catch(() =>
          caches.match(SHELL_KEY).then((cached) => cached || offlineResponse()),
        ),
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