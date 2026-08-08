import { useMemo, type MouseEvent } from 'react';
import { renderMarkdown } from '../markdown';

export default function Markdown({
  text,
  streaming,
  validTag,
}: {
  text: string;
  streaming?: boolean;
  validTag?: (tag: string) => boolean;
}) {
  const html = useMemo(() => renderMarkdown(text, streaming, validTag), [text, streaming, validTag]);
  const onCopy = (e: MouseEvent<HTMLDivElement>) => {
    const btn = (e.target as HTMLElement).closest('button[data-copy]');
    if (!(btn instanceof HTMLButtonElement)) return;
    const code = btn.parentElement?.querySelector('code');
    const content = code?.textContent ?? '';
    if (!content) return;
    void navigator.clipboard
      .writeText(content)
      .then(() => {
        const label = btn.textContent;
        btn.textContent = 'Copied';
        window.setTimeout(() => {
          btn.textContent = label;
        }, 1500);
      })
      .catch(() => {
        // clipboard unavailable — ignore
      });
  };
  return <div className="md" onClick={onCopy} dangerouslySetInnerHTML={{ __html: html }} />;
}
