import { useEffect } from 'react';

/**
 * iOS standalone PWAs occasionally lay out against stale launch-time viewport
 * bounds: 100dvh and fixed bottom:0 compute too short, so a bottom bar floats
 * above the screen edge until the first touch forces WebKit to recalculate.
 * In standalone mode we read the largest of several metrics (innerHeight,
 * clientHeight, visualViewport.height), capped at the screen size — zoomed-out
 * or expanded states can report a much larger visual viewport and must never
 * grow the shell (the v1 storage keys held such a bogus value, hence v2).
 * The largest plausible value is persisted per orientation and restored before
 * first paint (inline script in index.html), so a cold PWA relaunch starts at
 * the last known-good height instead of the stale one. Outside standalone mode
 * the viewport is reliable, so the height simply tracks the current window
 * (and may shrink). Re-measurements after first paint catch async WebKit
 * corrections.
 */
export function useAppHeight() {
  useEffect(() => {
    const standalone =
      window.matchMedia('(display-mode: standalone)').matches ||
      (window.navigator as { standalone?: boolean }).standalone === true;
    const portrait = () => window.matchMedia('(orientation: portrait)').matches;
    const key = () => `v1-app-height-v2:${portrait() ? 'portrait' : 'landscape'}`;
    const cap = () =>
      portrait()
        ? Math.max(window.screen.width, window.screen.height)
        : Math.min(window.screen.width, window.screen.height);
    const readStored = () => {
      try {
        return Number(localStorage.getItem(key())) || 0;
      } catch {
        return 0;
      }
    };
    // Full-screen standalone (window spans the screen width): the visible
    // height is the screen height, so floor the shell there. Cold-launch
    // metrics and 100dvh can read short — without a stored value that leaves
    // a gap under the bottom bar until the first touch recalibrates WebKit.
    const fullScreen =
      standalone && Math.abs(window.screen.width - window.innerWidth) < 2;
    let best = fullScreen ? Math.max(readStored(), cap()) : standalone ? readStored() : 0;
    const set = () => {
      // Force layout recalc before reading — sometimes nudges WebKit to
      // update stale metrics on iOS standalone PWAs.
      void document.body.offsetHeight;
      let height = Math.max(
        window.innerHeight,
        document.documentElement.clientHeight,
        window.visualViewport?.height ?? 0,
      );
      if (standalone) {
        height = Math.min(height, cap());
        if (height <= best) {
          document.documentElement.style.setProperty('--v1-app-height', `${best}px`);
          return;
        }
        best = height;
        try {
          localStorage.setItem(key(), String(best));
        } catch {
          // storage unavailable (e.g. private mode) — session-only fallback
        }
      } else {
        best = height;
      }
      document.documentElement.style.setProperty('--v1-app-height', `${best}px`);
    };
    set();
    // Re-measure after first paint and short delays: iOS corrects viewport
    // metrics asynchronously after initial PWA launch and reloads.
    const raf = requestAnimationFrame(() => requestAnimationFrame(set));
    const timers = [100, 300, 600, 1000].map((ms) => setTimeout(set, ms));
    const onOrientationChange = () => {
      best = fullScreen ? Math.max(readStored(), cap()) : 0;
      set();
    };
    window.addEventListener('resize', set);
    window.addEventListener('orientationchange', onOrientationChange);
    window.visualViewport?.addEventListener('resize', set);
    // iOS only recalibrates stale standalone viewport metrics once the user
    // interacts — re-measure on touches so any residual gap under the bottom
    // bar heals instead of persisting. Same when the app comes back to the
    // foreground.
    const heal = () => {
      if (document.hidden) return;
      set();
    };
    window.addEventListener('pointerdown', heal);
    window.addEventListener('touchstart', heal);
    // Same story after the app returns to the foreground.
    document.addEventListener('visibilitychange', heal);
    return () => {
      cancelAnimationFrame(raf);
      timers.forEach(clearTimeout);
      window.removeEventListener('resize', set);
      window.removeEventListener('orientationchange', onOrientationChange);
      window.visualViewport?.removeEventListener('resize', set);
      window.removeEventListener('pointerdown', heal);
      window.removeEventListener('touchstart', heal);
      document.removeEventListener('visibilitychange', heal);
    };
  }, []);
}
