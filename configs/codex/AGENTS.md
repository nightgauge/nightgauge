# AGENTS.md - Codex Nightgauge Adapter

This file configures Codex usage for Nightgauge pipeline stages.

## Scope

Use the unified stage runner for Codex CLI execution of Nightgauge
issue-to-PR stages — `scripts/run-stage.sh codex <stage> <issue>`:

1. `scripts/run-stage.sh codex issue-pickup <issue>`
2. `scripts/run-stage.sh codex feature-planning <issue>`
3. `scripts/run-stage.sh codex feature-dev <issue>`
4. `scripts/run-stage.sh codex feature-validate <issue>`
5. `scripts/run-stage.sh codex pr-create <issue>`
6. `scripts/run-stage.sh codex pr-merge <issue>`

Codex custom slash commands for these stages are defined in:

- `.codex/commands/nightgauge-issue-pickup.md`
- `.codex/commands/nightgauge-feature-planning.md`
- `.codex/commands/nightgauge-feature-dev.md`
- `.codex/commands/nightgauge-feature-validate.md`
- `.codex/commands/nightgauge-pr-create.md`
- `.codex/commands/nightgauge-pr-merge.md`

## Pipeline Contract

- Stage logic lives in shared skills under `skills/`.
- The stage runner must not re-implement stage behavior.
- Context artifacts are written in `.nightgauge/pipeline/`.
- Plans are written in `.nightgauge/plans/`.

## Required Runtime

- `node`, `git`, `gh` installed and available in `PATH`
- GitHub auth must be available in the current execution context:
  `GH_TOKEN`/`GITHUB_TOKEN` preferred for sandboxed runs, or `gh auth status`
  must pass in the same context
- The Codex stage runner defaults to
  `NIGHTGAUGE_CODEX_CLI_ARGS=\"exec --full-auto --sandbox danger-full-access\"`
  so branch operations can update `.git` refs; override with
  `NIGHTGAUGE_CODEX_SANDBOX_MODE` or `NIGHTGAUGE_CODEX_CLI_ARGS` when needed
- Feature branch checked out (not `main`/`master`)
- Codex adapter mode uses CLI auth preflight and does not require a direct
  provider API key

## Usage

From repository root:

```bash
scripts/run-stage.sh codex issue-pickup <issue-number>
scripts/run-stage.sh codex feature-planning <issue-number>
scripts/run-stage.sh codex feature-dev <issue-number>
scripts/run-stage.sh codex feature-validate <issue-number>
scripts/run-stage.sh codex pr-create <issue-number>
scripts/run-stage.sh codex pr-merge <issue-number>
```

For more details, see `configs/codex/README.md`.
