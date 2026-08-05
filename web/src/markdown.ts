import hljs from 'highlight.js/lib/core';
import bash from 'highlight.js/lib/languages/bash';
import c from 'highlight.js/lib/languages/c';
import clojure from 'highlight.js/lib/languages/clojure';
import cpp from 'highlight.js/lib/languages/cpp';
import csharp from 'highlight.js/lib/languages/csharp';
import css from 'highlight.js/lib/languages/css';
import dart from 'highlight.js/lib/languages/dart';
import diff from 'highlight.js/lib/languages/diff';
import dockerfile from 'highlight.js/lib/languages/dockerfile';
import elixir from 'highlight.js/lib/languages/elixir';
import erlang from 'highlight.js/lib/languages/erlang';
import go from 'highlight.js/lib/languages/go';
import gradle from 'highlight.js/lib/languages/gradle';
import graphql from 'highlight.js/lib/languages/graphql';
import haskell from 'highlight.js/lib/languages/haskell';
import http from 'highlight.js/lib/languages/http';
import ini from 'highlight.js/lib/languages/ini';
import java from 'highlight.js/lib/languages/java';
import javascript from 'highlight.js/lib/languages/javascript';
import json from 'highlight.js/lib/languages/json';
import kotlin from 'highlight.js/lib/languages/kotlin';
import less from 'highlight.js/lib/languages/less';
import lua from 'highlight.js/lib/languages/lua';
import makefile from 'highlight.js/lib/languages/makefile';
import markdown from 'highlight.js/lib/languages/markdown';
import nginx from 'highlight.js/lib/languages/nginx';
import objectivec from 'highlight.js/lib/languages/objectivec';
import perl from 'highlight.js/lib/languages/perl';
import php from 'highlight.js/lib/languages/php';
import plaintext from 'highlight.js/lib/languages/plaintext';
import powershell from 'highlight.js/lib/languages/powershell';
import properties from 'highlight.js/lib/languages/properties';
import python from 'highlight.js/lib/languages/python';
import r from 'highlight.js/lib/languages/r';
import ruby from 'highlight.js/lib/languages/ruby';
import rust from 'highlight.js/lib/languages/rust';
import scala from 'highlight.js/lib/languages/scala';
import scss from 'highlight.js/lib/languages/scss';
import sql from 'highlight.js/lib/languages/sql';
import stylus from 'highlight.js/lib/languages/stylus';
import swift from 'highlight.js/lib/languages/swift';
import typescript from 'highlight.js/lib/languages/typescript';
import xml from 'highlight.js/lib/languages/xml';
import yaml from 'highlight.js/lib/languages/yaml';
import { Marked, Renderer, type Tokens } from 'marked';

// Register the languages that actually show up in generated apps. highlight.js
// core ships ~200 grammars; importing them one by one keeps the bundle small.
// Svelte/Vue fall back to XML (script/style blocks still colorize) and TOML to
// INI — close enough syntactically.
hljs.registerLanguage('bash', bash);
hljs.registerLanguage('shell', bash);
hljs.registerLanguage('c', c);
hljs.registerLanguage('clojure', clojure);
hljs.registerLanguage('cpp', cpp);
hljs.registerLanguage('csharp', csharp);
hljs.registerLanguage('css', css);
hljs.registerLanguage('dart', dart);
hljs.registerLanguage('diff', diff);
hljs.registerLanguage('dockerfile', dockerfile);
hljs.registerLanguage('elixir', elixir);
hljs.registerLanguage('erlang', erlang);
hljs.registerLanguage('go', go);
hljs.registerLanguage('gradle', gradle);
hljs.registerLanguage('graphql', graphql);
hljs.registerLanguage('haskell', haskell);
hljs.registerLanguage('html', xml);
hljs.registerLanguage('http', http);
hljs.registerLanguage('ini', ini);
hljs.registerLanguage('java', java);
hljs.registerLanguage('javascript', javascript);
hljs.registerLanguage('json', json);
hljs.registerLanguage('kotlin', kotlin);
hljs.registerLanguage('less', less);
hljs.registerLanguage('lua', lua);
hljs.registerLanguage('makefile', makefile);
hljs.registerLanguage('markdown', markdown);
hljs.registerLanguage('md', markdown);
hljs.registerLanguage('nginx', nginx);
hljs.registerLanguage('objectivec', objectivec);
hljs.registerLanguage('perl', perl);
hljs.registerLanguage('php', php);
hljs.registerLanguage('plaintext', plaintext);
hljs.registerLanguage('powershell', powershell);
hljs.registerLanguage('properties', properties);
hljs.registerLanguage('python', python);
hljs.registerLanguage('r', r);
hljs.registerLanguage('ruby', ruby);
hljs.registerLanguage('rust', rust);
hljs.registerLanguage('scala', scala);
hljs.registerLanguage('scss', scss);
hljs.registerLanguage('sql', sql);
hljs.registerLanguage('stylus', stylus);
hljs.registerLanguage('svelte', xml);
hljs.registerLanguage('swift', swift);
hljs.registerLanguage('toml', ini);
hljs.registerLanguage('tsx', typescript);
hljs.registerLanguage('typescript', typescript);
hljs.registerLanguage('vue', xml);
hljs.registerLanguage('xml', xml);
hljs.registerLanguage('yaml', yaml);

const EXT_LANGS: Record<string, string> = {
  bash: 'bash',
  c: 'c',
  cc: 'cpp',
  cjs: 'javascript',
  clj: 'clojure',
  cljc: 'clojure',
  cljs: 'clojure',
  cpp: 'cpp',
  cs: 'csharp',
  cxx: 'cpp',
  dart: 'dart',
  diff: 'diff',
  dockerfile: 'dockerfile',
  edn: 'clojure',
  erl: 'erlang',
  ex: 'elixir',
  exs: 'elixir',
  gql: 'graphql',
  go: 'go',
  gradle: 'gradle',
  graphql: 'graphql',
  h: 'c',
  hh: 'cpp',
  hpp: 'cpp',
  hrl: 'erlang',
  hs: 'haskell',
  htm: 'html',
  html: 'html',
  hxx: 'cpp',
  ini: 'ini',
  java: 'java',
  js: 'javascript',
  json: 'json',
  jsonc: 'json',
  jsx: 'javascript',
  kt: 'kotlin',
  kts: 'kotlin',
  less: 'less',
  lhs: 'haskell',
  lua: 'lua',
  m: 'objectivec',
  md: 'markdown',
  markdown: 'markdown',
  mjs: 'javascript',
  mm: 'objectivec',
  php: 'php',
  pl: 'perl',
  pm: 'perl',
  properties: 'properties',
  ps1: 'powershell',
  psd1: 'powershell',
  psm1: 'powershell',
  py: 'python',
  r: 'r',
  rb: 'ruby',
  rs: 'rust',
  scala: 'scala',
  scss: 'scss',
  sh: 'bash',
  shell: 'bash',
  sql: 'sql',
  styl: 'stylus',
  svelte: 'svelte',
  svg: 'xml',
  swift: 'swift',
  toml: 'toml',
  ts: 'typescript',
  tsx: 'typescript',
  txt: 'plaintext',
  vue: 'vue',
  xml: 'xml',
  yaml: 'yaml',
  yml: 'yaml',
  zsh: 'bash',
};

/** Resolves the highlight.js language id for a file path, or null. */
export function fileLanguage(path: string): string | null {
  const base = path.split('/').pop() ?? '';
  if (/^dockerfile$/i.test(base)) return 'dockerfile';
  if (/^(?:g?makefile)$/i.test(base)) return 'makefile';
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
