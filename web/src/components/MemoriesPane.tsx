import { useCallback, useEffect, useState } from 'react';
import { api } from '../api';
import type { Memory } from '../types';
import { errMsg } from '../utils';
import { IconButton, Spinner } from './ui';
import { IconBrain, IconX } from './icons';

// Browse and delete the facts the agent saved with the remember tool.
export default function MemoriesPane({ projectId }: { projectId: string }) {
  const [memories, setMemories] = useState<Memory[] | null>(null);
  const [error, setError] = useState<string | null>(null);

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
        {error && <p className="text-xs text-red-400">{error}</p>}
        {memories !== null && memories.length === 0 && (
          <div className="flex flex-col items-center gap-2 py-10 text-center">
            <IconBrain className="h-8 w-8 text-border-strong" />
            <p className="text-sm text-subtle">No memories yet</p>
            <p className="max-w-[280px] text-xs text-faint">
              The agent saves durable facts and preferences for this project with the remember
              tool. They show up here.
            </p>
          </div>
        )}
        {memories?.map((m) => (
          <div
            key={m.id}
            className="flex items-start gap-2 rounded-xl border border-border bg-surface px-3 py-2.5"
          >
            <div className="min-w-0 flex-1">
              <p className="whitespace-pre-wrap break-words text-sm text-text">{m.content}</p>
              <p className="mt-1 font-mono text-[10px] text-faint">#{m.id}</p>
            </div>
            <IconButton
              aria-label={`Delete memory ${m.id}`}
              title="Delete memory"
              onClick={() => void remove(m.id)}
              className="h-7! w-7! shrink-0 hover:text-red-400"
            >
              <IconX className="h-3.5 w-3.5" />
            </IconButton>
          </div>
        ))}
      </div>
    </div>
  );
}
