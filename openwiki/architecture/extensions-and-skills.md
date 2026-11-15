---
type: "Reference"
title: "Extensions and skills"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-df0f1e523ee229bacc2d0532
    resource: repo://internal/extensions/builtin.go
  - id: openwiki-source-42a0e7bb74ce2f488e00e09c
    resource: repo://internal/extensions/extensions.go
  - id: openwiki-source-cee00abcc8db289b7bc94ec1
    resource: repo://internal/server/handlers_extensions.go
  - id: openwiki-source-6caba0b1f3a5bf1cea78537e
    resource: repo://internal/server/handlers_skills.go
  - id: openwiki-source-7d4ac4eaf17b216fdc5fd692
    resource: repo://internal/server/server.go
  - id: openwiki-source-1ca8b538823e4033b997ed7f
    resource: repo://internal/skills/materialize.go
  - id: openwiki-source-bdaf5b5b6e5a91b55411444f
    resource: repo://internal/skills/skills.go
  - id: openwiki-source-b21c0acda2ddbe945c0ee3e8
    resource: repo://sidecar/src/extensions.js
  - id: openwiki-source-c7740d98d388f2bd766d41b0
    resource: repo://sidecar/src/host.js
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# Extensions and skills

v1 has two ways to change what the agent can do, and they are deliberately
different things:

- An **extension** is JavaScript that runs inside the harness. It can add tools,
  inject prompt sections, hook the turn lifecycle and wrap other tools. Code
  executes, so installing one is a trust decision.
- A **skill** is Markdown. `SKILL.md` is pasted into the system prompt, so it
  teaches the agent a procedure using tools it already has, and nothing new is
  executed.

Both live under the data volume, so they survive a container rebuild, and both
are editable from Settings exactly like something the user wrote by hand.

The tools an extension contributes are covered in
[Agent tools, tool results and events](./agent-tools-and-events.md); the loader
runs inside the process described in
[The pi-durable harness sidecar](./harness-sidecar.md).

## The extension module contract

An extension is a single ESM file, `<data-dir>/extensions/<id>/index.js`, whose
default export is either an object or a factory called with the pi API:

```js
export default (pi) => {
  const limit = Number(pi.settings.limit ?? 500);
  return {
    name: "word-count",
    settings: [
      { key: "limit", label: "Warn above", type: "text", default: "500" },
    ],
    tools: [
      pi.defineTool({
        name: "word_count",
        description: "Count the words in a string and say whether it is over the configured limit.",
        parameters: {
          type: "object",
          properties: { text: { type: "string", description: "The text to count." } },
          required: ["text"],
        },
        async execute(args) {
          const words = (args?.text ?? "").trim().split(/\s+/).filter(Boolean).length;
          return { content: [{ type: "text", text: `${words} words (limit ${limit})` }] };
        },
      }),
    ],
    sections: [pi.section("word-count", () => "Prefer concise prose.")],
  };
};
```

The object may carry `tools`, `sections`, `hooks`, `wraps` and `settings`. Only
the factory form receives `settings`, because a plain object is constructed
before the loader knows which extension it is — that ordering is why every
configurable extension uses the factory form.

`pi.defineTool({ name, description, parameters, execute })` takes a JSON Schema
for its arguments. `execute` returns `{ content: [{ type: "text", text }] }`, may
throw (reported to the model as a tool failure), and may be async.
`pi.section(key, render)` injects text into the prompt while the extension is
loaded. `pi.hook(task, handlers)` attaches to `pi.generation`, `pi.tool` or
`pi.compaction`.

## Where files live

| Path | Contents |
| --- | --- |
| `<data-dir>/extensions/<id>/index.js` | one extension's source (`extensions.SourcePath`, `internal/extensions/extensions.go:71`) |
| `<data-dir>/extensions/.v1-builtins.json` | hash of each bundled extension's last shipped source (`builtinManifest`, `internal/extensions/extensions.go:240`) |
| `<data-dir>/skills/<id>/SKILL.md` | one installed skill (`internal/skills/skills.go:45`) |
| `<data-dir>/skills/.v1-builtins.json` | the same manifest idea for bundled skills (`builtinManifestFile`, `internal/skills/materialize.go:17`) |

Metadata (name, description, version, changelog, enabled flag) is not read out of
the file: it is stored in the settings row `extensions` and reconciled against
the directories on disk, so a directory added or deleted by hand still shows up
in the list (`installedExtensions` → `extensions.Reconcile`,
`internal/server/handlers_extensions.go:45`, `internal/extensions/extensions.go:196`).

## Installing: validation before storage

`extensions.Validate` (`internal/extensions/extensions.go:82`) is the gate every
install and every update passes:

- the id must match `^[a-z0-9][a-z0-9-]*$` (`idPattern`, `internal/extensions/extensions.go:30`).
  The comment says what that buys: the id is a directory name, and this pattern
  is the only thing standing between a caller and a path outside the extensions
  root.
- the source must be non-empty and no larger than `maxSource = 1 << 20`
  (`internal/extensions/extensions.go:34`), so a runaway write cannot fill the
  data volume.
- the source is syntax-checked with `node --check` on a temporary `.mjs` file
  under a `checkTimeout = 20 * time.Second` (`internal/extensions/extensions.go:37`).
  Shelling out to Node rather than approximating JavaScript in Go is deliberate:
  the sidecar executes this file, so its parser is the authority — a Go-side
  check would accept what Node rejects and reject what it accepts. Node is
  guaranteed present because the sidecar is Node.

`handleExtensionSave` (`internal/server/handlers_extensions.go:617`) adds the two
rules that are about provenance rather than syntax:

- **A version and a one-line changelog are required.** Without them the settings
  list cannot tell two installs apart without opening the source.
- **An update must move the version forward.** `extensions.CompareVersions`
  (component-wise, so `1.10.0 > 1.9.0`) is checked *before* writing, so a
  rejected save leaves the installed source untouched.

A brand-new extension starts disabled unless the caller explicitly enables it:
loading code nobody asked for is the wrong default.

`extensions.Write` (`internal/extensions/extensions.go:122`) replaces the file
atomically through a temp file and rename, so a reader never sees a half-written
module.

## The collision verifier

Names are shared across every loaded extension, and the registry is
last-write-wins, so a duplicate does not throw — it silently wins. The loader
answers that with an explicit conflict report instead:
`extensionConflicts(extensions, hostToolNames)` (`sidecar/src/extensions.js:184`)
walks every loaded extension and returns a list of clashes:

- the same tool name claimed by two extensions (`kind: "tool"`);
- a tool name that shadows a host tool, reported as `ids: [<extension>, "builtin"]`;
- the same section key claimed twice (`kind: "section"`);
- the same display title claimed twice (`kind: "title"`).

The result is surfaced by `create_extension` (which refuses the install), by
`list_extensions`, and as a warning on the Extensions settings page. A tool that
shadows a host tool is dropped in favour of Go's definition at load time, which
is exactly the case the verifier makes visible rather than letting the reader
discover it through a tool that never runs the code they wrote.

## Loading

`loadExtensions(root, api, enabledIds, settingsFor)` (`sidecar/src/extensions.js:248`)
imports every enabled directory:

- a disabled extension is **not imported at all** — its factory is code the user
  asked not to run, so skipping the import is the point, not skipping the
  install;
- the import URL is cache-busted with the file's mtime
  (`${pathToFileURL(source).href}?v=${info.mtimeMs}`), because Node caches ES
  modules by URL and an edited file would otherwise keep serving the first
  version for the life of the process;
- the factory is called with `{ ...api, settings: settingsFor(id) }`, and the
  registry name is forced to the directory id, so what the agent sees cannot
  drift from what is on disk;
- nothing throws: a missing directory is "no extensions", and a module that fails
  to load lands in `errors` so the caller can surface it without losing the
  extensions that did load.

## Tool exposure: every schema, or search on demand

A large extension or MCP catalog costs tokens on every request, so the harness
has two modes, resolved by `s.toolExposure(userID)`
(`internal/server/server.go:762`) from the `tool_exposure` setting
(`keyToolExposure`, `internal/server/server.go:447`):

- `"all"` (the default) advertises every schema up front.
- `"search"` hides MCP and extension tools behind a `search_tools` tool that
  reveals them on demand.

`exposedTools` (`sidecar/src/host.js:543`) implements the split. In search mode
the deferred set is every MCP host def plus every extension tool, minus what this
conversation has already discovered (`discoveredTools`, recorded by
`recordDiscovered`, so a tool revealed in one turn stays loadable in the next).
Two exceptions keep the mode usable: a name claimed by a host tool is dropped
from the deferred catalog, and a tool in `EAGER_EXTENSION_TOOLS` (`sidecar/src/host.js:49`,
currently just `delegate`) is never deferred.

## Bundled extensions and upgrading them

v1 ships extensions (`extensions.Builtins`, `internal/extensions/builtin.go:9`,
currently the `delegate` sub-agent tool). Their source is written into the
extensions root at startup, so they behave exactly like a user's extension: they
can be read, edited, disabled or deleted.

`Materialize` (`internal/extensions/extensions.go:250`) is what makes an upgrade
safe:

- the hash of each builtin's shipped source is recorded in the manifest, so a
  later startup can tell an untouched copy from one the user edited;
- a copy whose hash still matches is upgraded, so a shipped fix reaches an
  existing install;
- a copy the user edited is left alone — and **deleting the file is therefore how
  you get the original back**;
- a copy that predates the manifest has no recorded hash, so it is upgraded once
  while the old source is kept beside it as `index.js.bak`, rather than left
  stale forever.

## Skills

A skill is `<data-dir>/skills/<id>/SKILL.md`. `skills.Write`
(`internal/skills/skills.go:45`) and `skills.Read` (`internal/skills/skills.go:58`)
both refuse an id that is not a plain directory name (`id != filepath.Base(id)`
or containing a slash), so a skill cannot be written outside the root.

`skills.SystemPrompt(root, installed)` (`internal/skills/skills.go:385`) is the
whole mechanism: for each enabled skill with a directory it prepends a `---`
separator, a `## Skill: <name> — <description>` heading and the trimmed
`SKILL.md` body, and returns the block only when at least one skill contributed.
The result is passed to the harness as part of the turn's instructions.

### The marketplace

`skills.Search` (`internal/skills/skills.go:73`) queries the SkillsMP index at
`apiBase = "https://skillsmp.com"` (`internal/skills/skills.go:21`), clamped to at
most 50 results. `skills.Install` (`internal/skills/skills.go:171`) fetches the
skill straight from its GitHub repository through the user's `gitops.GHClient`
— `parseGitHubURL` (`internal/skills/skills.go:153`) splits the repo from the
subdirectory, `download` (`internal/skills/skills.go:348`) writes the files, and
`previewPaths` / `Preview` (`internal/skills/skills.go:285`, `:308`) show what
would be installed before it is. A `Skill` (`internal/skills/skills.go:24`) carries
the marketplace metadata (`GitHubURL`, `SkillsMPURL`, `Stars`, `Branch`,
`SourcePath`, `Owner`, `Repo`, `Dir`) plus the local state (`Enabled`, `Builtin`,
`Version`, `Changelog`).

The handlers mirror one operation each: `handleSkillsSearch`
(`internal/server/handlers_skills.go:130`), `handleSkillsInstall` (`:165`),
`handleSkillReadme` (`:216`), `handleSkillPreview` (`:241`),
`handleSkillsRemove` (`:257`), `handleSkillsToggle` (`:287`).

### Bundled skills are seeded, once

Bundled skills (`internal/skills/builtin.go:912`) are installed by
`ensureBuiltinSkills` (`internal/server/handlers_skills.go:57`) the first time v1
sees them, guarded by a setting flag so **a skill the user removed never comes
back on the next restart**. A bundled skill added after that seed already ran
needs its own one-time flag (`seedBuiltinSkill(keySkillsMermaidSeeded, "mermaid-diagrams")`,
`internal/server/handlers_skills.go:61`) for the same reason.
`RefreshBuiltins` (`internal/skills/materialize.go:37`) upgrades an untouched
bundled skill's files using the same recorded-hash rule as extension builtins.

## Invariants

| Invariant | Where it is enforced |
| --- | --- |
| An extension id is safe as a directory name, and the source is real JavaScript | `extensions.Validate`, `internal/extensions/extensions.go:82` |
| A rejected save never touches the installed source | version check before `extensions.Write`, `internal/server/handlers_extensions.go:660` |
| Every update carries a version and a changelog, and the version increases | `handleExtensionSave`, `internal/server/handlers_extensions.go:630-666` |
| A duplicate tool, section key or display title is reported, never silently overwritten | `extensionConflicts`, `sidecar/src/extensions.js:184` |
| A disabled extension is never imported | `loadExtensions`, `sidecar/src/extensions.js:274` |
| An edited file is picked up by a reload | mtime cache-bust, `sidecar/src/extensions.js:281` |
| A user-edited bundled extension is never overwritten | recorded source hash, `internal/extensions/extensions.go:261` |
| A removed bundled skill stays removed | seed flag, `internal/server/handlers_skills.go:62` |
| Only enabled skills reach the prompt | `skills.SystemPrompt`, `internal/skills/skills.go:385` |
