import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent, type FormEvent, type ReactNode } from 'react';
import { Link, useLocation, useSearchParams } from 'react-router-dom';
import { SiVercel } from 'react-icons/si';
import { api, clearClientCaches, type SettingsUpdate } from '../api';
import { testNotification } from '../notify';
import type { Settings as SettingsType, UserInfo } from '../types';
import {
  errMsg,
  getChatSide,
  getChatTabLayout,
  getDebugHud,
  getJsonPretty,
  getNotifyEnabled,
  getNotifyOnlyBackground,
  getNotifyTurnDone,
  getNotifyTurnError,
  getThinkingCollapsed,
  getToolCallsCollapsed,
  iosVersion,
  isIOS,
  isStandalone,
  randomId,
  setChatSide,
  setChatTabLayout,
  setDebugHud,
  setJsonPretty,
  setNotifyEnabled,
  setNotifyOnlyBackground,
  setNotifyTurnDone,
  setNotifyTurnError,
  setThinkingCollapsed,
  setToolCallsCollapsed,
  type ChatSide,
  type ChatTab,
} from '../utils';
import {
  applyTheme,
  getStoredTheme,
  listThemeOptions,
  setStoredTheme,
} from '../themes';
import {
  Button,
  Center,
  ErrorBox,
  Field,
  Input,
  SaveRow,
  Section,
  Spinner,
} from '../components/ui';
import {
  IconAlert,
  IconArrowLeft,
  IconBrain,
  IconChat,
  IconCheck,
  IconChevronDown,
  IconCopy,
  IconChevronUp,
  IconDots,
  IconExternalLink,
  IconEye,
  IconEyeOff,
  IconFolder,
  IconGitBranch,
  IconGitHub,
  IconGlobe,
  IconGrip,
  IconKey,
  IconLock,
  IconLogout,
  IconModel,
  IconPencil,
  IconSettings,
  IconTerminal,
  IconTrash,
  IconUsers,
  IconWrench,
  IconX,
  IconPlus,
} from '../components/icons';
import ProviderSelector from '../components/ProviderSelector';
import GitHubConnect from '../components/GitHubConnect';
import ToolSettings, { type ToolsTab } from '../components/ToolSettings';

const NAV = [
  { id: 'llm', label: 'LLM & providers', icon: <IconModel className="h-4 w-4" /> },
  { id: 'tools', label: 'Tools & permissions', icon: <IconWrench className="h-4 w-4" /> },
  { id: 'notifications', label: 'Notifications', icon: <IconAlert className="h-4 w-4" /> },
  { id: 'github', label: 'GitHub', icon: <IconGitHub className="h-4 w-4" /> },
  { id: 'vercel', label: 'Vercel', icon: <SiVercel className="h-4 w-4" /> },
  { id: 'appearance', label: 'Appearance', icon: <IconSettings className="h-4 w-4" /> },
  { id: 'auth', label: 'Auth', icon: <IconLock className="h-4 w-4" /> },
  { id: 'users', label: 'Users', icon: <IconUsers className="h-4 w-4" />, admin: true },
  { id: 'about', label: 'About', icon: <IconDots className="h-4 w-4" /> },
] as const;
type NavId = (typeof NAV)[number]['id'];

const navItemClass = (active: boolean) =>
  `flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-sm transition-colors ${
    active ? 'bg-border text-text' : 'text-subtle hover:bg-border/60 hover:text-text'
  }`;

// Searchable catalog of every setting on the page. The id doubles as the
// section anchor for deep links (?page=…&section=…); keywords carry the
// loose phrasing users actually type ("tokens", "approve", "ios banner").
const SETTINGS_SEARCH: {
  id: string;
  page: NavId;
  label: string;
  hint: string;
  keywords: string;
}[] = [
  { id: 'sec-llm', page: 'llm', label: 'LLM provider', hint: 'Base URL, API key, model', keywords: 'openai compatible endpoint api key base url model provider connection test' },
  { id: 'sec-system-prompt', page: 'llm', label: 'Global system prompt', hint: 'Extra instructions for every chat', keywords: 'prompt instructions behavior context rules system agent' },
  { id: 'sec-thinking-default', page: 'llm', label: 'Default thinking level', hint: 'Off / low / medium / high / xhigh / max', keywords: 'thinking reasoning effort level default tokens model' },
  { id: 'sec-toon', page: 'llm', label: 'TOON', hint: 'Token-efficient tool result encoding', keywords: 'toon tokens efficient encode tool results format compact json' },
  { id: 'sec-caveman', page: 'llm', label: 'Caveman mode', hint: 'Terse caveman-style replies', keywords: 'caveman terse style reply grunt fun mode brief short' },
  { id: 'sec-auto-push', page: 'llm', label: 'Auto-push new projects', hint: 'Default for newly created projects only', keywords: 'auto push commits github default new projects git remote' },
  { id: 'sec-context-threshold', page: 'llm', label: 'Context compaction', hint: 'Percent of context before auto compaction', keywords: 'context compaction threshold percent auto compact tokens' },
  { id: 'sec-turn-timeouts', page: 'llm', label: 'Turn timeouts', hint: 'Soft/hard run time limits in minutes', keywords: 'turn timeout soft hard deadline minutes abort warn run length' },
  { id: 'sec-github', page: 'github', label: 'GitHub', hint: 'Personal access token', keywords: 'github token pat repo import push auth' },
  { id: 'sec-github', page: 'github', label: 'GitHub OAuth', hint: 'OAuth App client ID', keywords: 'github oauth client id connect login' },
  { id: 'sec-vercel', page: 'vercel', label: 'Vercel', hint: 'Deploy token', keywords: 'vercel deploy token push hosting deploy' },
  { id: 'sec-tools-mcp', page: 'tools', label: 'MCP', hint: 'Model Context Protocol servers', keywords: 'mcp servers tools context protocol connect' },
  { id: 'sec-tools-skills', page: 'tools', label: 'Skills', hint: 'Install skills from SkillsMP', keywords: 'skills skillsmp install markdown' },
  { id: 'sec-tools-perms', page: 'tools', label: 'Permissions', hint: 'Approval mode, rewind approval', keywords: 'permission approve ask auto yolo tools rewind approval confirm' },
  { id: 'sec-theme', page: 'appearance', label: 'Theme', hint: 'Dark, light, custom swatches', keywords: 'theme dark light color appearance swatch applies instantly remembered' },
  { id: 'sec-chat-side', page: 'appearance', label: 'Chat side', hint: 'Chat pane left or right', keywords: 'chat side left right desktop layout appearance' },
  { id: 'sec-chat-tabs', page: 'appearance', label: 'Chat tabs', hint: 'Reorder and hide project tabs', keywords: 'tabs chat files terminal git memories project order hide drag' },
  { id: 'sec-tool-calls', page: 'appearance', label: 'Tool calls', hint: 'Pretty-print JSON', keywords: 'tool calls json pretty print format result arguments' },
  { id: 'sec-thinking-collapsed', page: 'appearance', label: 'Thinking blocks', hint: 'Collapse reasoning blocks by default', keywords: 'thinking reasoning collapse blocks default stream hide' },
  { id: 'sec-terminal', page: 'appearance', label: 'Terminal', hint: 'Font size and wrapping', keywords: 'terminal font size wrap prompt output appearance' },
  { id: 'sec-clear-cache', page: 'llm', label: 'Cached data', hint: 'Clear the provider catalog cache', keywords: 'cache clear providers catalog thinking metadata refetch reset' },
  { id: 'sec-auth', page: 'auth', label: 'Auth', hint: 'Change the password', keywords: 'password auth login security change password' },
  { id: 'sec-oidc', page: 'auth', label: 'OIDC login', hint: 'Single sign-on (admin)', keywords: 'oidc sso single sign on authentik keycloak google issuer client id secret callback redirect allowed emails login' },
  { id: 'sec-users', page: 'users', label: 'Users', hint: 'Admin: create and delete accounts', keywords: 'users accounts admin create delete signup password login' },
  { id: 'sec-notifications', page: 'notifications', label: 'Notifications', hint: 'Turn-finished/failed alerts, background-only', keywords: 'notifications notify alerts push ios pwa banner turn finished failed error background' },
  { id: 'sec-debug-hud', page: 'about', label: 'Debug HUD', hint: 'Viewport metrics overlay', keywords: 'debug hud viewport metrics overlay diagnostics' },
];

// Scores an entry against a loose query: exact substrings weigh most, then
// token and prefix matches across the title, hint and keywords.
function scoreSettings(query: string, e: (typeof SETTINGS_SEARCH)[number]): number {
  const q = query.toLowerCase().trim();
  if (!q) return 0;
  const hay = `${e.label} ${e.hint} ${e.keywords}`.toLowerCase();
  let score = 0;
  if (hay.includes(q)) score += 50;
  if (e.label.toLowerCase().includes(q)) score += 30;
  for (const tok of q.split(/\s+/)) {
    if (tok.length < 2) continue;
    if (hay.includes(tok)) score += 10;
    if (tok.length >= 3) {
      for (const word of hay.split(/[^a-z0-9]+/)) {
        if (word.startsWith(tok)) {
          score += 4;
          break;
        }
      }
    }
  }
  return score;
}

function ThemePicker() {
  const [selected, setSelected] = useState<string>(() => getStoredTheme());
  const [expanded, setExpanded] = useState(false);
  const options = listThemeOptions();
  const current = options.find((t) => t.name === selected) ?? options[0];

  const choose = (name: string) => {
    setSelected(name);
    setStoredTheme(name);
    applyTheme(name);
  };

  const swatch = (t: { name: string; swatches: string[] }) => (
    <span className="flex h-4 overflow-hidden rounded">
      {t.swatches.map((c, i) => (
        <span key={i} className="h-full flex-1" style={{ backgroundColor: c }} />
      ))}
    </span>
  );

  return (
    <div className="flex flex-col gap-2">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-center gap-2.5 rounded-lg border border-border p-2.5 text-left transition-colors hover:border-border-strong"
      >
        <span className="w-10 shrink-0">{swatch(current)}</span>
        <span className="flex-1 truncate text-xs text-text">{current.name}</span>
        <IconChevronDown
          className={`h-4 w-4 shrink-0 text-dim transition-transform ${expanded ? 'rotate-180' : ''}`}
        />
      </button>
      {expanded && (
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          {options.map((t) => {
            const active = t.name === selected;
            return (
              <button
                key={t.name}
                type="button"
                onClick={() => choose(t.name)}
                className={`rounded-lg border p-2.5 text-left transition-colors ${
                  active
                    ? 'border-accent ring-1 ring-accent'
                    : 'border-border hover:border-border-strong'
                }`}
              >
                <span className="mb-2">{swatch(t)}</span>
                <span className={`block truncate text-xs ${active ? 'text-text' : 'text-dim'}`}>
                  {t.name}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

function ChatSideControl() {
  const [side, setSide] = useState<ChatSide>(() => getChatSide());

  const choose = (s: ChatSide) => {
    setSide(s);
    setChatSide(s);
  };

  return (
    <div className="grid w-full max-w-xs grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
      {(['left', 'right'] as const).map((s) => (
        <button
          key={s}
          type="button"
          onClick={() => choose(s)}
          className={`min-h-[36px] rounded-md text-sm transition-colors ${
            side === s ? 'bg-border text-text' : 'text-dim hover:text-text'
          }`}
        >
          {s === 'left' ? 'Chat on left' : 'Chat on right'}
        </button>
      ))}
    </div>
  );
}

// Whether chat tool call/result JSON starts pretty-printed (Settings →
// Appearance → Tool calls).
function JsonPrettyControl() {
  const [on, setOn] = useState(() => getJsonPretty());

  const choose = (v: boolean) => {
    setOn(v);
    setJsonPretty(v);
  };

  return (
    <div className="grid w-full max-w-xs grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
      {([false, true] as const).map((v) => (
        <button
          key={String(v)}
          type="button"
          onClick={() => choose(v)}
          className={`min-h-[36px] rounded-md text-sm transition-colors ${
            on === v ? 'bg-border text-text' : 'text-dim hover:text-text'
          }`}
        >
          {v ? 'On' : 'Off'}
        </button>
      ))}
    </div>
  );
}

function ToolCallsCollapseControl() {
  const [on, setOn] = useState(() => getToolCallsCollapsed());

  const choose = (v: boolean) => {
    setOn(v);
    setToolCallsCollapsed(v);
  };

  return (
    <div className="grid w-full max-w-xs grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
      {([false, true] as const).map((v) => (
        <button
          key={String(v)}
          type="button"
          onClick={() => choose(v)}
          className={`min-h-[36px] rounded-md text-sm transition-colors ${
            on === v ? 'bg-border text-text' : 'text-dim hover:text-text'
          }`}
        >
          {v ? 'On' : 'Off'}
        </button>
      ))}
    </div>
  );
}

function ThinkingCollapsedControl() {
  const [on, setOn] = useState(() => getThinkingCollapsed());

  const choose = (v: boolean) => {
    setOn(v);
    setThinkingCollapsed(v);
  };

  return (
    <div className="grid w-full max-w-xs grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
      {([false, true] as const).map((v) => (
        <button
          key={String(v)}
          type="button"
          onClick={() => choose(v)}
          className={`min-h-[36px] rounded-md text-sm transition-colors ${
            on === v ? 'bg-border text-text' : 'text-dim hover:text-text'
          }`}
        >
          {v ? 'On' : 'Off'}
        </button>
      ))}
    </div>
  );
}

function ClearCacheControl() {
  const [cleared, setCleared] = useState(false);
  return (
    <div className="flex items-center gap-2">
      <Button
        variant="outline"
        onClick={() => {
          clearClientCaches();
          setCleared(true);
          window.setTimeout(() => setCleared(false), 2500);
        }}
      >
        Clear caches
      </Button>
      {cleared && (
        <span className="flex items-center gap-1 text-xs text-emerald-500">
          <IconCheck className="h-3.5 w-3.5" /> Cleared — the catalog will refetch on next use
        </span>
      )}
    </div>
  );
}

function ContextThresholdControl() {
  const [value, setValue] = useState('');
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .getSettings()
      .then((s) => setValue(String(s.contextThreshold ?? 80)))
      .catch(() => {});
  }, []);

  const restore = () => {
    api
      .getSettings()
      .then((s) => setValue(String(s.contextThreshold ?? 80)))
      .catch(() => {});
  };

  const save = async () => {
    const n = Number(value);
    if (!Number.isFinite(n) || n <= 0 || n > 100) {
      setError('Enter a percentage between 1 and 100.');
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await api.updateSettings({ contextThreshold: n });
      setSaved(true);
      window.setTimeout(() => setSaved(false), 2000);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <div className="relative w-24">
          <Input
            type="number"
            min={1}
            max={100}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onBlur={() => void save()}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.currentTarget.blur();
              } else if (e.key === 'Escape') {
                restore();
                e.currentTarget.blur();
              }
            }}
            className="h-8 pr-7 text-sm"
          />
          <span className="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 text-xs text-faint">
            %
          </span>
        </div>
        <span className="text-xs text-dim">of the context window</span>
        {saving && <Spinner className="h-3.5 w-3.5" />}
        {saved && !saving && (
          <span className="flex items-center gap-1 text-xs text-emerald-500">
            <IconCheck className="h-3.5 w-3.5" /> Saved
          </span>
        )}
      </div>
      {error && <p className="text-xs text-red-400">{error}</p>}
    </div>
  );
}

// Whether tool results are TOON-encoded for the model (Settings → Appearance
// → Tool calls). Server-backed so the agent loop reads it too.
function ToonControl() {
  const [on, setOn] = useState(true);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .getSettings()
      .then((s) => {
        setOn(s.toonEnabled ?? true);
        setLoaded(true);
      })
      .catch(() => setLoaded(true));
  }, []);

  const choose = async (v: boolean) => {
    setBusy(true);
    setError(null);
    try {
      await api.updateSettings({ toonEnabled: v });
      setOn(v);
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="grid w-full max-w-xs grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
        {([false, true] as const).map((v) => (
          <button
            key={String(v)}
            type="button"
            disabled={!loaded || busy}
            onClick={() => void choose(v)}
            className={`min-h-[36px] rounded-md text-sm transition-colors disabled:opacity-50 ${
              on === v ? 'bg-border text-text' : 'text-dim hover:text-text'
            }`}
          >
            {v ? 'On' : 'Off'}
          </button>
        ))}
      </div>
      {error && <p className="text-xs text-red-400">{error}</p>}
    </div>
  );
}

// CavemanControl toggles the terse "caveman" reply style for the agent.
function CavemanControl() {
  const [on, setOn] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .getSettings()
      .then((s) => {
        setOn(s.caveman ?? false);
        setLoaded(true);
      })
      .catch(() => setLoaded(true));
  }, []);

  const choose = async (v: boolean) => {
    setBusy(true);
    setError(null);
    try {
      await api.updateSettings({ caveman: v });
      setOn(v);
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="grid w-full max-w-xs grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
        {([false, true] as const).map((v) => (
          <button
            key={String(v)}
            type="button"
            disabled={!loaded || busy}
            onClick={() => void choose(v)}
            className={`min-h-[36px] rounded-md text-sm transition-colors disabled:opacity-50 ${
              on === v ? 'bg-border text-text' : 'text-dim hover:text-text'
            }`}
          >
            {v ? 'On' : 'Off'}
          </button>
        ))}
      </div>
      {error && <p className="text-xs text-red-400">{error}</p>}
    </div>
  );
}

// TurnTimeoutControl sets the per-user soft/hard turn timeouts in minutes
// (0 disables that side; leaving both 0 disables timeouts entirely).
function TurnTimeoutControl() {
  const [soft, setSoft] = useState(5);
  const [hard, setHard] = useState(10);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const loadedRef = useRef<{ soft: number; hard: number }>({ soft: 5, hard: 10 });

  useEffect(() => {
    api
      .getSettings()
      .then((s) => {
        const t = s.turnTimeouts ?? { soft: 5, hard: 10 };
        setSoft(t.soft);
        setHard(t.hard);
        loadedRef.current = t;
        setLoaded(true);
      })
      .catch(() => setLoaded(true));
  }, []);

  const save = async () => {
    setBusy(true);
    setSaved(false);
    setError(null);
    try {
      await api.updateSettings({ turnTimeouts: { soft, hard } });
      loadedRef.current = { soft, hard };
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const dirty = soft !== loadedRef.current.soft || hard !== loadedRef.current.hard;
  const num = (label: string, hint: string, val: number, set: (n: number) => void) => (
    <label className="flex flex-col gap-1">
      <span className="text-sm text-text">{label}</span>
      <input
        type="number"
        min={0}
        step={1}
        value={val}
        disabled={!loaded || busy}
        onChange={(e) => set(Math.max(0, Number(e.target.value) || 0))}
        className="w-24 rounded-lg border border-border bg-surface px-3 py-2 text-sm text-text outline-none transition-colors focus:border-subtle disabled:opacity-60"
      />
      <span className="text-[11px] text-faint">{hint}</span>
    </label>
  );

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      className="flex flex-col gap-3"
    >
      <div className="flex flex-wrap gap-4">
        {num('Soft timeout (min)', 'Warns the model it is working too long. 0 = off', soft, setSoft)}
        {num('Hard timeout (min)', 'Aborts the turn at this deadline. 0 = off', hard, setHard)}
      </div>
      {error && <p className="text-xs text-red-400">{error}</p>}
      <SaveRow saving={busy} saved={saved} error={null} pulse={dirty && loaded} />
    </form>
  );
}

// TerminalSettingsControl sets the terminal font size and wrapping.
function TerminalSettingsControl() {
  const [fontSize, setFontSize] = useState(13);
  const [wrap, setWrap] = useState(true);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const loadedRef = useRef({ fontSize: 13, wrap: true });

  useEffect(() => {
    api
      .getSettings()
      .then((s) => {
        setFontSize(s.terminalFontSize ?? 13);
        setWrap(s.terminalWrap ?? true);
        loadedRef.current = { fontSize: s.terminalFontSize ?? 13, wrap: s.terminalWrap ?? true };
        setLoaded(true);
      })
      .catch(() => setLoaded(true));
  }, []);

  const save = async () => {
    setBusy(true);
    setSaved(false);
    setError(null);
    try {
      await api.updateSettings({ terminalFontSize: fontSize, terminalWrap: wrap });
      loadedRef.current = { fontSize, wrap };
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const dirty = fontSize !== loadedRef.current.fontSize || wrap !== loadedRef.current.wrap;

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      className="flex flex-col gap-3"
    >
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <label className="flex flex-col gap-1">
          <span className="text-sm text-text">Font size</span>
          <input
            type="number"
            min={8}
            max={28}
            step={1}
            value={fontSize}
            disabled={!loaded || busy}
            onChange={(e) => setFontSize(Math.min(28, Math.max(8, Number(e.target.value) || 13)))}
            className="w-24 rounded-lg border border-border bg-surface px-3 py-2 text-sm text-text outline-none transition-colors focus:border-subtle disabled:opacity-60"
          />
          <span className="text-[11px] text-faint">px (8–28)</span>
        </label>
        <div className="flex flex-col gap-1">
          <span className="text-sm text-text">Wrap output</span>
          <div className="grid w-full max-w-[120px] grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
            {([true, false] as const).map((v) => (
              <button
                key={String(v)}
                type="button"
                disabled={!loaded || busy}
                onClick={() => setWrap(v)}
                className={`min-h-[30px] rounded-md text-sm transition-colors disabled:opacity-50 ${
                  wrap === v ? 'bg-border text-text' : 'text-dim hover:text-text'
                }`}
              >
                {v ? 'On' : 'Off'}
              </button>
            ))}
          </div>
        </div>
      </div>
      {error && <p className="text-xs text-red-400">{error}</p>}
      <SaveRow saving={busy} saved={saved} error={null} pulse={dirty && loaded} />
    </form>
  );
}

// AutoPushControl toggles the default for NEW projects: existing projects
// keep whatever they had (per-project settings are not rewritten).
function AutoPushControl() {
  const [on, setOn] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .getSettings()
      .then((s) => {
        setOn(s.autoPushDefault ?? false);
        setLoaded(true);
      })
      .catch(() => setLoaded(true));
  }, []);

  const choose = async (v: boolean) => {
    setBusy(true);
    setError(null);
    try {
      await api.updateSettings({ autoPushDefault: v });
      setOn(v);
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="grid w-full max-w-xs grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
        {([false, true] as const).map((v) => (
          <button
            key={String(v)}
            type="button"
            disabled={!loaded || busy}
            onClick={() => void choose(v)}
            className={`min-h-[36px] rounded-md text-sm transition-colors disabled:opacity-50 ${
              on === v ? 'bg-border text-text' : 'text-dim hover:text-text'
            }`}
          >
            {v ? 'On' : 'Off'}
          </button>
        ))}
      </div>
      <p className="text-xs text-subtle">
        New projects start with auto-push {on ? 'enabled' : 'disabled'}. Existing
        projects are never changed.
      </p>
      {error && <p className="text-xs text-red-400">{error}</p>}
    </div>
  );
}

function DebugHudControl() {
  const [on, setOn] = useState(() => getDebugHud());

  const choose = (v: boolean) => {
    setOn(v);
    setDebugHud(v);
  };

  return (
    <div className="grid w-full max-w-xs grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
      {([false, true] as const).map((v) => (
        <button
          key={String(v)}
          type="button"
          onClick={() => choose(v)}
          className={`min-h-[36px] rounded-md text-sm transition-colors ${
            on === v ? 'bg-border text-text' : 'text-dim hover:text-text'
          }`}
        >
          {v ? 'On' : 'Off'}
        </button>
      ))}
    </div>
  );
}

// OIDC single sign-on configuration, admin-only. Minimal required fields are
// issuer + client id + secret; the callback URI defaults to
// <origin>/api/auth/oidc/callback and allowed emails defaults to everyone.
function OidcControl() {
  // The values loaded from the server, so dirty only reflects real changes.
  const loaded = useRef({ issuer: '', clientId: '', callbackUri: '', allowedEmails: '' });
  const [issuer, setIssuer] = useState('');
  const [clientId, setClientId] = useState('');
  const [clientSecret, setClientSecret] = useState('');
  const [secretSet, setSecretSet] = useState(false);
  const [callbackUri, setCallbackUri] = useState('');
  const [allowedEmails, setAllowedEmails] = useState('');
  const [enabled, setEnabled] = useState(false);
  const [issuerEnv, setIssuerEnv] = useState(false);
  const [clientIdEnv, setClientIdEnv] = useState(false);
  const [secretEnv, setSecretEnv] = useState(false);
  const [callbackEnv, setCallbackEnv] = useState(false);
  const [allowedEnv, setAllowedEnv] = useState(false);
  const [copied, setCopied] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    api
      .oidcSettings()
      .then((c) => {
        setIssuer(c.issuer);
        setClientId(c.clientId);
        setSecretSet(c.clientSecretSet);
        setCallbackUri(c.callbackUri);
        setAllowedEmails(c.allowedEmails);
        setEnabled(c.enabled);
        setIssuerEnv(c.issuerFromEnv ?? false);
        setClientIdEnv(c.clientIdFromEnv ?? false);
        setSecretEnv(c.clientSecretFromEnv ?? false);
        setCallbackEnv(c.callbackUriFromEnv ?? false);
        setAllowedEnv(c.allowedEmailsFromEnv ?? false);
        loaded.current = {
          issuer: c.issuer,
          clientId: c.clientId,
          callbackUri: c.callbackUri,
          allowedEmails: c.allowedEmails,
        };
      })
      .catch(() => {});
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const defaultCallback = `${window.location.origin}/api/auth/oidc/callback`;
  const effectiveCallback = callbackUri.trim() || defaultCallback;
  // Only glow the save button when there are real unsaved changes. Env-sourced
  // fields are read-only and never count; an untouched secret input never
  // counts when a secret is already set (saved or env).
  const dirty = Boolean(
    !issuerEnv && issuer.trim() !== loaded.current.issuer.trim() ||
    !clientIdEnv && clientId.trim() !== loaded.current.clientId.trim() ||
    !secretEnv && clientSecret.trim() !== '' ||
    !callbackEnv && callbackUri.trim() !== loaded.current.callbackUri.trim() ||
    !allowedEnv && allowedEmails.trim() !== loaded.current.allowedEmails.trim(),
  );

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const res = await api.oidcSettingsSave({
        issuer: issuer.trim(),
        clientId: clientId.trim(),
        clientSecret: clientSecret.trim(),
        callbackUri: callbackUri.trim(),
        allowedEmails: allowedEmails.trim(),
      });
      setClientSecret('');
      setSecretSet(true);
      setEnabled(res.enabled);
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setSaving(false);
    }
  };

  const copyCallback = () => {
    void navigator.clipboard.writeText(effectiveCallback).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  // A read-only, horizontally scrollable box for env-managed values (long
  // URLs/issuers) — inputs can't scroll horizontally on every browser.
  const EnvValue = ({ value }: { value: string }) => (
    <div className="v1-no-scrollbar w-full overflow-x-auto whitespace-nowrap rounded-lg border border-border-strong bg-surface px-3 py-2 font-mono text-xs text-text opacity-70">
      {value || '(unset)'}
    </div>
  );

  return (
    <form onSubmit={(e) => { e.preventDefault(); void save(); }} className="flex flex-col gap-3">
      <p className="text-xs text-faint">
        Sign in with any OIDC provider (Authentik, Keycloak, Google…). The
        required fields are <span className="text-dim">Issuer</span>,{' '}
        <span className="text-dim">Client ID</span> and{' '}
        <span className="text-dim">Client secret</span> — the callback URI and
        allowed emails are optional.
      </p>
      {enabled ? (
        <p className="text-xs text-emerald-400">OIDC login is enabled.</p>
      ) : (
        <p className="text-xs text-faint">OIDC login is not configured yet.</p>
      )}
      <Field label="Issuer URL (required)">
        {issuerEnv ? (
          <>
            <EnvValue value={issuer} />
            <span className="mt-1 block text-[11px] text-faint">
              Set via{' '}
              <code className="font-mono text-dim">V1_OIDC_ISSUER</code>{' '}
              — read-only.
            </span>
          </>
        ) : (
          <Input
            value={issuer}
            onChange={(e) => setIssuer(e.target.value)}
            placeholder="https://auth.example.com/application/o/<slug>/"
            spellCheck={false}
            autoComplete="off"
          />
        )}
      </Field>
      <Field label="Client ID (required)">
        {clientIdEnv ? (
          <>
            <EnvValue value={clientId} />
            <span className="mt-1 block text-[11px] text-faint">
              Set via{' '}
              <code className="font-mono text-dim">V1_OIDC_CLIENT_ID</code>{' '}
              — read-only.
            </span>
          </>
        ) : (
          <Input
            value={clientId}
            onChange={(e) => setClientId(e.target.value)}
            placeholder="OIDC client ID from your provider"
            spellCheck={false}
            autoComplete="off"
          />
        )}
      </Field>
      <Field label="Client secret (required)">
        {secretEnv ? (
          <>
            <EnvValue value={secretSet ? '•••••••• (set via env)' : ''} />
            <span className="mt-1 block text-[11px] text-faint">
              Set via{' '}
              <code className="font-mono text-dim">V1_OIDC_CLIENT_SECRET</code>{' '}
              — read-only.
            </span>
          </>
        ) : (
          <Input
            type="password"
            value={clientSecret}
            onChange={(e) => setClientSecret(e.target.value)}
            placeholder={secretSet ? '(saved — leave blank to keep it)' : 'OIDC client secret'}
            autoComplete="new-password"
            className={secretEnv ? 'cursor-not-allowed opacity-60' : ''}
          />
        )}
      </Field>
      <Field label="Callback URI">
        {callbackEnv ? (
          <>
            <div className="relative">
              <EnvValue value={effectiveCallback} />
              <button
                type="button"
                onClick={copyCallback}
                aria-label="Copy callback URI"
                title="Copy"
                className="absolute right-2 top-1/2 inline-flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded-md text-faint transition-colors hover:bg-border hover:text-text"
              >
                {copied ? <IconCheck className="h-3.5 w-3.5" /> : <IconCopy className="h-3.5 w-3.5" />}
              </button>
            </div>
            <span className="mt-1 block text-[11px] text-faint">
              Set via{' '}
              <code className="font-mono text-dim">V1_OIDC_REDIRECT_URI</code>{' '}
              — read-only.
            </span>
          </>
        ) : (
          <>
            <Input
              value={callbackUri}
              onChange={(e) => setCallbackUri(e.target.value)}
              placeholder={defaultCallback}
              spellCheck={false}
              autoComplete="off"
              className="font-mono text-xs"
            />
            <span className="mt-1 block break-all font-mono text-[11px] text-faint">
              {effectiveCallback}
            </span>
          </>
        )}
      </Field>
      <Field label="Allowed emails">
        {allowedEnv ? (
          <>
            <EnvValue value={allowedEmails} />
            <span className="mt-1 block text-[11px] text-faint">
              Set via{' '}
              <code className="font-mono text-dim">V1_OIDC_ALLOWED_EMAILS</code>{' '}
              — read-only.
            </span>
          </>
        ) : (
          <>
            <Input
              value={allowedEmails}
              onChange={(e) => setAllowedEmails(e.target.value)}
              placeholder="you@example.com, teammate@example.com"
              spellCheck={false}
              autoComplete="off"
            />
            <span className="mt-1 block text-[11px] text-faint">
              Comma-separated list; blank allows any authenticated user.
            </span>
          </>
        )}
      </Field>
      <div className="flex items-center gap-2 rounded-lg border border-border-strong bg-surface/60 px-3 py-2 text-xs text-dim">
        <div className="min-w-0 flex-1">
          Register this URI with your provider:
          <span className="mt-0.5 block v1-no-scrollbar overflow-x-auto whitespace-nowrap font-mono text-text">
            {effectiveCallback}
          </span>
        </div>
        <button
          type="button"
          onClick={copyCallback}
          aria-label="Copy callback URI"
          title="Copy"
          className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-faint transition-colors hover:bg-border hover:text-text"
        >
          {copied ? <IconCheck className="h-4 w-4" /> : <IconCopy className="h-4 w-4" />}
        </button>
      </div>
      <SaveRow saving={saving} saved={saved} error={error} pulse={dirty} disabled={secretEnv && clientSecret !== ''} />
    </form>
  );
}

function UsersControl() {
  const [users, setUsers] = useState<UserInfo[] | null>(null);
  const [me, setMe] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [isAdmin, setIsAdmin] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    api
      .listUsers()
      .then(setUsers)
      .catch((e) => setError(errMsg(e)));
  }, []);

  useEffect(() => {
    load();
    api
      .getAuthStatus()
      .then((s) => setMe(s.user?.username ?? ''))
      .catch(() => {});
  }, [load]);

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.createUser(username.trim(), password, firstUser || isAdmin);
      setUsername('');
      setPassword('');
      setIsAdmin(false);
      await load();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (u: UserInfo) => {
    if (!window.confirm(`Delete ${u.username}? Their projects will be removed with them.`)) {
      return;
    }
    setError(null);
    try {
      await api.deleteUser(u.id);
      await load();
    } catch (err) {
      setError(errMsg(err));
    }
  };

  // Row actions: toggle the admin flag and reset another user's password.
  const [rowBusy, setRowBusy] = useState<string | null>(null);
  const [pwFor, setPwFor] = useState<string | null>(null);
  const [newPw, setNewPw] = useState('');

  const toggleAdmin = async (u: UserInfo, admin: boolean) => {
    if (u.username === me) return;
    setRowBusy(u.id);
    setError(null);
    try {
      await api.updateUser(u.id, { isAdmin: admin });
      await load();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setRowBusy(null);
    }
  };

  const savePassword = async (u: UserInfo) => {
    if (!newPw) return;
    setRowBusy(u.id);
    setError(null);
    try {
      await api.updateUser(u.id, { password: newPw });
      setPwFor(null);
      setNewPw('');
      await load();
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setRowBusy(null);
    }
  };

  // The first account of an instance must be an admin.
  const firstUser = users !== null && users.length === 0;
  // The last admin cannot be demoted (mirrors the server-side guard).
  const lastAdmin = (u: UserInfo) =>
    u.isAdmin && (users?.filter((x) => x.isAdmin).length ?? 0) <= 1;

  return (
    <div className="flex flex-col">
      <form onSubmit={create} className="flex flex-col gap-3">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Field label="Username">
            <Input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="e.g. alice"
              autoComplete="off"
            />
          </Field>
          <Field label="Password">
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="New account password"
              autoComplete="new-password"
            />
          </Field>
        </div>
        <div className="flex items-center justify-between gap-2">
          <label className="flex items-center gap-2 text-xs text-dim">
            Admin
            <button
              type="button"
              role="switch"
              aria-checked={firstUser || isAdmin}
              aria-label="Make the new user an admin"
              disabled={firstUser}
              onClick={() => setIsAdmin((v) => !v)}
              className={`relative h-5 w-9 shrink-0 rounded-full transition-colors ${
                firstUser || isAdmin ? 'bg-accent' : 'bg-border'
              } ${firstUser ? 'cursor-not-allowed opacity-60' : ''}`}
            >
              <span
                className={`absolute top-0.5 h-4 w-4 rounded-full bg-bg transition-all ${
                  firstUser || isAdmin ? 'left-[18px]' : 'left-0.5'
                }`}
              />
            </button>
          </label>
          {firstUser && (
            <p className="text-xs text-faint">The first user must be an admin.</p>
          )}
          <Button
            type="submit"
            disabled={busy || username.trim() === '' || password === ''}
            className="h-8 whitespace-nowrap px-3 text-sm"
          >
            {busy ? <Spinner className="h-3.5 w-3.5" /> : 'Add user'}
          </Button>
        </div>
        {error && <ErrorBox message={error} />}
      </form>

      {users !== null && users.length > 0 && (
        <>
          <div className="my-4 border-t border-border" />
          <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            {users.map((u) => {
              const isMe = u.username === me;
              return (
                <li
                  key={u.id}
                  className="flex flex-col gap-2.5 rounded-xl border border-border bg-surface p-3"
                >
                  <div className="flex items-center gap-2">
                    <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-border text-xs font-semibold text-dim">
                      {(u.username[0] ?? '?').toUpperCase()}
                    </span>
                    <span className="min-w-0 flex-1 truncate text-sm font-medium text-text">
                      {u.username}
                    </span>
                    {u.oidc ? (
                      <span
                        className="shrink-0 rounded-full bg-accent/15 px-1.5 py-0.5 text-[10px] font-medium text-accent"
                        title="This account signs in via OIDC (no local password)"
                      >
                        OIDC
                      </span>
                    ) : (
                      <span
                        className="shrink-0 rounded-full bg-border px-1.5 py-0.5 text-[10px] text-dim"
                        title="Local account with a password"
                      >
                        internal
                      </span>
                    )}
                    {isMe && (
                      <span className="shrink-0 rounded-full bg-border px-1.5 py-0.5 text-[10px] text-dim">
                        you
                      </span>
                    )}
                    <label
                      className={`flex items-center gap-1.5 text-[11px] text-subtle ${
                        isMe || lastAdmin(u) ? 'opacity-60' : ''
                      }`}
                    >
                      Admin
                      <button
                        type="button"
                        role="switch"
                        aria-checked={u.isAdmin}
                        aria-label={`${u.isAdmin ? 'Remove admin role from' : 'Grant admin role to'} ${u.username}`}
                        title={
                          isMe
                            ? 'You cannot change your own role'
                            : lastAdmin(u)
                              ? 'Cannot demote the last admin'
                              : u.isAdmin
                                ? 'Remove admin role'
                                : 'Grant admin role'
                        }
                        disabled={isMe || rowBusy === u.id || lastAdmin(u)}
                        onClick={() => void toggleAdmin(u, !u.isAdmin)}
                        className={`relative h-5 w-9 shrink-0 rounded-full transition-colors ${
                          u.isAdmin ? 'bg-accent' : 'bg-border'
                        } ${isMe ? 'cursor-not-allowed' : ''}`}
                      >
                        <span
                          className={`absolute top-0.5 h-4 w-4 rounded-full bg-bg transition-all ${
                            u.isAdmin ? 'left-[18px]' : 'left-0.5'
                          }`}
                        />
                      </button>
                    </label>
                    {!isMe && (
                      <>
                        <button
                          type="button"
                          aria-label={`Set password for ${u.username}`}
                          title={pwFor === u.id ? 'Cancel' : 'Set password'}
                          onClick={() => {
                            setPwFor(pwFor === u.id ? null : u.id);
                            setNewPw('');
                          }}
                          className={`inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-text ${
                            pwFor === u.id ? 'bg-border text-text' : ''
                          }`}
                        >
                          <IconKey className="h-3.5 w-3.5" />
                        </button>
                        <button
                          type="button"
                          aria-label={`Delete ${u.username}`}
                          title="Delete user"
                          onClick={() => void remove(u)}
                          className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-red-400"
                        >
                          <IconTrash className="h-3.5 w-3.5" />
                        </button>
                      </>
                    )}
                  </div>
                  {pwFor === u.id && (
                    <div className="flex items-center gap-2">
                      <Input
                        type="password"
                        value={newPw}
                        onChange={(e) => setNewPw(e.target.value)}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter' && newPw) void savePassword(u);
                        }}
                        placeholder="New password"
                        autoFocus
                        className="h-8 text-sm"
                      />
                      <Button
                        variant="outline"
                        className="h-8 shrink-0 px-3 text-xs"
                        disabled={!newPw || rowBusy === u.id}
                        onClick={() => void savePassword(u)}
                      >
                        {rowBusy === u.id ? <Spinner className="h-3 w-3" /> : 'Save'}
                      </Button>
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        </>
      )}
      {users === null && <Spinner className="h-4 w-4" />}
      {users !== null && users.length === 0 && (
        <p className="text-sm text-faint">No users yet.</p>
      )}
    </div>
  );
}

function NotificationsControl() {
  const supported = 'Notification' in window;
  const ios = isIOS();
  const standalone = isStandalone();
  const ver = iosVersion();
  const [on, setOn] = useState(() => getNotifyEnabled());
  const [turnDone, setTurnDone] = useState(() => getNotifyTurnDone());
  const [turnError, setTurnError] = useState(() => getNotifyTurnError());
  const [onlyBackground, setOnlyBackground] = useState(() => getNotifyOnlyBackground());
  const [perm, setPerm] = useState(() => (supported ? Notification.permission : 'denied'));
  const [testing, setTesting] = useState(false);
  const [tested, setTested] = useState<'ok' | 'fail' | null>(null);
  const [swReady, setSwReady] = useState<boolean | null>(null);

  useEffect(() => {
    if (!('serviceWorker' in navigator)) {
      setSwReady(false);
      return;
    }
    navigator.serviceWorker
      .getRegistration()
      .then((r) => setSwReady(r != null))
      .catch(() => setSwReady(false));
  }, []);

  const choose = async (v: boolean) => {
    if (!v) {
      setOn(false);
      setNotifyEnabled(false);
      return;
    }
    setOn(true);
    setNotifyEnabled(true);
    if (supported && Notification.permission === 'default') {
      const p = await Notification.requestPermission();
      setPerm(p);
      if (p !== 'granted') {
        setOn(false);
        setNotifyEnabled(false);
      }
    }
  };

  // Small segmented toggle reused by each fine-grained option.
  const Seg = ({ value, onChoose }: { value: boolean; onChoose: (v: boolean) => void }) => (
    <div className="grid w-full max-w-[120px] grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
      {([true, false] as const).map((v) => (
        <button
          key={String(v)}
          type="button"
          onClick={() => onChoose(v)}
          className={`min-h-[30px] rounded-md text-sm transition-colors ${
            value === v ? 'bg-border text-text' : 'text-dim hover:text-text'
          }`}
        >
          {v ? 'On' : 'Off'}
        </button>
      ))}
    </div>
  );

  const sendTest = async () => {
    setTesting(true);
    setTested(null);
    try {
      setTested((await testNotification()) ? 'ok' : 'fail');
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="grid w-full max-w-xs grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
        {([false, true] as const).map((v) => (
          <button
            key={String(v)}
            type="button"
            onClick={() => void choose(v)}
            className={`min-h-[36px] rounded-md text-sm transition-colors ${
              on === v ? 'bg-border text-text' : 'text-dim hover:text-text'
            }`}
          >
            {v ? 'On' : 'Off'}
          </button>
        ))}
      </div>
      {ios && !standalone && (
        <p className="text-xs leading-relaxed text-faint">
          iOS only supports notifications in the installed app: tap{' '}
          <span className="text-dim">Share → Add to Home Screen</span>, then open v1 from the
          home screen icon.
        </p>
      )}
      {!supported && (
        <p className="text-xs text-faint">This browser does not support notifications.</p>
      )}
      {supported && ios && ver > 0 && ver < 16.4 && (
        <p className="text-xs text-faint">
          Notifications on iOS require iOS 16.4 or later — update your iPhone first.
        </p>
      )}
      {supported && ios && ver >= 16.4 && ver < 16.5 && (
        <p className="text-xs text-faint">
          iOS 16.4 has a known bug where the permission prompt may not appear at all. Update to
          iOS 16.5 or later, then try again.
        </p>
      )}
      {supported && perm === 'denied' && (
        <p className="text-xs leading-relaxed text-faint">
          {ios && !standalone
            ? 'Open v1 from your Home Screen to be able to request notification permission.'
            : ios
              ? 'iOS shows the permission prompt only once per install. If it was denied or dismissed — or never appeared — delete the app from your Home Screen (long-press the icon → Remove) and add it again via Share → Add to Home Screen. A fresh install can request permission again.'
              : 'Notifications are blocked — allow them in the browser or system settings.'}
        </p>
      )}
      {supported && perm === 'granted' && ios && (
        <p className="text-xs leading-relaxed text-faint">
          If notifications don&apos;t arrive, open iOS Settings → v1 → Notifications and make
          sure it&apos;s enabled. The entry appears under the app&apos;s home screen name once
          permission has been granted.
        </p>
      )}
      {supported && swReady === false && (
        <p className="text-xs text-faint">
          No service worker is active — iOS requires one for notifications. Reload the page
          once, then try again.
        </p>
      )}
      {supported && perm === 'granted' && (
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            className="h-8 px-3 text-xs"
            disabled={testing}
            onClick={() => void sendTest()}
          >
            {testing ? <Spinner className="h-3.5 w-3.5" /> : 'Send test notification'}
          </Button>
          {tested === 'ok' && (
            <span className="flex items-center gap-1 text-xs text-emerald-500">
              <IconCheck className="h-3.5 w-3.5" /> Sent
            </span>
          )}
          {tested === 'fail' && <span className="text-xs text-red-400">Could not send</span>}
        </div>
      )}
      {/* Fine-grained options (only meaningful when notifications are on). */}
      {on && (
        <div className="mt-1 flex flex-col gap-3 border-t border-border pt-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <p className="text-sm text-text">Turn finished</p>
              <p className="text-[11px] text-faint">When a chat turn completes</p>
            </div>
            <Seg value={turnDone} onChoose={(v) => { setTurnDone(v); setNotifyTurnDone(v); }} />
          </div>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <p className="text-sm text-text">Turn failed</p>
              <p className="text-[11px] text-faint">When a chat turn errors out</p>
            </div>
            <Seg value={turnError} onChoose={(v) => { setTurnError(v); setNotifyTurnError(v); }} />
          </div>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <p className="text-sm text-text">Only while backgrounded</p>
              <p className="text-[11px] text-faint">Skip notifications when the app is focused</p>
            </div>
            <Seg value={onlyBackground} onChoose={(v) => { setOnlyBackground(v); setNotifyOnlyBackground(v); }} />
          </div>
        </div>
      )}
    </div>
  );
}

// Tab metadata for the chat tabs control (mirrors the bar in ChatPanel).
const CHAT_TAB_META: Record<ChatTab, { label: string; icon: ReactNode }> = {
  chat: { label: 'Chat', icon: <IconChat className="h-4 w-4" /> },
  files: { label: 'Files', icon: <IconFolder className="h-4 w-4" /> },
  terminal: { label: 'Terminal', icon: <IconTerminal className="h-4 w-4" /> },
  git: { label: 'Git', icon: <IconGitBranch className="h-4 w-4" /> },
  memories: { label: 'Memories', icon: <IconBrain className="h-4 w-4" /> },
  github: { label: 'GitHub', icon: <IconGitHub className="h-4 w-4" /> },
  project: { label: 'Project', icon: <IconSettings className="h-4 w-4" /> },
};

// Chat tabs control: reorder (drag & drop, or the arrows) and show/hide the
// project view's tabs. Hidden tabs sit in a dashed group below a divider.
function ChatTabsControl() {
  const [tabs, setTabs] = useState<{ id: ChatTab; hidden: boolean }[]>(() => {
    const l = getChatTabLayout();
    return [
      ...l.order.map((id) => ({ id, hidden: false })),
      ...l.hidden.map((id) => ({ id, hidden: true })),
    ];
  });
  const [dragId, setDragId] = useState<ChatTab | null>(null);
  const [drop, setDrop] = useState<{ id: ChatTab; side: 'before' | 'after' } | null>(null);

  const persist = (next: { id: ChatTab; hidden: boolean }[]) => {
    setTabs(next);
    setChatTabLayout({
      order: next.filter((t) => !t.hidden).map((t) => t.id),
      hidden: next.filter((t) => t.hidden).map((t) => t.id),
    });
  };

  const hiddenOf = (id: ChatTab) => tabs.find((t) => t.id === id)?.hidden ?? false;

  const toggleHidden = (id: ChatTab) => {
    const t = tabs.find((x) => x.id === id);
    if (!t) return;
    // Keep at least one tab visible so the bar never empties out.
    if (!t.hidden && tabs.filter((x) => !x.hidden).length === 1) return;
    persist(tabs.map((x) => (x.id === id ? { ...x, hidden: !x.hidden } : x)));
  };

  // Move id next to toId within its own group (visible or hidden).
  const move = (id: ChatTab, toId: ChatTab, side: 'before' | 'after') => {
    if (id === toId || hiddenOf(id) !== hiddenOf(toId)) return;
    const item = tabs.find((t) => t.id === id);
    if (!item) return;
    const rest = tabs.filter((t) => t.id !== id);
    const at = rest.findIndex((t) => t.id === toId);
    rest.splice(at + (side === 'after' ? 1 : 0), 0, item);
    persist(rest);
  };

  const shift = (id: ChatTab, dir: -1 | 1) => {
    const idx = tabs.findIndex((t) => t.id === id);
    const item = tabs[idx];
    if (!item) return;
    const group = tabs.filter((t) => t.hidden === item.hidden);
    const gIdx = group.findIndex((t) => t.id === id);
    const target = gIdx + dir;
    if (target < 0 || target >= group.length) return;
    const next = [...tabs];
    const otherIdx = next.findIndex((t) => t.id === group[target].id);
    [next[idx], next[otherIdx]] = [next[otherIdx], next[idx]];
    persist(next);
  };

  const row = (t: { id: ChatTab; hidden: boolean }, i: number, groupLen: number) => {
    const meta = CHAT_TAB_META[t.id];
    return (
      <div
        key={t.id}
        draggable
        onDragStart={(e) => {
          setDragId(t.id);
          setDrop(null);
          e.dataTransfer.effectAllowed = 'move';
        }}
        onDragEnd={() => {
          setDragId(null);
          setDrop(null);
        }}
        onDragOver={(e: DragEvent<HTMLDivElement>) => {
          if (!dragId || hiddenOf(dragId) !== t.hidden) return;
          e.preventDefault();
          e.dataTransfer.dropEffect = 'move';
          const rect = e.currentTarget.getBoundingClientRect();
          const side = e.clientY < rect.top + rect.height / 2 ? 'before' : 'after';
          setDrop((d) => (d && d.id === t.id && d.side === side ? d : { id: t.id, side }));
        }}
        onDrop={(e: DragEvent<HTMLDivElement>) => {
          e.preventDefault();
          if (dragId && drop) move(dragId, drop.id, drop.side);
          setDragId(null);
          setDrop(null);
        }}
        className={`relative flex cursor-grab items-center gap-2 rounded-lg border px-2.5 py-2 transition-colors active:cursor-grabbing ${
          dragId === t.id ? 'opacity-40' : ''
        } ${t.hidden ? 'border-dashed border-border' : 'border-border bg-surface'}`}
      >
        {drop && drop.id === t.id && (
          <span
            className={`pointer-events-none absolute inset-x-1.5 h-0.5 rounded-full bg-accent ${
              drop.side === 'before' ? '-top-0.5' : '-bottom-0.5'
            }`}
          />
        )}
        <IconGrip className="h-4 w-4 shrink-0 text-faint" />
        <span
          className={`flex min-w-0 items-center gap-2 text-sm ${
            t.hidden ? 'text-faint' : 'text-text'
          }`}
        >
          {meta.icon}
          {meta.label}
        </span>
        <div className="ml-auto flex shrink-0 items-center gap-0.5">
          {!t.hidden && (
            <>
              <button
                type="button"
                aria-label={`Move ${meta.label} up`}
                disabled={i === 0}
                onClick={() => shift(t.id, -1)}
                className="inline-flex h-7 w-7 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-text disabled:opacity-30"
              >
                <IconChevronUp className="h-3.5 w-3.5" />
              </button>
              <button
                type="button"
                aria-label={`Move ${meta.label} down`}
                disabled={i === groupLen - 1}
                onClick={() => shift(t.id, 1)}
                className="inline-flex h-7 w-7 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-text disabled:opacity-30"
              >
                <IconChevronDown className="h-3.5 w-3.5" />
              </button>
            </>
          )}
          <button
            type="button"
            aria-label={t.hidden ? `Show ${meta.label} tab` : `Hide ${meta.label} tab`}
            title={t.hidden ? 'Show tab' : 'Hide tab'}
            disabled={!t.hidden && tabs.filter((x) => !x.hidden).length === 1}
            onClick={() => toggleHidden(t.id)}
            className="inline-flex h-7 w-7 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-text disabled:opacity-30"
          >
            {t.hidden ? <IconEyeOff className="h-3.5 w-3.5" /> : <IconEye className="h-3.5 w-3.5" />}
          </button>
        </div>
      </div>
    );
  };

  const visible = tabs.filter((t) => !t.hidden);
  const hidden = tabs.filter((t) => t.hidden);

  return (
    <div className="flex max-w-md flex-col gap-1.5">
      {visible.map((t, i) => row(t, i, visible.length))}
      {hidden.length > 0 && (
        <>
          <div className="mt-2 flex items-center gap-2 text-[11px] text-faint">
            Hidden
            <div className="h-px flex-1 bg-border" />
          </div>
          {hidden.map((t, i) => row(t, i, hidden.length))}
        </>
      )}
    </div>
  );
}

export default function Settings() {
  const [settings, setSettings] = useState<SettingsType | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  // LLM
  const [baseURL, setBaseURL] = useState('');
  const [model, setModel] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [llmSaving, setLlmSaving] = useState(false);
  const [llmSaved, setLlmSaved] = useState(false);
  const [llmError, setLlmError] = useState<string | null>(null);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{
    ok: boolean;
    error?: string;
    baseURL: string;
  } | null>(null);

  // Saved providers
  const [providers, setProviders] = useState<SettingsType['llm']['providers']>([]);
  const [provName, setProvName] = useState('');
  const [provBusyId, setProvBusyId] = useState<string | null>(null);
  const [provError, setProvError] = useState<string | null>(null);
  // What the form edits: a saved provider id, 'new' for a fresh entry, or null
  // when collapsed (only the Add button + saved list are shown). Keeping the
  // form collapsed by default is what stops accidental overwrites.
  const [editing, setEditing] = useState<string | 'new' | null>(null);
  const [currency, setCurrency] = useState('USD');

  // GitHub PAT
  const [token, setToken] = useState('');
  const [ghSaving, setGhSaving] = useState(false);
  const [ghSaved, setGhSaved] = useState(false);
  const [ghError, setGhError] = useState<string | null>(null);

  // GitHub OAuth client ID + secret
  const [oauthClientId, setOauthClientId] = useState('');
  const [oauthClientIdEnv, setOauthClientIdEnv] = useState(false);
  const [oauthClientSecret, setOauthClientSecret] = useState('');
  const [oauthClientSecretSet, setOauthClientSecretSet] = useState(false);
  const [secretEnv, setSecretEnv] = useState(false);
  const [cidSaving, setCidSaving] = useState(false);
  const [cidSaved, setCidSaved] = useState(false);
  const [cidError, setCidError] = useState<string | null>(null);
  // While connected, the config forms collapse into a details card; this
  // flips them back without clearing the token.
  const [editingGithub, setEditingGithub] = useState(false);

  // Vercel
  const [vercelToken, setVercelToken] = useState('');
  const [vercelClientId, setVercelClientId] = useState('');
  const [vercelClientSecret, setVercelClientSecret] = useState('');
  const [vercelSaving, setVercelSaving] = useState(false);
  const [vercelSaved, setVercelSaved] = useState(false);
  const [vercelError, setVercelError] = useState<string | null>(null);
  const [vercelUser, setVercelUser] = useState<string | null>(null);
  const [vercelUserError, setVercelUserError] = useState<string | null>(null);
  const [githubUser, setGithubUser] = useState<string | null>(null);

  // Auth
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [pwSaving, setPwSaving] = useState(false);
  const [pwSaved, setPwSaved] = useState(false);
  const [pwError, setPwError] = useState<string | null>(null);

  // Current user — the Users page is admin-only and About shows the identity.
  const [me, setMe] = useState<string | null>(null);
  const [isAdmin, setIsAdmin] = useState(false);
  const [oidcUser, setOidcUser] = useState(false);
  useEffect(() => {
    api
      .getAuthStatus()
      .then((s) => {
        setMe(s.user?.username ?? null);
        setIsAdmin(s.user?.isAdmin ?? false);
        setOidcUser(s.user?.oidcUser ?? false);
      })
      .catch(() => {});
  }, []);

  // Global system prompt
  const [sysPrompt, setSysPrompt] = useState('');
  const [sysSaving, setSysSaving] = useState(false);
  const [sysSaved, setSysSaved] = useState(false);
  const [sysError, setSysError] = useState<string | null>(null);

  // Default thinking level ('' = the model's lowest level)
  const [defThinking, setDefThinking] = useState('');
  const [dtSaving, setDtSaving] = useState(false);
  const [dtSaved, setDtSaved] = useState(false);
  const [dtError, setDtError] = useState<string | null>(null);

  // Default model for new sessions (auto-populated from the first provider).
  const [defaultModel, setDefaultModel] = useState('');
  // The set of model ids the user can pick as a default, from their providers.
  const defaultModelOptions = useMemo(() => {
    const out: { provider: string; models: { id: string; name: string; m: string }[] }[] = [];
    const seen = new Set<string>();
    for (const p of providers) {
      const m = p.model.trim();
      if (!m || seen.has(m)) continue;
      seen.add(m);
      out.push({ provider: p.name || 'Provider', models: [{ id: m, name: m, m }] });
    }
    return out;
  }, [providers]);
  const [dmSaved, setDmSaved] = useState(false);
  const [dmError, setDmError] = useState<string | null>(null);

  const location = useLocation();
  const from = (location.state as { from?: string } | null)?.from;
  const backTo = from && from.startsWith('/') ? from : '/';
  const [params, setSearchParams] = useSearchParams();
  const initialPage = params.get('page');
  const [page, setPage] = useState<NavId>(
    NAV.some((n) => n.id === initialPage) ? (initialPage as NavId) : 'llm',
  );
  // OAuth-callback failures come back as ?page=vercel&vercel=<reason>.
  const [vercelOAuthError, setVercelOAuthError] = useState<string | null>(null);
  useEffect(() => {
    const why = params.get('vercel');
    if (why) {
      setVercelOAuthError(
        why === 'invalid_client'
          ? 'Vercel rejected the OAuth Client ID/Secret — check that the Client ID and Secret saved above match the OAuth2 app at vercel.com/account/settings/oauth (the secret is shown only when the app is created; a token is not the secret).'
          : why === 'expired'
            ? 'The Vercel authorization expired before completing — try connecting again.'
            : 'The Vercel OAuth flow failed. Check the Client ID/Secret and callback URL, then try again.',
      );
      // Clear the param so the banner doesn't persist on reloads.
      const next = new URLSearchParams(params);
      next.delete('vercel');
      setSearchParams(next, { replace: true });
    }
  }, [params, setSearchParams]);
  // Settings search: a loose query over the catalog; picking a result
  // deep-links to ?page=…&section=… which switches the tab and scrolls.
  const [query, setQuery] = useState('');
  const searchResults = useMemo(() => {
    if (!query.trim()) return [] as (typeof SETTINGS_SEARCH)[number][];
    return SETTINGS_SEARCH.map((e) => ({ e, s: scoreSettings(query, e) }))
      .filter((x) => x.s > 0)
      .sort((a, b) => b.s - a.s)
      .slice(0, 5)
      .map((x) => x.e);
  }, [query]);
  const goToSetting = (e: (typeof SETTINGS_SEARCH)[number]) => {
    setQuery('');
    setSearchParams({ page: e.page, section: e.id });
  };
  // Deep links can land while another tab is showing — follow the page param.
  useEffect(() => {
    const p = params.get('page');
    if (p && NAV.some((n) => n.id === p)) setPage(p as NavId);
  }, [params]);
  // Non-admins can't open the Users page; OIDC non-admins can't open Auth.
  useEffect(() => {
    if (!isAdmin && page === 'users') setPage('llm');
    if (oidcUser && !isAdmin && page === 'auth') setPage('llm');
  }, [isAdmin, oidcUser, page]);
  const visibleNav = NAV.filter(
    (n) =>
      (!('admin' in n) || !n.admin || isAdmin) &&
      // OIDC non-admins can't change a password — hide the Auth nav entry.
      // Admins keep it so they can reach the OIDC configuration.
      !(n.id === 'auth' && oidcUser && !isAdmin),
  );
  // Tools deep links pick the MCP/skills/perms tab.
  const toolsSection = params.get('section');
  const toolsInitialTab: ToolsTab =
    toolsSection === 'skills' ? 'skills' : toolsSection === 'perms' ? 'perms' : 'mcp';
  // Scroll the target section into view and flash it once the page renders.
  const sectionParam = params.get('section');
  useEffect(() => {
    if (!sectionParam) return;
    const t = setTimeout(() => {
      const el = document.getElementById(sectionParam);
      if (!el) return;
      el.scrollIntoView({ behavior: 'smooth', block: 'start' });
      el.classList.add('v1-section-flash');
      setTimeout(() => el.classList.remove('v1-section-flash'), 2600);
    }, 150);
    return () => clearTimeout(t);
  }, [sectionParam, page]);
  const [mobileOpen, setMobileOpen] = useState(false);
  const ddRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!mobileOpen) return;
    const onDoc = (e: MouseEvent) => {
      if (ddRef.current && !ddRef.current.contains(e.target as Node)) setMobileOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [mobileOpen]);

  const reloadSettings = () => {
    api
      .getSettings()
      .then((s) => {
        setSettings(s);
        setBaseURL(s.llm.baseURL);
        setModel(s.llm.model);
        setDefaultModel(s.llm.defaultModel ?? '');
        setOauthClientId(s.github.oauthClientId);
        setOauthClientIdEnv(s.github.oauthClientIdFromEnv ?? false);
        setOauthClientSecretSet(s.github.oauthClientSecretSet ?? false);
        setSecretEnv(s.github.oauthClientSecretFromEnv ?? false);
        setVercelClientId(s.vercel.oauthClientId);
        setProviders(s.llm.providers ?? []);
        setCurrency(s.llm?.currency ?? 'USD');
        setSysPrompt(s.systemPrompt ?? '');
        setDefThinking(s.defaultThinking ?? '');
      })
      .catch((e) => setLoadError(errMsg(e)));
  };

  const loadVercelUser = useCallback(() => {
    api
      .vercelUser()
      .then((u) => {
        setVercelUser(u.connected && u.login ? u.login : null);
        setVercelUserError(u.connected ? null : (u.error ?? null));
      })
      .catch((e) => setVercelUserError(errMsg(e)));
  }, []);

  const loadGithubUser = useCallback(() => {
    api
      .githubUser()
      .then((u) => setGithubUser(u.login ?? null))
      .catch(() => setGithubUser(null));
  }, []);

  useEffect(() => {
    reloadSettings();
    void loadVercelUser();
  }, [loadVercelUser]);

  // Fetch the GitHub login whenever a GitHub token is present.
  useEffect(() => {
    if (settings?.github.tokenSet) void loadGithubUser();
    else setGithubUser(null);
  }, [settings?.github.tokenSet, loadGithubUser]);

  const saveLLM = async (e: FormEvent) => {
    e.preventDefault();
    setLlmSaving(true);
    setLlmSaved(false);
    setLlmError(null);
    try {
      let host = '';
      try {
        host = new URL(baseURL).hostname;
      } catch {
        host = '';
      }
      const name = provName.trim() || host || 'Provider';
      if (editing && editing !== 'new') {
        // Editing one saved provider only.
        const list = providers.map((p) =>
          p.id === editing
            ? { id: p.id, name, baseURL, model, ...(apiKey ? { apiKey } : {}) }
            : { id: p.id, name: p.name, baseURL: p.baseURL, model: p.model },
        );
        await api.updateSettings({ llm: { providers: list } });
      } else {
        // Adding a fresh provider: append it. On the very first save the
        // backend mirrors it into the effective config so chat works
        // immediately, and the default model is populated from the first
        // model that provider actually serves.
        const firstProvider = providers.length === 0;
        const newId = randomId();
        const list = providers.map((p) => ({
          id: p.id,
          name: p.name,
          baseURL: p.baseURL,
          model: p.model,
        }));
        list.push({ id: newId, name, baseURL, model, ...(apiKey ? { apiKey } : {}) });
        await api.updateSettings({ llm: { providers: list } });
        if (firstProvider) await populateDefaultModel(newId);
      }
      setLlmSaved(true);
      setApiKey('');
      setEditing(null);
      reloadSettings();
    } catch (err) {
      setLlmError(errMsg(err));
    } finally {
      setLlmSaving(false);
    }
  };

  // Open a blank form for a brand-new provider. Never touches existing ones.
  const startAdd = () => {
    setEditing('new');
    setProvName('');
    setBaseURL('');
    setModel('');
    setApiKey('');
    setLlmError(null);
  };

  // Load a saved provider into the form for editing.
  const startEdit = (p: { id: string; name: string; baseURL: string; model: string }) => {
    setEditing(p.id);
    setProvName(p.name);
    setBaseURL(p.baseURL);
    setModel(p.model);
    setApiKey('');
    setLlmError(null);
  };

  const cancelEdit = () => {
    setEditing(null);
    setLlmError(null);
  };

  const saveCurrency = async (v: string) => {
    setCurrency(v);
    try {
      await api.updateSettings({ llm: { currency: v } });
    } catch {
      // ignore — the picker still reflects the chosen value locally
    }
  };

  const saveDefaultModel = async (v: string) => {
    const next = v.trim();
    setDefaultModel(next);
    setDmError(null);
    try {
      await api.updateSettings({ llm: { defaultModel: next } });
      setDmSaved(true);
      window.setTimeout(() => setDmSaved(false), 2000);
    } catch (e) {
      setDmError(errMsg(e));
    }
  };

  // After the very first provider is saved, populate the default model with
  // the first model that provider actually serves (fall back to the server
  // mirroring it when the provider list is still empty or offline).
  const populateDefaultModel = async (providerId: string) => {
    if (defaultModel.trim()) return;
    try {
      const res = await api.getProviders();
      const p = res.providers.find((x) => x.id === providerId && x.added);
      const firstModel = p && p.models.length > 0 ? p.models[0].id : '';
      if (!firstModel) return;
      await api.updateSettings({ llm: { defaultModel: firstModel } });
      setDefaultModel(firstModel);
    } catch {
      // Non-fatal — the user can set the default manually in the field above.
    }
  };

  const deleteProvider = async (id: string) => {
    setProvBusyId(id);
    setProvError(null);
    try {
      const rest = providers.filter((p) => p.id !== id);
      await api.updateSettings({
        llm: {
          providers: rest.map((p) => ({ id: p.id, name: p.name, baseURL: p.baseURL, model: p.model })),
        },
      });
      reloadSettings();
    } catch (err) {
      setProvError(errMsg(err));
    } finally {
      setProvBusyId(null);
    }
  };

  const testLLM = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const r = await api.testLLM({ baseURL, apiKey, model });
      setTestResult({ ok: r.ok, error: r.error, baseURL });
    } catch (err) {
      setTestResult({ ok: false, error: errMsg(err), baseURL });
    } finally {
      setTesting(false);
    }
  };

  const saveGitHub = async (e: FormEvent) => {
    e.preventDefault();
    if (!token.trim()) {
      setGhError('Enter a token to save.');
      return;
    }
    setGhSaving(true);
    setGhSaved(false);
    setGhError(null);
    try {
      await api.updateSettings({ githubToken: token.trim() });
      setGhSaved(true);
      setToken('');
      setEditingGithub(false);
      setSettings((prev) =>
        prev ? { ...prev, github: { ...prev.github, tokenSet: true, source: 'pat' } } : prev,
      );
    } catch (err) {
      setGhError(errMsg(err));
    } finally {
      setGhSaving(false);
    }
  };

  const disconnectToken = async () => {
    if (!window.confirm('Disconnect GitHub? The stored token will be cleared.')) return;
    setGhSaving(true);
    setGhError(null);
    try {
      await api.updateSettings({ githubToken: '' });
      setSettings((prev) =>
        prev ? { ...prev, github: { ...prev.github, tokenSet: false, source: null } } : prev,
      );
      setGithubUser(null);
      setEditingGithub(false);
      reloadSettings();
    } catch (err) {
      setGhError(errMsg(err));
    } finally {
      setGhSaving(false);
    }
  };

  const saveClientId = async (e: FormEvent) => {
    e.preventDefault();
    setCidSaving(true);
    setCidSaved(false);
    setCidError(null);
    try {
      // Both fields save together; empty secret keeps whatever is stored
      // (server only overwrites when non-empty is sent).
      await api.updateSettings({
        githubOAuthClientId: oauthClientId.trim(),
        ...(oauthClientSecret !== '' ? { githubOAuthClientSecret: oauthClientSecret.trim() } : {}),
      });
      setOauthClientSecret('');
      setCidSaved(true);
      setSettings((prev) =>
        prev
          ? {
              ...prev,
              github: {
                ...prev.github,
                oauthClientId: oauthClientId.trim(),
                oauthClientSecretSet: oauthClientSecret.trim() !== '' || prev.github.oauthClientSecretSet === true,
              },
            }
          : prev,
      );
    } catch (err) {
      setCidError(errMsg(err));
    } finally {
      setCidSaving(false);
    }
  };

  const saveVercel = async (e: FormEvent) => {
    e.preventDefault();
    setVercelSaving(true);
    setVercelSaved(false);
    setVercelError(null);
    try {
      const upd: SettingsUpdate = {};
      if (vercelToken.trim()) upd.vercelToken = vercelToken.trim();
      if (vercelClientId.trim()) upd.vercelOAuthClientId = vercelClientId.trim();
      if (vercelClientSecret.trim()) upd.vercelOAuthClientSecret = vercelClientSecret.trim();
      await api.updateSettings(upd);
      setVercelSaved(true);
      setVercelToken('');
      setVercelClientSecret('');
      reloadSettings();
      void loadVercelUser();
    } catch (err) {
      setVercelError(errMsg(err));
    } finally {
      setVercelSaving(false);
    }
  };

  const disconnectVercel = async () => {
    setVercelSaving(true);
    setVercelError(null);
    try {
      await api.updateSettings({ vercelToken: '' });
      setVercelSaved(true);
      setVercelUser(null);
      reloadSettings();
    } catch (err) {
      setVercelError(errMsg(err));
    } finally {
      setVercelSaving(false);
    }
  };

  const savePassword = async (e: FormEvent) => {
    e.preventDefault();
    setPwSaved(false);
    setPwError(null);
    if (!pw) {
      setPwError('Enter a new password.');
      return;
    }
    if (pw !== pw2) {
      setPwError('Passwords do not match.');
      return;
    }
    setPwSaving(true);
    try {
      await api.updateSettings({ password: pw });
      setPwSaved(true);
      setPw('');
      setPw2('');
    } catch (err) {
      setPwError(errMsg(err));
    } finally {
      setPwSaving(false);
    }
  };

  const saveSystemPrompt = async (e: FormEvent) => {
    e.preventDefault();
    setSysSaving(true);
    setSysSaved(false);
    setSysError(null);
    try {
      await api.updateSettings({ systemPrompt: sysPrompt.trim() });
      setSysSaved(true);
      setSettings((prev) => (prev ? { ...prev, systemPrompt: sysPrompt.trim() } : prev));
    } catch (err) {
      setSysError(errMsg(err));
    } finally {
      setSysSaving(false);
    }
  };

  const saveDefaultThinking = async (value: string) => {
    setDtSaving(true);
    setDtSaved(false);
    setDtError(null);
    try {
      await api.updateSettings({ defaultThinking: value });
      setDefThinking(value);
      setDtSaved(true);
    } catch (err) {
      setDtError(errMsg(err));
    } finally {
      setDtSaving(false);
    }
  };

  const logout = async () => {
    try {
      await api.logout();
    } catch {
      // ignore
    }
    window.location.href = '/login';
  };

  if (loadError) {
    return (
      <Center>
        <ErrorBox message={loadError} />
        <Link to="/">
          <Button variant="outline">Back to projects</Button>
        </Link>
      </Center>
    );
  }

  if (!settings) {
    return (
      <Center>
        <Spinner className="h-6 w-6" />
      </Center>
    );
  }

  const ghBadge = settings.github.tokenSet ? (
    <span className="rounded-full bg-emerald-950 px-2 py-0.5 text-xs text-emerald-400">
      connected{settings.github.source ? ` via ${settings.github.source}` : ''}
    </span>
  ) : null;

  const vercelBadge = settings.vercel.tokenSet ? (
    <span className="rounded-full bg-emerald-950 px-2 py-0.5 text-xs text-emerald-400">
      connected{settings.vercel.source ? ` via ${settings.vercel.source}` : ''}
      {vercelUser ? ` as @${vercelUser}` : ''}
    </span>
  ) : null;

  const oauthReady = settings.vercel.oauthClientId !== '' && settings.vercel.clientSecretSet;

  return (
    <div className="v1-safe-top flex h-[max(var(--v1-app-height,0px),100dvh)] flex-col overflow-hidden">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border px-3 md:h-12 md:px-5">
        <Link
          to={backTo}
          aria-label="Back to projects"
          className="inline-flex h-11 w-11 items-center justify-center rounded-lg text-dim transition-colors hover:bg-border hover:text-text md:h-9 md:w-9"
        >
          <IconArrowLeft className="h-5 w-5" />
        </Link>
        <h1 className="text-sm font-semibold text-text">Settings</h1>
        <div className="relative ml-auto w-full max-w-[16rem] sm:max-w-xs md:max-w-sm">
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') setQuery('');
              if (e.key === 'Enter' && searchResults.length > 0) goToSetting(searchResults[0]);
            }}
            placeholder="Search settings…"
            className="!py-1.5 text-sm"
          />
          {query.trim() !== '' && (
            <div className="absolute inset-x-0 top-full z-50 mt-1 overflow-hidden rounded-lg border border-border bg-bg shadow-2xl">
              {searchResults.length === 0 ? (
                <p className="px-3 py-2 text-xs text-faint">No settings found</p>
              ) : (
                searchResults.map((r) => (
                  <button
                    key={r.id}
                    type="button"
                    onClick={() => goToSetting(r)}
                    className="flex w-full items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-surface"
                  >
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm text-text">{r.label}</span>
                      <span className="block truncate text-[11px] text-faint">{r.hint}</span>
                    </span>
                    <span className="shrink-0 text-[10px] text-faint">
                      {visibleNav.find((n) => n.id === r.page)?.label}
                    </span>
                  </button>
                ))
              )}
            </div>
          )}
        </div>
      </header>

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <nav className="relative border-b border-border p-2 md:hidden" ref={ddRef}>
          <button
            type="button"
            onClick={() => setMobileOpen((o) => !o)}
            aria-haspopup="listbox"
            aria-expanded={mobileOpen}
            className="flex w-full items-center gap-2.5 rounded-lg border border-border bg-surface px-3 py-2 text-sm text-text transition-colors focus:border-subtle"
          >
            {visibleNav.find((n) => n.id === page)?.icon}
            <span className="flex-1 text-left">{visibleNav.find((n) => n.id === page)?.label}</span>
            <IconChevronDown
              className={`h-4 w-4 shrink-0 text-dim transition-transform ${
                mobileOpen ? 'rotate-180' : ''
              }`}
            />
          </button>
          {mobileOpen && (
            <div
              role="listbox"
              className="absolute inset-x-2 top-full z-40 mt-1 max-h-[60vh] overflow-y-auto rounded-xl border border-border bg-bg p-1.5 shadow-2xl"
            >
              {visibleNav.map((n) => (
                <button
                  key={n.id}
                  type="button"
                  role="option"
                  aria-selected={page === n.id}
                  onClick={() => {
                    setPage(n.id);
                    setMobileOpen(false);
                  }}
                  className={`flex w-full items-center gap-2.5 rounded-lg px-3 py-2.5 text-sm transition-colors ${
                    page === n.id
                      ? 'bg-border text-text'
                      : 'text-dim hover:bg-surface hover:text-text'
                  }`}
                >
                  {n.icon}
                  <span className="flex-1 text-left">{n.label}</span>
                  {page === n.id && <IconCheck className="h-4 w-4 shrink-0 text-accent" />}
                </button>
              ))}
            </div>
          )}
        </nav>
        <nav className="hidden w-52 shrink-0 flex-col gap-1 border-r border-border p-3 md:flex">
          {visibleNav.map((n) => (
            <button
              key={n.id}
              type="button"
              onClick={() => setPage(n.id)}
              className={navItemClass(page === n.id)}
            >
              {n.icon}
              {n.label}
            </button>
          ))}
        </nav>

        <main
          className={`flex min-h-0 flex-1 flex-col ${
            page === 'tools' ? 'overflow-hidden' : 'fade-y overflow-y-auto overscroll-contain'
          }`}
        >
          <div
            className={`mx-auto flex w-full max-w-3xl flex-col ${
              page === 'tools' ? 'min-h-0 flex-1' : 'gap-4 p-4 md:p-6'
            }`}
          >
            <div className={page === 'llm' ? 'flex flex-col gap-4' : 'hidden'}>
              <Section id="sec-llm" title="LLM" description="The OpenAI-compatible endpoint v1 uses to generate apps. The model is picked per project in the chat.">
          {editing === null ? (
            <div className="flex flex-col gap-3">
              <Button variant="primary" onClick={startAdd} className="w-fit">
                <IconPlus className="h-4 w-4" /> Add Provider
              </Button>
              <p className="text-xs text-subtle">
                Provider fields stay hidden until you add one, so saving never
                overwrites an existing provider. Add a new entry here, or pick
                Edit on a saved provider below to change it.
              </p>
            </div>
          ) : (
            <form onSubmit={(e) => void saveLLM(e)} className="flex flex-col gap-3">
              <div className="flex items-center justify-between border-b border-border pb-1.5">
                <h3 className="text-sm font-medium text-text">
                  {editing === 'new' ? 'New provider' : `Edit: ${providers.find((p) => p.id === editing)?.name ?? 'provider'}`}
                </h3>
                <Button variant="ghost" onClick={cancelEdit} className="h-8 px-2.5 text-xs">
                  Cancel
                </Button>
              </div>
              <ProviderSelector
              baseURL={baseURL}
              model={model}
              onBaseURLChange={setBaseURL}
              onModelChange={setModel}
              hideModel
              hideBrowse={editing !== null && editing !== 'new'}
              onPickProvider={(p) => setProvName(p.name)}
            >
              <Field label="Provider name (optional)">
                <Input
                  value={provName}
                  onChange={(e) => setProvName(e.target.value)}
                  placeholder="e.g. opencode"
                  autoComplete="off"
                  autoCorrect="off"
                  autoCapitalize="off"
                  spellCheck={false}
                />
              </Field>
              <Field label="API key">
                <Input
                  type="password"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  placeholder={
                    settings.llm.apiKeySet ? '•••••••• (set — enter to replace)' : 'Not set'
                  }
                  autoComplete="off"
                />
              </Field>
            </ProviderSelector>
            <SaveRow
              saving={llmSaving}
              saved={llmSaved}
              error={llmError}
              pulse={
                settings
                  ? baseURL !== settings.llm.baseURL ||
                    model !== settings.llm.model ||
                    apiKey !== '' ||
                    provName !==
                      (editing && editing !== 'new'
                        ? (providers.find((p) => p.id === editing)?.name ?? '')
                        : '')
                  : false
              }
              extra={
                <>
                  <Button variant="outline" onClick={() => void testLLM()} disabled={testing}>
                    {testing ? <Spinner className="h-4 w-4" /> : 'Test connection'}
                  </Button>
                </>
              }
            />
            {testResult &&
              (testResult.ok ? (
                <p className="flex items-center gap-1.5 text-xs text-emerald-500">
                  <IconCheck className="h-3.5 w-3.5" /> Connection successful — against{' '}
                  {testResult.baseURL.trim() ? testResult.baseURL : 'stored endpoint'}
                </p>
              ) : (
                <p className="flex items-start gap-1.5 text-xs text-red-400">
                  <IconX className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  <span>
                    {testResult.error || 'Connection failed'} — against{' '}
                    {testResult.baseURL.trim() ? testResult.baseURL : 'stored endpoint'}
                  </span>
                </p>
              ))}
          </form>
          )}

          {providers.length > 0 && (
            <div className="border-t border-border pt-3">
              <div className="mb-2 flex items-center justify-between">
                <span className="text-xs text-subtle">Saved providers</span>
                <span className="text-[11px] text-faint">{providers.length}</span>
              </div>
              {provError && <p className="mb-2 text-xs text-red-400">{provError}</p>}
              <ul className="flex flex-col gap-1.5">
                {providers.map((p) => (
                  <li
                    key={p.id}
                    className="flex items-center gap-2 rounded-lg border border-border-strong bg-bg px-3 py-2 shadow-sm"
                  >
                    <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-accent/10 text-accent">
                      <IconGlobe className="h-4 w-4" />
                    </div>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-sm text-text">{p.name}</span>
                      </div>
                      <div className="truncate font-mono text-[11px] text-faint">
                        {p.baseURL || '(no base URL)'}
                      </div>
                    </div>
                    {!p.apiKeySet && (
                      <span className="shrink-0 text-[10px] text-red-400">no key</span>
                    )}
                    <div className="flex shrink-0 items-center gap-1">
                      <button
                        type="button"
                        aria-label={`Edit provider ${p.name}`}
                        title="Edit provider"
                        disabled={provBusyId === p.id}
                        onClick={() => startEdit(p)}
                        className="inline-flex h-7 w-7 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-text disabled:opacity-40"
                      >
                        <IconPencil className="h-3.5 w-3.5" />
                      </button>
                      <button
                        type="button"
                        aria-label={`Delete provider ${p.name}`}
                        title="Delete provider"
                        disabled={provBusyId === p.id}
                        onClick={() => void deleteProvider(p.id)}
                        className="inline-flex h-7 w-7 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-red-400 disabled:opacity-40"
                      >
                        <IconX className="h-3.5 w-3.5" />
                      </button>
                    </div>
                  </li>
                ))}
              </ul>
            </div>
          )}

          <Field label="Currency for cost display">
            <select
              value={currency}
              onChange={(e) => void saveCurrency(e.target.value)}
              className="w-full rounded-lg border border-border-strong bg-surface px-3 py-2 text-sm text-text outline-none transition-colors focus:border-subtle"
            >
              {['USD', 'EUR', 'GBP', 'JPY', 'CAD', 'AUD', 'INR', 'CHF', 'CNY', 'KRW', 'SEK', 'DKK'].map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
            <p className="mt-1 text-[11px] text-subtle">
              Used to format provider-supplied cost at the end of each chat turn.
            </p>
          </Field>

          <Field label="Default model">
            <div className="relative">
              <select
                value={defaultModel}
                onChange={(e) => void saveDefaultModel(e.target.value)}
                className="w-full max-w-xs appearance-none rounded-lg border border-border-strong bg-surface px-3 py-2 pl-9 pr-9 text-sm text-text outline-none transition-colors focus:border-subtle"
              >
                {defaultModel === '' && <option value="">Select a model…</option>}
                {defaultModelOptions.map((g) => (
                  <optgroup key={g.provider} label={g.provider}>
                    {g.models.map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.name || m.id}
                      </option>
                    ))}
                  </optgroup>
                ))}
              </select>
              <IconModel className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-subtle" />
              <IconChevronDown className="pointer-events-none absolute right-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-subtle" />
            </div>
            <p className="mt-1 text-[11px] text-subtle">
              Model used by new sessions when nothing is selected yet. Populated
              automatically from your saved providers. Choosing one here does not
              change any chat you&apos;ve already opened.
            </p>
            <div className="mt-1.5 flex items-center gap-2 text-xs">
              {dmSaved && (
                <span className="flex items-center gap-1 text-emerald-500">
                  <IconCheck className="h-3.5 w-3.5" /> Saved
                </span>
              )}
              {dmError && <span className="text-red-400">{dmError}</span>}
            </div>
          </Field>
        </Section>

        <Section
          id="sec-system-prompt"
          title="Global system prompt"
          description="Extra instructions appended to the system prompt of every chat, across all projects."
        >
          <form onSubmit={(e) => void saveSystemPrompt(e)} className="flex flex-col gap-3">
            <textarea
              value={sysPrompt}
              onChange={(e) => setSysPrompt(e.target.value)}
              rows={7}
              placeholder="e.g. Always use TypeScript. Never use crypto.randomUUID() — it throws on insecure origins like the preview iframe."
              className="w-full resize-y rounded-lg border border-border-strong bg-surface px-3 py-2 text-sm text-text outline-none transition-colors focus:border-subtle"
            />
            <SaveRow
              saving={sysSaving}
              saved={sysSaved}
              error={sysError}
              pulse={settings ? sysPrompt !== settings.systemPrompt : false}
            />
          </form>
        </Section>

        <Section
          id="sec-thinking-default"
          title="Default thinking level"
          description="The thinking level used when you pick a model. Falls back to the model's lowest level when it doesn't support the chosen one; per-model selections in the chat override this. Saves automatically."
        >
          <div className="flex flex-col gap-2">
            <select
              value={defThinking}
              disabled={dtSaving}
              onChange={(e) => void saveDefaultThinking(e.target.value)}
              className="w-full min-w-0 max-w-xs rounded-lg border border-border bg-surface px-3 py-2 text-sm text-text outline-none transition-colors focus:border-subtle disabled:opacity-60"
            >
              <option value="">Lowest</option>
              <option value="off">Off</option>
              <option value="low">Low</option>
              <option value="medium">Medium</option>
              <option value="high">High</option>
              <option value="xhigh">XHigh</option>
              <option value="max">Max</option>
            </select>
            <div className="flex items-center gap-2 text-xs">
              {dtSaved && (
                <span className="flex items-center gap-1 text-emerald-500">
                  <IconCheck className="h-3.5 w-3.5" /> Saved
                </span>
              )}
              {dtError && <span className="text-red-400">{dtError}</span>}
            </div>
          </div>
        </Section>
        <Section
          id="sec-toon"
          title="TOON"
          description="Encode tool results as TOON — a compact, token-efficient format — when feeding them to the model. The chat keeps showing the original JSON either way."
        >
          <ToonControl />
        </Section>
        <Section
          id="sec-caveman"
          title="Caveman mode"
          description="Make the model reply in a terse, caveman-style way: few words, short chunky sentences, no fluff. The work still gets done — it just talks like a caveman."
        >
          <CavemanControl />
        </Section>
        <Section
          id="sec-turn-timeouts"
          title="Turn timeouts"
          description="How long a chat turn may run before it is warned (soft) or aborted (hard). Set either to 0 to disable it that side."
        >
          <TurnTimeoutControl />
        </Section>
        <Section
          id="sec-auto-push"
          title="Auto-push new projects"
          description="Whether newly created projects automatically push each finished chat turn's commit to their GitHub remote. Existing projects keep their own per-project setting and are unaffected by this."
        >
          <AutoPushControl />
        </Section>
        <Section
          id="sec-context-threshold"
          title="Context compaction"
          description="Compaction runs automatically once the conversation crosses this share of the model's context window. Also sets the threshold shown in the chat's context popup."
        >
          <ContextThresholdControl />
        </Section>
        <Section
          id="sec-clear-cache"
          title="Cached data"
          description="The provider catalog and per-model thinking levels are cached in your browser. Clear them to force a fresh fetch."
        >
          <div>
            <span className="mb-1 block text-xs text-subtle">Provider catalog &amp; thinking metadata</span>
            <ClearCacheControl />
          </div>
        </Section>
            </div>

            <div className={page === 'github' ? '' : 'hidden'}>
              <Section
          id="sec-github"
          title="GitHub"
          description="Used for repo import, create, and push."
          badge={ghBadge}
        >
          {settings.github.tokenSet && !editingGithub ? (
            /* Connected — show the connection details instead of the config
               forms, so re-entering tokens/OAuth credentials changes the
               wrong thing while a connection is live. */
            <div className="flex flex-col gap-3">
              <div className="flex items-center gap-3 rounded-lg border border-border-strong bg-surface px-3 py-2.5">
                <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-border text-sm font-semibold text-dim">
                  {(githubUser ?? '?')[0]?.toUpperCase()}
                </span>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm text-text">
                    {githubUser ? `@${githubUser}` : 'GitHub connected'}
                  </p>
                  <p className="text-[11px] text-subtle">
                    via{' '}
                    {settings.github.source === 'oauth'
                      ? 'OAuth (redirect/device flow)'
                      : settings.github.source === 'env'
                        ? 'environment token'
                        : 'personal access token'}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  className="shrink-0"
                  title="Disconnect (clears the stored GitHub token)"
                  onClick={() => void disconnectToken()}
                >
                  Disconnect
                </Button>
              </div>
              <p className="text-xs text-subtle">
                Need to change the connection?{' '}
                <button
                  type="button"
                  className="text-accent hover:underline"
                  onClick={() => setEditingGithub(true)}
                >
                  Show configuration
                </button>{' '}
                (the token stays stored until you replace it or disconnect).
              </p>
            </div>
          ) : (
          <>
          {settings.github.tokenSet && editingGithub && (
            <div className="flex items-center justify-between">
              <p className="text-xs text-subtle">Editing the connection configuration.</p>
              <Button
                variant="ghost"
                className="h-8 px-2.5 text-xs"
                onClick={() => setEditingGithub(false)}
              >
                Close configuration
              </Button>
            </div>
          )}
          <form onSubmit={(e) => void saveGitHub(e)} className="flex flex-col gap-3">
            <Field label={settings.github.source === 'oauth' ? 'GitHub OAuth token' : 'Personal access token'}>
              <Input
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder={
                  settings.github.tokenSet ? '•••••••• (set — enter to replace)' : 'ghp_…'
                }
                autoComplete="off"
              />
            </Field>
            <details className="text-xs text-subtle">
              <summary className="cursor-pointer select-none text-faint hover:text-dim">
                Required token scopes
              </summary>
              <div className="mt-2 flex flex-col gap-2">
                <div>
                  <p className="font-medium text-dim">Classic PAT</p>
                  <ul className="mt-0.5 list-inside list-disc">
                    <li><code className="font-mono text-dim">repo</code> — full repo access (read/write)</li>
                  </ul>
                </div>
                <div>
                  <p className="font-medium text-dim">Fine-grained PAT</p>
                  <ul className="mt-0.5 list-inside list-disc">
                    <li>
                      Contents: <span className="font-medium text-text">Read &amp; write</span> on your
                      repos
                    </li>
                    <li className="text-faint">
                      Fine-grained tokens can&apos;t create new repos — use a classic PAT or OAuth if
                      you want v1 to create repos for you.
                    </li>
                  </ul>
                </div>
              </div>
            </details>
            <p className="text-xs text-subtle">
              <a
                href="https://github.com/settings/tokens"
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-0.5 text-accent hover:underline"
              >
                Create a token <IconExternalLink className="h-3 w-3" />
              </a>
            </p>
            <SaveRow saving={ghSaving} saved={ghSaved} error={ghError} pulse={token !== ''} />
          </form>

          <div className="my-1 flex items-center gap-3 text-xs text-faint">
            <div className="h-px flex-1 bg-border" />
            or connect with OAuth
            <div className="h-px flex-1 bg-border" />
          </div>

          <form onSubmit={(e) => void saveClientId(e)} className="flex flex-col gap-3">
            <Field label="OAuth Client ID">
              {oauthClientIdEnv ? (
                <>
                  <div className="v1-no-scrollbar w-full overflow-x-auto whitespace-nowrap rounded-lg border border-border-strong bg-surface px-3 py-2 font-mono text-xs text-text opacity-70">
                    {oauthClientId}
                  </div>
                  <span className="mt-1 block text-[11px] text-faint">
                    Set via <code className="font-mono text-dim">V1_GITHUB_OAUTH_CLIENT_ID</code> — read-only.
                  </span>
                </>
              ) : (
                <Input
                  value={oauthClientId}
                  onChange={(e) => setOauthClientId(e.target.value)}
                  placeholder="Ov23li…"
                  autoComplete="off"
                />
              )}
            </Field>
            <Field label="OAuth Client Secret">
              {secretEnv ? (
                <>
                  <div className="v1-no-scrollbar w-full overflow-x-auto whitespace-nowrap rounded-lg border border-border-strong bg-surface px-3 py-2 font-mono text-xs text-text opacity-70">
                    {oauthClientSecretSet ? '•••••••• (set via env)' : ''}
                  </div>
                  <span className="mt-1 block text-[11px] text-faint">
                    Set via <code className="font-mono text-dim">V1_GITHUB_OAUTH_CLIENT_SECRET</code> — read-only.
                  </span>
                </>
              ) : (
                <Input
                  type="password"
                  value={oauthClientSecret}
                  onChange={(e) => setOauthClientSecret(e.target.value)}
                  placeholder={
                    oauthClientSecretSet ? '(saved — enter to replace)' : 'GitHub OAuth client secret'
                  }
                  autoComplete="new-password"
                />
              )}
            </Field>
            <p className="text-xs leading-relaxed text-subtle">
              Create an OAuth App at{' '}
              <a
                href="https://github.com/settings/developers"
                target="_blank"
                rel="noopener noreferrer"
                className="text-accent hover:underline"
              >
                github.com/settings/developers
              </a>{' '}
              — callback URL: <span className="break-all font-mono text-dim">{window.location.origin}/api/auth/github/oauth/callback</span>. Saving
              the secret enables the automatic redirect flow (no code entry); without
              it, v1 falls back to the device flow.
            </p>
            {!(oauthClientIdEnv && secretEnv) && (
            <SaveRow
              saving={cidSaving}
              saved={cidSaved}
              error={cidError}
              pulse={
                settings
                  ? oauthClientId !== settings.github.oauthClientId || oauthClientSecret !== ''
                  : false
              }
            />
          )}
          {/* Reconnect is always available when a client id exists — even when
              it comes from env — so disconnecting never strands the user. */}
          <GitHubConnect
            enabled={settings.github.oauthClientId !== ''}
            secretEnabled={settings.github.oauthClientSecretSet ?? false}
            onConnected={() => {
              setEditingGithub(false);
              reloadSettings();
            }}
          />
          </form>
          </>
          )}
        </Section>

            </div>

            <div className={page === 'vercel' ? '' : 'hidden'}>
              <Section
                id="sec-vercel"
                title="Vercel"
                description="Deploy this instance's projects to Vercel."
                badge={vercelBadge}
              >
          <form onSubmit={(e) => void saveVercel(e)} className="flex flex-col gap-3">
            <Field label={settings.vercel.tokenSet ? 'Access token (a token is set)' : 'Access token'}>
              <Input
                type="password"
                value={vercelToken}
                onChange={(e) => setVercelToken(e.target.value)}
                placeholder={
                  settings.vercel.tokenSet ? '•••••••• (set — enter to replace)' : 'vercel_…'
                }
                autoComplete="off"
              />
            </Field>
            <details className="text-xs text-subtle">
              <summary className="cursor-pointer select-none text-faint hover:text-dim">
                Required token scopes
              </summary>
              <p className="mt-1.5 leading-relaxed">
                A manual token needs the <code className="font-mono text-dim">deployment</code>{' '}
                and <code className="font-mono text-dim">user</code> scopes. The OAuth flow
                requests them automatically.
              </p>
            </details>
            <p className="text-xs text-subtle">
              <a
                href="https://vercel.com/account/tokens"
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-0.5 text-accent hover:underline"
              >
                Create a token <IconExternalLink className="h-3 w-3" />
              </a>
            </p>
            <SaveRow
              saving={vercelSaving}
              saved={vercelSaved}
              error={vercelError}
              pulse={
                settings
                  ? vercelToken !== '' ||
                    vercelClientId !== settings.vercel.oauthClientId ||
                    vercelClientSecret !== ''
                  : false
              }
              extra={
                settings.vercel.tokenSet ? (
                  <Button variant="ghost" onClick={() => void disconnectVercel()}>
                    Disconnect
                  </Button>
                ) : undefined
              }
            />
          </form>

          <div className="my-1 flex items-center gap-3 text-xs text-faint">
            <div className="h-px flex-1 bg-border" />
            or connect with OAuth
            <div className="h-px flex-1 bg-border" />
          </div>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="OAuth Client ID">
              <Input
                value={vercelClientId}
                onChange={(e) => setVercelClientId(e.target.value)}
                placeholder="Client ID"
                autoComplete="off"
              />
            </Field>
            <Field label="OAuth Client Secret">
              <Input
                type="password"
                value={vercelClientSecret}
                onChange={(e) => setVercelClientSecret(e.target.value)}
                placeholder={
                  settings.vercel.clientSecretSet
                    ? '•••••••• (set — enter to replace)'
                    : 'Client secret'
                }
                autoComplete="off"
              />
            </Field>
          </div>
          <p className="text-xs leading-relaxed text-subtle">
            Create an OAuth2 app at{' '}
            <a
              href="https://vercel.com/account/settings/oauth"
              target="_blank"
              rel="noopener noreferrer"
              className="text-accent hover:underline"
            >
              vercel.com/account/settings/oauth
            </a>{' '}
            and use this callback URL:
            <code className="mt-1 block break-all rounded-md bg-surface px-2 py-1 font-mono text-[11px] leading-relaxed text-dim">
              {window.location.origin}/api/auth/vercel/oauth/callback
            </code>
            Then save the Client ID and Secret above and hit Save. Access tokens
            expire after an hour; v1 refreshes them automatically.
          </p>
          <div className="flex items-center gap-2">
            {vercelOAuthError && (
              <p className="w-full rounded-lg border border-red-900/60 bg-red-950/40 px-3 py-2 text-xs text-red-300">
                {vercelOAuthError}
              </p>
            )}
            {oauthReady ? (
              <a
                href="/api/auth/vercel/oauth/start"
                className="inline-flex min-h-[36px] items-center justify-center gap-2 rounded-lg border border-border-strong px-3.5 text-sm font-medium text-text transition-colors hover:bg-border"
              >
                <SiVercel className="h-4 w-4" /> Connect with Vercel
              </a>
            ) : (
              <Button
                variant="outline"
                disabled
                title="Save an OAuth Client ID and secret first"
              >
                <SiVercel className="h-4 w-4" /> Connect with Vercel
              </Button>
            )}
            {vercelUserError && <span className="text-xs text-red-400">{vercelUserError}</span>}
          </div>
        </Section>
            </div>

            <div className={page === 'tools' ? 'flex min-h-0 flex-1 flex-col' : 'hidden'}>
              <ToolSettings initialTab={toolsInitialTab} />
            </div>

            <div className={page === 'appearance' ? 'flex flex-col gap-4' : 'hidden'}>
              <Section id="sec-theme" title="Theme" description="Applies instantly and is remembered.">
                <ThemePicker />
              </Section>
              <Section id="sec-chat-side" title="Chat side" description="Which side of the preview the chat pane sits on (desktop).">
                <ChatSideControl />
              </Section>
        <Section
          id="sec-chat-tabs"
          title="Chat tabs"
          description="Choose which tabs appear in the project view and their order. Drag to reorder; the eye toggles a tab on or off."
        >
          <ChatTabsControl />
        </Section>
        <Section
          id="sec-tool-calls"
          title="Tool calls"
          description="Pretty-print the JSON arguments and results of chat tool calls. The Raw/Pretty toggle on each block still overrides per call."
        >
          <div>
            <span className="mb-1 block text-xs text-subtle">Pretty-print JSON</span>
            <JsonPrettyControl />
          </div>
          <div>
            <span className="mb-1 block text-xs text-subtle">Collapse tool calls</span>
            <ToolCallsCollapseControl />
          </div>
        </Section>
        <Section
          id="sec-thinking-collapsed"
          title="Thinking blocks"
          description="Controls how the model's reasoning appears in the chat. On: thinking starts collapsed — expand a block to read it. Off: thinking stays open and streams in as the model reasons."
        >
          <div>
            <span className="mb-1 block text-xs text-subtle">Start thinking blocks collapsed</span>
            <ThinkingCollapsedControl />
          </div>
        </Section>
        <Section
          id="sec-terminal"
          title="Terminal"
          description="Font size and wrapping for the project terminal."
        >
          <TerminalSettingsControl />
        </Section>
            </div>

            <div className={page === 'notifications' ? 'flex flex-col gap-4' : 'hidden'}>
              <Section
                id="sec-notifications"
                title="Notifications"
                description="System alerts when a chat turn finishes or fails. Requires browser permission and, on iOS, the installed app."
              >
                <NotificationsControl />
              </Section>
            </div>

            <div className={page === 'auth' ? 'flex flex-col gap-4' : 'hidden'}>
              {isAdmin && (
                <Section id="sec-oidc" title="OIDC login">
                  <OidcControl />
                </Section>
              )}
              {!oidcUser && (
              <Section id="sec-auth" title="Auth">
          {settings.auth.disabled ? (
            <p className="text-sm text-dim">
              Password authentication is disabled for this instance.
            </p>
          ) : (
            <form onSubmit={(e) => void savePassword(e)} className="flex flex-col gap-3">
              <Field label="New password">
                <Input
                  type="password"
                  value={pw}
                  onChange={(e) => setPw(e.target.value)}
                  autoComplete="new-password"
                />
              </Field>
              <Field label="Confirm new password">
                <Input
                  type="password"
                  value={pw2}
                  onChange={(e) => setPw2(e.target.value)}
                  autoComplete="new-password"
                />
              </Field>
              <SaveRow
                saving={pwSaving}
                saved={pwSaved}
                error={pwError}
                pulse={pw !== '' || pw2 !== ''}
              />
              <div>
                <Button variant="ghost" onClick={() => void logout()}>
                  <IconLogout className="h-4 w-4" /> Sign out
                </Button>
              </div>
            </form>
          )}
        </Section>
              )}
            </div>

            <div
              id="sec-users"
              className={page === 'users' ? 'flex flex-col' : 'hidden'}
            >
              <UsersControl />
            </div>

            <div className={page === 'about' ? 'flex flex-col gap-4' : 'hidden'}>
              <Section title="About">
                <div className="flex flex-col gap-1">
                  <p className="text-sm text-dim">
                    v1{' '}
                    <span className="font-mono text-subtle">{settings.version || 'dev'}</span>
                  </p>
                  <p className="text-xs text-faint">
                    commit{' '}
                    <span className="font-mono text-subtle">
                      {settings.commit ? settings.commit.slice(0, 7) : 'dev'}
                    </span>
                  </p>
                  <p className="text-xs text-faint">
                    signed in as <span className="text-dim">{me ?? '…'}</span>
                    {isAdmin ? ' (admin)' : ''}
                  </p>
                </div>
              </Section>
              <Section
                id="sec-debug-hud"
                title="Debug HUD"
                description="Shows live viewport metrics in the project view. Applies on reload."
              >
                <DebugHudControl />
              </Section>
            </div>
          </div>
        </main>
      </div>
    </div>
  );
}
