// Mermaid support for markdown replies: render a fenced `mermaid` block to an
// SVG once, cache it, and export it as SVG or a 3x PNG.
//
// Mermaid is heavy (~1.5 MB of parser + renderers), so it is imported
// dynamically: a chat with no diagrams never downloads it, and the chunk is
// fetched the first time the model draws one.
import type { MermaidConfig } from 'mermaid';

export type DiagramTheme = 'dark' | 'light';

// A rendered diagram, keyed by theme + source so a streaming message that
// re-renders its markdown on every token does not re-run mermaid.
const cache = new Map<string, string>();
let mermaidPromise: Promise<typeof import('mermaid').default> | null = null;
let readyFor: DiagramTheme | null = null;
let seq = 0;

function cssColor(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

// The chat is dark by default, but a light theme is possible — mermaid picks
// its palette from the threshold, so read the page background rather than
// hard-coding one.
export function diagramTheme(): DiagramTheme {
  try {
    const bg = getComputedStyle(document.body).backgroundColor;
    const m = /rgba?\(([^)]+)\)/.exec(bg);
    if (!m) return 'dark';
    const [r, g, b, a] = m[1].split(',').map((p) => Number.parseFloat(p));
    if (a === 0) return 'dark';
    const lum = (0.299 * r + 0.587 * g + 0.114 * b) / 255;
    return lum > 0.6 ? 'light' : 'dark';
  } catch {
    return 'dark';
  }
}

async function loadMermaid(theme: DiagramTheme) {
  if (!mermaidPromise) mermaidPromise = import('mermaid').then((m) => m.default);
  const mermaid = await mermaidPromise;
  if (readyFor !== theme) {
    // `htmlLabels: false` keeps every label a real <text> node, so the SVG
    // rasterises to PNG on every engine (a <foreignObject> label draws blank
    // in Safari's canvas).
    const config: MermaidConfig = {
      startOnLoad: false,
      securityLevel: 'strict',
      suppressErrorRendering: true,
      theme: theme === 'dark' ? 'dark' : 'default',
      fontFamily:
        "ui-sans-serif, system-ui, -apple-system, 'Segoe UI', 'Helvetica Neue', sans-serif",
      flowchart: { htmlLabels: false, useMaxWidth: true },
      sequence: { useMaxWidth: true },
      gantt: { useMaxWidth: true },
      er: { useMaxWidth: true },
      themeVariables: {
        background: 'transparent',
        fontFamily:
          "ui-sans-serif, system-ui, -apple-system, 'Segoe UI', 'Helvetica Neue', sans-serif",
      },
    };
    mermaid.initialize(config);
    readyFor = theme;
  }
  return mermaid;
}

// renderDiagram turns mermaid source into SVG markup. The result is memoised,
// so repeated markdown passes (a streaming reply re-renders on every token)
// cost a map lookup.
export async function renderDiagram(code: string): Promise<string> {
  const theme = diagramTheme();
  const key = `${theme}\u0000${code}`;
  const hit = cache.get(key);
  if (hit) return hit;
  const mermaid = await loadMermaid(theme);
  seq += 1;
  const id = `v1-diagram-${Date.now().toString(36)}-${seq}`;
  try {
    const { svg } = await mermaid.render(id, code);
    if (cache.size > 80) cache.clear();
    cache.set(key, svg);
    return svg;
  } finally {
    // mermaid measures in a scratch element; a failed render can leave it
    // behind and it would paint an error diagram into the page.
    document.getElementById(`d${id}`)?.remove();
    document.getElementById(id)?.remove();
  }
}

export function cachedDiagram(code: string): string | null {
  return cache.get(`${diagramTheme()}\u0000${code}`) ?? null;
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
// markup mermaid emits sizes itself with percentages, which an <img> and a
// canvas both need resolved before they will draw it.
function prepared(markup: string): string {
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
// "flowchart TD" + "A[Login] --> B[Home]" exports as "flowchart-diagram.svg".
export function diagramName(code: string): string {
  const first = code.trim().split('\n')[0]?.trim() ?? '';
  const kind = /^(?:flowchart|graph|sequenceDiagram|classDiagram|stateDiagram(?:-v2)?|erDiagram|journey|gantt|pie|quadrantChart|requirementDiagram|gitGraph|mindmap|timeline|sankey|block|packet|xychart)/.exec(
    first,
  )?.[0];
  return `${kind ? kind.toLowerCase() : 'diagram'}`;
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
  // The SVG itself is transparent; paint the chat surface first so the PNG is
  // readable when it lands somewhere white (or dark).
  ctx.fillStyle = cssColor('--v1-bg', diagramTheme() === 'dark' ? '#0b0d10' : '#ffffff');
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
  const blob = await new Promise<Blob | null>((resolve) =>
    canvas.toBlob((b) => resolve(b), 'image/png'),
  );
  if (!blob) throw new Error('The diagram could not be rasterised.');
  saveBlob(blob, `${name}@${scale}x.png`);
}
