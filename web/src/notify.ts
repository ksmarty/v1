import { getNotifyAsk, getNotifyEnabled, getNotifyOnlyBackground, getNotifyTurnDone, getNotifyTurnError } from './utils';
import { pushActive } from './push';

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

// Common gating for turn notifications: master toggle, the specific
// category toggle, and (by default) only when the window is not focused.
//
// The device's own push subscription is the final gate. Whenever it exists the
// server pushes for turn events, and that push arrives even when iOS has
// suspended the app — so showing the in-page notification as well delivers the
// same turn twice. The in-page copy is the one that lands late, because the page
// only discovers a finished turn when the user comes back to it.
async function shouldNotify(
  category: boolean,
  sessionId: string,
  title: string,
  body: string,
  url: string,
) {
  if (!getNotifyEnabled() || !category) return;
  if (getNotifyOnlyBackground() && document.visibilityState === 'visible') return;
  if (await pushActive()) return;
  // Same tag as the push path, so a duplicate replaces instead of stacking.
  await showNotification(title, body, `v1-turn-${sessionId}`, url);
}

/**
 * System notification for a finished chat turn. Fires whenever the window
 * does not have focus (backgrounded tab or another window is active) — the
 * user is not watching the respond otherwise. Requires the Notifications
 * toggle and a granted browser permission.
 */
export async function notifyTurnDone(
  projectId: string,
  sessionId: string,
  projectName: string,
  text: string,
) {
  const title = projectName ? `${projectName} — turn finished` : 'Turn finished';
  const body = (text.replace(/\s+/g, ' ').trim() || 'Response complete.').slice(0, 140);
  const url = `/project/${encodeURIComponent(projectId)}?session=${encodeURIComponent(sessionId)}`;
  await shouldNotify(getNotifyTurnDone(), sessionId, title, body, url);
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
  const title = projectName ? `${projectName} — turn failed` : 'Turn failed';
  const body = (message.replace(/\s+/g, ' ').trim() || 'Something went wrong.').slice(0, 140);
  const url = `/project/${encodeURIComponent(projectId)}?session=${encodeURIComponent(sessionId)}`;
  await shouldNotify(getNotifyTurnError(), sessionId, title, body, url);
}

/**
 * System notification for an agent question (the ask_user tool). The turn is
 * blocked until the user answers, so this is the notification most likely to be
 * missed — it fires under the same gating as the turn notifications, which by
 * default means only while the app is not focused.
 */
export async function notifyAsk(
  projectId: string,
  sessionId: string,
  projectName: string,
  questions: string[],
) {
  const title = projectName ? `${projectName} — question` : 'The agent asked a question';
  const body = (questions.join(' · ').replace(/\s+/g, ' ').trim() || 'Waiting for your answer.').slice(
    0,
    140,
  );
  const url = `/project/${encodeURIComponent(projectId)}?session=${encodeURIComponent(sessionId)}`;
  await shouldNotify(getNotifyAsk(), sessionId, title, body, url);
}

// Test notification for the Settings → About control: fires regardless of the
// foreground state so the user can verify the permission + service worker
// path on their device.
export async function testNotification(): Promise<boolean> {
  return showNotification('v1 — notifications work', 'This is a test notification.', 'v1-test');
}