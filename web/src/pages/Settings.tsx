import { useEffect, useState, type FormEvent } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { api } from '../api';
import type { Settings as SettingsType } from '../types';
import { errMsg, getChatSide, setChatSide, type ChatSide } from '../utils';
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
  IconArrowLeft,
  IconCheck,
  IconDots,
  IconExternalLink,
  IconGitHub,
  IconLock,
  IconLogout,
  IconModel,
  IconSettings,
  IconWrench,
  IconX,
} from '../components/icons';
import ProviderSelector from '../components/ProviderSelector';
import GitHubConnect from '../components/GitHubConnect';
import ToolSettings from '../components/ToolSettings';

const NAV = [
  { id: 'llm', label: 'LLM & providers', icon: <IconModel className="h-4 w-4" /> },
  { id: 'github', label: 'GitHub', icon: <IconGitHub className="h-4 w-4" /> },
  { id: 'tools', label: 'Tools & permissions', icon: <IconWrench className="h-4 w-4" /> },
  { id: 'appearance', label: 'Appearance', icon: <IconSettings className="h-4 w-4" /> },
  { id: 'auth', label: 'Auth', icon: <IconLock className="h-4 w-4" /> },
  { id: 'about', label: 'About', icon: <IconDots className="h-4 w-4" /> },
] as const;
type NavId = (typeof NAV)[number]['id'];

const navItemClass = (active: boolean) =>
  `flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-sm transition-colors ${
    active ? 'bg-border text-text' : 'text-subtle hover:bg-border/60 hover:text-text'
  }`;

function ThemePicker() {
  const [selected, setSelected] = useState<string>(() => getStoredTheme());
  const options = listThemeOptions();

  const choose = (name: string) => {
    setSelected(name);
    setStoredTheme(name);
    applyTheme(name);
  };

  return (
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
            <span className="mb-2 flex h-4 overflow-hidden rounded">
              {t.swatches.map((c, i) => (
                <span key={i} className="h-full flex-1" style={{ backgroundColor: c }} />
              ))}
            </span>
            <span className={`block truncate text-xs ${active ? 'text-text' : 'text-dim'}`}>
              {t.name}
            </span>
          </button>
        );
      })}
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
  const [activeProviderId, setActiveProviderId] = useState('');
  const [provName, setProvName] = useState('');
  const [provBusyId, setProvBusyId] = useState<string | null>(null);
  const [provSaving, setProvSaving] = useState(false);
  const [provSaved, setProvSaved] = useState(false);
  const [provError, setProvError] = useState<string | null>(null);

  // GitHub PAT
  const [token, setToken] = useState('');
  const [ghSaving, setGhSaving] = useState(false);
  const [ghSaved, setGhSaved] = useState(false);
  const [ghError, setGhError] = useState<string | null>(null);

  // GitHub OAuth client ID
  const [oauthClientId, setOauthClientId] = useState('');
  const [cidSaving, setCidSaving] = useState(false);
  const [cidSaved, setCidSaved] = useState(false);
  const [cidError, setCidError] = useState<string | null>(null);

  // Auth
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [pwSaving, setPwSaving] = useState(false);
  const [pwSaved, setPwSaved] = useState(false);
  const [pwError, setPwError] = useState<string | null>(null);

  const location = useLocation();
  const from = (location.state as { from?: string } | null)?.from;
  const backTo = from && from.startsWith('/') ? from : '/';
  const [page, setPage] = useState<NavId>('llm');

  const reloadSettings = () => {
    api
      .getSettings()
      .then((s) => {
        setSettings(s);
        setBaseURL(s.llm.baseURL);
        setModel(s.llm.model);
        setOauthClientId(s.github.oauthClientId);
        setProviders(s.llm.providers ?? []);
        setActiveProviderId(s.llm.activeProviderId ?? '');
      })
      .catch((e) => setLoadError(errMsg(e)));
  };

  useEffect(() => {
    reloadSettings();
  }, []);

  const saveLLM = async (e: FormEvent) => {
    e.preventDefault();
    setLlmSaving(true);
    setLlmSaved(false);
    setLlmError(null);
    try {
      const active = providers.find((p) => p.id === activeProviderId) ?? null;
      if (providers.length === 0) {
        // No saved providers yet — plain single-configuration save.
        await api.updateSettings({
          llm: { baseURL, model, ...(apiKey ? { apiKey } : {}) },
        });
      } else if (active) {
        // Save into the active provider and keep it active.
        const list = providers.map((p) =>
          p.id === active.id
            ? { id: p.id, name: p.name, baseURL, model, ...(apiKey ? { apiKey } : {}) }
            : { id: p.id, name: p.name, baseURL: p.baseURL, model: p.model },
        );
        await api.updateSettings({ llm: { providers: list, activeProviderId: active.id } });
      } else {
        // Providers exist but none is active — create one from the form.
        const newId = crypto.randomUUID();
        let host = '';
        try {
          host = new URL(baseURL).hostname;
        } catch {
          host = '';
        }
        const name = provName.trim() || host || 'Provider';
        const list = providers.map((p) => ({
          id: p.id,
          name: p.name,
          baseURL: p.baseURL,
          model: p.model,
        }));
        list.push({ id: newId, name, baseURL, model, ...(apiKey ? { apiKey } : {}) });
        await api.updateSettings({ llm: { providers: list, activeProviderId: newId } });
      }
      setLlmSaved(true);
      setApiKey('');
      reloadSettings();
    } catch (err) {
      setLlmError(errMsg(err));
    } finally {
      setLlmSaving(false);
    }
  };

  const saveAsProvider = async (e: FormEvent) => {
    e.preventDefault();
    const name = provName.trim();
    if (!name) {
      setProvError('Enter a provider name.');
      return;
    }
    setProvSaving(true);
    setProvSaved(false);
    setProvError(null);
    try {
      const newId = crypto.randomUUID();
      const list = providers.map((p) => ({
        id: p.id,
        name: p.name,
        baseURL: p.baseURL,
        model: p.model,
      }));
      list.push({ id: newId, name, baseURL, model, ...(apiKey ? { apiKey } : {}) });
      await api.updateSettings({ llm: { providers: list, activeProviderId: newId } });
      setProvSaved(true);
      setProvName('');
      setApiKey('');
      reloadSettings();
    } catch (err) {
      setProvError(errMsg(err));
    } finally {
      setProvSaving(false);
    }
  };

  const useProvider = async (id: string) => {
    setProvBusyId(id);
    setProvError(null);
    try {
      await api.updateSettings({ llm: { activeProviderId: id } });
      reloadSettings();
    } catch (err) {
      setProvError(errMsg(err));
    } finally {
      setProvBusyId(null);
    }
  };

  const deleteProvider = async (id: string) => {
    setProvBusyId(id);
    setProvError(null);
    try {
      const rest = providers.filter((p) => p.id !== id);
      const nextActive =
        rest.length > 0 ? (activeProviderId === id ? rest[0].id : activeProviderId) : '';
      await api.updateSettings({
        llm: {
          providers: rest.map((p) => ({ id: p.id, name: p.name, baseURL: p.baseURL, model: p.model })),
          activeProviderId: nextActive,
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
      setSettings((prev) =>
        prev ? { ...prev, github: { ...prev.github, tokenSet: true, source: 'pat' } } : prev,
      );
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
      await api.updateSettings({ githubOAuthClientId: oauthClientId.trim() });
      setCidSaved(true);
      setSettings((prev) =>
        prev
          ? { ...prev, github: { ...prev.github, oauthClientId: oauthClientId.trim() } }
          : prev,
      );
    } catch (err) {
      setCidError(errMsg(err));
    } finally {
      setCidSaving(false);
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

  return (
    <div className="flex min-h-dvh flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border px-3 md:h-12 md:px-5">
        <Link
          to={backTo}
          aria-label="Back to projects"
          className="inline-flex h-11 w-11 items-center justify-center rounded-lg text-dim transition-colors hover:bg-border hover:text-text md:h-9 md:w-9"
        >
          <IconArrowLeft className="h-5 w-5" />
        </Link>
        <h1 className="text-sm font-semibold text-text">Settings</h1>
      </header>

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <nav className="flex shrink-0 gap-1 overflow-x-auto border-b border-border px-2 py-1.5 md:hidden">
          {NAV.map((n) => (
            <button
              key={n.id}
              type="button"
              onClick={() => setPage(n.id)}
              className={`shrink-0 ${navItemClass(page === n.id)}`}
            >
              {n.icon}
              {n.label}
            </button>
          ))}
        </nav>
        <nav className="hidden w-52 shrink-0 flex-col gap-1 border-r border-border p-3 md:flex">
          {NAV.map((n) => (
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

        <main className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto flex w-full max-w-3xl flex-col gap-4 p-4 md:p-6">
            <div className={page === 'llm' ? '' : 'hidden'}>
              <Section title="LLM" description="The OpenAI-compatible endpoint v1 uses to generate apps. The model is picked per project in the chat.">
          <form onSubmit={(e) => void saveLLM(e)} className="flex flex-col gap-3">
            <ProviderSelector
              baseURL={baseURL}
              model={model}
              onBaseURLChange={setBaseURL}
              onModelChange={setModel}
              hideModel
            >
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
              extra={
                <Button variant="outline" onClick={() => void testLLM()} disabled={testing}>
                  {testing ? <Spinner className="h-4 w-4" /> : 'Test connection'}
                </Button>
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

          {providers.length > 0 && (
            <div className="border-t border-border pt-3">
              <div className="mb-2 flex items-center justify-between">
                <span className="text-xs text-subtle">Saved providers</span>
                <span className="text-[11px] text-faint">{providers.length}</span>
              </div>
              <ul className="flex flex-col gap-1.5">
                {providers.map((p) => (
                  <li
                    key={p.id}
                    className={`flex items-center gap-2 rounded-lg border border-border px-3 py-2 ${
                      p.id === activeProviderId ? 'bg-surface' : ''
                    }`}
                  >
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-sm text-text">{p.name}</span>
                        {p.id === activeProviderId && (
                          <span className="rounded-full bg-emerald-950 px-1.5 py-0.5 text-[10px] text-emerald-400">
                            active
                          </span>
                        )}
                      </div>
                      <div className="truncate font-mono text-[11px] text-faint">
                        {p.baseURL || '(no base URL)'}
                      </div>
                    </div>
                    {!p.apiKeySet && (
                      <span className="shrink-0 text-[10px] text-red-400">no key</span>
                    )}
                    <div className="flex shrink-0 items-center gap-1">
                      {p.id !== activeProviderId && (
                        <Button
                          variant="outline"
                          className="h-7 px-2 text-xs"
                          disabled={provBusyId === p.id}
                          onClick={() => void useProvider(p.id)}
                        >
                          {provBusyId === p.id ? (
                            <Spinner className="h-3.5 w-3.5" />
                          ) : (
                            'Use'
                          )}
                        </Button>
                      )}
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

          <form onSubmit={(e) => void saveAsProvider(e)} className="flex items-end gap-2">
            <div className="flex-1">
              <Field label="Save current config as provider">
                <Input
                  value={provName}
                  onChange={(e) => setProvName(e.target.value)}
                  placeholder="e.g. opencode"
                  autoComplete="off"
                />
              </Field>
            </div>
            <Button type="submit" variant="outline" disabled={provSaving}>
              {provSaving ? <Spinner className="h-4 w-4" /> : 'Save as provider'}
            </Button>
          </form>
          {(provError || provSaved) && (
            <div className="flex items-center gap-2 text-xs">
              {provSaved && (
                <span className="flex items-center gap-1 text-emerald-500">
                  <IconCheck className="h-3.5 w-3.5" /> Provider saved
                </span>
              )}
              {provError && <span className="text-red-400">{provError}</span>}
            </div>
          )}
        </Section>
            </div>

            <div className={page === 'github' ? '' : 'hidden'}>
              <Section
          title="GitHub"
          description="Used for repo import, create, and push."
          badge={ghBadge}
        >
          <form onSubmit={(e) => void saveGitHub(e)} className="flex flex-col gap-3">
            <Field label={settings.github.tokenSet ? 'Personal access token (a token is set)' : 'Personal access token'}>
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
              <p className="mt-1.5 leading-relaxed">
                Classic PAT: needs the <code className="font-mono text-dim">repo</code> scope.
                Fine-grained PAT: Contents: Read &amp; write on your repos — but fine-grained
                tokens can&apos;t create new repos, so use a classic PAT or OAuth below if you
                want v1 to create repos for you.
              </p>
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
            <SaveRow saving={ghSaving} saved={ghSaved} error={ghError} />
          </form>

          <div className="my-1 flex items-center gap-3 text-xs text-faint">
            <div className="h-px flex-1 bg-border" />
            or connect with OAuth
            <div className="h-px flex-1 bg-border" />
          </div>

          <form onSubmit={(e) => void saveClientId(e)} className="flex flex-col gap-3">
            <Field label="OAuth Client ID">
              <Input
                value={oauthClientId}
                onChange={(e) => setOauthClientId(e.target.value)}
                placeholder="Ov23li…"
                autoComplete="off"
              />
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
              (any homepage/callback URL), enable Device Flow, paste the Client ID here. Save an
              empty value to clear it.
            </p>
            <SaveRow
              saving={cidSaving}
              saved={cidSaved}
              error={cidError}
              extra={
                <GitHubConnect
                  enabled={settings.github.oauthClientId !== ''}
                  onConnected={reloadSettings}
                />
              }
            />
          </form>
        </Section>

            </div>

            <div className={page === 'tools' ? '' : 'hidden'}>
              <ToolSettings variant="stacked" />
            </div>

            <div className={page === 'appearance' ? '' : 'hidden'}>
              <Section title="Appearance" description="Theme applies instantly and is remembered.">
          <div>
            <span className="mb-1 block text-xs text-subtle">Chat side (desktop)</span>
            <ChatSideControl />
          </div>
          <div>
            <span className="mb-2 block text-xs text-subtle">Theme</span>
            <ThemePicker />
          </div>
        </Section>
            </div>

            <div className={page === 'auth' ? '' : 'hidden'}>
              <Section title="Auth">
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
              <SaveRow saving={pwSaving} saved={pwSaved} error={pwError} />
              <div>
                <Button variant="ghost" onClick={() => void logout()}>
                  <IconLogout className="h-4 w-4" /> Sign out
                </Button>
              </div>
            </form>
          )}
        </Section>
            </div>

            <div className={page === 'about' ? '' : 'hidden'}>
              <Section title="About">
          <p className="text-sm text-dim">
            v1 <span className="font-mono text-subtle">{settings.version || 'dev'}</span>
          </p>
        </Section>
            </div>
          </div>
        </main>
      </div>
    </div>
  );
}
