# PR Merge Stage — Two-Path Architecture

> Status: shipped in PR #3264.
>
> Predecessors: #3001, #3259, #3260.

The `pr-merge` pipeline stage runs in one of two modes:

1. **Deterministic path** (default) — a Go-native stage runner that consults
   GitHub via `gh pr view`, decides whether the PR is safe to merge, and
   issues `gh pr merge --squash --delete-branch` directly. Zero LLM tokens,
   zero subagent processes, completes in seconds.
2. **LLM path** (fallback) — the existing `nightgauge-pr-merge` skill
   runs via the normal `StageRunner` → Claude pipeline. Reached only when the
   deterministic runner punts.

The post-stage checkpoint (`Scheduler.verifyPRMerged` on the Go-auto path,
`verifyPostMergeState` on the TS/IPC path) runs after either path and is the
**single fail-closed authority** on whether the merge landed: it asserts
`state == MERGED` **and** that the linked issue closed, names the specific
blocker when it did not, and halts the pipeline on failure (#4070). See
[Post-stage gate](#post-stage-gate) below.

## When the deterministic path runs

The runner pre-flights the PR with:

```text
gh pr view <N> --json state,statusCheckRollup,mergeable,mergeStateStatus,reviewDecision
```

It then evaluates a pure decision function over the typed snapshot:

| `state`  | `mergeable`   | `mergeStateStatus`     | failed checks    | review        | Result                        |
| -------- | ------------- | ---------------------- | ---------------- | ------------- | ----------------------------- |
| `MERGED` | -             | -                      | -                | -             | merged                        |
| `OPEN`   | `MERGEABLE`   | `CLEAN`                | none             | approved/none | merge → re-poll → merged      |
| `OPEN`   | `MERGEABLE`   | `BLOCKED` / `UNSTABLE` | none, CI pending | approved/none | **wait for CI** → re-evaluate |
| `OPEN`   | `CONFLICTING` | -                      | -                | -             | punt                          |
| `OPEN`   | `MERGEABLE`   | `DIRTY` / `BEHIND`     | -                | -             | punt                          |
| `OPEN`   | `MERGEABLE`   | `CLEAN`                | any              | -             | punt                          |
| `OPEN`   | `MERGEABLE`   | `CLEAN`                | none             | required      | punt                          |
| `CLOSED` | -             | -                      | -                | -             | punt                          |

### Bounded CI wait (Issue #297)

pr-merge starts **immediately** after pr-create, so on repos whose CI takes
minutes (bowlsheet ~10 min) the PR's first snapshot is `BLOCKED`/`UNSTABLE` with
still-running checks. Pre-#297 the runner punted `dirty-merge-state: BLOCKED` on
**every** such run and the LLM skill "won" pr-merge purely by babysitting CI for
~10 minutes at ~$3–4.44/run.

When the **only** thing blocking an otherwise mergeable, conflict-free,
review-clean PR is **in-flight CI** (`mergeBlockedByPendingCI`: `MERGEABLE`, no
`FAILURE`/`ERROR` check, review not blocking, `BLOCKED`/`UNSTABLE` with ≥1
pending check), the runner now **polls until the merge state clears** —
`DefaultCIPollMax` × `DefaultCIPollInterval` = 30 × 30 s = 15 min — instead of
punting. On each poll it re-evaluates: a check reporting `FAILURE`/`ERROR`, a
conflict, or a required review appearing mid-wait ends the wait and punts with
that reason; a `CLEAN` state issues the merge; exhausting the budget punts
`ci-wait-timeout`. A structural blocker (`DIRTY`, `BEHIND`, `DRAFT`, or
`BLOCKED`/`UNSTABLE` with no pending checks) never triggers a wait — it punts
immediately so the LLM path still gets its turn. The classifier and wait are
pure/bounded and unit-tested (`prmerge_test.go`, `TestMergeBlockedByPendingCI`,
`TestDeterministicRunner_CIPending_*`).

After issuing the merge call, the runner re-polls (4 × 2 s) for `state == MERGED`
to absorb GitHub's eventual-consistency window. If polls exhaust without
observing `MERGED` — or the post-merge re-fetch errors — the runner **punts**
with `merge-ec-timeout` rather than self-reporting `merged` (#4070). It never
claims a merge it did not observe; the canonical `verifyPRMerged` checkpoint is
the sole MERGED authority.

### Rate limits

A `429` / `rate limit exceeded` error from `gh pr view` or `gh pr merge` is
**not retried inside the deterministic path** (ADR-004) — the runner returns
`Path: "punt"` with `Reason: "rate-limited"`.

**The scheduler no longer falls through to the LLM path on a rate-limit punt
(ADR-006, Issue #3976).** The skill would only re-shell `gh pr merge` into the
same exhausted bucket — a near-certain re-failure that burns $5–$25 of tokens
and can leave the issue stuck "In review". Instead the scheduler fails the stage
with a `github-quota-low` marker, which `ClassifyTerminalKind` routes to
`TerminalKindGitHubQuotaLow` and the autonomous scheduler handles via the #3896
environmental-recovery path: a **GLOBAL** GitHub-quota cooldown until the bucket
resets, the issue reverted to **Ready**, **no** lifetime-failure-cap increment,
and **zero** LLM tokens. The post-condition reconcile gate (#3835) still runs
first, so a merge that actually landed (merge call succeeded, only the re-poll
was throttled) is reconciled to success rather than deferred.

pr-create gets the same treatment: its GitHub calls go through the in-process
client (already rate-limit-aware via #3976), but if a punt reason still carries a
rate-limit signal the scheduler defers it identically rather than spending the
LLM path.

### Failure modes

| Reason                         | Cause                                                                           | What happens               |
| ------------------------------ | ------------------------------------------------------------------------------- | -------------------------- |
| `no-pr-context-file`           | `pr-{N}.json` missing (pr-create did not write it)                              | Punt → LLM                 |
| `pr-context-invalid-json`      | Corrupted pr-{N}.json                                                           | Punt → LLM                 |
| `pr-context-missing-pr-number` | pr-{N}.json present but `pr_number` is empty/zero                               | Punt → LLM                 |
| `gh-unavailable`               | `gh` CLI not on PATH                                                            | Punt → LLM                 |
| `rate-limited`                 | GitHub or `gh` rate limit                                                       | Defer → cooldown (ADR-006) |
| `unexpected-error: …`          | Anything else from `gh`                                                         | Punt → LLM                 |
| `merge-call-failed: …`         | Pre-flight passed but `gh pr merge` returned non-zero                           | Punt → LLM                 |
| `not-mergeable`                | `mergeable != MERGEABLE`                                                        | Punt → LLM                 |
| `dirty-merge-state`            | `mergeStateStatus != CLEAN`                                                     | Punt → LLM                 |
| `failed-ci-checks: <name>`     | At least one `FAILURE` or `ERROR` check                                         | Punt → LLM                 |
| `review-not-approved: …`       | `REVIEW_REQUIRED` or `CHANGES_REQUESTED`                                        | Punt → LLM                 |
| `merge-ec-timeout: …`          | Merge call succeeded but post-merge re-fetch errored or never observed `MERGED` | Punt → LLM (see #4070)     |

> **#4070 — no self-reported merge on post-verify failure.** The deterministic
> runner used to return `PathMerged` when the post-merge re-fetch _errored_
> (logging a "post-verify warn"). That was a phantom-success risk: a masked
> fetch failure could close the issue on an unconfirmed merge. The runner now
> **punts** (`merge-ec-timeout`) on any post-verify failure, deferring the
> MERGED verdict entirely to the canonical scheduler checkpoint below.

## When the LLM path runs

The skill at `skills/nightgauge-pr-merge/SKILL.md` runs unchanged when:

- The deterministic runner punts (any of the reasons above).
- The runner is explicitly disabled (no production code path disables it
  today, but `Scheduler.WithPRMergeRunner(nil)` will skip it for tests).

The skill is responsible for content-heavy work that the deterministic path
cannot author:

- Resolving real merge conflicts. When the skill cannot land a rebase conflict
  in-place (Step 6.1.5), it no longer triggers a blind fresh-branch restart that
  discards the dev work. It captures `conflict-context-{N}.json` (conflicting
  files + both sides) and emits a `CONFLICT_RESOLUTION_NEEDED` feedback signal,
  **preserving the branch**; the `conflict-recovery-loop` recovery action then
  re-dispatches feature-dev on the same branch to resolve it (#4072). See
  [AUTO_TRIAGE.md](AUTO_TRIAGE.md#conflict-recovery-loop-4072) and
  [FEEDBACK_LOOPS.md](FEEDBACK_LOOPS.md#conflict-resolution_needed-4072).
- Future: opt-in release-note authoring (out of scope for #3264).

## Telemetry

Per-stage `execution_path` is recorded on `V2StageDetail` (Go) /
`HistoryStageDetail` (TS Zod). Values:

- `"deterministic"` — the deterministic path completed the stage.
- `"llm"` — the deterministic path punted; the skill ran.
- _absent_ — record predates PR #3264; readers MUST treat as
  `unknown` rather than defaulting.

`punt_reason` (Issue #297) sits alongside `execution_path` on both
`V2StageDetail.PuntReason` (Go) and `HistoryStageDetail.punt_reason` (TS Zod).
It carries the machine-readable reason the deterministic path declined —
`missing-dev-context`, `dirty-merge-state: BLOCKED`, `ci-wait-timeout`, … — and
is present **only** when `execution_path == "llm"` and a deterministic hook
actually ran and punted (absent on deterministic successes and on stages with no
deterministic hook). This closes the diagnosis gap that made #288 take forensic
session-log archaeology: the history JSONL now answers **why** the expensive path
ran. Both producers write the identical wire shape — Go via
`RuntimeState.RecordExecutionPath` / `RecordStagePuntReason` → `BuildV2Record`;
the TS `HeadlessOrchestrator` via its per-stage execution-path map →
`ExecutionHistoryWriter.buildRunRecord`.

> **Root cause of #297 — which orchestrator actually runs.** The Go
> deterministic-first hooks (`tryDeterministicPRMerge`/`tryDeterministicPRCreate`,
> `scheduler.runPipeline`) fire on the `nightgauge run` CLI and IPC-`StageRunner`
> paths. The VSCode **autonomous / concurrent** dogfood runs execute through the
> legacy TS `HeadlessOrchestrator.runPipeline` (one per `ConcurrentPipelineManager`
> slot, in each issue's `.worktrees/issue-N`), which drives its own stage loop and
> **never round-trips the Go scheduler** — so the Go hooks (and #288's
> `stageWorkspace` fix) never applied to those runs. `HeadlessOrchestrator` has a
> deterministic short-circuit only for `issue-pickup`; its pr-create/pr-merge
> stages always ran the LLM skill, and its post-stage `tryDeterministicMergeFallback`
> requires an already-`CLEAN` merge state so it declined on pending CI. Migrating
> the HeadlessOrchestrator pr stages to a deterministic-first attempt (reusing the
> shared classifier `orchestrator/stages/prMergeReadiness.ts` and the Go runner's
> bounded CI wait) is the dogfood cost fix; #297 lands the observable decision,
> the CI-wait on the canonical Go runner, and the shared decision core.

> **Dogfood cost fix landed — Issue #300.** `HeadlessOrchestrator.runPipeline`
> now runs pr-create/pr-merge **deterministic-FIRST** via a new
> `nightgauge pr-stage <create|merge>` CLI seam
> (`cmd/nightgauge/pr_stage.go`). That verb constructs the **exact** runners the
> Go scheduler uses (`orchestrator.NewDefaultPRCreateRunner` /
> `stages.NewDeterministicRunner`) — including the rich-body render (create) and
> the bounded CI-wait (merge) — so there is no second, divergent decision matrix
> in TypeScript. `HeadlessOrchestrator.runDeterministicPrStage` invokes the verb
> with `--workdir <worktree>` (the #288 context-locality contract: pr-{N}.json
> and dev/validate context live only in the worktree on concurrent runs) and
> reacts to the small JSON contract exactly like the scheduler:
>
> - **created / merged** → record `execution_path="deterministic"`, complete the
>   stage, and SKIP the LLM skill (~$0), still flowing through the normal
>   post-success gates (`verifyPostCreateState` / `verifyPostMergeState`,
>   context validation, outcome recording).
> - **punt** → record `execution_path="llm"` + `punt_reason`, then fall through
>   to `executeSkill` exactly as before (no behavior regression).
> - **rate-limited** → **DEFER**: never run the LLM into an exhausted GitHub
>   bucket (#3976); trip the rate-limit breaker and fail the stage with a
>   `[github-quota-low]` marker that routes to the transient cooldown path.
>
> Fails **open**: any binary-resolution / parse / subprocess error returns the
> `llm` outcome so the pipeline degrades to the pre-#300 behavior. The CLI verb
> writes `reviewers: []` into pr-{N}.json (required non-null by the SDK
> `PRContextSchema` the TS path validates, which the Go-only scheduler path never
> ran).

`execution_path` is part of the local history record. Optional telemetry
integrations receive only fields present in their documented public schema; the
local value must not be assumed to upload automatically.

When the deterministic path lands a merge, the scheduler also emits a
`stage_deterministic` `pipeline_event` with metadata:

```json
{
  "path": "merged",
  "pr_number": 1234,
  "pr_state": "MERGED",
  "reason": "already-merged",
  "duration_ms": 1234
}
```

When the deterministic path **punts** to the LLM, the scheduler emits the
companion `stage_punt` `pipeline_event` (Issue #297) so the decision is
observable on **both** outcomes, not just success:

```json
{
  "execution_path": "llm",
  "reason": "dirty-merge-state: BLOCKED"
}
```

Dashboards group on `stage_name + execution_path` to surface the cost split.
A populated chart panel is a follow-up issue (see ADR-005); the data field
ships in #3264.

## Post-stage gate

### `verifyPRMerged` — the single fail-closed MERGED authority (#2843, #4070)

`Scheduler.verifyPRMerged` (Go) is the **sole** authority on whether pr-merge
actually landed. It runs in `runPipeline` after **every** pr-merge attempt —
deterministic or LLM — "regardless of which path produced the result". No
other code path may report merge success: the deterministic runner only ever
returns `PathMerged` after observing `MERGED` itself, and a post-verify
_failure_ now punts (see #4070 note above) so this checkpoint is unavoidable.

Its fail-closed contract:

1. Parse the PR URL from runtime (`PrUrl`). Empty URL / nil client → trust the
   upstream result (test/mock seams; defense-in-depth, not the only gate).
2. `GetPR`. When `state == MERGED`, **additionally assert the linked issue is
   CLOSED** (`GetIssue`) — a merged-but-issue-still-OPEN state is a phantom
   success and fails closed naming the open issue.
3. When `state != MERGED`, classify and **name the precise blocker**, reusing
   the deterministic runner's reason vocabulary so telemetry buckets are
   identical across both paths:
   - `mergeable == CONFLICTING` → `not-mergeable: CONFLICTING`
   - `mergeStateStatus ∈ {DIRTY, BEHIND, BLOCKED, UNSTABLE}` →
     `dirty-merge-state: <status>`
   - `reviewStatus ∈ {REVIEW_REQUIRED, CHANGES_REQUESTED}` →
     `review-not-approved: <status>`
   - otherwise → `unflipped (state=<state>)`
4. Return `(false, "<blocker>")`. The scheduler routes that reason verbatim
   into `runtime.SetStageError` **and** the `stage_error` telemetry `Metadata`,
   then returns before `pipelineSuccess = true`. The deferred failure block
   records `outcome=failed` and reverts the sub-issue board status to **Ready**
   (never Done); the linked issue is **never closed** on a non-merge. The named
   blocker is the explicit handoff that sibling **#4073** consumes to surface
   the epic-level stall alert.

**Inconclusive-on-transient-error policy.** A `GetPR`/`GetIssue` error
(eventual consistency, transient API failure) must **not** flap the pipeline
into a hard failure — it is treated as inconclusive (log + trust the upstream
MERGED signal), matching the empty-URL / nil-client tolerance. Only an
_observed_ non-MERGED PR or an _observed_ still-OPEN linked issue fails closed.
`MergeStateStatus` is fetched on the PR (added to the GraphQL selection in
#4070) precisely so this classifier can distinguish a clean-but-unflipped PR
from one blocked by a dirty/behind/blocked merge state.

### `HeadlessOrchestrator.verifyPostMergeState` (TS / IPC path)

`verifyPostMergeState` runs after either path on the VSCode-IPC route. It is a
thin shell over the Go `PrMergeGate` and shares the same MERGED semantics as
`verifyPRMerged`. Its contract:

1. Read `pr-{N}.json` for the PR number. Skip silently if absent.
2. Poll `gh pr view <N> --json state,statusCheckRollup` up to
   `EC_MAX_POLLS` × `EC_POLL_INTERVAL_MS` until `state == MERGED`.
3. Return an `Error` if the gate fails (state != MERGED at terminal attempt).
4. Log a warning when the merged PR has failing CI checks (the merge already
   landed; failing here would just leave the pipeline in a broken state).

The PR-#3260 inline merge fallback was removed in PR #3264 because the
deterministic path now performs the merge before this gate runs. This
function reverts to a pure post-state assertion.

## Post-merge state reconciliation (#3979 / #3980 / #3981)

Closing the issue is not the end of post-merge state. GitHub's native
auto-close keyword does not move the project board, does not walk the epic ↔
sub-issue tree, and only fires for the exact issues a PR enumerates. Three
production gaps followed from that:

- **#3981** — a just-closed issue lingers on the board as "In progress" / "Ready"
  because nothing syncs its Status to Done.
- **#3980** — an epic stays OPEN after its last sub-issue closes when the subs
  closed in separate, non-epic PRs (the cascade never fires).
- **#3979** — an epic-umbrella PR closes the epic but leaves its sub-issues OPEN,
  and the autonomous picker re-spawns them into conflicting PRs.

`hooks.EvaluatePostMerge` (the `nightgauge hook post-merge` deterministic
path and the in-process scheduler share it) now reconciles all three after the
issue closes:

1. **Sync to Done** — the merged issue's own board Status is set to Done
   (`BoardSyncer`, when a project is configured).
2. **Close orphaned subs** — if the merged issue is itself an epic, its open
   sub-issues are closed via `EpicService.CloseOrphanSubs`, **guarded by
   `stateReason`**: only an epic closed as `COMPLETED` has its subs closed; an
   epic closed as `NOT_PLANNED` (cancelled) leaves its subs untouched.
3. **Auto-close parent epic** — unchanged (`AutoCloseSingle`), now one step of
   the same fan-out.

The hot path self-heals the common case at merge time. The board-wide backstop
for anything the hooks miss (e.g. a sub closed entirely outside the pipeline) is:

```bash
nightgauge project reconcile --project <N>
```

`EpicService.ReconcileBoard` sweeps every board item and applies the same three
rules idempotently — safe to run repeatedly and on a schedule. The author-side
prevention for #3979 lives in the pr-create skill: an epic-umbrella PR must
enumerate `Closes #sub` for every shipped sub, not just `Closes #epic`.

## Rebase-before-merge / wave merge-train (#4071)

Within a single wave, multiple sub-issue PRs can be ready at once. They are
merged **one at a time per repo**: the scheduler acquires
`Scheduler.getMergeLock(item.Repo)` (a per-repo mutex) for the whole pr-merge
stage scope, so PR A merges before PR B's pr-merge stage even begins. After A
lands, B's branch is `BEHIND` (or `DIRTY` if they touched the same lines).

`Decide()` is the safety floor here: it punts on **any** non-`CLEAN`
mergeStateStatus, so a `BEHIND`/`DIRTY` sibling is never merged blindly — the
gate emits a `KindNoOp` with `dirty-merge-state: BEHIND` / `DIRTY` evidence
instead. That punt is what makes the merge-train safe; it is also what triggers
recovery.

The recovery half closes the loop so the stale sibling is not abandoned. The
`branch-out-of-date` recovery action (see
[AUTO_TRIAGE.md](AUTO_TRIAGE.md#branch-out-of-date--wave-merge-train-4071))
rebases B onto the just-merged `main`, **waits for CI to re-pass on the rebased
head**, then re-runs this same deterministic runner. Because the runner routes
through `Decide()`, B is only merged once it is genuinely `CLEAN` with green
checks — recovery is claimed only on `PathMerged`. Net effect: both same-wave
PRs land (the second rebased automatically) and **no PR is ever merged while
`BEHIND` its base**. When the rebase hits a real conflict, the action captures
the conflict context **before** `git rebase --abort` and defers to the
`conflict-recovery-loop`, which re-dispatches feature-dev on the **same
(preserved) branch** to resolve it (#4072) — the rebase path never resolves
conflicts itself, and the branch is never discarded for a fresh-branch restart.

## Decisions (ADRs)

The full ADR set lives in
`.nightgauge/knowledge/features/3264-deterministic-first-pr-merge-stage/decisions.md`.
Highlights:

- **ADR-001** Deterministic path is the default; LLM is the punt fallback.
  Cost per pr-merge run drops from $5–$25 to ~$0 for the clean-PR majority.
- **ADR-002** `execution_path` is per-stage, not per-run. Forward compatible
  for future stages (pr-create has been suggested in epic #3261).
- **ADR-003** The decision rule is a pure function over `PRViewSnapshot`,
  separately testable from the `gh` shell-out.
- **ADR-004** Rate-limit errors punt, not retry _inside the deterministic
  runner_. Mirrors today's TS behavior and avoids quota burn. (Amended by
  ADR-006: the scheduler no longer cascades a rate-limit punt into the LLM
  path.)
- **ADR-005** Dashboard panel scope is data-only in this PR. The chart
  follows once data accrues.
- **ADR-006** (Issue #3976) A deterministic rate-limit punt does **not** fall
  through to the LLM path. The scheduler fails the stage with a
  `github-quota-low` marker so it routes to the environmental-recovery path
  (#3896): global quota cooldown until reset, board→Ready, no lifetime-cap
  penalty, zero LLM tokens. Closes the residual cascade where the skill would
  re-shell `gh` into the same exhausted bucket. Applies to pr-create too.

## Migration notes

- Behavior is unchanged for already-merged or skill-only paths (the LLM path
  receives the same input it always did).
- For clean-PR runs that previously cost $5–$25, the same merge now lands in
  ~1 s with zero tokens.
- The PR-#3260 inline merge fallback in `HeadlessOrchestrator.ts` is removed.
  Any code that depended on that behavior should rely on the Go-side runner
  instead.
- `V2StageDetail.execution_path` is additive — older daily JSONLs parse
  unchanged; readers see absence as "unknown" rather than a value.
