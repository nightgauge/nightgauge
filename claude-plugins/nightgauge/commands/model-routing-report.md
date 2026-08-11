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

> **What the record actually carries.** The Go writer emits
> `model_selection` as `{ "model": ..., "source": ... }` and nothing else
> (`V2ModelSelect`, internal/state/history.go). There is **no** auto-selector
> confidence and **no** complexity band on a history record — do not invent
> either, and do not report a zero for them. `source` is one of
> `MODEL_SELECTION_SOURCES`, and `scheduler` means "the scheduler resolved the
> model and nothing substituted it" — it counts operator pins (env overrides,
> `pipeline.stage_models`, manual-mode defaults) exactly the same as router
> picks, so never describe it as proof a model was chosen automatically.

1. **Unsubstituted-model success rates by stage** — Filter to records where
   `model_selection.source === 'scheduler'` (see `MODEL_SELECTION_SOURCES`),
   compute success/failure per stage
2. **Cost comparison** — Compare actual costs of those runs vs hypothetical
   static-model costs
3. **Under-routing patterns** — Cases where a lighter model failed on complex
   tasks. Complexity is NOT recorded on history records, so this section can
   only be filled from another source; report it as "not recorded" otherwise.
4. **Over-routing patterns** — Cases where opus ran simple tasks that succeeded
   easily. Same limitation: complexity is not recorded.
5. **Threshold recommendations** — Suggest adjustments to
   `complexity_thresholds.haiku_max` and `sonnet_max`. Requires the complexity
   data above; omit the section when it is unavailable.

### Step 4: Output Report

Output a formatted report with these sections:

```
## Model Routing Performance Report

### Summary
- Records analyzed: N
- Records with an unsubstituted model (source=scheduler): N (X%)
- Success rate for those records: X%
- Estimated cost savings vs static defaults: $X.XXXX

### Success Rates by Stage (source=scheduler)

| Stage | Unsubstituted | Success Rate | Primary Model |
|-------|---------------|-------------|---------------|
| ...   | ...           | ...         | ...           |

### Under-Routing Patterns
(Cases where lighter models failed on complex tasks — complexity is NOT
recorded on history records; omit this section unless another source supplies
it)

| Stage | Model | Complexity | Failures | Suggestion |
|-------|-------|-----------|----------|------------|
| ...   | ...   | ...       | ...      | ...        |

### Over-Routing Patterns
(Cases where expensive models were used on simple tasks — same complexity
limitation as above)

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

If no records carry `model_selection.source === 'scheduler'`, report "No
records with an unsubstituted model found. Enable automatic model routing in
.nightgauge/config.yaml to start collecting data."

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
