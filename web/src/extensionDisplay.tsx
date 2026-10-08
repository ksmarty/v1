import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { api } from './api';
import type { ExtensionToolDisplay } from './types';

/**
 * How extensions want their tool calls presented.
 *
 * An extension declares a title, an icon and which argument identifies a call.
 * v1 learns it from the extension list rather than from each tool call, so it is
 * fetched once and shared through context instead of threaded through every
 * component that renders a tool chip.
 */
const NONE: Record<string, ExtensionToolDisplay> = {};

const ExtensionDisplayContext = createContext<Record<string, ExtensionToolDisplay>>(NONE);

export function ExtensionDisplayProvider({ children }: { children: ReactNode }) {
  const [display, setDisplay] = useState<Record<string, ExtensionToolDisplay>>(NONE);

  useEffect(() => {
    let alive = true;
    api
      .extensions()
      .then((res) => {
        const merged: Record<string, ExtensionToolDisplay> = {};
        for (const ext of res.harness?.loaded ?? []) {
          if (ext.display) Object.assign(merged, ext.display);
        }
        if (alive) setDisplay(merged);
      })
      // A missing list only means extension tools fall back to their own names,
      // which is exactly what an unknown tool does anyway.
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);

  return (
    <ExtensionDisplayContext.Provider value={display}>{children}</ExtensionDisplayContext.Provider>
  );
}

/** The display metadata for one tool, when an extension declared any. */
export function useToolDisplay(name: string): ExtensionToolDisplay | undefined {
  return useContext(ExtensionDisplayContext)[name];
}
