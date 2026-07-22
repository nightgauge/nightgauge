# Auto-Triage — FailureRecovery Registry

Issue #3268. The
auto-triage framework lets the orchestrator self-heal recurring stage failures
that have a deterministic remedy. It runs alongside RetryEngine and the
adaptive stall-recovery path — never instead of them — and consumes only the
shell-out primitives already used by stage gates and the deterministic-first
runners.

## Why this exists

Several recurring failure modes are deterministically resolvable but today
require either an LLM-driven retry (expensive, slow) or human triage:

- pr-merge skill exited 0 but the PR is still OPEN
- pr-create skill exited 0 but `pr-{N}.json` was never written
- a single CI check flaked and a `gh run rerun --failed` would clear it
- the PR fell behind `origin/main` and a `--force-with-lease` rebase fixes it
- the issue was closed on GitHub but the project board didn't get the memo
- pr-merge stalled, but the PR is clean+mergeable and a deterministic merge
  succeeds

The FailureRecovery registry handles each of these as a deterministic action.
On a match the stage is marked recovered and the pipeline continues. On no
match (or after the per-run cap is reached) the existing failure path runs
unchanged.

## Architecture

```
Stage runs ──► Gate verifies ──► Failure?
                                  │
                                  ▼
                    Stall-recovery applies?  ──► Rewind to feature-planning
                                  │ no
                                  ▼
            FailureRecovery.TryRecover(StageFailure)
              ├─ matched, Recovered=true  ──► stageIdx++; continue
              ├─ matched, Recovered=false ──► record + fall through
              └─ no match                 ──► fall through
                                  │
                                  ▼
                       Model escalation eligible?
                                  │ no
                                  ▼
                        Terminal failure
```

The registry sits between adaptive stall-recovery and model escalation. Stall
rewind has higher priority because it is the more invasive recovery (re-runs
upstream stages); the registry's actions are in-place self-heals. Model
escalation runs only when no deterministic action could recover the failure.

### Insertion site

`internal/orchestrator/scheduler.go` `runPipeline`'s failure branch — see the
block introduced by Issue #3268 immediately before
`s.retryEngine.EvaluateEscalation`. The `recoveryAttemptsThisRun` counter
bounds the per-run budget; the cap is read once via `recovery.Default()`
from `pipeline.recovery.max_attempts_per_run` (default 3).

### Wiring

`recovery.Default(workspaceRoot, prMergeRunner, prCreateRunner)` returns a
registry with all eight default actions. Tests replace the registry via
`Scheduler.WithRecoveryRegistry(r *recovery.Registry)` to inject a controlled
action set and a small cap.

## Registered actions

Order matters: the registry walks the slice and executes the first action
whose `Matches` predicate returns true. When two actions could match the same
failure, the more specific one MUST come first.

| Order | Action id (`Name()`)               | Triggering signal                                                                                                | Recovery                                                                                                                                                                                                                                 |
| ----- | ---------------------------------- | ---------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1     | `stall-killed-on-pr-merge`         | pr-merge stage stall-killed (not cost-cap), PR exists                                                            | re-run `pmstages.PRMergeRunner`; on `PathMerged` → recovered                                                                                                                                                                             |
| 2     | `conflict-recovery-loop`           | pr-merge gate KindNoOp, evidence mentions a **conflict**, workspace set                                          | emit `CONFLICT_RESOLUTION_NEEDED` feedback → **rewind to feature-dev** on the same branch (`Recovered=false`, `FollowUp=stage can resume`); escalate once `max_dev_redispatch` exhausted or context missing                              |
| 3     | `skill-exited-without-merging`     | pr-merge gate KindNoOp ("PR is not MERGED"), PR exists, not stall-kill                                           | re-run `pmstages.PRMergeRunner`; on `PathMerged` → recovered                                                                                                                                                                             |
| 4     | `skill-exited-without-creating-pr` | pr-create gate KindNoOp, no PR yet                                                                               | re-run `pmstages.PRCreateRunner`; on `CreatePathCreated` → recovered                                                                                                                                                                     |
| 5     | `branch-out-of-date`               | pr-merge gate KindNoOp, evidence mentions `BEHIND` or `DIRTY` (`dirty-merge-state`)                              | `fetch`+`rebase origin/main`+`force-with-lease`, then **wait for CI on the rebased head** and **re-run `pmstages.PRMergeRunner`**; recovered ONLY on `PathMerged`. A rebase that hits a real conflict defers to `conflict-recovery-loop` |
| 6     | `ci-check-transiently-failed`      | pr-merge gate KindNoOp, evidence mentions failed-ci-checks, exactly one failing check                            | `gh run rerun <run-id> --failed`; poll until completed; SUCCESS → recovered                                                                                                                                                              |
| 7     | `stale-project-status`             | failure reason mentions `stale-project-status`; issue verified CLOSED on GitHub                                  | `nightgauge project move-status <N> done`                                                                                                                                                                                                |
| 8     | `pipeline-heal-base`               | pr-merge gate KindNoOp, evidence mentions `pipeline-failed-inherited`, baseline file has inherited-only failures | match `internal/heal/` pattern registry → create heal PR against base branch (possibly cross-repo)                                                                                                                                       |

> **Ordering note (#4072):** `conflict-recovery-loop` is registered **before**
> `branch-out-of-date`. A plain `BEHIND` is a fast-forward the rebase resolves
> on its own; a genuine **content conflict** needs the LLM dev stage, so it must
> be routed to the dev re-dispatch first (first-match-wins). `branch-out-of-date`
> still owns the case where the BEHIND/DIRTY rebase only _discovers_ the conflict
> at `git rebase` time — in that case it captures the conflict context and
> defers to the same dev-rewind path rather than escalating.

### Pipeline-heal-base (#3683)

`pipeline-heal-base` is the most aggressive recovery action. When the pr-merge
auto-fix loop's Step 2.5 has classified every failure as `inherited` — i.e.
main is broken, not this PR — the action reads
`.nightgauge/pipeline/auto-fix-baseline-{PR}.json`, matches the cluster
against the heal pattern registry in `internal/heal/`, and opens a fix-PR
against the affected base branch.

Pattern registry: see `internal/heal/registry.go`. Patterns are an allowlist —
only patterns compiled into the binary can produce a heal PR. Adding a new
pattern requires a human-reviewed PR.

Guardrails (configurable via `pipeline.heal.*`, see
[CONFIGURATION.md](CONFIGURATION.md#pipelineheal-issue-3683)):

- **Human-approval gate (#4136, default on, not configurable off)**: because a
  base-branch heal PR mutates the base branch entirely outside the feature-dev
  path, the architecture-approval gate never sees it. The action therefore
  requires an out-of-band human approval **before** it pushes any branch or
  opens the PR. Approve via either:
  - the `pipeline-heal:approved` label on the **failing** PR (the durable
    signal — survives worktree cleanup), or
  - a workspace file `.nightgauge/pipeline/approval-heal-base-{PR}.json`
    containing `{"approved": true}`.

  Until approved, the action returns human-triage with an approval-required
  reason and performs no git/forge mutation. This gating lives on the recovery
  path — the pure `approvalGate.Evaluate` trade-off function is unchanged.

- **Per-repo active throttle** (default 1): if `pipeline-heal:auto` PRs are
  already open at or above the cap, the action declines.
- **24h throttle** (default 3): heal PRs created in the trailing 24h count
  toward the cap regardless of merge state.
- **First-occurrence gate** (default on): the first time a given pattern
  fires, the action opens the PR with `pipeline-heal:needs-review` instead of
  `pipeline-heal:auto`. Subsequent occurrences default to auto.
- **Diff budget**: patterns supply `DiffLineEstimate`; auto-merge gates
  (enforced outside this action) may decline when the estimate exceeds the
  configured budget.

The action never claims `Recovered=true` — main is not fixed by the time
Execute returns. It always returns `FollowUp=issue requires human triage`
because a real PR is now in flight that needs review.

### Branch-out-of-date / wave merge-train (#4071)

`branch-out-of-date` is the runtime half of the wave merge-train: when two
same-wave PRs touch shared files, the first merges and the second goes `BEHIND`
(or `DIRTY`). `stages.Decide()` correctly punts on any non-`CLEAN`
mergeStateStatus, so the pr-merge gate surfaces a `KindNoOp` with `BEHIND` /
`dirty-merge-state` evidence rather than merging the stale sibling.

Recovery is **not** a bare rebase. After `git fetch origin main` →
`git rebase origin/main` → `git push --force-with-lease`, the action:

1. **Waits for CI on the rebased head** — the pre-rebase checks are stale, so it
   polls `gh pr view --json statusCheckRollup` with a tight, bounded budget
   (`DefaultRebaseCIPolls` × `DefaultRebaseCIPollInterval`, with a `ctx.Done()`
   guard). A failing check → triage; still-in-flight after the budget → declines
   without claiming recovery (no merge attempted).
2. **Re-runs `pmstages.PRMergeRunner`** once CI is green. `Decide()` now finds
   the snapshot `CLEAN` and the runner issues + re-verifies the merge.
3. Claims `Recovered=true` **only on `PathMerged`** — so the rebased PR actually
   lands instead of being skipped while the scheduler advances. A runner punt
   (e.g. review still required) returns `FollowUp=issue requires human triage`.

Real rebase **conflicts** now defer to `conflict-recovery-loop` (#4072): the
action captures the conflicting files + both sides into
`conflict-context-{N}.json` and emits a `CONFLICT_RESOLUTION_NEEDED` feedback
signal **before** `git rebase --abort` (the conflict blobs vanish after the
abort), then returns `FollowUp=stage can resume` so the scheduler rewinds to
feature-dev. It still aborts the rebase to leave the tree clean for the
re-dispatch. It never resolves the conflict itself.

### Conflict-recovery-loop (#4072)

`conflict-recovery-loop` is the **one recovery action that triggers an LLM stage
rewind** rather than a deterministic fix. The action itself stays deterministic
(per the rule below): on a pr-merge `KindNoOp` whose evidence names a conflict,
it reads `conflict-context-{N}.json` (written by the pr-merge skill's Step 6.1.5
or by `branch-out-of-date`'s deferral), ensures a `CONFLICT_RESOLUTION_NEEDED`
feedback signal is present in `feedback-{N}.json` (merging — never clobbering an
existing feature-validate signal), and returns `Recovered=false` +
`FollowUp=stage can resume`. The scheduler honors that follow-up by evaluating
the feedback file and **rewinding the pipeline to feature-dev on the same
branch** (`scheduler.go`). The actual conflict resolution is done by the LLM dev
stage, which checks out the existing PR branch (never a fresh branch from main),
resolves preserving both sides, and flows forward through feature-validate →
pr-create → pr-merge.

Bound: `pipeline.recovery.conflict_recovery.max_dev_redispatch` (default 2,
[CONFIGURATION.md](CONFIGURATION.md#pipelinerecoveryconflict_recovery-4072)).
It is enforced by two cooperating layers, both sized by `max_dev_redispatch`:
the **in-memory per-edge count** in the RetryEngine is the authoritative
termination bound (reliable on every path, cleared per run), while the
**on-disk `CONFLICT_RESOLUTION_NEEDED` signal count** in `feedback-{N}.json` is
the primary escalation trigger on the normal path (the pr-merge skill appends one
signal per failure). Whichever trips first stops the loop at exactly
`max_dev_redispatch` re-dispatches. Once exhausted — or when the context file is
missing (e.g. a rebase failed with no markers) — the loop ends with a terminal
state naming the specific conflicting files. This converts the old dead-stop
(blind fresh-branch restart that discarded all dev work, then human triage) into
either active dev work on the same branch or an explicit, file-named escalation.

Conflict re-dispatches are **independently bounded** and do **not** draw from
`pipeline.recovery.max_attempts_per_run` (the global per-run cap) — they are
cap-exempt, so an unrelated earlier recovery in the same run cannot silently
shorten the configured conflict bound.

See [FEEDBACK_LOOPS.md](FEEDBACK_LOOPS.md) for the `CONFLICT_RESOLUTION_NEEDED`
signal + `conflict-context-{N}.json` schema, and
[PR_MERGE_STAGE.md](PR_MERGE_STAGE.md) for the pr-merge skill side.

Same-wave merges are serialized per repo by the scheduler's per-repo merge lock
(`Scheduler.getMergeLock(repo)`, held for the whole pr-merge stage scope
including recovery), so the second PR is naturally re-validated against the
just-merged `main`. The CI-wait budget is kept tight precisely because the lock
is held during the wait — a never-green head must not head-of-line block the
rest of the wave's merges.

### Determinism rule

Per `.claude/rules/scripts.md`, every recovery action is **deterministic-only**.
Fixed input → fixed output. No LLM calls. Allowed primitives:

- `gh` CLI (via `recovery.execGh` indirection — stubbed in tests)
- `git` CLI (via `recovery.execGit` indirection — stubbed in tests)
- the local `nightgauge` CLI (via `recovery.execNightgauge`)
- the deterministic stage runners (`pmstages.PRMergeRunner`,
  `pmstages.PRCreateRunner`)

Action authors MUST NOT introduce new shell-out points; reuse the existing
indirections so tests stay deterministic.

`conflict-recovery-loop` (#4072) does not break this rule: its `Execute` only
reads a JSON sidecar and writes a feedback signal (deterministic IO via the same
indirections). It performs **no LLM call**. The probabilistic conflict
resolution happens later, in the rewound feature-dev stage — a normal pipeline
stage, not the recovery action. The recovery action merely sets up the rewind.

## Adding a new recovery action

1. Add a new file `internal/orchestrator/recovery/<action_name>.go` with a
   struct that implements `RecoveryAction` (`Name`, `Description`, `Matches`,
   `Execute`).
2. Add the sibling test file `<action_name>_test.go` with at least:
   - `TestAction_<Name>_Matches_AndRecovers` — happy path
   - `TestAction_<Name>_NoMatch_FallsThrough` — at least 3 false-positive cases
3. Register the action in `recovery.Default()` with a comment explaining
   ordering vs the existing actions when the predicates overlap.
4. Update the table in this doc.
5. If the action consumes new context, extend `StageFailure` (additive) and
   the scheduler's failure-handler hand-off site.

## Telemetry

Stage-level: every matched attempt is appended to
`V2StageDetail.RecoveryAttempts` via `runtime.AppendRecoveryAttempt`. The
`RecoveryAttempt` shape:

```json
{
  "action": "skill-exited-without-merging",
  "recovered": true,
  "reason": "PR #1234 merged via deterministic runner (clean-mergeable: merged)",
  "evidence": ["pr=1234", "runner_reason=clean-mergeable: merged"],
  "follow_up": "stage can resume",
  "cost_usd": 0,
  "duration_ms": 387,
  "at": "2026-05-09T22:35:42Z"
}
```

Event: `pipeline.recovery_attempt` is emitted via the platform telemetry
service with the same shape plus `attempt_ordinal`.

### Distinction from `V2RunRecord.RecoveryEvents`

`RecoveryAttempts` is **per-stage** (deterministic auto-triage); a stage can
carry many. `RecoveryEvents` (Issue #3239) is **per-run** and records the VS
Code Recovery Dialog interactions. The two coexist on the same record but
their producers and consumers are different — keep them separate.

## What the registry is NOT

- **Not a replacement for stall-recovery.** Stall rewind is the more invasive
  recovery and runs first.
- **Not a replacement for RetryEngine.** Model escalation runs after the
  registry declines. The model-unavailable fallback (#42) also lives on the
  RetryEngine, not here: when the API rejects the stage's model
  (`terminal kind model_unavailable` — not on plan / unknown ID / model usage
  cap), the failure branch substitutes the next-best tier
  (fable → opus → sonnet → haiku, sticky for the run) and retries the stage
  INSTEAD of escalating upward — a stronger model on a plan that already
  refused this one would be rejected the same way.
- **Not a place for LLM-based decisions.** The determinism rule applies.
- **Not a denormalized run-level summary.** `V2RunRecord.RecoveryEvents` is
  reserved for the Recovery Dialog (Issue #3239).
- **Not stateful across runs.** The cap counter and registry resets on every
  pipeline run; recovery actions are stateless.

## Configuration

```yaml
# .nightgauge/config.yaml
pipeline:
  recovery:
    max_attempts_per_run: 3
  heal:
    max_active_per_repo: 1
    max_24h_per_repo: 3
    diff_budget_lines: 30
    require_human_first: true
```

Env override: `NIGHTGAUGE_RECOVERY_MAX_ATTEMPTS=5`. Higher precedence
than YAML, mirroring `GetPipelineFailureMode`.

## Future Work

- **Self-healed runs panel** on the pipeline-health dashboard — surfaces
  `RecoveryAttempts` aggregate per repo / per action (follow-up issue).
- **LLM-fallback actions** — out of scope. The registry is deterministic-only.
- **Cross-run recovery memory** — out of scope. The registry is stateless.
