import hljs from 'highlight.js/lib/core';
import bash from 'highlight.js/lib/languages/bash';
import css from 'highlight.js/lib/languages/css';
import dockerfile from 'highlight.js/lib/languages/dockerfile';
import go from 'highlight.js/lib/languages/go';
import ini from 'highlight.js/lib/languages/ini';
import javascript from 'highlight.js/lib/languages/javascript';
import json from 'highlight.js/lib/languages/json';
import markdown from 'highlight.js/lib/languages/markdown';
import python from 'highlight.js/lib/languages/python';
import rust from 'highlight.js/lib/languages/rust';
import scss from 'highlight.js/lib/languages/scss';
import sql from 'highlight.js/lib/languages/sql';
import typescript from 'highlight.js/lib/languages/typescript';
import xml from 'highlight.js/lib/languages/xml';
import yaml from 'highlight.js/lib/languages/yaml';
import { Marked, Renderer, type Tokens } from 'marked';

// A handful of common languages are registered to keep the bundle small.
hljs.registerLanguage('bash', bash);
hljs.registerLanguage('shell', bash);
hljs.registerLanguage('css', css);
hljs.registerLanguage('dockerfile', dockerfile);
hljs.registerLanguage('go', go);
hljs.registerLanguage('html', xml);
hljs.registerLanguage('ini', ini);
hljs.registerLanguage('javascript', javascript);
hljs.registerLanguage('json', json);
hljs.registerLanguage('markdown', markdown);
hljs.registerLanguage('md', markdown);
hljs.registerLanguage('python', python);
hljs.registerLanguage('rust', rust);
hljs.registerLanguage('scss', scss);
hljs.registerLanguage('sql', sql);
hljs.registerLanguage('tsx', typescript);
hljs.registerLanguage('typescript', typescript);
hljs.registerLanguage('xml', xml);
hljs.registerLanguage('yaml', yaml);

const EXT_LANGS: Record<string, string> = {
  bash: 'bash',
  cjs: 'javascript',
  css: 'css',
  dockerfile: 'dockerfile',
  go: 'go',
  htm: 'html',
  html: 'html',
  ini: 'ini',
  js: 'javascript',
  json: 'json',
  jsonc: 'json',
  jsx: 'javascript',
  mjs: 'javascript',
  md: 'markdown',
  markdown: 'markdown',
  py: 'python',
  rs: 'rust',
  scss: 'scss',
  sh: 'bash',
  shell: 'bash',
  sql: 'sql',
  svg: 'xml',
  ts: 'typescript',
  tsx: 'typescript',
  xml: 'xml',
  yaml: 'yaml',
  yml: 'yaml',
  zsh: 'bash',
};

/** Resolves the highlight.js language id for a file path, or null. */
export function fileLanguage(path: string): string | null {
  const base = path.split('/').pop() ?? '';
  if (/^dockerfile$/i.test(base)) return 'dockerfile';
  const dot = base.lastIndexOf('.');
  if (dot <= 0) return null;
  return EXT_LANGS[base.slice(dot + 1).toLowerCase()] ?? null;
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

const renderer = new Renderer();

renderer.code = ({ text, lang, escaped }: Tokens.Code): string => {
  let body = text;
  if (!escaped) {
    if (lang && hljs.getLanguage(lang)) {
      try {
        body = hljs.highlight(text, { language: lang }).value;
      } catch {
        body = escapeHtml(text);
      }
    } else {
      body = escapeHtml(text);
    }
  }
  const cls = lang ? ` class="hljs language-${escapeHtml(lang)}"` : '';
  const copy =
    '<button type="button" data-copy aria-label="Copy code" ' +
    'class="absolute right-2 top-2 z-10 rounded-md border border-border bg-surface px-1.5 py-0.5 ' +
    'font-sans text-[10px] text-dim opacity-100 transition-colors hover:text-text ' +
    'md:opacity-0 md:group-hover:opacity-100 md:focus-visible:opacity-100">Copy</button>';
  return `<pre class="group relative"><code${cls}>${body}</code>${copy}</pre>`;
};

// Escape raw HTML so model output cannot inject markup/scripts.
renderer.html = ({ text }: Tokens.HTML | Tokens.Tag): string => escapeHtml(text);

// Drop javascript: and other non-http(s) links.
renderer.link = ({ href, title, tokens }: Tokens.Link): string => {
  const text = renderer.parser.parseInline(tokens);
  if (href && /^(?:https?:|mailto:|#|\/)/i.test(href)) {
    return `<a href="${escapeHtml(href)}"${title ? ` title="${escapeHtml(title)}"` : ''}>${text}</a>`;
  }
  return text;
};

const marked = new Marked({ renderer, gfm: true });

// Appended to the raw source of a streaming message before rendering, then
// swapped for a blinking caret span in the output so it tracks the last token.
const CURSOR_MARKER = '\u200b\u200bV1CURSOR\u200b\u200b';
const CURSOR_SPAN = '<span class="v1-stream-cursor" />';

export function renderMarkdown(src: string, streaming = false): string {
  // Only append the blinking caret while there is actual text on screen — an
  // empty assistant bubble (e.g. between tool steps) should not blink.
  const withCursor = streaming && src.trim() !== '';
  const html = marked.parse(withCursor ? src + CURSOR_MARKER : src, { async: false });
  return html.replaceAll(CURSOR_MARKER, CURSOR_SPAN);
}
