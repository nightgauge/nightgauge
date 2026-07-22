# Adaptive Stall Recovery — Rewind to Planning Once on First Stall-Kill

**Date:** 2026-04-25 **Author:** nightgauge **Status:** Decided
**Issue:** #3005
**Epic:** #3000 — Pipeline reliability hardening

---

## Executive Summary

The Go scheduler today treats a stall-killed stage the same as any other
non-zero stage failure: model escalation is offered, otherwise the run goes
terminal. The feedback-signal infrastructure (`SCOPE_DISCOVERED`,
`COMPLEXITY_UNDERESTIMATED`, `PLAN_REVISION_NEEDED`) wired into
`scheduler.go:1850-1877` is never consulted on a stall-kill — by the time the
scheduler observes the kill, the subagent is dead and cannot emit anything.

This decision wires stall-kill into the existing feedback-signal path. The
**first** stall-kill in a run synthesizes a feedback signal scheduler-side,
writes `feedback-{N}.json`, and rewinds once to `feature-planning` via the
existing `RetryEngine` (so `max_backtracks` and oscillation guards apply
unchanged). The **second** stall-kill in the same run is terminal and carries a
new `failure_category: "stall-killed-after-retry"`.

Behavior is gated by a new opt-in flag `pipeline.adaptive_stall_recovery`
(default `false`). Cost-cap kills (#3002) are explicitly excluded — they are
never retried.

---

## Findings and Decisions

### Decision 1: Signal Classification Heuristic

**Severity:** HIGH (correctness — a wrong classification wastes a retry slot)

**Rules in priority order, evaluated by `ClassifyStallSignal` in
`internal/orchestrator/stall_recovery.go`:**

1. If the killed stage is `feature-dev` or `feature-validate` AND the planning
   context for the issue (`planning-{N}.json`) lists 4 or more entries in
   `files_to_modify` → emit `COMPLEXITY_UNDERESTIMATED`. Rationale: the plan
   was correct in shape but the work area is large enough that a fresh planning
   pass with adjusted scope is the most accurate response.
2. Else if the killed stage is `feature-dev` or `feature-validate` AND the
   planning context references files that do not exist on disk → emit
   `SCOPE_DISCOVERED` with the missing files as evidence. Rationale: the plan
   anticipated files that the codebase does not contain.
3. Else → emit `PLAN_REVISION_NEEDED` (the most generic blocking signal).

When uncertain, fall through to `PLAN_REVISION_NEEDED` rather than
mis-classify. A wasted retry on a wrong classification is strictly worse than
the most-generic signal — both consume a backtrack slot, but the wrong
classification primes the planner with a misleading frame.

The heuristic is deterministic. No AI/LLM call; no per-attempt state. Same
input → same output.

---

### Decision 2: Cost-Cap Precedence (#3002)

**Severity:** HIGH (operator contract)

A cost-cap kill (`pipeline.stage_cost_caps`, Issue #3002) is **never** retried.
The scheduler inspects the error text **before** deciding to retry. If
`ClassifyTerminalKind(errMsg) == TerminalKindStallKill` AND none of
`cost-cap-exceeded` / `cost cap exceeded` substrings are present, retry is
allowed. If both signals are present (defensive), cost-cap wins → terminal
failure.

Operators who set a per-stage cost cap retain the cap's full force. Stall-recovery
fires only on watchdog-driven, time-based kills.

---

### Decision 3: Retry Counter Scope — Per-Run, Not Per-Stage

**Severity:** MEDIUM (bounded blast radius)

A per-stage counter would allow a run to stall once per stage (up to 6 retries
per run), compounding spend. Per-run `stallRetryCount` caps total retries at 1
regardless of which stages stall.

A run that stalls in `feature-dev`, rewinds to `feature-planning`, then stalls
in `feature-validate` is suffering from the same root failure mode
(underestimated complexity). The second stall is terminal — the operator gets
a clean signal that re-planning did not resolve the issue.

The counter is scheduler-local (lives on the `runPipeline` stack frame, reset
per run). It does NOT replace `RetryEngine.backtrackCount` — the engine still
enforces `max_backtracks` and oscillation guards. The local counter only
guarantees "first stall-kill only" without consulting the engine for that
specific decision.

---

### Decision 4: Interaction With `failure_mode: halt` (#3001)

**Severity:** MEDIUM

The stall-recovery branch fires before the deferred terminal-classification
block at `scheduler.go:1357-1370`. A recovered run never reaches the
`failure_mode` switch — it completes successfully via the rewind + retry. A
run that exhausts the stall retry and stalls again is terminal and obeys
`failure_mode` unchanged: `halt` pauses the queue, `continue-queue` keeps
dispatching, `auto-resume` triggers a single re-dispatch.

`terminal_failure_kind = stall_kill` is set on the second-stall path so the V3
record correctly identifies what aborted the run.

---

### Decision 5: Signal Severity — Blocking

**Severity:** LOW (mechanical)

Synthetic stall-kill signals are emitted with `severity: "blocking"`. The run
cannot proceed without re-planning — there is no fallback path. Warning-severity
signals are logged but trigger no automatic action per
`retry_engine.go:100`, which would render the synthetic signal inert.

---

### Decision 6: Emitter Convention Deviation

**Severity:** MEDIUM (documentation debt)

`docs/FEEDBACK_LOOPS.md` documents that feedback signals are emitted by stage
agents (feature-dev, feature-validate). On a stall-kill, the subagent is dead
and cannot emit. The scheduler synthesizes the signal on its behalf:

- `emitted_by_stage` carries the killed stage name (e.g., `feature-dev`)
- `rationale` includes the substring `"synthesized by scheduler on stall-kill"`
  so audits can grep for synthetic signals
- `evidence` is scheduler-generated (e.g., the killed-stage error text, the
  matched classifier rule)

This deviation is documented here and cross-referenced from
`docs/FEEDBACK_LOOPS.md`. Future synthetic-signal additions
(orchestrator-level conditions where the stage agent cannot emit) should
follow the same convention.

---

### Decision 7: Default `false` — Opt-In For All Repos

**Severity:** MEDIUM

The original AC asked for "default `true` for new repos, `false` for
existing — opt-in migration". Implementing per-repo defaults requires either a
migration step in `nightgauge:repo-init` or a runtime check on the absence
of the field. Both add scope without strong evidence the new behavior is safe
by default.

`pipeline.adaptive_stall_recovery: false` everywhere on this PR. Operators
opt in via `.nightgauge/config.yaml`. The dogfood
`.nightgauge/config.yaml` in this repo flips it on so the integration
test exercises the path. Once dogfooding produces a sprint of data,
`nightgauge:repo-init` can be updated to ship `true` in new-repo
templates as a follow-up.

---

### Decision 8: Stall-Recovery Branch Runs Before Model Escalation

**Severity:** MEDIUM (cost discipline)

The failure-handling block at `scheduler.go:1736-1792` evaluates model
escalation immediately on stage failure. If stall-recovery is added after
escalation, a stall-kill would first escalate the model and then re-plan —
doubling spend on what is rarely a model-capacity issue.

Stall-recovery branch runs first. If it triggers, the run rewinds without
consuming an escalation slot. If it does not trigger (cost-cap, second stall,
flag disabled, non-rewindable stage), control falls through to the existing
escalation evaluation unchanged.

Stall-kills that happen because of a model-capacity issue still get one
re-plan attempt. If the re-plan re-stalls, the run is terminal — escalation is
unavailable for that run because the second stall path is terminal. This is
acceptable per the epic's "bounded blast radius" goal.

---

## Consequences

- Costs at most one extra planning + dev attempt per run. Bounded by
  `max_backtracks` (default 2) — a run that already used its backtrack budget
  on a feedback-driven retry will not also retry on stall.
- New informational outcome `STALL_RETRIED` documented in
  `docs/FAILURE_TAXONOMY.md` for grep-ability and dashboard cross-reference.
- New `failure_category` value `stall-killed-after-retry` carried on the
  second-attempt stage detail. Classified as `agent` (50% weight) in
  `failureClassifier.ts` — the underlying issue is still agent-class even
  though the first retry was already consumed.
- No schema migration required. `FeedbackContextSchema` is unchanged;
  `failure_category` is a free string on the existing optional field.
- Synthetic signals are distinguishable from agent-emitted signals via the
  `"synthesized by scheduler on stall-kill"` rationale prefix.

---

## Files Changed

- `docs/decisions/004-adaptive-stall-recovery.md` — this document
- `internal/orchestrator/stall_recovery.go` — classifier, feedback writer,
  config reader, rewindable-stage helper
- `internal/orchestrator/stall_recovery_test.go` — unit tests
- `internal/orchestrator/scheduler_stall_recovery_test.go` — integration tests
- `internal/orchestrator/scheduler.go` — wire stall-recovery branch into the
  failure-handling block before model escalation
- `internal/state/history.go` — `FailureCategory` field on `V2StageDetail`,
  `StageFailureCategories` on `V2RunInput`
- `packages/nightgauge-vscode/src/config/schema.ts` —
  `pipeline.adaptive_stall_recovery` Zod field
- `packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts` —
  `stall-killed-after-retry` substring in `agent` bucket
- `docs/FAILURE_TAXONOMY.md` — `STALL_RETRIED` informational outcome,
  `stall-killed-after-retry` agent-bucket pattern
- `docs/FEEDBACK_LOOPS.md` — synthetic stall-kill signal note in
  Backtrack Behavior section
- `docs/CONFIGURATION.md` — `pipeline.adaptive_stall_recovery` reference
- `docs/decisions/README.md` — Active Decisions table row
- `.nightgauge/config.yaml` — flag enabled for dogfood repo
