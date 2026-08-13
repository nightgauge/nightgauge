# Pipeline Learning System

> **Terminology note**: This document was formerly titled 'Self-Improvement
> Loop'. The system is now called the **Pipeline Learning System** to distinguish
> it from product improvement (see
> [SELF_IMPROVEMENT_BOUNDARIES.md](SELF_IMPROVEMENT_BOUNDARIES.md)).

> The pipeline learning system is one component of the broader health monitoring
> system. For the multi-dimensional analysis engine that measures its
> effectiveness, see [docs/HEALTH_MONITORING.md](HEALTH_MONITORING.md). For the
> overall system architecture, see [docs/ARCHITECTURE.md](ARCHITECTURE.md).

## Overview

The pipeline learning system enables the pipeline to learn from observed execution
outcomes. It operates through read-only analysis — all insights are surfaced in
the dashboard for human review, not automatically applied to configuration.

### PostPipelineAnalyzer

**File**: `packages/nightgauge-vscode/src/services/PostPipelineAnalyzer.ts`

Runs after every successful pipeline completion:

1. **Read execution history** from `.nightgauge/execution-history.jsonl`
2. **Model performance analysis** via `ModelPerformanceAnalyzer` — detects
   routing patterns, cost efficiency, model recommendations
3. **Failure pattern detection** via `FailurePatternDetector` — identifies
   recurring failure categories and trends
4. **Cost-per-issue aggregation** — tracks cost across recent issues
5. **Gate effectiveness** — measures quality gate hit rates
6. **Skill effectiveness** — tracks impact of SKILL.md changes on success rates
7. **Calibration table update** — refines complexity estimation from outcomes
8. **Store results** in `.nightgauge/analysis/` with retention

### Outcome Recording

Two distinct systems both called "outcome recording". They write different
files, are owned by different layers, and feed different consumers — conflating
them is how #304 went unnoticed.

**1. Learning / calibration outcome corpus** — Go-owned.

**Writers**: `internal/intelligence/learning.Recorder`, called from
`Scheduler.recordOutcome` (autonomous path) **and** from the Go side of
`pipeline.notifyComplete` in `internal/ipc/server.go` (extension/interactive
path, #304).

**File**: `<targetRepoRoot>/.nightgauge/pipeline/history/outcomes.jsonl` —
**per-repo**, rooted at the run's target repo. That is the same root the run
record the outcome is derived from is written to (#215/#232: a run's persisted
state lands in its target repo, never the daemon's launch root), and the same
root `nightgauge intelligence loop-verdicts --workdir X` / `nightgauge learn
tune --workdir X` read `X`'s run history from — so both of their inputs describe
the same runs.

Neither writer roots the corpus at the IPC server's `workspaceRoot`. That field
is a mutable pointer to the workspace's **active** repo, reassigned by
`workspace.setRoot` from the extension's `resolveActiveRepository` (in a
multi-repo workspace: whichever repo owns the focused text editor), so a corpus
rooted there accumulates other repos' runs and leaves the target repo's corpus
empty.

Each outcome includes: issue number, repo, predicted/actual size,
predicted/actual model, success, duration, tokens, cost, complexity score and
failed stage. Consumers: the calibration, cost-optimization and reliability loop
verdicts (`internal/intelligence/loopverdicts`) and `nightgauge learn tune`.

#### Predicted vs actual — the corpus contract

Both writers derive these four fields through the **same** helpers, in
`internal/orchestrator/outcome_semantics.go`. Read that file before changing any
of them. Three rules hold everywhere:

1. **One vocabulary per pair.** Sizes are `small｜medium｜large`; models are
   registry bands (`haiku｜sonnet｜opus｜fable`) **and nothing else** — a model
   reference the registry has no band for records `""` and is excluded, never
   the id verbatim. A pair written in two vocabularies reports a _measured_ 0%
   forever — worse than no data, because the reader stops saying "bootstrapping"
   and starts asserting a number that can never move. (Attribution of what
   actually ran is not lost: the run record's per-stage `model_selection` keeps
   the concrete id.)
2. **Absent means empty.** Unknown is `""`, never a plausible default. Every
   consumer counts a row toward an accuracy only when **both** halves of that
   pair are non-empty, so an absent value is excluded rather than booked as a
   miss — and a fabricated one would be counted as a measurement of nothing.
3. **An `actual` is a measurement.** It must be something the run produced,
   never a second reading of the same pre-run inputs the prediction came from.

The model pair has one more rule, because the two halves are written in
different places: the run is JUDGED for divergence in the concrete-id space
(what the adapter actually launched) and RECORDED in the band space (what the
prediction is written in). A non-Claude adapter translates a band into a
multi-band id — `opus` → `gpt-5.6-sol`, which also serves `fable` — so
collapsing the served id onto its strongest band would book every
codex/gemini/copilot run as a routing miss. `OutcomeActualBand` inverts the
mapping against the prediction instead. See
[OUTCOME_RECORDING.md](OUTCOME_RECORDING.md) for the worked table.

| Field            | Meaning                                                                                                                                                | Absent when                                                                    |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------ |
| `predictedSize`  | `SizeBucketForScore(routing.complexity_score)`                                                                                                         | the issue carried no size input (see below), or is unscored                    |
| `actualSize`     | how big the change turned out to be, bucketed from lines **changed** captured at `pr-create` exit                                                      | the run never reached `pr-create`, or the pre-merge diff could not be measured |
| `predictedModel` | `routing.pickup_recommendation.dev_model` as a registry band (`OutcomeModelBand`)                                                                      | the router made no recommendation, or the recommendation has no registry band  |
| `actualModel`    | the band the **`feature-dev`** stage served — the adapter mapping inverted against the prediction (`OutcomeActualBand`), not a strongest-band collapse | that stage never ran, reported no model, or served an id with no registry band |

**What counts as a size input, and why it is one rule.** Presence follows the
**router's own** resolution order — project board **Size** field, then a `size:*`
label, then absent (`routing.resolveSize`) — because that is the term
`complexity_score` was computed from. Both writers call one function with the raw
inputs (`orchestrator.OutcomeSizeInput`), so neither can key on a different
source than the other. Round 3 shared the helper but not its argument: the
scheduler passed the board field and the extension passed the label, so one issue
with board `Size=L` and no label recorded `medium` on one path and `""` on the
other — one corpus field, two presence rules, no discriminator.

**Known input gap on the extension path.** The RULE is shared; the INPUTS are
not symmetric. `issue-{N}.json` carries `labels` and `routing`, never the board
`Size` field, so the extension writer's board term is always empty and a
board-sized, unlabelled issue records no size prediction there. That is an input
the extension path does not receive, not a second definition. The both-halves
denominator guard keeps such a row out of size accuracy even though it now
carries `actualSize`; closing the input gap would increase coverage without
changing the meaning of any measured pair.

**Why `predictedSize` needs a size input at all.** Complexity scores are clamped
to `[1,8]` and default to the `M` base score for an issue with no size term, so
`score==0` essentially never occurs in the field — a guard keyed on it is dead
code, and ~95% of real issues (measured on this repo's own history) would record
a fabricated `"small"` through it. Absence is derived from the missing input.

**Why `actualSize` is captured before merge.** The non-circular definition is
`github.OutcomeService.getActualSizeBucket`: bucket insertions+deletions actually
changed against the PR base. A diff computed at terminal time is invalid because
successful `pr-merge` runs have already landed and would appear to be ~0 lines.
Both dispatch paths therefore capture the diff at `pr-create` exit, persist the
raw count on `RuntimeState`, and project its `SizeBucketForScore`-compatible
`small|medium|large` bucket into both the V2 run record and learning corpus.
Pointer semantics preserve a measured zero while runs that never reach
`pr-create` stay absent.

**Operational consequence:** `nightgauge learn tune` tunes `size_accuracy` when
the corpus contains at least one row with both halves. A legacy-only corpus, or a
period containing only pre-`pr-create` failures / rows without a size prediction,
still reports `skipped` and writes no `tuning-audit.jsonl` entry. The calibration
loop likewise reports `sizeCalibration: no-data` only when its selected period
contains zero measured size pairs.

What must never come back is bucketing the issue's own size term for the actual
half: that is one of the same inputs `complexity_score` is computed from
(`fib_round(SIZE_MAP[size] × PRIORITY_MULT[priority])`), so the comparison
measures the arithmetic and produces permanent structural misses — `size:M` +
`priority:critical` scores 5, i.e. predicted `medium` against an "actual" of
`small`, for a run the router sized exactly right.

**Why `actualModel` is the `feature-dev` model.** `predictedModel` is the
router's recommendation _for the implementation stage_, so the measured half has
to be what that stage served, or the two halves are about different things. It is
deliberately **not** the model of whichever stage dominated the run's cost: on
this machine's real history the dominant-cost stage is `feature-dev` in well
under half of runs, so a run that died in `issue-pickup` on opus booked a routing
miss against a dev-stage prediction for a stage that never ran — and no routing
improvement could move it. It is also never a **copy** of `predictedModel`, which
is what `Scheduler.recordOutcome` used to write: that made every autonomous row a
tautological routing hit, and mixed with the extension path's real measurement in
one undiscriminated file. The CLI's `model_refusal_fallback` served model (#91)
still wins when the swap happened **in `feature-dev`** — that is a real
measurement of what produced the code.

Two terminal states deliberately record **nothing**, on both paths: a
blocked-dependency deferral (#305 — a non-failure that did no work) and a
`network_unavailable` failure (#3296 — environmental noise, not model signal).
The two writers key the deferral on different fields, because #305's
extension-path override clears the terminal kind: `Scheduler.runPipeline` tests
`terminal_failure_kind == blocked_dependency`, and the `notifyComplete` handler
tests `record.outcome_type == deferred`. Same two states, and both are covered
by tests on their own path. Leaving the deferral in on one side is not cosmetic:
a deferral is a failed run at ~$0 that ran no AI stage, so five of them in the
recent half of a 20-run corpus flip cost-optimization to `closing` ("cost per
run decreasing") and reliability to `degrading` ("failure rate increasing") —
credit for savings and blame for failures from runs that never executed.

#### Guarded denominators, and what mixed history does

`learning.Recorder.Calibrate` reports `sizeSamples` / `modelSamples` alongside
`sizeAccuracy` / `modelAccuracy`, and the accuracies are **`null` when their
sample count is zero** — "nothing measurable" is a different finding from
"measured, and wrong every time", and `nightgauge learn tune` now declines to
tune an unmeasurable target instead of optimizing toward its goal from a
substituted `0.0`. The calibration loop verdict likewise counts only measurable
pairs, reports `measuredPredictions` and the per-pair split
(`sizePairsMeasured` / `modelPairsMeasured`), and returns `no-data` when there
are none.

**The trend window is measured in comparisons, not rows.** `recentAccuracy` is
computed over the newest **10 measurable comparisons**, capped at half the
period's comparisons; `historicalAccuracy` is computed over all of them. Both
halves of that rule matter. Counting rows made "recent" a window in which two
things were measured out of ten, and letting the window expand until it found 10
measurements made it swallow the whole corpus on any period with ≤ ~20 of them —
`recentAccuracy` then equals `historicalAccuracy` by construction, only the
`stalling` branch is reachable, and the loop banks +5 composite points forever
for a verdict with no information in it. With the cap, a router that regressed
from perfect to useless over a 20-run period reads `degrading`, and one that
recovered reads `closing`.

**Mixed old/new history: what the guard does and does not cover.** Rows this
repo's writers produced before the contract carry `predictedModel: ""`,
`actualModel: ""`, `predictedSize: "small"` (fabricated from an unscored run)
and no `actualSize`, so the both-halves-present guard excludes them from every
accuracy while still counting them as runs for cost, success rate and totals. On
this repo's real eight-row legacy corpus that is the difference between
`modelAccuracy 1.0` (eight `"" == ""` hits) and `modelAccuracy: null` over
`modelSamples: 0`.

That exclusion is **not** structural. It holds for legacy rows whose
`predictedModel` was empty — which was every row here, because the old scheduler
could not find `issue-{N}.json` at the canonical root (stages write it into the
worktree). A deployment where the old scheduler COULD read the context wrote
`predictedModel = dev_model-or-a-fabricated-"sonnet"` and `actualModel :=
predictedModel` — both non-empty and equal, in raw model-id vocabulary
(`claude-sonnet-4-6`) that is not comparable with the band vocabulary. Those rows
pass the guard and score as tautological hits, inflating `modelAccuracy` toward
100%. If you are reading a corpus that predates this contract on a machine where
context files resolved at the run root, treat its `modelAccuracy` as unreliable
and re-baseline from the first row written after the upgrade.

**2. Complexity-model calibration** — TypeScript/SDK-owned.

**File**: `packages/nightgauge-sdk/src/services/OutcomeRecorder.ts`, driven by
the extension's `PostPipelineAnalyzer`, writing
`.nightgauge/complexity-model.yaml`. Success-only: it keys off a completed run
record with a resolvable PR. See [OUTCOME_RECORDING.md](OUTCOME_RECORDING.md).

### Complexity Calibration

**File**: `packages/nightgauge-sdk/src/services/CalibrationService.ts`

Builds calibration tables from pipeline outcome history. Maps size buckets (XS,
S, M, L, XL) to observed cost/duration/token distributions, improving future
size estimates.

### Per-(Stage, Model) Cost Calibration (Issue #142)

**File**: `packages/nightgauge-sdk/src/services/StageModelCalibrationService.ts`

The `(mode, size)` calibration above corrects the whole-run cost estimate but
does not distinguish _which stage_ drove the actual cost — `AutoModelSelector`
used to rescale every stage's static baseline by a single whole-run scale
factor, which preserved the (often wrong) relative shape of `TOKEN_BASELINES`
and produced near-zero rank correlation between estimated and actual per-stage
cost. `StageModelCalibrationService` buckets observed cost and token usage by
`(stage, model)` instead — e.g. `(feature-dev, sonnet)`, `(pr-create, haiku)` —
from each completed run's `tokens.per_stage` history, mirroring
`CalibrationService`'s percentile math and atomic-write pattern.

`AutoModelSelector.estimatePipelineCost()` looks up the exact `(stage,
selected-model)` cell for every non-skipped stage and, once a cell has ≥5
samples, uses its observed p75 cost in place of the static
`TOKEN_BASELINES` figure for that stage only — no cross-model fallback (a
different model's cost distribution is not a meaningful default) and no
special-casing for high-variance cells (e.g. `feature-dev` at mid-tier models):
each cell reports its own honest p75, and the existing budget-ceiling gate
(`budgetIntelligence.ts`) continues to own tail-risk enforcement. Stages
without enough history still fall back to `TOKEN_BASELINES`, so the estimate
improves stage-by-stage as history accumulates rather than waiting for every
stage to calibrate at once.

The estimator also now selects each stage's model against the run's actual
performance-mode envelope (`modeProfiles.toModelEnvelope()`) rather than
always defaulting to the Elevated envelope, so the estimated tier matches the
tier the run will actually serve.

`PostPipelineAnalyzer` rebuilds and persists
`.nightgauge/pipeline/stage-model-calibration.json` after every completed run,
parallel to the existing `(mode, size)` calibration table update.

### Survival Calibration (Issues #4152/#4153)

**Files**: `internal/github/outcome_survival.go` (Go),
`packages/nightgauge-sdk/src/services/OutcomeRecorder.ts`'s
`applySurvivalVerdicts` (TS)

The outcome recording above calibrates on a merge-time proxy (predicted vs.
actual size); it has no way to know whether merged code actually **held up**
afterward. Spike #4134's post-merge survival outcome model
(`internal/intelligence/survival/`, #4151) closes that loop: it captures a
`pending` record at merge (keyed by merge commit SHA) and finalizes it —
`reverted`, `broke`, `survived`, or `unobserved` — on the autonomous
reconcile sweep once the observation window elapses.

`ApplySurvivalVerdicts` / `applySurvivalVerdicts` feed those **finalized**
verdicts into a bias-safe, asymmetric calibration rule (see
[docs/OUTCOME_RECORDING.md#survival-calibration-issues-41524153](OUTCOME_RECORDING.md#survival-calibration-issues-41524153)
for the full mechanics): reverted/broke apply an immediate confidence
penalty once ≥5 negative observations exist; a weak reward for `survived`
only starts once ≥5 **finalized** survived observations exist, and is
deliberately smaller than the penalty. `pending`/`unobserved` never move
calibration — this is the same "penalize proven harm, never reward unproven
survival" rule the rest of this doc's read-only philosophy is built on,
applied to real ground truth instead of a proxy.

## Skill Self-Assessment Epilogues

Every pipeline skill now includes a final phase — the **self-assessment
epilogue** — that evaluates whether the skill's instructions matched execution
reality. This happens after the stage's primary work is complete.

### How It Works

- The agent reflects on any friction encountered during the stage: instructions
  that were ambiguous, steps that required workarounds, or assumptions that
  turned out to be incorrect.
- **If friction is detected**, the agent writes a JSON assessment record to:
  `.nightgauge/pipeline/assessments/<stage>-<issue>.json`
- **If everything worked as written**, nothing is written. Silence indicates
  health — the absence of an assessment file is itself a signal.

### Assessment Record Schema

```json
{
  "stage": "feature-dev",
  "issue": 1234,
  "timestamp": "2026-03-16T10:00:00Z",
  "frictionPoints": [
    {
      "instruction": "Run `npm run build`",
      "reality": "Build script not present; used `npm run compile` instead",
      "severity": "minor" | "moderate" | "blocking"
    }
  ],
  "suggestedAmendments": ["Update build command to check for compile fallback"]
}
```

Assessment records are the raw input to the post-epic synthesis phase (see
below). They are not acted on automatically — they accumulate until a retro run
synthesizes them into improvement proposals.

## Base Branch Freshness Check

**Defined in**: `skills/_shared/FRESHNESS_CHECK.md`

Two pipeline stages now proactively rebase onto the latest base branch before
performing their primary work:

| Stage              | Phase | When                           |
| ------------------ | ----- | ------------------------------ |
| `feature-validate` | 1.4   | Before running build and tests |
| `pr-merge`         | 5.5   | Before merging the PR          |

### Why

When multiple sub-issues of the same epic are processed concurrently, a
worktree created from an earlier base commit can diverge from `main` while the
issue is in-flight. Without a freshness check, `feature-validate` builds against
stale code and `pr-merge` creates unnecessary merge conflicts.

### Behavior

```
1. git fetch origin <base-branch>
2. Check if HEAD is behind origin/<base-branch>
3. If behind → git rebase origin/<base-branch>
   - On rebase conflict → abort, surface conflict details, fail the stage
   - On clean rebase → continue to next phase
4. If up to date → no-op, continue immediately
```

A rebase conflict is a hard stop. The stage fails with a structured error that
includes the conflicting files so the human operator can resolve before
retrying.

## Post-Epic Synthesis

**Invoked via**: `/nightgauge:retro --epic N`

When all sub-issues of an epic are complete, the retro skill aggregates the
assessment records written during the epic's lifetime and identifies patterns
that recurred across multiple issues.

### Synthesis Process

1. **Collect** all `.nightgauge/pipeline/assessments/*-<issue>.json` files
   for sub-issues belonging to epic N.
2. **Group** friction points by the instruction they reference (normalized to
   the skill file + phase header).
3. **Threshold**: any friction pattern appearing in **2 or more** sub-issues is
   flagged as a recurring pattern.
4. **Generate** a `SkillImprovementProposal` record for each recurring pattern:

```json
{
  "skill": "feature-dev",
  "phase": "Phase 3 — Implementation",
  "pattern": "Build command mismatch",
  "occurrences": 4,
  "affectedIssues": [1230, 1232, 1235, 1238],
  "proposedAmendment": "...",
  "severity": "moderate"
}
```

5. **Optionally create GitHub issues** for each proposal, labeled `skill-drift`,
   in the `nightgauge/nightgauge` repository. The operator confirms before
   issues are created — the retro skill prompts for approval.

### SkillImprovementProposal Storage

Proposals are written to:
`.nightgauge/pipeline/proposals/retro-epic-<N>.json`

They are also surfaced in the VSCode dashboard under a future "Skill Health" tab
(tracked separately).

## What Was Removed

The auto-tune layer that automatically wrote configuration changes was removed
**from the extension runtime**. See
[docs/ADAPTIVE_PIPELINE.md](ADAPTIVE_PIPELINE.md) for details on what was
removed and why.

Previously, PostPipelineAnalyzer would:

- Apply threshold adjustments to `config.yaml` via `AdaptivePolicyEngine`
- Trigger rollbacks via `AutoRollbackEngine` when health degraded
- Evaluate A/B experiments via `ExperimentEvaluator`
- Log all changes to `self-tuning-log.jsonl` via `SelfTuningLogger`

These runtime integrations were removed from the extension in favor of
display-only analysis that surfaces recommendations without automatically acting
on them. The underlying SDK classes (`AdaptivePolicyEngine`,
`AutoRollbackEngine`, `ExperimentEvaluator`) are retained as library exports in
`packages/nightgauge-sdk/src/` with full test suites, available for offline
analysis and platform use.

## Data Flow

```
Pipeline Stage Complete (per issue)
        │
        ├─ Skill Self-Assessment Epilogue
        │       │
        │       ├─ Friction detected? → write assessments/<stage>-<issue>.json
        │       └─ No friction?       → no-op (silence = health)
        │
        ▼
PostPipelineAnalyzer.analyze()
        │
        ├─ ModelPerformanceAnalyzer   → routing recommendations (display-only)
        ├─ FailurePatternDetector     → failure patterns (display-only)
        ├─ Cost aggregation           → cost-per-issue data
        ├─ Gate effectiveness         → gate hit rates
        ├─ Skill effectiveness        → SKILL.md change impact
        ├─ CalibrationService         → updated calibration table
        │
        ▼
Store in .nightgauge/analysis/
        │
        ▼
Dashboard displays results

Epic Complete → /nightgauge:retro --epic N
        │
        ▼
Collect assessments/<stage>-<issue>.json for all sub-issues
        │
        ▼
Group & threshold (2+ occurrences = recurring pattern)
        │
        ▼
Generate SkillImprovementProposal records
        │
        ├─ Write proposals/retro-epic-<N>.json
        │
        └─ (Optional, human-approved) Create GitHub issues labeled `skill-drift`

Periodic Review → /nightgauge:continuous-improvement
        │
        ▼
Gather signals from ALL sources above
        │
        ▼
Analyze 5 pipeline learning loops:
  ├─ Skill Drift (friction → fix → silence?)
  ├─ Calibration (outcomes → predictions → accuracy?)
  ├─ Health Monitor (findings → recommendations → improvement?)
  ├─ Cost Efficiency (tracking → routing → savings?)
  └─ Reliability (failures → patterns → fewer failures?)
        │
        ▼
Generate prioritized improvement proposals
        │
        ├─ Dogfood mode: skill-fix, doc-update, code-change, architecture
        └─ Customer mode: config-adjust, workflow, calibration, investigation
```

## File Locations

| File                                                    | Purpose                                                      |
| ------------------------------------------------------- | ------------------------------------------------------------ |
| `.nightgauge/execution-history.jsonl`                   | Pipeline execution records                                   |
| `.nightgauge/pipeline/history/outcomes.jsonl`           | Learning outcome corpus (per target repo, both exec paths)   |
| `.nightgauge/analysis/latest.json`                      | Most recent analysis result                                  |
| `.nightgauge/analysis/analysis-*.json`                  | Timestamped analysis history                                 |
| `.nightgauge/gate-metrics.jsonl`                        | Gate invocation records                                      |
| `.nightgauge/skill-effectiveness.jsonl`                 | Skill change effectiveness                                   |
| `.nightgauge/calibration.json`                          | Size estimate calibration                                    |
| `.nightgauge/pipeline/stage-model-calibration.json`     | Per-(stage, model) cost calibration (#142)                   |
| `.nightgauge/pipeline/assessments/<stage>-<issue>.json` | Per-stage friction records (written only on friction)        |
| `.nightgauge/pipeline/proposals/retro-epic-<N>.json`    | SkillImprovementProposal records from retro runs             |
| `.nightgauge/pipeline/continuous-improvement-*.json`    | Periodic continuous improvement review reports               |
| `.nightgauge/pipeline/survival-records.jsonl`           | Post-merge survival verdicts feeding #4152/#4153 calibration |
