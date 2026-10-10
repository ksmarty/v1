import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { api } from '../api';
import type { Memory } from '../types';
import { errMsg } from '../utils';
import { Button, Dialog, Field, IconButton, Input, Spinner, Textarea } from './ui';
import { IconBrain, IconPencil, IconX } from './icons';

// Browse, add, edit and delete the facts the agent saved with the remember
// tool. `live` carries updates pushed by the chat stream so agent changes
// appear immediately, without a remount.
export default function MemoriesPane({
  projectId,
  live,
}: {
  projectId: string;
  live: Memory[] | null;
}) {
  const [memories, setMemories] = useState<Memory[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [newText, setNewText] = useState('');
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<{ id: number; text: string; tags: string } | null>(null);
  const [savingEdit, setSavingEdit] = useState(false);

  useEffect(() => {
    if (live) setMemories(live);
  }, [live]);

  const load = useCallback(async () => {
    try {
      const r = await api.getMemories(projectId);
      setMemories(r.memories);
      setError(null);
    } catch (e) {
      setError(errMsg(e));
    }
  }, [projectId]);

  useEffect(() => {
    void load();
  }, [load]);

  const add = async (e: FormEvent) => {
    e.preventDefault();
    if (!newText.trim()) return;
    setAdding(true);
    setError(null);
    try {
      const r = await api.createMemory(projectId, newText.trim());
      setMemories(r.memories);
      setNewText('');
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setAdding(false);
    }
  };

  const saveEdit = async () => {
    if (!editing || !editing.text.trim()) return;
    setSavingEdit(true);
    setError(null);
    try {
      const r = await api.updateMemory(projectId, editing.id, editing.text.trim(), editing.tags.trim());
      setMemories(r.memories);
      setEditing(null);
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setSavingEdit(false);
    }
  };

  const remove = async (id: number) => {
    const prev = memories;
    setMemories((m) => m?.filter((x) => x.id !== id) ?? m);
    try {
      await api.deleteMemory(projectId, id);
    } catch (e) {
      setMemories(prev);
      setError(errMsg(e));
    }
  };

  const toggle = async (id: number, enabled: boolean) => {
    const prev = memories;
    setMemories((m) => m?.map((x) => (x.id === id ? { ...x, enabled } : x)) ?? m);
    try {
      const r = await api.toggleMemory(projectId, id, enabled);
      setMemories(r.memories);
    } catch (e) {
      setMemories(prev);
      setError(errMsg(e));
    }
  };

  if (memories === null && !error) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner className="h-5 w-5" />
      </div>
    );
  }

  return (
    <div className="fade-y h-full overflow-y-auto p-3 md:p-4">
      <div className="mx-auto flex max-w-2xl flex-col gap-2">
        <form onSubmit={(e) => void add(e)} className="flex items-end gap-2">
          <div className="flex-1">
            <Textarea
              value={newText}
              onChange={(e) => setNewText(e.target.value)}
              placeholder="Add a memory…"
              autoComplete="off"
              rows={2}
              className="resize-y"
            />
          </div>
          <Button type="submit" variant="outline" disabled={adding || !newText.trim()} className="h-[42px] sm:h-[38px]">
            {adding ? <Spinner className="h-4 w-4" /> : 'Add'}
          </Button>
        </form>
        {error && <p className="text-xs text-red-400">{error}</p>}
        {memories !== null && memories.length === 0 && (
          <div className="flex flex-col items-center gap-2 py-10 text-center">
            <IconBrain className="h-8 w-8 text-border-strong" />
            <p className="text-sm text-subtle">No memories yet</p>
            <p className="max-w-[280px] text-xs text-faint">
              The agent saves durable facts and preferences for this project with the remember
              tool — or add one yourself above.
            </p>
          </div>
        )}
        {memories?.map((m) => (
          <div
            key={m.id}
            className="flex items-start gap-2 rounded-xl border border-border bg-surface px-3 py-2.5"
          >
            <div className="min-w-0 flex-1">
              <p
                className={`whitespace-pre-wrap break-words text-sm ${
                  m.enabled ? 'text-text' : 'text-faint line-through'
                }`}
              >
                {m.content}
              </p>
              {/* Tags are what the ranker matches separately from the
                  prose, so they are worth showing: they are how the user
                  sees why a memory will be found again. */}
              {m.tags && (
                <div className="mt-1.5 flex flex-wrap gap-1">
                  {m.tags
                    .split(',')
                    .map((t) => t.trim())
                    .filter(Boolean)
                    .map((t) => (
                      <span
                        key={t}
                        className="rounded-full border border-border bg-bg px-1.5 py-0.5 text-[10px] text-subtle"
                      >
                        {t}
                      </span>
                    ))}
                </div>
              )}
            </div>
            {/* The toggle and the actions stack vertically. Side by side
                they claimed over 100px of a narrow row, which is width the
                memory itself needs. They spread across the full height of
                the row rather than clustering at the top, so a long memory
                does not leave the controls bunched against its first line.
                The row id is deliberately not shown: it means nothing to
                the person reading their own memories. */}
            <div className="flex shrink-0 flex-col items-center justify-between gap-1.5 self-stretch">
              <button
                type="button"
                role="switch"
                aria-checked={m.enabled}
                aria-label={`Toggle memory ${m.id}`}
                title={m.enabled ? 'Disable memory (kept, but excluded from the prompt)' : 'Enable memory'}
                onClick={() => void toggle(m.id, !m.enabled)}
                className={`relative h-5 w-9 shrink-0 rounded-full transition-colors ${
                  m.enabled ? 'bg-accent' : 'bg-border'
                }`}
              >
                <span
                  className={`absolute top-0.5 h-4 w-4 rounded-full bg-bg transition-all ${
                    m.enabled ? 'left-[18px]' : 'left-0.5'
                  }`}
                />
              </button>
              <IconButton
                aria-label={`Edit memory ${m.id}`}
                title="Edit memory"
                onClick={() => setEditing({ id: m.id, text: m.content, tags: m.tags ?? '' })}
                className="h-7! w-7! shrink-0"
              >
                <IconPencil className="h-3.5 w-3.5" />
              </IconButton>
              <IconButton
                aria-label={`Delete memory ${m.id}`}
                title="Delete memory"
                onClick={() => void remove(m.id)}
                className="h-7! w-7! shrink-0 hover:text-red-400"
              >
                <IconX className="h-3.5 w-3.5" />
              </IconButton>
            </div>
          </div>
        ))}
      </div>

      <Dialog
        open={editing !== null}
        onClose={() => setEditing(null)}
        title="Edit memory"
        wide
        fullScreen
        fixedBody
        align="top"
      >
        <div className="flex h-full min-h-0 flex-col gap-3">
          <Field label="Memory">
            <Textarea
              value={editing?.text ?? ''}
              onChange={(e) =>
                setEditing((cur) => (cur ? { ...cur, text: e.target.value } : cur))
              }
              rows={10}
              className="resize-y"
              autoFocus
            />
          </Field>
          <Field label="Tags">
            <Input
              value={editing?.tags ?? ''}
              onChange={(e) =>
                setEditing((cur) => (cur ? { ...cur, tags: e.target.value } : cur))
              }
              placeholder="sqlite, migrations, store.go"
              autoComplete="off"
            />
          </Field>
          <p className="text-xs text-subtle">
            Comma-separated short technical terms. Tags are matched separately from the prose and
            are the strongest retrieval signal.
          </p>
          <div className="mt-auto flex justify-end gap-2 pt-2">
            <Button variant="outline" onClick={() => setEditing(null)}>
              Cancel
            </Button>
            <Button onClick={() => void saveEdit()} disabled={savingEdit || !editing?.text.trim()}>
              {savingEdit ? <Spinner className="h-4 w-4" /> : 'Save'}
            </Button>
          </div>
        </div>
      </Dialog>
    </div>
  );
}
