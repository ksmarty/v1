import { memo, useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { api } from '../api';
import type {
  EmbeddingSettings,
  ExtensionConflict,
  ExtensionSettingField,
  InstalledExtension,
  InstalledSkill,
  MCPServer,
  MCPServerStatus,
  PermissionMode,
  SkillSearchResult,
} from '../types';
import { errMsg, randomId } from '../utils';
import { PERMISSION_MODES } from '../permissions';
import { Button, Dialog, Field, InfoTip, Input, SaveRow, Select, Spinner, toast } from './ui';
import CodeEditor from './CodeEditor';
import Markdown from './Markdown';
import NewExtensionDialog from './NewExtensionDialog';
import { IconCheck, IconExternalLink, IconFlask, IconPencil, IconX } from './icons';

const TABS = [
  { id: 'mcp', label: 'MCP' },
  { id: 'skills', label: 'Skills' },
  { id: 'extensions', label: 'Extensions' },
  { id: 'tools', label: 'Tools' },
  { id: 'perms', label: 'Permissions' },
] as const;

type Tab = (typeof TABS)[number]['id'];
export type ToolsTab = Tab;

const MEM_AUTO_HELP =
  'Off by default. The agent reads back each finished turn and keeps what will still matter later — decisions and their reasons, preferences, gotchas, what failed. It reads only that turn\'s exchange and never the transcript, so it cannot compound its own earlier memories, and a fact the project already remembers is refused rather than stored twice. Anything wrapped in <private> tags is stripped before saving.';
const EMBED_HELP =
  'With a provider set, a memory written one way is found by a question asked another. Without one, matching is lexical — it still works, it just misses a memory phrased differently. Built-in needs no key and nothing leaves the machine; or point at any OpenAI-compatible /embeddings endpoint (Ollama, LM Studio, OpenAI) or the Hugging Face inference API.';
const EMBED_NATIVE_HELP =
  'Paste the model page URL or an org/name id. Compatible families: BERT, DistilBERT, MiniLM and NomicBERT derivatives such as all-MiniLM-L6-v2, bge-small-en-v1.5, gte-small or nomic-embed-text-v1.5. RoBERTa, MPNet, DeBERTa and ModernBERT are refused rather than run incorrectly. Weights are downloaded once and cached in the data volume, so they survive a redeploy.';

// Web search is not registered at all without a key, so its row says which of
// the two states this account is in rather than repeating a static "needs a
// key" whatever the answer is.
function toolHint(t: { name: string; hint: string }, webKeySet: boolean): string {
  if (t.name === 'web_search' && !webKeySet) return 'Search the web — no API key';
  return t.hint;
}

function ViewOnSkillsMP({ href, name }: { href: string; name: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={`View ${name} on SkillsMP`}
      title="View on SkillsMP"
      className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-text"
    >
      <IconExternalLink className="h-3.5 w-3.5" />
    </a>
  );
}

// SkillsMP page URL for a skill, falling back to its GitHub repo when the
// marketplace route is missing (e.g. skills installed before the URL existed).
// 386158 -> "386k", 950 -> "950".
function formatStars(n: number): string {
  if (n >= 1000) {
    const k = n / 1000;
    return `${k >= 100 ? Math.round(k) : k.toFixed(1).replace(/\.0$/, '')}k`;
  }
  return String(n);
}

function skillsmpHref(sk: { skillsmpUrl?: string; githubUrl: string }): string {
  return sk.skillsmpUrl || sk.githubUrl;
}

type SkillPreviewTarget = {
  name: string;
  author: string;
  description: string;
  githubUrl: string;
  skillsmpUrl?: string;
  /** Set when opened from search results — enables the Install action. */
  result?: SkillSearchResult;
  /** Set when already installed — enables readme, toggle and remove. */
  installed?: InstalledSkill;
};

// Detail dialog for a skill: marketplace metadata, the installed SKILL.md
// when available, and install/toggle/remove actions — no trip to SkillsMP.
function SkillPreviewDialog({
  target,
  busy,
  onClose,
  onInstall,
  onToggle,
  onRemove,
}: {
  target: SkillPreviewTarget;
  busy: boolean;
  onClose: () => void;
  onInstall: () => void;
  onToggle: (enabled: boolean) => void;
  onRemove: () => void;
}) {
  const [readme, setReadme] = useState<string | null>(null);
  const [readmeLoading, setReadmeLoading] = useState(false);
  const [readmeError, setReadmeError] = useState<string | null>(null);
  useEffect(() => {
    // An installed skill is read from disk; anything else is fetched from its
    // repository, so the SKILL.md can be read before committing to an install.
    const installed = target.installed;
    const candidate = target.result;
    if (!installed && !candidate) return;
    setReadmeLoading(true);
    setReadme(null);
    setReadmeError(null);
    const load = installed
      ? api.skillReadme(installed.id).then((r) => r.content)
      : api.skillPreview(candidate!).then((r) => r.content);
    load
      .then((content) => setReadme(content))
      .catch(() => {
        setReadme(null);
        setReadmeError('Could not load this skill\u2019s SKILL.md.');
      })
      .finally(() => setReadmeLoading(false));
  }, [target.installed, target.result]);

  return (
    <Dialog open onClose={onClose} title={target.name} wide fullScreen fixedBody align="top">
      <div className="flex h-full min-h-0 flex-col gap-3">
        <p className="text-xs text-subtle">by {target.author}</p>
        {target.description && <p className="text-sm text-text">{target.description}</p>}
        {(readmeLoading || readme || readmeError) && <div className="shrink-0 border-t border-border" />}
        {readmeLoading && (
          <div className="flex justify-center py-4">
            <Spinner className="h-4 w-4" />
          </div>
        )}
        {readmeError && <p className="text-xs text-red-400">{readmeError}</p>}
        {readme && (
          <div className="fade-y min-h-0 flex-1 overflow-y-auto overscroll-contain rounded-lg border border-border bg-surface p-3">
            <Markdown text={readme} />
          </div>
        )}
        <div className="flex shrink-0 items-center gap-2">
          {target.installed ? (
            <>
              <Button variant="outline" onClick={() => onToggle(!target.installed!.enabled)}>
                {target.installed.enabled ? 'Disable' : 'Enable'}
              </Button>
              <Button variant="ghost" className="text-red-400" onClick={onRemove}>
                Remove
              </Button>
            </>
          ) : (
            target.result && (
              <Button variant="outline" onClick={onInstall} disabled={busy}>
                {busy ? <Spinner className="h-4 w-4" /> : 'Install'}
              </Button>
            )
          )}
          {skillsmpHref(target) && (
            <a
              href={skillsmpHref(target)}
              target="_blank"
              rel="noopener noreferrer"
              className="ml-auto inline-flex items-center gap-1 text-xs text-dim transition-colors hover:text-text"
            >
              View on SkillsMP
              <IconExternalLink className="h-3 w-3" />
            </a>
          )}
        </div>
      </div>
    </Dialog>
  );
}

/**
 * MCP servers, installed skills, and the tool approval mode. Shared by the
 * Settings page and the chat's quick-access tools dialog: one section at a
 * time under a fixed tab bar, with the active section scrolling below.
 */
function ToolSettings({
  initialTab = 'mcp',
  initialPermissionMode,
  onPermissionSaved,
  onPermissionModeChange,
}: {
  /** Tab to show when the dialog opens (used for deep links). */
  initialTab?: ToolsTab;
  /** Initial permission mode (from the chat header) to avoid a flash of the default. */
  initialPermissionMode?: PermissionMode;
  /** Called with the saved mode after a successful permission save. */
  onPermissionSaved?: (mode: PermissionMode) => void;
  /** Called immediately when the selected mode changes. */
  onPermissionModeChange?: (mode: PermissionMode) => void;
}) {
  const [tab, setTab] = useState<ToolsTab>(initialTab);

  // MCP servers
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [status, setStatus] = useState<Record<string, MCPServerStatus>>({});
  const [mcpName, setMcpName] = useState('');
  const [mcpCommand, setMcpCommand] = useState('');
  const [mcpTesting, setMcpTesting] = useState(false);
  const [mcpSaving, setMcpSaving] = useState(false);
  const [mcpError, setMcpError] = useState<string | null>(null);
  const [mcpTestResult, setMcpTestResult] = useState<{
    ok: boolean;
    tools?: { name: string; description: string }[];
    error?: string;
  } | null>(null);
  // Editing an existing server happens inline on the card; saving replaces it
  // in place (the id stays stable).
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editName, setEditName] = useState('');
  const [editCommand, setEditCommand] = useState('');
  const [editSaving, setEditSaving] = useState(false);
  const [editError, setEditError] = useState<string | null>(null);
  // Per-card connectivity test result and the card currently testing.
  const [cardTest, setCardTest] = useState<{
    id: string;
    ok: boolean;
    tools?: { name: string; description: string }[];
    error?: string;
  } | null>(null);
  const [cardTestingId, setCardTestingId] = useState<string | null>(null);
  // Remote servers authorize against their own OAuth server; this maps a server
  // id to whether v1 already holds a usable grant.
  const [oauth, setOAuth] = useState<Record<string, string>>({});
  const [oauthBusyId, setOAuthBusyId] = useState<string | null>(null);
  const [oauthNotice, setOAuthNotice] = useState<{ ok: boolean; text: string } | null>(null);

  // Skills (skillsmp)
  const [skills, setSkills] = useState<InstalledSkill[]>([]);
  const [skillQuery, setSkillQuery] = useState('');
  const [skillResults, setSkillResults] = useState<SkillSearchResult[]>([]);
  const [suggestedSkills, setSuggestedSkills] = useState<SkillSearchResult[]>([]);
  const [skillBusy, setSkillBusy] = useState(false);
  const [skillBusyId, setSkillBusyId] = useState<string | null>(null);
  const [skillError, setSkillError] = useState<string | null>(null);
  const [skillPreview, setSkillPreview] = useState<SkillPreviewTarget | null>(null);

  // Extensions: JS modules the sidecar loads to add tools, prompt sections or
  // hooks. An agent can write one, which is how v1 extends itself.
  const [extensions, setExtensions] = useState<InstalledExtension[]>([]);
  const [extErrors, setExtErrors] = useState<string[]>([]);
  const [extConflicts, setExtConflicts] = useState<ExtensionConflict[]>([]);
  const [extError, setExtError] = useState<string | null>(null);
  const [extBusy, setExtBusy] = useState(false);
  // Whether the "describe an extension" chat-starter dialog is open.
  const [newExtOpen, setNewExtOpen] = useState(false);
  const [extEditor, setExtEditor] = useState<{
    id: string;
    description: string;
    source: string;
    isNew: boolean;
    /** Bundled with v1: offered for disabling, never for deleting. */
    builtin: boolean;
    /** The source is still on its way; the dialog is already open. */
    loading?: boolean;
    /** The fields the extension declares, in the order it listed them. */
    schema: ExtensionSettingField[];
    /** The values being edited, keyed by field. */
    settings: Record<string, string | boolean>;
    /** The values as loaded, so Save can stay disabled until something changes. */
    original: {
      description: string;
      source: string;
      settings: Record<string, string | boolean>;
    };
  } | null>(null);
  // Saving back exactly what was loaded is never what you meant.
  const extDirty =
    extEditor !== null &&
    (extEditor.description !== extEditor.original.description ||
      extEditor.source !== extEditor.original.source ||
      extEditor.schema.some(
        (f) => extEditor.settings[f.key] !== extEditor.original.settings[f.key],
      ));
  // Which pane the extension dialog is showing: the detail (description,
  // settings), the source popup, or the full-height description editor.
  const [extView, setExtView] = useState<'detail' | 'code' | 'description'>('detail');

  // Approval mode
  const [permissionMode, setPermissionMode] = useState<PermissionMode>(initialPermissionMode ?? 'ask');
  const [savedMode, setSavedMode] = useState<PermissionMode>(initialPermissionMode ?? 'ask');
  const [permSaving, setPermSaving] = useState(false);
  const [permSaved, setPermSaved] = useState(false);
  const [permError, setPermError] = useState<string | null>(null);
  // Rewind approval (deleting chat history)
  const [rewindApproval, setRewindApproval] = useState(false);
  const [savedRewind, setSavedRewind] = useState(false);
  // Builtin agent tools that can be disabled per user in the Tools tab.
  // Builtin agent tools that can be disabled per user in the Tools tab,
  // grouped under headings so related capabilities sit together.
  const TOOL_GROUPS: { id: string; label: string; tools: { name: string; label: string; hint: string }[] }[] = [
    {
      id: 'files',
      label: 'Files',
      tools: [
        { name: 'list_files', label: 'List files', hint: 'List the project directory tree' },
        { name: 'search_files', label: 'Search files', hint: 'Grep/find inside the project' },
        { name: 'read_file', label: 'Read file', hint: 'Read a file’s contents' },
        { name: 'write_file', label: 'Write file', hint: 'Create or overwrite a file' },
        { name: 'edit_file', label: 'Edit file', hint: 'Apply an edit to a file' },
        { name: 'delete_file', label: 'Delete file', hint: 'Remove a file' },
        { name: 'move_file', label: 'Move file', hint: 'Rename/move a file' },
      ],
    },
    {
      id: 'run',
      label: 'Run & preview',
      tools: [
        { name: 'run_command', label: 'Run command', hint: 'Run a command in the project' },
        { name: 'run_command_background', label: 'Run in background', hint: 'Start a command detached from the turn' },
        { name: 'run_container', label: 'Run container', hint: 'Run a container for builds/tests' },
        { name: 'restart_preview', label: 'Restart preview', hint: 'Restart the app preview' },
        { name: 'screenshot_app', label: 'Screenshot app', hint: 'Capture the preview as an image' },
      ],
    },
    {
      id: 'web',
      label: 'Web',
      tools: [
        { name: 'fetch_url', label: 'Fetch URL', hint: 'Fetch and read a web page' },
        { name: 'web_search', label: 'Web search', hint: 'Search the web' },
      ],
    },
    {
      id: 'session',
      label: 'Session & memory',
      tools: [
        { name: 'set_project_name', label: 'Set project name', hint: 'Rename the project (first session only)' },
        { name: 'set_session_name', label: 'Set session name', hint: 'Rename the current session (new sessions)' },
        { name: 'set_todos', label: 'Update todos', hint: 'Keep the visible todo list' },
        { name: 'remember', label: 'Remember', hint: 'Save a durable project fact' },
        { name: 'forget', label: 'Forget', hint: 'Remove a saved fact' },
        { name: 'search_memories', label: 'Search memories', hint: 'Search saved facts by meaning' },
        { name: 'ask_user', label: 'Ask user', hint: 'Ask you a question mid-turn' },
      ],
    },
    {
      id: 'git',
      label: 'Git',
      tools: [{ name: 'git', label: 'Git', hint: 'Stage, commit and push' }],
    },
    {
      id: 'extensions',
      label: 'Extensions',
      tools: [
        { name: 'delegate', label: 'Delegate', hint: 'Hand a task to a sub-agent in its own context' },
      ],
    },
  ];

  const [disabledTools, setDisabledTools] = useState<string[]>([]);
  const [toolsError, setToolsError] = useState<string | null>(null);

  // Web search runs on a LangSearch key. It is stored per user, and the tool is
  // hidden from the agent entirely until one is set — the model should never be
  // offered a tool that cannot work.
  const [webKey, setWebKey] = useState('');
  const [webKeySet, setWebKeySet] = useState(false);
  const [webKeyHint, setWebKeyHint] = useState('');
  const [webSaving, setWebSaving] = useState(false);
  const [webError, setWebError] = useState<string | null>(null);

  const saveWebKey = async (value: string) => {
    setWebSaving(true);
    setWebError(null);
    try {
      await api.updateSettings({ webSearchKey: value });
      setWebKey('');
      const s = await api.getSettings();
      setWebKeySet(s.webSearch?.keySet ?? false);
      setWebKeyHint(s.webSearch?.keyHint ?? '');
      toast(value ? 'Web search key saved' : 'Web search key cleared');
    } catch (e) {
      setWebError(errMsg(e));
    } finally {
      setWebSaving(false);
    }
  };

  // Memory retrieval's embedding provider. Entirely optional: with none set,
  // memories are matched to the message lexically, which is what v1 did before
  // embeddings existed. The form is a draft that only persists on Save, because
  // a half-typed base URL would otherwise be stored and break retrieval
  // silently — which is exactly the failure this feature exists to avoid.
  const [emb, setEmb] = useState({ provider: '', model: '', baseUrl: '', apiKey: '' });
  const [embSaved, setEmbSaved] = useState<EmbeddingSettings | null>(null);
  // Automatic remembering is a single switch saved the moment it is flipped:
  // there is no draft to review, so a Save button would only add a step.
  const [memAuto, setMemAuto] = useState(false);
  const [memAutoSaving, setMemAutoSaving] = useState(false);
  const [embSaving, setEmbSaving] = useState(false);
  const [embError, setEmbError] = useState<string | null>(null);
  const [embTesting, setEmbTesting] = useState(false);
  const [embTest, setEmbTest] = useState<{ ok: boolean; error?: string; dims?: number } | null>(null);

  // A test result describes one provider+model pair, so changing either drops it
  // rather than leaving a green "Ready" next to something untested.
  const setEmbField = (patch: Partial<typeof emb>) => {
    setEmb((v) => ({ ...v, ...patch }));
    setEmbTest(null);
  };

  const testEmbedding = async () => {
    setEmbTesting(true);
    setEmbTest(null);
    try {
      setEmbTest(await api.testEmbedding({ provider: emb.provider, model: emb.model }));
    } catch (e) {
      setEmbTest({ ok: false, error: errMsg(e) });
    } finally {
      setEmbTesting(false);
    }
  };

  const embDirty =
    emb.provider !== (embSaved?.provider ?? '') ||
    emb.model !== (embSaved?.model ?? '') ||
    emb.baseUrl !== (embSaved?.baseUrl ?? '') ||
    emb.apiKey.trim() !== '';

  const saveEmbedding = async (clearKey = false) => {
    setEmbSaving(true);
    setEmbError(null);
    try {
      await api.updateSettings({
        embedding: {
          provider: emb.provider,
          model: emb.model,
          baseUrl: emb.baseUrl,
          apiKey: clearKey ? '' : emb.apiKey,
        },
      });
      setEmb((e) => ({ ...e, apiKey: '' }));
      const s = await api.getSettings();
      setEmbSaved(s.embedding ?? null);
      toast(clearKey ? 'Embedding key cleared' : 'Embedding provider saved');
    } catch (e) {
      setEmbError(errMsg(e));
    } finally {
      setEmbSaving(false);
    }
  };

  // Optimistic, like the tool switches: flip immediately and roll back if the
  // save fails.
  const toggleMemAuto = async () => {
    const next = !memAuto;
    setMemAuto(next);
    setMemAutoSaving(true);
    try {
      await api.updateSettings({ memoryAutoCapture: next });
      toast(
        next
          ? 'The agent will remember facts from each turn'
          : 'The agent will only remember when asked',
      );
    } catch (e) {
      setMemAuto(!next);
      toast(errMsg(e));
    } finally {
      setMemAutoSaving(false);
    }
  };

  // Optimistic: flip the switch immediately, persist after (no Save button —
  // every change auto-saves).
  const toggleTool = (name: string) => {
    setToolsError(null);
    const next = disabledTools.includes(name)
      ? disabledTools.filter((t) => t !== name)
      : [...disabledTools, name];
    setDisabledTools(next);
    api.updateSettings({ disabledTools: next }).catch((e) => setToolsError(errMsg(e)));
  };

  // Two layouts, by width. On a phone the three cards are taller than the
  // available height, so pinning them and giving the list the leftover space
  // shrank the list to zero: nothing appeared below the memory embeddings card.
  // There the whole column keeps its natural height and the tab's own scroller
  // moves it. From md up the cards fit, so the list gets its own scroll
  // container under them, the same shape the Skills and Extensions tabs use.
  const toolsSection = (
    <div className="flex min-w-0 flex-col gap-2.5 md:h-full md:min-h-0">
      <div className="shrink-0 rounded-lg border border-border-strong bg-surface/50 p-3 shadow-sm">
        <div className="flex items-center gap-2">
          <p className="text-sm text-text">Web search</p>
          <span
            className={`rounded-full border px-1.5 py-0.5 text-[10px] ${
              webKeySet
                ? 'border-accent/40 bg-accent/10 text-accent'
                : 'border-border bg-bg text-faint'
            }`}
          >
            {webKeySet ? 'enabled' : 'needs a key'}
          </span>
        </div>
        <p className="mt-0.5 text-[11px] text-faint">
          {webKeySet ? (
            <>
              Web search is on — the agent can look things up with the{' '}
              <span className="text-subtle">web_search</span> tool. Paste a different key to
              replace the one saved.
            </>
          ) : (
            <>
              The <span className="text-subtle">web_search</span> tool runs on LangSearch. Get an
              API key at{' '}
              <a
                href="https://langsearch.com/api-keys"
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-0.5 text-accent hover:underline"
              >
                langsearch.com
                <IconExternalLink className="h-3 w-3" />
              </a>{' '}
              and paste it here. Until a key is set the tool is not offered to the agent at all.
            </>
          )}
        </p>
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Input
            type="password"
            value={webKey}
            onChange={(e) => setWebKey(e.target.value)}
            placeholder={
              webKeySet
                ? webKeyHint
                  ? `${webKeyHint}… (set — enter to replace)`
                  : '•••••••• (set — enter to replace)'
                : 'Not set'
            }
            autoComplete="new-password"
            data-1p-ignore
            data-lpignore="true"
            className="min-w-0 flex-1"
          />
          <Button
            disabled={webSaving || webKey.trim() === ''}
            onClick={() => void saveWebKey(webKey.trim())}
          >
            Save
          </Button>
          {webKeySet && (
            <Button variant="ghost" disabled={webSaving} onClick={() => void saveWebKey('')}>
              Clear
            </Button>
          )}
        </div>
        {webError && <p className="mt-1.5 text-xs text-red-400">{webError}</p>}
      </div>
      <div className="shrink-0 rounded-lg border border-border-strong bg-surface/50 p-3 shadow-sm">
        <label className="flex items-start gap-3">
          <span className="min-w-0 flex-1">
            <span className="flex items-center gap-2">
              <span className="text-sm text-text">Remember automatically</span>
              <span
                className={`rounded-full border px-1.5 py-0.5 text-[10px] ${
                  memAuto
                    ? 'border-accent/40 bg-accent/10 text-accent'
                    : 'border-border bg-bg text-faint'
                }`}
              >
                {memAuto ? 'on' : 'off'}
              </span>
              <InfoTip text={MEM_AUTO_HELP} />
            </span>
            <span className="mt-0.5 block text-[11px] leading-relaxed text-faint">
              The agent keeps decisions, preferences and gotchas from each finished turn. One
              extra model call per turn.
            </span>
          </span>
          <button
            type="button"
            role="switch"
            aria-checked={memAuto}
            disabled={memAutoSaving}
            onClick={() => void toggleMemAuto()}
            className={`relative mt-0.5 h-5 w-9 shrink-0 rounded-full transition-colors disabled:opacity-50 ${
              memAuto ? 'bg-accent' : 'bg-border'
            }`}
          >
            <span
              className={`absolute top-0.5 h-4 w-4 rounded-full bg-bg transition-all ${
                memAuto ? 'left-[18px]' : 'left-0.5'
              }`}
            />
          </button>
        </label>
      </div>
      <div className="shrink-0 rounded-lg border border-border-strong bg-surface/50 p-3 shadow-sm">
        <div className="flex items-center gap-2">
          <p className="text-sm text-text">Memory embeddings</p>
          <span
            className={`rounded-full border px-1.5 py-0.5 text-[10px] ${
              embSaved?.enabled
                ? 'border-accent/40 bg-accent/10 text-accent'
                : 'border-border bg-bg text-faint'
            }`}
          >
            {embSaved?.enabled ? 'semantic' : 'lexical'}
          </span>
          <InfoTip text={EMBED_HELP} />
        </div>
        <p className="mt-0.5 text-[11px] text-faint">
          Optional. Matches memories by meaning instead of shared words. Pick{' '}
          <span className="text-subtle">Built-in</span> to run a model inside v1, or point at any{' '}
          <span className="text-subtle">/embeddings</span> endpoint.
        </p>
        {emb.provider === 'native' && (
          <div className="mt-1.5 flex items-start gap-1.5">
            <p className="min-w-0 text-[11px] text-faint">
              Any BERT-family encoder from{' '}
              <a
                href="https://huggingface.co/models?library=sentence-transformers&pipeline_tag=feature-extraction"
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-0.5 text-accent hover:underline"
              >
                Hugging Face
                <IconExternalLink className="h-3 w-3" />
              </a>{' '}
              works (MiniLM, bge, gte, nomic). RoBERTa, MPNet, DeBERTa and ModernBERT are
              refused. Weights download once and are cached.
            </p>
            <InfoTip text={EMBED_NATIVE_HELP} />
          </div>
        )}
        <div className="mt-2 grid gap-2 sm:grid-cols-2">
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-faint">Provider</span>
            <Select
              value={emb.provider}
              onChange={(e) => setEmbField({ provider: e.target.value })}
            >
              <option value="">None (lexical matching)</option>
              <option value="native">Built-in (runs in v1)</option>
              <option value="openai">OpenAI-compatible</option>
              <option value="huggingface">Hugging Face</option>
            </Select>
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-faint">
              {emb.provider === 'native' ? 'Model (Hugging Face repo or URL)' : 'Model'}
            </span>
            <Input
              value={emb.model}
              onChange={(e) => setEmbField({ model: e.target.value })}
              placeholder={
                emb.provider === 'native'
                  ? 'sentence-transformers/all-MiniLM-L6-v2'
                  : 'nomic-embed-text-v1.5'
              }
              autoComplete="off"
            />
          </label>
          {emb.provider === 'openai' && (
            <label className="flex flex-col gap-1 sm:col-span-2">
              <span className="text-[11px] text-faint">Base URL</span>
              <Input
                value={emb.baseUrl}
                onChange={(e) => setEmb((v) => ({ ...v, baseUrl: e.target.value }))}
                placeholder="http://localhost:11434/v1"
                autoComplete="off"
              />
            </label>
          )}
          {emb.provider !== 'native' && (
            <label className="flex flex-col gap-1 sm:col-span-2">
              <span className="text-[11px] text-faint">
                API key{emb.provider === 'openai' ? ' (optional for a local endpoint)' : ''}
              </span>
              <Input
                type="password"
                value={emb.apiKey}
                onChange={(e) => setEmb((v) => ({ ...v, apiKey: e.target.value }))}
                placeholder={
                  embSaved?.keySet
                    ? embSaved.keyHint
                      ? `${embSaved.keyHint}… (set — enter to replace)`
                      : '•••••••• (set — enter to replace)'
                    : 'Not set'
                }
                autoComplete="new-password"
                data-1p-ignore
                data-lpignore="true"
              />
            </label>
          )}
        </div>
        {emb.provider === 'native' && (
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Button variant="ghost" disabled={embTesting} onClick={() => void testEmbedding()}>
              {embTesting ? 'Downloading…' : embSaved?.downloaded ? 'Test' : 'Download & test'}
            </Button>
            {embSaved?.downloaded && (
              <span className="inline-flex items-center gap-1 text-[11px] text-accent">
                <IconCheck className="h-3 w-3" /> Downloaded
                {embSaved.dims > 0 ? ` — ${embSaved.dims}-dim` : ''}
              </span>
            )}
            {embTest && (
              <span
                className={`text-[11px] ${embTest.ok ? 'text-accent' : 'text-red-400'}`}
              >
                {embTest.ok
                  ? `Ready — ${embTest.dims}-dimension vectors`
                  : embTest.error}
              </span>
            )}
          </div>
        )}
        {embSaved?.enabled && embSaved.dims > 0 && (
          <p className="mt-1.5 text-[11px] text-faint">
            {embSaved.dims}-dimension vectors. Memories saved before this was configured are
            embedded in the background over the next few turns.
          </p>
        )}
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Button disabled={embSaving || !embDirty} onClick={() => void saveEmbedding()}>
            Save
          </Button>
          {embSaved?.keySet && (
            <Button variant="ghost" disabled={embSaving} onClick={() => void saveEmbedding(true)}>
              Clear key
            </Button>
          )}
        </div>
        {embError && <p className="mt-1.5 text-xs text-red-400">{embError}</p>}
      </div>
      <p className="shrink-0 text-xs text-faint">
        Disable agent tools you don&apos;t want the model to use. Disabled tools
        are hidden from the model and refused if called anyway. Changes save
        instantly.
      </p>
      {toolsError && <p className="shrink-0 text-xs text-red-400">{toolsError}</p>}
      {/* The list scrolls under the fixed cards, the way the Skills and
          Extensions tabs do. As one long column in the tab's outer scroller the
          cards pushed the list off the bottom, where it read as missing rather
          than as something to scroll to. */}
      <div className="fade-y v1-fade-y-md min-w-0 md:min-h-0 md:flex-1 md:overflow-x-hidden md:overflow-y-auto md:overscroll-contain">
        {TOOL_GROUPS.map((g) => (
          <div key={g.id} className="mb-3">
            <h4 className="mb-1.5 text-xs font-medium text-subtle">{g.label}</h4>
            <div className="flex flex-col gap-1.5">
              {g.tools.map((t) => {
                const off = disabledTools.includes(t.name);
                return (
                  <label
                    key={t.name}
                    className={`flex items-center gap-2.5 rounded-lg border px-3 py-2.5 shadow-sm transition-colors ${
                      off ? 'opacity-60' : 'border-border-strong bg-surface/50'
                    }`}
                  >
                    <div className="min-w-0 flex-1">
                      <p className="text-sm text-text">{t.label}</p>
                      <p className="truncate text-[11px] text-faint">{toolHint(t, webKeySet)}</p>
                    </div>
                    <button
                      type="button"
                      role="switch"
                      aria-checked={!off}
                      aria-label={`${off ? 'Enable' : 'Disable'} ${t.label}`}
                      onClick={() => toggleTool(t.name)}
                      className={`relative h-5 w-9 shrink-0 rounded-full transition-colors ${
                        off ? 'bg-border' : 'bg-accent'
                      }`}
                    >
                      <span
                        className={`absolute top-0.5 h-4 w-4 rounded-full bg-bg transition-all ${
                          off ? 'left-0.5' : 'left-[18px]'
                        }`}
                      />
                    </button>
                  </label>
                );
              })}
            </div>
          </div>
        ))}
      </div>
    </div>
  );

  const load = useCallback(async () => {
    try {
      const [s, st] = await Promise.all([api.getSettings(), api.mcpStatus()]);
      setServers((s.mcp ?? []).map((sv) => ({ ...sv, enabled: sv.enabled !== false })));
      setSkills(s.skills ?? []);
      setPermissionMode(s.permissionMode ?? 'ask');
      setSavedMode(s.permissionMode ?? 'ask');
      setRewindApproval(s.rewindApproval ?? false);
      setSavedRewind(s.rewindApproval ?? false);
      setDisabledTools(s.disabledTools ?? []);
      setWebKeySet(s.webSearch?.keySet ?? false);
      setWebKeyHint(s.webSearch?.keyHint ?? '');
      setEmbSaved(s.embedding ?? null);
      setMemAuto(s.memoryAutoCapture ?? false);
      setEmb({
        provider: s.embedding?.provider ?? '',
        model: s.embedding?.model ?? '',
        baseUrl: s.embedding?.baseUrl ?? '',
        apiKey: '',
      });
      const byId: Record<string, MCPServerStatus> = {};
      for (const sv of st.servers) byId[sv.id] = sv;
      setStatus(byId);
      setOAuth(st.oauth ?? {});
    } catch {
      // transient — keep the previous state
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // The OAuth callback lands back on /settings with the outcome in the query.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const result = params.get('mcp');
    if (!result) return;
    const reason = params.get('reason');
    setOAuthNotice(
      result === 'connected'
        ? { ok: true, text: 'Authorized. The server connects on the next chat.' }
        : { ok: false, text: reason || 'Authorization failed.' },
    );
    // Drop the parameters so a reload does not repeat the notice.
    const cleaned = new URL(window.location.href);
    cleaned.searchParams.delete('mcp');
    cleaned.searchParams.delete('reason');
    window.history.replaceState({}, '', cleaned.pathname + cleaned.search + cleaned.hash);
    void load();
  }, [load]);

  const connectMCP = async (srv: MCPServer) => {
    setOAuthBusyId(srv.id);
    setOAuthNotice(null);
    try {
      const { url } = await api.mcpOAuthStart(srv.id);
      // Leave the app for the authorization server; it sends the browser back
      // to /settings when the user is done.
      window.location.href = url;
    } catch (e) {
      setOAuthNotice({ ok: false, text: errMsg(e) });
      setOAuthBusyId(null);
    }
  };

  const disconnectMCP = async (srv: MCPServer) => {
    setOAuthBusyId(srv.id);
    setOAuthNotice(null);
    try {
      await api.mcpOAuthDisconnect(srv.id);
      await load();
    } catch (e) {
      setOAuthNotice({ ok: false, text: errMsg(e) });
    } finally {
      setOAuthBusyId(null);
    }
  };

  useEffect(() => {
    // Built-in skills are always shown in the "Suggested / included" group
    // below the search bar — the agent doesn't have to search for them.
    api
      .skillSearch('')
      .then((r) => setSuggestedSkills((r.skills ?? []).filter((s) => s.builtin)))
      .catch(() => {});
  }, []);

  useEffect(() => {
    setTab(initialTab);
  }, [initialTab]);

  // parseMCPSpec reads the "command line or URL" field. A URL selects the
  // streamable HTTP transport; anything else is a command line whose first
  // token is the executable.
  const parseMCPSpec = (raw: string, fallbackName: string) => {
    const value = raw.trim();
    if (/^https?:\/\//i.test(value)) {
      let name = fallbackName;
      if (!name) {
        try {
          name = new URL(value).host;
        } catch {
          name = 'server';
        }
      }
      return { name, command: '', args: [] as string[], url: value };
    }
    const parts = value.split(/\s+/).filter(Boolean);
    return {
      name: fallbackName || parts[0] || 'server',
      command: parts[0] ?? '',
      args: parts.slice(1),
      url: undefined,
    };
  };

  const parseMCP = (): MCPServer => {
    const spec = parseMCPSpec(mcpCommand, mcpName.trim());
    return { id: randomId(), ...spec, enabled: true };
  };

  const testMCP = async () => {
    if (!mcpCommand.trim()) {
      setMcpError('Enter a command line or URL to test.');
      return;
    }
    setMcpTesting(true);
    setMcpTestResult(null);
    setMcpError(null);
    try {
      setMcpTestResult(await api.mcpTest(parseMCP()));
    } catch (err) {
      setMcpTestResult({ ok: false, error: errMsg(err) });
    } finally {
      setMcpTesting(false);
    }
  };

  const addMCP = async (e: FormEvent) => {
    e.preventDefault();
    if (!mcpCommand.trim()) {
      setMcpError('Enter a command line or URL.');
      return;
    }
    setMcpSaving(true);
    setMcpError(null);
    try {
      await api.updateSettings({ mcp: [...servers, parseMCP()] });
      setMcpName('');
      setMcpCommand('');
      setMcpTestResult(null);
      await load();
    } catch (err) {
      setMcpError(errMsg(err));
    } finally {
      setMcpSaving(false);
    }
  };

  const toggleMCP = async (id: string, enabled: boolean) => {
    setMcpError(null);
    try {
      await api.updateSettings({ mcp: servers.map((s) => (s.id === id ? { ...s, enabled } : s)) });
      await load();
    } catch (err) {
      setMcpError(errMsg(err));
    }
  };

  const editMCP = (srv: MCPServer) => {
    if (editingId === srv.id) {
      setEditingId(null);
      return;
    }
    setEditingId(srv.id);
    setEditName(srv.name);
    setEditCommand(srv.url ?? [srv.command, ...srv.args].join(' '));
    setEditError(null);
    setCardTest(null);
  };

  const saveEdit = async (srv: MCPServer) => {
    if (!editCommand.trim()) {
      setEditError('Enter a command line or URL.');
      return;
    }
    setEditSaving(true);
    setEditError(null);
    try {
      const spec = parseMCPSpec(editCommand, editName.trim());
      const updated = servers.map((s) =>
        s.id === srv.id ? { ...s, ...spec, name: spec.name || s.name } : s,
      );
      await api.updateSettings({ mcp: updated });
      setEditingId(null);
      await load();
    } catch (err) {
      setEditError(errMsg(err));
    } finally {
      setEditSaving(false);
    }
  };

  const testCard = async (srv: MCPServer) => {
    setCardTestingId(srv.id);
    setCardTest(null);
    setMcpError(null);
    try {
      setCardTest({ id: srv.id, ...(await api.mcpTest(srv)) });
    } catch (err) {
      setCardTest({ id: srv.id, ok: false, error: errMsg(err) });
    } finally {
      setCardTestingId(null);
    }
  };

  const removeMCP = async (id: string) => {
    const srv = servers.find((s) => s.id === id);
    if (srv && !window.confirm(`Remove MCP server ${srv.name}?`)) return;
    setMcpError(null);
    try {
      await api.updateSettings({ mcp: servers.filter((s) => s.id !== id) });
      await load();
    } catch (err) {
      setMcpError(errMsg(err));
    }
  };

  const searchSkills = async (e?: FormEvent) => {
    e?.preventDefault();
    const q = skillQuery.trim();
    if (!q) return;
    setSkillBusy(true);
    setSkillError(null);
    try {
      const r = await api.skillSearch(q);
      setSkillResults(r.skills ?? []);
    } catch (err) {
      setSkillError(errMsg(err));
    } finally {
      setSkillBusy(false);
    }
  };

  const installSkill = async (skill: SkillSearchResult) => {
    setSkillBusyId(skill.id);
    setSkillError(null);
    try {
      const r = await api.skillInstall(skill);
      setSkillResults([]);
      setSkillQuery('');
      setSkills(r.skills ?? []);
    } catch (err) {
      setSkillError(errMsg(err));
    } finally {
      setSkillBusyId(null);
    }
  };

  const removeSkill = async (id: string) => {
    const sk = skills.find((s) => s.id === id);
    if (sk && !window.confirm(`Remove skill ${sk.name}?`)) return;
    setSkillError(null);
    try {
      const r = await api.skillRemove(id);
      setSkills(r.skills ?? []);
    } catch (err) {
      setSkillError(errMsg(err));
    }
  };

  const toggleSkill = async (id: string, enabled: boolean) => {
    setSkillError(null);
    // Optimistic: flip the switch immediately, sync with the server after.
    const prev = skills;
    setSkills(prev.map((s) => (s.id === id ? { ...s, enabled } : s)));
    try {
      const r = await api.skillToggle(id, enabled);
      setSkills(r.skills ?? []);
    } catch (err) {
      setSkills(prev);
      setSkillError(errMsg(err));
    }
  };

  const refreshExtensions = useCallback(async () => {
    try {
      const r = await api.extensions();
      setExtensions(r.extensions ?? []);
      setExtErrors((r.errors ?? []).map(String));
      setExtConflicts(r.harness?.conflicts ?? []);
    } catch (err) {
      setExtError(errMsg(err));
    }
  }, []);

  useEffect(() => {
    if (tab === 'extensions') void refreshExtensions();
  }, [tab, refreshExtensions]);

  // Opening an extension used to wait for its source to arrive before the
  // dialog appeared at all, which is what made it feel slow. The source is a
  // small file we know we will need, so fetch the whole list as soon as it is
  // on screen and open straight from here.
  const extCache = useRef(
    new Map<
      string,
      {
        id: string;
        description: string;
        source: string;
        builtin: boolean;
        settings?: ExtensionSettingField[];
        values?: Record<string, string | boolean>;
      }
    >(),
  );
  useEffect(() => {
    if (tab !== 'extensions') return;
    let cancelled = false;
    void (async () => {
      // One at a time: this is a background nicety, not a reason to open six
      // sockets at once.
      for (const ext of extensions) {
        if (cancelled) return;
        if (extCache.current.has(ext.id)) continue;
        try {
          const r = await api.extension(ext.id);
          if (cancelled) return;
          extCache.current.set(ext.id, {
            id: r.id,
            description: r.description ?? '',
            source: r.source ?? '',
            builtin: r.builtin ?? false,
            settings: ext.settings,
            values: ext.values,
          });
        } catch {
          // A failed prefetch only means the click fetches it instead.
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [tab, extensions]);

  const openExtension = async (id: string) => {
    setExtError(null);
    setExtView('detail');
    const cached = extCache.current.get(id);
    if (cached) {
      setExtEditor({
        id: cached.id,
        description: cached.description,
        source: cached.source,
        isNew: false,
        builtin: cached.builtin,
        schema: cached.settings ?? [],
        settings: { ...(cached.values ?? {}) },
        original: {
          description: cached.description,
          source: cached.source,
          settings: { ...(cached.values ?? {}) },
        },
      });
      return;
    }
    // Not prefetched yet (a fresh install, or a click that beat the fetch):
    // open immediately and fill the fields in when the source lands, so the
    // click never looks like it did nothing.
    const listed = extensions.find((e) => e.id === id);
    const listedSchema = listed?.settings ?? [];
    const listedValues = { ...(listed?.values ?? {}) };
    setExtEditor({
      id,
      description: listed?.description ?? '',
      source: '',
      isNew: false,
      builtin: listed?.builtin ?? false,
      schema: listedSchema,
      settings: { ...listedValues },
      loading: true,
      original: { description: listed?.description ?? '', source: '', settings: { ...listedValues } },
    });
    try {
      const r = await api.extension(id);
      const description = r.description ?? '';
      const source = r.source ?? '';
      extCache.current.set(id, {
        id: r.id,
        description,
        source,
        builtin: r.builtin ?? false,
      });
      setExtEditor({
        id: r.id,
        description,
        source,
        isNew: false,
        builtin: r.builtin ?? false,
        schema: listedSchema,
        settings: { ...listedValues },
        original: { description, source, settings: { ...listedValues } },
      });
    } catch (err) {
      setExtError(errMsg(err));
      setExtEditor(null);
    }
  };

  const closeExtEditor = () => {
    setExtEditor(null);
    setExtError(null);
    setExtView('detail');
  };

  const saveExtension = async () => {
    if (!extEditor) return;
    setExtBusy(true);
    setExtError(null);
    try {
      const r = await api.extensionSave({
        id: extEditor.id.trim(),
        description: extEditor.description,
        source: extEditor.source,
        settings: extEditor.schema.length > 0 ? extEditor.settings : undefined,
        enabled: true,
      });
      setExtensions(r.extensions ?? []);
      setExtEditor(null);
      setExtView('detail');
      await refreshExtensions();
    } catch (err) {
      setExtError(errMsg(err));
    } finally {
      setExtBusy(false);
    }
  };

  const toggleExtension = async (id: string, enabled: boolean) => {
    setExtError(null);
    try {
      const r = await api.extensionToggle(id, enabled);
      setExtensions(r.extensions ?? []);
    } catch (err) {
      setExtError(errMsg(err));
    }
  };

  const removeExtension = async (id: string) => {
    if (!window.confirm(`Delete the ${id} extension? This cannot be undone.`)) return;
    setExtBusy(true);
    setExtError(null);
    try {
      const r = await api.extensionRemove(id);
      setExtensions(r.extensions ?? []);
      setExtEditor(null);
      setExtView('detail');
      await refreshExtensions();
    } catch (err) {
      setExtError(errMsg(err));
    } finally {
      setExtBusy(false);
    }
  };

  const reloadExtensions = async () => {
    setExtBusy(true);
    setExtError(null);
    try {
      await api.extensionReload();
      await refreshExtensions();
    } catch (err) {
      setExtError(errMsg(err));
    } finally {
      setExtBusy(false);
    }
  };

  const savePermissionMode = async (e: FormEvent) => {
    e.preventDefault();
    setPermSaving(true);
    setPermSaved(false);
    setPermError(null);
    try {
      await api.updateSettings({ permissionMode, rewindApproval });
      setPermSaved(true);
      setSavedMode(permissionMode);
      setSavedRewind(rewindApproval);
      onPermissionSaved?.(permissionMode);
    } catch (err) {
      setPermError(errMsg(err));
    } finally {
      setPermSaving(false);
    }
  };

  const mcpSection = (
    <div className="flex flex-col">
      <form onSubmit={(e) => void addMCP(e)} className="flex flex-col gap-3">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Field label="Name (optional)">
            <Input
              value={mcpName}
              onChange={(e) => setMcpName(e.target.value)}
              placeholder="e.g. filesystem"
              autoComplete="off"
            />
          </Field>
          <Field label="Command line or URL">
            <Input
              value={mcpCommand}
              onChange={(e) => setMcpCommand(e.target.value)}
              placeholder="npx -y @modelcontextprotocol/server-filesystem /tmp — or https://example.com/mcp"
              autoComplete="off"
              autoCorrect="off"
              autoCapitalize="off"
              spellCheck={false}
            />
          </Field>
        </div>
        <p className="text-xs leading-relaxed text-subtle">
          The command line is split on whitespace — the first token is the executable, the rest
          are arguments. Enter an http(s) URL instead to reach a remote server over streamable
          HTTP.
        </p>
        <SaveRow
          saving={mcpSaving}
          saved={false}
          error={mcpError}
          pulse={mcpCommand.trim() !== ''}
          disabled={mcpCommand.trim() === ''}
          extra={
            <Button
              type="button"
              variant="outline"
              onClick={() => void testMCP()}
              disabled={mcpTesting || mcpCommand.trim() === ''}
            >
              {mcpTesting ? <Spinner className="h-4 w-4" /> : 'Test'}
            </Button>
          }
        />
        {mcpTestResult &&
          (mcpTestResult.ok ? (
            <p className="flex items-start gap-1.5 text-xs text-emerald-500">
              <IconCheck className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>
                Connected — {mcpTestResult.tools?.length ?? 0} tool
                {(mcpTestResult.tools?.length ?? 0) === 1 ? '' : 's'}
                {mcpTestResult.tools && mcpTestResult.tools.length > 0 && (
                  <span className="text-faint">
                    {' '}
                    ({mcpTestResult.tools
                      .slice(0, 5)
                      .map((t) => t.name)
                      .join(', ')}
                    {mcpTestResult.tools.length > 5 ? ` +${mcpTestResult.tools.length - 5} more` : ''}
                    )
                  </span>
                )}
              </span>
            </p>
          ) : (
            <p className="flex items-start gap-1.5 text-xs text-red-400">
              <IconX className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>{mcpTestResult.error || 'Connection failed'}</span>
            </p>
          ))}
      </form>

      {oauthNotice && (
        <p
          className={`mt-3 text-xs ${oauthNotice.ok ? 'text-emerald-400' : 'text-red-400'}`}
          role="status"
        >
          {oauthNotice.text}
        </p>
      )}

      {servers.length > 0 && (
        <>
          <div className="my-4 border-t border-border" />
          <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            {servers.map((srv) => {
              const st = status[srv.id];
              const enabled = srv.enabled !== false;
              return (
                <li key={srv.id} className="flex flex-col gap-1.5 rounded-xl border border-border-strong bg-surface p-3 shadow-sm">
                  <div className="flex items-center gap-2">
                    <span
                      className={`h-2 w-2 shrink-0 rounded-full ${
                        enabled && st?.connected ? 'bg-emerald-500' : 'bg-border'
                      }`}
                      title={
                        !enabled
                          ? 'Disabled'
                          : st?.connected
                            ? `Connected — ${st.toolCount} tools`
                            : 'Not connected (connects on the next chat)'
                      }
                    />
                    <span
                      className={`min-w-0 flex-1 truncate text-sm font-medium ${
                        enabled ? 'text-text' : 'text-dim'
                      }`}
                    >
                      {srv.name}
                    </span>
                    {!enabled && (
                      <span className="shrink-0 rounded-full bg-border px-1.5 py-0.5 text-[10px] text-dim">
                        disabled
                      </span>
                    )}
                    {enabled && st?.connected && (
                      <span className="shrink-0 rounded-full bg-emerald-950 px-1.5 py-0.5 text-[10px] text-emerald-400">
                        {st.toolCount} tools
                      </span>
                    )}
                    <button
                      type="button"
                      role="switch"
                      aria-checked={enabled}
                      aria-label={`${enabled ? 'Disable' : 'Enable'} MCP server ${srv.name}`}
                      title={enabled ? 'Disable server' : 'Enable server'}
                      onClick={() => void toggleMCP(srv.id, !enabled)}
                      className={`relative h-5 w-9 shrink-0 rounded-full transition-colors ${
                        enabled ? 'bg-accent' : 'bg-border'
                      }`}
                    >
                      <span
                        className={`absolute top-0.5 h-4 w-4 rounded-full bg-bg transition-all ${
                          enabled ? 'left-[18px]' : 'left-0.5'
                        }`}
                      />
                    </button>
                    <button
                      type="button"
                      aria-label={`Test MCP server ${srv.name}`}
                      title="Test connection"
                      onClick={() => void testCard(srv)}
                      className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-text"
                    >
                      {cardTestingId === srv.id ? (
                        <Spinner className="h-3.5 w-3.5" />
                      ) : (
                        <IconFlask className="h-3.5 w-3.5" />
                      )}
                    </button>
                    {srv.url &&
                      (oauth[srv.id] === 'connected' ? (
                        <button
                          type="button"
                          title="Authorized — click to disconnect"
                          aria-label={`Disconnect authorization for ${srv.name}`}
                          onClick={() => void disconnectMCP(srv)}
                          className="shrink-0 rounded-md bg-emerald-950 px-1.5 py-0.5 text-[10px] text-emerald-400 transition-colors hover:bg-emerald-900"
                        >
                          {oauthBusyId === srv.id ? '…' : 'Authorized'}
                        </button>
                      ) : (
                        <button
                          type="button"
                          title="Sign in to this server"
                          aria-label={`Authorize MCP server ${srv.name}`}
                          onClick={() => void connectMCP(srv)}
                          className="shrink-0 rounded-md border border-border px-1.5 py-0.5 text-[10px] text-dim transition-colors hover:bg-border hover:text-text"
                        >
                          {oauthBusyId === srv.id ? '…' : 'Authorize'}
                        </button>
                      ))}
                    <button
                      type="button"
                      aria-label={`Edit MCP server ${srv.name}`}
                      title="Edit server"
                      onClick={() => editMCP(srv)}
                      className={`inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-text ${
                        editingId === srv.id ? 'bg-border text-text' : ''
                      }`}
                    >
                      <IconPencil className="h-3.5 w-3.5" />
                    </button>
                    <button
                      type="button"
                      aria-label={`Remove MCP server ${srv.name}`}
                      title="Remove server"
                      onClick={() => void removeMCP(srv.id)}
                      className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-red-400"
                    >
                      <IconX className="h-3.5 w-3.5" />
                    </button>
                  </div>
                  <div className="truncate font-mono text-[11px] text-faint">
                    {srv.url ?? `${srv.command} ${srv.args.join(' ')}`.trim()}
                  </div>
                  {!enabled ? (
                    <span className="text-[10px] text-faint">
                      Disabled — connects on the next chat once enabled
                    </span>
                  ) : (
                    !st?.connected && (
                      <span className="text-[10px] text-faint">
                        Not connected — connects on the next chat
                      </span>
                    )
                  )}
                  {editingId === srv.id && (
                    <form
                      onSubmit={(e) => {
                        e.preventDefault();
                        void saveEdit(srv);
                      }}
                      className="flex flex-col gap-2 border-t border-border pt-2"
                    >
                      <Input
                        value={editName}
                        onChange={(e) => setEditName(e.target.value)}
                        placeholder="Name (optional)"
                        autoComplete="off"
                        className="h-8 text-sm"
                      />
                      <Input
                        value={editCommand}
                        onChange={(e) => setEditCommand(e.target.value)}
                        placeholder="Command line"
                        autoComplete="off"
                        autoCorrect="off"
                        autoCapitalize="off"
                        spellCheck={false}
                        className="h-8 font-mono text-sm"
                      />
                      {editError && <p className="text-xs text-red-400">{editError}</p>}
                      <div className="flex items-center gap-2">
                        <Button
                          type="submit"
                          variant="outline"
                          className="h-8 px-3 text-xs"
                          disabled={editSaving || editCommand.trim() === ''}
                        >
                          {editSaving ? <Spinner className="h-3.5 w-3.5" /> : 'Save'}
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          className="h-8 px-3 text-xs"
                          onClick={() => setEditingId(null)}
                        >
                          Cancel
                        </Button>
                      </div>
                    </form>
                  )}
                  {cardTest && cardTest.id === srv.id &&
                    (cardTest.ok ? (
                      <p className="flex items-start gap-1.5 text-xs text-emerald-500">
                        <IconCheck className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                        <span>
                          Connected — {cardTest.tools?.length ?? 0} tool
                          {(cardTest.tools?.length ?? 0) === 1 ? '' : 's'}
                          {cardTest.tools && cardTest.tools.length > 0 && (
                            <span className="text-faint">
                              {' '}
                              ({cardTest.tools
                                .slice(0, 5)
                                .map((t) => t.name)
                                .join(', ')}
                              {cardTest.tools.length > 5
                                ? ` +${cardTest.tools.length - 5} more`
                                : ''}
                              )
                            </span>
                          )}
                        </span>
                      </p>
                    ) : (
                      <p className="flex items-start gap-1.5 text-xs text-red-400">
                        <IconX className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                        <span>{cardTest.error || 'Connection failed'}</span>
                      </p>
                    ))}
                </li>
              );
            })}
          </ul>
        </>
      )}
    </div>
  );

  const extensionsSection = (
    <div className="flex h-full min-h-0 min-w-0 flex-col gap-3">
      <div className="flex shrink-0 flex-col gap-2">
        <p className="text-[11px] leading-relaxed text-subtle">
          Extensions are small JavaScript modules the agent loads to add tools, inject prompt
          sections or hook into a run. The agent can write them itself, which is how v1 grows new
          abilities.
        </p>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            className="h-7 shrink-0 px-2 text-xs"
            disabled={extBusy}
            onClick={() => void reloadExtensions()}
          >
            Reload
          </Button>
          <Button
            variant="outline"
            className="h-7 shrink-0 px-2 text-xs"
            onClick={() => setNewExtOpen(true)}
          >
            New
          </Button>
        </div>
      </div>

      {extError !== null && (
        <div className="shrink-0 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-[11px] text-red-400">
          {extError}
        </div>
      )}

      {extErrors.map((msg, i) => (
        <div
          key={i}
          className="shrink-0 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 font-mono text-[11px] text-amber-400"
        >
          {msg}
        </div>
      ))}

      {extEditor !== null && (
        <Dialog
          open
          onClose={closeExtEditor}
          title={
            extView === 'code'
              ? `Source — ${extEditor.id || 'extension'}`
              : extView === 'description'
                ? 'Description'
                : extEditor.isNew
                  ? 'New extension'
                  : `Edit ${extEditor.id}`
          }
          wide
          fullScreen
          fixedBody
          align="top"
        >
          <div className="flex h-full min-h-0 flex-col gap-3">
            {extView === 'code' ? (
              /* The source is a wall of code, so it lives behind a button rather
                 than in the middle of the settings. */
              <div className="min-h-0 flex-1 overflow-hidden rounded-lg border border-border bg-surface">
                {extEditor.loading ? (
                  <div className="flex h-full items-center justify-center gap-2 text-xs text-faint">
                    <Spinner className="h-3.5 w-3.5" /> Loading source…
                  </div>
                ) : (
                  <CodeEditor
                    value={extEditor.source}
                    onChange={(v) => setExtEditor({ ...extEditor, source: v })}
                    path={`${extEditor.id.trim() || 'extension'}.js`}
                  />
                )}
              </div>
            ) : extView === 'description' ? (
              /* A full-height editor: the point is that the text has the whole
                 sheet to be read and rewritten in. */
              <textarea
                value={extEditor.description}
                onChange={(e) => setExtEditor({ ...extEditor, description: e.target.value })}
                placeholder="What this extension does"
                autoFocus
                className="min-h-0 w-full flex-1 resize-none rounded-lg border border-border-strong bg-surface px-3 py-2 text-sm text-text outline-none transition-colors focus:border-subtle"
              />
            ) : (
              <>
                {/* Fields stack on a phone: side by side, a fixed-width Id
                    leaves the description too narrow to read. */}
                <div className="flex shrink-0 flex-col gap-2 sm:flex-row sm:items-end">
                  <div className="sm:w-40">
                    <Field label="Id">
                      <Input value={extEditor.id} disabled autoComplete="off" />
                    </Field>
                  </div>
                  <div className="min-w-0 flex-1">
                    <Field label="Description">
                      <p className="min-h-[2.25rem] rounded-lg border border-border-strong bg-surface px-3 py-2 text-xs leading-relaxed text-text">
                        {extEditor.description !== '' ? extEditor.description : 'No description'}
                      </p>
                    </Field>
                  </div>
                </div>
                <div className="flex shrink-0 flex-wrap items-center gap-2">
                  <Button
                    variant="outline"
                    className="h-8 px-3 text-xs"
                    onClick={() => setExtView('description')}
                  >
                    <IconPencil className="h-3.5 w-3.5" /> Edit description
                  </Button>
                  <Button
                    variant="outline"
                    className="h-8 px-3 text-xs"
                    onClick={() => setExtView('code')}
                  >
                    View source
                  </Button>
                </div>
                {extEditor.schema.length > 0 && (
                  <div className="shrink-0 space-y-2.5 rounded-lg border border-border-strong bg-surface px-3 py-2.5">
                    <div className="text-[11px] font-medium text-subtle">Settings</div>
                    {extEditor.schema.map((field) => (
                      <div key={field.key}>
                        {field.type === 'checkbox' ? (
                          <label className="flex items-center gap-2 text-xs text-text">
                            <input
                              type="checkbox"
                              checked={extEditor.settings[field.key] === true}
                              onChange={(e) =>
                                setExtEditor((prev) =>
                                  prev
                                    ? {
                                        ...prev,
                                        settings: { ...prev.settings, [field.key]: e.target.checked },
                                      }
                                    : prev,
                                )
                              }
                              className="h-3.5 w-3.5 shrink-0 accent-accent"
                            />
                            <span className="min-w-0 truncate">{field.label ?? field.key}</span>
                          </label>
                        ) : (
                          <label className="block text-[11px] text-subtle">
                            {field.label ?? field.key}
                            <input
                              value={String(extEditor.settings[field.key] ?? '')}
                              onChange={(e) =>
                                setExtEditor((prev) =>
                                  prev
                                    ? {
                                        ...prev,
                                        settings: { ...prev.settings, [field.key]: e.target.value },
                                      }
                                    : prev,
                                )
                              }
                              className="mt-1 w-full rounded-md border border-border-strong bg-bg px-2 py-1 text-xs text-text outline-none focus:border-accent"
                            />
                          </label>
                        )}
                        {field.help && <p className="mt-0.5 text-[10px] text-faint">{field.help}</p>}
                      </div>
                    ))}
                  </div>
                )}
              </>
            )}
            {/* The actions read as what you do once you have read the detail.
                Delete is destructive and sits apart on the left; Save and Cancel
                are the pair you actually choose between. */}
            <div className="flex shrink-0 items-center gap-2">
              {extView !== 'detail' ? (
                <Button
                  variant="ghost"
                  className="h-8 px-3 text-xs"
                  onClick={() => setExtView('detail')}
                >
                  Back
                </Button>
              ) : (
                <>
                  {!extEditor.isNew && !extEditor.builtin && (
                    <Button
                      variant="danger"
                      className="h-8 px-3 text-xs"
                      disabled={extBusy}
                      onClick={() => void removeExtension(extEditor.id)}
                    >
                      Delete
                    </Button>
                  )}
                  {extEditor.builtin && (
                    <span className="text-[11px] text-faint">
                      Bundled with v1 — it can be disabled but not removed.
                    </span>
                  )}
                </>
              )}
              <div className="ml-auto flex items-center gap-2">
                <Button variant="ghost" className="h-8 px-3 text-xs" onClick={closeExtEditor}>
                  Cancel
                </Button>
                <Button
                  variant="primary"
                  className="h-8 px-3 text-xs"
                  disabled={
                    extBusy ||
                    !extDirty ||
                    extEditor.id.trim() === '' ||
                    extEditor.source.trim() === ''
                  }
                  onClick={() => void saveExtension()}
                >
                  {extBusy ? <Spinner className="h-4 w-4" /> : 'Save'}
                </Button>
              </div>
            </div>
          </div>
        </Dialog>
      )}

      <div className="fade-y flex min-h-0 flex-1 flex-col gap-2 overflow-x-hidden overflow-y-auto overscroll-contain">
          {extConflicts.length > 0 && (
            <div className="rounded-xl border border-amber-300/30 bg-amber-300/5 px-3 py-2 text-[11px] text-amber-200">
              <div className="font-medium">Name conflicts</div>
              <ul className="mt-1 list-disc space-y-0.5 pl-4">
                {extConflicts.map((c, i) => (
                  <li key={`${c.kind}-${c.name}-${i}`}>
                    {c.kind} <span className="font-mono">{c.name}</span> is claimed by {c.ids.join(', ')}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {extensions.length === 0 && (
            <div className="text-[11px] text-faint">
              No extensions yet. The agent can create one when it needs a capability it does not have.
            </div>
          )}
          {extensions.map((ext) => (
            <div
              key={ext.id}
              className="flex items-center gap-2 rounded-xl border border-border-strong bg-surface px-3 py-2 shadow-sm"
            >
              <button
                type="button"
                onClick={() => void openExtension(ext.id)}
                title={`Edit ${ext.id}`}
                className="min-w-0 flex-1 text-left"
              >
                <div className="truncate text-sm text-text">{ext.id}</div>
                <div className="line-clamp-2 text-[11px] text-faint">
                  {ext.description !== '' ? ext.description : 'No description'}
                </div>
              </button>
              <button
                type="button"
                role="switch"
                aria-checked={ext.enabled}
                aria-label={`${ext.enabled ? 'Disable' : 'Enable'} ${ext.id}`}
                onClick={() => void toggleExtension(ext.id, !ext.enabled)}
                className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors ${
                  ext.enabled ? 'bg-accent' : 'bg-border'
                }`}
              >
                <span
                  className={`inline-block h-3.5 w-3.5 transform rounded-full bg-bg transition-transform ${
                    ext.enabled ? 'translate-x-[18px]' : 'translate-x-[3px]'
                  }`}
                />
              </button>
            </div>
          ))}
      </div>

      <NewExtensionDialog open={newExtOpen} onClose={() => setNewExtOpen(false)} />
    </div>
  );

  const skillsSection = (
    <div className="flex h-full min-h-0 min-w-0 flex-col gap-3">
      <form onSubmit={(e) => void searchSkills(e)} className="flex shrink-0 min-w-0 items-end gap-2">
        <div className="flex-1">
          <Field label="Search SkillsMP">
            <div className="relative">
              <Input
                value={skillQuery}
                onChange={(e) => setSkillQuery(e.target.value)}
                placeholder="e.g. react, security, postgres"
                autoComplete="off"
                className="pr-8"
              />
              {/* Only offered once there is something to clear, and as an icon
                  so it reads as part of the field rather than a second action. */}
              {(skillQuery !== '' || skillResults.length > 0 || skillError !== null) && (
                <button
                  type="button"
                  aria-label="Clear search"
                  title="Clear search"
                  onClick={() => {
                    setSkillQuery('');
                    setSkillResults([]);
                    setSkillError(null);
                  }}
                  className="absolute right-2 top-1/2 inline-flex h-5 w-5 -translate-y-1/2 items-center justify-center rounded text-faint transition-colors hover:bg-border hover:text-text"
                >
                  <IconX className="h-3.5 w-3.5" />
                </button>
              )}
            </div>
          </Field>
        </div>
        <Button type="submit" variant="outline" disabled={skillBusy || skillQuery.trim() === ''} className="h-[42px] sm:h-[38px]">
          {skillBusy ? <Spinner className="h-4 w-4" /> : 'Search'}
        </Button>
      </form>

      {suggestedSkills.filter((sk) => !skills.some((s) => s.id === sk.id)).length > 0 && (
        <div className="shrink-0">
          <h4 className="mb-2 text-xs font-medium text-dim">Suggested / included</h4>
          <ul className="flex flex-col gap-2">
            {suggestedSkills
              .filter((sk) => !skills.some((s) => s.id === sk.id))
              .map((sk) => (
                <li
                  key={sk.id}
                  className="flex items-center gap-2 rounded-xl border border-border-strong bg-surface/50 px-3 py-2 shadow-sm"
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-sm font-medium text-text">{sk.name}</span>
                    </div>
                    <div className="truncate text-[11px] text-faint">
                      {sk.author}
                      {sk.description ? ` · ${sk.description}` : ''}
                    </div>
                  </div>
                  <Button
                    variant="outline"
                    className="h-7 shrink-0 px-2 text-xs"
                    disabled={skillBusyId === sk.id}
                    onClick={() => void installSkill(sk)}
                  >
                    {skillBusyId === sk.id ? <Spinner className="h-3.5 w-3.5" /> : 'Install'}
                  </Button>
                </li>
              ))}
          </ul>
        </div>
      )}

      <div className="shrink-0 border-t border-border" />

      <div className="fade-y flex min-h-0 flex-1 flex-col gap-3 overflow-x-hidden overflow-y-auto overscroll-contain">
        {skillResults.length > 0 && (
          <ul className="min-w-0 flex flex-col gap-2">
            {skillResults.map((sk) => (
              <li
                key={sk.id}
                className="flex items-center gap-2 rounded-xl border border-border-strong bg-surface px-3 py-2 shadow-sm"
              >
                <button
                  type="button"
                  onClick={() =>
                    setSkillPreview({
                      name: sk.name,
                      author: sk.author,
                      description: sk.description,
                      githubUrl: sk.githubUrl,
                      skillsmpUrl: sk.skillsmpUrl,
                      result: sk,
                    })
                  }
                  title={`About ${sk.name}`}
                  className="min-w-0 flex-1 text-left"
                >
                  <div className="truncate text-sm text-text">
                    {sk.name}
                    {typeof sk.stars === 'number' && sk.stars > 0 && (
                      <span className="ml-1.5 text-[10px] text-faint" title="GitHub stars">
                        ★ {formatStars(sk.stars)}
                      </span>
                    )}
                  </div>
                  <div className="truncate text-[11px] text-faint">
                    {sk.author}
                    {sk.description ? ` · ${sk.description}` : ''}
                  </div>
                </button>
                {!sk.builtin && <ViewOnSkillsMP href={skillsmpHref(sk)} name={sk.name} />}
                <Button
                  variant="outline"
                  className="h-7 shrink-0 px-2 text-xs"
                  disabled={skillBusyId === sk.id}
                  onClick={() => void installSkill(sk)}
                >
                  {skillBusyId === sk.id ? <Spinner className="h-3.5 w-3.5" /> : 'Install'}
                </Button>
              </li>
            ))}
          </ul>
        )}

        {skillError && <p className="text-xs text-red-400">{skillError}</p>}

        {skills.length > 0 && (
          <div>
            <div className="mb-2 flex items-center justify-between">
              <span className="text-xs text-subtle">Installed</span>
              <span className="text-[11px] text-faint">{skills.length}</span>
            </div>
            <ul className="flex flex-col gap-1.5">
              {skills.map((sk) => (
                <li
                  key={sk.id}
                  className="flex items-center gap-2 rounded-lg border border-border-strong bg-surface px-3 py-2 shadow-sm"
                >
                  <button
                    type="button"
                    role="switch"
                    aria-checked={sk.enabled}
                    aria-label={`Toggle skill ${sk.name}`}
                    title={sk.enabled ? 'Disable skill' : 'Enable skill'}
                    onClick={() => void toggleSkill(sk.id, !sk.enabled)}
                    className={`relative h-5 w-9 shrink-0 rounded-full transition-colors ${
                      sk.enabled ? 'bg-accent' : 'bg-border'
                    }`}
                  >
                    <span
                      className={`absolute top-0.5 h-4 w-4 rounded-full bg-bg transition-all ${
                        sk.enabled ? 'left-[18px]' : 'left-0.5'
                      }`}
                    />
                  </button>
                  <button
                    type="button"
                    onClick={() =>
                      setSkillPreview({
                        name: sk.name,
                        author: sk.author,
                        description: sk.description,
                        githubUrl: sk.githubUrl,
                        skillsmpUrl: sk.skillsmpUrl,
                        installed: sk,
                      })
                    }
                    title={`About ${sk.name}`}
                    className="min-w-0 flex-1 text-left"
                  >
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-sm text-text">{sk.name}</span>
                      {!sk.enabled && (
                        <span className="rounded-full bg-border px-1.5 py-0.5 text-[10px] text-dim">
                          disabled
                        </span>
                      )}
                    </div>
                    <div className="truncate text-[11px] text-faint">
                      {sk.author}
                      {sk.description ? ` · ${sk.description}` : ''}
                    </div>
                  </button>
                  {!sk.builtin && skillsmpHref(sk) !== '' && (
                    <ViewOnSkillsMP href={skillsmpHref(sk)} name={sk.name} />
                  )}
                  <button
                    type="button"
                    aria-label={`Remove skill ${sk.name}`}
                    title="Remove skill"
                    onClick={() => void removeSkill(sk.id)}
                    className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-red-400"
                  >
                    <IconX className="h-3.5 w-3.5" />
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </div>
  );

  const permsSection = (
    <form onSubmit={(e) => void savePermissionMode(e)} className="flex flex-col gap-3">
      <p className="text-xs leading-relaxed text-subtle">
        Used by every project that hasn't chosen its own permission mode. A
        project's own setting takes precedence, so changing this does not affect
        projects that already have one.
      </p>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
        {PERMISSION_MODES.map((m) => {
          const active = permissionMode === m.id;
          return (
            <button
              key={m.id}
              type="button"
              onClick={() => {
                setPermissionMode(m.id);
                setPermSaved(false);
                onPermissionModeChange?.(m.id);
              }}
              className={`flex min-w-0 overflow-hidden flex-col gap-1.5 rounded-xl border p-3 text-left transition-colors ${
                active ? m.selected : 'border-border hover:border-border-strong'
              }`}
            >
              <span className="flex items-center gap-2 text-sm font-medium text-text">
                <span className="flex h-4 w-4 shrink-0 items-center justify-center">
                  {active && <IconCheck className="h-4 w-4" />}
                </span>
                {m.name}
              </span>
              <span className="text-xs leading-relaxed text-subtle">{m.desc}</span>
            </button>
          );
        })}
      </div>
      <label className="flex items-center gap-3 rounded-xl border border-border p-3">
        <span className="min-w-0 flex-1">
          <span className="block text-sm font-medium text-text">
            Require approval to rewind a chat
          </span>
          <span className="block text-xs leading-relaxed text-subtle">
            Shows a confirmation prompt before rewinding to an earlier message, which deletes
            everything after it from the conversation.
          </span>
        </span>
        <button
          type="button"
          role="switch"
          aria-checked={rewindApproval}
          onClick={() => {
            setRewindApproval((v) => !v);
            setPermSaved(false);
          }}
          className={`relative h-5 w-9 shrink-0 rounded-full transition-colors ${
            rewindApproval ? 'bg-accent' : 'bg-border'
          }`}
        >
          <span
            className={`absolute top-0.5 h-4 w-4 rounded-full bg-bg transition-all ${
              rewindApproval ? 'left-[18px]' : 'left-0.5'
            }`}
          />
        </button>
      </label>
      <SaveRow
        saving={permSaving}
        saved={permSaved}
        error={permError}
        pulse={permissionMode !== savedMode || rewindApproval !== savedRewind}
      />
    </form>
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="shrink-0 z-10 flex gap-0.5 overflow-x-auto overflow-y-hidden border-b border-border bg-bg">
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => setTab(t.id)}
            className={`-mb-px flex h-9 shrink-0 grow basis-auto items-center justify-center gap-1.5 whitespace-nowrap border-b-2 px-2 text-sm transition-colors ${
              tab === t.id
                ? 'border-accent text-text'
                : 'border-transparent text-subtle hover:text-text'
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>
      <div className="fade-y min-h-0 min-w-0 flex-1 overflow-x-hidden overflow-y-auto px-3 pt-2 pb-2 overscroll-contain">
        {tab === 'mcp' && mcpSection}
        {tab === 'skills' && <div className="flex h-full min-h-0 flex-col">{skillsSection}</div>}
        {tab === 'extensions' && (
          <div className="flex h-full min-h-0 flex-col">{extensionsSection}</div>
        )}
        {tab === 'tools' && (
          <div className="flex min-w-0 flex-col md:h-full md:min-h-0">{toolsSection}</div>
        )}
        {tab === 'perms' && permsSection}
      </div>
      {skillPreview && (
        <SkillPreviewDialog
          target={skillPreview}
          busy={skillPreview.result ? skillBusyId === skillPreview.result.id : false}
          onClose={() => setSkillPreview(null)}
          onInstall={() => {
            if (skillPreview.result) {
              void installSkill(skillPreview.result);
              setSkillPreview(null);
            }
          }}
          onToggle={(enabled) => {
            const inst = skillPreview.installed;
            if (!inst) return;
            void toggleSkill(inst.id, enabled);
            setSkillPreview(null);
          }}
          onRemove={() => {
            if (skillPreview.installed) {
              void removeSkill(skillPreview.installed.id);
              setSkillPreview(null);
            }
          }}
        />
      )}
    </div>
  );
}

export default memo(ToolSettings);
