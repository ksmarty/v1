import { useEffect, useState, type FormEvent } from 'react';
import { api } from '../api';
import type { Project } from '../types';
import { errMsg } from '../utils';
import { Field, Input, SaveRow, Section, Textarea } from './ui';

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
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setName(project?.name ?? '');
    setPreviewCommand(project?.previewCommand ?? '');
    setInstructions(project?.instructions ?? '');
    setAutoPush(project?.autoPush ?? false);
    setPreviewDisabled(project?.previewDisabled ?? false);
  }, [project?.id, project?.name, project?.previewCommand, project?.instructions, project?.autoPush, project?.previewDisabled]);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!project) return;
    setSaving(true);
    setSaved(false);
    setError(null);
    try {
      const updated = await api.updateProject(project.id, { name, previewCommand, instructions, autoPush, previewDisabled });
      onProjectChange({ ...project, ...updated });
      setSaved(true);
    } catch (err) {
      setError(errMsg(err));
    } finally {
      setSaving(false);
    }
  };

  // Glow the Save button while any field differs from what's persisted.
  // Backend omits empty optional fields, so normalize undefined to ''.
  const dirty = Boolean(
    project &&
      (name !== project.name ||
        previewCommand !== (project.previewCommand ?? '') ||
        instructions !== (project.instructions ?? '') ||
        autoPush !== project.autoPush ||
        previewDisabled !== (project.previewDisabled ?? false)),
  );

  return (
    <div className="fade-y h-full overflow-y-auto p-3 md:p-4">
      <div className="mx-auto flex max-w-2xl flex-col gap-4">
        <Section
          title="Project settings"
          description="Only affect this project. Instructions are appended to the agent's system prompt on every turn."
        >
          <form onSubmit={(e) => void save(e)} className="flex flex-col gap-3">
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
            <Field label="Auto-push commits">
              <div className="grid w-full max-w-[200px] grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
                {([false, true] as const).map((v) => (
                  <button
                    key={String(v)}
                    type="button"
                    onClick={() => setAutoPush(v)}
                    className={`min-h-[32px] rounded-md text-sm transition-colors ${
                      autoPush === v ? 'bg-border text-text' : 'text-dim hover:text-text'
                    }`}
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
            <Field label="Preview">
              <div className="grid w-full max-w-[200px] grid-cols-2 gap-1 rounded-lg border border-border bg-surface p-1">
                {([false, true] as const).map((v) => (
                  <button
                    key={String(v)}
                    type="button"
                    onClick={() => setPreviewDisabled(v)}
                    className={`min-h-[36px] rounded-md text-sm transition-colors ${
                      previewDisabled === v ? 'bg-border text-text' : 'text-dim hover:text-text'
                    }`}
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
            <SaveRow saving={saving} saved={saved} error={error} pulse={dirty} />
          </form>
        </Section>
      </div>
    </div>
  );
}
