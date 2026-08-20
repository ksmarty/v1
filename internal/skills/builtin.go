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

// Builtins returns the skills bundled with v1.
func Builtins() []Builtin {
	return []Builtin{githubWorkflows}
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
