export interface AuthStatus {
  authRequired: boolean;
  authenticated: boolean;
  setupRequired: boolean;
  oidcEnabled: boolean;
  signupEnabled: boolean;
  user: { username: string; isAdmin: boolean } | null;
}

export interface UserInfo {
  id: string;
  username: string;
  isAdmin: boolean;
  createdAt: number;
}

/** One chat thread of a project. */
export interface ChatSession {
  id: string;
  name: string;
  createdAt: number;
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

export type PermissionMode = 'ask' | 'auto' | 'yolo';

export interface Settings {
  llm: LLMSettings;
  github: GitHubSettings;
  vercel: VercelSettings;
  auth: { disabled: boolean };
  mcp: MCPServer[];
  skills: InstalledSkill[];
  permissionMode: PermissionMode;
  rewindApproval: boolean;
  defaultThinking: string;
  toonEnabled: boolean;
  autoPushDefault?: boolean;
  /** Compaction threshold in percent of the context window (e.g. 80). */
  contextThreshold: number;
  systemPrompt: string;
  version: string;
  commit: string;
}

export interface VercelSettings {
  tokenSet: boolean;
  oauthClientId: string;
  clientSecretSet: boolean;
  source: 'oauth' | 'pat' | 'env' | null;
}

export interface VercelDeployment {
  id: string;
  state: string; // readyState: READY / BUILDING / ERROR / ...
  url: string;
  createdAt: number; // unix ms
  production: boolean;
}

export type VercelActiveDeploy =
  | (VercelDeployment & { error?: string })
  | { state: string; error?: string }
  | null;

export interface VercelDeploymentsResponse {
  connected: boolean;
  active: VercelActiveDeploy;
  recent: VercelDeployment[];
  error?: string;
}

export interface VercelUserInfo {
  connected: boolean;
  login?: string;
  error?: string;
}

export interface ProviderModel {
  id: string;
  name: string;
  /** Model accepts image input (vision) per models.dev. */
  imageInput?: boolean;
  /** Thinking support per models.dev (reasoning models; levels and default when published). */
  reasoning?: { effort?: boolean; levels?: string[]; default?: string };
  /** Context window in tokens, when models.dev publishes one. */
  context?: number;
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
  previewCommand?: string;
  defaultPreviewCommand?: string;
  instructions?: string;
  autoPush: boolean;
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

export interface ContextUsage {
  used: number;
  budget: number;
  /** Token count at which compaction is recommended. */
  threshold: number;
}

export interface Todo {
  title: string;
  done: boolean;
}

export interface Memory {
  id: number;
  content: string;
  enabled: boolean;
  createdAt: number;
}

export interface ChatAttachmentMeta {
  name: string;
  mime: string;
  kind: 'text' | 'image';
  size: number;
}

export interface ChatMessage {
  id: string;
  role: string;
  content: string;
  tool?: unknown;
  reasoning?: string;
  usage?: ChatUsage;
  model?: string;
  attachments?: ChatAttachmentMeta[];
  createdAt: string;
}

export type ChatEvent =
  | { type: 'delta'; text: string }
  | { type: 'reasoning'; text: string }
  | { type: 'snapshot'; text?: string; reasoning?: string }
  | { type: 'info'; text: string }
  | { type: 'tool_start'; name: string; detail: string }
  | { type: 'tool_end'; name: string; ok: boolean; detail: string }
  | { type: 'todos'; todos: Todo[] }
  | { type: 'memories'; memories: Memory[] }
  | { type: 'permission_request'; requestId: string; tool: string; detail: string }
  | { type: 'done'; usage?: ChatUsage }
  | { type: 'error'; error: string }
  | {
      type: 'question_request';
      requestId: string;
      text?: string;
      options?: string[];
      questions?: { question: string; options?: string[] }[];
    }
  | { type: 'project_renamed'; text?: string }
  | {
      type: 'injected_message';
      messageId?: number;
      text?: string;
      attachments?: ChatAttachmentMeta[];
    };

export interface MCPServer {
  id: string;
  name: string;
  command: string;
  args: string[];
  /** Missing means enabled (servers saved before the toggle existed). */
  enabled?: boolean;
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
  skillsmpUrl?: string;
  dir: string;
  enabled: boolean;
}

export interface SkillSearchResult {
  id: string;
  name: string;
  author: string;
  description: string;
  githubUrl: string;
  skillsmpUrl?: string;
  stars?: number;
  branch: string;
  sourcePath: string;
  owner: string;
  repo: string;
  builtin?: boolean;
  dir?: string;
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
  ahead?: number;
  behind?: number;
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

export interface GitHubWorkflowRun {
  id: number;
  name: string;
  display_title: string;
  head_branch: string;
  event: string;
  status: string;
  conclusion: string | null;
  created_at: string;
  html_url: string;
}

export interface GitHubWorkflows {
  owner: string;
  repo: string;
  count: number;
  workflows: GitHubWorkflowRun[];
}

export interface GitHubContainerImage {
  name: string;
  visibility: string;
  url: string;
  created_at: string;
  updated_at: string;
  full: string;
}

export interface GitHubImages {
  owner: string;
  count: number;
  images: GitHubContainerImage[];
}
