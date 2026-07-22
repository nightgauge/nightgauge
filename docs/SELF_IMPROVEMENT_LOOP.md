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

**File**: `packages/nightgauge-sdk/src/services/OutcomeRecorder.ts`

Records pipeline outcomes to `.nightgauge/outcomes.jsonl` for complexity
model calibration. Each outcome includes: issue number, size estimate, actual
cost, duration, and success/failure status.

### Complexity Calibration

**File**: `packages/nightgauge-sdk/src/services/CalibrationService.ts`

Builds calibration tables from pipeline outcome history. Maps size buckets (XS,
S, M, L, XL) to observed cost/duration/token distributions, improving future
size estimates.

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
| `.nightgauge/outcomes.jsonl`                            | Outcome records for calibration                              |
| `.nightgauge/analysis/latest.json`                      | Most recent analysis result                                  |
| `.nightgauge/analysis/analysis-*.json`                  | Timestamped analysis history                                 |
| `.nightgauge/gate-metrics.jsonl`                        | Gate invocation records                                      |
| `.nightgauge/skill-effectiveness.jsonl`                 | Skill change effectiveness                                   |
| `.nightgauge/calibration.json`                          | Size estimate calibration                                    |
| `.nightgauge/pipeline/assessments/<stage>-<issue>.json` | Per-stage friction records (written only on friction)        |
| `.nightgauge/pipeline/proposals/retro-epic-<N>.json`    | SkillImprovementProposal records from retro runs             |
| `.nightgauge/pipeline/continuous-improvement-*.json`    | Periodic continuous improvement review reports               |
| `.nightgauge/pipeline/survival-records.jsonl`           | Post-merge survival verdicts feeding #4152/#4153 calibration |
