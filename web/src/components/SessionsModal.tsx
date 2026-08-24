import { useState } from 'react';
import type { ChatSession } from '../types';
import { Dialog, IconButton, Input } from './ui';
import { IconArchive, IconCheck, IconPencil, IconPlus, IconTrash, IconUndo, IconX } from './icons';

/**
 * Chat session switcher — same layout as the model picker: a "New session"
 * action on top, the switchable session list below. Switching reloads the
 * chat with that session's thread; a new session starts an empty thread.
 * The pencil on a row renames that session inline; the archive icon hides it
 * (restorable from the Archived tab). The header and close button share one
 * inline row.
 */
export default function SessionsModal({
  open,
  onClose,
  sessions,
  activeId,
  onSwitch,
  onNew,
  onRename,
  onArchive,
  onUnarchive,
  onDelete,
  creating,
}: {
  open: boolean;
  onClose: () => void;
  sessions: ChatSession[];
  activeId: string;
  onSwitch: (id: string) => void;
  onNew: () => void;
  onRename: (id: string, name: string) => void;
  onArchive: (id: string) => void;
  onUnarchive: (id: string) => void;
  onDelete: (id: string) => void;
  creating: boolean;
}) {
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editText, setEditText] = useState('');
  const [tab, setTab] = useState<'active' | 'archived'>('active');

  const save = (id: string) => {
    if (editingId === id && editText.trim()) {
      onRename(id, editText.trim());
    }
    setEditingId(null);
  };

  const archived = sessions.filter((s) => s.archived);
  const active = sessions.filter((s) => !s.archived);

  const renderRow = (s: ChatSession) => (
    <div
      key={s.id}
      className={`flex min-h-[48px] items-center gap-2 rounded-lg border px-3 py-2 transition-colors ${
        s.id === activeId ? 'border-accent bg-surface' : 'border-border'
      }`}
    >
      {s.archived && tab === 'archived' && (
        <IconArchive className="h-4 w-4 shrink-0 text-faint" />
      )}
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
      {s.archived && tab === 'archived' ? (
        <>
          <button
            type="button"
            aria-label={`Restore ${s.name}`}
            title="Restore"
            onClick={() => onUnarchive(s.id)}
            className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-faint transition-colors hover:bg-border hover:text-text"
          >
            <IconUndo className="h-4 w-4" />
          </button>
          <button
            type="button"
            aria-label={`Delete ${s.name}`}
            title="Delete permanently"
            onClick={() => onDelete(s.id)}
            className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-faint transition-colors hover:bg-border hover:text-red-400"
          >
            <IconTrash className="h-4 w-4" />
          </button>
        </>
      ) : (
        <>
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
          <button
            type="button"
            aria-label={`Archive ${s.name}`}
            title="Archive"
            onClick={() => onArchive(s.id)}
            className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-faint transition-colors hover:bg-border hover:text-text"
          >
            <IconArchive className="h-4 w-4" />
          </button>
          {s.id === activeId && <IconCheck className="h-4 w-4 shrink-0 text-accent" />}
        </>
      )}
    </div>
  );

  return (
    <Dialog open={open} onClose={onClose} title="" wide fullScreen fixedBody align="top">
      {/* Header and close button share one inline row (the Dialog supplies
          the close; this just left-aligns the title with it). */}
      <div className="flex shrink-0 items-center justify-between gap-2">
        <h2 className="text-lg font-semibold text-text">Sessions</h2>
        <IconButton onClick={onClose} aria-label="Close" className="-mr-1 h-8 w-8 md:h-8 md:w-8">
          <IconX className="h-4 w-4" />
        </IconButton>
      </div>

      <div className="flex h-full min-h-0 flex-col gap-4 pt-3">
        <section className="shrink-0">
          {tab === 'active' ? (
            <button
              type="button"
              onClick={onNew}
              disabled={creating}
              className="flex min-h-[48px] w-full items-center gap-2 rounded-lg border border-dashed border-border-strong px-3 py-2 text-left text-sm text-dim transition-colors hover:border-accent hover:text-text disabled:opacity-60"
            >
              <IconPlus className="h-4 w-4 shrink-0 text-accent" />
              New session
            </button>
          ) : (
            <p className="px-1 text-[11px] text-faint">
              Archived sessions are hidden from the list above. Restore one to
              bring it back, or delete it permanently.
            </p>
          )}
        </section>
        <div className="fade-y min-h-0 min-w-0 flex-1 overflow-y-auto overscroll-contain">
          <div className="flex flex-col gap-1">
            {(tab === 'active' ? active : archived).length === 0 && (
              <p className="px-1 py-2 text-xs text-faint">
                {tab === 'active' ? 'No sessions yet.' : 'Nothing archived yet.'}
              </p>
            )}
            {(tab === 'active' ? active : archived).map(renderRow)}
          </div>
        </div>
      </div>
      <div className="mt-4 flex shrink-0 gap-1 rounded-lg border border-border bg-surface p-1">
        <button
          type="button"
          onClick={() => setTab('active')}
          className={`min-h-[34px] flex-1 rounded-md text-sm transition-colors ${
            tab === 'active' ? 'bg-border text-text' : 'text-dim hover:text-text'
          }`}
        >
          Active ({active.length})
        </button>
        <button
          type="button"
          onClick={() => setTab('archived')}
          className={`flex min-h-[34px] flex-1 items-center justify-center gap-1.5 rounded-md text-sm transition-colors ${
            tab === 'archived' ? 'bg-border text-text' : 'text-dim hover:text-text'
          }`}
        >
          <IconArchive className="h-3.5 w-3.5" />
          Archived ({archived.length})
        </button>
      </div>
    </Dialog>
  );
}