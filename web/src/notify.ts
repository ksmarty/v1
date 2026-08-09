import { getNotifyEnabled } from './utils';

/**
 * System notification for a finished chat turn. Only fires when the app is
 * backgrounded (document.hidden) — the user is watching otherwise. Prefers
 * the service worker's showNotification, which is the path iOS PWAs support;
 * falls back to the page constructor elsewhere. Requires the Notifications
 * toggle (Settings → About) and a granted browser permission.
 */
export async function notifyTurnDone(projectId: string, projectName: string, text: string) {
  if (!getNotifyEnabled()) return;
  if (!document.hidden) return;
  if (!('Notification' in window) || Notification.permission !== 'granted') return;
  const title = projectName ? `${projectName} — turn finished` : 'Turn finished';
  const body = (text.replace(/\s+/g, ' ').trim() || 'Response complete.').slice(0, 140);
  const options = { body, icon: '/icon-192.png', tag: `v1-turn-${projectId}` };
  try {
    const reg = await navigator.serviceWorker?.getRegistration();
    if (reg) {
      await reg.showNotification(title, options);
      return;
    }
  } catch {
    // fall through to the page constructor
  }
  try {
    new Notification(title, options);
  } catch {
    // notifications unsupported here — ignore
  }
}
