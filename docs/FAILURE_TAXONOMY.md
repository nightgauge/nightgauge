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

| Kind                         | Meaning                                                                                                                                                                                                                                                                                                           |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `stall_kill`                 | Subagent exceeded `stall_kill_multiplier × stall_thresholds` and was forcibly killed                                                                                                                                                                                                                              |
| `budget_exceeded`            | Pipeline-level or per-stage **token** budget ceiling tripped (with grace buffer applied)                                                                                                                                                                                                                          |
| `validation_error`           | Context schema validation failed terminally (e.g., missing output context for the stage)                                                                                                                                                                                                                          |
| `subagent_crash`             | Subagent process exited non-zero with no recovery path (model escalation exhausted)                                                                                                                                                                                                                               |
| `orchestrator_crash`         | Orchestrator process died mid-stage; record synthesized on next startup from a stale `current-run.json` sidecar                                                                                                                                                                                                   |
| `network_unavailable`        | Extended GitHub connectivity loss aborted the run (Issue #3296) — environmental                                                                                                                                                                                                                                   |
| `stream_idle_timeout`        | Anthropic API closed a streaming response mid-flight (Issue #3398) — environmental                                                                                                                                                                                                                                |
| `rate_limit_quota_exhausted` | Idle stall fired while the rate-limit bucket was drained (Issue #3386) — environmental                                                                                                                                                                                                                            |
| `worktree_uncommitted`       | Failure **recovered**: uncommitted work was auto-committed before cleanup (Issue #3542)                                                                                                                                                                                                                           |
| `budget_ceiling_hit`         | The USD pipeline budget ceiling killed a running stage (Issue #3542) — real spend, not a defect                                                                                                                                                                                                                   |
| `github_quota_low`           | GitHub API rate-limit bucket below headroom at the pipeline-start preflight (Issue #3896) — environmental                                                                                                                                                                                                         |
| `api_connection_lost`        | Anthropic API transport drop mid-stage (socket close / DNS blip; Issue #4002) — environmental                                                                                                                                                                                                                     |
| `github_network_outage`      | api.github.com unreachable at the pipeline-start preflight (Issue #4002) — environmental                                                                                                                                                                                                                          |
| `model_unavailable`          | API rejected the selected model: not on plan / unknown ID / model usage cap (Issue #42) — triggers tier fallback                                                                                                                                                                                                  |
| `premature_turn_end`         | Stage exited 0 but produced no state change — the agent ended its turn on a promise (Issue #74)                                                                                                                                                                                                                   |
| `dev_produced_no_changes`    | feature-dev's gate found the stage workspace empty despite a truthful dev context — work landed where the pipeline never reads (Issue #202)                                                                                                                                                                       |
| `adapter_auth_failed`        | Pipeline-start adapter auth gate refused to launch: probe timed out after retry, or the adapter CLI is logged out (Issue #312) — retryable infra                                                                                                                                                                  |
| `no_changes_produced`        | pr-create's deterministic fallback confirmed zero commits ahead of base — genuinely nothing to open a PR for (Issue #317) — planning/scope                                                                                                                                                                        |
| `validation_failed`          | feature-validate honestly failed its quality gates (`validation_status="failed"`) — organic implementation failure (Issue #326)                                                                                                                                                                                   |
| `branch_forked`              | The run's branch diverged from its remote; every push is rejected non-fast-forward (Issue #163) — unrecoverable by retry, needs human action                                                                                                                                                                      |
| `abandoned_commit`           | A stage upstream of pr-create was killed/crashed after committing valid, unmerged work (clean tree, ahead of base) — the `abandoned-commit-recoverable` action matched but could neither self-heal nor set up a resume (Issue #191)                                                                               |
| `commit_orphaned`            | A killed stage's commit landed on the wrong branch (a stray `temp-pre-push-<n>` left by a SIGKILL bypassing pre_push.go's restore-defer) and feature-validate's branch-identity self-heal could not check out the expected feature branch to recover it (Issue #266) — unrecoverable by retry, needs human action |
| `permission_denied`          | Harness denied a tool call outright — most commonly a stage's foreground `sleep` wait loop (Issue #289) — harness fault, retryable                                                                                                                                                                                |

`permission_denied` (Issue #289) is a **harness-fault** kind, distinct from a
stage failure. The harness rejects certain tool calls outright — the observed
trigger is a stage reaching for a foreground `sleep` wait loop
(`until grep -q DONE log; do sleep 30; done`) to poll a backgrounded job, which
the host denies (`is_error:true`,
`tool_use_result: "User rejected tool use"`). A denial is not a defect in the
work: the stage still had turns available and could have used a backgrounded
command plus its completion notification on its very next turn instead.
Pre-fix, a denial fell through to the generic `subagent_crash` fallback,
which killed the run permanently, incremented `LifetimeIssueFailures`, and
tripped `haltQueueOnSlotFailure` — turning one rejected tool call into a
fleet-wide pause. Routed like `adapter_auth_failed`: short backoff, board →
Ready, **no `LifetimeIssueFailures` increment, no cascade-breaker feed, no
pause** — bounded by a small consecutive-attempt cap so a stage that keeps
reaching for the same denied pattern eventually stops re-dispatching rather
than looping forever. Emitted with the `[permission-denied]` marker /
`user rejected tool use` text, matched BEFORE the generic subagent-crash
fallback so the "exit " substring in the rejection text doesn't misclassify it
as a process death.

`branch_forked` (Issue #163) is the one kind that is **strictly harmful to
retry**. The remote branch head is not reachable from the run's local tip, so
every push is rejected as non-fast-forward; a retry rebuilds the same local
history against the same unchanged remote and is rejected identically — after
regenerating a full implementation (~$25 observed) to get there. Two causes
produce it, and neither is recoverable by the pipeline unaided:

1. A stage SIGTERM-killed mid-`git push` that had **already pushed**. The kill
   discards the local run; it does not discard the remote commit. The next
   attempt branches from the base again and forks.
2. An operator pushing to a pipeline-owned branch, which makes
   `--force-with-lease` refuse.

Three mechanisms address it, all deterministic:

- **Pre-flight** (`CheckBranchFork`, `internal/orchestrator/branch_fork.go`) —
  one `git ls-remote` before every post-pickup stage, comparing the remote head
  against the local branch tip. Turns a full wasted run into an immediate
  diagnosis naming both SHAs. Fail-open: an unreachable remote or a missing
  branch degrades to "unknown", never to "forked".
- **Orphaned-push reclamation** (`ReclaimOrphanedRemoteBranch`) — on a failed
  run with no PR, drops origin's copy of the branch **only** when the remote
  head is contained in the run's local history (proof the pipeline pushed it).
  A commit the run never authored is left standing for the pre-flight to report
  rather than deleted.
- **Routing** — the autonomous scheduler does NOT increment
  `LifetimeIssueFailures`, does NOT feed the cascade breaker, does NOT set a
  retry backoff, and the Go scheduler skips the board revert. The Action Center
  `branch-fork` producer raises an `unblock` card naming both SHAs; resolving it
  clears the failure cooldown and requeues.

Classified `infrastructure` (0.05 weight): the dominant cause is the pipeline's
own kill path, not the quality of the implementation the run produced.

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

`dev_produced_no_changes` (Issue #202) reads like `no_changes_produced` and
means close to its opposite. Keep them apart:

|                       | `no_changes_produced` (#317)       | `dev_produced_no_changes` (#202)   |
| --------------------- | ---------------------------------- | ---------------------------------- |
| Was there work to do? | No — a human-only issue            | Yes                                |
| Did the stage do it?  | Correctly did nothing              | Yes, in full                       |
| What is wrong         | It was dispatched at all           | The pipeline cannot see the result |
| Raised by             | pr-create's deterministic fallback | feature-dev's post-condition gate  |

Both weigh `agent` (0.5), so the difference is not cost — it is triage. #202
spent 31 minutes and $3.16 writing five files, then reported nothing because
the stage had delegated to a subagent running under **worktree isolation**
(`.claude/worktrees/agent-<id>`). The pipeline reads only
`.worktrees/issue-<n>`, so the implementation was invisible to every later
stage and would have been destroyed by the next worktree sweep. Filing that as
"there was nothing to do" would send the reader looking at issue eligibility
instead of at a stranded deliverable.

The gate reason names the sibling worktree still holding the work, so the
failure record is enough to recover from. Emitted with the stable
`[dev-produced-no-changes]` marker and matched BEFORE `premature_turn_end` in
both classifiers: the scheduler wraps every `KindNoOp` gate reason in a
`premature turn end:` envelope, so without that ordering the broader kind
swallows the narrower one on every text-classified path while the gate path
reports it — the two disagreeing about one failure.

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

### Gate-Sourced Structured Terminal Kind (Issue #9)

Everything above describes `ClassifyTerminalKind`'s **prose classification**
path: lowercase-and-substring-match the synthesized failure text. That path
is still the fallback, but stage post-condition gates
(`internal/orchestrator/gates/`, see `docs/STAGE_GATES.md`) now optionally
carry a structured `TerminalKind` directly on `GateResult` /
`StageGateResult`, set at the point the gate detects the failure rather than
re-derived downstream from prose. When a gate ran and set one, callers
(`internal/orchestrator/scheduler.go`, `internal/ipc/server.go`,
`internal/orchestrator/scheduler_exit_record.go`, `internal/orchestrator/
autonomous.go`) call `orchestrator.ResolveTerminalKind(gateRan,
gateResult.TerminalKind, errorText)` instead of `ClassifyTerminalKind`
directly — it prefers the gate's structured kind and only falls back to prose
classification when the gate didn't run, or ran but left `TerminalKind`
empty (including every historical `StageGateResult` record persisted before
the field existed; it's `omitempty`/optional).

This closes a real, previously-silent mismatch: several gates emit JSON-parse
failures worded `"...is not valid JSON"` (`issue-pickup`, `feature-planning`,
`feature-dev`) or `"unparseable JSON"` (`pr-create`, `pr-merge`), but
`ClassifyTerminalKind` only matched the literal substring `"invalid json"` —
none of the actual gate Reason strings contained it, so every JSON-parse gate
failure fell through to the generic `subagent_crash` bucket instead of
`validation_error`. The rule itself is now also fixed to catch both real
phrasings, so the prose-fallback path is correct too (this matters for
historical records with no `terminal_kind` to prefer). Since #306 the SDK does
not mirror that fix — it interprets the same rule table, so it has it by
construction (`classifyTerminalKind` / `resolveTerminalKind` in
`packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts`).

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
| `dev_produced_no_changes`    | `agent` — the stage's delegation/turn-ending behavior, not the issue                        |
| `adapter_auth_failed`        | `infrastructure` — probe starvation / credential state, not the issue                       |
| `no_changes_produced`        | `agent` — planning/scope failure (dispatch-eligibility gap), not the model's implementation |
| `validation_failed`          | `organic` — true implementation failure caught by feature-validate's own quality gate       |
| `branch_forked`              | `infrastructure` — the pipeline's own orphaned push (or an operator's), not the code        |
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
  "dev_produced_no_changes", // Issue #202
  "adapter_auth_failed", // Issue #312
  "no_changes_produced", // Issue #317
  "validation_failed", // Issue #326
  "branch_forked", // Issue #163
]);

export const ExecutionHistoryRunRecordV3Schema = ExecutionHistoryRunRecordV2Schema.extend({
  schema_version: z.literal("3"),
  terminal_failure_kind: TerminalFailureKindSchema.optional(),
});
```

The `TerminalKind*` constants live in
`internal/orchestrator/failure_handler.go`; the rules that produce them live in
`internal/terminalkind/table.json` (see below). When changing the enum, update
**all three** in lockstep — the `TerminalFailureKindSchema` test in
`packages/nightgauge-vscode/tests/views/dashboard/FailedRun.test.ts`
guards against drift.

### One rule table, three interpreters (Issue #306)

Terminal-kind classification used to exist in **three** places, each deciding
how the fleet reacts to a failure, held aligned by "keep aligned" comments —
which is how the same run could be recorded as one kind and reacted to as
another while all three sides stayed individually green. On a corpus of 98 real
and synthetic inputs the ladders disagreed 19 times.

It is now written down **once**, as an ordered rule table, and every consumer
interprets it:

| Site       | How it gets the rules                                                                            | Authority                                                                       |
| ---------- | ------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------- |
| **Table**  | `internal/terminalkind/table.json` — the canonical, ordered ladder                               | The definition. Nothing else contains a matching literal.                       |
| **Go**     | `internal/terminalkind` embeds it; `ClassifyTerminalKind` delegates                              | **Authoritative.** Writes `terminal_kind` into the run record; drives recovery. |
| **SDK**    | `terminalKindTable.generated.ts`, generated from the table; `classifyTerminalKind` interprets it | Published classifier — reproduces Go by reading the same rules.                 |
| **Signal** | `classifyTerminalKindForSignal` projects the same table                                          | Sent to Go with `autonomousComplete`; a **non-empty** answer is used verbatim.  |

**Rule shape.** A rule is a kind plus an OR of ANDs: it fires when any clause is
satisfied, and a clause is satisfied when every term holds. A term is a literal
(the lowercased error text contains it) or a named predicate — used exactly once,
for "does this text name a model?", which depends on the model registry and
cannot be a literal. Rules are evaluated in order and the first match wins.

**The order is the contract.** Many real failure strings satisfy two rules and
the earlier one wins on purpose: cost-cap versus stall, the USD ceiling versus
the token budget, `pr_merge_unmerged` and the three narrower no-op kinds versus
`premature_turn_end`. Moving a rule is a routing change.

**The signal side cannot override the record.** Go's `NotifyComplete` uses a
non-empty kind from the extension verbatim, so the extension runs the _full_
ladder and returns the winning rule's kind only when that rule is declared
`signal: true`. Its answer is therefore either nothing or exactly what Go
records — bounded above and below. (Skipping non-signal rules instead would
reintroduce disagreement: a lower-precedence signal rule could claim text a
higher-precedence non-signal rule owns. That is the bug the old ladder had.)

**What keeps it honest:**

- **Behaviour** — `internal/terminalkind/testdata/corpus.json`, read by
  `internal/terminalkind/corpus_test.go`,
  `packages/nightgauge-sdk/tests/analysis/health/terminalKind.corpusParity.test.ts`
  and
  `packages/nightgauge-vscode/tests/services/terminalKindSignal.corpusParity.test.ts`.
  `expected` is Go's answer because Go writes the record; `expected_signal` is
  what the extension may forward. Every row carries a mandatory `rationale`, so
  changing an expectation means editing the argument for it in the same diff.
- **Evidence** — the core rows are real failure text captured from live
  telemetry by `scripts/capture-terminal-kind-fixture.sh` (#166). A row marked
  `captured` must appear verbatim in the committed capture output, and no shape
  in that file may be left unclassified. Both are checks over a **tracked,
  generated file** — the script needs the operator's local workspace roots and
  is not run in CI — so the guarantee is "this string is in the reviewed
  evidence file", enforced in the diff, not a proof of provenance.
- **Equivalence** — `internal/terminalkind/stress.go` and its verbatim
  TypeScript twin derive ~1,400 inputs _from the table_ (every clause, every
  term, every ordered rule pair) and all three suites must reproduce
  `testdata/stress-golden.json`. Deleting a clause, widening a literal or
  swapping two rules changes a committed answer, so it lands in review as an
  explicit before/after of the inputs whose routing changed.
- **Distribution** — the generated SDK module and the golden are byte-compared
  by `TestGeneratedTypeScriptIsInSync` / `TestStressGoldenIsInSync`,
  `.husky/pre-commit` and `scripts/ci-local.sh`. A consumer cannot be edited on
  its own; `make generate-terminal-kind-table` is the only way to change one.
- **Taxonomy** — a `TerminalKind*` constant with no rule and no corpus row is
  red, unless it is listed in the table's `kinds_without_rules` because it is
  set structurally and never derived from text.

### The one deliberate record-vs-reaction divergence

The run **record** and the fleet's **reaction** are produced by two different
paths: Go writes `terminal_kind` from `Classify`, and the extension forwards a
kind over IPC that `NotifyComplete` uses **verbatim** when non-empty. #306 exists
because those two disagreed. They now read one table, and on every RULE the
reaction is either silent or exactly the record's kind.

There is exactly one declared exception, and it lives in the table as data —
`signal_extensions` in `internal/terminalkind/table.json`:

| Extension                   | Kind                         | Fires on                                       |
| --------------------------- | ---------------------------- | ---------------------------------------------- |
| `session-usage-limit-quota` | `rate_limit_quota_exhausted` | a word-bounded `session limit` / `usage limit` |

**Why it exists (#3792).** A bare Anthropic or Codex quota line — `You've hit
your usage limit · resets 3pm`, `Claude AI usage limit reached|<unix>`,
`usage limit reached for this account` — names no model, so no rule classifies
it: the table's `usage limit` / `weekly limit` clauses belong to
`model_unavailable` and are gated on a model actually being named. The record is
therefore empty and `NotifyComplete` falls back to `subagent_crash`, which
increments the lifetime failure cap and feeds the cascade breaker for a window
that clears on its own — instead of the quota cooldown, board→Ready and
`RecordNonFaultOutcome` the condition deserves. `SkillRunner` normalises this
shape to `[rate-limit-quota-exhausted]` **only** on the Claude stream-json
result-envelope path, so for plain-text and Codex runs this branch _is_ the
routing.

**Why it is bounded.** Extensions are consulted only after the rule ladder has
projected **no signal**, so an extension can never overrule a kind projected by a
`signal: true` **rule** — the widest it can reach is text the signal subset
ignores. That is deliberately narrower than "a kind the record names": a kind the
record names through a **non-signal** rule is not protected, which is exactly the
divergence described below. It is pinned from both sides: corpus rows where
`expected_signal` differs from `expected`, which the corpus well-formedness test
permits **only** for a declared extension; a table-level test requiring every
declared extension to actually produce such a row; a test requiring every
extension **clause** to be necessary to one of those rows (so a clause cannot be
_added_ silently); and a test requiring every `~` word-bounded term to move a row
when its boundary is dropped.

**One disclosed narrowing against the original.** The pre-#306 rule was
`/\b(?:session|usage)\s+limit\b/i`. The `~` term keeps the word boundary — plain
containment would also claim `usage limits`, `usage limited` and
`session limits`, and this kind triggers a **global** quota cooldown — but a
literal term is exactly one space where `\s+` was one-or-more. So `usage  limit`
and a phrase split across lines (`usage\nlimit`, which
`SkillRunner.extractTailError` produces when it joins the last three non-empty
lines) no longer signal, and those runs book a crash for a window that clears on
its own. The loss is one-directional and pinned by the corpus row
`boundary-negative-usage-limit-double-space`; closing it means a whitespace-run
term kind reproduced character-for-character in both interpreters.

**Where it still diverges from the record, on purpose.** A usage-limit line that
_does_ name a model records `model_unavailable` (a plan restriction) and reacts
`rate_limit_quota_exhausted` (an environmental window). That pair is exactly what
the pre-#306 ladders did, and it is now written down with a reason instead of
being an accident of two hand-maintained lists.

**Downstream text matchers.** `ConcurrentPipelineManager` decides whether a
failure halts the queue. Those branches used to be four more private regex
ladders with their own "keep aligned" comments; they now resolve the kind
through the table and test set membership, with the kind sets pinned by
`tests/services/concurrentPipelineManager.haltPolicy.test.ts`. One deliberate
raw-text condition remains and is documented in place — the same session/usage
limit wording the signal extension carries. It stays raw there because a
queue-halt decision is a local policy, not a kind: the halt branch has to answer
for text whose kind is empty.

See [`internal/terminalkind/testdata/README.md`](../internal/terminalkind/testdata/README.md)
for capture provenance and the redaction rules.

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

---

## Defect Classes (Cross-Cutting Engineering Patterns)

The sections above classify _run outcomes_. This section names recurring
_defect shapes_ in the deterministic layer itself, so retros can point at a
class instead of re-deriving it, and reviews can ask the class's question
before the next instance ships.

### Silent No-Op (Issue #166)

**Shape:** code that runs, succeeds, and does nothing on the inputs that
matter. No exception, no error, no log line — exit code 0 and an empty result
indistinguishable from "nothing to do."

**The common precondition:** an empty/zero/absent value is **semantically
meaningful** (it implies a real precondition failure) but is treated as
**vacuously fine**, because emptiness and "no work required" share the same
value. The bug is never the emptiness itself; it is that the distinction
cannot be observed.

**Why the class is worse than a crash:** a crash is self-reporting and lands
on the failing change. A silent no-op surfaces only when something downstream
is missing — often days later — and the investigation starts far from the
cause. Worst case, the empty result is read as an affirmative verdict and
**authorizes a destructive action** (#165: "empty diff" → "fully merged" →
"safe to delete").

**Fixed instances (evidence):**

| Issue | Where          | What silently did nothing                                                                          |
| ----- | -------------- | -------------------------------------------------------------------------------------------------- |
| #149  | recovery       | the catch-path branch was dropped, so the recovery never ran                                       |
| #151  | capture        | shape-blind parse — the runtime shape fell through the singular-only branch                        |
| #154  | tokenParser    | `tool_use.id` discarded at a parse boundary, so `last_bash_exit` could never populate              |
| #163  | cleanup        | `loadFeatureBranch(workspaceRoot, …)` resolved `""` on worktree-isolated runs; cleanup hit nothing |
| #165  | branch cleanup | a pathspec that matched no file, so the "is it merged?" diff was empty and read as "merged"        |

Every one passed its tests. Several sat in code whose own comment asserted it
was the sole detection channel for the thing it was failing to detect.

**The rule (binding at review):** at any boundary where an empty result
implies a precondition failure rather than a clean no-op —

1. **Assert non-empty where empty is meaningful.** Check it and fail loud.
2. **Never let a not-found path share a return value with a success path.**
   Where the states are genuinely three ("found work" / "nothing to do" /
   "could not determine"), return three states. `UNKNOWN` must never fold
   into "nothing found" — and a destructive consumer must treat `UNKNOWN` as
   "do not act" (`scripts/branch-merged-check.sh` exit 2 is the model).
3. **Test the shape production actually sends.** Fixtures drawn from real
   transcripts, not from the shape the parser happens to expect (#151, #154).
4. **"The comment says this is the only detector" is a review trigger.**
   Sole-detection-channel code gets a test that removes the input and asserts
   the alarm fires.

**Sweep (2026-08-03):** the deterministic-layer cleanup and capture paths were
swept for the same shape. Ten candidates were verified against surrounding
code; sites already carrying an explicit three-state verdict (e.g.
`classifyWorktree`'s `SkipReason`s, `CheckBranchFork`'s `ForkStateUnknown`,
the change classifier's distinct `Empty` class) were confirmed GUARDED and
excluded. The confirmed instances are filed as:

- **#296** — `reconcileOrphanedComposeProjects` treats an undetermined
  worktree set as empty and tears down live cross-repo docker stacks
  (destructive; same root cause as #163).
  **Fixed:** `activeWorktreeIssues` returns `(set, determined)` and enumerates
  every repo scan root; both destructive callers honour the verdict
- **#323** — the same defect in the two copies #296 did not reach: `doctor`
  reported a live cross-repo run's stack as orphaned, and `nightgauge cleanup`
  — the command that report tells the operator to run — discarded its git
  error (`active, _ :=`) and tore down every stack that an unreadable set made
  look orphaned. Diagnostic and destructive halves of one defect, one
  indirection apart.
  **Fixed:** all three consumers call one
  `execution.ActiveWorktreeIssues(roots)`; CLI callers discover roots via
  `config.WorkspaceRepoRoots(cwd)` (git toplevel + workspace manifest);
  `doctor` reports **unverifiable** and `cleanup` refuses rather than either
  reporting or acting on a set they could not read. Rule 2 above, applied to
  the advisory path as well as the destructive one — an operator acting on a
  false "orphaned" report reaches the destructive outcome by hand.
  `TestExactlyOneWorktreeIssueParser` is the standing drift guard
- **#297** — `preserveUnlandedDeliverable` (the #289 guard) no-ops on
  detached HEAD, temp branches, worktree-scoped context, and the legacy
  `files_changed` shape — and `ResetPipeline` proceeds regardless.
  **Fixed:** the guard returns a three-state `preserveVerdict` whose zero
  value is `preserveUndetermined`, and `ResetPipeline` checkpoint-commits the
  tree on that verdict — refusing to reset at all if the checkpoint itself
  fails. Rule 2 above, applied literally
- **#298** — `git.resetPipeline` IPC handler drops its unmarshal error;
  empty `workDir` aims the hard reset at the workspace root.
  **Fixed:** the handler checks the error, and destructive git verbs resolve
  through `destructiveGitService`, which refuses an empty `workDir` rather
  than falling back to the workspace root. `workDir` lost its `omitempty` so
  the generated client demands it at compile time. The fallback remains
  correct for read verbs — it is the _destructive_ consumer that must treat
  "unspecified" as "do not act", per rule 2 above
- **#299** — two remaining `loadFeatureBranch(workspaceRoot, …)` call sites
  (non-terminal reconcile, V2 history) still miss worktree-isolated runs
- **#300** — `ParseStreamLine` ignores assistant per-turn usage; a stage
  killed before the `result` event books zero tokens
- **#301** — `captureConflictContextFromIndex` writes an empty capture as
  success, then `rebase --abort` destroys the evidence
- **#302** — batch of four small guards (bash-ring correlation self-check,
  zero-root worktree sweep, nil-state card retraction, unlogged
  `autonomousComplete` skip)

### Dual-Path Drift (Issue #257)

**Shape:** a behavior wired to one pipeline execution path silently does not
exist on the other. The code is present, correct, and covered by tests — and
never reached in the mode that matters. No error, no failed test, no log line.

Nightgauge has two dispatch paths that must stay behavior-parallel:

| Path      | Entry                                                                                                | Used by                               |
| --------- | ---------------------------------------------------------------------------------------------------- | ------------------------------------- |
| Go        | `Scheduler.runPipeline()` via `dispatchItem`                                                         | CLI/auto mode, autonomous scan        |
| Extension | `queue.dequeueIndependent` over IPC → `ConcurrentPipelineManager.fillSlots` → `HeadlessOrchestrator` | the VSCode extension (operating mode) |

**Confirmed instances (evidence):**

| Issue | Behavior                   | Wired to               | Consequence in the operating mode                                           |
| ----- | -------------------------- | ---------------------- | --------------------------------------------------------------------------- |
| #210  | Stage gates                | Extension wired 3 of 6 | `feature-dev`, `feature-planning`, `issue-pickup` ran ungated               |
| #254  | `CompleteQueueItem`        | Go `runPipeline` only  | every autonomous run leaked a permanent `processing` queue item             |
| #304  | Learning outcome record    | Go `runPipeline` only  | extension runs feed the self-improvement loop nothing                       |
| #305  | Run-scoped attention cards | Go `runPipeline` only  | no IPC raise verb exists; interactive runs produce zero Action Center cards |

**Why review does not catch it:** confirming _where_ a call sits inside a
function says nothing about whether that function is ever entered. #254's
comment even named the unwired path — as an aside, so it read as a benign edge
case. The question review must ask is not "is this correct?" but **"which of
the two paths reaches this — and is the other intentionally excluded?"**

**Guards now in place:**

1. **Parity manifest + twin tests.** Every terminal-path behavior is listed in
   `internal/orchestrator/testdata/terminal_behaviors.json` with its call-site
   anchor on each path, or an explicit `pathSpecific` reason (and tracking
   issue when the gap is a defect). `internal/orchestrator/terminal_parity_test.go`
   and `packages/nightgauge-vscode/tests/services/terminalParity.test.ts` both
   enforce it: anchors must exist, and the two terminal funnels
   (`runPipeline`'s defer; `runSlotPipeline`'s finally) are content-pinned by
   hash — any edit inside them fails until the manifest answers the parity
   question.
2. **The paths are named in the code.** `runPipeline`'s doc comment states
   the extension never enters it; `ConcurrentPipelineManager`'s states it is
   the operating path and never enters the Go loop.

**Relationship to adjacent classes:** distinct from Silent No-Op (above —
an empty value conflated with success) and from shape-blindness (#169 — a
dead branch because the runtime delivers a different shape). Here the code is
live and correct but sits on a path the operating mode never takes; the guard
is a path-parity assertion, not a value or shape check.

Known intentional divergences (board terminal status, queue-halt semantics,
worktree preservation on failure) and remaining alignment gaps (#307
abort-deadline bookkeeping) are recorded in the manifest and its notes.
Terminal-kind classification is no longer among them: #306 replaced the three
ladders with one interpreted rule table (see
[One rule table, three interpreters](#one-rule-table-three-interpreters-issue-306)).
