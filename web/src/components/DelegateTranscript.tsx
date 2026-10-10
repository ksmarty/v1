import { useEffect, useState } from 'react';
import { api } from '../api';
import type {
  DelegateContentPart,
  DelegateMessage,
  DelegateTranscript,
  DelegateTranscriptEntry,
} from '../types';
import Markdown from './Markdown';
import { Dialog } from './ui';

function textOf(content: string | DelegateContentPart[]): string {
  if (typeof content === 'string') return content;
  return content
    .filter((p): p is { type: 'text'; text: string } => p.type === 'text')
    .map((p) => p.text)
    .join('\n');
}

function MessageView({ message }: { message: DelegateMessage }) {
  if (message.role === 'user') {
    const text = textOf(message.content);
    if (!text.trim()) return null;
    return (
      <div className="ml-auto max-w-[85%] whitespace-pre-wrap break-words rounded-2xl rounded-br-md border border-border bg-surface px-3 py-2 text-sm text-text">
        {text}
      </div>
    );
  }
  if (message.role === 'toolResult') {
    const text = textOf(message.content);
    if (!text.trim()) return null;
    return (
      <div
        className={`max-w-[95%] whitespace-pre-wrap break-words rounded-lg border px-3 py-2 font-mono text-[11px] leading-relaxed ${
          message.isError
            ? 'border-red-500/30 bg-red-500/5 text-red-400'
            : 'border-border bg-surface/40 text-subtle'
        }`}
      >
        <div className="mb-1 text-[10px] opacity-70">{message.toolName}</div>
        {text}
      </div>
    );
  }
  // Assistant: one element per content block, in order.
  return (
    <>
      {message.content.map((part, i) => {
        if (part.type === 'text') {
          if (!part.text.trim()) return null;
          return (
            <div key={i} className="max-w-[95%] text-sm text-text">
              <Markdown text={part.text} />
            </div>
          );
        }
        if (part.type === 'thinking') {
          if (!part.thinking.trim()) return null;
          return (
            <details
              key={i}
              className="max-w-[95%] rounded-lg border border-border/60 bg-surface/40 px-3 py-2 text-xs text-dim"
            >
              <summary className="cursor-pointer select-none font-medium">Thinking</summary>
              <div className="mt-2 whitespace-pre-wrap break-words">{part.thinking}</div>
            </details>
          );
        }
        if (part.type === 'toolCall') {
          return (
            <div
              key={i}
              className="max-w-[95%] rounded-lg border border-border bg-surface/40 px-3 py-2 font-mono text-[11px] text-dim"
            >
              <span className="text-text">{part.name}</span>
              {part.arguments && (
                <div className="mt-1 whitespace-pre-wrap break-words opacity-80">
                  {JSON.stringify(part.arguments)}
                </div>
              )}
            </div>
          );
        }
        return null;
      })}
    </>
  );
}

function EntryView({ entry }: { entry: DelegateTranscriptEntry }) {
  return (
    <>
      {(entry.model ?? []).map((message, i) => (
        <MessageView key={i} message={message} />
      ))}
    </>
  );
}

export default function DelegateTranscriptDialog({
  open,
  onClose,
  projectId,
  conversationId,
}: {
  open: boolean;
  onClose: () => void;
  projectId: string;
  conversationId: string | null;
}) {
  const [transcript, setTranscript] = useState<DelegateTranscript | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!open || !conversationId) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    setTranscript(null);
    api
      .getDelegateMessages(projectId, conversationId)
      .then((t) => {
        if (!cancelled) setTranscript(t);
      })
      .catch((e) => {
        if (!cancelled) setError(e instanceof Error ? e.message : 'Could not load the transcript.');
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, projectId, conversationId]);

  // The sidecar returns newest-first; the reader wants oldest-first.
  const visible = transcript
    ? [...transcript.items].reverse().filter((e) => (e.model ?? []).length > 0)
    : [];

  return (
    <Dialog open={open} onClose={onClose} title="Sub-agent transcript" wide fixedBody>
      <div className="flex flex-col gap-3 pb-2">
        {loading && <div className="py-8 text-center text-sm text-dim">Loading transcript…</div>}
        {error && <div className="py-8 text-center text-sm text-red-400">{error}</div>}
        {!loading && !error && visible.length === 0 && (
          <div className="py-8 text-center text-sm text-dim">
            No transcript was recorded for this sub-agent.
          </div>
        )}
        {visible.map((entry) => (
          <EntryView key={entry.id} entry={entry} />
        ))}
      </div>
    </Dialog>
  );
}
