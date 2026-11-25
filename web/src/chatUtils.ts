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
    it: { kind: string; role?: string; content?: string };
    d?: { calls: unknown[]; results: unknown[]; ask?: unknown } | null;
  }>,
): number {
  if (entries.length === 0) return -1;
  const lastIdx = entries.length - 1;
  const last = entries[lastIdx];
  if (last.it.kind !== 'msg' || last.it.role !== 'assistant' || !last.it.content?.trim()) {
    return -1;
  }
  if (last.d && (last.d.calls.length > 0 || last.d.results.length > 0 || last.d.ask != null)) {
    return -1;
  }
  return lastIdx;
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
