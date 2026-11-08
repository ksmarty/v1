// The Appearance page is account-scoped: the server is the source of truth, and
// this module keeps a localStorage mirror of it on the device.
//
// The mirror is not redundancy — it is what makes the settings usable
// synchronously. The theme has to apply before the first paint, the chat side and
// the tab bar are read during render, and the chat asks how it should render on
// every row. So reads stay synchronous (`localAppearance()`), a change writes the
// mirror and the server, and the owner's values land from `hydrateAppearance()`
// once the app has a session.
import { api } from './api';
import { debugError } from './debuglog';
import { applyTheme, getStoredTheme, setStoredTheme } from './themes';
import type { AppearanceSettings } from './types';
import {
  getChatSide,
  getChatTabLayout,
  getJsonPretty,
  getThinkingCollapsed,
  getToolCallsCollapsed,
  getTurnFoldEnabled,
  setChatSide,
  setChatTabLayout,
  setJsonPretty,
  setThinkingCollapsed,
  setToolCallsCollapsed,
  setTurnFoldEnabled,
  type ChatTab,
} from './utils';

type Listener = (a: AppearanceSettings) => void;

const listeners = new Set<Listener>();

/** Notifies subscribers when the account's appearance is (re)applied. */
export function subscribeAppearance(fn: Listener): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

/** The appearance as this device knows it: the mirror, or the defaults. */
export function localAppearance(): AppearanceSettings {
  const tabs = getChatTabLayout();
  return {
    theme: getStoredTheme(),
    chatSide: getChatSide(),
    chatTabs: { order: tabs.order, hidden: tabs.hidden },
    jsonPretty: getJsonPretty(),
    toolCallsCollapsed: getToolCallsCollapsed(),
    turnFold: getTurnFoldEnabled(),
    thinkingCollapsed: getThinkingCollapsed(),
  };
}

/** Writes the given knobs to the mirror and applies the theme. */
export function applyAppearance(patch: Partial<AppearanceSettings>): AppearanceSettings {
  if (patch.theme !== undefined && patch.theme !== '' && patch.theme !== getStoredTheme()) {
    setStoredTheme(patch.theme);
    applyTheme(patch.theme);
  }
  if (patch.chatSide !== undefined) setChatSide(patch.chatSide);
  if (patch.chatTabs !== undefined) {
    const order = patch.chatTabs.order.filter((id): id is ChatTab => isChatTab(id));
    // An empty order means "the built-in order" (the server's default), not an
    // empty tab bar — `getChatTabLayout` treats an empty array as a real order.
    setChatTabLayout({
      order: order.length > 0 ? order : (CHAT_TABS as ChatTab[]),
      hidden: patch.chatTabs.hidden.filter((id): id is ChatTab => isChatTab(id)),
    });
  }
  if (patch.jsonPretty !== undefined) setJsonPretty(patch.jsonPretty);
  if (patch.toolCallsCollapsed !== undefined) setToolCallsCollapsed(patch.toolCallsCollapsed);
  if (patch.turnFold !== undefined) setTurnFoldEnabled(patch.turnFold);
  if (patch.thinkingCollapsed !== undefined) setThinkingCollapsed(patch.thinkingCollapsed);
  const next = localAppearance();
  for (const fn of listeners) fn(next);
  return next;
}

const CHAT_TABS: string[] = ['chat', 'files', 'terminal', 'git', 'memories', 'github', 'project'];

function isChatTab(id: string): boolean {
  return CHAT_TABS.includes(id);
}

let hydration: Promise<void> | null = null;

/**
 * Pulls the account's appearance and applies it. Called once at startup; a
 * failure (offline, no session yet, an older server) leaves the mirror in place,
 * which is exactly what makes the settings usable before the fetch lands.
 */
export function hydrateAppearance(): Promise<void> {
  hydration ??= api
    .getSettings()
    .then((s) => {
      const a = s.appearance;
      if (!a) return;
      applyAppearance({
        theme: a.theme,
        chatSide: a.chatSide,
        chatTabs: a.chatTabs,
        jsonPretty: a.jsonPretty,
        toolCallsCollapsed: a.toolCallsCollapsed,
        turnFold: a.turnFold,
        thinkingCollapsed: a.thinkingCollapsed,
      });
    })
    .catch((e) => {
      debugError('appearance', e);
    });
  return hydration;
}

/**
 * Saves one Appearance change: the device updates immediately and the account
 * follows. A failed save is reported to the diagnostics log rather than
 * blocking the UI — the mirror still holds the choice for this device.
 */
export async function saveAppearance(patch: Partial<AppearanceSettings>): Promise<void> {
  applyAppearance(patch);
  try {
    await api.updateSettings({ appearance: patch });
  } catch (e) {
    debugError('appearance', e, { patch });
  }
}
