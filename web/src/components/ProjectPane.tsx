import { useEffect, useState, type FormEvent } from 'react';
import { api } from '../api';
import type { Project } from '../types';
import { errMsg } from '../utils';
import { Field, Input, SaveRow, Section, Textarea } from './ui';

type TogglePatch = Partial<Pick<Project, 'autoPush' | 'previewDisabled' | 'vercelEnabled' | 'githubTab'>>;

// Per-project settings: name, preview command, and custom instructions that
// are appended to the agent's system prompt for this project only.
export default function ProjectPane({
  project,
  onProjectChange,
}: {
  project: Project | null;
  onProjectChange: (p: Project) => void;
}) {
  const [name, setName] = useState('');
  const [previewCommand, setPreviewCommand] = useState('');
  const [instructions, setInstructions] = useState('');
  const [autoPush, setAutoPush] = useState(false);
  const [previewDisabled, setPreviewDisabled] = useState(false);
  const [vercelEnabled, setVercelEnabled] = useState(false);
  const [githubTab, setGithubTab] = useState<'auto' | 'on' | 'off'>('auto');
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [toggleBusy, setToggleBusy] = useState(false);

  useEffect(() => {
    setName(project?.name ?? '');
    setPreviewCommand(project?.previewCommand ?? '');
    setInstructions(project?.instructions ?? '');
    setAutoPush(project?.autoPush ?? false);
    setPreviewDisabled(project?.previewDisabled ?? false);
    setVercelEnabled(project?.vercelEnabled ?? false);
    setGithubTab(project?.githubTab ?? 'auto');
  }, [project?.id, project?.name, project?.previewCommand, project?.instructions, project?.autoPush, project?.previewDisabled, project?.vercelEnabled, project?.githubTab]);

  // The Save button only covers the text fields above it.
  const saveText = async (e: FormEvent) => {
    e.preventDefault();
    if (!project) return;
    setSaving(true);
    setSaved(false);
    setError(null);
    try {
      const updated = await api.updateProject(project.id, { name, previewCommand, instructions });
      onProjectChange({ ...project, ...updated });
      setSaved(true);
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setSaving(false);
    }
  };

  // Toggles persist the moment they are clicked.
  const saveToggle = async (patch: TogglePatch) => {
    if (!project || toggleBusy) return;
    setToggleBusy(true);
    setSaved(false);
    setError(null);
    try {
      const updated = await api.updateProject(project.id, patch);
      onProjectChange({ ...project, ...updated });
      setSaved(true);
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setToggleBusy(false);
    }
  };

  // Glow Save only while a text field differs from what's persisted.
  const textDirty = Boolean(
    project &&
      (name !== project.name ||
        previewCommand !== (project.previewCommand ?? '') ||
        instructions !== (project.instructions ?? '')),
  );

  const toggleClass = (active: boolean) =>
    `min-h-[32px] rounded-md text-sm transition-colors disabled:opacity-50 ${
      active ? 'bg-border text-text' : 'text-dim hover:text-text'
    }`;

  return (
    <div className="fade-y h-full overflow-y-auto p-3 md:p-4">
      <div className="mx-auto flex max-w-2xl flex-col gap-4">
        <Section
          title="Project settings"
          description="Only affect this project. Instructions are appended to the agent's system prompt on every turn."
        >
          <form onSubmit={(e) => void saveText(e)} className="flex flex-col gap-3">
            <Field label="Name">
              <Input value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
            </Field>
            <Field label="Preview command (optional override)">
              <Input
                value={previewCommand}
                onChange={(e) => setPreviewCommand(e.target.value)}
                placeholder={project?.defaultPreviewCommand || 'static — no command to run'}
                autoComplete="off"
                className="font-mono text-xs"
              />
            </Field>
            <Field label="Custom instructions">
              <Textarea
                value={instructions}
                onChange={(e) => setInstructions(e.target.value)}
                placeholder="e.g. Always use TypeScript strict mode. Prefer minimal, dark UIs."
                rows={5}
                className="resize-y"
              />
            </Field>
            <SaveRow saving={saving} saved={saved} error={error} pulse={textDirty} />
          </form>

          <div className="mt-4 flex flex-col gap-3 border-t border-border pt-4">
            <Field label="Auto-push commits">
              <div className="grid w-full max-w-[200px] grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
                {([false, true] as const).map((v) => (
                  <button
                    key={String(v)}
                    type="button"
                    disabled={toggleBusy}
                    onClick={() => void saveToggle({ autoPush: v })}
                    className={toggleClass(autoPush === v)}
                  >
                    {v ? 'On' : 'Off'}
                  </button>
                ))}
              </div>
              <p className="mt-1.5 text-xs text-subtle">
                Push this project's finished chat-turn commits to its GitHub
                remote automatically.
              </p>
            </Field>
            <Field label="GitHub tab">
              <div className="grid w-full max-w-[260px] grid-cols-3 gap-1 rounded-lg border border-border bg-surface p-1">
                {(['auto', 'on', 'off'] as const).map((v) => (
                  <button
                    key={v}
                    type="button"
                    disabled={toggleBusy}
                    onClick={() => void saveToggle({ githubTab: v })}
                    className={toggleClass(githubTab === v)}
                  >
                    {v === 'auto' ? 'Auto' : v === 'on' ? 'Always' : 'Never'}
                  </button>
                ))}
              </div>
              <p className="mt-1.5 text-xs text-subtle">
                Whether the chat offers the GitHub tab. Auto shows it when this project's repository
                is on GitHub; use Always to point the tab at any repo, or Never to hide it.
              </p>
            </Field>
            <Field label="Preview">
              <div className="grid w-full max-w-[200px] grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
                {([false, true] as const).map((v) => (
                  <button
                    key={String(v)}
                    type="button"
                    disabled={toggleBusy}
                    onClick={() => void saveToggle({ previewDisabled: v })}
                    className={toggleClass(previewDisabled === v)}
                  >
                    {v ? 'Disabled' : 'Enabled'}
                  </button>
                ))}
              </div>
              <p className="mt-1.5 text-xs text-subtle">
                Hides the preview pane and its bottom-nav button. Useful on
                mobile for extra vertical space.
              </p>
            </Field>
            <Field label="Vercel">
              <div className="grid w-full max-w-[200px] grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
                {([true, false] as const).map((v) => (
                  <button
                    key={String(v)}
                    type="button"
                    disabled={toggleBusy}
                    onClick={() => void saveToggle({ vercelEnabled: v })}
                    className={toggleClass(vercelEnabled === v)}
                  >
                    {v ? 'Enabled' : 'Disabled'}
                  </button>
                ))}
              </div>
              <p className="mt-1.5 text-xs text-subtle">
                Shows the Vercel button in the chat header for deploying this
                project. Off by default.
              </p>
            </Field>
          </div>
        </Section>
      </div>
    </div>
  );
}
