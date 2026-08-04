import type {
  AuthStatus,
  ChatEvent,
  ChatMessage,
  DeviceFlowPoll,
  DeviceFlowStart,
  FileEntry,
  GitHubRepo,
  GitStatus,
  PreviewStatus,
  Project,
  ProjectTemplate,
  ProviderAddResult,
  ProvidersRefreshResult,
  ProvidersResponse,
  ProvidersSearchResult,
  PushResult,
  Settings,
  Todo,
} from './types';

export class ApiError extends Error {
  status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
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

export interface SettingsUpdate {
  llm?: { baseURL?: string; apiKey?: string; model?: string };
  githubToken?: string;
  githubOAuthClientId?: string;
  password?: string;
}

export const api = {
  // Auth
  getAuthStatus: () => request<AuthStatus>('/api/auth/status'),
  login: (password: string) => post<void>('/api/auth/login', { password }),
  setup: (password: string) => post<void>('/api/auth/setup', { password }),
  logout: () => post<void>('/api/auth/logout'),

  // Settings
  getSettings: () => request<Settings>('/api/settings'),
  updateSettings: (body: SettingsUpdate) => put<void>('/api/settings', body),
  testLLM: (opts?: { baseURL?: string; apiKey?: string; model?: string }) =>
    post<{ ok: boolean; error?: string }>('/api/settings/test-llm', opts),

  // LLM providers
  getProviders: () => request<ProvidersResponse>('/api/providers'),
  refreshProviders: () => post<ProvidersRefreshResult>('/api/providers/refresh'),
  searchProviders: (query: string) =>
    request<ProvidersSearchResult>(`/api/providers/search?q=${encodeURIComponent(query)}`),
  addProvider: (id: string) => post<ProviderAddResult>('/api/providers/add', { id }),
  removeProvider: (id: string) => post<void>('/api/providers/remove', { id }),

  // Projects
  listProjects: () => request<Project[]>('/api/projects'),
  createProject: (name: string, template: ProjectTemplate) =>
    post<Project>('/api/projects', { name, template }),
  getProject: (id: string) => request<Project>(`/api/projects/${id}`),
  deleteProject: (id: string) => request<void>(`/api/projects/${id}`, { method: 'DELETE' }),
  importProject: (repoUrl: string, name?: string) =>
    post<Project>('/api/projects/import', name ? { repoUrl, name } : { repoUrl }),

  // Files
  listFiles: (id: string, path: string) =>
    request<{ entries: FileEntry[] }>(`/api/projects/${id}/files?path=${encodeURIComponent(path)}`),
  readFile: (id: string, path: string) =>
    request<{ content: string }>(`/api/projects/${id}/file?path=${encodeURIComponent(path)}`),
  writeFile: (id: string, path: string, content: string) =>
    put<void>(`/api/projects/${id}/file`, { path, content }),
  deleteFile: (id: string, path: string) =>
    request<void>(`/api/projects/${id}/file?path=${encodeURIComponent(path)}`, { method: 'DELETE' }),

  // Chat history (streaming lives in streamChat below)
  getMessages: (id: string) => request<ChatMessage[]>(`/api/projects/${id}/messages`),
  truncateMessages: (id: string, messageId: number) =>
    post<void>(`/api/projects/${id}/messages/truncate`, { id: messageId }),
  getTodos: (id: string) => request<{ todos: Todo[] }>(`/api/projects/${id}/todos`),

  // Preview
  getPreviewStatus: (id: string) => request<PreviewStatus>(`/api/projects/${id}/preview/status`),
  startPreview: (id: string) => post<{ url: string }>(`/api/projects/${id}/preview/start`),
  stopPreview: (id: string) => post<void>(`/api/projects/${id}/preview/stop`),

  // GitHub / git
  listGitHubRepos: () => request<GitHubRepo[]>('/api/github/repos'),
  githubCreate: (id: string, name: string, isPrivate: boolean) =>
    post<void>(`/api/projects/${id}/github/create`, { name, private: isPrivate }),
  githubPush: (id: string, message: string) =>
    post<PushResult>(`/api/projects/${id}/github/push`, { message }),
  gitStatus: (id: string) => request<GitStatus>(`/api/projects/${id}/git/status`),
  oauthDeviceStart: () => post<DeviceFlowStart>('/api/github/oauth/device/start'),
  oauthDevicePoll: (flowId: string) =>
    post<DeviceFlowPoll>('/api/github/oauth/device/poll', { flowId }),
};

/**
 * Shared SSE streaming helper: POSTs to `path` (with an optional JSON body),
 * parses `data:` events and dispatches them to `onEvent`. Resolves when the
 * stream ends (with or without a `done` event); rejects on HTTP/parse-level
 * failures and aborts.
 */
async function streamChatEvents(
  path: string,
  body: unknown,
  onEvent: (ev: ChatEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  const res = await fetch(path, {
    method: 'POST',
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

/**
 * Streams a chat response as SSE over fetch. `message` is the user text;
 * `model` is an optional per-turn model override; `editMessageId`, when set,
 * edits that existing user message and rewinds the thread to it before
 * re-running. Dispatches parsed events to `onEvent`.
 */
export function streamChat(
  projectId: string,
  message: string,
  opts: { model?: string; editMessageId?: number } | undefined,
  onEvent: (ev: ChatEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  const body: { message: string; model?: string; editMessageId?: number } = { message };
  if (opts?.model && opts.model.trim() !== '') body.model = opts.model;
  if (opts?.editMessageId) body.editMessageId = opts.editMessageId;
  return streamChatEvents(`/api/projects/${projectId}/chat`, body, onEvent, signal);
}

/**
 * Replays the last user turn as the same SSE stream. Dispatches parsed events
 * to `onEvent`.
 */
export function retryChat(
  projectId: string,
  onEvent: (ev: ChatEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  return streamChatEvents(`/api/projects/${projectId}/chat/retry`, undefined, onEvent, signal);
}
