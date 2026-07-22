---
name: continuous-improvement
description: Unified continuous improvement review — orchestrates all self-improvement
  mechanisms (skill assessments, health analysis, calibration, recommendations,
  feedback loops) into a periodic review cycle. Two modes — dogfood (internal,
  proposes code/skill/doc changes) and customer (external, proposes config and
  workflow adjustments). Use on a regular cadence (weekly/bi-weekly) or after
  significant pipeline changes.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: pipeline-optimizer
model: haiku
disable-model-invocation: true
---

# Nightgauge Continuous Improvement

## Description

Orchestrates all self-improvement mechanisms into a unified periodic review
cycle. Gathers signals from 8+ data sources, analyzes whether the
pipeline learning system is actually closing (are fixes reducing friction? is
calibration improving accuracy? are health scores trending up?), and generates
prioritized improvement proposals routed by operating mode.

**Two Modes:**

- **Dogfood mode** (`--mode dogfood`): For the Nightgauge team running the
  pipeline against its own codebase. Can propose skill SKILL.md fixes, Go binary
  changes, SDK improvements, documentation updates, and architecture issues.
  Proposals become GitHub issues labeled `continuous-improvement`.
- **Customer mode** (`--mode customer`): For external teams using Nightgauge
  on their codebases. Can only propose configuration adjustments, workflow
  recommendations, and calibration insights. Never modifies source code or
  proposes internal product changes. See
  [docs/SELF_IMPROVEMENT_BOUNDARIES.md](../../docs/SELF_IMPROVEMENT_BOUNDARIES.md).

**Use Cases:**

- Weekly/bi-weekly self-improvement reviews
- Post-sprint retrospective on pipeline effectiveness
- After shipping a batch of skill changes — verify they reduced friction
- When pipeline health scores are declining and you need actionable next steps
- Verifying calibration accuracy is improving over time
- Checking if past recommendations were implemented and effective

**Relationship to Existing Skills:**

| Skill             | Focus                          | This Skill's Value-Add                          |
| ----------------- | ------------------------------ | ----------------------------------------------- |
| `pipeline-health` | 8-dimension health snapshot    | Trends health over time, checks if improving    |
| `pipeline-audit`  | Execution efficiency snapshot  | Correlates efficiency with other signals        |
| `retro`           | Post-failure root cause        | Checks if retro fixes actually reduced failures |
| Self-Assessment   | Per-execution friction capture | Synthesizes patterns across all assessments     |

This skill is the **meta-layer** — it doesn't generate raw analysis (existing
skills do that). It evaluates whether the entire self-improvement system is
working and proposes what to do next.

## Invocation

| Tool        | Command                                                            |
| ----------- | ------------------------------------------------------------------ |
| Claude Code | `/nightgauge:continuous-improvement [options]`                     |
| Copilot     | Invoke via Agent Skills extension                                  |
| Cursor      | Run via Agent Skills or direct SKILL.md                            |
| Standalone  | `claude --skill skills/nightgauge-continuous-improvement/SKILL.md` |

## Arguments

| Argument             | Description                                          | Default     |
| -------------------- | ---------------------------------------------------- | ----------- |
| `--mode MODE`        | Operating mode: `dogfood` or `customer`              | auto-detect |
| `--period N`         | Analyze last N days                                  | `14`        |
| `--since DATE`       | Start date (YYYY-MM-DD)                              | -           |
| `--focus AREA`       | Focus on specific area (see Focus Areas)             | `all`       |
| `--format FORMAT`    | Output format: `summary`, `json`, `both`             | `both`      |
| `--create-issues`    | Auto-create GitHub issues for proposals              | `false`     |
| `--dry-run`          | Preview proposals without creating issues (default)  | `true`      |
| `--compare-baseline` | Compare current period against prior baseline period | `false`     |

### Focus Areas

When `--focus` is specified, the skill narrows analysis to one area:

| Focus             | What It Analyzes                                     |
| ----------------- | ---------------------------------------------------- |
| `all`             | All 6 signal groups (default)                        |
| `skill-health`    | Skill assessments, drift synthesis, effectiveness    |
| `calibration`     | Complexity model accuracy, prediction trends         |
| `pipeline-health` | 8-dimension health scores, trends, cross-references  |
| `recommendations` | Follow-through rate, effectiveness, recurring issues |
| `cost-efficiency` | Token economics, cost trends, model routing          |
| `reliability`     | Failure patterns, MTBF, Ralph Loop effectiveness     |

### Mode Auto-Detection

When `--mode` is omitted, the skill auto-detects by reading
`.nightgauge/config.yaml`:

```
owner == "nightgauge" AND repo == "nightgauge" → dogfood
otherwise → customer
```

### Examples

```bash
# Full review (auto-detects mode)
/nightgauge:continuous-improvement

# Dogfood mode, last 7 days, focus on skill health
/nightgauge:continuous-improvement --mode dogfood --period 7 --focus skill-health

# Customer mode, create issues for proposals
/nightgauge:continuous-improvement --mode customer --create-issues

# Compare current 14-day period against prior 14-day baseline
/nightgauge:continuous-improvement --compare-baseline

# Quick cost review
/nightgauge:continuous-improvement --focus cost-efficiency --period 7
```

### Scheduled / Autonomous Invocation

When invoked by GitHub Actions (`continuous-improvement.yml`), the skill runs
weekly with `--mode dogfood --create-issues`. The workflow respects the
kill-switch from `.nightgauge/config.yaml`:

```yaml
# .nightgauge/config.yaml
autonomous_discovery:
  kill_switch: true # Disables --create-issues even in scheduled runs
```

**Focus Lens Integration**: The skill reads `.nightgauge/focus.yaml` and
applies dimension boosts to proposal ranking. With an active focus lens,
proposals in the focused area are prioritized for issue creation. For example,
with `active_lens: performance`, performance-related proposals are ranked higher
and are more likely to meet the score threshold for auto-creation.

See [docs/SCHEDULED_DISCOVERY.md](../../docs/SCHEDULED_DISCOVERY.md) for full
documentation on the scheduled discovery workflow.

---

## Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

## Execution Flow

<!-- phase-registry: standalone-skill -->

This skill is standalone (not a pipeline execution stage), so its
`stage="continuous-improvement"` emits intentionally do not appear in
`PHASE_REGISTRY`. The annotation above opts the skill out of
`scripts/validate-phase-markers.ts`.

```
Phase 1: Configuration & Mode Detection
Phase 2: Signal Gathering (6 signal groups)
Phase 3: Loop Effectiveness Analysis
Phase 4: Improvement Proposal Generation
Phase 5: Mode-Based Routing & Output
Phase 6: Self-Assessment Epilogue
```

---

## Phase 1: Configuration & Mode Detection

<!-- phase:start name="configuration" index=1 total=6 stage="continuous-improvement" -->

**Goal**: Determine operating mode, analysis period, and available data sources.

### Step 1.1 — Read Configuration

```bash
CONFIG=".nightgauge/config.yaml"
if [ ! -f "$CONFIG" ]; then
  echo "ERROR: .nightgauge/config.yaml not found"
  echo "Run /nightgauge:repo-init first"
  exit 1
fi
```

Extract owner, repo, and self_assessment config:

```bash
OWNER=$(nightgauge config show --key owner --raw 2>/dev/null)
REPO=$(nightgauge config show --key repo --raw 2>/dev/null)
```

### Step 1.2 — Mode Detection

If `--mode` not specified:

```bash
if [ "$OWNER" = "nightgauge" ] && [ "$REPO" = "nightgauge" ]; then
  MODE="dogfood"
else
  MODE="customer"
fi
```

### Step 1.3 — Validate Data Sources

Check which data sources exist. A missing source is not an error — the skill
adapts to available data.

```bash
DATA_SOURCES=""
[ -f ".nightgauge/execution-history.jsonl" ] && DATA_SOURCES="$DATA_SOURCES execution-history"
[ -d ".nightgauge/pipeline/assessments" ] && DATA_SOURCES="$DATA_SOURCES assessments"
[ -f ".nightgauge/calibration.json" ] && DATA_SOURCES="$DATA_SOURCES calibration"
[ -d ".nightgauge/analysis" ] && DATA_SOURCES="$DATA_SOURCES analysis"
[ -f ".nightgauge/gate-metrics.jsonl" ] && DATA_SOURCES="$DATA_SOURCES gate-metrics"
[ -f ".nightgauge/skill-effectiveness.jsonl" ] && DATA_SOURCES="$DATA_SOURCES skill-effectiveness"
[ -d ".nightgauge/health" ] && DATA_SOURCES="$DATA_SOURCES health-trends"
[ -f ".nightgauge/complexity-model.yaml" ] && DATA_SOURCES="$DATA_SOURCES complexity-model"
[ -f ".nightgauge/pipeline/recommendation-history.jsonl" ] && DATA_SOURCES="$DATA_SOURCES recommendations"
```

Report available sources. If fewer than 3 sources exist, warn that analysis will
be limited but continue.

### Step 1.4 — Load Focus Lens

Load the active focus lens from `.nightgauge/focus.yaml`. This is optional
— missing file or parse errors default to `general` (no weighting applied).

```bash
FOCUS_FILE=".nightgauge/focus.yaml"
ACTIVE_LENS="general"
FOCUS_DESCRIPTION="Balanced improvement across all dimensions — no specific bias."

if [ -f "$FOCUS_FILE" ]; then
  PARSED_LENS=$(nightgauge config show --key focus.active_lens --raw 2>/dev/null)
  if [ -n "$PARSED_LENS" ]; then
    ACTIVE_LENS="$PARSED_LENS"
    echo "Focus lens loaded: $ACTIVE_LENS"
  else
    echo "WARNING: focus.yaml found but active_lens not parseable — defaulting to general"
  fi
else
  echo "No focus.yaml found — using general lens (no weighting)"
fi
```

Cache `ACTIVE_LENS` for use throughout Phase 4 proposal generation. Do NOT
re-read `focus.yaml` during Phase 4 — use this cached value to ensure
consistency across the full run.

### Lens Definitions Reference

The following built-in lenses are available (from `internal/focus/focus.go`):

| Lens            | Description                                                             | Keywords                                                                  |
| --------------- | ----------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| `general`       | Balanced improvement across all dimensions — no specific bias           | (none — no weighting applied)                                             |
| `quality`       | Code quality, test coverage, linting, type safety, correctness          | test, coverage, lint, quality, type, strict, validate, correctness        |
| `features`      | New capabilities, tools, integrations, and product value                | feature, capability, tool, integration, new, add, enable                  |
| `security`      | Vulnerability remediation, auth hardening, input validation, compliance | security, vulnerability, auth, permission, secret, encrypt, sanitize, CVE |
| `performance`   | Speed, token efficiency, cost reduction, resource optimization          | performance, speed, token, cost, optimize, cache, reduce, efficient       |
| `documentation` | Docs accuracy, coverage, onboarding, and knowledge management           | documentation, docs, readme, guide, tutorial, onboard, reference          |
| `reliability`   | Error handling, recovery, monitoring, health, and fault tolerance       | reliability, error, recovery, health, monitor, retry, resilient, fault    |
| `ux`            | Developer experience, CLI ergonomics, VSCode UI, onboarding friction    | ux, experience, ergonomic, ui, interface, usability, friction, onboard    |

Keyword matching is **case-insensitive substring matching** — a proposal whose
title or description contains any lens keyword is considered aligned.

<!-- phase:end name="configuration" -->

---

## Phase 2: Signal Gathering

<!-- phase:start name="signal-gathering" index=2 total=6 stage="continuous-improvement" -->

**Goal**: Collect raw data from all self-improvement mechanisms into a unified
analysis dataset.

### Signal Group 1: Skill Friction (INTERNAL)

Read assessment records from `.nightgauge/pipeline/assessments/*.json`.
Parse and validate each against the `AssessmentRecordSchema`. Run
`SkillSelfAssessmentSynthesizer.synthesize()` to get proposals.

**Key metrics to extract:**

- `recordsAnalyzed`: How many skill executions produced friction?
- `totalFrictionItems`: Total friction observations
- `proposals[]`: Recurring patterns (2+ issues)
- `isolatedCount`: One-off friction (noise)
- Friction trend: compare first half vs second half of period

### Signal Group 2: Health Dimensions (SHARED)

Read health trend entries from `.nightgauge/health/trends.jsonl` (last N
entries matching the analysis period).

**Key metrics to extract per dimension:**

- Score trajectory: first half avg vs second half avg
- Direction: improving (>2pt gain), stable, degrading (>2pt loss)
- Worst dimension: lowest average score
- Best dimension: highest average score
- Overall health trend: weighted average trajectory

### Signal Group 3: Calibration Accuracy (SHARED)

Read from `.nightgauge/complexity-model.yaml`:

**Key metrics to extract:**

- `prediction_accuracy.total_predictions` / `.correct_predictions`
- `prediction_accuracy.by_size` — per-bucket accuracy
- `prediction_accuracy.recent_outcomes` — trajectory of recent predictions
- `total_observations` — sample size
- Compare early vs late outcomes in `recent_outcomes` for trend

### Signal Group 4: Recommendation Follow-Through (SHARED)

Read from `.nightgauge/pipeline/recommendation-history.jsonl`:

**Key metrics to extract:**

- Total recommendations generated
- Implemented count (issues closed)
- Pending count (issues still open)
- Not-created count (no issue was made)
- Effectiveness: of implemented recommendations, how many improved the metric?
- Recurring findings: same finding appearing 2+ times despite recommendations

### Signal Group 5: Execution Efficiency (EXTERNAL)

Read from `.nightgauge/execution-history.jsonl` (last N records in period):

**Key metrics to extract:**

- Success rate over period
- Average cost per run (and trend)
- Average duration per run (and trend)
- Retry rate (runs with retries / total runs)
- Model routing efficiency (auto-selection success rate)
- Cache hit rate trend

### Signal Group 6: Feedback & Recovery (INTERNAL)

Read from execution history records that contain feedback signals and Ralph Loop
activations:

**Key metrics to extract:**

- Backtrack count over period (and reasons)
- Model escalation count
- Ralph Loop activation count
- Ralph Loop success rate (fixed vs escalated to human)
- Gate effectiveness (hit rates from `.nightgauge/gate-metrics.jsonl`)
- Skill effectiveness deltas (from `.nightgauge/skill-effectiveness.jsonl`)

<!-- phase:end name="signal-gathering" -->

---

## Phase 3: Loop Effectiveness Analysis

<!-- phase:start name="loop-analysis" index=3 total=6 stage="continuous-improvement" -->

**Goal**: Determine whether each pipeline learning loop is actually closing —
turning observations into improvements.

> **Deterministic path (preferred)**: When the `nightgauge` binary is
> available, call `nightgauge intelligence loop-verdicts` to compute all
> five verdicts and the composite score deterministically. The binary reads the
> same data files described below and applies the same verdict rules.
>
> **Prose fallback**: When the binary is unavailable, apply the manual analysis
> rules in the subsections below.

```bash
WORKDIR=$(pwd)
PERIOD=${PERIOD:-30}
VERDICTS_FILE=$(mktemp /tmp/loop-verdicts-XXXXXX.json)
USE_BINARY_VERDICTS=false

if command -v nightgauge >/dev/null 2>&1; then
  nightgauge intelligence loop-verdicts --workdir "$WORKDIR" --period "$PERIOD" \
    > "$VERDICTS_FILE" 2>/dev/null && USE_BINARY_VERDICTS=true || {
    echo "WARNING: loop-verdicts binary call failed — falling back to manual analysis"
  }
fi

if [ "$USE_BINARY_VERDICTS" = "true" ] && [ -s "$VERDICTS_FILE" ]; then
  COMPOSITE=$(jq -r '.compositeScore' "$VERDICTS_FILE")
  HEALTH_BAND=$(jq -r '.healthBand' "$VERDICTS_FILE")
  echo "Loop effectiveness composite score: $COMPOSITE ($HEALTH_BAND)"
  echo ""
  jq -r '.loops[] | "  \(.loop): \(.verdict) — \(.reason)"' "$VERDICTS_FILE"
  echo ""
  # Export verdict per loop for downstream Phase 4 proposal generation
  SKILL_DRIFT_VERDICT=$(jq -r '.loops[] | select(.loop=="skill-drift") | .verdict' "$VERDICTS_FILE")
  CALIBRATION_VERDICT=$(jq -r '.loops[] | select(.loop=="calibration") | .verdict' "$VERDICTS_FILE")
  HEALTH_MONITORING_VERDICT=$(jq -r '.loops[] | select(.loop=="health-monitoring") | .verdict' "$VERDICTS_FILE")
  COST_OPTIMIZATION_VERDICT=$(jq -r '.loops[] | select(.loop=="cost-optimization") | .verdict' "$VERDICTS_FILE")
  RELIABILITY_VERDICT=$(jq -r '.loops[] | select(.loop=="reliability") | .verdict' "$VERDICTS_FILE")
else
  # --- Prose fallback: manual analysis ---
  echo "Running manual loop analysis (nightgauge binary not available)"
fi
```

### Analysis 3.1 — Skill Drift Loop (fallback)

**Question**: Are skill fixes reducing friction over time?

```
LOOP: Friction detected → Assessment record → Synthesis → GitHub issue →
      Developer fix → Reduced friction in subsequent runs
```

**Metrics**:

- Friction rate: `totalFrictionItems / totalPipelineRuns` over period
- Friction trend: is the rate decreasing period-over-period?
- Fix-to-silence ratio: for proposals that got fixed (issue closed), did the
  friction type stop appearing?
- Open proposals age: how long have unaddressed proposals been open?

**Verdicts**:

| Verdict     | Condition                                         |
| ----------- | ------------------------------------------------- |
| `closing`   | Friction rate decreasing AND fix-to-silence > 50% |
| `stalling`  | Friction rate flat AND open proposals > 3         |
| `degrading` | Friction rate increasing OR fix-to-silence < 25%  |
| `no-data`   | Fewer than 5 assessment records in period         |

### Analysis 3.2 — Calibration Loop (fallback)

**Question**: Are predictions getting more accurate over time?

```
LOOP: Pipeline run → Outcome recorded → Model calibrated →
      Better predictions for next run
```

**Metrics**:

- Overall prediction accuracy (correct / total)
- Recent accuracy (last 10 outcomes) vs historical accuracy
- Per-bucket accuracy (are certain sizes consistently mis-predicted?)
- Sample size adequacy (enough observations per bucket?)

**Verdicts**:

| Verdict         | Condition                                   |
| --------------- | ------------------------------------------- |
| `closing`       | Recent accuracy > historical AND > 60%      |
| `stalling`      | Recent accuracy ≈ historical (within 5%)    |
| `degrading`     | Recent accuracy < historical by > 10%       |
| `bootstrapping` | Total observations < 10 (insufficient data) |

### Analysis 3.3 — Health Monitoring Loop (fallback)

**Question**: Are health findings leading to actual improvements?

```
LOOP: Health analysis → Findings → Recommendations → Issues →
      Fixes → Improved health scores
```

**Metrics**:

- Recommendation follow-through rate (implemented / total)
- Recommendation effectiveness rate (improved metric / implemented)
- Recurring finding rate (same finding 2+ times)
- Overall health score trend

**Verdicts**:

| Verdict     | Condition                                                 |
| ----------- | --------------------------------------------------------- |
| `closing`   | Follow-through > 50% AND effectiveness > 50% AND score up |
| `stalling`  | Recommendations exist but follow-through < 30%            |
| `degrading` | Recurring findings > 3 AND follow-through < 20%           |
| `no-data`   | No recommendation history available                       |

### Analysis 3.4 — Cost Optimization Loop (fallback)

**Question**: Is the pipeline getting more cost-efficient?

```
LOOP: Cost tracked → Anomalies detected → Routing adjusted →
      Lower cost per successful run
```

**Metrics**:

- Cost per successful run (trend)
- Token waste fraction (trend)
- Over-routing rate (expensive models on simple tasks)
- Cache hit rate (trend)

**Verdicts**:

| Verdict     | Condition                                       |
| ----------- | ----------------------------------------------- |
| `closing`   | Cost per success decreasing AND cache improving |
| `stalling`  | Cost flat, no over-routing reduction            |
| `degrading` | Cost increasing OR cache declining              |
| `no-data`   | Fewer than 5 completed runs in period           |

### Analysis 3.5 — Reliability Loop (fallback)

**Question**: Is the pipeline getting more reliable?

```
LOOP: Failures recorded → Patterns detected → Fixes applied →
      Fewer failures, faster recovery
```

**Metrics**:

- Failure rate (trend)
- MTBF hours (trend)
- Ralph Loop success rate
- Retry rate (trend)

**Verdicts**:

| Verdict     | Condition                                         |
| ----------- | ------------------------------------------------- |
| `closing`   | Failure rate decreasing AND MTBF increasing       |
| `stalling`  | Failure rate flat, same failure categories repeat |
| `degrading` | Failure rate increasing OR MTBF decreasing        |
| `no-data`   | Fewer than 5 runs in period                       |

### Composite Verdict

Combine all loop verdicts into an overall self-improvement health score:

| Loop Verdict | Points |
| ------------ | ------ |
| `closing`    | +20    |
| `stalling`   | +5     |
| `degrading`  | -10    |
| `no-data`    | 0      |

**Total**: Sum across 5 loops, normalize to 0-100 scale.

- 80-100: Self-improvement system is highly effective
- 60-79: System is working but some loops need attention
- 40-59: Multiple loops stalling — intervention needed
- 0-39: Self-improvement system is not closing loops — urgent review

<!-- phase:end name="loop-analysis" -->

---

### Run-Reflection (deltas since last run)

Before generating proposals, load the previous review so the output leads with
**movement** (friction rate up/down, health trend, cost trajectory, loop-score
delta) rather than re-reporting steady-state signals. This is intentionally a
prose sub-step (no phase marker) so it does not alter the phase total.

```bash
SKILL_NAME="nightgauge-continuous-improvement"
RUN_LOG=".nightgauge/pipeline/continuous-improvement-runs.jsonl"
```

<!-- include: ../_shared/RUN_REFLECTION.md -->

Set `RUN_COUNTS` (e.g. `{"loop_score":N,"friction":N,"proposals":N}`) and
`RUN_SUMMARY` from this review before the append step; report the delta against
the previous record at the top of the Phase 5 output.

---

## Phase 4: Improvement Proposal Generation

<!-- phase:start name="proposal-generation" index=4 total=6 stage="continuous-improvement" -->

**Goal**: Generate prioritized, actionable improvement proposals based on
analysis results.

### Proposal Categories

Each proposal is categorized by type and priority:

| Category        | Description                                     | Dogfood? | Customer? |
| --------------- | ----------------------------------------------- | -------- | --------- |
| `skill-fix`     | Fix a SKILL.md instruction that causes friction | Yes      | No        |
| `doc-update`    | Update stale/incorrect documentation            | Yes      | No        |
| `code-change`   | Modify Go binary, SDK, or extension code        | Yes      | No        |
| `architecture`  | Structural improvement to system design         | Yes      | No        |
| `config-adjust` | Adjust .nightgauge/config.yaml settings         | Yes      | Yes       |
| `workflow`      | Change how the pipeline is used (not code)      | Yes      | Yes       |
| `calibration`   | Reset or seed calibration data                  | Yes      | Yes       |
| `investigation` | Further analysis needed before action           | Yes      | Yes       |

### Priority Levels

| Priority   | Criteria                                               |
| ---------- | ------------------------------------------------------ |
| `critical` | Loop degrading AND directly impacting run success rate |
| `high`     | Loop degrading OR recurring unaddressed finding        |
| `medium`   | Loop stalling OR metric trending in wrong direction    |
| `low`      | Optimization opportunity, no functional impact         |

### Proposal Generation Rules

#### Focus-Aware Prioritization

When generating proposals, apply weighting based on the `ACTIVE_LENS` cached in
Phase 1. For each proposal, determine alignment by checking whether the
proposal's title or description contains any keyword from the active lens (case-
insensitive substring match). Then apply the weighting rules:

| Condition                    | Priority adjustment                                  |
| ---------------------------- | ---------------------------------------------------- |
| `general` focus (default)    | No adjustment — baseline behavior                    |
| Aligned + degrading loop     | Increase priority one tier (low→medium, medium→high) |
| Aligned + stalling loop      | Keep as-is (medium)                                  |
| Not aligned + stalling loop  | Decrease priority one tier (medium→low)              |
| Not aligned + degrading loop | Keep as-is (don't suppress critical findings)        |

**Hard constraint**: Never reduce a `degrading` loop proposal below `high`
priority, regardless of focus alignment. Safety-critical verdicts (reliability,
security-related findings) always retain at least `high` priority.

#### Per-Loop Generation Rules (with Focus Examples)

**For each degrading loop**, generate at least one proposal:

1. **Skill Drift degrading** →
   - Dogfood: `skill-fix` proposals from synthesis (top 3 by severity)
     - If focus=`quality`: boost quality-related fixes (test improvements, type
       safety, validation instructions) — increase priority one tier
     - If focus=`features`: boost capability-enabling fixes
     - If focus=`documentation`: boost doc-accuracy fixes
   - Customer: `investigation` — "run /nightgauge:retro --skill-feedback"

2. **Calibration degrading** →
   - Both: `calibration` — "reset calibration with --bootstrap" if accuracy < 40%
   - Both: `investigation` — identify which size bucket is most mis-predicted
   - If focus=`quality` or `reliability`: boost calibration proposals (accuracy
     and correctness are focus-aligned)

3. **Health monitoring stalling** →
   - Dogfood: `doc-update` or `code-change` for unimplemented recommendations
   - Customer: `workflow` — "review pending recommendations and close or implement"
   - If focus=`reliability` or `performance`: boost health-monitor proposals

4. **Cost optimization degrading** →
   - Both: `config-adjust` — suggest model routing changes, budget adjustments
   - Dogfood: `code-change` if over-routing detection logic needs improvement
   - If focus=`performance`: boost cost proposals (cost, token, efficiency are aligned)

5. **Reliability degrading** →
   - Both: `config-adjust` — suggest retry budget changes, Ralph Loop tuning
   - Dogfood: `skill-fix` if failure patterns map to specific skill instructions
   - If focus=`reliability` or `security`: boost reliability proposals (always
     receive at minimum `high` priority regardless of alignment)

**For stalling loops**, generate `medium` priority proposals focused on
unblocking the stall. Apply focus weighting: aligned stalling proposals keep
`medium`; unaligned stalling proposals reduce to `low`.

**For closing loops**, generate no proposals unless a `low` priority optimization
is obvious. Focus weighting does not boost closing-loop proposals above `low`.

#### Alignment Determination and Priority Ranking

> **Deterministic path (preferred)**: Write all generated proposals to a temp
> JSON file and call `nightgauge focus rank` to apply lens keyword
> alignment and priority weighting deterministically. The binary uses the same
> keyword sets from `internal/focus/focus.go` — no prose duplication, no drift.
>
> **Prose fallback**: When the binary is unavailable, apply the manual alignment
> check below.

```bash
PROPOSALS_FILE=$(mktemp /tmp/proposals-XXXXXX.json)
# Write proposals array to file (proposals generated in per-loop rules above)
# Each proposal must include "loopVerdict" field with the loop's verdict string
# so the binary can apply the correct priority adjustment rules.
printf '%s' "$PROPOSALS_JSON" > "$PROPOSALS_FILE"

RANKED_FILE=$(mktemp /tmp/proposals-ranked-XXXXXX.json)
if command -v nightgauge >/dev/null 2>&1; then
  nightgauge focus rank \
    --proposals "$PROPOSALS_FILE" \
    --lens "${ACTIVE_LENS:-general}" \
    > "$RANKED_FILE" 2>/dev/null || {
    echo "WARNING: focus rank failed — using unranked proposals"
    cp "$PROPOSALS_FILE" "$RANKED_FILE"
  }
else
  cp "$PROPOSALS_FILE" "$RANKED_FILE"
fi

RANKED_PROPOSALS=$(jq '.proposals' "$RANKED_FILE" 2>/dev/null || cat "$PROPOSALS_FILE")
```

When the binary is unavailable, apply the manual alignment check (prose
fallback) for each proposal before writing it to the output:

```bash
# Prose fallback: alignment check (implement in agent reasoning)
PROPOSAL_TEXT="$title $description"
FOCUS_ALIGNED=false
FOCUS_KEYWORDS_MATCHED=()

# Lens keyword sets (from focus.go BuiltinLenses())
case "$ACTIVE_LENS" in
  quality)      LENS_KEYWORDS="test coverage lint quality type strict validate correctness" ;;
  features)     LENS_KEYWORDS="feature capability tool integration new add enable" ;;
  security)     LENS_KEYWORDS="security vulnerability auth permission secret encrypt sanitize CVE" ;;
  performance)  LENS_KEYWORDS="performance speed token cost optimize cache reduce efficient" ;;
  documentation) LENS_KEYWORDS="documentation docs readme guide tutorial onboard reference" ;;
  reliability)  LENS_KEYWORDS="reliability error recovery health monitor retry resilient fault" ;;
  ux)           LENS_KEYWORDS="ux experience ergonomic ui interface usability friction onboard" ;;
  general|*)    LENS_KEYWORDS="" ;;  # No matching — general = no weighting
esac

for kw in $LENS_KEYWORDS; do
  if echo "$PROPOSAL_TEXT" | grep -qi "$kw"; then
    FOCUS_ALIGNED=true
    FOCUS_KEYWORDS_MATCHED+=("$kw")
  fi
done
```

After determining alignment, apply the priority adjustment from the table above,
then record `FOCUS_ALIGNED` and `FOCUS_KEYWORDS_MATCHED` on the proposal.

### Proposal Schema

```json
{
  "id": "ci-001",
  "category": "skill-fix",
  "priority": "high",
  "loop": "skill-drift",
  "title": "Fix stale build command in feature-dev SKILL.md",
  "description": "The instruction 'npm run build' fails in 3/5 recent runs...",
  "evidence": {
    "frictionCount": 3,
    "affectedIssues": [2100, 2105, 2110],
    "synthesisProposal": "Replace npm run build with npm run compile"
  },
  "suggestedAction": "Edit skills/nightgauge-feature-dev/SKILL.md Phase 3...",
  "mode": "dogfood",
  "estimatedImpact": "Eliminates ~3 workarounds per week",
  "focus_aligned": true,
  "focus_keywords_matched": ["test", "coverage", "validate"]
}
```

The `focus_aligned` field is `false` when `ACTIVE_LENS` is `general` (no
weighting applied). `focus_keywords_matched` is an empty array (`[]`) for
unaligned proposals or when using the general lens.

<!-- phase:end name="proposal-generation" -->

---

## Phase 5: Mode-Based Routing & Output

<!-- phase:start name="output" index=5 total=6 stage="continuous-improvement" -->

**Goal**: Format and deliver proposals appropriate to the operating mode.

### Step 5.1 — Filter Proposals by Mode

```
if mode == "customer":
    proposals = proposals.filter(p => p.mode != "dogfood")
```

Customer mode only sees: `config-adjust`, `workflow`, `calibration`,
`investigation`.

### Step 5.2 — Generate Summary Report

Write a structured summary to the output window. When `ACTIVE_LENS` is not
`general`, include the focus line and alignment stats. When `ACTIVE_LENS` is
`general` (or no focus.yaml), omit the focus lines. Group focus-aligned
proposals before unaligned proposals within the same priority tier.

```
┌─────────────────────────────────────────────────────┐
│  Continuous Improvement Review                       │
│  Period: 2026-03-06 → 2026-03-19 (14 days)          │
│  Mode: dogfood                                       │
│  Active Focus: quality                               │
│  (quality-aligned proposals prioritized)             │
├─────────────────────────────────────────────────────┤
│                                                      │
│  Self-Improvement Score: 72/100 (good)               │
│  Focus-Aligned Proposals: 3/4 (75%)                  │
│                                                      │
│  Loop Status:                                        │
│    Skill Drift:     closing  (+20)                   │
│    Calibration:     stalling (+5)                    │
│    Health Monitor:  closing  (+20)                   │
│    Cost Efficiency: stalling (+5)                    │
│    Reliability:     closing  (+20)                   │
│                                                      │
│  Proposals: 4 (1 high, 2 medium, 1 low)             │
│                                                      │
└─────────────────────────────────────────────────────┘

[HIGH] ★ Calibration accuracy declining for size:L bucket
  → Reset L-bucket calibration: 12 observations, 33% accuracy
  → Action: check if recent L issues had unusual scope
  → Focus-aligned: yes (keywords: coverage, validate)

[MEDIUM] ★ 2 skill drift proposals still unaddressed (14+ days)
  → Review synthesis.json for open proposals
  → Action: /nightgauge:retro --skill-feedback --create-issues

[MEDIUM] Cache hit rate declining (62% → 48% over period)
  → Check if new skills are invalidating cache
  → Action: review prompt structure in recently changed skills

[LOW] Over-routing detected on 3 XS issues (opus used)
  → Action: verify model routing complexity detection for trivial issues
```

`★` marks focus-aligned proposals. When `ACTIVE_LENS` is `general`, omit the
`★` marker, the `Active Focus` header line, and the `Focus-Aligned Proposals`
stat line.

### Step 5.3 — Write JSON Report

Write structured output to
`.nightgauge/pipeline/continuous-improvement-YYYY-MM-DD.json`:

```json
{
  "schema_version": "2",
  "analyzed_at": "2026-03-19T10:00:00Z",
  "mode": "dogfood",
  "active_focus": {
    "lens_name": "quality",
    "description": "Focus on code quality, test coverage, linting, type safety, and correctness.",
    "scoring_boosts": {
      "safety_reliability": 10,
      "pipeline_stage": 5,
      "developer_experience": 5
    }
  },
  "period": { "start": "2026-03-06", "end": "2026-03-19", "days": 14 },
  "data_sources_available": ["execution-history", "assessments", "calibration", "..."],
  "self_improvement_score": 72,
  "loop_verdicts": {
    "skill-drift": { "verdict": "closing", "points": 20, "evidence": {} },
    "calibration": { "verdict": "stalling", "points": 5, "evidence": {} },
    "health-monitor": { "verdict": "closing", "points": 20, "evidence": {} },
    "cost-efficiency": { "verdict": "stalling", "points": 5, "evidence": {} },
    "reliability": { "verdict": "closing", "points": 20, "evidence": {} }
  },
  "proposals": [
    {
      "id": "ci-001",
      "category": "skill-fix",
      "priority": "high",
      "loop": "skill-drift",
      "title": "Fix stale build command in feature-dev SKILL.md",
      "description": "...",
      "suggestedAction": "...",
      "mode": "dogfood",
      "estimatedImpact": "...",
      "focus_aligned": true,
      "focus_keywords_matched": ["test", "coverage", "validate"]
    }
  ],
  "proposals_summary": {
    "total": 4,
    "by_priority": { "critical": 0, "high": 1, "medium": 2, "low": 1 },
    "by_focus_alignment": { "aligned": 3, "unaligned": 1 }
  },
  "metrics_summary": {
    "friction_rate": 0.15,
    "prediction_accuracy": 0.68,
    "health_score_avg": 74,
    "cost_per_success_usd": 1.82,
    "failure_rate": 0.08,
    "recommendation_follow_through": 0.55
  }
}
```

When `ACTIVE_LENS` is `general`, the `active_focus` object is:

```json
"active_focus": {
  "lens_name": "general",
  "description": "Balanced improvement across all dimensions — no specific bias.",
  "scoring_boosts": {}
}
```

And all proposals will have `"focus_aligned": false` and
`"focus_keywords_matched": []`.

**Schema version bump**: Reports generated with focus lens support use
`schema_version: "2"`. This allows downstream consumers to detect whether focus
metadata is present.

### Step 5.4 — Create GitHub Issues (if --create-issues)

Only when `--create-issues` is specified (default is `--dry-run`):

For each proposal with priority `high` or `critical`:

```bash
gh issue create \
  --title "$PROPOSAL_TITLE" \
  --body "$PROPOSAL_BODY" \
  --label continuous-improvement \
  --label "priority:$PRIORITY" \
  --label "type:fix"
```

**Deduplication**: Before creating, check for existing open issues with the
`continuous-improvement` label and matching title prefix. Skip if duplicate
found.

**Customer mode guardrail**: In customer mode, NEVER create issues labeled
`skill-fix`, `doc-update`, `code-change`, or `architecture`. Only create
`config-adjust`, `workflow`, `calibration`, and `investigation` issues.

### Step 5.5 — Retain Previous Reports

Keep the last 10 continuous improvement reports. Delete oldest when limit
exceeded.

```bash
REPORT_DIR=".nightgauge/pipeline"
ls -t "$REPORT_DIR"/continuous-improvement-*.json 2>/dev/null | tail -n +11 | xargs rm -f
```

<!-- phase:end name="output" -->

---

## Phase 6: Self-Assessment Epilogue

<!-- phase:start name="self-assessment" index=6 total=6 stage="continuous-improvement" -->

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Configuration

This skill reads configuration from `.nightgauge/config.yaml`:

| Config Key                       | Default   | Description                           |
| -------------------------------- | --------- | ------------------------------------- |
| `owner`                          | -         | GitHub organization (for mode detect) |
| `repo`                           | -         | Repository name (for mode detect)     |
| `self_assessment.enabled`        | `true`    | Whether skill assessments are active  |
| `self_assessment.action_mode`    | `display` | Assessment action mode                |
| `self_assessment.retention_days` | `90`      | Assessment record retention           |

## Data Sources

| Source                                              | Signal Group         | Required? |
| --------------------------------------------------- | -------------------- | --------- |
| `.nightgauge/pipeline/assessments/*.json`           | Skill Friction       | No        |
| `.nightgauge/health/trends.jsonl`                   | Health Dimensions    | No        |
| `.nightgauge/complexity-model.yaml`                 | Calibration          | No        |
| `.nightgauge/pipeline/recommendation-history.jsonl` | Recommendations      | No        |
| `.nightgauge/execution-history.jsonl`               | Execution Efficiency | No        |
| `.nightgauge/gate-metrics.jsonl`                    | Feedback & Recovery  | No        |
| `.nightgauge/skill-effectiveness.jsonl`             | Feedback & Recovery  | No        |
| `.nightgauge/analysis/latest.json`                  | Health Dimensions    | No        |
| `.nightgauge/calibration.json`                      | Calibration          | No        |

All sources are optional. The skill adapts gracefully — missing sources reduce
the number of analyzable loops but never prevent the skill from running.

## Output Files

| File                                                 | Purpose                    |
| ---------------------------------------------------- | -------------------------- |
| `.nightgauge/pipeline/continuous-improvement-*.json` | Timestamped review reports |

## Error Handling

| Error                           | Recovery                                     |
| ------------------------------- | -------------------------------------------- |
| No config.yaml                  | Fail with "run /nightgauge:repo-init"        |
| No data sources available       | Warn and produce empty report                |
| Individual signal read failure  | Skip that signal group, continue with others |
| Issue creation failure          | Log warning, continue with remaining         |
| JSON parse failure on data file | Skip malformed records, log count            |

## Completion Checklist

- [ ] Configuration read and mode detected
- [ ] Available data sources identified
- [ ] Signal groups gathered (at least 1)
- [ ] Loop effectiveness verdicts computed
- [ ] Proposals generated and prioritized
- [ ] Proposals filtered by mode
- [ ] Summary report displayed
- [ ] JSON report written
- [ ] Issues created (if --create-issues)
- [ ] Report retention enforced

## Dependencies

- `gh` CLI (for `--create-issues` mode)
- Python3 (for JSON/YAML parsing)
- `jq` (for JSONL processing)

## Related Skills

- `/nightgauge:pipeline-health` — Comprehensive health snapshot (this skill
  tracks trends across snapshots)
- `/nightgauge:pipeline-audit` — Quick efficiency check
- `/nightgauge:retro` — Post-failure root cause analysis
- `/nightgauge:health-check` — Codebase quality assessment (separate
  concern)

## Related Documentation

- [docs/SELF_IMPROVEMENT_LOOP.md](../../docs/SELF_IMPROVEMENT_LOOP.md) —
  Pipeline learning system
- [docs/SELF_IMPROVEMENT_BOUNDARIES.md](../../docs/SELF_IMPROVEMENT_BOUNDARIES.md) —
  Internal/external classification rules
- [docs/HEALTH_MONITORING.md](../../docs/HEALTH_MONITORING.md) — 8-dimension
  health analysis
- [docs/OUTCOME_RECORDING.md](../../docs/OUTCOME_RECORDING.md) — Complexity
  calibration
- [docs/SKILL_SELF_ASSESSMENT.md](../../docs/SKILL_SELF_ASSESSMENT.md) — Skill
  friction detection
- [docs/FEEDBACK_LOOPS.md](../../docs/FEEDBACK_LOOPS.md) — In-pipeline signals
- [docs/RALPH_LOOP.md](../../docs/RALPH_LOOP.md) — Self-healing pattern

---

**Author:** nightgauge **License:** Apache-2.0
