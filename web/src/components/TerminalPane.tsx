import { useEffect, useRef, useState } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { Button } from './ui';

export default function TerminalPane({
  projectId,
  active,
}: {
  projectId: string;
  active: boolean;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const connectRef = useRef<() => void>(() => {});
  const retriesRef = useRef(0);
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      theme: {
        background: '#0a0a0a',
        foreground: '#e5e5e5',
        cursor: '#3b82f6',
        selectionBackground: '#264f78',
      },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(container);
    termRef.current = term;
    fitRef.current = fit;
    try {
      fit.fit();
    } catch {
      // container may be hidden on first mount
    }

    const sendResize = () => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
      }
    };

    const connect = () => {
      const prev = wsRef.current;
      if (prev && (prev.readyState === WebSocket.OPEN || prev.readyState === WebSocket.CONNECTING)) {
        return;
      }
      // Relative URL: resolves against the page origin so it inherits the same
      // scheme and host the browser used to load v1 — robust behind reverse
      // proxies (Traefik, Caddy, nginx, Cloudflare) regardless of TLS offload
      // or forwarded-host rewriting.
      const ws = new WebSocket(`/api/projects/${projectId}/terminal`);
      wsRef.current = ws;
      ws.onopen = () => {
        retriesRef.current = 0;
        setConnected(true);
        sendResize();
      };
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data as string) as { type?: string; data?: string };
          if (msg.type === 'output' && typeof msg.data === 'string') {
            term.write(msg.data);
          }
        } catch {
          // ignore malformed frames
        }
      };
      ws.onclose = (ev) => {
        setConnected(false);
        const why = ev.reason || `code ${ev.code}`;
        // A close reason from the server means the shell couldn't start (a
        // terminal-server problem, not a flaky proxy) — don't retry forever.
        const serverRejected = ev.code === 1006 && ev.reason !== '';
        if (serverRejected) {
          term.write(`\r\n\x1b[2m[terminal failed: ${why}]\x1b[0m\r\n`);
          return;
        }
        // Proxies (Cloudflare, Traefik) drop idle/awkward WebSockets —
        // reconnect automatically with backoff instead of dying silently.
        const delay = Math.min(1000 * 2 ** retriesRef.current, 10000);
        retriesRef.current += 1;
        term.write(`\r\n\x1b[2m[connection closed: ${why} — reconnecting…]\x1b[0m\r\n`);
        retryTimerRef.current = setTimeout(connect, delay);
      };
    };
    connectRef.current = () => {
      // Manual reconnect: cancel any pending backoff retry and start fresh.
      if (retryTimerRef.current) {
        clearTimeout(retryTimerRef.current);
        retryTimerRef.current = null;
      }
      retriesRef.current = 0;
      connect();
    };
    connect();

    const dataSub = term.onData((data) => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'input', data }));
      }
    });

    const ro = new ResizeObserver(() => {
      if (container.clientWidth === 0 || container.clientHeight === 0) return;
      try {
        fit.fit();
      } catch {
        return;
      }
      sendResize();
    });
    ro.observe(container);

    return () => {
      ro.disconnect();
      dataSub.dispose();
      connectRef.current = () => {};
      if (retryTimerRef.current) {
        clearTimeout(retryTimerRef.current);
        retryTimerRef.current = null;
      }
      wsRef.current?.close();
      wsRef.current = null;
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, [projectId]);

  // Re-fit when the pane becomes visible (it may have been display:none).
  useEffect(() => {
    if (!active) return;
    const container = containerRef.current;
    const fit = fitRef.current;
    if (container && fit && container.clientWidth > 0) {
      try {
        fit.fit();
      } catch {
        // ignore
      }
    }
    termRef.current?.focus();
  }, [active]);

  return (
    <div className="relative h-full min-h-0 bg-[#0a0a0a] p-1">
      <div ref={containerRef} className="h-full w-full" />
      {!connected && (
        <div className="absolute inset-0 flex items-center justify-center bg-black/60">
          <Button variant="outline" onClick={() => connectRef.current()}>
            Reconnect
          </Button>
        </div>
      )}
    </div>
  );
}
