import { useState } from 'react';
import type { ChatSession } from '../types';
import { Dialog, Input } from './ui';
import { IconCheck, IconPencil, IconPlus } from './icons';

/**
 * Chat session switcher — same layout as the model picker: a "New session"
 * action on top, the switchable session list below. Switching reloads the
 * chat with that session's thread; a new session starts an empty thread.
 * The pencil on a row renames that session inline.
 */
export default function SessionsModal({
  open,
  onClose,
  sessions,
  activeId,
  onSwitch,
  onNew,
  onRename,
  creating,
}: {
  open: boolean;
  onClose: () => void;
  sessions: ChatSession[];
  activeId: string;
  onSwitch: (id: string) => void;
  onNew: () => void;
  onRename: (id: string, name: string) => void;
  creating: boolean;
}) {
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editText, setEditText] = useState('');

  const save = (id: string) => {
    if (editingId === id && editText.trim()) {
      onRename(id, editText.trim());
    }
    setEditingId(null);
  };

  return (
    <Dialog open={open} onClose={onClose} title="" wide fullScreen fixedBody align="top">
      {/* Larger, bordered header so the modal's reach is obvious and switching
          is easy (bigger touch targets). */}
      <div className="shrink-0 border-b border-border pb-3">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-lg font-semibold text-text">Sessions</h2>
        </div>
      </div>
      <div className="flex h-full min-h-0 flex-col gap-4 pt-3">
        <section className="shrink-0">
          <button
            type="button"
            onClick={onNew}
            disabled={creating}
            className="flex min-h-[48px] w-full items-center gap-2 rounded-lg border border-dashed border-border-strong px-3 py-2 text-left text-sm text-dim transition-colors hover:border-accent hover:text-text disabled:opacity-60"
          >
            <IconPlus className="h-4 w-4 shrink-0 text-accent" />
            New session
          </button>
          <p className="mt-1.5 px-1 text-[11px] text-faint">
            A new session starts an empty chat thread. Previous sessions stay
            available here.
          </p>
        </section>
        <div className="fade-y min-h-0 min-w-0 flex-1 overflow-y-auto overscroll-contain">
          <div className="flex flex-col gap-1">
            {sessions.length === 0 && (
              <p className="px-1 py-2 text-xs text-faint">No sessions yet.</p>
            )}
            {sessions.map((s) => (
              <div
                key={s.id}
                className={`flex min-h-[48px] items-center gap-2 rounded-lg border px-3 py-2 transition-colors ${
                  s.id === activeId ? 'border-accent bg-surface' : 'border-border'
                }`}
              >
                {editingId === s.id ? (
                  <Input
                    autoFocus
                    value={editText}
                    onChange={(e) => setEditText(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') save(s.id);
                      else if (e.key === 'Escape') setEditingId(null);
                    }}
                    onBlur={() => save(s.id)}
                    className="h-9 min-w-0 flex-1 text-sm"
                    spellCheck={false}
                  />
                ) : (
                  <button
                    type="button"
                    onClick={() => {
                      if (s.id !== activeId) onSwitch(s.id);
                      onClose();
                    }}
                    className="min-w-0 flex-1 truncate text-left text-[15px] text-text"
                  >
                    {s.name}
                  </button>
                )}
                <button
                  type="button"
                  aria-label={`Rename ${s.name}`}
                  title="Rename"
                  onClick={() => {
                    setEditingId(editingId === s.id ? null : s.id);
                    setEditText(s.name);
                  }}
                  className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-faint transition-colors hover:bg-border hover:text-text"
                >
                  <IconPencil className="h-4 w-4" />
                </button>
                {s.id === activeId && <IconCheck className="h-4 w-4 shrink-0 text-accent" />}
              </div>
            ))}
          </div>
        </div>
      </div>
    </Dialog>
  );
}
