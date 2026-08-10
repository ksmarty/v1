// Working-border (composer rainbow track) tunables. The Settings UI was
// removed, so these are fixed defaults now.
export type TrackSettings = {
  /** Seconds per lap around the border. */
  lap: number;
  /** Arc length, percent of the circumference. */
  arc: number;
  /** Seconds per hue cycle; 0 = colors stay fixed. */
  hue: number;
  /** Stroke width in px. */
  width: number;
  /** Corner radius in px. */
  radius: number;
  /** Number of arc segments on the track (1 = single comet). */
  dashes: number;
  /** Palette id from TRACK_PALETTES. */
  palette: string;
  /** Counter-clockwise travel. */
  reverse: boolean;
  /** Soft glow around the track. */
  glow: boolean;
};

export const TRACK_DEFAULTS: TrackSettings = {
  lap: 1.2,
  arc: 33,
  hue: 2.5,
  width: 1.5,
  radius: 12,
  dashes: 1,
  palette: 'rainbow',
  reverse: false,
  glow: true,
};

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
  // This wrapper is rendered as a sibling of the overflow-hidden composer box,
  // positioned over it, so nothing clips the track. Strokes are centered on
  // their path, so the path is inset by half the stroke width — that puts the
  // stroke's outer edge exactly on the box edge, on top of the border. The
  // corner radius is reduced by half the stroke the same way, so the outer
  // edge follows the requested radius instead of the path.
  const half = ts.width / 2;
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
            x: half,
            y: half,
            width: `calc(100% - ${ts.width}px)`,
            height: `calc(100% - ${ts.width}px)`,
            animationDuration: `${ts.lap}s`,
            animationDirection: ts.reverse ? 'reverse' : 'normal',
          }}
          rx={Math.max(0, ts.radius - half)}
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
