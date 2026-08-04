import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import type { Settings as SettingsType } from '../types';
import { errMsg, getChatSide, setChatSide, type ChatSide } from '../utils';
import {
  applyTheme,
  getStoredTheme,
  listThemeOptions,
  setStoredTheme,
} from '../themes';
import { Button, Center, ErrorBox, Input, Spinner } from '../components/ui';
import {
  IconArrowLeft,
  IconCheck,
  IconExternalLink,
  IconLogout,
  IconX,
} from '../components/icons';
import ProviderSelector from '../components/ProviderSelector';
import GitHubConnect from '../components/GitHubConnect';

function Section({
  title,
  description,
  badge,
  children,
}: {
  title: string;
  description?: string;
  badge?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="rounded-xl border border-border bg-bg p-4 md:p-5">
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-semibold text-text">{title}</h2>
        {badge}
      </div>
      {description && <p className="mt-1 text-xs text-subtle">{description}</p>}
      <div className="mt-4 flex flex-col gap-3">{children}</div>
    </section>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs text-subtle">{label}</span>
      {children}
    </label>
  );
}

function SaveRow({
  saving,
  saved,
  error,
  extra,
}: {
  saving: boolean;
  saved: boolean;
  error: string | null;
  extra?: ReactNode;
}) {
  return (
    <div className="flex items-center gap-2">
      <Button type="submit" variant="outline" disabled={saving}>
        {saving ? <Spinner className="h-4 w-4" /> : 'Save'}
      </Button>
      {extra}
      {saved && !saving && (
        <span className="flex items-center gap-1 text-xs text-emerald-500">
          <IconCheck className="h-3.5 w-3.5" /> Saved
        </span>
      )}
      {error && <span className="text-xs text-red-400">{error}</span>}
    </div>
  );
}

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

  const reloadSettings = () => {
    api
      .getSettings()
      .then((s) => {
        setSettings(s);
        setBaseURL(s.llm.baseURL);
        setModel(s.llm.model);
        setOauthClientId(s.github.oauthClientId);
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
      await api.updateSettings({
        llm: {
          baseURL,
          model,
          ...(apiKey ? { apiKey } : {}),
        },
      });
      setLlmSaved(true);
      setApiKey('');
      setSettings((prev) =>
        prev
          ? {
              ...prev,
              llm: { ...prev.llm, baseURL, model, apiKeySet: prev.llm.apiKeySet || !!apiKey },
            }
          : prev,
      );
    } catch (err) {
      setLlmError(errMsg(err));
    } finally {
      setLlmSaving(false);
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
          to="/"
          aria-label="Back to projects"
          className="inline-flex h-11 w-11 items-center justify-center rounded-lg text-dim transition-colors hover:bg-border hover:text-text md:h-9 md:w-9"
        >
          <IconArrowLeft className="h-5 w-5" />
        </Link>
        <h1 className="text-sm font-semibold text-text">Settings</h1>
      </header>

      <main className="mx-auto flex w-full max-w-2xl flex-1 flex-col gap-4 p-4 md:p-6">
        <Section title="LLM" description="The OpenAI-compatible endpoint v1 uses to generate apps.">
          <form onSubmit={(e) => void saveLLM(e)} className="flex flex-col gap-3">
            <ProviderSelector
              baseURL={baseURL}
              model={model}
              onBaseURLChange={setBaseURL}
              onModelChange={setModel}
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
        </Section>

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

        <Section title="About">
          <p className="text-sm text-dim">
            v1 <span className="font-mono text-subtle">{settings.version || 'dev'}</span>
          </p>
        </Section>
      </main>
    </div>
  );
}
