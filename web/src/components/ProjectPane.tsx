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
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setName(project?.name ?? '');
    setPreviewCommand(project?.previewCommand ?? '');
    setInstructions(project?.instructions ?? '');
  }, [project?.id, project?.name, project?.previewCommand, project?.instructions]);

  const save = async (e: FormEvent) => {
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
            <SaveRow saving={saving} saved={saved} error={error} />
          </form>
        </Section>
      </div>
    </div>
  );
}
