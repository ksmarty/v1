// Client-side diagnostics for the server's /diagnostics export.
//
// Nothing in this module may EVER throw or reject into application code: the
// logger is the last thing that is allowed to break the page it is trying to
// explain. Every public entry point and every event handler is wrapped in a
// try/catch, and the flush request swallows all errors on purpose.

export type DebugEntry = {
  at: string;
  kind: string;
  msg: string;
  stack?: string;
  extra?: Record<string, unknown>;
};

const MAX_ENTRIES = 250;
const MAX_OUTBOX = 100;
const FLUSH_DELAY_MS = 800;
const MAX_FLUSH_DELAY_MS = 30_000;
const MAX_FIELD = 4000;

// Everything we have seen, newest last, bounded. Breadcrumbs live here only —
// they are never sent as standalone entries; they ride along as context on the
// next error (`extra.breadcrumbs`) and are retried via the outbox below.
const buffer: DebugEntry[] = [];

// Errors awaiting acceptance by the server. On success we drop the sent
// prefix; on ANY failure we keep it so a later flush retries.
const outbox: DebugEntry[] = [];

let installed = false;
let flushTimer: ReturnType<typeof setTimeout> | null = null;
let flushing = false;
let failures = 0;

function clip(s: string): string {
  if (s.length <= MAX_FIELD) return s;
  return `${s.slice(0, MAX_FIELD)}…[${s.length - MAX_FIELD} more]`;
}

function safeString(value: unknown): string {
  try {
    return String(value);
  } catch {
    return '<unprintable>';
  }
}

export function describeError(err: unknown): { msg: string; stack?: string } {
  try {
    if (err instanceof Error) {
      const stack = typeof err.stack === 'string' ? clip(err.stack) : undefined;
      return { msg: clip(`${err.name}: ${err.message}`), stack };
    }
    if (typeof err === 'string') {
      return { msg: clip(err) };
    }
    let json: string | undefined;
    try {
      json = JSON.stringify(err);
    } catch {
      json = undefined;
    }
    return { msg: clip(typeof json === 'string' ? json : safeString(err)) };
  } catch {
    return { msg: '<failed to describe error>' };
  }
}

export function clientInfo(): Record<string, unknown> {
  try {
    return {
      url: location.href,
      userAgent: navigator.userAgent,
      platform: navigator.platform,
      language: navigator.language,
      viewport: `${window.innerWidth}x${window.innerHeight}`,
      dpr: window.devicePixelRatio,
      visibility: document.visibilityState,
      timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      online: navigator.onLine,
    };
  } catch {
    return {};
  }
}

function remember(entry: DebugEntry): void {
  buffer.push(entry);
  if (buffer.length > MAX_ENTRIES) {
    buffer.splice(0, buffer.length - MAX_ENTRIES);
  }
}

function recentBreadcrumbs(): DebugEntry[] {
  const out: DebugEntry[] = [];
  for (let i = buffer.length - 1; i >= 0 && out.length < 20; i--) {
    if (buffer[i].kind !== 'error') out.push(buffer[i]);
  }
  return out.reverse();
}

function enqueue(entry: DebugEntry): void {
  outbox.push(entry);
  if (outbox.length > MAX_OUTBOX) {
    outbox.splice(0, outbox.length - MAX_OUTBOX);
  }
}

function scheduleFlush(delayMs: number = FLUSH_DELAY_MS): void {
  if (flushTimer !== null) return;
  flushTimer = setTimeout(() => {
    flushTimer = null;
    void flush();
  }, delayMs);
}

async function flush(): Promise<void> {
  if (flushing || outbox.length === 0) return;
  flushing = true;
  const batchSize = outbox.length;
  try {
    const body = JSON.stringify({
      client: clientInfo(),
      entries: outbox.slice(0, batchSize),
    });
    const res = await fetch('/api/client-log', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body,
      keepalive: true,
    });
    if (res.ok) {
      failures = 0;
      // Drop only the entries we sent; anything appended during the request
      // stays queued for the next flush.
      outbox.splice(0, Math.min(batchSize, outbox.length));
    } else {
      failures++;
    }
  } catch {
    // Transport failed — keep the outbox so the next flush retries.
    failures++;
  } finally {
    flushing = false;
    // Back off when the endpoint keeps refusing, so a broken (or missing)
    // collector cannot turn into a request every FLUSH_DELAY_MS forever.
    if (outbox.length > 0) {
      scheduleFlush(Math.min(FLUSH_DELAY_MS * 2 ** failures, MAX_FLUSH_DELAY_MS));
    }
  }
}

export function debugError(
  kind: string,
  err: unknown,
  extra?: Record<string, unknown>,
): void {
  try {
    const { msg, stack } = describeError(err);
    const entry: DebugEntry = {
      at: new Date().toISOString(),
      kind,
      msg,
      stack,
      extra: { ...extra, breadcrumbs: recentBreadcrumbs() },
    };
    remember(entry);
    enqueue(entry);
    void flush();
  } catch {
    // never let the logger break the page
  }
}

export function debugBreadcrumb(
  kind: string,
  msg: string,
  extra?: Record<string, unknown>,
): void {
  try {
    remember({
      at: new Date().toISOString(),
      kind,
      msg: clip(msg),
      extra: extra ? { ...extra } : undefined,
    });
    // Breadcrumbs are buffered only; a lone breadcrumb makes no request, but a
    // pending flush is scheduled so a previously-failed error still retries.
    scheduleFlush();
  } catch {
    // never let the logger break the page
  }
}

function onWindowError(ev: Event): void {
  try {
    const e = ev as ErrorEvent;
    if (e.error) {
      debugError('error', e.error, {
        source: `${e.filename}:${e.lineno}:${e.colno}`,
      });
      return;
    }
    // No Error object means a resource (img/script/etc.) failed to load.
    const target = ev.target as (Element & { src?: string; href?: string }) | null;
    let tag = 'resource';
    if (target && typeof target.tagName === 'string') {
      tag = target.tagName.toLowerCase();
    }
    let src: string | undefined;
    if (target) {
      if (typeof target.src === 'string') src = target.src;
      else if (typeof target.href === 'string') src = target.href;
    }
    debugBreadcrumb('resource', `failed to load <${tag}>`, src ? { src } : undefined);
  } catch {
    // never let the logger break the page
  }
}

function onUnhandledRejection(ev: Event): void {
  try {
    debugError('unhandledrejection', (ev as PromiseRejectionEvent).reason);
  } catch {
    // never let the logger break the page
  }
}

export function installDebugLog(): void {
  if (installed) return;
  installed = true;
  try {
    window.addEventListener('error', onWindowError, true);
    window.addEventListener('unhandledrejection', onUnhandledRejection);
    debugBreadcrumb('boot', 'debug log installed');
  } catch {
    // never let the logger break the page
  }
}
