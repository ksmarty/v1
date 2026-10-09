import { api } from './api';

// Sessions that were created but never used.
//
// Both ways of starting a chat — the dashboard's session menu and the sessions
// sheet — create the row first and put you in it. Leaving without typing
// anything used to strand an empty session in the list forever, so an id
// created here is deleted when its chat is left unused. The set lives in
// sessionStorage so a reload in between does not lose track of it.
const KEY = 'v1:unused-sessions';

type Unused = { projectId: string; sessionId: string };

function read(): Unused[] {
  try {
    const raw: unknown = JSON.parse(sessionStorage.getItem(KEY) ?? '[]');
    return Array.isArray(raw) ? (raw as Unused[]) : [];
  } catch {
    return [];
  }
}

function write(list: Unused[]) {
  try {
    sessionStorage.setItem(KEY, JSON.stringify(list));
  } catch {
    // Storage disabled (private mode): the cleanup simply does not happen.
  }
}

/** Records a session that was created but has not been used yet. */
export function markSessionUnused(projectId: string, sessionId: string) {
  write([...read().filter((u) => u.sessionId !== sessionId), { projectId, sessionId }]);
}

/** Forgets a session because something was sent in it. */
export function markSessionUsed(sessionId: string) {
  write(read().filter((u) => u.sessionId !== sessionId));
}

/**
 * Called when a chat is left. If it is one we created and nothing was ever sent
 * in it, the session is deleted — the server re-checks emptiness, because a
 * message may have arrived from elsewhere while nobody was looking.
 */
export async function releaseUnusedSession(projectId: string, sessionId: string) {
  const list = read();
  if (!list.some((u) => u.sessionId === sessionId)) return;
  write(list.filter((u) => u.sessionId !== sessionId));
  try {
    await api.deleteSession(projectId, sessionId, true);
  } catch {
    // The session stays; the next visit shows it as an ordinary empty chat.
  }
}
