import { useCallback, useEffect, useRef, useState } from 'react';
import { api, streamChat } from '../api';
import type { ChatEvent } from '../types';
import { errMsg } from '../utils';
import { Button, ErrorBox, IconButton, Spinner } from './ui';
import {
  IconArrowUp,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconSquare,
  IconWrench,
  IconX,
} from './icons';

type Item =
  | { kind: 'msg'; key: string; role: string; content: string; streaming?: boolean }
  | { kind: 'tool'; key: string; name: string; detail: string; running: boolean; ok?: boolean };

function ToolRow({ item }: { item: Extract<Item, { kind: 'tool' }> }) {
  const [open, setOpen] = useState(false);
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

export default function ChatPane({
  projectId,
  onPreviewRestart,
}: {
  projectId: string;
  onPreviewRestart: () => void;
}) {
  const [items, setItems] = useState<Item[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [input, setInput] = useState('');
  const [streaming, setStreaming] = useState(false);

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

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const msgs = await api.getMessages(projectId);
      const mapped: Item[] = msgs.map((m) =>
        m.tool || m.role === 'tool'
          ? {
              kind: 'tool',
              key: m.id,
              name: m.tool || 'tool',
              detail: m.content,
              running: false,
              ok: true,
            }
          : { kind: 'msg', key: m.id, role: m.role, content: m.content },
      );
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

  // Auto-scroll on new content.
  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [items]);

  // Auto-grow the input textarea.
  useEffect(() => {
    const el = taRef.current;
    if (el) {
      el.style.height = 'auto';
      el.style.height = `${Math.min(el.scrollHeight, 160)}px`;
    }
  }, [input]);

  const finish = useCallback(() => {
    setStreaming(false);
    abortRef.current = null;
    assistantKeyRef.current = null;
    update((prev) =>
      prev.map((it) => (it.kind === 'msg' && it.streaming ? { ...it, streaming: false } : it)),
    );
  }, [update]);

  const handleEvent = useCallback(
    (ev: ChatEvent) => {
      switch (ev.type) {
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
        case 'done': {
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

  const send = useCallback(async () => {
    const text = input.trim();
    if (!text || streaming) return;
    setInput('');
    update((prev) => [
      ...prev,
      { kind: 'msg', key: `u${++counterRef.current}`, role: 'user', content: text },
    ]);
    setStreaming(true);
    assistantKeyRef.current = null;
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    try {
      await streamChat(projectId, text, handleEvent, ctrl.signal);
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
  }, [input, streaming, projectId, handleEvent, finish, update]);

  const stop = () => abortRef.current?.abort();

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto px-3 py-4 md:px-4">
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
            {items.map((it) => {
              if (it.kind === 'tool') return <ToolRow key={it.key} item={it} />;
              if (it.role === 'user') {
                return (
                  <div
                    key={it.key}
                    className="ml-auto max-w-[85%] whitespace-pre-wrap break-words rounded-2xl bg-border px-3.5 py-2 text-sm text-text"
                  >
                    {it.content}
                  </div>
                );
              }
              if (it.role === 'error') {
                return (
                  <div key={it.key} className="whitespace-pre-wrap break-words text-sm text-red-400">
                    {it.content}
                  </div>
                );
              }
              return (
                <div
                  key={it.key}
                  className="whitespace-pre-wrap break-words text-sm leading-relaxed text-text"
                >
                  {it.content}
                  {it.streaming && (
                    <span className="ml-0.5 inline-block h-4 w-[7px] animate-pulse rounded-[2px] bg-subtle align-text-bottom" />
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>

      <div className="shrink-0 border-t border-border p-2.5 md:p-3">
        <div className="mx-auto flex max-w-2xl items-end gap-2 rounded-xl border border-border-strong bg-surface p-2 transition-colors focus-within:border-subtle">
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
    </div>
  );
}
