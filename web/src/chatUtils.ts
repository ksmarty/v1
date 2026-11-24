function inlineHash(value: string): string {
  let hash = 2166136261;
  for (let i = 0; i < value.length; i++) {
    hash = Math.imul(hash ^ value.charCodeAt(i), 16777619);
  }
  return (hash >>> 0).toString(36);
}

/**
 * Fingerprints every persisted part of a transcript row, not just content
 * lengths and collection sizes. Ephemeral editor/streaming flags are ignored;
 * inline attachment bytes are hashed so a data URL cannot megabyte-bloat the
 * signature while same-size replacements still compare differently.
 */
export function transcriptSignature(items: unknown[]): string {
  return JSON.stringify(items, (key, value) => {
    if (key === 'editing' || key === 'stale' || key === 'reasoningCollapsed' || key === 'streaming') {
      return undefined;
    }
    if (key === 'url' && typeof value === 'string' && value.startsWith('data:')) {
      return `data:${value.length}:${inlineHash(value)}`;
    }
    return value;
  });
}

/** Extracts the useful reason from a stored tool failure, if there is one. */
export function resultError(detail: string): string | null {
  const t = detail.trim();
  if (!t) return null;
  if (/^error[:\s]/i.test(t)) return t;
  if (t[0] !== '{') return null;
  try {
    const d = JSON.parse(t) as Record<string, unknown>;
    if (!d || typeof d !== 'object') return null;
    const e = d.error;
    if (typeof e === 'string' && e) return e;
    if (e && typeof e === 'object') {
      const inner = (e as { innerText?: unknown }).innerText;
      if (typeof inner === 'string' && inner.trim()) return inner;
      const msg = (e as { message?: unknown }).message;
      return typeof msg === 'string' && msg ? msg : t;
    }
    if (d.success === false || d.ok === false) return t;
    return null;
  } catch {
    return null;
  }
}
