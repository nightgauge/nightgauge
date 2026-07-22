# Codex CLI Adapter And Slash Commands

This directory configures the Codex adapter and slash commands for the core
Nightgauge issue-to-PR pipeline stages:

- `issue-pickup`
- `feature-planning`
- `feature-dev`
- `feature-validate`
- `pr-create`
- `pr-merge`

Stages are invoked through the unified entrypoint script
`scripts/run-stage.sh codex <stage> <issue>`. (The per-tool wrapper scripts that
previously lived under `configs/codex/commands/` were removed in #1804 — see
`docs/MIGRATION.md`.)

Codex slash commands for these stages are defined in custom command files under
`.codex/commands/` and invoked as `/<name>`.

Install/update Codex skills (`~/.codex/skills/`, invoked as `$nightgauge-<name>`)
from the local working tree with:

```bash
./scripts/install-agent-skills.sh --codex-only
```

This also runs automatically as part of the VS Code extension `dev-install.sh`,
which refreshes the Claude Code plugins and Codex skills alongside the extension.

## Why This Exists

The Codex adapter provides Codex CLI command ergonomics without duplicating
stage logic. Stage behavior remains in shared skills and SDK orchestration:

- `skills/nightgauge-issue-pickup/SKILL.md`
- `skills/nightgauge-feature-planning/SKILL.md`
- `skills/nightgauge-feature-dev/SKILL.md`
- `skills/nightgauge-feature-validate/SKILL.md`
- `skills/nightgauge-pr-create/SKILL.md`
- `skills/nightgauge-pr-merge/SKILL.md`
- `packages/nightgauge-sdk/src/stages/`

## Migration Status

The per-tool wrapper scripts under `configs/codex/commands/*.sh` were removed in
#1804. Codex stages now run through the unified `scripts/run-stage.sh` entry
point, positioned as an invocation adapter over the shared cross-tool capability
contract.

- Canonical contract: `docs/strategy/CROSS_TOOL_COMMAND_CONTRACT.md`
- Removal history and adapter mapping: `docs/MIGRATION.md`

This keeps adapter ergonomics while preventing behavior drift between Codex,
Claude, Copilot, and Cursor.

## Runtime Contract

### Required Tools

- `node` (v18+)
- `git`
- `gh` (GitHub CLI)
- `codex` (Codex CLI 0.111.0+)

### Required Environment

- GitHub auth must be available in the same execution context as the stage: use
  `GH_TOKEN`/`GITHUB_TOKEN` (recommended for sandboxed runs), or ensure
  `gh auth status` succeeds there
- `NIGHTGAUGE_ADAPTER=codex` (set automatically by wrappers)
- `NIGHTGAUGE_OUTPUT_FORMAT=json` by default (set by shared entrypoint)
- `NIGHTGAUGE_CODEX_CLI_ARGS` defaults to
  `exec --full-auto --sandbox danger-full-access` via `scripts/run-stage.sh` to
  allow `.git` ref updates during branch operations
- Optional override: set `NIGHTGAUGE_CODEX_SANDBOX_MODE=workspace-write`
  (or provide full `NIGHTGAUGE_CODEX_CLI_ARGS`) if you need stricter
  sandboxing
- No direct provider API key is required in Codex adapter mode
- Git working tree should be clean before stage execution
- Current branch cannot be `main`/`master` when running Codex adapter mode

### Required Build Output

The stage runner executes the compiled CLI entrypoint:

- `packages/nightgauge-sdk/dist/cli/index.js`

If missing, build it from repository root:

```bash
npm run -w @nightgauge/sdk build
```

## Commands

Run from repository root:

```bash
# Pickup
scripts/run-stage.sh codex issue-pickup 554

# Planning
scripts/run-stage.sh codex feature-planning 554

# Development
scripts/run-stage.sh codex feature-dev 554

# Validation
scripts/run-stage.sh codex feature-validate 554

# PR creation
scripts/run-stage.sh codex pr-create 554

# PR merge
scripts/run-stage.sh codex pr-merge 554
```

## Slash Commands

From an interactive Codex session opened at repository root:

```text
/nightgauge-issue-pickup 554
/nightgauge-feature-planning 554
/nightgauge-feature-dev 554
/nightgauge-feature-validate 554
/nightgauge-pr-create 554
/nightgauge-pr-merge 554
```

These custom commands are defined in `.codex/commands/*.md` and route to the
same `scripts/run-stage.sh` entry point listed above.

Any extra arguments are passed to `nightgauge-sdk stage`, for example:

```bash
scripts/run-stage.sh codex feature-dev 554 --model sonnet
```

## Adapter Switching And Fallback

The Codex stage runner and Claude commands share the same stage contract and
handoff artifacts. To switch adapters safely:

1. Keep the same issue number and branch.
2. Verify `.nightgauge/pipeline/*.json` and `.nightgauge/plans/*.md`
   exist.
3. Continue at the next stage in the other adapter.

Example switching flow:

```bash
# Codex stages
scripts/run-stage.sh codex issue-pickup 554
scripts/run-stage.sh codex feature-planning 554

# Switch to Claude for later stages
/nightgauge:feature-dev
/nightgauge:pr-create
```

Fallback flow when a stage fails:

```bash
# 1) Restore build output
npm run -w @nightgauge/sdk build

# 2) Validate stage contract parity
npx -w @nightgauge/sdk vitest run tests/cli/stageParity.test.ts
```

If stage execution still fails after these checks, run the remaining stages in
Claude on the same branch.

## Codex CLI Version Compatibility

The minimum known compatible version is Codex CLI 0.111.0. The adapter and
shell entrypoint warn (but do not block) when an older version is detected.
Newer CLI releases are covered by automated command-contract tests; Codex
remains a beta adapter until a representative issue has completed the recorded
live six-stage provider matrix.

### Auth Commands

Only `codex login status` is valid for auth checking in Codex CLI 0.98+:

- `codex login status` — **Valid** (used by adapter)
- `codex auth status` — **Invalid** (no `auth` subcommand exists)
- `codex login --status` — **Invalid** (`--status` is not a recognized flag)

This is the auth command used by the current adapter.

### Sandbox Modes

Pipeline stages derive sandbox flags from the skill's `allowed-tools`. Read-only
skills use `--sandbox read-only`; edit-only skills use
`--sandbox workspace-write`; stages requiring shell, network, task, or MCP tools
use `--dangerously-bypass-approvals-and-sandbox`. Autonomous execution always
uses a non-interactive approval policy.

Available sandbox modes: `danger-full-access`, `workspace-write`, `read-only`.

### JSON Output Format

Codex CLI `--json` produces JSONL events with these types:

- `item.completed` with `item.type: "agent_message"` — agent text responses
- `item.completed` with `item.type: "command_execution"` — shell command results
- `turn.completed` — end of turn marker

Current Codex JSONL includes token usage on `turn.completed.usage`. Nightgauge
parses `input_tokens`, `cached_input_tokens`, and `output_tokens`. The CLI does
not provide provider USD cost; Nightgauge derives cost from model pricing and
observed tokens.

### Adapter Capabilities

| Capability            | Status | Notes                                                               |
| --------------------- | ------ | ------------------------------------------------------------------- |
| `interactive`         | false  | Codex exec runs headless                                            |
| `sessionResume`       | true   | Opt-in `exec resume`; set `NIGHTGAUGE_CODEX_RESUME_ENABLED=true`    |
| `streamJson`          | true   | `--json` produces JSONL event stream                                |
| `nativeTokenTracking` | true   | Parses token usage from `turn.completed.usage`; USD cost is derived |
| `fastMode`            | defer  | Model tiers are routed through the canonical Codex model registry   |

### Investigated But Not Adopted

The following CLI patterns were evaluated during the beta and GA maturation
cycle but were not adopted in the current adapter implementation:

| Pattern                           | Decision      | Reason                                                                                            |
| --------------------------------- | ------------- | ------------------------------------------------------------------------------------------------- |
| `codex exec --prompt-file <path>` | Not adopted   | Early Go adapter prototype used this; replaced by `exec` + JSONL stream in the TypeScript adapter |
| Per-tool allowlist flags          | Not available | Nightgauge maps `allowed-tools` to sandbox and approval policy instead                            |
| Native provider USD cost          | Not available | Nightgauge derives cost from observed tokens and its model pricing table                          |

## Current Parity Status (March 2026 — Post-GA Adoption)

| Capability Group                                         | Status    | Notes                                                          |
| -------------------------------------------------------- | --------- | -------------------------------------------------------------- |
| Six issue-to-PR stages (`issue-pickup` -> `pr-merge`)    | Supported | Stable Codex wrapper path over shared stage contract           |
| Utility commands (`test-gen`, `issue-create`, `doc-gen`) | Beta      | Not required for core issue-to-PR completion                   |
| Queue/project-sync/backlog orchestration                 | Deferred  | Claude-first automation; use manual GitHub updates when needed |

## Known Limitations And Mitigations

| Limitation                                                       | Mitigation                                                                                 |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| Missing `packages/nightgauge-sdk/dist/cli/index.js` build output | Run `npm run -w @nightgauge/sdk build`                                                     |
| Adapter drift between Codex and shared stage contract            | Run `npx -w @nightgauge/sdk vitest run tests/cli/stageParity.test.ts` before pipeline runs |
| Claude-only queue/project-sync automation                        | Perform manual GitHub board updates for Codex workflows                                    |

## Implementation Notes

- `scripts/run-stage.sh codex <stage> <issue>` invokes the SDK stage runner.
- The unified entrypoint sets `NIGHTGAUGE_ADAPTER=codex` and defaults
  `NIGHTGAUGE_OUTPUT_FORMAT` to `json`.
- Codex preflight checks execute in `nightgauge-sdk` command handlers.

## Parity Regression Validation

Run this from repository root to verify core Codex/Claude stage contract parity:

```bash
npx -w @nightgauge/sdk vitest run tests/cli/stageParity.test.ts
```
