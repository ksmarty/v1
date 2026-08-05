import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent,
} from 'react';
import { useNavigate } from 'react-router-dom';
import { api, retryChat, streamChat } from '../api';
import type { ChatEvent, ChatUsage, Provider, ProviderModel, SavedProvider, Todo } from '../types';
import { errMsg } from '../utils';
import { renderMarkdown } from '../markdown';
import { Button, Dialog, ErrorBox, IconButton, Spinner } from './ui';
import ToolSettings from './ToolSettings';
import { ModelCombobox } from './ModelCombobox';
import {
  IconArrowUp,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconModel,
  IconRefresh,
  IconSquare,
  IconWrench,
  IconX,
} from './icons';

type ToolCall = { name: string; detail: string };

const WORKING_PHRASES = [
  'Thinking…',
  'Writing code…',
  'Checking files…',
  'Running commands…',
  'Polishing…',
];

type MsgItem = {
  kind: 'msg';
  key: string;
  role: 'user' | 'assistant' | 'error';
  content: string;
  reasoning?: string;
  usage?: ChatUsage;
  model?: string;
  toolCalls?: ToolCall[];
  toolResults?: ToolCall[];
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

/** Parses an assistant message's tool_json: {"tool_calls":[{function:{name,arguments}}]}. */
function parseToolCalls(tool: string): ToolCall[] | null {
  try {
    const data = JSON.parse(tool) as {
      tool_calls?: { function?: { name?: string; arguments?: string } }[];
    };
    if (!Array.isArray(data.tool_calls)) return null;
    return data.tool_calls.map((c) => ({
      name: c?.function?.name ?? 'tool',
      detail: c?.function?.arguments ?? '',
    }));
  } catch {
    return null;
  }
}

/** Parses a role "tool" message's tool_json: {"name": "..."}. */
function parseToolName(tool: string): string {
  try {
    const data = JSON.parse(tool) as { name?: string };
    return typeof data.name === 'string' && data.name ? data.name : 'tool';
  } catch {
    return 'tool';
  }
}

function formatUsage(usage: ChatUsage, msgModel?: string): string {
  const parts = [`${usage.input.toLocaleString()} in · ${usage.output.toLocaleString()} out`];
  const m = msgModel ?? usage.model;
  if (m) parts.push(m);
  return parts.join(' · ');
}

/** True when a message key is a persisted row id (not a live/synthetic key). */
function persisted(key: string): boolean {
  return Number.isFinite(Number(key));
}

function ToolRow({ item }: { item: ToolItem }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="v1-cv rounded-lg border border-border/80 bg-surface/50 text-xs">
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
        <IconWrench className="h-3.5 w-3.5 shrink-0" />
        <span className="shrink-0 font-mono text-text">{item.name}</span>
        <span className="min-w-0 flex-1 truncate text-faint">{item.detail}</span>
        {item.running ? (
          <Spinner className="h-3.5 w-3.5 shrink-0" />
        ) : item.ok ? (
          <IconCheck className="h-3.5 w-3.5 shrink-0 text-emerald-500" />
        ) : (
          <IconX className="h-3.5 w-3.5 shrink-0 text-red-500" />
        )}
      </button>
      {open && item.detail && (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words border-t border-border/80 px-3 py-2 font-mono text-[11px] leading-relaxed text-subtle">
          {item.detail}
        </pre>
      )}
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

function ToolChip({ name, detail }: ToolCall) {
  return (
    <span className="inline-flex min-w-0 max-w-full items-center gap-1.5 rounded-md border border-border bg-surface/50 px-2 py-0.5 font-mono text-[10px] text-dim">
      <IconWrench className="h-3 w-3 shrink-0 text-faint" />
      <span className="shrink-0 text-text">{name}</span>
      {detail && <span className="min-w-0 flex-1 truncate text-faint">{detail}</span>}
    </span>
  );
}

function ToolResultBlock({ name, detail }: ToolCall) {
  const [open, setOpen] = useState(false);
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
        <IconWrench className="h-3.5 w-3.5 shrink-0" />
        <span className="shrink-0 font-mono text-text">{name}</span>
        <span className="text-faint">result</span>
      </button>
      {open && (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words border-t border-border/80 px-3 py-2 font-mono text-[11px] leading-relaxed text-subtle">
          {detail}
        </pre>
      )}
    </div>
  );
}

function Markdown({ text, streaming }: { text: string; streaming?: boolean }) {
  const html = useMemo(() => renderMarkdown(text, streaming), [text, streaming]);
  const onCopy = (e: MouseEvent<HTMLDivElement>) => {
    const btn = (e.target as HTMLElement).closest('button[data-copy]');
    if (!(btn instanceof HTMLButtonElement)) return;
    const code = btn.parentElement?.querySelector('code');
    const content = code?.textContent ?? '';
    if (!content) return;
    void navigator.clipboard
      .writeText(content)
      .then(() => {
        const label = btn.textContent;
        btn.textContent = 'Copied';
        window.setTimeout(() => {
          btn.textContent = label;
        }, 1500);
      })
      .catch(() => {
        // clipboard unavailable — ignore
      });
  };
  return <div className="md" onClick={onCopy} dangerouslySetInnerHTML={{ __html: html }} />;
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
  onEdit,
  onRewind,
  onRegenerate,
  onEditStart,
}: {
  item: Item;
  isLast: boolean;
  streaming: boolean;
  onEdit: (key: string, text: string) => void;
  onRewind: (key: string) => void;
  onRegenerate: () => void;
  onEditStart: (key: string, editing: boolean) => void;
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
      <div className="v1-cv ml-auto flex max-w-[85%] flex-col items-end gap-1">
        <div className="w-full rounded-2xl bg-border px-3.5 py-2 text-sm text-text">
          <div className="whitespace-pre-wrap break-words">{item.content}</div>
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
                onClick={() => void onRewind(item.key)}
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
      <div className="v1-cv whitespace-pre-wrap break-words text-sm text-red-400">
        {item.content}
      </div>
    );
  }
  return (
    <div
      className={`v1-cv flex min-w-0 flex-col gap-1.5 ${item.stale ? 'opacity-45' : ''}`}
    >
      {item.reasoning && (
        <ReasoningBlock text={item.reasoning} autoOpen={item.streaming ?? false} />
      )}
      <div className="min-w-0">
        <Markdown text={item.content} streaming={item.streaming} />
      </div>
      {item.toolCalls && item.toolCalls.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {item.toolCalls.map((tc, j) => (
            <ToolChip key={j} {...tc} />
          ))}
        </div>
      )}
      {item.toolResults && item.toolResults.map((tr, j) => <ToolResultBlock key={j} {...tr} />)}
      {item.usage && (
        <div className="text-[10px] text-faint">{formatUsage(item.usage, item.model)}</div>
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
  onPreviewRestart,
  llmReady,
}: {
  projectId: string;
  onPreviewRestart: () => void;
  llmReady: boolean;
}) {
  const [items, setItems] = useState<Item[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [input, setInput] = useState('');
  const [streaming, setStreaming] = useState(false);
  const [model, setModel] = useState('');
  const [providerId, setProviderId] = useState(''); // '' = custom (no saved provider)
  const [providers, setProviders] = useState<SavedProvider[]>([]);
  const [catalog, setCatalog] = useState<Provider[]>([]);
  const [todos, setTodos] = useState<Todo[]>([]);
  const [todosOpen, setTodosOpen] = useState(true);
  const [toolsOpen, setToolsOpen] = useState(false);
  const [workPhrase, setWorkPhrase] = useState(0);
  const [permPrompt, setPermPrompt] = useState<{
    requestId: string;
    tool: string;
    detail: string;
  } | null>(null);
  const navigate = useNavigate();

  const itemsRef = useRef<Item[]>([]);
  const counterRef = useRef(0);
  const assistantKeyRef = useRef<string | null>(null);
  const toolStackRef = useRef<Record<string, string[]>>({});
  const abortRef = useRef<AbortController | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const taRef = useRef<HTMLTextAreaElement>(null);
  const restartRef = useRef(onPreviewRestart);
  restartRef.current = onPreviewRestart;

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
    let persisted: { providerId: string; model: string } | null = null;
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
        const sel = persisted ?? { providerId: s.llm.activeProviderId ?? '', model: s.llm.model };
        // A persisted provider that was deleted falls back to the active one.
        if (sel.providerId === '' || saved.some((p) => p.id === sel.providerId)) {
          setProviderId(sel.providerId);
        } else {
          setProviderId(s.llm.activeProviderId ?? '');
        }
        setModel(sel.model);
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

  const persistSelection = useCallback(
    (pid: string, m: string) => {
      try {
        localStorage.setItem(`v1.chatModel.${projectId}`, JSON.stringify({ providerId: pid, model: m }));
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
    persistSelection(pid, nextModel);
  };

  const changeModel = (m: string) => {
    setModel(m);
    persistSelection(providerId, m);
  };

  const modelOverride = model.trim() || undefined;
  const providerOverride = providerId || undefined;

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const msgs = await api.getMessages(projectId);
      const mapped: Item[] = [];
      for (const m of msgs) {
        if (m.role === 'tool') {
          const name = m.tool ? parseToolName(m.tool) : 'tool';
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
            usage: m.usage,
            model: m.model,
          };
          const calls = m.tool ? parseToolCalls(m.tool) : null;
          if (calls) item.toolCalls = calls;
          mapped.push(item);
        } else if (m.role === 'user') {
          mapped.push({ kind: 'msg', key: m.id, role: 'user', content: m.content });
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

  // Auto-scroll on new content, but only while the user is already at (or near)
  // the bottom — otherwise reading history during a stream gets yanked around.
  const nearBottomRef = useRef(true);
  const onScroll = useCallback(() => {
    const el = scrollRef.current;
    if (el) nearBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  }, []);
  useEffect(() => {
    const el = scrollRef.current;
    if (el && nearBottomRef.current) el.scrollTop = el.scrollHeight;
  }, [items]);

  // Auto-grow the input textarea.
  useEffect(() => {
    const el = taRef.current;
    if (el) {
      el.style.height = 'auto';
      el.style.height = `${Math.min(el.scrollHeight, 160)}px`;
    }
  }, [input]);

  // Cycle the "working" phrase while a generation is streaming.
  useEffect(() => {
    if (!streaming) return;
    const t = setInterval(() => setWorkPhrase((i) => (i + 1) % WORKING_PHRASES.length), 2500);
    return () => clearInterval(t);
  }, [streaming]);

  const finish = useCallback(() => {
    setStreaming(false);
    abortRef.current = null;
    assistantKeyRef.current = null;
    setPermPrompt(null);
    update((prev) =>
      prev.map((it) => (it.kind === 'msg' && it.streaming ? { ...it, streaming: false } : it)),
    );
  }, [update]);

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
          setTodosOpen(true);
          break;
        }
        case 'permission_request': {
          setPermPrompt({ requestId: ev.requestId, tool: ev.tool, detail: ev.detail });
          break;
        }
        case 'done': {
          if (ev.usage) {
            const k = assistantKeyRef.current;
            if (k) {
              const ck = k;
              update((prev) =>
                prev.map((it) =>
                  it.kind === 'msg' && it.key === ck ? { ...it, usage: ev.usage } : it,
                ),
              );
            }
          }
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
      }
    },
    [update, finish],
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
      if (!trimmed || streaming || !llmReady) return;
      setInput('');
      update((prev) => [
        ...prev,
        { kind: 'msg', key: `u${++counterRef.current}`, role: 'user', content: trimmed },
      ]);
      void run((signal) =>
        streamChat(projectId, trimmed, { model: modelOverride, providerId: providerOverride }, handleEvent, signal),
      );
    },
    [streaming, llmReady, projectId, modelOverride, providerOverride, handleEvent, run, update],
  );

  const send = useCallback(() => sendText(input), [input, sendText]);

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
          usage: undefined,
          toolCalls: undefined,
          toolResults: undefined,
        };
        return prev.slice(0, idx).concat(edited);
      });
      toolStackRef.current = {};
      assistantKeyRef.current = null;
      void run((signal) =>
        streamChat(projectId, trimmed, { model: modelOverride, providerId: providerOverride, editMessageId: id }, handleEvent, signal),
      );
    },
    [streaming, llmReady, projectId, modelOverride, providerOverride, handleEvent, run, update],
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

  const lastMsgIdx = useMemo(() => {
    for (let i = items.length - 1; i >= 0; i--) {
      if (items[i].kind === 'msg') return i;
    }
    return -1;
  }, [items]);

  return (
    <div className="flex h-full min-h-0 flex-col">
      {llmReady && (
        <div className="shrink-0 border-b border-border px-3 py-1.5 md:px-4">
          <div className="mx-auto flex max-w-2xl items-center gap-2">
            <IconModel className="h-3.5 w-3.5 shrink-0 text-dim" />
            <select
              value={providerId}
              onChange={(e) => changeProvider(e.target.value)}
              aria-label="Provider"
              className="max-w-[45%] shrink-0 rounded-md border border-border bg-surface px-2 py-1 font-mono text-xs text-text outline-none transition-colors focus:border-subtle"
            >
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
              <option value="">Custom</option>
            </select>
            {showFreeText ? (
              <input
                value={model}
                onChange={(e) => changeModel(e.target.value)}
                placeholder="model id"
                spellCheck={false}
                className="min-w-0 flex-1 rounded-md border border-border bg-surface px-2 py-1 font-mono text-xs text-text outline-none transition-colors placeholder:text-faint focus:border-subtle"
              />
            ) : (
              <div className="min-w-0 flex-1">
                <ModelCombobox
                  models={catalogModels}
                  value={model}
                  onChange={changeModel}
                  className="h-8! rounded-md! px-2! py-1! font-mono! text-xs!"
                />
              </div>
            )}
            {streaming && (
              <span
                className="flex shrink-0 items-center gap-1.5 rounded-full bg-border px-2 py-1 text-[11px] text-subtle"
                title={`Working with ${model || (selectedProvider?.name ?? 'provider')}`}
              >
                <span className="relative flex h-2 w-2">
                  <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-accent opacity-60" />
                  <span className="relative inline-flex h-2 w-2 rounded-full bg-accent" />
                </span>
                Working…
              </span>
            )}
          </div>
        </div>
      )}

      <div
        ref={scrollRef}
        onScroll={onScroll}
        className="min-h-0 flex-1 overflow-y-auto px-3 py-4 md:px-4"
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
        {todos.length > 0 && (
          <div className="mx-auto mb-2 w-full max-w-2xl rounded-xl border border-border bg-surface/50">
            <button
              type="button"
              onClick={() => setTodosOpen((o) => !o)}
              className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs font-medium text-subtle transition-colors hover:text-text"
            >
              {todosOpen ? (
                <IconChevronDown className="h-3.5 w-3.5 shrink-0 text-faint" />
              ) : (
                <IconChevronRight className="h-3.5 w-3.5 shrink-0 text-faint" />
              )}
              To do
              <span className="ml-auto font-normal text-faint">
                {todos.filter((t) => !t.done).length} left
              </span>
            </button>
            {todosOpen && (
              <ul className="border-t border-border/80 px-3 py-2">
                {todos.map((t, i) => (
                  <li key={i} className="flex items-start gap-2.5 py-1">
                    <span
                      className={`mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded border ${
                        t.done ? 'border-emerald-500 bg-emerald-500 text-emerald-50' : 'border-border-strong'
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
            )}
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
              <MessageRow
                key={it.key}
                item={it}
                isLast={i === lastMsgIdx}
                streaming={streaming}
                onEdit={editUserMessage}
                onRewind={rewindTo}
                onRegenerate={regenerate}
                onEditStart={setItemEditing}
              />
            ))}
          </div>
        )}
      </div>

      {llmReady ? (
        <div className="shrink-0 border-t border-border p-2.5 md:p-3">
          <div
            className={`mx-auto flex max-w-2xl items-end gap-2 rounded-xl border border-border-strong bg-surface p-2 transition-colors focus-within:border-subtle ${
              streaming ? 'v1-working' : ''
            }`}
          >
            <IconButton
              onClick={() => setToolsOpen(true)}
              aria-label="Tools, skills & permissions"
              title="Tools, skills & permissions"
              className="h-9 w-9 shrink-0 md:h-9 md:w-9"
            >
              <IconWrench className="h-4 w-4" />
            </IconButton>
            <textarea
              ref={taRef}
              rows={1}
              value={input}
              placeholder="Describe what to build…"
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault();
                  void send();
                }
              }}
              className="max-h-40 min-h-[36px] flex-1 resize-none bg-transparent px-1.5 py-1.5 text-sm text-text outline-none placeholder:text-faint"
            />
            {streaming && (
              <span className="flex shrink-0 items-center gap-1.5 pr-1 text-xs text-dim">
                {WORKING_PHRASES[workPhrase]}
                <span className="flex items-center gap-0.5 text-accent">
                  <span className="v1-dot" />
                  <span className="v1-dot" />
                  <span className="v1-dot" />
                </span>
              </span>
            )}
            {streaming ? (
              <IconButton
                onClick={stop}
                aria-label="Stop generating"
                className="h-9 w-9 shrink-0 md:h-9 md:w-9"
              >
                <IconSquare className="h-4 w-4" />
              </IconButton>
            ) : (
              <IconButton
                onClick={() => void send()}
                disabled={!input.trim()}
                aria-label="Send message"
                className="h-9 w-9 shrink-0 bg-primary text-primary-text hover:opacity-90 hover:text-primary-text disabled:bg-border disabled:text-faint md:h-9 md:w-9"
              >
                <IconArrowUp className="h-4 w-4" />
              </IconButton>
            )}
          </div>
        </div>
      ) : (
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
      )}

      <Dialog
        open={toolsOpen}
        onClose={() => setToolsOpen(false)}
        title="Tools & permissions"
        wide
        fixedBody
        align="top"
      >
        <ToolSettings />
      </Dialog>
    </div>
  );
}
