// Safe localStorage access. iOS caps the origin's storage at ~5MB counted in
// UTF-16 units (so ~2.5M characters), and a full quota makes every
// `setItem` throw — which, from a keystroke effect or a render, crashes the
// chat. Everything that writes goes through here: on quota failure the
// re-derivable caches are evicted and the write is retried once; if the retry
// also fails the write is dropped. Settings are never evicted.

const EVICTABLE = ['v1-chat-cache:', 'v1.notified.'];
const EVICT_SINGLE: Record<string, true> = {
  'v1.providersCache': true,
  'v1.providersCacheAt': true,
  'v1.thinkingMeta': true,
};

function quotaError(e: unknown): boolean {
  return (
    e instanceof DOMException &&
    (e.name === 'QuotaExceededError' || e.name === 'NS_ERROR_DOM_QUOTA_REACHED')
  );
}

/** Removes re-derivable entries until storage accepts writes again. */
function evict(): void {
  // Collect first: mutating the list while walking it by index skips keys.
  const keys: string[] = [];
  for (let i = 0; i < localStorage.length; i++) {
    const k = localStorage.key(i);
    if (k) keys.push(k);
  }
  const evictable = keys.filter((k) => {
    if (EVICT_SINGLE[k]) return true;
    return EVICTABLE.some((p) => k.startsWith(p));
  });
  for (const k of evictable) {
    try {
      localStorage.removeItem(k);
    } catch {
      // ignore
    }
  }
}

export function lsSet(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch (e) {
    if (!quotaError(e)) return;
    evict();
    try {
      localStorage.setItem(key, value);
    } catch {
      // Still full: this write is dropped. Nothing that matters lives here
      // without a server copy.
    }
  }
}

export function lsRemove(key: string): void {
  try {
    localStorage.removeItem(key);
  } catch {
    // ignore
  }
}
