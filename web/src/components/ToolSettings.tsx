import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { api } from '../api';
import type { InstalledSkill, MCPServer, MCPServerStatus, SkillSearchResult } from '../types';
import { errMsg } from '../utils';
import { Button, Field, Input, SaveRow, Section, Spinner } from './ui';
import { IconCheck, IconX } from './icons';

/**
 * MCP servers, installed skills, and tool permission policy. Shared by the
 * Settings page and the chat's quick-access tools dialog so both stay in sync.
 */
export default function ToolSettings() {
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

  // Tool permissions
  const [policy, setPolicy] = useState<Record<string, string>>({});
  const [newToolKey, setNewToolKey] = useState('');
  const [policySaving, setPolicySaving] = useState(false);
  const [policySaved, setPolicySaved] = useState(false);
  const [policyError, setPolicyError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [s, st] = await Promise.all([api.getSettings(), api.mcpStatus()]);
      setServers(s.mcp ?? []);
      setSkills(s.skills ?? []);
      setPolicy(s.toolPolicy ?? {});
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

  const parseMCP = (): MCPServer => {
    const parts = mcpCommand.trim().split(/\s+/).filter(Boolean);
    return {
      id: crypto.randomUUID(),
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

  const setPolicyEntry = (tool: string, value: string) => {
    setPolicy((prev) => {
      const next = { ...prev };
      if (value === 'allow') delete next[tool]; // allow is the default — drop the entry
      else next[tool] = value;
      return next;
    });
  };

  const addPolicyKey = () => {
    const key = newToolKey.trim();
    if (!key) return;
    setPolicy((prev) => (prev[key] ? prev : { ...prev, [key]: 'ask' }));
    setNewToolKey('');
  };

  const savePolicy = async (e: FormEvent) => {
    e.preventDefault();
    setPolicySaving(true);
    setPolicySaved(false);
    setPolicyError(null);
    try {
      await api.updateSettings({ toolPolicy: policy });
      setPolicySaved(true);
      await load();
    } catch (err) {
      setPolicyError(errMsg(err));
    } finally {
      setPolicySaving(false);
    }
  };

  const policyKeys = ['run_command', 'mcp.*', '*'];
  for (const k of Object.keys(policy)) {
    if (!policyKeys.includes(k)) policyKeys.push(k);
  }

  return (
    <>
      <Section
        title="MCP servers"
        description="Model Context Protocol servers expose extra tools to the agent. Each server is connected on demand when a chat starts; remove and re-add to change a config."
      >
        {servers.length > 0 && (
          <ul className="flex flex-col gap-1.5">
            {servers.map((srv) => {
              const st = status[srv.id];
              return (
                <li
                  key={srv.id}
                  className="flex items-center gap-2 rounded-lg border border-border px-3 py-2"
                >
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
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-sm text-text">{srv.name}</span>
                      {st?.connected && (
                        <span className="rounded-full bg-emerald-950 px-1.5 py-0.5 text-[10px] text-emerald-400">
                          {st.toolCount} tools
                        </span>
                      )}
                    </div>
                    <div className="truncate font-mono text-[11px] text-faint">
                      {srv.command} {srv.args.join(' ')}
                    </div>
                  </div>
                  <button
                    type="button"
                    aria-label={`Remove MCP server ${srv.name}`}
                    title="Remove server"
                    onClick={() => void removeMCP(srv.id)}
                    className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-dim transition-colors hover:bg-border hover:text-red-400"
                  >
                    <IconX className="h-3.5 w-3.5" />
                  </button>
                </li>
              );
            })}
          </ul>
        )}

        <form onSubmit={(e) => void addMCP(e)} className="flex flex-col gap-3">
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
          <p className="text-xs leading-relaxed text-subtle">
            The command line is split on whitespace — the first token is the executable, the
            rest are arguments.
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
                      {mcpTestResult.tools.length > 5
                        ? ` +${mcpTestResult.tools.length - 5} more`
                        : ''}
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
      </Section>

      <Section
        title="Skills"
        description="Install agent skills from the SkillsMP marketplace. Enabled skills are added to the agent's instructions."
      >
        <form onSubmit={(e) => void searchSkills(e)} className="flex items-end gap-2">
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
          <Button type="submit" variant="outline" disabled={skillBusy}>
            {skillBusy ? <Spinner className="h-4 w-4" /> : 'Search'}
          </Button>
        </form>

        {skillResults.length > 0 && (
          <ul className="flex flex-col gap-1.5">
            {skillResults.map((sk) => (
              <li
                key={sk.id}
                className="flex items-center gap-2 rounded-lg border border-border px-3 py-2"
              >
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm text-text">{sk.name}</div>
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
        )}

        {skillError && <p className="text-xs text-red-400">{skillError}</p>}

        {skills.length > 0 && (
          <div className="border-t border-border pt-3">
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
      </Section>

      <Section
        title="Tool permissions"
        description="Controls which tools the agent may run without asking. 'Ask' pauses the chat and prompts you to allow or deny."
      >
        <form onSubmit={(e) => void savePolicy(e)} className="flex flex-col gap-2">
          {policyKeys.map((tool) => (
            <div
              key={tool}
              className="flex items-center gap-2 rounded-lg border border-border px-3 py-2"
            >
              <code className="min-w-0 flex-1 truncate font-mono text-xs text-text">{tool}</code>
              <select
                value={policy[tool] ?? 'allow'}
                onChange={(e) => setPolicyEntry(tool, e.target.value)}
                className="h-8 shrink-0 rounded-md border border-border bg-surface px-2 text-xs text-text outline-none focus:border-accent"
              >
                <option value="allow">Allow</option>
                <option value="ask">Ask</option>
                <option value="deny">Deny</option>
              </select>
            </div>
          ))}
          <div className="flex items-end gap-2">
            <div className="flex-1">
              <Field label="Add tool key">
                <Input
                  value={newToolKey}
                  onChange={(e) => setNewToolKey(e.target.value)}
                  placeholder="e.g. mcp.filesystem.read_file"
                  autoComplete="off"
                />
              </Field>
            </div>
            <Button type="button" variant="outline" onClick={addPolicyKey}>
              Add
            </Button>
          </div>
          <SaveRow saving={policySaving} saved={policySaved} error={policyError} />
          <p className="text-xs leading-relaxed text-subtle">
            Keys: <code className="font-mono">run_command</code> for shell commands,{' '}
            <code className="font-mono">mcp.&lt;server&gt;.&lt;tool&gt;</code> for individual MCP
            tools, <code className="font-mono">mcp.*</code> for all MCP tools, and{' '}
            <code className="font-mono">*</code> for everything. The default is Allow.
          </p>
        </form>
      </Section>
    </>
  );
}
