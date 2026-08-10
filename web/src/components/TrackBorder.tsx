import type { TrackSettings } from '../utils';

export const TRACK_PALETTES: Record<string, string[]> = {
  rainbow: ['var(--v1-accent)', '#a855f7', '#ec4899', '#f59e0b'],
  candy: ['#ec4899', '#f472b6', '#a855f7', '#818cf8'],
  ocean: ['#22d3ee', '#3b82f6', '#8b5cf6', '#22d3ee'],
  lava: ['#f59e0b', '#ef4444', '#ec4899', '#f97316'],
  lime: ['#84cc16', '#22d3ee', '#3b82f6', '#84cc16'],
  mono: ['var(--v1-accent)', 'var(--v1-accent)'],
};

export const TRACK_PALETTE_LABELS: Record<string, string> = {
  rainbow: 'Rainbow',
  candy: 'Candy',
  ocean: 'Ocean',
  lava: 'Lava',
  lime: 'Lime',
  mono: 'Accent',
};

// The animated border shown around the composer while the agent runs: an SVG
// rect stroke traveling the box edge. pathLength=100 keeps the lap speed
// uniform; the glow drop-shadow sits on an HTML wrapper because animating
// filter on SVG children is unreliable.
export default function TrackBorder({ ts, id }: { ts: TrackSettings; id: string }) {
  const stops = TRACK_PALETTES[ts.palette] ?? TRACK_PALETTES.rainbow;
  // dashes>1 splits the arc into equal segments with small gaps between them.
  const seg = Math.max(1, ts.arc / ts.dashes - (ts.dashes > 1 ? 2 : 0));
  const dasharray =
    ts.dashes > 1
      ? `${Array.from({ length: ts.dashes }, () => `${seg} 2`).join(' ')} ${100 - ts.arc}`
      : `${ts.arc} ${100 - ts.arc}`;
  return (
    <div
      className={`pointer-events-none absolute inset-0 ${ts.glow ? 'v1-track-glow' : ''}`}
      aria-hidden
    >
      <svg
        className="v1-track-hue h-full w-full"
        style={{ animationDuration: `${ts.hue}s` }}
      >
        <defs>
          <linearGradient id={id} x1="0%" y1="0%" x2="100%" y2="0%">
            {stops.map((c, i) => (
              <stop key={i} offset={`${(i / (stops.length - 1)) * 100}%`} stopColor={c} />
            ))}
          </linearGradient>
        </defs>
        <rect
          className="v1-track-rect"
          style={{
            x: 0.5,
            y: 0.5,
            width: 'calc(100% - 1px)',
            height: 'calc(100% - 1px)',
            animationDuration: `${ts.lap}s`,
            animationDirection: ts.reverse ? 'reverse' : 'normal',
          }}
          rx={ts.radius}
          fill="none"
          stroke={`url(#${id})`}
          strokeWidth={ts.width}
          strokeLinecap="round"
          pathLength={100}
          strokeDasharray={dasharray}
        />
      </svg>
    </div>
  );
}
