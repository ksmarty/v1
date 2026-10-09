package skills

// Builtin skills ship with v1 — no marketplace download required. Each is a
// set of files written into the skills root on install; the SKILL.md is
// injected into the agent's system prompt when enabled, exactly like any other
// installed skill.

// Builtin is one bundled skill: its metadata plus the files to materialize.
type Builtin struct {
	Skill Skill
	Files map[string]string // relative path -> file contents
}

// githubWorkflows is the bundled "github-workflows" skill: it lets the agent
// add GitHub Actions workflows (CI, auto-increment versions, container image
// publishing) to any repository, including ones other than the project it is
// working in, using the git tool and the run_container tool.
var githubWorkflows = Builtin{
	Skill: Skill{
		ID:          "github-workflows",
		Name:        "GitHub Workflows",
		Author:      "v1",
		Description: "Add GitHub Actions workflows to any repo: CI, automatic version increments (git tags / npm version bumps) and multi-arch container image publishing to GHCR. Includes ready-to-adapt templates.",
		Dir:         "github-workflows",
		Enabled:     true,
	},
	Files: map[string]string{
		"SKILL.md": `# GitHub Workflows

Use this skill when the user wants to add GitHub Actions workflows to a
repository — the current project or any other repo they point you at.

## Capabilities

You have the tools needed to fully automate this:

- git — clone/fetch/commit/push workflows into a repo (including repos
  other than the current project, when the user provides a URL).
- run_container — build and test Docker images locally before publishing
  (uses podman when installed, otherwise docker).
- GitHub credentials — use the user's linked GitHub account/token for pushing
  to their repos (private repos included).

## Templates

Ready-to-adapt templates live in the skill's templates/ directory. Read
them, adapt placeholders (OWNER, REPO, IMAGE, version numbers) to the target
repo, then write them into .github/workflows/ in the target repo and commit
and push.

### 1. CI — templates/ci.yml
Lint / test / build on pull requests and pushes. Adapt the steps to the
project's language (Go, Node, Python, ...). Keep the v1 convention of building
the frontend and vetting/testing the backend.

### 2. Auto-increment version — templates/auto-increment.yml
Bumps the version on every push to main:
- Reads the current version from package.json (npm projects) or the latest
  git tag (everything else).
- Increments the patch number (1.2.3 -> 1.2.4).
- Commits the bump and tags the commit with v<version>, then pushes both.
- Include a manual workflow_dispatch input to trigger a bump by hand.

### 3. Publish container image — templates/publish-image.yml
Builds and pushes a multi-arch image to GHCR on release/tag:
- Authenticates with GITHUB_TOKEN (automatic, no PAT needed).
- Uses docker/build-push-action with linux/amd64 + linux/arm64.
- Tags: the release version (from github.ref, e.g. v0.42.0) and latest.
- Requires a Dockerfile in the repo root; create one if missing.

## Workflow

1. Confirm the target repository (current project or an external repo URL).
2. Read the relevant template(s) and adapt them.
3. Write them to .github/workflows/ in the target repo.
4. Commit and push with the git tool.
5. If publishing images, verify the Dockerfile builds locally with
   run_container (podman/docker build) before pushing.
6. Point the user at the Actions tab to watch the runs.
`,
		"templates/ci.yml": `name: CI

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  build:
    name: Lint / test / build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      # --- Frontend (adapt or remove if there is no web/ directory) ---
      - name: Setup Node
        uses: actions/setup-node@v4
        with:
          node-version: 22
          cache: npm
          cache-dependency-path: web/package-lock.json
      - name: Frontend build
        working-directory: web
        run: |
          npm ci
          npm run build

      # --- Backend / core (adapt to the project's language) ---
      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version: "1.23"
      - name: Go vet / test / build
        run: |
          go vet ./...
          go test ./...
          go build ./...
`,
		"templates/auto-increment.yml": `name: Auto-increment version

on:
  push:
    branches: [main]
  workflow_dispatch:
    inputs:
      manual:
        description: "Bump the version by hand"
        type: boolean
        default: true

permissions:
  contents: write

jobs:
  bump:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Read current version
        id: current
        run: |
          if [ -f package.json ]; then
            V=$(node -p "require('./package.json').version")
          else
            V=$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo "0.0.0")
          fi
          echo "version=$V" >> "$GITHUB_OUTPUT"

      - name: Compute next patch version
        id: next
        run: |
          IFS=. read -r MAJ MIN PAT <<< "${{ steps.current.outputs.version }}"
          echo "version=$MAJ.$MIN.$((PAT + 1))" >> "$GITHUB_OUTPUT"

      - name: Bump package.json (when present)
        if: hashFiles('package.json') != ''
        run: npm version --no-git-tag-version "${{ steps.next.outputs.version }}"

      - name: Commit and tag
        run: |
          git config user.name "github-actions[bot]"
          git config user.email "github-actions[bot]@users.noreply.github.com"
          git add -A
          git commit -m "chore: release v${{ steps.next.outputs.version }}" || true
          git tag "v${{ steps.next.outputs.version }}"
          git push origin HEAD --tags
`,
		"templates/publish-image.yml": `name: Publish container image

on:
  push:
    tags: ["v*"]
  workflow_dispatch:

env:
  REGISTRY: ghcr.io
  # Change OWNER and IMAGE to your GitHub owner and image name:
  IMAGE_NAME: ${{ github.repository }}

permissions:
  contents: read
  packages: write

jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up QEMU
        uses: docker/setup-qemu-action@v3

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to GHCR
        uses: docker/login-action@v3
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Extract version
        id: version
        run: echo "tag=${GITHUB_REF#refs/tags/v}" >> "$GITHUB_OUTPUT"

      - name: Build and push multi-arch image
        uses: docker/build-push-action@v6
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: true
          tags: |
            ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}:v${{ steps.version.outputs.tag }}
            ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}:latest
          cache-from: type=gha
          cache-to: type=gha,mode=max
`,
	},
}

// persistentToolInstall is the bundled "persistent-tool-install" skill. v1
// runs in a container whose only persisted volume is /data, so a tool the
// agent installs the obvious way (apt-get, a bare `npm install -g`, a download
// into /usr/local) is gone on the next image update. The skill teaches the
// agent to install into /data instead. The Dockerfile sets the matching
// environment (NPM_CONFIG_PREFIX, PYTHONUSERBASE, CARGO_HOME, GOPATH,
// BUN_INSTALL, PIPX_*, UV_*) so the plain commands land there and are on PATH.
var persistentToolInstall = Builtin{
	Skill: Skill{
		ID:          "persistent-tool-install",
		Name:        "Persistent Tool Install",
		Author:      "v1",
		Description: "Install CLI tools, libraries and language runtimes so they survive container recreation: npm, pip, pipx, uv, cargo, go, bun and standalone binaries, all under the persisted /data volume.",
		Dir:         "persistent-tool-install",
		Enabled:     true,
	},
	Files: map[string]string{
		"SKILL.md": `# Persistent Tool Install

Use this skill when you need a CLI tool, library or language runtime that is
not already installed, and the user will expect it to still be there next week.

## Why the obvious install is the wrong one

v1 runs in a container. Only /data is a persisted volume:

- /data — survives everything, including a backup of the volume.
- /workspace — survives container recreation.
- /usr, /usr/local, /opt, /root — survive a plain restart, but are DESTROYED
  when the container is recreated from a new image (an update, a redeploy,
  docker compose down && up).

So apt-get, a bare npm install -g, and anything downloaded into /usr/local
silently disappear on the next update — the user reinstalls the tool, and you
are asked to fix the same missing-command problem again. Install under /data.

## Where things go

The toolchains are already configured (by the image's environment) to install
into /data, and every one of these directories is on PATH. Use the plain
command — no extra flags needed:

    tool            command                     lands in
    npm  global     npm install -g TOOL         /data/npm/bin
    pip  user       pip install --user TOOL     /data/python/bin
    pipx            pipx install TOOL           /data/bin
    uv              uv tool install TOOL        /data/bin
    cargo           cargo install TOOL          /data/cargo/bin
    go              go install MODULE@latest    /data/go/bin
    bun             bun add -g TOOL             /data/bun/bin
    any binary      curl -o /data/bin/TOOL URL  /data/bin

The volume may predate this configuration, so create the target directory
before the first install:

    mkdir -p /data/npm/bin /data/python/bin /data/cargo/bin /data/go/bin /data/bun/bin /data/bin

## Recipes

### npm — the zero-setup path, prefer this when the tool is published there

    npm install -g TOOL

### Python

    pip install --user TOOL
    pipx install TOOL
    uv tool install TOOL

### Rust

    curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
    cargo install TOOL

### Go

    go install MODULE@latest

### Bun

    curl -fsSL https://bun.sh/install | bash
    bun add -g TOOL

### A standalone binary from a release URL

    curl -fsSL URL -o /data/bin/TOOL && chmod +x /data/bin/TOOL

## Verify where it landed

command -v tells you whether the install will survive. A path under /data is
persistent; a path under /usr, /usr/local or /opt is lost on the next image
update:

    command -v TOOL

To check several at once:

    for t in TOOL1 TOOL2; do printf '%s -> %s\n' "$t" "$(command -v "$t" || echo MISSING)"; done

If a tool reports that it is missing right after you installed it, the shell
may be holding a stale PATH; the directories above are already on PATH, so
re-running the command in a fresh shell is enough.

## Never install these this way

The v1 binary, the pi-durable harness sidecar, Node.js and pnpm are managed by
the image and replaced on every update — installing your own copy under /data
shadows the managed one and breaks the next update.

## System packages

apt-get installs into the container filesystem, so it is fine for a build-time
dependency you need only for this task, and wrong for anything the user expects
to keep. If a tool genuinely requires a system package at runtime, add it to
the project's Dockerfile instead of installing it here — that is the only place
it can be made to persist.
`,
	},
}

// Builtins returns the skills bundled with v1.
// v1Extensions is the bundled "v1-extensions" skill. It teaches the agent to
// extend v1 itself — writing a JavaScript module that adds a tool, a prompt
// section, a hook or a wrapper — and to install it with the create_extension
// tool. The API described here is the object the sidecar builds in
// sidecar/src/host.js (extensionApi) and the pi-durable define.ts helpers.
var v1Extensions = Builtin{
	Skill: Skill{
		ID:          "v1-extensions",
		Name:        "v1 Extensions",
		Author:      "v1",
		Description: "Write and install v1 extensions: JavaScript modules that add new tools, prompt sections, hooks or tool wrappers to the agent itself, installed with create_extension.",
		Dir:         "v1-extensions",
		Enabled:     true,
	},
	Files: map[string]string{
		"SKILL.md": `# v1 Extensions

Use this skill when the user wants v1 itself to gain a new ability: a new tool
for you to call, extra instructions in your prompt, a hook around a run, or a
change to an existing tool. An extension is a small JavaScript module that the
agent harness loads, and you write and install it yourself with the
create_extension tool.

Do not use this skill for a change to the user's project. It is for extending
the agent.

## What an extension can add

- tools - new functions you can call, each with a JSON Schema for its
  arguments. This is the usual case, and the one to prefer.
- sections - text injected into your instructions while the extension is
  loaded.
- hooks - handlers that run around a named task.
- wraps - pure functions that wrap another extension's tool or section.

Extensions live at <data-dir>/extensions/<id>/index.js. The id must match
[a-z0-9][a-z0-9-]* - lowercase letters, digits and dashes, and it cannot start
with a dash.

## The module

The default export is a function. v1 calls it with an api object and uses what
it returns:

    export default (pi) => ({
      name: "word-count",
      tools: [ /* ... */ ],
    });

The api object provides:

- pi.defineTool({ name, description, parameters, execute })
- pi.section(key, render, options)
- pi.hook(task, handlers)
- pi.wrapTool(tool, wrapper)
- pi.wrapSection(key, wrapper)
- pi.defineExtension(extension) - identity, used only to type the object
- pi.log - a logger with log.info(message, fields) and log.error(...)
- pi.delegate({ task }, context) - run a sub-agent, returning { text, toolCalls }
- pi.session.rename(name) - rename the chat session this extension is running in
  (v1 owns the name, so this goes through the same host tool the agent uses:
  the rename lands in v1's store and shows up in the UI immediately)

### defineTool

    pi.defineTool({
      name: "word_count",
      description:
        "Count the words in a string. Use when the user asks how long a piece " +
        "of text is.",
      parameters: {
        type: "object",
        properties: {
          text: { type: "string", description: "The text to count." },
        },
        required: ["text"],
      },
      async execute(args, api, context) {
        const text = typeof args?.text === "string" ? args.text : "";
        const trimmed = text.trim();
        const words = trimmed ? trimmed.split(/\s+/).length : 0;
        return { content: [{ type: "text", text: words + " words" }] };
      },
    })

Rules that matter:

- The name must be unique across everything loaded. A name that collides with
  a builtin tool is dropped, with a log line - the model never sees it.
- parameters is a JSON Schema object, and required lists the mandatory
  properties. Without them the model cannot call the tool correctly.
- execute returns an object with a content array of { type: "text", text }
  items. It may be async and may await.
- A thrown error is reported to the model as a tool failure, so throw with a
  message that says what to do differently.
- The description is what the model reads when deciding whether to call the
  tool. Say when to use it, not only what it does.

### Presentation

A tool call appears in the chat as a chip with an icon, a title and a trailing
line. Declare a display object to control that, rather than leaving the reader
with the tool's raw name:

    pi.defineTool({
      name: "get_weather",
      display: {
        title: "Get weather",
        icon: "globe",
        summary: "{city}",
      },
      ...
    })

- title replaces the tool's name in the chip.
- icon picks from v1's own icon set by name: globe, map, plan, file, search,
  terminal, flask, test, git, code, layers, extension, list, lock, model, user,
  bookmark, brain, camera, download, refresh, send, trash, pencil, check, plus,
  compress, expand, arrow-up, square, tool, wrench. An unknown name is not an
  error - the call falls back to the extension icon.
- summary is a template for the chip's trailing text. {field} is replaced with
  that argument's value, so "{city}" shows the city the tool was called with.
  Use it whenever one argument identifies the call: without it the chip falls
  back to the usual command/path/query/url guesswork, which knows nothing about
  your arguments.

### section

    pi.section("house-style", () => "Prefer tabs in this project.")

render receives (input, context) and returns a string, or undefined to add
nothing. By default the text is wrapped as <house-style>...</house-style> so the
model can tell it apart from the rest of the prompt. Pass { tag: false } to
inject it unwrapped.

### hook and wrap

    pi.hook("some-task", { onStart: (event) => { /* ... */ } })

    pi.wrapTool(someTool, (tool) => ({
      ...tool,
      description: tool.description + " Also reports the character count.",
    }))

Hooks attach to a task by name. Wrappers are pure functions applied where the
wrapping extension is selected. Both are advanced - prefer a plain new tool
unless the user needs to change something that already exists.

## Installing it

Call create_extension with the id, a one-line description and the complete
module source:

    create_extension({
      id: "word-count",
      description: "Adds a word_count tool.",
      source: "<the whole module>"
    })

It validates the id, syntax-checks the source with node, writes it, enables it
and reloads the harness, then reports what loaded. Installing an id that
already exists replaces its source. A new tool becomes available on the next
turn, so tell the user to send another message before expecting to use it.

Read the result of create_extension rather than assuming the extension loaded.
If it reports an error, fix the source and call it again.

## Failure modes to check before you install

- A syntax error - node rejects the file and nothing loads. The result quotes
  node's message.
- A duplicate tool name - the tool is dropped with a log line, so the model
  never sees it.
- A bad id - must be lowercase, and cannot start with a dash.
- A module whose default export is not a function - nothing is registered.

## Working style

Write the smallest extension that does the job. Prefer one tool with a clear
description over several vague ones. After installing, verify from the result
that the tool you intended is listed.
`,
	},
}

// sessionNaming is the bundled "session-naming" skill: it has the agent name the
// chat after what it is about, which is what makes the session list readable.
// The rename goes through the set_session_name tool, so it lands in v1's store
// and appears in the UI like any other rename.
var sessionNaming = Builtin{
	Skill: Skill{
		ID:          "session-naming",
		Name:        "Session naming",
		Author:      "v1",
		Description: "Name the chat session after what it is about, so the session list is scannable: once the intent is clear, and again only if the work changes direction.",
		Dir:         "session-naming",
		Enabled:     true,
	},
	Files: map[string]string{
		"SKILL.md": `# Session naming

Name the chat session after what it is actually about, so the session list can
be read at a glance.

## When

- At the start of a session, as soon as the user's intent is clear — after their
  first substantive message, or after your first look at the code. Not before
  you know what they want.
- At the end of a turn, only if the work has turned into something materially
  different from the current name — a bug fix that became a refactor.

Do not rename on every turn. One good name is the whole point.

## How

Call set_session_name with the title. It lands in v1's store and shows up in the
session list immediately, so there is nothing to explain to the user.

## Rules

- Three to six words.
- Describe the outcome, not the request: "Fix login redirect loop", not "User
  asked about login".
- Sentence case, no trailing punctuation, no quotes, no dashes as separators.
- Name the subject, not the first file you happened to open.
- Never rename a session the user has named themselves. If the current name
  does not look like one of yours, leave it alone.
`,
	},
}

func Builtins() []Builtin {
	return []Builtin{githubWorkflows, persistentToolInstall, v1Extensions, sessionNaming}
}

// FindBuiltin returns the builtin skill with the given id, or nil.
func FindBuiltin(id string) *Builtin {
	for _, b := range Builtins() {
		if b.Skill.ID == id || b.Skill.Dir == id {
			return &b
		}
	}
	return nil
}
