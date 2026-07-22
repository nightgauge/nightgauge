# Gemini CLI Adapter

This directory configures the Gemini adapter for the core Nightgauge
issue-to-PR pipeline stages:

- `issue-pickup`
- `feature-planning`
- `feature-dev`
- `feature-validate`
- `pr-create`
- `pr-merge`

Stages are invoked through the unified entrypoint script
`scripts/run-stage.sh gemini <stage> <issue>`. (The per-tool wrapper scripts that
previously lived under `configs/gemini/commands/` were removed in #1804 — see
`docs/MIGRATION.md`.)

## Why This Exists

The Gemini adapter provides Gemini CLI command ergonomics without duplicating
stage logic. Stage behavior remains in shared skills and SDK orchestration:

- `skills/nightgauge-issue-pickup/SKILL.md`
- `skills/nightgauge-feature-planning/SKILL.md`
- `skills/nightgauge-feature-dev/SKILL.md`
- `skills/nightgauge-feature-validate/SKILL.md`
- `skills/nightgauge-pr-create/SKILL.md`
- `skills/nightgauge-pr-merge/SKILL.md`
- `packages/nightgauge-sdk/src/stages/`

## Runtime Contract

### Required Tools

- `node` (v18+)
- `git`
- `gh` (GitHub CLI)
- `gemini` (Gemini CLI)

### Required Environment

- `NIGHTGAUGE_ADAPTER=gemini` (set automatically by wrappers)
- `NIGHTGAUGE_OUTPUT_FORMAT=json` by default (set by shared entrypoint)
- `NIGHTGAUGE_GEMINI_CLI_ARGS` defaults to `--output-format stream-json`
  via `scripts/run-stage.sh`
- Optional: `NIGHTGAUGE_GEMINI_CLI_COMMAND` to override CLI binary path
- No direct provider API key is required in Gemini adapter mode (uses Google
  Cloud auth)
- Git working tree should be clean before stage execution
- Current branch cannot be `main`/`master` when running Gemini adapter mode

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
scripts/run-stage.sh gemini issue-pickup 554

# Planning
scripts/run-stage.sh gemini feature-planning 554

# Development
scripts/run-stage.sh gemini feature-dev 554

# Validation
scripts/run-stage.sh gemini feature-validate 554

# PR creation
scripts/run-stage.sh gemini pr-create 554

# PR merge
scripts/run-stage.sh gemini pr-merge 554
```

Any extra arguments are passed to `nightgauge-sdk stage`, for example:

```bash
scripts/run-stage.sh gemini feature-dev 554 --model gemini-2.5-pro
```

## Adapter Switching And Fallback

The Gemini stage runner and Claude/Codex commands share the same stage contract
and handoff artifacts. To switch adapters safely:

1. Keep the same issue number and branch.
2. Verify `.nightgauge/pipeline/*.json` and `.nightgauge/plans/*.md`
   exist.
3. Continue at the next stage in the other adapter.

Example switching flow:

```bash
# Gemini stages
scripts/run-stage.sh gemini issue-pickup 554
scripts/run-stage.sh gemini feature-planning 554

# Switch to Claude for later stages
/nightgauge:feature-dev
/nightgauge:pr-create
```

## Gemini CLI Version Compatibility

The adapter and shell entrypoint include preflight version checks that warn (but
do not block) when an older version is detected.

### Auth

Gemini CLI uses Google Cloud authentication (gcloud-based or API key). There is
no CLI-specific `auth status` subcommand. The adapter validates CLI availability
via `gemini --version` only.

### Output Format

Gemini CLI supports `--output-format stream-json` which produces newline-
delimited JSON (NDJSON) events. The adapter parses these structured events to
extract assistant messages and detect failures, similar to the Codex JSONL
pattern.

Prompts are delivered as positional arguments (`gemini "prompt" --flags`).

Event types in the stream-json output:

| Event Type    | Description                                    |
| ------------- | ---------------------------------------------- |
| `init`        | Session metadata (session ID, model)           |
| `message`     | User/assistant message chunks (role, content)  |
| `tool_use`    | Tool call requests with arguments              |
| `tool_result` | Tool execution results (success/error)         |
| `error`       | Non-fatal warnings and system errors           |
| `result`      | Final outcome with status and token statistics |

### Adapter Capabilities

| Capability            | Status | Notes                                           |
| --------------------- | ------ | ----------------------------------------------- |
| `interactive`         | false  | Gemini runs headless via stream-json output     |
| `sessionResume`       | false  | Not available in Gemini CLI                     |
| `streamJson`          | true   | NDJSON events via `--output-format stream-json` |
| `nativeTokenTracking` | false  | Stats available in result events but not parsed |

## Current Parity Status

| Capability Group                                      | Status    | Notes                                          |
| ----------------------------------------------------- | --------- | ---------------------------------------------- |
| Six issue-to-PR stages (`issue-pickup` -> `pr-merge`) | Supported | Gemini wrapper path over shared stage contract |
| Utility commands                                      | Deferred  | Claude-first automation                        |
| Queue/project-sync/backlog orchestration              | Deferred  | Use manual GitHub updates when needed          |

## Known Limitations And Mitigations

| Limitation                                          | Mitigation                                      |
| --------------------------------------------------- | ----------------------------------------------- |
| Missing `packages/nightgauge-sdk/dist/cli/index.js` | Run `npm run -w @nightgauge/sdk build`          |
| No CLI auth status command                          | Validates CLI availability via `--version` only |
| Stream-json format changes                          | Parser handles unknown event types gracefully   |
| No sandbox mode                                     | Not applicable to Gemini CLI                    |
| No native token tracking                            | Token usage not available in text output        |
