export function errMsg(e: unknown): string {
  if (e instanceof Error) return e.message;
  return String(e);
}

export function randomId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  if (typeof crypto !== 'undefined' && typeof crypto.getRandomValues === 'function') {
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

export function timeAgo(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const diff = Date.now() - t;
  const sec = Math.floor(diff / 1000);
  if (sec < 10) return 'just now';
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.floor(hr / 24);
  if (day < 30) return `${day}d ago`;
  return new Date(t).toLocaleDateString();
}

export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export type ChatSide = 'left' | 'right';

const CHAT_SIDE_KEY = 'v1.chatSide';

export function getChatSide(): ChatSide {
  try {
    return localStorage.getItem(CHAT_SIDE_KEY) === 'right' ? 'right' : 'left';
  } catch {
    return 'left';
  }
}

export function setChatSide(side: ChatSide): void {
  try {
    localStorage.setItem(CHAT_SIDE_KEY, side);
  } catch {
    // ignore (private mode etc.)
  }
}

const DEBUG_HUD_KEY = 'v1.debugHud';

export function getDebugHud(): boolean {
  try {
    return localStorage.getItem(DEBUG_HUD_KEY) === '1';
  } catch {
    return false;
  }
}

export function setDebugHud(on: boolean): void {
  try {
    localStorage.setItem(DEBUG_HUD_KEY, on ? '1' : '0');
  } catch {
    // ignore (private mode etc.)
  }
}

const NOTIFY_KEY = 'v1.notifications';

export function getNotifyEnabled(): boolean {
  try {
    return localStorage.getItem(NOTIFY_KEY) === '1';
  } catch {
    return false;
  }
}

export function setNotifyEnabled(on: boolean): void {
  try {
    localStorage.setItem(NOTIFY_KEY, on ? '1' : '0');
  } catch {
    // ignore (private mode etc.)
  }
}

// Whether chat tool call/result JSON starts pretty-printed (Settings →
// Appearance → Tool calls). The per-block Raw/Pretty toggle still overrides.
const JSON_PRETTY_KEY = 'v1.jsonPretty';

export function getJsonPretty(): boolean {
  try {
    return localStorage.getItem(JSON_PRETTY_KEY) !== '0';
  } catch {
    return true;
  }
}

export function setJsonPretty(on: boolean): void {
  try {
    localStorage.setItem(JSON_PRETTY_KEY, on ? '1' : '0');
  } catch {
    // ignore (private mode etc.)
  }
}

// iOS detection (iPadOS 13+ masks its user agent as a Mac — touch is the tell).
export function isIOS(): boolean {
  return (
    /iPad|iPhone|iPod/.test(navigator.userAgent) ||
    (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
  );
}

// iOS major.minor version (e.g. 17.5), or 0 on other platforms (and iPadOS,
// whose user agent no longer carries the OS version).
export function iosVersion(): number {
  const m = navigator.userAgent.match(/OS (\d+)[_.](\d+)/);
  return m ? parseFloat(`${m[1]}.${m[2]}`) : 0;
}

// True when running as an installed PWA (e.g. iOS Add to Home Screen).
export function isStandalone(): boolean {
  return (
    window.matchMedia('(display-mode: standalone)').matches ||
    (navigator as Navigator & { standalone?: boolean }).standalone === true
  );
}

// Project-view tabs: which tabs appear in the chat/files/terminal bar and in
// what order (Settings → Appearance → Chat tabs).
export type ChatTab = 'chat' | 'files' | 'terminal' | 'git' | 'memories' | 'project';

export type ChatTabLayout = {
  /** Tabs shown in the bar, in display order. */
  order: ChatTab[];
  /** Tabs excluded from the bar. */
  hidden: ChatTab[];
};

const CHAT_TABS_KEY = 'v1.chatTabs';

const CHAT_TAB_IDS: ChatTab[] = ['chat', 'files', 'terminal', 'git', 'memories', 'project'];

export function getChatTabLayout(): ChatTabLayout {
  try {
    const v = JSON.parse(localStorage.getItem(CHAT_TABS_KEY) ?? 'null') as {
      order?: unknown;
      hidden?: unknown;
    } | null;
    const known = (x: unknown): x is ChatTab =>
      typeof x === 'string' && (CHAT_TAB_IDS as string[]).includes(x);
    if (v && Array.isArray(v.order)) {
      return {
        order: v.order.filter(known),
        hidden: Array.isArray(v.hidden) ? v.hidden.filter(known) : [],
      };
    }
  } catch {
    // ignore
  }
  return { order: [...CHAT_TAB_IDS], hidden: [] };
}

export function setChatTabLayout(l: ChatTabLayout): void {
  try {
    localStorage.setItem(CHAT_TABS_KEY, JSON.stringify(l));
  } catch {
    // ignore (private mode etc.)
  }
}
