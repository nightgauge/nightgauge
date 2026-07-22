# Feedback Loops Architecture

> This document covers **in-pipeline feedback signals** (active, used for
> runtime recovery). The adaptive policy engine (formerly 'auto-tune') has been
> removed from the extension runtime — see
> [ADAPTIVE_PIPELINE.md](ADAPTIVE_PIPELINE.md).

This document is the canonical reference for the Nightgauge pipeline with
runtime feedback signals. Stage agents emit structured backward signals; the
orchestrator either backtracks to an upstream stage for replanning or escalates
to a more capable model. Two guards — a recursion limit and an oscillation
guard — prevent infinite revision loops.

## Signal Type Reference

| Signal Type                     | Emitted By                    | Typical Severity    | Orchestrator Action                                                                  | `backtrack_target_stage` |
| ------------------------------- | ----------------------------- | ------------------- | ------------------------------------------------------------------------------------ | ------------------------ |
| `PLAN_REVISION_NEEDED`          | feature-dev                   | blocking            | Backtrack to feature-planning                                                        | feature-planning         |
| `SCOPE_DISCOVERED`              | feature-dev, feature-validate | blocking            | Backtrack to feature-planning                                                        | feature-planning         |
| `COMPLEXITY_UNDERESTIMATED`     | feature-dev, feature-validate | warning or blocking | If blocking: backtrack; always: update complexity model via FeedbackLearningService  | feature-planning or null |
| `MODEL_ESCALATION_NEEDED`       | feature-dev, feature-validate | blocking            | Retry same stage with next model in escalation path                                  | null (same-stage retry)  |
| `ACCEPTANCE_CRITERIA_AMBIGUOUS` | feature-validate              | blocking            | Backtrack to feature-planning                                                        | feature-planning         |
| `CONFLICT_RESOLUTION_NEEDED`    | pr-merge                      | blocking            | Backtrack to **feature-dev** on the same branch to resolve a rebase conflict (#4072) | feature-dev              |

### `CONFLICT_RESOLUTION_NEEDED` (#4072)

Unlike the other signals (emitted by the dev/validate stages targeting
feature-planning), `CONFLICT_RESOLUTION_NEEDED` is emitted by **pr-merge** and
targets **feature-dev**. When a rebase hits a non-trivial conflict that pr-merge
cannot land in-place, instead of discarding the branch via a fresh-branch
restart, it captures the conflict and rewinds to feature-dev to resolve it on
the same branch — then flows forward through feature-validate → pr-create →
pr-merge. The signal rides the same `feedback-{N}.json` rewind plumbing as the
other backward edges; the deterministic `conflict-recovery-loop` recovery action
(see [AUTO_TRIAGE.md](AUTO_TRIAGE.md#conflict-recovery-loop-4072)) is what emits
it on the pr-merge failure and bounds the re-dispatch via
`pipeline.recovery.conflict_recovery.max_dev_redispatch`.

The signal is paired with a `conflict-context-{N}.json` sidecar (see schema
below) carrying the conflicting files and both sides of each conflict.

## Backward Edge Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                       FEEDBACK-DRIVEN PIPELINE                               │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌──────────────┐     ┌──────────────────┐     ┌──────────────────────┐    │
│  │ issue-pickup │────▶│ feature-planning │────▶│    feature-dev       │    │
│  └──────────────┘     └──────────────────┘     └──────────────────────┘    │
│                               ▲                    │              │◀──┐     │
│                               │                    │              │   │     │
│                               │  PLAN_REVISION_    │              │   │     │
│                               │  NEEDED /          │              │   │     │
│                               │  SCOPE_DISCOVERED /│              │   │     │
│                               │  ACCEPTANCE_       │              │   │     │
│                               │  CRITERIA_AMBIGUOUS│              │   │ MODEL│
│                               └────────────────────┘              │   │ ESCA-│
│                                                                    ▼   │ LATION│
│                               ┌─────────────────────────────────────┐ │ NEEDED│
│                               │          feature-validate           │─┘     │
│                               └─────────────────────────────────────┘       │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

Backward arrows labeled
`PLAN_REVISION_NEEDED / SCOPE_DISCOVERED / ACCEPTANCE_CRITERIA_AMBIGUOUS` rewind
to `feature-planning`. The self-loop on `feature-dev` and `feature-validate`
labeled `MODEL_ESCALATION_NEEDED` retries the same stage with a more capable
model.

## Recursion Guards

Two independent guards prevent infinite revision loops:

**`max_backtracks` limit (default: 1, range 0–5):** The orchestrator tracks the
total number of backward stage transitions per pipeline run. When this limit is
exceeded, blocking signals that would trigger a backtrack are surfaced to the
user but no automatic backtrack occurs. The pipeline stalls and waits for human
intervention.

**Oscillation guard:** Independently of the `max_backtracks` quota, the same
`from→to` edge (e.g., `feature-dev → feature-planning`) cannot be traversed
twice in a single pipeline run. This prevents oscillation between two stages
even when backtrack quota remains. The guard is tracked in the orchestrator's
in-memory traversal history and is not configurable.

Together, these two guards ensure that even a misconfigured or adversarial plan
cannot produce an infinite loop.

## Backtrack Behavior and Commit Location (Issue #1608)

Since commit+push moved from feature-dev to feature-validate, backtracking is
simpler:

- **If feature-validate fails**: Code is on disk but NOT committed or pushed.
  Backtracking to feature-dev only needs to re-implement from the current disk
  state — there are no pushed commits to undo or revert.
- **If feature-dev emits a signal**: No commits exist on the remote branch for
  the current implementation attempt. Backtracking to feature-planning is clean
  — the orchestrator simply re-runs planning and dev stages on the same branch
  with no commit history to untangle.

This eliminates the previous scenario where backtracking required reverting or
amending already-pushed commits.

### Synthetic stall-kill signals (Issue #3005)

Feedback signals are normally emitted by stage agents (`feature-dev`,
`feature-validate`). On a stall-kill the subagent is dead and cannot emit, so
the Go scheduler synthesizes a signal on its behalf when
`pipeline.adaptive_stall_recovery: true`. Synthetic signals consume the same
`max_backtracks` quota and obey oscillation detection — the scheduler reuses
the existing `RetryEngine.EvaluateBacktrack` / `RecordBacktrack` path rather
than introducing a parallel counter.

Synthetic signals carry `rationale` prefixed with the substring
`"synthesized by scheduler on stall-kill"` so audits can distinguish them
from agent-emitted signals. The classification heuristic
(`COMPLEXITY_UNDERESTIMATED`, `SCOPE_DISCOVERED`, fallback
`PLAN_REVISION_NEEDED`) is documented in
[docs/decisions/004-adaptive-stall-recovery.md](decisions/004-adaptive-stall-recovery.md).
Cost-cap kills (#3002) are explicitly excluded from synthetic-signal
generation — they are never retried.

## Model Escalation Path

When `MODEL_ESCALATION_NEEDED` is emitted, the orchestrator retries the same
stage with the next model in the fixed escalation path:

```
┌─────────┐     ┌─────────┐     ┌─────────┐
│  haiku  │────▶│ sonnet  │────▶│  opus   │
└─────────┘     └─────────┘     └─────────┘
```

`max_escalations_per_stage` (default: 1) caps how many times escalation fires
per stage per pipeline run. Setting to `0` completely disables escalation.
Escalation does not consume backtrack quota — it is an independent counter.

## `feedback-{N}.json` Schema Reference

The orchestrator writes `feedback-{N}.json` when it needs to pass backtrack
signals across pipeline runs (e.g., from feature-validate back to
feature-planning). The schema is defined in
`packages/nightgauge-sdk/src/context/schemas/feedback.ts`.

**FeedbackContextSchema fields:**

| Field            | Type     | Description                               |
| ---------------- | -------- | ----------------------------------------- |
| `schema_version` | string   | Schema version (e.g., `"1.0"`)            |
| `issue_number`   | integer  | GitHub issue number                       |
| `signals`        | array    | Array of `PipelineFeedbackSignal` objects |
| `created_at`     | datetime | ISO 8601 timestamp (nullable)             |

**Example `feedback-{N}.json`:**

```json
{
  "schema_version": "1.0",
  "issue_number": 42,
  "signals": [
    {
      "signal_type": "PLAN_REVISION_NEEDED",
      "emitted_by_stage": "feature-dev",
      "backtrack_target_stage": "feature-planning",
      "rationale": "UserRepository class does not exist in codebase; plan assumed it was implemented in #1200 but it was not merged.",
      "evidence": [
        "grep -r 'UserRepository' src/ returned no results",
        "Plan referenced import from 'src/repositories/UserRepository.ts' which does not exist"
      ],
      "severity": "blocking",
      "timestamp": "2026-02-26T10:00:00Z"
    }
  ],
  "created_at": "2026-02-26T10:00:00Z"
}
```

Canonical source: `packages/nightgauge-sdk/src/context/schemas/feedback.ts`

## `conflict-context-{N}.json` Schema Reference (#4072)

Written by the pr-merge stage (`merge.md` Step 6.1.5), or by the
`branch-out-of-date` recovery action when its rebase discovers a conflict,
alongside a `CONFLICT_RESOLUTION_NEEDED` signal in `feedback-{N}.json`. It
captures the conflicting files and **both sides** of each conflict so the
re-dispatched feature-dev stage can resolve preserving both. The ours/theirs
blobs MUST be captured **before** `git rebase --abort` (the conflict index is
gone after the abort). Schema: `ConflictContextSchema` in
`packages/nightgauge-sdk/src/context/schemas/feedback.ts`.

| Field               | Type     | Description                                             |
| ------------------- | -------- | ------------------------------------------------------- |
| `schema_version`    | string   | Schema version (e.g., `"1.0"`)                          |
| `issue_number`      | integer  | GitHub issue number                                     |
| `pr_number`         | integer  | The open PR number                                      |
| `branch`            | string   | The PR's head branch (checked out as-is by feature-dev) |
| `base_ref`          | string   | The base branch the rebase targets (e.g. `main`)        |
| `conflicting_files` | array    | `{ path, ours, theirs }` — both sides of each conflict  |
| `created_at`        | datetime | ISO 8601 timestamp (nullable)                           |

```json
{
  "schema_version": "1.0",
  "issue_number": 143,
  "pr_number": 200,
  "branch": "feat/143-thing",
  "base_ref": "main",
  "conflicting_files": [
    { "path": "internal/foo.go", "ours": "<PR side>", "theirs": "<base side>" }
  ],
  "created_at": "2026-06-25T00:00:00Z"
}
```

## Configuration Reference

| Config Key                                               | Type    | Default | Env Override                                    | Description                                                                       |
| -------------------------------------------------------- | ------- | ------- | ----------------------------------------------- | --------------------------------------------------------------------------------- |
| `pipeline.max_backtracks`                                | integer | `1`     | `NIGHTGAUGE_PIPELINE_MAX_BACKTRACKS`            | Max backward transitions per run                                                  |
| `model_routing.max_escalations_per_stage`                | integer | `1`     | `NIGHTGAUGE_PIPELINE_MAX_ESCALATIONS_PER_STAGE` | Max model escalations per stage per run                                           |
| `pipeline.recovery.conflict_recovery.enabled`            | boolean | `true`  | —                                               | Gate the conflict-recovery loop (#4072)                                           |
| `pipeline.recovery.conflict_recovery.max_dev_redispatch` | integer | `2`     | `NIGHTGAUGE_CONFLICT_MAX_REDISPATCH`            | Max feature-dev re-dispatches on a `CONFLICT_RESOLUTION_NEEDED` before escalating |

See [CONFIGURATION.md](CONFIGURATION.md) for full documentation of each option,
including YAML examples and environment override syntax.

## When NOT to Emit Feedback Signals

Feedback signals are reserved for **upstream structural problems** that require
discarding the current approach and starting over. They are not general-purpose
error reporting.

**Do NOT emit a signal when:**

- A test flakes or fails transiently — this is not a plan problem
- A file already exists with the right content — not `SCOPE_DISCOVERED`
- Uncertainty about one minor detail that can be resolved by reading an existing
  file in the repo
- A warning condition is already handled gracefully by the existing
  implementation
- The implementation required renaming a parameter or using an alternative
  method with the same purpose (reasonable adaptation, not a structural
  mismatch)
- 1–2 extra files were touched beyond what was planned (normal implementation
  variance)

**Severity guidance:**

- Use `severity: "warning"` for `COMPLEXITY_UNDERESTIMATED` when the plan _can_
  still be executed but architectural complexity was higher than anticipated.
  Warning signals are logged by the orchestrator but trigger no automatic
  action.
- Use `severity: "blocking"` for `COMPLEXITY_UNDERESTIMATED` only when the plan
  _cannot_ be executed without revision.
- All other signal types should default to `blocking` when they accurately
  describe the situation.

**Decision threshold for `SCOPE_DISCOVERED`:** Only emit when 3 or more files
beyond the plan's scope were required. Under this threshold, implementation
variance is expected and does not warrant replanning.

**Decision threshold for `PLAN_REVISION_NEEDED`:** Only emit when a core API,
class, or function the plan specified is entirely absent from the codebase and
no reasonable adaptation preserving functional intent is possible.

## Adaptive Policy Engine (Continuous Feedback)

The feedback mechanisms described above operate **within a single pipeline run**
— a signal is emitted, the orchestrator backtracks or escalates, and the run
continues. The Adaptive Policy Engine operates at a different timescale: it is a
**post-pipeline, macro-level feedback loop** that converts accumulated health
analysis into persistent configuration changes over days and weeks.

Where intra-run feedback signals address immediate plan or model failures, the
Adaptive Policy Engine addresses systemic trends:

| Aspect        | Intra-Run Signals                | Adaptive Policy Engine                 |
| ------------- | -------------------------------- | -------------------------------------- |
| Timing        | Within a single pipeline run     | After each pipeline completes          |
| Scope         | Single issue                     | All historical runs                    |
| Output        | Backtrack or model escalation    | Configuration changes in `config.yaml` |
| Lifecycle     | Cleared at run end               | Persisted to self-tuning log           |
| Reversibility | Automatic (orchestrator-managed) | Auto-rollback within 10 runs           |

The Adaptive Policy Engine forms a **closed control loop**: health analysis
outputs become policy decisions, policy decisions adjust configuration, adjusted
configuration changes pipeline behavior, changed behavior produces new health
data. This loop operates continuously without human intervention.

Five subsystems drive adaptive behavior:

1. **Auto-tune** — Adjusts model complexity thresholds based on routing
   performance data
2. **Health-Gated Policies** — Applies temporary per-run overrides (retry
   budget, model escalation) when health scores fall below thresholds
3. **Auto-Rollback** — Reverts auto-tune changes when post-change health
   degrades by ≥ 10 points
4. **Efficiency Adjustment** — Scales token budgets ±10% (capped at ±15%) based
   on efficiency trends
5. **Experiment Evaluation** — Concludes A/B model experiments when either group
   accumulates ≥ 10 runs and the treatment meets success criteria

See [docs/ADAPTIVE_PIPELINE.md](ADAPTIVE_PIPELINE.md) for the complete
reference: decision types, guardrail values, health tier thresholds, experiment
evaluation criteria, configuration options, and troubleshooting.

## Author

nightgauge
