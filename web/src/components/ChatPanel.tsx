import { useEffect, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import type { Project as ProjectType } from '../types';
import { getChatTabLayout, type ChatTab } from '../utils';
import ChatPane from './ChatPane';
import FilesPane from './FilesPane';
import TerminalPane from './TerminalPane';
import GitPane from './GitPane';
import MemoriesPane from './MemoriesPane';
import GitHubPane from './GitHubPane';
import ProjectPane from './ProjectPane';
import GitHubMenu from './GitHubMenu';
import VercelMenu from './VercelMenu';
import type { Memory } from '../types';
import {
  IconArrowLeft,
  IconBrain,
  IconChat,
  IconChevronLeft,
  IconFolder,
  IconGitBranch,
  IconGitHub,
  IconSettings,
  IconTerminal,
} from './icons';

const iconLinkClass =
  'inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-dim transition-colors hover:bg-border hover:text-text md:h-9 md:w-9';

const TABS: { id: ChatTab; label: string; icon: ReactNode }[] = [
  { id: 'chat', label: 'Chat', icon: <IconChat className="h-4 w-4" /> },
  { id: 'files', label: 'Files', icon: <IconFolder className="h-4 w-4" /> },
  { id: 'terminal', label: 'Terminal', icon: <IconTerminal className="h-4 w-4" /> },
  { id: 'git', label: 'Git', icon: <IconGitBranch className="h-4 w-4" /> },
  { id: 'memories', label: 'Memories', icon: <IconBrain className="h-4 w-4" /> },
  { id: 'github', label: 'GitHub', icon: <IconGitHub className="h-4 w-4" /> },
  { id: 'project', label: 'Project', icon: <IconSettings className="h-4 w-4" /> },
];

export default function ChatPanel({
  projectId,
  project,
  llmReady,
  showCollapse,
  onCollapse,
  onPreviewRestart,
  onProjectChange,
  onProjectRename,
  initialPrompt,
  initialProviderId,
  initialModel,
  initialThinking,
}: {
  projectId: string;
  project: ProjectType | null;
  /** null while the LLM configuration is still loading. */
  llmReady: boolean | null;
  showCollapse: boolean;
  onCollapse: () => void;
  onPreviewRestart: () => void;
  onProjectChange: (p: ProjectType) => void;
  /** The agent renamed the project (set_project_name). */
  onProjectRename?: (name: string) => void;
  /** Description from the New project dialog — auto-sent as the first message. */
  initialPrompt?: string;
  /** Optional model selection from the New project dialog. */
  initialProviderId?: string;
  initialModel?: string;
  initialThinking?: string;
}) {
  const [tabLayout] = useState(() => getChatTabLayout());
  // Tabs in the user's chosen order, minus the ones they excluded.
  const tabs = TABS.filter((t) => !tabLayout.hidden.includes(t.id)).sort(
    (a, b) => tabLayout.order.indexOf(a.id) - tabLayout.order.indexOf(b.id),
  );
  const initialTab = tabLayout.order[0] ?? 'chat';
  const [tab, setTab] = useState<ChatTab>(initialTab);
  // Live memory list pushed up from the chat stream when the agent uses the
  // remember/forget tools — forwarded to the Memories tab.
  const [liveMemories, setLiveMemories] = useState<Memory[] | null>(null);
  const [visited, setVisited] = useState<ReadonlySet<ChatTab>>(
    () => new Set<ChatTab>([initialTab]),
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

      <div className="fade-x v1-no-scrollbar flex h-10 shrink-0 items-center gap-0.5 overflow-x-auto overflow-y-hidden border-b border-border px-2">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => setTab(t.id)}
            className={`-mb-px flex h-10 shrink-0 items-center gap-1.5 border-b-2 px-3 text-sm transition-colors ${
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
            onMemories={setLiveMemories}
            onProjectRename={onProjectRename}
            llmReady={llmReady}
            initialPrompt={initialPrompt}
            initialProviderId={initialProviderId}
            initialModel={initialModel}
            initialThinking={initialThinking}
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
            <MemoriesPane projectId={projectId} live={liveMemories} />
          </div>
        )}
        {visited.has('github') && (
          <div className={tab === 'github' ? 'h-full min-h-0' : 'hidden'}>
            <GitHubPane repoUrl={project?.repoUrl ?? ''} />
          </div>
        )}
        {visited.has('project') && (
          <div className={tab === 'project' ? 'h-full min-h-0' : 'hidden'}>
            <ProjectPane project={project} onProjectChange={onProjectChange} />
          </div>
        )}
      </div>
    </div>
  );
}
