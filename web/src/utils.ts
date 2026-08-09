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
