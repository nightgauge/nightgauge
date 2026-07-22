---
name: nightgauge-pipeline-health
description: Comprehensive pipeline health analysis across 7 dimensions — token economics,
  cost health, stage effectiveness, model routing, reliability, self-improvement
  loop health, and pipeline velocity. Cross-references all telemetry data
  sources to generate actionable findings and auto-create improvement issues.
  Use periodically or after a batch of pipeline runs to assess overall pipeline health.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.2.0"
  source: https://github.com/nightgauge/nightgauge
  chainable: true
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: pipeline-optimizer
model: haiku
---

# Nightgauge Pipeline Health

## Description

Comprehensive pipeline health analysis across 7 dimensions. Uses deterministic
Python3/jq for data extraction and metric computation; AI interprets results
into human-readable findings with severity-classified recommendations.

**For a quick point-in-time snapshot, use `/nightgauge:pipeline-audit`
instead.**

**Use Cases:**

- Weekly/bi-weekly pipeline health reviews
- Cost health and token economics analysis
- Identifying reliability patterns and failure modes
- Tracking whether past recommendations improved metrics
- Comparing current period against baseline
- Auto-creating improvement issues for actionable findings

**When to Use:**

- On a regular cadence (weekly recommended) for continuous improvement
- After significant pipeline changes to verify impact
- When pipeline costs or failure rates seem abnormal
- Before planning pipeline optimization sprints
- When you need **comprehensive analysis** — pipeline-health cross-references 7
  data sources and tracks recommendations over time. For a quick efficiency
  check, use `/nightgauge:pipeline-audit`.

**Relationship to Pipeline Audit:**

Pipeline health is complementary to pipeline audit — not a replacement:

| Aspect       | Pipeline Audit              | Pipeline Health                             |
| ------------ | --------------------------- | ------------------------------------------- |
| Scope        | 5 analysis categories       | 7 analysis dimensions                       |
| Data sources | Execution history primarily | Cross-references 7 data sources             |
| Output       | Findings + recommendations  | Findings + auto-issue creation with full AC |
| Tracking     | Point-in-time snapshot      | Recommendation tracking over time           |
| Comparison   | None                        | Baseline period comparison                  |
| Use case     | Quick efficiency check      | Comprehensive health assessment             |

## Invocation

| Tool        | Command                                                     |
| ----------- | ----------------------------------------------------------- |
| Claude Code | `/nightgauge:pipeline-health [options]`                     |
| Copilot     | Invoke via Agent Skills extension                           |
| Cursor      | Run via Agent Skills or direct SKILL.md                     |
| Standalone  | `claude --skill skills/nightgauge-pipeline-health/SKILL.md` |

## Arguments

### Core Options

| Argument                  | Description                                        | Default   |
| ------------------------- | -------------------------------------------------- | --------- |
| `--period N`              | Analyze last N days                                | `7`       |
| `--since DATE`            | Start date (YYYY-MM-DD)                            | -         |
| `--until DATE`            | End date (YYYY-MM-DD)                              | -         |
| `--dimensions DIMS`       | Comma-separated dimensions to analyze              | `all`     |
| `--create-issues`         | Auto-create GitHub issues for findings             | `false`   |
| `--severity LEVEL`        | Min severity for `--create-issues`                 | `high`    |
| `--dry-run`               | Show what issues would be created without creating | `false`   |
| `--compare-to-baseline`   | Compare current vs previous period                 | `false`   |
| `--track-recommendations` | Check past recommendation effectiveness            | `false`   |
| `--format FORMAT`         | Output format: `summary`, `json`, `both`           | `summary` |

### Examples

```bash
# Weekly health check (default: last 7 days, summary format)
/nightgauge:pipeline-health

# Analyze last 30 days
/nightgauge:pipeline-health --period 30

# Analyze specific date range
/nightgauge:pipeline-health --since 2026-02-01 --until 2026-02-14

# Only analyze cost and reliability dimensions
/nightgauge:pipeline-health --dimensions cost-health,reliability

# Preview what issues would be created
/nightgauge:pipeline-health --create-issues --dry-run

# Create issues for medium+ severity findings
/nightgauge:pipeline-health --create-issues --severity medium

# Compare current week against previous week
/nightgauge:pipeline-health --compare-to-baseline

# Track whether past recommendations improved metrics
/nightgauge:pipeline-health --track-recommendations

# JSON output for programmatic consumption
/nightgauge:pipeline-health --format json
```

---

## Prerequisites

- `python3` installed (for reliable JSONL parsing — `cat | jq` drops records)
- `nightgauge` CLI installed and authenticated (`nightgauge forge auth login`)
- `jq` installed for JSON processing (fallback data sources only)
- Git repository with `.nightgauge/` directory
- At least one data source populated (see Data Sources below)

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Data Sources

The skill reads data from 8 sources in priority order, with graceful fallback
when sources are unavailable:

| Source                  | Location                                    | What It Provides                                                                                                                                                                                                          |
| ----------------------- | ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Execution History       | `.nightgauge/pipeline/history/*.jsonl`      | Per-run tokens, costs, durations, stage breakdowns                                                                                                                                                                        |
| History Index           | `.nightgauge/pipeline/history/index.json`   | Lightweight run summaries for fast filtering                                                                                                                                                                              |
| Health Score History    | `.nightgauge/pipeline/health-history.jsonl` | Component health scores and trends over time                                                                                                                                                                              |
| Post-Pipeline Analysis  | `.nightgauge/analysis/*.json`               | Model performance metrics, failure analysis                                                                                                                                                                               |
| A/B Experiments         | `.nightgauge/analysis/experiments/*.jsonl`  | Experiment results and variant comparisons                                                                                                                                                                                |
| Past Health Reports     | `.nightgauge/pipeline/health-report-*.json` | Historical findings for recommendation tracking                                                                                                                                                                           |
| Dimension Trend History | `.nightgauge/health/trends.jsonl`           | Per-dimension time-series (HealthTrendEntry records, 90-day retention). Query with `last N runs` using `limit`, or `last N days` using `startDate`/`endDate`. Each entry contains all 7 dimension scores + overall score. |

Each data source is optional. The skill analyzes whatever is available and notes
which sources were missing in the output report.

### Querying Dimension Trends

Use `HealthTrendsWriter.read()` from the SDK for programmatic access:

```typescript
// Last 20 runs for sparkline display
const entries = await HealthTrendsWriter.read(workspaceRoot, { limit: 20 });

// Last 30 days for trend analysis
const since = new Date();
since.setDate(since.getDate() - 30);
const entries = await HealthTrendsWriter.read(workspaceRoot, {
  startDate: since,
  endDate: new Date(),
});
```

---

## Analysis Dimensions

### 1. Token Economics

Identifies wasteful token usage patterns and optimization opportunities:

- Per-stage token averages and distribution
- Cache hit rate trends (prompt caching effectiveness)
- **Per-stage cache hit rate** (Issue #3804): the canonical
  `cache_read / (cache_read + cache_creation + input)` ratio computed per stage,
  surfaced as `perStageCacheHitRate.<stage>` metrics. Stages with no cacheable
  input report no metric (treated as "no data"), never `0%`. Each stage with
  enough samples whose rate falls below its resolved threshold
  (`pipeline.cache.stage_alert_thresholds.<stage>`, else
  `pipeline.cache.alert_threshold`, default 40%) emits a low-reuse finding —
  `high` severity below 10%, `medium` otherwise. This is the same threshold and
  formula the `nightgauge-pipeline-audit` skill uses, so the two surfaces
  agree by construction.
- Input/output token ratios per stage
- Outlier detection (runs with abnormally high token usage)
- Redundant file reads across stages

### 2. Cost Health

Financial analysis and cost optimization:

- Total cost trends (daily, weekly)
- Per-run cost distribution with outlier detection
- Cost-per-stage breakdown
- Cost by issue size/type/priority correlation
- Projected monthly cost at current rate

### 3. Stage Effectiveness

Timing and reliability analysis per pipeline stage:

- Per-stage duration baselines (median, avg, p90, max)
- First-attempt success rates per stage
- Retry frequency and impact on total duration
- Bottleneck detection (stages >1.5x slower than average)
- Stage skip effectiveness analysis

### 4. Model Routing

AI model selection analysis:

- Model usage distribution across stages
- Cost impact of model selection decisions
- Quality outcomes by model (success rate per model)
- Opportunities for model downgrading on simple tasks
- A/B experiment results for model routing changes

### 5. Reliability & Failure Patterns

Failure analysis and pattern detection:

- Failure rate by stage, issue type, and complexity
- Common failure modes and root causes
- Mean time to recovery (MTTR) per failure type
- Cascading failure detection (one stage failure causing downstream failures)
- Flaky stage detection (intermittent failures)

### 6. Learning Effectiveness

Effectiveness of the pipeline's self-tuning mechanisms:

- Self-tuning action frequency and types
- Impact of self-tuning on subsequent runs
- Recommendation implementation rate
- Closed-loop effectiveness (did changes improve target metrics?)
- Stale recommendations (old findings never addressed)

#### 6.5 Skill-Catalog Usage (telemetry)

The pipeline records outcomes for the 6 core stages, but the other ~37 skills
have no outcome record. The PreToolUse(Skill) hook closes that gap by logging
invocations to `.nightgauge/skills/usage.jsonl`. Read the rollup with:

```bash
nightgauge skills usage --json
```

Surface as findings:

- **Never-triggered skills** (`never_seen: true`) — a skill that never fires is
  usually a `description:` that isn't matching its intended triggers (fix the
  description) or a genuinely dead skill (retire it).
- **Under-triggering vs. expectation** — skills you expect on a cadence
  (backlog-groom, docs-watch) that show stale `last_seen`.
- **Popular skills** — the high-`trigger_count` skills worth investing in
  (better gotchas, verification, scripts).

Feed never-triggered / under-triggering skills into the improvement-issue
generation below, just like any other dimension finding.

### 7. Pipeline Velocity

Throughput and efficiency trends:

- Issues completed per day/week
- Average end-to-end pipeline duration
- Time-in-stage distribution
- Queue wait times (time between stages)
- Velocity trend (improving, stable, degrading)

---

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Parse Arguments and Detect Data Sources

#### Step 1.1: Parse Arguments

Extract options from invocation:

```bash
PERIOD_DAYS=7
SINCE_DATE=""
UNTIL_DATE=""
DIMENSIONS="all"
CREATE_ISSUES=false
MIN_SEVERITY="high"
DRY_RUN=false
COMPARE_BASELINE=false
TRACK_RECOMMENDATIONS=false
OUTPUT_FORMAT="summary"

# Parse arguments from invocation
# --period N: set PERIOD_DAYS
# --since YYYY-MM-DD: set SINCE_DATE
# --until YYYY-MM-DD: set UNTIL_DATE
# --dimensions DIMS: set DIMENSIONS (comma-separated)
# --create-issues: set CREATE_ISSUES=true
# --severity LEVEL: set MIN_SEVERITY
# --dry-run: set DRY_RUN=true
# --compare-to-baseline: set COMPARE_BASELINE=true
# --track-recommendations: set TRACK_RECOMMENDATIONS=true
# --format FORMAT: set OUTPUT_FORMAT
```

#### Step 1.2: Validate Arguments

```bash
# Validate date formats
if [ -n "$SINCE_DATE" ]; then
  if ! echo "$SINCE_DATE" | grep -qE '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'; then
    echo "ERROR: --since must be YYYY-MM-DD format (got: $SINCE_DATE)"
    exit 1
  fi
fi
if [ -n "$UNTIL_DATE" ]; then
  if ! echo "$UNTIL_DATE" | grep -qE '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'; then
    echo "ERROR: --until must be YYYY-MM-DD format (got: $UNTIL_DATE)"
    exit 1
  fi
fi

# Validate dimensions
VALID_DIMS="token-economics,cost-health,stage-effectiveness,model-routing,reliability,self-improvement,pipeline-velocity"
if [ "$DIMENSIONS" != "all" ]; then
  for dim in $(echo "$DIMENSIONS" | tr ',' '\n'); do
    if ! echo "$VALID_DIMS" | grep -q "$dim"; then
      echo "ERROR: Unknown dimension '$dim'. Valid: $VALID_DIMS"
      exit 1
    fi
  done
fi

# Validate --dry-run requires --create-issues
if [ "$DRY_RUN" = true ] && [ "$CREATE_ISSUES" = false ]; then
  echo "WARNING: --dry-run has no effect without --create-issues"
fi
```

#### Step 1.3: Locate Data Sources

Check for each data source with priority ordering and graceful fallbacks:

```bash
SOURCES_FOUND=()
SOURCES_MISSING=()

# Check each data source
HISTORY_DIR=".nightgauge/pipeline/history"
if ls "${HISTORY_DIR}"/*.jsonl 2>/dev/null | head -1 > /dev/null; then
  SOURCES_FOUND+=("execution-history")
else
  SOURCES_MISSING+=("execution-history")
fi

if [ -f "${HISTORY_DIR}/index.json" ]; then
  SOURCES_FOUND+=("history-index")
else
  SOURCES_MISSING+=("history-index")
fi

if [ -f ".nightgauge/pipeline/health-history.jsonl" ]; then
  SOURCES_FOUND+=("health-history")
else
  SOURCES_MISSING+=("health-history")
fi

if ls .nightgauge/analysis/*.json 2>/dev/null | head -1 > /dev/null; then
  SOURCES_FOUND+=("post-pipeline-analysis")
else
  SOURCES_MISSING+=("post-pipeline-analysis")
fi

if ls .nightgauge/analysis/experiments/*.jsonl 2>/dev/null | head -1 > /dev/null; then
  SOURCES_FOUND+=("ab-experiments")
else
  SOURCES_MISSING+=("ab-experiments")
fi

if ls .nightgauge/pipeline/health-report-*.json 2>/dev/null | head -1 > /dev/null; then
  SOURCES_FOUND+=("past-health-reports")
else
  SOURCES_MISSING+=("past-health-reports")
fi

if [ ${#SOURCES_FOUND[@]} -eq 0 ]; then
  echo "WARNING: No pipeline data sources found."
  echo "  Expected: ${HISTORY_DIR}/*.jsonl (primary)"
  echo "  Run some pipeline stages first to generate data."
  exit 0
fi

echo "Data sources found: ${SOURCES_FOUND[*]}"
echo "Data sources missing: ${SOURCES_MISSING[*]}"
```

---

### Phase 2: Data Aggregation (Deterministic)

**IMPORTANT**: Use Python3 for data extraction. Do NOT read raw JSONL into AI
context — extract only aggregated metrics into `/tmp/health_*.json` intermediate
files.

#### Step 2.1: Extract Execution History

When execution history JSONL files are available:

```python
# /tmp/health_extract_history.py — deterministic metric extraction
import json, glob, sys
from collections import defaultdict
from pathlib import Path
from datetime import datetime, timedelta

HISTORY_DIR = ".nightgauge/pipeline/history"
PERIOD_DAYS = int(sys.argv[1]) if len(sys.argv) > 1 else 7
SINCE_DATE = sys.argv[2] if len(sys.argv) > 2 else ""
UNTIL_DATE = sys.argv[3] if len(sys.argv) > 3 else ""

# Compute date range
if not SINCE_DATE:
    SINCE_DATE = (datetime.now() - timedelta(days=PERIOD_DAYS)).strftime("%Y-%m-%d")
if not UNTIL_DATE:
    UNTIL_DATE = datetime.now().strftime("%Y-%m-%d")

# Parse all JSONL records within date range
records = []
for f in sorted(glob.glob(f"{HISTORY_DIR}/*.jsonl")):
    file_date = Path(f).stem
    if file_date < SINCE_DATE or file_date > UNTIL_DATE:
        continue
    with open(f) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                r = json.loads(line)
                if r.get("record_type") == "run":
                    records.append(r)
            except json.JSONDecodeError:
                pass

# Build stage-level metrics
STAGE_NAMES = ["issue-pickup", "feature-planning", "feature-dev",
               "feature-validate", "pr-create", "pr-merge"]
stage_durations = defaultdict(list)
stage_statuses = defaultdict(lambda: defaultdict(int))
stage_tokens = defaultdict(lambda: {"input": [], "output": [],
                                     "cache_read": [], "cache_creation": []})

for r in records:
    for stage in STAGE_NAMES:
        s = r.get("stages", {}).get(stage, {})
        if not s:
            continue
        d = s.get("duration_ms", 0)
        if d > 0:
            stage_durations[stage].append(d)
        stage_statuses[stage][s.get("status", "unknown")] += 1
        ps = r.get("tokens", {}).get("per_stage", {}).get(stage, {})
        for key in ("input", "output", "cache_read", "cache_creation"):
            val = ps.get(key, 0)
            if val > 0:
                stage_tokens[stage][key].append(val)

# Build per-run metrics
run_metrics = []
for r in records:
    t = r.get("tokens", {})
    run_metrics.append({
        "issue_number": r.get("issue_number"),
        "title": r.get("title", ""),
        "outcome": r.get("outcome"),
        "total_duration_ms": r.get("total_duration_ms", 0),
        "started_at": r.get("started_at", ""),
        "total_input": t.get("total_input", 0),
        "total_output": t.get("total_output", 0),
        "total_cache_read": t.get("total_cache_read", 0),
        "total_cache_creation": t.get("total_cache_creation", 0),
        "estimated_cost_usd": t.get("estimated_cost_usd", 0),
        "labels": r.get("labels", []),
        "size": r.get("size"),
        "type": r.get("type"),
        "priority": r.get("priority"),
        "stage_durations": {
            s: r.get("stages", {}).get(s, {}).get("duration_ms", 0)
            for s in STAGE_NAMES
        },
        "stage_statuses": {
            s: r.get("stages", {}).get(s, {}).get("status", "skipped")
            for s in STAGE_NAMES
        },
    })

output = {
    "runs_analyzed": len(records),
    "date_from": SINCE_DATE,
    "date_to": UNTIL_DATE,
    "run_metrics": run_metrics,
    "stage_durations": {k: v for k, v in stage_durations.items()},
    "stage_statuses": {k: dict(v) for k, v in stage_statuses.items()},
    "stage_tokens": {k: {tk: tv for tk, tv in v.items()}
                     for k, v in stage_tokens.items()},
}
with open("/tmp/health_execution.json", "w") as f:
    json.dump(output, f, indent=2)
print(f"Extracted {len(records)} runs to /tmp/health_execution.json")
```

```bash
python3 /tmp/health_extract_history.py ${PERIOD_DAYS} "${SINCE_DATE}" "${UNTIL_DATE}"
```

#### Step 2.2: Extract Health Score History

```bash
# Extract health trend scores using Go binary (replaces Python script)
nightgauge health trends --limit 50 --json > /tmp/health_scores.json 2>/tmp/health_scores_warn.txt
SCORE_COUNT=$(jq 'length' /tmp/health_scores.json 2>/dev/null || echo 0)
echo "Extracted ${SCORE_COUNT} health trend entries to /tmp/health_scores.json"
[ -s /tmp/health_scores_warn.txt ] && cat /tmp/health_scores_warn.txt
```

#### Step 2.3: Extract Post-Pipeline Analysis

```bash
# Aggregate analysis files into single summary
python3 -c "
import json, glob
analyses = []
for f in sorted(glob.glob('.nightgauge/analysis/*.json')):
    try:
        with open(f) as fh:
            analyses.append({'file': f, 'data': json.load(fh)})
    except (json.JSONDecodeError, IOError):
        pass
with open('/tmp/health_analysis.json', 'w') as f:
    json.dump({'records': len(analyses), 'analyses': analyses[-20:]}, f, indent=2)
print(f'Extracted {len(analyses)} analysis files')
"
```

#### Step 2.5: Extract A/B Experiment Data

```bash
python3 -c "
import json, glob
experiments = []
for f in sorted(glob.glob('.nightgauge/analysis/experiments/*.jsonl')):
    try:
        with open(f) as fh:
            for line in fh:
                line = line.strip()
                if line:
                    experiments.append(json.loads(line))
    except (json.JSONDecodeError, IOError):
        pass
with open('/tmp/health_experiments.json', 'w') as f:
    json.dump({'records': len(experiments), 'experiments': experiments[-30:]}, f, indent=2)
print(f'Extracted {len(experiments)} experiment records')
"
```

#### Step 2.6: Extract Gate Metrics (Issue #1412)

```bash
# Extract gate metrics with hit rates using Go binary (replaces jq aggregation)
nightgauge health gate-metrics --json > /tmp/health_gates.json 2>/tmp/health_gates_warn.txt
GATE_COUNT=$(jq 'length' /tmp/health_gates.json 2>/dev/null || echo 0)
if [ "$GATE_COUNT" -eq 0 ]; then
  echo "No gate metrics found — gate hit-rate analysis will be skipped"
else
  echo "Extracted ${GATE_COUNT} gate aggregates to /tmp/health_gates.json"
fi
[ -s /tmp/health_gates_warn.txt ] && cat /tmp/health_gates_warn.txt
```

---

### Phase 3: Analysis per Dimension

Read `/tmp/health_*.json` intermediate files (produced in Phase 2) and compute
metrics per dimension. Only analyze dimensions specified by `--dimensions`.

For each dimension, compute all metrics listed in the Analysis Dimensions
section above. Write per-dimension results to `/tmp/health_dim_*.json`.

**IMPORTANT**: Use `estimated_cost_usd` directly from records — do NOT
recalculate from token counts.

---

### Phase 4: Cross-Reference Analysis

Correlate findings across dimensions to identify root causes:

- Cost spike + model routing change → model selection caused cost increase
- Reliability drop + self-tuning action → self-tuning may have degraded quality
- Velocity decrease + stage duration increase → bottleneck stage identified
- Token increase + new data source → context size growth detected

Write cross-references to `/tmp/health_cross_ref.json`.

---

### Phase 5: Finding Generation (Probabilistic)

AI interprets computed metrics into severity-classified findings:

```json
{
  "dimension": "token-economics | cost-health | stage-effectiveness | model-routing | reliability | self-improvement | pipeline-velocity",
  "severity": "critical | high | medium | low | info",
  "title": "Short description of finding",
  "description": "Detailed explanation with supporting metrics",
  "estimated_impact": "$X.XX/run or X% improvement",
  "recommendation": "Specific actionable step to address this",
  "acceptance_criteria": ["AC1", "AC2"],
  "related_dimensions": ["other-dimension"]
}
```

**Severity Classification Rules:**

| Severity   | Criteria                                                  |
| ---------- | --------------------------------------------------------- |
| `critical` | >50% waste or >30% failure rate                           |
| `high`     | >25% waste or >15% failure rate or clear cost opportunity |
| `medium`   | >10% waste or noticeable inefficiency                     |
| `low`      | Minor optimization opportunity                            |
| `info`     | Informational — positive trend or baseline observation    |

**Gate Hit-Rate Finding Rules (Issue #1412):**

Read `/tmp/health_gates.json` (produced in Step 2.6). For each gate in the
`stage-effectiveness` dimension:

- Gate with `hit_rate > 0.5` (>50% of runs catch defects) → `info`: "High-value
  gate — [gate_name] catches defects in [hit_rate]% of runs"
- Gate with `hit_rate < 0.05` AND `invocations > 10` (<5% catch rate with 10+
  samples) → `low`: "Rarely-catching gate — [gate_name] catches defects in only
  [hit_rate]% of runs; consider repositioning or reviewing necessity"
- No gate data (empty `/tmp/health_gates.json`) → skip gate finding entirely

---

### Phase 6: Recommendation Tracking

Only executes when `--track-recommendations` is passed.

Load past health reports from `.nightgauge/pipeline/health-report-*.json`.
For each past recommendation:

1. Check if a corresponding issue was created and completed
2. Compare the target metric before and after the recommendation period
3. Classify as: implemented-effective, implemented-ineffective, not-implemented,
   partially-implemented

Output recommendation tracking results alongside current findings.

---

### Phase 7: Baseline Comparison

Only executes when `--compare-to-baseline` is passed.

Compare current period against the immediately preceding period of equal length:

- Current period: `--since` to `--until` (or last N days)
- Baseline period: Equal duration immediately before current period

For each dimension, compute delta (absolute and percentage) for key metrics.
Flag significant regressions (>10% worse) and improvements (>10% better).

---

### Phase 8: Issue Creation

Only executes when `--create-issues` is passed and `--dry-run` is false.

#### Step 8.1: Filter by Severity

Only create issues for findings at or above `--severity` threshold.

#### Step 8.2: Check for Existing Issues

Before creating, search for duplicates across both open and recently closed
issues:

```bash
nightgauge forge issue list --repo "$REPO" --search "${FINDING_KEYWORDS}" --state open --json number,title
nightgauge forge issue list --repo "$REPO" --search "${FINDING_KEYWORDS}" --state closed --json number,title --limit 20
nightgauge forge issue list --repo "$REPO" --label "type:epic" --state all --limit 30 --json number,title
```

Skip creation if a matching issue exists.

#### Step 8.3: Create Issues

When creating 3+ findings as issues, create as an epic with sub-issues:

```bash
# Create epic
nightgauge forge issue create \
  --title "epic: Pipeline health findings — ${SUMMARY}" \
  --body "${EPIC_BODY}" \
  --label "type:epic,priority:high,size:L"

# Create sub-issues
nightgauge issue create-sub <EPIC_NUMBER> "<FINDING_TITLE>" "<FINDING_BODY>"

# Add labels separately (issue create-sub doesn't support --labels)
nightgauge forge graphql -f query='mutation($id:ID!,$labels:[ID!]!){addLabelsToLabelable(input:{labelableId:$id,labelIds:$labels}){clientMutationId}}' \
  -f id="$SUB_ISSUE_NODE_ID" -f labels="$LABEL_IDS"

# Sync epic to project board
nightgauge project add <EPIC_NUMBER>
```

For `--dry-run`, output what would be created without actually creating.

For fewer than 3 findings, create standalone issues.

#### Step 8.4: Report Created Issues

Output list of created issues with numbers, titles, and URLs.

---

### Phase 9: Output Report

#### Step 9.1: Write JSON Report

Write structured report to
`.nightgauge/pipeline/health-report-YYYY-MM-DD.json`:

```json
{
  "schema_version": "1.0",
  "analysis_period": {
    "from": "YYYY-MM-DD",
    "to": "YYYY-MM-DD",
    "period_days": 7,
    "data_sources_found": ["execution-history", "health-history"],
    "data_sources_missing": ["ab-experiments"]
  },
  "summary": {
    "total_cost_usd": 45.0,
    "avg_cost_per_run": 3.0,
    "total_runs": 15,
    "success_rate": 0.87,
    "avg_duration_minutes": 45,
    "total_tokens": 5000000,
    "cache_hit_rate": 0.82
  },
  "dimensions": {
    "token-economics": { "status": "analyzed", "metrics": {}, "findings": [] },
    "cost-health": { "status": "analyzed", "metrics": {}, "findings": [] },
    "stage-effectiveness": {
      "status": "analyzed",
      "metrics": {},
      "findings": []
    },
    "model-routing": { "status": "no-data", "metrics": {}, "findings": [] },
    "reliability": { "status": "analyzed", "metrics": {}, "findings": [] },
    "self-improvement": { "status": "no-data", "metrics": {}, "findings": [] },
    "pipeline-velocity": { "status": "analyzed", "metrics": {}, "findings": [] }
  },
  "summary": {
    "total_cost_usd": 45.0,
    "avg_cost_per_run": 3.0,
    "total_runs": 15,
    "success_rate": 0.87,
    "avg_duration_minutes": 45,
    "total_tokens": 5000000,
    "cache_hit_rate": 0.82,
    "gate_effectiveness": {
      "total_invocations": 75,
      "by_gate": [
        {
          "gate_name": "build",
          "invocations": 15,
          "catches": 2,
          "hit_rate": 0.13
        },
        {
          "gate_name": "unit-tests",
          "invocations": 15,
          "catches": 3,
          "hit_rate": 0.2
        },
        {
          "gate_name": "integration-tests",
          "invocations": 15,
          "catches": 1,
          "hit_rate": 0.07
        },
        {
          "gate_name": "type-check",
          "invocations": 15,
          "catches": 4,
          "hit_rate": 0.27
        },
        {
          "gate_name": "lint",
          "invocations": 15,
          "catches": 1,
          "hit_rate": 0.07
        }
      ]
    }
  },
  "findings": [],
  "cross_references": [],
  "recommendations": [],
  "baseline_comparison": null,
  "recommendation_tracking": null,
  "issues_created": [],
  "created_at": "ISO-8601"
}
```

#### Step 9.2: Write Markdown Summary

Write human-readable report to
`.nightgauge/pipeline/health-report-YYYY-MM-DD.md`:

```
PIPELINE HEALTH REPORT
═══════════════════════════════════════════════════════════

Analysis Period: 2026-02-07 to 2026-02-14 (7 days, 15 runs)
Data Sources: execution-history, health-history (5/7 available)

SUMMARY
───────────────────────────────────────────────────────────
  Total Cost:        $45.00 (avg $3.00/run)
  Total Tokens:      5,000,000 (avg 333,333/run)
  Success Rate:      87% (13/15 runs)
  Avg Duration:      45 minutes
  Cache Hit Rate:    82%

DIMENSION HEALTH
───────────────────────────────────────────────────────────
  Token Economics:         ██████████░░ 83%  [HEALTHY]
  Cost Health:             ████████░░░░ 67%  [WARNING]
  Stage Effectiveness:     █████████░░░ 75%  [HEALTHY]
  Model Routing:           ░░░░░░░░░░░░ N/A  [NO DATA]
  Reliability:             ███████████░ 92%  [HEALTHY]
  Self-Improvement:        ░░░░░░░░░░░░ N/A  [NO DATA]
  Pipeline Velocity:       ████████░░░░ 67%  [WARNING]

FINDINGS (6 total: 1 high, 3 medium, 2 info)
───────────────────────────────────────────────────────────

  [HIGH] Cost Health: 3 runs exceeded $5.00 threshold
    → Estimated impact: $4.50/week savings with model routing
    → Recommendation: Route S/XS issues to Sonnet

  ...

GATE HIT-RATES (Issue #1412)
───────────────────────────────────────────────────────────
  Gate               Invocations  Catches  Hit Rate
  ─────────────────  ───────────  ───────  ────────
  type-check                  15        4     27%
  unit-tests                  15        3     20%
  build                       15        2     13%
  integration-tests           15        1      7%
  lint                        15        1      7%

RECOMMENDATIONS (sorted by impact)
───────────────────────────────────────────────────────────
  1. Route simple issues to Sonnet    (~$4.50/week, medium effort)
  2. Optimize feature-validate tests  (~20% stage speedup, low effort)
  3. Enable prompt caching for docs/  (~10% token reduction, low effort)

───────────────────────────────────────────────────────────
Run with --create-issues to auto-create GitHub issues for findings.
Next: /nightgauge:pipeline-health --create-issues --severity medium
```

If `--format json`, write only JSON. If `--format summary`, write only markdown.
If `--format both`, write both files and output the markdown summary.

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Output Format

### JSON Schema

See Phase 9 Step 9.1 for the complete JSON report structure.

### Report Files

| File                                                 | Format   | When Written                          |
| ---------------------------------------------------- | -------- | ------------------------------------- |
| `.nightgauge/pipeline/health-report-YYYY-MM-DD.json` | JSON     | `--format json` or `--format both`    |
| `.nightgauge/pipeline/health-report-YYYY-MM-DD.md`   | Markdown | `--format summary` or `--format both` |

---

## Error Handling

| Condition                                         | Action                                                         |
| ------------------------------------------------- | -------------------------------------------------------------- |
| No data sources found                             | Output friendly message, exit 0                                |
| Malformed JSONL records                           | Skip bad records, report count at end                          |
| python3 not installed                             | Error with install instructions                                |
| forge not authenticated                           | Error with `nightgauge forge auth login` instructions          |
| `--create-issues` without findings                | Output "No findings at severity threshold"                     |
| Insufficient data for dimension                   | Mark dimension as "insufficient-data", skip analysis           |
| `--track-recommendations` with no past reports    | Output "No past reports found for tracking"                    |
| `--compare-to-baseline` with insufficient history | Output "Insufficient history for baseline comparison"          |
| `issue create-sub` label error                    | Use `nightgauge forge graphql` addLabelsToLabelable separately |
| Duplicate finding exists                          | Skip creation, report as already tracked                       |
| `--dry-run` without `--create-issues`             | Warn, continue with analysis only                              |

---

## Pipeline Position

```
UTILITIES (not part of main pipeline)

/nightgauge:pipeline-health
       ↑
  Use on regular cadence (weekly recommended)
  Reads: .nightgauge/pipeline/history/*.jsonl
  Reads: .nightgauge/pipeline/health-history.jsonl
  Reads: .nightgauge/analysis/*.json
  Reads: .nightgauge/analysis/experiments/*.jsonl
  Reads: .nightgauge/pipeline/health-report-*.json
  Writes: .nightgauge/pipeline/health-report-YYYY-MM-DD.json
  Writes: .nightgauge/pipeline/health-report-YYYY-MM-DD.md
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs.

---

## Configuration

This skill reads configuration from `.nightgauge/config.yaml`:

| Config Key                | Default | Description                         |
| ------------------------- | ------- | ----------------------------------- |
| `project.number`          | -       | GitHub Project number for issues    |
| `health.default_period`   | `7`     | Default `--period` value (days)     |
| `health.default_severity` | `high`  | Default severity for issue creation |
| `health.auto_create`      | `false` | Auto-create issues without flag     |

---

**Author:** nightgauge **License:** Apache-2.0
