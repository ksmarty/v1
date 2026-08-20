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
import { Button, Dialog, Field, Input, SaveRow, Spinner } from './ui';
import Markdown from './Markdown';
import { IconCheck, IconExternalLink, IconFlask, IconPencil, IconX } from './icons';

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
  useEffect(() => {
    if (!target.installed) return;
    setReadmeLoading(true);
    api
      .skillReadme(target.installed.id)
      .then((r) => setReadme(r.content))
      .catch(() => setReadme(null))
      .finally(() => setReadmeLoading(false));
  }, [target.installed]);

  return (
    <Dialog open onClose={onClose} title={target.name} wide fullScreen fixedBody align="top">
      <div className="flex h-full min-h-0 flex-col gap-3">
        <p className="text-xs text-subtle">by {target.author}</p>
        {target.description && <p className="text-sm text-text">{target.description}</p>}
        {readmeLoading && (
          <div className="flex justify-center py-4">
            <Spinner className="h-4 w-4" />
          </div>
        )}
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

  // Skills (skillsmp)
  const [skills, setSkills] = useState<InstalledSkill[]>([]);
  const [skillQuery, setSkillQuery] = useState('');
  const [skillResults, setSkillResults] = useState<SkillSearchResult[]>([]);
  const [suggestedSkills, setSuggestedSkills] = useState<SkillSearchResult[]>([]);
  const [skillBusy, setSkillBusy] = useState(false);
  const [skillBusyId, setSkillBusyId] = useState<string | null>(null);
  const [skillError, setSkillError] = useState<string | null>(null);
  const [skillPreview, setSkillPreview] = useState<SkillPreviewTarget | null>(null);

  // Approval mode
  const [permissionMode, setPermissionMode] = useState<PermissionMode>(initialPermissionMode ?? 'ask');
  const [savedMode, setSavedMode] = useState<PermissionMode>(initialPermissionMode ?? 'ask');
  const [permSaving, setPermSaving] = useState(false);
  const [permSaved, setPermSaved] = useState(false);
  const [permError, setPermError] = useState<string | null>(null);
  // Rewind approval (deleting chat history)
  const [rewindApproval, setRewindApproval] = useState(false);
  const [savedRewind, setSavedRewind] = useState(false);

  const load = useCallback(async () => {
    try {
      const [s, st] = await Promise.all([api.getSettings(), api.mcpStatus()]);
      setServers((s.mcp ?? []).map((sv) => ({ ...sv, enabled: sv.enabled !== false })));
      setSkills(s.skills ?? []);
      setPermissionMode(s.permissionMode ?? 'ask');
      setSavedMode(s.permissionMode ?? 'ask');
      setRewindApproval(s.rewindApproval ?? false);
      setSavedRewind(s.rewindApproval ?? false);
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

  const parseMCP = (): MCPServer => {
    const parts = mcpCommand.trim().split(/\s+/).filter(Boolean);
    return {
      id: randomId(),
      name: mcpName.trim() || parts[0] || 'server',
      command: parts[0] ?? '',
      args: parts.slice(1),
      enabled: true,
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
    setEditCommand([srv.command, ...srv.args].join(' '));
    setEditError(null);
    setCardTest(null);
  };

  const saveEdit = async (srv: MCPServer) => {
    if (!editCommand.trim()) {
      setEditError('Enter a command line.');
      return;
    }
    setEditSaving(true);
    setEditError(null);
    try {
      const parts = editCommand.trim().split(/\s+/).filter(Boolean);
      const updated = servers.map((s) =>
        s.id === srv.id
          ? {
              ...s,
              name: editName.trim() || parts[0] || s.name,
              command: parts[0] ?? '',
              args: parts.slice(1),
            }
          : s,
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
      setSkills(r.skills);
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
      setSkills(r.skills);
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
      setSkills(r.skills);
    } catch (err) {
      setSkills(prev);
      setSkillError(errMsg(err));
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
          <Field label="Command line">
            <Input
              value={mcpCommand}
              onChange={(e) => setMcpCommand(e.target.value)}
              placeholder="npx -y @modelcontextprotocol/server-filesystem /tmp"
              autoComplete="off"
              autoCorrect="off"
              autoCapitalize="off"
              spellCheck={false}
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

      {servers.length > 0 && (
        <>
          <div className="my-4 border-t border-border" />
          <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            {servers.map((srv) => {
              const st = status[srv.id];
              const enabled = srv.enabled !== false;
              return (
                <li key={srv.id} className="flex flex-col gap-1.5 rounded-xl border border-border bg-surface p-3">
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
                    {srv.command} {srv.args.join(' ')}
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
                  className="flex items-center gap-2 rounded-xl border border-border bg-surface/50 px-3 py-2"
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
                className="flex items-center gap-2 rounded-xl border border-border bg-surface px-3 py-2"
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
                  className="flex items-center gap-2 rounded-lg border border-border bg-surface px-3 py-2"
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
      <div className="fade-y min-h-0 min-w-0 flex-1 overflow-x-hidden overflow-y-auto px-3 pt-2 pb-2 overscroll-contain">
        {tab === 'mcp' && mcpSection}
        {tab === 'skills' && <div className="flex h-full min-h-0 flex-col">{skillsSection}</div>}
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
