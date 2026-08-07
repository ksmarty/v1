import { useEffect } from 'react';

/**
 * iOS standalone PWAs occasionally lay out against stale launch-time viewport
 * bounds: 100dvh and fixed bottom:0 compute too short, so a bottom bar floats
 * above the screen edge until the first touch forces WebKit to recalculate.
 * window.innerHeight is correct immediately, so this pins --v1-app-height to
 * it and keeps it in sync as the viewport changes.
 */
export function useAppHeight() {
  useEffect(() => {
    const set = () => {
      document.documentElement.style.setProperty('--v1-app-height', `${window.innerHeight}px`);
    };
    set();
    window.addEventListener('resize', set);
    window.addEventListener('orientationchange', set);
    return () => {
      window.removeEventListener('resize', set);
      window.removeEventListener('orientationchange', set);
    };
  }, []);
}
