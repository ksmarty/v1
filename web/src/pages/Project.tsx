import { useCallback, useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels';
import { api } from '../api';
import type { Project as ProjectType } from '../types';
import { errMsg, getChatSide } from '../utils';
import { useMediaQuery } from '../hooks/useMediaQuery';
import { Button, Center } from '../components/ui';
import { IconChat, IconChevronLeft, IconMonitor } from '../components/icons';
import ChatPanel from '../components/ChatPanel';
import PreviewPane from '../components/PreviewPane';

export default function Project() {
  const { id = '' } = useParams();
  const isDesktop = useMediaQuery('(min-width: 768px)');
  const [project, setProject] = useState<ProjectType | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [mobilePane, setMobilePane] = useState<'chat' | 'preview'>('chat');
  const [chatCollapsed, setChatCollapsed] = useState(false);
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
      .then((s) => setLlmReady(s.llm.apiKeySet))
      .catch(() => setLlmReady(false));
  }, []);

  useEffect(() => {
    void loadSettings();
  }, [loadSettings]);

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

  const chatSidePanel = (
    <ChatPanel
      projectId={id}
      project={project}
      llmReady={llmReady}
      showCollapse={isDesktop}
      onCollapse={() => setChatCollapsed(true)}
      onPreviewRestart={() => setPreviewRefreshKey((k) => k + 1)}
    />
  );

  const previewPanel = <PreviewPane projectId={id} refreshKey={previewRefreshKey} />;

  const expandStrip = (
    <button
      type="button"
      onClick={() => setChatCollapsed(false)}
      aria-label="Show chat"
      title="Show chat"
      className="flex w-9 shrink-0 items-start justify-center border-r border-border pt-3 text-dim transition-colors hover:bg-border/60 hover:text-text"
    >
      <IconChevronLeft className="h-5 w-5 rotate-180" />
    </button>
  );

  const resizeHandle = (
    <PanelResizeHandle className="w-1 bg-border transition-colors hover:bg-accent data-[resize-handle-state=drag]:bg-accent" />
  );

  return (
    <div className="v1-safe-top flex h-[var(--v1-app-height,100dvh)] flex-col overflow-hidden">
      {isDesktop ? (
        chatCollapsed ? (
          <div className="flex min-h-0 flex-1">
            {chatSide === 'left' && expandStrip}
            <div className="min-h-0 flex-1">{previewPanel}</div>
            {chatSide === 'right' && expandStrip}
          </div>
        ) : (
          <div className="min-h-0 flex-1">
            <PanelGroup direction="horizontal" autoSaveId={`v1-project-${id}-${chatSide}`}>
              {chatSide === 'left' ? (
                <>
                  <Panel defaultSize={35} minSize={20} className="min-h-0">
                    {chatSidePanel}
                  </Panel>
                  {resizeHandle}
                  <Panel minSize={30} className="min-h-0">
                    {previewPanel}
                  </Panel>
                </>
              ) : (
                <>
                  <Panel minSize={30} className="min-h-0">
                    {previewPanel}
                  </Panel>
                  {resizeHandle}
                  <Panel defaultSize={35} minSize={20} className="min-h-0">
                    {chatSidePanel}
                  </Panel>
                </>
              )}
            </PanelGroup>
          </div>
        )
      ) : (
        <>
          {/* The bottom nav is in-flow (not fixed): with the shell pinned to
              the JS-measured viewport height, the bar can't float above the
              screen edge in iOS standalone mode. */}
          <div className="min-h-0 flex-1 overflow-hidden">
            <div className={mobilePane === 'chat' ? 'h-full min-h-0 min-w-0' : 'hidden'}>
              {chatSidePanel}
            </div>
            <div className={mobilePane === 'preview' ? 'h-full min-h-0 min-w-0' : 'hidden'}>
              {previewPanel}
            </div>
          </div>
          <nav className="grid shrink-0 grid-cols-2 border-t border-border bg-bg pb-[env(safe-area-inset-bottom)]">
            {[
              { id: 'chat' as const, label: 'Chat', icon: <IconChat className="h-5 w-5" /> },
              { id: 'preview' as const, label: 'Preview', icon: <IconMonitor className="h-5 w-5" /> },
            ].map((item) => (
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
        </>
      )}
    </div>
  );
}
