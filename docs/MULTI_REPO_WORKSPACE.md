# Multi-Repository Workspace Support

This document describes Nightgauge's multi-repository workspace
capabilities, enabling coordinated development across multiple related
repositories.

> **Note — January 2026 refactor:** the workspace-global "current
> repository" pointer (status-bar switcher, "Switch to Repository" arrow
> button, `ctrl+alt+r` keybinding, persisted last-active-repo, and the
> `onRepositoryChanged` event) has been removed. Repository selection
> now happens per call site:
>
> - **Pipeline routing** uses the repo baked into the issue/PR being
>   processed or an explicit `repoPath` argument.
> - **Autonomous mode** scans every repo in the allowlist
>   (`autonomous.enabled_repos`, toggleable via checkboxes on the
>   Repositories view).
> - **Contextual defaults** (dashboard opening, CLAUDE.md resolution
>   when no repo argument is passed) are derived on demand from the
>   active editor via `resolveActiveRepository()` — the repo whose path
>   contains the currently focused file, falling back to the
>   `role: primary` repo and then to the first loaded repo.
>
> Sections below that reference "switch to repository" or the status bar
> switcher describe the old behavior and are retained for historical
> context. They will be rewritten in a follow-up pass.

## Overview

Multi-repository workspace mode enables Nightgauge to coordinate operations
across multiple repositories within a single VSCode workspace. This is useful
for:

- **Monorepos** — Multiple packages in subdirectories
- **Multi-repo setups** — Related repositories in sibling directories
- **Microservices** — Frontend, backend, and shared libraries

### When to Use Multi-Repo Mode

| Scenario               | Use Multi-Repo? | Why                                        |
| ---------------------- | --------------- | ------------------------------------------ |
| Single repository      | No              | Standard mode handles this automatically   |
| Monorepo with packages | Yes             | Track issues and pipelines per-package     |
| Multiple git repos     | Yes             | Coordinate cross-repo features             |
| Shared library + apps  | Yes             | Route issues to correct repo automatically |

### Key Benefits

- **Repository-scoped pipelines** — Each repo maintains isolated context files
- **Automatic routing** — Route issues to repositories based on labels
- **Repository switching** — Quick pick to switch active repository
- **Cross-repo epics** — Track features spanning multiple repositories
- **Unified workspace** — Single VSCode window for all repositories

---

## Topology Guide

Nightgauge supports two workspace topologies:

| Topology          | Description                                   | Config                                                   |
| ----------------- | --------------------------------------------- | -------------------------------------------------------- |
| **1:1** (default) | One repo per GitHub project board             | Per-repo `.nightgauge/config.yaml` with `project.number` |
| **N:1**           | Multiple repos share one GitHub project board | `shared_project_number` in workspace manifest            |

### When to use 1:1

Most teams start here. Each repo has its own project board (or different project numbers). Issue routing and board views work independently per repo. No workspace manifest required.

### When to use N:1

Use N:1 when multiple repositories contribute issues to a single shared GitHub Project. Common scenarios:

- A platform org with a unified roadmap project
- A monorepo where all packages feed one board
- A frontend + backend pair tracked in one project

**Benefits:** The Repositories view auto-derives the repo list from the project, the view title shows `· Project #N`, and all repos are visible in one workspace.

**Trade-off:** Each individual repo still needs its own `.nightgauge/config.yaml` with `project.number` set so pipeline stages can route their operations correctly. The workspace manifest handles the view-layer listing; config.yaml handles the pipeline-layer routing.

### N:1 Configuration

Create `.vscode/nightgauge-workspace.yaml`:

```yaml
workspace:
  name: "MyPlatform"
  shared_project_number: 6 # GitHub Project #6 is shared by all repos

repositories: [] # empty — derived at runtime from ProjectV2.repositories
```

Or with an explicit list (overrides auto-derivation):

```yaml
workspace:
  name: "MyPlatform"
  shared_project_number: 6

repositories:
  - name: nightgauge
    path: ./nightgauge
    project_number: 6
  - name: acme-platform
    path: ./acme-platform
    project_number: 6
```

### Known Limitations

- The Go binary forge adapter binds one project per invocation. Per-repo `config.yaml` with `project.number` is still required for pipeline stages (`issue-pickup`, `feature-planning`, etc.).
- Auto-derivation is capped at 100 linked repos — sufficient for any real workspace.
- `drift-check` operates per project per invocation; in N:1 setups, run it once per repo (each will use its own `config.yaml` project number).
- Epic cross-repo tracking already works for N:1 (uses issue node IDs, not project-specific).
- Board-sync automation (nightly sweeps + closed→Done) is **not** installed into member repos automatically — provision it once per workspace with `workspace provision-board-sync` (see [Board-Sync Provisioning](#board-sync-provisioning)).

---

## Quick Start

### 1. Create Workspace Configuration

The recommended path is the **`workspace-init` skill**, which detects member
repos, derives the shared project, generates the manifest, and verifies it via
`workspace sync-payload`:

```bash
# Run repo-init in each member repo FIRST (one-time, per repo), then:
/nightgauge:workspace-init            # from the parent folder
/nightgauge:workspace-init --dry-run  # preview without writing
```

`workspace-init` is idempotent — re-running merges newly-onboarded members
without duplicating entries. See
[skills/nightgauge-workspace-init/SKILL.md](../skills/nightgauge-workspace-init/SKILL.md).

To author the manifest by hand instead, create
`.vscode/nightgauge-workspace.yaml` in your workspace root:

```yaml
workspace:
  name: "MyApp"

repositories:
  - name: frontend
    path: ./packages/frontend
    role: primary
  - name: backend
    path: ./packages/backend
    role: primary

routing:
  patterns:
    "area:frontend": frontend
    "area:backend": backend
  default_repository: frontend
```

### 2. Reload VSCode

Nightgauge detects workspace configuration on activation. Reload VSCode or
run **Developer: Reload Window**.

### 3. Use the Repository Switcher

Click the repository indicator in the status bar to switch between repositories.
The current repository determines where pipeline operations execute.

For complete configuration reference, see
[docs/CONFIGURATION.md#workspace-configuration](./CONFIGURATION.md#workspace-configuration).

---

## Architecture

### Detection Priority

Nightgauge detects workspace mode using this priority order:

```
┌─────────────────────────────────────────────────────────────────┐
│  1. Explicit Configuration (highest priority)                   │
│     .vscode/nightgauge-workspace.yaml exists                       │
│     → Multi-workspace mode with explicit config                 │
└─────────────────────────────────────────────────────────────────┘
                              ↓ not found
┌─────────────────────────────────────────────────────────────────┐
│  2. Auto-Detection                                               │
│     Multiple VSCode workspace folders                           │
│     Each folder has .nightgauge/config.yaml                        │
│     → Multi-workspace mode with auto-config                     │
└─────────────────────────────────────────────────────────────────┘
                              ↓ not found
┌─────────────────────────────────────────────────────────────────┐
│  3. Fallback (default)                                          │
│     Single repository mode                                      │
│     → Existing single-repo behavior                             │
└─────────────────────────────────────────────────────────────────┘
```

### Component Overview

Multi-repo workspace support is implemented across several components:

| Component                 | Purpose                                           |
| ------------------------- | ------------------------------------------------- |
| `WorkspaceManager`        | Singleton service managing workspace state        |
| `RepositoryContextLoader` | Repository-scoped context paths for pipeline      |
| `Repository`              | Model with lazy-loaded configuration              |
| `RepositorySwitcher`      | Status bar indicator and quick pick for switching |

#### WorkspaceManager

The `WorkspaceManager` service
(`packages/nightgauge-vscode/src/services/WorkspaceManager.ts`) is a
singleton that:

- Detects workspace mode on initialization
- Loads repository configurations
- Manages current/active repository state
- Provides events for UI components (`onRepositoryChanged`,
  `onWorkspaceChanged`)
- Persists last active repository across sessions

```typescript
// Example usage
const manager = WorkspaceManager.getInstance(workspaceRoot, context.workspaceState);
await manager.initialize();

// Check mode
if (manager.isMultiWorkspace()) {
  const repos = manager.getAllRepositories();
  console.log(`${repos.length} repositories in workspace`);
}

// Get current repository
const repo = manager.getCurrentRepository();
console.log(`Active: ${repo?.name}`);

// Subscribe to changes
manager.onRepositoryChanged((repo) => {
  console.log(`Switched to ${repo.name}`);
});
```

#### RepositoryContextLoader

The `RepositoryContextLoader` service
(`packages/nightgauge-vscode/src/services/RepositoryContextLoader.ts`)
provides repository-scoped paths for:

- Pipeline context files (`.nightgauge/pipeline/`)
- CLAUDE.md files
- Documentation (`docs/`)
- Standards (`standards/`)

```typescript
// Example usage
const loader = RepositoryContextLoader.getInstance();
await loader.initialize(workspaceManager);

// Get context directory for current repo
const contextDir = loader.getContextDir();
// Returns: /path/to/repo/.nightgauge/pipeline

// Get specific context file
const issuePath = loader.getContextFile("issue", 42);
// Returns: /path/to/repo/.nightgauge/pipeline/issue-42.json

// Load docs with precedence
const claudeMd = await loader.loadClaudeMd();
```

#### Repository Model

The `Repository` model
(`packages/nightgauge-vscode/src/models/Repository.ts`) represents a single
repository:

```typescript
interface Repository {
  name: string; // Unique identifier within workspace
  path: string; // Absolute path to repository root
  role?: "primary" | "secondary" | "shared";
  incrediConfig?: IncrediConfig; // Lazy-loaded from .nightgauge/config.yaml
}
```

Key features:

- **Lazy config loading** — Configuration loaded on first access
- **Role classification** — Primary, secondary, or shared for routing
- **GitHub integration** — Extracts owner/repo/project from config

---

## Repository Management

### Repository Roles

Roles classify repositories for routing and display purposes:

| Role        | Description                 | Example Use Case     |
| ----------- | --------------------------- | -------------------- |
| `primary`   | Main development repository | App frontend/backend |
| `secondary` | Supporting repository       | Admin tools, scripts |
| `shared`    | Shared libraries or types   | Shared UI components |

Roles are optional—omit for simple workspaces.

### Repository Switching

Switch between repositories using:

1. **Status bar** — Click the repository indicator (left side)
2. **Command palette** — Run "Nightgauge: Switch Repository"
3. **Keyboard shortcut** — Configurable in keybindings

The repository switcher
(`packages/nightgauge-vscode/src/views/RepositorySwitcher.ts`) displays:

- Current repository name
- Repository role
- Ready/in-progress issue counts
- Quick pick with all available repositories

```
┌────────────────────────────────────────────────────────────────┐
│ $(repo) frontend | 3 ready, 1 in progress                      │
└────────────────────────────────────────────────────────────────┘
```

### Current Repository Context

The current repository affects:

- **Pipeline execution** — Context files written to current repo's
  `.nightgauge/`
- **Issue queries** — Filters by current repo's GitHub config
- **Documentation loading** — Reads from current repo's `docs/`
- **Working directory** — Commands execute in current repo's path

---

## Routing Configuration

### Label-Based Routing

Route issues to repositories based on labels:

```yaml
routing:
  patterns:
    "area:frontend": frontend
    "area:backend": backend
    "area:api": backend
  default_repository: frontend
```

When an issue is picked up:

1. Check issue labels against `patterns` (exact match)
2. First matching pattern determines target repository
3. If no match, use `default_repository`
4. If no default, use current repository

### Pattern Matching

Patterns are matched exactly. For label `area:frontend`, use pattern key
`"area:frontend"`.

| Label              | Pattern           | Matches? |
| ------------------ | ----------------- | -------- |
| `area:frontend`    | `"area:frontend"` | Yes      |
| `area:frontend`    | `"frontend"`      | No       |
| `area:frontend-ui` | `"area:frontend"` | No       |

### Routing Examples

#### Web Application (Frontend + Backend)

```yaml
routing:
  patterns:
    "area:frontend": web-app
    "area:backend": api-service
    "area:database": api-service
  default_repository: web-app
```

#### Microservices

```yaml
routing:
  patterns:
    "service:auth": auth-service
    "service:payments": payments-service
    "service:notifications": notification-service
  default_repository: api-gateway
```

---

## Knowledge Configuration

The `knowledge:` section configures workspace-level knowledge aggregation for
multi-repository workspaces. When present, the Knowledge Explorer and wiki-link
resolver use these settings to aggregate and cross-link knowledge files across
repositories.

This section is **optional** — omitting it uses the defaults below.

### Configuration Options

```yaml
# .vscode/nightgauge-workspace.yaml
knowledge:
  workspace_root: .nightgauge/knowledge/ # default
  aggregate: true # default
  cross_repo_links: true # default
```

| Field              | Type    | Default                  | Description                                                               |
| ------------------ | ------- | ------------------------ | ------------------------------------------------------------------------- |
| `workspace_root`   | string  | `.nightgauge/knowledge/` | Root directory for aggregated knowledge files, relative to workspace root |
| `aggregate`        | boolean | `true`                   | Aggregate knowledge files from all repositories into `workspace_root`     |
| `cross_repo_links` | boolean | `true`                   | Resolve and follow wiki-links across repositories                         |

### Defaults

When the `knowledge:` section is absent from the config file, the parser applies
these defaults automatically — no configuration is required to enable basic
knowledge aggregation.

### Examples

#### Minimal (all defaults)

```yaml
workspace:
  name: my-workspace
repositories:
  - name: frontend
    path: ./frontend
  - name: backend
    path: ./backend
# knowledge: section omitted — uses defaults
```

#### Custom knowledge root

```yaml
workspace:
  name: my-workspace
repositories:
  - name: frontend
    path: ./frontend
knowledge:
  workspace_root: .docs/knowledge/
  aggregate: true
  cross_repo_links: false # disable cross-repo wiki-link resolution
```

#### Aggregation disabled

```yaml
knowledge:
  aggregate: false
  cross_repo_links: false
```

---

## Cross-Repository Workflows

### Epic Decomposition

Epics can span multiple repositories. The `nightgauge-issue-create` skill
supports:

- **AI-powered decomposition** — Break epic into repository-specific sub-issues
- **Cross-repo linking** — Sub-issues link back to parent epic
- **Automatic routing** — Each sub-issue routed to appropriate repository

```
┌─────────────────────────────────────────────────────────────────┐
│  Epic #100: User Authentication                                  │
├─────────────────────────────────────────────────────────────────┤
│  Sub-issues:                                                     │
│  ├── #101: Login UI [frontend] ──────► frontend repo           │
│  ├── #102: Auth API [backend] ───────► backend repo            │
│  └── #103: Token types [shared] ─────► shared repo             │
└─────────────────────────────────────────────────────────────────┘
```

### Cross-Repo Issue Creation

Create issues in any workspace repository:

```yaml
# In nightgauge-workspace.yaml
epic:
  cross_repo_tracking: true
  shared_milestones: true
```

The skill automatically:

1. Analyzes epic requirements
2. Identifies repository boundaries
3. Creates linked sub-issues in target repos
4. Updates epic body with issue references

### Board-Sync Provisioning

A multi-repo workspace shares one GitHub Project across its member repos, but a
member repo gets **no board automation on its own** — the per-stage status sync
only fires while the pipeline runs a stage, so issues closed out-of-band (manual
close, an external merge) and epics whose children all completed will drift out
of the board's `Status` field and never self-heal. The fix is to install the
nightly sweeps and a per-event reconciler into the workspace's repos.

`workspace provision-board-sync` generates these from the manifest:

```bash
# From anywhere inside the workspace (walks up to the manifest):
nightgauge workspace provision-board-sync            # dry-run plan
nightgauge workspace provision-board-sync --print    # + full rendered YAML
nightgauge workspace provision-board-sync --write    # create the files
```

It resolves every member's `owner/repo` and shared project from each member's
own `.nightgauge/config.yaml` (N:1 topology: all members resolve to the
same project number) and writes:

| File                             | Installed in     | Trigger                        | What it does                                                                                                                    |
| -------------------------------- | ---------------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------- |
| `nightgauge-lifecycle-sweep.yml` | **primary** repo | nightly 02:30 UTC + dispatch   | Loops every member against the shared project: board-status drift, stale blockers, premature/missing Done, closed-with-open-PR. |
| `nightgauge-epic-sweep.yml`      | **primary** repo | nightly 02:00 UTC + dispatch   | Auto-closes completed epics in each member repo and moves them to Done.                                                         |
| `nightgauge-board-done.yml`      | **every** member | `issues`/`pull_request` closed | Reconciles that repo's just-closed item to Done immediately (does not wait for the nightly sweep).                              |

The sweeps live in the **primary** member repo only — one shared-project sweep,
not N racing copies. The per-event `board-done` reconciler must live in **each**
member repo, because a GitHub Actions workflow only fires for events in its own
repo; this does not race the board since each copy only touches its own repo's
closed items.

**Why a single sweep covers the shared project.** `audit lifecycle` resolves
each board item to its own home repo (#3792), so board-status fixes are already
cross-repo within one project invocation. `STALE_EPIC` and `ORPHANED_ISSUE`
detection are per-`--repo`, which is why the generated sweep iterates every
member with the **same** `--project`.

#### Prerequisites (one-time, per workspace)

1. **Token secret.** Each member repo needs an Actions secret (default name
   `BOARD_SYNC_TOKEN`, override with `--token-secret`) holding a PAT with
   **project write** on the project's owner and **issues:write** on every member
   repo. For a shared org project this is one org-scoped fine-grained PAT.
2. **Binary install.** The generated jobs install the CLI via the Homebrew tap
   (`brew install --cask nightgauge/tap/nightgauge`). Override the install step with
   `--install-cmd` for non-brew runners. The default `runs-on` is `self-hosted`
   (`--runner` to change).
3. **Built-in fallback (recommended).** Also enable the GitHub Projects built-in
   **"Item closed → Done"** workflow on the shared project (Project → ⋯ →
   Workflows). It is the cheapest per-event Done path; the provisioned workflows
   are the deterministic belt-and-suspenders on top of it.

The generated files carry a "do not edit — regenerate" banner: re-run
`provision-board-sync` after changing the manifest rather than hand-editing them.

---

## End-to-End Multi-Repo Routing Example

This walkthrough shows the full lifecycle of a multi-repo issue from creation
through pipeline completion.

**Scenario**: A developer opens an issue in the `nightgauge` repo that
requires changes in both the SDK and the platform API.

**Step 1 — Issue created in primary repo**:

```
Issue #500: Add license validation to pipeline startup
Repository: nightgauge/nightgauge
Labels: [type:feature, size:M, area:sdk, area:platform]
```

**Step 2 — Workspace configuration** (in `nightgauge-workspace.yaml`):

```yaml
repositories:
  - name: nightgauge
    path: ./nightgauge
    remote: nightgauge/nightgauge
  - name: acme-platform
    path: ./acme-platform
    remote: acme/platform

routing:
  patterns:
    "area:sdk": nightgauge
    "area:platform": acme-platform
  default_repository: nightgauge
```

**Step 3 — Pipeline detects multi-repo context**:

The `issue-pickup` stage reads labels and routing configuration:

```
Label scan: [area:sdk, area:platform]
Routing matches:
  area:sdk       → nightgauge (primary)
  area:platform  → acme-platform (cross-repo)
Result: Multi-repo issue detected — primary repo: nightgauge
```

**Step 4 — Feature planning decomposes into sub-issues**:

```
Sub-issue #501: SDK license check hook → nightgauge
Sub-issue #502: Platform license API endpoint → acme-platform
```

Each sub-issue gets its own pipeline context in its target repo:

```
nightgauge/.nightgauge/pipeline/planning-501.json
acme-platform/.nightgauge/pipeline/planning-502.json
```

**Step 5 — Isolated pipeline execution per repo**:

Each sub-issue runs through feature-dev → feature-validate → pr-create in its
own repository with independent branch, context files, and build commands.
Cross-repo state is tracked in the parent epic body via issue references.

**Step 6 — Cross-repo CI self-healing**:

If CI fails in a target repo's PR, the orchestrator reads the failure logs via
GitHub API, diagnoses the error, and pushes fix commits to the feature branch —
no manual intervention or target repo configuration required. See
[RALPH_LOOP.md § Cross-Repository Self-Healing](./RALPH_LOOP.md#cross-repository-self-healing)
for details and a real-world example.

---

## Pipeline Execution

### Repository-Scoped Context Files

Each repository maintains isolated pipeline context:

```
frontend/.nightgauge/
├── config.yaml            # Repository config
├── pipeline/
│   ├── state.json         # Pipeline state
│   ├── issue-42.json      # Issue context
│   ├── planning-42.json   # Planning context
│   └── dev-42.json        # Development context
└── plans/
    └── 42-login-form.md   # Feature plan

backend/.nightgauge/
├── config.yaml            # Separate repository config
├── pipeline/
│   ├── state.json         # Separate pipeline state
│   ├── issue-43.json      # Backend issue context
│   └── ...
└── plans/
    └── 43-auth-api.md     # Backend feature plan
```

### Pipeline Isolation

Each pipeline execution is isolated to the current repository:

- Context files read/written to current repo's `.nightgauge/pipeline/`
- Plans stored in current repo's `.nightgauge/plans/`
- Git operations in current repo's working directory
- CLAUDE.md and docs loaded from current repo

### Working Directory Handling

Skills execute in the current repository's root:

```typescript
const workingDir = RepositoryContextLoader.getInstance().getWorkingDirectory();
// Returns: /path/to/current/repo

// All Bash commands execute here
// All file paths resolved relative to here
```

---

## Knowledge Base

In multi-repository workspace mode, knowledge directories are maintained at two
levels:

| Level     | Location                                  | Scope              |
| --------- | ----------------------------------------- | ------------------ |
| Workspace | `<workspace-root>/.nightgauge/knowledge/` | Cross-repo content |
| Per-repo  | `{repo-root}/.nightgauge/knowledge/`      | Single repository  |

**Workspace knowledge** uses the categories `product/` and `cross-repo/`.
**Per-repo knowledge** uses the categories `epics/` and `features/`.

The workspace-level schema, including the optional `repos` frontmatter field for
cross-repo scoping, is fully documented in
[docs/KNOWLEDGE_BASE.md § Workspace-Level Knowledge Directory](./KNOWLEDGE_BASE.md#workspace-level-knowledge-directory).

### Example Multi-Repo Knowledge Layout

```text
acme/                                       ← workspace root
├── .vscode/
│   └── nightgauge-workspace.yaml
├── .nightgauge/
│   └── knowledge/                           ← workspace knowledge
│       ├── product/
│       │   └── q3-roadmap/
│       │       ├── PRD.md                   ← repos: [nightgauge, acme-platform]
│       │       └── decisions.md
│       └── cross-repo/
│           └── 1695-workspace-knowledge-epic/
│               ├── PRD.md                   ← repos: [nightgauge]
│               └── decisions.md
├── nightgauge/
│   └── .nightgauge/
│       └── knowledge/                       ← repo-level knowledge (nightgauge)
│           ├── epics/
│           └── features/
└── acme-platform/
    └── .nightgauge/
        └── knowledge/                       ← repo-level knowledge (platform)
            ├── epics/
            └── features/
```

---

## UI Components

### Status Bar Indicator

The repository switcher status bar item shows:

```
$(repo) frontend | 3 ready, 1 in progress
```

- **Icon** — Repository icon from VSCode Codicons
- **Name** — Current repository name
- **Stats** — Ready and in-progress issue counts (if available)

The status bar only appears in multi-workspace mode.

### Repository Quick Pick

Click the status bar to open the repository picker:

```
┌────────────────────────────────────────────────────────────────┐
│ Select a repository to switch to                                │
├────────────────────────────────────────────────────────────────┤
│ ✓ frontend                        primary | Project #10        │
│   /path/to/workspace/packages/frontend                         │
├────────────────────────────────────────────────────────────────┤
│   backend                         primary | Project #10        │
│   /path/to/workspace/packages/backend                          │
├────────────────────────────────────────────────────────────────┤
│   shared-types                    shared                       │
│   /path/to/workspace/packages/types                            │
└────────────────────────────────────────────────────────────────┘
```

### Context Variable

The context variable `nightgauge.multiRepoMode` is set for conditional UI:

```json
{
  "when": "nightgauge.multiRepoMode"
}
```

Use this in `package.json` for conditional menu items or keybindings.

---

## Migration Guide

### From Single-Repo to Multi-Repo

#### Step 1: Create Workspace Configuration

Create `.vscode/nightgauge-workspace.yaml`:

```yaml
workspace:
  name: "My Workspace"

repositories:
  - name: myapp
    path: .
    role: primary
```

This is the minimal configuration—it wraps your existing repo.

#### Step 2: Add Additional Repositories

As you add repos to your workspace:

```yaml
repositories:
  - name: myapp
    path: .
    role: primary
  - name: another-repo
    path: ../another-repo
    role: secondary
```

#### Step 3: Configure Routing (Optional)

Add routing patterns for automatic issue assignment:

```yaml
routing:
  patterns:
    "area:other": another-repo
  default_repository: myapp
```

### Validation Steps

After migration:

1. **Reload VSCode** — Trigger workspace detection
2. **Check Output panel** — Look for "Nightgauge" channel logs
3. **Verify status bar** — Repository indicator should appear
4. **Test switching** — Click status bar to switch repos
5. **Run pipeline** — Verify context files in correct location

---

## Troubleshooting

### Workspace Not Detected

**Symptoms**: Repository switcher not shown, single-repo behavior

**Solutions**:

1. Verify `.vscode/nightgauge-workspace.yaml` exists
2. Check YAML syntax with a linter
3. Check Output panel (Nightgauge) for errors
4. Ensure `workspace.name` and `repositories` are present

### Repository Paths Not Resolving

**Symptoms**: "Repository not found" errors

**Solutions**:

1. Paths are relative to workspace root (where `.vscode/` is)
2. Use `./` prefix for subdirectories: `./packages/frontend`
3. Use `../` for sibling directories: `../other-repo`
4. Absolute paths are not supported

### Routing Not Working

**Symptoms**: Issues not routed to expected repository

**Solutions**:

1. Check pattern matches label exactly: `"area:frontend"` not `"frontend"`
2. Verify `routing.patterns` uses repository names from `repositories[].name`
3. Check `default_repository` is set as fallback
4. Labels are case-sensitive

### Context Files in Wrong Location

**Symptoms**: Pipeline state not found, stale context

**Solutions**:

1. Verify current repository (check status bar)
2. Switch to correct repository before running pipeline
3. Check `.nightgauge/pipeline/` exists in target repo
4. Delete stale context files and re-run pipeline

### Auto-Detection Not Working

**Symptoms**: Multiple folders but single-repo mode

**Requirements for auto-detection**:

1. Multiple workspace folders (File → Add Folder to Workspace)
2. Each folder contains `.nightgauge/config.yaml` (or
   legacy `nightgauge.yaml`)
3. At least 2 folders meet both criteria

If auto-detection fails, create explicit
`.vscode/nightgauge-workspace.yaml`.

---

## FAQ

### Can I use multi-repo mode with a single repository?

Yes—create a workspace config listing your single repo. This is useful as a
starting point before adding more repos.

### Does each repository need its own `.nightgauge/config.yaml`?

No—only the workspace config (`.vscode/nightgauge-workspace.yaml`) is
required. Individual repos can optionally have their own config for GitHub
project integration.

### How do I run pipelines across multiple repos?

Switch repositories as needed. Each pipeline stage runs in the context of the
current repository. Cross-repo coordination uses epics with linked sub-issues.

### Can I have different GitHub projects per repository?

Yes—each repository can have its own `project.number` in its
`.nightgauge/config.yaml`. The pipeline uses the current repo's project
configuration.

### What happens to existing context files after switching repos?

Context files remain in their respective repositories. Switching repos changes
which `.nightgauge/pipeline/` directory is used, but doesn't affect other
repos.

---

## Multi-Forge Workspaces

> **Design context** — the forge abstraction layer behind multi-forge
> routing is documented in [FORGE_ABSTRACTION.md](FORGE_ABSTRACTION.md)
> (interface layout, adapter contract, lifecycle, sentinel errors, GitLab
> CE-vs-EE feature matrix). The schema migration mechanics live in
> [ADR-009](decisions/009-workspace-schema-migration.md). This section
> covers the **operator-facing** workspace configuration.

Nightgauge supports workspaces that span multiple forges — for example,
some repositories on GitHub and others on a self-hosted GitLab instance.

### Configuring multiple forges

Add a `forges:` block to `.nightgauge/config.yaml`. Each entry maps a forge
ID (used throughout the config) to the adapter kind and its credentials:

```yaml
forges:
  github:
    kind: github
    owner: nightgauge
    project_number: 1
    token_env: GITHUB_TOKEN

  acme-gitlab:
    kind: gitlab
    host: gitlab.mycompany.com # omit for gitlab.com SaaS
    owner: acme
    token_env: GITLAB_TOKEN
```

### Assigning repositories to forges

In `autonomous.repositories`, add a `forge:` field to each entry whose forge
differs from the workspace default:

```yaml
autonomous:
  repositories:
    nightgauge/nightgauge:
      max_concurrent: 2
      forge: github # matches key in forges: block

    acme/platform:
      sequential: true
      forge: acme-gitlab # routes this repo to the GitLab adapter
```

Repositories without a `forge:` field resolve through the default forge
(the `forge` set as the router's default — typically "github").

### Cross-forge link resolution

When `Router.ResolveLink` is called across forge boundaries, it produces full
URLs instead of compact slug references:

| Scenario             | Output form                                |
| -------------------- | ------------------------------------------ |
| Same forge           | `owner/repo#42`                            |
| Cross-forge → GitHub | `https://github.com/owner/repo/issues/42`  |
| Cross-forge → GitLab | `https://<host>/group/project/-/issues/42` |

Issue body references using full GitHub or GitLab URLs are automatically
detected by the depgraph parser and normalized to `CrossRepoRef` entries with
a `SourceURL` field for rendering clickable links.

### Validating the workspace configuration

Run `nightgauge workspace doctor` to check for misconfigurations:

```
$ nightgauge workspace doctor

Registered forges: [acme-gitlab github]

REPO                          FORGE ID      KIND    REACHABLE  AUTH
nightgauge/nightgauge      github        github  yes        ok
acme/platform                acme-gitlab  gitlab  yes        ok

No validation errors.
```

Add `--json` for machine-readable output:

```bash
nightgauge workspace doctor --json
```

The doctor checks:

- **Dangling forge refs** — a repo's `forge:` field references an ID not in the
  `forges:` block (fatal error; blocks startup).
- **Orphan forges** — a forge is registered in `forges:` but no repo maps to it
  (warning only; the forge is still usable via `--forge <id>`).

---

## Related Documentation

- [Configuration Reference](./CONFIGURATION.md#workspace-configuration) — Full
  workspace schema and field reference
- [Architecture](./ARCHITECTURE.md) — System design and component overview
- [Context Architecture](./CONTEXT_ARCHITECTURE.md) — Pipeline handoff schemas
- [Troubleshooting](./TROUBLESHOOTING.md) — General troubleshooting guide

---

## Author

nightgauge
