---
name: nightgauge-pipeline-audit
description: Analyze pipeline execution history for efficiency insights - compute token
  usage, stage performance, cost optimization, quality correlation, and trend
  metrics. Use anytime to identify improvement opportunities.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.4.0"
  source: https://github.com/nightgauge/nightgauge
  chainable: true
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: pipeline-optimizer
model: haiku
---

# Nightgauge Pipeline Audit

## Description

Scrubs pipeline execution history, computes efficiency metrics across 8
categories, and generates actionable improvement recommendations. Uses
deterministic Bash/jq for data extraction and metric computation; AI interprets
results into human-readable findings.

**For comprehensive cross-referenced analysis with recommendation tracking, use
`/nightgauge:pipeline-health` instead.**

**Use Cases:**

- Post-sprint efficiency reviews
- Cost optimization analysis
- Identifying bottleneck stages
- Tracking pipeline improvement over time
- Creating issues for recurring problems

**When to Use:**

- After completing several pipeline runs to identify patterns
- When pipeline costs seem higher than expected
- Before planning pipeline optimization work
- Periodically (weekly/bi-weekly) for trend monitoring
- When you need a **quick snapshot** — pipeline-audit runs fast against a single
  data source. For deep multi-source analysis with historical tracking, use
  `/nightgauge:pipeline-health`.

## Invocation

| Tool        | Command                                                    |
| ----------- | ---------------------------------------------------------- |
| Claude Code | `/nightgauge:pipeline-audit [options]`                     |
| Copilot     | Invoke via Agent Skills extension                          |
| Cursor      | Run via Agent Skills or direct SKILL.md                    |
| Standalone  | `claude --skill skills/nightgauge-pipeline-audit/SKILL.md` |

## Arguments

### Core Options

| Argument           | Description                                    | Default |
| ------------------ | ---------------------------------------------- | ------- |
| `--runs N`         | Analyze last N pipeline runs                   | `10`    |
| `--since DATE`     | Analyze runs since date (YYYY-MM-DD)           | -       |
| `--issue N`        | Analyze runs for specific issue number         | -       |
| `--create-issues`  | Auto-create GitHub issues for high findings    | `false` |
| `--severity LEVEL` | Minimum severity for `--create-issues`         | `high`  |
| `--format FORMAT`  | Output format: `summary`, `json`, `both`       | `both`  |
| `--compare DATE`   | Compare metrics before/after date (YYYY-MM-DD) | -       |

### Examples

```bash
# Analyze last 10 runs (default)
/nightgauge:pipeline-audit

# Analyze last 50 runs
/nightgauge:pipeline-audit --runs 50

# Analyze since a specific date
/nightgauge:pipeline-audit --since 2026-02-01

# Analyze a specific issue's pipeline runs
/nightgauge:pipeline-audit --issue 628

# Generate improvement issues for high-severity findings
/nightgauge:pipeline-audit --create-issues

# Create issues for medium+ severity findings
/nightgauge:pipeline-audit --create-issues --severity medium

# JSON output only (for programmatic consumption)
/nightgauge:pipeline-audit --format json

# Compare metrics before/after an optimization
/nightgauge:pipeline-audit --compare 2026-03-01
```

---

## Prerequisites

- `python3` installed (for reliable JSONL parsing — `cat | jq` drops records)
- `nightgauge` CLI installed and authenticated (`nightgauge forge auth login`)
- `jq` installed for JSON processing (fallback data sources only)
- Git repository with `.nightgauge/` directory
- Pipeline history in `.nightgauge/pipeline/history/*.jsonl` (preferred) or
  `state.json` files as fallback

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Parse Arguments and Locate Data

#### Step 1.1: Parse Arguments

Extract options from invocation:

```bash
RUNS_LIMIT=10
SINCE_DATE=""
ISSUE_FILTER=""
CREATE_ISSUES=false
MIN_SEVERITY="high"
OUTPUT_FORMAT="both"
COMPARE_DATE=""

# Parse arguments from invocation
# --runs N: set RUNS_LIMIT
# --since YYYY-MM-DD: set SINCE_DATE
# --issue N: set ISSUE_FILTER
# --create-issues: set CREATE_ISSUES=true
# --severity LEVEL: set MIN_SEVERITY
# --format FORMAT: set OUTPUT_FORMAT
# --compare YYYY-MM-DD: set COMPARE_DATE
```

#### Step 1.2: Locate Data Sources

Check for execution history in priority order:

```bash
HISTORY_DIR=".nightgauge/pipeline/history"
STATE_FILE=".nightgauge/pipeline/state.json"
PIPELINE_DIR=".nightgauge/pipeline"

DATA_SOURCE="none"

# Priority 1: JSONL history files (from execution history persistence)
if ls "${HISTORY_DIR}"/*.jsonl 2>/dev/null | head -1 > /dev/null; then
  DATA_SOURCE="history"
  JSONL_FILES=$(ls -1 "${HISTORY_DIR}"/*.jsonl | sort -r)
  echo "Found execution history JSONL files"
fi

# Priority 2: Pipeline state.json
if [ "$DATA_SOURCE" = "none" ] && [ -f "$STATE_FILE" ]; then
  DATA_SOURCE="state"
  echo "No JSONL history found. Falling back to state.json"
fi

# Priority 3: Pipeline context files (dev-*.json, planning-*.json, etc.)
if [ "$DATA_SOURCE" = "none" ]; then
  CONTEXT_FILES=$(ls -1 "${PIPELINE_DIR}"/dev-*.json 2>/dev/null)
  if [ -n "$CONTEXT_FILES" ]; then
    DATA_SOURCE="context"
    echo "No history or state.json. Using pipeline context files"
  fi
fi

# No data available
if [ "$DATA_SOURCE" = "none" ]; then
  echo "WARNING: No pipeline execution data found."
  echo "  Expected: ${HISTORY_DIR}/*.jsonl (from execution history)"
  echo "  Fallback: ${STATE_FILE}"
  echo "  Minimum:  ${PIPELINE_DIR}/dev-*.json"
  echo ""
  echo "Run some pipeline stages first, or enable execution history (#649)."
  exit 0
fi
```

#### Step 1.3: Apply Filters

Filter data based on arguments:

```bash
# Validate --since date format (YYYY-MM-DD)
if [ -n "$SINCE_DATE" ]; then
  if ! echo "$SINCE_DATE" | grep -qE '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'; then
    echo "ERROR: --since must be YYYY-MM-DD format (got: $SINCE_DATE)"
    exit 1
  fi
fi

# Filter by date (--since)
if [ -n "$SINCE_DATE" ]; then
  # For JSONL: filter files by name (YYYY-MM-DD.jsonl)
  # For state/context: filter by created_at field
  JSONL_FILES=$(echo "$JSONL_FILES" | while read f; do
    FILE_DATE=$(basename "$f" .jsonl)
    if [[ "$FILE_DATE" >= "$SINCE_DATE" ]]; then echo "$f"; fi
  done)
fi

# Filter by issue (--issue)
if [ -n "$ISSUE_FILTER" ]; then
  # For JSONL: filter records by issue_number field
  # For context: filter files by issue number in filename
  echo "Filtering for issue #${ISSUE_FILTER}"
fi

# Limit by run count (--runs)
# Applied during extraction phase
```

---

### Phase 2: Extract Raw Metrics (Deterministic)

**IMPORTANT**: This phase uses Python3 for data extraction because JSONL files
contain one JSON object per line and piping through `cat | jq` loses records.
Python's `glob.glob()` + `json.loads()` per line is the reliable approach. Do
NOT read raw JSONL into AI context — extract only aggregated metrics.

#### Actual JSONL Record Schema

Each line in `*.jsonl` is a complete pipeline run record:

```json
{
  "schema_version": "1",
  "record_type": "run",
  "issue_number": 654,
  "title": "feat: ...",
  "branch": "feat/654-...",
  "base_branch": "main",
  "execution_mode": "automatic",
  "started_at": "ISO-8601",
  "completed_at": "ISO-8601",
  "total_duration_ms": 813341,
  "outcome": "complete",
  "stages": {
    "issue-pickup": {
      "status": "complete",
      "started_at": "ISO-8601",
      "completed_at": "ISO-8601",
      "duration_ms": 55014,
      "execution_mode": "headless"
    },
    "feature-planning": { "...same shape..." },
    "feature-dev": { "...same shape..." },
    "feature-validate": { "...same shape..." },
    "pr-create": { "...same shape..." },
    "pr-merge": { "...same shape..." }
  },
  "tokens": {
    "total_input": 304,
    "total_output": 30158,
    "total_cache_read": 5350629,
    "total_cache_creation": 154560,
    "estimated_cost_usd": 3.56,
    "per_stage": {
      "issue-pickup": {
        "input": 71,
        "output": 2965,
        "cache_read": 459492,
        "cache_creation": 21950
      }
    }
  },
  "labels": ["type:feature"],
  "size": "M",
  "type": "feature",
  "priority": "high",
  "recorded_at": "ISO-8601"
}
```

**Key schema notes:**

- Stages are **nested under `stages`** (not flat `.stage` field)
- A stage with `duration_ms: 0` or missing means it was skipped
- Token data is under `tokens` with `total_*` fields and `per_stage` breakdown
- `estimated_cost_usd` is **pre-computed** — use it instead of recalculating
- `labels` contains the full GitHub labels array (Issue #844) — classification
  labels only (`type:*`, `component:*`); priority/size are board fields
- `size`, `type`, `priority` are extracted from board fields (or legacy labels)
  for convenient filtering
- Older records may have `labels: []` and missing size/type/priority fields

#### Step 2.1: Extract from JSONL History (Primary)

When `DATA_SOURCE="history"`, invoke the deterministic Go verb
`nightgauge pipeline aggregate` (audit row **B2**). The verb replaces
~230 lines of inline-Python aggregation with a single binary call that emits
a stable JSON schema (v1) — see
[docs/GO_BINARY.md](../../docs/GO_BINARY.md#pipeline-analysis) for the schema
reference and flag list.

```bash
# Audit row B2 — deterministic Go aggregation over the daily JSONL history.
nightgauge pipeline aggregate \
  --runs "${RUNS_LIMIT}" \
  ${SINCE_DATE:+--since "${SINCE_DATE}"} \
  ${ISSUE_FILTER:+--issue "${ISSUE_FILTER}"} \
  --include analysis \
  --workdir . \
  --json > /tmp/audit_extracted.json 2>/dev/null \
  || echo '{"v":1,"runs_analyzed":0,"runs":[],"stage_metrics":{},"model_usage":{"by_stage":{},"by_source":{}},"warnings":["pipeline aggregate failed"]}' > /tmp/audit_extracted.json

echo "Extracted $(jq -r '.runs_analyzed' /tmp/audit_extracted.json) runs to /tmp/audit_extracted.json"
```

The output schema (v1) provides the same metric set the previous Python
aggregator computed, with these field renames (locked by the v1 contract):

| Old (Python) field                        | New (Go schema v1) field               |
| ----------------------------------------- | -------------------------------------- |
| `run_metrics`                             | `runs`                                 |
| `stage_durations.<stage>`                 | `stage_metrics.<stage>.duration_stats` |
| `stage_statuses.<stage>`                  | `stage_metrics.<stage>.status`         |
| `stage_tokens.<stage>`                    | `stage_metrics.<stage>.token_stats`    |
| `size_estimation_accuracy.baselines`      | `analysis.size_baselines`              |
| `size_estimation_accuracy.accuracy_rates` | `analysis.size_accuracy_rates`         |
| `size_estimation_accuracy.oversized`      | `analysis.oversized`                   |
| `size_estimation_accuracy.undersized`     | `analysis.undersized`                  |
| `size_estimation_accuracy.weekly_trend`   | `analysis.weekly_accuracy`             |

`duration_stats` and `token_stats` are pre-computed `Stats` objects with
`count`, `median`, `mean`, `p90`, `min`, `max` — the analysis phase reads them
directly instead of recomputing percentiles in Python.

#### Step 2.2: Extract from state.json (Fallback)

When `DATA_SOURCE="state"`, use jq to extract basic pipeline state:

```bash
jq '{
  total_runs: (.runs // [] | length),
  stages: (.runs // [] | map(.stages // []) | flatten |
    group_by(.name) | map({
      stage: .[0].name,
      count: length,
      statuses: ([.[].status] | group_by(.) |
        map({(.[0]): length}) | add)
    }))
}' "$STATE_FILE" > /tmp/audit_extracted.json
```

Note: state.json provides minimal data. Most analysis categories will be
unavailable. Output will indicate limited data source.

#### Step 2.3: Extract from Context Files (Minimal Fallback)

When `DATA_SOURCE="context"`, extract from dev-\*.json pipeline context files:

```bash
for f in ${PIPELINE_DIR}/dev-*.json; do
  ISSUE=$(jq -r '.issue_number' "$f")
  TESTS_PASSED=$(jq -r '.tests_status.passed // 0' "$f")
  TESTS_FAILED=$(jq -r '.tests_status.failed // 0' "$f")
  FILES_MODIFIED=$(jq -r '.files_changed.modified | length' "$f")
  echo "{\"issue\": $ISSUE, \"tests_passed\": $TESTS_PASSED, \"tests_failed\": $TESTS_FAILED, \"files_modified\": $FILES_MODIFIED}"
done | jq -s '.' > /tmp/audit_extracted.json
```

---

### Phase 3: Compute Analysis

Read `/tmp/audit_extracted.json` (produced in Phase 2) and compute aggregates.
The extraction step already parsed the JSONL and built structured data — this
phase reads that pre-aggregated JSON to compute higher-level analysis.

#### Step 3.1: Read Extracted Data

Read the file `/tmp/audit_extracted.json` to load the extracted metrics into
context. This file is small (pre-aggregated) — safe to read directly.

Use the extracted `runs`, `stage_metrics`, and `model_usage` blocks to compute
the following analyses. Per-stage stats (count/median/mean/p90/min/max) are
pre-computed under `stage_metrics.<stage>.duration_stats` and
`stage_metrics.<stage>.token_stats` — read them directly instead of
recomputing percentiles.

#### Step 3.2: Token Efficiency Analysis

From `runs`, compute:

- **Total tokens**: Sum of `total_input + total_output` across all runs
- **Avg tokens/run**: Total divided by `runs_analyzed`
- **Cache hit rate**:
  `sum(total_cache_read) / sum(total_cache_read + total_cache_creation + total_input) * 100`
- **Per-stage cache hit rate** (Issue #3804): For each stage in
  `stage_metrics`, compute
  `cache_read.mean / (cache_read.mean + cache_creation.mean + input.mean) * 100`
  from `stage_metrics.<stage>.token_stats`. This is the same canonical formula
  as the global rate above, applied per stage. A stage whose `cache_read.count`
  is `0` (or whose denominator is `0` — e.g. a skipped deterministic stage with
  no cacheable input) is reported as `null` ("no data"), **never** `0%`, so it
  does not raise a false low-reuse finding. Emit the result as the
  `per_stage_cache_hit_rate` report block (see Phase 5.1).
- **Per-stage token distribution**: From `stage_metrics.<stage>.token_stats`,
  compute what % each stage uses
- **Output token outliers**: Runs where `total_output` exceeds 2x the median

#### Step 3.3: Stage Performance Analysis

From `stage_metrics`, compute:

- **Per-stage median/avg/p90/max duration**: Read directly from
  `stage_metrics.<stage>.duration_stats` (in milliseconds)
- **Failure rate**: From `stage_metrics.<stage>.status`, count "failed"/"error"
  vs total
- **Bottleneck detection**: Stages where mean duration > 1.5x overall average
- **Stage skip rate**: Count runs where stage appears in `runs[].skipped_stages`

#### Step 3.4: Cost Analysis

From `runs`, compute:

- **Total cost**: Sum of `estimated_cost_usd` (pre-computed in records — use it
  directly, do NOT recalculate from token counts)
- **Avg/median/p90/max cost per run**
- **Top 10 most expensive runs** by `estimated_cost_usd`
- **Per-stage cost breakdown** for expensive runs (from
  `stage_metrics.<stage>.token_stats`)
- **Daily cost trends**: Group by `started_at[:10]`

#### Step 3.5: Quality Correlation

From `runs`, compute:

- **Success rate**: Count `outcome == "complete"` / total
- **Validate-to-dev ratio**: Per-run, compare feature-validate duration vs
  feature-dev duration. Ratio > 2.0 is a red flag (validate doing too much work)

#### Step 3.6: Size-Correlated Analysis (Issue #844)

From `runs`, group by `size`, `type`, and `priority`:

- **Cost by size**: Avg/median `estimated_cost_usd` for each size (XS/S/M/L/XL)
- **Duration by size**: Avg/median `total_duration_ms` for each size
- **Cost by type**: Avg cost for feature vs bug vs docs vs refactor
- **Priority vs cost**: Does priority correlate with cost?
- **Size accuracy**: Flag runs where cost deviates significantly from size
  expectations (e.g., S-sized issue with L-sized cost)

Note: Older records without size/type/priority fields should be excluded from
size-correlated analysis. Count how many records have these fields populated.

#### Step 3.7: Model Usage Analysis (Issue #1590)

From `model_usage`, compute:

- **Per-stage model distribution**: For each stage, show which model was used
  and how often (e.g., feature-dev: 80% sonnet, 20% opus). Read from
  `model_usage.by_stage.<stage>` (also mirrored at
  `stage_metrics.<stage>.models`).
- **Model source distribution**: How models were selected per stage (auto vs
  config vs env vs stage-default). Read from `model_usage.by_source.<stage>`.
- **Misrouting detection**: Flag runs where expensive models (opus) were used
  for deterministic stages (issue-pickup, pr-create, pr-merge)
- **Cost-per-model**: Cross-reference with `stage_metrics.<stage>.token_stats`
  to compute avg cost when each model was used per stage

Note: Older records without `model_selection` in stages will have empty
`model_usage`. Report the count of records with model data vs without.

#### Step 3.9: Size Estimate Accuracy (Issue #1591)

From the `analysis` block (populated when `--include analysis` was passed):

- **Per-size accuracy table**: For each size label (XS/S/M/L/XL), show:
  - Run count, median cost, avg cost, min/max cost — read directly from
    `analysis.size_baselines.<size>`
  - Accuracy rate — % of runs whose actual cost fell within 0.5x–2x of the
    size's median cost — read from `analysis.size_accuracy_rates.<size>`
  - Flag sizes with accuracy rate below 70% as needing recalibration
- **Oversize detection**: List issues from `analysis.oversized` — labeled
  bigger than the actual cost bracket suggests (e.g., labeled L but cost
  matched S median). These represent wasted budget.
- **Undersize detection**: List issues from `analysis.undersized` — labeled
  smaller than actual cost suggests. These represent budget-constrained runs
  that may have produced lower quality output.
- **Sizing accuracy trend**: From `analysis.weekly_accuracy`, determine if
  sizing accuracy is improving, stable, or degrading over time. Compare
  first-half accuracy to second-half accuracy across the analysis window.

Note: Runs without a `size` field are excluded. The aggregator reports
`analysis.runs_with_size` and `analysis.runs_without_size` directly.

#### Step 3.8: Trend Analysis

From `runs`, group by `started_at[:10]` and compute:

- **Daily run count, avg cost, total cost**
- **Week-over-week comparisons** if data spans multiple weeks
- **Efficiency trajectory**: Is avg cost/run increasing, stable, or decreasing?

#### Step 3.10: Retry & Backtrack Analysis

From `runs`, analyze retry patterns:

- **Per-stage retry rate**: Count stages with `retry_count > 0` / total stage
  executions
- **Avg retries per run**: Mean of `retry_count` across all runs
- **Backtrack frequency**: Count runs that triggered stage backtracks
- **Retry cost impact**: For runs with retries, compute the cost premium vs
  no-retry runs
- **Flag**: Stages with retry rate > 20% as needing investigation

#### Step 3.11: Comparison Analysis (Conditional)

Only runs when `--compare DATE` is provided. Splits runs into "before" and
"after" the comparison date, then computes deltas for:

- Avg cost per run (before vs after, % change)
- Success rate (before vs after)
- Avg duration (before vs after)
- Cache hit rate (before vs after)

Display as a comparison table in the human-readable output.

#### Step 3.12: Friction Correlation (Optional)

If `.nightgauge/pipeline/assessments/` directory exists and contains
assessment records:

- Count friction records per skill
- Correlate skills with most friction to their cost data
- Include "Skills with Most Friction" section in findings

Gracefully skip if no assessments directory exists.

---

### Phase 4: Generate Findings (Probabilistic)

**PURPOSE**: AI interprets the computed metrics to produce human-readable
findings with severity levels and actionable recommendations.

#### Step 4.1: Read Computed Metrics

Read all `/tmp/audit_*.json` files produced in Phase 3. These are small,
pre-aggregated JSON — safe to load into AI context.

#### Step 4.2: Classify Findings

For each analysis category, generate findings:

```json
{
  "category": "token_efficiency | stage_performance | cost_optimization | quality_correlation | trends | size_estimation_accuracy | retry_analysis",
  "severity": "critical | high | medium | low | info",
  "title": "Short description of finding",
  "description": "Detailed explanation with supporting metrics",
  "estimated_savings": "$X.XX/run or X% improvement",
  "recommendation": "Specific actionable step to address this"
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

**Per-stage low-reuse cache finding (Issue #3804):**

For each stage whose `per_stage_cache_hit_rate` (from Step 3.2) is non-`null`
and **below the configured threshold**, emit a `token_efficiency` finding. The
threshold is resolved from config:

- `pipeline.cache.stage_alert_thresholds.<stage>` if present for that stage,
- else `pipeline.cache.alert_threshold` (default `40`, i.e. 40%).

Both this skill and the Token Economics health dimension read the same resolved
value, so the per-stage finding agrees across surfaces. Severity:

| Per-stage rate | Severity |
| -------------- | -------- |
| `< 10%`        | `high`   |
| `< threshold`  | `medium` |

Stages reported as `null` (no cacheable input) are skipped — never flagged.
Example finding `title`: `"Low cache reuse for feature-validate (18%)"`.

#### Step 4.3: Generate Recommendations

For each finding, produce a specific recommendation. Recommendations must be:

- **Actionable**: Describes a specific change to make
- **Measurable**: Includes expected improvement metric
- **Scoped**: References specific stages, files, or configurations

---

### Phase 5: Output Results

#### Step 5.1: Structured JSON Report

Assemble the full report:

```json
{
  "schema_version": "1.1",
  "analysis_period": {
    "from": "YYYY-MM-DD",
    "to": "YYYY-MM-DD",
    "runs_analyzed": 15,
    "data_source": "history | state | context"
  },
  "summary": {
    "total_cost_usd": 12.5,
    "avg_cost_per_run": 0.83,
    "total_tokens": 1500000,
    "success_rate": 0.87,
    "avg_duration_minutes": 45,
    "cache_hit_rate": 0.96
  },
  "per_stage_cache_hit_rate": {
    "<stage>": {
      "cache_hit_rate": "number (0-1) | null",
      "sample_count": "number",
      "threshold": "number (0-1, resolved per-stage threshold)",
      "below_threshold": "boolean"
    },
    "feature-dev": {
      "cache_hit_rate": 0.974,
      "sample_count": 41,
      "threshold": 0.4,
      "below_threshold": false
    },
    "issue-pickup": {
      "cache_hit_rate": null,
      "sample_count": 0,
      "threshold": 0.4,
      "below_threshold": false
    }
  },
  "findings": [
    {
      "category": "token_efficiency",
      "severity": "high",
      "title": "Feature-dev reads ARCHITECTURE.md on every run (15KB)",
      "description": "...",
      "estimated_savings": "$0.15/run",
      "recommendation": "Use context compaction or cache this file"
    }
  ],
  "recommendations": [
    {
      "priority": 1,
      "action": "Enable prompt caching for frequently-read docs",
      "impact": "~15% token reduction",
      "effort": "low"
    }
  ],
  "model_usage": {
    "by_stage": {
      "issue-pickup": { "haiku": 15 },
      "feature-planning": { "sonnet": 15 },
      "feature-dev": { "sonnet": 12, "opus": 3 },
      "feature-validate": { "haiku": 8, "sonnet": 7 },
      "pr-create": { "haiku": 15 },
      "pr-merge": { "haiku": 15 }
    },
    "by_source": {
      "issue-pickup": { "stage-default": 15 },
      "feature-dev": { "auto": 12, "config": 3 }
    }
  },
  "size_estimation_accuracy": {
    "baselines": {
      "S": { "count": 40, "median_cost": 3.5, "avg_cost": 4.1 },
      "M": { "count": 30, "median_cost": 7.2, "avg_cost": 8.0 },
      "L": { "count": 20, "median_cost": 12.5, "avg_cost": 13.8 }
    },
    "accuracy_rates": {
      "S": { "total": 40, "within_range": 34, "accuracy_pct": 85.0 },
      "M": { "total": 30, "within_range": 22, "accuracy_pct": 73.3 },
      "L": { "total": 20, "within_range": 12, "accuracy_pct": 60.0 }
    },
    "oversized": [
      {
        "issue_number": 1234,
        "labeled_size": "L",
        "actual_bracket": "S",
        "cost_usd": 3.2
      }
    ],
    "undersized": [
      {
        "issue_number": 1235,
        "labeled_size": "S",
        "actual_bracket": "L",
        "cost_usd": 14.5
      }
    ],
    "weekly_trend": [
      { "week": "2026-W05", "total": 8, "accurate": 6, "accuracy_pct": 75.0 },
      { "week": "2026-W06", "total": 7, "accurate": 6, "accuracy_pct": 85.7 }
    ],
    "sizing_trend": "improving | stable | degrading"
  },
  "retry_analysis": {
    "per_stage_retry_rate": {
      "<stage>": "number (0-1)"
    },
    "avg_retries_per_run": "number",
    "backtrack_count": "number",
    "retry_cost_premium_pct": "number",
    "flagged_stages": ["string (stages with retry rate > 20%)"]
  },
  "trends": {
    "cost_trend": "decreasing | stable | increasing",
    "efficiency_trend": "improving | stable | degrading",
    "quality_trend": "improving | stable | degrading"
  },
  "created_at": "ISO-8601 timestamp"
}
```

If `--format json`, write to
`.nightgauge/pipeline/audit-report-YYYY-MM-DD.json` and output path.

#### Step 5.2: Human-Readable Summary

Output a formatted summary to the console:

```
PIPELINE AUDIT REPORT
═══════════════════════════════════════════════════════════

Analysis Period: 2026-02-01 to 2026-02-12 (15 runs)
Data Source: execution history (JSONL)

SUMMARY
───────────────────────────────────────────────────────────
  Total Cost:        $12.50 (avg $0.83/run)
  Total Tokens:      1,500,000 (avg 100,000/run)
  Success Rate:      87% (13/15 runs)
  Avg Duration:      45 minutes
  Cache Hit Rate:    23%

PER-STAGE CACHE HIT RATE
───────────────────────────────────────────────────────────
  feature-planning:   96%
  feature-dev:        97%
  feature-validate:   18%  ⚠ below 40% threshold
  pr-create:          96%
  pr-merge:           95%
  issue-pickup:       — (no cacheable input)

MODEL USAGE
───────────────────────────────────────────────────────────
  issue-pickup:       haiku (100% via stage-default)
  feature-planning:   sonnet (100% via auto)
  feature-dev:        sonnet 80% / opus 20% (auto)
  feature-validate:   haiku 53% / sonnet 47% (auto)
  pr-create:          haiku (100% via stage-default)
  pr-merge:           haiku (100% via stage-default)

SIZE ESTIMATE ACCURACY (90 runs with size data)
───────────────────────────────────────────────────────────
  Size   Runs   Median $   Accuracy
  XS       10     $1.20      90.0%
  S        40     $3.50      85.0%
  M        30     $7.20      73.3%  ⚠ below 70% threshold
  L        20    $12.50      60.0%  ⚠ below 70% threshold

  Oversized (3 issues — labeled bigger than actual cost):
    #1234  L → S bracket  ($3.20)
    #1456  M → S bracket  ($3.80)
    #1478  XL → M bracket ($7.10)

  Undersized (2 issues — labeled smaller than actual cost):
    #1235  S → L bracket  ($14.50)
    #1467  S → M bracket  ($8.20)

  Sizing Trend: improving (75.0% → 85.7% over 2 weeks)

RETRY & BACKTRACK ANALYSIS
───────────────────────────────────────────────────────────
  Avg retries/run:     0.3
  Backtrack count:     2 runs triggered backtracks
  Retry cost premium:  +18% vs no-retry runs

  Per-Stage Retry Rates:
    feature-dev:        25%  ⚠ above 20% threshold
    feature-validate:   12%
    pr-create:           5%

FINDINGS (4 total: 1 high, 2 medium, 1 info)
───────────────────────────────────────────────────────────

  [HIGH] Token Efficiency: feature-dev reads ARCHITECTURE.md every run
    → Estimated savings: $0.15/run ($2.25 over 15 runs)
    → Recommendation: Enable prompt caching for stable docs

  [MEDIUM] Stage Performance: feature-validate is 2.3x slower than average
    → Avg duration: 12 min (vs 5.2 min average)
    → Recommendation: Review test suite for redundant test cases

  [MEDIUM] Cost Optimization: 3 runs used Opus for simple bug fixes
    → Estimated savings: $1.20/run with Sonnet
    → Recommendation: Use model routing based on issue complexity

  [INFO] Trends: Cost per run decreased 12% week-over-week
    → Positive trend — continue current optimization approach

RECOMMENDATIONS (sorted by impact)
───────────────────────────────────────────────────────────
  1. Enable prompt caching for docs/  (~15% token reduction, low effort)
  2. Route simple issues to Sonnet    (~$1.20/run savings, medium effort)
  3. Optimize feature-validate tests  (~40% stage speedup, medium effort)

───────────────────────────────────────────────────────────
Run with --create-issues to auto-create GitHub issues for findings.
Next: /nightgauge:pipeline-audit --create-issues --severity medium
```

---

### Phase 6: Create Issues (Optional)

Only executes when `--create-issues` is passed.

#### Step 6.1: Filter by Severity

```bash
# Only create issues for findings at or above MIN_SEVERITY
# Default: high (critical + high only)
# With --severity medium: critical + high + medium
```

#### Step 6.2: Check for Existing Issues

Before creating, search for duplicates across **both open and recently closed**
issues. Findings may overlap with recently completed epics.

```bash
# Search open issues
nightgauge forge issue list --repo "$REPO" --search "${FINDING_KEYWORDS}" --state open --json number,title

# Search recently closed issues/epics (last 30 days)
nightgauge forge issue list --repo "$REPO" --search "${FINDING_KEYWORDS}" --state closed --json number,title --limit 20

# Also check open epics that may already cover this finding
nightgauge forge issue list --repo "$REPO" --label "type:epic" --state all --limit 30 --json number,title
```

Skip creation if a matching issue exists (open or recently closed).

#### Step 6.3: Create Issues

When creating 3+ findings as issues, create them as an **epic with sub-issues**
using the project's deterministic hooks. This follows the
`/nightgauge-issue-create` workflow:

**Step 6.3a**: Create the epic:

```bash
nightgauge forge issue create \
  --title "epic: Pipeline audit findings — ${SUMMARY}" \
  --body "${EPIC_BODY}" \
  --label "type:epic,priority:high,size:L"
```

**Step 6.3b**: Create each sub-issue using the Go binary:

```bash
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"

"$BINARY" issue create-sub <EPIC_NUMBER> "<FINDING_TITLE>" "<FINDING_BODY>"
```

Note: `issue create-sub` does NOT support `--labels`. After creating the
sub-issue, add labels separately:

```bash
nightgauge forge graphql -f query='mutation($id:ID!,$labels:[ID!]!){addLabelsToLabelable(input:{labelableId:$id,labelIds:$labels}){clientMutationId}}' \
  -f id="$SUB_ISSUE_NODE_ID" -f labels="$LABEL_IDS"
```

**Step 6.3c**: Sync the epic to the project board:

```bash
"$BINARY" project add <EPIC_NUMBER>
```

Each sub-issue created via `issue create-sub` is automatically synced to the
project board by the binary.

**Severity to priority mapping:**

| Finding Severity | Issue Priority    |
| ---------------- | ----------------- |
| `critical`       | `priority:high`   |
| `high`           | `priority:high`   |
| `medium`         | `priority:medium` |
| `low`            | `priority:low`    |

For fewer than 3 findings, create standalone issues instead of an epic.

#### Step 6.4: Report Created Issues

Output list of created issues with numbers, titles, and URLs.

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Output Format

### JSON Schema

The full JSON report follows this structure:

```json
{
  "schema_version": "1.1",
  "analysis_period": {
    "from": "string (YYYY-MM-DD)",
    "to": "string (YYYY-MM-DD)",
    "runs_analyzed": "number",
    "data_source": "string (history | state | context)"
  },
  "summary": {
    "total_cost_usd": "number",
    "avg_cost_per_run": "number",
    "total_tokens": "number",
    "success_rate": "number (0-1)",
    "avg_duration_minutes": "number",
    "cache_hit_rate": "number (0-1, global)"
  },
  "per_stage_cache_hit_rate": {
    "<stage>": {
      "cache_hit_rate": "number (0-1) | null",
      "sample_count": "number",
      "threshold": "number (0-1)",
      "below_threshold": "boolean"
    }
  },
  "findings": [
    {
      "category": "string",
      "severity": "string (critical | high | medium | low | info)",
      "title": "string",
      "description": "string",
      "estimated_savings": "string",
      "recommendation": "string"
    }
  ],
  "recommendations": [
    {
      "priority": "number",
      "action": "string",
      "impact": "string",
      "effort": "string (low | medium | high)"
    }
  ],
  "model_usage": {
    "by_stage": {
      "<stage>": { "<model>": "number (count)" }
    },
    "by_source": {
      "<stage>": { "<source>": "number (count)" }
    }
  },
  "size_estimation_accuracy": {
    "baselines": {
      "<size>": {
        "count": "number",
        "median_cost": "number",
        "avg_cost": "number",
        "min_cost": "number",
        "max_cost": "number",
        "median_duration_ms": "number",
        "avg_duration_ms": "number"
      }
    },
    "accuracy_rates": {
      "<size>": {
        "total": "number",
        "within_range": "number",
        "accuracy_pct": "number (0-100)"
      }
    },
    "oversized": [
      {
        "issue_number": "number",
        "title": "string",
        "labeled_size": "string",
        "actual_bracket": "string",
        "cost_usd": "number"
      }
    ],
    "undersized": ["...same shape as oversized..."],
    "weekly_trend": [
      {
        "week": "string (YYYY-Www)",
        "total": "number",
        "accurate": "number",
        "accuracy_pct": "number (0-100)"
      }
    ],
    "sizing_trend": "string (improving | stable | degrading)",
    "runs_with_size": "number",
    "runs_without_size": "number"
  },
  "retry_analysis": {
    "per_stage_retry_rate": {
      "<stage>": "number (0-1)"
    },
    "avg_retries_per_run": "number",
    "backtrack_count": "number",
    "retry_cost_premium_pct": "number",
    "flagged_stages": ["string (stages with retry rate > 20%)"]
  },
  "trends": {
    "cost_trend": "string (decreasing | stable | increasing)",
    "efficiency_trend": "string (improving | stable | degrading)",
    "quality_trend": "string (improving | stable | degrading)"
  },
  "created_at": "string (ISO-8601)"
}
```

---

## Analysis Categories

### 1. Token Efficiency

Identifies wasteful token usage patterns:

- **Per-stage token averages**: Which stages consume the most tokens
- **Outlier detection**: Runs with abnormally high token usage
- **Cache hit rates**: How effectively prompt caching reduces input tokens
- **Input/output ratios**: Balance between context injection and generation
- **Redundant reads**: Same files read multiple times across stages

### 2. Stage Performance

Timing and reliability analysis per pipeline stage:

- **Duration baselines**: Average time per stage with percentiles
- **Failure rates**: Which stages fail most often
- **Retry frequency**: How often stages need re-execution
- **Bottleneck detection**: Stages that are >1.5x slower than average
- **Stage skip effectiveness**: When bypassing stages saves time

### 3. Cost Optimization

Financial analysis and savings opportunities:

- **Total cost trends**: Week-over-week cost tracking
- **Per-run cost distribution**: Identify expensive outlier runs
- **Cost-per-stage**: Where money is spent
- **Model opportunities**: Where a cheaper model would suffice
- **Cache optimization**: Potential savings from better caching

### 4. Quality Correlation

Relationship between spending and outcomes:

- **Success rate trends**: Pipeline reliability over time
- **First-attempt pass rates**: How often runs succeed without retries
- **Token-spend vs outcome**: Does spending more produce better results
- **PR quality signals**: Review iteration counts correlated with spend

### 5. Model Routing (Issue #1590)

Per-stage model selection analysis:

- **Model distribution by stage**: Which model was used for each stage and how
  often
- **Selection source breakdown**: auto vs config vs stage-default vs env
  override
- **Misrouting detection**: Expensive models used for deterministic stages
- **Cost-per-model-per-stage**: Average cost when each model is used per stage

### 6. Trend Analysis

Longitudinal patterns across time:

- **Week-over-week comparisons**: Cost, tokens, duration, success rate
- **Rolling averages**: Smoothed 7-day metrics
- **Efficiency trajectory**: Improving, stable, or degrading
- **Seasonal patterns**: Certain issue types consistently cost more

### 7. Size Estimate Accuracy (Issue #1591)

Validates whether issue size labels accurately predict actual cost and duration:

- **Per-size baselines**: Median and average cost/duration for each size bracket
  (XS/S/M/L/XL)
- **Accuracy rates**: Percentage of runs whose actual cost falls within 0.5x–2x
  of the size's median cost — the "within expected range" metric
- **Oversize detection**: Issues labeled with a larger size than their actual
  cost suggests (wasted budget — could have used cheaper model/budget)
- **Undersize detection**: Issues labeled smaller than actual cost suggests
  (budget-constrained runs that may have produced lower quality)
- **Sizing trend**: Weekly accuracy rate over time — is the team getting better
  or worse at estimating issue size?

### 8. Retry & Backtrack Analysis

Analyzes retry and backtrack patterns across pipeline runs:

- **Per-stage retry rates**: Which stages require the most retries
- **Average retries per run**: Mean retry count across all analyzed runs
- **Backtrack frequency**: How often runs trigger stage backtracks
- **Retry cost premium**: Additional cost incurred by runs with retries vs clean
  runs
- **Flagged stages**: Stages with retry rate above 20% that need investigation

---

## Error Handling

| Condition                          | Action                                                         |
| ---------------------------------- | -------------------------------------------------------------- |
| No JSONL history files             | Fall back to state.json                                        |
| No state.json                      | Fall back to context files (dev-\*.json)                       |
| No data at all                     | Output friendly message, exit 0                                |
| Malformed JSONL records            | Skip bad records, report count at end                          |
| python3 not installed              | Error with install instructions                                |
| forge not authenticated            | Error with `nightgauge forge auth login` instructions          |
| `--create-issues` without findings | Output "No findings at severity threshold"                     |
| Insufficient data for trends       | Skip trend analysis, note in output                            |
| `issue create-sub` label error     | Use `nightgauge forge graphql` addLabelsToLabelable separately |
| Duplicate finding exists           | Skip creation, report as already tracked                       |

---

## Pipeline Position

```
UTILITIES (not part of main pipeline)

/nightgauge:pipeline-audit
       ↑
  Use anytime to analyze pipeline efficiency
  Reads: .nightgauge/pipeline/history/*.jsonl
  Reads: .nightgauge/pipeline/state.json (fallback)
  Reads: .nightgauge/pipeline/dev-*.json (minimal fallback)
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs.

---

## Configuration

This skill reads configuration from `.nightgauge/config.yaml`:

| Config Key           | Default | Description                      |
| -------------------- | ------- | -------------------------------- |
| `project.number`     | -       | GitHub Project number for issues |
| `audit.default_runs` | `10`    | Default `--runs` value           |
| `audit.cost_model`   | `opus`  | Cost model for estimation        |
| `audit.auto_create`  | `false` | Auto-create issues without flag  |

---

**Author:** nightgauge **License:** Apache-2.0
