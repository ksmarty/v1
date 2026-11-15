import { useEffect, useState } from 'react';
import type { ProviderUsage, ProviderUsageWindow } from '../types';

/**
 * Concentric plan-usage rings. The outermost ring is the shortest window
 * (5 hour) and the innermost the longest (monthly), so a binding short-term
 * limit — the one that actually stops you mid-afternoon — reads first.
 *
 * Radii are larger than the context ring's (r=7.5 in an 18px box) because three
 * rings have to fit inside the same square: at 11.5/8/4.5 with a 1.8 stroke
 * they stay separated by ~1.7px.
 */
const RADII = [11.5, 8, 4.5];
const VIEW = 28;
const CENTER = VIEW / 2;
const STROKE = 1.8;

/**
 * Hues shared with the context ring: green when the allowance is fresh, red
 * when it is spent. The two buttons therefore mean the same thing at a glance —
 * wide green ring, plenty left.
 */
export function usageHue(pct: number): number {
  return 120 - (120 * Math.max(0, Math.min(100, pct))) / 100;
}

export function usageColor(pct: number): string {
  return `hsl(${usageHue(pct)} 70% 50%)`;
}

/** The window closest to its limit — what the button's border is tinted by. */
export function worstWindow(windows?: ProviderUsageWindow[]): ProviderUsageWindow | undefined {
  let worst: ProviderUsageWindow | undefined;
  for (const w of windows ?? []) {
    if (!worst || w.percent > worst.percent) worst = w;
  }
  return worst;
}

/** "5 hour 42% · Weekly 17% · Monthly 25%", for the button's tooltip. */
export function usageSummary(windows?: ProviderUsageWindow[]): string {
  if (!windows || windows.length === 0) return 'no usage reported';
  return windows
    .map((w) => `${w.label} ${Math.round(w.percent)}%`)
    .join(' · ');
}

/** Human countdown to a window's reset: "resets in 3 h". */
export function resetLabel(iso?: string): string {
  if (!iso) return '';
  const at = Date.parse(iso);
  if (!Number.isFinite(at)) return '';
  const ms = at - Date.now();
  if (ms <= 0) return 'resets now';
  const mins = Math.round(ms / 60000);
  if (mins < 60) return `resets in ${mins} min`;
  const hours = Math.round(mins / 60);
  if (hours < 48) return `resets in ${hours} h`;
  return `resets in ${Math.round(hours / 24)} d`;
}

/** A short reason the reading failed, for the button's tooltip. */
export function usageError(usage: ProviderUsage | null): string {
  if (!usage) return 'usage loading…';
  if (!usage.supported) return 'This provider reports no plan usage';
  if (usage.error) return `usage unavailable: ${usage.error}`;
  return 'no usage reported';
}

export function UsageRings({
  windows,
  size = VIEW,
  className = '',
}: {
  windows?: ProviderUsageWindow[];
  size?: number;
  className?: string;
}) {
  // Sweep on mount rather than snapping to the value, matching the context
  // ring: the animation is what says "this is a measurement, not decoration".
  const [shown, setShown] = useState(false);
  useEffect(() => {
    const id = requestAnimationFrame(() => setShown(true));
    return () => cancelAnimationFrame(id);
  }, []);

  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${VIEW} ${VIEW}`}
      className={`-rotate-90 ${className}`}
      aria-hidden="true"
    >
      {RADII.map((r, i) => {
        const circ = 2 * Math.PI * r;
        const pct = windows?.[i]?.percent;
        return (
          <g key={r}>
            <circle
              cx={CENTER}
              cy={CENTER}
              r={r}
              fill="none"
              strokeWidth={STROKE}
              className="stroke-border"
            />
            {pct !== undefined && (
              <circle
                cx={CENTER}
                cy={CENTER}
                r={r}
                fill="none"
                strokeWidth={STROKE}
                stroke={usageColor(pct)}
                strokeDasharray={circ}
                strokeDashoffset={shown ? circ * (1 - Math.min(100, Math.max(0, pct)) / 100) : circ}
                strokeLinecap="round"
                style={{
                  transition: 'stroke-dashoffset 700ms ease-out, stroke 700ms ease',
                }}
              />
            )}
          </g>
        );
      })}
    </svg>
  );
}
