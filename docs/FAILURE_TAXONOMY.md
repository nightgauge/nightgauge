# Failure Taxonomy

> Classification of pipeline failure outcomes for weighted reliability scoring.
>
> **Issue #1260** — Classify infrastructure vs. organic failures in the health
> reliability component.

---

## Overview

Not all pipeline failures are equal. A schema validation error that prevents a
stage from starting is not the same as a test failure caused by a bug in the
implementation. Treating them identically depresses the health score even when
the underlying code quality is fine.

The failure taxonomy classifies each failed pipeline stage into one of three
categories and applies a differential weight in the **reliability** health
dimension. This means infrastructure and transient failures have minimal impact
on the score, while true implementation failures (organic) carry full weight.

---

## Categories

### `infrastructure`

**Weight: 0.05 (5%)**

Failures caused by the pipeline tooling or runtime environment rather than
implementation quality. These indicate that the pipeline itself — not the code
being automated — needs attention.

**Patterns detected:**

| Pattern                      | Example                                                                                                                                                                                                                                                                   |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `schema validation`          | Context file fails Zod schema parse                                                                                                                                                                                                                                       |
| `pre-condition failed`       | Stage pre-condition guard triggered                                                                                                                                                                                                                                       |
| `context file`               | Context handoff file missing or unreadable                                                                                                                                                                                                                                |
| `enoent`                     | File or directory not found (Node.js I/O)                                                                                                                                                                                                                                 |
| `eacces`                     | Permission denied on file access                                                                                                                                                                                                                                          |
| `eperm`                      | Operation not permitted                                                                                                                                                                                                                                                   |
| `invalid json`               | JSON parse error on context file                                                                                                                                                                                                                                          |
| `extension lifecycle`        | VSCode extension activation/deactivation error                                                                                                                                                                                                                            |
| `failed to read`             | Generic read failure on required file                                                                                                                                                                                                                                     |
| `cannot read`                | Property access on undefined/null file data                                                                                                                                                                                                                               |
| `pipeline state`             | Pipeline state file corrupt or missing                                                                                                                                                                                                                                    |
| `[cost-cap-exceeded]`        | Stage killed by `pipeline.stage_cost_caps` (Issue #3002)                                                                                                                                                                                                                  |
| `cost cap exceeded`          | Stage killed by `pipeline.stage_cost_caps` (Issue #3002)                                                                                                                                                                                                                  |
| `AC_ALREADY_SATISFIED`       | Deterministic AC reconciliation pre-flight gate found work already merged (Issue #3003). Pipeline correctly short-circuited; not an organic failure.                                                                                                                      |
| `[baseline-ci-deferred]`     | Baseline-CI dependency gate deferred dispatch — `main`'s recent runs of a referenced workflow are failing (Issue #3004). Pipeline correctly held; not a failure. Auto-promoted by daily `baseline-defer-sweep` cron when the baseline goes green.                         |
| `baseline ci deferred`       | Same gate, free-text variant.                                                                                                                                                                                                                                             |
| `baseline-ci red`            | Same gate, summary line variant emitted by the deferral comment + outcome record.                                                                                                                                                                                         |
| `[blocked-dependency]`       | Native-blockedBy dependency gate deferred issue pickup — the issue has an OPEN `blockedBy` dependency (blocker's PR not merged, Issue #231). A controlled hold, not a failure. Auto-requeued by `deps-gate promote` (and the autonomous cascade) when the blockers close. |
| `blocked by open dependency` | Same gate, summary line variant emitted by the deferral comment + outcome record.                                                                                                                                                                                         |

### `agent`

**Weight: 0.5 (50%)**

Transient or recoverable failures on the AI agent side. These are genuine
failures worth tracking but typically resolve on retry and do not reflect code
quality problems.

**Patterns detected:**

| Pattern                    | Example                                                             |
| -------------------------- | ------------------------------------------------------------------- |
| `timeout` / `etimedout`    | Claude API request timed out                                        |
| `rate limit`               | API rate limit exceeded                                             |
| `503` / `502` / `504`      | Transient HTTP gateway errors                                       |
| `context exhausted`        | Stage exceeded context window                                       |
| `token limit`              | Token budget exhausted                                              |
| `maximum context`          | Context length limit reached                                        |
| `api error`                | Generic Claude/Anthropic API error                                  |
| `overloaded`               | API overloaded, service degraded                                    |
| `stall kill threshold`     | Subagent stalled and was killed by the stall watchdog (Issue #2871) |
| `stalled and killed`       | Same — alternate phrasing emitted by the watchdog                   |
| `heartbeat stall`          | Heartbeat-based stall detection (IPC mode)                          |
| `stall-killed-after-retry` | Second stall after adaptive retry exhausted (Issue #3005)           |

### `organic`

**Weight: 1.0 (100%) — the default**

True implementation failures: bugs, test failures, build errors, and any other
outcome where the implementation itself is the root cause. This is the
conservative default — any failure that does not match infrastructure or agent
patterns is treated as organic.

**Examples:**

- TypeScript type errors (`tsc` fails)
- Test failures (`vitest` reports failures)
- Build errors in application code
- Acceptance criteria not met
- Unrecognized error messages

---

## Weighting Rationale

| Category         | Weight | Rationale                                                                                                                                                                                               |
| ---------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `infrastructure` | 5%     | These failures are pipeline tooling bugs, not code quality regressions. Excluding them entirely would hide systemic tooling issues; a 5% weight keeps them visible in trends without tanking the score. |
| `agent`          | 50%    | Transient failures are real events that affect developer experience and should appear in the score, but they're often not actionable from a code perspective. 50% balances visibility with fairness.    |
| `organic`        | 100%   | A failing test or build error is always actionable. Full weight ensures the score accurately reflects implementation quality.                                                                           |

---

## Effect on Health Score

The reliability health dimension uses `weightedFailureRate` instead of raw
`failureRate` for:

1. **Score base**: `score = (1 - weightedFailureRate) * 100`
2. **Finding threshold**: High failure rate finding fires when
   `weightedFailureRate > 0.2` (was previously `failureRate > 0.2`)

Raw `failureRate` and `failureCount` are still available in the metrics object
for informational purposes (e.g., MTBF calculation, trend analysis, stage
concentration detection).

### Example

A pipeline with 10 stage executions, 3 of which failed:

| Failure                       | Category         | Weight | Contribution |
| ----------------------------- | ---------------- | ------ | ------------ |
| Schema validation error       | `infrastructure` | 0.05   | 0.05         |
| Claude API timeout            | `agent`          | 0.5    | 0.50         |
| Feature-validate test failure | `organic`        | 1.0    | 1.00         |

```
weightedFailureCount = 0.05 + 0.50 + 1.00 = 1.55
weightedFailureRate  = 1.55 / 10 = 0.155 (15.5%)
score (base)         = (1 - 0.155) * 100 = 84.5
```

Without classification, raw `failureRate = 3/10 = 30%` would yield a base score
of 70.

---

## Implementation

The classifier lives in the SDK layer so it is importable from both the VSCode
extension writer and any future SDK-internal callers:

```
packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts
```

### Data Flow

```
Pipeline stage fails
     ↓
executionHistoryWriter.buildRunRecord()
     ↓ classifyFailureCategory(stageState.error, stageName)
stage record written to JSONL with failure_category field
     ↓
PostPipelineAnalyzer.adaptRecords()
     ↓ maps failure_category → ExecutionHistoryRecord
analyzeReliability()
     ↓ uses failure_category for weighted scoring
Reliability health score
```

---

## Extending Classification Patterns

To add new patterns, edit `failureClassifier.ts` and add substring patterns to
the appropriate category block. All matching is **case-insensitive**
(`toLowerCase()`).

**Guidelines:**

- Add infrastructure patterns for errors that originate in the pipeline tooling
  (file I/O, schema validation, extension internals)
- Add agent patterns for errors that originate in the AI API layer (network,
  rate limits, context window)
- When in doubt, leave the error unclassified — it defaults to `organic`
  (conservative)
- Add tests in `failureClassifier.test.ts` for every new pattern

---

## Schema

`failure_category` is an **optional** field on `HistoryStageDetailSchema`
(VSCode JSONL schema) and `ExecutionHistoryRecord` (SDK analysis type).

Existing JSONL records without this field parse fine — the optional field
normalizes to `undefined`, which the classifier treats as `organic` at analysis
time. No schema version bump was required.

```typescript
// packages/nightgauge-vscode/src/schemas/executionHistory.ts
failure_category: z.enum(['infrastructure', 'agent', 'organic']).optional(),

// packages/nightgauge-sdk/src/analysis/types.ts
failure_category?: 'infrastructure' | 'agent' | 'organic';
```

---

## Terminal Failure Kind (Issue #3001)

`failure_category` answers **"who/what is to blame for the failure"** for
weighted reliability scoring. `terminal_failure_kind` answers a different
question: **"what aborted the run"**. The two are independent — a single
record may carry both fields, neither, or only one.

### Values

| Kind                         | Meaning                                                                                                                                          |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `stall_kill`                 | Subagent exceeded `stall_kill_multiplier × stall_thresholds` and was forcibly killed                                                             |
| `budget_exceeded`            | Pipeline-level or per-stage **token** budget ceiling tripped (with grace buffer applied)                                                         |
| `validation_error`           | Context schema validation failed terminally (e.g., missing output context for the stage)                                                         |
| `subagent_crash`             | Subagent process exited non-zero with no recovery path (model escalation exhausted)                                                              |
| `orchestrator_crash`         | Orchestrator process died mid-stage; record synthesized on next startup from a stale `current-run.json` sidecar                                  |
| `network_unavailable`        | Extended GitHub connectivity loss aborted the run (Issue #3296) — environmental                                                                  |
| `stream_idle_timeout`        | Anthropic API closed a streaming response mid-flight (Issue #3398) — environmental                                                               |
| `rate_limit_quota_exhausted` | Idle stall fired while the rate-limit bucket was drained (Issue #3386) — environmental                                                           |
| `worktree_uncommitted`       | Failure **recovered**: uncommitted work was auto-committed before cleanup (Issue #3542)                                                          |
| `budget_ceiling_hit`         | The USD pipeline budget ceiling killed a running stage (Issue #3542) — real spend, not a defect                                                  |
| `github_quota_low`           | GitHub API rate-limit bucket below headroom at the pipeline-start preflight (Issue #3896) — environmental                                        |
| `api_connection_lost`        | Anthropic API transport drop mid-stage (socket close / DNS blip; Issue #4002) — environmental                                                    |
| `github_network_outage`      | api.github.com unreachable at the pipeline-start preflight (Issue #4002) — environmental                                                         |
| `model_unavailable`          | API rejected the selected model: not on plan / unknown ID / model usage cap (Issue #42) — triggers tier fallback                                 |
| `premature_turn_end`         | Stage exited 0 but produced no state change — the agent ended its turn on a promise (Issue #74)                                                  |
| `adapter_auth_failed`        | Pipeline-start adapter auth gate refused to launch: probe timed out after retry, or the adapter CLI is logged out (Issue #312) — retryable infra |
| `no_changes_produced`        | pr-create's deterministic fallback confirmed zero commits ahead of base — genuinely nothing to open a PR for (Issue #317) — planning/scope       |
| `validation_failed`          | feature-validate honestly failed its quality gates (`validation_status="failed"`) — organic implementation failure (Issue #326)                  |

`validation_failed` (Issue #326) is an **organic** kind — a true
implementation failure, full (1.0) weight. feature-validate exits 0 even when
its quality gates fail: it writes `validation_status: "failed"` (+ an
`errorCategory`) and deliberately leaves the code uncommitted on disk for
retry rather than exiting non-zero, delegating the halt decision to the
orchestrator (`HeadlessOrchestrator.verifyPostValidateState`). Pre-fix,
`ClassifyTerminalKind` had no matcher for this stage-gate message, so it fell
through to the generic `subagent_crash` fallback — this taxonomy doc already
declared "Feature-validate test failure → organic, weight 1.0" (see the
Example table under Weighting Rationale, above), but the classifier never
implemented the matcher.
Emitted with the `[validation-failed]` marker embedded in the failure text
(mirrors the `[adapter-auth-failed]` / `[no-changes-produced]` marker
pattern). Unlike `no_changes_produced` and `adapter_auth_failed`,
`validation_failed` needs no dedicated `classifyFailureCategory` block —
`organic` is that function's default fallthrough, the same path
`subagent_crash` relies on. Routes through the ordinary failure path (counts
toward `LifetimeIssueFailures`, feeds the cascade breaker like any other real
failure) — only its reported kind differs from `subagent_crash`; the
reliability weight is the same 1.0.

`no_changes_produced` (Issue #317) is a **planning/scope** kind, not
infrastructure. A human-only issue (labeled `owner-action`: work only an
operator can do, e.g. rotating a cloud credential in a provider dashboard) was
dispatched before the exclusion existed; the pipeline ran issue-pickup →
planning → feature-dev → validate, which CORRECTLY produced zero commits
(there was no code to write), and then failed at pr-create — the deterministic
create fallback confirmed the feature branch has no commits ahead of base and
declined to fabricate a PR. Pre-fix that fell through to the generic
`subagent_crash` fallback, overstating a correct no-op as a process crash and
counting it at full (1.0) reliability weight. The defect was dispatching a
human-only issue at all (fixed separately by `autonomous.exclude_labels`, see
docs/AUTONOMOUS_ORCHESTRATOR.md); this kind exists as the honest classification
for whenever a run still reaches this state (e.g. a custom exclude-label list
that misses a repo's own human-only convention). Emitted with the
`[no-changes-produced]` marker embedded in the failure text — deliberately not
matched on the bare phrases "pr context file missing" (also produced by a
genuine crash-before-write) or "no commits ahead of" (also produced by
feature-validate's unrelated lost-implementation check, which must keep its
organic classification). Routes through the ordinary failure path (counts
toward `LifetimeIssueFailures`, feeds the cascade breaker like any other
real failure) — only its reported kind and reliability weight differ from
`subagent_crash`; see the category table below.

`adapter_auth_failed` (Issue #312) is a **retryable-infra** kind. The
pipeline-start auth gate probes each adapter's `claude auth status`; under a
concurrent dispatch burst (autonomous restart fanning out N runs in seconds)
cold probes can lose the CPU race and time out at 5s even though auth is fine.
The gate now dedups probes process-wide (single-flight + short-TTL cache, so a
4-slot burst costs one spawn) and retries a timed-out probe once before failing.
A failure — whether a post-retry timeout or a definitive logged-out negative —
routes like the other transient kinds: short backoff, board → Ready, **no
`LifetimeIssueFailures` increment, no cascade-breaker feed, no pause**. The
timeout-vs-logged-out distinction is carried in the human-readable reason; both
share the terminal kind. `worktree_uncommitted` and `budget_ceiling_hit` are
**recoverable** kinds: the
autonomous scheduler does not increment `LifetimeIssueFailures` for them and the
Go scheduler skips the board-status revert — the issue is re-dispatchable.

### Relationship to `failure_category`

| Terminal Kind                | Typical `failure_category` (heuristic)                                                      |
| ---------------------------- | ------------------------------------------------------------------------------------------- |
| `stall_kill`                 | `agent` — transient runtime issue                                                           |
| `budget_exceeded`            | `agent` — call-pattern under operator control                                               |
| `validation_error`           | `infrastructure` — pipeline contract failed                                                 |
| `subagent_crash`             | `organic` — implementation failure, full weight                                             |
| `orchestrator_crash`         | `infrastructure` — ours, not the model's                                                    |
| `network_unavailable`        | `infrastructure` — environmental, excluded from calibration                                 |
| `stream_idle_timeout`        | `infrastructure` — upstream API, not the issue                                              |
| `rate_limit_quota_exhausted` | `infrastructure` — upstream API quota, not the issue                                        |
| `github_quota_low`           | `infrastructure` — GitHub API quota, not the issue                                          |
| `api_connection_lost`        | `infrastructure` — local network/transport, not the issue                                   |
| `github_network_outage`      | `infrastructure` — local network/transport, not the issue                                   |
| `model_unavailable`          | `infrastructure` — plan/limit environment, not the issue                                    |
| `premature_turn_end`         | `agent` — the agent's turn-ending behavior, not the issue                                   |
| `adapter_auth_failed`        | `infrastructure` — probe starvation / credential state, not the issue                       |
| `no_changes_produced`        | `agent` — planning/scope failure (dispatch-eligibility gap), not the model's implementation |
| `validation_failed`          | `organic` — true implementation failure caught by feature-validate's own quality gate       |
| `worktree_uncommitted`       | recoverable — work preserved, not counted as a failure                                      |
| `budget_ceiling_hit`         | recoverable — real spend, not a code defect                                                 |

The Go scheduler classifies the kind on every terminal-failure path; the
synthesized record always carries it. Older V2 records (pre-#3001) have no
`terminal_failure_kind`; dashboards group those into an `(unknown)` bucket
without forcing a JSONL migration. See ADR-002 in
`.nightgauge/knowledge/features/3001-preserve-pipeline-queue-state-on-terminal-failure/decisions.md`.

### Schema marker (V3)

`terminal_failure_kind` only appears on `ExecutionHistoryRunRecordV3Schema`.
Writers bump `schema_version` from `"2"` to `"3"` whenever the field (or any
per-stage `last_output_lines`) is populated. Readers use a Zod union (V1 ∪ V2 ∪
V3) so legacy records remain valid — there is no migration step.

```typescript
// packages/nightgauge-vscode/src/schemas/executionHistory.ts
export const TerminalFailureKindSchema = z.enum([
  "stall_kill",
  "budget_exceeded",
  "validation_error",
  "subagent_crash",
  "orchestrator_crash",
  "network_unavailable", // Issue #3296
  "stream_idle_timeout", // Issue #3398
  "rate_limit_quota_exhausted", // Issue #3386
  "worktree_uncommitted", // Issue #3542
  "budget_ceiling_hit", // Issue #3542
  "issue_closed", // Issue #3661
  "api_overloaded", // Issue #3835
  "github_quota_low", // Issue #3896
  "api_connection_lost", // Issue #4002
  "github_network_outage", // Issue #4002
  "model_unavailable", // Issue #42
  "premature_turn_end", // Issue #74
  "adapter_auth_failed", // Issue #312
  "no_changes_produced", // Issue #317
  "validation_failed", // Issue #326
]);

export const ExecutionHistoryRunRecordV3Schema = ExecutionHistoryRunRecordV2Schema.extend({
  schema_version: z.literal("3"),
  terminal_failure_kind: TerminalFailureKindSchema.optional(),
});
```

The Go mirror lives in `internal/orchestrator/failure_handler.go` (constants
`TerminalKind*`) and the classifier in
`packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts`
(`classifyTerminalKind`). When changing the enum, update **all three** in
lockstep — the `TerminalFailureKindSchema` test in
`packages/nightgauge-vscode/tests/views/dashboard/FailedRun.test.ts`
guards against drift.

---

## Informational Outcomes

Some pipeline events are not failures but are worth surfacing in dashboards
and trend analysis. They are emitted as log markers and telemetry events
rather than as `terminal_failure_kind` values, so they don't reduce the
reliability score.

### `STALL_RETRIED` (Issue #3005)

A run that hit a stall-kill, rewound to `feature-planning` once via adaptive
stall-recovery, and then completed successfully on the retry. Logged with the
literal token `STALL_RETRIED` and emitted as a `stall_retried` telemetry event
with metadata `{ signal_type, target_stage, killed_stage, retry_count }`.

The recovered run is recorded with `outcome: complete` — its successful
completion is not a failure. Operators investigating "did we recover from a
stall recently?" can grep daily JSONL for `STALL_RETRIED` log markers or
filter the telemetry stream by `event_type=stall_retried`.

If a run exhausts its single stall-retry slot and stalls again, the second
stall is terminal: `terminal_failure_kind: stall_kill` and the failed stage
detail carries `failure_category: stall-killed-after-retry`.

See [docs/decisions/004-adaptive-stall-recovery.md](decisions/004-adaptive-stall-recovery.md)
for the heuristic and config flag.

### Blocked-Dependency Deferral (Issue #189 / #305)

A dispatched issue whose native GitHub `blockedBy` edges are still open must
**defer** pickup, not fail. The `#189` fail-closed guard in the deterministic
issue-pickup path detects the open blockers before any tokens are spent and
terminates the run as a **non-failure deferral** — nothing crashed and no work
was attempted.

The run is recorded with:

- `outcome: cancelled` — the closest non-failure state the fixed run-record
  outcome enum (`complete` | `failed` | `cancelled`) and the platform's
  telemetry wire (`ExecutionHistoryRunRecordV4`) accept. It is **never**
  `failed`.
- `outcome_type: deferred` — the first-class informational classifier for this
  case (added to `PipelineOutcomeType` and the history/state schemas).
- **No `terminal_failure_kind`** — a deferral is not a terminal failure, so the
  field is absent (definitely never `subagent_crash`).

Consequently there is **no autonomous pause, no cascade-breaker feed, no
`LifetimeIssueFailures` increment, and no user-facing failure notification** —
at most an info-level `[blocked-dependency]` log marker. The issue's board
status stays Ready and it remains eligible for a later tick; the Go
blocker-close requeue (`deps-gate promote` / the autonomous cascade)
re-dispatches it once the blockers close.

`blocked_dependency` is a **scheduler-routing** terminal kind (const
`TerminalKindBlockedDependency` in `internal/orchestrator/failure_handler.go`),
passed on the `autonomousComplete` IPC so `onPipelineComplete` takes the
non-failure branch. It is deliberately **not** written into the run record's
`terminal_failure_kind` (which stays empty) — the record distinguishes the
deferral via `outcome`/`outcome_type` instead. Pre-fix, the TS pickup path
routed this deferral through the generic failure path and mislabeled it
`failed` / `subagent_crash`, pausing autonomous (Issue #305).

---

## Retro Failure Categories (`AutoRetroService`)

The categories above (`infrastructure`, `agent`, `organic`) feed weighted
reliability scoring. A separate, finer-grained taxonomy is emitted by
`AutoRetroService.classifyFailure()` and written to `*_retro.json` files
under `.nightgauge/retros/`. These categories drive the retro
dashboard view, auto-issue creation, and recommendations surfaced to
operators.

| Category                 | Severity | Source                                          | Notes                                                                                                                 |
| ------------------------ | -------- | ----------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `budget-exceeded`        | high     | extension log: budget enforcer                  | Token or cost ceiling tripped before grace.                                                                           |
| `shipped-but-overbudget` | low      | state-aware override                            | `budget-exceeded` finding with a MERGED PR — work shipped (#3108).                                                    |
| `false-negative-shipped` | low      | state-aware override (#3275)                    | Generalizes the shipped-but-merged path: ANY pr-merge failure where `gh pr view` shows MERGED reclassifies here.      |
| `state-management`       | high     | extension log: schema/context errors            | Pipeline contract failed (missing context file, schema validation).                                                   |
| `ci-infrastructure`      | medium   | gh CLI / CI poll                                | External CI checks failed.                                                                                            |
| `model-capability`       | high     | extension parser                                | Empty/garbled model output.                                                                                           |
| `timeout`                | medium   | free-form                                       | Configurable stage timeout (distinct from skillRunner stall-kill).                                                    |
| `validation-failure`     | high     | subagent stdout                                 | Tests/typecheck/build failed.                                                                                         |
| `stall-kill`             | medium   | skillRunner                                     | Subagent went silent past idle/hard-cap threshold.                                                                    |
| `cost-cap`               | high     | skillRunner log line OR diagnostic file (#3275) | Per-stage `pipeline.stage_cost_caps` fired. The file-existence check (`<stage>-cost-capped.log`) is deterministic.    |
| `infrastructure-outage`  | low      | OfflineManager / DNS                            | Network outage during the run.                                                                                        |
| `stop-hook-error`        | medium   | Claude CLI notification (time-gated #3275)      | Pre-result `stop-hook-error` notification — the genuine #3204 silent-hang signature. Post-result emissions are noise. |
| `skill-no-op`            | high     | pr-merge context (#3275)                        | pr-merge LLM path reported success but post-merge verification found the PR is not actually merged.                   |
| `adapter-unavailable`    | high     | dispatcher envelope                             | Primary adapter prereq failed; no fallback walked (#3223).                                                            |
| `no-adapter-available`   | high     | dispatcher envelope                             | Full fallback chain exhausted (#3231).                                                                                |
| `unknown`                | low      | fallback                                        | No structured signal or keyword match.                                                                                |

### Time-Gated `stop-hook-error` (Issue #3275)

The Claude CLI emits a routine `stop-hook-error` notification at the end of
EVERY stage as part of teardown. Pre-#3275 the classifier matched on string
presence and `stop-hook-error` won for almost every failed run, masking the
real cause (cost-cap, skill-no-op, shipped-but-failed).

The fix:

1. **Demote** the extractor to LAST in `SIGNAL_EXTRACTORS` so any other
   structured signal wins first.
2. **Time-gate** via `isPreResultStopHook(text)` — only fire when the
   `stop-hook-error` match index precedes the LAST `"type":"result"` event
   in the same evidence corpus. When no terminal result event exists, the
   stop-hook is treated as the genuine cause (the legitimate #3204
   silent-hang signature).
3. **File-existence cost-cap signal**: a deterministic extractor fires
   when `<stage>-cost-capped.log` is present in `evidence.sourcesAnalyzed`,
   regardless of whether the textual `[cost-cap-exceeded]` line appears.
4. **State-aware `false-negative-shipped` override**: any pr-merge failure
   with a MERGED PR reclassifies the PRIMARY finding to
   `false-negative-shipped` (low severity), generalizing the budget-only
   `shipped-but-overbudget` path.
