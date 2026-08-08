import { memo, useCallback, useEffect, useState, type FormEvent } from 'react';
import { api } from '../api';
import type {
  InstalledSkill,
  MCPServer,
  MCPServerStatus,
  PermissionMode,
  SkillSearchResult,
} from '../types';
import { errMsg, randomId } from '../utils';
import { PERMISSION_MODES } from '../permissions';
import { Button, Field, Input, SaveRow, Spinner } from './ui';
import { IconCheck, IconExternalLink, IconX } from './icons';

const TABS = [
  { id: 'mcp', label: 'MCP servers' },
  { id: 'skills', label: 'Skills' },
  { id: 'perms', label: 'Permissions' },
] as const;
type Tab = (typeof TABS)[number]['id'];
export type ToolsTab = Tab;

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
function skillsmpHref(sk: { skillsmpUrl?: string; githubUrl: string }): string {
  return sk.skillsmpUrl || sk.githubUrl;
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

  // Skills (skillsmp)
  const [skills, setSkills] = useState<InstalledSkill[]>([]);
  const [skillQuery, setSkillQuery] = useState('');
  const [skillResults, setSkillResults] = useState<SkillSearchResult[]>([]);
  const [skillBusy, setSkillBusy] = useState(false);
  const [skillBusyId, setSkillBusyId] = useState<string | null>(null);
  const [skillError, setSkillError] = useState<string | null>(null);

  // Approval mode
  const [permissionMode, setPermissionMode] = useState<PermissionMode>(initialPermissionMode ?? 'ask');
  const [savedMode, setSavedMode] = useState<PermissionMode>(initialPermissionMode ?? 'ask');
  const [permSaving, setPermSaving] = useState(false);
  const [permSaved, setPermSaved] = useState(false);
  const [permError, setPermError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [s, st] = await Promise.all([api.getSettings(), api.mcpStatus()]);
      setServers(s.mcp ?? []);
      setSkills(s.skills ?? []);
      setPermissionMode(s.permissionMode ?? 'ask');
      setSavedMode(s.permissionMode ?? 'ask');
      const byId: Record<string, MCPServerStatus> = {};
      for (const sv of st.servers) byId[sv.id] = sv;
      setStatus(byId);
    } catch {
      // transient — keep the previous state
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    setTab(initialTab);
  }, [initialTab]);

  const parseMCP = (): MCPServer => {
    const parts = mcpCommand.trim().split(/\s+/).filter(Boolean);
    return {
      id: randomId(),
      name: mcpName.trim() || parts[0] || 'server',
      command: parts[0] ?? '',
      args: parts.slice(1),
    };
  };

  const testMCP = async () => {
    if (!mcpCommand.trim()) {
      setMcpError('Enter a command line to test.');
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
      setMcpError('Enter a command line.');
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

  const removeMCP = async (id: string) => {
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
      setSkills(r.skills);
    } catch (err) {
      setSkillError(errMsg(err));
    } finally {
      setSkillBusyId(null);
    }
  };

  const removeSkill = async (id: string) => {
    setSkillError(null);
    try {
      const r = await api.skillRemove(id);
      setSkills(r.skills);
    } catch (err) {
      setSkillError(errMsg(err));
    }
  };

  const toggleSkill = async (id: string, enabled: boolean) => {
    setSkillError(null);
    try {
      const r = await api.skillToggle(id, enabled);
      setSkills(r.skills);
    } catch (err) {
      setSkillError(errMsg(err));
    }
  };

  const savePermissionMode = async (e: FormEvent) => {
    e.preventDefault();
    setPermSaving(true);
    setPermSaved(false);
    setPermError(null);
    try {
      await api.updateSettings({ permissionMode });
      setPermSaved(true);
      setSavedMode(permissionMode);
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
          <Field label="Command line">
            <Input
              value={mcpCommand}
              onChange={(e) => setMcpCommand(e.target.value)}
              placeholder="npx -y @modelcontextprotocol/server-filesystem /tmp"
              autoComplete="off"
            />
          </Field>
        </div>
        <p className="text-xs leading-relaxed text-subtle">
          The command line is split on whitespace — the first token is the executable, the rest
          are arguments.
        </p>
        <SaveRow
          saving={mcpSaving}
          saved={false}
          error={mcpError}
          extra={
            <Button
              type="button"
              variant="outline"
              onClick={() => void testMCP()}
              disabled={mcpTesting}
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

      {servers.length > 0 && (
        <>
          <div className="my-4 border-t border-border" />
          <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            {servers.map((srv) => {
              const st = status[srv.id];
              return (
                <li key={srv.id} className="flex flex-col gap-1.5 rounded-xl border border-border p-3">
                  <div className="flex items-center gap-2">
                    <span
                      className={`h-2 w-2 shrink-0 rounded-full ${
                        st?.connected ? 'bg-emerald-500' : 'bg-border'
                      }`}
                      title={
                        st?.connected
                          ? `Connected — ${st.toolCount} tools`
                          : 'Not connected (connects on the next chat)'
                      }
                    />
                    <span className="min-w-0 flex-1 truncate text-sm font-medium text-text">
                      {srv.name}
                    </span>
                    {st?.connected && (
                      <span className="shrink-0 rounded-full bg-emerald-950 px-1.5 py-0.5 text-[10px] text-emerald-400">
                        {st.toolCount} tools
                      </span>
                    )}
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
                    {srv.command} {srv.args.join(' ')}
                  </div>
                  {!st?.connected && (
                    <span className="text-[10px] text-faint">
                      Not connected — connects on the next chat
                    </span>
                  )}
                </li>
              );
            })}
          </ul>
        </>
      )}
    </div>
  );

  const skillsSection = (
    <div className="flex h-full min-h-0 min-w-0 flex-col gap-3">
      <form onSubmit={(e) => void searchSkills(e)} className="flex shrink-0 min-w-0 items-end gap-2">
        <div className="flex-1">
          <Field label="Search SkillsMP">
            <Input
              value={skillQuery}
              onChange={(e) => setSkillQuery(e.target.value)}
              placeholder="e.g. react, security, postgres"
              autoComplete="off"
            />
          </Field>
        </div>
        <Button type="submit" variant="outline" disabled={skillBusy} className="h-[42px] sm:h-[38px]">
          {skillBusy ? <Spinner className="h-4 w-4" /> : 'Search'}
        </Button>
      </form>

      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-x-hidden overflow-y-auto overscroll-contain">
        {skillResults.length > 0 && (
          <>
            <div className="border-t border-border" />
            <ul className="min-w-0 flex flex-col gap-2">
              {skillResults.map((sk) => (
                <li
                  key={sk.id}
                  className="flex items-center gap-2 rounded-xl border border-border px-3 py-2"
                >
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm text-text">{sk.name}</div>
                  <div className="truncate text-[11px] text-faint">
                    {sk.author}
                    {sk.description ? ` · ${sk.description}` : ''}
                  </div>
                </div>
                <ViewOnSkillsMP href={skillsmpHref(sk)} name={sk.name} />
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
          </>
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
                  className="flex items-center gap-2 rounded-lg border border-border px-3 py-2"
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
                  <div className="min-w-0 flex-1">
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
                  </div>
                  <ViewOnSkillsMP href={skillsmpHref(sk)} name={sk.name} />
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
      <SaveRow
        saving={permSaving}
        saved={permSaved}
        error={permError}
        pulse={permissionMode !== savedMode}
      />
    </form>
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="shrink-0 z-10 flex gap-0.5 border-b border-border bg-bg">
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => setTab(t.id)}
            className={`-mb-px flex h-9 flex-1 items-center justify-center gap-1.5 border-b-2 px-2 text-sm transition-colors ${
              tab === t.id
                ? 'border-accent text-text'
                : 'border-transparent text-subtle hover:text-text'
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>
      <div className="min-h-0 min-w-0 flex-1 overflow-x-hidden overflow-y-auto px-3 pt-2 pb-2 overscroll-contain">
        {tab === 'mcp' && mcpSection}
        {tab === 'skills' && <div className="flex h-full min-h-0 flex-col">{skillsSection}</div>}
        {tab === 'perms' && permsSection}
      </div>
    </div>
  );
}

export default memo(ToolSettings);
