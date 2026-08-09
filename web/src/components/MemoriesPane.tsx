import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { api } from '../api';
import type { Memory } from '../types';
import { errMsg } from '../utils';
import { Button, IconButton, Input, Spinner } from './ui';
import { IconBrain, IconCheck, IconPencil, IconX } from './icons';

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
  const [editing, setEditing] = useState<{ id: number; text: string } | null>(null);

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
    setError(null);
    try {
      const r = await api.updateMemory(projectId, editing.id, editing.text.trim());
      setMemories(r.memories);
      setEditing(null);
    } catch (err) {
      setError(errMsg(err));
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
            <Input
              value={newText}
              onChange={(e) => setNewText(e.target.value)}
              placeholder="Add a memory (max 300 chars)…"
              autoComplete="off"
              maxLength={300}
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
            {editing?.id === m.id ? (
              <div className="flex min-w-0 flex-1 items-end gap-2">
                <div className="flex-1">
                  <Input
                    value={editing.text}
                    onChange={(e) => setEditing({ id: m.id, text: e.target.value })}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault();
                        void saveEdit();
                      }
                      if (e.key === 'Escape') setEditing(null);
                    }}
                    autoFocus
                    maxLength={300}
                  />
                </div>
                <IconButton
                  aria-label="Save memory"
                  title="Save"
                  onClick={() => void saveEdit()}
                  className="h-7! w-7! shrink-0 text-accent"
                >
                  <IconCheck className="h-3.5 w-3.5" />
                </IconButton>
                <IconButton
                  aria-label="Cancel editing"
                  title="Cancel"
                  onClick={() => setEditing(null)}
                  className="h-7! w-7! shrink-0"
                >
                  <IconX className="h-3.5 w-3.5" />
                </IconButton>
              </div>
            ) : (
              <>
                <div className="min-w-0 flex-1">
                  <p className="whitespace-pre-wrap break-words text-sm text-text">{m.content}</p>
                  <p className="mt-1 font-mono text-[10px] text-faint">#{m.id}</p>
                </div>
                <IconButton
                  aria-label={`Edit memory ${m.id}`}
                  title="Edit memory"
                  onClick={() => setEditing({ id: m.id, text: m.content })}
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
              </>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
