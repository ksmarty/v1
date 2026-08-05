import { useCallback, useEffect, useRef, useState } from 'react';
import { api } from '../api';
import type { FileEntry } from '../types';
import { errMsg, formatBytes } from '../utils';
import { useMediaQuery } from '../hooks/useMediaQuery';
import { Button, ErrorBox, IconButton, Spinner } from './ui';
import {
  IconArrowLeft,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconFile,
  IconFolder,
  IconFolderOpen,
  IconRefresh,
} from './icons';

interface TreeNode {
  entry: FileEntry;
  children: TreeNode[] | null; // null = not loaded yet
  expanded: boolean;
  loading: boolean;
}

function sortEntries(entries: FileEntry[]): FileEntry[] {
  return [...entries].sort((a, b) => {
    const ad = a.type === 'dir';
    const bd = b.type === 'dir';
    if (ad !== bd) return ad ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
}

function toNode(entry: FileEntry): TreeNode {
  return { entry, children: null, expanded: false, loading: false };
}

function updateTree(
  nodes: TreeNode[],
  path: string,
  fn: (n: TreeNode) => TreeNode,
): TreeNode[] {
  return nodes.map((n) => {
    if (n.entry.path === path) return fn(n);
    if (n.children) return { ...n, children: updateTree(n.children, path, fn) };
    return n;
  });
}

function TreeView({
  nodes,
  depth,
  selectedPath,
  onToggleDir,
  onOpenFile,
}: {
  nodes: TreeNode[];
  depth: number;
  selectedPath: string | null;
  onToggleDir: (n: TreeNode) => void;
  onOpenFile: (e: FileEntry) => void;
}) {
  return (
    <>
      {nodes.map((n) => {
        const isDir = n.entry.type === 'dir';
        const selected = !isDir && n.entry.path === selectedPath;
        return (
          <div key={n.entry.path}>
            <button
              type="button"
              onClick={() => (isDir ? onToggleDir(n) : onOpenFile(n.entry))}
              className={`flex min-h-[36px] w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[13px] transition-colors md:min-h-[28px] md:py-1 ${
                selected
                  ? 'bg-border text-text'
                  : 'text-dim hover:bg-surface hover:text-text'
              }`}
              style={{ paddingLeft: `${depth * 14 + 8}px` }}
            >
              {isDir ? (
                <>
                  {n.loading ? (
                    <Spinner className="h-3.5 w-3.5 shrink-0" />
                  ) : n.expanded ? (
                    <IconChevronDown className="h-3.5 w-3.5 shrink-0 text-faint" />
                  ) : (
                    <IconChevronRight className="h-3.5 w-3.5 shrink-0 text-faint" />
                  )}
                  {n.expanded ? (
                    <IconFolderOpen className="h-4 w-4 shrink-0 text-subtle" />
                  ) : (
                    <IconFolder className="h-4 w-4 shrink-0 text-subtle" />
                  )}
                </>
              ) : (
                <>
                  <span className="w-3.5 shrink-0" />
                  <IconFile className="h-4 w-4 shrink-0 text-faint" />
                </>
              )}
              <span className="truncate">{n.entry.name}</span>
            </button>
            {isDir && n.expanded && n.children && (
              <TreeView
                nodes={n.children}
                depth={depth + 1}
                selectedPath={selectedPath}
                onToggleDir={onToggleDir}
                onOpenFile={onOpenFile}
              />
            )}
          </div>
        );
      })}
    </>
  );
}

export default function FilesPane({ projectId }: { projectId: string }) {
  const isDesktop = useMediaQuery('(min-width: 768px)');
  const [root, setRoot] = useState<TreeNode[] | null>(null);
  const [treeError, setTreeError] = useState<string | null>(null);
  const [selected, setSelected] = useState<{ path: string; size: number } | null>(null);
  const [content, setContent] = useState('');
  const [savedContent, setSavedContent] = useState('');
  const [fileLoading, setFileLoading] = useState(false);
  const [fileError, setFileError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [savedFlash, setSavedFlash] = useState(false);
  const expandedRef = useRef<Set<string>>(new Set());

  const dirty = content !== savedContent;

  const loadRoot = useCallback(async () => {
    try {
      const res = await api.listFiles(projectId, '');
      setRoot(sortEntries(res.entries).map(toNode));
      setTreeError(null);
    } catch (e) {
      setTreeError(errMsg(e));
    }
  }, [projectId]);

  useEffect(() => {
    void loadRoot();
  }, [loadRoot]);

  // Re-fetch the root and every expanded directory in the background so newly
  // written files appear without a manual refresh. Selected-file content is
  // left untouched to avoid clobbering an in-progress edit.
  const refreshTree = useCallback(async () => {
    try {
      const res = await api.listFiles(projectId, '');
      let nodes = sortEntries(res.entries).map(toNode);
      for (const path of Array.from(expandedRef.current)) {
        try {
          const r = await api.listFiles(projectId, path);
          nodes = updateTree(nodes, path, (n) => ({
            ...n,
            children: sortEntries(r.entries).map(toNode),
            expanded: true,
          }));
        } catch {
          // the directory may have been removed — leave it collapsed
        }
      }
      setRoot(nodes);
    } catch {
      // transient failure — keep the previous tree
    }
  }, [projectId]);

  useEffect(() => {
    const t = setInterval(() => void refreshTree(), 4000);
    return () => clearInterval(t);
  }, [refreshTree]);

  const toggleDir = async (node: TreeNode) => {
    if (node.expanded) {
      expandedRef.current.delete(node.entry.path);
      setRoot((prev) =>
        prev ? updateTree(prev, node.entry.path, (n) => ({ ...n, expanded: false })) : prev,
      );
      return;
    }
    if (node.children !== null) {
      expandedRef.current.add(node.entry.path);
      setRoot((prev) =>
        prev ? updateTree(prev, node.entry.path, (n) => ({ ...n, expanded: true })) : prev,
      );
      return;
    }
    setRoot((prev) =>
      prev ? updateTree(prev, node.entry.path, (n) => ({ ...n, loading: true })) : prev,
    );
    try {
      const res = await api.listFiles(projectId, node.entry.path);
      expandedRef.current.add(node.entry.path);
      setRoot((prev) =>
        prev
          ? updateTree(prev, node.entry.path, (n) => ({
              ...n,
              children: sortEntries(res.entries).map(toNode),
              expanded: true,
              loading: false,
            }))
          : prev,
      );
    } catch (e) {
      setRoot((prev) =>
        prev ? updateTree(prev, node.entry.path, (n) => ({ ...n, loading: false })) : prev,
      );
      setTreeError(errMsg(e));
    }
  };

  const openFile = async (entry: FileEntry) => {
    setSelected({ path: entry.path, size: entry.size });
    setFileLoading(true);
    setFileError(null);
    setSavedFlash(false);
    try {
      const res = await api.readFile(projectId, entry.path);
      setContent(res.content);
      setSavedContent(res.content);
    } catch (e) {
      setFileError(errMsg(e));
      setContent('');
      setSavedContent('');
    } finally {
      setFileLoading(false);
    }
  };

  const save = useCallback(async () => {
    if (!selected || saving || content === savedContent) return;
    setSaving(true);
    setFileError(null);
    try {
      await api.writeFile(projectId, selected.path, content);
      setSavedContent(content);
      setSavedFlash(true);
      setTimeout(() => setSavedFlash(false), 2000);
    } catch (e) {
      setFileError(errMsg(e));
    } finally {
      setSaving(false);
    }
  }, [selected, saving, content, savedContent, projectId]);

  const tree = (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex h-9 shrink-0 items-center justify-between border-b border-border px-2">
        <span className="px-1 text-xs font-medium uppercase tracking-wide text-faint">
          Files
        </span>
        <IconButton
          aria-label="Refresh file tree"
          onClick={() => void refreshTree()}
          className="h-7 w-7 md:h-7 md:w-7"
        >
          <IconRefresh className="h-3.5 w-3.5" />
        </IconButton>
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-1.5">
        {root === null && !treeError && (
          <div className="flex justify-center py-8">
            <Spinner className="h-4 w-4" />
          </div>
        )}
        {treeError && (
          <div className="p-2">
            <ErrorBox message={treeError} />
          </div>
        )}
        {root !== null && root.length === 0 && (
          <p className="p-3 text-xs text-faint">No files yet.</p>
        )}
        {root !== null && root.length > 0 && (
          <TreeView
            nodes={root}
            depth={0}
            selectedPath={selected?.path ?? null}
            onToggleDir={(n) => void toggleDir(n)}
            onOpenFile={(e) => void openFile(e)}
          />
        )}
      </div>
    </div>
  );

  const editor = (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex h-11 shrink-0 items-center gap-2 border-b border-border px-2 md:h-9">
        {!isDesktop && (
          <IconButton
            aria-label="Back to files"
            onClick={() => setSelected(null)}
            className="h-8 w-8 md:h-7 md:w-7"
          >
            <IconArrowLeft className="h-4 w-4" />
          </IconButton>
        )}
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-dim">
          {selected?.path}
        </span>
        {selected && (
          <span className="shrink-0 text-xs text-faint">{formatBytes(selected.size)}</span>
        )}
        {savedFlash && !dirty && (
          <span className="flex shrink-0 items-center gap-1 text-xs text-emerald-500">
            <IconCheck className="h-3.5 w-3.5" /> Saved
          </span>
        )}
        {dirty && <span className="h-2 w-2 shrink-0 rounded-full bg-amber-500" title="Unsaved" />}
        <Button
          variant="outline"
          onClick={() => void save()}
          disabled={!dirty || saving || fileLoading}
          className="min-h-[30px] shrink-0 px-2.5 py-1 text-xs"
        >
          {saving ? <Spinner className="h-3.5 w-3.5" /> : 'Save'}
        </Button>
      </div>
      <div className="min-h-0 flex-1">
        {fileLoading ? (
          <div className="flex h-full items-center justify-center">
            <Spinner className="h-5 w-5" />
          </div>
        ) : fileError ? (
          <div className="p-3">
            <ErrorBox message={fileError} />
          </div>
        ) : (
          <textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            onKeyDown={(e) => {
              if ((e.metaKey || e.ctrlKey) && e.key === 's') {
                e.preventDefault();
                void save();
              }
            }}
            spellCheck={false}
            className="h-full w-full resize-none bg-transparent p-3 font-mono text-[13px] leading-5 text-text outline-none"
            style={{ whiteSpace: 'pre', overflowWrap: 'normal', overflowX: 'auto' }}
          />
        )}
      </div>
    </div>
  );

  if (!isDesktop) {
    // Mobile: tree and editor are full-screen, one at a time.
    return selected ? editor : tree;
  }

  return (
    <div className="flex h-full min-h-0">
      <div className="w-60 shrink-0 border-r border-border">{tree}</div>
      <div className="min-w-0 flex-1">
        {selected ? (
          editor
        ) : (
          <div className="flex h-full items-center justify-center">
            <p className="text-sm text-faint">Select a file to view or edit</p>
          </div>
        )}
      </div>
    </div>
  );
}
