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
    if (
      key === 'editing' ||
      key === 'stale' ||
      key === 'reasoningCollapsed' ||
      key === 'streaming' ||
      key === 'afterId'
    ) {
      return undefined;
    }
    if (key === 'url' && typeof value === 'string' && value.startsWith('data:')) {
      return `data:${value.length}:${inlineHash(value)}`;
    }
    return value;
  });
}

/**
 * Orders persisted numeric message keys by SQLite ID while keeping synthetic
 * live keys (`uN`, `sN`, `tN`, `aN`, `eN`) anchored after the persisted message
 * ID that preceded them (`afterId`). Putting synthetic rows at
 * `MAX_SAFE_INTEGER` caused mid-turn numeric rows (`injected_message`,
 * `notice`, follow-up user messages) to jump above earlier live user/assistant
 * rows and pushed earlier synthetic rounds to the bottom of the transcript.
 */
export function canonicalizeTranscriptItems<T extends { key: string; afterId?: number }>(
  items: T[],
): T[] {
  let inferredAfterId = 0;
  const decorated = items.map((it, index) => {
    const n = Number(it.key);
    if (Number.isFinite(n) && n > 0) {
      if (n > inferredAfterId) inferredAfterId = n;
      return { it, index, primary: n, secondary: 0 };
    }
    const afterId =
      typeof it.afterId === 'number' && Number.isFinite(it.afterId) && it.afterId >= 0
        ? it.afterId
        : inferredAfterId;
    const m = /^[ustae](\d+)$/.exec(it.key);
    const secondary = m ? Number(m[1]) : index + 1;
    return { it, index, primary: afterId, secondary };
  });
  const sorted = decorated
    .slice()
    .sort((a, b) => a.primary - b.primary || a.secondary - b.secondary || a.index - b.index);
  for (let i = 0; i < sorted.length; i++) {
    if (sorted[i].it !== items[i]) return sorted.map((d) => d.it);
  }
  return items;
}

/**
 * Finds the assistant row in `mapped` that owns a persisted `"tool"` result row.
 * A mid-round injection (`"From the agent"` screenshot, background completion,
 * steer, or compaction notice) can be persisted between an assistant row and
 * the remaining tool results of that round; checking only `mapped[length - 1]`
 * orphaned those results into a new assistant bubble below the injection.
 */
export function findAssistantIndexForToolResult(
  mapped: Array<{
    kind: string;
    role?: string;
    injected?: boolean;
    background?: boolean;
    steered?: boolean;
    notice?: boolean;
    toolCalls?: Array<{ id?: string }>;
  }>,
  toolCallId?: string,
): number {
  if (toolCallId) {
    for (let i = mapped.length - 1; i >= 0; i--) {
      const it = mapped[i];
      if (
        it.kind === 'msg' &&
        it.role === 'assistant' &&
        it.toolCalls?.some((c) => c.id === toolCallId)
      ) {
        return i;
      }
    }
  }
  for (let i = mapped.length - 1; i >= 0; i--) {
    const it = mapped[i];
    if (it.kind === 'msg' && it.role === 'assistant') return i;
    if (
      it.kind === 'msg' &&
      it.role === 'user' &&
      (it.injected || it.background || it.steered || it.notice)
    ) {
      continue;
    }
    break;
  }
  return -1;
}

/**
 * Chooses the turn-final summary entry rendered outside `CollapsedToolsGroup`.
 * Only the final entry of a turn part can be the summary, and only when it is
 * pure assistant text without tool calls, tool results, or an ask of its own.
 * Promoting an earlier assistant entry pulled pre-tool agent comments below the
 * tools that followed them.
 */
export function pickTurnSummaryIndex(
  entries: Array<{
    it: { kind: string; role?: string; content?: string; usage?: unknown };
    d?: { calls: unknown[]; results: unknown[]; ask?: unknown } | null;
  }>,
): number {
  if (entries.length === 0) return -1;
  const isToolFreeAssistantText = (entry: {
    it: { kind: string; role?: string; content?: string; usage?: unknown };
    d?: { calls: unknown[]; results: unknown[]; ask?: unknown } | null;
  }): boolean => {
    if (entry.it.kind !== 'msg' || entry.it.role !== 'assistant' || !entry.it.content?.trim()) {
      return false;
    }
    if (
      entry.d &&
      (entry.d.calls.length > 0 || entry.d.results.length > 0 || entry.d.ask != null)
    ) {
      return false;
    }
    return true;
  };
  const lastIdx = entries.length - 1;
  if (isToolFreeAssistantText(entries[lastIdx])) {
    return lastIdx;
  }
  for (let k = lastIdx - 1; k >= 0; k--) {
    if (isToolFreeAssistantText(entries[k]) && Boolean(entries[k].it.usage)) {
      return k;
    }
  }
  return -1;
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

export type UsageLimitWindowKind = '5-hour' | 'weekly' | 'monthly' | 'daily';

export type UsageLimitErrorInfo = {
  cleanMessage: string;
  errorType?: string;
  windowKind: UsageLimitWindowKind | null;
  windowLabel: string | null;
  resetMs: number | null;
  resetAt: number | null;
};

function decodePercentText(raw: string): string {
  if (!/%(?:20|22|27|28|29|2C|3A|3B|5B|5D|7B|7D)/i.test(raw)) return raw;
  try {
    return decodeURIComponent(raw);
  } catch {
    return raw.replace(/%[0-9A-Fa-f]{2}/g, (m) => {
      try {
        return decodeURIComponent(m);
      } catch {
        return m;
      }
    });
  }
}

function parseResetDurationMs(text: string): number | null {
  const m =
    /resets?\s+in\s+((?:\d+\s*(?:d|days?|h|hrs?|hours?|m|mins?|minutes?|s|secs?|seconds?)\s*(?:,\s*|\s+and\s+|\s+)?)+)/i.exec(
      text,
    );
  if (!m) return null;
  const body = m[1];
  const tokenRe = /(\d+)\s*(d|days?|h|hrs?|hours?|m|mins?|minutes?|s|secs?|seconds?)\b/gi;
  let totalSec = 0;
  let matched = false;
  let tok: RegExpExecArray | null;
  while ((tok = tokenRe.exec(body)) !== null) {
    matched = true;
    const value = Number(tok[1]);
    if (!Number.isFinite(value)) continue;
    const unit = tok[2].toLowerCase()[0];
    if (unit === 'd') totalSec += value * 86400;
    else if (unit === 'h') totalSec += value * 3600;
    else if (unit === 'm') totalSec += value * 60;
    else if (unit === 's') totalSec += value;
  }
  return matched && totalSec > 0 ? totalSec * 1000 : null;
}

function detectUsageWindow(text: string): {
  kind: UsageLimitWindowKind | null;
  label: string | null;
} {
  const withoutReset = text.replace(
    /resets?\s+in\s+(?:\d+\s*(?:d|days?|h|hrs?|hours?|m|mins?|minutes?|s|secs?|seconds?)\s*(?:,\s*|\s+and\s+|\s+)?)+/gi,
    '',
  );
  if (/\b(?:5[-\s]?hours?|5h|rolling)\b/i.test(withoutReset)) {
    return { kind: '5-hour', label: '5-hour' };
  }
  if (/\bweekly\b|\bweek\b/i.test(withoutReset)) {
    return { kind: 'weekly', label: 'weekly' };
  }
  if (/\bmonthly\b|\bmonth\b/i.test(withoutReset)) {
    return { kind: 'monthly', label: 'monthly' };
  }
  if (/\bdaily\b|\bday\b/i.test(withoutReset)) {
    return { kind: 'daily', label: 'daily' };
  }
  return { kind: null, label: null };
}

/**
 * Parses a provider usage-wall error (such as OpenCode Go's 429
 * `GoUsageLimitError` for 5-hour, weekly, or monthly limits), even when URL-
 * encoded or wrapped in an `error: 429: {...}` JSON envelope.
 */
export function parseUsageLimitError(
  raw: string,
  baseTimeMs = Date.now(),
): UsageLimitErrorInfo | null {
  const decoded = decodePercentText((raw ?? '').trim());
  if (!decoded) return null;

  let errorType: string | undefined;
  let cleanMessage = decoded;
  let jsonResetMs: number | null = null;

  const braceStart = decoded.indexOf('{');
  const braceEnd = decoded.lastIndexOf('}');
  if (braceStart >= 0 && braceEnd > braceStart) {
    try {
      const parsed = JSON.parse(decoded.slice(braceStart, braceEnd + 1)) as {
        type?: unknown;
        message?: unknown;
        resetSec?: unknown;
        resetInSeconds?: unknown;
        resetsAt?: unknown;
        error?: {
          type?: unknown;
          code?: unknown;
          message?: unknown;
          resetSec?: unknown;
          resetInSeconds?: unknown;
          resetsAt?: unknown;
        };
      };
      const inner = parsed.error && typeof parsed.error === 'object' ? parsed.error : undefined;
      const t =
        typeof parsed.type === 'string'
          ? parsed.type
          : typeof inner?.type === 'string'
            ? inner.type
            : typeof inner?.code === 'string'
              ? inner.code
              : undefined;
      const msg =
        typeof parsed.message === 'string'
          ? parsed.message
          : typeof inner?.message === 'string'
            ? inner.message
            : undefined;
      if (t) errorType = t.trim();
      if (msg && msg.trim()) cleanMessage = decodePercentText(msg.trim());
      const sec =
        typeof parsed.resetSec === 'number'
          ? parsed.resetSec
          : typeof parsed.resetInSeconds === 'number'
            ? parsed.resetInSeconds
            : typeof inner?.resetSec === 'number'
              ? inner.resetSec
              : typeof inner?.resetInSeconds === 'number'
                ? inner.resetInSeconds
                : null;
      if (sec !== null && Number.isFinite(sec) && sec > 0) {
        jsonResetMs = Math.round(sec * 1000);
      } else {
        const resetsAt = parsed.resetsAt ?? inner?.resetsAt;
        if (typeof resetsAt === 'string' || typeof resetsAt === 'number') {
          const ts = new Date(resetsAt).getTime();
          if (Number.isFinite(ts) && ts > baseTimeMs) {
            jsonResetMs = ts - baseTimeMs;
          }
        }
      }
    } catch {
      // Not valid JSON — inspect the decoded text directly.
    }
  }

  const combined = `${errorType ?? ''} ${cleanMessage} ${decoded}`;
  const resetMs = jsonResetMs ?? parseResetDurationMs(cleanMessage) ?? parseResetDurationMs(decoded);
  const windowInfo = detectUsageWindow(cleanMessage).kind
    ? detectUsageWindow(cleanMessage)
    : detectUsageWindow(decoded);

  const isUsageLimit =
    (errorType ? /usage.?limit|quota|rate.?limit/i.test(errorType) : false) ||
    /usage\s+limit\s+(?:reached|exceeded|hit)/i.test(combined) ||
    (/\b(?:5[-\s]?hour|weekly|monthly|daily|rolling)\b/i.test(combined) &&
      (/limit|quota|429/i.test(combined) || resetMs !== null)) ||
    (/429/.test(combined) && resetMs !== null);

  if (!isUsageLimit) return null;

  return {
    cleanMessage,
    errorType,
    windowKind: windowInfo.kind,
    windowLabel: windowInfo.label,
    resetMs,
    resetAt: resetMs !== null ? baseTimeMs + resetMs : null,
  };
}

export function formatUsageResetCountdown(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return 'now';
  const totalSec = Math.max(1, Math.ceil(ms / 1000));
  const days = Math.floor(totalSec / 86400);
  const hours = Math.floor((totalSec % 86400) / 3600);
  const minutes = Math.floor((totalSec % 3600) / 60);
  const seconds = totalSec % 60;
  if (days > 0) {
    return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
  }
  if (hours > 0) {
    return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`;
  }
  if (minutes > 0) {
    return seconds > 0 && minutes < 10 ? `${minutes}m ${seconds}s` : `${minutes}m`;
  }
  return `${seconds}s`;
}
