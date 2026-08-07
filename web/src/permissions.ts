import type { PermissionMode } from './types';

/**
 * Single source of truth for the permission modes' names and colors, shared by
 * the tools dialog (selection cards) and the chat header indicator badge so
 * the selected card outline and the badge always agree.
 */
export interface PermissionMeta {
  id: PermissionMode;
  name: string;
  /** Short label for the header badge. */
  short: string;
  desc: string;
  /** Tooltip for the header badge. */
  title: string;
  /** Classes for the selected option card in the permissions picker. */
  selected: string;
  /** Classes for the header indicator badge. */
  badge: string;
}

export const PERMISSION_MODES: PermissionMeta[] = [
  {
    id: 'ask',
    name: 'Ask for approvals',
    short: 'Ask',
    desc: 'Pause and prompt you before the agent runs tools. You approve or deny each call in the chat.',
    title: 'Permission mode: ask for approvals on each tool call',
    selected: 'border-emerald-900/60 bg-surface ring-1 ring-emerald-500/50 text-emerald-400',
    badge: 'border-emerald-900/60 bg-emerald-950/40 text-emerald-400',
  },
  {
    id: 'auto',
    name: "Don't ask for approvals",
    short: 'Auto',
    desc: 'Auto-approve every tool call. The agent runs commands and edits files without prompting.',
    title: 'Permission mode: auto-approve tool calls without prompting',
    selected: 'border-amber-900/60 bg-surface ring-1 ring-amber-500/50 text-amber-400',
    badge: 'border-amber-900/60 bg-amber-950/40 text-amber-400',
  },
  {
    id: 'yolo',
    name: 'Yolo mode',
    short: 'Yolo',
    desc: 'Full autonomy, no prompts, no checks. The agent can do anything it has access to — use at your own risk.',
    title: 'Permission mode: yolo — no prompts, full autonomy',
    selected: 'border-red-900/60 bg-surface ring-1 ring-red-500/50 text-red-400',
    badge: 'border-red-900/60 bg-red-950/40 text-red-400',
  },
];

export function permissionMeta(mode: PermissionMode): PermissionMeta {
  return PERMISSION_MODES.find((m) => m.id === mode) ?? PERMISSION_MODES[0];
}
