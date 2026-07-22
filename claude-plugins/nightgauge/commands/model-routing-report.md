---
name: nightgauge-model-routing-report
description: Generate a model routing performance report from execution history. Shows
  auto-selection success rates, cost savings, under/over routing patterns, and
  threshold recommendations.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Glob Grep Bash
disable-model-invocation: true
---

# Model Routing Report

> Analyze model routing performance from execution history

## Description

This command reads execution history JSONL files, runs the
ModelPerformanceAnalyzer with auto-selection analysis, and outputs a formatted
report.

## Invocation

| Tool        | Command                            |
| ----------- | ---------------------------------- |
| Claude Code | `/nightgauge:model-routing-report` |

## Workflow

### Step 1: Locate Execution History Files

```bash
ls .nightgauge/pipeline/history/*.jsonl 2>/dev/null
```

If no files exist, report "No execution history found" and exit.

### Step 2: Parse JSONL Records

Read all `.jsonl` files from `.nightgauge/pipeline/history/`. Each line is
a JSON record. Filter to records with `record_type: "run"`.

For each run record, extract per-stage data including model selection metadata
(`model_selection` field in each stage).

### Step 3: Run Analysis

Use the parsed records to compute:

1. **Auto-selection success rates by stage** — Filter to records where
   `model_selection.source === 'auto'`, compute success/failure per stage
2. **Cost comparison** — Compare actual costs of auto-selected runs vs
   hypothetical static-model costs
3. **Confidence distribution** — Histogram of auto-selector confidence values
4. **Under-routing patterns** — Cases where auto-selection chose a lighter model
   that failed on complex tasks
5. **Over-routing patterns** — Cases where auto-selection chose opus for simple
   tasks that succeeded easily
6. **Threshold recommendations** — Suggest adjustments to
   `complexity_thresholds.haiku_max` and `sonnet_max`

### Step 4: Output Report

Output a formatted report with these sections:

```
## Model Routing Performance Report

### Summary
- Records analyzed: N
- Auto-selected records: N (X%)
- Overall auto-selection success rate: X%
- Estimated cost savings vs static defaults: $X.XXXX

### Auto-Selection Success Rates by Stage

| Stage | Auto-Selected | Success Rate | Avg Confidence | Primary Model |
|-------|---------------|-------------|----------------|---------------|
| ...   | ...           | ...         | ...            | ...           |

### Under-Routing Patterns
(Cases where lighter models failed on complex tasks)

| Stage | Model | Complexity | Failures | Suggestion |
|-------|-------|-----------|----------|------------|
| ...   | ...   | ...       | ...      | ...        |

### Over-Routing Patterns
(Cases where expensive models were used on simple tasks)

| Stage | Model | Complexity | Successes | Est. Waste | Suggestion |
|-------|-------|-----------|-----------|------------|------------|
| ...   | ...   | ...       | ...       | ...        | ...        |

### Threshold Recommendations

| Field | Current | Suggested | Confidence | Rationale |
|-------|---------|-----------|-----------|-----------|
| ...   | ...     | ...       | ...       | ...       |

### Model Usage Distribution

| Model | Runs | Success Rate | Avg Cost |
|-------|------|-------------|---------|
| ...   | ...  | ...         | ...     |
```

If no auto-selected records exist, report "No auto-selected records found.
Enable automatic model routing in .nightgauge/config.yaml to start
collecting data."

## Arguments

```bash
# Default: analyze all history
/nightgauge:model-routing-report

# Analyze last 30 days
/nightgauge:model-routing-report --days 30

# Analyze specific stage
/nightgauge:model-routing-report --stage feature-dev
```

## Author

nightgauge
