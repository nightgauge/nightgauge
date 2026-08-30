# Stage Verification Gates

> Issue #3266 — Each pipeline stage publishes a deterministic post-condition
> gate. The orchestrator runs the gate immediately after the skill reports
> success and treats `passed: false` as a stage failure.

## Why

LLM-driven stages can report `success` without actually doing the work. We
saw this concretely in:

- **#1819 / #2868** — `pr-merge` exited 0 without merging the PR; CI failures
  hid in the noise.
- The "skill said success but didn't write the context file" failure mode
  surfaced in different stages over time.

Before #3266, each stage had its own ad-hoc post-state check
(`verifyPostMergeState` in TS, inline `loadFeatureBranch` / `loadPrUrl`
calls on the Go side). This guide describes the unified replacement.

## The deliverable-schema policy

A gate whose only verb is _reject_ punishes a bookkeeping defect exactly as hard
as a substantive one. Before decoding a deliverable, `FeatureDevGate` and
`FeatureValidateGate` run the closed rule table in `internal/deliverable/policy.go`:
a totally repairable shape is rewritten and recorded, an unattributable telemetry
entry is quarantined and marked untrustworthy, and anything genuinely missing
still fails. The SAME table runs at the TypeScript context-file validator and
in-stage via `nightgauge gate check-deliverable`, so one defect cannot be a
warning in one stage and a run-ender in the next.

See
[docs/CONTEXT_ARCHITECTURE.md § The Deliverable-Schema Policy](CONTEXT_ARCHITECTURE.md#the-deliverable-schema-policy-1182-1176-1177)
for the rule table and dispositions.

## The framework

```
┌──────────────────────┐    success     ┌────────────────────────────┐
│  Skill (LLM-driven)  │ ─────────────▶ │  Stage post-condition gate │
│  feature-dev, etc.   │                │   (deterministic Go code)  │
└──────────────────────┘                └────────────┬───────────────┘
                                                     │
                              passed=true            │           passed=false
                                  │                  │                 │
                                  ▼                  ▼                 ▼
                         advance to next       persist gate        synthesize
                            stage              result on run        stage error
                                               record (always)         │
                                                                       ▼
                                                              fall through to
                                                              existing failure
                                                              branch (retry,
                                                              backtrack, etc.)
```

A `StageGate` is a pure function over (workspace, issue number) that returns
a `GateResult{ Passed, Reason, Evidence, ... }`. Gates live in
`internal/orchestrator/gates/` and MUST remain deterministic — no LLM calls,
no network beyond the `gh` queries the prior post-state logic already
performed (see `.claude/rules/scripts.md`).

> **Network-bound checks are NOT StageGates.** A check that must reach the
> network or call an LLM cannot live in the registry without breaking the
> determinism contract above. Such checks run as **CLI/skill preflights**
> instead — e.g. the dependency-guard (`nightgauge preflight
dependency-guard`, #4095) hits package registries, so it runs in
> `skills/pr-preflight` with network-inconclusive lookups treated as
> non-blocking, rather than as a registry `StageGate`. Any future LLM-as-judge
> verification (#4097) must follow this same precedent — run as a preflight, or
> explicitly fork/relax this contract here.

The orchestrator's stage loop (in
`internal/orchestrator/scheduler.go:runPipeline`) calls
`gate.Verify(...)` after the skill reports success. On `passed: false` the
loop synthesizes an error and falls through to the existing
failure-handling branch — the retry/backtrack engine treats it like any
other stage failure. There is no separate code path for gate failures. The
synthesized text depends on the gate's `Kind` (#74): `KindNoOp` — the skill
exited 0 but produced no state change, i.e. the agent ended its turn on a
promise — stamps `premature turn end: stage exited 0 with no state change
(gate no-op): <reason>` so `ClassifyTerminalKind` records the
`premature_turn_end` terminal kind (pr-merge's no-op keeps its richer
`pr_merge_unmerged` classification, #3691); `KindFail` keeps the original
`stage gate failed: <reason>` text.

### TerminalKind (Issue #9)

`Kind` (`ok`/`no_op`/`fail`) discriminates outcome _shape_. `GateResult` also
carries an optional, finer-grained `TerminalKind string` field naming the
specific `TerminalKind*` constant (from `internal/orchestrator/
failure_handler.go`) that the failure maps onto — e.g. a JSON-parse failure
sets `TerminalKind: TerminalKindValidationError`. Before this field existed,
the terminal kind was always re-derived downstream by lowercasing and
substring-matching the synthesized error text via `ClassifyTerminalKind` —
fragile by construction, and it silently mis-bucketed the JSON-parse-failure
gates (`"...is not valid JSON"`, `"unparseable JSON"`) into the generic
subagent-crash kind because the classifier only matched the literal
substring `"invalid json"` (now fixed to also match both real phrasings).

Rules for gate authors:

- Only set `TerminalKind` on `KindFail` returns whose failure shape maps
  cleanly onto an **existing** `TerminalKind*` constant. Do not invent new
  constants for this — the taxonomy in `failure_handler.go` is canonical.
- Leave `TerminalKind` empty (`""`) when no clean constant applies, or on any
  `KindOK`/`KindNoOp` return — those already classify structurally via `Kind`
  alone (see above) and don't need a terminal kind.
- `gates.GateResult.TerminalKind` is duplicated as `gates.TerminalKindValidationError`
  etc. rather than importing `internal/orchestrator`'s constants directly —
  `orchestrator` imports `gates`, so the reverse import would cycle. Keep the
  two sets of constants in sync by hand.

Consumers should call `orchestrator.ResolveTerminalKind(gateRan,
gateResult.TerminalKind, errorText)` instead of calling
`ClassifyTerminalKind` directly whenever a `gates.GateResult` is in scope.
`ResolveTerminalKind` prefers the gate's structured kind when present and
falls back to prose classification of `errorText` otherwise — including for
every historical `StageGateResult` record persisted before `terminal_kind`
existed on the type (the field is `omitempty`/optional on both the Go struct
and the TypeScript `StageGateResultSchema` mirror). The TypeScript SDK
mirrors this precedence as `resolveTerminalKind` in
`packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts`.

## Persistence

Gate results land in `V2StageDetail.gate_results []StageGateResult` on the
run record. The field is additive `omitempty` — old records read with a
nil/empty slice (V1 ∪ V2 ∪ V3 union convention from ADR-002). The schema
version is **not** bumped.

> **Naming collision**: `state.GateResult` (already in the codebase) records
> the build/lint/test **quality-gate** outcome. The new `state.StageGateResult`
> records **stage post-condition** outcomes. They have different shapes
> (`Result string` vs `Passed bool`) and different semantics. They coexist
> on the same run record:
>
> - `V2RunRecord.GateResults` — quality gates (run-level)
> - `V2StageDetail.GateResults` — stage gates (per-stage)

## The six default gates

| Stage              | Gate (Go)             | What it checks                                                                                                          |
| ------------------ | --------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `issue-pickup`     | `IssuePickupGate`     | `pipeline/issue-{N}.json` exists, parses, names a feature branch                                                        |
| `feature-planning` | `FeaturePlanningGate` | `pipeline/planning-{N}.json` references a non-empty `plan_file`                                                         |
| `feature-dev`      | `FeatureDevGate`      | `pipeline/dev-{N}.json` records ≥1 file change, build_verification ok — **and git agrees the workspace changed** (#202) |
| `feature-validate` | `FeatureValidateGate` | every `gate-metrics.jsonl` quality-gate record `result == "pass"`                                                       |
| `pr-create`        | `PrCreateGate`        | `pipeline/pr-{N}.json` records `pr_number`; `gh pr view` is OPEN                                                        |
| `pr-merge`         | `PrMergeGate`         | `gh pr view` reports `state == "MERGED"`                                                                                |

Gates that call `gh` use a 3-attempt, 1-second-backoff internal retry to
absorb transient API failures (rate-limit, transient 5xx) before reporting
`passed: false`.

### Self-report vs ground truth (#202)

Most of the table reads a context file — which is the **skill's report of
itself**, not evidence. A stage can describe its work perfectly honestly and
still have performed it somewhere no later stage will look, and the gate will
pass it. That is what happened on #202: `feature-dev` truthfully listed five
changed files, but a worktree-isolated subagent had written them into
`.claude/worktrees/agent-<id>` rather than the run's `.worktrees/issue-<n>`,
and `feature-validate` spent another $0.87 rediscovering it.

`FeatureDevGate` therefore ends with a git check the skill cannot influence:
the workspace must hold uncommitted changes, or the branch must carry commits
its base lacks. Two rules make it safe to run after the money is spent:

- **Fail open on anything unverifiable.** Not a git repo, or no base ref
  resolves (`origin/main` → `main`) → the gate passes. "Cannot verify" must
  never become "verified empty", or every dev stage in a repo whose default
  branch is not `main` fails and the safety rails halt the queue.
- **Bookkeeping is not the deliverable.** `.nightgauge/` and `.claude/` are
  excluded (`ci.BookkeepingDirs`). Whether they show in `git status` is a
  per-repo accident — this repo ignores `.nightgauge/pipeline` but not
  `.nightgauge/attention` — so counting them would let the run's own exhaust
  answer "did this produce work?" and quietly disable the gate.

When it fails it reports `dev_produced_no_changes` and names any sibling
worktree still holding uncommitted work, so the stranded implementation can be
recovered from the failure record instead of by hand.

## Which statuses satisfy a `blockedBy` edge (#281)

Same theme as the section above — board status is not ground truth without
corroborating evidence, applied to `AutonomousScheduler`'s dependency
evaluation (`evaluateDeps` in `internal/orchestrator/autonomous.go`) rather
than a stage gate. Before #281, `isWorkCompleteStatus` treated the board
status string `"In review"` as unconditional proof that a dep's work was
done — true when a PR-merge case parked a genuinely-blocked issue there, but
false when the architecture-approval gate sidelined a zero-implementation
issue to the same status (the gate fires _before_ feature-dev ever runs, so
no branch, no PR). Every dependent of that issue read the bare status string
and dispatched against a contract that was never built.

- **`Done` / issue `CLOSED`** — always satisfies. Terminal, unambiguous.
- **`In review`** — satisfies **only** when an OPEN PR is confirmed for that
  issue, via `openPRMergeStatesForRepo` (one batched `gh pr list` per repo,
  cached each fresh graph build as `AutonomousScheduler.inReviewPRBacked`).
  The board string alone is never sufficient — `evaluateDeps` checks
  membership in that confirmed set before treating the dep as satisfied.
  Fail-closed on query failure: a repo whose PR query fails contributes
  nothing to `inReviewPRBacked`, so its `"In review"` deps read as blocked,
  never as silently satisfied.
- **`Ready`, `Backlog`, `In progress`, no status** — never satisfies.

The halt path that produced the false positive is fixed the same way:
`sidelineHalt` now checks `openPRMergeStatesForRepo` before choosing a board
status — `"In review"` only when a PR is actually confirmed open, `"In
progress"` otherwise (a new `moveIssueToInProgress`, non-dispatchable but
also never mistaken for PR-backed work). The architecture-approval and
pr-merge-unmerged halt sites both route through it now instead of calling
`moveIssueToInReview` directly.

## CLI seam — `nightgauge gate verify`

```
nightgauge gate verify <stage> <issue-number> [--workdir <path>] [--json] [--timeout <sec>]
```

Exit codes:

| Code | Meaning                                    |
| ---- | ------------------------------------------ |
| `0`  | `passed: true`                             |
| `2`  | `passed: false` (gate ran, post-state bad) |
| `1`  | invalid arguments / IO error / no gate     |

The TypeScript `HeadlessOrchestrator.verifyPostMergeState` is now a thin
shell over `nightgauge gate verify pr-merge <N>` — single source of
truth. The deterministic merge fallback (Issue #3259) and
`escalateUnverifiedMerge` paths stay in TS because they consume gh fields
the gate doesn't expose (`mergeable`, `mergeStateStatus`).

### Reachability from the extension path (Issue #210)

All six registered gates are invoked from the extension's legacy TS pipeline
loop (`HeadlessOrchestrator.runPipeline()`), not just the three that had
dedicated `verifyPost*State` methods:

- `pr-merge`, `feature-validate`, `pr-create` keep their existing dedicated
  methods (`verifyPostMergeState`, `verifyPostValidateState` +
  `deriveUnexercisedDeliverable`, `verifyPostCreateState`) — each has bespoke
  retry/fallback logic (merge retry, unexercised-deliverable derivation,
  deterministic create fallback) that a generic gate call would not
  replicate.
- `issue-pickup`, `feature-planning`, `feature-dev` run through one shared
  helper, `runPostConditionGate(stage, issueNumber)`, called at the single
  point every stage passes through after context validation/recovery and
  before any stage-specific side effects. A `KindNoOp` result is mapped onto
  the same `premature turn end: stage exited 0 with no state change (gate
no-op): <reason>` text the Go scheduler synthesizes
  (`internal/orchestrator/scheduler.go`), so `ClassifyTerminalKind` /
  `ResolveTerminalKind` classify identically regardless of which
  orchestration path produced the failure.

Every one of the six `execFileAsync(binary, ["gate", "verify", ...])` call
sites in `HeadlessOrchestrator.ts` passes `--record` (see below) so the
result lands in the run record's `stageGateResults` map — previously only
the in-process Go scheduler loop persisted gate results; the TS path never
did, even for the three already-wired gates.

A completion-time guard, `logGateNotInvoked`, runs after the stage loop ends:
for every completed, non-bookend stage with a registered gate
(`PIPELINE_STAGE_GATE_STAGES` in `HeadlessOrchestrator.ts`), it confirms the
persisted run record has at least one recorded result for that stage. A
completed gated stage with zero recorded results logs a structured
`[gate-not-invoked] stage=<stage> issue=<N>` line via `onStderr` — a
non-fatal signal on first landing, not a hard pipeline failure, so a
legitimate future NoOp/skipped stage cannot become a false failure.

### `--record`: persisting a gate result from outside the Go scheduler

`nightgauge gate verify <stage> <N> --record` additionally appends the
`GateResult` onto the run record via
`internal/state.AppendStageGateResultToDisk`, which loads
`{workdir}/.nightgauge/pipeline/runtime-{N}.json` (falling back to a fresh
`RuntimeState` when absent), appends through the same
`RuntimeState.AppendStageGateResult` the in-process scheduler loop uses, and
persists via `RuntimeState.Persist` — the same file the scheduler itself
writes to. The flag defaults to `false` and is purely additive: it does not
change the `0`/`1`/`2` exit-code contract.

## Disabling specific gates

Set `NIGHTGAUGE_DISABLE_GATES=<comma-separated stage names>` to remove
those stages from the registry at scheduler startup. This is meant for
integration-test environments that cannot satisfy a gate's external
dependencies (e.g., the IPC E2E tests run without real `gh` access, so they
disable `pr-create,pr-merge`). In production the var should be unset.

## Adding a new stage gate

1. **Implement `StageGate`** — add `internal/orchestrator/gates/<stage>_gate.go`.
   The body should be deterministic and read whatever skill output the
   stage produces under `.nightgauge/pipeline/`. Use the `timed(...)`
   helper to fill in `DurationMs` and `Timestamp` automatically.
2. **Register** — add an entry to `gates.Default()` keyed by the stage's
   `state.PipelineStage` constant.
3. **Test** — add `<stage>_gate_test.go` with at minimum:
   - `Pass` — happy path
   - `Fail_<reason>` — at least one explicit-failure case
   - `SkillSaidSuccessButGateFailed_<scenario>` — the canonical "skill
     reported success but didn't actually do the work" scenario
4. **Update this table** — keep the "six default gates" list above current.
5. **No schema change required** — `V2StageDetail.GateResults` is keyed by
   `GateName`; new gates slot in without bumping `schema_version`.

## Testing seams

- `gates.execGh` — package-level function pointer the gh-backed gates
  call. Tests swap it out to inject canned `gh` JSON.
- `Scheduler.WithStageGates(reg)` — replaces the post-condition registry.
  Pass `nil` to restore the default.

## See also

- `internal/orchestrator/gates/gate.go` — the `StageGate` interface and
  shared helpers
- `internal/orchestrator/scheduler.go` — the stage-loop hook that runs
  gates after `RunStage` returns success
- `internal/state/history.go` — `StageGateResult` and `V2StageDetail.GateResults`
- `cmd/nightgauge/gate.go` — the CLI subcommand
- `packages/nightgauge-vscode/src/services/HeadlessOrchestrator.ts`
  (`verifyPostMergeState`) — the TS shim that delegates to the binary
