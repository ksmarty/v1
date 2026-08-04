import hljs from 'highlight.js/lib/core';
import bash from 'highlight.js/lib/languages/bash';
import css from 'highlight.js/lib/languages/css';
import javascript from 'highlight.js/lib/languages/javascript';
import json from 'highlight.js/lib/languages/json';
import markdown from 'highlight.js/lib/languages/markdown';
import typescript from 'highlight.js/lib/languages/typescript';
import xml from 'highlight.js/lib/languages/xml';
import { Marked, Renderer, type Tokens } from 'marked';

// Only a handful of common languages are registered to keep the bundle small.
hljs.registerLanguage('bash', bash);
hljs.registerLanguage('shell', bash);
hljs.registerLanguage('css', css);
hljs.registerLanguage('html', xml);
hljs.registerLanguage('javascript', javascript);
hljs.registerLanguage('json', json);
hljs.registerLanguage('markdown', markdown);
hljs.registerLanguage('md', markdown);
hljs.registerLanguage('tsx', typescript);
hljs.registerLanguage('typescript', typescript);
hljs.registerLanguage('xml', xml);

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
