import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Link, useParams } from 'react-router-dom';
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels';
import { api } from '../api';
import type { Project as ProjectType } from '../types';
import { errMsg, getChatSide } from '../utils';
import { useMediaQuery } from '../hooks/useMediaQuery';
import { Button, Center } from '../components/ui';
import {
  IconArrowLeft,
  IconChat,
  IconFolder,
  IconMonitor,
  IconSettings,
  IconTerminal,
} from '../components/icons';
import ChatPane from '../components/ChatPane';
import PreviewPane from '../components/PreviewPane';
import FilesPane from '../components/FilesPane';
import TerminalPane from '../components/TerminalPane';
import GitHubMenu from '../components/GitHubMenu';

type WorkspaceTab = 'preview' | 'files' | 'terminal';
type PaneName = 'chat' | WorkspaceTab;

const iconLinkClass =
  'inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-dim transition-colors hover:bg-border hover:text-text md:h-9 md:w-9';

const WORKSPACE_TABS: { id: WorkspaceTab; label: string; icon: ReactNode }[] = [
  { id: 'preview', label: 'Preview', icon: <IconMonitor className="h-4 w-4" /> },
  { id: 'files', label: 'Files', icon: <IconFolder className="h-4 w-4" /> },
  { id: 'terminal', label: 'Terminal', icon: <IconTerminal className="h-4 w-4" /> },
];

const MOBILE_NAV: { id: PaneName; label: string; icon: ReactNode }[] = [
  { id: 'chat', label: 'Chat', icon: <IconChat className="h-5 w-5" /> },
  { id: 'preview', label: 'Preview', icon: <IconMonitor className="h-5 w-5" /> },
  { id: 'files', label: 'Files', icon: <IconFolder className="h-5 w-5" /> },
  { id: 'terminal', label: 'Terminal', icon: <IconTerminal className="h-5 w-5" /> },
];

export default function Project() {
  const { id = '' } = useParams();
  const isDesktop = useMediaQuery('(min-width: 768px)');
  const [project, setProject] = useState<ProjectType | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<WorkspaceTab>('preview');
  const [mobilePane, setMobilePane] = useState<PaneName>('chat');
  const [visited, setVisited] = useState<ReadonlySet<PaneName>>(
    () => new Set<PaneName>(['chat', 'preview']),
  );
  const [previewRefreshKey, setPreviewRefreshKey] = useState(0);
  const [chatSide] = useState<'left' | 'right'>(() => getChatSide());
  const [llmReady, setLlmReady] = useState(false);

  useEffect(() => {
    api
      .getProject(id)
      .then(setProject)
      .catch((e) => setError(errMsg(e)));
  }, [id]);

  const loadSettings = useCallback(() => {
    api
      .getSettings()
      .then((s) => setLlmReady(s.llm.apiKeySet && s.llm.model.trim() !== ''))
      .catch(() => setLlmReady(false));
  }, []);

  useEffect(() => {
    void loadSettings();
  }, [loadSettings]);

  const active: PaneName = isDesktop ? tab : mobilePane;

  useEffect(() => {
    setVisited((prev) => {
      if (prev.has(active)) return prev;
      const next = new Set(prev);
      next.add(active);
      return next;
    });
  }, [active]);

  // Re-check LLM config when the chat pane becomes visible again.
  useEffect(() => {
    if (active === 'chat') void loadSettings();
  }, [active, loadSettings]);

  if (error) {
    return (
      <Center>
        <p className="text-sm text-dim">{error}</p>
        <Link to="/">
          <Button variant="outline">Back to projects</Button>
        </Link>
      </Center>
    );
  }

  const paneContent = (name: PaneName): ReactNode => {
    switch (name) {
      case 'chat':
        return (
          <ChatPane
              projectId={id}
              onPreviewRestart={() => setPreviewRefreshKey((k) => k + 1)}
              llmReady={llmReady}
            />
        );
      case 'preview':
        return <PreviewPane projectId={id} refreshKey={previewRefreshKey} />;
      case 'files':
        return <FilesPane projectId={id} />;
      case 'terminal':
        return <TerminalPane projectId={id} active={active === 'terminal'} />;
    }
  };

  const renderPanes = (names: PaneName[]) => (
    <>
      {names.map(
        (name) =>
          visited.has(name) && (
            <div key={name} className={active === name ? 'h-full min-h-0' : 'hidden'}>
              {paneContent(name)}
            </div>
          ),
      )}
    </>
  );

  const header = (
    <header className="flex h-14 shrink-0 items-center gap-1 border-b border-border px-2 md:h-12 md:gap-2 md:px-3">
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
      <GitHubMenu projectId={id} projectName={project?.name ?? ''} repoUrl={project?.repoUrl ?? ''} />
      <Link to="/settings" aria-label="Settings" className={iconLinkClass}>
        <IconSettings className="h-5 w-5" />
      </Link>
    </header>
  );

  const workspaceTabs = (
    <div className="flex h-11 shrink-0 items-center gap-0.5 border-b border-border px-2">
      {WORKSPACE_TABS.map((t) => (
        <button
          key={t.id}
          type="button"
          onClick={() => setTab(t.id)}
          className={`-mb-px flex h-11 items-center gap-1.5 border-b-2 px-3 text-sm transition-colors ${
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
  );

  const bottomNav = (
    <nav className="fixed inset-x-0 bottom-0 z-40 grid grid-cols-4 border-t border-border bg-bg/95 pb-[env(safe-area-inset-bottom)] backdrop-blur">
      {MOBILE_NAV.map((item) => (
        <button
          key={item.id}
          type="button"
          onClick={() => setMobilePane(item.id)}
          className={`flex h-14 flex-col items-center justify-center gap-0.5 text-[10px] transition-colors ${
            mobilePane === item.id ? 'text-accent' : 'text-subtle'
          }`}
        >
          {item.icon}
          {item.label}
        </button>
      ))}
    </nav>
  );

  const chatPanel = (
    <Panel defaultSize={35} minSize={20} className="min-h-0">
      {paneContent('chat')}
    </Panel>
  );

  const workspacePanel = (
    <Panel minSize={30} className="flex min-h-0 flex-col">
      {workspaceTabs}
      <div className="min-h-0 flex-1">{renderPanes(['preview', 'files', 'terminal'])}</div>
    </Panel>
  );

  const resizeHandle = (
    <PanelResizeHandle className="w-1 bg-border transition-colors hover:bg-accent data-[resize-handle-state=drag]:bg-accent" />
  );

  return (
    <div className="flex h-dvh flex-col overflow-hidden">
      {header}
      {isDesktop ? (
        <div className="min-h-0 flex-1">
          <PanelGroup direction="horizontal" autoSaveId={`v1-project-${id}-${chatSide}`}>
            {chatSide === 'left' ? (
              <>
                {chatPanel}
                {resizeHandle}
                {workspacePanel}
              </>
            ) : (
              <>
                {workspacePanel}
                {resizeHandle}
                {chatPanel}
              </>
            )}
          </PanelGroup>
        </div>
      ) : (
        <div className="min-h-0 flex-1 pb-[calc(3.5rem+env(safe-area-inset-bottom))]">
          {renderPanes(['chat', 'preview', 'files', 'terminal'])}
        </div>
      )}
      {!isDesktop && bottomNav}
    </div>
  );
}
