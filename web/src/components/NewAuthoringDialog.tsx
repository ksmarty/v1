import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { api } from '../api';
import { errMsg } from '../utils';
import { Button, Dialog, ErrorBox, Spinner } from './ui';

// One project holds every authoring chat for a kind, so building several
// extensions (or several skills) means several sessions in here rather than
// several projects.
type Kind = 'extension' | 'skill';

const CONFIG: Record<
  Kind,
  { project: string; description: string; title: string; placeholder: string; starter: (r: string) => string }
> = {
  extension: {
    project: 'Extensions',
    description: 'Chats that build v1 extensions.',
    title: 'New extension',
    placeholder: 'e.g. a tool that summarises a URL into a few bullet points',
    starter: (request) =>
      `Build a v1 extension for this request:\n\n${request}\n\n` +
      'Use the extension API described in your skills, install it with the create_extension tool, ' +
      'and confirm it loaded.',
  },
  skill: {
    project: 'Skills',
    description: 'Chats that build v1 skills.',
    title: 'New skill',
    placeholder: 'e.g. a checklist for reviewing a pull request',
    starter: (request) =>
      `Create a v1 skill for this request:\n\n${request}\n\n` +
      'Write the SKILL.md, install it with the create_skill tool, and confirm it loaded.',
  },
};

// Sessions are named after the request so several authoring chats stay
// distinguishable in the project's session list.
function sessionName(request: string): string {
  const first = request.split('\n')[0].trim();
  return first.length > 60 ? `${first.slice(0, 57)}...` : first;
}

/**
 * Starts a chat dedicated to writing an extension or a skill. Both are written
 * by the agent, so this is the same shape as creating a project: describe what
 * you want, and the conversation that opens does the work.
 */
export default function NewAuthoringDialog({
  open,
  onClose,
  kind = 'extension',
}: {
  open: boolean;
  onClose: () => void;
  kind?: Kind;
}) {
  const navigate = useNavigate();
  const [request, setRequest] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const config = CONFIG[kind];

  useEffect(() => {
    if (!open) return;
    setRequest('');
    setError(null);
    setBusy(false);
  }, [open]);

  const start = async () => {
    const text = request.trim();
    if (!text || busy) return;
    setBusy(true);
    setError(null);
    try {
      const projects = await api.listProjects();
      const existing = projects.find(
        (p) => p.name.trim().toLowerCase() === config.project.toLowerCase(),
      );
      const project =
        existing ??
        (await api.createProject({
          name: config.project,
          description: config.description,
        }));
      // The account defaults, so the authoring chat starts on the model and
      // thinking level the user actually configured rather than the model's
      // lowest level. This session is created outside the chat page, so nothing
      // else applies them.
      const settings = await api.getSettings().catch(() => null);
      const created = await api.createSession(project.id, sessionName(text));
      const sessionId = created.session.id;
      // Queued rather than sent from the chat page: the turn is already
      // running server-side by the time the page opens, so the reply streams
      // in instead of the message appearing to do nothing.
      await api.queueChat(
        project.id,
        sessionId,
        config.starter(text),
        settings?.llm.defaultModel ?? settings?.llm.model,
        settings?.llm.providers?.[0]?.id,
        settings?.defaultThinking || undefined,
      );
      onClose();
      navigate(`/project/${project.id}?session=${sessionId}`);
    } catch (e) {
      setError(errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title={config.title}>
      <p className="text-sm text-dim">
        Describe what the {kind} should do. A chat opens in the{' '}
        <span className="font-medium text-text">{config.project}</span> project, where the agent
        writes and installs it.
      </p>
      <textarea
        value={request}
        onChange={(e) => setRequest(e.target.value)}
        rows={4}
        autoFocus
        placeholder={config.placeholder}
        className="mt-3 w-full resize-y rounded-lg border border-border bg-surface px-3 py-2 text-sm text-text outline-none focus:border-accent"
      />
      {error && <ErrorBox message={error} className="mt-3" />}
      <div className="mt-4 flex justify-end gap-2">
        <Button variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button onClick={() => void start()} disabled={busy || request.trim() === ''}>
          {busy ? <Spinner className="h-4 w-4" /> : 'Create'}
        </Button>
      </div>
    </Dialog>
  );
}
