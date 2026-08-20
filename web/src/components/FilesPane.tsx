import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels';
import { api } from '../api';
import type { FileEntry } from '../types';
import { errMsg, formatBytes, fuzzyScore } from '../utils';
import { useMediaQuery } from '../hooks/useMediaQuery';
import { ErrorBox, IconButton, Input, Spinner } from './ui';
import CodeEditor from './CodeEditor';
import Markdown from './Markdown';
import FileIcon from './FileIcon';
import {
  IconArrowLeft,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconFolder,
  IconFolderOpen,
  IconRefresh,
  IconSearch,
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
                  <FileIcon name={n.entry.name} className="h-4 w-4 shrink-0" />
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
  // Markdown files open in a rendered preview with an Edit toggle.
  const [preview, setPreview] = useState(true);
  const expandedRef = useRef<Set<string>>(new Set());

  const dirty = content !== savedContent;
  const isMarkdown = !!selected && /\.(md|markdown|mdx)$/i.test(selected.path);

  // VSCode-style file search over the whole project (recursive listing,
  // fetched on first use and refreshed whenever the tree reloads).
  const [fileQuery, setFileQuery] = useState('');
  const [allFiles, setAllFiles] = useState<FileEntry[] | null>(null);
  useEffect(() => {
    if (!fileQuery.trim() || allFiles !== null) return;
    let live = true;
    api
      .listFiles(projectId, '', true)
      .then((r) => {
        if (live) setAllFiles(r.entries);
      })
      .catch(() => {
        if (live) setAllFiles([]);
      });
    return () => {
      live = false;
    };
  }, [fileQuery, allFiles, projectId]);
  const searchResults = useMemo(() => {
    const q = fileQuery.trim();
    if (!q || !allFiles) return null;
    return allFiles
      .map((f) => ({ f, s: fuzzyScore(q, f.path) ?? Infinity }))
      .filter((x) => x.s !== Infinity)
      .sort((a, b) => a.s - b.s)
      .slice(0, 100)
      .map((x) => x.f);
  }, [fileQuery, allFiles]);

  const loadRoot = useCallback(async () => {
    try {
      const res = await api.listFiles(projectId, '');
      setRoot(sortEntries(res.entries).map(toNode));
      setTreeError(null);
      setAllFiles(null); // refresh the search cache too
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
      setAllFiles(null); // keep the search cache as fresh as the tree
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
    setPreview(true);
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
      <div className="shrink-0 border-b border-border p-1.5">
        <div className="relative">
          <IconSearch className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-faint" />
          <Input
            value={fileQuery}
            onChange={(e) => setFileQuery(e.target.value)}
            placeholder="Search files…"
            autoComplete="off"
            spellCheck={false}
            className="h-8 pl-7 text-xs"
          />
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-1.5">
        {fileQuery.trim() ? (
          searchResults === null ? (
            <div className="flex justify-center py-8">
              <Spinner className="h-4 w-4" />
            </div>
          ) : searchResults.length === 0 ? (
            <p className="p-3 text-xs text-faint">No files match.</p>
          ) : (
            searchResults.map((f) => (
              <button
                key={f.path}
                type="button"
                onClick={() => void openFile(f)}
                className={`flex min-h-[36px] w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[13px] transition-colors md:min-h-[28px] md:py-1 ${
                  f.path === selected?.path
                    ? 'bg-border text-text'
                    : 'text-dim hover:bg-surface hover:text-text'
                }`}
              >
                <FileIcon name={f.name} className="h-4 w-4 shrink-0" />
                <span className="min-w-0 flex-1 truncate" title={f.path}>
                  {f.path}
                </span>
              </button>
            ))
          )
        ) : (
          <>
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
          </>
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
        {selected && <FileIcon name={selected.path} className="h-3.5 w-3.5 shrink-0" />}
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-dim">
          {selected?.path}
        </span>
        {selected && (
          <span className="shrink-0 text-xs text-faint">{formatBytes(selected.size)}</span>
        )}
        {isMarkdown && (
          <div className="flex shrink-0 items-center gap-0.5 rounded-md border border-border p-0.5">
            {(['preview', 'edit'] as const).map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => setPreview(m === 'preview')}
                className={`rounded px-2 py-0.5 text-[11px] capitalize transition-colors ${
                  (m === 'preview') === preview
                    ? 'bg-border text-text'
                    : 'text-faint hover:text-text'
                }`}
              >
                {m}
              </button>
            ))}
          </div>
        )}
        {savedFlash && !dirty && (
          <span className="flex shrink-0 items-center gap-1 text-xs text-emerald-500">
            <IconCheck className="h-3.5 w-3.5" /> Saved
          </span>
        )}
        {dirty && <span className="h-2 w-2 shrink-0 rounded-full bg-amber-500" title="Unsaved" />}
        {!preview && (
          <button
            type="button"
            onClick={() => void save()}
            disabled={!dirty || saving || fileLoading}
            className="h-7 shrink-0 rounded-md border border-border-strong px-2.5 text-xs text-text transition-colors hover:bg-border disabled:cursor-not-allowed disabled:opacity-50"
          >
            {saving ? <Spinner className="h-3.5 w-3.5" /> : 'Save'}
          </button>
        )}
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
        ) : isMarkdown && preview ? (
          <div className="h-full overflow-y-auto overscroll-contain">
            <div className="mx-auto max-w-3xl px-5 py-5">
              <Markdown text={content} />
            </div>
          </div>
        ) : (
          <CodeEditor
            value={content}
            onChange={setContent}
            path={selected?.path}
            onKeyDown={(e) => {
              if ((e.metaKey || e.ctrlKey) && e.key === 's') {
                e.preventDefault();
                void save();
              }
            }}
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
    <div className="h-full min-h-0">
      <PanelGroup direction="horizontal" autoSaveId={`v1-files-${projectId}`}>
        <Panel defaultSize={28} minSize={18} maxSize={55} className="min-h-0">
          {tree}
        </Panel>
        <PanelResizeHandle className="w-1 bg-border transition-colors hover:bg-accent data-[resize-handle-state=drag]:bg-accent" />
        <Panel minSize={30} className="min-h-0">
          {selected ? (
            editor
          ) : (
            <div className="flex h-full items-center justify-center">
              <p className="text-sm text-faint">Select a file to view or edit</p>
            </div>
          )}
        </Panel>
      </PanelGroup>
    </div>
  );
}
