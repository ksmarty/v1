import { getNotifyEnabled } from './utils';

// Shows a notification through the service worker when one is registered
// (the path iOS PWAs support), falling back to the page constructor. Returns
// whether it was shown. data.url is used by the service worker's
// notificationclick handler to navigate to the exact chat.
async function showNotification(
  title: string,
  body: string,
  tag: string,
  url?: string,
): Promise<boolean> {
  if (!('Notification' in window) || Notification.permission !== 'granted') return false;
  const options: NotificationOptions = { body, icon: '/icon-192.png', tag };
  if (url) options.data = { url };
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
 * System notification for a finished chat turn. Fires whenever the window
 * does not have focus (backgrounded tab or another window is active) — the
 * user is not watching the respond otherwise. Requires the Notifications
 * toggle (Settings → About) and a granted browser permission.
 */
export async function notifyTurnDone(
  projectId: string,
  sessionId: string,
  projectName: string,
  text: string,
) {
  if (!getNotifyEnabled()) return;
  if (document.hasFocus()) return;
  const title = projectName ? `${projectName} — turn finished` : 'Turn finished';
  const body = (text.replace(/\s+/g, ' ').trim() || 'Response complete.').slice(0, 140);
  const url = `/project/${encodeURIComponent(projectId)}?session=${encodeURIComponent(sessionId)}`;
  await showNotification(title, body, `v1-turn-${projectId}`, url);
}

/**
 * System notification for a failed chat turn (LLM request error, tool
 * failure, network drop, ...). Fires only when the window doesn't have
 * focus, mirroring the finished-turn notification.
 */
export async function notifyTurnError(
  projectId: string,
  sessionId: string,
  projectName: string,
  message: string,
) {
  if (!getNotifyEnabled()) return;
  if (document.hasFocus()) return;
  const title = projectName ? `${projectName} — turn failed` : 'Turn failed';
  const body = (message.replace(/\s+/g, ' ').trim() || 'Something went wrong.').slice(0, 140);
  const url = `/project/${encodeURIComponent(projectId)}?session=${encodeURIComponent(sessionId)}`;
  await showNotification(title, body, `v1-turn-${projectId}`, url);
}

// Test notification for the Settings → About control: fires regardless of the
// foreground state so the user can verify the permission + service worker
// path on their device.
export async function testNotification(): Promise<boolean> {
  return showNotification('v1 — notifications work', 'This is a test notification.', 'v1-test');
}