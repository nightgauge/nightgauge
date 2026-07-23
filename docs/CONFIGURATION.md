# Configuration Reference

This document describes all configuration options available for customizing
Nightgauge pipeline behavior.

> **Note:** The config file was renamed from `nightgauge.yaml` to
> `config.yaml` in v0.8.0 (Issue #433). The legacy `nightgauge.yaml` is
> still supported for backward compatibility but will show deprecation warnings.
> Run the `Nightgauge: Migrate Config File` command in VSCode to migrate
> automatically.
>
> **See also:** [Migration Guide](./MIGRATION.md#config-file-migration) for
> step-by-step migration instructions and [Deprecations](./DEPRECATIONS.md) for
> the full timeline.
>
> **MCP servers:** To configure MCP tool servers for pipeline stage agents, see
> [MCP Integration Guide](./MCP_INTEGRATION.md).

## Overview

Nightgauge implements a **7-tier configuration system** with precedence
from built-in defaults through CLI flags. This follows patterns used by Git,
Cargo, and Claude Code, with the addition of local developer overrides and a
runtime (ephemeral) tier for VSCode UI state.

### 3-Tier Conceptual Model

At a high level, configuration is organized into three conceptual tiers that
map to different storage locations and ownership models:

| Conceptual Tier | Storage                                           | Committed? | Owner                                      |
| --------------- | ------------------------------------------------- | ---------- | ------------------------------------------ |
| **Team**        | `.nightgauge/config.yaml`                         | Yes        | Team — stable, reviewed via PR             |
| **Machine**     | `~/.nightgauge/config.yaml`                       | No         | Developer — personal preferences, per-host |
| **Runtime**     | VSCode `globalState` / `workspaceState` (memento) | n/a        | UI — ephemeral, never produces a YAML diff |

These three conceptual tiers map to seven technical tiers in the merge engine
(see precedence diagram below). The key placement guide later in this section
maps each setting key to its conceptual tier.

For the detailed key-by-key tier classification, see
[`docs/SETTINGS_ARCHITECTURE.md`](SETTINGS_ARCHITECTURE.md).

### 7-Tier Configuration Precedence

Configuration values are resolved using a layered merge system. Higher tiers
override lower tiers:

```
┌─────────────────────────────────────────────────────────────────┐
│                    7-TIER CONFIG PRECEDENCE                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  7. CLI flags (--config-*)             ← Highest priority       │
│         ↓ if not set                                             │
│  6. Environment Variables (NIGHTGAUGE_*)                    │
│         ↓ if not set                                             │
│  5. Runtime (VSCode memento/globalState)  ← ephemeral UI state  │
│         ↓ if not set                                             │
│  4. Local Config (.nightgauge/config.local.yaml)            │
│         ↓ if not set                     [gitignored]            │
│  3. Project Config (.nightgauge/config.yaml)                │
│         ↓ if not set                       [team tier]           │
│  2. Global Config (~/.nightgauge/config.yaml)               │
│         ↓ if not set                       [machine tier]        │
│  1. Built-in Defaults                  ← Lowest priority        │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Configuration Files

| Tier | File / Storage                          | Conceptual Tier | Purpose                      | Committed | Scope            |
| ---- | --------------------------------------- | --------------- | ---------------------------- | --------- | ---------------- |
| 1    | (built-in)                              | —               | Sensible defaults            | -         | All              |
| 2    | `~/.nightgauge/config.yaml`             | Machine         | User-wide preferences        | No        | All repositories |
| 3    | `.nightgauge/config.yaml`               | Team            | Project/team settings        | Yes       | Current repo     |
| 4    | `.nightgauge/config.local.yaml`         | Machine         | Developer overrides          | No        | Current repo     |
| 5    | VSCode `globalState` / `workspaceState` | Runtime         | Ephemeral UI state (memento) | No        | Current session  |
| 6    | `NIGHTGAUGE_*` environment vars         | —               | CI/CD and process override   | -         | Current process  |
| 7    | `--config-*` CLI flags                  | —               | One-time override            | -         | Current command  |
|      | `.nightgauge/nightgauge.yaml` (legacy)  | Team            | Deprecated project config    | Yes       | Current repo     |

### Tier Placement Guide

Use this guide to decide which tier a setting belongs to:

| Setting Type                                           | Conceptual Tier | Technical Tier | Reason                                          |
| ------------------------------------------------------ | --------------- | -------------- | ----------------------------------------------- |
| Team/project pipeline policy                           | Team            | Project (3)    | Shared via git; same value for all contributors |
| Personal user preferences                              | Machine         | Global (2)     | Follows the developer across all repositories   |
| Developer-local overrides                              | Machine         | Local (4)      | Per-developer, not committed                    |
| UI ephemeral state (concurrency, paused, picker state) | Runtime         | Runtime (5)    | Flipped often by UI; must not dirty the tree    |
| CI/CD overrides                                        | —               | Env vars (6)   | Set in pipeline configuration                   |
| One-time testing                                       | —               | CLI flags (7)  | Transient, not persisted                        |

**Key examples by conceptual tier:**

| Setting                                | Conceptual Tier | Why                                              |
| -------------------------------------- | --------------- | ------------------------------------------------ |
| `pr.merge_strategy`                    | Team            | Repo policy — reviewed via PR                    |
| `project.number`                       | Team            | Repo-specific GitHub project board ID            |
| `pr.reviewers`                         | Team            | Team membership list                             |
| `pipeline.budget_preset`               | Team            | Project default cost envelope                    |
| `autonomous.scan_interval`             | Team            | Team-wide scheduler tuning                       |
| `pipeline.max_concurrent`              | Runtime         | UI concurrency knob — ephemeral, not committed   |
| `human_in_the_loop.auto_accept_stages` | Team            | Team trust posture (personal override via local) |
| CI token overrides                     | Env vars        | Different tokens per CI environment              |
| `ui.notifications.sounds.*`            | VSCode settings | UI-only, not portable to config.yaml             |
| `ui.output_window.verbose`             | VSCode settings | Display preference, not pipeline behavior        |

> **Behavior vs UI Settings**: Settings under `ui.*` are VSCode-specific and
> should be configured via VSCode settings, not `.nightgauge/config.yaml`.
> See [UI Configuration](#ui-configuration-vscode-specific) for details.

### Merge Semantics

When tiers are merged, these rules apply:

| Type      | Behavior                         | Example                                                  |
| --------- | -------------------------------- | -------------------------------------------------------- |
| Objects   | Deep merge (properties combined) | `pr:` in global + `pr:` in project = merged pr section   |
| Arrays    | Replace (not concatenated)       | `reviewers: [alice]` + `reviewers: [bob]` = `[bob]` only |
| Scalars   | Last (highest tier) value wins   | `delete_branch: true` + `delete_branch: false` = `false` |
| Null      | Explicit null overrides          | `reviewers: null` removes reviewers from lower tier      |
| Undefined | Does not override                | Missing field doesn't clear existing value               |

### Environment Variable Pattern

All options can be overridden via environment variables following this pattern:

```
key.path -> NIGHTGAUGE_KEY_PATH
```

For example:

- `pr.delete_branch` -> `NIGHTGAUGE_PR_DELETE_BRANCH`
- `branch.base` -> `NIGHTGAUGE_BRANCH_BASE`
- `pipeline.ci_timeout` -> `NIGHTGAUGE_PIPELINE_CI_TIMEOUT`

Supported dynamic and compatibility families that cannot be inferred from a
single static path are:

- `NIGHTGAUGE_BATCH_MAX_ISSUES` — maximum issues selected for one batch.
- `NIGHTGAUGE_PIPELINE_RETRY_MAX_AUTO_ATTEMPTS` — canonical retry limit
  override (`NIGHTGAUGE_RETRY_MAX_AUTO_ATTEMPTS` remains a compatibility alias).
- `NIGHTGAUGE_PIPELINE_OUTPUT_TOKEN_LIMIT_<STAGE>` — per-stage output ceiling;
  replace `<STAGE>` with the uppercase stage name and convert hyphens to
  underscores, for example `..._FEATURE_DEV`.

> **Scope note.** The TypeScript layer resolves env overrides generically for
> every key registered in `envVarResolver.ts` (`KNOWN_CONFIG_PATHS`). The Go
> binary supports env overrides for a specific set of keys read at
> point-of-use (e.g. `NIGHTGAUGE_PERFORMANCE_MODE`,
> `NIGHTGAUGE_ADAPTER`, `NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CEILING_USD`)
> — not for arbitrary paths. When adding a new key that needs a Go-side env
> override, wire it explicitly.

### Uniform Resolution & Worktree Behavior

Every synchronous config read resolves the **tier-merged view** — not just
the project file:

- **TypeScript**: all `utils/resolvers/*` getters, the auto-accept loader,
  and the approval dialog read through
  `utils/mergedConfigReader.ts` (global → project → local → env, cached by
  file mtime). The async settings UI uses the full 7-tier
  `configMergeEngine` (adds runtime + CLI).
- **Go**: every gate and command loads via `config.Load(workdir)` →
  `LoadMerged` (machine → project → local), including `approval-gate`
  invoked with `--workdir <worktree>`.
- **Exception (by design)**: `github_user` identity resolution
  (`authResolver.getGitHubUser`) is repo-scoped — local/project only, never
  inherited from the machine config — so multi-account workspaces cannot
  leak the wrong identity (#2487).

Pipeline stages run in **git worktrees**, which changes which tier files are
physically present:

| Tier                                | Reaches a pipeline worktree? | How                                                                                                                             |
| ----------------------------------- | ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| Machine (`~/.nightgauge/…`)         | Always                       | Read from `$HOME` (or `NIGHTGAUGE_CONFIG_HOME` / `XDG_CONFIG_HOME`) — worktree-independent.                                     |
| Project (`.nightgauge/config.yaml`) | Always                       | Tracked file — arrives with the `origin/<base>` checkout. Note: the **committed** content applies, not uncommitted local edits. |
| Local (`config.local.yaml`)         | Always                       | Gitignored, so both worktree paths copy it in: TS `WorktreeManager.create()` and Go `internal/execution/worktree.go`.           |
| Runtime (VSCode memento)            | Orchestrator-level only      | Applied by the extension before dispatch (e.g. `pipeline.max_concurrent`); never visible to Go gates inside worktrees.          |
| Env / CLI                           | Always                       | Inherited by spawned processes / passed per invocation.                                                                         |

**Practical placement guide:**

- Team policy (same for every operator, reviewed): commit it in
  `.nightgauge/config.yaml` via a normal PR.
- Per-operator override for ONE repo (e.g. disable the architecture-approval
  gate for headless runs without a commit): `.nightgauge/config.local.yaml`
  — the settings UI's default save target.
- Per-operator default for ALL repos (identity, adapter, local model
  servers, notification webhooks): `~/.nightgauge/config.yaml` — but
  remember the project tier overrides it where both define a key.
- CI or one-shot pinning: `NIGHTGAUGE_*` env vars / `--config-*` flags.

---

## Global Configuration (Issue #434)

The global configuration file provides user-specific defaults that apply across
all repositories. This is useful for:

- Setting preferred merge strategy once instead of per-repo
- Configuring notification preferences
- Defining default reviewers
- Setting human-in-the-loop preferences

### Global Config Location

The global config path is determined by platform and environment:

| Priority | Check                        | Path                                      |
| -------- | ---------------------------- | ----------------------------------------- |
| 1        | `NIGHTGAUGE_CONFIG_HOME` env | `$NIGHTGAUGE_CONFIG_HOME/config.yaml`     |
| 2        | `XDG_CONFIG_HOME` env        | `$XDG_CONFIG_HOME/nightgauge/config.yaml` |
| 3        | macOS default                | `~/.nightgauge/config.yaml`               |
| 3        | Linux default                | `~/.config/nightgauge/config.yaml`        |
| 3        | Windows default              | `%APPDATA%/nightgauge/config.yaml`        |

### Creating a Global Config

```bash
# macOS/Linux
mkdir -p ~/.nightgauge
cat > ~/.nightgauge/config.yaml << 'EOF'
# Global Nightgauge Configuration
# These settings apply to all repositories

pr:
  merge_strategy: squash
  delete_branch: true
  reviewers:
    - alice

human_in_the_loop:
  auto_accept_stages: false
  auto_accept_permissions: false
EOF
```

### Global vs Project Settings

Some settings make more sense at the global level, others at the project level:

| Setting               | Best At | Reason                                    |
| --------------------- | ------- | ----------------------------------------- |
| `pr.merge_strategy`   | Global  | Personal preference                       |
| `pr.reviewers`        | Project | Team-specific                             |
| `project.number`      | Project | Repo-specific project board               |
| `branch.base`         | Project | Repo-specific (main vs master vs develop) |
| `human_in_the_loop.*` | Global  | Personal trust level                      |

### Viewing Effective Configuration

Use the `/nightgauge-config-show` command to see the merged configuration
with source annotations:

```bash
/nightgauge-config-show

# Output shows where each value came from:
# pr.merge_strategy: squash    [global]
# project.number: 10           [project]
# branch.base: main            [default]
```

### Overriding Global Settings

Project config always wins over global config. To override a global setting for
a specific project, simply set the value in `.nightgauge/config.yaml`:

```yaml
# .nightgauge/config.yaml
pr:
  merge_strategy: rebase # Override global 'squash' setting
```

---

## Local Configuration (Issue #435)

The local configuration file (`.nightgauge/config.local.yaml`) provides
developer-specific overrides that are **not committed to git**. This tier sits
between project config and environment variables.

### When to Use Local Config

Use local config for settings that:

- Are specific to your development machine
- Should not be shared with the team
- Override team settings for local development
- Are temporary during debugging/development

**Common use cases:**

| Setting               | Why Local                                          |
| --------------------- | -------------------------------------------------- |
| `pipeline.skip.tests` | Skip tests locally while iterating quickly         |
| `pr.reviewers`        | Override default reviewers for your local branches |
| `pipeline.auto_fix`   | Disable auto-fix while debugging linting           |

### Creating a Local Config

```bash
# Create local config (automatically gitignored)
cat > .nightgauge/config.local.yaml << 'EOF'
# Local developer overrides - NOT committed to git
# This file is listed in .gitignore

pipeline:
  skip:
    tests: true  # Skip tests while debugging

EOF
```

### Gitignore Entry

Ensure `.nightgauge/config.local.yaml` is gitignored. Nightgauge's
`smart-setup` skill automatically adds this, but you can add it manually:

```bash
echo ".nightgauge/config.local.yaml" >> .gitignore
```

### Viewing Local Overrides

Use `/nightgauge-config-show` to see which values come from local config:

```bash
/nightgauge-config-show

# Output shows source annotations:
# pipeline.skip.tests: true    [local]
# pr.delete_branch: false     [local]
# pr.merge_strategy: squash    [global]
# project.number: 10           [project]
```

### Precedence with Local Config

Local config (tier 4) overrides both project (tier 3) and global (tier 2), but
is overridden by environment variables (tier 5) and CLI flags (tier 6):

```
Global: pr.delete_branch = true
Project: pr.delete_branch = true
Local: pr.delete_branch = false  ← WINS (highest file-based tier)
Env: NIGHTGAUGE_PR_DELETE_BRANCH=?  ← Would override if set
```

---

## Quick Start

Create a `.nightgauge/config.yaml` file in your repository:

```yaml
# .nightgauge/config.yaml - Minimal configuration
project:
  number: 10 # Your GitHub Project number

pr:
  merge_strategy: squash
```

## Recommended Config Profiles (Sonnet 4.6 Era)

The shipped defaults are tuned for a **balanced cost/quality** tradeoff. If you
want to optimize for a specific dimension, copy one of these profiles into your
`.nightgauge/config.yaml`.

### Sonnet 4.6 Era Model Tiers

| Model  | Best For                         | Relative Cost  | Effort Pairing |
| ------ | -------------------------------- | -------------- | -------------- |
| Haiku  | Structured extraction, templates | Lowest (~1x)   | low or omit    |
| Sonnet | Reasoning, code generation       | Medium (~3x)   | medium         |
| Opus   | Complex multi-file refactors     | Highest (~15x) | high           |

### Balanced (Shipped Default)

The built-in defaults use Haiku for lightweight stages, Sonnet for reasoning
stages, and medium/low effort. **No config file needed** — this is what you get
out of the box.

```yaml
# Shipped defaults — shown for reference, no config.yaml needed
pipeline:
  stage_models:
    issue-pickup: haiku
    feature-planning: sonnet
    feature-dev: sonnet
    feature-validate: sonnet
    pr-create: haiku
    pr-merge: haiku

model_routing:
  stage_efforts:
    feature-planning: medium
    feature-dev: medium
    feature-validate: low
```

### Cost-Optimized

Minimizes token spend. Uses Haiku everywhere except feature-dev, and low effort
across the board. Best for small (XS/S) issues or budget-constrained pipelines.

```yaml
# Cost-optimized — ~40% cheaper than balanced
pipeline:
  stage_models:
    issue-pickup: haiku
    feature-planning: haiku
    feature-dev: sonnet
    feature-validate: haiku
    pr-create: haiku
    pr-merge: haiku

model_routing:
  stage_efforts:
    feature-planning: low
    feature-dev: low
    feature-validate: low
```

### Quality-Optimized

Maximizes output quality for complex (L/XL) issues. Uses Sonnet as the baseline
and Opus for feature-dev, with high effort where it matters.

```yaml
# Quality-optimized — ~3x more expensive than balanced
pipeline:
  stage_models:
    issue-pickup: haiku
    feature-planning: sonnet
    feature-dev: opus
    feature-validate: sonnet
    pr-create: haiku
    pr-merge: haiku

model_routing:
  stage_efforts:
    feature-planning: medium
    feature-dev: high
    feature-validate: medium
```

## Configuration Sections

### github_user

Per-repository GitHub user identity for multi-account workspaces. When set,
token resolution uses `gh auth token --user <github_user>` instead of the
globally active `gh` account. This ensures all `gh` commands, Go binary API
calls, and skill subprocesses use the correct identity for each repository.

| Option        | Type   | Default | Description                         |
| ------------- | ------ | ------- | ----------------------------------- |
| `github_user` | string | -       | gh CLI username for this repository |

**Per-repo example** (`.nightgauge/config.yaml`):

```yaml
github_user: acmebot

project:
  owner: Acme-Community
  number: 1
```

#### Concurrent multi-user workspaces (per-repo `gh` identity)

When several workspaces — each owned by a **different** GitHub user — are open at
once on the same machine, `gh`'s globally active account (`gh auth status`) is a
single value and will be wrong for all but one of them. Set `github_user` per
repo so every `gh` call, Go-binary API call, and skill subprocess authenticates
as that repo's user instead.

Put it in the **gitignored local tier** so it is per-repo, never committed, and
requires no PAT on disk — the token is resolved from your `gh` keyring via
`gh auth token --user <github_user>`:

```yaml
# .nightgauge/config.local.yaml  (gitignored — see ensureGitignore.ts)
github_user: octocat
```

Prerequisite: run `gh auth login` once for each user; both stay in the keyring.

How it reaches every `gh` call — the per-repo token is injected at **every**
process root, so any `gh` a window spawns inherits it and there is no
active-account fallback path left open:

| Surface                     | Mechanism                                                                                                                                                                                                                           |
| --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Go binary (all subcommands) | The root `PersistentPreRunE` exports `GH_TOKEN`/`GITHUB_TOKEN` (github_user-scoped) so every `gh` the binary spawns — gates, reconcile, deterministic pr-merge, board sync — inherits it, regardless of how the binary was launched |
| Go binary API calls         | `NewClientFromConfig` resolves the token via the same chain (`gh auth token --user <github_user>`), never the active account                                                                                                        |
| Pipeline subprocesses       | `skillRunner` injects `GH_TOKEN`/`GITHUB_TOKEN` into the spawned stage env                                                                                                                                                          |
| VSCode integrated terminal  | The extension injects `GH_TOKEN`/`GITHUB_TOKEN` into the terminal `EnvironmentVariableCollection`, per workspace                                                                                                                    |
| VSCode extension host       | The extension also sets its own `process.env` `GH_TOKEN`/`GITHUB_TOKEN`, so direct `gh`/`gh api` calls from extension code (board writes, dashboard, commands) inherit it                                                           |
| Skill & hook shells         | `skills/_shared/PREFLIGHT.md` and `hooks/lib/guard.sh` export `GH_TOKEN` via `forge auth token` (ambient env stripped) and **override** a shadowing ambient token when the resolved per-repo identity differs                       |

**Authority rule (#4068): a configured per-repo identity is authoritative over
the ambient env.** When a repo declares a `github_user` (or `github_auth.users`
maps its owner), the github*user-scoped token wins over an ambient
`GH_TOKEN`/`GITHUB_TOKEN` — even one injected by the runner. The Go binary
re-resolves and **overrides** the ambient token (`maybeExportGitHubToken`), and
`gh auth token --user` runs with the ambient `GH_TOKEN`/`GITHUB_TOKEN` **stripped
from its child env** so `gh` reads the keyring entry for that user rather than
the shadowing ambient token (gh's order is `GH_TOKEN` > `GITHUB_TOKEN` >
keyring). This closes the failure mode where an ambient (wrong-user) token, or a
keyring whose \_active* account is the wrong user, silently shadowed the
configured identity.

Repos with **no** configured identity (no `github_user`, no
`github_auth.token`/`tokens`) keep the previous behavior: an upstream
`GH_TOKEN` wins and the machine-global active account is the final fallback —
single-identity and CI flows are unaffected.

> **Caveats for a configured `github_user`:**
>
> - The token must be available **for that user** — either in `github_auth.token`
>   / `github_auth.tokens[owner]` or in the gh keyring (`gh auth login --user
<github_user>`). An env-only `GITHUB_TOKEN` is **intentionally ignored** for a
>   configured identity (the ambient token may be the wrong user), so a setup that
>   declares `github_user` but supplies the token only via `GITHUB_TOKEN` env will
>   fail the preflight rather than silently act as the wrong user.
> - Do **not** also point `github_auth.token`/`tokens[owner]` at
>   `env:GITHUB_TOKEN` when a `github_user` is configured: a config token is
>   resolved before the identity-scoped token, so an `env:`-backed config token
>   would shadow the declared identity. Use `github_auth.token: env:GITHUB_TOKEN`
>   **only** for single-identity repos that set no `github_user`.

Verify the active identity for a repo:

```bash
nightgauge forge auth whoami     # login resolved for this repo
nightgauge forge auth status     # scopes + resolution source
nightgauge forge auth assert --repo <owner>/<repo>   # preflight permission check (see below)
```

> Both `github_user` and `github_auth.token` are honored from
> `config.local.yaml` (tier 4, highest precedence). Prefer `github_user` for a
> secret-free per-repo identity; use `github_auth.token` only when a specific PAT
> is required (e.g. a fine-grained token with narrower scopes).

### github_auth

GitHub authentication and token configuration. Supports single-org per-project
tokens, multi-org per-owner token maps, and legacy gh CLI fallback for
backwards compatibility.

| Option                            | Type          | Default | Description                                      |
| --------------------------------- | ------------- | ------- | ------------------------------------------------ |
| `github_auth.token`               | string        | -       | Per-project GitHub PAT (supports `env:VAR_NAME`) |
| `github_auth.tokens`              | map\<string\> | -       | Per-org PAT map for multi-org workspaces         |
| `github_auth.users`               | map\<string\> | -       | Maps org/owner name → gh CLI username (legacy)   |
| `github_auth.suppress_gh_warning` | boolean       | `false` | Suppress deprecation warning on gh CLI fallback  |

#### Token Resolution Priority

The pipeline resolves a GitHub token using this chain (highest to lowest). The
chain **branches** on whether a `github_user` is configured for the target
owner (#4068):

1. `--token` CLI flag (one-shot override)
2. `github_auth.token` — per-project PAT from config
3. `github_auth.tokens[owner]` — per-org PAT map (global config)
4. **If a `github_user` is configured for the owner** (explicit `github_user`,
   or `github_auth.users[owner]`):
   `gh auth token --user <github_user>` — **with ambient `GH_TOKEN`/`GITHUB_TOKEN`
   stripped from the child env**. This step is **authoritative over the ambient
   `GITHUB_TOKEN` env var** so the configured per-repo identity always wins.
5. **If no `github_user` is configured:** `GITHUB_TOKEN` environment variable,
   then `gh auth token` (default gh account) — the single-identity / CI path,
   unchanged.

The key change from earlier versions: when a repo declares a specific identity,
the github_user-scoped token (step 4) is tried **before** the ambient
`GITHUB_TOKEN` env var, and the env is stripped so `gh` cannot return the
shadowing ambient token. Previously `GITHUB_TOKEN` env sat above the gh CLI
fallback for all repos, which let an ambient (wrong-user) token shadow a repo
that configured only `github_user`.

When the gh CLI fallback is used, a warning is printed to stderr:

```
warning: Using gh CLI for token resolution — configure github_auth.token
in config.yaml for reliable multi-org support
```

This warning is intentionally non-blocking. The pipeline continues, but CI/CD
environments without `gh` installed will fail at this step — in CI, set
`github_auth.token: env:GITHUB_TOKEN` (or rely on the ambient `GITHUB_TOKEN`)
and do **not** configure a `github_user`.

#### `forge auth assert` — deterministic preflight permission check

`nightgauge forge auth assert --repo <owner>/<repo>` resolves the per-repo
identity, confirms the **effective** login (`Whoami`, after the env-stripped
token resolution above) equals the configured `github_user`, and confirms that
identity has **push** access on the target repo. With `--admin` it additionally
requires admin (needed to bypass a required-review ruleset / branch protection).

It exits **0** when the identity matches and has the required access, and
**non-zero with a one-line remediation** otherwise — so a misconfigured identity
fails loudly here rather than silently at a later merge/push. Use `--json` for
skill consumption.

```bash
# Pass: resolved identity is acmebot and has push on the repo
nightgauge forge auth assert --repo Acme-Community/acme-tracker

# Fail (exit 1): prints the blocker + remediation, e.g.
#   error: identity assertion failed: resolved identity is "octocat" but config expects "acmebot" ...
#   remediation: run: GH_TOKEN=$(env -u GH_TOKEN -u GITHUB_TOKEN gh auth token --user acmebot) gh ...
```

The scheduler runs the same assertion automatically before dispatching any stage
for a target repo (see [GIT_WORKFLOW.md](GIT_WORKFLOW.md#identity-preflight)), so
a read-only or wrong-user identity is rejected at preflight instead of producing
an un-mergeable PR.

#### Local/dev convenience — per-repo `direnv` `.envrc` (no secret at rest)

For interactive work in a repo with a per-repo identity, a gitignored
`direnv` `.envrc` exports the configured user's token on `cd`, fetched from the
`gh` keyring with the ambient env stripped — no PAT stored on disk:

```bash
# .envrc  (gitignored — the repo may be PUBLIC; never commit this)
# Force the per-repo identity for every gh/HTTPS call in this shell.
export GH_TOKEN="$(env -u GH_TOKEN -u GITHUB_TOKEN gh auth token --user acmebot)"
export GITHUB_TOKEN="$GH_TOKEN"
```

Prerequisite: `gh auth login` once for that user (it stays in the keyring), and
`direnv allow` the `.envrc`. Equivalent to what the Go binary and `guard.sh` do
automatically — this just makes a bare terminal in the repo correct by default.
Add `.envrc` to `.gitignore`. (Git `push` already uses the SSH host alias for
these repos; this closes the `gh` API / HTTPS gap.)

#### Single-Org Setup (Recommended)

For projects using one organization, set a single PAT:

```yaml
# .nightgauge/config.yaml (committed — use env: to avoid plaintext tokens)
github_auth:
  token: env:GITHUB_TOKEN_NIGHTGAUGE
```

The `env:` prefix resolves the token from an environment variable, keeping
plaintext secrets out of version control.

**In CI/CD** (e.g., GitHub Actions):

```yaml
# .github/workflows/pipeline.yml
env:
  GITHUB_TOKEN_NIGHTGAUGE: ${{ secrets.GITHUB_TOKEN_NIGHTGAUGE }}
```

**In local development** (`.nightgauge/config.local.yaml`, gitignored):

```yaml
github_auth:
  token: ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

#### Multi-Org Setup

For workspaces spanning multiple orgs or GitHub accounts, map each owner to its
own token. Set this in your global config so it applies to all repos:

```yaml
# ~/.nightgauge/config.yaml (global — never committed)
github_auth:
  tokens:
    nightgauge: env:GITHUB_TOKEN_NIGHTGAUGE
    OtherOrg: env:GITHUB_TOKEN_OTHER
```

The pipeline resolves the token for each repo by looking up its owner in this
map. Token scope requirements: `repo`, `project`, `read:org`.

#### Suppress gh CLI Warning

If you intentionally use the gh CLI fallback (e.g., during local development
while migrating to config-based tokens), suppress the warning:

```yaml
github_auth:
  suppress_gh_warning: true
```

> **Note**: Suppressing the warning does not fix the underlying issue. CI/CD
> environments without `gh` installed will still fail. Migrate to
> `github_auth.token` or `github_auth.tokens[owner]` for production use.

#### Legacy: gh CLI User Mapping

The `github_auth.users` map was the original multi-identity mechanism. It maps
org/owner names to `gh` CLI usernames for `gh auth token --user <user>` calls:

```yaml
github_auth:
  users:
    nightgauge: octocat
    Acme-Community: acmebot
```

This approach requires `gh` CLI >= 2.40 and works only in environments with
`gh` installed. Prefer `github_auth.tokens[owner]` for new setups.

**Prerequisites for gh CLI fallback:**

- `gh` CLI >= 2.40 (supports `--user` flag on `gh auth token`)
- Users must run `gh auth login` for each identity before configuring mappings

**Used by:**

- Go binary (`cmd/nightgauge/main.go`) — all GitHub API calls via
  `clientFromConfig()`
- VSCode extension (`IpcClientBase.ts`) — token resolution at startup
- Skill subprocesses (`skillRunner.ts`) — `GH_TOKEN` env var injection

**See also:** [CI/CD Runbook](./CI_CD_RUNBOOK.md) for CI/CD token setup patterns.

### project

GitHub Project board integration settings.

| Option       | Type    | Default | Description                                |
| ------------ | ------- | ------- | ------------------------------------------ |
| `number`     | number  | -       | GitHub Project number (from project URL)   |
| `owner`      | string  | -       | Project owner (defaults to repo owner)     |
| `auto_dates` | boolean | `false` | Auto-populate Start/Target date fields     |
| `sprint`     | object  | -       | Sprint/iteration configuration (see below) |

**Example:**

```yaml
project:
  number: 10
  owner: nightgauge
  auto_dates: true
```

**Environment overrides:**

```bash
export NIGHTGAUGE_PROJECT_NUMBER=10
export NIGHTGAUGE_PROJECT_OWNER=nightgauge
export NIGHTGAUGE_PROJECT_AUTO_DATES=true
```

**Used by:**

- `/nightgauge-issue-pickup` - Adds issues to project, sets Start date
- `/nightgauge-pr-create` - Updates Status field to "In review", warns if
  Target date missing
- `/nightgauge-pr-merge` - Status set to "Done" by built-in workflow on
  close
- Pipeline stages - Status field updates via `sync-project-status.sh`

**Note:** GitHub Project board fields (Status, Priority, Size) are the source of
truth for project management. The pipeline writes directly to project fields via
GraphQL — no `status:*` labels are used. Priority and Size are set directly as
board fields at issue creation (by `create-sub-issue.sh` or the issue-create
workflow) — `add-to-project.sh` adds issues to the board but does not map labels
to Priority/Size fields. Labels are for classification (`type:*`,
`component:*`).

#### project.sprint

Sprint/iteration field integration settings. See
[SPRINT_WORKFLOW.md](./SPRINT_WORKFLOW.md) for complete setup instructions.

| Option           | Type    | Default    | Description                                   |
| ---------------- | ------- | ---------- | --------------------------------------------- |
| `enabled`        | boolean | `false`    | Enable iteration field integration            |
| `auto_assign`    | boolean | `false`    | Auto-assign @current iteration on pickup      |
| `field_name`     | string  | `"Sprint"` | Name of the iteration field in GitHub Project |
| `current`        | string  | -          | Explicit current sprint/iteration name        |
| `duration_weeks` | integer | -          | Sprint duration in weeks (minimum 1)          |

**Example:**

```yaml
project:
  number: 10
  sprint:
    enabled: true
    auto_assign: true
    field_name: "Sprint"
    current: "Sprint 12"
    duration_weeks: 2
```

**Environment overrides:**

```bash
export NIGHTGAUGE_PROJECT_SPRINT_ENABLED=true
export NIGHTGAUGE_PROJECT_SPRINT_AUTO_ASSIGN=true
export NIGHTGAUGE_PROJECT_SPRINT_FIELD_NAME="Iteration"
```

**Used by:**

- `/nightgauge-issue-pickup` - Assigns current iteration when picking up an
  issue

#### project.custom_fields

Custom field configuration for syncing additional GitHub Project fields beyond
Status, Priority, and Size. See [CUSTOM_FIELDS.md](./CUSTOM_FIELDS.md) for
complete documentation.

| Option         | Type   | Required          | Description                                             |
| -------------- | ------ | ----------------- | ------------------------------------------------------- |
| `name`         | string | Yes               | GitHub Project field name (exact match)                 |
| `field_id`     | string | Yes               | GraphQL field ID (PVTSSF*... or PVTF*...)               |
| `label_prefix` | string | Yes               | Label prefix (e.g., "component" for component:frontend) |
| `type`         | string | Yes               | Field type: single_select, text, or number              |
| `mappings`     | object | For single_select | Label suffix to option value mapping                    |

**Field Types:**

| Type            | Description                   | Label Format    | Example              |
| --------------- | ----------------------------- | --------------- | -------------------- |
| `single_select` | Single selection from options | `prefix:suffix` | `component:frontend` |
| `text`          | Free-form text                | `prefix:value`  | `customer:acme`      |
| `number`        | Numeric value                 | `prefix:N`      | `effort:5`           |

**Example:**

```yaml
project:
  number: 10
  custom_fields:
    # Single select field with explicit mappings
    - name: "Component"
      field_id: "PVTSSF_abc123"
      label_prefix: "component"
      type: "single_select"
      mappings:
        frontend: "Frontend"
        backend: "Backend"
        infra: "Infrastructure"

    # Text field (no mappings needed)
    - name: "Customer"
      field_id: "PVTF_def456"
      label_prefix: "customer"
      type: "text"

    # Number field
    - name: "Story Points"
      field_id: "PVTF_ghi789"
      label_prefix: "points"
      type: "number"
```

**Discovery:**

Use the init script to discover custom field IDs:

```bash
scripts/init-nightgauge-config.sh --project 10 --custom-field "Component"
```

**Label Prefix Rules:**

- Must be unique across all custom fields
- Cannot use reserved prefixes: `priority`, `size`
- Recommend lowercase with hyphens (e.g., `story-points`)

**Used by:**

- `add-to-project.sh` - Syncs custom field labels to project fields
- `init-nightgauge-config.sh` - Discovers custom field IDs and options

---

### projects (Multi-Project Mode)

For repositories that need to sync issues to multiple GitHub Projects (e.g.,
different teams viewing the same issues on different boards), use the `projects`
array instead of or alongside the single `project` configuration.

| Option              | Type    | Required | Description                                    |
| ------------------- | ------- | -------- | ---------------------------------------------- |
| `name`              | string  | Yes      | Display name for the project                   |
| `number`            | number  | Yes      | GitHub Project number (from project URL)       |
| `id`                | string  | No       | Cached GitHub Project node ID                  |
| `status_field_id`   | string  | No       | Cached Status field ID                         |
| `priority_field_id` | string  | No       | Cached Priority field ID                       |
| `size_field_id`     | string  | No       | Cached Size field ID                           |
| `sync_filter`       | string  | No       | Boolean expression to filter which issues sync |
| `default`           | boolean | No       | Mark as default project for reverse sync       |

**Example:**

```yaml
# Multi-project configuration (Issue #135)
projects:
  # Engineering team board - all feature work
  - name: "Engineering Board"
    number: 10
    sync_filter: "type:feature OR type:bug"
    default: true

  # QA team board - bugs and testing
  - name: "QA Board"
    number: 15
    sync_filter: "type:bug OR needs-qa"

  # Leadership board - high priority items only
  - name: "Leadership Review"
    number: 20
    sync_filter: "priority:critical OR priority:high"
```

**Backward Compatibility:**

The single `project:` configuration continues to work. When both `project:` and
`projects:` are defined, `projects:` takes precedence. Internally, a single
`project:` is converted to a one-element `projects:` array.

```yaml
# These are equivalent:
project:
  number: 10

# ...and...
projects:
  - name: "Default"
    number: 10
    default: true
```

#### sync_filter Syntax

The `sync_filter` field uses a boolean expression syntax to determine which
issues sync to each project:

**Operators:**

| Operator | Description | Example                                        |
| -------- | ----------- | ---------------------------------------------- |
| `OR`     | Logical OR  | `type:feature OR type:bug`                     |
| `AND`    | Logical AND | `priority:high AND needs-review`               |
| `NOT`    | Negation    | `NOT status:done`                              |
| `()`     | Grouping    | `(type:feature OR type:bug) AND priority:high` |

**Label Matching:**

- Labels are matched exactly: `type:feature` matches the label `type:feature`
- Case-sensitive matching
- Partial matches are not supported

**Precedence:**

1. `NOT` (highest)
2. `AND`
3. `OR` (lowest)

Use parentheses to override default precedence.

**Examples:**

```yaml
# All features and bugs
sync_filter: "type:feature OR type:bug"

# High priority features only
sync_filter: "type:feature AND priority:high"

# Exclude done items
sync_filter: "NOT status:done"

# Complex: Features OR high-priority bugs
sync_filter: "type:feature OR (type:bug AND priority:high)"

# Everything except epics
sync_filter: "NOT type:epic"
```

**No filter (sync all):**

If `sync_filter` is omitted or empty, all issues are synced to that project.

#### Default Project

When multiple projects are configured, the `default: true` project is used as
the primary board for status reads and dashboard display.

**Rules:**

1. Only one project can have `default: true`
2. If no project is marked as default, the first project in the array is used
3. The default project is the source of truth for the VSCode extension dashboard
4. All configured projects receive issue additions from `add-to-project.sh`

**Example:**

```yaml
projects:
  - name: "Engineering"
    number: 10
    default: true # Primary board for dashboard and status reads

  - name: "Leadership"
    number: 20
    # Secondary board — issues added but not used for status reads
```

**Used by:**

- `add-to-project.sh` - Adds issues to all matching projects
- `sync-project-status.sh` - Updates Status field across all projects
- `ProjectBoardService.ts` - Dashboard project selector and aggregation

**Dashboard Integration:**

When multi-project mode is active, the dashboard displays:

- Project selector dropdown to switch between projects
- "Aggregate" option to view combined counts across all projects
- Individual project views with project-specific issue lists

---

### pr

Pull request creation and merge settings.

| Option                  | Type     | Default  | Description                                                     |
| ----------------------- | -------- | -------- | --------------------------------------------------------------- |
| `merge_strategy`        | string   | `squash` | Merge strategy for sub-issue/feature PRs: squash, merge, rebase |
| `epic_merge_strategy`   | string   | `merge`  | Merge strategy for epic→main PRs: merge, squash, rebase         |
| `delete_branch`         | boolean  | `true`   | Delete feature branch after merge                               |
| `reviewers`             | string[] | `[]`     | Auto-request these reviewers                                    |
| `auto_merge`            | boolean  | `false`  | Enable GitHub auto-merge                                        |
| `auto_merge_epic`       | boolean  | `true`   | Auto-merge epic→main PR when all sub-issues complete            |
| `auto_fix_ci`           | boolean  | `true`   | Auto-fix CI failures before merge                               |
| `auto_fix_max_attempts` | number   | `2`      | Maximum auto-fix retry attempts (lowered from 3 in #3108)       |
| `ci_check_timeout`      | number   | `600`    | Timeout for CI checks in seconds                                |

**Two-tier merge strategy**: `merge_strategy` controls how sub-issue PRs are
merged (into epic branches or main). `epic_merge_strategy` controls how epic
branches are merged into main. The default pairing (`squash` + `merge`) gives
one clean commit per sub-issue on the epic branch, and preserves all sub-issue
commits when merging the epic into main.

#### CI Check Gate (Issue #426)

The pr-merge stage always waits for CI checks to complete before merging.
There is no admin bypass — nothing skips branch protection or the local CI
check verification (#186).

**Auto-Fix Retry Loop:**

When CI checks fail, pr-merge can automatically attempt to fix the failures:

1. Fetches failure logs via `gh run view`
2. Classifies the failure type (lint, test, build, type error)
3. Generates a fix using AI
4. Commits and pushes the fix
5. Waits for CI to re-run
6. Repeats until success or `auto_fix_max_attempts` reached

**When auto-fix is disabled or max attempts reached:**

- In interactive mode: prompts user with options
  **Edge Cases:**

- **No CI checks configured**: Merge proceeds normally (no false block)
- **Flaky tests**: Auto-fix detects repeat failures and escalates
- **Multiple failing checks**: Addresses each in sequence
- **Check timeout during retry**: Respects `ci_check_timeout` per attempt

**Example:**

```yaml
pr:
  merge_strategy: squash
  delete_branch: true
  reviewers:
    - alice
    - bob
  auto_merge: false
  auto_merge_epic: true # auto-merge epic→main PR when all sub-issues complete
  # CI check gate settings (Issue #426)
  auto_fix_ci: true
  auto_fix_max_attempts: 2
  ci_check_timeout: 600
```

**Environment overrides:**

```bash
export NIGHTGAUGE_PR_ADMIN_MERGE=true
export NIGHTGAUGE_PR_MERGE_STRATEGY=rebase
export NIGHTGAUGE_PR_DELETE_BRANCH=false
export NIGHTGAUGE_PR_REVIEWERS=alice,bob
export NIGHTGAUGE_PR_AUTO_MERGE=true
export NIGHTGAUGE_PR_AUTO_MERGE_EPIC=true
export NIGHTGAUGE_PR_AUTO_FIX_CI=true
export NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS=2
export NIGHTGAUGE_PR_CI_CHECK_TIMEOUT=600
```

**Used by:**

- `/nightgauge-pr-create` - Requests reviewers and enables auto-merge
- `/nightgauge-pr-merge` - Uses admin bypass, merge strategy, branch
  deletion, CI check gate

---

### branch

Branch naming and protection settings.

| Option      | Type     | Default  | Description                       |
| ----------- | -------- | -------- | --------------------------------- |
| `base`      | string   | `main`   | Default base branch for PRs       |
| `protected` | string[] | `[main]` | Branches that cannot be pushed to |

**Example:**

```yaml
branch:
  base: develop
  protected:
    - main
    - develop
```

**Environment overrides:**

```bash
export NIGHTGAUGE_BRANCH_BASE=develop
export NIGHTGAUGE_BRANCH_PROTECTED=main,develop
```

**Used by:**

- `/nightgauge-issue-pickup` - Creates branches from the issue type labels
- `/nightgauge-pr-create` - Targets configured base branch

---

### issue

Issue creation and assignment settings.

| Option           | Type     | Default     | Description                                            |
| ---------------- | -------- | ----------- | ------------------------------------------------------ |
| `default_status` | string   | `"backlog"` | Default project board status: `"backlog"` or `"ready"` |

**Example:**

```yaml
issue:
  default_status: backlog
```

**Environment overrides:**

```bash
export NIGHTGAUGE_ISSUE_DEFAULT_STATUS=backlog
export NIGHTGAUGE_ISSUE_DEFAULT_LABELS=needs-triage
```

**Used by:**

- `/nightgauge-issue-create` - Applies default labels, default status,
  auto-assigns. Supports `--ready` and `--backlog` per-invocation overrides.

---

### pipeline

Pipeline execution settings.

| Option                    | Type    | Default      | Description                                                                    |
| ------------------------- | ------- | ------------ | ------------------------------------------------------------------------------ |
| `ci_timeout`              | number  | `300`        | Timeout for CI checks in seconds                                               |
| `auto_fix`                | boolean | `true`       | Auto-fix linting issues in feature-dev                                         |
| `skip`                    | object  | -            | Skip specific validation checks                                                |
| `max_turns`               | integer | _(no limit)_ | Max turns per headless CLI invocation (Issue #626)                             |
| `auto_create_epic_branch` | boolean | `true`       | Auto-create epic branch from default branch when first sub-issue is dispatched |
| `failure_mode`            | enum    | `halt`       | Behavior on terminal pipeline failure (Issue #3001)                            |
| `adaptive_stall_recovery` | boolean | `false`      | Rewind to feature-planning once on first stall-kill (Issue #3005)              |
| `performance_mode`        | object  | -            | Default performance mode + per-mode overrides (Issue #3009)                    |

**Skip object:**

| Key         | Default | Description            |
| ----------- | ------- | ---------------------- |
| `tests`     | `false` | Skip test execution    |
| `lint`      | `false` | Skip linting           |
| `typecheck` | `false` | Skip type checking     |
| `format`    | `false` | Skip format validation |

**Example:**

```yaml
pipeline:
  ci_timeout: 600
  auto_fix: true
  skip:
    tests: false
    lint: false
    typecheck: false
    format: false
```

**auto_create_epic_branch details:**

When `true` (the default), the Go scheduler creates `epic/{N}-{slug}` from the
repository's default branch immediately after `issue-pickup` completes for any
sub-issue. This ensures `enforceEpicBaseBranch()` in TypeScript finds the branch
before `feature-planning` runs.

Set to `false` to disable auto-creation. Sub-issue PRs will target `main` unless
the epic branch was created manually.

**Known limitation**: The pipeline does NOT auto-rebase epic branches when `main`
advances after the epic branch was created. Users are responsible for manually
rebasing long-lived epic branches.

#### `pipeline.performance_mode` (Issue #3009)

Selects the named cost/quality envelope applied to every pipeline stage. See
[PERFORMANCE_MODES.md](PERFORMANCE_MODES.md) for the full per-stage matrix.

```yaml
pipeline:
  performance_mode:
    default: elevated # efficiency | elevated | maximum | frontier
    overrides:
      maximum:
        model: opus # heavy-tier model (default: opus)
        codex_model: gpt-5.5 # optional Codex override
        stall_kill_multiplier: 10
        disable_budget_ceiling: true
```

`frontier` is the premium opt-in tier: it routes the reasoning stages to
**Fable 5** (`claude-fable-5`, ~2× Opus) and keeps mechanical stages on Haiku.
Automatic routing never selects Fable — `frontier` (or an explicit
`model_routing.minimum_model: fable` / per-run override) is the only way in.
Unlike `maximum`, `frontier` leaves the budget ceiling enabled. See
[PERFORMANCE_MODES.md § When is Fable used over Opus?](PERFORMANCE_MODES.md#when-is-fable-used-over-opus).

The active mode is normally driven by the status-bar QuickPick (writes
`.nightgauge/performance-mode.yaml`); `pipeline.performance_mode.default`
applies only when no state file is present. Override per-shell with
`NIGHTGAUGE_PERFORMANCE_MODE=<mode>`.

The legacy `pipeline.supercharge` block is still parsed for one release as a
synonym for `pipeline.performance_mode.overrides.maximum`. See
[DEPRECATIONS.md](DEPRECATIONS.md#supercharge-toggle--performance_mode-selector).

#### `pipeline.recovery.conflict_recovery` (#4072)

Controls the `conflict-recovery-loop` recovery action — see
[AUTO_TRIAGE.md §Conflict-recovery-loop](AUTO_TRIAGE.md#conflict-recovery-loop-4072).
On an unresolvable rebase conflict, pr-merge captures the conflict context and
the loop **re-dispatches feature-dev on the same branch** (via the feedback
rewind) to resolve it, instead of the old blind fresh-branch restart that
discarded all dev work. After the bound is exhausted — or if no conflict context
could be captured — the action escalates with the specific conflicting files.

| Key                                                      | Type    | Default | Description                                                                                                                  |
| -------------------------------------------------------- | ------- | ------- | ---------------------------------------------------------------------------------------------------------------------------- |
| `pipeline.recovery.conflict_recovery.enabled`            | boolean | `true`  | Gate the conflict-recovery loop.                                                                                             |
| `pipeline.recovery.conflict_recovery.max_dev_redispatch` | integer | `2`     | Max feature-dev re-dispatches before escalating with the specific files. Env override: `NIGHTGAUGE_CONFLICT_MAX_REDISPATCH`. |

```yaml
pipeline:
  recovery:
    max_attempts_per_run: 3 # total recovery attempts per run (env: NIGHTGAUGE_RECOVERY_MAX_ATTEMPTS)
    conflict_recovery:
      enabled: true
      max_dev_redispatch: 2 # env: NIGHTGAUGE_CONFLICT_MAX_REDISPATCH
```

> **Independent bounds.** Conflict re-dispatches are bounded **only** by
> `max_dev_redispatch` and do not draw from `max_attempts_per_run`. The
> conflict-recovery loop is cap-exempt, so an unrelated recovery (e.g. a
> transient-CI retry) earlier in the same run cannot pre-empt the configured
> conflict bound.

**Why 2 (not 3).** A dev re-dispatch is more expensive than a fresh-branch
restart, so the bound is lower than the legacy `MaxConflictRestarts` (3). Two
attempts at branch-preserving resolution before a file-named escalation is the
single resolved value (no parallel knob). The bound is enforced by two
cooperating layers, both sized by `max_dev_redispatch`: the **in-memory per-edge
count** (`RetryEngine.conflictEdges`) is the authoritative termination bound
(reliable on every path, cleared per run by `RetryEngine.Reset`), while the
**on-disk `CONFLICT_RESOLUTION_NEEDED` signal count** in `feedback-{N}.json` is
the primary escalation trigger on the normal path. Whichever trips first stops
the loop at exactly `max_dev_redispatch` re-dispatches — see
[AUTO_TRIAGE.md](AUTO_TRIAGE.md) and `conflict_recovery_loop.go` for the full
rationale.

**Determinism.** The recovery action is deterministic-only — it emits the
feedback signal and rewinds; the probabilistic conflict resolution happens in the
rewound feature-dev stage, not the action.

#### `pipeline.heal` (Issue #3683)

Controls the `pipeline-heal-base` recovery action — see
[AUTO_TRIAGE.md §Pipeline-heal-base](AUTO_TRIAGE.md#pipeline-heal-base-3683)
for the action overview. The action fires when the pr-merge auto-fix loop's
Step 2.5 has labelled the PR `pipeline-failed-inherited` (every failure also
fails on the merge-base, so main is broken). It opens a fix-PR against the
affected base branch instead of looping LLM spend on a PR that cannot fix the
root cause.

| Key                                 | Type    | Default | Description                                                                         |
| ----------------------------------- | ------- | ------- | ----------------------------------------------------------------------------------- |
| `pipeline.heal.max_active_per_repo` | integer | `1`     | Max open `pipeline-heal:auto` PRs before the action declines.                       |
| `pipeline.heal.max_24h_per_repo`    | integer | `3`     | Max heal PRs created in any rolling 24h window (counts both open and closed PRs).   |
| `pipeline.heal.diff_budget_lines`   | integer | `30`    | Diff-line budget patterns target; downstream auto-merge gates use this to back off. |
| `pipeline.heal.require_human_first` | boolean | `true`  | First occurrence of every pattern slug is labelled `pipeline-heal:needs-review`.    |

```yaml
pipeline:
  heal:
    max_active_per_repo: 1
    max_24h_per_repo: 3
    diff_budget_lines: 30
    require_human_first: true
```

**Why the defaults are conservative.** Heal PRs touch the shared base branch
across every dependent PR. A misfire is more disruptive than letting the
auto-fix loop surface the failure to a human. `require_human_first: true`
means the first occurrence of any new pattern always lands in front of a
reviewer before subsequent fires are eligible for the auto-merge label.

**Determinism.** The action is deterministic-only. New patterns require a
human-reviewed PR adding code to `internal/heal/` — patterns cannot be
declared in YAML. See `internal/heal/registry.go` for the allowlist.

#### `pipeline.failure_mode` (Issue #3001)

Controls what the Go scheduler does when a pipeline run hits a terminal failure
(stall-kill, budget ceiling, validation error, subagent crash, orchestrator
crash). All three modes preserve the failed `RunRecord` to JSONL — the
difference is what happens to the rest of the queue.

| Value            | Behavior                                                                                                           |
| ---------------- | ------------------------------------------------------------------------------------------------------------------ |
| `halt` (default) | Mark every downstream queued item `paused`, stop dispatching. Operator must Retry / Skip / Discard the failed run. |
| `continue-queue` | Leave the failed run's item as `failed`, keep dispatching the rest of the queue.                                   |
| `auto-resume`    | Single capped re-dispatch of the same item, then fall back to halt. Subsequent items proceed.                      |

```yaml
pipeline:
  failure_mode: halt
```

Or via env override:

```bash
export NIGHTGAUGE_PIPELINE_FAILURE_MODE=continue-queue
```

**Why `halt` is the default.** Customer onboarding requires predictable,
debuggable behavior. Silent recovery hides root causes; aggressive auto-resume
can wedge into infinite retry loops on a shared dependency failure. `halt`
makes every failure visible — operators acknowledge it via the dashboard's
RunningNow widget.

**Cascading-failure caveat for `continue-queue`.** If the failure stems from a
shared dependency (a broken CI runner, a degraded model endpoint), every
subsequent run will fail too. `continue-queue` will rip through the queue
producing N failed records before anyone notices. Use it only when you've
confirmed failures are isolated to specific issues.

**`auto-resume` cap.** Tracked on the run record — one auto-resume per failed
run, then halt for that item even if `auto-resume` remains the global setting.
Prevents infinite-loop wedging on a flaky model.

**Paused items.** When `halt` triggers, queued items get
`status: "paused"` with `pausedReason = { kind: "upstream_failure", failed_run_id }`.
The dashboard's queue tree renders a paused-clock icon and the failed_run_id in
the tooltip so operators can correlate paused items with the failed JSONL
record. Resume happens via `Scheduler.ResumePausedItems(failedRunID)` (called
from the dashboard webview's Skip / Discard handlers).

**Crash recovery is independent of `failure_mode`.** A `current-run.json`
sidecar is written at every stage-start regardless of mode. On scheduler
startup, a stale sidecar always synthesizes a
`terminal_failure_kind: orchestrator_crash` record and pauses the queue —
otherwise an in-flight run that died mid-stage would leave the queue dispatching
into a partial state.

See [docs/HEALTH_MONITORING.md §Failure Preservation Contract](HEALTH_MONITORING.md#failure-preservation-contract-issue-3001)
for the full guarantees and [docs/FAILURE_TAXONOMY.md §Terminal Failure Kind](FAILURE_TAXONOMY.md#terminal-failure-kind-issue-3001)
for the per-kind classification heuristics.

#### `pipeline.adaptive_stall_recovery` (Issue #3005)

When `true`, the Go scheduler synthesizes a feedback signal on the first
stall-killed stage in a run and rewinds once to `feature-planning`. The retry
goes through the existing `RetryEngine`, so `max_backtracks` and oscillation
guards apply unchanged. The second stall-kill in the same run is terminal and
carries `failure_category: stall-killed-after-retry`.

```yaml
pipeline:
  adaptive_stall_recovery: true
```

Or via env override:

```bash
export NIGHTGAUGE_PIPELINE_ADAPTIVE_STALL_RECOVERY=true
```

Default: `false`. Opt-in for both new and existing repos until dogfooding
data justifies flipping the default. See
[docs/decisions/004-adaptive-stall-recovery.md](decisions/004-adaptive-stall-recovery.md).

**Cost-cap precedence (#3002).** A cost-cap kill is **never** retried, even
when its error text also matches the stall-kill heuristic. Operators who set
`pipeline.stage_cost_caps` retain the cap's full force.

**Branch placement.** The stall-recovery branch runs **before** model
escalation. Stall-kill is rarely a model-capacity issue — re-planning is the
more accurate response, and escalating model on every stall while _also_
re-planning would double-count spend.

**Environment overrides:**

```bash
export NIGHTGAUGE_PIPELINE_CI_TIMEOUT=600
export NIGHTGAUGE_PIPELINE_AUTO_FIX=false
export NIGHTGAUGE_PIPELINE_SKIP_TESTS=true
export NIGHTGAUGE_PIPELINE_MAX_TURNS=100
export NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH=false
```

**Used by:**

- `/nightgauge-pr-create` - Waits for CI with configured timeout
- `/nightgauge-pr-merge` - Waits for CI with configured timeout
- `/nightgauge-feature-dev` - Uses auto_fix setting
- `/nightgauge-feature-validate` - Respects skip settings

---

#### pipeline.stages.{stage}.mcp_tools

Per-stage MCP tool allowlist. Specifies which MCP tools (named
`mcp__{server}__{tool}`) are appended to `--allowedTools` when running that
pipeline stage. This is the config-level counterpart to the `mcp-tools`
frontmatter field in SKILL.md files.

**Precedence**: Config-level `mcp_tools` overrides the frontmatter `mcp-tools`
field — if both are set, the config value wins.

**Special value `all`**: When a SKILL.md specifies `mcp-tools: all`, the
extension reads `.claude/settings.json` in the workspace root to enumerate MCP
server names and generates wildcard patterns (`mcp__{server}__*`) for each.
Config-level `mcp_tools` does not support `all` — list patterns explicitly.

**Example:**

```yaml
pipeline:
  stages:
    feature-dev:
      mcp_tools:
        - mcp__playwright__*
        - mcp__sentry__get_issue
    feature-validate:
      mcp_tools:
        - mcp__playwright__browser_click
        - mcp__playwright__browser_snapshot
```

**Used by:**

- `packages/nightgauge-vscode/src/utils/incrediConfig.ts` —
  `getStageMcpTools()`
- `packages/nightgauge-vscode/src/utils/skillRunner.ts` —
  `runStageSkillHeadless()`, `runStageSkillInteractive()`

**See also:**

- SKILL.md frontmatter `mcp-tools` field — skill-level declaration (lower
  precedence than config)
- Issue #1725 — core MCP passthrough mechanism

---

#### pipeline.stall_thresholds

Per-stage stall warning thresholds in seconds. Controls when the headless runner
emits stall warnings for each pipeline stage. Follow-up warnings use escalating
intervals (2x, 3x, 4x of the original threshold) instead of repeating at a
constant interval.

| Stage              | Type    | Default | Description                        |
| ------------------ | ------- | ------- | ---------------------------------- |
| `issue-pickup`     | integer | `60`    | 1 minute — lightweight extraction  |
| `feature-planning` | integer | `180`   | 3 minutes — planning/analysis      |
| `feature-dev`      | integer | `600`   | 10 minutes — implementation stage  |
| `feature-validate` | integer | `300`   | 5 minutes — testing/validation     |
| `pr-create`        | integer | `60`    | 1 minute — PR template filling     |
| `pr-merge`         | integer | `420`   | 7 minutes — CI wait + review/merge |

All values must be >= 30 seconds.

**CI-aware stall detection (pr-merge):** During the pr-merge stage, the stall
detector is CI-aware. When `wait-for-ci-checks.sh` emits `CI_PROGRESS:` updates,
stall warnings are suppressed and replaced with informative CI status messages
(e.g., "CI: 2/5 passed, 3 pending"). Generic stall warnings only appear if CI
progress reporting stops unexpectedly for more than 60 seconds.

**Example:**

```yaml
pipeline:
  stall_thresholds:
    issue-pickup: 60 # 1 minute
    feature-planning: 180 # 3 minutes
    feature-dev: 600 # 10 minutes
    feature-validate: 300 # 5 minutes
    pr-create: 60 # 1 minute
    pr-merge: 420 # 7 minutes (CI typically takes 3-5 min)
```

**Environment overrides (per stage):**

```bash
export NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_ISSUE_PICKUP=90
export NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_FEATURE_DEV=1200
export NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_PR_MERGE=600
```

**Escalating follow-up warnings:**

After the initial stall warning fires, follow-up warnings are emitted at
escalating intervals based on the stage's threshold:

- 1st warning: at threshold (e.g., 10 min for feature-dev)
- 2nd warning: at 2x threshold (20 min)
- 3rd warning: at 3x threshold (30 min)
- And so on...

This replaces the previous fixed 2-minute interval, reducing noise for
long-running stages while still alerting for genuine stalls.

**Used by:**

- `skillRunner.ts` — Headless mode stall detection timer

---

#### pipeline.stall_kill_multiplier

Multiplier of the stall threshold at which the subagent process is forcibly
killed. For example, with a `feature-dev` stall threshold of 600s and a
`stall_kill_multiplier` of 8, the process is terminated after 4800s (80 min).

| Option                  | Type    | Default | Range | Description                                        |
| ----------------------- | ------- | ------- | ----- | -------------------------------------------------- |
| `stall_kill_multiplier` | integer | `8`     | 0+    | Kill multiplier over stall threshold. 0 = disable. |

**Example:**

```yaml
pipeline:
  stall_kill_multiplier: 5 # Kill at 5x stall threshold instead of 8x
```

Setting to `0` disables auto-kill entirely (stall warnings still fire).

**Used by:**

- `skillRunner.ts` — Headless mode process auto-kill timer

**See also:**

- Issue #1620 — Subagent stall auto-kill

---

#### pipeline.stall_idle_ms

Absolute idle-kill threshold in milliseconds. When set, this value **replaces**
the computed `stall_threshold × stall_kill_multiplier` value as the idle-kill
gate. Use this to cap the maximum idle time before a stall-kill, independent of
the multiplier system.

When unset (the default), the multiplier-derived value is used unchanged.

| Option          | Type    | Default     | Range | Description                                         |
| --------------- | ------- | ----------- | ----- | --------------------------------------------------- |
| `stall_idle_ms` | integer | `undefined` | 0+    | Absolute idle threshold (ms). Overrides multiplier. |

**Example:**

```yaml
pipeline:
  stall_idle_ms: 480000 # Cap idle kill at 8 minutes (vs. default 20 min for feature-validate)
```

A 60-second nudge grace period applies: when the idle threshold is first
reached, a `[stall-nudge]` warning is logged and SIGTERM is deferred for 60
seconds. If no new output arrives, the kill proceeds. Hard-cap and
quota-fast-fail paths skip the grace period.

**Environment variable override:**

```bash
export NIGHTGAUGE_PIPELINE_STALL_IDLE_MS=480000
```

**Used by:**

- `skillRunner.ts` — Overrides idle-kill gate when set
- `monitoringResolver.ts` — `getStallIdleMs()` resolver

**See also:**

- Issue #3484 — Fix model stall after tool result
- Issue #1620 — Subagent stall auto-kill

---

#### pipeline.stage_cost_caps

Per-stage hard USD ceiling. When a stage's accumulated cost
(`tokenAccumulator.getTotal().costUsd`) exceeds the **effective** cap (base
× model scale, see below), the subagent is forcibly terminated using the
same SIGTERM/SIGKILL sequence as the stall-kill path. A missing entry or a
value of `0` means uncapped for that stage.

The check runs **push-based** on every streaming token-usage update (Issue
#3180), bounding overshoot to a single tool-use's incremental cost. The 30s
stall ticker also re-checks as a polling fallback for stages that don't emit
parseable stream-json usage.

Distinct from `BudgetEnforcer` (`pipeline.budget_mode` /
`pipeline.budget_grace_percent`), which uses an estimate-vs-actual flow with
a grace buffer. This cap is a hard, deterministic ceiling — no grace, no
prompt, no retry context written. Terminations are categorized as
`stage-cost-cap-exceeded` (infrastructure-weight in reliability scoring).

| Stage              | Default | Effective on Sonnet medium (1.0×) | Effective on Opus high (5.0×) | p95 (90d) | n   |
| ------------------ | ------- | --------------------------------- | ----------------------------- | --------- | --- |
| `issue-pickup`     | `1.00`  | $1.00                             | $5.00                         | $0.68     | 561 |
| `feature-planning` | `6.00`  | $6.00                             | $30.00                        | $2.97     | 733 |
| `feature-dev`      | `23.00` | $23.00                            | $115.00                       | $11.25    | 848 |
| `feature-validate` | `7.00`  | $7.00                             | $35.00                        | $3.72     | 755 |
| `pr-create`        | `3.00`  | $3.00                             | $15.00                        | $1.56     | 828 |
| `pr-merge`         | `4.00`  | $4.00                             | $20.00                        | $2.25     | 841 |

Each entry is a `float >= 0.0`; `0` disables the cap for that stage.

**Default calibration (Issue #3208, 2026-05-06)**: bases are p95 × 2 (rounded
to the nearest dollar) over the last 90 days of `complete | cancelled` runs
in `.nightgauge/pipeline/history/*.jsonl`. The factor of 2 gives ~50%
headroom above the typical-but-real productive cost while staying well below
runaway outliers (max observed = $215.97 on `feature-dev`). Re-run the
distribution at any time:

```bash
npx tsx scripts/audit-stage-cost-distribution.ts
```

**Example override:**

```yaml
pipeline:
  stage_cost_caps:
    feature-dev: 30.00 # raise from default $23 for known-large refactors
    pr-create: 0 # disable the cap for this stage
```

Set a stage to `0` to disable its cap. Omit a stage entirely to fall back to
the calibrated default above.

**Environment variable:**

```bash
# Override per stage via env (uppercase, hyphens become underscores)
export NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV=10.00
```

**Mode-aware multiplier (recalibrated post-2026-05-04):**

The configured cap is the **base** value calibrated for Sonnet at medium
effort. When the model router escalates (Opus, Maximum performance-mode,
high-effort thinking), the effective cap is multiplied so heavier modes
get proportional headroom rather than tripping a Sonnet-calibrated cap on
legitimate work. The same multiplier scales the `BudgetEnforcer` hard-mode
terminate path so the two limiters can never disagree on whether a heavier
model gets headroom.

| Resolved model + effort | Multiplier | Effective cap (when base = $23, feature-dev) |
| ----------------------- | ---------- | -------------------------------------------- |
| `haiku` (any effort)    | 1.0×       | $23.00                                       |
| `sonnet` medium         | 1.0×       | $23.00                                       |
| `sonnet` high           | 1.3×       | $29.90                                       |
| `opus` medium           | 3.5×       | $80.50                                       |
| `opus` high             | 5.0×       | $115.00                                      |
| `fable` medium          | 7.0×       | $161.00                                      |
| `fable` high            | 10.0×      | $230.00                                      |
| Other (`gpt-5`, etc.)   | 1.0×       | $23.00                                       |

`fable` (the premium frontier tier, used by the `frontier` performance mode) is
scaled ~2× the Opus values because Fable 5 is ~2× Opus pricing — a
Sonnet-calibrated cap would otherwise kill legitimate Fable runs prematurely.

Empirical anchors:

- Issue #3089 (Opus high) terminated at $9.02 against a $5 cap. The original
  3.0× multiplier (#3180) widened to $15 — sufficient.
- Issue #871 (Opus high, MAXIMUM mode) terminated at $25.31 final / $23.03
  cost-at-kill against a $15 effective cap — the 3.0× value was undersized
  for real MAXIMUM-mode feature-dev. The 5.0× recalibration gives a $25
  effective cap, just at the observed peak; runaways above $25 still kill.
- Issue #331 (Opus high, MAXIMUM mode) terminated at $5.74 against a
  Sonnet-calibrated `BudgetEnforcer` limit of $4.50 (M, generous, hard
  mode + 50% grace). Applying the multiplier in `BudgetEnforcer.checkBudget`
  raises the effective limit to $22.50.

**Env-var overrides:** the multiplier table can be tuned without a code
change via `NIGHTGAUGE_BUDGET_MODEL_SCALE_<FAMILY>[_<EFFORT>]`:

```bash
# Widen Opus high-effort to 6.0× (e.g. for known-large refactor work)
export NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS_HIGH=6.0

# Tighten Opus medium to 2.5× (revert to the pre-2026-05-04 value)
export NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS=2.5
```

Invalid (non-numeric, zero, negative) env values are ignored; the table
default is used.

The effective cap is logged once at stage start. Example output for a
non-default scale:

```text
[skillRunner] Stage cost cap for feature-dev: $115.00 (base $23.00 × 5.00 for claude-opus-4-7/high)
```

**Used by:**

- `skillRunner.ts` — Push-based check on every `tokenAccumulator.add(...)`
  call (#3180) plus the legacy 30s polling fallback. On cap crossing,
  terminates the subagent and records `costCapExceeded` /
  `costAtTerminationUsd` / `costCapUsd` (effective) on the completion
  payload. The diagnostic log additionally records `cost_cap_base_usd` and
  `cost_cap_scale` for triage.
- `HeadlessOrchestrator.ts` — Detects `result.costCapExceeded` and records a
  `[cost-cap-exceeded]` failure with the actual cost at termination.

**Provider scale (Issue #3229):**

The base caps in `DEFAULT_STAGE_COST_CAPS` were calibrated from 90 days of
Claude-only history (PR #3209). With per-stage providers (Epic B) and
unified cost computation (C2), a `feature-planning: gemini` stage costs
~80% less than the same stage on Claude Opus — a Claude-calibrated cap
forces non-Claude stages to over-spend before tripping. The provider
scale composes multiplicatively last so Claude users see no change while
non-Claude adapters get a proportionally tighter ceiling.

The full formula is therefore:

```
effectiveCap = baseCap × modelScale × modeMultiplier × providerScale
```

Provider scale defaults (seeded from C1 pricing-table ratios — see
`packages/nightgauge-vscode/src/utils/providerPricing.ts`):

| Adapter      | Default scale | Effective cap (base = $23, feature-dev, sonnet/medium, elevated) |
| ------------ | ------------- | ---------------------------------------------------------------- |
| `claude`     | 1.0×          | $23.00 (preserves PR #3209 calibration byte-for-byte)            |
| `codex`      | 0.7×          | $16.10                                                           |
| `gemini`     | 0.4×          | $9.20                                                            |
| `gemini-sdk` | 0.4×          | $9.20                                                            |
| `copilot`    | 0.2×          | $4.60                                                            |
| `lm-studio`  | 0.0           | _switches to time-based cap_ — see `pipeline.stage_time_caps`    |
| `ollama`     | 0.0           | _switches to time-based cap_                                     |

**Cross-axis composition example.** `feature-dev` on `gemini` with the
default `gemini-2.5-pro` model in `elevated` mode:

```
$23.00 × 1.0 (no Claude family match) × 1.0 (elevated) × 0.4 (gemini) = $9.20
```

`provider_scale=0` is the explicit "this provider has no meaningful
per-token cost — switch to time-based cap" signal. It is distinct from
`modelScale` and `modeMultiplier`, both of which reject `0` as a typo.
Local adapters (lm-studio, ollama) opt into time-based termination via
this asymmetry.

---

#### pipeline.cost_cap_provider_scale

Per-adapter override for the provider scale described above. Each value
is a non-negative float; `0` is the explicit time-cap-mode sentinel.
Invalid (non-numeric, negative) env / config values fall through to the
default table.

```yaml
pipeline:
  cost_cap_provider_scale:
    gemini: 0.5 # tighten gemini further than the 0.4 default
    codex: 1.0 # opt out — give codex stages a Claude-equivalent ceiling
    lm-studio: 0.0 # explicit (also the default)
```

**Environment variable:**

```bash
# Adapter is uppercased; hyphens become underscores
export NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI=0.5
export NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI_SDK=0.4
export NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_LM_STUDIO=0.0
```

---

#### pipeline.stage_cost_caps_per_provider

Per-(adapter, stage) override for the **baseCap** of
`getEffectiveStageCostCap`. When set, replaces only `baseCap` for that
tuple; model, mode, and provider scales still compose on top (semantic
symmetry with `pipeline.stage_cost_caps`). Use this to set explicit USD
ceilings per provider when the global default is the wrong baseline.

```yaml
pipeline:
  stage_cost_caps_per_provider:
    gemini:
      feature-dev: 15.00 # raise from $23 × 0.4 = $9.20 effective
      pr-create: 2.00
    codex:
      feature-dev: 30.00
```

The override is `baseCap`, so a $15 override for `gemini.feature-dev` at
`elevated` mode still gets the provider scale on top:
`$15.00 × 1.0 × 1.0 × 0.4 = $6.00`. Users who want the override to act as
the literal effective cap pin `cost_cap_provider_scale.<adapter>: 1.0`
alongside it.

**Environment variable:**

```bash
# Adapter and stage are uppercased; hyphens become underscores.
export NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_GEMINI_FEATURE_DEV=15.00
```

---

#### pipeline.stage_cost_warn_thresholds

Per-stage warn multiplier overrides for the cost warn threshold introduced in
Issue #3508. When a stage's accumulated cost exceeds
`historicalMedian × multiplier`, a non-blocking VSCode warning toast is fired
and `[cost-warn]` is written to stderr. The pipeline **continues** — this is
not a kill.

Setting a stage key to `0` disables the warn for that stage. When no history
exists (median = 0), the warn is automatically disabled regardless of
multiplier.

```yaml
pipeline:
  stage_cost_warn_thresholds:
    feature-dev: 2.0 # warn at 2× median instead of global 1.5×
    pr-create: 0 # disable warn for this stage
```

**Resolution priority (highest to lowest):**

1. Per-stage env var: `NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_<STAGE>`
2. `pipeline.stage_cost_warn_thresholds.<stage>` in config
3. `pipeline.cost_warn_multiplier` in config (global)
4. `NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER` env var (global)
5. Default: `1.5`

**Environment variable (per-stage):**

```bash
# Stage is uppercased; hyphens become underscores
export NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV=2.0
export NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_PR_CREATE=0
```

---

#### pipeline.cost_warn_multiplier

Global cost warn multiplier applied to all stages that do not have a per-stage
override in `pipeline.stage_cost_warn_thresholds`. See
[`pipeline.stage_cost_warn_thresholds`](#pipelinestage_cost_warn_thresholds)
for the full resolution priority.

```yaml
pipeline:
  cost_warn_multiplier: 2.0 # warn at 2× historical median for all stages
```

Set to `0` to disable the warn feature globally.

**Environment variable:**

```bash
export NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER=2.0
```

---

#### pipeline.runaway_ceiling_multiplier

Multiplier used to compute the **runaway ceiling** for each stage:

```
runwayCeilingUsd = max($75, effectiveCap × runaway_ceiling_multiplier)
```

When a stage's accumulated cost exceeds the runaway ceiling, the stage is
terminated with a `[runaway-ceiling-exceeded]` marker. Unlike the legacy
`[cost-cap-exceeded]` path (which halts the queue), this routes through
`TerminalKindStallKill` — a 30-minute backoff with no autonomous pause and no
lifetime failure cap increment.

The `$75` floor ensures that even low-cost stages get a meaningful safety net
that prevents runaway loops from burning budget unchecked. The multiplier must
be `≥ 1`; values below `1` are ignored and the default `3.0` applies.

```yaml
pipeline:
  runaway_ceiling_multiplier: 4.0 # push ceiling to 4× effectiveCap (min $75)
```

**Typical values:**

| effectiveCap | multiplier | ceiling     |
| ------------ | ---------- | ----------- |
| $23 (sonnet) | 3.0        | $75 (floor) |
| $115 (opus)  | 3.0        | $345        |
| $23 (sonnet) | 4.0        | $92         |

**Environment variable:**

```bash
export NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER=4.0
```

---

#### pipeline.stage_time_caps

Per-stage absolute time cap (in **seconds**) used as the fallback hard
ceiling when `cost_cap_provider_scale.<adapter>` is `0` (lm-studio,
ollama). Token cost is meaningless for local adapters but runaway loops
are still a real failure mode — `stage_time_caps` is the explicit knob
that bounds them.

When the cost-cap path disables itself (`provider_scale=0`), the stall
ticker ORs `stage_time_caps.<stage>` with `pipeline.stage_hard_caps.<stage>`
— whichever is smaller and `> 0` wins, leaving the absolute hard-cap
escape hatch intact. The kill diagnostic identifies the path
(`time_cap` vs `hard_cap` vs `idle`).

`0` (the default) means uncapped on time. Computing per-stage defaults
from `p95(elapsed) × 1.5` over historical data is **out-of-scope for
Issue #3229** (per AC #4) and tracked as a follow-up audit. Until that
audit lands, lm-studio / ollama runs are uncapped on time unless the
operator opts in here.

```yaml
pipeline:
  stage_time_caps:
    feature-dev: 1800 # 30 min hard ceiling for local-adapter feature-dev
    pr-create: 600 # 10 min for pr-create
```

**Environment variable:**

```bash
export NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_FEATURE_DEV=1800
```

**See also:**

- Issue #3002 — Per-stage cost circuit breaker (original implementation)
- Issue #3180 — Push-based enforcement + mode-aware multiplier
- Issue #3217 — Per-mode cost-cap multiplier
- Issue #3229 — Provider-relative cost-cap defaults + override path
- `docs/FAILURE_TAXONOMY.md` — `stage-cost-cap-exceeded` category

---

#### pipeline.retry_limits

Per-stage retry limits auto-tuned from execution history. Keys are stage names,
values are maximum retry counts. Written automatically by the self-improvement
loop based on historical success rates.

| Option                         | Type    | Default | Range | Description                    |
| ------------------------------ | ------- | ------- | ----- | ------------------------------ |
| `{stage}` (e.g. `feature-dev`) | integer | -       | 1–5   | Max retry count for that stage |

**Example:**

```yaml
pipeline:
  retry_limits:
    feature-dev: 3
    feature-validate: 2
    pr-create: 1
```

**See also:**

- Issue #1573 — Retry policy auto-tuning

---

#### pipeline.stage_timeouts

Per-stage timeout values (in milliseconds) auto-tuned from execution history.
Keys are stage names, values are timeout durations. Written automatically by the
pipeline learning system based on historical execution times.

| Option                         | Type    | Default | Range              | Description                            |
| ------------------------------ | ------- | ------- | ------------------ | -------------------------------------- |
| `{stage}` (e.g. `feature-dev`) | integer | -       | 60000–1800000 (ms) | Timeout for that stage in milliseconds |

**Example:**

```yaml
pipeline:
  stage_timeouts:
    feature-dev: 900000 # 15 minutes
    feature-validate: 600000 # 10 minutes
```

**See also:**

- Issue #1573 — Stage timeout auto-tuning

---

#### pipeline.max_concurrent

Unified concurrent-slot ceiling — the **single source of truth** for both
drag-to-pipeline (TS `ConcurrentPipelineManager`) and autonomous-mode dispatch
(Go `internal/orchestrator` scheduler). Each concurrent pipeline runs in an
isolated worktree directory under `{worktree_base}/issue-{N}/`. Set to 1 to
disable concurrent execution (sequential mode, no worktrees).

> **Deprecated:** `autonomous.max_concurrent` was previously a separate key.
> It is now honored only as a fallback when `pipeline.max_concurrent` is
> unset, and the extension prompts to consolidate on activation. See
> [DEPRECATIONS.md](DEPRECATIONS.md#autonomousmax_concurrent).

| Option           | Type    | Default        | Range | Description                                     |
| ---------------- | ------- | -------------- | ----- | ----------------------------------------------- |
| `max_concurrent` | integer | `3`            | 1–10  | Maximum concurrent pipeline executions          |
| `worktree_base`  | string  | `".worktrees"` | -     | Base directory for worktrees (relative to repo) |

**Example:**

```yaml
pipeline:
  max_concurrent: 4
  worktree_base: ".worktrees"
```

**Environment override:**

```bash
export NIGHTGAUGE_PIPELINE_MAX_CONCURRENT=2
```

**Used by:**

- `HeadlessOrchestrator` — Creates and manages worktree-based concurrent
  pipeline slots
- `DiscordService` — Concurrent slot notifications

**See also:**

- Issue #1621 — Git worktree-based concurrent pipeline execution

---

#### pipeline.epic_queue_filter

Pre-filter applied when an epic is dragged onto the pipeline queue. Only
sub-issues whose project-board status is in `eligible_statuses` are enqueued,
and (by default) sub-issues that already have an open PR are skipped —
enqueuing those produced the "git worktree add fatal: branch already exists"
error fixed by Issue #2992.

This filter only runs on the **drag path**. Autonomous scheduling already
respects board status upstream via GitHub's
`ProjectV2.items(query: "status:...")` parameter, so no filter is applied
to that code path.

| Option                     | Type     | Default     | Description                                                           |
| -------------------------- | -------- | ----------- | --------------------------------------------------------------------- |
| `eligible_statuses`        | string[] | `["Ready"]` | Project-board statuses that remain pickup-eligible (case-insensitive) |
| `skip_issues_with_open_pr` | boolean  | `true`      | Skip sub-issues that already have an open PR linked                   |

**Example:**

```yaml
pipeline:
  epic_queue_filter:
    eligible_statuses:
      - Ready
      - In progress
    skip_issues_with_open_pr: true
```

**Behaviour on drop:**

- When some sub-issues are skipped, an info toast summarises the breakdown
  (e.g. `Queued 3 sub-issues of epic #42; skipped 2 (Backlog: 1, open PR: 1).`).
- When all sub-issues are skipped, a warning toast lists the reasons and
  nothing is enqueued.

**Used by:**

- `IssueDragAndDropController` — epic drop path pre-filter
- `EpicQueueFilter` service — `filterEligibleSubIssues()` helper

**See also:**

- Issue #2992 — epic drag queues Backlog/in-review sub-issues

---

### pipeline.baseline_ci_gate (Issue #3004)

Defers dispatch of issues whose acceptance criteria require promoting a CI
check on `main` when `main`'s recent runs of that check are failing. Runs in
`issue-pickup` Phase 2.8 (after the size gate). A daily
`baseline-defer-sweep.yml` cron resumes deferred items when the baseline goes
green.

```yaml
pipeline:
  baseline_ci_gate:
    enabled: true # default true; set false to bypass
    lookback_runs: 5 # number of recent completed runs to inspect (max 20)
    red_threshold: 2 # defer when ≥ this many of the last N runs failed
    green_threshold: 2 # promote when last N runs are all `success`
```

| Field             | Type    | Default | Description                                                                                                                                   |
| ----------------- | ------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `enabled`         | boolean | `true`  | Master toggle. When `false`, the gate is skipped during issue-pickup and the daily promote sweep is a no-op.                                  |
| `lookback_runs`   | number  | `5`     | How many recent _completed_ runs of the referenced workflow on `main` to inspect when computing the failure rate. Capped at 20 by the binary. |
| `red_threshold`   | number  | `2`     | Defer dispatch when at least this many of the last `lookback_runs` failed.                                                                    |
| `green_threshold` | number  | `2`     | Promote (resume) a deferred item when the most-recent N completed runs are all `success`.                                                     |

**Trigger semantics**: the gate's classifier scans each AC item for the
keywords `required check`, `required status`, `branch protection`, `ruleset`,
or the regex patterns `promote.*to.*required`, `enforce.*on.*main`,
`make.*required.*check`. When matched, the workflow path is extracted from
`.github/workflows/<file>.ya?ml` references and the job name from the first
backticked phrase that looks like a job name. Best-effort: if either is
unparseable, dispatch is allowed and the gate logs the unparseable AC.

**Failure classification**: Deferrals record an outcome with the
`[baseline-ci-deferred]` reason tag, classified as `infrastructure` (0.05
weight) by the failure taxonomy — see
[FAILURE_TAXONOMY.md](FAILURE_TAXONOMY.md#infrastructure).

**Queue surface**: Deferred items appear in the queue with
`status: "paused"` and `pausedReason.kind: "baseline_ci_red"`. The Go queue
schema bumps additively from `2.1` to `2.2`. The dashboard renders deferred
items in the existing paused-items panel without any separate UI.

**Environment variables**:

```bash
NIGHTGAUGE_PIPELINE_BASELINE_CI_GATE_ENABLED=false
NIGHTGAUGE_PIPELINE_BASELINE_CI_GATE_LOOKBACK_RUNS=10
NIGHTGAUGE_PIPELINE_BASELINE_CI_GATE_RED_THRESHOLD=3
NIGHTGAUGE_PIPELINE_BASELINE_CI_GATE_GREEN_THRESHOLD=2
```

---

### pipeline.scope_drift_gate (Issue #3040)

Verifies that files modified for a `type:docs` or `type:chore` issue fall
within an allowlist. Out-of-scope changes indicate scope drift — typically
caused by stale worktrees reverting recently-merged work alongside legitimate
scoped changes. Runs in `pr-create` Phase 2.6 and consumes
`dev-{N}.json.files_changed` (no live `git diff`).

```yaml
pipeline:
  scope_drift_gate:
    enabled: true # default true; set false to bypass entirely
    enforcement_mode: warn # "warn" (log only) | "strict" (block PR)
    bypass_label: scope:cross-cutting
    allowlist_docs:
      - docs/**
      - "*.md"
      - .github/**
      - README*
    allowlist_chore: # falls back to allowlist_docs when empty
      - docs/**
      - "*.md"
      - .github/**
      - README*
      - configs/**
```

| Field              | Type     | Default                                        | Description                                                                                                                               |
| ------------------ | -------- | ---------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `enabled`          | boolean  | `true`                                         | Master toggle. When `false`, the gate is skipped and `scope_drift_check` is `"skipped"`.                                                  |
| `enforcement_mode` | string   | `"warn"`                                       | `"warn"` logs drift and lets the PR proceed (status `passed`). `"strict"` blocks PR creation (status `failed`, exit 1) when drift exists. |
| `bypass_label`     | string   | `"scope:cross-cutting"`                        | Label that, when present on the issue, bypasses the gate entirely. Useful for cross-cutting docs that legitimately touch code.            |
| `allowlist_docs`   | string[] | `["docs/**", "*.md", ".github/**", "README*"]` | Glob patterns evaluated for `type:docs` issues. See _Pattern syntax_ below.                                                               |
| `allowlist_chore`  | string[] | falls back to `allowlist_docs`                 | Glob patterns evaluated for `type:chore` issues. When unset or empty, the docs allowlist is used.                                         |

**Pattern syntax** (root-anchored, `.gitignore`-style):

- `docs/**` — matches `docs` itself or anything under `docs/`
- `*.md` — single-segment glob; matches `README.md` but **not** `docs/index.md`
- `**/README*` — explicit "any depth" form
- `Makefile` — exact match only (does not match `src/Makefile`)

There is no implicit basename fallback. To allow a file at any depth, use a
`**` segment or include a parent `<dir>/**` pattern.

**Behavior**:

- Issue type is inferred from `type:docs` / `type:chore` labels. Other types
  short-circuit with status `"skipped"`.
- Deleted files are never considered drift — only `files_changed.created` and
  `files_changed.modified` from `dev-{N}.json` are evaluated.
- Drift events emit a `scope_drift_detected` pipeline event (best-effort —
  silent when no platform is configured) and append to
  `.nightgauge/audit/scope-drift-stats.json` for offline counter tuning.

**Failure classification**: Strict-mode rejections exit `1` with a
human-readable `Scope drift gate: BLOCKED` banner on stderr. The pr-create
skill propagates the exit so the orchestrator records the failure on the
stage. Warn-mode drift exits `0` and is informational.

**Environment variables** (where applicable):

```bash
NIGHTGAUGE_PIPELINE_SCOPE_DRIFT_GATE_ENABLED=false
NIGHTGAUGE_PIPELINE_SCOPE_DRIFT_GATE_ENFORCEMENT_MODE=strict
NIGHTGAUGE_PIPELINE_SCOPE_DRIFT_GATE_BYPASS_LABEL=scope:cross-cutting
```

---

### agent_teams

Dynamic subagent scaling settings for wave orchestration. Controls how the wave
orchestrator adjusts concurrency per wave based on wave size, remaining token
budget, and configured limits.

When an epic is processed in parallel waves, each wave may contain more issues
than can be efficiently run simultaneously. The `agent_teams` config allows the
orchestrator to dynamically scale concurrency:

1. Start with wave size (ideal: one goroutine per sub-issue)
2. Apply `max_concurrent` ceiling
3. Apply budget constraint: if `remaining_budget / concurrency` falls below
   `min_budget_per_agent`, reduce concurrency
4. Floor at 1 (always run at least one agent)

When concurrency is less than wave size, the wave is split into sequential
batches. For example, 8 items with concurrency 4 produces two batches of 4,
run one after the other.

| Option                 | Type    | Default  | Range | Description                                           |
| ---------------------- | ------- | -------- | ----- | ----------------------------------------------------- |
| `max_concurrent`       | integer | `6`      | 1-12  | Hard ceiling on parallel subagents per wave           |
| `min_budget_per_agent` | integer | `100000` | -     | Minimum tokens per agent to be viable (default: 100K) |

**Example:**

```yaml
agent_teams:
  max_concurrent: 6
  min_budget_per_agent: 100000
```

**Scaling decision reasons:**

| Reason              | Meaning                                         |
| ------------------- | ----------------------------------------------- |
| `ideal`             | Wave size fits within ceiling and budget        |
| `config_ceiling`    | Wave size exceeded `max_concurrent`             |
| `budget_constraint` | Remaining budget too small for full parallelism |

**Event emission:**

When a scaling decision is made, the orchestrator emits a `wave.scaling` event
via the `onScalingDecision` callback with the wave index, wave size, chosen
concurrency, reason, remaining budget, and per-agent budget.

**Used by:**

- `WaveOrchestrator` — Dynamic concurrency per wave in epic execution
- `Scheduler.OnScalingDecision()` — Callback for UI/IPC observability

**See also:**

- Issue #2403 — Dynamic subagent scaling
- Issue #2401 — Wave orchestrator

---

#### pipeline.mcp-tools

User-level MCP tool configuration for pipeline stages. Controls which MCP tools
are available to each pipeline stage, merged (union) with the SKILL.md
`mcp-tools` frontmatter field. This is additive only — it cannot remove tools
declared in SKILL.md.

Resolution order (all merged via set union):

```
SKILL.md `mcp-tools` ∪ config.yaml `pipeline.mcp-tools.global` ∪ config.yaml `pipeline.mcp-tools.stages.<stage>`
```

When neither config.yaml nor SKILL.md specifies MCP tools, no MCP tools are
passed (existing behavior preserved).

| Option           | Type     | Default | Description                                       |
| ---------------- | -------- | ------- | ------------------------------------------------- |
| `global`         | string[] | -       | MCP tools available to all pipeline stages        |
| `stages.{stage}` | string[] | -       | Per-stage MCP tool overrides (merged with global) |

**Example:**

```yaml
pipeline:
  mcp-tools:
    global:
      - mcp__sentry__capture_error
    stages:
      feature-dev:
        - mcp__playwright__browser_navigate
        - mcp__playwright__browser_snapshot
      feature-validate:
        - mcp__playwright__*
```

**Difference from `pipeline.stages.{stage}.mcp_tools`:** The older
`pipeline.stages.{stage}.mcp_tools` syntax (Issue #1725) overrides SKILL.md
declarations entirely for a specific stage. The newer `pipeline.mcp-tools`
syntax (Issue #1726) uses additive union semantics instead. Both can coexist;
when both are present, all tool lists are merged.

**Used by:**

- `incrediConfig.ts` — `getMcpToolsConfig()`
- `skillRunner.ts` — `runStageSkillHeadless()`, `runStageSkillInteractive()`

**See also:**

- Issue #1726 — Add pipeline.mcp-tools config for user-level MCP tool control
- [MCP Integration Guide](./MCP_INTEGRATION.md) — MCP server setup

---

#### pipeline.context_budgets

Per-stage input token budget configuration. Controls maximum input tokens
injected per pipeline stage. Default mode is `soft` (warn only) to avoid
breaking existing pipelines.

| Option          | Type    | Default  | Description                                         |
| --------------- | ------- | -------- | --------------------------------------------------- |
| `enabled`       | boolean | `true`   | Master toggle for context budget enforcement        |
| `mode`          | string  | `'soft'` | Enforcement mode: `'soft'`, `'hard'`, `'threshold'` |
| `grace_percent` | number  | `50`     | Grace buffer percentage before enforcement (0-500)  |
| `stage_limits`  | object  | -        | Per-stage input token limits (flat or per-size)     |

**Default per-stage per-size input token budgets:**

| Stage              | XS      | S       | M       | L       | XL      |
| ------------------ | ------- | ------- | ------- | ------- | ------- |
| `issue-pickup`     | 5,000   | 8,000   | 15,000  | 25,000  | 40,000  |
| `feature-planning` | 50,000  | 80,000  | 120,000 | 200,000 | 300,000 |
| `feature-dev`      | 100,000 | 150,000 | 250,000 | 400,000 | 600,000 |
| `feature-validate` | 50,000  | 80,000  | 120,000 | 200,000 | 300,000 |
| `pr-create`        | 5,000   | 8,000   | 15,000  | 25,000  | 40,000  |
| `pr-merge`         | 10,000  | 15,000  | 25,000  | 40,000  | 60,000  |

**Enforcement modes:**

- **`soft`** (default): Warns when input tokens exceed budget, never terminates.
  Recommended for initial adoption.
- **`hard`**: Terminates stage when input tokens exceed budget + grace buffer.
- **`threshold`**: Same as `hard` — terminates at configured grace percentage.

**Example:**

```yaml
pipeline:
  context_budgets:
    enabled: true
    mode: soft
    grace_percent: 50
    stage_limits:
      feature-dev: 300000 # Flat limit for all sizes
      feature-planning: # Per-size limits
        S: 60000
        M: 100000
        L: 180000
```

**Environment overrides:**

```bash
export NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_ENABLED=false
export NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_MODE=hard
export NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_GRACE_PERCENT=75
export NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_LIMIT_FEATURE_DEV=300000
export NIGHTGAUGE_PIPELINE_CONTEXT_BUDGET_LIMIT_FEATURE_PLANNING=150000
```

**Relationship to other budgets:**

Context budgets complement existing cost budgets (`budget_mode`,
`budget_grace_percent`, `stage_budgets`) and output token limits
(`output_token_limits`). All three enforcement systems operate independently in
the `onTokenUsage` callback:

1. **Cost budget** (USD) — enforces spending limits per stage
2. **Output token limit** — caps generated output per stage
3. **Context budget** (input tokens) — caps context injection per stage

If any enforcer triggers termination, the stage is killed.

@see Issue #790 - Per-stage context budgets

---

#### Budget Tuning

Default per-stage cost budgets are re-baselined at **≤ 2× observed p90**
(Sample: ≥30 runs, post-deterministic-first era, Sonnet 4.6 pricing — Issue #3269).
The 50% grace buffer means the effective termination limit sits at approximately
1.5× these values. `pr-merge` and `pr-create` caps apply to the LLM fallback path
only — when the deterministic-first runner succeeds (Issue #3264), stage cost ≈ $0.

**Current default budgets (USD):**

| Stage            | XS    | S     | M      | L      | XL     |
| ---------------- | ----- | ----- | ------ | ------ | ------ |
| issue-pickup     | $0.30 | $0.30 | $1.50  | $1.50  | $2.00  |
| feature-planning | $3.00 | $4.00 | $5.00  | $7.00  | $10.00 |
| feature-dev      | $4.00 | $8.00 | $16.00 | $50.00 | $80.00 |
| feature-validate | $2.00 | $4.00 | $20.00 | $40.00 | $70.00 |
| pr-create        | $0.50 | $1.00 | $3.00  | $4.00  | $5.50  |
| pr-merge         | $0.40 | $1.00 | $2.00  | $3.00  | $5.00  |

**Budget presets** provide named multipliers over the standard defaults. Set
`budget_preset` in your config to apply a preset:

```yaml
pipeline:
  budget_preset: generous # conservative (0.5x) | standard (1.0x) | generous (2.0x)
```

| Preset         | Multiplier | Use Case                              |
| -------------- | ---------- | ------------------------------------- |
| `conservative` | 0.5x       | Cost-sensitive pipelines, XS/S issues |
| `standard`     | 1.0x       | Balanced defaults (shipped)           |
| `generous`     | 2.0x       | Complex issues, opus-heavy pipelines  |

When `budget_preset` is set, all default stage budgets are scaled by the
preset's multiplier. Explicit `stage_budgets` overrides take precedence over
preset-derived values.

When `budget_preset` is not set (the default), standard budgets apply without
any multiplier.

To replicate a preset via explicit overrides instead, multiply each default by
the preset's multiplier. For example, a "generous" feature-dev M budget:
`$8.00 × 2.0 = $16.00`.

```yaml
# Example: generous-equivalent overrides for feature-dev
pipeline:
  stage_budgets:
    feature-dev:
      XS: 4.00
      S: 8.00
      M: 16.00
      L: 50.00
      XL: 100.00
```

**Analyzing your own pipeline history:**

1. Export pipeline run costs from the dashboard or `.nightgauge/pipeline/`
   context files
2. Calculate p50 and p90 per stage
3. Set M budget to p90, then apply size multipliers
4. The 50% grace buffer provides headroom above your chosen baseline

@see Issue #947 - Recalibrate budget defaults

#### pipeline.stage_budget_multipliers

Per-stage budget multiplier overrides, written automatically by the adaptive
policy engine when budget-rebalancing decisions are applied. Multipliers scale
all size tiers of the built-in default budgets for a given stage.

**Example:**

```yaml
pipeline:
  stage_budget_multipliers:
    feature-dev: 1.5 # 50% more budget than default
    feature-validate: 0.8 # 20% less budget than default
```

Each multiplier is applied to all size tiers (XS through XL) of that stage's
default budget. Explicit `stage_budgets` entries take precedence over
multiplier-derived values.

**Used by:**

- `incrediConfig.ts` — `getBudgetEnforcementConfig()` reads and applies
  multipliers
- `AdaptivePolicyEngine` (SDK) — Writes multipliers based on execution history

---

#### pipeline.cache

Cache efficiency monitoring configuration. Controls the alert threshold for the
dashboard health widget's cache hit rate component.

| Option                   | Type   | Default | Description                                                                                |
| ------------------------ | ------ | ------- | ------------------------------------------------------------------------------------------ |
| `alert_threshold`        | number | `40`    | Minimum cache hit rate (0-100%) before health status degrades                              |
| `stage_alert_thresholds` | object | `{}`    | Per-stage cache hit rate overrides (0-100%), keyed by stage name (Issue #3804). See below. |

**Example:**

```yaml
pipeline:
  cache:
    alert_threshold: 50 # Require 50% cache hit rate (global default)
    stage_alert_thresholds:
      feature-validate: 70 # feature-validate must hit 70%
      issue-pickup: 20 # tolerate lower reuse for the lightweight pickup stage
```

##### Per-stage cache hit rate threshold (Issue #3804)

`stage_alert_thresholds` lets you flag low cache reuse on a **per-stage** basis.
The threshold for a given stage resolves as:

1. `pipeline.cache.stage_alert_thresholds.<stage>` if an entry exists for that
   stage, otherwise
2. `pipeline.cache.alert_threshold` (the global default, `40`).

Both reporting surfaces read the **same resolved value**, so they always agree:

- **`nightgauge-pipeline-audit`** computes a per-stage cache hit rate from
  `stage_metrics.<stage>.token_stats` and emits a `token_efficiency` finding for
  each stage below its threshold (`per_stage_cache_hit_rate` report block).
- **`nightgauge-pipeline-health`** (Token Economics dimension) surfaces
  `perStageCacheHitRate.<stage>` metrics and the same per-stage low-reuse
  finding. See [HEALTH_MONITORING.md](HEALTH_MONITORING.md#1-token-economics-token-economics).

The cache hit rate is `cache_read / (cache_read + cache_creation + input)` per
stage. A stage with no cacheable input is reported as "no data" (`null`), never
`0%`, and never flagged. Defaults preserve current behavior — without any
`stage_alert_thresholds`, every stage uses the global `alert_threshold`.

**Environment override:**

```bash
export NIGHTGAUGE_PIPELINE_CACHE_ALERT_THRESHOLD=50
```

##### Prompt Caching Best Practices

Anthropic's prompt caching gives a 90% cost discount on cached input tokens. The
first ~1024 tokens of the system prompt are eligible for caching. To maximize
cache hits:

1. **Stable prefix**: All 6 pipeline SKILL.md files include a byte-identical
   "System Context" block in the first ~1024 tokens. This ensures consecutive
   pipeline stages reuse the cached prefix.

2. **Structure matters**: Content must be byte-identical between API calls to
   get cache hits. Avoid injecting variable data (timestamps, issue numbers)
   into the stable prefix area.

3. **Monitor the dashboard**: The health widget displays a cache hit rate
   sparkline and trend indicator. When the rate drops below
   `pipeline.cache.alert_threshold`, the cache health component shows as
   "degrading".

4. **Reading cache metrics**: In the dashboard health widget, the "Cache Hit
   Rate" component shows a 0-100% score derived from the average
   `cacheReadTokens / (cacheReadTokens + inputTokens)` across recent runs.

5. **Troubleshooting low rates**: If cache hit rates are consistently below the
   threshold, check that SKILL.md files haven't been modified in the stable
   prefix area. Run `md5` on the prefix block across all 6 files to verify
   byte-identity.

@see Issue #788 - Cache hit rate improvement

---

#### pipeline.alerting

Post-run alerting thresholds for cost and duration. When a pipeline run exceeds
configured thresholds, warnings are emitted to the output window. Alerting is
non-critical and never blocks pipeline completion.

Default thresholds are derived from P95 values in the telemetry audit (Feb
2026): cost > $45/run, duration > 32 minutes.

| Option                       | Type    | Default | Description                                  |
| ---------------------------- | ------- | ------- | -------------------------------------------- |
| `enabled`                    | boolean | `true`  | Master toggle for post-run alerting          |
| `cost_threshold_usd`         | number  | `45`    | Maximum expected cost per run in USD         |
| `duration_threshold_minutes` | number  | `32`    | Maximum expected duration per run in minutes |

**Example:**

```yaml
pipeline:
  alerting:
    enabled: true
    cost_threshold_usd: 50
    duration_threshold_minutes: 45
```

**Environment overrides:**

```bash
export NIGHTGAUGE_PIPELINE_ALERTING_ENABLED=false
export NIGHTGAUGE_PIPELINE_ALERTING_COST_THRESHOLD_USD=50
export NIGHTGAUGE_PIPELINE_ALERTING_DURATION_THRESHOLD_MINUTES=45
```

@see Issue #1048 - Automated cost/duration alerting

---

#### pipeline.token_budget_ceiling

Pipeline-level total cost ceiling across all stages in a single pipeline run.
Independent of per-stage budgets — both can fire; per-stage fires first for
individual stage overruns, ceiling catches cumulative runaway.

Enforcement phases:

1. **Warn-only threshold** at an absolute USD value (`warn_threshold_usd`,
   default `$50`) — logs a warning **without** stopping the stage. Separates
   "you're spending a lot" from "stop now" so a near-complete run isn't killed.
2. **Warning** at a configurable percentage (default 70%) — logs to output window
3. **Checkpoint** at a configurable percentage (default 85%) — writes a signal
   file (`.nightgauge/pipeline/checkpoint-signal-{N}.json`) so the running
   agent can commit current work and exit gracefully
4. **Hard stop** at 100% of `ceiling_usd` — pipeline will not start the next stage

Issue #3542 raised the ceiling from `$50` to `$150` after the old `$50` ceiling
killed a `$61.51` run that was 97% complete. The maintainer has since set the
default ceiling to `$75` — enough headroom above that `$61` run to let
near-complete work finish while keeping per-run costs bounded. `warn_threshold_usd`
keeps the `$50` figure visible as a non-fatal warning; use `override_ceiling_usd`
for legitimately longer runs.

| Option                         | Type    | Default | Description                                            |
| ------------------------------ | ------- | ------- | ------------------------------------------------------ |
| `enabled`                      | boolean | `true`  | Master toggle for pipeline-level ceiling               |
| `ceiling_usd`                  | number  | `75`    | Maximum total cost in USD per pipeline run (hard stop) |
| `warn_threshold_usd`           | number  | `50`    | Absolute USD spend at which to warn without stopping   |
| `warning_threshold_percent`    | number  | `70`    | Percentage of ceiling to emit warning                  |
| `checkpoint_threshold_percent` | number  | `85`    | Percentage of ceiling to signal graceful wrap-up       |
| `override_ceiling_usd`         | number  | -       | Override ceiling for intentionally large tasks         |

**Example:**

```yaml
pipeline:
  token_budget_ceiling:
    enabled: true
    ceiling_usd: 75
    warn_threshold_usd: 50
    warning_threshold_percent: 60
    checkpoint_threshold_percent: 80
```

**Override for large tasks** (via `config.local.yaml` — gitignored):

```yaml
pipeline:
  token_budget_ceiling:
    override_ceiling_usd: 300
```

**Environment overrides:**

```bash
export NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_ENABLED=true
export NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CEILING_USD=75
export NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_WARN_THRESHOLD_USD=50
export NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_OVERRIDE_CEILING_USD=300
```

When the ceiling is reached, the pipeline outcome is classified as
`budget-ceiling` (distinct from failures or cancellations) for cost tracking. The
Go scheduler classifies a ceiling kill as the `budget_ceiling_hit` terminal kind
— see [FAILURE_TAXONOMY.md](FAILURE_TAXONOMY.md).

@see Issue #1047 - Configurable token budget ceiling
@see Issue #3542 - Ceiling default 50 → 150 + warn-only threshold
@see Issue #3727 - Maintainer set ceiling default to $75 (warn stays $50)

---

#### pipeline.stage_models

Per-stage model routing using a 3-tier model strategy. Each pipeline stage can
use a different model tier to balance cost and capability.

**3-Tier Model Strategy:**

| Tier        | Model  | Cost (per MTok) | Used For                            |
| ----------- | ------ | --------------- | ----------------------------------- |
| **Heavy**   | Opus   | $5/$25          | Planning, development, validation   |
| **Default** | Sonnet | $3/$15          | Global default (no override needed) |
| **Light**   | Haiku  | $1/$5           | Issue pickup, PR create, PR merge   |

Lightweight stages perform structured, template-driven tasks (JSON extraction,
PR template filling, merge flow) that don't require advanced reasoning. Routing
these to Haiku saves ~67% per stage compared to Sonnet.

| Stage              | Default Model | Rationale                          |
| ------------------ | ------------- | ---------------------------------- |
| `issue-pickup`     | `haiku`       | Structured JSON extraction         |
| `feature-planning` | `opus`        | Deep reasoning for plan design     |
| `feature-dev`      | `opus`        | Complex code generation            |
| `feature-validate` | `opus`        | Quality review and test validation |
| `pr-create`        | `haiku`       | Template-based PR description      |
| `pr-merge`         | `haiku`       | Review/merge flow management       |

**Example:**

```yaml
pipeline:
  stage_models:
    issue-pickup: haiku
    feature-planning: opus
    feature-dev: opus
    feature-validate: opus
    pr-create: haiku
    pr-merge: haiku
```

**Environment overrides (per-stage):**

```bash
# Override a single stage model
export NIGHTGAUGE_PIPELINE_STAGE_MODEL_ISSUE_PICKUP=sonnet
export NIGHTGAUGE_PIPELINE_STAGE_MODEL_PR_CREATE=sonnet
```

**Used by:**

- All pipeline stages - Model selection for CLI invocation
- `skillRunner.ts` - Passes `--model` flag to Claude CLI when model differs from
  the default (sonnet)
- Codex adapter - Translates shared tiers to OpenAI models before invoking
  Codex CLI:
  `haiku` → `gpt-5.4-mini`, `sonnet` → configured `ui.core.codex.model`
  (default `gpt-5.4`), `opus` → `gpt-5.5` (mapping owned by the SDK's canonical
  `codexModelRegistry`, #4018)

**Cross-model cache-loss rationale (issue #3806):**

The prompt cache is **model-specific**, so a model change across an adjacent-stage
boundary forecloses any reuse of a cacheable prefix. The default mapping above is
deliberately **cache-optimal**: the three reasoning stages
(`feature-planning` → `feature-dev` → `feature-validate`) are **contiguous
same-model**, so there are **no interior model switches**; the only two switches
sit at the lightweight↔reasoning seams (`issue-pickup → feature-planning` and
`feature-validate → pr-create`), where the cheaper model is genuinely the right
tool and is also cheaper today (cache-read bills at 0.1× base input, so forcing
the seam to one model only wins once cross-stage prefix reuse exists). The
recommended target is therefore **no change** to this mapping. Measure the
boundaries with `scripts/measure-cache-boundary-loss.sh` before changing it.
Any mapping change that affects stage **quality** is gated on the per-stage
routing re-validation spike (#3818).

**See also:**

- Issue #638 - Pipeline token efficiency
- Issue #707 - Per-stage model routing
- Issue #725 - Haiku model routing for lightweight stages
- Issue #3806 - Minimize cross-model prompt-cache loss across stage boundaries

---

#### pipeline.retry

Automatic retry configuration for transient API errors during pipeline
execution.

When a stage fails due to an API error (500, 502, 503, 504), Nightgauge can
automatically retry with exponential backoff. This improves pipeline reliability
by recovering from temporary service outages without manual intervention.

| Option                 | Type    | Default                | Description                                       |
| ---------------------- | ------- | ---------------------- | ------------------------------------------------- |
| `max_auto_attempts`    | integer | `3`                    | Maximum automatic retry attempts per stage        |
| `backoff_multiplier`   | number  | `2`                    | Exponential backoff multiplier                    |
| `initial_delay_ms`     | integer | `100`                  | Initial retry delay in milliseconds               |
| `retryable_api_errors` | array   | `[500, 502, 503, 504]` | HTTP status codes that trigger automatic retry    |
| `rate_limit_delay_ms`  | integer | `60000`                | Delay for rate limit errors (429) in milliseconds |

**Retry Behavior:**

- **Transient errors only**: Only retries API errors (5xx status codes)
- **Exponential backoff**: Delays increase exponentially: 100ms, 200ms, 400ms,
  800ms, ...
- **Hard cap**: Maximum delay capped at 30 seconds to prevent infinite waits
- **Separate retry tracking**: Automatic retries tracked separately from manual
  retries
- **Circuit breaker**: After max attempts exhausted, user can still manually
  retry via "Retry Stage" button

**Backoff Formula:**

```
delay = min(initial_delay_ms * (backoff_multiplier ^ attempt), 30000ms)
```

With default config (initial=100ms, multiplier=2, max_attempts=3):

- Attempt 1: 100ms delay
- Attempt 2: 200ms delay
- Attempt 3: 400ms delay
- Total wait time: 700ms

**Example:**

```yaml
pipeline:
  retry:
    max_auto_attempts: 3
    backoff_multiplier: 2
    initial_delay_ms: 100
    retryable_api_errors: [500, 502, 503, 504]
    rate_limit_delay_ms: 60000
```

**Environment overrides:**

```bash
export NIGHTGAUGE_RETRY_MAX_AUTO_ATTEMPTS=5
export NIGHTGAUGE_RETRY_BACKOFF_MULTIPLIER=1.5
export NIGHTGAUGE_RETRY_INITIAL_DELAY_MS=200
```

**Used by:**

- All pipeline stages - Automatic retry on API errors

**See also:**

- Issue #79 - API error retry with exponential backoff
- [docs/ARCHITECTURE.md](./ARCHITECTURE.md#deterministic-vs-probabilistic-architecture) -
  Retry implementation architecture

---

#### pipeline.phase_timeouts

Per-phase timeout and stale-output detection for pipeline stages (Issue #1187).
Each pipeline stage is composed of multiple _phases_ (e.g., "read-context",
"implement-core", "run-tests"). This section configures wall-clock hard timeouts
and stale-output detection at the individual phase level, complementing the
stage-level `stall_thresholds` above.

| Option               | Type    | Default  | Description                                             |
| -------------------- | ------- | -------- | ------------------------------------------------------- |
| `enabled`            | boolean | `true`   | Master enable/disable for phase timeouts                |
| `stale_detection_ms` | integer | `300000` | Milliseconds with no stdout/stderr before stale event   |
| `max_auto_retries`   | integer | `2`      | Maximum automatic retries before escalating to the user |
| `defaults`           | object  | -        | Default timeout (ms) per phase-type classification      |
| `per_stage`          | object  | -        | Optional per-stage, per-phase timeout overrides         |

**Default timeouts by phase type:**

| Phase Type       | Default (ms) | Human-Readable | Description                      |
| ---------------- | ------------ | -------------- | -------------------------------- |
| `context`        | `120000`     | 2 minutes      | Context-loading phases           |
| `implementation` | `600000`     | 10 minutes     | Implementation / code-gen phases |
| `testing`        | `480000`     | 8 minutes      | Test and validation phases       |
| `context_write`  | `180000`     | 3 minutes      | Context write / summarize phases |

**Phase-type classification:**

Phase names are classified into a type using keyword heuristics. The first
matching keyword determines the type:

| Phase Type       | Keywords                                                                                |
| ---------------- | --------------------------------------------------------------------------------------- |
| `context`        | `read`, `load`, `context`, `discover`, `scan`, `gather`, `fetch`, `collect`             |
| `implementation` | `implement`, `write`, `create`, `build`, `generate`, `develop`, `code`, `scaffold`      |
| `testing`        | `test`, `validate`, `verify`, `check`, `lint`, `type-check`, `build-check`, `run-tests` |
| `context_write`  | `write-context`, `save`, `output`, `record`, `persist`, `emit`, `summarize`             |

If no keywords match, the phase defaults to the `implementation` type.

**Timeout behavior:**

- **Stale detection** fires when a phase produces no stdout or stderr output for
  `stale_detection_ms` milliseconds. This catches phases that silently hang
  without producing any progress output.
- **Hard timeout** fires when the total wall-clock time of a phase exceeds its
  per-type limit (from `defaults` or `per_stage` overrides). This is a safety
  net ensuring no phase runs indefinitely.
- Both mechanisms are **disabled** when `enabled: false`.
- When a timeout or stale event fires, the system retries up to
  `max_auto_retries` times before escalating (e.g., prompting the user or
  cancelling the phase).

**Example:**

```yaml
pipeline:
  phase_timeouts:
    enabled: true
    stale_detection_ms: 300000 # 5 min no output → stale
    max_auto_retries: 2
    defaults:
      context: 120000 # 2 min for context-loading phases
      implementation: 600000 # 10 min for implementation phases
      testing: 480000 # 8 min for test/validation phases
      context_write: 180000 # 3 min for context write phases
    per_stage: # Optional per-stage, per-phase overrides
      feature-dev:
        implement-core: 900000 # 15 min override for heavy implementation
```

**Environment overrides:**

```bash
export NIGHTGAUGE_PIPELINE_PHASE_TIMEOUTS_ENABLED=false
```

**Relationship to other pipeline settings:**

- **`pipeline.stall_thresholds`** — Stage-level stall warnings (coarse). Phase
  timeouts provide finer-grained, per-phase detection within a stage.
- **`pipeline.retry`** — API-error retry with backoff. Phase timeout retries
  handle a different failure mode (hung or slow phases, not HTTP errors).

**Used by:**

- `HeadlessOrchestrator` — Monitors phase execution wall-clock and output
- All pipeline stages — Phase-level timeout enforcement

**See also:**

- Issue #1187 — Pipeline cancel & phase timeouts

---

#### pipeline.max_backtracks

Maximum number of backward stage transitions (backtracks) allowed per pipeline
run (Issue #1342). When a downstream stage (e.g., feature-dev) emits a blocking
feedback signal with a `backtrack_target_stage`, the orchestrator can rewind to
the target stage and re-run from there. When the limit is exceeded, blocking
signals are surfaced to the user but do not trigger backtrack.

An oscillation guard independently blocks the same from→to edge from being
traversed twice, regardless of remaining backtrack quota.

| Option           | Type    | Default | Range | Description                         |
| ---------------- | ------- | ------- | ----- | ----------------------------------- |
| `max_backtracks` | integer | `1`     | 0–5   | Maximum backtracks per pipeline run |

Setting to `0` completely disables backtracking.

```yaml
pipeline:
  max_backtracks: 2
```

**Environment override:**

```bash
export NIGHTGAUGE_PIPELINE_MAX_BACKTRACKS=0 # Disable backtracking
```

**Used by:**

- `HeadlessOrchestrator` — Evaluates feedback signals after context validation
- `PipelineOrchestrator` (SDK) — Same logic for programmatic execution

**See also:**

- Issue #1341 — PipelineFeedbackSignal schema
- Issue #1342 — Orchestrator Backtrack Engine

---

#### model_routing.max_escalations_per_stage

Maximum number of model escalations allowed per stage per pipeline run (Issue
#1343). When `MODEL_ESCALATION_NEEDED` is emitted, the orchestrator retries the
same stage with the next model in the fixed escalation path
(`haiku → sonnet → opus`). When the per-stage limit is exceeded, the signal is
surfaced to the user but no escalation occurs.

| Option                      | Type    | Default | Range | Description                             |
| --------------------------- | ------- | ------- | ----- | --------------------------------------- |
| `max_escalations_per_stage` | integer | `1`     | 0–3   | Max model escalations per stage per run |

Setting to `0` completely disables model escalation.

```yaml
model_routing:
  max_escalations_per_stage: 2
```

**Environment override:**

```bash
export NIGHTGAUGE_PIPELINE_MAX_ESCALATIONS_PER_STAGE=0 # Disable escalation
```

**Used by:**

- `HeadlessOrchestrator` — Evaluates `MODEL_ESCALATION_NEEDED` signals after
  stage completion
- `PipelineOrchestrator` (SDK) — Same logic for programmatic execution

**See also:**

- Issue #1343 — Dynamic Model Escalation Engine
- [FEEDBACK_LOOPS.md](FEEDBACK_LOOPS.md) — Full feedback system reference

---

#### pipeline.logs

Pipeline execution log retention settings. Logs are stored in
`.nightgauge/logs/nightgauge-output-*.json` and are NEVER
automatically deleted.

| Option                   | Type    | Default              | Description                                  |
| ------------------------ | ------- | -------------------- | -------------------------------------------- |
| `retain`                 | boolean | `true`               | Enable log retention (always keep logs)      |
| `max_age_days`           | integer | `null` (unlimited)   | Default age threshold for manual cleanup     |
| `max_count`              | integer | `null` (unlimited)   | Default count threshold for manual cleanup   |
| `dir`                    | string  | `".nightgauge/logs"` | Log directory location                       |
| `history_retention_days` | integer | `90`                 | Days to retain execution history JSONL files |

**Default Behavior: Infinite Retention**

By default, all pipeline execution logs are retained indefinitely
(`retain: true`, `max_age_days: null`, `max_count: null`). This supports:

- Post-mortem analysis of failed pipeline runs
- Historical trend analysis (token usage, execution time)
- Debugging complex issues with full execution context

**Manual Cleanup**

Use `scripts/cleanup-logs.sh` to manually clean old logs when desired:

```bash
# Preview cleanup of logs older than 30 days (dry-run, safe)
scripts/cleanup-logs.sh --older-than-days 30

# Actually remove logs older than 30 days
scripts/cleanup-logs.sh --older-than-days 30 --force

# Keep only the newest 100 logs
scripts/cleanup-logs.sh --keep-count 100 --force
```

**Configuration Examples**

Infinite retention (default):

```yaml
pipeline:
  logs:
    retain: true
    max_age_days: null
    max_count: null
```

Age-based retention (keep last 30 days):

```yaml
pipeline:
  logs:
    retain: true
    max_age_days: 30 # Used by cleanup-logs.sh when no CLI flags provided
    max_count: null
```

Count-based retention (keep last 100 runs):

```yaml
pipeline:
  logs:
    retain: true
    max_age_days: null
    max_count: 100 # Used by cleanup-logs.sh when no CLI flags provided
```

**Environment overrides:**

```bash
export NIGHTGAUGE_PIPELINE_LOGS_RETAIN=true
export NIGHTGAUGE_PIPELINE_LOGS_MAX_AGE_DAYS=30
export NIGHTGAUGE_PIPELINE_LOGS_MAX_COUNT=100
export NIGHTGAUGE_PIPELINE_LOGS_DIR=".nightgauge/logs"
```

**Safety Features**

- Logs are never automatically deleted (manual script invocation only)
- `cleanup-logs.sh` defaults to dry-run mode (preview without deleting)
- Requires `--force` flag to actually delete files
- Script refuses to run if `retain: false` (fail securely)
- Preserves `.gitkeep` file in log directory

**Used by:**

- `scripts/cleanup-logs.sh` - Manual log cleanup script
- All pipeline skills - Write execution logs to configured directory

#### pipeline.targeted_tests

Controls whether the feature-validate stage runs only tests corresponding to
changed source files instead of the full test suite. This reduces validation
cost for localized changes by mapping source files to test files using naming
conventions (e.g., `src/foo/bar.ts` → `tests/foo/bar.test.ts`).

| Value    | Behavior                                                                             |
| -------- | ------------------------------------------------------------------------------------ |
| `auto`   | Use targeted tests when mapping yields candidates; fall back to full suite otherwise |
| `always` | Always attempt targeted selection (still falls back if no candidates found)          |
| `never`  | Always run the full test suite                                                       |

**Default:** `auto`

**Cross-cutting detection:** When the change set exceeds 10 files or spans more
than 3 top-level directories, the change is classified as cross-cutting and the
full suite runs (unless mode is `always`).

**Example:**

```yaml
pipeline:
  targeted_tests: auto # default — auto-select when possible
  # targeted_tests: never # opt-out — always run full suite
```

**Environment override:** `NIGHTGAUGE_PIPELINE_TARGETED_TESTS`

**Used by:**

- `feature-validate` skill (SKILL.md Phase 2.0.5 — bash path)
- `PTCValidationRunner.buildPrompt()` (PTC path)

#### pipeline.gemini_context

Controls generation of `GEMINI.md` context file for Gemini CLI adapters. When
the execution adapter is `gemini` or `gemini-sdk`, a `GEMINI.md` file is
generated in the project root before each stage execution. This provides Gemini
CLI with project context (analogous to how Claude reads `CLAUDE.md`).

| Field                  | Type    | Default | Description                                |
| ---------------------- | ------- | ------- | ------------------------------------------ |
| `enabled`              | boolean | `true`  | Enable GEMINI.md generation                |
| `include_standards`    | boolean | `true`  | Include coding standards section           |
| `include_git_workflow` | boolean | `true`  | Include git workflow section               |
| `custom_sections`      | array   | `[]`    | Additional sections to append to GEMINI.md |

Each entry in `custom_sections` has:

| Field     | Type   | Description                 |
| --------- | ------ | --------------------------- |
| `heading` | string | Section heading in markdown |
| `content` | string | Section body content        |

**Example:**

```yaml
pipeline:
  gemini_context:
    enabled: true
    include_standards: true
    include_git_workflow: true
    custom_sections:
      - heading: "API Guidelines"
        content: "Use RESTful conventions. All endpoints require auth."
```

**Notes:**

- `GEMINI.md` is a generated artifact — it is gitignored and cleaned up after
  each stage
- When disabled, no file is generated even for Gemini adapters
- Missing standards files are handled gracefully (sections omitted)

---

### model_routing

Model routing configuration controls how pipeline stages select AI models. This
is a cross-cutting concern placed at the top level (alongside `pipeline`,
`routing`) because it affects how `pipeline.stage_models` are interpreted.

| Option                  | Type   | Default                                      | Description                                                                                                                                       |
| ----------------------- | ------ | -------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `mode`                  | enum   | `"automatic"`                                | Model selection strategy (see below)                                                                                                              |
| `complexity_thresholds` | object | -                                            | Score boundaries for auto model tier selection                                                                                                    |
| `minimum_model`         | object | -                                            | Per-stage model floor (auto cannot go below)                                                                                                      |
| `confidence_threshold`  | number | `0.7`                                        | Min confidence for auto-selection (0.0-1.0)                                                                                                       |
| `stage_efforts`         | object | planning: medium, dev: medium, validate: low | Per-stage Claude effort (`low\|medium\|high`)                                                                                                     |
| `effort_auto`           | bool   | `true`                                       | Auto-derive effort from stage + complexity (automatic/hybrid)                                                                                     |
| `default_effort`        | enum   | -                                            | Default effort for all stages when the active model supports it (`low\|medium\|high`). Overridden by `stage_efforts`. Silently ignored for Haiku. |

**Model Routing Modes:**

| Mode        | Behavior                                                            |
| ----------- | ------------------------------------------------------------------- |
| `manual`    | Static per-stage mapping (legacy behavior)                          |
| `automatic` | AutoModelSelector (#730) determines model for every stage (default) |
| `hybrid`    | AutoModelSelector runs, but explicit per-stage config overrides win |

In all modes, environment variable overrides
(`NIGHTGAUGE_PIPELINE_STAGE_MODEL_*`) take highest priority.

**Complexity Thresholds:**

| Option       | Type   | Default | Description                            |
| ------------ | ------ | ------- | -------------------------------------- |
| `haiku_max`  | number | `3`     | Max complexity score for Haiku (0-10)  |
| `sonnet_max` | number | `6`     | Max complexity score for Sonnet (0-10) |

Scores above `sonnet_max` route to Opus.

**Mode Behavior with `getStageModel()`:**

```
manual mode:     env var > config stage_models > DEFAULT_STAGE_MODELS
automatic mode:  env var > undefined (defer to AutoModelSelector)
hybrid mode:     env var > config stage_models override > undefined (defer)
```

Returning `undefined` signals "use AutoModelSelector" to the caller
(skillRunner.ts). In manual mode, `undefined` is never returned — it always
falls back to `DEFAULT_STAGE_MODELS`.

**Effort Resolution (`--effort`):**

When using the Claude adapter, effort is resolved with this precedence:

```
1. Environment variable    NIGHTGAUGE_PIPELINE_STAGE_EFFORT_*         -> highest priority
2. Config stage override   model_routing.stage_efforts.<stage>
3. Per-model default       model_routing.default_effort                    (env: NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT)
4. Manual mode default     DEFAULT_STAGE_EFFORTS (medium/medium/low for planning/dev/validate)
5. Auto-derive (optional)  model_routing.effort_auto + automatic|hybrid mode
6. Omit --effort           default Claude behavior
```

> **Model capability guard (Issue #1235):** `--effort` is only passed to Claude
> Code when the active model supports extended thinking. Sonnet, Opus, and
> Fable support `--effort`; Haiku does not. When a Haiku stage resolves an
> effort value it is silently dropped. The supported-model list is defined in
> `EFFORT_SUPPORTING_MODELS` (`incrediConfig.ts`) and should be updated as new
> models gain support. Valid levels: `low`, `medium`, `high`, `xhigh`.

> **Fable conformance (#73):** before an effort value reaches a Fable run it is
> conformed to Anthropic's published guidance (`conformEffortForFable`): an
> explicit `low`/`medium` is floored at `high` (Fable's own server-side default
> — Sonnet-era config must not downgrade a frontier run), a router-selected
> Fable stage (only reachable on L/XL planning/dev) gets `xhigh`, and a
> deliberate Fable pin with no explicit effort omits the flag so the server
> default applies. Coercions are logged on the stage's stderr stream.

> **Note (Issue #944):** In manual mode, `DEFAULT_STAGE_EFFORTS` provides
> sensible defaults (`medium` for planning/dev, `low` for validate). Lightweight
> stages (`issue-pickup`, `pr-create`, `pr-merge`) omit `--effort` by default.
> Override any stage via `model_routing.stage_efforts` or the corresponding
> environment variable.

Deterministic auto-derive rules:

- Lightweight stages (`issue-pickup`, `pr-create`, `pr-merge`) -> `low`
- Non-lightweight `XS`/`S` -> `low`
- Non-lightweight `M` -> `medium`
- Non-lightweight `L`/`XL` -> `high`

**Example — Automatic mode (default, zero config change):**

```yaml
# No model_routing section needed — shipped defaults apply automatically:
# AutoModelSelector picks model per stage based on issue complexity.
# effort_auto derives effort from complexity signal.
# Override thresholds as needed:
model_routing:
  complexity_thresholds:
    haiku_max: 3
    sonnet_max: 6
  confidence_threshold: 0.8
```

**Example — Manual mode (legacy, static mapping):**

```yaml
model_routing:
  mode: manual
  stage_efforts:
    feature-dev: high # Pair with Opus for maximum quality
# In manual mode, models come from pipeline.stage_models / DEFAULT_STAGE_MODELS:
pipeline:
  stage_models:
    feature-dev: opus # Upgrade dev to Opus for complex issues
```

**Example — Hybrid mode (auto + overrides):**

```yaml
model_routing:
  mode: hybrid
  minimum_model:
    feature-dev: sonnet
    feature-validate: sonnet
  stage_efforts:
    feature-dev: high
pipeline:
  stage_models:
    feature-dev: opus # Explicit override — always Opus for dev
```

**Environment overrides:**

```bash
export NIGHTGAUGE_MODEL_ROUTING_MODE=manual  # Revert to legacy static mapping
export NIGHTGAUGE_MODEL_ROUTING_HAIKU_MAX=2
export NIGHTGAUGE_MODEL_ROUTING_SONNET_MAX=5
export NIGHTGAUGE_MODEL_ROUTING_CONFIDENCE_THRESHOLD=0.9
export NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_FEATURE_DEV=sonnet
export NIGHTGAUGE_MODEL_ROUTING_EFFORT_AUTO=true
export NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV=high
```

**Backward compatibility:**

- Missing `model_routing` section defaults to `automatic` mode (changed from
  `manual` in #946)
- Users who want the legacy static-mapping behavior can set `mode: manual`
  explicitly in their config or via `NIGHTGAUGE_MODEL_ROUTING_MODE=manual`
- Existing `pipeline.stage_models` config continues to work unchanged in manual
  and hybrid modes
- Existing `NIGHTGAUGE_PIPELINE_STAGE_MODEL_*` env vars still override in
  all modes

**Used by:**

- All pipeline stages - Model selection for CLI invocation
- `skillRunner.ts` - Passes `--model` and optional `--effort` to Claude CLI

**Resolution Chain:**

The full model resolution chain, implemented in `skillRunner.ts`, is:

```
1.   Environment variable    NIGHTGAUGE_PIPELINE_STAGE_MODEL_*         → highest priority
1.5. Lightweight stage default  issue-pickup, pr-create, pr-merge → haiku   → before AutoModelSelector
2.   Config stage override   pipeline.stage_models.<stage>                  → mode-aware
3.   AutoModelSelector       complexity × stage matrix                      → automatic/hybrid only
4.   Global default          pipeline.default_model                         → fallback
5.   Hardcoded fallback      'sonnet'                                       → final safety net
```

This chain applies identically in all three modes. The `mode` setting controls
whether step 2 returns `undefined` (deferring to step 3) or a static value.

Step 1.5 applies only when no environment variable override is set and the stage
is one of the three lightweight stages (`issue-pickup`, `pr-create`,
`pr-merge`). These stages perform structured, template-driven work (JSON
extraction, PR description filling, merge flow) that does not require advanced
reasoning, so they default to `haiku` without requiring any config entry. The
resolved source is annotated as `stage-default` in `ModelDecision.source`.

**`ModelSource` values** (annotated on `ModelDecision.source` for
observability):

| Value           | Resolved by                                           |
| --------------- | ----------------------------------------------------- |
| `env`           | Step 1 — environment variable override                |
| `stage-default` | Step 1.5 — built-in lightweight stage default (haiku) |
| `config`        | Step 2 — explicit `pipeline.stage_models` entry       |
| `auto`          | Step 3 — AutoModelSelector complexity × stage matrix  |
| `default`       | Steps 4–5 — global default or hardcoded fallback      |

**Migration Guide:**

**Upgrading from manual (pre-#946):** If you previously relied on the implicit
`manual` default and want to preserve static model assignment, add
`mode: manual` to your config:

```yaml
# Preserve legacy manual behavior:
model_routing:
  mode: manual
```

Or set the environment variable: `NIGHTGAUGE_MODEL_ROUTING_MODE=manual`.

**Migrating from expensive static overrides to automatic selection:**

```yaml
# BEFORE (expensive legacy — Opus for planning/dev):
pipeline:
  stage_models:
    issue-pickup: haiku
    feature-planning: opus # Expensive — Sonnet 4.6 handles this well
    feature-dev: opus # Expensive — reserve for L/XL issues
    feature-validate: sonnet
    pr-create: haiku
    pr-merge: haiku

# AFTER (automatic mode — now the default, complexity-driven):
model_routing:
  confidence_threshold: 0.8 # Upgrade model tier if confidence < 0.8
  minimum_model:
    feature-dev: sonnet # Never use haiku for dev, even on XS issues
# Remove pipeline.stage_models — AutoModelSelector handles it
```

For a gradual transition, use `hybrid` mode to keep explicit overrides for
critical stages while letting AutoModelSelector handle the rest:

```yaml
# HYBRID (auto + overrides for critical stages):
model_routing:
  mode: hybrid
  minimum_model:
    feature-dev: sonnet
pipeline:
  stage_models:
    feature-planning: opus # Always Opus for planning
    # Other stages: AutoModelSelector decides
```

**Example — Hybrid mode for feature-validate only (Issue #864):**

To use AutoModelSelector for `feature-validate` while keeping static overrides
for all other stages, remove `feature-validate` from `stage_models` and enable
hybrid mode. AutoModelSelector uses the validate matrix (XS/S → sonnet, M/L/XL →
opus).

```yaml
model_routing:
  mode: hybrid
  confidence_threshold: 0.7
  complexity_thresholds:
    haiku_max: 3
    sonnet_max: 4
pipeline:
  stage_models:
    issue-pickup: haiku
    feature-planning: opus
    feature-dev: opus
    # feature-validate: omitted — defers to AutoModelSelector
    pr-create: haiku
    pr-merge: haiku
```

With this config, `getStageModel('feature-validate')` returns `undefined` in
hybrid mode, causing `resolveModel()` to fall through to AutoModelSelector. For
a size:S issue, the validate matrix returns `sonnet` (confidence 0.9). For a
size:M or larger issue, it returns `opus` (confidence 0.9). All other stages
continue using their explicit static overrides.

**See also:**

- [ARCHITECTURE.md — Automatic Model Selection](./ARCHITECTURE.md#automatic-model-selection)
  for the component design and per-stage matrix
- [CONTEXT_ARCHITECTURE.md](./CONTEXT_ARCHITECTURE.md#model-selection-decision-flow)
  for how decisions are logged in pipeline execution
- Issue #731 - Model routing configuration modes
- Issue #730 - AutoModelSelector service
- Issue #944 - Recommended default config for Sonnet 4.6 era
- Issue #732 - Pipeline integration
- Issue #864 - Hybrid model routing for feature-validate

---

### routing

Complexity-based stage routing settings. Controls when pipeline stages can be
skipped based on issue complexity and change type.

| Option                     | Type    | Default               | Description                                                                                        |
| -------------------------- | ------- | --------------------- | -------------------------------------------------------------------------------------------------- |
| `trivial_max_complexity`   | number  | `2`                   | Max complexity score for trivial path                                                              |
| `extensive_min_complexity` | number  | `5`                   | Min complexity score for extensive path                                                            |
| `force_full_pipeline`      | boolean | `false`               | Always run all stages regardless of routing                                                        |
| `change_rules`             | array   | _(built-in defaults)_ | Customizable fast-track table mapping change types/globs → skip_stages, ci_jobs, route. See below. |

**Routing Paths:**

| Path        | Complexity | Stages Skipped                      | Est. Time |
| ----------- | ---------- | ----------------------------------- | --------- |
| `trivial`   | 1-2        | feature-planning, feature-validate  | ~6 min    |
| `standard`  | 3-5        | (none)                              | ~30 min   |
| `extensive` | 5-8        | (none, uses extended documentation) | ~45 min   |

#### `routing.change_rules` — the fast-track table

`change_rules` is the single, user-customizable source of truth that maps a
change to fast-track behavior (#4125). Each rule is:

| Field            | Type                                 | Description                                                                                                       |
| ---------------- | ------------------------------------ | ----------------------------------------------------------------------------------------------------------------- |
| `name`           | string (required)                    | Unique rule name. A user rule whose name equals a built-in default replaces it.                                   |
| `description`    | string                               | Human-facing note; ignored by matching.                                                                           |
| `globs`          | string[]                             | Gitignore-style patterns (`docs/**`, segment-anchored). Drive **authoritative** post-dev matching (scheduler/CI). |
| `change_types`   | `("code"\|"docs"\|"config")[]`       | Drive **predictive** matching inside `Derive()` at issue-pickup (no diff yet).                                    |
| `skip_stages`    | string[]                             | Stages this rule's match may skip; **replaces** the complexity-derived list.                                      |
| `ci_jobs`        | string[]                             | CI jobs the matched change is allowed to run (consumed by the CI fast-track, #4127).                              |
| `override_route` | `"trivial"\|"standard"\|"extensive"` | Replaces the complexity-derived route when set.                                                                   |

**Predictive vs authoritative.** `routing.Derive()` runs at issue-pickup, before
any diff exists, so it matches rules by `change_types` against the derived
change_type. The real changed files are matched against `globs` post-dev by the
scheduler (#4126) and CI (#4127) — that authoritative layer is the drift-revoke
safety check (a "docs" issue that edited source classifies as source and is not
fast-tracked).

**Precedence (first-match-wins):**

```
risk_high floor  >  force_full_pipeline  >  first matching user rule
                 >  built-in default      >  complexity-derived
```

A matched rule's `skip_stages` **replace** the complexity-derived list; a valid
`override_route` replaces the route. The label-based `risk_high` floor (security/
billing/auth/migration/public-api) and `force_full_pipeline: true` both disable
all skipping.

**Built-in defaults** (apply with zero config):

| Rule              | Globs                                                            | change_types  | Effect                                     |
| ----------------- | ---------------------------------------------------------------- | ------------- | ------------------------------------------ |
| `docs-only`       | `docs/**`, `**/*.md`, `**/*.mdx`, …                              | `[docs]`      | skip planning + validate, route trivial    |
| `config-only`     | `.nightgauge/**`, `.github/**`, `**/*.yaml`, …                   | `[config]`    | skip validate, route trivial               |
| `high-risk-floor` | `**/auth/**`, `**/payments/**`, `**/billing/**`, `migrations/**` | _(glob-only)_ | route extensive, no skips (post-dev guard) |

```yaml
routing:
  trivial_max_complexity: 2
  extensive_min_complexity: 5
  force_full_pipeline: false
  change_rules:
    # Redefine the docs-only default to be less aggressive (name match overrides):
    - name: docs-only
      globs: ["docs/**", "**/*.md"]
      change_types: [docs]
      skip_stages: [feature-validate]
      override_route: trivial
    # A new rule fast-tracking generated code:
    - name: generated
      globs: ["**/*.gen.go"]
      change_types: [code]
      ci_jobs: [build-and-test]
```

Note: `issue-pickup` and `feature-dev` are always enforced — no rule can skip
them. In the scheduler, only `feature-planning` and `feature-validate` are
skippable so every pipeline still produces and merges a PR.

**Complexity-Based Routing:**

In addition to task type profiles, complexity routing may skip additional stages:

1. **Complexity Score** (Fibonacci scale 1-8):
   - Base score from size label (XS=1, S=2, M=3, L=5, XL=8)
   - Adjusted by priority (critical +2, high +1)

2. **Complexity Paths**:
   - Low complexity (≤2) follows trivial path (additional skips)
   - High complexity with critical/high priority uses extensive documentation

**Example:**

```yaml
routing:
  trivial_max_complexity: 2
  extensive_min_complexity: 5
  force_full_pipeline: false
```

**Environment overrides:**

```bash
export NIGHTGAUGE_ROUTING_TRIVIAL_MAX_COMPLEXITY=2
export NIGHTGAUGE_ROUTING_EXTENSIVE_MIN_COMPLEXITY=5
export NIGHTGAUGE_ROUTING_FORCE_FULL_PIPELINE=true
```

**CLI override:**

```bash
# Force full pipeline for a single run (via issue-pickup)
/nightgauge:issue-pickup 42 --full-pipeline
```

**Used by:**

- `/nightgauge-issue-pickup` - Analyzes issue and writes routing to context
  file
- HeadlessOrchestrator - Reads routing and skips stages as configured
- Dashboard - Displays route taken and skipped stages

---

### execution

Execution mode settings for pipeline stage processing. Controls how stages are
executed—either in automated headless mode or interactive conversational mode.

| Option                        | Type   | Default    | Description                        |
| ----------------------------- | ------ | ---------- | ---------------------------------- |
| `default_mode`                | enum   | `headless` | Default mode for Run Stage command |
| `interactive.timeout_minutes` | number | `30`       | Inactivity timeout for interactive |

**Valid mode values:**

- `headless` - Automated execution with token tracking (default)
- `interactive` - Conversational sessions with stdin open

**Example:**

```yaml
execution:
  default_mode: headless
  interactive:
    timeout_minutes: 30
```

**Environment overrides:**

```bash
export NIGHTGAUGE_EXECUTION_DEFAULT_MODE=interactive
export NIGHTGAUGE_EXECUTION_INTERACTIVE_TIMEOUT_MINUTES=60
```

**Used by:**

- VSCode extension Run Stage command - QuickPick defaults to configured mode
- InteractiveOrchestrator - Applies timeout for session termination

**See also:** [INTERACTIVE_MODE.md](./INTERACTIVE_MODE.md) for complete mode
documentation including architecture diagrams, process lifecycles, and feature
compatibility.

---

### enforcement

Pipeline enforcement settings. Controls quality gates and dependency checking.

#### enforcement.dependencies

Dependency enforcement settings. Prevents picking up issues that have unresolved
dependencies, avoiding wasted effort on blocked work.

| Option             | Type    | Default | Description                                    |
| ------------------ | ------- | ------- | ---------------------------------------------- |
| `enabled`          | boolean | `true`  | Enable dependency checking during issue-pickup |
| `mode`             | string  | `warn`  | Enforcement mode: warn, block, or ignore       |
| `check_transitive` | boolean | `false` | Check dependencies of dependencies (A->B->C)   |

**Enforcement Modes:**

| Mode     | Behavior                                             |
| -------- | ---------------------------------------------------- |
| `warn`   | Show warning, ask for confirmation before proceeding |
| `block`  | Refuse to pick up issues with open dependencies      |
| `ignore` | Skip dependency checking entirely                    |

**Example:**

```yaml
enforcement:
  dependencies:
    enabled: true
    mode: warn
    check_transitive: false
```

**Environment overrides:**

```bash
export NIGHTGAUGE_ENFORCEMENT_DEPENDENCIES_ENABLED=true
export NIGHTGAUGE_ENFORCEMENT_DEPENDENCIES_MODE=block
export NIGHTGAUGE_ENFORCEMENT_DEPENDENCIES_CHECK_TRANSITIVE=true
```

**Used by:**

- `/nightgauge-issue-pickup` - Checks dependencies before creating branch
- VSCode extension - Shows dependency warnings in Ready Items tree

**How Dependencies Are Detected:**

Nightgauge uses GitHub's native **blocking relationships** (`blockedBy` /
`blocking` GraphQL fields) to detect dependencies. To create blocking links:

1. In the GitHub UI, use "Add blocked by" in the issue sidebar
2. Or use the `addBlockedBy` GraphQL mutation (see `issue-create` skill)
3. Or use `check-dependencies.sh` to query existing relationships

**IMPORTANT**: Do NOT confuse with `trackedInIssues`/`trackedIssues` (task list
checkboxes `- [ ] #NNN`). Those are a separate GitHub feature for progress
tracking and do NOT create blocking relationships.

Dependencies with `depends-on:N` labels can be added as a future enhancement.

#### Dependency-Aware Auto-Selection (Issue #443)

When `enforcement.dependencies.enabled` is `true`, the issue-pickup
auto-selection algorithm automatically filters out blocked issues from all 7
tiers:

**Filtering Behavior:**

- Issues with any OPEN `blockedBy` entries are considered blocked
- Blocked issues are excluded from auto-selection tiers 1-7
- The `check-dependencies.sh` script queries `blockedBy` via GraphQL and filters
  for entries with `state == "OPEN"`

**"All Issues Blocked" Scenario:**

If all ready issues have open dependencies, the skill provides guidance:

1. Finds the "least blocked" issue (fewest open dependencies)
2. Lists the blocking issues that need resolution first
3. Offers options:
   - Pick up a blocker issue instead
   - Proceed with the blocked issue (if mode is `warn`)
   - Manual issue selection

**Smart Sort Dependency Ordering:**

In "smart" sort mode, issues are now sorted by topological order as the primary
key:

1. **Topological order** — Issues that unblock others appear first
2. **Priority** — P0 > P1 > P2 within same dependency tier
3. **Size** — Smaller issues first (XS > S > M > L > XL)
4. **Age** — Older issues first (creation date)

This ensures that when viewing the Ready Items list, issues are naturally
ordered to maximize workflow efficiency.

**Visual Distinction in Ready View:**

Blocked issues are visually distinguished in the Ready Items tree:

| Visual Element | Unblocked Issue        | Blocked Issue            |
| -------------- | ---------------------- | ------------------------ |
| Icon           | Priority color (P0-P2) | Lock icon (red)          |
| Label          | `#123 - Title`         | `#123 - Title (blocked)` |
| Description    | `[M]`                  | `🔒2 blockers [M]`       |
| Tooltip        | Standard               | Lists blocking issues    |

---

### commands

Override default commands used by skills.

| Option      | Default       | Description        |
| ----------- | ------------- | ------------------ |
| `test`      | auto-detected | Test command       |
| `lint`      | auto-detected | Lint command       |
| `typecheck` | auto-detected | Type check command |
| `format`    | auto-detected | Format command     |
| `build`     | auto-detected | Build command      |

**Example:**

```yaml
commands:
  test: pnpm test
  lint: pnpm lint
  typecheck: pnpm typecheck
  format: pnpm format
  build: pnpm build
```

**Environment overrides:**

```bash
export NIGHTGAUGE_COMMANDS_TEST="pnpm test"
export NIGHTGAUGE_COMMANDS_LINT="pnpm lint"
```

**Used by:**

- `/nightgauge-feature-dev` - Runs tests
- `/nightgauge-feature-validate` - Runs configured test/lint commands
- `/nightgauge-pr-create` - Pre-flight test execution

---

### performance

Build/test performance baselines used by the minimum duration check (Issue #3041).

| Option              | Default       | Description                                                                                                                                               |
| ------------------- | ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `build_time_p10_ms` | auto-detected | p10 (10th percentile) build time in milliseconds. Used to detect suspiciously fast builds that may indicate the deterministic build did not actually run. |

When `feature-validate` completes a build faster than the p10 baseline, it logs
a warning and records `minimum_duration_check.flagged: true` in `validate-{N}.json`.
This does **not** fail validation — it is an informational signal for operators.

Auto-detected defaults (when `build_time_p10_ms` is not set):

| Project type   | Default p10 |
| -------------- | ----------- |
| Node.js / npm  | 15 seconds  |
| Go             | 10 seconds  |
| Flutter / Dart | 20 seconds  |
| Other          | (no check)  |

**Example:**

```yaml
performance:
  build_time_p10_ms: 30000 # 30 seconds — adjust to your repo's typical build time
```

**Environment override:**

```bash
# No env override — use config.yaml for this setting
```

**Used by:**

- `/nightgauge-feature-validate` - Phase 1.5 minimum duration check

---

### validation

PR validation and quality gate settings.

| Option              | Type    | Default    | Description                                                                                       |
| ------------------- | ------- | ---------- | ------------------------------------------------------------------------------------------------- |
| `require_tests`     | boolean | `true`     | Require tests for PRs                                                                             |
| `require_changelog` | boolean | `false`    | Require changelog entry for PRs                                                                   |
| `max_files_changed` | number  | `20`       | Warn if PR changes more files                                                                     |
| `max_lines_changed` | number  | `500`      | Warn if PR changes more lines                                                                     |
| `dead_code`         | string  | `"gate"`   | Dead code gating: `"gate"` (fail on current-issue dead code), `"warn"` (log only), `"off"` (skip) |
| `integration_tests` | string  | `"strict"` | Integration-test gate (#2909): `"strict"`, `"best_effort"`, `"off"` — see description below       |
| `mobile_mcp_tests`  | string  | `"strict"` | Mobile-mcp E2E gate (#24): `"strict"`, `"best_effort"`, `"skip"` — see description below          |
| `verify_ui_tests`   | string  | `"strict"` | Web UI verification gate (#4193): `"strict"`, `"best_effort"`, `"skip"` — see description below   |

**`integration_tests` mode (Issue #2909):**

- `"strict"` (default): if CI declares integration tests (via
  `test:integration` npm script, a CI workflow step containing `integration`,
  or a `tests/integration/` directory), they must actually execute locally.
  Environmental failures (docker daemon unavailable, postgres unreachable,
  missing env vars) fail the stage with `validation_status: "failed"` — no PR
  is created.
- `"best_effort"`: attempt to run; record a warning if services are
  unavailable but allow PR creation. Legacy pre-#2909 behavior.
- `"off"`: skip the integration-test gate entirely.

See
[PIPELINE_EXECUTION.md](PIPELINE_EXECUTION.md#integration-test-strict-gate-issue-2909)
for the rationale and classification rules.

**`mobile_mcp_tests` mode (Issue #24):**

Controls the agent-driven mobile-mcp E2E phase (feature-validate Phase 2.4),
which builds the debug APK, boots the Android emulator, installs the app, and
drives the `test/mobile_mcp/specs/*.md` specs via the mobile-mcp MCP server.
The phase is skipped with zero overhead when `test/mobile_mcp/specs/` has no
runnable specs, or when the `flutter`/`adb`/`emulator` toolchain is unavailable
(a missing toolchain is an environment gap, never a test failure).

- `"strict"` (default): a failing or erroring spec sets
  `validation_status: "failed"` with `errorCategory: "mobile-mcp-tests-failed"`
  — no PR is created. A failed debug-APK build sets
  `errorCategory: "mobile-apk-build-failed"`.
- `"best_effort"`: run the specs but record results as a warning without
  blocking PR creation.
- `"skip"`: skip the phase entirely.

Spec results (including screenshot evidence paths) are written to the
`mobile_mcp` block of `validate-{N}.json`; `pr-create` reads that block to
attach screenshots to the PR body.

**`verify_ui_tests` mode (Issue #4193):**

Controls the browser-driven web UI verification gate (feature-validate Phase
2.45), the web counterpart to `mobile_mcp_tests`. Trigger detection is fully
deterministic — `nightgauge ci classify-ui-surface` decides whether the
diff touches frontend code in a UI-bearing repo (dashboard, acme-site,
acme-web, flutter web), never an LLM judgment. The phase is skipped with zero
overhead when the diff is not UI-relevant (docs-only/config-only/non-UI
source), preserving fast-track economics. When the diff IS UI-relevant but no
`nightgauge-verify-ui` flow is registered for the repo, the phase records
an **explicit skip reason** — never a silent pass — so the coverage gap stays
visible.

- `"strict"` (default): a failed flow step, a new browser console error, or an
  exceeded Core Web Vitals budget sets
  `validation_status: "failed"` with `errorCategory: "verify-ui-gate-failed"`
  — no PR is created.
- `"best_effort"`: run the flow but record results as a warning without
  blocking PR creation.
- `"skip"`: skip the phase entirely.

Optional Core Web Vitals budget (absent by default — vitals are reported, not
enforced):

```yaml
validation:
  verify_ui:
    web_vitals_budget:
      lcp_ms: 2500
      cls: 0.1
```

Flow results (status, per-step console-error deltas, Core Web Vitals) are
written to the `verify_ui` block of `validate-{N}.json`.

**Example:**

```yaml
validation:
  require_tests: true
  require_changelog: true
  max_files_changed: 15
  max_lines_changed: 400
  dead_code: gate
  integration_tests: strict
  mobile_mcp_tests: strict
  verify_ui_tests: strict
```

**Environment overrides:**

```bash
export NIGHTGAUGE_VALIDATION_REQUIRE_TESTS=true
export NIGHTGAUGE_VALIDATION_REQUIRE_CHANGELOG=true
export NIGHTGAUGE_VALIDATION_MAX_FILES_CHANGED=15
export NIGHTGAUGE_VALIDATION_MAX_LINES_CHANGED=400
export NIGHTGAUGE_VALIDATION_DEAD_CODE="warn"
export NIGHTGAUGE_VALIDATION_INTEGRATION_TESTS="strict"
export NIGHTGAUGE_VALIDATION_MOBILE_MCP_TESTS="strict"
export NIGHTGAUGE_VALIDATION_VERIFY_UI_TESTS="strict"
```

**Used by:**

- `/nightgauge-pr-create` - Validates before creating PR
- `/nightgauge-feature-validate` - Dead code gating (Phase 1.6),
  integration-test strict gate (Phase 2.1), web UI verification gate (Phase 2.45)

---

### sanitization

Prompt injection sanitization and firewall settings. Controls destructive
command detection, allowlist/blocklist patterns, and directory-scoped bypass.

| Option             | Type     | Default     | Description                                                             |
| ------------------ | -------- | ----------- | ----------------------------------------------------------------------- |
| `enabled`          | boolean  | `true`      | Enable output sanitization                                              |
| `mode`             | string   | `warn`      | Firewall mode: `warn` (log + allow), `block` (reject), `disabled`       |
| `sanitize_input`   | boolean  | `false`     | Enable input (prompt) sanitization                                      |
| `logging`          | boolean  | `true`      | Log sanitization events to NDJSON log                                   |
| `warn_only`        | boolean  | `false`     | **Deprecated** — use `mode` instead. `true` → `warn`, `false` → `block` |
| `allowlist`        | string[] | `[]`        | Regex patterns for commands to bypass sanitization                      |
| `blocklist`        | string[] | `[]`        | Additional regex patterns to block                                      |
| `safe_directories` | string[] | (see below) | Project-relative directories safe for `rm` commands                     |

#### sanitization.safe_directories

Directory-scoped firewall bypass (Issue #785). When a `rm -rf` (or `rm -f`)
command targets **only** paths under these directories, it bypasses the
destructive pattern check with audit logging.

**Default directories:**

| Directory        | Purpose               |
| ---------------- | --------------------- |
| `./dist`         | Build output          |
| `./build`        | Build output          |
| `./node_modules` | npm dependencies      |
| `./.next`        | Next.js build cache   |
| `./coverage`     | Test coverage reports |
| `./out`          | Generic output        |
| `./.cache`       | Build/tool caches     |

**Security notes:**

- Paths are resolved to absolute paths using `realpath` (with fallback) to
  prevent `../` traversal attacks.
- Commands targeting paths outside the project root are always blocked.
- Commands targeting the project root itself (`rm -rf .`) are blocked.
- If **any** target in a multi-target `rm` is outside safe directories, the
  entire command is blocked.
- Only `rm` commands with `-r` and/or `-f` flags are evaluated. Other
  destructive patterns (dd, mkfs, etc.) are unaffected.

**Check order:** The safe directory check runs **after** the allowlist (which
has highest priority) but **before** destructive pattern matching.

**Example:**

```yaml
sanitization:
  enabled: true
  mode: warn # warn (default), block, disabled
  safe_directories:
    - ./dist
    - ./build
    - ./node_modules
    - ./.next
    - ./coverage
    - ./out
    - ./.cache
  allowlist:
    - rm -rf ./node_modules
```

> **Mode precedence:** If both `mode` and `warn_only` are set, `mode` takes
> precedence. The `warn_only` field is deprecated and will be removed in a
> future release.
>
> **Scope:** Mode only affects Gate 6 (sanitization pattern matching). Security
> gates (push-to-main, force-push, destructive-git, secret-read, secret-write)
> always block regardless of mode.

**Environment overrides:**

```bash
export NIGHTGAUGE_SKIP_SANITIZATION=1          # Disable all sanitization
export NIGHTGAUGE_SANITIZATION_WARN_ONLY=1     # Warn instead of block
```

**Used by:**

- `workflow-gate.sh` - PreToolUse hook for Bash commands

---

### ralph_loop

Ralph Wiggum Loop self-healing configuration. Enables automatic error correction
during build and test phases in `/nightgauge-feature-validate`.

See [docs/RALPH_LOOP.md](./RALPH_LOOP.md) for complete documentation.

| Option                              | Type     | Default  | Description                               |
| ----------------------------------- | -------- | -------- | ----------------------------------------- |
| `enabled`                           | boolean  | `true`   | Enable/disable Ralph Loop globally        |
| `build`                             | boolean  | `true`   | Enable Ralph Loop for build phase         |
| `tests`                             | boolean  | `true`   | Enable Ralph Loop for tests phase         |
| `lint`                              | boolean  | `false`  | Enable Ralph Loop for lint phase (future) |
| `limits.max_iterations`             | number   | `3`      | Maximum fix attempts per error            |
| `limits.token_budget_per_iteration` | number   | `2000`   | Token budget per iteration                |
| `limits.total_token_budget`         | number   | `10000`  | Total token budget for all iterations     |
| `limits.iteration_timeout_ms`       | number   | `60000`  | Timeout per iteration (ms)                |
| `limits.total_timeout_ms`           | number   | `300000` | Total timeout for all iterations (ms)     |
| `abort_patterns`                    | string[] | -        | Patterns that escalate to human           |

**Example:**

```yaml
ralph_loop:
  enabled: true
  build: true
  tests: true
  lint: false

  limits:
    max_iterations: 3
    token_budget_per_iteration: 2000
    total_token_budget: 10000
    iteration_timeout_ms: 60000
    total_timeout_ms: 300000

  abort_patterns:
    - "Custom error pattern"
```

**Environment overrides:**

```bash
export NIGHTGAUGE_RALPH_LOOP_ENABLED=false
export NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS=5
export NIGHTGAUGE_RALPH_LOOP_TOKEN_BUDGET=3000
export NIGHTGAUGE_RALPH_LOOP_TOTAL_TOKEN_BUDGET=15000
```

**Used by:**

- `/nightgauge-feature-validate` - Auto-fix build and test failures

**Default abort patterns** (always escalate to human):

- `Module not found` / `Cannot find module`
- `ENOENT` / `EACCES` / `EPERM`
- `Permission denied`
- `Out of memory` / `ENOMEM`
- `Segmentation fault`
- `npm ERR! code ERESOLVE`

---

### automations

Workflow automation configuration. Executes actions when issues transition
between statuses. See [docs/AUTOMATIONS.md](./AUTOMATIONS.md) for complete
documentation.

| Option     | Type    | Default                             | Description                    |
| ---------- | ------- | ----------------------------------- | ------------------------------ |
| `enabled`  | boolean | `true`                              | Enable/disable all automations |
| `dry_run`  | boolean | `false`                             | Log without executing          |
| `log_file` | string  | `".nightgauge/logs/automation.log"` | Audit log location             |
| `triggers` | array   | `[]`                                | Array of trigger definitions   |

#### Trigger Definition

Each trigger in the `triggers` array:

| Field     | Type   | Required | Description                                      |
| --------- | ------ | -------- | ------------------------------------------------ |
| `name`    | string | No       | Human-readable name for the trigger              |
| `trigger` | string | Yes      | Status value that activates this trigger         |
| `from`    | string | No       | Only trigger when transitioning from this status |
| `actions` | array  | Yes      | Array of actions to execute                      |

#### Action Types

| Type               | Description                   | Required Fields            |
| ------------------ | ----------------------------- | -------------------------- |
| `post_slack`       | Post message to Slack webhook | `webhook_env`, `message`   |
| `assign_reviewers` | Request reviewers on PR       | `reviewers` (array)        |
| `add_label`        | Add label to issue            | `label`                    |
| `remove_label`     | Remove label from issue       | `label`                    |
| `notify`           | Post comment mentioning users | `users` (array), `message` |
| `run_script`       | Execute custom script         | `script`, optional `args`  |

#### Template Variables

Use `{{variable}}` syntax in messages and script arguments:

| Variable             | Description            | Example                                  |
| -------------------- | ---------------------- | ---------------------------------------- |
| `{{issue.number}}`   | GitHub issue number    | `137`                                    |
| `{{issue.title}}`    | Issue title            | `Add workflow automation`                |
| `{{issue.url}}`      | Full GitHub issue URL  | `https://github.com/org/repo/issues/137` |
| `{{issue.labels}}`   | Comma-separated labels | `type:feature,priority:high`             |
| `{{issue.assignee}}` | Current assignee       | `username`                               |
| `{{status.old}}`     | Previous status value  | `In progress`                            |
| `{{status.new}}`     | New status value       | `In review`                              |
| `{{repo.owner}}`     | Repository owner       | `nightgauge`                             |
| `{{repo.name}}`      | Repository name        | `nightgauge`                             |
| `{{timestamp}}`      | ISO 8601 timestamp     | `2026-02-07T20:00:00Z`                   |

**Example:**

```yaml
automations:
  enabled: true
  dry_run: false
  log_file: ".nightgauge/logs/automation.log"

  triggers:
    - name: "notify-on-review"
      trigger: "status:in-review"
      actions:
        - type: "assign_reviewers"
          reviewers:
            - "@team/platform-reviewers"
        - type: "post_slack"
          webhook_env: "SLACK_WEBHOOK_CODE_REVIEWS"
          message: "{{issue.title}} (#{{issue.number}}) is ready for review"

    - name: "notify-blocked"
      trigger: "status:blocked"
      actions:
        - type: "add_label"
          label: "needs-help"
        - type: "notify"
          users:
            - "@tech-lead"
          message: "Issue #{{issue.number}} is blocked"
```

**Environment overrides:**

```bash
export NIGHTGAUGE_AUTOMATION_ENABLED=false
export NIGHTGAUGE_AUTOMATION_DRY_RUN=true
```

**Used by:**

- `sync-project-status.sh` - Invokes automations after project field updates
- `AutomationService.ts` - VSCode extension integration

---

### complexity_model

Complexity model configuration controls cross-project pattern transfer for the
issue complexity scoring system.

```yaml
complexity_model:
  cross_project:
    enabled: false
    confidence_damping: 0.5
    min_export_confidence: 0.3
```

#### complexity_model.cross_project

Cross-project pattern transfer settings. When enabled, complexity patterns
learned in one repository can be exported and imported into another, allowing
new projects to benefit from existing calibration data.

| Option                  | Type    | Default | Range   | Description                                      |
| ----------------------- | ------- | ------- | ------- | ------------------------------------------------ |
| `enabled`               | boolean | `false` | -       | Enable cross-project pattern import/export       |
| `confidence_damping`    | number  | `0.5`   | 0.0–1.0 | Confidence damping factor for imported patterns  |
| `min_export_confidence` | number  | `0.3`   | 0.0–1.0 | Minimum confidence to include patterns in export |

**Behavior:**

- Imported patterns have their confidence multiplied by `confidence_damping` to
  account for cross-project applicability differences
- Only patterns with confidence at or above `min_export_confidence` are included
  when exporting from a source repository
- Requires explicit opt-in (`enabled: true`) because patterns are
  project-specific by nature

**See also:**

- Issue #1415 — Cross-project pattern transfer

---

### saved-queries

**File**: `.nightgauge/saved-queries.yaml`

Stores saved queries for quick access from VSCode and CLI. Queries can be
created manually or saved from the UI.

See [QUERY_LANGUAGE.md](./QUERY_LANGUAGE.md) for query syntax documentation.

| Option    | Type   | Required | Description                      |
| --------- | ------ | -------- | -------------------------------- |
| `version` | string | Yes      | Schema version (currently "1.0") |
| `queries` | array  | Yes      | Array of saved query objects     |

#### Query Object

| Field         | Type    | Required | Description                        |
| ------------- | ------- | -------- | ---------------------------------- |
| `name`        | string  | Yes      | Unique query name                  |
| `query`       | string  | Yes      | Query expression                   |
| `description` | string  | No       | Optional description               |
| `isBuiltIn`   | boolean | No       | True for built-in queries          |
| `createdAt`   | string  | No       | ISO 8601 creation timestamp        |
| `lastUsedAt`  | string  | No       | ISO 8601 last usage timestamp      |
| `runCount`    | number  | No       | Number of times query has been run |

**Example:**

```yaml
version: "1.0"
queries:
  - name: high-priority-ready
    query: "status:ready AND priority:P0"
    description: "High priority issues ready for pickup"
    createdAt: "2024-01-15T10:00:00Z"
    lastUsedAt: "2024-01-20T14:30:00Z"
    runCount: 5

  - name: my-assigned
    query: "assignee:@me AND status:in-progress"
    description: "Issues assigned to me in progress"
    createdAt: "2024-01-16T09:00:00Z"

  - name: small-bugs
    query: "(size:XS OR size:S) AND type:bug"
    description: "Small bugs for quick wins"
    createdAt: "2024-01-18T11:00:00Z"
```

**VSCode Commands:**

| Command                            | Description                  |
| ---------------------------------- | ---------------------------- |
| `Nightgauge: Query Project Items`  | Run a query                  |
| `Nightgauge: Save Query`           | Save current query           |
| `Nightgauge: Load Saved Query`     | Load and run a saved query   |
| `Nightgauge: Delete Saved Query`   | Delete a saved query         |
| `Nightgauge: Manage Saved Queries` | Import/export/manage queries |

**CLI Commands:**

```bash
# Save a query
npx @nightgauge/sdk query "status:ready" --save "ready-issues"

# List saved queries
npx @nightgauge/sdk query --list

# Run a saved query
npx @nightgauge/sdk query --run "ready-issues"
```

**Used by:**

- VSCode QueryService - Loads and executes queries
- CLI query command - Manages saved queries
- Query Results Tree View - Quick access to saved queries

### epic.summary

Epic summary generation configuration. Controls automatic summary generation
when epics complete (all sub-issues closed).

| Option        | Type    | Default      | Description                                       |
| ------------- | ------- | ------------ | ------------------------------------------------- |
| `enabled`     | boolean | `true`       | Enable/disable epic summary generation            |
| `tier`        | string  | `auto`       | Override tier: `full`, `standard`, `none`, `auto` |
| `summary_dir` | string  | `docs/epics` | Directory for full-tier summary docs              |

**Example:**

```yaml
epic:
  summary:
    # Enable/disable epic summary generation (default: true)
    enabled: true

    # Override tier for all epics: "full", "standard", "none"
    # Default: auto (uses deterministic classifier)
    tier: auto

    # Directory for full-tier summary docs (relative to repo root)
    summary_dir: docs/epics
```

**Tier Values:**

| Value      | Description                                        |
| ---------- | -------------------------------------------------- |
| `auto`     | Use deterministic classifier (labels, body, count) |
| `full`     | Always generate full summary with doc commit       |
| `standard` | Always generate standard summary (comment only)    |
| `none`     | Disable summary generation for all epics           |

When `tier` is `auto`, the classifier uses this priority chain:

1. Label override (`epic:full-summary` → full, `epic:no-summary` → none)
2. "Expected Deliverables" section in epic body → full
3. Sub-issue count: 5+ → full, 3-4 → standard, 1-2 → none

**Environment overrides:**

```bash
export NIGHTGAUGE_EPIC_SUMMARY_ENABLED=false
export NIGHTGAUGE_EPIC_SUMMARY_TIER=full
export NIGHTGAUGE_EPIC_SUMMARY_SUMMARY_DIR=docs/epics
```

**Used by:**

- `/nightgauge-pr-merge` Phase 7.3 - Triggers summary generation on epic
  completion
- `classify-epic-summary-tier.sh` - Reads `enabled` and `tier` config
- `generate-epic-summary.sh` - Reads `summary_dir` for doc file placement

**Related labels:**

| Label               | Purpose                               |
| ------------------- | ------------------------------------- |
| `epic:full-summary` | Force full summary tier for this epic |
| `epic:no-summary`   | Skip summary generation for this epic |

---

## UI Configuration (VSCode-Specific)

UI settings control the VSCode extension's visual behavior. Current `ui.*`
settings are stored through the tiered YAML Settings panel and are portable
between extension installations when placed in project configuration. A small
set of legacy `nightgauge.*` VS Code contributions remains in settings.json;
those entries are compatibility surfaces and are identified explicitly in
their descriptions.

> **Note**: UI settings are separate from behavior settings. Behavior settings
> (like `pr.merge_strategy`) affect pipeline execution and are portable across
> CLI, SDK, and VSCode. UI settings (like notification sounds or output window
> verbosity) only affect the VSCode user interface.

### Behavior vs UI Settings

| Category     | Examples                                        | Portability | Storage                   |
| ------------ | ----------------------------------------------- | ----------- | ------------------------- |
| **Behavior** | `pr.merge_strategy`, `pipeline.auto_fix`        | CLI + SDK   | `.nightgauge/config.yaml` |
| **UI**       | `ui.notifications.sounds`, `ui.output_window.*` | VSCode only | tiered YAML (`ui.*`)      |

**Guidelines:**

- Put settings that affect _what_ the pipeline does in behavior config
- Put settings that affect _how_ VSCode displays information in UI config
- When in doubt, ask: "Would this setting make sense in a headless CI
  environment?"
  - Yes → behavior config
  - No → UI config

### ui.core

Core VSCode extension settings for authentication and paths.

| Option           | Type   | Default                  | Description                                                                                   |
| ---------------- | ------ | ------------------------ | --------------------------------------------------------------------------------------------- |
| `adapter`        | enum   | `"claude"`               | Agentic pipeline adapter: claude, codex (beta), gemini (experimental), copilot (experimental) |
| `auth_provider`  | enum   | `"max"`                  | Authentication provider: max, bedrock, vertex                                                 |
| `default_model`  | enum   | `"sonnet"`               | Default model: sonnet, opus, haiku                                                            |
| `fallback_model` | enum   | _(none)_                 | Fallback model on overload: sonnet, opus, haiku (#626)                                        |
| `context_path`   | string | `".nightgauge/pipeline"` | Directory for pipeline context files                                                          |
| `plans_path`     | string | `".nightgauge/plans"`    | Directory for feature plan files                                                              |

> **Backend Setup**: For detailed setup instructions including IAM policies,
> service accounts, and credential configuration for Bedrock, Vertex, and
> Gemini, see [MULTI_BACKEND_SETUP.md](./MULTI_BACKEND_SETUP.md).

#### Per-stage adapter selection

Different pipeline stages can route to different execution adapters in the
same run. The headless dispatcher consumes `pipeline.stage_adapters` end-to-end
(Issue #3223 / Epic #3212), so a mixed-adapter pipeline like Claude planning →
Gemini development → Claude PR creation works without further configuration.

**This schema is canonical across all three execution layers (#54)**: the
VSCode dispatcher (full chain below, including the auto-router and the
health-aware fallback walker), the Go scheduler's Go-direct/autonomous path
(per-stage resolution before each dispatch; `--adapter` / `NIGHTGAUGE_ADAPTER`
pins the whole run and skips per-stage overrides), and the SDK CLI (`stage`
command resolves with its stage; `run` uses the global keys). The Go binary's
old API-key auto-detect is gone — an exported `ANTHROPIC_API_KEY` no longer
silently changes which adapter runs (the SDK CLI keeps key-implies-sdk as a
below-config legacy rung for headless CI).

Resolution precedence (highest → lowest):

1. `NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_<STAGE>` env var (uppercased,
   underscored stage name — e.g. `NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV=gemini`)
2. `NIGHTGAUGE_ADAPTER` env var / `--adapter` flag (Go + SDK CLI: the
   per-invocation override, pins every stage)
3. `pipeline.stage_adapters.<stage>` from `.nightgauge/config.yaml`
4. **AutoProviderRouter** (Issue #3230, VSCode layer) — when enabled, picks
   the `(adapter, model)` pair from the set of authenticated adapters using a
   deterministic decision tree weighted by cost, capability, and context
   window. Source recorded as `auto-router`. See
   [How auto-routing works](./ADAPTER_GUIDE.md#how-auto-routing-works) for the
   full decision tree.
5. Global `ui.core.adapter`
6. Hardcoded default (`claude` — the Go/SDK layers map it to their headless/
   SDK flavor)

The resolved adapter and the source step that produced it are recorded on
each per-stage history entry as `adapter` and `adapter_source` for analytics
attribution.

```yaml
ui:
  core:
    adapter: claude # global default — used by stages without an override
pipeline:
  stage_adapters:
    feature-planning: claude
    feature-dev: gemini
    pr-create: claude
    pr-merge: claude

  # Optional: when the resolved adapter's prereq check fails (auth missing,
  # CLI not in PATH, etc.), the dispatcher tries each entry below in order
  # before surfacing a `[stage:adapter-unavailable]` error envelope. Empty
  # or missing means today's behavior (fail immediately on prereq failure).
  adapter_fallback_chain:
    - claude
    - codex

  # Optional: AutoProviderRouter (Issue #3230). When enabled (default true),
  # the resolver consults the SDK router after explicit overrides and before
  # the global `ui.core.adapter` fallback. The router scores authenticated
  # adapters by cost, capability, and context window and abstains when no
  # candidate dominates — falling through to the existing precedence chain.
  auto_router:
    enabled: true
    weights:
      cost: 0.4 # 0..1 — higher → cheaper adapter wins more often
      capability: 0.4 # 0..1 — higher → most-capable adapter wins
      context_window: 0.2 # 0..1 — higher → big-context adapters get an edge
```

##### `pipeline.auto_router` reference

| Key                      | Type      | Default | Description                                                                                           |
| ------------------------ | --------- | ------- | ----------------------------------------------------------------------------------------------------- |
| `enabled`                | `boolean` | `true`  | Master switch. Set `false` to fully bypass the router and rely on the rest of the precedence chain.   |
| `weights.cost`           | `number`  | `0.4`   | Weight applied to the cost sub-score. The router normalises weights so they sum to 1.0 internally.    |
| `weights.capability`     | `number`  | `0.4`   | Weight applied to the capability sub-score (per-stage, per-adapter capability matrix in the SDK).     |
| `weights.context_window` | `number`  | `0.2`   | Weight applied to context-window fit. Saturates at 1.0 once the adapter window covers the active set. |

Examples:

```yaml
# "I want cheapest acceptable" — heavily favour cost
pipeline:
  auto_router:
    enabled: true
    weights: { cost: 0.7, capability: 0.2, context_window: 0.1 }
```

```yaml
# "I want best capability regardless of cost" — capability dominates
pipeline:
  auto_router:
    enabled: true
    weights: { cost: 0.1, capability: 0.8, context_window: 0.1 }
```

The router only acts when `model_routing.mode` is `automatic` or `hybrid`. In
`manual` mode it abstains unconditionally so explicit user picks always win.
In `hybrid` mode it requires the top score to dominate the second by at least
`0.15`; otherwise it abstains so the resolver falls through to the existing
chain.

> **Interactive mode is unaffected.** The interactive dispatcher
> (`runStageSkillInteractive`) intentionally keeps the global adapter lookup
> because the user is steering one stage at a time and selects the adapter
> implicitly via the "Switch Execution Adapter" command.

#### Codex Adapter Settings

When `ui.core.adapter: codex` is selected, Nightgauge uses your local
`codex` CLI login. This path does not require a direct OpenAI API key.
The VS Code settings panel reads available Codex models from your local
`~/.codex/models_cache.json` catalog when present.

| Option                 | Type    | Default     | Description                                                 |
| ---------------------- | ------- | ----------- | ----------------------------------------------------------- |
| `codex.model`          | string  | `"gpt-5.4"` | Default Codex model for `sonnet`-tier stages and fallbacks  |
| `codex.cli_command`    | string  | `"codex"`   | Codex executable name or absolute path                      |
| `codex.cli_args`       | string  | _(none)_    | Optional raw CLI args override; blank uses adapter defaults |
| `codex.resume_enabled` | boolean | `false`     | Enable Codex session resume for resumable stages            |

#### Codex Adapter Environment Variables

| Variable                          | Description                          |
| --------------------------------- | ------------------------------------ |
| `NIGHTGAUGE_CODEX_MODEL`          | Override Codex model                 |
| `NIGHTGAUGE_CODEX_CLI_COMMAND`    | Override Codex binary path           |
| `NIGHTGAUGE_CODEX_CLI_ARGS`       | Override default CLI arguments       |
| `NIGHTGAUGE_CODEX_RESUME_ENABLED` | Enable Codex session resume (`true`) |

#### Gemini Adapter Environment Variables

When using the `gemini` or `gemini-sdk` adapter, the following environment
variables are available:

| Variable                        | Adapter      | Description                                         |
| ------------------------------- | ------------ | --------------------------------------------------- |
| `GEMINI_API_KEY`                | `gemini-sdk` | API key for Gemini SDK adapter (primary)            |
| `GOOGLE_API_KEY`                | `gemini-sdk` | Alternative API key (fallback)                      |
| `GOOGLE_GENAI_USE_VERTEXAI`     | `gemini-sdk` | Use Vertex AI endpoint (`true`/`false`)             |
| `NIGHTGAUGE_GEMINI_MODEL`       | both         | Override Gemini model (default: `gemini-2.5-flash`) |
| `NIGHTGAUGE_GEMINI_CLI_COMMAND` | `gemini`     | Override CLI binary name                            |
| `NIGHTGAUGE_GEMINI_CLI_ARGS`    | `gemini`     | Extra CLI arguments                                 |

#### Gemini VSCode Settings

| Setting                        | Type   | Default              | Description                                |
| ------------------------------ | ------ | -------------------- | ------------------------------------------ |
| `nightgauge.gemini.authMethod` | enum   | `"api-key"`          | Auth method: api-key, google-login, vertex |
| `nightgauge.gemini.model`      | string | `"gemini-2.5-flash"` | Gemini model to use                        |
| `nightgauge.gemini.apiKey`     | string | _(none)_             | API key (stored securely)                  |

#### LM Studio Adapter Environment Variables

LM Studio is chat-completion-only. These settings apply to evaluation, judging,
and summarization; pipeline dispatch rejects this adapter because it cannot edit
files or run tools.

When using the `lm-studio` adapter, the following environment variables are
available:

| Variable                          | Description             | Default                    |
| --------------------------------- | ----------------------- | -------------------------- |
| `NIGHTGAUGE_LM_STUDIO_BASE_URL`   | LM Studio server URL    | `http://localhost:1234/v1` |
| `NIGHTGAUGE_LM_STUDIO_MODEL`      | Model name (required)   | _(none)_                   |
| `NIGHTGAUGE_LM_STUDIO_API_KEY`    | API key (value ignored) | `lm-studio`                |
| `NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS` | Request timeout ms      | `180000`                   |

#### LM Studio VSCode Settings

| Setting                           | Type    | Default                      | Description                           |
| --------------------------------- | ------- | ---------------------------- | ------------------------------------- |
| `nightgauge.lmStudio.baseUrl`     | string  | `"http://localhost:1234/v1"` | LM Studio server URL                  |
| `nightgauge.lmStudio.model`       | string  | _(none)_                     | Model name to use                     |
| `nightgauge.lmStudio.timeoutMs`   | number  | `180000`                     | Request timeout (ms)                  |
| `nightgauge.lmStudio.toolCalling` | boolean | `false`                      | Enable tool calling (model-dependent) |
| `nightgauge.lmStudio.maxTokens`   | number  | `8192`                       | Max tokens per response               |

> **Note**: LM Studio only supports `localhost` connections. Remote LM Studio
> servers are not supported.

**Example:**

```yaml
ui:
  core:
    adapter: lm-studio

lm_studio:
  model: "lmstudio-community/Meta-Llama-3.1-8B-Instruct-GGUF"
  base_url: "http://localhost:1234/v1"
  api_key: "lm-studio"
  timeout_ms: 180000
  max_tokens: 8192
  stream_options:
    include_usage: true
  tool_calling: false
```

> **Setup guide**: See
> [MULTI_BACKEND_SETUP.md](./MULTI_BACKEND_SETUP.md#lm-studio-local-model) for
> full LM Studio server setup, model loading, and troubleshooting.

#### Copilot Adapter Environment Variables

When using the `copilot` adapter, the following environment variables are
available for authentication and configuration:

| Variable                         | Description                                 | Default             |
| -------------------------------- | ------------------------------------------- | ------------------- |
| `GH_TOKEN`                       | GitHub personal access token (primary auth) | _(none)_            |
| `GITHUB_TOKEN`                   | Alternative GitHub token (fallback)         | _(none)_            |
| `COPILOT_GITHUB_TOKEN`           | Copilot-specific token override (fallback)  | _(none)_            |
| `NIGHTGAUGE_COPILOT_CLI_COMMAND` | Override CLI binary name                    | `copilot`           |
| `NIGHTGAUGE_COPILOT_CLI_ARGS`    | Extra CLI arguments (space-separated)       | `--allow-all-tools` |
| `NIGHTGAUGE_COPILOT_MODEL`       | Model override passed to the Copilot CLI    | _(CLI default)_     |

**Authentication**: The adapter cascades through token env vars first (no
subprocess cost), then falls back to `copilot auth status` CLI to verify
existing login.

> **Cost model**: Copilot uses **premium requests** rather than per-token
> billing. Each stage invocation costs one premium request (~$0.04 per call at
> `COPILOT_PREMIUM_REQUEST_COST_USD`). This differs from Claude/Gemini adapters
> which track input/output tokens. See `TokenTracker` for usage details.

#### Copilot VSCode Settings

| Setting                    | Type   | Default  | Description                    |
| -------------------------- | ------ | -------- | ------------------------------ |
| `nightgauge.copilot.model` | string | _(none)_ | Model override for Copilot CLI |

> **Note**: Copilot model names are free-form strings controlled by GitHub
> (e.g., `"gpt-4o"`, `"claude-3.5-sonnet"`). Check `gh copilot --help` for
> available models.

**Example:**

```yaml
ui:
  core:
    adapter: copilot

copilot:
  model: "gpt-4o"
```

> **Prerequisites**: Install GitHub Copilot CLI
> (`npm install -g @github/copilot-cli`) and authenticate with
> `gh auth login` or set `GH_TOKEN`.

**Example:**

```yaml
ui:
  core:
    adapter: claude
    auth_provider: max
    default_model: sonnet
    context_path: .nightgauge/pipeline
    plans_path: .nightgauge/plans
```

---

### ui.dashboard

Dashboard display settings including time savings estimates for ROI
calculations.

| Option                          | Type   | Default | Description                           |
| ------------------------------- | ------ | ------- | ------------------------------------- |
| `time_savings.issue_pickup`     | number | `5`     | Estimated manual minutes for pickup   |
| `time_savings.feature_planning` | number | `30`    | Estimated manual minutes for planning |
| `time_savings.feature_dev`      | number | `120`   | Estimated manual minutes for dev      |
| `time_savings.pr_create`        | number | `10`    | Estimated manual minutes for PR       |
| `time_savings.pr_merge`         | number | `5`     | Estimated manual minutes for merge    |

These values are used to calculate "time saved" metrics in the dashboard. Adjust
based on your team's actual experience with manual development.

**Example:**

```yaml
ui:
  dashboard:
    time_savings:
      issue_pickup: 5
      feature_planning: 30
      feature_dev: 120
      pr_create: 10
      pr_merge: 5
```

---

### ui.output_window

Output window display settings.

| Option             | Type    | Default    | Description                                |
| ------------------ | ------- | ---------- | ------------------------------------------ |
| `auto_open`        | boolean | `true`     | Auto-open output window on pipeline start  |
| `auto_scroll`      | boolean | `true`     | Auto-scroll to latest output               |
| `verbose_level`    | enum    | `"normal"` | Verbosity: minimal, normal, verbose, debug |
| `show_token_usage` | boolean | `true`     | Show real-time token/cost tracking         |
| `word_wrap`        | boolean | `true`     | Wrap long lines in output                  |

**Example:**

```yaml
ui:
  output_window:
    auto_open: true
    auto_scroll: true
    verbose_level: normal
    show_token_usage: true
    word_wrap: true
```

---

### ui.notifications

Notification and sound settings.

| Option                   | Type    | Default   | Description                       |
| ------------------------ | ------- | --------- | --------------------------------- |
| `enabled`                | boolean | `true`    | Master toggle for notifications   |
| `sounds.enabled`         | boolean | `true`    | Enable notification sounds        |
| `sounds.alert`           | enum    | `"Glass"` | Sound for user input needed       |
| `sounds.success`         | enum    | `"Hero"`  | Sound for pipeline completion     |
| `sounds.error`           | enum    | `"Basso"` | Sound for pipeline errors         |
| `sounds.volume`          | number  | `0.5`     | Volume level (0.0-1.0)            |
| `banner_enabled`         | boolean | `true`    | Show VS Code notification banners |
| `dock_bounce_enabled`    | boolean | `true`    | Bounce dock icon on macOS         |
| `respect_do_not_disturb` | boolean | `true`    | Suppress when DND is enabled      |

**Available sounds:**

- Alert: Glass, Ping, Blow, Bottle, Frog, Funk, none
- Success: Hero, Purr, Pop, Submarine, none
- Error: Basso, Sosumi, Morse, Tink, none

**Example:**

```yaml
ui:
  notifications:
    enabled: true
    sounds:
      enabled: true
      alert: Glass
      success: Hero
      error: Basso
      volume: 0.5
    banner_enabled: true
    dock_bounce_enabled: true
    respect_do_not_disturb: true
```

---

### ui.ready_items

Ready Items tree view settings.

| Option              | Type    | Default   | Description                             |
| ------------------- | ------- | --------- | --------------------------------------- |
| `auto_refresh`      | boolean | `false`   | Periodically refresh issue list         |
| `refresh_interval`  | number  | `300`     | Seconds between refreshes (min 60)      |
| `sort_by`           | enum    | `"smart"` | Sort field (see below)                  |
| `sort_direction`    | enum    | `"asc"`   | Sort direction: asc, desc               |
| `filters.priority`  | enum    | `"all"`   | Filter by priority: all, P0, P1, P2, P3 |
| `filters.size`      | enum    | `"all"`   | Filter by size: all, XS, S, M, L, XL    |
| `filters.component` | string  | `"all"`   | Filter by component label               |
| `search_text`       | string  | `""`      | Text search filter                      |
| `show_dependencies` | boolean | `true`    | Show dependency indicators              |

**Sort options:**

- `smart` - Intelligent sorting by dependency graph, priority, then size
- `board` - Match project board order
- `priority` - Sort by priority (P0 first)
- `number` - Sort by issue number
- `size` - Sort by size (XS first)
- `dependencies` - Sort by dependency count

**Example:**

```yaml
ui:
  ready_items:
    auto_refresh: false
    refresh_interval: 300
    sort_by: smart
    sort_direction: asc
    filters:
      priority: all
      size: all
    show_dependencies: true
```

---

### ui.sidebar

Sidebar display settings.

| Option                | Type    | Default | Description                 |
| --------------------- | ------- | ------- | --------------------------- |
| `hide_empty_sections` | boolean | `false` | Hide sections with no items |

**Example:**

```yaml
ui:
  sidebar:
    hide_empty_sections: false
```

---

### ui.pipeline

Pipeline execution UI settings.

| Option                | Type    | Default | Description                                  |
| --------------------- | ------- | ------- | -------------------------------------------- |
| `auto_continue`       | boolean | `true`  | Auto-run next stage on completion            |
| `auto_continue_delay` | number  | `1000`  | Delay in ms before auto-continuing (0-10000) |

**Example:**

```yaml
ui:
  pipeline:
    auto_continue: true
    auto_continue_delay: 1000
```

---

### ui.project_board

Project board display settings.

| Option                   | Type    | Default | Description                            |
| ------------------------ | ------- | ------- | -------------------------------------- |
| `group_by_epic`          | boolean | `true`  | Group issues under parent epic         |
| `default_epic_collapsed` | boolean | `false` | Default collapse state for epic groups |

**Example:**

```yaml
ui:
  project_board:
    group_by_epic: true
    default_epic_collapsed: false
```

---

### ui.warnings

Warning dialogs configuration.

| Option                | Type    | Default | Description                           |
| --------------------- | ------- | ------- | ------------------------------------- |
| `enabled`             | boolean | `true`  | Master toggle for drag warnings       |
| `warn_on_in_progress` | boolean | `true`  | Warn when dragging in-progress issues |
| `warn_on_in_review`   | boolean | `true`  | Warn when dragging in-review issues   |

**Example:**

```yaml
ui:
  warnings:
    enabled: true
    warn_on_in_progress: true
    warn_on_in_review: true
```

---

### ui.plugins

Plugin management settings.

| Option            | Type    | Default                                          | Description                      |
| ----------------- | ------- | ------------------------------------------------ | -------------------------------- |
| `auto_prompt`     | boolean | `true`                                           | Prompt to install Claude plugins |
| `marketplace_url` | string  | `"https://github.com/nightgauge/nightgauge.git"` | Plugin marketplace repository    |

**Example:**

```yaml
ui:
  plugins:
    auto_prompt: true
    marketplace_url: https://github.com/nightgauge/nightgauge.git
```

---

## Example Configurations by Tier

### Tier 2: Global Config Example (Machine Tier)

```yaml
# ~/.nightgauge/config.yaml - User-wide preferences

# Personal PR preferences (follow you across all repos)
pr:
  merge_strategy: squash
  delete_branch: true

# Automation trust level
human_in_the_loop:
  auto_accept_stages: false
  auto_accept_permissions: false
```

### Tier 3: Project Config Example (Team Tier)

```yaml
# .nightgauge/config.yaml - Team/project settings (committed to git)

project:
  number: 10
  owner: nightgauge
  auto_dates: true
  sprint:
    enabled: true
    auto_assign: true
    field_name: "Sprint"

pr:
  reviewers:
    - lead-dev
    - "@org/platform-team"
  auto_merge: false

branch:
  base: main
  protected:
    - main
    - release

pipeline:
  ci_timeout: 600
  auto_fix: true

routing:
  trivial_max_complexity: 2
  extensive_min_complexity: 5

commands:
  test: pnpm test
  lint: pnpm lint
  build: pnpm build

validation:
  require_tests: true
  require_changelog: false
  max_files_changed: 20
  max_lines_changed: 500
```

### Tier 4: Local Config Example (Machine Tier)

```yaml
# .nightgauge/config.local.yaml - Developer overrides (NOT committed)

# Override for local development
pipeline:
  skip:
    tests: true # Skip tests while debugging
    lint: false

# Keep my local branches around while iterating
pr:
  delete_branch: false
```

### Tier 5: Runtime Tier (Ephemeral VSCode State)

The runtime tier is managed automatically by the VSCode extension. Values are
stored in VSCode `globalState` / `workspaceState` (memento) and are never
written to YAML files — they cannot be configured manually.

Keys in the runtime tier include UI state that changes frequently:

- `pipeline.max_concurrent` — concurrency slider toggled in the dashboard
- Enabled-repos selections, last-used pickers, paused/running state

Runtime values are ephemeral: they reset when the extension host restarts and
do not appear in `git status`. This tier exists so that flipping a UI control
never dirties the working tree.

### Tier 6: Environment Variables Example

```bash
# Environment variable overrides (e.g., in CI/CD)
export NIGHTGAUGE_PROJECT_NUMBER=10
export NIGHTGAUGE_PR_ADMIN_MERGE=true
export NIGHTGAUGE_PR_MERGE_STRATEGY=squash
export NIGHTGAUGE_PIPELINE_CI_TIMEOUT=600
export NIGHTGAUGE_PIPELINE_SKIP_TESTS=true
export NIGHTGAUGE_AUTO_ACCEPT_STAGES=true
export NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS=true
```

### Tier 7: CLI Flags Example

```bash
# One-time overrides via CLI (highest priority)
# Note: CLI flag support depends on the specific command

# Force full pipeline for a single run
/nightgauge:issue-pickup 42 --full-pipeline

# Skip validation for a specific run
/nightgauge:feature-validate --skip-e2e
```

---

## Full Example (Project Config)

```yaml
# .nightgauge/config.yaml - Full configuration example

project:
  number: 10
  owner: nightgauge
  auto_dates: true
  sprint:
    enabled: true
    auto_assign: true
    field_name: "Sprint"

pr:
  merge_strategy: squash
  delete_branch: true
  reviewers:
    - lead-dev
  auto_merge: false

branch:
  base: main
  protected:
    - main
    - release

pipeline:
  ci_timeout: 600
  auto_fix: true
  skip:
    tests: false
    lint: false

routing:
  trivial_max_complexity: 2
  extensive_min_complexity: 5
  force_full_pipeline: false

commands:
  test: pnpm test
  lint: pnpm lint
  build: pnpm build

validation:
  require_tests: true
  require_changelog: false
  max_files_changed: 20
  max_lines_changed: 500
```

---

## Implementation Details

### Config Parser

Configuration parsing is implemented in
`claude-plugins/nightgauge/hooks/lib/common.sh`:

- `get_config_value(key.path, default)` - Get string value
- `get_config_bool(key.path, default)` - Get boolean value (normalizes
  true/false/yes/no/1/0)
- `get_config_list(key.path)` - Get array value (returns newline-separated list)

### Bash 3.2 Compatibility

The config parser is compatible with macOS's default Bash 3.2:

- Uses `tr` for case conversion instead of `${var^^}`
- Uses awk for YAML parsing instead of associative arrays
- No external dependencies (no `yq` required)

### Security

- No secrets should be stored in `.nightgauge/config.yaml`
- Use environment variables for sensitive values (tokens, keys)
- Config file should be committed to version control

---

## Troubleshooting

### Config not being read

1. Verify config file exists at `.nightgauge/config.yaml` (or legacy
   `.nightgauge/nightgauge.yaml`)
2. Check YAML syntax:
   `python3 -c "import yaml; yaml.safe_load(open('.nightgauge/config.yaml'))"`
3. Enable debug logging: `NIGHTGAUGE_HOOKS_DEBUG=1`

### Environment variable not working

1. Verify the variable name follows the pattern: `NIGHTGAUGE_KEY_PATH`
2. Check the variable is exported: `export NIGHTGAUGE_PR_ADMIN_MERGE=true`
3. Verify case: environment variables must be UPPERCASE

### Boolean values not parsing

The config parser accepts these boolean values:

- **True**: `true`, `yes`, `1`, `on`
- **False**: `false`, `no`, `0`, `off`

### human_in_the_loop

Controls automatic approval of pipeline prompts and permissions. Allows running
pipelines autonomously when you trust the workflow.

| Option                    | Type     | Default | Description                                           |
| ------------------------- | -------- | ------- | ----------------------------------------------------- |
| `auto_accept_stages`      | boolean  | `false` | Auto-approve all stage gates (e.g., feature-planning) |
| `auto_accept_permissions` | boolean  | `false` | Auto-accept tool/file permission prompts from Claude  |
| `trusted_stages`          | string[] | `[]`    | Optional list of specific stages to auto-accept       |

**Valid stage names:**

- `issue-pickup`
- `feature-planning`
- `feature-dev`
- `feature-validate`
- `pr-create`
- `pr-merge`

**Example:**

```yaml
human_in_the_loop:
  # Auto-approve all stage gates
  auto_accept_stages: false

  # Auto-accept tool permissions
  auto_accept_permissions: false

  # Or trust specific stages only
  trusted_stages:
    - feature-planning
    - feature-validate
```

**Environment overrides:**

```bash
export NIGHTGAUGE_AUTO_ACCEPT_STAGES=true
export NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS=true
```

**Used by:**

- `/nightgauge-feature-planning` - Auto-approves plan without showing
  dialog
- `/nightgauge-feature-validate` - Auto-approves validation gates
- All pipeline skills - Auto-accepts Claude permission prompts for tools/files

**Security notes:**

- Both flags default to `false` (require manual approval)
- Environment variables override config file settings
- `trusted_stages` provides granular control (trust planning but not merge)
- Auto-accept decisions are logged for audit trail
- If config loading fails, auto-accept is disabled (fail-safe)

**When to use:**

- **Development**: Enable `auto_accept_stages` for rapid iteration on trusted
  branches
- **CI/CD**: Set `NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS=true` to run pipelines
  without manual intervention
- **Review-focused**: Trust `feature-planning` and `feature-validate` but
  manually review `pr-create` and `pr-merge`

**First-use warning:**

When you enable auto-accept for the first time, the extension shows a warning
dialog explaining the implications. This warning appears once per workspace and
can be dismissed with "Don't show again".

---

## Workspace Configuration

**File**: `.vscode/nightgauge-workspace.yaml`

Multi-repository workspace configuration enables Nightgauge to coordinate
operations across multiple related repositories in a VSCode workspace.

### Detection Priority

Nightgauge detects workspace type using this priority order:

1. **Explicit**: `.vscode/nightgauge-workspace.yaml` exists →
   multi-workspace mode
2. **Auto-detect**: Multiple VSCode workspace folders with
   `.nightgauge/config.yaml` (or `.nightgauge/nightgauge.yaml`) →
   multi-workspace mode
3. **Fallback**: Single repository mode (existing behavior)

### Schema

```yaml
# Workspace metadata
workspace:
  name: "My Monorepo" # Required: Display name for workspace
  description: "Frontend and backend services" # Optional

# Repositories in workspace (required, non-empty array)
repositories:
  - name: frontend # Required: Unique repository name
    path: ./packages/frontend # Required: Path from workspace root
    role: primary # Optional: primary | secondary | shared

  - name: backend
    path: ./packages/backend
    role: primary

  - name: shared-types
    path: ./packages/types
    role: shared

# Routing configuration (optional)
routing:
  # Label patterns mapped to repository assignments
  patterns:
    "area:frontend": frontend
    "area:backend": backend
    "area:api": backend

  # Default repository when no pattern matches
  default_repository: frontend

# Epic tracking configuration (optional)
epic:
  # Enable tracking epics across multiple repositories
  cross_repo_tracking: true

  # Share milestone tracking across repositories
  shared_milestones: true
```

### Field Reference

#### workspace (required)

| Field                   | Type    | Required | Description                                                                                                                                     |
| ----------------------- | ------- | -------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `name`                  | string  | Yes      | Display name for workspace                                                                                                                      |
| `description`           | string  | No       | Optional workspace description                                                                                                                  |
| `shared_project_number` | integer | No       | GitHub Project number shared by all repos (N:1 topology). When set and `repositories` is empty, the repo list is auto-derived from the project. |

#### Workspace Topologies

**1:1 (default)** — each repo has its own project board:

```yaml
workspace:
  name: "MyApp"

repositories:
  - name: frontend
    path: ./frontend
    project_number: 3
  - name: backend
    path: ./backend
    project_number: 4
```

**N:1** — multiple repos share one project, list auto-derived:

```yaml
workspace:
  name: "MyPlatform"
  shared_project_number: 6 # All repos linked to Project #6

repositories: [] # derived at runtime from GitHub ProjectV2.repositories
```

**N:1** — multiple repos share one project, list explicit:

```yaml
workspace:
  name: "MyPlatform"
  shared_project_number: 6

repositories:
  - name: api
    path: ./api
    project_number: 6
  - name: web
    path: ./web
    project_number: 6
```

> **Note**: Per-repo `.nightgauge/config.yaml` with `project.number` is still
> required for pipeline stages regardless of topology. The workspace manifest
> controls the Repositories view; config.yaml controls pipeline routing.

#### repositories (required)

Array of repository configurations. Each repository must have:

| Field            | Type    | Required | Description                                        |
| ---------------- | ------- | -------- | -------------------------------------------------- |
| `name`           | string  | Yes      | Unique repository name                             |
| `path`           | string  | Yes      | Path from workspace root to repository             |
| `role`           | string  | No       | Role classification (see below)                    |
| `project_number` | integer | No       | GitHub Project number for this repo (N:1 topology) |

**Valid role values**:

- `primary` - Main development repository
- `secondary` - Supporting repository
- `shared` - Shared libraries or types

#### routing (optional)

Configuration for routing issues to specific repositories.

| Field                | Type                   | Required | Description                   |
| -------------------- | ---------------------- | -------- | ----------------------------- |
| `patterns`           | Record<string, string> | No       | Label pattern to repo mapping |
| `default_repository` | string                 | No       | Default repo when no match    |

**Pattern matching**: Labels are matched exactly. For label `area:frontend`, use
pattern `"area:frontend": frontend`.

#### epic (optional)

Configuration for cross-repository epic tracking.

| Field                 | Type    | Required | Description                     |
| --------------------- | ------- | -------- | ------------------------------- |
| `cross_repo_tracking` | boolean | No       | Track epics across repositories |
| `shared_milestones`   | boolean | No       | Share milestones across repos   |

### Examples

#### Monorepo with Multiple Packages

```yaml
workspace:
  name: "MyApp Monorepo"

repositories:
  - name: web
    path: ./apps/web
    role: primary
  - name: api
    path: ./apps/api
    role: primary
  - name: shared
    path: ./packages/shared
    role: shared

routing:
  patterns:
    "area:frontend": web
    "area:backend": api
  default_repository: web
```

#### Multi-Repo Workspace (Different Git Roots)

```yaml
workspace:
  name: "MyApp Multi-Repo"
  description: "Frontend and backend in separate repositories"

repositories:
  - name: frontend
    path: ../myapp-frontend
    role: primary
  - name: backend
    path: ../myapp-backend
    role: primary

epic:
  cross_repo_tracking: true
  shared_milestones: true
```

### Getting Started

1. **Copy template**:

   ```bash
   cp .vscode/nightgauge-workspace.yaml.template .vscode/nightgauge-workspace.yaml
   ```

2. **Edit configuration**:
   - Set `workspace.name` to your workspace name
   - Add each repository with unique name and path
   - Configure routing patterns (optional)
   - Enable epic tracking (optional)

3. **Validate configuration**:
   - Open workspace in VSCode
   - Check VSCode Output panel for Nightgauge logs
   - Workspace detection logs appear on extension activation

### Validation Rules

Nightgauge validates workspace configuration on load:

- `workspace.name` must be non-empty string
- `repositories` must be non-empty array
- Each repository must have unique `name`
- Each repository must have valid `path`
- `role` must be one of: `primary`, `secondary`, `shared` (if specified)
- `patterns` must map strings to strings (if specified)

**Invalid configurations** fail with clear error messages in the Output panel.

### Troubleshooting

#### Workspace not detected

1. Check file exists: `.vscode/nightgauge-workspace.yaml`
2. Validate YAML syntax (use a YAML linter)
3. Check VSCode Output panel (Nightgauge channel) for errors
4. Ensure `workspace.name` and `repositories` are present

#### Auto-detection not working

Auto-detection requires:

- Multiple workspace folders (File → Add Folder to Workspace)
- Each folder contains `.nightgauge/config.yaml` (or
  `.nightgauge/nightgauge.yaml`)
- At least 2 folders meet both criteria

#### Repository paths not resolving

- Paths are relative to workspace root (where `.vscode/` is located)
- Use `./` prefix for subdirectories: `./packages/frontend`
- Use `../` for parent directories: `../sibling-repo`
- Absolute paths are not supported

### Environment Variables

Workspace configuration cannot be overridden by environment variables. The
configuration file is the single source of truth for workspace structure.

### Future Enhancements

The workspace configuration schema is designed for future features:

- **Cross-repository pipelines**: Coordinate changes across multiple repos
- **Dependency tracking**: Understand inter-repository dependencies
- **Routing automation**: Auto-assign issues based on label patterns
- **Epic coordination**: Track feature development spanning multiple repos

---

## Discord Notifications

Send live-updating pipeline status to a Discord channel. One embed message is
posted per pipeline run and **edited in-place** as stages progress — no flood of
individual stage messages.

### What It Looks Like

**Running (with phase progress and elapsed time):**

```
🔨 Pipeline #42 — Running…
Add Discord notifications              ← linked to GitHub issue
nightgauge · feat/42-discord-notifications
M  ·  8 files  ·  Epic #3 (2/5)

✅  Issue Pickup  —  12s  ($0.008)
✅  Feature Planning  —  45s  ($0.021)
🔄  Feature Dev — Writing tests (5 phases)  —  1m 32s  ← live elapsed time
⏳  Feature Validate
⏳  PR Create
⏳  PR Merge

💰 $0.042  ⏱ 2m 14s
```

**Completed (success with enrichment):**

```
🔨 Pipeline #42 — Complete ✓
Add Discord notifications
nightgauge · feat/42-discord-notifications → epic/3-dashboard
M  ·  8 files  ·  Epic #3 (2/5)

✅  Issue Pickup  —  12s  ($0.008)
✅  Feature Planning  —  45s  ($0.021)
✅  Feature Dev  —  3m 12s  ($0.089)
✅  Feature Validate  —  1m 45s  ($0.034)
✅  PR Create  —  22s  ($0.011)
✅  PR Merge  —  8s  ($0.002)

💰 Budget         $0.165 / $50.00 (0%)  ·  Est: $2.12
🏥 Pipeline Health  🟢 100/100 — Excellent
📦 Cache           72% hit rate
📋 Pull Request    #87
🤖 Model           Sonnet 4

🧪 Gate Results
✅ build  ✅ tests  ✅ lint  ✅ types

💰 $0.165  ⏱ 6m 24s
```

**Failed (with diagnostics):**

```
🔨 Pipeline #42 — Failed ✗
Add Discord notifications
nightgauge · feat/42-discord-notifications
M  ·  13 files

✅  Issue Pickup  —  12s
✅  Feature Planning  —  45s
❌  Feature Dev  —  3m 12s
⏳  Feature Validate
⏳  PR Create
⏳  PR Merge

🔍 Error Details
❌ Feature Dev: exit 1: build error in src/index.ts

🧪 Gate Results
❌ build  ⏳ tests

🔄 Retries & Escalations
2 retry attempts
Feature Dev: sonnet-4-6 → opus-4-7

💰 Budget  $0.074 / $50.00 (0%)

⚡ Recommended Action
Manual fix needed — build errors require code changes

💰 $0.074  ⏱ 4m 09s
```

**Cancelled:**

```
🔨 Pipeline #42 — Cancelled

⏹️ Cancelled
Stopped during Feature Dev
2/6 stages complete
Issue open · Branch preserved

⚡ Recommended Action
Re-run when ready — issue and branch preserved
```

**Budget ceiling:**

```
🔨 Pipeline #42 — Budget Ceiling

💰 Budget Ceiling
Spent $0.480 before hitting limit
Increase budget or re-run with higher ceiling
```

Colors update automatically: blue (running) → green (success) → red (failed) →
grey (cancelled) → yellow (budget ceiling).

### Embed Content

#### Always Shown (description area)

- **Issue title** — Real GitHub issue title (synced after issue-pickup), linked
  to the GitHub issue when repo slug is available
- **Repository & branch** — Repo name + feature branch; shows base branch when
  targeting an epic branch (e.g., `feat/42-... → epic/3-dashboard`)
- **Context line** — Complexity label (XS/S/M/L/XL), planned file count, epic
  progress (position/total), routing decision (if non-standard)
- **Stage progress** — Per-stage status icons with duration and per-stage cost
- **Phase progress** — For running stages, shows current phase name and total
  (e.g., "Writing tests (5 phases)")
- **Running stage elapsed time** — Live elapsed time for the currently running
  stage (e.g., "Feature Dev — 3m 22s") so you can monitor long-running stages
- **Footer** — Total cost + elapsed time

#### Live Fields (shown during progress AND on completion)

These fields appear in real-time as events occur, not just at completion:

- **🔄 Retries & Escalations** — Retry count and model escalations (e.g.,
  sonnet → opus) — visible as soon as they happen
- **🔁 RALPH Self-Healing** — Self-healing loop iterations per stage — visible
  during the run so you can see self-correction in progress

#### Terminal-State Fields (shown on completion/failure)

| Outcome          | Color  | Fields Shown                                                |
| ---------------- | ------ | ----------------------------------------------------------- |
| Running          | Blue   | Stage progress, phase progress, context line, retries/RALPH |
| Complete ✓       | Green  | Budget, health, cache, PR #, model, gates                   |
| Failed ✗         | Red    | Error details, gates, retries, budget, model, action        |
| Cancelled        | Grey   | Interrupted stage, progress, preservation status            |
| Budget Ceiling   | Yellow | Spend vs limit, recommended action                          |
| Already Resolved | Green  | Stage progress                                              |

#### Diagnostic & Enrichment Fields

- **🔍 Error Details** — Which stage failed and the error message
- **🧪 Gate Results** — Build, test, lint, type-check pass/fail status
- **💰 Budget** — Actual spend vs ceiling with percentage and pre-flight
  estimate
- **🏥 Pipeline Health** — Health score (0-100) with status label
  (Excellent/Good/Needs Attention)
- **📦 Cache** — Prompt cache hit rate (indicates cost efficiency)
- **📋 Pull Request** — PR number (embed title also links to PR URL)
- **🤖 Model** — Claude model(s) used across stages
- **⚡ Recommended Action** — Actionable guidance based on the failure type:
  - Build/compile errors → "Manual fix needed"
  - Test failures after retries → "Manual investigation needed"
  - Rate limit / timeout → "Transient error — safe to re-run"
  - Max retries exhausted → "Manual intervention required"
  - Cancelled → "Re-run when ready — issue and branch preserved"
  - Budget ceiling → "Increase budget limit"

### Setup

**1. Create a Discord webhook**

In Discord: Channel Settings → Integrations → Webhooks → New Webhook. Copy the
webhook URL.

**2. Set the env var in your shell** (never paste the URL in config files)

```bash
export DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/<id>/<token>
```

Add to `~/.zshrc` or `~/.bashrc` to persist across sessions.

**3. Enable in `.nightgauge/config.yaml`** (already configured for this
repo)

```yaml
notifications:
  discord:
    enabled: true
    webhook_env: DISCORD_WEBHOOK_URL # name of the env var
```

### Config Reference

| Key                                 | Type    | Description                             |
| ----------------------------------- | ------- | --------------------------------------- |
| `notifications.discord.enabled`     | boolean | Enable/disable Discord embeds           |
| `notifications.discord.webhook_env` | string  | Name of env var holding the webhook URL |

### Notes

- The webhook URL is resolved from `process.env[webhook_env]` at runtime. If
  using VSCode, you can also store it securely via the command palette:
  "Nightgauge: Configure Discord Notifications" (uses OS keychain)
- If no webhook URL is found, Discord notifications are silently skipped
- Message updates are debounced (1.5 s) to stay within Discord rate limits
- Completed/failed pipeline embeds remain in the channel as a history log
- The final PATCH (with outcome status) retries up to 3 times with exponential
  backoff (3 s, 6 s) to ensure the embed reflects the terminal state
- Per-stage cost is shown when available (requires Claude CLI cost reporting)
- PR URL is linked in the embed title after pr-create completes
- Issue title is linked to the GitHub issue page when the repo slug is
  available (cross-repo and concurrent pipelines)
- Running stages show live elapsed time (updated on each debounce cycle) so
  you can monitor how long a stage has been running
- Retries, model escalations, and RALPH self-healing iterations are surfaced
  in real-time during the pipeline run, not just in the final embed
- Enrichment metadata (complexity, file count, epic context, budget, routing,
  health score) is injected into pipeline state by `HeadlessOrchestrator` via
  `PipelineStateService.setMeta()` and flows to Discord via `onStateChanged`
- Phase progress within stages (e.g., "Writing tests (5 phases)") updates
  live as the Go scheduler reports phase transitions

---

## Notification Routing Rules (`notifiers:`)

> **Issue #3374** — Per-channel routing rules for multi-notifier dispatch.

The `notifiers:` block lets you route specific pipeline events to specific
notifier channels. Without this block, every registered notifier receives every
event (default behavior, unchanged from before #3374).

### Event Key Taxonomy

| Event Key           | When fired                                              |
| ------------------- | ------------------------------------------------------- |
| `pipeline.start`    | Pipeline begins for an issue                            |
| `pipeline.update`   | Any mid-pipeline state change (stage transitions, etc.) |
| `pipeline.complete` | Pipeline finishes successfully                          |
| `pipeline.failure`  | Pipeline fails or is aborted                            |
| `stage.start`       | Individual stage begins execution                       |
| `stage.complete`    | Individual stage completes successfully                 |
| `stage.failure`     | Individual stage fails                                  |
| `budget.warning`    | Monthly budget threshold reached                        |
| `stall.warning`     | Pipeline stall detected                                 |

### Field Reference

| Field                | Type                      | Description                                                            |
| -------------------- | ------------------------- | ---------------------------------------------------------------------- |
| `id`                 | string (required)         | Unique identifier — must match a notifier wired in services.ts         |
| `type`               | `discord` \| `mattermost` | Notifier provider type                                                 |
| `channel`            | string (optional)         | Channel name (informational, used for display only)                    |
| `events`             | EventKey[] (optional)     | Allowlist of event keys. Absent or empty = all events.                 |
| `suppress`           | EventKey[] (optional)     | Denylist of event keys. Takes precedence over `events`.                |
| `webhook_secret_key` | string (optional)         | SecretStorage key name for this notifier's webhook URL (not a raw URL) |

### Examples

**Route failures to alerts channel, completions to success channel:**

```yaml
notifiers:
  - id: discord
    type: discord
    channel: "#pipeline-alerts"
    events:
      - pipeline.failure
      - stage.failure
      - stall.warning
      - budget.warning

  - id: mattermost
    type: mattermost
    channel: "#pipeline-success"
    events:
      - pipeline.complete
    suppress:
      - pipeline.update
```

**Suppress noisy update events on production channel:**

```yaml
notifiers:
  - id: discord
    type: discord
    channel: "#pipeline-prod"
    suppress:
      - pipeline.update
      - stage.start
      - stage.complete
```

### Merge Semantics (Important)

The `notifiers:` block is an **array** — it replaces (not deep-merges) across
config tiers. If your team config defines a `notifiers:` block and your local
config also defines one, the local array wins entirely. Copy the full array when
overriding at multiple tiers.

This is intentional (ADR-003): deep-merging two `notifiers:` arrays could
produce unexpected combinations of stale fields from different tiers.

### Default Behavior

When `notifiers:` is absent from all config tiers, every registered notifier
receives every event — the pre-#3374 behavior. No migration is required for
existing configurations.

---

## Usage Limits and Budget Tracking (UsageLimitsService)

**File**: `packages/nightgauge-vscode/src/services/UsageLimitsService.ts`

UsageLimitsService tracks cumulative pipeline cost against a user-configured
monthly budget. It polls `DashboardState.getAggregates()` on a configurable
interval, displays live cost in the status bar, and fires threshold-based
warning and critical notifications via `NotificationService`.

### Configuration

All settings live under the `ui.limits` key in `.nightgauge/config.yaml`:

```yaml
ui:
  limits:
    monthly_budget_usd: 0 # Monthly budget in USD (0 = disabled)
    warning_threshold_pct: 70 # Percentage at which a warning fires
    critical_threshold_pct: 90 # Percentage at which a critical alert fires
    polling_interval_seconds: 60 # How often to check usage
```

| Key                        | Type   | Default | Description                                       |
| -------------------------- | ------ | ------- | ------------------------------------------------- |
| `monthly_budget_usd`       | number | `0`     | Monthly cost budget in USD. `0` disables tracking |
| `warning_threshold_pct`    | number | `70`    | Usage percentage that triggers a warning          |
| `critical_threshold_pct`   | number | `90`    | Usage percentage that triggers a critical alert   |
| `polling_interval_seconds` | number | `60`    | Seconds between usage polls                       |

### Behavior

- When `monthly_budget_usd` is `0`, the service is disabled (no polling, no
  status bar item).
- Alerts are deduplicated: a warning fires once, and a critical alert fires
  once. The alert level resets when the user runs
  `Nightgauge: Reset Usage Counter`.
- The reset command records the current total cost as an offset baseline so
  future reads show usage since the reset point, without clearing history.

---

## Knowledge Base Configuration

Controls knowledge directory scaffolding and behavior during pipeline execution.
See [docs/KNOWLEDGE_BASE.md](KNOWLEDGE_BASE.md) for full knowledge base
documentation.

### Config Reference

All settings live under the `knowledge:` key in `.nightgauge/config.yaml`:

```yaml
knowledge:
  enabled: true # Enable knowledge directory scaffolding
  auto_scaffold: true # Scaffold automatically during issue-pickup
  wiki_links: true # Enable wiki-link resolution in knowledge docs
  index_on_commit: false # Regenerate index on commit (reserved for future use)
  auto_prune_on_merge: true # Remove boilerplate-only knowledge dirs after PR merge
```

| Key                    | Type    | Default | Description                                                                                                |
| ---------------------- | ------- | ------- | ---------------------------------------------------------------------------------------------------------- |
| `enabled`              | boolean | `true`  | Master switch. When `false`, all knowledge operations are disabled                                         |
| `auto_scaffold`        | boolean | `true`  | Automatically create knowledge directory during issue-pickup (requires `enabled: true`)                    |
| `wiki_links`           | boolean | `true`  | Enable `[[wiki-link]]` resolution in knowledge documents                                                   |
| `index_on_commit`      | boolean | `false` | Regenerate the knowledge index on every commit via a git hook (reserved for future git hook use)           |
| `auto_prune_on_merge`  | boolean | `true`  | Remove knowledge directories that contain only boilerplate content after a PR is merged                    |
| `recall.dev_threshold` | float   | `1.5`   | Minimum recall score for constraints shown to feature-dev. Higher than planning's default to reduce noise. |
| `recall.dev_limit`     | integer | `5`     | Max recalled architectural constraints shown to feature-dev per invocation.                                |

### Behavior

- Setting `enabled: false` disables all knowledge operations — scaffolding,
  indexing, and wiki-link resolution are all skipped.
- `auto_scaffold: true` causes issue-pickup to create a
  `.nightgauge/knowledge/{epics|features}/{N}-{slug}/` directory with
  `PRD.md` and `decisions.md` templates.
- `index_on_commit` is currently a no-op — the flag is accepted and stored but
  the git hook is not yet implemented. It is reserved to avoid a breaking change
  when the hook is added.
- Config files that do not include a `knowledge:` section continue to work
  unchanged — all four fields default to the values shown above.

### Environment Variables

| Variable                                   | Default | Description                              |
| ------------------------------------------ | ------- | ---------------------------------------- |
| `NIGHTGAUGE_KNOWLEDGE_ENABLED`             | `true`  | Override `knowledge.enabled`             |
| `NIGHTGAUGE_KNOWLEDGE_AUTO_SCAFFOLD`       | `true`  | Override `knowledge.auto_scaffold`       |
| `NIGHTGAUGE_KNOWLEDGE_WIKI_LINKS`          | `true`  | Override `knowledge.wiki_links`          |
| `NIGHTGAUGE_KNOWLEDGE_INDEX_ON_COMMIT`     | `false` | Override `knowledge.index_on_commit`     |
| `NIGHTGAUGE_KNOWLEDGE_AUTO_PRUNE_ON_MERGE` | `true`  | Override `knowledge.auto_prune_on_merge` |

---

## Platform Configuration

Controls all communication with the optional Nightgauge cloud API. The
`platform.enabled` flag acts as a **master kill switch** — when set to `false`,
all platform communication is disabled and the extension operates in fully
offline mode. This is useful in air-gapped environments or during local
development without cloud access.

### Config Reference

All settings live under the `platform:` key in `.nightgauge/config.yaml`:

```yaml
platform:
  enabled: false # Opt in to cloud features; false = fully offline mode
  api_url: "https://api.nightgauge.dev" # Platform API base URL
  connection_timeout_ms: 30000 # Request timeout in milliseconds
  retry_policy:
    attempts: 3 # Max retry attempts on failure
    backoff_ms: 1000 # Initial backoff delay (ms)
    backoff_multiplier: 2 # Exponential backoff multiplier
  telemetry:
    enabled: false # Opt-in. Send anonymized telemetry to the platform
  feature_flags: {} # Platform feature flag overrides
```

| Key                               | Type              | Default                        | Description                                                                        |
| --------------------------------- | ----------------- | ------------------------------ | ---------------------------------------------------------------------------------- |
| `enabled`                         | boolean           | `false`                        | Opt-in master switch. `false` disables config-derived platform communication       |
| `api_url`                         | string (URL)      | `'https://api.nightgauge.dev'` | Platform API base URL. Override for dev/staging environments                       |
| `connection_timeout_ms`           | integer (≥0)      | `30000`                        | Connection timeout in milliseconds. `0` disables timeout                           |
| `retry_policy.attempts`           | integer (1–10)    | `3`                            | Number of retry attempts before giving up on a failed request                      |
| `retry_policy.backoff_ms`         | integer (≥0)      | `1000`                         | Initial backoff delay in milliseconds before the first retry                       |
| `retry_policy.backoff_multiplier` | number (1–10)     | `2`                            | Multiplier applied to `backoff_ms` on each subsequent retry (exponential backoff)  |
| `telemetry.enabled`               | boolean           | `false`                        | Opt-in. Enable sending anonymized usage telemetry to the platform (off by default) |
| `feature_flags`                   | record (str→bool) | `{}`                           | Platform feature flag map. Keys are flag names, values enable/disable the flag     |

### Who Reads Platform Config

**The Go binary** is the sole consumer of `platform.*` configuration. The
extension does **not** hold a platform client — it routes all platform calls
through the Go binary via IPC.

| Config Consumer            | What It Uses                                                                                                                                                                                                                                                              |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Go binary** (`serve`)    | `enabled`, `api_url`, and `license_key` — explicit flags/environment variables opt in directly; config-derived values are used only when `platform.enabled: true`. `connection_timeout_ms` and `retry_policy` are schema-validated but not yet consumed by the Go binary. |
| **Extension** (TypeScript) | Reads `platform.enabled` only to decide whether to display platform-related UI (license badge, skill tier badge). Does **not** make direct platform API calls.                                                                                                            |

### Behavior

- Setting `platform.enabled: false` disables all platform API calls. The
  extension continues to function for local pipeline execution; only cloud
  features (skill serving, license validation, team management) are unavailable.
- `api_url` accepts any valid HTTP/HTTPS URL. Use `http://localhost:PORT` for
  local development against a self-hosted platform instance.
- `feature_flags` records are **replaced** (not merged) when overriding at a
  higher config tier. A tier that sets `feature_flags: { flag_a: true }` will
  remove all flags from lower tiers — not merge with them.
- Config files that omit the `platform:` section entirely continue to work
  unchanged — all fields default to the values shown above.

### Environment Variables

| Variable                                      | Default                      | Description                                              |
| --------------------------------------------- | ---------------------------- | -------------------------------------------------------- |
| `NIGHTGAUGE_PLATFORM_ENABLED`                 | —                            | Not currently consumed; use `platform.enabled` in config |
| `NIGHTGAUGE_PLATFORM_API_URL`                 | `https://api.nightgauge.dev` | Override `platform.api_url`                              |
| `NIGHTGAUGE_PLATFORM_CONNECTION_TIMEOUT_MS`   | `30000`                      | Override `platform.connection_timeout_ms`                |
| `NIGHTGAUGE_PLATFORM_RETRY_POLICY_ATTEMPTS`   | `3`                          | Override `platform.retry_policy.attempts`                |
| `NIGHTGAUGE_PLATFORM_RETRY_POLICY_BACKOFF_MS` | `1000`                       | Override `platform.retry_policy.backoff_ms`              |
| `NIGHTGAUGE_PLATFORM_TELEMETRY_ENABLED`       | `false`                      | Override `platform.telemetry.enabled`                    |

---

## Product Audit Configuration (`product_audit`)

Configuration for the `/nightgauge:product-audit` skill. Controls
dimension weights, scoring thresholds, auto-fix behavior, and reporting.

See [docs/PRODUCT_AUDIT.md](PRODUCT_AUDIT.md) for the full product audit user guide.

### Top-Level Fields

| Config Key               | Type    | Default | Description                                            |
| ------------------------ | ------- | ------- | ------------------------------------------------------ |
| `product_audit.enabled`  | boolean | `true`  | Enable/disable the skill                               |
| `product_audit.schedule` | string  | `null`  | Cron schedule for automated runs (e.g., `"0 9 * * 1"`) |

### Dimension Weights

Weights control each dimension's contribution to the overall score. Must sum to
1.0. Override individual weights without specifying all 8 — unspecified weights
retain their defaults.

| Config Key                                       | Default | Description           |
| ------------------------------------------------ | ------- | --------------------- |
| `product_audit.dimension_weights.api_alignment`  | `0.20`  | API Alignment weight  |
| `product_audit.dimension_weights.lifecycle`      | `0.10`  | Epic Lifecycle weight |
| `product_audit.dimension_weights.documentation`  | `0.10`  | Documentation weight  |
| `product_audit.dimension_weights.feature_parity` | `0.15`  | Feature Parity weight |
| `product_audit.dimension_weights.test_coverage`  | `0.20`  | Test Coverage weight  |
| `product_audit.dimension_weights.security`       | `0.15`  | Security weight       |
| `product_audit.dimension_weights.dependencies`   | `0.05`  | Dependencies weight   |
| `product_audit.dimension_weights.ci_cd`          | `0.05`  | CI/CD weight          |

### Dimension-Specific Settings

#### `product_audit.dimensions.api_alignment`

| Config Key           | Default                 | Description                                     |
| -------------------- | ----------------------- | ----------------------------------------------- |
| `platform_api_url`   | `http://localhost:3000` | Platform API URL for live probing               |
| `fallback_to_static` | `true`                  | Use static route analysis if API is unavailable |

#### `product_audit.dimensions.test_coverage`

| Config Key          | Default                            | Description                                                        |
| ------------------- | ---------------------------------- | ------------------------------------------------------------------ |
| `threshold_percent` | `80`                               | Coverage percentage below which a finding is emitted               |
| `cache_coverage`    | `true`                             | Cache coverage reports for `--quick` mode                          |
| `cache_dir`         | `.nightgauge/audit/coverage-cache` | Directory for cached coverage reports                              |
| `critical_paths`    | `[]`                               | File paths where coverage failures are elevated to `high` severity |

#### `product_audit.dimensions.feature_parity`

| Config Key              | Default                                | Description                                            |
| ----------------------- | -------------------------------------- | ------------------------------------------------------ |
| `config_file`           | `.nightgauge/audit/parity-config.json` | Feature parity matrix file                             |
| `required_parity_score` | `85`                                   | Minimum parity score (0-100) before emitting a finding |

#### `product_audit.dimensions.security`

| Config Key             | Default | Description                              |
| ---------------------- | ------- | ---------------------------------------- |
| `skip_secret_patterns` | `[]`    | Patterns to exclude from secret scanning |
| `custom_patterns`      | `[]`    | Additional secret patterns to scan for   |

#### `product_audit.dimensions.ci_cd`

| Config Key                | Default                     | Description                         |
| ------------------------- | --------------------------- | ----------------------------------- |
| `branch_protection_check` | `true`                      | Check branch protection rules       |
| `required_status_checks`  | `["build", "test", "lint"]` | Status checks that must be enforced |

#### `product_audit.dimensions.lifecycle`

| Config Key          | Default                                                    | Description                                           |
| ------------------- | ---------------------------------------------------------- | ----------------------------------------------------- |
| `max_stale_days`    | `30`                                                       | Days of inactivity before an epic is considered stale |
| `board_status_enum` | `["Backlog", "Ready", "In Progress", "In Review", "Done"]` | Valid project board statuses                          |

### Issue Creation

| Config Key                                  | Default    | Description                                 |
| ------------------------------------------- | ---------- | ------------------------------------------- |
| `product_audit.issue_creation.enabled`      | `true`     | Allow issue creation with `--create-issues` |
| `product_audit.issue_creation.label_prefix` | `"audit:"` | Label prefix for created issues             |
| `product_audit.issue_creation.assignee`     | `null`     | Default assignee for created issues         |
| `product_audit.issue_creation.project`      | `null`     | GitHub project to add issues to             |

### Auto-Fix

| Config Key                       | Default                                                 | Description                                |
| -------------------------------- | ------------------------------------------------------- | ------------------------------------------ |
| `product_audit.auto_fix_enabled` | `["STALE_EPIC", "BOARD_STATUS_DRIFT", "STALE_BLOCKER"]` | Categories eligible for `--fix` automation |

### Reporting

| Config Key                                       | Default  | Description                                   |
| ------------------------------------------------ | -------- | --------------------------------------------- |
| `product_audit.reporting.output_format`          | `"both"` | `json`, `markdown`, or `both`                 |
| `product_audit.reporting.report_retention_days`  | `90`     | Days to keep historical reports in `history/` |
| `product_audit.reporting.include_trend_analysis` | `true`   | Include trend data in reports                 |

### CI Integration

| Config Key                                   | Default              | Description                                   |
| -------------------------------------------- | -------------------- | --------------------------------------------- |
| `product_audit.ci.fail_on_score_below`       | `75`                 | Score threshold for CI failure                |
| `product_audit.ci.fail_on_critical_findings` | `true`               | Fail CI if any critical findings are detected |
| `product_audit.ci.report_artifact_path`      | `.nightgauge/audit/` | Path for CI artifact upload                   |

### Environment Variables

| Variable                                 | Default | Description                      |
| ---------------------------------------- | ------- | -------------------------------- |
| `NIGHTGAUGE_PRODUCT_AUDIT_ENABLED`       | `true`  | Override `product_audit.enabled` |
| `NIGHTGAUGE_PRODUCT_AUDIT_THRESHOLD`     | `75`    | Override CI score threshold      |
| `NIGHTGAUGE_PRODUCT_AUDIT_OUTPUT_FORMAT` | `both`  | Override output format           |

### Example Configuration

```yaml
product_audit:
  enabled: true
  schedule: "0 9 * * 1" # Weekly Monday 9 AM UTC

  dimension_weights:
    api_alignment: 0.20
    lifecycle: 0.10
    documentation: 0.10
    feature_parity: 0.15
    test_coverage: 0.20
    security: 0.15
    dependencies: 0.05
    ci_cd: 0.05

  create_issues_on_severity:
    - critical
    - high

  auto_fix_enabled:
    - STALE_EPIC
    - BOARD_STATUS_DRIFT
    - STALE_BLOCKER

  dimensions:
    api_alignment:
      platform_api_url: "http://localhost:3000"
      fallback_to_static: true

    test_coverage:
      cache_coverage: true
      threshold_percent: 80
      critical_paths:
        - "src/core/"
        - "packages/nightgauge-sdk/src/"

    feature_parity:
      config_file: ".nightgauge/audit/parity-config.json"
      required_parity_score: 85

    lifecycle:
      max_stale_days: 30

  reporting:
    output_format: "both"
    report_retention_days: 90

  ci:
    fail_on_score_below: 75
    fail_on_critical_findings: true
```

---

## Focus Mode Configuration (`focus.yaml`)

Focus mode is configured through a **separate file** at
`.nightgauge/focus.yaml` — not through `config.yaml`. This file is read
by the Go binary, VSCode extension, and all skills that participate in
focus-aware prioritization.

> For the complete user guide including lens descriptions, workflow examples,
> and CLI reference, see [docs/FOCUS_MODE.md](FOCUS_MODE.md).

### File Location

```
.nightgauge/
├── config.yaml      ← pipeline/project settings (this document)
└── focus.yaml       ← focus lens state (separate, managed by focus commands)
```

### Schema

```yaml
# .nightgauge/focus.yaml

# Active lens name. Built-in: general, quality, features, security,
# performance, documentation, reliability, ux
active_lens: security

# Timestamp of last change (UTC ISO 8601, set automatically)
set_at: 2026-03-15T09:00:00Z

# Source of last change: "cli", "vscode", "ipc"
set_by: cli

# Optional: user-defined lenses
custom_lenses:
  - name: mobile
    description: Mobile app quality — Flutter, iOS, Android
    scoring_boosts:
      cross_repo: 15
      developer_experience: 5
    keywords:
      - flutter
      - ios
      - android
      - mobile
```

### Field Reference

| Field                            | Type      | Default   | Description                             |
| -------------------------------- | --------- | --------- | --------------------------------------- |
| `active_lens`                    | string    | `general` | Name of the currently active focus lens |
| `set_at`                         | timestamp | auto      | When the focus was last changed (UTC)   |
| `set_by`                         | string    | —         | Source: `cli`, `vscode`, or `ipc`       |
| `custom_lenses[].name`           | string    | required  | Unique lens identifier (lowercase)      |
| `custom_lenses[].description`    | string    | —         | Human-readable lens description         |
| `custom_lenses[].scoring_boosts` | map       | —         | Dimension → bonus points (0–20)         |
| `custom_lenses[].keywords`       | string[]  | —         | Keywords for issue/proposal matching    |

Valid `scoring_boosts` keys: `safety_reliability`, `pipeline_stage`,
`automation_potential`, `developer_experience`, `cross_repo`,
`implementation_complexity`.

### Management

Use the CLI — do not edit `focus.yaml` directly for lens activation:

```bash
nightgauge focus set quality   # activate a lens
nightgauge focus show          # inspect current state
nightgauge focus clear         # reset to general
```

Custom lenses can be added manually to `focus.yaml` under `custom_lenses`, then
activated via `nightgauge focus set <name>`.

### Behavior When Missing

When `focus.yaml` does not exist:

- All consumers default to the `general` (no-boost) lens
- No errors are produced
- Behavior is identical to pre-focus-mode releases (fully backward-compatible)

---

## Autonomous Scheduler Configuration

Controls the autonomous pipeline scheduler — the cross-repo scan-prioritize-
dispatch loop that processes issues from project boards without manual
intervention.

```yaml
autonomous:
  scan_interval: 30s # How often to re-scan project boards (default: 30s)
  # max_concurrent: DEPRECATED — set pipeline.max_concurrent instead.
  # The autonomous scheduler resolves through pipeline.max_concurrent first
  # and only falls back to this key for configs predating Issue #3195.
  budget_ceiling: 500000 # Global token budget, 0 = unlimited (default: 0)
  enabled_repos: # Optional allowlist — scan only these repos (default: all)
    - acme-platform
  exclude_labels: # Human-only labels never dispatched (default: ["owner-action"])
    - owner-action
  pickup_backlog: false # Dispatch Backlog items after all Ready items done (default: false)
  auto_actionable: false # Move auto-refined issues directly to Ready (default: false)
  refinement_enabled: true # Enable autonomous refinement scheduler (default: true)
  refinement_interval: 60s # Time between refinement scans, min 30s (default: 60s)
  refinement_max_concurrent: 1 # Max concurrent refinement operations, 1-3 (default: 1)
  safety_rails:
    circuit_breaker_max: 3 # Consecutive failures before trip (default: 3)
    rate_limit_per_hour: 20 # Max pipeline starts per hour (default: 20)
    epic_checkpoint: true # Pause after each epic wave (default: false)
    health_gate_min: 30 # Min health score to continue (default: 0)
```

### enabled_repos

| Key             | Type         | Default                       |
| --------------- | ------------ | ----------------------------- |
| `enabled_repos` | list[string] | (unset — scan all configured) |

Restricts the autonomous scheduler to scanning only the listed repos. Each
scan cycle queries every configured repo's project board via GitHub GraphQL
plus per-issue `blockedBy`/`blocking` sub-queries — a 4-repo workspace can
exhaust the 5,000/hour GraphQL quota in under an hour even while idle.
Scoping to a subset cuts that cost proportionally.

- Values may be short names (`acme-platform`) or fully-qualified
  (`acme/platform`). Short names are expanded against
  the top-level `owner` value.
- Matching is case-insensitive on `owner/repo`.
- Takes effect at scheduler start (CLI: on `autonomous run`; VS Code: on
  Start/Resume). A running scheduler must be stopped and restarted to pick
  up changes.
- In VS Code, the allowlist is **intersected** with the set of workspace
  folders. If the intersection is empty (the configured repo isn't open in
  this workspace) the explicit allowlist still wins — the user's stated
  intent overrides workspace membership.
- Use the `Autonomous: Select Repos` command in VS Code to edit this list
  via a multi-select UI.

### exclude_labels

| Key              | Type         | Default        |
| ---------------- | ------------ | -------------- |
| `exclude_labels` | list[string] | `owner-action` |

Issues carrying one of these labels are **never dispatched** — by the
autonomous candidate loop, by epic-expansion enqueue (`EnqueueEpic`), or by the
`nightgauge queue add` CLI command. These are human-only issues: work only an
operator can do (e.g. rotating a cloud credential in a provider dashboard, a
DNS change, revoking an over-scoped token). Dispatching one burns tokens
through issue-pickup → planning → feature-dev → validate — which correctly
produces zero code changes — and then fails at pr-create with nothing to
commit (Issue #317).

- Matching is case-insensitive against each issue's labels.
- This is a single resolved option, not an additive allowlist: setting
  `exclude_labels` **replaces** the default `["owner-action"]` entirely. To
  keep the default while adding your own convention, list both explicitly
  (e.g. `["owner-action", "needs-human"]`).
- Empty/unset resolves to the default `["owner-action"]` — there is no
  separate on/off knob.
- The excluded issue stays on the project board for a human to act on; it is
  not closed or relabeled.

### Per-Repository Concurrency Cap

The autonomous scheduler supports a per-repository ceiling on concurrent
pipelines. This lets you keep, for example, a global cap of 3 while still
limiting a single repo to 1 or 2 concurrent runs (so it never monopolizes
global slots).

There are two equivalent forms:

| Form                                                                      | Meaning                                                          |
| ------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| `autonomous.repositories.<repo>.sequential: true` (legacy boolean)        | At most 1 pipeline at a time. Equivalent to `max_concurrent: 1`. |
| `autonomous.repositories.<repo>.max_concurrent: N` (numeric, Issue #2987) | At most N concurrent pipelines (`N ≥ 1`).                        |

When both are set, the numeric `max_concurrent` (when `> 0`) wins. Use short
names (matching the last segment of the repo path) or fully-qualified
`owner/repo` names — both forms are resolved.

```yaml
autonomous:
  max_concurrent: 3 # global ceiling across all repos
  repositories:
    acme-platform:
      max_concurrent: 2 # at most 2 concurrent pipelines from platform
    acme-mobile:
      sequential: true # at most 1 pipeline at a time (legacy form for cap=1)
    acme-dashboard:
      max_concurrent: 1 # equivalent to sequential: true
```

Resolution order (mirrors `AutonomousConfig.MaxForRepo()` in
`internal/config/config.go`):

1. `max_concurrent` (when `≥ 1`) wins.
2. `sequential: true` → cap of 1.
3. Otherwise no per-repo cap (defers to global `max_concurrent`).

The scheduler skips dispatching from a repo once its currently-running count
meets or exceeds the cap, regardless of available global slots.

**VS Code Repositories view**: each row shows the resolved cap as a short
description suffix (`[max: 2]`, `[seq]`, or no suffix when unlimited):

- Inline numeric icon (`$(symbol-number)`) → opens a quick-pick (`Unlimited`,
  `1 (sequential)`, `2`, `3`, `4`, `5`, `Custom…`) and writes the result to
  `.nightgauge/config.yaml`. No YAML editing required.
- Right-click → **Toggle Sequential Mode** still works for the cap-of-1
  case and round-trips with the new numeric form.

Changes take effect on the next scan cycle — no restart required. When using
the CLI (`nightgauge autonomous run`), stop and restart the scheduler for
changes to `.nightgauge/config.yaml` to take effect.

### Board Status Gating

The autonomous scheduler uses the project board **Status** field as a dispatch
gate:

| Board Status  | Dispatched?                                                             |
| ------------- | ----------------------------------------------------------------------- |
| **Ready**     | Always — primary dispatch pool                                          |
| **Backlog**   | Only when `pickup_backlog: true` AND no Ready items remain for the repo |
| In progress   | Never — already being worked on                                         |
| In review     | Never — pipeline already completed                                      |
| Done          | Never — already completed                                               |
| _(no status)_ | Never — not yet triaged onto the board                                  |

**Why**: New issues should start in Backlog and be promoted to Ready only after
all relationships (`blockedBy`, `addSubIssue`) are fully configured. This
eliminates the race condition where the scheduler dispatches an issue during the
30-second window between creation and dependency setup.

### pickup_backlog

When `true`, the scheduler also dispatches Backlog items — but only after all
Ready items for the same repo have been dispatched or completed. Ready items
always take priority regardless of this setting.

Use `pickup_backlog: true` for repos where issues are created directly into a
pipeline-ready state with no manual triage step.

### auto_actionable

| Key               | Type    | Default | Env Variable                            |
| ----------------- | ------- | ------- | --------------------------------------- |
| `auto_actionable` | boolean | `false` | `NIGHTGAUGE_AUTONOMOUS_AUTO_ACTIONABLE` |

Controls whether issues that pass through the autonomous refinement pipeline are
placed directly into **Ready** status (`true`) or held in **Backlog** for manual
review (`false`).

- **`false` (default)**: Refined issues land in Backlog. A human promotes them
  to Ready after reviewing the refinement output. Recommended for teams that
  want visibility into automated changes.
- **`true`**: Refined issues move directly to Ready and are dispatched by the
  autonomous scheduler on the next scan cycle. Use for high-trust workflows
  where refinement quality is well-established.

### refinement_enabled

| Key                  | Type    | Default | Env Variable                               |
| -------------------- | ------- | ------- | ------------------------------------------ |
| `refinement_enabled` | boolean | `true`  | `NIGHTGAUGE_AUTONOMOUS_REFINEMENT_ENABLED` |

Master switch for the autonomous refinement scheduler. When `false`, no
refinement scans run. The dispatch scheduler (`scan_interval`) continues to
operate normally — only the refinement loop is paused.

### refinement_interval

| Key                   | Type     | Default | Minimum | Env Variable                                |
| --------------------- | -------- | ------- | ------- | ------------------------------------------- |
| `refinement_interval` | duration | `"60s"` | `"30s"` | `NIGHTGAUGE_AUTONOMOUS_REFINEMENT_INTERVAL` |

Time between refinement scan cycles. Accepts duration strings: `"30s"`, `"1m"`,
`"5m"`, etc.

**Minimum: 30 seconds.** Values below 30s are rejected by the Go resolver with
a clear error to prevent GitHub API rate-limit abuse. The default of 60s
provides a comfortable margin above the minimum.

### refinement_max_concurrent

| Key                         | Type    | Default | Range | Env Variable                                      |
| --------------------------- | ------- | ------- | ----- | ------------------------------------------------- |
| `refinement_max_concurrent` | integer | `1`     | `1–3` | `NIGHTGAUGE_AUTONOMOUS_REFINEMENT_MAX_CONCURRENT` |

Maximum number of refinement operations that run concurrently. Capped at 3 to
prevent resource exhaustion. The conservative default of 1 is appropriate for
most teams; increase to 2 or 3 only when refinement throughput is a bottleneck.

---

## Autonomous Discovery Configuration

Controls the scheduled autonomous self-improvement loop. See
[docs/SCHEDULED_DISCOVERY.md](SCHEDULED_DISCOVERY.md) for full documentation.

### `autonomous_discovery`

| Key                  | Type            | Default                           | Description                                                                  |
| -------------------- | --------------- | --------------------------------- | ---------------------------------------------------------------------------- |
| `enabled`            | boolean         | `true`                            | Master switch. Set `false` to disable all scheduled runs.                    |
| `kill_switch`        | boolean         | `false`                           | Pause issue creation without disabling infrastructure. Monitoring continues. |
| `score_threshold`    | integer (0-100) | `70`                              | Minimum relevance score to auto-create a GitHub issue.                       |
| `auto_created_label` | string          | `"type:chore,area:release-watch"` | Labels applied to auto-created issues.                                       |

```yaml
autonomous_discovery:
  enabled: true
  kill_switch: false # Set true to pause issue creation
  score_threshold: 70
  auto_created_label: "type:chore,area:release-watch"
```

**Kill-switch behavior**: When `kill_switch: true`, detection and skill runs
continue normally but `--create-issues` is not passed. No issues are created.
The dashboard Discovery tab still shows all activity. Use this to temporarily
pause issue creation (e.g., before a release freeze) without dismantling the
scheduled infrastructure.

**Disable all runs**: Set `enabled: false` to skip both the release-watch skill
invocation and the continuous-improvement run entirely. Detection still runs
(release-watchdog.yml detects versions), but no AI assessment is performed.

---

### `discovery_budget`

Token budget for scheduled discovery runs, separate from the main pipeline
budget controlled by `pipeline.budget_preset`.

| Key                                 | Type            | Default | Description                                          |
| ----------------------------------- | --------------- | ------- | ---------------------------------------------------- |
| `release_watch_max_tokens`          | integer or null | `null`  | Max tokens per release-watch run. `null` = no limit. |
| `continuous_improvement_max_tokens` | integer or null | `null`  | Max tokens per CI review run. `null` = no limit.     |

```yaml
discovery_budget:
  release_watch_max_tokens: null
  continuous_improvement_max_tokens: null
```

---

### `scheduled_tasks`

Informational documentation of scheduled task cadences. Actual execution is
driven by GitHub Actions workflows — these entries do not directly control
scheduling.

| Key                               | Type    | Default       | Description                                   |
| --------------------------------- | ------- | ------------- | --------------------------------------------- |
| `release_watch.enabled`           | boolean | `true`        | Document that release-watch runs are enabled. |
| `release_watch.schedule`          | string  | `"0 9 * * *"` | Cron schedule (documentation only).           |
| `continuous_improvement.enabled`  | boolean | `true`        | Document that CI review runs are enabled.     |
| `continuous_improvement.schedule` | string  | `"0 8 * * 1"` | Cron schedule (documentation only).           |

```yaml
scheduled_tasks:
  release_watch:
    enabled: true
    schedule: "0 9 * * *" # Daily at 9 AM UTC
  continuous_improvement:
    enabled: true
    schedule: "0 8 * * 1" # Weekly on Monday 8 AM UTC
```

---

## Forge Configuration (`schema_version: 2`)

> **Design context** — for the _why_ behind the abstraction (interface
> layout, adapter contract, lifecycle, CE-vs-EE feature matrix, sentinel
> errors), see [FORGE_ABSTRACTION.md](FORGE_ABSTRACTION.md). For the
> migration semantics, see
> [ADR-009 (Workspace schema migration)](decisions/009-workspace-schema-migration.md).
> This section is the **operational reference** — schema fields, validation,
> and migration mechanics.

Nightgauge supports multiple forge backends (GitHub, GitLab) via the `forges:` block
introduced in schema version 2. The `schema_version` field gates migration behavior — a
config without it is treated as v1 and automatically migrated in-memory.

### `schema_version`

```yaml
schema_version: "2"
```

| Value           | Meaning                                                                                                                                                                                                |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| absent or `"1"` | Legacy v1 config. Migration warning is emitted and a default `forges.github` block pointing at `https://github.com` is inserted in-memory. The YAML file on disk is **never** rewritten automatically. |
| `"2"`           | Current format. No migration performed.                                                                                                                                                                |

### `forges:` block

The `forges:` map declares available forge instances. Each key is a user-defined
forge ID (e.g. `"github"`, `"corp-gitlab"`).

```yaml
schema_version: "2"
forges:
  github:
    kind: github
    base_url: https://github.com
    auth_method: token
    token_env: GITHUB_TOKEN
  corp-gitlab:
    kind: gitlab
    base_url: https://gitlab.corp.example.com
    graphql_url: https://gitlab.corp.example.com/api/graphql
    auth_method: token
    token_env: CORP_GITLAB_TOKEN
    ca_bundle: certs/corp-ca.pem
    default_project_id: 42
    proxy: http://proxy.corp.example.com:3128
```

| Field                | Type    | Default   | Description                                                                                            |
| -------------------- | ------- | --------- | ------------------------------------------------------------------------------------------------------ |
| `kind`               | string  | —         | Forge adapter type: `"github"` or `"gitlab"`.                                                          |
| `base_url`           | string  | —         | Canonical base URL of the forge. Required for `kind: gitlab`. GitHub defaults to `https://github.com`. |
| `graphql_url`        | string  | derived   | GraphQL API endpoint. When empty, the adapter derives it from `base_url`.                              |
| `auth_method`        | string  | `"token"` | Authentication mechanism: `"token"`, `"app"`, or `"pat"`.                                              |
| `token_env`          | string  | —         | Environment variable name holding the access token (required when `auth_method: token`).               |
| `ca_bundle`          | string  | —         | Path to a PEM CA certificate bundle. Resolved relative to the config file directory.                   |
| `default_project_id` | integer | —         | Default numeric project/group ID (GitLab-specific).                                                    |
| `proxy`              | string  | —         | HTTP/HTTPS proxy URL. Falls back to `HTTPS_PROXY` environment variable when empty.                     |

**Legacy fields** (`host`, `owner`, `project_number`, `owner_type`) are retained for
backward compatibility but are deprecated in v2. When both `host` and `base_url` are set,
`base_url` takes precedence.

### Per-repo forge selection

Repositories can be routed to a specific forge via `autonomous.repositories.<name>.forge`:

```yaml
autonomous:
  repositories:
    nightgauge:
      forge: github
    corp-service:
      forge: corp-gitlab
```

When `forge` is absent, the workspace default forge is used (the `github` key when present,
otherwise the singleton GitHub adapter built from the legacy top-level fields).

### Validation

Run `nightgauge config validate` to check forge configuration:

```bash
# Text report (exit 0 = valid, exit non-zero = errors)
nightgauge config validate

# Machine-readable JSON
nightgauge config validate --json

# Validate a specific file
nightgauge config validate --config /path/to/config.yaml
```

The validator checks:

- Unknown forge `kind` values (only `github` and `gitlab` are valid)
- Missing `base_url` for `kind: gitlab`
- Unknown `auth_method` values
- `auth_method: token` without a `token_env`
- `autonomous.repositories` entries referencing forge keys that do not exist in `forges:`

### Migration notes

**v1 → v2 (automatic, in-memory)**:

1. Load any v1 config as usual — no changes needed.
2. At load time, if `schema_version` is absent, the Go binary emits a warning and inserts
   a `forges.github` block pointing at `https://github.com` in-memory.
3. The YAML file on disk is **never** rewritten automatically — use `nightgauge config migrate`
   to write the migrated YAML to disk on request.
4. Migration is idempotent — loading a v2 config or calling migrate twice produces the same result.

---

## Schema Migration

Use `nightgauge config migrate` to migrate a v1 configuration file to v2 schema on disk.
The migration uses the `yaml.v3` Node API to preserve comments, blank lines, and key ordering.

### What changes in v1 → v2

| Change                      | Description                                                                     |
| --------------------------- | ------------------------------------------------------------------------------- |
| `schema_version: "2"` added | Inserted at the top of the file                                                 |
| `forges.github` inserted    | Default `kind: github` / `base_url: https://github.com` entry added when absent |

Everything else (project settings, autonomous configuration, pipeline settings, etc.) is preserved verbatim.

### Before migration (v1)

```yaml
project:
  owner: nightgauge
  number: 1
  repo: nightgauge
```

### After migration (v2)

```yaml
schema_version: "2"
project:
  owner: nightgauge
  number: 1
  repo: nightgauge
forges:
  github:
    kind: github
    base_url: https://github.com
```

### Commands

```bash
# Preview changes without writing the file
nightgauge config migrate --dry-run

# Migrate the default config path (.nightgauge/config.yaml)
nightgauge config migrate

# Migrate a custom path
nightgauge config migrate --config /path/to/config.yaml

# Machine-readable output
nightgauge config migrate --json
```

### Idempotency and safety

- Running `config migrate` on an already-v2 file exits cleanly with "already at schema_version 2".
- `--dry-run` prints a unified diff but writes nothing.
- Comments and blank lines in the original file are preserved verbatim.
- If a `forges.github` entry already exists, it is **not** duplicated.
- Post-migration validation runs automatically; if the result is invalid (e.g., unknown forge kind),
  migration is aborted and the original file is unchanged.

---

## Author

nightgauge
