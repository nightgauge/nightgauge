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

### `ours` / `theirs` are the consumer's vocabulary, not git's (#301)

**`ours` is always the PR branch's own work; `theirs` is always the base it is
being landed onto.** Git's index stage names are relative to what is _checked
out_, and a rebase checks out the upstream and replays your commits onto it — so
under a rebase git calls the base "ours", the exact inverse of a merge. Every
writer translates; a writer that passes git's naming through hands feature-dev
the base branch under the field its intake calls "this PR's feature work", and
the resolution then inverts both sides and flows forward through
feature-validate → pr-create → pr-merge.

| operation | `ours` (PR branch work) | `theirs` (base) | detected from                      |
| --------- | ----------------------- | --------------- | ---------------------------------- |
| rebase    | index stage 3           | index stage 2   | `rebase-merge/` or `rebase-apply/` |
| merge     | index stage 2           | index stage 3   | `MERGE_HEAD`                       |

`conflict_operation` on the document records which mapping was applied.

### Write invariant — the file's existence IS the claim (#301)

A `conflict-context-{N}.json` is written **only** for a capture that found at
least one conflicting path, and the Go writer writes one only when EVERY such
path is representable and the `branch` resolved. `conflicting_files` is never
empty (`ConflictContextSchema` requires ≥ 1). A non-captured outcome also
REMOVES a document a previous attempt left behind — `.nightgauge/pipeline/`
outlives the run, and a stale context reads exactly like a fresh one.

There are two writers and they fail differently, on purpose:

- **The Go writer** (`branch-out-of-date`) writes nothing at all on a failed or
  empty capture. Absence is the whole signal.
- **The skill writer** (`merge.md` `capture_conflict_and_signal`) is an
  LLM-driven shell function that has already emitted output by the time a blob
  read fails, so it marks the document `capture_failed: true` and names the
  reason in `capture_error`. `branch` may then be the `"unknown"` sentinel —
  never silently, always paired with the marker.

Readers reject both shapes. `"unknown"` is a hard stop in its own right:
feature-dev's conflict intake skips the branch checkout on that value, so a
context carrying it would silently discard the same-branch guarantee this whole
loop exists to provide.

"Representable" is checked **per path**, not in aggregate (#301). One readable
file must not license a capture whose siblings landed with both sides empty. A
path fails the check when its index blob is unreadable, is not valid UTF-8
(`encoding/json` substitutes U+FFFD for every invalid byte, so a binary conflict
cannot round-trip), exceeds the per-side size cap (1 MiB — a truncated side is
worse than none, because feature-dev resolves against what the context says), or
carries a path NAME that is not valid UTF-8. Any failing path fails the whole
capture; the operator's remedy is the `conflict-evidence-{N}/` dump described
below.

**A submodule pointer is metadata, not bytes.** `git ls-files -u` reports a
conflicted gitlink with index mode `160000`, and its per-stage object ids are
COMMITs in the submodule's own object store — `git cat-file blob <that id>`
exits 128 in the superproject. Such a side is recorded as
`ours_mode`/`theirs_mode` `160000` plus `ours_commit`/`theirs_commit`, with
`ours`/`theirs` empty and **no object read anywhere**, in the context document
and in the evidence dump alike. Symlinks (`120000`) are ordinary blobs whose
content is the target path and keep being inlined normally; the rule keys on
"is this a blob", not on "is this mode unusual". Getting this wrong was not a
data-quality bug: the capture failed, the dump failed for the same reason, the
abort was therefore suppressed, and an ordinary pointer conflict left a detached
worktree with an unmerged index that nothing in the pipeline reclaims.

Delete/delete conflicts (index stage 1 only) are likewise captured, as an entry
with `ours_present` and `theirs_present` both `false`.

The index is enumerated with `git ls-files -u -z`, never
`git diff --name-only --diff-filter=U`: the latter C-quotes non-ASCII paths (a
conflict in `café.txt` prints as the literal `"caf\303\251.txt"`), and `-z` also
removes newline ambiguity. `ls-files -u` additionally yields the per-stage blob
ids, so every blob is read by id and no path has to survive a round-trip through
a git argument.

The three ways a capture can end are three distinct states:

| Capture outcome     | Meaning                                                               | Artifacts                     | `rebase --abort`                      | Follow-up     |
| ------------------- | --------------------------------------------------------------------- | ----------------------------- | ------------------------------------- | ------------- |
| `captured`          | ≥ 1 conflicting path, all representable, branch resolved              | context file + signal         | yes — the context IS the durable copy | stage resumes |
| `no-conflict-state` | enumeration succeeded, zero unmerged paths (dirty index, unborn base) | none                          | yes — the rebase is this run's own    | human triage  |
| `failed`            | enumeration errored, branch unresolvable, or a path unrepresentable   | `conflict-evidence-{N}/` dump | yes, once the dump succeeded          | human triage  |

**A failed capture preserves the raw index and then aborts.** The `:2:`/`:3:`
stages are copied out verbatim to `.nightgauge/pipeline/conflict-evidence-{N}/`
(content-addressed `blobs/<sha>` plus a `manifest.json` naming which stage of
which path each blob was) before `git rebase --abort` runs. Evidence carries
`evidence_preserved=true` and `evidence_dir=…`.

Leaving the rebase in progress instead does **not** preserve anything here: the
scheduler's terminal defer reads the conflicted `UU` paths as uncommitted work
and `git add -A`s the stages away seconds later, commits conflict markers onto
the detached HEAD, and books the run as `worktree_uncommitted` — a kind meaning
"recovered, not a failure". The detached worktree is then skipped by the sweep
forever and cannot be checked out or stashed. Copying the bytes out and aborting
keeps the evidence and leaves a worktree the rest of the system can handle.
(`RecoverUncommittedWork` now refuses an unmerged index outright as well.)

The one exception is the case where the capture failed AND the dump failed too:
aborting then would leave zero record, so the in-index stages are kept as the
last copy, with `evidence_preserved=false` and `rebase_left_in_progress=true`.
That state is genuinely unrecoverable by the pipeline — `RecoverUncommittedWork`
refuses an unmerged index, the reconcile sweep skips a detached HEAD, and worktree
reuse hands the same tree to the next run — so the escalation names the manual
remedy (copy each side out with `git show :2:`/`:3:`, or read the commit ids from
`git ls-files -u` for a submodule, then `git rebase --abort`). It requires a git
that cannot read back a blob it just listed, or an unwritable tree; a submodule
conflict is **not** one of these cases (see the gitlink rule above), which it
used to be.

The `captured` abort is the one whose exit status is checked: it is the only
outcome that returns a resumable stage, and an abort that failed would leave a
live rebase with an unmerged index for feature-dev to be dispatched into. A
failed abort downgrades to human triage with `abort_failed=true`. The other two
aborts already return human triage, so they stay best-effort.

Otherwise the follow-up is human triage, never a resumable stage — a dev stage
must not be dispatched against a conflict nobody could record.

**A rebase that was already in progress is never touched.** `branch-out-of-date`
probes for `rebase-merge`/`rebase-apply` before it mutates anything and escalates
with `preexisting_rebase=true` if one exists. It cannot tell an operator's paused
`git rebase -i` from its own, and aborting the former destroys work no artifact
records.

Readers enforce the same invariant, at the same granularity as the writers.
`conflict-recovery-loop` escalates on a missing context file, on a
`capture_failed: true` marker, on one naming zero files, on any ENTRY whose two
sides are both empty with nothing explaining why, and on one naming no
resolvable branch. Rejecting only the whole-document shapes was not enough: a
two-file context with one silently-empty entry passed every check and cost the
full `max_dev_redispatch` budget. An entry is legitimately empty on both sides
only when a non-blob mode says its content is a commit id, or the presence flags
say the index carried no such side.

| Field                | Type     | Description                                                           |
| -------------------- | -------- | --------------------------------------------------------------------- |
| `schema_version`     | string   | Schema version (`"1.1"`)                                              |
| `issue_number`       | integer  | GitHub issue number                                                   |
| `pr_number`          | integer  | The open PR number                                                    |
| `branch`             | string   | The PR's head branch (checked out as-is by feature-dev)               |
| `base_ref`           | string   | The base branch the rebase targets (e.g. `main`)                      |
| `conflict_operation` | string   | `"rebase"` or `"merge"` — which stage→side mapping the writer applied |
| `capture_failed`     | boolean  | Writer's admission it could not record faithfully (skill writer only) |
| `conflicting_files`  | array    | See the per-entry fields below                                        |
| `created_at`         | datetime | ISO 8601 timestamp (nullable)                                         |

Per `conflicting_files[]` entry:

| Field                           | Type    | Description                                                                   |
| ------------------------------- | ------- | ----------------------------------------------------------------------------- |
| `path`                          | string  | Repo-relative path, raw bytes from `ls-files -z` (never C-quoted)             |
| `ours` / `theirs`               | string  | The PR branch's side / the base's side. Empty for a gitlink or an absent side |
| `ours_present` / `…_present`    | boolean | Whether the index carried that stage at all (`false` = deleted on that side)  |
| `ours_mode` / `theirs_mode`     | string  | Index mode of that stage (`""` when absent). Non-blob ⇒ the entry is metadata |
| `ours_commit` / `theirs_commit` | string  | Submodule commit id. Present only when the corresponding mode is `160000`     |

```json
{
  "schema_version": "1.1",
  "issue_number": 143,
  "pr_number": 200,
  "branch": "feat/143-thing",
  "base_ref": "main",
  "conflict_operation": "rebase",
  "capture_failed": false,
  "conflicting_files": [
    {
      "path": "internal/foo.go",
      "ours": "<the PR branch's version>",
      "theirs": "<the base's version>",
      "ours_present": true,
      "theirs_present": true,
      "ours_mode": "100644",
      "theirs_mode": "100644"
    },
    {
      "path": "vendor/sub",
      "ours": "",
      "theirs": "",
      "ours_present": true,
      "theirs_present": true,
      "ours_mode": "160000",
      "theirs_mode": "160000",
      "ours_commit": "0f1e2d3c4b5a69788796a5b4c3d2e1f009182736",
      "theirs_commit": "9182736450f1e2d3c4b5a69788796a5b4c3d2e1f"
    }
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
