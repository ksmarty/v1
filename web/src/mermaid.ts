// Mermaid support for markdown replies: render a fenced `mermaid` block to an
// SVG once, cache it (in memory and on disk), and export it as SVG or a 3x PNG.
//
// Mermaid is heavy (~1.5 MB of parser + renderers), so it is imported
// dynamically: a chat with no diagrams never downloads it, and the chunk is
// prefetched on idle so the first diagram does not have to wait for it.
import type { MermaidConfig } from 'mermaid';

// Diagrams are drawn with mermaid's light palette on a white card, in every
// theme. Model-authored diagrams (and their classDef fills) are written for a
// white canvas: rendering them on the dark surface produced pale text on pale
// fills that could not be read.
const FONT =
  "ui-sans-serif, system-ui, -apple-system, 'Segoe UI', 'Helvetica Neue', sans-serif";

// A rendered diagram, keyed by source so a streaming message that re-renders
// its markdown on every token does not re-run mermaid.
const cache = new Map<string, string>();
let mermaidPromise: Promise<typeof import('mermaid').default> | null = null;
let initialized = false;
let seq = 0;

// --- on-disk cache -------------------------------------------------------
//
// A rendered SVG is deterministic, so a reopened chat can paint its diagrams
// before mermaid has even been parsed. The store is small and bounded: a
// couple of dozen diagrams fits comfortably in localStorage, and an entry that
// does not fit is simply not persisted.

const STORE_KEY = 'v1.diagrams.v1';
const STORE_MAX = 24;
const STORE_ENTRY_MAX = 160_000;
type StoredEntry = { code: string; svg: string };
let stored: StoredEntry[] | null = null;

function hash(code: string): string {
  let h = 2166136261;
  for (let i = 0; i < code.length; i += 1) {
    h ^= code.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return (h >>> 0).toString(36);
}

function loadStored(): StoredEntry[] {
  if (stored) return stored;
  try {
    const raw = localStorage.getItem(STORE_KEY);
    const parsed = raw ? (JSON.parse(raw) as StoredEntry[]) : [];
    stored = Array.isArray(parsed)
      ? parsed.filter((e) => e && typeof e.code === 'string' && typeof e.svg === 'string')
      : [];
  } catch {
    stored = [];
  }
  return stored;
}

function persist(code: string, svg: string) {
  if (svg.length > STORE_ENTRY_MAX) return;
  const list = loadStored();
  const idx = list.findIndex((e) => hash(e.code) === hash(code));
  if (idx >= 0) list.splice(idx, 1);
  list.unshift({ code, svg });
  if (list.length > STORE_MAX) list.length = STORE_MAX;
  try {
    localStorage.setItem(STORE_KEY, JSON.stringify(list));
  } catch {
    // Quota or a private-mode refusal: the in-memory cache still works.
  }
}

function storedSvg(code: string): string | null {
  const key = hash(code);
  const hit = loadStored().find((e) => hash(e.code) === key);
  return hit ? hit.svg : null;
}

// --- loading -------------------------------------------------------------

async function loadMermaid() {
  if (!mermaidPromise) mermaidPromise = import('mermaid').then((m) => m.default);
  const mermaid = await mermaidPromise;
  if (!initialized) {
    // `htmlLabels: false` keeps every label a real <text> node, so the SVG
    // rasterises to PNG on every engine (a <foreignObject> label draws blank
    // in Safari's canvas).
    const config: MermaidConfig = {
      startOnLoad: false,
      securityLevel: 'strict',
      suppressErrorRendering: true,
      theme: 'default',
      fontFamily: FONT,
      flowchart: { htmlLabels: false, useMaxWidth: true },
      sequence: { useMaxWidth: true },
      gantt: { useMaxWidth: true },
      er: { useMaxWidth: true },
      themeVariables: {
        fontFamily: FONT,
        fontSize: '14px',
        background: '#ffffff',
        darkMode: false,
        primaryColor: '#eef2ff',
        primaryTextColor: '#0f172a',
        primaryBorderColor: '#818cf8',
        secondaryColor: '#f1f5f9',
        secondaryTextColor: '#0f172a',
        tertiaryColor: '#f8fafc',
        tertiaryTextColor: '#0f172a',
        mainBkg: '#eef2ff',
        nodeBorder: '#818cf8',
        nodeTextColor: '#0f172a',
        textColor: '#0f172a',
        titleColor: '#0f172a',
        lineColor: '#475569',
        clusterBkg: '#f8fafc',
        clusterBorder: '#cbd5e1',
        edgeLabelBackground: '#ffffff',
        labelBackground: '#ffffff',
        noteBkgColor: '#fef9c3',
        noteTextColor: '#422006',
        noteBorderColor: '#eab308',
        actorBkg: '#eef2ff',
        actorBorder: '#818cf8',
        actorTextColor: '#0f172a',
        signalColor: '#475569',
        signalTextColor: '#0f172a',
      },
    };
    mermaid.initialize(config);
    initialized = true;
  }
  return mermaid;
}

// prefetchMermaid starts the download while the app is idle, so the first
// diagram in a conversation renders without a visible wait.
export function prefetchMermaid() {
  if (mermaidPromise) return;
  const start = () => {
    void loadMermaid().catch(() => undefined);
  };
  const idle = (window as unknown as { requestIdleCallback?: (cb: () => void, o?: object) => void })
    .requestIdleCallback;
  if (typeof idle === 'function') idle(start, { timeout: 4000 });
  else window.setTimeout(start, 1500);
}

// renderDiagram turns mermaid source into SVG markup. The result is memoised,
// so repeated markdown passes (a streaming reply re-renders on every token)
// cost a map lookup.
export async function renderDiagram(code: string): Promise<string> {
  const hit = cache.get(code);
  if (hit) return hit;
  const mermaid = await loadMermaid();
  seq += 1;
  const id = `v1-diagram-${Date.now().toString(36)}-${seq}`;
  try {
    const { svg } = await mermaid.render(id, code);
    if (cache.size > 80) cache.clear();
    cache.set(code, svg);
    persist(code, svg);
    return svg;
  } finally {
    // mermaid measures in a scratch element; a failed render can leave it
    // behind and it would paint an error diagram into the page.
    document.getElementById(`d${id}`)?.remove();
    document.getElementById(id)?.remove();
  }
}

// cachedDiagram is the synchronous lookup used while rendering markdown: a
// diagram that has been drawn before (this session or a previous one) is
// inline from the very first paint, with no placeholder frame.
export function cachedDiagram(code: string): string | null {
  return cache.get(code) ?? storedSvg(code);
}

// --- export -------------------------------------------------------------

type Size = { w: number; h: number };

function svgSize(markup: string): Size | null {
  const tag = /<svg\b[^>]*>/.exec(markup)?.[0] ?? '';
  const box = /viewBox="([^"]+)"/.exec(tag)?.[1];
  if (box) {
    const parts = box.trim().split(/[\s,]+/).map(Number);
    if (parts.length === 4 && parts[2] > 0 && parts[3] > 0) {
      return { w: Math.round(parts[2]), h: Math.round(parts[3]) };
    }
  }
  const w = Number.parseFloat(/width="([\d.]+)"/.exec(tag)?.[1] ?? '');
  const h = Number.parseFloat(/height="([\d.]+)"/.exec(tag)?.[1] ?? '');
  if (Number.isFinite(w) && Number.isFinite(h) && w > 0 && h > 0) {
    return { w: Math.round(w), h: Math.round(h) };
  }
  return null;
}

// prepared gives the root <svg> a namespace and fixed pixel dimensions: the
// markup mermaid emits sizes itself with percentages, which an <img>, a canvas
// and a flex container all resolve to nothing before they will draw it.
export function prepared(markup: string): string {
  const size = svgSize(markup) ?? { w: 1024, h: 768 };
  return markup.replace(/<svg\b[^>]*>/, (tag) => {
    let t = tag;
    if (!/\sxmlns=/.test(t)) {
      t = t.replace('<svg', '<svg xmlns="http://www.w3.org/2000/svg"');
    }
    if (/\swidth="[^"]*"/.test(t)) t = t.replace(/\swidth="[^"]*"/, ` width="${size.w}"`);
    else t = t.replace('<svg', `<svg width="${size.w}"`);
    if (/\sheight="[^"]*"/.test(t)) t = t.replace(/\sheight="[^"]*"/, ` height="${size.h}"`);
    else t = t.replace('<svg', `<svg height="${size.h}"`);
    // mermaid's inline max-width would make the export shrink to the dialog
    // width; the export needs its natural size.
    t = t.replace(/\sstyle="[^"]*"/, '');
    return t;
  });
}

function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.rel = 'noopener';
  document.body.appendChild(a);
  a.click();
  a.remove();
  // Revoking immediately can cancel the download in Safari.
  window.setTimeout(() => URL.revokeObjectURL(url), 4000);
}

// diagramName turns the first line of the source into a filename, so
// "flowchart TD" + "A[Login] --> B[Home]" exports as "flowchart.svg".
export function diagramName(code: string): string {
  const first = code.trim().split('\n')[0]?.trim() ?? '';
  const kind =
    /^(?:flowchart|graph|sequenceDiagram|classDiagram|stateDiagram(?:-v2)?|erDiagram|journey|gantt|pie|quadrantChart|requirementDiagram|gitGraph|mindmap|timeline|sankey|block|packet|xychart)(?:[\s-]+\w+)?/.exec(
      first,
    )?.[0];
  if (!kind) return 'diagram';
  // "flowchart TD" reads as "flowchart-td" in a filename.
  return kind.toLowerCase().replace(/\s+/g, '-');
}

export function exportSvg(markup: string, name = 'diagram'): void {
  const blob = new Blob([prepared(markup)], { type: 'image/svg+xml;charset=utf-8' });
  saveBlob(blob, `${name}.svg`);
}

// exportPng rasterises the SVG at a multiple of its natural size. 3x keeps a
// pasted diagram legible on a retina screen or in a slide deck.
export async function exportPng(
  markup: string,
  name = 'diagram',
  scale = 3,
): Promise<void> {
  const svg = prepared(markup);
  const size = svgSize(svg) ?? { w: 1024, h: 768 };
  const url = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
  const img = new Image();
  img.decoding = 'sync';
  await new Promise<void>((resolve, reject) => {
    img.onload = () => resolve();
    img.onerror = () => reject(new Error('The diagram could not be rasterised.'));
    img.src = url;
  });
  const canvas = document.createElement('canvas');
  canvas.width = Math.max(1, Math.round(size.w * scale));
  canvas.height = Math.max(1, Math.round(size.h * scale));
  const ctx = canvas.getContext('2d');
  if (!ctx) throw new Error('This browser cannot export images.');
  // The SVG itself is transparent; paint the same white card the diagram is
  // drawn on so the PNG is readable wherever it lands.
  ctx.fillStyle = '#ffffff';
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
  const blob = await new Promise<Blob | null>((resolve) =>
    canvas.toBlob((b) => resolve(b), 'image/png'),
  );
  if (!blob) throw new Error('The diagram could not be rasterised.');
  saveBlob(blob, `${name}@${scale}x.png`);
}
