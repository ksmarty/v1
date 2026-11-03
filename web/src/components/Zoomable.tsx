import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { IconMinus, IconPlus } from './icons';

// Zoom bounds. 1x is "fit the box", which is how an image or a diagram opens;
// past 8x a screenshot is a field of pixels and the pan becomes unusable.
const MIN = 1;
const MAX = 8;
const STEP = 1.6;
const DOUBLE_TAP = 2.5;

const SAFE_TOP = 'calc(env(safe-area-inset-top, 0px) + 0.75rem)';

/**
 * A zoomable, pannable surface for the image lightbox and the diagram dialog.
 *
 * Both used to show their content at whatever size the box allowed and nothing
 * else — a screenshot of a UI at 1280px was unreadable on a phone, and a wide
 * diagram could only be squinted at. Zoom is anchored on the pointer, so a
 * wheel or a pinch grows the thing under the finger instead of the middle of
 * the screen, and a drag pans once it is bigger than its box.
 *
 * Touch, trackpad and mouse all drive the same transform: two pointers pinch,
 * one pointer drags, the wheel zooms, a double click toggles between fit and
 * 2.5x. The controls in the corner do the same thing without a gesture.
 */
export default function Zoomable({
  children,
  className = '',
  label,
}: {
  children: ReactNode;
  className?: string;
  /** Accessible name for the controls ("Zoom image", "Zoom diagram"). */
  label?: string;
}) {
  const boxRef = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(MIN);
  const [offset, setOffset] = useState({ x: 0, y: 0 });
  const [dragging, setDragging] = useState(false);
  // The gesture handlers are native listeners or fire between renders, so they
  // read the live transform from refs rather than from the state they closed
  // over.
  const scaleRef = useRef(MIN);
  const offsetRef = useRef({ x: 0, y: 0 });
  scaleRef.current = scale;
  offsetRef.current = offset;

  const pointersRef = useRef(new Map<number, { x: number; y: number }>());
  const pinchRef = useRef<{ dist: number; scale: number } | null>(null);
  const dragRef = useRef<{ x: number; y: number } | null>(null);
  // How far the pointer travelled, so a drag is not mistaken for a tap on the
  // backdrop (which closes the overlay).
  const movedRef = useRef(0);

  const clampScale = (v: number) => Math.min(MAX, Math.max(MIN, v));

  // Keep the content from being dragged entirely out of the box: at 3x there is
  // real content in every direction, at 1x there is nothing to pan.
  const clampOffset = useCallback((o: { x: number; y: number }, s: number) => {
    const el = boxRef.current;
    if (!el) return o;
    const maxX = (el.clientWidth * (s - 1)) / 2;
    const maxY = (el.clientHeight * (s - 1)) / 2;
    return {
      x: Math.min(maxX, Math.max(-maxX, o.x)),
      y: Math.min(maxY, Math.max(-maxY, o.y)),
    };
  }, []);

  const apply = useCallback(
    (s: number, o: { x: number; y: number }) => {
      setScale(s);
      setOffset(clampOffset(o, s));
    },
    [clampOffset],
  );

  /** Scale by `factor`, keeping the point under (clientX, clientY) in place. */
  const zoomAt = useCallback(
    (factor: number, clientX?: number, clientY?: number) => {
      const el = boxRef.current;
      if (!el) return;
      const from = scaleRef.current;
      const to = clampScale(from * factor);
      if (to === from) return;
      if (to === MIN) {
        apply(MIN, { x: 0, y: 0 });
        return;
      }
      const rect = el.getBoundingClientRect();
      // Distance from the centre of the box, which is the transform origin.
      const px = clientX != null ? clientX - rect.left - rect.width / 2 : 0;
      const py = clientY != null ? clientY - rect.top - rect.height / 2 : 0;
      const o = offsetRef.current;
      const k = to / from;
      apply(to, { x: px - (px - o.x) * k, y: py - (py - o.y) * k });
    },
    [apply],
  );

  const reset = useCallback(() => apply(MIN, { x: 0, y: 0 }), [apply]);

  // The wheel has to be a native listener: React attaches wheel handlers
  // passively, and a passive listener cannot stop the page from scrolling
  // underneath the overlay.
  useEffect(() => {
    const el = boxRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      // A trackpad pinch arrives as ctrl+wheel with small deltas, so it needs a
      // steeper response than a mouse wheel notch.
      const factor = Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.002));
      zoomAt(factor, e.clientX, e.clientY);
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, [zoomAt]);

  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    const el = e.currentTarget;
    el.setPointerCapture?.(e.pointerId);
    pointersRef.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    movedRef.current = 0;
    if (pointersRef.current.size === 2) {
      const [a, b] = [...pointersRef.current.values()];
      pinchRef.current = { dist: Math.hypot(a.x - b.x, a.y - b.y), scale: scaleRef.current };
      dragRef.current = null;
      setDragging(true);
    } else if (pointersRef.current.size === 1) {
      dragRef.current = { x: e.clientX, y: e.clientY };
      if (scaleRef.current > MIN) setDragging(true);
    }
  };

  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!pointersRef.current.has(e.pointerId)) return;
    pointersRef.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    const el = boxRef.current;

    if (pointersRef.current.size >= 2) {
      const [a, b] = [...pointersRef.current.values()];
      const pinch = pinchRef.current;
      const dist = Math.hypot(a.x - b.x, a.y - b.y);
      if (!pinch || !el || pinch.dist === 0) return;
      const to = clampScale(pinch.scale * (dist / pinch.dist));
      const rect = el.getBoundingClientRect();
      const px = (a.x + b.x) / 2 - rect.left - rect.width / 2;
      const py = (a.y + b.y) / 2 - rect.top - rect.height / 2;
      const o = offsetRef.current;
      const k = to / scaleRef.current;
      movedRef.current += 10; // a pinch is never a tap
      apply(to, { x: px - (px - o.x) * k, y: py - (py - o.y) * k });
      return;
    }

    const drag = dragRef.current;
    if (!drag) return;
    const dx = e.clientX - drag.x;
    const dy = e.clientY - drag.y;
    dragRef.current = { x: e.clientX, y: e.clientY };
    if (scaleRef.current <= MIN) return; // nothing to pan at fit size
    movedRef.current += Math.abs(dx) + Math.abs(dy);
    apply(scaleRef.current, { x: offsetRef.current.x + dx, y: offsetRef.current.y + dy });
  };

  const endPointer = (e: React.PointerEvent<HTMLDivElement>) => {
    pointersRef.current.delete(e.pointerId);
    if (pointersRef.current.size < 2) pinchRef.current = null;
    const [left] = [...pointersRef.current.values()];
    dragRef.current = left ? { x: left.x, y: left.y } : null;
    if (pointersRef.current.size === 0) setDragging(false);
  };

  return (
    <div
      ref={boxRef}
      className={`relative touch-none overflow-hidden select-none ${className}`}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endPointer}
      onPointerCancel={endPointer}
      onDoubleClick={(e) => zoomAt(scaleRef.current > MIN ? MIN / scaleRef.current : DOUBLE_TAP, e.clientX, e.clientY)}
      // A drag that ends on the backdrop must not count as a click on it, or
      // panning a zoomed screenshot would close the viewer.
      onClick={(e) => {
        if (movedRef.current > 6) e.stopPropagation();
      }}
      style={{ cursor: scale > MIN ? 'grab' : 'zoom-in' }}
    >
      <div
        className="flex h-full w-full items-center justify-center"
        style={{
          transform: `translate3d(${offset.x}px, ${offset.y}px, 0) scale(${scale})`,
          transformOrigin: 'center center',
          transition: dragging ? 'none' : 'transform 140ms ease-out',
          willChange: 'transform',
        }}
      >
        {children}
      </div>

      <div
        style={{ top: SAFE_TOP, left: '0.75rem' }}
        className="absolute z-10 flex items-center gap-1 rounded-lg bg-surface/85 px-1 py-1 text-dim backdrop-blur-sm"
        onClick={(e) => e.stopPropagation()}
        role="group"
        aria-label={label ?? 'Zoom'}
      >
        <button
          type="button"
          onClick={() => zoomAt(1 / STEP)}
          disabled={scale <= MIN}
          aria-label="Zoom out"
          className="inline-flex h-8 w-8 items-center justify-center rounded-md transition-colors enabled:hover:bg-border enabled:hover:text-text disabled:opacity-40"
        >
          <IconMinus className="h-4 w-4" />
        </button>
        <button
          type="button"
          onClick={reset}
          aria-label="Reset zoom"
          className="min-w-[3rem] rounded-md px-1 font-mono text-[11px] tabular-nums transition-colors hover:text-text"
        >
          {Math.round(scale * 100)}%
        </button>
        <button
          type="button"
          onClick={() => zoomAt(STEP)}
          disabled={scale >= MAX}
          aria-label="Zoom in"
          className="inline-flex h-8 w-8 items-center justify-center rounded-md transition-colors enabled:hover:bg-border enabled:hover:text-text disabled:opacity-40"
        >
          <IconPlus className="h-4 w-4" />
        </button>
      </div>
    </div>
  );
}
