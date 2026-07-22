---
name: retro
description: Analyze pipeline failures to identify root causes, recurring patterns, and
  actionable remediation steps using deterministic binary classification. Also
  records outcome data and lessons learned to the knowledge base when
  knowledge_path is available. Use after a batch run or periodically to
  understand why runs did not complete successfully.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.5.1"
  source: https://github.com/nightgauge/nightgauge
  chainable: true
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: pipeline-optimizer
model: haiku
---

# Nightgauge Pipeline Retro

## Description

Scrubs session logs, pipeline context files, batch state, and execution history
to surface failure events, classify them into one of 7 categories, and generate
a root-cause retrospective with remediation recommendations. Produces actionable
recommendation templates with config snippets, recovery guidance, and optional
GitHub issue creation. Uses deterministic Bash/Python3 for data extraction and
failure event collection; AI interprets patterns and produces the narrative
findings.

**Use Cases:**

- Post-batch failure triage after a multi-issue run
- Identifying recurring failure modes across issues
- Understanding why a specific issue's pipeline did not complete
- Tracking whether past remediations are working
- Creating actionable issues for systemic pipeline problems

**When to Use:**

- After any batch run where one or more issues did not reach `complete` status
- When a single issue's pipeline stalled or failed unexpectedly
- Periodically (post-sprint) to review accumulated failure patterns
- Before planning pipeline reliability improvements

## Invocation

| Tool        | Command                                           |
| ----------- | ------------------------------------------------- |
| Claude Code | `/nightgauge:retro [options]`                     |
| Copilot     | Invoke via Agent Skills extension                 |
| Cursor      | Run via Agent Skills or direct SKILL.md           |
| Standalone  | `claude --skill skills/nightgauge-retro/SKILL.md` |

## Arguments

### Core Options

| Argument           | Description                                                                   | Default |
| ------------------ | ----------------------------------------------------------------------------- | ------- |
| `--issue N`        | Analyze failures for a specific issue number                                  | -       |
| `--since DATE`     | Analyze failures since date (YYYY-MM-DD)                                      | -       |
| `--period N`       | Analyze last N days                                                           | `7`     |
| `--all-failures`   | Include all failure events, not just last batch run                           | `false` |
| `--format FORMAT`  | Output format: `summary`, `json`, `both`                                      | `both`  |
| `--dry-run`        | Preview issues that would be created (default)                                | `true`  |
| `--create-issues`  | Auto-create GitHub issues for high findings                                   | `false` |
| `--severity LEVEL` | Minimum severity for `--create-issues`                                        | `high`  |
| `--record-outcome` | Record outcome to knowledge base (auto-detected when `knowledge_path` is set) | `false` |
| `--epic N`         | Run post-epic synthesis: aggregate patterns across all sub-issues of epic #N  | -       |
| `--skill-feedback` | Run skill self-assessment synthesis (aggregate friction from assessments dir) | `false` |

### Examples

```bash
# Retro on the last batch run (default scope)
/nightgauge:retro

# Retro on a specific issue
/nightgauge:retro --issue 960

# Failures from the last 14 days
/nightgauge:retro --period 14

# Failures since a specific date
/nightgauge:retro --since 2026-02-01

# All failures in history (not just last batch)
/nightgauge:retro --all-failures

# JSON output only (for programmatic consumption)
/nightgauge:retro --format json

# Preview what issues would be created (default behavior)
/nightgauge:retro --dry-run

# Auto-create GitHub issues for high-severity findings
/nightgauge:retro --create-issues

# Create issues for medium+ severity findings
/nightgauge:retro --create-issues --severity medium

# Post-epic synthesis: aggregate patterns across all sub-issues
/nightgauge:retro --epic 347

# Post-epic synthesis with auto-issue creation for recurring friction
/nightgauge:retro --epic 347 --create-issues

# Skill self-assessment synthesis (aggregate all friction records)
/nightgauge:retro --skill-feedback

# Record outcome to knowledge base for a specific completed issue
/nightgauge:retro --issue 960 --record-outcome

# Record outcome automatically (auto-detected when knowledge_path is set in context)
/nightgauge:retro --issue 960
```

---

## Prerequisites

- `python3` installed (for reliable JSONL/log parsing)
- nightgauge binary installed and configured (`nightgauge forge auth status`)
- `jq` installed for JSON processing (fallback data sources)
- Git repository with `.nightgauge/` directory
- At least one data source populated (see Data Sources below)

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Data Sources

| Source            | Location                                      | What It Provides                                                   |
| ----------------- | --------------------------------------------- | ------------------------------------------------------------------ |
| Session Logs      | `.nightgauge/logs/YYYY-MM-DD_NNN_session.log` | Timestamped stage events, error messages, token budget warnings    |
| Pipeline Context  | `.nightgauge/pipeline/{stage}-{N}.json`       | Per-issue stage outputs, test results, validation status           |
| Batch State       | `.nightgauge/pipeline/batch-state.json`       | Per-issue completion status, failed stages, token usage per run    |
| Execution History | `.nightgauge/pipeline/history/*.jsonl`        | Structured per-run records with outcome, stage statuses, durations |

Each data source is optional. The skill analyzes whatever is available and notes
which sources were absent in the output report.

---

## Failure Categories

| Category             | Description                                                                | Example Signals                                                                            |
| -------------------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `budget-exceeded`    | Run terminated because token or cost budget was exhausted                  | Log: `budget exceeded`, `token limit`, `costUsd > budget`; stage killed mid-execution      |
| `state-management`   | Context file missing, corrupt, or schema mismatch caused stage to abort    | Missing `dev-N.json` when `feature-validate` starts; JSON parse failure on handoff file    |
| `ci-infrastructure`  | External CI system (GitHub Actions, test runner) failed or was unavailable | Log: `CI checks failed`, `workflow run failed`, forge `pr checks --wait` non-zero exit     |
| `model-capability`   | AI model produced output that did not meet task requirements after retries | Log: `model returned empty`, `unexpected output format`, repeated re-prompt loops          |
| `timeout`            | Stage or full pipeline exceeded configured time limit before completing    | Log: `timed out`, `exceeded ci_timeout`, `stage duration > max`; partial context file      |
| `validation-failure` | Tests, TypeScript check, or manual checklist blocked stage progression     | Log: `tests failed`, `tsc error`, `build failed`; `feature-validate` status `failed`       |
| `unknown`            | Failure event detected but does not match any other category               | Unexpected process exit, missing log entries, outcome `failed` with no clear cause in logs |

---

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Parse Arguments and Determine Scope

#### Step 1.1: Parse Arguments

```bash
ISSUE_FILTER=""
SINCE_DATE=""
PERIOD_DAYS=7
ALL_FAILURES=false
OUTPUT_FORMAT="both"
DRY_RUN=true
CREATE_ISSUES=false
MIN_SEVERITY="high"
RECORD_OUTCOME=false

# Parse arguments from invocation:
# --issue N         → ISSUE_FILTER=N
# --since YYYY-MM-DD → SINCE_DATE
# --period N        → PERIOD_DAYS=N
# --all-failures    → ALL_FAILURES=true
# --format FORMAT   → OUTPUT_FORMAT
# --dry-run         → DRY_RUN=true (default, explicit for clarity)
# --create-issues   → CREATE_ISSUES=true, DRY_RUN=false
# --severity LEVEL  → MIN_SEVERITY
# --record-outcome  → RECORD_OUTCOME=true
```

#### Step 1.2: Validate Arguments

```bash
if [ -n "$SINCE_DATE" ]; then
  if ! echo "$SINCE_DATE" | grep -qE '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'; then
    echo "ERROR: --since must be YYYY-MM-DD format (got: $SINCE_DATE)"
    exit 1
  fi
fi

VALID_FORMATS="summary json both"
if ! echo "$VALID_FORMATS" | grep -qw "$OUTPUT_FORMAT"; then
  echo "ERROR: --format must be one of: $VALID_FORMATS"
  exit 1
fi
```

#### Step 1.3: Determine Scope

Default scope is the **last batch run** (batch-state.json). If batch state is
absent or `--all-failures` is passed, fall back to date-based scope using
`--period` or `--since`:

```bash
LOGS_DIR=".nightgauge/logs"
PIPELINE_DIR=".nightgauge/pipeline"
HISTORY_DIR=".nightgauge/pipeline/history"
BATCH_STATE="${PIPELINE_DIR}/batch-state.json"

SCOPE="batch"

# Override scope
if [ "$ALL_FAILURES" = true ] || [ -n "$SINCE_DATE" ] || [ -n "$ISSUE_FILTER" ]; then
  SCOPE="date-range"
fi

if [ ! -f "$BATCH_STATE" ] && [ "$SCOPE" = "batch" ]; then
  echo "No batch-state.json found. Falling back to date-range scope (last ${PERIOD_DAYS} days)."
  SCOPE="date-range"
fi

# Compute SINCE_DATE from --period if not provided
if [ -z "$SINCE_DATE" ]; then
  SINCE_DATE=$(python3 -c "
from datetime import datetime, timedelta
import sys
print((datetime.now() - timedelta(days=${PERIOD_DAYS})).strftime('%Y-%m-%d'))
")
fi

# Load knowledge_path from pipeline context when ISSUE_FILTER is set.
# Also auto-enable RECORD_OUTCOME when knowledge_path is found.
KNOWLEDGE_PATH=""
if [ -n "$ISSUE_FILTER" ]; then
  KNOWLEDGE_PATH=$(jq -r '.knowledge_path // empty' \
    ".nightgauge/pipeline/issue-${ISSUE_FILTER}.json" 2>/dev/null)
  # Auto-detect: if knowledge_path is set, record outcome even without flag
  if [ -n "$KNOWLEDGE_PATH" ]; then
    RECORD_OUTCOME=true
  fi
fi
```

---

### Phase 2: Locate and Read Data Sources (Deterministic Binary)

**IMPORTANT**: All extraction is now performed by the deterministic
`nightgauge` Go binary — `pipeline batch-failures` (consolidates batch
state, JSONL history, and context-files fallback) and `logs scan-failures`
(16-pattern session-log scan). Do NOT re-implement these in Python; the
binary owns the schema and the regex set as single sources of truth (audit
row B29).

#### Step 2.1: Extract Pipeline Failures

Replaces the previous inline-Python parsers for batch state (old Phase 2.1),
execution history (old Phase 2.2), and context-files fallback (old Phase 2.4).
The binary reads `.nightgauge/pipeline/batch-state.json`,
`.nightgauge/pipeline/history/*.jsonl`, and the `issue-*.json` /
`pr-*.json` set in one call, emitting a stable v1 JSON schema.

```bash
ALL_FAILURES_FLAG=""
[ "${ALL_FAILURES}" = "true" ] && ALL_FAILURES_FLAG="--all-failures"
ISSUE_FLAG=""
[ -n "${ISSUE_FILTER}" ] && ISSUE_FLAG="--issue ${ISSUE_FILTER}"
SINCE_FLAG=""
[ -n "${SINCE_DATE}" ] && SINCE_FLAG="--since ${SINCE_DATE}"

"$BINARY" pipeline batch-failures \
  ${SINCE_FLAG} ${ISSUE_FLAG} ${ALL_FAILURES_FLAG} \
  --json > /tmp/retro_pipeline_failures.json

# Quick summary for the run log
jq -r '
  "Pipeline failures: batch=\(.batch_failures|length) " +
  "history=\(.history_failures|length) " +
  "context=\(.context_failures|length) " +
  "skipped_records=\(.skipped_records)"
' /tmp/retro_pipeline_failures.json
```

The output schema (top-level keys):

- `batch` — batch-state.json metadata (`null` when absent)
- `batch_failures[]` — failure rows from `batch-state.json`
- `history_failures[]` — failure rows from JSONL history
- `context_failures[]` — fallback rows for incomplete pipeline context
- `skipped_records` — count of malformed JSONL lines
- `warnings[]` — non-fatal issues (e.g. unreadable history file)

See `docs/GO_BINARY.md` `### Pipeline Operations` →
`pipeline batch-failures` for the full schema.

#### Step 2.2: Extract Session Log Failure Signals

Replaces the previous inline-Python regex scan (old Phase 2.3). The 16-pattern
set lives in `internal/cmd/scanfailures/scanner.go` (`var FailurePatterns`)
as the single Go source of truth. Lines are bounded to 300 bytes and per-file
matches capped at 50 — same as the old Python parser.

```bash
"$BINARY" logs scan-failures \
  ${SINCE_FLAG} ${ISSUE_FLAG} \
  --json > /tmp/retro_logs.json

jq -r '
  "Logs: scanned \(.log_files_scanned) files, " +
  "\(.files_with_signals) with failure signals"
' /tmp/retro_logs.json
```

The output schema preserves the existing `log_signals[]` collection (with
`log_file`, `issue_number`, `date`, `failure_signals[].{line,text}`). See
`docs/GO_BINARY.md` `### Logs Operations` → `logs scan-failures` for the full
schema.

---

### Phase 3: Extract Failure Events

Read the binary-emitted intermediates and merge into a unified failure event
list. Deduplicate by `issue_number` — prefer `history` source over
`batch-state` over `context-files` for the same issue.

```bash
# Read the two binary outputs into AI context (small, pre-aggregated)
cat /tmp/retro_pipeline_failures.json 2>/dev/null || \
  echo '{"batch_failures":[],"history_failures":[],"context_failures":[]}'
cat /tmp/retro_logs.json 2>/dev/null || echo '{"log_signals":[]}'
```

The pipeline-failures file groups failure rows by source under three top-level
arrays (`batch_failures`, `history_failures`, `context_failures`); the logs
file remains a single `log_signals` array. Iterate each array when building
the unified event list.

Build the unified failure event list with deduplication. For each unique
`issue_number`, merge all available signals into one event record:

```json
{
  "issue_number": 960,
  "title": "feat: ...",
  "outcome": "failed",
  "failed_stages": ["feature-validate", "pr-create"],
  "log_signals": ["[ERROR] tests failed: 3 failures", "[ERROR] tsc error: ..."],
  "estimated_cost_usd": 1.23,
  "total_duration_ms": 480000,
  "sources": ["batch-state", "logs"]
}
```

If no failure events are found across all data sources, output:

```
No pipeline failures found in the specified scope.
  Scope: last batch run (or last N days)
  Issues analyzed: X (all completed successfully)
```

Then exit 0.

---

### Phase 4: Categorize Failures (Deterministic Classification)

Use the deterministic `nightgauge failure classify` binary verb to categorize each failure event. The binary implements fixed rules for 7 failure categories (budget-exceeded, timeout, ci-infrastructure, validation-failure, state-management, model-capability, unknown).

**Invocation:**

```bash
CLASSIFICATION=$("$BINARY" failure classify \
  --stage <stage-name> \
  --stderr "<error-output-text>" 2>&1)
```

**Output format** (JSON):

```json
{
  "category": "validation-failure",
  "severity": "high",
  "retryable": false,
  "maxRetries": 1,
  "escalate": false,
  "description": "..."
}
```

Produce a categorized failure record for each event:

```json
{
  "issue_number": 960,
  "title": "feat: ...",
  "category": "validation-failure",
  "severity": "high",
  "evidence": ["[ERROR] tests failed: 3 failures"],
  "failed_stages": ["feature-validate"],
  "outcome": "failed",
  "estimated_cost_usd": 1.23
}
```

(For full category definitions and pattern rules, see `internal/intelligence/failure/taxonomy.go` in the binary codebase.)

---

### Phase 5: AI Interpretation (Root Cause Analysis)

**PURPOSE**: AI reads the categorized failure events and interprets patterns,
root causes, and actionable recommendations. This is the probabilistic phase.

Read `/tmp/retro_batch.json`, `/tmp/retro_history.json`, `/tmp/retro_logs.json`,
`/tmp/retro_context.json` (all pre-aggregated — safe to load into AI context).

For each failure category present in the event list, generate a finding:

```json
{
  "category": "validation-failure",
  "severity": "high",
  "count": 3,
  "affected_issues": [960, 961, 963],
  "title": "Test failures blocking feature-validate stage",
  "root_cause": "Detailed explanation of what is causing this failure category",
  "pattern": "Recurring pattern description (e.g., TypeScript strict mode errors on async handlers)",
  "recommendation": "Specific actionable step to remediate",
  "estimated_recurrence_risk": "high | medium | low"
}
```

**Severity Classification Rules:**

| Severity   | Criteria                                                     |
| ---------- | ------------------------------------------------------------ |
| `critical` | >50% of issues in scope failed; or same failure on 3+ issues |
| `high`     | >25% failure rate; or 2+ issues share the same root cause    |
| `medium`   | Single failure with known root cause and clear fix available |
| `low`      | Single failure, ambiguous cause, or one-off event            |
| `info`     | Successful runs with minor warning signals worth noting      |

Also identify:

- **Cross-issue patterns**: failures that share the same root cause
- **Stage hotspots**: stages that appear most frequently in failed runs
- **Cost impact**: total cost of failed runs (wasted spend)
- **Remediation priority**: order recommendations by highest impact first

---

### Phase 6: Generate Report

#### Step 6.1: Structured JSON Report

Assemble the full retro report:

```json
{
  "schema_version": "1.0",
  "scope": {
    "type": "batch | date-range | single-issue",
    "from": "YYYY-MM-DD",
    "to": "YYYY-MM-DD",
    "issue_filter": null,
    "issues_analyzed": 12,
    "failure_count": 3,
    "data_sources": ["batch-state", "history", "logs"]
  },
  "summary": {
    "failure_rate": 0.25,
    "total_wasted_cost_usd": 3.69,
    "most_common_category": "validation-failure",
    "stage_hotspot": "feature-validate",
    "categories_found": ["validation-failure", "ci-infrastructure"]
  },
  "failures": [
    {
      "issue_number": 960,
      "title": "feat: ...",
      "categories": ["validation-failure"],
      "confidence": "high",
      "evidence": ["[ERROR] tests failed: 3 failures"],
      "failed_stages": ["feature-validate"],
      "estimated_cost_usd": 1.23
    }
  ],
  "findings": [
    {
      "category": "validation-failure",
      "severity": "high",
      "count": 3,
      "affected_issues": [960, 961, 963],
      "title": "Recurring test failures in feature-validate",
      "root_cause": "...",
      "pattern": "...",
      "recommendation": "...",
      "estimated_recurrence_risk": "high"
    }
  ],
  "recommendations": [
    {
      "priority": 1,
      "action": "Fix failing test suite for async handlers",
      "impact": "Eliminates 3 recurring failures",
      "effort": "medium",
      "category": "validation-failure",
      "config_snippet": null,
      "recovery_commands": ["nightgauge forge pr create --head feat/960-... --base main"],
      "affected_issues": [960, 961, 963],
      "estimated_cost_savings_usd": 3.69
    }
  ],
  "created_at": "ISO-8601 timestamp"
}
```

If `--format json` or `--format both`, write to
`.nightgauge/pipeline/retro-report-YYYY-MM-DD.json`.

#### Step 6.2: Human-Readable Summary

Output a formatted summary to the console:

```
PIPELINE RETRO REPORT
═══════════════════════════════════════════════════════════

Scope:          Last batch run (2026-02-21, 12 issues)
Data Sources:   batch-state, execution history, session logs
Failure Rate:   25% (3/12 issues)
Wasted Cost:    $3.69 across failed runs

FAILURE BREAKDOWN BY CATEGORY
───────────────────────────────────────────────────────────
  validation-failure    ████████░░  2 issues  (#960, #963)
  ci-infrastructure     ████░░░░░░  1 issue   (#961)
  budget-exceeded       ░░░░░░░░░░  0 issues
  state-management      ░░░░░░░░░░  0 issues
  model-capability      ░░░░░░░░░░  0 issues
  timeout               ░░░░░░░░░░  0 issues
  unknown               ░░░░░░░░░░  0 issues

STAGE HOTSPOT: feature-validate (appears in 2 of 3 failures)

FINDINGS
───────────────────────────────────────────────────────────

  [HIGH] validation-failure: Recurring test failures blocking 2 issues
    Issues: #960, #963
    Root Cause: TypeScript strict mode errors on async handler return types
    Pattern: Both issues modified the same module (src/pipeline/skillRunner.ts)
    → Recommendation: Add strict type coverage to skillRunner test suite before
      merging any changes to that module
    → Recurrence risk: HIGH

  [MEDIUM] ci-infrastructure: GitHub Actions workflow timeout on #961
    Issues: #961
    Root Cause: Intermittent runner timeout on macOS-latest pool
    Pattern: Single occurrence, likely transient
    → Recommendation: Re-run pipeline for #961; add ci_timeout buffer to config
    → Recurrence risk: LOW

RECOMMENDATIONS (sorted by impact)
───────────────────────────────────────────────────────────
  # │ Action                          │ Category           │ Effort │ Issues
  1 │ Fix async handler type errors   │ validation-failure │ medium │ #960, #963
  2 │ Increase ci_timeout in config   │ ci-infrastructure  │ low    │ #961

  Config snippet for #2:
    pipeline:
      ci_timeout: 600

RECOVERY GUIDANCE
───────────────────────────────────────────────────────────
  #960 │ Branch has commits, no PR → nightgauge forge pr create --head feat/960-... --base main
  #961 │ PR #187 exists, CI failed → nightgauge forge pr merge --node-id $ID --strategy squash

ESTIMATED RECOVERY EFFORT: 1 medium code fix + 1 low-effort config change

───────────────────────────────────────────────────────────
Report saved: .nightgauge/pipeline/retro-report-2026-02-21.json
Run with --create-issues to auto-create GitHub issues for findings.
Next: /nightgauge:retro --create-issues --severity medium
```

If `--format summary`, output only the console summary. If `--format json`,
write JSON only and output the file path. If `--format both`, write JSON and
output the console summary.

---

### Phase 7: Recommendation Template Generation

**PURPOSE**: Transform categorized findings into structured, actionable
recommendation templates with config snippets, recovery guidance, and
deduplication.

#### Step 7.1: Deduplicate Findings

Before generating recommendations, group findings by root cause:

1. Group findings sharing the same `category` AND `pattern` text
2. Merge affected issues into a single recommendation
3. Sum `estimated_cost_usd` across merged findings
4. Take the highest `severity` from merged findings
5. Combine `evidence` arrays (deduplicated)

```python
# /tmp/retro_dedup.py — group findings by category + pattern
import json

findings = []  # loaded from Phase 5 output

seen = {}
for f in findings:
    key = f"{f['category']}::{f['pattern']}"
    if key in seen:
        existing = seen[key]
        existing["affected_issues"] = list(
            set(existing["affected_issues"]) | set(f["affected_issues"])
        )
        existing["count"] += f["count"]
        existing["estimated_cost_usd"] = (
            existing.get("estimated_cost_usd", 0)
            + f.get("estimated_cost_usd", 0)
        )
        # Take highest severity
        SEVERITY_ORDER = ["info", "low", "medium", "high", "critical"]
        if SEVERITY_ORDER.index(f["severity"]) > SEVERITY_ORDER.index(
            existing["severity"]
        ):
            existing["severity"] = f["severity"]
    else:
        seen[key] = {**f}

deduped_findings = list(seen.values())
```

#### Step 7.2: Generate Recommendation Templates

For each deduplicated finding, generate a recommendation template based on its
category. Each template maps to specific config snippets and recovery actions.

**Category-to-recommendation mappings:**

##### budget-exceeded

- **Primary action**: Set `budget_preset: generous` in config.yaml
- **Secondary action**: Per-stage budget override for the specific stage
- **Tertiary action**: Size label correction if issue was under-sized
- **Config snippet**:

```yaml
# Option A: Apply generous preset (2x all budgets)
pipeline:
  budget_preset: generous

# Option B: Override specific stage budget
pipeline:
  stage_budgets:
    feature-dev:
      M: 16.00
      L: 50.00
```

##### state-management

- **Primary action**: Suggest model upgrade for the failing stage
- **Secondary action**: Suggest context file rebuild or state validation fix
- **Config snippet**:

```yaml
# Upgrade model for affected stage to improve state handling
model_routing:
  mode: hybrid
  minimum_model:
    <stage>: sonnet
pipeline:
  stage_models:
    <stage>: opus
```

##### ci-infrastructure

- **Primary action**: Suggest token refresh (`nightgauge forge auth login`)
- **Secondary action**: Suggest CI timeout increase
- **Config snippet**:

```yaml
# Increase CI timeout to accommodate slow runners
pipeline:
  ci_timeout: 600
```

- **Recovery command**: `nightgauge forge auth login` (configure via env var `GITHUB_TOKEN` for non-interactive)

##### model-capability

- **Primary action**: Suggest model upgrade (haiku → sonnet or sonnet → opus)
- **Config snippet**:

```yaml
# Upgrade model for affected stage
model_routing:
  mode: hybrid
  minimum_model:
    <stage>: sonnet
pipeline:
  stage_models:
    <stage>: opus
```

##### timeout

- **Primary action**: Suggest effort reduction or scope split
- **Secondary action**: Increase timeout config
- **Config snippet**:

```yaml
# Increase CI timeout
pipeline:
  ci_timeout: 600

# Or reduce effort for the stage
model_routing:
  stage_efforts:
    <stage>: low
```

##### validation-failure

- **Primary action**: Include specific test failure details from evidence
- **Secondary action**: Suggest running tests locally before pipeline
- **No config snippet** — requires code fixes
- **Recovery command**: `npm test` or relevant test command from evidence

##### unknown

- **Primary action**: Manual investigation with log file references
- **No config snippet**
- **Recovery command**: Review session log at
  `.nightgauge/logs/<date>_<issue>_session.log`

#### Step 7.3: Generate Recovery Guidance

For each failed issue with branch work, detect branch/PR state and generate
recovery commands:

```bash
# For each affected issue number:
BRANCH_NAME="feat/${ISSUE_NUM}-..."

# 1. Check if remote branch exists with commits
git branch -r --list "origin/${BRANCH_NAME}*"

# 2. Check for existing PR
nightgauge forge pr list --head "${BRANCH_NAME}" --state all --json number,state,url

# 3. Generate recovery command based on state:
#    - Branch with commits, no PR → nightgauge forge pr create --head <branch> --base main
#    - PR exists, not merged      → nightgauge forge pr merge --node-id $ID --strategy squash (or re-run CI)
#    - PR merged but pipeline killed → no recovery needed
#    - No branch found            → re-run pipeline from issue-pickup
```

#### Step 7.4: Assemble Recommendation Records

For each recommendation, produce a structured record:

```json
{
  "priority": 1,
  "action": "Increase budget for feature-dev stage",
  "impact": "Eliminates 2 budget-exceeded failures",
  "effort": "low",
  "category": "budget-exceeded",
  "config_snippet": "pipeline:\n  budget_preset: generous",
  "recovery_commands": ["nightgauge forge pr create --head feat/960-... --base main"],
  "affected_issues": [960, 963],
  "estimated_cost_savings_usd": 2.46
}
```

Sort recommendations by:

1. Severity (critical > high > medium > low > info)
2. Number of affected issues (descending)
3. Effort (low > medium > high — prefer quick wins)

---

### Phase 8: Issue Creation / Dry Run

**PURPOSE**: Preview or create GitHub issues for actionable recommendations.

#### Step 8.1: Filter by Severity Threshold

Only process recommendations at or above the `--severity` threshold (default:
`high`):

```bash
SEVERITY_ORDER="info low medium high critical"
# Filter recommendations where severity >= MIN_SEVERITY
```

#### Step 8.2: Dry Run Output (Default)

When `DRY_RUN=true` (default, or `--create-issues` not passed), output a preview
table:

```text
ISSUE CREATION PREVIEW (dry-run)
═══════════════════════════════════════════════════════════

  # │ Title                                  │ Labels              │ Issues │ Effort
  1 │ fix(pipeline): Increase feature-dev    │ chore, priority:hi  │ 2      │ low
    │   budget for budget-exceeded failures  │                     │        │
  2 │ fix(pipeline): Upgrade pr-create model │ chore, priority:hi  │ 1      │ low
    │   for state-management failures        │                     │        │

To create these issues: /nightgauge:retro --create-issues
To include medium+ severity: /nightgauge:retro --create-issues --severity medium
```

#### Step 8.3: Create Issues (`--create-issues`)

When `CREATE_ISSUES=true`:

For each recommendation at or above the severity threshold:

**Step 8.3.1: Check for duplicate issues**

```bash
# Search for existing issues with similar title
SEARCH_TITLE="fix(pipeline): ${ACTION_SUMMARY}"
EXISTING=$(nightgauge forge issue list --search "${SEARCH_TITLE}" --state open --json number,title --limit 5)

# If duplicate found, skip and report
if echo "$EXISTING" | jq -e 'length > 0' > /dev/null 2>&1; then
  echo "SKIP: Similar issue already exists: $(echo "$EXISTING" | jq -r '.[0] | "#\(.number) \(.title)"')"
  continue
fi
```

**Step 8.3.2: Create the issue**

```bash
ISSUE_BODY=$(cat <<'ISSUE_EOF'
## Summary

${ACTION_DESCRIPTION}

## Motivation

Pipeline retro analysis identified ${COUNT} failure(s) in category
\`${CATEGORY}\` affecting issue(s) ${AFFECTED_ISSUES_LIST}.

Estimated wasted cost: $${ESTIMATED_COST_SAVINGS_USD}

## Recommended Fix

${RECOMMENDATION_DETAIL}

### Config Change

\`\`\`yaml
${CONFIG_SNIPPET}
\`\`\`

### Recovery Commands

${RECOVERY_COMMANDS_LIST}

## Acceptance Criteria

- [ ] Config change applied and validated
- [ ] Affected issues re-run successfully (or manually recovered)
- [ ] No regression in pipeline success rate

## Technical Notes

- Category: \`${CATEGORY}\`
- Severity: ${SEVERITY}
- Affected issues: ${AFFECTED_ISSUES_LIST}
- Source: Pipeline retro report (${REPORT_DATE})

## Related Issues

${RELATED_ISSUES_REFS}

---

_Generated by \`/nightgauge:retro --create-issues\`_
ISSUE_EOF
)

nightgauge forge issue create \
  --title "fix(pipeline): ${ACTION_SUMMARY}" \
  --body "$ISSUE_BODY" \
  --label "chore" \
  --label "priority:high"
```

**Step 8.3.3: Report created issues**

```text
ISSUES CREATED
═══════════════════════════════════════════════════════════
  ✓ #1201  fix(pipeline): Increase feature-dev budget    (2 affected issues)
  ✓ #1202  fix(pipeline): Upgrade pr-create model        (1 affected issue)
  ✗ SKIP   fix(pipeline): Fix async handler errors       (duplicate: #1180)

Created: 2 issues | Skipped: 1 (duplicate)
```

---

### Phase 9: Record Outcome to Knowledge Base

**PURPOSE**: Append an `## Outcome` section to the knowledge base for the
completed issue, closing the knowledge loop started by issue-pickup (scaffold)
and feature-planning (decisions). This section becomes corpus for the
pipeline learning system.

**No-op when**:

- `RECORD_OUTCOME` is false, AND
- `KNOWLEDGE_PATH` is empty or unset

#### Step 9.1: Load Pipeline Context for Outcome Data

When `ISSUE_FILTER` is set and `RECORD_OUTCOME=true`:

```bash
ISSUE_NUMBER="${ISSUE_FILTER}"

# Load outcome metrics from dev context (best-effort)
DEV_CONTEXT=".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json"
PIPELINE_DURATION_MINS=0
TOTAL_TOKENS=0
ESTIMATED_COST_USD=0
OUTCOME_STATUS="complete"

if [ -f "$DEV_CONTEXT" ]; then
  TOTAL_TOKENS=$(jq -r '.token_usage.total // 0' "$DEV_CONTEXT" 2>/dev/null || echo "0")
  ESTIMATED_COST_USD=$(jq -r '.token_usage.estimated_cost_usd // 0' "$DEV_CONTEXT" 2>/dev/null || echo "0")
fi

# Agent fills in narrative from the retro analysis above:
# - WHAT_WENT_WELL: positive signals, stages that completed quickly, tests passing
# - WHAT_DIDNT: failures, retries, unexpected scope, CI issues
# - LESSONS_LEARNED: actionable guidance for future similar work
WHAT_WENT_WELL=""
WHAT_DIDNT=""
LESSONS_LEARNED=""
```

#### Step 9.1b: Graduation Candidates (auto-detected)

When `ISSUE_NUMBER` is set, call `nightgauge knowledge graduate-candidates`
to deterministically rank ADR blocks in this issue's `decisions.md`. The
binary uses telemetry signals (recall_hit events) plus structural heuristics
to score each ADR and returns only those at or above the threshold.

```bash
GRADUATION_SECTION=""
if [ -n "$ISSUE_NUMBER" ]; then
  CANDIDATES_JSON=$(nightgauge knowledge graduate-candidates "$ISSUE_NUMBER" --json 2>/dev/null || echo '{"issue":0,"candidates":[]}')
  CAND_COUNT=$(echo "$CANDIDATES_JSON" | jq '.candidates | length' 2>/dev/null || echo 0)
  if [ "$CAND_COUNT" -gt 0 ]; then
    GRADUATION_SECTION=$(echo "$CANDIDATES_JSON" | jq -r '
      "## Graduation Candidates\n\nThese ADRs from this issue look like good candidates to graduate to permanent docs. Run `nightgauge knowledge graduate \(.issue) --section <docs-path> --adr ADR-NNN` to start the ritual.\n\n| ADR | Score | Signals | Suggested Dest |\n|-----|-------|---------|----------------|\n" +
      (.candidates | map("| \"\(.adr_title)\" (ADR-\(.adr_index | tostring | (if length < 3 then ("000" + .) | .[-3:] else . end))) | \(.score) | \(.signals | join(", ")) | \(.suggested_dest) |") | join("\n"))
    ')
  fi
fi
```

The agent appends `$GRADUATION_SECTION` to the retro summary output when set.
When `CAND_COUNT == 0`, the section is omitted entirely — no static
"Graduation checklist" blockquote is emitted. The candidate threshold and
scoring rubric live in
[docs/GO_BINARY.md](../../docs/GO_BINARY.md#knowledge-graduate-candidates).

> **Manual fallback**: When the binary is unavailable on stale checkouts the
> command silently returns an empty list; reviewers can still scan
> `decisions.md` manually and run `nightgauge knowledge graduate <issue>
--section <docs-path> --adr ADR-NNN` to start the ritual. See
> [docs/KNOWLEDGE_BASE.md#graduation-workflow](../../docs/KNOWLEDGE_BASE.md#graduation-workflow).

**Content guidelines** — the agent fills in the narrative fields:

| Field             | Source                                             | Format                             |
| ----------------- | -------------------------------------------------- | ---------------------------------- |
| `What Went Well`  | Phases 3–5 analysis: stages that completed cleanly | 2–5 bullet points or short prose   |
| `What Didn't`     | Failure events, retries, validation failures       | 2–5 bullet points or "None" if N/A |
| `Lessons Learned` | Actionable guidance distilled from the above       | 2–5 actionable bullet points       |

When the retro scope had **no failures** for the issue (all stages completed),
`What Didn't Go Well` should be "None — all stages completed successfully."

#### Step 9.2: Record Outcome via Go Binary

Call the deterministic Go binary to append the outcome block. The binary
handles directory creation, file selection (prefers `decisions.md`), idempotency
detection, and Markdown formatting:

```bash
nightgauge knowledge record-outcome \
  --issue "$ISSUE_NUMBER" \
  --status "$OUTCOME_STATUS" \
  --duration "$PIPELINE_DURATION_MINS" \
  --tokens "$TOTAL_TOKENS" \
  --cost "$ESTIMATED_COST_USD" \
  --what-went-well "$WHAT_WENT_WELL" \
  --what-didnt "$WHAT_DIDNT" \
  --lessons-learned "$LESSONS_LEARNED"
```

Exit code 0 = success (appended or idempotent no-op). Exit code 1 = error
(invalid status, permission failure, etc.). The binary prints the target file
path on success; errors go to stderr.

---

### Phase 10: Post-Epic Synthesis (Skill Feedback Loop)

**PURPOSE**: After an epic completes (all sub-issues merged), aggregate findings
across all sub-issues to detect systemic patterns that individual retros miss.
This closes the feedback loop: epic failures → pattern detection → skill/config
improvements → fewer failures next epic.

**Trigger**: Runs when `--epic N` is passed OR when batch state shows an epic
with all sub-issues complete.

**No-op when**: No epic context, or epic is still in progress.

#### Step 10.1: Gather Cross-Issue Data

```bash
EPIC_NUMBER="${ARG_EPIC:-}"

# Auto-detect epic if not specified
if [ -z "$EPIC_NUMBER" ] && [ -f ".nightgauge/pipeline/batch-state.json" ]; then
  EPIC_NUMBER=$(jq -r '.epic_number // empty' .nightgauge/pipeline/batch-state.json 2>/dev/null)
fi

if [ -z "$EPIC_NUMBER" ]; then
  echo "No epic context — skipping post-epic synthesis"
else
  echo "Running post-epic synthesis for epic #$EPIC_NUMBER..."

  # Collect all assessment records for this epic's sub-issues
  ASSESSMENT_DIR=".nightgauge/pipeline/assessments"
  RETRO_DIR=".nightgauge/retros"
  HISTORY_DIR=".nightgauge/pipeline/history"

  # Get sub-issue numbers from batch state or GitHub
  SUB_ISSUES=$(jq -r '.issues[]?.number // empty' .nightgauge/pipeline/batch-state.json 2>/dev/null)
  if [ -z "$SUB_ISSUES" ]; then
    SUB_ISSUES=$(nightgauge forge issue view "$EPIC_NUMBER" --json body -q '.body' 2>/dev/null | \
      grep -oE '#[0-9]+' | grep -oE '[0-9]+' | sort -u)
  fi

  echo "Sub-issues: $SUB_ISSUES"
fi
```

#### Step 10.2: Aggregate Patterns Across Sub-Issues

```bash
if [ -n "$EPIC_NUMBER" ] && [ -n "$SUB_ISSUES" ]; then
  # Collect all assessment records
  ASSESSMENT_FILES=""
  for ISSUE_NUM in $SUB_ISSUES; do
    for f in "$ASSESSMENT_DIR"/*-"$ISSUE_NUM".json; do
      [ -f "$f" ] && ASSESSMENT_FILES="$ASSESSMENT_FILES $f"
    done
  done

  # Collect all retro records
  RETRO_FILES=""
  for ISSUE_NUM in $SUB_ISSUES; do
    for f in "$RETRO_DIR"/*_"$ISSUE_NUM"_retro.json; do
      [ -f "$f" ] && RETRO_FILES="$RETRO_FILES $f"
    done
  done

  # Parse and aggregate with Python3
  SYNTHESIS_RESULT=$(python3 << 'PYEOF'
import json, glob, sys, os
from collections import defaultdict

assessment_files = os.environ.get("ASSESSMENT_FILES", "").split()
retro_files = os.environ.get("RETRO_FILES", "").split()

# Aggregate friction patterns from assessments
friction_patterns = defaultdict(lambda: {"count": 0, "issues": set(), "suggestions": [], "severity": "low"})

for f in assessment_files:
    if not f or not os.path.isfile(f):
        continue
    try:
        with open(f) as fh:
            data = json.load(fh)
        issue_num = data.get("issue_number", 0)
        for friction in data.get("friction", []):
            key = f"{friction.get('type', 'unknown')}:{friction.get('description', '')[:80]}"
            friction_patterns[key]["count"] += 1
            friction_patterns[key]["issues"].add(issue_num)
            if friction.get("suggested_fix"):
                friction_patterns[key]["suggestions"].append(friction["suggested_fix"])
            sev = friction.get("severity", "low")
            if sev == "high" or (sev == "medium" and friction_patterns[key]["severity"] == "low"):
                friction_patterns[key]["severity"] = sev
    except (json.JSONDecodeError, KeyError):
        continue

# Aggregate failure categories from retros
failure_categories = defaultdict(lambda: {"count": 0, "issues": set()})
for f in retro_files:
    if not f or not os.path.isfile(f):
        continue
    try:
        with open(f) as fh:
            data = json.load(fh)
        issue_num = data.get("issue_number", 0)
        for finding in data.get("findings", []):
            cat = finding.get("category", "unknown")
            failure_categories[cat]["count"] += 1
            failure_categories[cat]["issues"].add(issue_num)
    except (json.JSONDecodeError, KeyError):
        continue

# Build synthesis output
recurring = []
for pattern, info in friction_patterns.items():
    info["issues"] = sorted(info["issues"])
    if info["count"] >= 2:  # Two-strike rule
        recurring.append({
            "pattern": pattern,
            "count": info["count"],
            "affected_issues": info["issues"],
            "severity": info["severity"],
            "suggested_fix": info["suggestions"][0] if info["suggestions"] else None,
        })

recurring.sort(key=lambda x: (-{"high": 3, "medium": 2, "low": 1}.get(x["severity"], 0), -x["count"]))

result = {
    "recurring_friction": recurring,
    "failure_categories": {k: {"count": v["count"], "issues": sorted(v["issues"])} for k, v in failure_categories.items()},
    "total_assessments": len(assessment_files),
    "total_retros": len(retro_files),
}
print(json.dumps(result, indent=2))
PYEOF
  ) || SYNTHESIS_RESULT='{}'

  echo "$SYNTHESIS_RESULT"
fi
```

#### Step 10.3: Generate Improvement Proposals

For each **recurring** friction pattern (count >= 2), generate a
`SkillImprovementProposal`:

```bash
if [ -n "$EPIC_NUMBER" ] && [ -n "$SYNTHESIS_RESULT" ]; then
  RECURRING_COUNT=$(echo "$SYNTHESIS_RESULT" | jq '.recurring_friction | length' 2>/dev/null || echo "0")

  if [ "$RECURRING_COUNT" -gt 0 ]; then
    echo "=== RECURRING SKILL FRICTION (${RECURRING_COUNT} patterns) ==="
    echo "$SYNTHESIS_RESULT" | jq -r '.recurring_friction[] |
      "[\(.severity | ascii_upcase)] \(.pattern) — seen \(.count)x across issues \(.affected_issues | join(", "))
      Fix: \(.suggested_fix // "No suggestion")\n"'

    # Write synthesis to assessments dir for dashboard consumption
    SYNTHESIS_FILE="$ASSESSMENT_DIR/synthesis-epic-${EPIC_NUMBER}.json"
    mkdir -p "$ASSESSMENT_DIR"
    echo "$SYNTHESIS_RESULT" | jq --arg epic "$EPIC_NUMBER" \
      '. + {"epic_number": ($epic | tonumber), "synthesized_at": (now | todate)}' \
      > "$SYNTHESIS_FILE"
    echo "Synthesis written to: $SYNTHESIS_FILE"
  else
    echo "No recurring friction patterns detected across epic #$EPIC_NUMBER sub-issues."
  fi
fi
```

#### Step 10.4: Auto-Create Improvement Issues (When Configured)

When `--create-issues` is active and recurring friction has `severity >= medium`:

```bash
if [ "$CREATE_ISSUES" = "true" ] && [ "$RECURRING_COUNT" -gt 0 ]; then
  echo "$SYNTHESIS_RESULT" | jq -c '.recurring_friction[] | select(.severity == "high" or .severity == "medium")' | \
  while IFS= read -r proposal; do
    PATTERN=$(echo "$proposal" | jq -r '.pattern')
    FIX=$(echo "$proposal" | jq -r '.suggested_fix // "Review and update skill instructions"')
    COUNT=$(echo "$proposal" | jq -r '.count')
    ISSUES=$(echo "$proposal" | jq -r '.affected_issues | map("#" + tostring) | join(", ")')
    SEVERITY=$(echo "$proposal" | jq -r '.severity')

    TITLE="fix(skills): ${PATTERN}"
    BODY="## Recurring Skill Friction

**Pattern**: \`${PATTERN}\`
**Severity**: ${SEVERITY}
**Occurrences**: ${COUNT} (across ${ISSUES})
**Source**: Post-epic synthesis for epic #${EPIC_NUMBER}

## Suggested Fix

${FIX}

## Evidence

Detected by self-assessment epilogues across multiple pipeline runs.
See \`.nightgauge/pipeline/assessments/synthesis-epic-${EPIC_NUMBER}.json\` for details.

---
*Auto-created by /nightgauge:retro --epic ${EPIC_NUMBER} --create-issues*"

    # Check for duplicate
    EXISTING=$(nightgauge forge issue list --search "\"${PATTERN}\" label:skill-drift" --json number -q '.[0].number' 2>/dev/null)
    if [ -n "$EXISTING" ]; then
      echo "Duplicate exists: #$EXISTING — skipping"
    else
      if [ "$DRY_RUN" = "true" ]; then
        echo "[DRY RUN] Would create: $TITLE"
      else
        nightgauge forge issue create --title "$TITLE" --body "$BODY" --label "skill-drift,priority:medium" 2>/dev/null || \
          echo "WARNING: Failed to create issue for pattern: $PATTERN"
      fi
    fi
  done
fi
```

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Output Format

### JSON Schema

```json
{
  "schema_version": "string (1.0)",
  "scope": {
    "type": "string (batch | date-range | single-issue)",
    "from": "string (YYYY-MM-DD)",
    "to": "string (YYYY-MM-DD)",
    "issue_filter": "number | null",
    "issues_analyzed": "number",
    "failure_count": "number",
    "data_sources": "string[]"
  },
  "summary": {
    "failure_rate": "number (0-1)",
    "total_wasted_cost_usd": "number",
    "most_common_category": "string",
    "stage_hotspot": "string",
    "categories_found": "string[]"
  },
  "failures": [
    {
      "issue_number": "number",
      "title": "string",
      "categories": "string[]",
      "confidence": "string (high | medium | low)",
      "evidence": "string[]",
      "failed_stages": "string[]",
      "estimated_cost_usd": "number"
    }
  ],
  "findings": [
    {
      "category": "string",
      "severity": "string (critical | high | medium | low | info)",
      "count": "number",
      "affected_issues": "number[]",
      "title": "string",
      "root_cause": "string",
      "pattern": "string",
      "recommendation": "string",
      "estimated_recurrence_risk": "string (high | medium | low)"
    }
  ],
  "recommendations": [
    {
      "priority": "number",
      "action": "string",
      "impact": "string",
      "effort": "string (low | medium | high)",
      "category": "string",
      "config_snippet": "string | null",
      "recovery_commands": "string[]",
      "affected_issues": "number[]",
      "estimated_cost_savings_usd": "number"
    }
  ],
  "created_at": "string (ISO-8601)"
}
```

---

## Error Handling

| Condition                           | Action                                                                    |
| ----------------------------------- | ------------------------------------------------------------------------- |
| No data sources found               | Output friendly message listing expected paths, exit 0                    |
| No failures found in scope          | Output "No failures found", list total runs analyzed                      |
| Malformed JSONL records             | Skip bad records, report count at end                                     |
| Malformed log lines                 | Skip unreadable lines, continue scanning                                  |
| python3 not installed               | Error with install instructions, exit 1                                   |
| forge auth not configured           | Error with `nightgauge forge auth login` instructions                     |
| `--create-issues` without findings  | Output "No findings at severity threshold"                                |
| `--issue N` not found in any source | Output "Issue #N not found in available data sources"                     |
| Duplicate finding exists            | Skip creation, report as already tracked                                  |
| `issue create-sub` label error      | Use `nightgauge forge graphql` `addLabelsToLabelable` mutation separately |
| Batch state and history both empty  | Fall back to context file scan, note limited coverage                     |

---

## Pipeline Position

```
UTILITIES (not part of main pipeline)

/nightgauge:retro
       ↑
  Use after batch runs or periodically to triage failures
  Also records outcome data to knowledge base when knowledge_path is set
  Reads:  .nightgauge/logs/*_session.log
  Reads:  .nightgauge/pipeline/batch-state.json
  Reads:  .nightgauge/pipeline/history/*.jsonl
  Reads:  .nightgauge/pipeline/{stage}-{N}.json
  Reads:  .nightgauge/pipeline/issue-{N}.json (for knowledge_path)
  Reads:  .nightgauge/pipeline/assessments/*.json (--epic, --skill-feedback)
  Reads:  .nightgauge/retros/*_retro.json (--epic cross-issue aggregation)
  Writes: .nightgauge/pipeline/retro-report-YYYY-MM-DD.json (optional)
  Writes: .nightgauge/pipeline/assessments/synthesis-epic-{N}.json (--epic)
  Writes: {knowledge_path}/decisions.md or {knowledge_path}/outcomes.md (when knowledge_path set)
  Creates: GitHub issues via nightgauge forge issue create (--create-issues only)
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs.

---

## Configuration

This skill reads configuration from `.nightgauge/config.yaml`:

| Config Key               | Default | Description                         |
| ------------------------ | ------- | ----------------------------------- |
| `project.number`         | -       | GitHub Project number for issues    |
| `retro.default_period`   | `7`     | Default `--period` value (days)     |
| `retro.default_severity` | `high`  | Default severity for issue creation |
| `retro.auto_create`      | `false` | Auto-create issues without flag     |

---

**Author:** nightgauge **License:** Apache-2.0
