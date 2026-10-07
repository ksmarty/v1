import { api } from './api';
import { getNotifyEnabled } from './utils';

/** Push needs a service worker, the Push API and Notification (iOS only grants
 * these to an installed, home-screen PWA). */
export function pushSupported(): boolean {
  return (
    typeof navigator !== 'undefined' &&
    'serviceWorker' in navigator &&
    typeof window !== 'undefined' &&
    'PushManager' in window &&
    'Notification' in window
  );
}

function b64urlToBytes(base64: string): Uint8Array {
  const padded = base64.replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(padded + '='.repeat((4 - (padded.length % 4)) % 4));
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

function bytesToB64url(bytes: ArrayBuffer): string {
  let raw = '';
  for (const b of new Uint8Array(bytes)) raw += String.fromCharCode(b);
  return btoa(raw).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** Whether an existing subscription was made with the key the server holds now. */
function usesKey(sub: PushSubscription, publicKey: string): boolean {
  const current = sub.options?.applicationServerKey;
  if (!current) return false;
  return bytesToB64url(current) === publicKey;
}

/**
 * Register this device for Web Push, so a finished turn reaches it even when iOS
 * has suspended the app.
 *
 * Idempotent and safe to call on every load: re-registering refreshes the
 * server's row rather than adding a second device. Every failure is swallowed —
 * a denied permission, an insecure origin or no push service must never disturb
 * the app, which still has the in-page notification as a fallback.
 */
export async function registerPush(): Promise<boolean> {
  if (!pushSupported() || !getNotifyEnabled()) return false;
  try {
    const reg = await navigator.serviceWorker.ready;
    const { publicKey } = await api.pushVapid();
    if (!publicKey) return false;

    let sub = await reg.pushManager.getSubscription();
    // A subscription made with a different server key can never be decrypted by
    // this server, so it is replaced rather than reused.
    if (sub && !usesKey(sub, publicKey)) {
      await sub.unsubscribe();
      sub = null;
    }
    if (!sub) {
      sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: b64urlToBytes(publicKey) as BufferSource,
      });
    }
    const json = sub.toJSON();
    await api.pushSubscribe({
      endpoint: sub.endpoint,
      p256dh: json.keys?.p256dh ?? '',
      auth: json.keys?.auth ?? '',
      userAgent: navigator.userAgent,
    });
    return true;
  } catch {
    return false;
  }
}

/** Forget this device, so no further pushes are sent to it. */
export async function unregisterPush(): Promise<void> {
  if (!pushSupported()) return;
  try {
    const reg = await navigator.serviceWorker.ready;
    const sub = await reg.pushManager.getSubscription();
    if (!sub) return;
    await api.pushUnsubscribe(sub.endpoint);
    await sub.unsubscribe();
  } catch {
    // Best effort: the server also drops a subscription the push service
    // reports as gone.
  }
}
