import { useEffect, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import type { Project as ProjectType } from '../types';
import ChatPane from './ChatPane';
import FilesPane from './FilesPane';
import TerminalPane from './TerminalPane';
import GitPane from './GitPane';
import MemoriesPane from './MemoriesPane';
import GitHubMenu from './GitHubMenu';
import VercelMenu from './VercelMenu';
import {
  IconArrowLeft,
  IconBrain,
  IconChat,
  IconChevronLeft,
  IconFolder,
  IconGitBranch,
  IconSettings,
  IconTerminal,
} from './icons';

export type ChatTab = 'chat' | 'files' | 'terminal' | 'git' | 'memories';

const iconLinkClass =
  'inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-dim transition-colors hover:bg-border hover:text-text md:h-9 md:w-9';

const TABS: { id: ChatTab; label: string; icon: ReactNode }[] = [
  { id: 'chat', label: 'Chat', icon: <IconChat className="h-4 w-4" /> },
  { id: 'files', label: 'Files', icon: <IconFolder className="h-4 w-4" /> },
  { id: 'terminal', label: 'Terminal', icon: <IconTerminal className="h-4 w-4" /> },
  { id: 'git', label: 'Git', icon: <IconGitBranch className="h-4 w-4" /> },
  { id: 'memories', label: 'Memories', icon: <IconBrain className="h-4 w-4" /> },
];

export default function ChatPanel({
  projectId,
  project,
  llmReady,
  showCollapse,
  onCollapse,
  onPreviewRestart,
}: {
  projectId: string;
  project: ProjectType | null;
  llmReady: boolean;
  showCollapse: boolean;
  onCollapse: () => void;
  onPreviewRestart: () => void;
}) {
  const [tab, setTab] = useState<ChatTab>('chat');
  const [visited, setVisited] = useState<ReadonlySet<ChatTab>>(
    () => new Set<ChatTab>(['chat']),
  );

  useEffect(() => {
    setVisited((prev) => {
      if (prev.has(tab)) return prev;
      const next = new Set(prev);
      next.add(tab);
      return next;
    });
  }, [tab]);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex h-12 shrink-0 items-center gap-1 border-b border-border px-2">
        <Link to="/" aria-label="Back to projects" className={iconLinkClass}>
          <IconArrowLeft className="h-5 w-5" />
        </Link>
        <div className="min-w-0 flex-1 px-1">
          {project ? (
            <span className="block truncate text-sm font-medium text-text">{project.name}</span>
          ) : (
            <span className="block h-4 w-32 animate-pulse rounded bg-border" />
          )}
        </div>
        <GitHubMenu projectId={projectId} projectName={project?.name ?? ''} repoUrl={project?.repoUrl ?? ''} />
        <VercelMenu projectId={projectId} projectName={project?.name ?? ''} />
        <Link
          to="/settings"
          state={{ from: `/project/${projectId}` }}
          aria-label="Settings"
          className={iconLinkClass}
        >
          <IconSettings className="h-5 w-5" />
        </Link>
        {showCollapse && (
          <button
            type="button"
            onClick={onCollapse}
            aria-label="Collapse chat"
            title="Collapse chat"
            className={iconLinkClass}
          >
            <IconChevronLeft className="h-5 w-5" />
          </button>
        )}
      </header>

      <div className="flex h-10 shrink-0 items-center gap-0.5 border-b border-border px-2">
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => setTab(t.id)}
            className={`-mb-px flex h-10 items-center gap-1.5 border-b-2 px-3 text-sm transition-colors ${
              tab === t.id
                ? 'border-accent text-text'
                : 'border-transparent text-subtle hover:text-text'
            }`}
          >
            {t.icon}
            {t.label}
          </button>
        ))}
      </div>

      <div className="min-h-0 flex-1">
        {/* Chat stays mounted even when hidden so in-flight streaming continues. */}
        <div className={tab === 'chat' ? 'h-full min-h-0' : 'hidden'}>
          <ChatPane
            projectId={projectId}
            projectName={project?.name ?? ''}
            onPreviewRestart={onPreviewRestart}
            llmReady={llmReady}
          />
        </div>
        {visited.has('files') && (
          <div className={tab === 'files' ? 'h-full min-h-0' : 'hidden'}>
            <FilesPane projectId={projectId} />
          </div>
        )}
        {visited.has('terminal') && (
          <div className={tab === 'terminal' ? 'h-full min-h-0' : 'hidden'}>
            <TerminalPane projectId={projectId} active={tab === 'terminal'} />
          </div>
        )}
        {visited.has('git') && (
          <div className={tab === 'git' ? 'h-full min-h-0' : 'hidden'}>
            <GitPane projectId={projectId} onPreviewRestart={onPreviewRestart} />
          </div>
        )}
        {visited.has('memories') && (
          <div className={tab === 'memories' ? 'h-full min-h-0' : 'hidden'}>
            <MemoriesPane projectId={projectId} />
          </div>
        )}
      </div>
    </div>
  );
}
