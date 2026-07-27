# Adaptive Pipeline (Runtime Integration Removed)

The adaptive policy / auto-tune **runtime integration was removed from the
extension** in the dashboard cleanup initiative. The system automatically
rewrote `config.yaml` after every pipeline run — adjusting model thresholds,
budgets, retry policies, timeouts, and routing overrides. This created opacity
and complexity that didn't justify its value.

The underlying SDK library classes are retained for analysis and platform use
(see "What Was Retained in the SDK" below).

## What Was Removed from the Extension

- Auto-tune threshold adjustments in PostPipelineAnalyzer
- Adaptive policy dashboard panel and health widget integration
- `SelfTuningLogger` — JSONL audit trail for auto-tune changes (fully removed)
- 8-component weighted health score (simplified to 4 components)
- All runtime invocations of `AdaptivePolicyEngine`, `AutoRollbackEngine`, and
  `ExperimentEvaluator` from the extension

## What Was Retained in the SDK

The following classes remain in `packages/nightgauge-sdk/src/` with full
test suites. They are exported for offline analysis, platform integration, and
future re-enablement — they are simply no longer called at runtime by the
extension:

- **AdaptivePolicyEngine** (`services/AdaptivePolicyEngine.ts`) — policy
  decision generation, budget rebalancing, recurring adjustment logic
- **AutoRollbackEngine** (`services/AutoRollbackEngine.ts`) — health degradation
  detection and rollback logic
- **ExperimentEvaluator** (`analysis/ExperimentEvaluator.ts`) — A/B experiment
  evaluation

## What IS Currently Active in the Extension

- **HealthActionService** — evaluates health dimensions and triggers
  health-gated tier actions
- **PipelinePolicyOverrides** — applies static policy overrides to pipeline
  configuration (no auto-tuning)
- **Health-gated tiers** — pipeline behavior adapts based on health score
  thresholds, but thresholds are fixed in configuration (not auto-adjusted)

## What Was Kept

- **Model routing display** (read-only) — shows which model was selected for
  each stage
- **Cost tracking** — per-issue and per-run cost aggregation
- **Failure pattern detection** — identifies recurring failure categories
- **Outcome recording** — records pipeline outcomes for complexity calibration
- **Complexity model + calibration** — predicts issue effort from history
- **Pipeline history** — execution history for analysis
- **Health score** — simplified to 4 components: Success Rate, Cost Trend,
  Reliability, Cache Hit Rate
- **Gate effectiveness** — tracks quality gate hit rates
- **Skill effectiveness** — tracks impact of SKILL.md changes

## Go-Path Performance Mode

The Go scheduler reads `.nightgauge/performance-mode.yaml` on every routing
call via `resolvePerformanceMode()` in `internal/intelligence/routing/performance_mode.go`.
This ensures mode changes take effect on the next pickup without restarting the
binary.

### Precedence

1. `NIGHTGAUGE_PERFORMANCE_MODE` environment variable (highest)
2. `.nightgauge/performance-mode.yaml` in the workspace root
3. `elevated` default (no model overrides applied)

### Mode Effects

| Mode         | Lightweight stages (`issue-pickup`, `pr-create`, `pr-merge`) | Development stages (`feature-planning`, `feature-dev`, `feature-validate`) |
| ------------ | ------------------------------------------------------------ | -------------------------------------------------------------------------- |
| `efficiency` | Haiku                                                        | Sonnet                                                                     |
| `elevated`   | No override (complexity-based selection)                     | No override (complexity-based selection)                                   |
| `maximum`    | Opus                                                         | Opus                                                                       |

When a non-elevated mode is active, the routing `Reasoning` field is annotated
with `(performance-mode: <mode>)` so it is visible in cost estimates and logs.

### File Ownership

The VSCode extension writes `.nightgauge/performance-mode.yaml` via the
performance mode UI. The Go binary only reads it — never writes it. The TypeScript
`skillRunner.ts` path reads the same file via `getPerformanceMode()` in
`monitoringResolver.ts`, so both execution paths now honor the same user
preference.

## Progress-Based Runaway Detection (Issue #3783)

Replaces the dollar-ceiling hard-kill with semantic forward-progress awareness.
A stage is killed only when no new progress signal arrives within a sliding
time window — so a genuinely active-but-expensive stage runs to completion while
a cheap infinite loop is stopped quickly.

### How It Works

`ProgressMonitor` (in `packages/nightgauge-vscode/src/utils/progressMonitor.ts`)
tracks five signal types:

| Signal type     | When recorded                                          |
| --------------- | ------------------------------------------------------ |
| `phase_marker`  | `<!-- phase:start ... -->` detected in output          |
| `file_change`   | Bash command matching write/commit patterns            |
| `ci_progress`   | `CI_PROGRESS:` JSON line detected in stderr            |
| `distinct_tool` | New unique tool signature (toolName + first 200 chars) |
| `commit`        | `git commit` command observed                          |

`distinct_tool` uses deduplication: repeated identical tool signatures (same tool
name + input prefix) are ignored and do NOT reset the no-progress window. This
catches the "reads the same file in a loop" runaway pattern.

### Kill Logic

`progressMonitor.check(costUsd)` is called on the 30-second stall ticker. It
returns `shouldKill: true` when:

1. `enabled: true` (master toggle)
2. `costUsd >= minCostToActivateUsd` (default $0.50 — prevents false kills on
   short/cheap stages)
3. `Date.now() - lastProgressMs > noProgressWindowMs` (default 2 min)
4. `Date.now() - lastActivityMs > noProgressWindowMs` — the **activity gate**
   (Issue #128), see below
5. `observeOnly: false` (maximum performance mode forces this to true — warns but
   never kills)

### Activity Gate (Issue #128)

Productive signals are _artifact_ signals, but the terminal phase of every stage
— final verification, scope-tidying, the self-assessment epilogue — produces no
artifacts by definition. Under a productive-signals-only rule that phase is
indistinguishable from a wedged process, so the monitor was most likely to fire
exactly when a stage was closest to succeeding. A `feature-dev` stage was killed
153s into precisely that phase, after 3 productive signals and a complete
implementation.

The monitor therefore keeps a second clock, `lastActivityMs`, advanced by every
**novel** tool invocation (and by every productive signal). The plain
no-progress kill requires both clocks to be cold. "Novel" is load-bearing:
repeated identical tool signatures are deduplicated before they reach the
activity clock, so a spin loop is still killed, and a wedged process — which
issues no tool calls at all — is killed on exactly the schedule it was before.

The gate defers the no-progress kill; it cannot disable it. The churn detector
(below), the stage hard-cap, and the catastrophic cost backstop all still fire
against a busy-but-unproductive stage. When the gate defers a kill, skillRunner
logs `[runaway-progress-activity-gate]` once so the deferral is visible in a
retro.

### Nx Stall-Multiple Escalation (#3851, gated in #161)

The escalating stall warnings (2×, 3×, … the warn threshold) used to be a pure
no-op. #3851 turned the Nth into a kill: at `NX_RUNAWAY_KILL_MULTIPLE` (8×) with
no productive progress over the window, skillRunner escalates to the runaway
kill machinery. This is a **derived** wall-clock ceiling —
`stall warn threshold × 8`, so 40 min for `feature-validate` (300s default) and
80 min for `feature-dev` (600s default) — that exists in no config file.

As shipped it called the kill in force mode, which bypassed
`ProgressMonitor.check` entirely: not just the progress window, but the cost
activation floor and the #128 activity gate with it. #161 recorded the result —
two `feature-validate` stages killed at exactly 2400s with `idle_ms_at_exit` of
376ms and 621ms, one of them having already built, validated, committed, and
reached its final `git push`. The kill even reported the monitor's _non-kill_
sentence ("cost $0.0000 below activation threshold $0.5") as its termination
reason.

The Nx path is now activity-gated on the same clock as the plain no-progress
kill: a stage that issued a novel tool call inside the window is working, not
looping, and is not killed. Deferrals log `[runaway-nx-activity-gate]` once. A
kill that does fire names its ceiling (`nx-stall-multiple`) and value in the
stage-exit record — see
[STAGE_EXIT_DIAGNOSTIC.md § Kill Ceilings](STAGE_EXIT_DIAGNOSTIC.md#kill-ceilings-161).
Churn is still bounded by the churn detector below, which counts distinct tool
signatures rather than clocks and is deliberately not gated.

### Churn Detector

A non-converging stage spins through many distinct tool signatures while making
no productive progress. The churn detector kills when cost is past the
activation floor, distinct-tool signatures have climbed by
`churn_tool_threshold` (default 40) since the last productive signal, and the
productive window has elapsed. It is deliberately **not** activity-gated —
killing a stage that is busy but not converging is its entire purpose (#3811:
530 tool calls, 0 commits, $112).

### Dollar-Ceiling Demotion

The former `runwayCeilingUsd` kill path (`max($75, effectiveCap × 3.0)`) is
demoted to **warn-only** (`checkRunawayCeilingWarn`). It still fires a toast but
no longer terminates the stage. The `stage_cost_caps` per-stage hard USD ceilings
remain unchanged as the catastrophic backstop.

### Configuration

```yaml
pipeline:
  progress_runaway:
    enabled: true # master toggle (default: true)
    no_progress_window_ms: 120000 # 2 min window (minimum: 30s)
    min_cost_to_activate_usd: 0.50 # don't fire on cheap stages
    catastrophic_limit_usd: 200 # warn-only backstop if monitor itself fails
```

All fields support env var overrides:

- `NIGHTGAUGE_PIPELINE_PROGRESS_RUNAWAY_ENABLED`
- `NIGHTGAUGE_PIPELINE_PROGRESS_RUNAWAY_WINDOW_MS`
- `NIGHTGAUGE_PIPELINE_PROGRESS_RUNAWAY_MIN_COST_USD`
- `NIGHTGAUGE_PIPELINE_PROGRESS_RUNAWAY_CATASTROPHIC_LIMIT_USD`

### Interaction with Performance Modes

| Mode         | Effect on progress monitor                                       |
| ------------ | ---------------------------------------------------------------- |
| `efficiency` | Normal kill behavior                                             |
| `elevated`   | Normal kill behavior                                             |
| `maximum`    | `observeOnly: true` — warns but never kills (maximum throughput) |

### Terminal Kind

When the progress monitor fires, `exitSignalSource = "runaway-progress"` is set
and the kill marker `[runaway-progress-exceeded]` is emitted. The Go
`failure_handler.go` maps this to `TerminalKindRunawayProgress` — same recovery
path as stall-kill (30m backoff, board→Ready, no lifetime-failure-cap increment).

### Work-in-Progress Preservation (Issue #128)

`feature-dev` never commits — the commit lives in `feature-validate` Phase 5
(#1608). So between a stage's first edit and that commit, the entire deliverable
exists only as uncommitted changes in the worktree, and **every** guard kill
lands on work nothing downstream can see.

`preserveWorkInProgress` (`packages/nightgauge-vscode/src/utils/`) closes that
hole. Every kill path — progress-runaway, idle-stall, hard-cap, quota fast-fail,
autonomous abort — funnels through skillRunner's single process-close handler,
which calls it whenever the stage was killed by a guard (`shouldPreserveWorkOnExit`)
and the worktree is dirty. It:

- commits the worktree to the stage branch with `--no-verify` and
  `commit.gpgsign=false`, so no hook or pinentry prompt can block a kill path;
- refuses on `main` / `master` and on a detached HEAD, leaving the work in place
  rather than committing it somewhere dangerous or unreachable;
- writes a durable anchor ref under `refs/nightgauge/wip/`. Re-dispatch
  force-removes the worktree and runs `git branch -D <branch>` before
  re-creating the branch from `origin/<base>` (`WorktreeManager.create`), which
  would orphan a branch-only commit; the anchor survives both, so the work is
  always a `git log refs/nightgauge/wip/` away;
- never throws — every failure mode is reported and logged
  (`[wip-preserved]` / `[wip-preserve-skipped]`), never propagated into the kill
  path.

Stages that fail on their own are left alone: a non-zero exit is the stage's own
verdict and `feature-validate` explicitly leaves a failed tree in place for
triage. Only work the pipeline destroyed is work the pipeline preserves.

This is the safety net that makes the whole class of guard kill non-destructive
regardless of how the guards are tuned — a mis-tuned kill costs a retry, never
the work.
