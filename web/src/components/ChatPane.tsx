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
import { api, messageAttachmentUrl, retryChat, streamChat, type ChatAttachmentInput } from '../api';
import type {
  ChatAttachmentMeta,
  ChatEvent,
  ChatUsage,
  Memory,
  PermissionMode,
  Provider,
  ProviderModel,
  SavedProvider,
  Todo,
} from '../types';
import { errMsg, getJsonPretty } from '../utils';
import { notifyTurnDone } from '../notify';
import { permissionMeta } from '../permissions';
import { Button, Dialog, ErrorBox, IconButton, Input, Spinner } from './ui';
import ToolSettings, { type ToolsTab } from './ToolSettings';
import Markdown from './Markdown';
import ModelPicker from './ModelPicker';
import TrackBorder, { TRACK_DEFAULTS } from './TrackBorder';
import {
  IconArrowUp,
  IconBookmark,
  IconBookmarkOff,
  IconBrain,
  IconCamera,
  IconChat,
  IconCheck,
  IconCheckSquare,
  IconChevronDown,
  IconChevronRight,
  IconCode,
  IconCompress,
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
  restart_preview: IconRefresh,
  screenshot_app: IconCamera,
  set_todos: IconCheckSquare,
  remember: IconBookmark,
  forget: IconBookmarkOff,
  ask_user: IconUser,
};

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
  /** Token usage for the turn this message closed (turn-final messages only). */
  usage?: ChatUsage;
  streaming?: boolean;
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

type Item = MsgItem | ToolItem;

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
    <div className="rounded-lg border border-border/80 bg-surface/50 text-xs">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[32px] w-full items-center gap-2 px-2.5 py-1.5 text-left text-dim transition-colors hover:text-text"
      >
        {open ? (
          <IconChevronDown className="h-3.5 w-3.5 shrink-0" />
        ) : (
          <IconChevronRight className="h-3.5 w-3.5 shrink-0" />
        )}
        <Icon className="h-3.5 w-3.5 shrink-0" />
        <span className="shrink-0 font-mono text-text">{toolLabel(item.name)}</span>
        <span className="min-w-0 flex-1 truncate text-faint">{item.detail}</span>
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

function ReasoningBlock({ text, autoOpen }: { text: string; autoOpen: boolean }) {
  const [open, setOpen] = useState(autoOpen);
  useEffect(() => {
    setOpen(autoOpen);
  }, [autoOpen]);
  return (
    <div className="rounded-lg border border-border/80 bg-surface/50 text-xs">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[30px] w-full items-center gap-2 px-2.5 py-1.5 text-left text-dim transition-colors hover:text-text"
      >
        {open ? (
          <IconChevronDown className="h-3.5 w-3.5 shrink-0" />
        ) : (
          <IconChevronRight className="h-3.5 w-3.5 shrink-0" />
        )}
        <span className="font-mono">Thinking</span>
      </button>
      {open && (
        <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words border-t border-border/80 px-3 py-2 font-mono text-[11px] leading-relaxed text-subtle">
          {text}
        </pre>
      )}
    </div>
  );
}

// Splits an edit's old/new strings into shared context lines and the changed
// middle section, for the chat's edit diff view.
function diffLines(oldText: string, newText: string) {
  const a = oldText.split('\n');
  const b = newText.split('\n');
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }
  return {
    before: a.slice(0, start),
    removed: a.slice(start, endA),
    added: b.slice(start, endB),
    after: a.slice(endA),
  };
}

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
    return detail;
  } catch {
    return detail;
  }
}

function ToolChip({ name, detail }: ToolCall) {
  const [open, setOpen] = useState(false);
  const Icon = toolIcon(name);
  const diff = name === 'edit_file' ? parseEditDiff(detail) : null;
  const label = diff ? diff.path : chipLabel(detail);
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
        <Icon className="h-3 w-3 shrink-0 text-faint" />
        <span className="shrink-0 text-text">{toolLabel(name)}</span>
        {label && <span className="min-w-0 flex-1 truncate text-faint">{label}</span>}
        {diff && (
          <>
            <span className="shrink-0 text-red-400">-{diff.removed.length}</span>
            <span className="shrink-0 text-emerald-400">+{diff.added.length}</span>
          </>
        )}
      </button>
      {open &&
        (diff ? (
          <div className="max-h-60 overflow-auto border-t border-border/80 px-2 py-1.5 leading-relaxed">
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
          </div>
        ) : (
          <ToolBody detail={detail} />
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
        <pre
          className="max-h-60 overflow-auto whitespace-pre-wrap break-words px-3 py-2 font-mono text-[11px] leading-relaxed text-subtle"
          dangerouslySetInnerHTML={{ __html: html }}
        />
      ) : (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words px-3 py-2 font-mono text-[11px] leading-relaxed text-subtle">
          {detail}
        </pre>
      )}
    </div>
  );
}

// A run_command call and its result merged into one card: the command in the
// header (with an exit-code indicator), the result body below.
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
      {open && <ToolBody detail={result.detail} />}
    </div>
  );
}

// Renders an assistant message's tool calls and results as one column, pairing
// each run_command call with its result (both lists are in chronological
// order) so they show as a single block.
function ToolBlocks({ calls, results }: { calls: ToolCall[]; results: ToolCall[] }) {
  const out: ReactNode[] = [];
  let next = 0;
  calls.forEach((tc, j) => {
    if (tc.name === 'run_command') {
      let k = next;
      while (k < results.length && results[k].name !== 'run_command') k++;
      if (k < results.length) {
        next = k + 1;
        out.push(
          <RunCommandBlock key={`c${j}`} command={chipLabel(tc.detail)} result={results[k]} />,
        );
        return;
      }
    }
    out.push(<ToolChip key={`c${j}`} {...tc} />);
  });
  results.forEach((tr, j) => {
    // results already merged into a RunCommandBlock are skipped
    if (j < next && tr.name === 'run_command') return;
    out.push(<ToolResultBlock key={`r${j}`} {...tr} />);
  });
  return <div className="flex flex-col gap-1.5">{out}</div>;
}

function ToolResultBlock({ name, detail }: ToolCall) {
  const [open, setOpen] = useState(false);
  const Icon = toolIcon(name);
  return (
    <div className="rounded-lg border border-border/80 bg-surface/50 text-xs">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[30px] w-full items-center gap-2 px-2.5 py-1.5 text-left text-dim transition-colors hover:text-text"
      >
        {open ? (
          <IconChevronDown className="h-3.5 w-3.5 shrink-0" />
        ) : (
          <IconChevronRight className="h-3.5 w-3.5 shrink-0" />
        )}
        <Icon className="h-3.5 w-3.5 shrink-0" />
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
  onEditStart,
  onImageClick,
}: {
  item: Item;
  isLast: boolean;
  streaming: boolean;
  turnEnd: boolean;
  validTag: (tag: string) => boolean;
  onEdit: (key: string, text: string) => void;
  onRewind: (key: string, text: string) => void;
  onRegenerate: () => void;
  onEditStart: (key: string, editing: boolean) => void;
  onImageClick: (url: string, name: string) => void;
}) {
  if (item.kind === 'tool') return <ToolRow item={item} />;
  if (item.role === 'user') {
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
        {persisted(item.key) && !streaming && (
          <div className="flex gap-2 pr-1">
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
        )}
      </div>
    );
  }
  if (item.role === 'error') {
    return (
      <div className="whitespace-pre-wrap break-words text-sm text-red-400">
        {item.content}
      </div>
    );
  }
  return (
    <div
      className={`flex min-w-0 flex-col gap-1.5 ${item.stale ? 'opacity-45' : ''}`}
    >
      {item.reasoning && (
        <ReasoningBlock text={item.reasoning} autoOpen={item.streaming ?? false} />
      )}
      <div className="min-w-0">
        <Markdown text={item.content} streaming={item.streaming} validTag={validTag} />
      </div>
      {(item.toolCalls?.length ?? 0) + (item.toolResults?.length ?? 0) > 0 && (
        <ToolBlocks calls={item.toolCalls ?? []} results={item.toolResults ?? []} />
      )}
      {turnEnd && item.usage && !item.streaming && (
        <div className="text-[10px] text-faint">
          {item.usage.input.toLocaleString()} in · {item.usage.output.toLocaleString()} out
          {item.usage.model ? ` · ${item.usage.model}` : ''}
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
  llmReady,
  initialPrompt,
}: {
  projectId: string;
  projectName: string;
  onPreviewRestart: () => void;
  onMemories?: (mems: Memory[]) => void;
  /** null while the LLM configuration is still loading. */
  llmReady: boolean | null;
  /** Description from the New project dialog — auto-sent once, when ready. */
  initialPrompt?: string;
}) {
  const [items, setItems] = useState<Item[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [input, setInput] = useState('');
  const [suggestions, setSuggestions] = useState<Suggestion[]>([]);
  const [suggestionIndex, setSuggestionIndex] = useState(0);
  const [plusOpen, setPlusOpen] = useState(false);
  const [streaming, setStreaming] = useState(false);
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
    question: string;
    options: string[];
  } | null>(null);
  const [askText, setAskText] = useState('');
  const navigate = useNavigate();

  const itemsRef = useRef<Item[]>([]);
  const counterRef = useRef(0);
  const assistantKeyRef = useRef<string | null>(null);
  const toolStackRef = useRef<Record<string, string[]>>({});
  const abortRef = useRef<AbortController | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const taRef = useRef<HTMLTextAreaElement>(null);
  const echoRef = useRef<HTMLDivElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  // File and enabled-skill lists power the @/# autocomplete and tag
  // validation (only real files/skills get pill-highlighted). Loaded eagerly;
  // files refresh after each agent run since runs create/edit files.
  const [fileList, setFileList] = useState<string[]>([]);
  const [skillList, setSkillList] = useState<{ name: string; hint: string }[]>([]);
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
        const sel = persisted ?? { providerId: s.llm.activeProviderId ?? '', model: s.llm.model };
        // A persisted provider that was deleted falls back to the active one.
        if (sel.providerId === '' || saved.some((p) => p.id === sel.providerId)) {
          setProviderId(sel.providerId);
        } else {
          setProviderId(s.llm.activeProviderId ?? '');
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
    [projectId],
  );

  const changeProvider = (pid: string) => {
    setProviderId(pid);
    const nextModel = pid === '' ? '' : providers.find((p) => p.id === pid)?.model ?? '';
    setModel(nextModel);
    applyThinkingForModel(nextModel);
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
  const thinkingMetaCache = useRef(new Map<string, { levels: string[]; off: boolean }>());
  // The level a fresh selection gets: the global default when the model
  // supports it, otherwise the next highest available level (or the lowest
  // when the default sits below everything the model offers).
  const freshThinkingLevel = (meta: { levels: string[]; off: boolean }): string => {
    if (defaultThinking === 'off' && (meta.off || meta.levels.includes('none'))) return 'off';
    if (defaultThinking === '') return meta.levels[0] ?? '';
    if (meta.levels.includes(defaultThinking)) return defaultThinking;
    const reqRank = THINKING_LEVEL_RANK[defaultThinking] ?? -1;
    let best = '';
    let bestRank = -Infinity;
    meta.levels.forEach((lvl, i) => {
      const rank = THINKING_LEVEL_RANK[lvl] ?? i + 10;
      if (rank <= reqRank && rank > bestRank) {
        best = lvl;
        bestRank = rank;
      }
    });
    return best || meta.levels[0] || '';
  };
  useEffect(() => {
    if (!model.trim()) {
      setThinkingMeta(null);
      return;
    }
    const key = `${providerId}|${model}`;
    const cached = thinkingMetaCache.current.get(key);
    if (cached) {
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

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const msgs = await api.getMessages(projectId);
      const mapped: Item[] = [];
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
            usage: m.usage ? { ...m.usage, model: m.model || m.usage.model } : undefined,
          };
          const calls = m.tool ? parseToolCalls(m.tool) : null;
          if (calls) {
            // set_todos repeats are pure progress noise — only the first call
            // of a message is worth a chip.
            let seenTodos = false;
            item.toolCalls = calls.filter((c) => {
              if (c.name !== 'set_todos') return true;
              if (seenTodos) return false;
              seenTodos = true;
              return true;
            });
          }
          mapped.push(item);
        } else if (m.role === 'user') {
          mapped.push({
            kind: 'msg',
            key: m.id,
            role: 'user',
            content: m.content,
            attachments: m.attachments?.map((a, i) => ({
              ...a,
              url: a.kind === 'image' ? messageAttachmentUrl(projectId, m.id, i) : undefined,
            })),
          });
        } else {
          mapped.push({ kind: 'msg', key: m.id, role: 'error', content: m.content });
        }
      }
      itemsRef.current = mapped;
      setItems(mapped);
    } catch (e) {
      setLoadError(errMsg(e));
    } finally {
      setLoading(false);
    }
  }, [projectId]);

  useEffect(() => {
    void load();
  }, [load]);

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
    setDotSize(Math.min(10, Math.max(4, ((stripH - 24) / grouped.length) * 0.8)));
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
      switch (ev.type) {
        case 'reasoning': {
          let k = assistantKeyRef.current;
          if (!k) {
            k = `s${++counterRef.current}`;
            assistantKeyRef.current = k;
            const nk = k;
            update((prev) => [
              ...prev,
              {
                kind: 'msg',
                key: nk,
                role: 'assistant',
                content: '',
                reasoning: ev.text,
                streaming: true,
              },
            ]);
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
              ...prev,
              { kind: 'msg', key: nk, role: 'assistant', content: '', streaming: true },
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
        case 'tool_start': {
          assistantKeyRef.current = null;
          const key = `t${++counterRef.current}`;
          (toolStackRef.current[ev.name] ||= []).push(key);
          update((prev) => [
            ...prev,
            { kind: 'tool', key, name: ev.name, detail: ev.detail, running: true },
          ]);
          break;
        }
        case 'tool_end': {
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
        case 'question_request': {
          setAskPrompt({ requestId: ev.requestId, question: ev.text ?? '', options: ev.options ?? [] });
          setAskText('');
          break;
        }
        case 'done': {
          if (ev.usage) {
            const u = ev.usage;
            const k = assistantKeyRef.current;
            if (k) {
              update((prev) =>
                prev.map((it) => (it.kind === 'msg' && it.key === k ? { ...it, usage: u } : it)),
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
          void notifyTurnDone(projectId, projectName, snippet);
          finish();
          break;
        }
        case 'error': {
          update((prev) => [
            ...prev,
            { kind: 'msg', key: `e${++counterRef.current}`, role: 'error', content: ev.error },
          ]);
          finish();
          break;
        }
        case 'injected_message': {
          // The agent added a user message mid-turn (a screenshot from the
          // screenshot_app tool) — render it like any other user turn.
          const id = ev.messageId ?? 0;
          update((prev) => [
            ...prev,
            {
              kind: 'msg',
              key: id > 0 ? String(id) : `i${++counterRef.current}`,
              role: 'user',
              content: ev.text ?? '',
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
    [update, finish, projectId, projectName],
  );

  const run = useCallback(
    async (start: (signal: AbortSignal) => Promise<void>) => {
      if (streaming) return;
      setStreaming(true);
      assistantKeyRef.current = null;
      const ctrl = new AbortController();
      abortRef.current = ctrl;
      try {
        await start(ctrl.signal);
        finish();
      } catch (e) {
        const aborted = e instanceof DOMException && e.name === 'AbortError';
        update((prev) => [
          ...prev,
          {
            kind: 'msg',
            key: `e${++counterRef.current}`,
            role: 'error',
            content: aborted ? 'Generation stopped.' : errMsg(e),
          },
        ]);
        finish();
      }
    },
    [streaming, finish, update],
  );

  const sendText = useCallback(
    (text: string) => {
      const trimmed = text.trim();
      if (!trimmed || streaming || !llmReady || !modelOverride) return;
      setInput('');
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
    [streaming, llmReady, projectId, modelOverride, providerOverride, handleEvent, run, update, attachments, thinkingEffort],
  );

  // Auto-send the New project dialog's "what do you want to create?"
  // description once the initial history and model selection are ready.
  const initialSentRef = useRef(false);
  useEffect(() => {
    if (!initialPrompt || initialSentRef.current) return;
    if (loading || !llmReady || !modelOverride) return;
    initialSentRef.current = true;
    sendText(initialPrompt);
  }, [initialPrompt, loading, llmReady, modelOverride, sendText]);

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
          await api.truncateMessages(projectId, 0);
          await load();
          setLocalStatus('Chat cleared.');
        } catch (e) {
          setLocalStatus(errMsg(e));
        }
        break;
      default:
        // /compact
        if (streaming) {
          setLocalStatus('Stop the current run before compacting.');
          break;
        }
        setLocalStatus('Compacting conversation…');
        try {
          await api.compact(projectId);
          setLocalStatus('Conversation compacted.');
        } catch (e) {
          setLocalStatus(errMsg(e));
        }
    }
    setInput('');
    return true;
  }, [projectId, streaming, load]);

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
        await api.queueChat(projectId, text, modelOverride, providerOverride);
        setExpanded(false);
      } catch (e) {
        setInput(text);
        setLoadError(errMsg(e));
      }
      return;
    }
    sendText(input);
    setExpanded(false);
  }, [input, streaming, attachments.length, runLocalCommand, sendText, projectId, modelOverride, providerOverride]);

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
          insert: c.name,
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
    void run((signal) => retryChat(projectId, handleEvent, signal));
  }, [streaming, llmReady, projectId, handleEvent, run, update]);

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
        await api.truncateMessages(projectId, id);
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

  const stop = () => abortRef.current?.abort();

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
    async (answer: string) => {
      const a = askPrompt;
      const text = answer.trim();
      if (!a || !text) return;
      setAskPrompt(null);
      try {
        await api.askRespond(projectId, a.requestId, text);
      } catch {
        // 404 — already answered or timed out
      }
    },
    [askPrompt, projectId],
  );

  const lastMsgIdx = useMemo(() => {
    for (let i = items.length - 1; i >= 0; i--) {
      if (items[i].kind === 'msg') return i;
    }
    return -1;
  }, [items]);

  // Keys of the user's messages, in order — the minimap shows one dot each.
  const userKeys = useMemo(
    () =>
      items.filter((it) => it.kind === 'msg' && it.role === 'user').map((it) => it.key),
    [items],
  );

  // Keys of the assistant messages that closed their turn — the usage line
  // (per-turn token counts) only shows on these.
  const turnEndKeys = useMemo(() => {
    const keys = new Set<string>();
    let pending = true;
    for (let i = items.length - 1; i >= 0; i--) {
      const it = items[i];
      if (it.kind !== 'msg') continue;
      if (it.role === 'user') {
        pending = true;
      } else if (it.role === 'assistant') {
        if (pending) keys.add(it.key);
        pending = false;
      }
    }
    return keys;
  }, [items]);

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
              <IconModel className="h-3.5 w-3.5 shrink-0 text-dim" />
              <span className="min-w-0 flex-1 truncate font-mono text-xs text-text">
                {modelLabel}
              </span>
              <IconChevronDown className="h-3.5 w-3.5 shrink-0 text-faint" />
            </button>
            {showThinking && (
              <button
                type="button"
                disabled={thinkingLoading}
                onClick={() => {
                  setPlusOpen(false);
                  setTodosOpen(false);
                  setThinkingOpen(true);
                }}
                aria-label="Thinking level"
                title={`Thinking: ${thinkingLoading ? '…' : thinkingLabel || 'off'} — click to change`}
                className={`flex min-h-[36px] shrink-0 items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs font-medium transition-colors hover:text-text disabled:cursor-not-allowed disabled:opacity-60 ${thinkingColor.border} ${thinkingColor.text}`}
              >
                <IconBrain
                  className={`h-3.5 w-3.5 shrink-0 ${thinkingLoading ? 'animate-spin' : ''}`}
                />
                {!thinkingLoading && <span>{thinkingLabel || 'Off'}</span>}
              </button>
            )}
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
            {userKeys.length > 1 && (
              <IconButton
                data-map-toggle
                onClick={() => setMapOpen((o) => !o)}
                aria-label="Toggle message map"
                title="Message map"
                className={`h-9! w-9! shrink-0 ${mapOpen ? 'text-accent' : ''}`}
              >
                <IconMap className="h-4 w-4" />
              </IconButton>
            )}
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
        {askPrompt && streaming && (
          <div className="mx-auto mb-3 w-full max-w-2xl rounded-lg border border-accent/50 bg-surface p-3 text-sm">
            <div className="flex items-start gap-2">
              <IconChat className="mt-0.5 h-4 w-4 shrink-0 text-accent" />
              <p className="min-w-0 flex-1 whitespace-pre-wrap break-words text-text">
                {askPrompt.question}
              </p>
            </div>
            {askPrompt.options.length > 0 && (
              <div className="mt-2.5 flex flex-wrap gap-1.5">
                {askPrompt.options.map((o) => (
                  <Button
                    key={o}
                    variant="outline"
                    className="h-8 px-3 text-xs"
                    onClick={() => void answerAsk(o)}
                  >
                    {o}
                  </Button>
                ))}
              </div>
            )}
            <form
              onSubmit={(e) => {
                e.preventDefault();
                void answerAsk(askText);
              }}
              className="mt-2.5 flex items-end gap-2"
            >
              <div className="flex-1">
                <Input
                  value={askText}
                  onChange={(e) => setAskText(e.target.value)}
                  placeholder={askPrompt.options.length > 0 ? '…or type an answer' : 'Type an answer…'}
                  autoComplete="off"
                />
              </div>
              <Button type="submit" variant="outline" className="h-[42px] sm:h-[38px]" disabled={!askText.trim()}>
                Answer
              </Button>
            </form>
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
            {items.map((it, i) => (
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
                  onEditStart={setItemEditing}
                  onImageClick={openLightbox}
                />
              </div>
            ))}
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
          <div className="flex h-full flex-col items-center justify-between">
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
      {llmReady === true ? (
        <div
          className={
            expanded
              ? 'absolute inset-0 z-20 flex flex-col bg-bg px-2 pt-2 pb-1'
              : 'mt-auto shrink-0 border-t border-border px-2 pt-2 pb-1 md:p-3'
          }
        >
          <div className={`relative flex flex-col gap-2 ${expanded ? 'min-h-0 flex-1' : ''}`}>
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
              {streaming && <TrackBorder ts={track} id="v1-track-grad" />}
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
