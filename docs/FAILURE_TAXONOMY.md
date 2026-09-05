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

| Pattern                      | Example                                                                                                                                                                                                                                                                       |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `schema validation`          | Context file fails Zod schema parse                                                                                                                                                                                                                                           |
| `pre-condition failed`       | Stage pre-condition guard triggered                                                                                                                                                                                                                                           |
| `context file`               | Context handoff file missing or unreadable                                                                                                                                                                                                                                    |
| `enoent`                     | File or directory not found (Node.js I/O)                                                                                                                                                                                                                                     |
| `eacces`                     | Permission denied on file access                                                                                                                                                                                                                                              |
| `eperm`                      | Operation not permitted                                                                                                                                                                                                                                                       |
| `invalid json`               | JSON parse error on context file                                                                                                                                                                                                                                              |
| `extension lifecycle`        | VSCode extension activation/deactivation error                                                                                                                                                                                                                                |
| `failed to read`             | Generic read failure on required file                                                                                                                                                                                                                                         |
| `cannot read`                | Property access on undefined/null file data                                                                                                                                                                                                                                   |
| `pipeline state`             | Pipeline state file corrupt or missing                                                                                                                                                                                                                                        |
| `[cost-cap-exceeded]`        | Stage killed by `pipeline.stage_cost_caps` (Issue #3002)                                                                                                                                                                                                                      |
| `cost cap exceeded`          | Stage killed by `pipeline.stage_cost_caps` (Issue #3002)                                                                                                                                                                                                                      |
| `AC_ALREADY_SATISFIED`       | Deterministic AC reconciliation pre-flight gate found work already merged (Issue #3003). Pipeline correctly short-circuited; not an organic failure.                                                                                                                          |
| `[baseline-ci-deferred]`     | Baseline-CI dependency gate deferred dispatch — `main`'s recent runs of a referenced workflow are failing (Issue #3004). Pipeline correctly held; not a failure. Does not resume on its own — an operator runs `nightgauge baseline-gate promote` once the baseline is green. |
| `baseline ci deferred`       | Same gate, free-text variant.                                                                                                                                                                                                                                                 |
| `baseline-ci red`            | Same gate, summary line variant emitted by the deferral comment + outcome record.                                                                                                                                                                                             |
| `[blocked-dependency]`       | Native-blockedBy dependency gate deferred issue pickup — the issue has an OPEN `blockedBy` dependency (blocker's PR not merged, Issue #231). A controlled hold, not a failure. Auto-requeued by `deps-gate promote` (and the autonomous cascade) when the blockers close.     |
| `blocked by open dependency` | Same gate, summary line variant emitted by the deferral comment + outcome record.                                                                                                                                                                                             |

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

The classifier lives in the SDK analysis layer so every reliability consumer
uses the same fallback when a history record has no explicit category:

```
packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts
```

### Data Flow

```
Go runtime writes the authoritative history record
     ↓ optional failure_category on the failed stage
VSCode history reader
     ↓
PostPipelineAnalyzer.adaptRecords()
     ↓ maps the persisted category when present
analyzeReliability()
     ↓ uses failure_category or the SDK classifier fallback
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

| Kind                             | Meaning                                                                                                                                                                                                                                                                                                             |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `stall_kill`                     | Subagent exceeded `stall_kill_multiplier × stall_thresholds` and was forcibly killed                                                                                                                                                                                                                                |
| `budget_exceeded`                | Pipeline-level or per-stage **token** budget ceiling tripped (with grace buffer applied)                                                                                                                                                                                                                            |
| `validation_error`               | Context schema validation failed terminally (e.g., missing output context for the stage)                                                                                                                                                                                                                            |
| `subagent_crash`                 | Subagent process exited non-zero with no recovery path (model escalation exhausted)                                                                                                                                                                                                                                 |
| `orchestrator_crash`             | Orchestrator process died mid-stage; record synthesized on next startup from a stale `current-run.json` sidecar                                                                                                                                                                                                     |
| `network_unavailable`            | Extended GitHub connectivity loss aborted the run (Issue #3296) — environmental                                                                                                                                                                                                                                     |
| `stream_idle_timeout`            | Anthropic API closed a streaming response mid-flight (Issue #3398) — environmental                                                                                                                                                                                                                                  |
| `rate_limit_quota_exhausted`     | Idle stall fired while the rate-limit bucket was drained (Issue #3386) — environmental                                                                                                                                                                                                                              |
| `worktree_uncommitted`           | Failure **recovered**: uncommitted work was auto-committed before cleanup (Issue #3542)                                                                                                                                                                                                                             |
| `budget_ceiling_hit`             | The USD pipeline budget ceiling killed a running stage (Issue #3542) — real spend, not a defect                                                                                                                                                                                                                     |
| `github_quota_low`               | GitHub API rate-limit bucket below headroom at the pipeline-start preflight (Issue #3896) — environmental                                                                                                                                                                                                           |
| `api_connection_lost`            | Anthropic API transport drop mid-stage (socket close / DNS blip; Issue #4002) — environmental                                                                                                                                                                                                                       |
| `github_network_outage`          | api.github.com unreachable at the pipeline-start preflight (Issue #4002) — environmental                                                                                                                                                                                                                            |
| `model_unavailable`              | API rejected the selected model: not on plan / unknown ID / model usage cap (Issue #42) — triggers tier fallback                                                                                                                                                                                                    |
| `premature_turn_end`             | Stage exited 0 but produced no state change — the agent ended its turn on a promise (Issue #74)                                                                                                                                                                                                                     |
| `git_transport_auth_failed`      | A git or forge transport refused the credentials the machine offered — go-git's `invalid auth method` against an SSH remote, `Permission denied (publickey)`, `could not read Username` with prompts disabled, the forge API's `Bad credentials` (Issue #878) — environmental; no rerun or stronger model clears it |
| `dev_produced_no_changes`        | feature-dev's gate found the stage workspace empty despite a truthful dev context — work landed where the pipeline never reads (Issue #202)                                                                                                                                                                         |
| `adapter_auth_failed`            | Pipeline-start adapter auth gate refused to launch: probe timed out after retry, or the adapter CLI is logged out (Issue #312) — retryable infra                                                                                                                                                                    |
| `no_changes_produced`            | pr-create's deterministic fallback confirmed zero commits ahead of base — genuinely nothing to open a PR for (Issue #317) — planning/scope                                                                                                                                                                          |
| `not_pipeline_actionable`        | A stage declared the issue's deliverable is not producible by any pipeline lap — counsel sign-off, an operator-only credential, a human decision (Issue #1241) — not a failure and not a deferral                                                                                                                   |
| `validation_failed`              | feature-validate honestly failed its quality gates (`validation_status="failed"`) — organic implementation failure (Issue #326)                                                                                                                                                                                     |
| `branch_forked`                  | The run's branch diverged from its remote; every push is rejected non-fast-forward (Issue #163) — unrecoverable by retry, needs human action                                                                                                                                                                        |
| `abandoned_commit`               | A stage upstream of pr-create was killed/crashed after committing valid, unmerged work (clean tree, ahead of base) — the `abandoned-commit-recoverable` action matched but could neither self-heal nor set up a resume (Issue #191)                                                                                 |
| `commit_orphaned`                | A killed stage's commit landed on the wrong branch (a stray `temp-pre-push-<n>` left by a SIGKILL bypassing pre_push.go's restore-defer) and feature-validate's branch-identity self-heal could not check out the expected feature branch to recover it (Issue #266) — unrecoverable by retry, needs human action   |
| `permission_denied`              | Harness denied a tool call outright — most commonly a stage's foreground `sleep` wait loop (Issue #289) — harness fault, retryable                                                                                                                                                                                  |
| `stage_context_unreadable`       | A post-condition gate could not read a file the stage's contract says it wrote — its context file, planning's `plan_file`, or `gate-metrics.jsonl` — for a reason other than absence (EISDIR / ENOTDIR / EACCES; Issue #1237) — filesystem fault, not the work                                                      |
| `dev_build_verification_missing` | feature-dev's context carries no `build_verification` object: the skill skipped the verification step the completion contract requires (Issue #1237, contract from #55) — agent behaviour                                                                                                                           |
| `dev_build_verification_failed`  | feature-dev ran its build and recorded `build_verification.status="failed"` (Issue #1237) — organic implementation failure                                                                                                                                                                                          |
| `dev_tests_failed`               | feature-dev's own test run recorded `tests_status.failed > 0` (Issue #1237) — organic implementation failure                                                                                                                                                                                                        |
| `pr_merge_lookup_failed`         | pr-merge's gate could not establish the PR's state: `gh pr view` failed or was rate-limited on every attempt and the local-git fallback found no merge commit (Issue #1237) — infrastructure; the merge may have landed unseen                                                                                      |

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

`not_pipeline_actionable` (Issue #1241) is the third member of that family, and
the one that says the quiet part: **the issue was never pipeline work.** It is
raised by a STAGE'S OWN DECLARATION — a `NOT_PIPELINE_ACTIONABLE` feedback
signal in `planning-{N}.json` or `dev-{N}.json` — not by an inference about an
empty tree, and it is neither a failure nor a deferral:

|                | `blocked_dependency` (#305) | `no_changes_produced` (#317) | `not_pipeline_actionable` (#1241) |
| -------------- | --------------------------- | ---------------------------- | --------------------------------- |
| What is true   | Not ready **yet**           | Human-only, discovered late  | Human-only, **declared**          |
| What unblocks  | The blocker closing         | (n/a — it already failed)    | A person doing the thing          |
| Re-dispatched? | Yes, on blocker close       | Yes — hence the repeat spend | **No.** Labelled and parked       |
| Raised by      | The pickup dependency check | pr-create's create fallback  | The stage that read the issue     |

The routing follows from "nothing here is defective": no
`LifetimeIssueFailures` increment, no cascade-breaker feed, no queue pause, and
— unlike every transient kind — **no retry and no board revert to Ready**. A
transient failure is re-dispatched because the next attempt can differ; this one
cannot, so re-queuing buys the identical verdict at the identical price forever.
The run instead applies the `owner-action` label (the sole default entry of
`autonomous.exclude_labels`, excluded by the candidate filter since #317), parks
the row in Backlog, writes the durable blocked finding, comments on the issue
and raises the Action Center card. The way back in is a human removing the
label.

**Ordered ahead of `dev_produced_no_changes` and `premature_turn_end` in both
classifiers**, because it arrives through them. A stage that correctly refuses
an unimplementable issue leaves an empty workspace, the gate fires its `KindNoOp`
verdict, and the scheduler wraps that in the `premature turn end:` envelope — so
all three rules match the same text, and the two later ones name an AGENT
failure. That is precisely what happened on the specimen run: the specimen run — a privacy-policy legal review awaiting counsel, was dispatched as
ordinary `type:docs` work; feature-dev refused it correctly and at length; the
run was booked `dev_produced_no_changes`, and autonomous dispatch halted for the
whole repository. The stage's own structured declaration outranks the gate's
inference from an empty tree.

A contributing defect is worth recording separately, because it is what made the
refusal look like a lie: with no handoff written, the extension's deterministic
fallback (`generateDeterministicDevContext`) synthesised one by diffing against
the bare LOCAL `main`, which lagged `origin/main` by five squash-merges — so it
stamped a handoff claiming six modified files belonging to other people's merged
work. The Go gate's ground truth resolves `origin/main` first
(`internal/ci.DefaultDiffBases`), found the tree empty, read the fabricated
claim, and convicted the stage. Two base-ref resolvers disagreeing is the
**dual-path drift** class below; here the disagreement was the difference
between a gate passing and a repository halting. Both now walk the same ladder,
and an unresolvable base records an EMPTY file list rather than inventing one
from `HEAD~1`.

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
share the terminal kind.

The pipeline-start probe is not the only producer (Issue #591). A CLI adapter
can also fail its own login check mid-dispatch — too fast for the preflight
probe to have run at all — and print its own vendor auth message straight to
stderr, e.g. grok Build's `Error: Not signed in. To authenticate without a
browser, run: grok login --device-code...`. That text carries none of the
`[adapter-auth-failed]` / `adapter-auth-failed` / `adapter_auth_failed`
markers the gate stamps, so it reaches the classifier only as ordinary stderr
(the #533 `cliFailureText` carry) and, pre-#591, fell through every rule to
`subagent_crash`. The `not signed in` clause on the same rule closes that gap:
both producers share one kind, one recovery routing, and — critically — **an
auth failure is excluded from model escalation** (`internal/orchestrator/
scheduler.go`'s escalation gate): a stronger model cannot authenticate a CLI
whose credentials are absent, so escalating on this kind is pure wasted spend,
not a recovery attempt.

`worktree_uncommitted` and `budget_ceiling_hit` are **recoverable** kinds: the
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

| Terminal Kind                    | Typical `failure_category` (heuristic)                                                      |
| -------------------------------- | ------------------------------------------------------------------------------------------- |
| `stall_kill`                     | `agent` — transient runtime issue                                                           |
| `budget_exceeded`                | `agent` — call-pattern under operator control                                               |
| `validation_error`               | `infrastructure` — pipeline contract failed                                                 |
| `subagent_crash`                 | `organic` — implementation failure, full weight                                             |
| `orchestrator_crash`             | `infrastructure` — ours, not the model's                                                    |
| `network_unavailable`            | `infrastructure` — environmental, excluded from calibration                                 |
| `stream_idle_timeout`            | `infrastructure` — upstream API, not the issue                                              |
| `rate_limit_quota_exhausted`     | `infrastructure` — upstream API quota, not the issue                                        |
| `github_quota_low`               | `infrastructure` — GitHub API quota, not the issue                                          |
| `api_connection_lost`            | `infrastructure` — local network/transport, not the issue                                   |
| `github_network_outage`          | `infrastructure` — local network/transport, not the issue                                   |
| `model_unavailable`              | `infrastructure` — plan/limit environment, not the issue                                    |
| `premature_turn_end`             | `agent` — the agent's turn-ending behavior, not the issue                                   |
| `dev_produced_no_changes`        | `agent` — the stage's delegation/turn-ending behavior, not the issue                        |
| `adapter_auth_failed`            | `infrastructure` — probe starvation / credential state, not the issue                       |
| `no_changes_produced`            | `agent` — planning/scope failure (dispatch-eligibility gap), not the model's implementation |
| `not_pipeline_actionable`        | non-failure — the issue is misfiled, not defective; no lifetime-cap increment, no cascade   |
| `validation_failed`              | `organic` — true implementation failure caught by feature-validate's own quality gate       |
| `branch_forked`                  | `infrastructure` — the pipeline's own orphaned push (or an operator's), not the code        |
| `worktree_uncommitted`           | recoverable — work preserved, not counted as a failure                                      |
| `budget_ceiling_hit`             | recoverable — real spend, not a code defect                                                 |
| `stage_context_unreadable`       | `infrastructure` — the gate's own filesystem read failed, not the issue                     |
| `dev_build_verification_missing` | `agent` — the skill skipped a contract step, not the issue                                  |
| `dev_build_verification_failed`  | `organic` — the stage's own build broke                                                     |
| `dev_tests_failed`               | `organic` — the stage's own tests failed                                                    |
| `pr_merge_lookup_failed`         | `infrastructure` — gh / local git could not answer, not the issue                           |

**The sweep (#1237).** #9 built the mechanism but left eleven `KindFail`
sites emitting an empty `TerminalKind` — four in `feature_dev_gate.go`, two
each in `feature_planning_gate.go` and `pr_merge_gate.go`, one each in
`issue_pickup_gate.go`, `feature_validate_gate.go` and `pr_create_gate.go`.
Empty is not "no opinion": `ResolveTerminalKind` falls back to the prose
ladder, which had no clause for the scheduler's `stage gate failed: <reason>`
wrapper, so the generic `exit ` clause of the `subagent-crash` rule decided and
every one of those honest gate failures was booked as an infrastructure crash —
corrupting failure weighting and sending auto-triage down a crash-recovery path
for a fault that never happened. Every `KindFail` site now names its failure at
the point it detects it, with five kinds added for the shapes no existing
constant described: `stage_context_unreadable`, `dev_build_verification_missing`,
`dev_build_verification_failed`, `dev_tests_failed`, `pr_merge_lookup_failed`
(meanings in the Values table above). The rule table carries a matching rule
per kind, gated on the wrapper text, for text-classified paths and records
written before the sweep. Two guards keep it closed:
`TestKindFail_AlwaysCarriesTerminalKind` in
`internal/orchestrator/gates/gate_test.go` drives every gate into each of its
`KindFail` branches and asserts a non-empty, non-`subagent_crash` kind, and
`TestGateTerminalKindConstantsMirrorOrchestrator` pins the gates-package
mirror constants to the orchestrator originals.

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
  "not_pipeline_actionable", // Issue #1241
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
  TypeScript twin derive ~1,450 inputs _from the table_ (every clause, every
  term, **both edges** of every `~` term, every ordered rule pair, and every
  `signal: true` rule composed with every extension clause in both orders) and
  all three suites must reproduce `testdata/stress-golden.json`. Deleting a
  clause, widening a literal, dropping one half of the word boundary, swapping
  two rules or swapping the two stages of the signal projection changes a
  committed answer, so it lands in review as an explicit before/after of the
  inputs whose routing changed. What the set does **not** derive, it cannot see:
  before it composed rules with extensions, reversing the two statements of
  `SignalKind` moved nothing at all.
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

The **ordering** that produces the bound is pinned too, and separately, because
it is a property of the interpreter rather than of the table: the derived set
composes every `signal: true` rule with every extension clause, the
`order-signal-rule-beats-extension-*` corpus rows do the same on real wording,
and `TestSignalNeverContradictsTheRecord` fails outright — on the shipped
projection, not on a regenerable artifact — if an extension ever answers for text
a signal rule already claims. Without those inputs the sentence above was true
only by inspection: swapping the two blocks of `SignalKind` left every suite
green.

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

## Attribution: the first cause, not the last symptom (#875, #878)

A run's `terminal_failure_kind` is not a label — it is what
[`OUTCOME_RECORDING.md`](OUTCOME_RECORDING.md) and the retro path consume. A kind
that names a downstream symptom is corpus poisoning, and two observed runs did
exactly that:

| What actually stopped the run      | What was recorded      | Where the symptom came from                           |
| ---------------------------------- | ---------------------- | ----------------------------------------------------- |
| stage SKILL.md would not render    | `worktree_uncommitted` | the pre-dispatch rescue found bookkeeping files dirty |
| `git push` — `invalid auth method` | `premature_turn_end`   | the post-condition check found no output context      |

Both symptoms are real observations. Neither is why the run could not proceed,
and in both the FIRST cause was already in the log.

Neither is recorded that way now. A credential-less push books
`git_transport_auth_failed` (#878). A render refusal books its own refusal's
kind rather than the rescue's (#875) — `validation_error`, which is generic but
is at least the failure the pipeline actually hit, not a hygiene condition
someone else found on the way past.

**The rule: a later refusal is context, never a replacement.**

- `refusePreDispatch` keeps the refusal's own kind. The #3542 uncommitted-work
  rescue no longer renames the failure after itself; it records that the work
  survived and leaves the diagnosis alone. The recovery marker stays in the
  stage error prose, because that is what the autonomous path re-derives "did
  the work survive" from — a separate question from "why did this fail", now
  answered by a separate mechanism.
- `skipBoardRevert` reads the run's `workRecovered` flag instead of inferring
  recovery from the kind. Reverting the board is harmful whenever a recovery
  commit exists — the re-dispatch regenerates the work in a fresh worktree while
  the preserved commit sits on a branch nobody re-runs — and that is true
  whatever name the failure ended up with.
- The post-stage output check (#2870) composes its reason as
  `<first cause> — then <post-condition symptom>` when the stage's captured
  output tail names a permission-class cause. The symptom is retained: it is
  still the fastest way to see where in the stage the run died.
- That site's terminal **kind** is derived from the same first cause, not
  hardcoded (#878). It used to book `validation_error` for every missing output,
  so a credential-less push was recorded as a stage that wrote a malformed
  context. It now classifies the first-cause line through the rule table and
  falls back to `validation_error` only when the table does not recognise it.
  The composed string is deliberately **not** what gets classified: it retains
  the symptom phrase, which is itself a `premature-turn-end` clause, so
  classifying the whole reason would make the answer depend on rule order rather
  than on the evidence.

The kind that closes the credential half is `git_transport_auth_failed`, a rule
placed **above** `premature-turn-end` in `internal/terminalkind/table.json`. Its
terms are the transport's own wording — `invalid auth method`,
`permission denied (publickey`, `could not read Username` / `Password`,
`invalid username or password`, `authentication failed for`,
`authentication required`, `ssh: unable to authenticate`, `bad credentials`,
`http 401`, `401 unauthorized`.

Those terms are a **deliberate subset** of `orchestrator.permissionPhrases`, not
a copy of it, because the two lists answer different questions at different
prices. The gate asks _could a stronger model possibly fix this?_ and pays one
skipped retry for a false yes, matching per line against a curated classifier.
The rule table asks _what is this failure called?_, matches the whole joined
string, is first-match-wins, and sits above sixteen other rules — so a term here
that appears in someone else's failure prose silently retitles their failure.
Four spellings are therefore excluded, each pinned by a corpus row that would go
red:

| Excluded                        | Pinned by                                                | Why                                                                                                 |
| ------------------------------- | -------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| bare `permission denied`        | `permission-denied-negative-eacces`                      | A filesystem EACCES is not a transport refusal; only the `(publickey` form is taken.                |
| bare `authentication failed`    | `git-transport-auth-negative-bare-authentication-failed` | A model adapter's own logged-out text, a different remedy; narrowed to `authentication failed for`. |
| bare `unauthorized`/`forbidden` | `boundary-negative-left-edge-session-limit`              | Both words appear in unrelated refusal prose.                                                       |
| the 403 forms                   | `boundary-negative-left-edge-session-limit`              | 403 is GitHub's **throttle** code as much as its refusal code.                                      |

A run whose only evidence is a bare 403 therefore still books `validation_error`
while the gate still declines to escalate it. That divergence is the
conservative answer on both sides, not a gap: the gate's wrong answer costs one
retry, this rule's wrong answer mislabels someone else's failure. The 401 forms
have no such ambiguity and are in.

### The cause the daemon logs has to reach the record it books (#878)

Naming the kind is worthless if the evidence never arrives. In the observed run
the `invalid auth method` line was **not** the subagent's: it came from
`Scheduler.ensureEpicBranchForItem`, which pushes the epic base branch on the
stage's behalf, is deliberately non-blocking, and did nothing with the failure
but `log.Printf` it. Every mechanism above reads `runtime.StageOutputTail(stage)`
— the SUBAGENT's captured output — so the first-cause scan found a clean tail,
the escalation gate saw no permission evidence and escalated `haiku → sonnet`,
and the record booked the post-condition symptom. The fix was in place and could
not fire.

`ensureEpicBranchForItem` now returns its failure text instead of only logging
it, and `runPipeline` appends that text to the stage's evidence through
`RuntimeState.AppendStageOutputTail` (which appends rather than replaces, so the
subagent's own tail is kept). **Non-blocking has never meant invisible**: a
failure the orchestrator performs on a stage's behalf belongs in that stage's
evidence, or every consumer built on that evidence is structurally blind to it.

---

## Escalation is for capability shortfalls only (#878)

Model escalation answers one question: _would a stronger model have got this
right?_ A permission failure is not a capability shortfall. The observed run
failed on a credential-less `git push`, escalated `haiku → sonnet`, re-dispatched
an identical 67,610-character prompt, and failed at the same line 44 seconds
later.

`orchestrator.EscalationBlockedByCategory` gates every escalation site — the
scheduler's stage-failure and missing-output paths and `IpcStageRunner`, so the
gate does not depend on which execution path the operator is running. It blocks
on `failure.CatPermission` and nothing else:

- **`CatInfra` is deliberately NOT blocked.** On the scheduler's paths the
  escalation branch is also the retry branch, so blocking it there would
  silently remove a retry from genuinely transient network failures.
- **Matching is per line**, because the classifier ladder is first-match-wins
  over the whole string: one line mentioning `timeout` in a 200-line tail would
  otherwise claim the verdict and hide the auth failure underneath it.
- **The bare HTTP codes `401`/`403` do not trigger the gate.** They are correct
  clauses for the curated stderr `failure.Classifier` was written for, and
  catastrophic against a raw output tail full of issue numbers and temp paths —
  the first version of this gate refused to escalate two of three issues in a
  wave test purely because they were numbered #401 and #403. The codes are
  honored in their written forms (`http 403`, `403 forbidden`) instead.
- **A filesystem EACCES does not trigger the gate (#1447), but the bare
  phrase `permission denied` still does.** Go's standard library reports an
  ordinary filesystem permission fault as
  `open /some/path: permission denied`, which is capability-fixable and has
  nothing to do with forge/git-auth — but was matching the same bare clause
  the gate uses for a genuine credential denial, and blocked escalation on it.
  The fix is a **negative guard**, not a narrower needle list: the gate still
  matches the bare phrase (so it keeps catching every real denial spelling —
  multi-auth-method OpenSSH lines like
  `Permission denied (publickey,password).`, this repo's own
  `forge: permission denied` sentinel, `gh`/GraphQL denials, and provider
  denials such as `Permission denied to access model`), then excludes only
  lines shaped like `os.PathError.Error()` — a syscall verb (`open`, `read`,
  `write`, `mkdir`, `stat`, …) followed by a colon-free path and
  `: permission denied`, or any line containing `EACCES`. An earlier version
  of this fix instead enumerated specific transport spellings
  (`permission denied (publickey)`, `remote: permission denied`); that
  narrowed correctly for the one GitHub example in the issue's AC but
  silently stopped blocking every multi-method SSH line, this repo's own
  forge sentinel, and the documented Vertex/model-provider denials below —
  an allowlist of denial spellings is provably incomplete and fails open
  (escalation proceeds) on whatever spelling was not anticipated, where the
  negative guard fails closed (still blocks) on anything it does not
  recognize as filesystem-shaped. The shared `failure.Classifier`'s own bare
  clause is left as-is, since it stays broad on purpose for its own
  curated-stderr callers.

The git-transport spellings (`invalid auth method`, `authentication required`,
`could not read Username`, `Bad credentials`, …) were added to
`failure.Classifier` for the same issue: before that, the observed push failure
matched nothing and classified `CatUnknown`.

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

### Model Refusal Fallback (Issue #91)

A safety refusal from the served model is not a pipeline failure: the claude
CLI itself absorbs it. When the model refuses mid-turn — `api_refusal_category`
values observed in the wild include `reasoning_extraction`, but the field is
not restricted to that value; the Go tracker copies whatever category the CLI
sends — the CLI emits a `system` event with `subtype: "model_refusal_fallback"`
carrying `original_model`, `fallback_model`, and `api_refusal_category`, silently
retries the turn on the fallback model, and the session still **exits 0**. Every
assistant message after the event reports the fallback model; the session's own
`init` event still names the originally-requested one. Nightgauge's retry ladder
(`exitCode != 0` plus literal model-rejection wording) cannot see this — the swap
happens one layer below our code, inside the CLI, before any result reaches us.

**Captured event.** Verbatim shape from a live claude CLI 2.1.186 capture
(`internal/execution/stream_test.go`, `TestServedModelTrackerRefusalFallback`):

```json
{
  "type": "system",
  "subtype": "model_refusal_fallback",
  "trigger": "refusal",
  "original_model": "claude-fable-5",
  "fallback_model": "claude-opus-4-8",
  "api_refusal_category": "reasoning_extraction",
  "content": "…"
}
```

**Fable 5.1 specifics.** The permitted server-side fallback targets for Fable 5.1
are `claude-opus-4-8` and `claude-opus-5`. A refusal raised before any output
token is produced is not billed at all. Where a mid-response refusal does trigger
a fallback, the fallback credit refunds the cache cost of the model switch itself
(the underlying turn's already-produced tokens are billed normally). The fallback
model has no access to Fable 5.1's thinking blocks from before the swap — they
are dropped, not replayed, and not billed.

**Two distinct fallback mechanisms — do not conflate.** Everything above is the
CLI's own **same-turn** substitution: one turn, one event, attributed via the
`ServedModelTracker`/`ModelSelection.source` path described next. A second,
unrelated mechanism lives in the extension orchestrator:
`HeadlessOrchestrator.shouldFallbackFableToOpus()` retries an entire **failed
stage** on Opus when that stage's effective model was Fable and the failure was
a usage/quota-limit error — Fable has its own Max-plan usage bucket, separate
from Opus/Sonnet, so a Fable-only exhaustion retries on Opus rather than
pausing the whole pipeline for the global cooldown. This has nothing to do with
`model_refusal_fallback` events: it fires on stage failure, not mid-turn, and
it is a fresh stage retry with a new context, not a same-turn substitution. Each
such retry is appended to `fableFallbacks` as `{stage, from: "fable", to:
"opus"}` and surfaced via `quota_fallbacks` state meta (#26) — a record shape
that looks superficially like the CLI fallback but is produced by, and only by,
this orchestrator-level retry decision.

**What we do about it: attribution only, never suppression or retry.** Both
stream parsers track the served model as the LAST model observed in the stream
(seeded by `system/init`, overridden by each message's `model` field and by a
refusal-fallback event):

- Go: `ServedModelTracker` (`internal/execution/stream.go`) surfaces a
  `ModelRefusalFallback{OriginalModel, FallbackModel, RefusalCategory}` value,
  threaded through `RunResult` → `StageRunResult` into the scheduler's
  cost/exit-record/telemetry/history sinks. The served stage's
  `ModelSelection.source` is recorded as `"cli-refusal-fallback"`, and the
  learning outcome's `ActualModel` becomes the served model — never the
  originally-requested one.
- TypeScript: `parseStreamJsonLine` (`packages/nightgauge-vscode/src/utils/tokenParser.ts`)
  extracts the `system` subtype and `message.model`; `SkillRunner`
  (`packages/nightgauge-vscode/src/utils/skillRunner.ts`) tracks the last-served
  model and forwards `servedModel` plus the `modelRefusalFallback` fields over
  `pipeline.stageResult` to Go.

Both paths log one observable line the moment the swap fires (`[model-refusal-fallback]`
in the Go scheduler, `[skillRunner] claude CLI model_refusal_fallback:` on the
IPC path). Without this, a "frontier" run silently downgrades to a cheaper served
model with no record of it: cost attribution, per-model telemetry, and any future
model-eval sampling would be poisoned by a served model that does not match the
one recorded. The fallback itself is never suppressed, retried, or fed back into
routing, escalation, or sticky-downgrade decisions — it is CLI safety behavior,
and Nightgauge's job here is faithful attribution of what actually served the
stage, nothing more.

---

## Retro Failure Categories (`AutoRetroService`)

The categories above (`infrastructure`, `agent`, `organic`) feed weighted
reliability scoring. A separate, finer-grained taxonomy is emitted by
`AutoRetroService.classifyFailure()` and written to `*_retro.json` files
under `.nightgauge/retros/`. These categories drive the retro
dashboard view, auto-issue creation, and recommendations surfaced to
operators.

| Category                  | Severity | Source                                          | Notes                                                                                                                 |
| ------------------------- | -------- | ----------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `budget-exceeded`         | high     | extension log: budget enforcer                  | Token or cost ceiling tripped before grace.                                                                           |
| `shipped-but-overbudget`  | low      | state-aware override                            | `budget-exceeded` finding with a MERGED PR — work shipped (#3108).                                                    |
| `false-negative-shipped`  | low      | state-aware override (#3275)                    | Generalizes the shipped-but-merged path: ANY pr-merge failure where `gh pr view` shows MERGED reclassifies here.      |
| `state-management`        | high     | extension log: schema/context errors            | Pipeline contract failed (missing context file, schema validation).                                                   |
| `ci-infrastructure`       | medium   | gh CLI / CI poll                                | External CI checks failed.                                                                                            |
| `model-capability`        | high     | extension parser                                | Empty/garbled model output.                                                                                           |
| `timeout`                 | medium   | free-form                                       | Configurable stage timeout (distinct from skillRunner stall-kill).                                                    |
| `validation-failure`      | high     | subagent stdout                                 | Tests/typecheck/build failed.                                                                                         |
| `stall-kill`              | medium   | skillRunner                                     | Subagent went silent past idle/hard-cap threshold.                                                                    |
| `cost-cap`                | high     | skillRunner log line OR diagnostic file (#3275) | Per-stage `pipeline.stage_cost_caps` fired. The file-existence check (`<stage>-cost-capped.log`) is deterministic.    |
| `infrastructure-outage`   | low      | OfflineManager / DNS                            | Network outage during the run.                                                                                        |
| `stop-hook-error`         | medium   | Claude CLI notification (time-gated #3275)      | Pre-result `stop-hook-error` notification — the genuine #3204 silent-hang signature. Post-result emissions are noise. |
| `skill-no-op`             | high     | pr-merge context (#3275)                        | pr-merge LLM path reported success but post-merge verification found the PR is not actually merged.                   |
| `adapter-unavailable`     | high     | dispatcher envelope                             | Primary adapter prereq failed; no fallback walked (#3223).                                                            |
| `no-adapter-available`    | high     | dispatcher envelope                             | Full fallback chain exhausted (#3231).                                                                                |
| `quota-exhausted`         | low      | run record kind (#1448)                         | A provider or forge quota WINDOW is closed. Reopens on a clock; no config change helps.                               |
| `model-unavailable`       | high     | run record kind (#1448)                         | The API rejected the selected model, so the stage never ran. Routing/plan fault, not model quality.                   |
| `permission-denied`       | medium   | run record kind (#1448)                         | The harness refused a tool call. Not a defect — fix the pattern the stage reached for.                                |
| `human-decision-required` | medium   | run record kind (#1448)                         | Architecture approval halt, or a stage declared the deliverable is not producible by any lap. Parked, not broken.     |
| `dependency-blocked`      | low      | run record kind (#1448)                         | Dispatched over an open `blockedBy` edge. The dispatch decision is the defect, not the stage.                         |
| `no-work-required`        | low      | run record kind (#1448)                         | Nothing to produce: the issue was already closed, or the branch holds no commits to open a PR for.                    |
| `work-stranded`           | high     | run record kind (#1448)                         | The work EXISTS where the pipeline does not look (uncommitted, stray branch, diverged remote, commit with no PR).     |
| `containment-breach`      | high     | run record kind (#1448)                         | The stage wrote into a repository it does not own (#129) while reporting success.                                     |
| `validation-inconclusive` | medium   | run record kind (#1448)                         | A validation tier ran and executed zero tests — nothing failed, so nothing was verified (#221).                       |
| `credential-failure`      | high     | run record `terminal_failure_kind`              | `git_transport_auth_failed` — a git or forge transport refused the machine's credentials (#878).                      |
| `unknown`                 | low      | fallback                                        | No structured signal or keyword match.                                                                                |

### The Record's Kind Is Decided Here, Not Guessed (Issue #1448)

`terminal_failure_kind` on the V3 run record is the authoritative field: the
kind was decided by `internal/terminalkind/table.json` and booked by Go, so by
the time `AutoRetroService` reads it the cause is already named. Its extractor
must therefore DECIDE — dropping the kind and falling through to the prose
keyword passes is how a credential fault whose kind the record carried exactly
was written up as `state-management`, with the remedy "re-run the failed stage
after verifying context" (#878).

**Authoritative does not mean first.** The extractor sits after the
`merge-blocked` and `skill-no-op` extractors and before the `stop-hook-error`
and context-decode ones, and every one of those positions is load-bearing. A
kind names _how the run ended_; an extractor that owns a case knows _whether
ending that way was correct_. The pr-merge gate proves the two are not the same
verdict: for one run against a non-mergeable PR it emits `pr_merge_unmerged` as
the kind **and** `mergeStateStatus=BLOCKED` in the reason. Ordered first, the
kind wins and a correctly declined red PR is written up as "the stage reported
success but the work never landed" — a bug report against a run that did its
job. Ordered where it is, `merge-blocked` keeps the case and the kind rides
along as a secondary finding.

The kind is read from the **threaded field**, never by regexing the joined
evidence corpus. That corpus concatenates the terminal reason, the scoped
session log, the deliverable and the history record, so a
`"terminal_failure_kind"` key belonging to some other run quoted in a log would
win on first match — the source-blindness #134 and #1144 both fixed.
`collectEvidence` fills the field from the run record for _this_ issue
(`readRecordTerminalKind`) whenever the failing gate supplied no kind, which it
does on every non-gate failure, so there is one path rather than two.

The stage-authored `feedback[]` pass runs **unconditionally**, not only when the
structured passes come up empty. "Only if pass 1 is empty" meant "never" once
the record decided all thirty-nine kinds, because every V3 failure record
carries one. A category the structured passes already found absorbs the
feedback detail, so the stage's own rationale and evidence ride on the finding
they explain instead of being dropped as a duplicate.

Until #1448 the map held five kinds against a vocabulary of thirty-nine, and
the other thirty-four took that path by default. It is now exhaustive, and
exhaustive by construction rather than by discipline:

- `TERMINAL_KIND_CATEGORY` is typed `Record<TerminalFailureKind, …>`, so a kind
  added to the vocabulary is a **compile error** until someone chooses its
  category.
- `TerminalFailureKind` is pinned to Go's `TerminalKind*` constants in both
  directions by `failureClassifier.parity.test.ts` (#229), and every
  `table.json` kind is pinned to those constants by
  `TestTable_EveryKindHasCorpusCoverage`. A new kind cannot reach `table.json`
  without arriving in the map.
- `AutoRetroService.terminalKindCoverage.test.ts` closes the loop from the
  retro end: it reads `table.json` itself and fails when a kind in it produces
  no category through the record extractor.

Categories are shared between kinds only where the **remedy** is genuinely
shared; the kind's own name always rides along on the finding's evidence line
(`<stage> ended with terminal kind <kind>: <reason>`), so grouping never costs
the operator the specific cause.

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

| Issue | Where                  | What silently did nothing                                                                                                              |
| ----- | ---------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| #149  | recovery               | the catch-path branch was dropped, so the recovery never ran                                                                           |
| #151  | capture                | shape-blind parse — the runtime shape fell through the singular-only branch                                                            |
| #154  | tokenParser            | `tool_use.id` discarded at a parse boundary, so `last_bash_exit` could never populate                                                  |
| #163  | cleanup                | `loadFeatureBranch(workspaceRoot, …)` resolved `""` on worktree-isolated runs; cleanup hit nothing                                     |
| #165  | branch cleanup         | a pathspec that matched no file, so the "is it merged?" diff was empty and read as "merged"                                            |
| #299  | reconcile / V2 history | the same `loadFeatureBranch(workspaceRoot, …)` shape: the branch-PR probe never ran, and history recorded a fabricated `feat/{N}`      |
| #302  | four guards            | id-less stages passed the forensic self-check; zero-root sweeps, nil-state retraction, and all three terminal-funnel skips were silent |
| #402  | tool-call log          | `ToolCallLog` recorded calls no `tool_result` could join and counted nothing; the Dashboard rendered them as quiet successes           |

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
  (non-terminal reconcile, V2 history) still miss worktree-isolated runs.
  **Fixed:** both resolve through `resolveFeatureBranch` (runtime → the
  worktree the stages ran in → the workspace root), which is now the only
  caller of `loadFeatureBranch`. When no source can name a branch the
  degradation is loud: the #3873 reconcile logs the skipped PR probe (except
  at issue-pickup, where no branch can exist yet) and the history writer
  announces that the record will carry `BuildV2Record`'s synthetic
  placeholder (#397 tracks removing the fabrication itself). A reconciled
  stage also finishes the run instead of re-manufacturing the failure at the
  #2870 output check or the next stage's prerequisite check
- **#398** — the non-terminal reconcile's OPEN-PR arm treated _any_ open PR as
  proof the failure was a phantom. #299 armed that probe on every
  worktree-isolated run, which made the post-#4072 rewind shape reachable:
  conflict recovery rewinds pr-merge → feature-dev, **this run's own** PR is
  open, and a genuine feature-dev failure reconciles to a recorded success on
  the one path where the run demonstrably has unfinished work — and since #299
  the reconciled run ends there, so nothing downstream re-catches it.
  **Fixed:** ownership is decided by **identity, not content**. A run owns the
  PR whose **number** its pr-create recorded in `pr-{N}.json`, or — belt, for
  the edge where that record was never written — any open PR on the branch once
  the run has **reached pr-create** at all (a rewind is the only way an own PR
  can be open while a `feature-*` stage fails). The probe requests
  `--json state,number`; no git call and no SHA are involved. A two-pass scan
  decides it: (1) any **MERGED** PR reconciles unconditionally — list order must
  not let an open PR veto a merge; (2) otherwise an **own-run OPEN** PR blocks
  the reconcile, even when a foreign open PR is also listed; (3) otherwise a
  **foreign OPEN** PR still reconciles — #3873's stale-prior-run case,
  preserved, since the issue stays visibly open-with-a-PR for an operator while
  a false page erodes trust in every page. A PR the probe reported **no usable
  number** for counts as own-run: this arm's mistake direction is laundering a
  real failure into a success, so it must not act on a comparison it could not
  make.
  **Why not a commit comparison:** both content tests were measured and both
  misclassify, in opposite directions. Head-SHA equality calls an **own** PR
  foreign — the post-#4072 rewind re-dispatch rebases and commits on the branch,
  and WIP checkpoints commit locally with the push deferred, so the local tip
  outruns the pushed head and the genuine failure reconciles anyway (the defect,
  on its own motivating path). Head-SHA equality also calls a **foreign** PR own
  — issue-pickup reuses and resets the branch to the pushed tip
  (`reused-remote`), so a re-run's fresh checkout sits exactly at the prior PR's
  head and #3873's arm dies in the shape it was written for. Ancestry fails in
  both directions for the same two reasons: a rebase rewrites commits so the old
  head is no longer an ancestor, and branch reuse puts genuinely foreign heads
  inside the current tip's ancestry.
  The reconcile now reports **which** arm fired instead of a bare bool, so the
  log names the actual evidence (the old line said "closed / branch PR landed"
  for every arm), **every non-reconciling exit says why** — the own-run block,
  the no-PR fallthrough, the unnameable branch and the failed probe each log one
  line naming the issue — and the run's terminal board status is per-arm:
  issue-CLOSED → **Done**; PR-MERGED → **In review**, because that arm runs only
  after the issue check already answered NOT-closed and since #299 the
  reconciled run ends there, so `Done` would durably record
  Done-with-an-open-issue against the `Done ⟺ closed` invariant; stale foreign
  OPEN PR → **In review** (in review, not merged); every non-reconciled
  completion keeps its long-standing **In review**. The board write is no longer
  discarded — a status the run resolved but failed to persist logs
  `board status <S> NOT written: <err>`
- **#300** — `ParseStreamLine` ignores assistant per-turn usage; a stage
  killed before the `result` event books zero tokens
- **#301** — `captureConflictContextFromIndex` writes an empty capture as
  success, then `rebase --abort` destroys the evidence. **Fixed:** the capture
  returns three distinct outcomes and writes a document only for the one that
  earned it; both writers mark or omit a failure rather than emitting empty
  sides; and `conflict-recovery-loop` rejects a degenerate document per entry,
  not just per document. See
  [FEEDBACK_LOOPS.md](FEEDBACK_LOOPS.md#write-invariant--the-files-existence-is-the-claim-301)
- **#302** — batch of four small guards (bash-ring correlation self-check,
  zero-root worktree sweep, nil-state card retraction, unlogged
  `autonomousComplete` skip).
  **Fixed:** the forensic self-check gained a second arm that fires when no
  retained bash entry carried a usable `tool_use` id (retained-window
  indexed-ness — deliberately not lifetime correlation, which misdiagnoses a
  stage killed mid-command and goes blind to partial id-drift); zero resolved
  scan roots WARN in the sweep implementation (then two `sweepMergedWorktrees`
  copies, since unified by #403 with the scheduler-construction copy removed —
  constructors never delete); nil autonomous state
  is a logged fail-open that retracts nothing (per rule 2, "could not look" ≠
  "nothing wrong" — the reachable production analogue is #405); and all THREE
  terminal-funnel call sites (completed/failed/deferred) resolve their target
  through one total helper that warns with a per-site consequence instead of
  silently skipping — review found the unfixed success-path copy silently
  leaked the concurrency slot, a worse outcome than the failure path the
  issue named. #402 tracks the same hole in `ToolCallLog`
- **#402** — the same correlation hole in the all-tools `ToolCallLog` (#144),
  which #302 deliberately left untouched: entries were pushed but never
  indexed when the `tool_use` arrived id-less, `observeToolResult` returned
  silently, and no counter on the public surface noticed — so the Dashboard
  rendered rows with no result and no error, indistinguishable from calls that
  all succeeded quietly. **Fixed** for the id-less shape — batched parallel
  `tool_result`s (results 2..N of one user message) render the same symptom and
  remain a separate, pre-existing capture gap: the #302 mechanism mirrored onto
  the log — per-entry `indexed`/`joined` bookkeeping, lifetime `capturedTotal` /
  `correlatedResults` counters, a `retainedIndexedCount` over the retained
  window, and a `describeToolCallCorrelationGap` sibling emitted from the same
  self-check site under the same `[forensic-capture-gap]` prefix. The join is
  also where `duration_ms` — a wire field the Dashboard already read and
  nothing ever wrote to the persisted record — now gets populated; id-less
  entries stay without one, which is the absence the detector reports

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

| Issue | Behavior                                 | Wired to                        | Consequence in the operating mode                                                                                                                        |
| ----- | ---------------------------------------- | ------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| #210  | Stage gates                              | Extension wired 3 of 6          | `feature-dev`, `feature-planning`, `issue-pickup` ran ungated                                                                                            |
| #254  | `CompleteQueueItem`                      | Go `runPipeline` only           | every autonomous run leaked a permanent `processing` queue item                                                                                          |
| #304  | Learning outcome record                  | Go `runPipeline` only           | extension runs feed the self-improvement loop nothing                                                                                                    |
| #305  | Run-scoped attention cards               | Go `runPipeline` only           | no IPC raise verb exists; interactive runs produce zero Action Center cards                                                                              |
| #874  | Skill-root search path                   | TypeScript host: 2 roots; Go: 1 | `nightgauge queue run` could not render **any** stage skill in a repo that does not vendor `skills/`                                                     |
| #882  | `repoPathResolver` / `repoRootsResolver` | IPC server only                 | a cross-repo run rooted its state at the launch repo and pushed the branch to the launch repo's remote                                                   |
| #889  | Branch-name composition                  | BOTH — two implementations      | the extension hardcoded `feat/`, so every `type:bug` issue got a feature branch; it also doubled the issue number and truncated 10 chars shorter than Go |

**Why review does not catch it:** confirming _where_ a call sits inside a
function says nothing about whether that function is ever entered. #254's
comment even named the unwired path — as an aside, so it read as a benign edge
case. The question review must ask is not "is this correct?" but **"which of
the two paths reaches this — and is the other intentionally excluded?"**

**The third shape: BOTH paths implement it, and they disagree.** #889 is not a
capability missing on one side — it is the same capability written twice, once
per language, each self-consistent and each covered by its own tests. Nothing
compared them, so the drift was invisible for as long as nobody read two branch
names side by side. This shape is the most durable of the three, because every
local test is green and every reviewer sees correct code.

The repair is never "keep them in sync"; it is to delete one. `internal/git`'s
`ComposeBranchName` is now the only composer, reached from TypeScript over
`git.composeBranchName`, and
`TestNoSecondBranchNameComposerInTypeScript` fails the build if a second one
reappears in the extension sources. When you find this shape, ask which side the
design already names as the authority — for #889 the `issue-pickup` skill had
said "the Go binary" all along.

**The second shape: a capability the host wires up and the CLI leaves nil.**
The drift does not need an unreached call site. It is enough that one path is
_handed_ something the other constructs without. `Scheduler.WithRepoPathResolver`
and `WithRepoRootsResolver` are optional injection seams, and their doc comments
say so plainly:

```go
// The IPC server wires this from its ClientResolver.RepoPath; CLI/auto mode
// leaves it nil and every root resolves to the execution manager's workspace
// root (additive; single-repo behavior unchanged).
```

That comment was **true when written** — nothing could dispatch cross-repo from
the CLI — and became false the moment cross-repo queueing existed, without a
single line of it changing. The nil default then stopped being "single-repo
behavior" and started being "wrong repo": run state written under the launch
root, and a branch pushed to the launch root's remote (#882). #874 is the same
shape one layer down — the TypeScript host searched the workspace checkout _and_
the bundle beside the binary, Go searched only the checkout, so the Go-direct
path failed with `SKILL.md not found for stage "issue-pickup"` while the file sat
in `dist/skills/`.

**The rule: when one execution path is given a capability by its host, the other
path needs either the same capability or an explicit refusal — never a silent
default.** A resolver that may be nil must document what the nil case _means
today_, not what it meant when the seam was added, and a nil that is now
incorrect must fail loudly rather than resolve to the nearest plausible root.

**Wiring corollary: wire it at the CONSTRUCTOR, not at a call site.** A
component built from more than one place acquires this defect the moment a
capability is attached by whoever built it. The autonomous daemon is
constructed twice — by the CLI and by the IPC server on the extension's behalf
— so a sweep wired at one of those call sites is a sweep the other silently
does not have, and the half that runs is the half nobody tested. #885's
baseline evaluator is therefore wired inside `NewAutonomousScheduler` with a
test seam, not passed in by each caller: there is exactly one place to get it
right and no way for a second caller to forget. Ask of any new capability
"which constructors exist, and does every one of them reach this?" before
asking whether the code is correct.

**Testing corollary:** a test that exercises one path in isolation cannot see
this class — both paths pass their own suites, which is exactly why every
instance above shipped green. The assertion has to **compare the two paths**:
the same input through each entry point, asserting the same resolved capability
(the parity manifest below is that assertion, generalized).

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

### Vacuous Assertion (the test that cannot go red)

**Shape:** a test that passes on the fixed code and passes just as happily on
the broken code. It runs, it is counted, it is green — and it constrains
nothing. Unlike Silent No-Op the _product_ is fine; the defect is in the
evidence, so the class is invisible until the next regression sails past a suite
that was supposed to catch it.

**Worked example (#878).** A regression test was written for a `git push` that
failed on SSH remotes with `invalid auth method`. The assertion was "a push
succeeds with no credentials configured", exercised against a `file://` remote
in a temp dir. It passed. It also passed, byte for byte, against the code that
had the bug — a `file://` remote needs no credentials on either implementation,
so the one thing the test could never observe was the defect, which lived
entirely in **which transport ran**. Rewritten to assert the transport (the push
goes through the `git` CLI rather than the in-process library), it went red on
the old code immediately.

**Diagnosis: the assertion must name the thing that differs.** Ask what value
changed when the bug was fixed, then check the assertion can see that value. An
assertion about the code's _shape_ — a method exists, a field is present, a call
site is reachable — passes on both sides of a behavioral fix by construction.

**The proof is a revert, and it is one minute:**

1. `cp <file> /tmp/<file>.bak` — a **copy**. Never `git checkout -- <file>` to
   back a fix out: on an uncommitted branch that discards the fix itself.
2. Restore the pre-fix behavior from the copy (or hand-mutate the fixed line).
3. Run the new test. **It must FAIL.** If it is green, the test is decoration —
   rewrite the assertion, do not adjust the fixture.
4. Restore the fix, re-run, confirm green.

**Find the mutation that COMPILES.** A revert that only breaks the build proves
nothing about the assertion — the tests failed to compile, they did not fail.
Prefer a mutation that type-checks: flip a boundary, swap a branch, restore an
old default value.

**Corollary — a green suite is a statement about the cases that RAN**, not about
the cases that matter. Two assertions added to a `bash` suite running
`set -uo pipefail` **without `-e`** called helpers that did not exist yet; bash
printed `ok: command not found` to stderr, both cases silently did not run, and
the summary still reported all tests passed. Only the PASS count rising by one
instead of three exposed it. After adding assertions to any suite, **check the
reported count rose by exactly the number you added.**

### Unpinned Wiring (the guarantee that lives at the seam)

**Shape:** a mechanism is correct, its own unit tests are thorough and green —
and the property everyone relies on is produced not by the mechanism but by
**where it is constructed**. Nothing tests the construction site, so a one-line
scope change silently removes the guarantee while every test stays green.

This is a sibling of Vacuous Assertion, not an instance of it. There the
assertion could not see the defect; here the assertion is fine and **no test is
looking at the place the property actually lives**.

**Worked example (#907).** `internal/forge/boardcache` collapses repeated board
reads. Its headline win is that several repos sharing one project board cost
**one** 34-point read per sweep instead of N. That property is not implemented
in the cache at all — it comes from a single declaration in
`cachedSweepForgeClient`:

```go
var (
    routers = map[routerKey]*forge.Router{}
    boards  = boardcache.New(0)   // ONE cache for the whole factory
)
return func(repo string) (forge.ForgeClient, error) { … }
```

Move `boards` inside the returned closure — a plausible "scope it tightly"
cleanup — and the cache becomes per-repo. Every shared-board workspace pays a
full board read per repo again. The mutation compiled and **passed the entire
suite**, because:

- the cache's own tests exercise `Wrap` in isolation: they prove the cache
  works and never observe who _holds_ it; and
- the neighbouring test pinned **router** reuse, not snapshot reuse — two
  different objects with two different lifetimes, and only one was asserted.

**Diagnosis: ask where the property is produced, not where it is implemented.**
For any performance or correctness guarantee, name the line that would have to
change to lose it. If that line is a construction, a scope, a lifetime, or a
registration — and the tests all live in the package being constructed — the
guarantee is unpinned.

**The pin needs both directions.** Assert the sharing (N consumers, one
underlying call) _and_ assert the non-sharing (distinct keys stay distinct).
Without the second, a cache that over-shares — serving one board's items for
another — satisfies the first test while being strictly worse than no cache.

#### The worst case: wiring that never existed (#916)

`#907` was a guarantee that a plausible edit could _remove_. The degenerate form
is a guarantee that was **never wired in the first place**, and it is harder to
see because there is no regression — nothing ever worked, so nothing broke.

`execution.WorktreeSweepOptions.MergedPRLookup` shipped in #593 as the fail-open
second door for merged-ness. It had a fifteen-line doc comment explaining the
`gh pr update-branch` case it existed for, and two unit tests covering it. **All
three production call sites omitted the field**, and `lookupMergedPR` returns
`false` on a nil callback — so the door was closed everywhere it mattered and
open only in tests, each of which supplied the callback itself.

It stayed that way from #593 to #916 and cost real behaviour: a merged branch
went invisible to reclamation the moment the default branch touched any file the
branch owned. The `#912` scan lost a branch to it within an hour of shipping.

**The tell is an optional field with a nil-means-off default.** `Options` structs
make omission syntactically invisible: nothing at a call site marks the field as
absent, no compiler warns, and the mechanism's tests pass because they construct
their own. Any field whose zero value silently disables a guarantee is unpinned
by construction.

#### Pinning a construction site: read the source

The counter is a test that looks at the **construction**, not the behaviour —
which for Go means parsing the tree. `TestEveryProductionSweepCallSiteOpensTheMergedPRDoor`
walks every non-test `.go` file, finds each `WorktreeSweepOptions` /
`StrandedBranchOptions` composite literal, and fails when one omits
`MergedPRLookup`:

```
production sweep call site(s) omit MergedPRLookup, so the second door is closed
there and nothing says so:
  internal/doctor/leaked_state.go:85:46 WorktreeSweepOptions
```

Setting the field to a nil-returning expression passes — that is the documented
closed door, chosen and visible. **Omission** is the failure, because omission is
the thing nobody can see. This is the same instinct as
`TestEveryMutatingProjectMethodIsIntercepted`, which enumerates an interface by
reflection rather than trusting a hand-maintained list; where reflection cannot
reach — a struct literal's key set — the AST can.

Two properties keep such a test honest:

- **It must fail when the watched types vanish.** A source-walking test that
  finds nothing passes trivially, so assert a non-zero match count. A rename
  would otherwise turn the guard green forever.
- **It must exempt tests.** A test constructing its own door is doing the right
  thing; requiring the field there is noise that teaches people to widen the
  exemption.

### Read-Through Cache Without Write Interception

**Shape:** a read path is wrapped in a cache; the write paths that invalidate it
are not. Reads are fast and, for the length of the TTL, wrong. The staleness is
invisible to tests because every test either reads or writes, never both across
the boundary.

**Worked example (#848).** `boardcache` is safe on the sweep path precisely
because it intercepts mutating `ProjectService` methods and drops the snapshot —
`TestEveryMutatingProjectMethodIsIntercepted` exists to keep that true. The
obvious extension of the cache to the daemon's `board.list` would have satisfied
its acceptance criterion and shipped a UI bug: `board.list` builds a fresh
service per call, and the five mutating verbs (`board.updateStatus`,
`project.syncStatus`, `project.syncIteration`, `project.setHours`,
`project.addItem`) each build their own and route through no wrapper. An
operator moving an item to Done would keep seeing the old status for the whole
TTL, with nothing red.

**The cheap repair is the trap.** Invalidating at each of the five call sites is
bounded and easy, and it is the "keep two things in sync" shape this repo rules
out: the sixth mutating verb someone adds later reintroduces the staleness
silently. The repair is to route reads **and** writes for a given key through
one wrapped client, so the interception that already exists does the work and no
new sync surface is created.

**Diagnosis: before caching a read, enumerate every writer of the same data and
check each one passes through the wrapper.** If any writer constructs its own
client, the cache is not safe on that path yet — and a TTL short enough to hide
the problem is not a fix, it is a smaller version of the same bug.

### Broken vs. Could-Not-Run (the check that collapses two verdicts into one)

**Shape:** a check that cannot distinguish _the thing under test is broken_ from
_the check itself could not run_, and reports both as the same outcome. Which
outcome it picks is a detail; the defect is that a red (or green) run no longer
identifies which of the two happened, so the correct response and the incorrect
response are the same gesture.

**It shows up in both directions, and both are this class:**

| Instance                               | Could-not-run signal           | Collapsed into         |
| -------------------------------------- | ------------------------------ | ---------------------- |
| link-check on an errored fetch (#1004) | `Status: 0` — no HTTP response | **dead link** (fail)   |
| `ci-local.sh` step guards (#983)       | the script it runs is missing  | **step passed** (skip) |
| pre-push Node validation (#1159)       | `npm run build` has no script  | **build failed**       |
| SKILL.md metadata validation (#856)    | frontmatter block never closed | **missing field**      |
| staging platform smoke (#1087)         | no credential is provisioned   | **`main` is red**      |

**Why the direction does not matter.** Collapsing toward pass is a Silent No-Op:
coverage disappears with no observable difference from success. Collapsing
toward fail is worse in a way that is easy to under-rate — it is a standing
false alarm, and a check that cries wolf trains everyone to stop reading it.
Nine consecutive red scheduled runs (#1087) cost a real investigation on an
unrelated merge and taught the operator to disbelieve the post-merge query that
`AGENTS.md` mandates. The next time that query is right, the reflex will skip it.

**Diagnosis — one question, asked at every point a check reads a signal:** _can
this value mean "I could not find out"?_ An exit code, an HTTP status, a missing
file, an absent credential and an empty parse buffer all can. If the answer is
yes, that reading needs its own state.

**The repair, and it is the same three steps every time:**

1. **Name the third state.** Not `passed`/`failed` but `passed` / `failed` /
   `not-applicable` / `unreachable-from-runner` / `unknown` / `UNREADABLE` — a
   value a human reading the log can act on without opening the raw output.
2. **Decide its verdict deliberately, and write down why.** "Could not run" is
   usually **fail-closed** (#856, #1159's unparseable `package.json`, the
   AC-completion gate in `skills/nightgauge-feature-validate/`), because a gate
   that cannot run has not granted anything. It is **not fatal** only when the
   inability is provably outside the repository's control and re-probing has
   ruled out the real defect (#1004's `unreachable-from-runner`: a transport
   failure that survives four attempts and is not NXDOMAIN). Either way the
   state is **recorded** — `pre-push-<N>.json` carries `build: not-applicable`
   rather than omitting the phase.
3. **Keep the other half of the pair red.** Every fix here weakens a failure
   path, so it must be paired with a test that the genuine defect still fails:
   a dead _internal_ link still fails link-check, a build script that exists
   and fails still blocks, a genuinely missing `metadata.source` is still an
   ERROR. Without that pair, "stop failing on X" is satisfiable by never
   failing at all.

**Related classes:** Silent No-Op (the fail-open half), Vacuous Assertion (what
step 3 exists to prevent).

### Two Clocks In One Write Path (Issue #1455)

**Shape:** one operation takes a time-dependent decision twice, from two
independent reads of the clock, and the two readings are compared against each
other. The window between them is normally sub-millisecond, so the code passes
every test and every review — until the two readings straddle a boundary
(midnight, a retention edge, a timezone difference between two machines), and
then the operation contradicts itself.

**The instance.** `HistoryWriter.appendAndIndex` files a run record under a
caller-supplied `now` — that instant chooses the daily `YYYY-MM-DD.jsonl`
filename — and then ran a retention prune whose cutoff came from its own
`time.Now()`. Two clocks, one write: a record filed under `now` was measured
against a different instant, so the prune riding along with the append could
delete the daily file and the `index.json` entry the append had just produced.
The symptom was a test whose fixture is dated by hand — a record at
`2026-06-06`, a 90-day window — flipping to failing the day the wall clock
passed record-date plus retention. It failed on the UTC runner and passed 30/30
on a UTC-6 machine the same afternoon, because the two hosts were on opposite
sides of the boundary for six hours. Nothing in the diff, the branch or the test
was involved; the calendar was.

**Diagnosis:** grep the operation for every clock read. More than one, and ask
which of them the operation's own output is dated by — that is the only clock
the rest of the operation may use. Prefer an injected instant threaded through
the whole call over a convenient `time.Now()` in a helper: a helper that reads
the clock itself cannot be made consistent with its caller by any test.

**The paired invariant.** A single clock is necessary but not sufficient. Where
a write and a cleanup share a critical section, the cleanup must also be
forbidden from removing the thing the write just produced, because the two can
disagree on _which_ date describes a record even when they agree on now: the
history index entry's date comes from producer-supplied `recorded_at`, the
filename from `now`, and nothing forces those to match. Exempt the record being
written by identity, not by re-deriving its date.

**Related classes:** Dual-Path Drift (two implementations of one decision, here
two readings of one input), Silent No-Op (the write reports success and leaves
nothing behind).
