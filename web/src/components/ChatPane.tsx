import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { useNavigate } from 'react-router-dom';
import {
  api,
  messageAttachmentUrl,
  retryChat,
  streamChat,
  watchChat,
  THINKING_META_KEY,
  type ChatAttachmentInput,
} from '../api';
import type { ContextUsage } from '../types';
import type {
  ChatAttachmentMeta,
  ChatEvent,
  ChatSession,
  ChatUsage,
  Memory,
  PermissionMode,
  Provider,
  ProviderModel,
  SavedProvider,
  Todo,
} from '../types';
import { errMsg, diffLines, getDebugHud, getJsonPretty, getThinkingCollapsed, getToolCallsCollapsed } from '../utils';
import { notifyTurnDone, notifyTurnError } from '../notify';
import { permissionMeta } from '../permissions';

// sessionStorageKey is the localStorage key remembering the last-used chat
// session per project, so leaving a project and coming back reopens the same
// thread instead of resetting to the default session.
function sessionStorageKey(projectId: string): string {
  return `v1.session.${projectId}`;
}
import { Button, Dialog, ErrorBox, IconButton, Input, Spinner } from './ui';
import ToolSettings, { type ToolsTab } from './ToolSettings';
import Markdown from './Markdown';
import ModelPicker from './ModelPicker';
import SessionsModal from './SessionsModal';
import TrackBorder, { TRACK_DEFAULTS } from './TrackBorder';
import {
  IconArrowUp,
  IconBookmark,
  IconBookmarkOff,
  IconBrain,
  IconCamera,
  IconCheck,
  IconCheckSquare,
  IconChevronDown,
  IconLayers,
  IconChevronRight,
  IconChevronUp,
  IconCode,
  IconCompress,
  IconDownload,
  IconExpand,
  IconFile,
  IconGlobe,
  IconList,
  IconLock,
  IconMap,
  IconModel,
  IconMoveRight,
  IconPaperclip,
  IconPencil,
  IconPlus,
  IconRefresh,
  IconSearch,
  IconSend,
  IconSquare,
  IconTerminal,
  IconTrash,
  IconUser,
  IconWrench,
  IconX,
} from './icons';
import { useMediaQuery } from '../hooks/useMediaQuery';
import hljs from 'highlight.js/lib/core';
import json from 'highlight.js/lib/languages/json';

// Registered here (in addition to markdown.ts) so tool argument/result JSON
// gets the same token colors as fenced code blocks.
hljs.registerLanguage('json', json);

type ToolCall = { name: string; detail: string };

// Autocomplete item for the composer's /, @ and # menus. `insert` is the
// token written into the textarea; `label`/`hint` are display-only.
type Suggestion = { insert: string; label: string; hint?: string };

const CHAT_COMMANDS = [
  { name: '/plan', hint: 'Investigate and plan (read-only, no changes)' },
  { name: '/compact', hint: 'Summarize history to free up context' },
  { name: '/clear', hint: 'Clear the chat history' },
  { name: '/model', hint: 'Choose a model' },
  { name: '/preview', hint: 'Restart the preview' },
  { name: '/tools', hint: 'Open tools, skills & permissions' },
  { name: '/stop', hint: 'Stop the current run' },
  { name: '/help', hint: 'Show available commands' },
] as const;

// Thinking level → button colors: escalation from cool to hot. Unknown
// levels fall back to the accent color.
const THINKING_LEVEL_COLORS: Record<string, { text: string; border: string }> = {
  off: { text: 'text-dim', border: 'border-border' },
  on: { text: 'text-accent', border: 'border-accent/60' },
  none: { text: 'text-dim', border: 'border-border' },
  minimal: { text: 'text-emerald-500', border: 'border-emerald-500/50' },
  low: { text: 'text-emerald-500', border: 'border-emerald-500/50' },
  medium: { text: 'text-amber-500', border: 'border-amber-500/50' },
  high: { text: 'text-orange-500', border: 'border-orange-500/50' },
  xhigh: { text: 'text-red-500', border: 'border-red-500/50' },
  max: { text: 'text-red-500', border: 'border-red-500/50' },
};

// Canonical escalation order for thinking levels; unknown levels order by
// their position in the model's list instead.
const THINKING_LEVEL_RANK: Record<string, number> = {
  off: 0,
  none: 1,
  minimal: 2,
  low: 3,
  medium: 4,
  high: 5,
  xhigh: 6,
  max: 7,
  on: 8,
};

// Human-readable labels for agent tool names shown in the chat; unknown
// tools (e.g. mcp_*) keep their code name.
const TOOL_LABELS: Record<string, string> = {
  list_files: 'List files',
  search_files: 'Search files',
  read_file: 'Read file',
  write_file: 'Write file',
  edit_file: 'Edit file',
  delete_file: 'Delete file',
  move_file: 'Move file',
  fetch_url: 'Fetch URL',
  run_command: 'Run command',
  run_command_background: 'Run in background',
  restart_preview: 'Restart preview',
  screenshot_app: 'Screenshot app',
  set_todos: 'Update todos',
  remember: 'Remember',
  forget: 'Forget',
  ask_user: 'Ask user',
};

function toolLabel(name: string): string {
  return TOOL_LABELS[name] ?? name;
}

// One icon per agent tool shown in the chat; unknown tools (e.g. mcp_*)
// keep the generic wrench.
const TOOL_ICONS: Record<string, typeof IconWrench> = {
  list_files: IconList,
  search_files: IconSearch,
  read_file: IconFile,
  write_file: IconPencil,
  edit_file: IconCode,
  delete_file: IconTrash,
  move_file: IconMoveRight,
  fetch_url: IconGlobe,
  run_command: IconTerminal,
  run_command_background: IconTerminal,
  restart_preview: IconRefresh,
  screenshot_app: IconCamera,
  set_todos: IconCheckSquare,
  remember: IconBookmark,
  forget: IconBookmarkOff,
  ask_user: IconUser,
};

// The most recent finished assistant reply or persisted error in a loaded
// message list, if any. Used after load() to notify for runs that finished
// while the app was away (the SSE event was lost, so the notification can
// only be discovered here).
function findLastFinishedAssistant(items: Item[]): { key: string; content: string; error: boolean } | null {
  for (let i = items.length - 1; i >= 0; i--) {
    const it = items[i];
    if (it.kind === 'msg' && it.role === 'assistant' && !it.streaming) {
      return { key: it.key, content: it.content, error: false };
    }
    if (it.kind === 'msg' && it.role === 'error') {
      return { key: it.key, content: it.content, error: true };
    }
  }
  return null;
}

// True when the last user turn already has a finished assistant reply — the
// run completed (typically while the app was away), so there is nothing to
// resume. Tool rows don't count: an aborted run leaves only those.
async function turnCompleted(projectId: string, sessionId: string): Promise<boolean> {
  try {
    const msgs = await api.getMessages(projectId, sessionId);
    let lastUser = -1;
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i].role === 'user') {
        lastUser = i;
        break;
      }
    }
    if (lastUser === -1) return true; // nothing to resume
    for (let i = lastUser + 1; i < msgs.length; i++) {
      // An assistant reply OR a persisted error means the turn ended.
      if (msgs[i].role === 'assistant' || msgs[i].role === 'error') return true;
    }
    return false;
  } catch {
    return false; // can't tell — assume it needs resuming
  }
}

// Formats a duration as "42s" or "1m 23s".
function formatElapsed(ms: number): string {
  const s = Math.max(1, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}m ${s % 60}s`;
}

// Formats a queue wait estimate in seconds, e.g. "45s" or "2m".
function fmtQueueWait(seconds: number): string {
  if (seconds < 60) return `${Math.max(1, Math.round(seconds))}s`;
  return `${Math.ceil(seconds / 60)}m`;
}

// Formats a timestamp as a short local time, e.g. "10:42 AM".
function formatTime(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
}

// Formats a provider-supplied cost in the user's currency, e.g. "$0.0123"
// or "€0,01". The ISO 4217 currency code is rendered as its symbol when we
// recognize it, otherwise as a code suffix ("12.3 GBP").
function formatCost(value: number, currency: string): string {
  const sym: Record<string, string> = {
    USD: '$', EUR: '€', GBP: '£', JPY: '¥', CAD: 'CA$', AUD: 'A$', INR: '₹',
    CHF: 'CHF ', CNY: '¥', KRW: '₩', SEK: 'kr ', DKK: 'kr ',
  };
  const symOrCode = sym[currency] ?? `${currency} `;
  const locale = currency === 'EUR' ? 'de-DE' : 'en-US';
  const amount = value.toLocaleString(locale, { minimumFractionDigits: 2, maximumFractionDigits: 6 });
  return `${symOrCode}${amount}`;
}

// Persisted thinking-metadata cache (localStorage): provider|model → levels
// and off support, so reopening a project skips the /models round trip for
// models already seen. Stale entries are pruned on write.
const THINKING_META_TTL = 24 * 60 * 60 * 1000;

function readThinkingMeta(key: string): { levels: string[]; off: boolean } | null {
  try {
    const raw = localStorage.getItem(THINKING_META_KEY);
    if (!raw) return null;
    const map = JSON.parse(raw) as Record<string, { levels: string[]; off: boolean; at: number }>;
    const e = map[key];
    if (!e || Date.now() - e.at > THINKING_META_TTL) return null;
    return { levels: e.levels, off: e.off };
  } catch {
    return null;
  }
}

function writeThinkingMeta(key: string, meta: { levels: string[]; off: boolean }): void {
  try {
    const raw = localStorage.getItem(THINKING_META_KEY);
    const map = raw
      ? (JSON.parse(raw) as Record<string, { levels: string[]; off: boolean; at: number }>)
      : {};
    const now = Date.now();
    map[key] = { ...meta, at: now };
    for (const k of Object.keys(map)) {
      if (now - map[k].at > THINKING_META_TTL) delete map[k];
    }
    localStorage.setItem(THINKING_META_KEY, JSON.stringify(map));
  } catch {
    // ignore (private mode etc.)
  }
}

// A ring that fills with the context usage, shifting continuously from green
// to red as the context fills (no track — just the fill arc, like the other
// header icons). On mount the arc sweeps in from empty; value changes
// (e.g. model switches) transition smoothly instead of jumping.
function ContextRing({ ctx }: { ctx: ContextUsage | null }) {
  const used = ctx?.used ?? 0;
  const budget = ctx?.budget ?? 1;
  const pct = Math.min(100, (used / budget) * 100);
  const hue = 120 - (120 * pct) / 100; // 120 (green) → 0 (red)
  const R = 7.5;
  const C = 2 * Math.PI * R;
  // Start empty, then let the CSS transition sweep to the real value.
  const [shown, setShown] = useState(false);
  useEffect(() => {
    const raf = requestAnimationFrame(() => setShown(true));
    return () => cancelAnimationFrame(raf);
  }, []);
  return (
    <svg width="18" height="18" viewBox="0 0 18 18" className="-rotate-90">
      {/* Faint track so a very low fill still reads as a ring. */}
      <circle cx="9" cy="9" r={R} fill="none" strokeWidth="2.5" className="stroke-border" />
      <circle
        cx="9"
        cy="9"
        r={R}
        fill="none"
        strokeWidth="2.5"
        strokeLinecap="round"
        style={{
          stroke: `hsl(${hue} 70% 50%)`,
          strokeDasharray: C,
          strokeDashoffset: shown ? C * (1 - pct / 100) : C,
          transition: 'stroke-dashoffset 700ms ease-out, stroke 700ms ease',
        }}
      />
    </svg>
  );
}

// A network-level stream failure (dead connection — backgrounded tab, app
// suspended on iOS) surfaces as a bare "Failed to fetch", which tells the
// user nothing. Say what actually happened instead. Auto-reconnect handles
// the usual case; this text is the fallback when it gives up.
function streamErrorMsg(e: unknown): string {
  if (e instanceof TypeError) {
    return 'Connection lost — couldn\'t resume the generation. Reload the page to see where it got to.';
  }
  return errMsg(e);
}

function toolIcon(name: string): typeof IconWrench {
  return TOOL_ICONS[name] ?? IconWrench;
}

// Renders user message text with @file / #skill tags pill-highlighted. Tag
// characters are limited to path-ish ones so trailing punctuation stays out.
// When `valid` is given, only tags that resolve to a real file or enabled
// skill get the pill; the rest render as plain text.
// pillClassName overrides the pill style; the composer echo passes a
// width-neutral variant (negative margins cancel the padding) so the visible
// text keeps the exact metrics of the transparent textarea text above it.
function TaggedText({
  text,
  pillClassName,
  valid,
}: {
  text: string;
  pillClassName?: string;
  valid?: (tag: string) => boolean;
}) {
  const out: ReactNode[] = [];
  const re = /(^|\s)([@#][A-Za-z0-9_\-./]+)/g;
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    if (valid && !valid(m[2])) continue; // unknown file/skill — leave as plain text
    out.push(text.slice(last, m.index), m[1]);
    out.push(
      <span
        key={m.index}
        className={
          pillClassName ?? 'rounded bg-bg/70 px-1 py-px font-mono text-[0.85em] text-accent'
        }
      >
        {m[2]}
      </span>,
    );
    last = m.index + m[0].length;
  }
  out.push(text.slice(last));
  return <>{out}</>;
}

// AttachmentView adds a display URL to the stored metadata: a data URL for
// just-sent images, or the attachment endpoint URL for persisted ones.
type AttachmentView = ChatAttachmentMeta & { url?: string };

type MsgItem = {
  kind: 'msg';
  key: string;
  role: 'user' | 'assistant' | 'error';
  content: string;
  attachments?: AttachmentView[];
  reasoning?: string;
  toolCalls?: ToolCall[];
  toolResults?: ToolCall[];
  /** True for a finished background command's result row (not the user). */
  background?: boolean;
  /** Token usage for the turn this message closed (turn-final messages only). */
  usage?: ChatUsage;
  /** Duration of the turn this message closed, in milliseconds. */
  elapsedMs?: number;
  /** When the message was sent (ms epoch; live items use the send time). */
  sentAt?: number;
  streaming?: boolean;
  /** True when this user row landed via an explicit steer (visual badge). */
  steered?: boolean;
  reasoningCollapsed?: boolean;
  stale?: boolean;
  editing?: boolean;
};

type ToolItem = {
  kind: 'tool';
  key: string;
  name: string;
  detail: string;
  running: boolean;
  ok?: boolean;
};

/** The agent's ask_user question(s), shown inline like other harnesses. */
type AskItem = {
  kind: 'ask';
  key: string;
  questions: AskQuestionView[];
  result?: AskAnswerView[];
  failed?: boolean;
};

type Item = MsgItem | ToolItem | AskItem;

/** The API serves tool_json as raw JSON (an object), but older callers pass a
 * string — accept both. */
function asToolJSON(tool: unknown): unknown {
  if (typeof tool === 'string') {
    try {
      return JSON.parse(tool);
    } catch {
      return null;
    }
  }
  return tool ?? null;
}

/** Parses an assistant message's tool_json: {"tool_calls":[{function:{name,arguments}}]}. */
function parseToolCalls(tool: unknown): ToolCall[] | null {
  const data = asToolJSON(tool) as {
    tool_calls?: { function?: { name?: string; arguments?: string } }[];
  } | null;
  if (!data || !Array.isArray(data.tool_calls)) return null;
  return data.tool_calls.map((c) => ({
    name: c?.function?.name ?? 'tool',
    detail: c?.function?.arguments ?? '',
  }));
}

/** Parses a role "tool" message's tool_json: {"name": "..."}. */
function parseToolName(tool: unknown): string {
  const data = asToolJSON(tool) as { name?: string } | null;
  return data && typeof data.name === 'string' && data.name ? data.name : 'tool';
}

/** True when a message key is a persisted row id (not a live/synthetic key). */
function persisted(key: string): boolean {
  return Number.isFinite(Number(key));
}

const MAX_ATTACHMENTS = 6;
const MAX_ATTACHMENT_BYTES = 2 << 20;

// readAttachment classifies a picked file: images are read as base64 (sent to
// vision-capable models), everything else is read as text (code, config, logs).
function readAttachment(file: File): Promise<{ value: ChatAttachmentInput } | { error: string }> {
  if (file.size > MAX_ATTACHMENT_BYTES) {
    return Promise.resolve({ error: `${file.name} is too large (max 2 MB).` });
  }
  if (file.type.startsWith('image/')) {
    return new Promise((resolve) => {
      const reader = new FileReader();
      reader.onload = () => {
        const dataUrl = String(reader.result ?? '');
        resolve({
          value: {
            name: file.name,
            mime: file.type || 'image/png',
            kind: 'image',
            content: dataUrl.slice(dataUrl.indexOf(',') + 1),
          },
        });
      };
      reader.onerror = () => resolve({ error: `Could not read ${file.name}.` });
      reader.readAsDataURL(file);
    });
  }
  return new Promise((resolve) => {
    const reader = new FileReader();
    reader.onload = () => {
      const text = String(reader.result ?? '');
      if (text.includes('\u0000')) {
        resolve({ error: `${file.name} is a binary file — only images and text files can be attached.` });
        return;
      }
      resolve({
        value: { name: file.name, mime: file.type || 'text/plain', kind: 'text', content: text },
      });
    };
    reader.onerror = () => resolve({ error: `Could not read ${file.name}.` });
    reader.readAsText(file);
  });
}

function ToolRow({ item }: { item: ToolItem }) {
  const [open, setOpen] = useState(false);
  const Icon = toolIcon(item.name);
  return (
    <div className="rounded-md border border-border/80 bg-surface/50 text-[10px]">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[26px] w-full items-center gap-1.5 px-2 py-1 text-left text-dim transition-colors hover:text-text"
      >
        {open ? (
          <IconChevronDown className="h-3.5 w-3.5 shrink-0" />
        ) : (
          <IconChevronRight className="h-3.5 w-3.5 shrink-0" />
        )}
        <Icon className="h-3 w-3 shrink-0 text-faint" />
        <span className="shrink-0 font-mono text-text">{toolLabel(item.name)}</span>
        {item.detail && <span className="min-w-0 flex-1 truncate text-faint">{item.detail}</span>}
        {item.running ? (
          <Spinner className="h-3.5 w-3.5 shrink-0" />
        ) : item.ok ? (
          <IconCheck className="h-3.5 w-3.5 shrink-0 text-emerald-500" />
        ) : (
          <IconX className="h-3.5 w-3.5 shrink-0 text-red-500" />
        )}
      </button>
      {open && item.detail && <ToolBody detail={item.detail} />}
    </div>
  );
}

// Pins a scroll container to its bottom while its content grows — but only
// while the user is already at (or near) the bottom. Scrolling up releases
// the pin; returning to the bottom re-pins it.
function StickToBottom({
  children,
  className,
  initialStuck = false,
}: {
  children: ReactNode;
  className?: string;
  /** Start pinned (streaming content); otherwise the view starts at the top. */
  initialStuck?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [stuck, setStuck] = useState(initialStuck);
  useEffect(() => {
    const el = ref.current;
    if (el && stuck) el.scrollTop = el.scrollHeight;
  });
  const onScroll = () => {
    const el = ref.current;
    if (!el) return;
    setStuck(el.scrollHeight - el.scrollTop - el.clientHeight < 8);
  };
  return (
    <div ref={ref} onScroll={onScroll} className={className}>
      {children}
    </div>
  );
}

function ReasoningBlock({ text, autoOpen, skipAutoCollapse = false }: { text: string; autoOpen: boolean; skipAutoCollapse?: boolean }) {
  // When the "collapse thinking by default" setting is on, blocks start
  // closed — even while streaming — until the user opens one. Otherwise a
  // block auto-collapses as soon as ANY block appears after it (another
  // thinking block, the final text, or a tool call).
  const collapseDefault = getThinkingCollapsed();
  const [open, setOpen] = useState(autoOpen && !collapseDefault && !skipAutoCollapse);
  useEffect(() => {
    setOpen(autoOpen && !collapseDefault && !skipAutoCollapse);
  }, [autoOpen, collapseDefault, skipAutoCollapse]);
  return (
    <div className="rounded-md border border-accent/25 bg-surface/50 text-[10px]">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[26px] w-full items-center gap-1.5 px-2 py-1 text-left text-dim transition-colors hover:text-text"
      >
        {open ? (
          <IconChevronDown className="h-3 w-3 shrink-0" />
        ) : (
          <IconChevronRight className="h-3 w-3 shrink-0" />
        )}
        <IconBrain className="h-3 w-3 shrink-0 text-accent" />
        <span className="font-mono text-text">Thinking</span>
      </button>
      {open && (
        <StickToBottom
          initialStuck={autoOpen}
          className="max-h-64 overflow-auto whitespace-pre-wrap break-words border-t border-accent/20 px-3 py-2 font-mono text-[11px] leading-relaxed text-subtle"
        >
          {text}
        </StickToBottom>
      )}
    </div>
  );
}

// Splits an edit's old/new strings into shared context lines and the changed
// middle section, for the chat's edit diff view. (Shared: web/src/utils.ts)
function parseEditDiff(detail: string) {
  try {
    const a = JSON.parse(detail) as { path?: string; old_string?: string; new_string?: string };
    if (typeof a.old_string !== 'string' || typeof a.new_string !== 'string') return null;
    return { path: a.path ?? '', ...diffLines(a.old_string, a.new_string) };
  } catch {
    return null;
  }
}

function DiffContext({ lines, side }: { lines: string[]; side: 'before' | 'after' }) {
  // Keep the 2 context lines closest to the change, with a fold marker.
  const truncated = lines.length > 2;
  const shown = side === 'before' ? lines.slice(-2) : lines.slice(0, 2);
  return (
    <>
      {side === 'before' && truncated && <div className="text-faint">⋮</div>}
      {shown.map((l, i) => (
        <div key={i} className="whitespace-pre-wrap break-words text-faint">
          {'  '}
          {l}
        </div>
      ))}
      {side === 'after' && truncated && <div className="text-faint">⋮</div>}
    </>
  );
}

// True when a tool detail carries information: raw "{}" or argument objects
// with only empty values are noise (e.g. list_files called with no path).
function meaningfulDetail(detail: string): boolean {
  const t = detail.trim();
  if (!t || t === '{}') return false;
  try {
    const a = JSON.parse(t) as unknown;
    if (a && typeof a === 'object' && !Array.isArray(a)) {
      return Object.values(a as Record<string, unknown>).some(
        (x) => x !== '' && x !== undefined && x !== null,
      );
    }
    return true;
  } catch {
    return true;
  }
}

// The label shown on a plain tool chip: the meaningful arg (command, path)
// rather than the raw JSON arguments.
function chipLabel(detail: string): string {
  try {
    const a = JSON.parse(detail) as Record<string, unknown>;
    const v = a.command ?? a.path ?? a.query ?? a.url;
    if (typeof v === 'string') return v;
    if (Array.isArray(a.todos)) {
      return `${a.todos.length} todo${a.todos.length === 1 ? '' : 's'}`;
    }
    return meaningfulDetail(detail) ? detail : '';
  } catch {
    return meaningfulDetail(detail) ? detail : '';
  }
}

function ToolChip({ name, detail }: ToolCall) {
  const [open, setOpen] = useState(false);
  const Icon = toolIcon(name);
  const diff = name === 'edit_file' ? parseEditDiff(detail) : null;
  // write_file expands to just the file content — the path is already in the
  // header, and the JSON envelope around the content is noise.
  const writeContent = useMemo(() => {
    if (name !== 'write_file') return null;
    try {
      const a = JSON.parse(detail) as { content?: unknown };
      return typeof a.content === 'string' ? a.content : null;
    } catch {
      return null;
    }
  }, [name, detail]);
  const label = diff ? diff.path : chipLabel(detail);
  // Nothing worth expanding (e.g. restart_preview with empty args) renders as
  // a static row — no chevron, no dropdown.
  const expandable = diff !== null || writeContent !== null || meaningfulDetail(detail);
  const header = (
    <>
      {expandable ? (
        open ? (
          <IconChevronDown className="h-3 w-3 shrink-0" />
        ) : (
          <IconChevronRight className="h-3 w-3 shrink-0" />
        )
      ) : (
        <span className="w-3 shrink-0" />
      )}
      <Icon className="h-3 w-3 shrink-0 text-faint" />
      <span className="shrink-0 text-text">{toolLabel(name)}</span>
      {label && <span className="min-w-0 flex-1 truncate text-faint">{label}</span>}
      {diff && (
        <span className="ml-auto flex shrink-0 items-center gap-1 font-mono">
          <span className="rounded bg-red-500/10 px-1 py-0.5 text-red-400">
            -{diff.removed.length}
          </span>
          <span className="rounded bg-emerald-500/10 px-1 py-0.5 text-emerald-400">
            +{diff.added.length}
          </span>
        </span>
      )}
    </>
  );
  if (!expandable) {
    return (
      <div className="w-full overflow-hidden rounded-md border border-border bg-surface/50 font-mono text-[10px]">
        <div className="flex min-h-[26px] w-full items-center gap-1.5 px-2 py-1 text-left text-dim">
          {header}
        </div>
      </div>
    );
  }
  return (
    <div className="w-full overflow-hidden rounded-md border border-border bg-surface/50 font-mono text-[10px]">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[26px] w-full items-center gap-1.5 px-2 py-1 text-left text-dim transition-colors hover:text-text"
      >
        {header}
      </button>
      {open &&
        (diff ? (
          <StickToBottom className="max-h-60 overflow-auto border-t border-border/80 px-2 py-1.5 leading-relaxed">
            <DiffContext lines={diff.before} side="before" />
            {diff.removed.map((l, i) => (
              <div key={i} className="whitespace-pre-wrap break-words bg-red-500/10 text-red-400">
                - {l}
              </div>
            ))}
            {diff.added.map((l, i) => (
              <div key={i} className="whitespace-pre-wrap break-words bg-emerald-500/10 text-emerald-400">
                + {l}
              </div>
            ))}
            <DiffContext lines={diff.after} side="after" />
          </StickToBottom>
        ) : writeContent !== null ? (
          <StickToBottom className="max-h-60 overflow-auto whitespace-pre-wrap break-words border-t border-border/80 px-3 py-2 font-mono text-[11px] leading-relaxed text-subtle">
            {writeContent}
          </StickToBottom>
        ) : (
          meaningfulDetail(detail) && <ToolBody detail={detail} />
        ))}
    </div>
  );
}

// Parses a tool result's JSON exitCode, or null when the result is not the
// run_command shape (an error string or another tool's output).
function runExitCode(detail: string): number | null {
  try {
    const d = JSON.parse(detail) as { exitCode?: number };
    return typeof d.exitCode === 'number' ? d.exitCode : null;
  } catch {
    return null;
  }
}

// Tool call/result body: JSON is pretty-printed by default (Raw toggle) and
// syntax-highlighted; anything else renders as-is. The global default comes
// from Settings → Appearance → Tool calls.
function ToolBody({ detail }: { detail: string }) {
  const [pretty, setPretty] = useState(() => getJsonPretty());
  const parsed = useMemo(() => {
    try {
      return JSON.parse(detail) as unknown;
    } catch {
      return null;
    }
  }, [detail]);
  const shown = parsed !== null && pretty ? JSON.stringify(parsed, null, 2) : detail;
  const html = parsed !== null ? hljs.highlight(shown, { language: 'json' }).value : null;
  return (
    <div className="relative border-t border-border/80">
      {parsed !== null && (
        <button
          type="button"
          onClick={() => setPretty((p) => !p)}
          title={pretty ? 'Show the raw JSON' : 'Pretty-print the JSON'}
          className="absolute right-2 top-2 z-10 rounded-md border border-border bg-surface px-1.5 py-0.5 font-sans text-[10px] text-dim transition-colors hover:text-text"
        >
          {pretty ? 'Raw' : 'Pretty'}
        </button>
      )}
      {html !== null ? (
        <StickToBottom className="max-h-60 overflow-auto whitespace-pre-wrap break-words px-3 py-2 font-mono text-[11px] leading-relaxed text-subtle">
          <div dangerouslySetInnerHTML={{ __html: html }} />
        </StickToBottom>
      ) : (
        <StickToBottom className="max-h-60 overflow-auto whitespace-pre-wrap break-words px-3 py-2 font-mono text-[11px] leading-relaxed text-subtle">
          {detail}
        </StickToBottom>
      )}
    </div>
  );
}

// A run_command call and its result merged into one card: the header carries
// the command with a plain ✓/✗ status; expanding shows a code block with the
// command line followed by the raw output — not the JSON envelope.
function RunCommandBlock({ command, result }: { command: string; result: ToolCall }) {
  const [open, setOpen] = useState(false);
  const exitCode = runExitCode(result.detail);
  return (
    <div className="w-full overflow-hidden rounded-md border border-border bg-surface/50 font-mono text-[10px]">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[26px] w-full items-center gap-1.5 px-2 py-1 text-left text-dim transition-colors hover:text-text"
      >
        {open ? (
          <IconChevronDown className="h-3 w-3 shrink-0" />
        ) : (
          <IconChevronRight className="h-3 w-3 shrink-0" />
        )}
        <IconTerminal className="h-3 w-3 shrink-0 text-faint" />
        <span className="shrink-0 text-text">{toolLabel('run_command')}</span>
        <span className="min-w-0 flex-1 truncate text-faint">{command}</span>
        {exitCode === 0 && <IconCheck className="h-3 w-3 shrink-0 text-emerald-500" />}
        {exitCode !== null && exitCode !== 0 && <IconX className="h-3 w-3 shrink-0 text-red-500" />}
      </button>
      {open && <RunCommandOutput command={command} detail={result.detail} />}
    </div>
  );
}

// The expanded run_command body: `$ <command>` followed by the raw output.
// Falls back to the raw result when it isn't the {exitCode, output} shape
// (e.g. an error string).
function RunCommandOutput({ command, detail }: { command: string; detail: string }) {
  const output = useMemo(() => {
    try {
      const d = JSON.parse(detail) as { output?: unknown };
      return typeof d.output === 'string' ? d.output : null;
    } catch {
      return null;
    }
  }, [detail]);
  return (
    <StickToBottom className="max-h-60 overflow-auto whitespace-pre overflow-x-auto break-normal border-t border-border/80 px-3 py-2 font-mono text-[11px] leading-relaxed">
      <span className="text-accent">$ {command}</span>
      {'\n'}
      {output === null ? (
        <span className="text-subtle">{detail}</span>
      ) : output === '' ? (
        <span className="text-faint">(no output)</span>
      ) : (
        <span className="text-subtle whitespace-pre">{output}</span>
      )}
    </StickToBottom>
  );
}

// A search_files call and its result merged into one card: the header shows
// the query, expanding shows the search results — one entry instead of a
// call chip plus a separate "result" block.
function SearchFilesBlock({ query, result }: { query: string; result: ToolCall }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="overflow-hidden rounded-md border border-border bg-surface/50 text-[10px]">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[26px] w-full items-center gap-1.5 px-2 py-1 text-left text-dim transition-colors hover:text-text"
      >
        {open ? (
          <IconChevronDown className="h-3 w-3 shrink-0" />
        ) : (
          <IconChevronRight className="h-3 w-3 shrink-0" />
        )}
        <IconSearch className="h-3 w-3 shrink-0 text-faint" />
        <span className="shrink-0 text-text">{toolLabel('search_files')}</span>
        <span className="min-w-0 flex-1 truncate text-faint">{query}</span>
      </button>
      {open && (
        <div className="border-t border-border/80">
          <ToolBody detail={result.detail} />
        </div>
      )}
    </div>
  );
}

// Renders an assistant message's tool calls and results as one column, pairing
// each run_command call with its result (both lists are in chronological
// order) so they show as a single block.
// One-line summary of an assistant message's tool activity, e.g.
// "Read 2 files · Made 1 edit · Ran 1 command". Reads and listings are
// skipped — they are context, not actions.
function summarizeTools(calls: ToolCall[], results: ToolCall[]): string {
  // Each tool appears as a call and again as its result — count each tool
  // once, using the larger of the two lists when a call is still in flight
  // or a result arrived without its call.
  const counts = new Map<string, number>();
  const tally = (list: ToolCall[]) => {
    const per = new Map<string, number>();
    for (const c of list) per.set(c.name, (per.get(c.name) ?? 0) + 1);
    for (const [k, v] of per) counts.set(k, Math.max(counts.get(k) ?? 0, v));
  };
  tally(calls);
  tally(results);
  const n = (k: string) => counts.get(k) ?? 0;
  const plural = (x: number, s: string) => `${x} ${s}${x === 1 ? '' : 's'}`;
  const parts: string[] = [];
  if (n('read_file') > 0) parts.push(`Read ${plural(n('read_file'), 'file')}`);
  const files = n('write_file') + n('edit_file') + n('delete_file') + n('move_file');
  if (files > 0) parts.push(`Made ${plural(files, 'edit')}`);
  if (n('run_command') + n('run_command_background') > 0) {
    // Just the count — naming commands makes the summary noisy.
    parts.push(`Ran ${plural(n('run_command') + n('run_command_background'), 'command')}`);
  }
  if (n('fetch_url') > 0) parts.push(`Fetched ${plural(n('fetch_url'), 'page')}`);
  if (n('screenshot_app') > 0) parts.push(`Took ${plural(n('screenshot_app'), 'screenshot')}`);
  if (n('set_todos') > 0) parts.push('Updated todos');
  if (n('restart_preview') > 0) parts.push('Restarted preview');
  const mem = n('remember') + n('forget');
  if (mem > 0) parts.push(`Updated ${plural(mem, 'memory')}`);
  const skipped = new Set([
    'write_file', 'edit_file', 'delete_file', 'move_file', 'run_command', 'read_file',
    'fetch_url', 'screenshot_app', 'set_todos', 'restart_preview', 'remember', 'forget',
    'ask_user', 'list_files', 'search_files',
  ]);
  const other = [...counts.entries()].filter(([k]) => !skipped.has(k)).reduce((a, [, v]) => a + v, 0);
  if (other > 0) parts.push(`Ran ${plural(other, 'tool')}`);
  const total = [...counts.values()].reduce((a, b) => a + b, 0);
  if (parts.length === 0) return `${total} tool call${total === 1 ? '' : 's'}`;
  return parts.join(' · ');
}

// The pieces one assistant message contributes to the tool collapse: its
// reasoning, its tool calls/results (ask_user split out), and the ask itself.
type CollapseData = {
  /** Assistant text that accompanied the tool calls (shown in group expands). */
  content?: string;
  reasoning?: string;
  calls: ToolCall[];
  results: ToolCall[];
  ask: { questions: AskQuestionView[]; result?: AskAnswerView[]; failed?: boolean } | null;
  askCount: number;
};

function collapseData(item: MsgItem): CollapseData {
  const askCall = item.toolCalls?.find((c) => c.name === 'ask_user');
  const askResult = item.toolResults?.find((r) => r.name === 'ask_user');
  const ask = askCall
    ? {
        questions: askQuestions(askCall.detail),
        result: askResult ? askAnswers(askResult.detail) : undefined,
      }
    : null;
  return {
    content: item.content,
    reasoning: item.reasoning,
    calls: (item.toolCalls ?? []).filter((c) => c.name !== 'ask_user'),
    results: (item.toolResults ?? []).filter((r) => r.name !== 'ask_user'),
    ask: ask
      ? { ...ask, failed: ask.result !== undefined && ask.result.length === 0 }
      : null,
    askCount: ask ? ask.questions.length : 0,
  };
}

// The expanded body of a collapse entry: reasoning, tool blocks, and the ask.
function CollapsedToolBlocks({
  d,
  onAskAnswered,
}: {
  d: CollapseData;
  onAskAnswered: (answers: AskAnswerView[]) => void;
}) {
  return (
    <>
      {d.reasoning && <ReasoningBlock text={d.reasoning} autoOpen={false} />}
      <ToolBlocks calls={d.calls} results={d.results} />
      {d.ask && (
        <AskBlock
          questions={d.ask.questions}
          result={d.ask.result}
          failed={d.ask.failed}
          onAnswer={onAskAnswered}
        />
      )}
    </>
  );
}

function collapseSummary(calls: ToolCall[], results: ToolCall[], askCount: number): string {
  const summary = calls.length + results.length > 0 ? summarizeTools(calls, results) : '';
  const askPart =
    askCount > 0 ? `Asked user ${askCount === 1 ? '1 question' : `${askCount} questions`}` : '';
  return [summary, askPart].filter(Boolean).join(' · ');
}

// Collapses an assistant message's reasoning and tool calls into a single
// summary line (e.g. "Made 1 edit · Ran 1 command: npm test"); expanding
// shows the individual blocks. It starts expanded while the message is still
// streaming and only collapses once everything inside has completed. Toggle
// in Settings → Appearance → Tool calls.
function CollapsedTools({
  d,
  streaming,
  onAskAnswered,
}: {
  d: CollapseData;
  streaming: boolean;
  onAskAnswered: (answers: AskAnswerView[]) => void;
}) {
  const { reasoning, calls, results, ask, askCount } = d;
  // A pending question must stay visible so it can be answered — only
  // collapse once it has answers (or failed); manual toggles are respected.
  const pendingAsk = ask != null && ask.result === undefined && !ask.failed;
  const [open, setOpen] = useState(streaming || pendingAsk);
  useEffect(() => {
    if (!streaming && !pendingAsk) setOpen(false);
  }, [streaming, pendingAsk]);
  const fullSummary = collapseSummary(calls, results, askCount);
  const hasTools = calls.length + results.length > 0 || askCount > 0;
  return (
    <div className="text-[10px]">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[26px] w-full items-center gap-1.5 px-1 py-1 text-left text-dim transition-colors hover:text-text"
      >
        {open ? (
          <IconChevronDown className="h-3 w-3 shrink-0" />
        ) : (
          <IconChevronRight className="h-3 w-3 shrink-0" />
        )}
        {ask ? (
          <IconUser className="h-3 w-3 shrink-0 text-accent" />
        ) : hasTools ? (
          <IconWrench className="h-3 w-3 shrink-0 text-faint" />
        ) : (
          reasoning && <IconBrain className="h-3 w-3 shrink-0 text-accent" />
        )}
        <span className="min-w-0 flex-1 truncate text-faint">{fullSummary}</span>
      </button>
      {open && (
        <div className="flex flex-col gap-1.5 px-1 pb-1">
          <CollapsedToolBlocks d={d} onAskAnswered={onAskAnswered} />
        </div>
      )}
    </div>
  );
}

// Groups a run of consecutive collapsed tool summaries into one entry with a
// combined summary ("Read 4 files · Ran 2 commands"); expanding shows each
// message's blocks in order. Starts open while any member has an unanswered
// question.
function CollapsedToolsGroup({
  members,
  onAskAnswered,
}: {
  members: CollapseData[];
  onAskAnswered: (answers: AskAnswerView[]) => void;
}) {
  const anyPendingAsk = members.some((m) => m.ask != null && m.ask.result === undefined && !m.ask.failed);
  const [open, setOpen] = useState(anyPendingAsk);
  useEffect(() => {
    if (anyPendingAsk) setOpen(true);
  }, [anyPendingAsk]);
  const calls = members.flatMap((m) => m.calls);
  const results = members.flatMap((m) => m.results);
  const askCount = members.reduce((a, m) => a + m.askCount, 0);
  const fullSummary = collapseSummary(calls, results, askCount);
  const hasTools = calls.length + results.length > 0 || askCount > 0;
  const hasReasoning = members.some((m) => m.reasoning);
  return (
    <div className="text-[10px]">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[26px] w-full items-center gap-1.5 px-1 py-1 text-left text-dim transition-colors hover:text-text"
      >
        {open ? (
          <IconChevronDown className="h-3 w-3 shrink-0" />
        ) : (
          <IconChevronRight className="h-3 w-3 shrink-0" />
        )}
        {hasTools ? (
          <IconWrench className="h-3 w-3 shrink-0 text-faint" />
        ) : (
          hasReasoning && <IconBrain className="h-3 w-3 shrink-0 text-accent" />
        )}
        <span className="min-w-0 flex-1 truncate text-faint">{fullSummary}</span>
      </button>
      {open && (
        <div className="flex flex-col gap-1.5 px-1 pb-1">
          {members.map((m, j) => (
            <div key={j} className="flex flex-col gap-1.5">
              {m.content && m.content.trim() && (
                <div className="whitespace-pre-wrap break-words px-0.5 text-sm text-text">
                  {m.content}
                </div>
              )}
              <CollapsedToolBlocks d={m} onAskAnswered={onAskAnswered} />
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function ToolBlocks({ calls, results }: { calls: ToolCall[]; results: ToolCall[] }) {
  const out: ReactNode[] = [];
  let next = 0;
  calls.forEach((tc, j) => {
    // Merge call + its result into one card for these tools.
    if (tc.name === 'run_command' || tc.name === 'search_files') {
      let k = next;
      while (k < results.length && results[k].name !== tc.name) k++;
      if (k < results.length) {
        next = k + 1;
        if (tc.name === 'run_command') {
          out.push(
            <RunCommandBlock key={`c${j}`} command={chipLabel(tc.detail)} result={results[k]} />,
          );
        } else {
          out.push(
            <SearchFilesBlock key={`c${j}`} query={chipLabel(tc.detail)} result={results[k]} />,
          );
        }
        return;
      }
    }
    out.push(<ToolChip key={`c${j}`} {...tc} />);
  });
  results.forEach((tr, j) => {
    // results already merged into a RunCommandBlock/SearchFilesBlock are skipped
    if (j < next && (tr.name === 'run_command' || tr.name === 'search_files')) return;
    out.push(<ToolResultBlock key={`r${j}`} {...tr} />);
  });
  return <div className="flex flex-col gap-1.5">{out}</div>;
}

type AskQuestionView = { question: string; options: string[] };
type AskAnswerView = { question: string; answer: string };

// Parses an ask_user tool call's arguments JSON into the question list
// ("questions" array, or a single "question" with options). Falls back to the
// raw detail (a plain question string) when it isn't JSON.
function askQuestions(detail: string): AskQuestionView[] {
  try {
    const a = JSON.parse(detail) as { question?: unknown; options?: unknown; questions?: unknown };
    const clean = (q: { question?: unknown; options?: unknown }): AskQuestionView | null => {
      if (typeof q.question !== 'string' || !q.question.trim()) return null;
      return {
        question: q.question,
        options: Array.isArray(q.options) ? q.options.filter((o): o is string => typeof o === 'string') : [],
      };
    };
    if (Array.isArray(a.questions)) {
      const list = a.questions.map(clean).filter((q): q is AskQuestionView => q !== null);
      if (list.length > 0) return list;
    }
    const single = clean(a);
    if (single) return [single];
    return [{ question: detail, options: [] }];
  } catch {
    return [{ question: detail, options: [] }];
  }
}

// Extracts the answers from an ask_user tool result: multi-question results
// are {"answers":[{question,answer},…]}, single ones {"answer": "..."}.
function askAnswers(detail: string): AskAnswerView[] {
  try {
    const r = JSON.parse(detail) as { answer?: unknown; answers?: unknown };
    if (Array.isArray(r.answers)) {
      const list = r.answers
        .filter((a): a is { question?: unknown; answer?: unknown } => typeof a === 'object' && a !== null)
        .map((a) => ({
          question: typeof a.question === 'string' ? a.question : '',
          answer: typeof a.answer === 'string' ? a.answer : '',
        }))
        .filter((a) => a.answer !== '');
      if (list.length > 0) return list;
    }
    if (typeof r.answer === 'string' && r.answer !== '') {
      return [{ question: '', answer: r.answer }];
    }
    return [];
  } catch {
    return [];
  }
}

// The agent's ask_user question: the question with tappable options while
// pending, the chosen answer once answered. Rendered inline in the transcript
// (never inside the tool collapse) and for live streams.
// The agent's ask_user block. A single question shows the options + answer
// input directly; multiple questions render as a stepper the user walks
// through (back/next, answers editable) with a final "confirm all" action.
function AskBlock({
  questions,
  result,
  failed,
  onAnswer,
}: {
  questions: AskQuestionView[];
  result?: AskAnswerView[];
  /** The ask ended without answers (timed out, canceled). */
  failed?: boolean;
  onAnswer?: (answers: AskAnswerView[]) => void;
}) {
  const [step, setStep] = useState(0);
  const [drafts, setDrafts] = useState<string[]>(() => questions.map(() => ''));
  const answered = result !== undefined;
  const multi = questions.length > 1;
  const q = questions[Math.min(step, questions.length - 1)];
  const cur = drafts[step] ?? '';
  const allAnswered = drafts.every((d) => d.trim() !== '');

  const setCur = (v: string) =>
    setDrafts((prev) => prev.map((d, i) => (i === step ? v : d)));

  return (
    <div className="rounded-lg border border-accent/50 bg-surface p-3 text-sm">
      {answered ? (
        <>
          <div className="flex items-start gap-2">
            <IconUser className="mt-0.5 h-4 w-4 shrink-0 text-accent" />
            <div className="min-w-0 flex-1 space-y-2">
              {result!.map((r, i) => (
                <div key={i}>
                  <p className="whitespace-pre-wrap break-words text-text">
                    {multi && <span className="mr-1 font-mono text-[10px] text-faint">{i + 1}.</span>}
                    {r.question || questions[i]?.question || ''}
                  </p>
                  <p className="mt-1 flex items-center gap-1.5 text-xs text-dim">
                    <IconCheck className="h-3.5 w-3.5 shrink-0 text-emerald-400" />
                    {r.answer}
                  </p>
                </div>
              ))}
            </div>
          </div>
        </>
      ) : failed ? (
        <div className="flex items-start gap-2">
          <IconUser className="mt-0.5 h-4 w-4 shrink-0 text-accent" />
          <p className="min-w-0 flex-1 whitespace-pre-wrap break-words text-text">
            {multi ? questions.map((qq) => qq.question).join(' · ') : q.question}
          </p>
        </div>
      ) : (
        <>
          {multi && (
            <div className="mb-1.5 px-1 text-[10px] font-medium uppercase tracking-wider text-faint">
              Question {step + 1} of {questions.length}
            </div>
          )}
          <div className="flex items-start gap-2">
            <IconUser className="mt-0.5 h-4 w-4 shrink-0 text-accent" />
            <p className="min-w-0 flex-1 whitespace-pre-wrap break-words text-text">{q.question}</p>
          </div>
          {q.options.length > 0 && (
            <div className="mt-2.5 flex flex-wrap gap-1.5">
              {q.options.map((o) => (
                <Button
                  key={o}
                  variant="outline"
                  className={`px-3 text-xs whitespace-normal break-words text-left ${cur === o ? 'border-accent text-text' : ''}`}
                  onClick={() => setCur(o)}
                >
                  {o}
                </Button>
              ))}
            </div>
          )}
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (multi && !allAnswered) {
                setStep((s) => Math.min(s + 1, questions.length - 1));
              } else if (onAnswer) {
                onAnswer(questions.map((qq, i) => ({ question: qq.question, answer: drafts[i] ?? '' })));
              }
            }}
            className="mt-2.5 flex items-end gap-2"
          >
            <div className="flex-1">
              <Input
                value={cur}
                onChange={(e) => setCur(e.target.value)}
                placeholder={q.options.length > 0 ? '…or type an answer' : 'Type an answer…'}
                autoComplete="off"
              />
            </div>
            {multi ? (
              <div className="flex shrink-0 gap-1.5">
                <Button
                  type="button"
                  variant="outline"
                  className="h-[42px] px-3 text-xs sm:h-[38px]"
                  disabled={step === 0}
                  onClick={() => setStep((s) => Math.max(0, s - 1))}
                >
                  Back
                </Button>
                {step < questions.length - 1 ? (
                  <Button
                    type="submit"
                    variant="outline"
                    className="h-[42px] px-3 text-xs sm:h-[38px]"
                    disabled={!cur.trim()}
                  >
                    Next
                  </Button>
                ) : (
                  <Button
                    type="submit"
                    variant="outline"
                    className="h-[42px] px-3 text-xs sm:h-[38px]"
                    disabled={!allAnswered}
                  >
                    Confirm all
                  </Button>
                )}
              </div>
            ) : (
              <Button type="submit" variant="outline" className="h-[42px] sm:h-[38px]" disabled={!cur.trim()}>
                Answer
              </Button>
            )}
          </form>
        </>
      )}
    </div>
  );
}

function ToolResultBlock({ name, detail }: ToolCall) {
  const [open, setOpen] = useState(false);
  const Icon = toolIcon(name);
  return (
    <div className="rounded-md border border-border/80 bg-surface/50 text-[10px]">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[26px] w-full items-center gap-1.5 px-2 py-1 text-left text-dim transition-colors hover:text-text"
      >
        {open ? (
          <IconChevronDown className="h-3.5 w-3.5 shrink-0" />
        ) : (
          <IconChevronRight className="h-3.5 w-3.5 shrink-0" />
        )}
        <Icon className="h-3 w-3 shrink-0 text-faint" />
        <span className="shrink-0 font-mono text-text">{toolLabel(name)}</span>
        <span className="text-faint">result</span>
      </button>
      {open && <ToolBody detail={detail} />}
    </div>
  );
}

function ImageLightbox({
  url,
  name,
  onClose,
}: {
  url: string;
  name: string;
  onClose: () => void;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/90 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <img
        src={url}
        alt={name}
        className="max-h-full max-w-full rounded-lg object-contain shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      />
      <button
        type="button"
        onClick={onClose}
        aria-label="Close image preview"
        className="absolute right-3 top-3 inline-flex h-10 w-10 items-center justify-center rounded-lg bg-surface/80 text-dim transition-colors hover:bg-border hover:text-text"
      >
        <IconX className="h-5 w-5" />
      </button>
      <span className="absolute inset-x-0 bottom-3 mx-auto max-w-[80vw] truncate text-center text-xs text-dim">
        {name}
      </span>
    </div>
  );
}

function EditUserBubble({
  initial,
  onSubmit,
  onCancel,
}: {
  initial: string;
  onSubmit: (text: string) => void;
  onCancel: () => void;
}) {
  const [value, setValue] = useState(initial);
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (el) {
      el.focus();
      el.select();
    }
  }, []);
  const submit = () => {
    if (value.trim()) onSubmit(value);
  };
  return (
    <div className="ml-auto flex w-full max-w-[85%] flex-col gap-2">
      <textarea
        ref={ref}
        rows={3}
        value={value}
        autoCorrect="off"
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            submit();
          }
        }}
        className="max-h-60 w-full resize-y rounded-xl border border-border-strong bg-surface px-3.5 py-2 text-sm text-text outline-none transition-colors focus:border-subtle"
      />
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button variant="outline" onClick={submit}>
          Send
        </Button>
      </div>
    </div>
  );
}

const MessageRow = memo(function MessageRow({
  item,
  isLast,
  streaming,
  turnEnd,
  validTag,
  onEdit,
  onRewind,
  onRegenerate,
  onRetrySend,
  onEditStart,
  onImageClick,
  onAskAnswered,
  currency,
}: {
  item: Item;
  isLast: boolean;
  streaming: boolean;
  turnEnd: boolean;
  validTag: (tag: string) => boolean;
  onEdit: (key: string, text: string) => void;
  onRewind: (key: string, text: string) => void;
  onRegenerate: () => void;
  onRetrySend: (text: string) => void;
  onEditStart: (key: string, editing: boolean) => void;
  onImageClick: (url: string, name: string) => void;
  onAskAnswered: (answers: AskAnswerView[]) => void;
  currency: string;
}) {
  if (item.kind === 'tool') return <ToolRow item={item} />;
  if (item.kind === 'ask') {
    return (
      <AskBlock
        questions={item.questions}
        result={item.result}
        failed={item.failed}
        onAnswer={onAskAnswered}
      />
    );
  }
  if (item.role === 'user') {
    // Background job results are not the user speaking — render them on the
    // left with a branded "Recived bg task" header instead of a user bubble.
    if (item.background) {
      return (
        <div className="mr-auto flex w-full max-w-[85%] flex-col gap-1">
          <span className="inline-flex items-center gap-1 text-[10px] font-medium uppercase tracking-wide text-amber-300/80">
            <IconTerminal className="h-3 w-3" />
            Background task finished
          </span>
          <div className="w-full rounded-xl border border-amber-300/20 bg-amber-300/5 px-3.5 py-2 text-sm text-text">
            <div className="whitespace-pre-wrap break-words font-mono text-[12px] leading-relaxed text-amber-100/90">
              {item.content}
            </div>
          </div>
        </div>
      );
    }
    if (item.editing) {
      return (
        <EditUserBubble
          initial={item.content}
          onSubmit={(text) => onEdit(item.key, text)}
          onCancel={() => onEditStart(item.key, false)}
        />
      );
    }
    return (
      <div className="ml-auto flex max-w-[85%] flex-col items-end gap-1">
        {item.steered && (
          <span className="inline-flex items-center gap-1 rounded-full border border-accent/50 bg-accent/10 px-2 py-0.5 text-[10px] font-medium text-accent">
            <IconSend className="h-2.5 w-2.5" /> steered
          </span>
        )}
        <div className="w-full rounded-2xl bg-border px-3.5 py-2 text-sm text-text">
          <div className="whitespace-pre-wrap break-words"><TaggedText text={item.content} valid={validTag} /></div>
          {item.attachments && item.attachments.length > 0 && (
            <div className="mt-1.5 flex flex-wrap justify-end gap-1.5">
              {item.attachments.map((a, i) =>
                a.kind === 'image' && a.url ? (
                  <button
                    key={i}
                    type="button"
                    onClick={() => onImageClick(a.url as string, a.name)}
                    title={`View ${a.name}`}
                    className="overflow-hidden rounded-lg border border-border-strong/60 transition-opacity hover:opacity-80"
                  >
                    <img src={a.url} alt={a.name} loading="lazy" className="h-28 w-28 object-cover" />
                  </button>
                ) : (
                  <span
                    key={i}
                    className="flex items-center gap-1 rounded-md bg-surface px-1.5 py-0.5 text-[10px] text-dim"
                  >
                    <IconPaperclip className="h-3 w-3 shrink-0 text-faint" />
                    <span className="max-w-[140px] truncate">{a.name}</span>
                  </span>
                ),
              )}
            </div>
          )}
        </div>
        {persisted(item.key) && !streaming ? (
          <div className="flex items-center gap-2 pr-1">
            {item.sentAt && (
              <span className="text-[10px] text-faint">{formatTime(item.sentAt)}</span>
            )}
            <button
              type="button"
              onClick={() => onEditStart(item.key, true)}
              className="text-[11px] text-faint transition-colors hover:text-text"
            >
              Edit
            </button>
            {!isLast && (
              <button
                type="button"
                onClick={() => void onRewind(item.key, item.content)}
                title="Rewind to here (delete everything after)"
                className="text-[11px] text-faint transition-colors hover:text-text"
              >
                Rewind
              </button>
            )}
          </div>
        ) : (
          item.sentAt && <div className="pr-1 text-[10px] text-faint">{formatTime(item.sentAt)}</div>
        )}
      </div>
    );
  }
  if (item.role === 'error') {
    return (
      <div className="flex flex-col gap-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2.5">
        <div className="flex items-start gap-2">
          <IconX className="mt-0.5 h-4 w-4 shrink-0 text-red-400" />
          <div className="min-w-0 flex-1 whitespace-pre-wrap break-words text-sm leading-relaxed text-red-300">
            {item.content}
          </div>
        </div>
        {!streaming && (
          <div className="flex justify-end">
            <button
              type="button"
              onClick={onRegenerate}
              className="text-xs font-medium text-red-300 underline-offset-2 transition-colors hover:text-red-200 hover:underline"
            >
              Try again
            </button>
            <button
              type="button"
              onClick={() => onRetrySend(item.content)}
              className="ml-3 text-xs font-medium text-red-300 underline-offset-2 transition-colors hover:text-red-200 hover:underline"
              title="Send this message again"
            >
              Send this message again
            </button>
          </div>
        )}
      </div>
    );
  }
  // The ask joins the tool collapse like everything else, but stays open
  // while the question is unanswered so it can always be answered.
  const d = collapseData(item);
  const { calls, results, ask, askCount } = d;
  const hasTools = calls.length + results.length > 0 || askCount > 0;
  const collapseTools = getToolCallsCollapsed();
  return (
    <div
      className={`flex min-w-0 flex-col gap-1.5 ${item.stale ? 'opacity-45' : ''}`}
    >
      {collapseTools && hasTools ? (
        <CollapsedTools
          d={d}
          streaming={item.streaming ?? false}
          onAskAnswered={onAskAnswered}
        />
      ) : (
        <>
          {item.reasoning && (
            <ReasoningBlock
              text={item.reasoning}
              autoOpen={(item.streaming ?? false) && !item.reasoningCollapsed}
              skipAutoCollapse={item.reasoningCollapsed === true}
            />
          )}
          {hasTools && (
            <ToolBlocks calls={calls} results={results} />
          )}
          {ask && (
            <AskBlock
              questions={ask.questions}
              result={ask.result}
              failed={ask.failed}
              onAnswer={onAskAnswered}
            />
          )}
        </>
      )}
      {item.content && (
        <div className="min-w-0">
          <Markdown text={item.content} streaming={item.streaming} validTag={validTag} />
        </div>
      )}
      {turnEnd && item.usage && !item.streaming && (
        <div className="text-[10px] text-faint">
          {item.usage.input.toLocaleString()} in · {item.usage.output.toLocaleString()} out
          {typeof item.usage.cost === 'number' ? ` · ${formatCost(item.usage.cost, currency)}` : ''}
          {item.usage.model ? ` · ${item.usage.model}` : ''}
          {item.elapsedMs != null ? ` · ${formatElapsed(item.elapsedMs)}` : ''}
          {item.sentAt ? ` · ${formatTime(item.sentAt)}` : ''}
        </div>
      )}
      {persisted(item.key) && !streaming && isLast && (
        <div className="flex gap-1">
          <button
            type="button"
            onClick={onRegenerate}
            aria-label="Regenerate response"
            title="Retry with the same prompt"
            className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-text"
          >
            <IconRefresh className="h-3.5 w-3.5" />
          </button>
        </div>
      )}
    </div>
  );
});

export default function ChatPane({
  projectId,
  projectName,
  onPreviewRestart,
  onMemories,
  onProjectRename,
  onSessionName,
  llmReady,
  sessionsOpen,
  onSessionsOpenChange,
  initialPrompt,
  initialProviderId,
  initialModel,
  initialThinking,
}: {
  projectId: string;
  projectName: string;
  onPreviewRestart: () => void;
  onMemories?: (mems: Memory[]) => void;
  /** The agent renamed the project (set_project_name). */
  onProjectRename?: (name: string) => void;
  /** Reports the active session's name so the header can show it. */
  onSessionName?: (name: string) => void;
  /** null while the LLM configuration is still loading. */
  llmReady: boolean | null;
  /** Session switcher open state, lifted from ChatPane so the project title
   * in ChatPanel can open the same modal. */
  sessionsOpen: boolean;
  onSessionsOpenChange: (open: boolean) => void;
  /** Description from the New project dialog — auto-sent once, when ready. */
  initialPrompt?: string;
  /** Optional model selection from the New project dialog, used when the
   * project has no persisted selection yet. */
  initialProviderId?: string;
  initialModel?: string;
  initialThinking?: string;
}) {
  const [items, setItems] = useState<Item[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [input, setInput] = useState('');
  const [suggestions, setSuggestions] = useState<Suggestion[]>([]);
  const [suggestionIndex, setSuggestionIndex] = useState(0);
  const [plusOpen, setPlusOpen] = useState(false);
  const [streaming, setStreaming] = useState(false);
  // True while an interrupted turn is being re-run in the background after a
  // lost connection (the persisted history is shown instantly meanwhile).
  const [resuming, setResuming] = useState(false);
  // True while watching a generation that is still running server-side (the
  // viewer left mid-run and came back — the run survives the disconnect).
  // True while a generation is running server-side (seen from this client).
  const [runActive, setRunActive] = useState(false);
  // Context fill (tokens used vs budget) for the ring button + popup. The
  // ring only renders once a definitive value is in — a spinner shows while
  // the model-specific budget is still loading, so it never jumps between
  // provisional sizes.
  const [ctx, setCtx] = useState<ContextUsage | null>(null);
  const [ctxLoading, setCtxLoading] = useState(true);
  const ctxReqRef = useRef(0);
  const [ctxOpen, setCtxOpen] = useState(false);
  const [ctxCompacting, setCtxCompacting] = useState(false);
  const [attachments, setAttachments] = useState<ChatAttachmentInput[]>([]);
  const [attachError, setAttachError] = useState<string | null>(null);
  const [model, setModel] = useState('');
  const [thinking, setThinking] = useState('');
  const [thinkingOpen, setThinkingOpen] = useState(false);
  const [defaultThinking, setDefaultThinking] = useState('');
  const [providerId, setProviderId] = useState(''); // '' = custom (no saved provider)
  const [providers, setProviders] = useState<SavedProvider[]>([]);
  const [catalog, setCatalog] = useState<Provider[]>([]);
  const [todos, setTodos] = useState<Todo[]>([]);
  const [todosOpen, setTodosOpen] = useState(false);
  const [toolsOpen, setToolsOpen] = useState(false);
  const [toolsTab, setToolsTab] = useState<ToolsTab>('mcp');
  const openTools = (tab: ToolsTab) => {
    setToolsTab(tab);
    setToolsOpen(true);
  };
  const [modelPickerOpen, setModelPickerOpen] = useState(false);
  const isDesktop = useMediaQuery('(min-width: 768px)');
  // Mobile-only: the composer can cover the chat pane for long messages.
  const [expanded, setExpanded] = useState(false);
  const [lightbox, setLightbox] = useState<{ url: string; name: string } | null>(null);
  const track = TRACK_DEFAULTS;
  const [permissionMode, setPermissionMode] = useState<PermissionMode>('ask');
  const [rewindApproval, setRewindApproval] = useState(false);
  const [permPrompt, setPermPrompt] = useState<{
    requestId: string;
    tool: string;
    detail: string;
  } | null>(null);
  const [askPrompt, setAskPrompt] = useState<{
    requestId: string;
    questions: AskQuestionView[];
  } | null>(null);
  // Messages sent while a run is active, in processing order. They become
  // follow-up turns when the run finishes; the steer button injects one into
  // the current run immediately.
  const [queued, setQueued] = useState<{ id: string; text: string; position?: number; estimatedWaitSeconds?: number }[]>([]);
  // Messages steered into the current run, waiting to be injected at the next
  // round boundary.
  const [steering, setSteering] = useState<{ id: string; text: string }[]>([]);
  const steeringRef = useRef<{ id: string; text: string }[]>([]);
  useEffect(() => {
    steeringRef.current = steering;
  }, [steering]);
  const [queueEditId, setQueueEditId] = useState<string | null>(null);
  const [queueEditText, setQueueEditText] = useState('');
  const queueEditIdRef = useRef<string | null>(null);
  const watchRef = useRef<AbortController | null>(null);
  // Mirrors of streaming/handleEvent for effects that must react to state
  // changes without re-running on them.
  const streamingRef = useRef(false);
  const handleEventRef = useRef<(ev: ChatEvent) => void>(() => {});
  useEffect(() => {
    streamingRef.current = streaming;
  }, [streaming]);
  // The active chat session ('' until the session list loads).
  const [sessionId, setSessionId] = useState('');
  // Latest session id for event handlers (handleEvent is memoized without
  // sessionId in its deps — the ref keeps it current without re-subscribing).
  const sessionIdRef = useRef('');
  useEffect(() => {
    sessionIdRef.current = sessionId;
  }, [sessionId]);
  const [sessions, setSessions] = useState<ChatSession[]>([]);
  const [creatingSession, setCreatingSession] = useState(false);
  const navigate = useNavigate();

  // Report the active session name up to the header (project title + session
  // subtitle click to switch).
  useEffect(() => {
    const s = sessions.find((x) => x.id === sessionId);
    onSessionName?.(s?.name ?? '');
  }, [sessions, sessionId, onSessionName]);

  // Composer drafts persist per project+session so typed text survives app
  // restarts and session switches.
  const draftKey = sessionId ? `v1-chat-draft:${projectId}:${sessionId}` : '';
  const prevDraftKey = useRef('');
  useEffect(() => {
    setInput(draftKey ? (localStorage.getItem(draftKey) ?? '') : '');
  }, [draftKey]);
  useEffect(() => {
    if (!draftKey) return;
    if (prevDraftKey.current !== draftKey) {
      // Just switched sessions — the restore above applied the saved draft;
      // don't write the previous session's text into this key.
      prevDraftKey.current = draftKey;
      return;
    }
    if (input) localStorage.setItem(draftKey, input);
    else localStorage.removeItem(draftKey);
  }, [draftKey, input]);

  const itemsRef = useRef<Item[]>([]);
  const counterRef = useRef(0);
  const assistantKeyRef = useRef<string | null>(null);
  // Monotonic guard so a slow load() for an older session can't overwrite the
  // transcript after a switch; every load bumps it and stale results are dropped.
  const loadSeqRef = useRef(0);
  // Key of the last turn we notified about (persisted per project+session so
  // a reload does not re-notify for runs already seen).
  const notifiedKey = `v1.notified.${projectId}.${sessionId}`;
  const lastNotifiedRef = useRef<string | null>(null);
  useEffect(() => {
    try {
      lastNotifiedRef.current = localStorage.getItem(notifiedKey);
    } catch {}
  }, [notifiedKey]);
  const toolStackRef = useRef<Record<string, string[]>>({});
  const abortRef = useRef<AbortController | null>(null);
  // When the current turn started — for the elapsed time shown with usage.
  const turnStartRef = useRef(0);
  const scrollRef = useRef<HTMLDivElement>(null);
  const taRef = useRef<HTMLTextAreaElement>(null);
  const echoRef = useRef<HTMLDivElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  // File and enabled-skill lists power the @/# autocomplete and tag
  // validation (only real files/skills get pill-highlighted). Loaded eagerly;
  // files refresh after each agent run since runs create/edit files.
  const [fileList, setFileList] = useState<string[]>([]);
  const [skillList, setSkillList] = useState<{ name: string; hint: string }[]>([]);
  const [currency, setCurrency] = useState('USD');
  // Debug HUD flag (Settings → Appearance) also gates the diagnostics export.
  const [debugEnabled] = useState(() => getDebugHud());
  const restartRef = useRef(onPreviewRestart);
  restartRef.current = onPreviewRestart;
  const onMemoriesRef = useRef(onMemories);
  onMemoriesRef.current = onMemories;

  const openLightbox = useCallback((url: string, name: string) => setLightbox({ url, name }), []);

  const update = useCallback((fn: (prev: Item[]) => Item[]) => {
    itemsRef.current = fn(itemsRef.current);
    setItems(itemsRef.current);
  }, []);

  // Load the per-project chat selection (localStorage), the saved providers
  // and the models.dev catalog. The model shows as a plain selection whenever
  // it exists in the selected provider's catalog — never as "custom" — and as
  // a free-text id when it does not (i.e. it was custom before).
  useEffect(() => {
    let active = true;
    const selKey = `v1.chatModel.${projectId}`;
    let persisted: { providerId: string; model: string; thinking?: string } | null = null;
    try {
      persisted = JSON.parse(localStorage.getItem(selKey) ?? 'null');
    } catch {
      persisted = null;
    }
    api
      .getSettings()
      .then((s) => {
        if (!active) return;
        const saved = s.llm.providers ?? [];
        setProviders(saved);
        setPermissionMode(s.permissionMode ?? 'ask');
        setRewindApproval(s.rewindApproval ?? false);
        const sel =
          persisted ??
          (initialProviderId || initialModel || initialThinking
            ? {
                providerId: initialProviderId ?? saved[0]?.id ?? '',
                model: initialModel ?? s.llm.defaultModel ?? s.llm.model,
                thinking: initialThinking ?? '',
              }
            : {
                providerId: saved[0]?.id ?? '',
                model: s.llm.defaultModel ?? s.llm.model,
              });
        // A persisted provider that was deleted falls back to the first one.
        if (sel.providerId === '' || saved.some((p) => p.id === sel.providerId)) {
          setProviderId(sel.providerId);
        } else {
          setProviderId(saved[0]?.id ?? '');
        }
        setModel(sel.model);
        setThinking(typeof sel.thinking === 'string' ? sel.thinking : '');
        setDefaultThinking(s.defaultThinking ?? '');
        try {
          localStorage.setItem(selKey, JSON.stringify(sel));
        } catch {
          // storage unavailable — ignore
        }
      })
      .catch(() => {
        // header falls back to the custom model input
      });
    api
      .getProviders()
      .then((r) => {
        if (active) setCatalog(r.providers);
      })
      .catch(() => {
        // catalog unavailable — model input falls back to free text
      });
    return () => {
      active = false;
    };
  }, [projectId]);

  const selectedProvider = useMemo(
    () => providers.find((p) => p.id === providerId) ?? null,
    [providers, providerId],
  );

  // Model list for the selected provider, unioned across catalog entries that
  // share the same base URL.
  const catalogModels = useMemo(() => {
    if (!selectedProvider) return [] as ProviderModel[];
    const byId = new Map<string, ProviderModel>();
    for (const p of catalog) {
      if (p.baseURL !== selectedProvider.baseURL) continue;
      for (const m of p.models) {
        if (!byId.has(m.id)) byId.set(m.id, m);
      }
    }
    return [...byId.values()];
  }, [catalog, selectedProvider]);

  // A provider-backed model shows as a searchable combobox (free text is also
  // allowed there); only the "Custom" provider gets the plain input. Keeping
  // the element stable while typing prevents focus loss.
  const showFreeText = providerId === '';

  const selectedModelMeta = useMemo(
    () => catalogModels.find((m) => m.id === model) ?? null,
    [catalogModels, model],
  );

  // Text files inline as plain text, so any model accepts them; only images
  // need a vision-capable model. For custom or uncatalogued ids the
  // capability is unknown, so allow images and let the provider decide.
  const supportsImages =
    showFreeText || selectedModelMeta === null || selectedModelMeta.imageInput === true;

  const modelLabel = selectedModelMeta?.name || model || 'Select model';

  const persistSelection = useCallback(
    (pid: string, m: string, th: string) => {
      try {
        localStorage.setItem(
          `v1.chatModel.${projectId}`,
          JSON.stringify({ providerId: pid, model: m, thinking: th }),
        );
      } catch {
        // storage unavailable — ignore
      }
    },
    [projectId, sessionId],
  );

  const changeProvider = (pid: string) => {
    setProviderId(pid);
    // Do NOT reset the model here: the picker keeps the custom draft alive
    // and commits a model only when the user selects one (or saves the draft),
    // so switching provider options never erases a typed custom model.
  };

  const changeModel = (m: string) => {
    setModel(m);
    applyThinkingForModel(m);
  };

  // Applies the new model's thinking level immediately when its options are
  // cached; otherwise clears to the loading state and the fetch resolves it.
  const applyThinkingForModel = (m: string) => {
    const cached = m ? thinkingMetaCache.current.get(`${providerId}|${m}`) : undefined;
    if (cached) {
      setThinkingMeta(cached);
      const lvl = freshThinkingLevel(cached);
      setThinking(lvl);
      persistSelection(providerId, m, lvl);
    } else {
      setThinkingMeta(null);
      setThinking('');
      persistSelection(providerId, m, '');
    }
  };

  // Thinking options come from the provider's own /models endpoint (server
  // resolves it, with a family fallback when the endpoint publishes nothing).
  // Results are cached per provider+model so switching back is instant.
  const [thinkingMeta, setThinkingMeta] = useState<{ levels: string[]; off: boolean } | null>(
    null,
  );
  const [thinkingLoading, setThinkingLoading] = useState(false);
  const thinkingMetaCache = useRef(new Map<string, { levels: string[]; off: boolean }>());  // The level a fresh selection gets: the global default when the model
  // supports it, otherwise the next highest available level (or the lowest
  // when the default sits below everything the model offers).
  // The level a fresh selection gets: the global default when the model
  // supports it, otherwise the next highest available level (or the lowest
  // when the default sits below everything the model offers). A non-"off"
  // default always turns thinking on — it maps to the next available
  // thinking level even when the model's list starts with "off".
  const freshThinkingLevel = (meta: { levels: string[]; off: boolean }): string => {
    if (defaultThinking === 'off' && (meta.off || meta.levels.includes('none'))) return 'off';
    if (defaultThinking === '') return meta.levels[0] ?? '';
    if (meta.levels.includes(defaultThinking)) return defaultThinking;
    const reqRank = THINKING_LEVEL_RANK[defaultThinking] ?? -1;
    // The effective level always matches or exceeds the requested default:
    // pick the lowest available level that is at least as strong as it. Only
    // when the model has nothing that strong, settle for its strongest level
    // below the request. On/off rules are unchanged.
    let best = '';
    let bestRank = Infinity;
    let fallback = '';
    let fallbackRank = -Infinity;
    meta.levels.forEach((lvl, i) => {
      const rank = THINKING_LEVEL_RANK[lvl] ?? i + 10;
      // Skip off/none when the default asks for thinking: a non-off default
      // must not land on "off".
      if (defaultThinking !== 'off' && (lvl === 'off' || lvl === 'none')) return;
      if (rank >= reqRank && rank < bestRank) {
        best = lvl;
        bestRank = rank;
      }
      if (rank < reqRank && rank > fallbackRank) {
        fallback = lvl;
        fallbackRank = rank;
      }
    });
    return best || fallback || (defaultThinking !== 'off' ? meta.levels.find((l) => l !== 'off' && l !== 'none') ?? '' : '') || meta.levels[0] || '';
  };
  useEffect(() => {
    if (!model.trim()) {
      setThinkingMeta(null);
      return;
    }
    const key = `${providerId}|${model}`;
    // The in-memory map is per mount; fall back to the persisted cache so
    // reopening a project doesn't refetch the provider's /models for a model
    // we already asked about.
    const cached = thinkingMetaCache.current.get(key) ?? readThinkingMeta(key);
    if (cached) {
      thinkingMetaCache.current.set(key, cached);
      setThinkingMeta(cached);
      setThinkingLoading(false);
      return;
    }
    let active = true;
    setThinkingLoading(true);
    api
      .providerThinking(providerId, model)
      .then((r) => {
        if (!active) return;
        const meta = { levels: r.levels ?? [], off: r.off ?? false };
        thinkingMetaCache.current.set(key, meta);
        writeThinkingMeta(key, meta);
        setThinkingMeta(meta);
        // A fresh selection uses the global default thinking level when the
        // model supports it, otherwise its lowest level.
        setThinking((prev) => (prev === '' ? freshThinkingLevel(meta) : prev));
      })
      .catch(() => {
        if (active) setThinkingMeta(null);
      })
      .finally(() => {
        if (active) setThinkingLoading(false);
      });
    return () => {
      active = false;
    };
  }, [providerId, model, defaultThinking]);
  // The selectable levels: published levels (with 'none' folded into Off),
  // otherwise a single On.
  const thinkingOptions = useMemo(() => {
    if (!thinkingMeta) return [] as string[];
    const levels = thinkingMeta.levels.filter((l) => l !== 'none');
    if (levels.length > 0) return levels;
    return ['on'];
  }, [thinkingMeta]);
  // Off sends nothing, unless the model spells it 'none' (OpenAI) — then it's
  // sent explicitly, because those models require reasoning_effort to be set.
  const thinkingOffValue = thinkingMeta?.levels.includes('none') ? 'none' : '';
  const thinkingOffSupported = (thinkingMeta?.off ?? false) || thinkingOffValue !== '';
  // The level actually sent: 'off' sends nothing (or 'none'), otherwise the
  // selection when it is one of the model's options.
  const thinkingEffort =
    thinking === 'off' ? thinkingOffValue : thinkingOptions.includes(thinking) ? thinking : '';
  // What the button/popup show: the current selection, with Off standing in
  // for "unset" when the model supports turning thinking off.
  const thinkingDisplay =
    thinking === 'off' || (thinkingEffort === '' && thinkingMeta?.off)
      ? 'off'
      : thinkingEffort || '';
  // Button colors escalate with the level (low = green … max = red).
  const thinkingColor =
    thinkingDisplay === 'off' || thinkingDisplay === ''
      ? { text: 'text-dim', border: 'border-border' }
      : (THINKING_LEVEL_COLORS[thinkingDisplay] ?? { text: 'text-accent', border: 'border-accent/60' });
  const thinkingLabel =
    thinkingDisplay === 'off' ? 'Off' : thinkingDisplay === 'on' ? 'On' : thinkingDisplay;
  // Show the button while a new model's options are still loading, with a
  // placeholder instead of a wrong value.
  const showThinking = thinkingLoading || thinkingOptions.length > 0;

  const modelOverride = model.trim() || undefined;
  const providerOverride = providerId || undefined;
  const hasModel = modelOverride !== undefined;

  // Fetches the project's persisted pending ask_user question, if any, and
  // remembers its request id so the transcript block can answer it. The
  // server clears the record when the question is answered, times out, or a
  // new turn starts.
  const refreshPendingAsk = useCallback(async () => {
    try {
      const p = await api.askPending(projectId, sessionId);
      if (p.pending && p.requestId && p.question) {
        const questions: AskQuestionView[] =
          p.questions && p.questions.length > 0
            ? p.questions.map((q) => ({ question: q.question, options: q.options ?? [] }))
            : [{ question: p.question, options: p.options ?? [] }];
        setAskPrompt({ requestId: p.requestId, questions });
      } else {
        setAskPrompt(null);
      }
    } catch {
      // network hiccup — a live stream still surfaces the ask via SSE
    }
  }, [projectId, sessionId]);

  // Fetches the run's queued messages (they live server-side, so they survive
  // reloads and reconnects).
  const refreshQueue = useCallback(async () => {
    if (!sessionId) return;
    try {
      const q = await api.chatQueue(projectId, sessionId);
      setQueued(q.messages ?? []);
      setSteering(q.steering ?? []);
    } catch {
      // transient — the block catches up on the next refresh
    }
  }, [projectId, sessionId]);

  const load = useCallback(async () => {
    if (!sessionId) return;
    // A session switch while this fetch is in flight must not let the old
    // session's messages overwrite the new one's transcript.
    const loadSession = sessionId;
    const loadSeq = ++loadSeqRef.current;
    setLoading(true);
    setLoadError(null);
    try {
      const msgs = await api.getMessages(projectId, sessionId);
      const mapped: Item[] = [];
      let lastUserAt = 0;
      for (const m of msgs) {
        if (m.role === 'tool') {
          const name = m.tool ? parseToolName(m.tool) : 'tool';
          // Pure success acks ({"ok":true,...} with no payload) are noise —
          // the call chip already tells the story. Failures and
          // output-bearing results (read_file, run_command, …) stay.
          let noise = false;
          try {
            const d = JSON.parse(m.content) as Record<string, unknown>;
            noise =
              d !== null &&
              d.ok === true &&
              !('error' in d) &&
              !('content' in d) &&
              !('output' in d);
          } catch {
            // not JSON — keep it
          }
          if (noise) continue;
          const last = mapped[mapped.length - 1];
          if (last && last.kind === 'msg' && last.role === 'assistant') {
            last.toolResults = [...(last.toolResults ?? []), { name, detail: m.content }];
          } else {
            mapped.push({
              kind: 'msg',
              key: m.id,
              role: 'assistant',
              content: '',
              toolResults: [{ name, detail: m.content }],
            });
          }
        } else if (m.role === 'assistant') {
          const item: MsgItem = {
            kind: 'msg',
            key: m.id,
            role: 'assistant',
            content: m.content,
            reasoning: m.reasoning,
            sentAt: Number(m.createdAt) * 1000,
            usage: m.usage ? { ...m.usage, model: m.model || m.usage.model } : undefined,
          };
          // Turn-final messages carry the duration of the turn they closed:
          // the final reply's timestamp minus the triggering user message's.
          if (item.usage && lastUserAt > 0) {
            item.elapsedMs = (Number(m.createdAt) - lastUserAt) * 1000;
          }
          const calls = m.tool ? parseToolCalls(m.tool) : null;
          if (calls) {
            // set_todos repeats are pure progress noise — only the first call
            // of a message is worth a chip. list_files with no path is noise
            // too (it just lists the workspace root).
            let seenTodos = false;
            item.toolCalls = calls.filter((c) => {
              if (c.name === 'set_todos') {
                if (seenTodos) return false;
                seenTodos = true;
              }
              if (c.name === 'list_files' && !meaningfulDetail(c.detail)) return false;
              return true;
            });
          }
          mapped.push(item);
        } else if (m.role === 'user') {
          lastUserAt = Number(m.createdAt);
          const background = m.tool === 'background';
          mapped.push({
            kind: 'msg',
            key: m.id,
            role: 'user',
            content: m.content,
            background,
            sentAt: Number(m.createdAt) * 1000,
            attachments: m.attachments?.map((a, i) => ({
              ...a,
              url: a.kind === 'image' ? messageAttachmentUrl(projectId, m.id, i) : undefined,
            })),
          });
          if (!background) lastUserAt = Number(m.createdAt);
        } else {
          mapped.push({ kind: 'msg', key: m.id, role: 'error', content: m.content });
        }
      }
      // Drop the result if a newer load started or the session changed while
      // the fetch was in flight (switching mid-load shows the old transcript).
      if (loadSeqRef.current !== loadSeq || sessionIdRef.current !== loadSession) return;
      itemsRef.current = mapped;
      setItems(mapped);

      // Runs that finished while the app was away are discovered here (the
      // live SSE event was lost). Notify for the newest one, unless it was
      // already notified live or on a previous load.
      const finished = findLastFinishedAssistant(mapped);
      if (finished) {
        if (lastNotifiedRef.current === null) {
          // First time opening this session — seed the marker without
          // notifying for pre-existing history.
          lastNotifiedRef.current = finished.key;
          try { localStorage.setItem(notifiedKey, finished.key); } catch {}
        } else if (finished.key !== lastNotifiedRef.current) {
          if (finished.error) {
            void notifyTurnError(projectId, sessionId, projectName, finished.content);
          } else {
            void notifyTurnDone(projectId, sessionId, projectName, finished.content);
          }
          lastNotifiedRef.current = finished.key;
          try { localStorage.setItem(notifiedKey, finished.key); } catch {}
        }
      }
    } catch (e) {
      setLoadError(errMsg(e));
    } finally {
      setLoading(false);
      // Re-surface a question the agent asked while the app was away — the
      // card must survive reloads and reconnects, not just live streams.
      void refreshPendingAsk();
    }
  }, [projectId, sessionId, projectName, notifiedKey, refreshPendingAsk]);

  useEffect(() => {
    void load();
    void refreshQueue();
  }, [load, refreshQueue]);

  // Load the chat sessions, restoring the last-used thread for this project
  // when available, otherwise the project's default.
  useEffect(() => {
    api
      .listSessions(projectId)
      .then((res) => {
        const list = res.sessions ?? [];
        setSessions(list);
        // Switching away aborts the old session's live stream so its events
        // can't land on the new session's transcript.
        abortRef.current?.abort();
        setBgRunning([]);
        setSessionId((prev) => {
          if (prev) return prev;
          // Deep link from a notification → open that exact chat.
          const deep = new URLSearchParams(window.location.search).get('session');
          if (deep && list.some((s) => s.id === deep)) return deep;
          const stored = localStorage.getItem(sessionStorageKey(projectId));
          if (stored && list.some((s) => s.id === stored && !s.archived)) return stored;
          const def = list.find((s) => !s.archived)?.id || '';
          if (def) localStorage.setItem(sessionStorageKey(projectId), def);
          return def;
        });
      })
      .catch(() => {});
  }, [projectId]);

  // Starts a fresh chat thread and switches to it.
  const createNewSession = useCallback(async () => {
    setCreatingSession(true);
    try {
      const res = await api.createSession(projectId);
      setSessions((prev) => [...prev, res.session]);
      setSessionId(res.session.id);
      localStorage.setItem(sessionStorageKey(projectId), res.session.id);
      onSessionsOpenChange(false);
    } catch {
      // leave the modal open; the list is unchanged
    } finally {
      setCreatingSession(false);
    }
  }, [projectId, onSessionsOpenChange]);

  // Keep the queue block in sync while a run is active — messages drain to
  // follow-up turns as they finish. The status poll above refreshes it every
  // 3s regardless of streaming state, so a detached tab never goes stale.
  useEffect(() => {
    if (!streaming) return;
    return () => void refreshQueue();
  }, [streaming, refreshQueue]);

  // Context fill for the ring: on mount, after each finished turn, when the
  // popup opens — and whenever the selected model changes (the ring's budget
  // follows the model's context window). Stale responses are dropped so an
  // older fetch can't overwrite a newer one.
  const loadContext = useCallback(
    (force = false) => {
      const req = ++ctxReqRef.current;
      setCtxLoading(true);
      api
        .contextUsage(projectId, sessionId, model.trim() || undefined, providerId || undefined, force)
        .then((c) => {
          if (ctxReqRef.current === req) setCtx(c);
        })
        .catch(() => {})
        .finally(() => {
          if (ctxReqRef.current === req) setCtxLoading(false);
        });
    },
    [projectId, sessionId, model, providerId],
  );
  useEffect(() => {
    loadContext();
  }, [loadContext]);

  // While a generation runs server-side (it survives client disconnects),
  // poll the run status: if a run is active and we are not already streaming
  // it, attach to its live stream — the chat then behaves exactly as if we
  // had never left (thinking, tool rows, composer spinner, all live). When
  // no run is active, refresh the transcript the moment it finishes. The
  // queue/steering block syncs on the same tick so it never goes stale.
  useEffect(() => {
    if (!sessionId) return;
    let cancelled = false;
    let timer: number | undefined;
    let wasRunning = false;
    const tick = async () => {
      if (cancelled) return;
      void refreshQueue();
      try {
        const st = await api.chatStatus(projectId, sessionId);
        if (cancelled) return;
        setRunActive(st.running);
        if (wasRunning && !st.running && !streaming && !watchRef.current) {
          void load(); // the run finished while we were away — fetch the rest
        }
        wasRunning = st.running;
      } catch {
        // transient — try again on the next tick
      }
      timer = window.setTimeout(tick, 3000);
    };
    void tick();
    const onVisible = () => {
      if (!document.hidden) void tick();
    };
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      cancelled = true;
      if (timer) window.clearTimeout(timer);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [projectId, sessionId, load, refreshQueue, streaming]);

  const [bgRunning, setBgRunning] = useState<string[]>([]);
  // Background result rows carry "[Background #<shortid>:" — matches a running
  // entry started earlier in this session.
  const clearFinishedBg = (text: string | undefined) => {
    if (!text) return;
    const m = text.match(/\[Background #([0-9a-z]+):/);
    if (m) setBgRunning((prev) => prev.filter((id) => id !== m[1]));
  };

  // Load the agent-maintained todo list for this project.
  useEffect(() => {
    api
      .getTodos(projectId)
      .then((r) => setTodos(r.todos))
      .catch(() => {
        // no todos yet — leave the panel hidden
      });
  }, [projectId]);

  // Load the project file list and enabled skills for @/# completion and tag
  // validation (only real entries get pill-highlighted).
  useEffect(() => {
    let active = true;
    api
      .listAllFiles(projectId)
      .then((r) => {
        if (active) setFileList(r.entries.map((e) => e.path));
      })
      .catch(() => {});
    api
      .getSettings()
      .then((s) => {
        if (active) {
          setSkillList(
            (s.skills ?? [])
              .filter((x) => x.enabled)
              .map((x) => ({ name: x.name, hint: x.description || x.author })),
          );
          setCurrency(s.llm?.currency ?? 'USD');
        }
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [projectId]);

  const validTag = useMemo(() => {
    const files = new Set(fileList);
    const skills = new Set(skillList.map((s) => s.name));
    return (tag: string) => (tag[0] === '@' ? files.has(tag.slice(1)) : skills.has(tag.slice(1)));
  }, [fileList, skillList]);

  // Auto-scroll on new content, but only while the user is already at (or near)
  // the bottom — otherwise reading history during a stream gets yanked around.
  const nearBottomRef = useRef(true);
  const [showJump, setShowJump] = useState(false);
  // Minimap: a dot strip with one dot per viewport "screen" of the thread
  // (messages are grouped by the screen their top falls into), so the strip
  // stays short on long threads and dots remain circles. The dot for the
  // message nearest the viewport top is lit, and dragging along the strip
  // scrubs. It fades out on any interaction outside the strip or its toggle.
  const [mapOpen, setMapOpen] = useState(false);
  const [buckets, setBuckets] = useState<{ key: string; start: number }[]>([]);
  const [currentDot, setCurrentDot] = useState(-1);
  const [dotSize, setDotSize] = useState(10);
  const stripRef = useRef<HTMLDivElement>(null);
  const bucketRef = useRef<{ key: string }[]>([]);
  const updateCurrent = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const keys: string[] = [];
    const tops: number[] = [];
    for (const it of itemsRef.current) {
      if (it.kind !== 'msg' || it.role !== 'user') continue;
      keys.push(it.key);
      const row = el.querySelector(`[data-msg-key="${CSS.escape(it.key)}"]`) as HTMLElement | null;
      tops.push(row ? row.offsetTop : -1);
    }
    if (keys.length === 0) {
      setBuckets([]);
      setCurrentDot(-1);
      return;
    }
    // Group messages by the viewport bucket their top falls into.
    const h = Math.max(1, el.clientHeight);
    const grouped: { key: string; start: number }[] = [];
    const dotOf: number[] = [];
    let lastBucket = -1;
    for (let i = 0; i < keys.length; i++) {
      const b = tops[i] >= 0 ? Math.floor(tops[i] / h) : 0;
      if (b !== lastBucket) {
        lastBucket = b;
        grouped.push({ key: keys[i], start: i });
      }
      dotOf.push(grouped.length - 1);
    }
    bucketRef.current = grouped;
    setBuckets(grouped);
    const stripH = stripRef.current?.clientHeight ?? 0;
    // Keep the dots at their full size (10px) until they no longer fit with
    // the 8px gap (gap-2 on the strip column), then shrink to fit — floor 3px.
    const n = grouped.length;
    setDotSize(Math.min(10, Math.max(3, (stripH - 24 - (n - 1) * 8) / n)));
    // The dot for the message nearest the viewport top is lit; at the very
    // bottom the last message can sit below the tracking line — light its
    // dot anyway.
    const nearBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - 2;
    let currentIdx = 0;
    for (let i = 0; i < keys.length; i++) {
      if (nearBottom || (tops[i] >= 0 && tops[i] <= el.scrollTop + 80)) currentIdx = i;
    }
    setCurrentDot(dotOf[currentIdx]);
  }, []);
  const onScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const near = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
    nearBottomRef.current = near;
    setShowJump(!near);
    if (mapOpen) updateCurrent();
  }, [mapOpen, updateCurrent]);
  useEffect(() => {
    if (!mapOpen) return;
    const raf = requestAnimationFrame(updateCurrent);
    return () => cancelAnimationFrame(raf);
  }, [items, mapOpen, updateCurrent]);
  // Fade out on interaction anywhere else (capture, so it fires before the
  // target's own handler). The toggle button and the strip itself are exempt.
  useEffect(() => {
    if (!mapOpen) return;
    const onDown = (e: PointerEvent) => {
      const t = e.target as Node;
      if (stripRef.current?.contains(t)) return;
      if (t instanceof Element && t.closest('[data-map-toggle]')) return;
      setMapOpen(false);
    };
    document.addEventListener('pointerdown', onDown, true);
    return () => document.removeEventListener('pointerdown', onDown, true);
  }, [mapOpen]);
  const jumpTo = useCallback((key: string, smooth = true) => {
    const sc = scrollRef.current;
    const row = sc?.querySelector(`[data-msg-key="${CSS.escape(key)}"]`) as HTMLElement | null;
    if (!sc || !row) return;
    // block:'start' would park the message under the top fade — leave room.
    sc.scrollTo({ top: Math.max(0, row.offsetTop - 72), behavior: smooth ? 'smooth' : 'auto' });
  }, []);
  // Scrubbing: map a pointer Y on the strip to a dot (screen bucket) and jump
  // to that bucket's first message.
  const scrubTo = useCallback(
    (clientY: number) => {
      const rect = stripRef.current?.getBoundingClientRect();
      const buckets = bucketRef.current;
      if (!rect || buckets.length === 0) return;
      const ratio = Math.min(0.999, Math.max(0, (clientY - rect.top) / rect.height));
      jumpTo(buckets[Math.floor(ratio * buckets.length)].key, false);
    },
    [jumpTo],
  );
  const jumpToBottom = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    // Re-engage the sticky zone so streaming keeps pinning after the jump.
    nearBottomRef.current = true;
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' });
    // Images and late layout shifts can grow scrollHeight — re-pin once the
    // smooth scroll settles so we land on the true bottom.
    window.setTimeout(() => {
      const el2 = scrollRef.current;
      if (el2 && nearBottomRef.current) el2.scrollTop = el2.scrollHeight;
    }, 350);
  }, []);
  useEffect(() => {
    const el = scrollRef.current;
    if (!el || !nearBottomRef.current) return;
    const pin = () => {
      if (nearBottomRef.current) el.scrollTop = el.scrollHeight;
    };
    pin();
    // scrollHeight can keep growing after new content (images, async layout)
    // — re-pin over a short window so loads and streams land on the true bottom.
    const raf = requestAnimationFrame(pin);
    const timers = [100, 300, 600].map((ms) => setTimeout(pin, ms));
    return () => {
      cancelAnimationFrame(raf);
      timers.forEach(clearTimeout);
    };
  }, [items]);

  // Auto-grow the input textarea (full height while expanded on mobile).
  useEffect(() => {
    const el = taRef.current;
    if (!el) return;
    if (expanded) {
      el.style.height = '100%';
      el.focus();
      return;
    }
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, 160)}px`;
  }, [input, expanded]);

  const finish = useCallback(() => {
    setStreaming(false);
    abortRef.current = null;
    assistantKeyRef.current = null;
    setPermPrompt(null);
    setAskPrompt(null);
    update((prev) =>
      prev.map((it) => (it.kind === 'msg' && it.streaming ? { ...it, streaming: false } : it)),
    );
    // Runs create/edit project files — refresh the list behind @-completion
    // and tag validation.
    api
      .listAllFiles(projectId)
      .then((r) => setFileList(r.entries.map((e) => e.path)))
      .catch(() => {});
  }, [update, projectId]);

  const handleEvent = useCallback(
    (ev: ChatEvent) => {
      // A late event from a previous session (the stream outlived a switch
      // or a queued follow-up fired after we moved on) must not touch the
      // current session's transcript.
      if (sessionIdRef.current !== sessionId) return;
      switch (ev.type) {
        case 'reasoning': {
          let k = assistantKeyRef.current;
          if (!k) {
            k = `s${++counterRef.current}`;
            assistantKeyRef.current = k;
            const nk = k;
            update((prev) => {
              // A new thinking block starts a new round — collapse the
              // previous assistant message's block so only the live one is
              // open.
              let prevKey: string | null = null;
              for (let i = prev.length - 1; i >= 0; i--) {
                const it = prev[i];
                if (it.kind === 'msg' && it.role === 'assistant') {
                  prevKey = it.key;
                  break;
                }
              }
              return [
                ...prev.map((it) =>
                  it.kind === 'msg' && it.role === 'assistant'
                    ? { ...it, reasoningCollapsed: it.key === prevKey ? true : it.reasoningCollapsed, streaming: false }
                    : it,
                ),
                {
                  kind: 'msg',
                  key: nk,
                  role: 'assistant',
                  content: '',
                  reasoning: ev.text,
                  sentAt: Date.now(),
                  streaming: true,
                },
              ];
            });
          } else {
            const ck = k;
            update((prev) =>
              prev.map((it) =>
                it.kind === 'msg' && it.key === ck
                  ? { ...it, reasoning: (it.reasoning ?? '') + ev.text }
                  : it,
              ),
            );
          }
          break;
        }
        case 'delta': {
          let k = assistantKeyRef.current;
          if (!k) {
            k = `s${++counterRef.current}`;
            assistantKeyRef.current = k;
            const nk = k;
            update((prev) => [
              // The final text starts a new round — close any thinking block
              // that's still open from the previous one.
              ...prev.map((it) =>
                it.kind === 'msg' && it.role === 'assistant'
                  ? { ...it, reasoningCollapsed: it.key === nk ? it.reasoningCollapsed : true, streaming: false }
                  : it,
              ),
              { kind: 'msg', key: nk, role: 'assistant', content: '', sentAt: Date.now(), streaming: true },
            ]);
          }
          const ck: string = k;
          update((prev) =>
            prev.map((it) =>
              it.kind === 'msg' && it.key === ck ? { ...it, content: it.content + ev.text } : it,
            ),
          );
          break;
        }
        // The watch connection replays the run's event history (deltas,
        // reasoning, tool calls, side effects) then live events — the same
        // stream a client that stayed on the page receives, so there is no
        // separate snapshot path.
        case 'tool_start': {
          assistantKeyRef.current = null;
          // A tool call begins after the previous round — close any thinking
          // block still open from it.
          update((prev) =>
            prev.map((it) =>
              it.kind === 'msg' && it.role === 'assistant' && !it.streaming
                ? { ...it, reasoningCollapsed: true }
                : it,
            ),
          );
          const key = `t${++counterRef.current}`;
          (toolStackRef.current[ev.name] ||= []).push(key);
          update((prev) => [
            ...prev,
            { kind: 'tool', key, name: ev.name, detail: ev.detail, running: true },
          ]);
          break;
        }
        case 'tool_end': {
          if (ev.name === 'ask_user') {
            // The question block already shows the answers optimistically on
            // confirm; fold the persisted confirmation in here. A failed ask
            // (timeout, cancel) stops showing the answer controls.
            const ans = ev.detail ? askAnswers(ev.detail) : [];
            if (ans.length > 0) {
              update((prev) => prev.map((it) => (it.kind === 'ask' ? { ...it, result: ans } : it)));
            } else if (!ev.ok) {
              update((prev) => prev.map((it) => (it.kind === 'ask' ? { ...it, failed: true } : it)));
            }
            break;
          }
          if (ev.name === 'restart_preview' && ev.ok) restartRef.current();
          const stack = toolStackRef.current[ev.name];
          const key = stack?.pop();
          if (key) {
            update((prev) =>
              prev.map((it) =>
                it.kind === 'tool' && it.key === key
                  ? { ...it, running: false, ok: ev.ok, detail: ev.detail || it.detail }
                  : it,
              ),
            );
          } else {
            update((prev) => [
              ...prev,
              {
                kind: 'tool',
                key: `t${++counterRef.current}`,
                name: ev.name,
                detail: ev.detail,
                running: false,
                ok: ev.ok,
              },
            ]);
          }
          break;
        }
        case 'todos': {
          setTodos(ev.todos);
          break;
        }
        case 'memories': {
          onMemoriesRef.current?.(ev.memories);
          break;
        }
        case 'permission_request': {
          setPermPrompt({ requestId: ev.requestId, tool: ev.tool, detail: ev.detail });
          break;
        }
        case 'project_renamed': {
          onProjectRename?.(ev.text ?? '');
          break;
        }
        case 'session_renamed': {
          const name = ev.text ?? '';
          setSessions((prev) => prev.map((s) => (s.id === sessionIdRef.current ? { ...s, name } : s)));
          onSessionName?.(name);
          break;
        }
        case 'question_request': {
          const questions: AskQuestionView[] =
            ev.questions && ev.questions.length > 0
              ? ev.questions.map((q) => ({ question: q.question, options: q.options ?? [] }))
              : [{ question: ev.text ?? '', options: ev.options ?? [] }];
          setAskPrompt({ requestId: ev.requestId, questions });
          // Replace the bare ask_user tool row with the inline question block.
          const stack = toolStackRef.current['ask_user'] ?? [];
          const key = stack[stack.length - 1];
          if (key) {
            toolStackRef.current['ask_user'] = stack.filter((k) => k !== key);
            const askKey = key;
            update((prev) => {
              const idx = prev.findIndex((it) => it.kind === 'tool' && it.key === askKey);
              if (idx === -1) return prev;
              const next = [...prev];
              next[idx] = { kind: 'ask', key: askKey, questions };
              return next;
            });
          }
          // A user question lands after the previous round — close any
          // thinking block still open from it.
          update((prev) =>
            prev.map((it) =>
              it.kind === 'msg' && it.role === 'assistant' && !it.streaming
                ? { ...it, reasoningCollapsed: true }
                : it,
            ),
          );
          break;
        }
        case 'background_started': {
          // A detached command was dispatched — show it in the running pill
          // until its result row lands.
          const id = ev.text;
          if (id) setBgRunning((prev) => (prev.includes(id) ? prev : [...prev, id]));
          break;
        }
        case 'info': {
          setLocalStatus(ev.text);
          break;
        }
        case 'done': {
          if (ev.usage) {
            const u = ev.usage;
            const k = assistantKeyRef.current;
            if (k) {
              update((prev) =>
                prev.map((it) =>
                  it.kind === 'msg' && it.key === k
                    ? {
                        ...it,
                        usage: u,
                        elapsedMs: turnStartRef.current > 0 ? Date.now() - turnStartRef.current : undefined,
                      }
                    : it,
                ),
              );
            }
          }
          let snippet = '';
          for (let i = itemsRef.current.length - 1; i >= 0; i--) {
            const it = itemsRef.current[i];
            if (it.kind === 'msg' && it.role === 'assistant') {
              snippet = it.content;
              break;
            }
          }
          void notifyTurnDone(projectId, sessionId, projectName, snippet);
          // The live event notified — mark it so the reconnect load() path
          // does not notify a second time for the same turn.
          const doneKey = assistantKeyRef.current;
          if (doneKey) {
            lastNotifiedRef.current = doneKey;
            try { localStorage.setItem(notifiedKey, doneKey); } catch {}
          }
          void loadContext(true); // bypass the cache: usage changed this turn
          setAskPrompt(null); // the turn ended — no question can be pending
          setSteering([]); // the turn ended — nothing is pending injection
          setLocalStatus(null); // drop the transient info notice (e.g. continuation)
          void refreshQueue(); // queued messages drained into follow-up turns
          finish();
          break;
        }
        case 'error': {
          update((prev) => [
            ...prev,
            { kind: 'msg', key: `e${++counterRef.current}`, role: 'error', content: ev.error },
          ]);
          void notifyTurnError(projectId, sessionId, projectName, ev.error ?? '');
          const errorKey = `e${counterRef.current}`;
          lastNotifiedRef.current = errorKey;
          try { localStorage.setItem(notifiedKey, errorKey); } catch {}
          setAskPrompt(null);
          setSteering([]);
          setLocalStatus(null);
          void refreshQueue();
          finish();
          break;
        }
        case 'injected_message': {
          // The agent added a user message mid-turn (a screenshot from the
          // screenshot_app tool) — render it like any other user turn. If the
          // text matches a pending steer, that steer has landed and the row
          // gets a "steered" badge.
          const id = ev.messageId ?? 0;
          clearFinishedBg(ev.text);
          const steered = ev.text ? steeringRef.current.some((s) => s.text === ev.text) : false;
          if (ev.text) setSteering((prev) => prev.filter((s) => s.text !== ev.text));
          update((prev) => [
            // A screenshot/injected message closes any thinking block that was
            // still open from the previous round.
            ...prev.map((it) =>
              it.kind === 'msg' && it.role === 'assistant' && !it.streaming
                ? { ...it, reasoningCollapsed: true }
                : it,
            ),
            {
              kind: 'msg',
              key: id > 0 ? String(id) : `i${++counterRef.current}`,
              role: 'user',
              content: ev.text ?? '',
              steered,
              background: ev.tool === 'background',
              sentAt: Date.now(),
              attachments: ev.attachments?.map((a, i) => ({
                ...a,
                url: a.kind === 'image' && id > 0 ? messageAttachmentUrl(projectId, String(id), i) : undefined,
              })),
            },
          ]);
          break;
        }
      }
    },
    [update, finish, projectId, sessionId, projectName, loadContext, refreshQueue, onProjectRename],
  );

  useEffect(() => {
    handleEventRef.current = handleEvent;
  }, [handleEvent]);

  // Returning to a running chat: subscribe to the run's live stream. The hub
  // replays its event history so the attach continues the stream exactly
  // where it was; the stream ends when the run does. The effect deliberately does not depend on `streaming`
  // or `handleEvent` — the watch sets streaming itself and must not abort its
  // own stream when that state flips (refs keep both current). The run is
  // re-checked at attach time so a turn we just streamed ourselves (whose
  // runActive state is still settling) never triggers a spurious watch.
  useEffect(() => {
    if (!sessionId || !runActive || streamingRef.current || resuming) return;
    if (watchRef.current) return; // already attached
    let cancelled = false;
    void api.chatStatus(projectId, sessionId).then((st) => {
      if (cancelled || !st.running) return;
      const ctrl = new AbortController();
      watchRef.current = ctrl;
      setStreaming(true);
      let done = false;
      void watchChat(projectId, sessionId, handleEventRef.current, ctrl.signal)
        .catch(() => {
          // connection drop — the transcript refreshes below
        })
        .finally(() => {
          if (watchRef.current === ctrl) watchRef.current = null;
          setStreaming(false);
          if (!done) void load();
        });
      return () => {
        done = true;
        ctrl.abort();
        if (watchRef.current === ctrl) watchRef.current = null;
      };
    });
    return () => {
      cancelled = true;
    };
  }, [sessionId, runActive, resuming, projectId, load]);


  // Leaving the page cancels any in-flight generation: the server aborts the
  // run, so no zombie stream keeps running in the background and its
  // dead-connection error can't surface as "Failed to fetch" on return.
  useEffect(() => {
    return () => abortRef.current?.abort();
  }, []);

  // resumeTurn finishes the last user turn after a lost connection — silently:
  // its events go nowhere, and the persisted result is reloaded once it
  // finishes, so the user never sees the generation replay. The original run
  // survives client disconnects (server-side), so the retry usually answers
  // "run active" (409) — that means the run is alive and will finish on its
  // own, so we wait and re-check. Only a truly dead run (server restart,
  // crash) gets re-run.
  const resumeTurn = useCallback(
    async (signal: AbortSignal): Promise<boolean> => {
      let waiting = 0;
      let netDown = 0;
      for (;;) {
        if (signal.aborted) return false;
        if (await turnCompleted(projectId, sessionId)) return true;
        try {
          await retryChat(projectId, sessionId, () => {}, signal);
          return true;
        } catch (e) {
          if (e instanceof DOMException && e.name === 'AbortError') return false;
          const status = (e as { status?: number }).status;
          if (status === 409) {
            // The old run is still going — give it time to finish.
            waiting++;
            if (waiting > 40) return false; // ~3.5 min
            await new Promise((r) => setTimeout(r, 5000));
            continue;
          }
          if (e instanceof TypeError) {
            netDown++;
            if (netDown > 6) return false;
            await new Promise((r) => setTimeout(r, 3000));
            continue;
          }
          return false;
        }
      }
    },
    [projectId, sessionId],
  );

  const run = useCallback(
    async (start: (signal: AbortSignal) => Promise<void>) => {
      if (streaming) return;
      setStreaming(true);
      turnStartRef.current = Date.now();
      assistantKeyRef.current = null;
      const ctrl = new AbortController();
      abortRef.current = ctrl;
      try {
        await start(ctrl.signal);
        finish();
      } catch (e) {
        const aborted = e instanceof DOMException && e.name === 'AbortError';
        if (!aborted && e instanceof TypeError && !ctrl.signal.aborted) {
          // The connection died (backgrounded tab, suspended app). Resync the
          // chat from the server instantly; if the turn already completed
          // while we were away, that's all there is to do. Otherwise finish
          // it in the background — streaming the retry live would replay the
          // whole generation in front of the user.
          await load();
          // The resync can fail while the network is still down — that is
          // expected, not an error worth showing.
          setLoadError(null);
          if (await turnCompleted(projectId, sessionId)) {
            finish();
            return;
          }
          setResuming(true);
          assistantKeyRef.current = null;
          toolStackRef.current = {};
          const ok = await resumeTurn(ctrl.signal);
          setResuming(false);
          if (ok) {
            await load();
            finish();
            return;
          }
        }
        update((prev) => [
          ...prev,
          {
            kind: 'msg',
            key: `e${++counterRef.current}`,
            role: 'error',
            content: aborted ? 'Generation stopped.' : streamErrorMsg(e),
          },
        ]);
        finish();
      }
    },
    [streaming, finish, update, resumeTurn, load, sessionId, projectName, notifiedKey],
  );

  const sendText = useCallback(
    (text: string) => {
      const trimmed = text.trim();
      if (!trimmed || streaming || !llmReady || !modelOverride) return;
      setInput('');
      setAskPrompt(null); // a new turn supersedes any pending question
      const atts = attachments.length > 0 ? [...attachments] : undefined;
      setAttachments([]);
      setAttachError(null);
      update((prev) => [
        ...prev,
        {
          kind: 'msg',
          key: `u${++counterRef.current}`,
          role: 'user',
          content: trimmed,
          sentAt: Date.now(),
          attachments: atts?.map((a) => ({
            name: a.name,
            mime: a.mime,
            kind: a.kind,
            size: a.content.length,
            url: a.kind === 'image' ? `data:${a.mime};base64,${a.content}` : undefined,
          })),
        },
      ]);
      void run((signal) =>
        streamChat(
          projectId,
          sessionId,
          trimmed,
          {
            model: modelOverride,
            providerId: providerOverride,
            attachments: atts,
            thinking: thinkingEffort || undefined,
          },
          handleEvent,
          signal,
        ),
      );
    },
    [streaming, llmReady, projectId, sessionId, modelOverride, providerOverride, handleEvent, run, update, attachments, thinkingEffort],
  );

  // Auto-send the New project dialog's "what do you want to create?"
  // description once the initial history and model selection are ready.
  const initialSentRef = useRef(false);
  useEffect(() => {
    if (!initialPrompt || initialSentRef.current) return;
    if (loading || !llmReady || !modelOverride || !sessionId) return;
    initialSentRef.current = true;
    sendText(initialPrompt);
  }, [initialPrompt, loading, llmReady, modelOverride, sessionId, sendText]);

  const addFiles = async (files: FileList | null) => {
    if (!files || files.length === 0) return;
    setAttachError(null);
    let list = Array.from(files);
    if (!supportsImages) {
      const rejected = list.filter((f) => f.type.startsWith('image/'));
      list = list.filter((f) => !f.type.startsWith('image/'));
      if (rejected.length > 0) {
        setAttachError(
          `${rejected[0].name}: this model does not support images — attach text files instead.`,
        );
        if (list.length === 0) return;
      }
    }
    const results = await Promise.all(list.map(readAttachment));
    setAttachments((prev) => {
      const next = [...prev];
      let firstError: string | null = null;
      for (const r of results) {
        if ('error' in r) {
          firstError ??= r.error;
          continue;
        }
        if (next.length >= MAX_ATTACHMENTS) {
          firstError ??= `Too many attachments (max ${MAX_ATTACHMENTS}).`;
          break;
        }
        next.push(r.value);
      }
      if (firstError) setAttachError(firstError);
      return next;
    });
  };

  const [localStatus, setLocalStatus] = useState<string | null>(null);
  const runLocalCommand = useCallback(async (text: string): Promise<boolean> => {
    const command = text.trim().split(/\s+/)[0];
    if (!CHAT_COMMANDS.some((c) => c.name === command)) return false;
    switch (command) {
      case '/help':
        setLocalStatus(CHAT_COMMANDS.map((c) => `${c.name} ${c.hint.toLowerCase()}`).join(' · '));
        break;
      case '/model':
        setModelPickerOpen(true);
        break;
      case '/tools':
        openTools('mcp');
        break;
      case '/preview':
        restartRef.current?.();
        setLocalStatus('Preview restarting…');
        break;
      case '/stop':
        if (streaming) {
          abortRef.current?.abort();
          setLocalStatus('Stopped.');
        } else {
          setLocalStatus('Nothing is running.');
        }
        break;
      case '/clear':
        if (streaming) {
          setLocalStatus('Stop the current run before clearing.');
          break;
        }
        try {
          await api.truncateMessages(projectId, sessionId, 0);
          // History is gone — nothing left to notify about.
          try { localStorage.removeItem(notifiedKey); } catch {}
          lastNotifiedRef.current = null;
          await load();
          setLocalStatus('Chat cleared.');
        } catch (e) {
          setLocalStatus(errMsg(e));
        }
        break;
      case '/compact':
        if (streaming) {
          setLocalStatus('Stop the current run before compacting.');
          break;
        }
        setLocalStatus('Compacting conversation…');
        try {
          await api.compact(projectId, sessionId);
          loadContext();
          setLocalStatus('Conversation compacted.');
        } catch (e) {
          setLocalStatus(errMsg(e));
        }
        break;
      case '/plan':
        // Not a local command — the message goes to the agent as-is.
        return false;
    }
    setInput('');
    return true;
  }, [projectId, sessionId, streaming, load, loadContext]);

  const send = useCallback(async () => {
    if (await runLocalCommand(input)) return;
    if (streaming) {
      // Mid-run: the server steers the message into the current turn or
      // queues it as a follow-up; it renders via injected_message when
      // consumed. Attachments can't ride along — they need a fresh turn.
      const text = input.trim();
      if (!text) return;
      if (attachments.length > 0) {
        setAttachError('Attachments can only be sent when no run is active.');
        return;
      }
      setInput('');
      setSuggestions([]);
      try {
        const res = await api.queueChat(projectId, sessionId, text, modelOverride, providerOverride);
        // Show it in the queue block right away; it drains in order when the
        // run finishes (or can be steered into the current run).
        if (res.queued) {
          setQueued((prev) => [...prev, { id: res.id ?? '', text }]);
        } else {
          void refreshQueue();
        }
        setExpanded(false);
      } catch (e) {
        setInput(text);
        setLoadError(errMsg(e));
      }
      return;
    }
    sendText(input);
    setExpanded(false);
  }, [input, streaming, attachments.length, runLocalCommand, sendText, projectId, sessionId, modelOverride, providerOverride, refreshQueue]);

  // Moves a queued message up/down (optimistically; the server is the source
  // of truth on the next refresh).
  const moveQueued = useCallback(
    async (idx: number, dir: -1 | 1) => {
      setQueued((prev) => {
        const j = idx + dir;
        if (j < 0 || j >= prev.length) return prev;
        const next = [...prev];
        [next[idx], next[j]] = [next[j], next[idx]];
        void api
          .chatQueueReorder(projectId, sessionId, next.map((m) => m.id))
          .catch(() => void refreshQueue());
        return next;
      });
    },
    [projectId, sessionId, refreshQueue],
  );

  // Replaces one queued message's text in place. Saving also releases the
  // hold taken when the edit started, so the message can be sent again.
  const editQueued = useCallback(
    async (id: string, text: string) => {
      const trimmed = text.trim();
      if (!trimmed) return;
      setQueued((prev) => prev.map((m) => (m.id === id ? { ...m, text: trimmed } : m)));
      try {
        await api.chatQueueEdit(projectId, sessionId, id, trimmed);
      } catch {
        void refreshQueue();
      }
      void api.chatQueueHold(projectId, sessionId, id, false).catch(() => {});
    },
    [projectId, sessionId, refreshQueue],
  );

  // Mark a queued message held (being edited) so the server's follow-up
  // drain skips it, and release the hold when the edit ends.
  const setQueueEditing = useCallback(
    (id: string | null, text: string) => {
      setQueueEditId((prev) => {
        if (prev && prev !== id) {
          void api.chatQueueHold(projectId, sessionId, prev, false).catch(() => {});
        }
        return id;
      });
      queueEditIdRef.current = id;
      setQueueEditText(text);
      if (id) {
        void api.chatQueueHold(projectId, sessionId, id, true).catch(() => {});
      }
    },
    [projectId, sessionId],
  );

  // Leaving the page abandons any in-progress edit — release the hold so the
  // message isn't stuck.
  useEffect(() => {
    return () => {
      if (queueEditIdRef.current) {
        void api.chatQueueHold(projectId, sessionId, queueEditIdRef.current, false).catch(() => {});
      }
    };
  }, [projectId, sessionId]);

  // Exports a diagnostics dump of this chat (version, provider, run state,
  // session messages) as a JSON file — for reporting chats that end oddly.
  const exportDiagnostics = useCallback(async () => {
    if (!sessionId) return;
    try {
      const dump = await api.chatDiagnostics(projectId, sessionId);
      const blob = new Blob([JSON.stringify(dump, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `v1-diagnostics-${projectId}-${new Date().toISOString().replace(/[:.]/g, '-')}.json`;
      a.click();
      URL.revokeObjectURL(url);
      setLocalStatus('Diagnostics downloaded.');
    } catch (e) {
      setLocalStatus(errMsg(e));
    }
  }, [projectId, sessionId]);

  // Steers one queued message into the current run: the agent picks it up at
  // the next round boundary instead of waiting for the queue. The message
  // shows as "steering" until it lands as an injected message.
  const steerQueued = useCallback(
    async (id: string) => {
      const entry = queued.find((m) => m.id === id);
      try {
        await api.chatQueueSteer(projectId, sessionId, id);
        setQueued((prev) => prev.filter((m) => m.id !== id));
        if (entry) setSteering((prev) => [...prev, { id: entry.id, text: entry.text }]);
      } catch {
        void refreshQueue();
      }
    },
    [projectId, queued, refreshQueue],
  );

  // Deletes a queued follow-up so it is never sent.
  const deleteQueued = useCallback(
    async (id: string) => {
      setQueued((prev) => prev.filter((m) => m.id !== id));
      try {
        await api.chatQueueDelete(projectId, sessionId, id);
      } catch {
        void refreshQueue();
      }
    },
    [projectId, sessionId, refreshQueue],
  );

  const updateSuggestions = useCallback((value: string) => {
    const cursor = taRef.current?.selectionStart ?? value.length;
    const before = value.slice(0, cursor);
    const tokenMatch = before.match(/(?:^|\s)([@#/][^\s]*)$/);
    if (!tokenMatch) { setSuggestions([]); return; }
    const token = tokenMatch[1];
    const query = token.slice(1).toLowerCase();
    setSuggestionIndex(0);
    if (token[0] === '/') {
      setSuggestions(
        CHAT_COMMANDS.filter((c) => c.name.slice(1).startsWith(query)).map((c) => ({
          // /plan takes a prompt after it — insert the trailing space so the
          // user can type straight away.
          insert: c.name === '/plan' ? '/plan ' : c.name,
          label: c.name,
          hint: c.hint,
        })),
      );
      return;
    }
    if (token[0] === '@') {
      setSuggestions(
        fileList
          .filter((p) => p.toLowerCase().includes(query))
          .slice(0, 8)
          .map((p) => ({ insert: `@${p}`, label: p })),
      );
      return;
    }
    setSuggestions(
      skillList
        .filter((x) => x.name.toLowerCase().includes(query))
        .slice(0, 8)
        .map((x) => ({ insert: `#${x.name}`, label: `#${x.name}`, hint: x.hint })),
    );
  }, [fileList, skillList]);

  // Pick the highlighted suggestion: slash commands run right away (they take
  // no arguments), @/# tokens complete in the textarea.
  const chooseSuggestion = useCallback((suggestion: string) => {
    const textarea = taRef.current;
    const cursor = textarea?.selectionStart ?? input.length;
    const before = input.slice(0, cursor);
    const match = before.match(/(?:^|\s)([@#/][^\s]*)$/);
    if (!match) return;
    const start = cursor - match[1].length;
    const next = input.slice(0, start) + suggestion + ' ' + input.slice(cursor);
    setInput(next);
    setSuggestions([]);
    requestAnimationFrame(() => {
      textarea?.focus();
      const nextCursor = start + suggestion.length + 1;
      textarea?.setSelectionRange(nextCursor, nextCursor);
    });
  }, [input]);

  // Pick the highlighted suggestion: slash commands run right away (they take
  // no arguments), @/# tokens complete in the textarea.
  const acceptSuggestion = useCallback(
    (s: Suggestion) => {
      if (s.insert.startsWith('/')) {
        setSuggestions([]);
        void runLocalCommand(s.insert);
      } else {
        chooseSuggestion(s.insert);
      }
    },
    [runLocalCommand, chooseSuggestion],
  );

  const regenerate = useCallback(() => {
    if (streaming || !llmReady) return;
    update((prev) => {
      let idx = -1;
      for (let i = prev.length - 1; i >= 0; i--) {
        const it = prev[i];
        if (it.kind === 'msg' && it.role === 'assistant') {
          idx = i;
          break;
        }
      }
      if (idx === -1) return prev;
      return prev.map((it, i) => (i === idx ? { ...it, stale: true } : it));
    });
    setAskPrompt(null); // re-running the turn supersedes any pending question
    void run(async (signal) => {
      await retryChat(projectId, sessionId, handleEvent, signal);
      // A continued retry folds the partial + continuation into one message
      // and drops the error — reload to show that merged state.
      await load();
    });
  }, [streaming, llmReady, projectId, sessionId, handleEvent, run, update, load]);

  const setItemEditing = useCallback(
    (key: string, editing: boolean) => {
      update((prev) =>
        prev.map((it) => (it.kind === 'msg' && it.key === key ? { ...it, editing } : it)),
      );
    },
    [update],
  );

  // Editing a persisted user message rewinds the thread to it (drops the
  // display tail) and re-runs from the edited text via editMessageId.
  const editUserMessage = useCallback(
    (key: string, text: string) => {
      const trimmed = text.trim();
      const id = Number(key);
      if (!trimmed || streaming || !llmReady || !persisted(key)) return;
      setItemEditing(key, false);
      update((prev) => {
        const idx = prev.findIndex((it) => it.kind === 'msg' && it.key === key);
        if (idx === -1) return prev;
        const edited: MsgItem = {
          ...(prev[idx] as MsgItem),
          kind: 'msg',
          role: 'user',
          content: trimmed,
          editing: false,
          stale: false,
          toolCalls: undefined,
          toolResults: undefined,
        };
        return prev.slice(0, idx).concat(edited);
      });
      toolStackRef.current = {};
      assistantKeyRef.current = null;
      void run((signal) =>
        streamChat(
          projectId,
          sessionId,
          trimmed,
          {
            model: modelOverride,
            providerId: providerOverride,
            editMessageId: id,
            thinking: thinkingEffort || undefined,
          },
          handleEvent,
          signal,
        ),
      );
    },
    [streaming, llmReady, projectId, modelOverride, providerOverride, handleEvent, run, update, thinkingEffort],
  );

  // Rewind/revert: cut the thread back to a chosen message (drops everything
  // after it) without re-running.
  const rewindTo = useCallback(
    async (key: string) => {
      const id = Number(key);
      if (!persisted(key)) return;
      try {
        await api.truncateMessages(projectId, sessionId, id);
        await load();
      } catch (e) {
        setLoadError(errMsg(e));
      }
    },
    [projectId, load],
  );

  // Rewinds immediately, or opens the approval dialog when the setting
  // requires one.
  const [rewindTarget, setRewindTarget] = useState<{ key: string; text: string } | null>(null);
  const requestRewind = useCallback(
    (key: string, text: string) => {
      if (rewindApproval) {
        setRewindTarget({ key, text });
      } else {
        void rewindTo(key);
      }
    },
    [rewindApproval, rewindTo],
  );

  const stop = () => {
    // Runs survive the stream abort (they are detached server-side) — tell
    // the server to cancel explicitly, then drop the connection.
    void api.stopChat(projectId, sessionId).catch(() => {});
    abortRef.current?.abort();
  };

  const compactNow = async () => {
    setCtxCompacting(true);
    try {
      await api.compact(projectId, sessionId);
      loadContext();
      setCtxOpen(false);
    } catch (e) {
      setLocalStatus(errMsg(e));
    } finally {
      setCtxCompacting(false);
    }
  };

  const respondPerm = useCallback(
    async (allow: boolean) => {
      const p = permPrompt;
      if (!p) return;
      try {
        await api.permissionRespond(projectId, p.requestId, allow);
      } catch {
        // 404 — already answered; treat as resolved
      }
      setPermPrompt(null);
    },
    [permPrompt, projectId],
  );

  const answerAsk = useCallback(
    async (answers: AskAnswerView[]) => {
      const a = askPrompt;
      const clean = answers.filter((x) => x.answer.trim() !== '');
      if (!a || clean.length === 0) return;
      // Show the answers in the transcript block right away — the persisted
      // tool result catches up on the next load.
      update((prev) => {
        // Update the most recent open ask — live item or the last message
        // carrying an ask_user call.
        for (let i = prev.length - 1; i >= 0; i--) {
          const it = prev[i];
          if (it.kind === 'ask') {
            const next = [...prev];
            next[i] = { ...it, result: clean };
            return next;
          }
          if (
            it.kind === 'msg' &&
            it.role === 'assistant' &&
            it.toolCalls?.some((c) => c.name === 'ask_user')
          ) {
            const next = [...prev];
            next[i] = {
              ...it,
              toolResults: [
                ...(it.toolResults ?? []),
                { name: 'ask_user', detail: JSON.stringify({ answers: clean }) },
              ],
            };
            return next;
          }
        }
        return prev;
      });
      setAskPrompt(null);
      try {
        await api.askRespond(projectId, sessionId, a.requestId, clean);
      } catch {
        // 404 — already answered or timed out
      }
    },
    [askPrompt, projectId, sessionId, update],
  );

  // Keys of the user's messages, in order — the minimap shows one dot each.
  const userKeys = useMemo(
    () =>
      items.filter((it) => it.kind === 'msg' && it.role === 'user').map((it) => it.key),
    [items],
  );

  // Keys of the assistant messages that closed their turn — the usage line
  // (per-turn token counts) only shows on these. Tool-carrying rounds are never
  // turn-final: a message with tool calls always continues the turn, and the
  // usage-only markers on older history must not split collapsed-tool groups.
  const turnEndKeys = useMemo(() => {
    const keys = new Set<string>();
    let pending = true;
    let lastMsg = true; // the newest message in the list
    for (let i = items.length - 1; i >= 0; i--) {
      const it = items[i];
      if (it.kind !== 'msg') continue;
      if (it.role === 'user') {
        pending = true;
      } else if (it.role === 'assistant') {
        // While a run is active, the newest message is mid-turn (its text
        // isn't persisted until the round ends, so the last row looks final
        // on a return/reload) — its totals must not print yet.
        const toolRound = (it.toolCalls?.length ?? 0) > 0;
        if (pending && !toolRound && !(lastMsg && (streaming || runActive))) keys.add(it.key);
        pending = false;
      }
      lastMsg = false;
    }
    return keys;
  }, [items, streaming, runActive]);

  // True when a message renders as a collapsed tool summary row (the tool
  // collapse setting is on and the message carries tools, ask included).
  const isCollapsedToolsRow = useCallback(
    (it: Item): boolean => {
      if (it.kind !== 'msg' || it.role !== 'assistant' || it.streaming) return false;
      if (!getToolCallsCollapsed()) return false;
      const askCount = it.toolCalls?.filter((c) => c.name === 'ask_user').length ?? 0;
      const calls = (it.toolCalls ?? []).filter((c) => c.name !== 'ask_user');
      const results = (it.toolResults ?? []).filter((r) => r.name !== 'ask_user');
      return calls.length + results.length > 0 || askCount > 0;
    },
    [],
  );

  // Composer pieces, shared between the normal row layout and the expanded
  // layout (top button row in the corners, text field below at full width).
  const plusButton = (
    <IconButton
      onClick={() => {
        // Reopening the + menu dismisses any sub-menu first.
        setTodosOpen(false);
        setThinkingOpen(false);
        setPlusOpen((o) => !o);
      }}
      aria-label="More actions"
      title="More actions"
      className={`relative z-30 h-8! w-8! shrink-0 md:h-9! md:w-9! ${
        plusOpen ? 'bg-border text-text' : ''
      }`}
    >
      <IconPlus className="h-4 w-4" />
    </IconButton>
  );
  const collapseButton = (
    <IconButton
      onClick={() => setExpanded(false)}
      aria-label="Collapse composer"
      title="Collapse composer"
      className="h-8! w-8! shrink-0 md:h-9! md:w-9!"
    >
      <IconCompress className="h-4 w-4" />
    </IconButton>
  );
  const toolsButton = (
    <IconButton
      onClick={() => openTools('mcp')}
      aria-label="Tools, skills & permissions"
      title="Tools, skills & permissions"
      className="h-8! w-8! shrink-0 md:h-9! md:w-9!"
    >
      <IconWrench className="h-4 w-4" />
    </IconButton>
  );
  const attachButton = (
    <IconButton
      onClick={() => fileRef.current?.click()}
      disabled={!hasModel}
      aria-label="Attach a file"
      title={
        !hasModel
          ? 'Select a model first'
          : supportsImages
            ? 'Attach a file (image or text)'
            : 'Attach a text file (this model does not support images)'
      }
      className="h-8! w-8! shrink-0 md:h-9! md:w-9!"
    >
      <IconPaperclip className="h-4 w-4" />
    </IconButton>
  );
  const tasksButton = todos.length > 0 && (
    <IconButton
      onClick={() => {
        setPlusOpen(false);
        setThinkingOpen(false);
        setTodosOpen((o) => !o);
      }}
      aria-label="Todos"
      title={`Todos (${todos.filter((t) => !t.done).length} left)`}
      className={`relative z-30 h-8! w-8! shrink-0 md:h-9! md:w-9! ${
        todosOpen ? 'bg-border text-text' : ''
      }`}
    >
      <IconCheck className="h-4 w-4" />
    </IconButton>
  );
  const stopButton = streaming && (
    <IconButton
      onClick={stop}
      aria-label="Stop generating"
      title="Stop generating"
      className="h-8! w-8! shrink-0 md:h-9! md:w-9!"
    >
      <IconSquare className="h-4 w-4" />
    </IconButton>
  );
  // Compact thinking toggle for the expanded composer row (the header shows
  // the level as text instead). Mirrors the todos button: toggles, sits above
  // the popup backdrop, and lights up while its popup is open.
  const thinkingIconButton = showThinking && (
    <IconButton
      disabled={thinkingLoading}
      onClick={() => {
        setPlusOpen(false);
        setTodosOpen(false);
        setThinkingOpen((o) => !o);
      }}
      aria-label="Thinking level"
      title={`Thinking: ${thinkingLoading ? '…' : thinkingLabel || 'off'} — click to change`}
      className={`relative z-30 h-8! w-8! shrink-0 md:h-9! md:w-9! ${
        thinkingOpen ? 'bg-border' : ''
      } ${thinkingColor.text}`}
    >
      <IconBrain className={`h-4 w-4 ${thinkingLoading ? 'animate-spin' : ''}`} />
    </IconButton>
  );
  const sendButton = (
    <IconButton
      onClick={() => void send()}
      disabled={!input.trim() || !hasModel}
      aria-label={streaming ? 'Steer or queue for the next turn' : 'Send message'}
      title={
        streaming
          ? 'Send to steer the current run — it becomes a follow-up turn if the run finishes first'
          : hasModel
            ? 'Send message'
            : 'Select a model first'
      }
      className="h-8! w-8! shrink-0 bg-primary text-primary-text hover:opacity-90 hover:text-primary-text disabled:bg-border disabled:text-faint md:h-9! md:w-9!"
    >
      <IconArrowUp className="h-4 w-4" />
    </IconButton>
  );

  const textField = (
    <div className={`relative min-w-0 flex-1 ${expanded ? 'h-full min-h-0 w-full' : ''}`}>
      {/* The textarea's own text is transparent; this echo beneath it
          renders the same text with @/# tags pilled. The echo must
          keep the textarea's exact font/padding metrics — the pill
          uses negative margins to cancel its padding, otherwise the
          caret and selection drift from the visible text. */}
      <div
        ref={echoRef}
        aria-hidden
        className="pointer-events-none absolute inset-0 overflow-hidden whitespace-pre-wrap break-words px-1.5 py-1 text-base sm:text-sm md:py-1.5"
      >
        <TaggedText
          text={input}
          pillClassName="-mx-0.5 rounded-sm bg-border px-0.5 text-accent"
          valid={validTag}
        />
        {'​'}
      </div>
      <textarea
        ref={taRef}
        rows={1}
        value={input}
        autoCorrect="off"
        placeholder="Describe what to build…"
        onChange={(e) => {
          setInput(e.target.value);
          void updateSuggestions(e.target.value);
        }}
        onPaste={(e) => {
          const text = e.clipboardData.getData('text/plain');
          // Large pastes become a text attachment instead of flooding the
          // composer — the sent message references it as a highlighted chip.
          if (!text || (text.length <= 1200 && text.split('\n').length <= 40)) return;
          e.preventDefault();
          if (text.length > MAX_ATTACHMENT_BYTES) {
            setAttachError('Pasted text is too large to attach (max 2 MB).');
            return;
          }
          setAttachments((prev) => {
            if (prev.length >= MAX_ATTACHMENTS) {
              setAttachError(`Too many attachments (max ${MAX_ATTACHMENTS}).`);
              return prev;
            }
            const name = `paste-${new Date().toISOString().replace(/[:.]/g, '-')}.txt`;
            return [...prev, { name, mime: 'text/plain', kind: 'text', content: text }];
          });
        }}
        onKeyDown={(e) => {
          if (suggestions.length > 0 && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
            e.preventDefault();
            setSuggestionIndex((i) => (e.key === 'ArrowDown'
              ? (i + 1) % suggestions.length
              : (i + suggestions.length - 1) % suggestions.length));
            return;
          }
          if (suggestions.length > 0 && e.key === 'Tab') {
            e.preventDefault();
            chooseSuggestion(suggestions[suggestionIndex].insert);
            return;
          }
          if (suggestions.length > 0 && e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            acceptSuggestion(suggestions[suggestionIndex]);
            return;
          }
          if (e.key === 'Escape' && suggestions.length > 0) {
            e.preventDefault();
            setSuggestions([]);
            return;
          }
          // Markdown list continuation: starting a new line inside a list
          // item autofills the bullet/number marker (keeping the indent);
          // an empty item ends the list instead.
          if (e.key === 'Enter' && (e.shiftKey || !isDesktop)) {
            const ta = taRef.current;
            if (ta && suggestions.length === 0) {
              const selStart = ta.selectionStart;
              const before = input.slice(0, selStart);
              const lineStart = before.lastIndexOf('\n') + 1;
              const line = before.slice(lineStart);
              const m = line.match(/^([ \t]*)([-*+]|\d+[.)])\s+(.*)$/);
              if (m && m[2].trim() !== '') {
                // Insert the real newline plus the continued marker — the
                // default Enter handling is suppressed, so the line break
                // must be added here or the marker lands glued to the text.
                const marker = m[1] + m[2] + ' ';
                const pos = selStart + 1 + marker.length;
                e.preventDefault();
                setInput(input.slice(0, selStart) + '\n' + marker + input.slice(ta.selectionEnd));
                requestAnimationFrame(() => {
                  ta.selectionStart = pos;
                  ta.selectionEnd = pos;
                });
                return;
              }
            }
          }
          if (e.key === 'Enter' && !e.shiftKey && isDesktop) {
            e.preventDefault();
            void send();
          }
        }}
        onScroll={(e) => {
          const echo = echoRef.current;
          if (echo) echo.scrollTop = e.currentTarget.scrollTop;
        }}
        className={`relative block min-h-[32px] w-full resize-none bg-transparent px-1.5 py-1 text-base text-transparent caret-accent outline-none placeholder:text-faint sm:text-sm md:min-h-[36px] md:py-1.5 ${
          expanded ? 'h-full max-h-none' : 'max-h-40'
        }`}
      />
    </div>
  );

  // Ring/border hue for the context button — matches the ring's current
  // fill color so the button border and the ring always agree.
  const ctxPct = ctx && !ctxLoading ? Math.min(100, (ctx.used / ctx.budget) * 100) : 0;
  const ctxHue = 120 - (120 * ctxPct) / 100;
  const ctxBorder = `hsl(${ctxHue} 70% 50% / 0.45)`;

  return (
    <div className="relative flex h-full min-h-0 flex-col">
      {llmReady && (
        <div className="shrink-0 border-b border-border px-3 py-1.5 md:px-4">
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => setModelPickerOpen(true)}
              aria-label="Choose model"
              title={`Model: ${modelLabel}`}
              className="flex min-h-[36px] min-w-0 flex-1 items-center gap-2 rounded-md border border-border bg-surface px-2.5 py-1 text-left transition-colors focus:border-subtle"
            >
              <IconModel className="hidden h-3.5 w-3.5 shrink-0 text-dim sm:block" />
              <span className="min-w-0 flex-1 truncate font-mono text-xs text-text">
                {modelLabel}
              </span>
              <IconChevronDown className="hidden h-3.5 w-3.5 shrink-0 text-faint sm:block" />
            </button>
            {showThinking && (
              <button
                type="button"
                disabled={thinkingLoading}
                onClick={() => {
                  setPlusOpen(false);
                  setTodosOpen(false);
                  setCtxOpen(false);
                  setThinkingOpen(true);
                }}
                aria-label="Thinking level"
                title={`Thinking: ${thinkingLoading ? '…' : thinkingLabel || 'off'} — click to change`}
                className={`flex min-h-[36px] shrink-0 items-center rounded-md border px-2.5 py-1 transition-colors hover:text-text disabled:cursor-not-allowed disabled:opacity-60 ${thinkingColor.border} ${thinkingColor.text}`}
              >
                <IconBrain
                  className={`h-3.5 w-3.5 shrink-0 ${thinkingLoading ? 'animate-spin' : ''}`}
                />
              </button>
            )}
            <button
              type="button"
              onClick={() => {
                setPlusOpen(false);
                setTodosOpen(false);
                setThinkingOpen(false);
                setCtxOpen((o) => !o);
              }}
              aria-label="Context usage"
              title="Context usage — click for details and compaction"
              className={`flex min-h-[36px] shrink-0 items-center rounded-md border px-2.5 py-1 transition-colors hover:text-text ${
                ctxOpen ? 'bg-border' : ''
              }`}
              style={{ borderColor: ctxBorder }}
            >
              {/* While loading the ring shows 0%; the sweep animation plays
                  when the real value lands. */}
              <ContextRing ctx={ctxLoading || !ctx ? null : ctx} />
            </button>
            <button
              type="button"
              onClick={() => openTools('perms')}
              aria-label="Permission mode"
              title={`${permissionMeta(permissionMode).title} — click to change`}
              className={`flex min-h-[36px] shrink-0 items-center rounded-md border px-2.5 py-1 transition-colors hover:text-text ${permissionMeta(permissionMode).badge}`}
            >
              <IconLock className="h-3.5 w-3.5 shrink-0" />
            </button>
            {isDesktop && toolsButton}
            {isDesktop && tasksButton}
            <IconButton
              data-map-toggle
              onClick={() => setMapOpen((o) => !o)}
              aria-label="Toggle message map"
              title="Message map"
              disabled={userKeys.length <= 1}
              className={`h-9! w-9! shrink-0 rounded-md! border border-border ${mapOpen ? 'text-accent' : ''} disabled:opacity-40`}
            >
              <IconMap className="h-4 w-4" />
            </IconButton>
          </div>
        </div>
      )}

      <div className="relative min-h-0 flex-1">
      <div
        ref={scrollRef}
        onScroll={onScroll}
        className="fade-y h-full overflow-y-auto px-3 py-4 md:px-4"
      >
        {loading && (
          <div className="flex justify-center py-10">
            <Spinner className="h-5 w-5" />
          </div>
        )}
        {loadError && (
          <div className="mx-auto max-w-2xl">
            <ErrorBox message={loadError} />
            <div className="mt-3 flex justify-center">
              <Button variant="outline" onClick={() => void load()}>
                Retry
              </Button>
            </div>
          </div>
        )}
        {permPrompt && streaming && (
          <div className="mx-auto mb-3 w-full max-w-2xl rounded-lg border border-amber-900/60 bg-amber-950/30 p-3 text-sm">
            <div className="flex items-start gap-2">
              <IconWrench className="mt-0.5 h-4 w-4 shrink-0 text-amber-400" />
              <div className="min-w-0 flex-1">
                <p className="text-text">
                  Allow the agent to run <code className="font-mono">{permPrompt.tool}</code>?
                </p>
                {permPrompt.detail && (
                  <pre className="mt-1 max-h-24 overflow-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-relaxed text-subtle">
                    {permPrompt.detail}
                  </pre>
                )}
              </div>
            </div>
            <div className="mt-2.5 flex flex-wrap gap-1.5">
              <Button
                variant="outline"
                className="h-8 px-3 text-xs"
                onClick={() => void respondPerm(true)}
              >
                Allow
              </Button>
              <Button
                variant="ghost"
                className="h-8 px-3 text-xs"
                onClick={() => void respondPerm(false)}
              >
                Deny
              </Button>
            </div>
          </div>
        )}
        {!loading && !loadError && items.length === 0 && (
          <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
            <p className="text-sm font-medium text-text">What do you want to build?</p>
            <p className="max-w-[260px] text-xs text-faint">
              Describe your app and v1 will generate it. You can keep iterating in this chat.
            </p>
          </div>
        )}
        {!loading && !loadError && items.length > 0 && (
          <div className="mx-auto flex max-w-2xl flex-col gap-3">
            {(() => {
              // Silent collapsed-tool rounds (no text) fold into the work note
              // that precedes them, so bookkeeping like a lone "made 1 edit"
              // extends the note above's collapse summary instead of cluttering
              // the transcript. A round that says something is a real message —
              // its words render as its own row and the run of silent rounds
              // following it folds onto that note.
              const displayItems: Item[] = [];
              let note: MsgItem | null = null; // the current text-bearing work note
              for (const it of items) {
                const isSilent = it.kind === 'msg' && isCollapsedToolsRow(it) && !it.content?.trim();
                if (isSilent && note && note.content?.trim() && isCollapsedToolsRow(note)) {
                  const folded: MsgItem = {
                    ...note,
                    toolCalls: [...(note.toolCalls ?? []), ...(it.toolCalls ?? [])],
                    toolResults: [...(note.toolResults ?? []), ...(it.toolResults ?? [])],
                  };
                  displayItems[displayItems.length - 1] = folded;
                  note = folded;
                } else {
                  displayItems.push(it);
                  note = it.kind === 'msg' && !it.streaming ? it : null;
                }
              }
              const lastMsgIdx = (() => {
                for (let i = displayItems.length - 1; i >= 0; i--) {
                  if (displayItems[i].kind === 'msg') return i;
                }
                return -1;
              })();
              const rows: ReactNode[] = [];
              let run: { it: MsgItem; i: number }[] = [];
              const flush = () => {
                if (run.length === 0) return;
                if (run.length === 1) {
                  const { it, i } = run[0];
                  rows.push(
                    <div key={it.key} data-msg-key={it.key}>
                      <MessageRow
                        item={it}
                        isLast={i === lastMsgIdx}
                        streaming={streaming}
                        turnEnd={turnEndKeys.has(it.key)}
                        validTag={validTag}
                        onEdit={editUserMessage}
                        onRewind={requestRewind}
                        onRegenerate={regenerate}
                        onRetrySend={sendText}
                        onEditStart={setItemEditing}
                        onImageClick={openLightbox}
                        onAskAnswered={(answer) => void answerAsk(answer)}
                        currency={currency}
                      />
                    </div>,
                  );
                } else {
                  rows.push(
                    <div key={run[0].it.key} data-msg-key={run[0].it.key}>
                      <CollapsedToolsGroup
                        members={run.map(({ it }) => collapseData(it))}
                        onAskAnswered={(answer) => void answerAsk(answer)}
                      />
                    </div>,
                  );
                }
                run = [];
              };
              displayItems.forEach((it, i) => {
                // Merge consecutive silent collapsed-tool rounds; a round with
                // actual words is a message — render it alone (its text shows)
                // and break the run.
                if (it.kind === 'msg' && isCollapsedToolsRow(it) && !it.content?.trim()) {
                  run.push({ it, i });
                  return;
                }
                flush();
                rows.push(
                  <div key={it.key} data-msg-key={it.key}>
                    <MessageRow
                      item={it}
                      isLast={i === lastMsgIdx}
                      streaming={streaming}
                      turnEnd={turnEndKeys.has(it.key)}
                      validTag={validTag}
                      onEdit={editUserMessage}
                      onRewind={requestRewind}
                      onRegenerate={regenerate}
                      onRetrySend={sendText}
                      onEditStart={setItemEditing}
                      onImageClick={openLightbox}
                      onAskAnswered={(answer) => void answerAsk(answer)}
                      currency={currency}
                    />
                  </div>,
                );
              });
              flush();
              return rows;
            })()}
            {!streaming && (resuming || runActive) && (
              <div className="flex items-center gap-2 rounded-lg border border-border bg-surface px-3 py-2 text-xs text-dim">
                <Spinner className="h-3.5 w-3.5 shrink-0" />
                {resuming ? 'Resuming the interrupted generation…' : 'Generation still running…'}
              </div>
            )}
          </div>
        )}
      </div>
      {userKeys.length > 1 && (
        <div
          ref={stripRef}
          onPointerDown={(e) => {
            e.currentTarget.setPointerCapture(e.pointerId);
            scrubTo(e.clientY);
          }}
          onPointerMove={(e) => {
            if (e.buttons > 0) scrubTo(e.clientY);
          }}
          className={`absolute bottom-4 right-2 top-4 z-10 w-7 touch-none overflow-hidden rounded-full border border-border bg-bg/85 px-2 py-3 shadow-xl shadow-black/40 backdrop-blur transition-opacity duration-200 ${
            mapOpen ? 'opacity-100' : 'pointer-events-none opacity-0'
          }`}
        >
          <div className="flex h-full flex-col items-center justify-center gap-2">
            {buckets.map((b, i) => (
              <button
                key={b.key}
                type="button"
                onClick={() => jumpTo(b.key)}
                aria-label={`Jump to your message ${b.start + 1}`}
                title={`Jump to your message ${b.start + 1}`}
                style={{ width: dotSize, height: dotSize }}
                className={`shrink-0 rounded-full transition-all ${
                  currentDot === i ? 'scale-125 bg-accent' : 'bg-border-strong hover:bg-subtle'
                }`}
              />
            ))}
          </div>
        </div>
      )}
      </div>

      {showJump && (
        <div className="relative z-10 flex h-0 justify-center overflow-visible">
          <button
            type="button"
            onClick={jumpToBottom}
            aria-label="Scroll to bottom"
            title="Scroll to bottom"
            className="flex h-9 w-9 -translate-y-[calc(100%+0.75rem)] items-center justify-center rounded-full border border-border bg-surface/95 text-subtle shadow-lg backdrop-blur transition-colors hover:text-text"
          >
            <IconChevronDown className="h-4 w-4" />
          </button>
        </div>
      )}

      {localStatus && <div className="px-3 py-1 text-center text-xs text-subtle">{localStatus}</div>}
      {(queued.length > 0 || steering.length > 0) && (
        <div className="shrink-0 border-t border-accent/30 bg-bg px-2 pt-2 pb-1 md:px-3">
          <div className="mx-auto w-full max-w-2xl rounded-lg border border-accent/40 bg-surface p-2 text-xs shadow-lg shadow-black/20">
            <div className="mb-1.5 flex items-center gap-1.5 px-1 text-[10px] font-medium uppercase tracking-wider text-faint">
              <IconSend className="h-3 w-3 text-accent" />
              Queued ({queued.length})
              {queued.some((m) => m.estimatedWaitSeconds != null) && (
                <span className="ml-auto normal-case tracking-normal text-dim">
                  ~{fmtQueueWait(Math.max(...queued.map((m) => m.estimatedWaitSeconds ?? 0)))} to next turn
                </span>
              )}
            </div>
            {queued.map((m, i) => (
              <div
                key={m.id}
                className="mb-1 flex items-center gap-1 rounded-md border border-border/80 bg-surface/70 px-2 py-1.5 last:mb-0"
              >
                {queued.length > 1 && (
                  <span className="w-4 shrink-0 text-right font-mono text-[10px] text-faint">
                    {i + 1}.
                  </span>
                )}
                {queueEditId === m.id ? (
                  <textarea
                    autoFocus
                    rows={2}
                    value={queueEditText}
                    onChange={(e) => setQueueEditText(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' && !e.shiftKey) {
                        e.preventDefault();
                        void editQueued(m.id, queueEditText);
                        setQueueEditing(null, '');
                      } else if (e.key === 'Escape') {
                        setQueueEditing(null, '');
                      }
                    }}
                    onBlur={() => {
                      if (queueEditId === m.id) {
                        void editQueued(m.id, queueEditText);
                        setQueueEditing(null, '');
                      }
                    }}
                    className="min-h-[52px] min-w-0 flex-1 resize-y rounded-md border border-border bg-bg px-2 py-1.5 text-xs text-text outline-none focus:border-subtle"
                  />
                ) : (
                  <span className="min-w-0 flex-1 truncate text-dim">{m.text}</span>
                )}
                {queued.length > 1 && (
                  <span className="flex shrink-0 flex-col">
                    <button
                      type="button"
                      onClick={() => void moveQueued(i, -1)}
                      disabled={i === 0}
                      aria-label="Move up"
                      title="Move up"
                      className="flex h-4 w-5 items-center justify-center rounded text-faint transition-colors hover:text-text disabled:opacity-30"
                    >
                      <IconChevronUp className="h-3 w-3" />
                    </button>
                    <button
                      type="button"
                      onClick={() => void moveQueued(i, 1)}
                      disabled={i === queued.length - 1}
                      aria-label="Move down"
                      title="Move down"
                      className="flex h-4 w-5 items-center justify-center rounded text-faint transition-colors hover:text-text disabled:opacity-30"
                    >
                      <IconChevronDown className="h-3 w-3" />
                    </button>
                  </span>
                )}
                <button
                  type="button"
                  onClick={() => {
                    if (queueEditId === m.id) {
                      setQueueEditing(null, '');
                    } else {
                      setQueueEditing(m.id, m.text);
                    }
                  }}
                  aria-label="Edit queued message"
                  title="Edit"
                  className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-faint transition-colors hover:bg-border hover:text-text"
                >
                  <IconPencil className="h-3 w-3" />
                </button>
                <button
                  type="button"
                  onClick={() => void deleteQueued(m.id)}
                  disabled={queueEditId === m.id}
                  aria-label="Delete queued message"
                  title="Delete queued message"
                  className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-faint transition-colors hover:bg-border hover:text-red-400 disabled:opacity-40"
                >
                  <IconTrash className="h-3 w-3" />
                </button>
                <button
                  type="button"
                  onClick={() => void steerQueued(m.id)}
                  disabled={queueEditId === m.id}
                  aria-label="Steer now"
                  title="Steer into the current run — picked up as soon as the current tool call finishes"
                  className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-accent/40 text-accent transition-colors hover:bg-accent/10 disabled:opacity-40"
                >
                  <IconSend className="h-3 w-3" />
                </button>
              </div>
            ))}
            {steering.length > 0 && (
              <>
                <div className="mt-2 mb-1 flex items-center gap-1.5 px-1 text-[10px] font-medium uppercase tracking-wider text-accent">
                  <IconSend className="h-3 w-3 text-accent" />
                  Steering
                </div>
                {steering.map((m) => (
                  <div
                    key={m.id}
                    className="mb-1 flex items-center gap-1.5 rounded-md border border-accent/40 bg-accent/5 px-2 py-1.5 last:mb-0"
                  >
                    <Spinner className="h-3 w-3 shrink-0 text-accent" />
                    <span className="min-w-0 flex-1 truncate text-dim">{m.text}</span>
                  </div>
                ))}
              </>
            )}
          </div>
        </div>
      )}
      {llmReady === true ? (
        <div
          className={
            expanded
              ? 'absolute inset-0 z-20 flex flex-col bg-bg px-2 pt-2 pb-1'
              : 'mt-auto shrink-0 border-t border-border px-2 pt-2 pb-1 md:p-3'
          }
        >
          <div className={`relative flex flex-col gap-2 ${expanded ? 'min-h-0 flex-1' : ''}`}>
            {bgRunning.length > 0 && (
              <div className="flex shrink-0 items-center gap-1.5 rounded-md border border-amber-300/25 bg-amber-300/5 px-2.5 py-1 text-[11px] font-medium text-amber-300/90">
                <IconTerminal className="h-3 w-3 shrink-0 animate-pulse" />
                {bgRunning.length === 1
                  ? '1 background task running…'
                  : `${bgRunning.length} background tasks running…`}
              </div>
            )}
            {suggestions.length > 0 && (
              <div
                className={`absolute z-20 max-h-64 overflow-y-auto overscroll-contain rounded-lg border border-border bg-surface shadow-lg ${
                  expanded ? 'bottom-2 left-2 right-2' : 'inset-x-0 bottom-full mb-1'
                }`}
              >
                {suggestions.map((s, index) => (
                  <button
                    key={s.insert}
                    type="button"
                    onMouseEnter={() => setSuggestionIndex(index)}
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => acceptSuggestion(s)}
                    className={`flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs ${
                      index === suggestionIndex ? 'bg-border text-text' : 'text-subtle'
                    }`}
                  >
                    <span className="min-w-0 flex-1 truncate font-mono">{s.label}</span>
                    {s.hint && (
                      <span className="max-w-[50%] shrink-0 truncate text-[10px] text-faint">
                        {s.hint}
                      </span>
                    )}
                  </button>
                ))}
              </div>
            )}
            {plusOpen && (
              <>
                <button
                  type="button"
                  aria-label="Close menu"
                  className="fixed inset-0 z-20 cursor-default"
                  onClick={() => setPlusOpen(false)}
                />
                {/* bottom-full pops above the composer normally, but the
                    expanded column is full-height, which would shoot the menu
                    off the top of the screen — anchor to the bottom there. */}
                <div
                  className={`absolute z-30 w-56 overflow-hidden rounded-lg border border-border bg-surface shadow-lg ${
                    expanded ? 'bottom-2 left-2' : 'bottom-full left-2 mb-1'
                  }`}
                >
                  <button
                    type="button"
                    disabled={!hasModel}
                    onClick={() => {
                      setPlusOpen(false);
                      fileRef.current?.click();
                    }}
                    className="flex w-full items-center gap-2.5 px-3 py-2.5 text-left text-sm text-text transition-colors hover:bg-border disabled:opacity-40"
                  >
                    <IconPaperclip className="h-4 w-4 shrink-0 text-dim" />
                    {supportsImages ? 'Attach a file' : 'Attach a text file'}
                  </button>
                  {todos.length > 0 && (
                    <button
                      type="button"
                      onClick={() => {
                        setPlusOpen(false);
                        setTodosOpen(true);
                      }}
                      className="flex w-full items-center gap-2.5 px-3 py-2.5 text-left text-sm text-text transition-colors hover:bg-border"
                    >
                      <IconCheck className="h-4 w-4 shrink-0 text-dim" />
                      Todos ({todos.filter((t) => !t.done).length} left)
                    </button>
                  )}
                  <button
                    type="button"
                    disabled={thinkingOptions.length === 0}
                    title={
                      thinkingOptions.length === 0
                        ? "This model doesn't support thinking levels"
                        : 'Model thinking'
                    }
                    onClick={() => {
                      setPlusOpen(false);
                      setThinkingOpen(true);
                    }}
                    className="flex w-full items-center gap-2.5 px-3 py-2.5 text-left text-sm text-text transition-colors hover:bg-border disabled:opacity-40"
                  >
                    <IconBrain className="h-4 w-4 shrink-0 text-dim" />
                    Model thinking
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setPlusOpen(false);
                      onSessionsOpenChange(true);
                    }}
                    className="flex w-full items-center gap-2.5 px-3 py-2.5 text-left text-sm text-text transition-colors hover:bg-border"
                  >
                    <IconLayers className="h-4 w-4 shrink-0 text-dim" />
                    Sessions
                  </button>
                  {debugEnabled && (
                    <button
                      type="button"
                      onClick={() => {
                        setPlusOpen(false);
                        void exportDiagnostics();
                      }}
                      className="flex w-full items-center gap-2.5 px-3 py-2.5 text-left text-sm text-text transition-colors hover:bg-border"
                    >
                      <IconDownload className="h-4 w-4 shrink-0 text-dim" />
                      Export diagnostics
                    </button>
                  )}
                  {!isDesktop && (
                    <button
                      type="button"
                      onClick={() => {
                        setPlusOpen(false);
                        setExpanded((x) => !x);
                      }}
                      className="flex w-full items-center gap-2.5 px-3 py-2.5 text-left text-sm text-text transition-colors hover:bg-border"
                    >
                      {expanded ? (
                        <IconCompress className="h-4 w-4 shrink-0 text-dim" />
                      ) : (
                        <IconExpand className="h-4 w-4 shrink-0 text-dim" />
                      )}
                      {expanded ? 'Collapse composer' : 'Expand composer'}
                    </button>
                  )}
                  <button
                    type="button"
                    onClick={() => {
                      setPlusOpen(false);
                      openTools('mcp');
                    }}
                    className="flex w-full items-center gap-2.5 px-3 py-2.5 text-left text-sm text-text transition-colors hover:bg-border"
                  >
                    <IconWrench className="h-4 w-4 shrink-0 text-dim" />
                    Tools, skills & permissions
                  </button>
                </div>
              </>
            )}
            {todosOpen && todos.length > 0 && (
              <>
                <button
                  type="button"
                  aria-label="Close tasks"
                  className="fixed inset-0 z-20 cursor-default"
                  onClick={() => setTodosOpen(false)}
                />
                <div
                  className={`absolute z-30 w-80 overflow-hidden rounded-lg border bg-surface ${
                    expanded
                      ? 'bottom-2 left-2 border-border-strong shadow-[0_0_24px_rgba(0,0,0,0.7)]'
                      : 'bottom-full left-2 mb-1 border-border shadow-lg'
                  }`}
                >
                  <div className="border-b border-border px-3 py-2 text-xs font-medium text-subtle">
                    Todos
                    <span className="ml-1.5 font-normal text-faint">
                      {todos.filter((t) => !t.done).length} left
                    </span>
                  </div>
                  <ul className="fade-y max-h-64 overflow-y-auto overscroll-contain px-3 py-2">
                    {todos.map((t, i) => (
                      <li key={i} className="flex items-start gap-2.5 py-1">
                        <span
                          className={`mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded border ${
                            t.done
                              ? 'border-emerald-500 bg-emerald-500 text-emerald-50'
                              : 'border-border-strong'
                          }`}
                        >
                          {t.done && <IconCheck className="h-3 w-3" />}
                        </span>
                        <span
                          className={`min-w-0 text-sm ${
                            t.done ? 'text-faint line-through' : 'text-text'
                          }`}
                        >
                          {t.title}
                        </span>
                      </li>
                    ))}
                  </ul>
                </div>
              </>
            )}

            {thinkingOpen && (
              <>
                <button
                  type="button"
                  aria-label="Close thinking"
                  className="fixed inset-0 z-20 cursor-default"
                  onClick={() => setThinkingOpen(false)}
                />
                <div
                  className={`absolute z-30 w-64 overflow-hidden rounded-lg border bg-surface ${
                    expanded
                      ? 'bottom-2 left-2 border-border-strong shadow-[0_0_24px_rgba(0,0,0,0.7)]'
                      : 'bottom-full left-2 mb-1 border-border shadow-lg'
                  }`}
                >
                  <div className="border-b border-border px-3 py-2 text-xs font-medium text-subtle">
                    Model thinking
                  </div>
                  <div className="px-1.5 py-1.5">
                    {thinkingOptions.length === 0 ? (
                      <p className="px-2 py-1.5 text-xs text-faint">
                        This model doesn&apos;t support thinking levels.
                      </p>
                    ) : (
                      (thinkingOffSupported ? ['off', ...thinkingOptions] : thinkingOptions).map(
                        (opt) => (
                          <button
                            key={opt}
                            type="button"
                            onClick={() => {
                              setThinking(opt);
                              persistSelection(providerId, model, opt);
                              setThinkingOpen(false);
                            }}
                            className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-dim transition-colors hover:bg-border hover:text-text"
                          >
                            <span className="flex h-3.5 w-3.5 shrink-0 items-center justify-center">
                              {thinkingDisplay === opt && (
                                <IconCheck className="h-3.5 w-3.5 text-accent" />
                              )}
                            </span>
                            <span className="capitalize">
                              {opt === 'off' ? 'Off' : opt === 'on' ? 'On' : opt}
                            </span>
                          </button>
                        ),
                      )
                    )}
                  </div>
                </div>
              </>
            )}

            {ctxOpen && (
              <>
                <button
                  type="button"
                  aria-label="Close context"
                  className="fixed inset-0 z-20 cursor-default"
                  onClick={() => setCtxOpen(false)}
                />
                <div
                  className={`absolute z-30 w-64 overflow-hidden rounded-lg border bg-surface ${
                    expanded
                      ? 'bottom-2 left-2 border-border-strong shadow-[0_0_24px_rgba(0,0,0,0.7)]'
                      : 'bottom-full left-2 mb-1 border-border shadow-lg'
                  }`}
                >
                  <div className="border-b border-border px-3 py-2 text-xs font-medium text-subtle">
                    Context
                  </div>
                  <div className="flex flex-col gap-2.5 px-3 py-2.5">
                    {ctx && !ctxLoading ? (
                      <>
                        <div className="flex items-center gap-2.5">
                          <ContextRing ctx={ctx} />
                          <div className="min-w-0 flex-1">
                            <div className="text-sm font-medium text-text">
                              {Math.round((ctx.used / ctx.budget) * 100)}% full
                            </div>
                            <div className="text-[11px] text-faint">
                              {ctx.used.toLocaleString()} of {ctx.budget.toLocaleString()} tokens
                            </div>
                          </div>
                        </div>
                        <p className="text-[11px] leading-relaxed text-faint">
                          Compaction is recommended once the conversation passes{' '}
                          {ctx.threshold.toLocaleString()} tokens — it summarizes the history to
                          free up context.
                        </p>
                        <Button
                          variant="outline"
                          className="h-8 w-full text-xs"
                          disabled={ctxCompacting}
                          onClick={() => void compactNow()}
                        >
                          {ctxCompacting ? <Spinner className="h-3.5 w-3.5" /> : 'Compact now'}
                        </Button>
                      </>
                    ) : (
                      <p className="text-xs text-faint">Loading context usage…</p>
                    )}
                  </div>
                </div>
              </>
            )}

            {(attachments.length > 0 || attachError) && (
              <div className="flex flex-wrap items-center gap-1.5">
                {attachments.map((a, i) => (
                  <span
                    key={i}
                    className="flex items-center gap-1.5 rounded-md border border-border bg-surface px-2 py-1 text-xs text-dim"
                  >
                    <IconPaperclip className="h-3 w-3 shrink-0 text-faint" />
                    <span className="max-w-[140px] truncate">{a.name}</span>
                    <button
                      type="button"
                      aria-label={`Remove ${a.name}`}
                      onClick={() => setAttachments((prev) => prev.filter((_, j) => j !== i))}
                      className="text-faint transition-colors hover:text-text"
                    >
                      <IconX className="h-3 w-3" />
                    </button>
                  </span>
                ))}
                {attachError && <span className="text-xs text-red-400">{attachError}</span>}
              </div>
            )}
            {/* The track is a sibling of the clipped composer box so nothing
                masks it (see TrackBorder). */}
            <div className={`relative ${expanded ? 'flex min-h-0 flex-1 flex-col' : ''}`}>
              <div
                className={`flex min-w-0 gap-1.5 overflow-hidden rounded-xl border border-border-strong bg-surface p-1.5 transition-colors focus-within:border-subtle md:gap-2 md:p-2 ${
                  expanded ? 'min-h-0 flex-1 flex-col' : 'items-end'
                }`}
              >
                <input
                  ref={fileRef}
                  type="file"
                  multiple
                  className="hidden"
                  onChange={(e) => {
                    void addFiles(e.target.files);
                    e.target.value = '';
                  }}
                />
                {expanded ? (
                  <>
                    <div className="flex shrink-0 items-center justify-between">
                      <div className="flex items-center gap-1.5">
                        {toolsButton}
                        {attachButton}
                        {tasksButton}
                        {thinkingIconButton}
                        {collapseButton}
                      </div>
                      <div className="flex items-center gap-1.5">
                        {stopButton}
                        {sendButton}
                      </div>
                    </div>
                    {textField}
                  </>
                ) : (
                  <>
                    {isDesktop ? (
                      <>
                        {attachButton}
                        {plusButton}
                      </>
                    ) : (
                      plusButton
                    )}
                    {textField}
                    {stopButton}
                    {sendButton}
                  </>
                )}
              </div>
              {(streaming || runActive) && <TrackBorder ts={track} id="v1-track-grad" />}
            </div>
          </div>
        </div>
      ) : llmReady === false ? (
        <div className="shrink-0 border-t border-border p-2.5 md:p-3">
          <div className="mx-auto flex max-w-2xl flex-col items-center gap-3 rounded-xl border border-border-strong bg-surface px-4 py-5 text-center">
            <p className="text-sm text-text">
              No AI provider configured — set an API key and model in Settings to start building.
            </p>
            <Button
              onClick={() => navigate('/settings', { state: { from: `/project/${projectId}` } })}
              className="min-h-11 md:min-h-9"
            >
              Open Settings
            </Button>
          </div>
        </div>
      ) : (
        <div className="shrink-0 border-t border-border p-2.5 md:p-3">
          <div className="mx-auto flex max-w-2xl items-center justify-center gap-2 rounded-xl border border-border-strong bg-surface px-4 py-5 text-sm text-dim">
            <Spinner className="h-4 w-4" />
            Loading…
          </div>
        </div>
      )}

      <Dialog
        open={toolsOpen}
        onClose={() => setToolsOpen(false)}
        title="Tools & permissions"
        wide
        fixedBody
        align="top"
        fullScreen
        translucent
      >
        <ToolSettings
          key={toolsTab}
          initialTab={toolsTab}
          initialPermissionMode={permissionMode}
          onPermissionModeChange={setPermissionMode}
          onPermissionSaved={(m) => {
            setPermissionMode(m);
            setToolsOpen(false);
          }}
        />
      </Dialog>

      <ModelPicker
        open={modelPickerOpen}
        onClose={() => setModelPickerOpen(false)}
        providers={providers}
        providerId={providerId}
        model={model}
        models={catalogModels}
        onProviderChange={changeProvider}
        onModelChange={changeModel}
      />

      <SessionsModal
        open={sessionsOpen}
        onClose={() => onSessionsOpenChange(false)}
        sessions={sessions}
        activeId={sessionId}
        onSwitch={(id) => {
          setSessionId(id);
          localStorage.setItem(sessionStorageKey(projectId), id);
        }}
        onNew={() => void createNewSession()}
        onRename={(id, name) => {
          setSessions((prev) => prev.map((s) => (s.id === id ? { ...s, name } : s)));
          void api.renameSession(projectId, id, name).catch(() => {});
        }}
        onArchive={(id) => {
          setSessions((prev) => prev.map((s) => (s.id === id ? { ...s, archived: true } : s)));
          void api.archiveSession(projectId, id).catch(() => {});
        }}
        onUnarchive={(id) => {
          setSessions((prev) => prev.map((s) => (s.id === id ? { ...s, archived: false } : s)));
          void api.unarchiveSession(projectId, id).catch(() => {});
        }}
        onDelete={(id) => {
          setSessions((prev) => prev.filter((s) => s.id !== id));
          void api.deleteSession(projectId, id).catch(() => {});
        }}
        creating={creatingSession}
      />

      <Dialog
        open={rewindTarget !== null}
        onClose={() => setRewindTarget(null)}
        title="Approve rewind"
      >
        <p className="text-sm text-dim">
          Rewind the chat to{' '}
          <span className="font-medium text-text">
            &ldquo;{rewindTarget?.text.slice(0, 80)}
            {rewindTarget && rewindTarget.text.length > 80 ? '…' : ''}&rdquo;
          </span>
          ? Everything after this message will be removed from the conversation. This cannot be
          undone.
        </p>
        <div className="mt-4 flex justify-end gap-2">
          <Button variant="ghost" onClick={() => setRewindTarget(null)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={() => {
              const t = rewindTarget;
              setRewindTarget(null);
              if (t) void rewindTo(t.key);
            }}
          >
            Rewind
          </Button>
        </div>
      </Dialog>

      {lightbox && (
        <ImageLightbox url={lightbox.url} name={lightbox.name} onClose={() => setLightbox(null)} />
      )}
    </div>
  );
}
