import { getNotifyEnabled } from './utils';

// Shows a notification through the service worker when one is registered
// (the path iOS PWAs support), falling back to the page constructor. Returns
// whether it was shown.
async function showNotification(title: string, body: string, tag: string): Promise<boolean> {
  if (!('Notification' in window) || Notification.permission !== 'granted') return false;
  const options = { body, icon: '/icon-192.png', tag };
  try {
    const reg = await navigator.serviceWorker?.getRegistration();
    if (reg) {
      await reg.showNotification(title, options);
      return true;
    }
  } catch {
    // fall through to the page constructor
  }
  try {
    new Notification(title, options);
    return true;
  } catch {
    // notifications unsupported here
    return false;
  }
}

/**
 * System notification for a finished chat turn. Only fires when the app is
 * backgrounded (document.hidden) — the user is watching otherwise. Requires
 * the Notifications toggle (Settings → About) and a granted browser
 * permission.
 */
export async function notifyTurnDone(projectId: string, projectName: string, text: string) {
  if (!getNotifyEnabled()) return;
  if (!document.hidden) return;
  const title = projectName ? `${projectName} — turn finished` : 'Turn finished';
  const body = (text.replace(/\s+/g, ' ').trim() || 'Response complete.').slice(0, 140);
  await showNotification(title, body, `v1-turn-${projectId}`);
}

// Test notification for the Settings → About control: fires regardless of the
// foreground state so the user can verify the permission + service worker
// path on their device.
export async function testNotification(): Promise<boolean> {
  return showNotification('v1 — notifications work', 'This is a test notification.', 'v1-test');
}
