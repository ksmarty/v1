export interface AuthStatus {
  authRequired: boolean;
  authenticated: boolean;
  setupRequired: boolean;
}

export interface LLMSettings {
  baseURL: string;
  model: string;
  apiKeySet: boolean;
}

export interface GitHubSettings {
  tokenSet: boolean;
  oauthClientId: string;
  source: 'oauth' | 'pat' | 'env' | null;
}

export interface Settings {
  llm: LLMSettings;
  github: GitHubSettings;
  auth: { disabled: boolean };
  version: string;
}

export interface ProviderModel {
  id: string;
  name: string;
}

export interface Provider {
  id: string;
  name: string;
  baseURL: string;
  keyHint: string;
  doc: string;
  models: ProviderModel[];
}

export interface ProvidersResponse {
  source: string;
  providers: Provider[];
}

export interface ProvidersRefreshResult {
  ok: boolean;
  count?: number;
  error?: string;
}

export interface DeviceFlowStart {
  flowId: string;
  userCode: string;
  verificationUri: string;
  expiresIn: number;
  interval: number;
}

export interface DeviceFlowPoll {
  status: 'pending' | 'slow_down' | 'denied' | 'expired' | 'error' | 'complete';
  login?: string;
  error?: string;
}

export interface PreviewInfo {
  running: boolean;
  url: string;
}

export interface Project {
  id: string;
  name: string;
  repoUrl: string;
  preview: PreviewInfo;
  updatedAt: string;
}

export type ProjectTemplate = 'vite-react' | 'static' | 'empty';

export interface FileEntry {
  name: string;
  path: string;
  type: string; // "file" | "dir"
  size: number;
}

export interface ChatMessage {
  id: string;
  role: string;
  content: string;
  tool?: string;
  createdAt: string;
}

export type ChatEvent =
  | { type: 'delta'; text: string }
  | { type: 'tool_start'; name: string; detail: string }
  | { type: 'tool_end'; name: string; ok: boolean; detail: string }
  | { type: 'done' }
  | { type: 'error'; error: string };

export interface PreviewStatus {
  running: boolean;
  url: string;
  logs: string;
}

export interface GitHubRepo {
  name: string;
  fullName: string;
  url: string;
  private: boolean;
  updatedAt: string;
}

export interface GitStatus {
  isRepo: boolean;
  branch?: string;
  modified: number;
  untracked: number;
  repoUrl?: string;
}

export interface PushResult {
  committed: boolean;
  pushed: boolean;
  summary: string;
}
