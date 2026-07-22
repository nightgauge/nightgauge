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

| Stage              | Gate (Go)             | What it checks                                                        |
| ------------------ | --------------------- | --------------------------------------------------------------------- |
| `issue-pickup`     | `IssuePickupGate`     | `pipeline/issue-{N}.json` exists, parses, names a feature branch      |
| `feature-planning` | `FeaturePlanningGate` | `pipeline/planning-{N}.json` references a non-empty `plan_file`       |
| `feature-dev`      | `FeatureDevGate`      | `pipeline/dev-{N}.json` records ≥1 file change, build_verification ok |
| `feature-validate` | `FeatureValidateGate` | every `gate-metrics.jsonl` quality-gate record `result == "pass"`     |
| `pr-create`        | `PrCreateGate`        | `pipeline/pr-{N}.json` records `pr_number`; `gh pr view` is OPEN      |
| `pr-merge`         | `PrMergeGate`         | `gh pr view` reports `state == "MERGED"`                              |

Gates that call `gh` use a 3-attempt, 1-second-backoff internal retry to
absorb transient API failures (rate-limit, transient 5xx) before reporting
`passed: false`.

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
