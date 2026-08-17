</div>

      {st?.isRepo && (
        <div className="flex flex-col gap-2.5 rounded-lg border border-border bg-surface p-3">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
            <span className="inline-flex items-center gap-1 rounded-md bg-surface px-1.5 py-0.5 font-mono text-[11px]">
              <IconGitBranch className="h-3.5 w-3.5 text-subtle" />
              {info.branch}
            </span>
            <span className="inline-flex items-center gap-1 rounded-md bg-emerald-500/10 px-1.5 py-0.5 font-mono text-[11px] text-emerald-300">
              <IconArrowUp className="h-3 w-3" /> {ahead}
            </span>
            <span className="inline-flex items-center gap-1 rounded-md bg-sky-500/10 px-1.5 py-0.5 font-mono text-[11px] text-sky-300">
              <IconArrowDown className="h-3 w-3" /> {behind}
            </span>
            <span className="font-mono text-[11px] text-dim">{st.modified} mod</span>
            <span className="font-mono text-[11px] text-dim">{st.untracked} untracked</span>
            <div className="flex-1" />
            {dirty && (
              <Button
                variant="outline"
                className="shrink-0 px-2.5 text-xs"
                onClick={() => void runAction('commit')}
                disabled={busy}
                title="Commit local changes"
              >
                <IconCheck className="h-3.5 w-3.5" /> Commit
              </Button>
            )}
            {(dirty || ahead > 0) && (
              <Button
                variant="outline"
                className="shrink-0 px-2.5 text-xs"
                onClick={() => {
                  setAction('push');
                  setCommitMsg('');
                }}
                disabled={busy}
                title={dirty ? 'Commit & push' : 'Push commits'}
              >
                <IconArrowUp className="h-3.5 w-3.5" /> {dirty ? 'Commit & push' : 'Push'}
              </Button>
            )}
            <Button
              variant="outline"
              className="shrink-0 px-2.5 text-xs"
              onClick={() => {
                setBusy(true);
                setError(null);
                api
                  .gitPull(projectId)
                  .then(async () => {
                    await load();
                    onPreviewRestart();
                  })
                  .catch((e) => setError(errMsg(e)))
                  .finally(() => setBusy(false));
              }}
              disabled={busy || !st?.repoUrl}
              title="Pull from origin"
            >
              <IconArrowDown className="h-3.5 w-3.5" /> Pull
            </Button>
          </div>
        </div>
      )}