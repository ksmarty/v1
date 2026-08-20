import { useCallback, useEffect, useState } from 'react';
import { Link, useLocation, useParams } from 'react-router-dom';
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels';
import { api } from '../api';
import type { Project as ProjectType } from '../types';
import { errMsg, getChatSide, getDebugHud } from '../utils';
import { useMediaQuery } from '../hooks/useMediaQuery';
import { Button, Center, Spinner } from '../components/ui';
import { IconChat, IconChevronLeft, IconMonitor } from '../components/icons';
import ChatPanel from '../components/ChatPanel';
import PreviewPane from '../components/PreviewPane';

// Diagnostic overlay for the iOS PWA stale-viewport issue; enabled via
// Settings → Appearance → Debug HUD (v1.debugHud in localStorage).
function DebugHud() {
  const [txt, setTxt] = useState('');
  const [hidden, setHidden] = useState(false);
  useEffect(() => {
    const update = () => {
      const probe = document.getElementById('v1-dvh-probe');
      let stored = '?';
      try {
        stored = localStorage.getItem('v1-app-height-v2:portrait') ?? '-';
      } catch {
        stored = 'err';
      }
      setTxt(
        `ih=${window.innerHeight} ch=${document.documentElement.clientHeight} ` +
          `vv=${Math.round(window.visualViewport?.height ?? 0)} dvh=${probe?.offsetHeight ?? '?'} ` +
          `var=${document.documentElement.style.getPropertyValue('--v1-app-height') || '-'} stored=${stored} ` +
          `sy=${Math.round(window.scrollY)}`,
      );
    };
    update();
    const t = setInterval(update, 500);
    return () => clearInterval(t);
  }, []);
  if (hidden) return null;
  return (
    <>
      <div id="v1-dvh-probe" className="pointer-events-none absolute left-0 top-0 h-dvh w-0" />
      <button
        type="button"
        onClick={() => setHidden(true)}
        className="fixed bottom-1 left-1 z-[100] max-w-[95vw] rounded bg-red-900/80 px-1.5 py-0.5 text-left font-mono text-[10px] text-white"
      >
        {txt}
      </button>
    </>
  );
}

export default function Project() {
  const { id = '' } = useParams();
  const location = useLocation();
  // Description from the New project dialog, auto-sent as the first message,
  // plus the optional model selection that goes with it.
  const initialPrompt = (location.state as { prompt?: string } | null)?.prompt;
  const initialState = location.state as
    | { providerId?: string; model?: string; thinking?: string }
    | null;
  const isDesktop = useMediaQuery('(min-width: 768px)');
  const [project, setProject] = useState<ProjectType | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [mobilePane, setMobilePane] = useState<'chat' | 'preview'>('chat');
  const [chatCollapsed, setChatCollapsed] = useState(false);
  // Bumped when the bottom-nav Chat button is pressed — tells ChatPanel to
  // return to its chat subtab even if the user was on files/terminal/etc.
  const [chatResetSignal, setChatResetSignal] = useState(0);
  const [previewRefreshKey, setPreviewRefreshKey] = useState(0);
  const [chatSide] = useState<'left' | 'right'>(() => getChatSide());
  const [debugHud] = useState(() => getDebugHud());
  const [llmReady, setLlmReady] = useState<boolean | null>(null);

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

  if (!project) {
    return (
      <Center>
        <div className="flex flex-col items-center gap-3">
          <Spinner className="h-6 w-6" />
          <p className="text-sm text-dim">Loading project…</p>
        </div>
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
      onProjectChange={setProject}
      onProjectRename={(name) => setProject((prev) => (prev ? { ...prev, name } : prev))}
      initialPrompt={initialPrompt}
      initialProviderId={initialState?.providerId}
      initialModel={initialState?.model}
      initialThinking={initialState?.thinking}
      chatResetSignal={chatResetSignal}
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
    <div className="v1-safe-top flex h-[max(var(--v1-app-height,0px),100dvh)] flex-col overflow-hidden">
      {debugHud && <DebugHud />}
      {isDesktop ? (        chatCollapsed ? (
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
          {/* The bottom nav is in-flow (not fixed): the shell is pinned to
              max(100dvh, JS-measured/persisted viewport height) — whichever
              metric iOS currently reports taller wins, so the bar can't float
              above the screen edge in standalone mode. */}
          <div className="min-h-0 flex-1 overflow-hidden">
            <div className={mobilePane === 'chat' ? 'h-full min-h-0 min-w-0' : 'hidden'}>
              {chatSidePanel}
            </div>
            <div className={mobilePane === 'preview' ? 'h-full min-h-0 min-w-0' : 'hidden'}>
              {previewPanel}
            </div>
          </div>
          <nav className="grid shrink-0 grid-cols-2 border-t border-border bg-bg pb-[calc(env(safe-area-inset-bottom)/2)]">
            {[
              { id: 'chat' as const, label: 'Chat', icon: <IconChat className="h-5 w-5" /> },
              { id: 'preview' as const, label: 'Preview', icon: <IconMonitor className="h-5 w-5" /> },
            ].map((item) => (
              <button
                key={item.id}
                type="button"
                onClick={() => {
                  setMobilePane(item.id);
                  if (item.id === 'chat') setChatResetSignal((n) => n + 1);
                }}
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
