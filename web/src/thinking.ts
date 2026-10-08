// Canonical escalation order for thinking levels; unknown levels order by their
// position in the model's list instead.
export const THINKING_LEVEL_RANK: Record<string, number> = {
  off: 0,
  none: 1,
  minimal: 2,
  low: 3,
  medium: 4,
  high: 5,
  xhigh: 6,
  max: 7,
  on: 8,
};

export type ThinkingMeta = { levels: string[]; off: boolean };

// The level a fresh selection gets: the global default when the model supports
// it, otherwise the next highest available level (or the lowest when the default
// sits below everything the model offers). A non-"off" default always turns
// thinking on — it maps to the next available thinking level even when the
// model's list starts with "off".
//
// Shared so every place that picks a level for a model agrees on the rule. They
// did not: the chat escalated a "medium" preference to "high" on a model
// offering off/high/max, while the new-project dialog dropped it to the lowest
// level the model had.
export function freshThinkingLevel(defaultThinking: string, meta: ThinkingMeta): string {
  if (defaultThinking === 'off' && (meta.off || meta.levels.includes('none'))) return 'off';
  if (defaultThinking === '') return meta.levels[0] ?? '';
  if (meta.levels.includes(defaultThinking)) return defaultThinking;
  const reqRank = THINKING_LEVEL_RANK[defaultThinking] ?? -1;
  // The effective level always matches or exceeds the requested default: pick
  // the lowest available level that is at least as strong as it. Only when the
  // model has nothing that strong, settle for its strongest level below the
  // request. On/off rules are unchanged.
  let best = '';
  let bestRank = Infinity;
  let fallback = '';
  let fallbackRank = -Infinity;
  meta.levels.forEach((lvl, i) => {
    const rank = THINKING_LEVEL_RANK[lvl] ?? i + 10;
    // Skip off/none when the default asks for thinking: a non-off default must
    // not land on "off".
    if (defaultThinking !== 'off' && (lvl === 'off' || lvl === 'none')) return;
    if (rank >= reqRank && rank < bestRank) {
      best = lvl;
      bestRank = rank;
    }
    if (rank < reqRank && rank > fallbackRank) {
      fallback = lvl;
      fallbackRank = rank;
    }
  });
  return (
    best ||
    fallback ||
    (defaultThinking !== 'off' ? (meta.levels.find((l) => l !== 'off' && l !== 'none') ?? '') : '') ||
    meta.levels[0] ||
    ''
  );
}
