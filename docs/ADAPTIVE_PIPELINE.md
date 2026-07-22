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
4. `observeOnly: false` (maximum performance mode forces this to true — warns but
   never kills)

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
