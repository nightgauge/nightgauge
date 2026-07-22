# PR Create Stage — Two-Path Architecture

> Status: shipped in PR #3265.
>
> Predecessors: #3264 (deterministic-first pr-merge), epic #3261.

The `pr-create` pipeline stage runs in one of two modes:

1. **Deterministic path** (default) — a Go-native stage runner that reads
   `dev-{N}.json` / `validate-{N}.json` / `issue-{N}.json` /
   `planning-{N}.json`, evaluates a pure decision rule over a typed snapshot,
   renders a PR title and body from a fixed template, pushes the feature
   branch, and calls `internal/github.PRService.CreatePR` directly. Zero LLM
   tokens, zero subagent processes, completes in seconds.
2. **LLM path** (fallback) — the existing `nightgauge-pr-create` skill
   runs via the normal `StageRunner` → Claude pipeline. Reached whenever the
   deterministic runner punts (sparse context, spike, batch, security or
   scope concern, push failure, …).

The skill's downstream contract is unchanged: it writes
`.nightgauge/pipeline/pr-{N}.json` with the same schema today's skill
emits. The deterministic runner produces a superset-compatible payload —
`preflight_results` and `ci_monitoring` are populated with conservative
zero-values that downstream consumers (the pr-merge deterministic runner from
#3264, the VSCode extension's execution-history view) treat as "not run."

## When the deterministic path runs

The runner evaluates this decision matrix (in priority order — first match
wins):

| Snapshot signal                                | Result | Reason                         |
| ---------------------------------------------- | ------ | ------------------------------ |
| `dev-{N}.json` missing or unparseable          | Punt   | `missing-dev-context`          |
| `dev-batch-{E}.json` present                   | Punt   | `batch-mode`                   |
| `issue.type == "spike"`                        | Punt   | `spike-issue`                  |
| Branch missing or equal to base branch         | Punt   | `branch-is-base`               |
| `validate-{N}.json` missing                    | Punt   | `missing-validate-context`     |
| `validation_status != "passed"`                | Punt   | `validation-not-passed: <s>`   |
| `errorCategory != ""`                          | Punt   | `validate-error-category: <c>` |
| Any `dead_code_warnings[].severity == "error"` | Punt   | `dead-code-blocked`            |
| Security scan failed (preflight rerun)         | Punt   | `security-scan-failed`         |
| Scope drift gate failed                        | Punt   | `scope-drift-failed`           |
| Any `manual_checklist[].verified == false`     | Punt   | `manual-checklist-unverified`  |
| `dev.files_changed` empty                      | Punt   | `no-changes`                   |
| Otherwise (rich context)                       | Create | `rich-context`                 |

When the rule says "create," the runner:

1. Calls `prClient.ListOpenPRsForBranch` for idempotency. If an open PR
   already exists for the branch, the runner returns `created` with
   `reason=pr-already-exists` (no second push, no second create call).
2. Pushes the feature branch (`git push -u origin <branch>`). If the push is
   rejected, the runner is **idempotent on the branch** (#3828): it checks
   whether `origin` already has the branch (feature-dev typically pushed it).
   If so, it proceeds to open the PR from the remote branch with
   `reason=push-failed-remote-branch-exists` — it does **not** punt to the LLM
   path, which would attempt a (correctly blocked) force-push and then dead-end
   on `AskUserQuestion` in headless mode. Only when the branch is genuinely
   absent from `origin` (or existence cannot be determined) does it punt with
   `reason=push-failed: <stderr>`.
3. Looks up the repository node ID and calls `prClient.CreatePR(ctx,
repoID, title, body, head, base)`.
4. Writes `pr-{N}.json` with the issue number, PR number, PR URL, title,
   base branch, knowledge path, and zero-value `preflight_results` /
   `ci_monitoring` blocks.

### Title template

```
<typePrefix>(#<N>): <stripped-issue-title>
```

`<typePrefix>` is derived from `issue.type` (feature → `feat`, fix → `fix`,
docs → `docs`, …). `<stripped-issue-title>` removes any leading
`<knownPrefix>(<scope>)?:` segment from the issue title to avoid double-
prefixing PR titles whose issues already carry a conventional-commit prefix.

### Body template

Sections appear in fixed order:

```
## Summary

Implements #<N>: <stripped-title>

## Changes

Created:
- <sorted file list>
Modified:
- <sorted file list>
Deleted:
- <sorted file list>

## Validation

- Build: <passed | failed | skipped>
- Unit tests: <passed | not run> (<P> passed, <F> failed)
- Integration tests: passed             (only when integration tests passed)
- Security scan: <result>                (only when not skipped)
- Scope drift:  <result>                 (only when not skipped)

## Knowledge                              (only when knowledge dir is populated)

<rendered knowledge bullets — same Go renderer the skill calls>

Part of #<PARENT>                         (only when issue.native_parent > 0)
Closes #<N>
```

`RenderTitle` and `RenderBody` are pure functions over `PRCreateSnapshot` —
no `time.Now`, no map iteration, no environment reads, sorted file lists.
The deterministic-property test calls each function 100× and asserts
byte-equal output across calls.

### Failure modes

| Reason                        | Cause                                                      | What happens |
| ----------------------------- | ---------------------------------------------------------- | ------------ |
| `missing-dev-context`         | `dev-{N}.json` missing or unparseable                      | Punt → LLM   |
| `missing-validate-context`    | `validate-{N}.json` missing                                | Punt → LLM   |
| `validation-not-passed: <s>`  | Validation reported `failed` / `partial` / `skipped`       | Punt → LLM   |
| `validate-error-category: ↩`  | Hard-gate failure category recorded by feature-validate    | Punt → LLM   |
| `dead-code-blocked`           | Any error-severity dead-code warning                       | Punt → LLM   |
| `security-scan-failed`        | Preflight security re-scan failed                          | Punt → LLM   |
| `scope-drift-failed`          | type:docs / type:chore scope drift gate failed             | Punt → LLM   |
| `manual-checklist-unverified` | Validation produced an unchecked manual checklist          | Punt → LLM   |
| `spike-issue`                 | `issue.type == "spike"` — defer to skill's spike artifact  | Punt → LLM   |
| `batch-mode`                  | `dev-batch-{E}.json` present                               | Punt → LLM   |
| `no-changes`                  | `dev.files_changed` is empty                               | Punt → LLM   |
| `branch-is-base`              | Current branch equals base                                 | Punt → LLM   |
| `push-failed: …`              | `git push -u origin <branch>` returned non-zero            | Punt → LLM   |
| `pr-client-unavailable`       | Production scheduler did not wire the GitHub client        | Punt → LLM   |
| `create-call-failed: …`       | Pre-flight passed but `CreatePR` returned an error         | Punt → LLM   |
| `context-invalid-json`        | Issue/dev/validate context exists but is malformed JSON    | Punt → LLM   |
| `pr-already-exists`           | (Not a punt) An open PR already exists for the head branch | Created      |
| `rich-context`                | (Not a punt) New PR was created                            | Created      |

### Context resolution (worktree mode) — #275

The runner reads its snapshot from
`<workdir>/.nightgauge/pipeline/{issue,dev,validate,planning}-{N}.json`. On
worktree-isolated runs (`pipeline.worktree_base` set — the autonomous/`nightgauge
run` default), the stages write those files **only** into the run's worktree
(`.worktrees/issue-{N}/.nightgauge/pipeline/`); they are gitignored per-worktree
local state and never appear in the canonical repo root. The scheduler therefore
hands the runner the **worktree** path via `stageWorkspace(runtime,
workspaceRoot)` — the same resolution the LLM path and the post-condition gates
use — not the bare `workspaceRoot`.

Before #275 the deterministic dispatch passed `workspaceRoot` directly, so on any
worktree-configured repo the runner found no `dev-{N}.json`, `DecideCreate`
returned `missing-dev-context`, and **every** run fell through to the ~$2 LLM
path (bowlsheet/bowlsheet-flutter were 0-for-N). The identical fix applies to the
pr-merge runner (reads `pr-{N}.json`, which pr-create writes into the worktree)
and to the recovery registry's `StageFailure.Workspace` (which re-runs both
runners and drives git-op recovery actions). For in-place runs with no worktree
(`runtime.WorktreeDir == ""`, e.g. VSCode/headless), `stageWorkspace` returns
`workspaceRoot` unchanged, so behavior for the non-worktree majority is
byte-identical.

## When the LLM path runs

The skill at `skills/nightgauge-pr-create/SKILL.md` runs unchanged when:

- The deterministic runner punts (any of the reasons above).
- The runner's GitHub client is not wired (production safety: every Run punts
  with `pr-client-unavailable` until the scheduler injects the client).

> **TS (VSCode dogfood) path — Issue #300.** The VSCode
> autonomous/concurrent runs execute `HeadlessOrchestrator.runPipeline`, not the
> Go scheduler, so pre-#300 pr-create always ran the LLM skill there. It now
> runs deterministic-first via `nightgauge pr-stage create` — the same runner
> this doc describes, constructed by `orchestrator.NewDefaultPRCreateRunner` —
> invoked with `--repo owner/name --workdir <worktree>`. On `created` the TS
> orchestrator records `execution_path="deterministic"` and skips the skill; on a
> punt it records `execution_path="llm"` + `punt_reason` and falls through to the
> skill; a GitHub rate-limit **defers** (no LLM fallthrough, #3976). See
> [PR_MERGE_STAGE.md](PR_MERGE_STAGE.md) for the shared seam and JSON contract.

The skill is responsible for content the deterministic path explicitly does
not author:

- Reviewer assignment (per-issue config; runner punts when reviewer logic is
  required and the configured reviewer differs from the PR author).
- CI monitoring with classified failures (Phase 3.5 of the skill).
- Spike artifact authoring (`type:spike` issues bypass the runner).
- Batch / cross-repo PR bodies.

## Telemetry

Per-stage `execution_path` is recorded on `V2StageDetail` (Go) /
`HistoryStageDetail` (TS Zod). Values:

- `"deterministic"` — Go runner reported `created`.
- `"llm"` — Go runner punted; skill ran.
- _absent_ — record predates PR #3264; readers MUST treat as `unknown`.

`execution_path` is part of the local history record. Optional telemetry
integrations receive only fields present in their documented public schema; the
local value must not be assumed to upload automatically.

When the deterministic path lands a create, the scheduler emits a
`stage_deterministic` `pipeline_event` with metadata identical in shape to
the pr-merge variant so the dashboard panel groups across stage names:

```json
{
  "path": "created",
  "pr_number": 3265,
  "pr_url": "https://github.com/owner/repo/pull/3265",
  "reason": "rich-context",
  "duration_ms": 1234
}
```

## Post-stage gate

The skill's existing post-stage activity (writing `pr-{N}.json`, signaling
project-board move-status) runs after either path. The deterministic runner
writes `pr-{N}.json` with the same schema, so the downstream pr-merge
deterministic runner from #3264 (`readPRContextNumber`) reads it unchanged.

There is no separate "verify created" post-stage gate analogous to
`verifyPRMerged` — pr-create is itself a single-step state transition. The
LLM path's CI-monitoring loop is preserved when the runner punts.

## Decisions (ADRs)

The full ADR set lives in
`.nightgauge/knowledge/features/3265-deterministic-first-pr-create-stage-template/decisions.md`.
Highlights:

- **ADR-001** Deterministic path is the default; LLM is the punt fallback.
  Cost per pr-create run drops from ~$1–$3 to ~$0 for the rich-context
  majority. The skill remains the canonical path for spike/batch/sparse runs.
- **ADR-002** `execution_path` is per-stage, not per-run (mirrors #3264 ADR-002).
  Forward compatible for the next deterministic stage.
- **ADR-003** Decision rule is a pure function over `PRCreateSnapshot`,
  exhaustively unit-tested separately from the GitHub shell-out.
- **ADR-004** Title and body renderers are pure — no `time.Now`, sorted
  file lists, byte-equal output across repeated calls. The
  deterministic-property test asserts this directly.
- **ADR-005** Reviewer assignment punts to the LLM path (per-issue config is
  not derivable from rich-context alone). Bringing reviewer logic into the Go
  runner is a follow-up.

## Migration notes

- Behavior is unchanged for spike, batch, or sparse runs (the LLM path
  receives the same input it always did).
- For clean-context runs that previously cost ~$1–$3, the same PR now
  lands in ~1 s with zero tokens.
- `V2StageDetail.execution_path` is additive — older daily JSONLs parse
  unchanged; readers see absence as "unknown" rather than a value.
- The skill stays the source of truth for CI monitoring, reviewer
  assignment, and spike-artifact authoring.
