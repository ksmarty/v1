import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { api } from '../api';
import { errMsg } from '../utils';
import { Button, Dialog, ErrorBox, Spinner } from './ui';

// One project holds every extension-authoring chat, so building several
// extensions means several sessions in here rather than several projects.
const EXTENSIONS_PROJECT = 'Extensions';

const STARTER = (request: string) =>
  `Build a v1 extension for this request:\n\n${request}\n\n` +
  'Use the extension API described in your skills, install it with the create_extension tool, ' +
  'and confirm it loaded.';

// Sessions are named after the request so several extension chats stay
// distinguishable in the project's session list.
function sessionName(request: string): string {
  const first = request.split('\n')[0].trim();
  return first.length > 60 ? `${first.slice(0, 57)}...` : first;
}

/**
 * Starts a chat dedicated to writing an extension. Extensions are written by
 * the agent, so this is the same shape as creating a project: describe what you
 * want, and the conversation that opens does the work.
 */
export default function NewExtensionDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const [request, setRequest] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

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
        (p) => p.name.trim().toLowerCase() === EXTENSIONS_PROJECT.toLowerCase(),
      );
      const project =
        existing ??
        (await api.createProject({
          name: EXTENSIONS_PROJECT,
          description: 'Chats that build v1 extensions.',
        }));
      const created = await api.createSession(project.id, sessionName(text));
      const sessionId = created.session.id;
      // Queued rather than sent from the chat page: the turn is already
      // running server-side by the time the page opens, so the reply streams
      // in instead of the message appearing to do nothing.
      await api.queueChat(project.id, sessionId, STARTER(text));
      onClose();
      navigate(`/project/${project.id}?session=${sessionId}`);
    } catch (e) {
      setError(errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title="New extension">
      <p className="text-sm text-dim">
        Describe what the extension should do. A chat opens in the{' '}
        <span className="font-medium text-text">{EXTENSIONS_PROJECT}</span> project, where the agent
        writes and installs it.
      </p>
      <textarea
        value={request}
        onChange={(e) => setRequest(e.target.value)}
        rows={4}
        autoFocus
        placeholder="e.g. a tool that summarises a URL into a few bullet points"
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
