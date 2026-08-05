import hljs from 'highlight.js/lib/core';
import { useMemo, useRef, type KeyboardEvent, type TextareaHTMLAttributes } from 'react';
import { fileLanguage } from '../markdown';

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

/**
 * A lightweight code editor: a syntax-highlighted <pre> sits underneath a
 * transparent-text <textarea> with a visible caret, so editing stays native
 * while the highlighted code shows through. Scroll positions stay in sync.
 */
export default function CodeEditor({
  value,
  onChange,
  path,
  onKeyDown,
  ...rest
}: {
  value: string;
  onChange: (v: string) => void;
  /** File path — used only to pick the highlight language. */
  path?: string;
} & Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, 'value' | 'onChange'>) {
  const preRef = useRef<HTMLPreElement>(null);
  const taRef = useRef<HTMLTextAreaElement>(null);

  const html = useMemo(() => {
    const lang = path ? fileLanguage(path) : null;
    if (!lang || !value) return escapeHtml(value);
    try {
      return hljs.highlight(value, { language: lang }).value;
    } catch {
      return escapeHtml(value);
    }
  }, [value, path]);

  const syncScroll = () => {
    const ta = taRef.current;
    const pre = preRef.current;
    if (!ta || !pre) return;
    pre.scrollTop = ta.scrollTop;
    pre.scrollLeft = ta.scrollLeft;
  };

  const insertTab = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    const ta = taRef.current;
    if (ta && !e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      const { selectionStart, selectionEnd, value: v } = ta;
      const next = v.slice(0, selectionStart) + '  ' + v.slice(selectionEnd);
      onChange(next);
      requestAnimationFrame(() => {
        ta.selectionStart = ta.selectionEnd = selectionStart + 2;
      });
    }
  };

  return (
    <div className="relative h-full w-full overflow-hidden">
      <pre
        ref={preRef}
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 m-0 overflow-hidden whitespace-pre p-3 font-mono text-[13px] leading-5"
        style={{ whiteSpace: 'pre', overflowWrap: 'normal' }}
      >
        <code className="hljs" dangerouslySetInnerHTML={{ __html: html + '\n' }} />
      </pre>
      <textarea
        ref={taRef}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onScroll={syncScroll}
        onKeyDown={(e) => {
          if (e.key === 'Tab') insertTab(e);
          onKeyDown?.(e);
        }}
        spellCheck={false}
        className="absolute inset-0 h-full w-full resize-none overflow-auto bg-transparent p-3 font-mono text-[13px] leading-5 text-transparent caret-text outline-none"
        style={{ whiteSpace: 'pre', overflowWrap: 'normal' }}
        {...rest}
      />
    </div>
  );
}
