export interface AuthStatus {
  authRequired: boolean;
  authenticated: boolean;
  setupRequired: boolean;
  oidcEnabled: boolean;
}

export interface SavedProvider {
  id: string;
  name: string;
  baseURL: string;
  model: string;
  apiKeySet: boolean;
}

export interface LLMSettings {
  baseURL: string;
  model: string;
  apiKeySet: boolean;
  models: ProviderModel[];
  providers: SavedProvider[];
  activeProviderId: string;
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
  mcp: MCPServer[];
  skills: InstalledSkill[];
  toolPolicy: Record<string, string>;
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
  added?: boolean;
}

export interface ProvidersResponse {
  source: string;
  providers: Provider[];
}

export interface ProvidersSearchResult {
  providers: Provider[];
}

export interface ProviderAddResult {
  ok: boolean;
  existing?: boolean;
  provider?: Provider;
  error?: string;
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

export interface ChatUsage {
  input: number;
  output: number;
  model?: string;
}

export interface Todo {
  title: string;
  done: boolean;
}

export interface ChatMessage {
  id: string;
  role: string;
  content: string;
  tool?: string;
  reasoning?: string;
  usage?: ChatUsage;
  model?: string;
  createdAt: string;
}

export type ChatEvent =
  | { type: 'delta'; text: string }
  | { type: 'reasoning'; text: string }
  | { type: 'tool_start'; name: string; detail: string }
  | { type: 'tool_end'; name: string; ok: boolean; detail: string }
  | { type: 'todos'; todos: Todo[] }
  | { type: 'permission_request'; requestId: string; tool: string; detail: string }
  | { type: 'done'; usage?: ChatUsage }
  | { type: 'error'; error: string };

export interface MCPServer {
  id: string;
  name: string;
  command: string;
  args: string[];
}

export interface MCPServerStatus {
  id: string;
  name: string;
  connected: boolean;
  toolCount: number;
}

export interface InstalledSkill {
  id: string;
  name: string;
  author: string;
  description: string;
  githubUrl: string;
  dir: string;
  enabled: boolean;
}

export interface SkillSearchResult {
  id: string;
  name: string;
  author: string;
  description: string;
  githubUrl: string;
  branch: string;
  sourcePath: string;
  owner: string;
  repo: string;
}

export interface PreviewStatus {
  running: boolean;
  url: string;
  logs: string;
  revision: number;
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

export interface GitCommit {
  hash: string;
  short: string;
  message: string;
  author: string;
  time: number;
}

export interface GitInfo {
  isRepo: boolean;
  branch?: string;
  branches?: string[];
  commits?: GitCommit[];
}

export interface PushResult {
  committed: boolean;
  pushed: boolean;
  summary: string;
}
