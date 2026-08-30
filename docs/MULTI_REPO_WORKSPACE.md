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
  nightgaugeConfig?: NightgaugeConfig; // Lazy-loaded from .nightgauge/config.yaml
}
```

Key features:

- **Lazy config loading** — Configuration loaded on first access
- **Role classification** — Primary, secondary, or shared for routing
- **GitHub integration** — Extracts owner/repo/project from config

---

## Repository Management

### Workspace Repositories section

Nightgauge Settings → **Workspace Repositories** is the supported way to change
which repositories Nightgauge manages. It lists every configured entry with its
path, role and resolved board, offers per-row removal, and presents git
checkouts in the workspace that the manifest does **not** list as one-click
"Add to workspace" candidates.

Before this section existed the only way to bring a repository under management
was to hand-edit `.vscode/nightgauge-workspace.yaml` and reload the window,
which was documented nowhere in the UI. The specific confusion it removes: a
folder open in the VSCode workspace but absent from the manifest is invisible to
Nightgauge — it appears in no tree and **no attention producer evaluates it** —
because `WorkspaceManager` populates its repository map exclusively from the
manifest once one exists.

**Every mutation goes through the Go writer** (`workspace.repoAdd` /
`workspace.repoRemove` over IPC → `internal/workspacemanifest`). The panel never
parses, serializes or edits the YAML: the manifest carries load-bearing comments
— the `project_number: 0` footgun is documented only there — and a second writer
would reflow them. Validation failures are surfaced verbatim; the panel never
reports success for a write the binary rejected.

**A board is required at entry.** The add path refuses a repository with no
resolvable project board, in the UI _and_ in the daemon, and says a board must
be provisioned first rather than accepting the entry. There is no path from this
section that produces `project_number: 0`, which would resolve to project 0 and
silently misroute every issue the repository produces.

**Removal warns before orphaning routing.** If `routing.default_repository` or a
`routing.patterns[].preferred_repo` still names the repository, removal is
refused once with the references named, and proceeds only on explicit
confirmation.

After any change the section and the Repositories tree both reflect it with **no
window reload** — the manifest watcher fires unconditionally, and the panel also
reloads `WorkspaceManager` explicitly so the tree updates on the same click.

Adding a repository does **not** install board-sync automation into it. Those
workflows are generated from the manifest, so re-run
`nightgauge workspace provision-board-sync --write` afterwards; the panel says so
on every successful add.

The equivalent CLI is `nightgauge workspace repo add|remove|list`, and the
`coverage-gap` attention card offers the same repair as a one-click verb.

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

### Single-Resolver Contract (Issues #271, #313)

`.vscode/nightgauge-workspace.yaml`'s `repositories[].project_number` is
**not** an independent authority for "which project board does this repo
use?". It exists only as routing-manifest scaffolding input — a starting
point the `nightgauge-issue-create` skill's Phase 2.4 resolves through the
runtime resolver before using it, never a value consumed directly. It is also
what `doctor` validates _against_ the resolver, so the resolver must never
read it back in.

The authoritative answer comes from `config.ResolveRepoProject`, in this
precedence order. Every consumer — `nightgauge project resolve`, issue-create,
board sync, `doctor`, the stranded-ready sweep, **and the autonomous
scheduler's repo set** — uses this one lookup:

| #   | Source                                                              | Reason code            |
| --- | ------------------------------------------------------------------- | ---------------------- |
| 1   | The target is the local repo → its own `project.number`             | `local-config`         |
| 2   | `autonomous.repositories.<repo>.project_number` (operator override) | `explicit-mapping`     |
| 3   | The target repo's own `.nightgauge/config.yaml` `project.number`    | `member-config`        |
| 4   | The workspace-wide default board (`--project` / `project.number`)   | `shared-board-default` |
| 5   | Nothing — board `0`                                                 | `unmapped`             |

The reason code travels with every answer because two kinds of caller act on
it differently, and that difference must be explicit:

- **Callers that FILE** (issue-create, board sync, `project resolve`) accept
  only rungs 1–3. A `shared-board-default` is refused with an error naming the
  config key to set — defaulting a cross-repo target to the primary board is
  how #3232 silently misrouted issues.
- **Callers that POLL** (the autonomous scheduler) accept rung 4 as well.
  Scanning the wrong board wastes a poll; it cannot misfile anything.

Before #313 those two policies were two separate lookups. The resolver refused
to answer for an unmapped sibling while the scheduler built its repo set from
the bare top-level project number and polled that board for every repo — so on
a workspace whose siblings declared boards 4, 5, and 6, the scheduler polled
board 3 for all of them. Because both behaviors were individually defensible
and nothing reconciled them, a diagnostic written against either one stated
falsehoods about the other (#280 shipped a `doctor` message claiming "the
scheduler polls no board for this repo").

Resolve a repo's authoritative project number with:

```bash
nightgauge project resolve --repo <owner>/<repo> --json
# → { "number": 4, "owner": "...", "owner_type": "...", "id": "...", "title": "...", "url": "..." }
```

This is the same subcommand's `--number` mode (`nightgauge project resolve
--number N`) extended with a `--repo` mode — `--number` resolves ownership for
an already-known project number; `--repo` resolves the number itself, then
feeds it through the same ownership resolution, so the output shape is
identical either way. `--number` and `--repo` are mutually exclusive.

Any workspace-yaml `project_number` that disagrees with the runtime-resolved
value is a **misconfiguration**, not an alternate valid mapping:

- `nightgauge doctor` runs a `project_mapping` check for every
  `repositories[]` entry with a non-zero `project_number` and fails
  (`exit_code: 2`) on any mismatch, naming both values and the exact
  `.nightgauge/config.yaml` path to fix.
- The autonomous scheduler logs a loud (but non-fatal — see [Known
  Limitations](#known-limitations)) startup warning on the same mismatch,
  since a long-running scheduler process should not refuse to start over a
  config drift `doctor` already surfaces.
- An `attention sweep` producer (`stranded-ready-items`) cards any issue that
  is "Ready" on the stale workspace-yaml board while the two sources
  disagree — the issue the scheduler will never dispatch because it only
  polls the runtime-resolved board.

A repo that resolves only to `shared-board-default` is reported separately, as
a warning rather than a failure: nothing is misrouted yet, but the manifest
value was never compared against anything, so it is **not** agreement (#280).
That warning names the board the scheduler falls back to _and_ the fact that
filing will refuse — both read off the same resolution, so neither statement
can drift out of step with the other.

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

### Policy: One Issue, One Repo (#127)

**An epic may span repositories; a single issue's implementation work may
not.** `feature-dev` runs inside one issue's worktree and may only write
files inside that worktree's repository. If a feature genuinely needs
coordinated changes in multiple repos, decompose it into one sub-issue per
repository at issue-creation time (see Epic Decomposition below) — never rely
on a single feature-dev run to edit sibling repo checkouts.

This is enforced structurally, not by convention: the write-containment
mechanism (`worktreeContainment.ts` + `skillRunner.ts`, #129) captures a
baseline before each stage and detects any file written outside the stage's
own worktree, attributing and quarantining the out-of-scope patch instead of
letting it land silently in a sibling working tree. `pr-create` opens exactly
one PR, on the issue's own repo branch — it has no way to express a change set
spanning repos, so an issue whose implementation needs cross-repo writes will
never produce a landable PR no matter how the stages report their status.

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

### Cross-Repo Epic Rollup (#1181)

An epic number is not a coordinate.

When a cross-repo sub-issue's PR merges, the post-merge hook auto-closes the
parent epic once every sub-issue is closed. **The epic is resolved against its
own repository**, taken from GitHub's native sub-issue link
(`types.Issue.ParentIssueRepo`, from the same `repository { nameWithOwner }`
selection sub-issues and blockers have always carried). Nothing parses issue
bodies for `Part of owner/repo#N`; the native link is the single authority.

This is load-bearing because **issue numbers are per-repository**. Resolving a
parent epic's number against the _sub-issue's_ repo does not fail cleanly — it
has two faces, both observed in one session on a three-repo workspace:

| Face       | What the number hit in the wrong repo | Result                                       |
| ---------- | ------------------------------------- | -------------------------------------------- |
| **Loud**   | a merged **pull request**             | `Could not resolve to an Issue`, `failed`    |
| **Silent** | an unrelated **real, closed issue**   | `Total == 0` → `no_subs`, **`failed:false`** |

The silent face is the dangerous one: the hook reported success, the real epic
was never evaluated, and the epic sat open with every child closed — the same
outcome as epic #342 in `AGENTS.md`, through a different door.

Two mechanisms keep it fixed:

- **`github.EpicRef`** carries the epic's own `Owner`/`Repo` alongside its
  `Number`. There is no "ambient repo" default to inherit by omission, and a
  ref with no repository is refused (`epic_repo_missing`) before any API call.
- **An identity guard.** The post-merge path also passes the merged sub-issue
  (`ExpectSubIssueNumber`/`ExpectSubIssueRepo`). The epic must list it; if it
  does not, the check fails with `wrong_epic` instead of answering `no_subs`.
  `no_subs` therefore remains reachable only on the sweep path, where no
  triggering sub-issue exists and "this epic has no sub-issues" is a real
  answer.

The nightly sweep and the post-merge hook now read the same membership record
(`EpicCompletionResult.SubIssues`), so they cannot answer "does this epic have
sub-issues?" differently the way they did when the hook was looking in the
wrong repository.

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

### Editing the Manifest — `workspace repo` (#703)

`.vscode/nightgauge-workspace.yaml` has exactly one supported writer. Every
surface that mutates it — the settings UI, an attention repair verb, an agent in
a terminal — goes through `nightgauge workspace repo`, so the guards live in one
place instead of being reimplemented (and subtly re-broken) per caller.

```bash
nightgauge workspace repo list [--json]
nightgauge workspace repo add --name <n> --path <p> [--role <r>] [--project <n>]
nightgauge workspace repo remove --name <n> [--force]
```

**Guards.** `add` refuses a duplicate name, a path that is not a directory, a
path with no `.git`, and any non-positive `--project`. When `--project` is
omitted it resolves through the single authoritative resolver (see
[Single-Resolver Contract](#single-resolver-contract-issues-271-313)) and fails loudly rather than
writing a placeholder. `remove` refuses to orphan `routing.default_repository`
or a `routing.patterns[].preferred_repo` reference unless `--force` is passed.

**`project_number: 0` is now unwritable.** A zero resolves to project 0 and
silently misroutes issues. Before #703 that rule existed only as a comment in
this repository's own manifest and in operator memory; it is now enforced by
every write path and by the validator that gates them.

**Writes preserve the file.** The manifest carries load-bearing comments, so
writes are performed as line splices against the original bytes rather than by
re-marshalling: the target entry's line range is located and replaced, and every
other byte is left untouched. An add-then-remove cycle returns the file to a
byte-identical state, which is pinned by a test that runs against this
repository's actual manifest.

A marshal-based writer cannot do this. Measured on that same file, a
`gopkg.in/yaml.v3` node round-trip preserves comments, key order and
indentation but drops **every blank line** — 9 of them — reflowing a
hand-maintained file on the first write.

**Comment ownership.** yaml attaches a comment block to the node _below_ it, so
this repository's four-line `project_number` block — which documents the
whole list — is owned by the _first_ repositories entry. Removing that entry
therefore must not delete its leading comment, and does not: `remove` deletes an
entry's own body lines only, leaves the comment in place, and tells the operator
it did so. Text is never lost; a retained comment may end up heading an entry it
does not describe, which is the deliberate tradeoff over silent deletion.

Writes are atomic (temp file plus rename, preserving the original file mode) and
are validated before they land — a change that would produce an invalid manifest
is refused with the original left untouched.

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

### Write Containment (Issue #129)

Isolation of the working directory is not the same as isolation of writes. A
stage's CWD is its worktree, but nothing in the filesystem stops it writing
anywhere else — and when an issue's work genuinely lives in another repo of the
workspace, the agent reasons correctly about where the code is and writes into
**that repo's live checkout**: uncommitted, on whatever branch the operator has
out. Worktree isolation used to be a convention the agent happened to follow.
It is now a boundary the pipeline enforces.

Every stage dispatch snapshots `git status --porcelain` for each configured
workspace repo it does not own, and compares after the stage closes:

| Transition during the stage        | Verdict                                            |
| ---------------------------------- | -------------------------------------------------- |
| clean → dirty                      | **Stage failure.** Attributed, captured, reported. |
| dirty → dirty, fingerprint changed | Warning only. Never attributed, never captured.    |
| dirty at baseline, unchanged       | Ignored — the operator's standing work.            |
| anything under `.nightgauge/`      | Ignored — the pipeline mirrors artifacts there.    |

The asymmetry is deliberate. A sibling repo is very often dirty because the
**operator** is working in it, and an operator's edit is indistinguishable from
a stage write once both have happened. Only a path whose pre-stage content was
HEAD-or-absent can be attributed with confidence, so that is the only case that
fails the run. Under-detecting costs one more incident; misattributing costs the
operator's trust, and — if the response were destructive — their work.

The response is therefore **capture, never mutate**. The attributed changes are
written as a `git apply`-able patch under `.nightgauge/containment/` in the
stage repo's canonical root, which survives the worktree teardown a re-dispatch
performs. Nothing in the other repo is staged, committed, stashed, reverted or
touched. (This is why the #128 work-in-progress preservation is not reused: it
commits the worktree in place, which would sweep up the operator's unrelated
files, move their HEAD, and refuse outright on `main`.)

Scope notes:

- The repo's **main checkout is in scope** while a stage runs in a linked
  worktree of it — a separate working tree, and one that is frequently sitting
  on `main`.
- In single-repo mode, and for a stage running directly in its repo rather than
  in a worktree, there is nothing out of bounds and the check is a no-op.
- The check fails **open**: if git cannot be consulted the stage is unaffected.

See [TROUBLESHOOTING.md](TROUBLESHOOTING.md) for recovery steps.

### Where a Run Roots — and When It Refuses (Issue #882)

Containment above catches a stage that _writes_ outside its worktree. It cannot
catch the case where the pipeline hands the stage the wrong root to begin with.

Every piece of a run's on-disk state — its worktree, `runtime-{issue}-{runId}.json`,
stage context, exit records, lifecycle trace, history record — and every branch
the run creates roots at the run's **target** repo, never at the repo the
scheduler happened to be launched in. The mapping from an `owner/repo` slug to
that repo's checkout is the workspace repo registry: the daemon builds it at
startup from its client resolver, and a CLI invocation builds the same registry
from the launch root's config, the `.vscode/nightgauge-workspace.yaml`
`repositories[]` entries, and the sibling checkouts carrying
`.nightgauge/config.yaml`.

CLI mode used to build no registry at all. Because `nightgauge queue add <N>
--repo <other/repo>` is the documented way to queue cross-repo work, the CLI
could be asked for precisely the run it could not express: the target repo's
work was created under the launch repo, and the epic base branch was cut from
the **launch** repo's default branch and pushed to the **launch** repo's remote.
Nothing about that mechanism guaranteed the branch would be empty.

**The resolution fails closed.** When the launch root's own identity is known —
its config names `owner` + `defaultRepo`, or its `origin` remote is a forge URL
— and it is not the target repo, and the registry has no path for the target
repo, the run is **refused** at a preflight beside the run-identity, license and
identity gates. It is refused before any worktree, branch, push or stage
dispatch, and the error names the repo that could not be resolved. Add the repo
to the workspace and re-queue it.

The refusal is deliberate and not negotiable by fallback: rooting at the launch
repo is not a degraded mode, it is a write into a _real_ repository that has
nothing to do with the change. Not running is the better failure.

The one case that is not refused is the one with no evidence: a launch root that
names no repository at all (no config identifying it, no forge `origin`). There
is nothing to compare the target against, so the run proceeds from the launch
root with a warning. Refusing there would strand every run in a workspace that
merely declines to name itself — stopping work that was never in danger, which
is the one failure a fail-closed gate cannot afford.

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

## Board Reachability (issue #280)

**Config agreement is not evidence that work is reachable.** A repo's project
board is named in two places — the workspace manifest's
`repositories[].project_number` (Source A) and the runtime-resolved
`autonomous.repositories.<repo>.project_number` (Source B) — and both can agree
perfectly on a board that holds none of the repo's issues.

That is not hypothetical. It is the state #280 was filed from: ~28 open issues
lived on board A while both config sources named board B. The scheduler polled
B, found nothing, and reported `0 candidates` for hours. `nightgauge doctor`
passed, the stranded-ready sweep raised nothing, and no log named the
condition — because every check compared configuration against configuration.
An audit that validates a source against itself is not an audit.

### The contract

Reachability is decided by **issue→board membership, queried from the forge**,
never inferred from config:

| Check                         | Question it answers                                 | What it cannot tell you   |
| ----------------------------- | --------------------------------------------------- | ------------------------- |
| `doctor` → `project`          | Does the configured board resolve?                  | Whether it holds anything |
| `doctor` → `project_mapping`  | Do Source A and Source B agree?                     | Whether either is correct |
| `doctor` → `board_population` | Does the polled board hold this repo's open issues? | —                         |
| `stranded-ready-items` sweep  | Same, continuously, as an Action Center card        | —                         |

Only the last two consult ground truth. The first two are necessary and jointly
insufficient — treat a green `project` plus a green `project_mapping` as "the
config is internally consistent", nothing more.

### Diagnosing an idle scheduler

The scheduler logs one line per repo it is responsible for, every prioritize
pass:

```text
autonomous: repo=acme/platform project=4 nodes=0 open=0 dispatchable=0 — this repo
contributed NO nodes; if it has open issues, they are not on project 4
```

`nodes=0` for a repo that visibly has open issues is the signature of an
unreachable board. Confirm with `nightgauge doctor` (`board_population` names
the board(s) that actually hold the work), then either point the config at that
board or move the items onto the polled one. Nightgauge will not move them for
you — which board is correct is a human decision, and moving them on a guess is
worse than doing nothing.

### Unverifiable mappings

A repo listed in the manifest with **no** runtime mapping is reported as its
own condition, not folded into "agree" — the cross-check never happened, and
silence about that is not evidence of health:

```text
project mapping unverifiable for acme/platform: workspace yaml says project 4,
but runtime config has no mapping (...) — the manifest value is unchecked, and
issue creation or board sync targeting this repo will fail until
autonomous.repositories.acme/platform.project_number is set
```

This is a **warning**, not a failure, and the reason is worth understanding
because two different resolvers are in play:

| Caller                                      | Resolver                   | Behavior with no mapping              |
| ------------------------------------------- | -------------------------- | ------------------------------------- |
| `project resolve`, issue-create, board sync | `ResolveRepoProjectNumber` | **Errors** — refuses to guess (#3232) |
| Autonomous scheduler                        | one `RepoConfig` per repo  | Polls the top-level project number    |

The resolver's refusal is deliberate: defaulting a cross-repo target to the
primary board silently misrouted new issues, so anything that _files_ something
must fail loudly. The scheduler never calls it and polls the shared board
regardless. So an unverifiable mapping breaks issue creation for that repo
while leaving dispatch working — which is why the check warns rather than
declaring the workspace broken.

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

In VS Code, open **Nightgauge Settings → Project Board**, select a repository,
and manage its project assignments under **Repository project routing**.
Nightgauge discovers Projects linked to that GitHub repository, but does not
guess among multiple linked boards. Use `projects[]` when one repository
participates in several boards and mark exactly one assignment as the default.
The legacy `project.number` form remains supported as a one-project shorthand.

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
