import type {
  AuthStatus,
  ChatEvent,
  ChatMessage,
  ChatSession,
  ContextUsage,
  DeviceFlowPoll,
  DeviceFlowStart,
  FileEntry,
  GitHubRepo,
  GitStatus,
  GitInfo,
  InstalledSkill,
  MCPServer,
  MCPServerStatus,
  PermissionMode,
  PreviewStatus,
  Project,
  Provider,
  ProviderAddResult,
  ProvidersRefreshResult,
  ProvidersResponse,
  ProvidersSearchResult,
  PushResult,
  Settings,
  SkillSearchResult,
  Memory,
  Todo,
  UserInfo,
  VercelDeploymentsResponse,
  VercelUserInfo,
} from './types';

export class ApiError extends Error {
  status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

// ---- client-side caches ----

const PROVIDERS_CACHE_KEY = 'v1.providersCache';
const PROVIDERS_CACHE_AT_KEY = 'v1.providersCacheAt';
const PROVIDERS_CACHE_TTL = 6 * 60 * 60 * 1000; // 6 hours

let providersCache: { data: Provider[]; at: number } | null = null;

function readProvidersCache(): { data: Provider[]; at: number } | null {
  if (providersCache) return providersCache;
  try {
    const at = Number(localStorage.getItem(PROVIDERS_CACHE_AT_KEY) ?? '0');
    const raw = localStorage.getItem(PROVIDERS_CACHE_KEY);
    if (!raw || !at) return null;
    return { data: JSON.parse(raw) as Provider[], at };
  } catch {
    return null;
  }
}

function writeProvidersCache(data: Provider[]): void {
  providersCache = { data, at: Date.now() };
  try {
    localStorage.setItem(PROVIDERS_CACHE_KEY, JSON.stringify(data));
    localStorage.setItem(PROVIDERS_CACHE_AT_KEY, String(Date.now()));
  } catch {
    // storage unavailable — in-memory only
  }
}

const CTX_USAGE_TTL = 10 * 1000;

let ctxUsageCache: { key: string; at: number; data: ContextUsage } | null = null;

// Storage key for the persisted thinking-metadata cache (ChatPane). Kept
// here so clearClientCaches can drop it.
export const THINKING_META_KEY = 'v1.thinkingMeta';

// Clears the client-side caches — provider catalog, thinking metadata,
// context usage — so the next use refetches. Settings and preferences are
// not touched.
export function clearClientCaches(): void {
  providersCache = null;
  ctxUsageCache = null;
  try {
    localStorage.removeItem(PROVIDERS_CACHE_KEY);
    localStorage.removeItem(PROVIDERS_CACHE_AT_KEY);
    localStorage.removeItem(THINKING_META_KEY);
  } catch {
    // ignore (private mode etc.)
  }
}

function redirectToLogin(): void {
  const p = window.location.pathname;
  if (p !== '/login' && p !== '/setup') {
    window.location.href = '/login';
  }
}

async function parseError(res: Response): Promise<string> {
  try {
    const data = (await res.json()) as { error?: string };
    if (data && typeof data.error === 'string' && data.error) return data.error;
  } catch {
    // not JSON — fall through to status text
  }
  return res.statusText || `Request failed (${res.status})`;
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(path, { credentials: 'same-origin', ...init });
  if (res.status === 401) {
    redirectToLogin();
    throw new ApiError('Unauthorized', 401);
  }
  if (!res.ok) {
    throw new ApiError(await parseError(res), res.status);
  }
  if (res.status === 204) return undefined as T;
  const ct = res.headers.get('content-type') ?? '';
  if (ct.includes('application/json')) {
    return (await res.json()) as T;
  }
  return undefined as T;
}

function post<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: 'POST',
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
}

function put<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
}

function patch<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
}

export interface SavedProviderInput {
  id?: string;
  name: string;
  baseURL: string;
  model: string;
  apiKey?: string;
}

export interface SettingsUpdate {
  llm?: {
    baseURL?: string;
    apiKey?: string;
    model?: string;
    providers?: SavedProviderInput[];
    activeProviderId?: string;
  };
  githubToken?: string;
  githubOAuthClientId?: string;
  vercelToken?: string;
  vercelOAuthClientId?: string;
  vercelOAuthClientSecret?: string;
  password?: string;
  mcp?: MCPServer[];
  permissionMode?: PermissionMode;
  rewindApproval?: boolean;
  defaultThinking?: string;
  toonEnabled?: boolean;
  rtkEnabled?: boolean;
  contextThreshold?: number;
  systemPrompt?: string;
}

export const api = {
  // Auth
  getAuthStatus: () => request<AuthStatus>('/api/auth/status'),
  login: (username: string, password: string) =>
    post<void>('/api/auth/login', { username, password }),
  setup: (username: string, password: string) =>
    post<void>('/api/auth/setup', { username, password }),
  signup: (username: string, password: string) =>
    post<void>('/api/auth/signup', { username, password }),
  logout: () => post<void>('/api/auth/logout'),
  listUsers: () => request<UserInfo[]>('/api/users'),
  createUser: (username: string, password: string, isAdmin: boolean) =>
    post<void>('/api/users', { username, password, isAdmin }),
  updateUser: (id: string, patch: { password?: string; isAdmin?: boolean }) =>
    request<UserInfo>(`/api/users/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch),
    }),
  deleteUser: (id: string) => request<void>(`/api/users/${id}`, { method: 'DELETE' }),

  // Settings
  getSettings: () => request<Settings>('/api/settings'),
  updateSettings: (body: SettingsUpdate) => put<void>('/api/settings', body),
  oidcSettings: () =>
    request<{
      issuer: string;
      clientId: string;
      clientSecretSet: boolean;
      callbackUri: string;
      allowedEmails: string;
      enabled: boolean;
    }>('/api/settings/oidc'),
  oidcSettingsSave: (body: {
    issuer?: string;
    clientId?: string;
    clientSecret?: string;
    callbackUri?: string;
    allowedEmails?: string;
  }) => post<{ enabled: boolean }>('/api/settings/oidc', body),
  testLLM: (opts?: { baseURL?: string; apiKey?: string; model?: string }) =>
    post<{ ok: boolean; error?: string }>('/api/settings/test-llm', opts),

  // LLM providers
  // The models.dev catalog is large and changes rarely — cache it (memory +
  // localStorage, 6h) so opening projects doesn't refetch it every time.
  getProviders: async () => {
    const cached = readProvidersCache();
    if (cached && Date.now() - cached.at < PROVIDERS_CACHE_TTL) {
      return { source: 'cache', providers: cached.data };
    }
    const r = await request<ProvidersResponse>('/api/providers');
    writeProvidersCache(r.providers);
    return r;
  },
  refreshProviders: () => post<ProvidersRefreshResult>('/api/providers/refresh'),  providerThinking: (providerId: string, model: string) =>
    request<{ levels: string[]; off: boolean }>(
      `/api/providers/thinking?providerId=${encodeURIComponent(providerId)}&model=${encodeURIComponent(model)}`,
    ),
  searchProviders: (query: string) =>
    request<ProvidersSearchResult>(`/api/providers/search?q=${encodeURIComponent(query)}`),
  addProvider: (id: string) => post<ProviderAddResult>('/api/providers/add', { id }),
  removeProvider: (id: string) => post<void>('/api/providers/remove', { id }),

  // Projects
  listProjects: () => request<Project[]>('/api/projects'),
  createProject: (body: { name?: string; description?: string }) =>
    post<Project>('/api/projects', body),
  getProject: (id: string) => request<Project>(`/api/projects/${id}`),
  updateProject: (id: string, body: { name?: string; previewCommand?: string; instructions?: string }) =>
    patch<Project>(`/api/projects/${id}`, body),
  deleteProject: (id: string) => request<void>(`/api/projects/${id}`, { method: 'DELETE' }),
  importProject: (repoUrl: string, name?: string) =>
    post<Project>('/api/projects/import', name ? { repoUrl, name } : { repoUrl }),

  // Files
  listFiles: (id: string, path: string) =>
    request<{ entries: FileEntry[] }>(`/api/projects/${id}/files?path=${encodeURIComponent(path)}`),
  listAllFiles: (id: string) =>
    request<{ entries: FileEntry[] }>(`/api/projects/${id}/files?recursive=true`),
  readFile: (id: string, path: string) =>
    request<{ content: string }>(`/api/projects/${id}/file?path=${encodeURIComponent(path)}`),
  writeFile: (id: string, path: string, content: string) =>
    put<void>(`/api/projects/${id}/file`, { path, content }),
  deleteFile: (id: string, path: string) =>
    request<void>(`/api/projects/${id}/file?path=${encodeURIComponent(path)}`, { method: 'DELETE' }),

  // Chat history (streaming lives in streamChat below)
  getMessages: (id: string, sessionId: string) =>
    request<ChatMessage[]>(`/api/projects/${id}/messages?sessionId=${encodeURIComponent(sessionId)}`),
  // Chat sessions: independent threads per project.
  listSessions: (id: string) =>
    request<{ sessions: ChatSession[] }>(`/api/projects/${id}/sessions`),
  createSession: (id: string, name?: string) =>
    post<{ session: ChatSession }>(`/api/projects/${id}/sessions`, { name }),
  renameSession: (id: string, sessionId: string, name: string) =>
    post<void>(`/api/projects/${id}/sessions/${sessionId}/rename`, { name }),
  // Context usage refreshes after every turn, but the value is stable for a
  // few seconds — cache it briefly so popup opens and re-renders don't
  // refetch it. force skips the cache (used right after a turn completes).
  contextUsage: async (id: string, sessionId: string, model?: string, providerId?: string, force = false) => {
    const q = new URLSearchParams();
    q.set('sessionId', sessionId);
    if (model) q.set('model', model);
    if (providerId) q.set('providerId', providerId);
    const qs = q.toString();
    const key = `${id}|${qs}`;
    if (!force && ctxUsageCache && ctxUsageCache.key === key && Date.now() - ctxUsageCache.at < CTX_USAGE_TTL) {
      return ctxUsageCache.data;
    }
    const data = await request<ContextUsage>(`/api/projects/${id}/context?${qs}`);
    ctxUsageCache = { key, at: Date.now(), data };
    return data;
  },
  stopChat: (id: string, sessionId: string) =>
    post<void>(`/api/projects/${id}/chat/stop?sessionId=${encodeURIComponent(sessionId)}`),
  truncateMessages: (id: string, sessionId: string, messageId: number) =>
    post<void>(`/api/projects/${id}/messages/truncate`, { id: messageId, sessionId }),
  compact: (id: string, sessionId: string) =>
    post<{ coveredMessageId: number }>(`/api/projects/${id}/compact?sessionId=${encodeURIComponent(sessionId)}`),
  chatDiagnostics: (id: string, sessionId: string) =>
    request<DiagnosticsDump>(
      `/api/projects/${id}/diagnostics?sessionId=${encodeURIComponent(sessionId)}`,
    ),
  // Mid-run send: the same endpoint queues the message onto the active run
  // (steer/follow-up) instead of opening an SSE stream. When queued, the
  // response carries the queue entry's id.
  queueChat: (id: string, sessionId: string, message: string, model?: string, providerId?: string) =>
    post<{ queued?: boolean; id?: string }>(`/api/projects/${id}/chat`, { message, sessionId, model, providerId }),
  chatStatus: (id: string, sessionId: string) =>
    request<{ running: boolean }>(
      `/api/projects/${id}/chat/status?sessionId=${encodeURIComponent(sessionId)}`,
    ),
  chatQueue: (id: string, sessionId: string) =>
    request<{ messages: { id: string; text: string }[]; steering: { id: string; text: string }[] }>(
      `/api/projects/${id}/chat/queue?sessionId=${encodeURIComponent(sessionId)}`,
    ),
  chatQueueReorder: (id: string, sessionId: string, ids: string[]) =>
    post<void>(`/api/projects/${id}/chat/queue/reorder`, { ids, sessionId }),
  chatQueueSteer: (id: string, sessionId: string, entryId: string) =>
    post<void>(`/api/projects/${id}/chat/queue/steer`, { id: entryId, sessionId }),
  chatQueueEdit: (id: string, sessionId: string, entryId: string, text: string) =>
    post<void>(`/api/projects/${id}/chat/queue/edit`, { id: entryId, text, sessionId }),
  chatQueueHold: (id: string, sessionId: string, entryId: string, held: boolean) =>
    post<void>(`/api/projects/${id}/chat/queue/hold`, { id: entryId, held, sessionId }),
  getTodos: (id: string) => request<{ todos: Todo[] }>(`/api/projects/${id}/todos`),
  getMemories: (id: string) => request<{ memories: Memory[] }>(`/api/projects/${id}/memories`),
  createMemory: (id: string, content: string) =>
    post<{ memories: Memory[] }>(`/api/projects/${id}/memories`, { content }),
  updateMemory: (id: string, memId: number, content: string) =>
    put<{ memories: Memory[] }>(`/api/projects/${id}/memories/${memId}`, { content }),
  toggleMemory: (id: string, memId: number, enabled: boolean) =>
    post<{ memories: Memory[] }>(`/api/projects/${id}/memories/${memId}/toggle`, { enabled }),
  deleteMemory: (id: string, memId: number) =>
    request<void>(`/api/projects/${id}/memories/${memId}`, { method: 'DELETE' }),
  askRespond: (
    id: string,
    sessionId: string,
    requestId: string,
    answers: { question: string; answer: string }[],
  ) =>
    post<void>(`/api/projects/${id}/ask/respond`, { requestId, answers, sessionId }),
  askPending: (id: string, sessionId: string) =>
    request<{
      pending: boolean;
      requestId?: string;
      question?: string;
      options?: string[];
      questions?: { question: string; options?: string[] }[];
    }>(`/api/projects/${id}/ask/pending?sessionId=${encodeURIComponent(sessionId)}`),

  // Preview
  getPreviewStatus: (id: string) => request<PreviewStatus>(`/api/projects/${id}/preview/status`),
  startPreview: (id: string) => post<{ url: string }>(`/api/projects/${id}/preview/start`),
  stopPreview: (id: string) => post<void>(`/api/projects/${id}/preview/stop`),

  // GitHub / git
  listGitHubRepos: () => request<GitHubRepo[]>('/api/github/repos'),
  githubCreate: (id: string, name: string, isPrivate: boolean) =>
    post<void>(`/api/projects/${id}/github/create`, { name, private: isPrivate }),
  githubLink: (id: string, repoUrl: string) =>
    post<void>(`/api/projects/${id}/github/link`, { repoUrl }),
  githubPush: (id: string, message: string) =>
    post<PushResult>(`/api/projects/${id}/github/push`, { message }),
  gitStatus: (id: string) => request<GitStatus>(`/api/projects/${id}/git/status`),
  gitInfo: (id: string) => request<GitInfo>(`/api/projects/${id}/git/info`),
  gitInit: (id: string) => post<void>(`/api/projects/${id}/git/init`),
  gitBranch: (id: string, name: string) =>
    post<void>(`/api/projects/${id}/git/branch`, { name }),
  gitCheckout: (id: string, branch: string) =>
    post<void>(`/api/projects/${id}/git/checkout`, { branch }),
  gitRevert: (id: string, commit: string) =>
    post<void>(`/api/projects/${id}/git/revert`, { commit }),
  oauthDeviceStart: () => post<DeviceFlowStart>('/api/github/oauth/device/start'),
  oauthDevicePoll: (flowId: string) =>
    post<DeviceFlowPoll>('/api/github/oauth/device/poll', { flowId }),

  // Vercel
  vercelUser: () => request<VercelUserInfo>('/api/vercel/user'),
  vercelDeploy: (id: string, target?: 'production') =>
    post<{ started: boolean }>(`/api/projects/${id}/vercel/deploy`, target ? { target } : {}),
  vercelDeployments: (id: string) =>
    request<VercelDeploymentsResponse>(`/api/projects/${id}/vercel/deployments`),

  // MCP
  mcpStatus: () => request<{ servers: MCPServerStatus[] }>('/api/mcp/status'),
  mcpTest: (srv: MCPServer) =>
    post<{ ok: boolean; tools?: { name: string; description: string }[]; error?: string }>(
      '/api/mcp/test',
      srv,
    ),

  // Skills (skillsmp)
  skillSearch: (query: string) =>
    post<{ skills: SkillSearchResult[] }>('/api/skills/search', { query }),
  skillInstall: (skill: SkillSearchResult) =>
    post<{ skills: InstalledSkill[] }>('/api/skills/install', { skill }),
  skillRemove: (id: string) => post<{ skills: InstalledSkill[] }>('/api/skills/remove', { id }),
  skillReadme: (id: string) => request<{ content: string }>(`/api/skills/${id}/readme`),
  skillToggle: (id: string, enabled: boolean) =>
    post<{ skills: InstalledSkill[] }>('/api/skills/toggle', { id, enabled }),

  // Chat permissions
  permissionRespond: (projectId: string, requestId: string, allow: boolean) =>
    post<void>(`/api/projects/${projectId}/chat/permission`, { requestId, allow }),
};

/**
 * Shared SSE streaming helper: fetches `path` (POST with an optional JSON
 * body, or GET), parses `data:` events and dispatches them to `onEvent`.
 * Resolves when the stream ends (with or without a `done` event); rejects on
 * HTTP/parse-level failures and aborts.
 */
async function streamChatEvents(
  path: string,
  body: unknown,
  onEvent: (ev: ChatEvent) => void,
  signal: AbortSignal,
  get = false,
): Promise<void> {
  const res = await fetch(path, {
    method: get ? 'GET' : 'POST',
    credentials: 'same-origin',
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
  });

  if (res.status === 401) {
    redirectToLogin();
    throw new ApiError('Unauthorized', 401);
  }
  if (!res.ok) {
    throw new ApiError(await parseError(res), res.status);
  }
  if (!res.body) {
    throw new ApiError('Empty response body', res.status);
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  let gotDone = false;

  const emit = (raw: string) => {
    const data = raw
      .split(/\r?\n/)
      .filter((l) => l.startsWith('data:'))
      .map((l) => l.slice(5).replace(/^ /, ''))
      .join('\n');
    if (!data) return;
    let ev: ChatEvent;
    try {
      ev = JSON.parse(data) as ChatEvent;
    } catch {
      return; // ignore malformed events
    }
    if (ev.type === 'done') gotDone = true;
    onEvent(ev);
  };

  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let match: RegExpMatchArray | null;
    while ((match = buf.match(/\r?\n\r?\n/)) !== null) {
      const idx = match.index ?? 0;
      emit(buf.slice(0, idx));
      buf = buf.slice(idx + match[0].length);
    }
  }
  buf += decoder.decode();
  if (buf.trim()) emit(buf);

  if (!gotDone) onEvent({ type: 'done' });
}

/** One file attached to a chat turn (sent to the backend for the LLM). */
export interface ChatAttachmentInput {
  name: string;
  mime: string;
  kind: 'text' | 'image';
  content: string; // file text, or base64 data for images
}

/** URL serving one stored message attachment (images usable as <img> src). */
export function messageAttachmentUrl(projectId: string, messageId: string, idx: number): string {
  return `/api/projects/${projectId}/messages/${messageId}/attachments/${idx}`;
}

/**
 * Streams a chat response as SSE over fetch. `message` is the user text;
 * `model` is an optional per-turn model override; `editMessageId`, when set,
 * edits that existing user message and rewinds the thread to it before
 * re-running. Dispatches parsed events to `onEvent`.
 */
export function streamChat(
  projectId: string,
  sessionId: string,
  message: string,
  opts: {
    model?: string;
    editMessageId?: number;
    providerId?: string;
    attachments?: ChatAttachmentInput[];
    /** Thinking level for reasoning models ('' = off). */
    thinking?: string;
  } | undefined,
  onEvent: (ev: ChatEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  const body: {
    message: string;
    sessionId: string;
    model?: string;
    editMessageId?: number;
    providerId?: string;
    attachments?: ChatAttachmentInput[];
    thinking?: string;
  } = { message, sessionId };
  if (opts?.model && opts.model.trim() !== '') body.model = opts.model;
  if (opts?.editMessageId) body.editMessageId = opts.editMessageId;
  if (opts?.providerId) body.providerId = opts.providerId;
  if (opts?.attachments && opts.attachments.length > 0) body.attachments = opts.attachments;
  if (opts?.thinking) body.thinking = opts.thinking;
  return streamChatEvents(`/api/projects/${projectId}/chat`, body, onEvent, signal);
}

/**
 * Replays the last user turn as the same SSE stream. Dispatches parsed events
 * to `onEvent`.
 */
export function retryChat(
  projectId: string,
  sessionId: string,
  onEvent: (ev: ChatEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  return streamChatEvents(
    `/api/projects/${projectId}/chat/retry?sessionId=${encodeURIComponent(sessionId)}`,
    undefined,
    onEvent,
    signal,
  );
}

/**
 * Attaches to a running turn's live stream (GET /chat/watch): the run's
 * events from now on are relayed here until it finishes. Used when returning
 * to a chat mid-generation — the UI behaves exactly as if we had never left.
 */
export function watchChat(
  projectId: string,
  sessionId: string,
  onEvent: (ev: ChatEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  return streamChatEvents(
    `/api/projects/${projectId}/chat/watch?sessionId=${encodeURIComponent(sessionId)}`,
    undefined,
    onEvent,
    signal,
    true,
  );
}

// DiagnosticsDump is the project chat debug export (GET /diagnostics) — build
// version, effective provider (key masked), run state, and the session's
// messages. Used to diagnose chats that ended unexpectedly from the UI.
export interface DiagnosticsDump {
  project: { id: string; name: string };
  session: string;
  version: string;
  commit: string;
  provider: {
    id?: string;
    name: string;
    baseURL: string;
    model: string;
    apiKey: string;
  };
  run: {
    running: boolean;
    queued?: { id: string; text: string }[];
    steering?: { id: string; text: string }[];
  };
  messages: {
    id: number;
    role: string;
    content: string;
    toolJson: string;
    model: string;
    usage: string;
    reasoning: string;
    attachments: { name: string; mime: string; kind: string; size: number }[];
  }[];
}
