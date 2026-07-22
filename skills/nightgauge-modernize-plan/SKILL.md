---
name: nightgauge-modernize-plan
description: Modernization plan generator that consumes health-check, security-audit, and
  test-scaffold outputs to produce a prioritized, phased roadmap. Use after
  running assessment skills to get an actionable improvement plan.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
---

# Nightgauge Modernize Plan

## Description

Modernization plan generator that consumes structured JSON output from three
assessment skills (health-check, security-audit, test-scaffold) and produces a
prioritized, phased modernization roadmap. Turns raw assessment data into
actionable tasks with effort estimates, risk assessment, dependency ordering,
and optional GitHub issue generation.

**Use Cases:**

- Generating a prioritized improvement roadmap after running assessments
- Creating sprint-ready modernization tasks from health-check findings
- Planning tech debt reduction with effort estimates and timelines
- Producing executive-ready reports with quick wins highlighted
- Automating GitHub issue creation for modernization tasks

**When to Use:**

- After running `/nightgauge:health-check` and/or
  `/nightgauge:security-audit` and/or `/nightgauge:test-scaffold`
- When planning a brownfield modernization effort
- When producing stakeholder-facing improvement roadmaps
- When converting assessment findings into tracked work items

**Relationship to Other Skills:**

| Skill          | Purpose                              | Relationship               |
| -------------- | ------------------------------------ | -------------------------- |
| Health Check   | Codebase quality assessment (6 dims) | Input producer (upstream)  |
| Security Audit | Security posture assessment (7 dims) | Input producer (upstream)  |
| Test Scaffold  | Coverage gap analysis + test gen     | Input producer (upstream)  |
| Modernize Plan | Phased roadmap from assessments      | **Consumer (this skill)**  |
| Issue Create   | Create GitHub issues                 | Pattern reference (issues) |

**Architecture Pattern:**

This skill follows the **deterministic vs probabilistic** principle:

- **Deterministic**: Dependency ordering uses topological sort for predictable,
  debuggable task sequencing
- **Probabilistic**: Finding classification, severity mapping, and plan
  narrative generation use AI interpretation

## Invocation

| Tool        | Command                                                    |
| ----------- | ---------------------------------------------------------- |
| Claude Code | `/nightgauge:modernize-plan [options]`                     |
| Copilot     | Invoke via Agent Skills extension                          |
| Cursor      | Run via Agent Skills or direct SKILL.md                    |
| Standalone  | `claude --skill skills/nightgauge-modernize-plan/SKILL.md` |

## Arguments

| Argument              | Description                                          | Default |
| --------------------- | ---------------------------------------------------- | ------- |
| `--path DIR`          | Root directory containing assessment outputs         | `.`     |
| `--format FORMAT`     | Output format: `summary`, `json`, `both`             | `both`  |
| `--create-issues`     | Generate GitHub issues for plan tasks                | `false` |
| `--dry-run`           | Show what issues would be created without creating   | `false` |
| `--team-size N`       | Team size for timeline estimates                     | `1`     |
| `--sprint-length N`   | Sprint length in weeks                               | `2`     |
| `--output FILE`       | Custom JSON output path                              | auto    |
| `--skip-phase PHASES` | Comma-separated phase numbers to exclude (e.g., 0,5) | -       |

### Examples

```bash
# Generate modernization plan from all available assessments
/nightgauge:modernize-plan

# Generate plan for specific directory
/nightgauge:modernize-plan --path /path/to/project

# JSON output only
/nightgauge:modernize-plan --format json

# Preview issues that would be created (no side effects)
/nightgauge:modernize-plan --dry-run

# Create GitHub issues for all plan tasks
/nightgauge:modernize-plan --create-issues

# Estimate timeline for a team of 3 with 2-week sprints
/nightgauge:modernize-plan --team-size 3 --sprint-length 2

# Skip Phase 0 (Safety Net) and Phase 5 (Optimization)
/nightgauge:modernize-plan --skip-phase 0,5
```

---

## Prerequisites

- Bash shell
- `jq` installed (for JSON processing)
- At least one assessment output file must exist:
  - `.nightgauge/health-report.json` (from health-check)
  - `.nightgauge/security-audit.json` (from security-audit)
  - `.nightgauge/test-scaffold-report.json` (from test-scaffold)
- `gh` CLI authenticated (only required if `--create-issues` is used)

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion unless
`--create-issues` is specified (for user confirmation before creating issues).
Make autonomous decisions or fail with clear error.

---

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Load Assessment Data

#### Step 1.1: Parse Arguments

Extract options from invocation:

```bash
ASSESS_PATH="."
OUTPUT_FORMAT="both"
CREATE_ISSUES=false
DRY_RUN=false
TEAM_SIZE=1
SPRINT_LENGTH=2
OUTPUT_FILE=""
SKIP_PHASES=""

# Parse arguments from invocation
# --path DIR: set ASSESS_PATH
# --format FORMAT: set OUTPUT_FORMAT
# --create-issues: set CREATE_ISSUES=true
# --dry-run: set DRY_RUN=true
# --team-size N: set TEAM_SIZE
# --sprint-length N: set SPRINT_LENGTH
# --output FILE: set OUTPUT_FILE
# --skip-phase PHASES: set SKIP_PHASES (comma-separated)
```

#### Step 1.2: Check for Assessment Outputs

```bash
cd "$ASSESS_PATH"

HEALTH_REPORT=".nightgauge/health-report.json"
SECURITY_REPORT=".nightgauge/security-audit.json"
SCAFFOLD_REPORT=".nightgauge/test-scaffold-report.json"

HEALTH_AVAILABLE=false
SECURITY_AVAILABLE=false
SCAFFOLD_AVAILABLE=false
INPUTS_FOUND=0

if [ -f "$HEALTH_REPORT" ]; then
  HEALTH_AVAILABLE=true
  INPUTS_FOUND=$((INPUTS_FOUND + 1))
  echo "Health check report found: $HEALTH_REPORT"
fi

if [ -f "$SECURITY_REPORT" ]; then
  SECURITY_AVAILABLE=true
  INPUTS_FOUND=$((INPUTS_FOUND + 1))
  echo "Security audit report found: $SECURITY_REPORT"
fi

if [ -f "$SCAFFOLD_REPORT" ]; then
  SCAFFOLD_AVAILABLE=true
  INPUTS_FOUND=$((INPUTS_FOUND + 1))
  echo "Test scaffold report found: $SCAFFOLD_REPORT"
fi

if [ "$INPUTS_FOUND" -eq 0 ]; then
  echo "ERROR: No assessment outputs found."
  echo ""
  echo "Run at least one assessment skill first:"
  echo "  /nightgauge:health-check"
  echo "  /nightgauge:security-audit"
  echo "  /nightgauge:test-scaffold"
  echo ""
  echo "Expected files:"
  echo "  $HEALTH_REPORT"
  echo "  $SECURITY_REPORT"
  echo "  $SCAFFOLD_REPORT"
  exit 1
fi

echo "Inputs found: $INPUTS_FOUND of 3"
```

#### Step 1.3: Load Available Reports

For each available report, read and parse the JSON content:

```bash
if [ "$HEALTH_AVAILABLE" = true ]; then
  HEALTH_SCORE=$(jq -r '.summary.overall_health_score // "N/A"' \
    "$HEALTH_REPORT" 2>/dev/null)
  HEALTH_STATUS=$(jq -r '.summary.status // "N/A"' \
    "$HEALTH_REPORT" 2>/dev/null)
  echo "Health check score: $HEALTH_SCORE ($HEALTH_STATUS)"
fi

if [ "$SECURITY_AVAILABLE" = true ]; then
  SECURITY_SCORE=$(jq -r '.summary.overall_security_score // "N/A"' \
    "$SECURITY_REPORT" 2>/dev/null)
  SECURITY_STATUS=$(jq -r '.summary.status // "N/A"' \
    "$SECURITY_REPORT" 2>/dev/null)
  echo "Security audit score: $SECURITY_SCORE ($SECURITY_STATUS)"
fi

if [ "$SCAFFOLD_AVAILABLE" = true ]; then
  SCAFFOLD_GAPS=$(jq -r '.summary.critical_gaps_found // "N/A"' \
    "$SCAFFOLD_REPORT" 2>/dev/null)
  SCAFFOLD_COVERAGE=$(jq -r '.summary.coverage_before // "N/A"' \
    "$SCAFFOLD_REPORT" 2>/dev/null)
  echo "Test scaffold: $SCAFFOLD_GAPS critical gaps, ${SCAFFOLD_COVERAGE}% coverage"
fi
```

#### Step 1.4: Detect Codebase Metadata

```bash
CODEBASE_NAME=$(basename "$(pwd)")
ROOT_PATH=$(pwd)

# Detect from health-check if available
if [ "$HEALTH_AVAILABLE" = true ]; then
  CODEBASE_NAME=$(jq -r '.codebase.name // empty' \
    "$HEALTH_REPORT" 2>/dev/null)
  [ -z "$CODEBASE_NAME" ] && CODEBASE_NAME=$(basename "$(pwd)")
fi

echo "Codebase: $CODEBASE_NAME"
echo "Root path: $ROOT_PATH"
```

---

### Phase 2: Analyze and Classify Findings

#### Steps 2.1–2.4: Aggregate Findings (Go binary)

Run the deterministic Go binary to read all three assessment reports, apply
severity normalization, and deduplicate overlapping findings. This replaces the
manual shell+jq extraction previously in Steps 2.1–2.4 (audit row B31).

```bash
AGGREGATE_OUT=$(mktemp /tmp/aggregate-findings-XXXXXX.json)
nightgauge modernize aggregate-findings \
  --workdir "$ASSESS_PATH" \
  --out "$AGGREGATE_OUT"

if [ $? -ne 0 ]; then
  echo "ERROR: aggregate-findings failed" >&2
  exit 1
fi

echo "Aggregate findings written to: $AGGREGATE_OUT"
cat "$AGGREGATE_OUT" | jq '.summary'
```

The binary reads:

- `.nightgauge/health-report.json` — severity mapped from status string
  (`critical`→critical, `poor`→high, `fair`→medium, `good`→low, `excellent`→info)
- `.nightgauge/security-audit.json` — severity passed through unchanged
- `.nightgauge/test-scaffold-report.json` — priority mapped 1:1 to severity

Missing files are reported in `sources_missing` (not an error). All absent → exit 2.

Downstream steps read `$AGGREGATE_OUT` using `jq` as shown in the examples below.
The schema is stable v1 — see `docs/GO_BINARY.md` → _Modernize — Assessment Aggregation_
for the full field reference.

```bash
# Read deduplicated findings for downstream phase assignment
jq '.findings[]' "$AGGREGATE_OUT"

# Summary counts by severity
jq '.summary.by_severity' "$AGGREGATE_OUT"

# Filter to critical and high findings only
jq '[.findings[] | select(.severity == "critical" or .severity == "high")]' "$AGGREGATE_OUT"
```

#### Step 2.5: Map Findings to Modernization Tasks

For each unique finding, create a modernization task with:

- `id` — Format: `task-{phase}{sequence}` (e.g., `task-101`, `task-203`)
- `title` — Short actionable description
- `description` — Detailed explanation of what needs to be done
- `rationale` — Why this matters (business impact)
- `phase` — Assign to phase 0-5 based on classification rules (see Phase 4)
- `effort` — Estimate: `XS`, `S`, `M`, `L`, `XL`
- `risk` — Assessment: `low`, `medium`, `high`, `critical`
- `dependencies` — Array of task IDs this depends on
- `execution_method` — `manual`, `automated`, `ai-assisted`
- `source` — Which assessment skill identified this
- `source_dimension` — Which dimension (e.g., `dependency_health`)
- `source_finding_index` — Index into source findings array

**Phase Assignment Rules:**

| Finding Type                                 | Phase | Name           |
| -------------------------------------------- | ----- | -------------- |
| Missing test coverage, no test framework     | 0     | Safety Net     |
| Test scaffold gaps (critical/high priority)  | 0     | Safety Net     |
| Critical/high security vulnerabilities       | 1     | Critical Fixes |
| Hardcoded secrets                            | 1     | Critical Fixes |
| Critical dependency vulnerabilities          | 1     | Critical Fixes |
| Build system issues (CI/CD, lockfile)        | 2     | Foundation     |
| Dependency health (outdated, audit)          | 2     | Foundation     |
| Missing documentation (README, docs/)        | 2     | Foundation     |
| Linting/formatting not configured            | 3     | Quality        |
| Code quality issues (TODOs, large files)     | 3     | Quality        |
| Medium security findings (OWASP patterns)    | 3     | Quality        |
| Tech debt (deprecated APIs, legacy patterns) | 4     | Modernization  |
| Architecture improvements                    | 4     | Modernization  |
| Input validation improvements                | 4     | Modernization  |
| Performance optimization                     | 5     | Optimization   |
| Advanced security hardening (headers, CORS)  | 5     | Optimization   |
| Low/info severity findings                   | 5     | Optimization   |

**Effort Estimation Rules:**

| Effort | Criteria                                                   |
| ------ | ---------------------------------------------------------- |
| `XS`   | Single command or config change (e.g., `npm audit fix`)    |
| `S`    | Single file change, <30 min (e.g., add .gitignore entry)   |
| `M`    | Multiple file changes, 1-4 hours (e.g., add linter config) |
| `L`    | Multi-file refactor, 4-16 hours (e.g., add test coverage)  |
| `XL`   | Major effort, 16+ hours (e.g., migrate auth system)        |

---

### Phase 3: Build Dependency Graph (Deterministic)

This phase uses deterministic topological sort — no AI interpretation.

#### Step 3.1: Create Adjacency List

Build a directed graph from task dependencies:

```
For each task T:
  For each dependency D in T.dependencies:
    Add edge D → T (D must complete before T)
```

#### Step 3.2: Detect Cycles

Run cycle detection on the dependency graph. If a cycle is found:

```bash
echo "ERROR: Dependency cycle detected in modernization plan."
echo "Cycle: task-101 → task-203 → task-101"
echo "Please review task dependencies and break the cycle."
exit 1
```

#### Step 3.3: Topological Sort

Perform topological sort to determine execution order within each phase. Tasks
within the same phase that have no dependencies on each other can be executed in
parallel.

#### Step 3.4: Group by Phase

Group sorted tasks by phase number (0-5), preserving topological order within
each phase. Apply `--skip-phase` filter to exclude requested phases.

---

### Phase 4: Generate Phased Plan

Six phases, each with clear goals:

| Phase | Name           | Goal                                          |
| ----- | -------------- | --------------------------------------------- |
| 0     | Safety Net     | Establish test coverage before changes        |
| 1     | Critical Fixes | Address security vulnerabilities and blockers |
| 2     | Foundation     | Fix build system, CI/CD, dependency health    |
| 3     | Quality        | Code quality, linting, formatting, docs       |
| 4     | Modernization  | Architecture improvements, tech debt payoff   |
| 5     | Optimization   | Performance, caching, advanced improvements   |

For each phase, compile:

- Phase name and description
- Tasks sorted by topological order
- Total story points for the phase
- Estimated sprints for the phase

---

### Phase 5: Identify Quick Wins

Filter tasks that meet ALL criteria:

- Effort is `XS` or `S`
- Risk is `low` or `medium`

Sort quick wins by impact (derived from the severity of the original finding —
higher severity = higher impact).

Highlight the top 5-10 quick wins. These are prominently positioned after the
executive summary in the output to build momentum.

---

### Phase 6: Estimate Timeline

#### Step 6.1: Map Effort to Story Points

| Effort | Story Points |
| ------ | ------------ |
| `XS`   | 1            |
| `S`    | 2            |
| `M`    | 3            |
| `L`    | 5            |
| `XL`   | 8            |

#### Step 6.2: Calculate Sprint Capacity

```
sprint_capacity = team_size * sprint_length * 5  (story points per sprint)
```

Where `5` represents an average velocity of 5 story points per developer per
week.

#### Step 6.3: Compute Timeline per Phase

For each phase:

```
phase_sprints = ceil(phase_story_points / sprint_capacity)
```

#### Step 6.4: Total Duration

```
total_sprints = sum(phase_sprints for all phases)
total_weeks = total_sprints * sprint_length
```

---

### Phase 7: Write Output Files

#### Step 7.1: Write JSON Output

Ensure the `.nightgauge/` directory exists, then write the structured plan
to `.nightgauge/modernization-plan.json` (or custom `--output` path):

```bash
mkdir -p .nightgauge
```

**JSON Schema** (`modernization-plan.json`):

```json
{
  "schema_version": "1.0",
  "generated_at": "ISO-8601",
  "codebase": {
    "name": "string",
    "root_path": "string"
  },
  "inputs": {
    "health_check": {
      "available": true,
      "path": ".nightgauge/health-report.json",
      "overall_score": 72
    },
    "security_audit": {
      "available": true,
      "path": ".nightgauge/security-audit.json",
      "overall_score": 65
    },
    "test_scaffold": {
      "available": false,
      "path": null,
      "overall_score": null
    }
  },
  "summary": {
    "total_tasks": 24,
    "tasks_by_phase": { "0": 3, "1": 5, "2": 6, "3": 4, "4": 4, "5": 2 },
    "tasks_by_effort": { "XS": 4, "S": 8, "M": 6, "L": 4, "XL": 2 },
    "tasks_by_risk": {
      "low": 6,
      "medium": 10,
      "high": 6,
      "critical": 2
    },
    "total_story_points": 78,
    "quick_wins_count": 8,
    "estimated_sprints": 6,
    "estimated_weeks": 12
  },
  "quick_wins": [
    {
      "task_id": "task-101",
      "title": "Run npm audit fix to resolve known vulnerabilities",
      "effort": "XS",
      "impact": "Eliminates 3 known CVEs",
      "phase": 1
    }
  ],
  "phases": [
    {
      "phase_number": 0,
      "name": "Safety Net",
      "description": "Establish test coverage before making changes",
      "tasks": [
        {
          "id": "task-001",
          "title": "Add characterization tests for critical paths",
          "description": "Generate scaffold tests for untested critical paths identified by test-scaffold report",
          "rationale": "Prevents regressions during subsequent modernization phases",
          "effort": "M",
          "risk": "low",
          "dependencies": [],
          "execution_method": "ai-assisted",
          "source": "test-scaffold",
          "source_dimension": "gaps",
          "source_finding_index": 0
        }
      ],
      "total_story_points": 8,
      "estimated_sprints": 1
    }
  ],
  "dependency_graph": {
    "nodes": ["task-001", "task-002"],
    "edges": [["task-001", "task-003"]]
  },
  "timeline": {
    "team_size": 1,
    "sprint_length_weeks": 2,
    "phases": [
      {
        "phase": 0,
        "sprints": 1,
        "start_sprint": 1,
        "end_sprint": 1
      }
    ],
    "total_sprints": 6,
    "total_weeks": 12
  },
  "issues_created": [],
  "created_at": "ISO-8601"
}
```

#### Step 7.2: Verify JSON

```bash
python3 -m json.tool .nightgauge/modernization-plan.json > /dev/null && \
  echo "Plan written: .nightgauge/modernization-plan.json"
```

#### Step 7.3: Write Markdown Output

Write `.nightgauge/MODERNIZATION_PLAN.md` with the following structure:

```markdown
# Modernization Plan: {codebase_name}

Generated: {date} Team Size: {team_size} | Sprint Length: {sprint_length} weeks

## Executive Summary

| Input          | Score | Status  |
| -------------- | ----- | ------- |
| Health Check   | 72    | Good    |
| Security Audit | 65    | Fair    |
| Test Scaffold  | N/A   | Not run |

**Total Tasks**: {total_tasks} **Estimated Duration**: {total_weeks} weeks
({total_sprints} sprints) **Quick Wins**: {quick_wins_count} tasks with
immediate impact

## Quick Wins

Start here for immediate impact with minimal effort:

| #   | Task                          | Effort | Phase | Impact        |
| --- | ----------------------------- | ------ | ----- | ------------- |
| 1   | Run npm audit fix             | XS     | 1     | Fix 3 CVEs    |
| 2   | Add .gitignore entry for .env | XS     | 1     | Prevent leaks |
| ... |

## Phase 0: Safety Net

**Goal**: Establish test coverage before making changes **Story Points**: {sp} |
**Sprints**: {sprints}

| ID       | Task                       | Effort | Risk | Deps | Method      |
| -------- | -------------------------- | ------ | ---- | ---- | ----------- |
| task-001 | Add characterization tests | M      | low  | -    | ai-assisted |
| ...      |

## Phase 1: Critical Fixes

(same table format)

## Phase 2: Foundation

(same table format)

## Phase 3: Quality

(same table format)

## Phase 4: Modernization

(same table format)

## Phase 5: Optimization

(same table format)

## Timeline

{sprint_length}-week sprints, {team_size} developer(s)

| Phase | Name           | Points | Sprints | Start    | End      |
| ----- | -------------- | ------ | ------- | -------- | -------- |
| 0     | Safety Net     | 8      | 1       | Sprint 1 | Sprint 1 |
| 1     | Critical Fixes | 12     | 2       | Sprint 2 | Sprint 3 |
| ...   |

**Total**: {total_story_points} points, {total_sprints} sprints, {total_weeks}
weeks

## Dependency Graph

task-001 ──► task-003 task-002 ──► task-003 task-003 ──► task-005

## Methodology

- **Inputs**: Health Check, Security Audit, Test Scaffold
- **Phase assignment**: Based on finding type and severity
- **Dependency ordering**: Topological sort (deterministic)
- **Effort estimation**: XS(1) S(2) M(3) L(5) XL(8) story points
- **Sprint capacity**: {team_size} × {sprint_length} × 5 points/dev/week
```

If `--format json`, write only JSON. If `--format summary`, write only markdown.
If `--format both`, write both files.

#### Step 7.4: Print Console Summary

Output a human-readable summary to the console:

```text
MODERNIZATION PLAN
================================================================

Project: {codebase_name}
Generated: {date}

INPUT SCORES
----------------------------------------------------------------
  Health Check:     ██████████████░░ 72  [GOOD]
  Security Audit:   ████████████░░░░ 65  [FAIR]
  Test Scaffold:    (not available)

PLAN SUMMARY
----------------------------------------------------------------
  Total tasks:       24
  Story points:      78
  Quick wins:        8
  Estimated sprints: 6 (12 weeks)

QUICK WINS (start here)
----------------------------------------------------------------
  1. [XS] Run npm audit fix (Phase 1)
  2. [XS] Add .gitignore entry for .env (Phase 1)
  3. [S]  Configure ESLint (Phase 3)
  ...

PHASE BREAKDOWN
----------------------------------------------------------------
  Phase 0 - Safety Net:      8 pts (1 sprint)   ██░░░░
  Phase 1 - Critical Fixes: 12 pts (2 sprints)  ████░░
  Phase 2 - Foundation:     18 pts (2 sprints)  ██████
  Phase 3 - Quality:        15 pts (2 sprints)  █████░
  Phase 4 - Modernization:  17 pts (2 sprints)  █████░
  Phase 5 - Optimization:    8 pts (1 sprint)   ██░░░░

----------------------------------------------------------------
Plan saved: .nightgauge/modernization-plan.json
Roadmap saved: .nightgauge/MODERNIZATION_PLAN.md
```

---

### Phase 8: Optional Issue Generation

Only runs if `--create-issues` flag is set.

#### Step 8.1: Verify GitHub Authentication

```bash
if ! nightgauge forge auth status &>/dev/null; then
  echo "ERROR: forge CLI not authenticated."
  echo "Run: nightgauge forge auth login"
  exit 1
fi

REPO=$(nightgauge forge repo view --json nameWithOwner -q '.nameWithOwner' 2>/dev/null)
if [ -z "$REPO" ]; then
  echo "ERROR: Not in a GitHub repository."
  exit 1
fi

echo "Repository: $REPO"
```

#### Step 8.2: Dry-Run Preview

Always show a preview of issues to be created, even without `--dry-run`:

```text
ISSUES TO CREATE (24 total)
================================================================

Phase 0 - Safety Net (3 issues):
  [M] Add characterization tests for critical paths
  [S] Configure coverage reporting
  [S] Add test directory structure

Phase 1 - Critical Fixes (5 issues):
  [XS] Run npm audit fix
  [XS] Add .gitignore for .env files
  ...

(continue for all phases)
```

If `--dry-run` is set, stop here and do not create issues.

#### Step 8.3: User Confirmation

If `--create-issues` is set and NOT `--dry-run`, prompt user for confirmation:

```
Create 24 GitHub issues in {repo}? (This action cannot be undone)
```

Use `AskUserQuestion` to get confirmation.

#### Step 8.4: Create Issues

For each task, create a GitHub issue using `nightgauge forge issue create`:

```bash
for task in tasks; do
  nightgauge forge issue create \
    --title "[Modernize] ${task.title}" \
    --body "$(cat <<'EOF'
## Description

${task.description}

## Rationale

${task.rationale}

## Details

- **Phase**: ${task.phase} (${phase_name})
- **Effort**: ${task.effort}
- **Risk**: ${task.risk}
- **Execution Method**: ${task.execution_method}
- **Source**: ${task.source} → ${task.source_dimension}

## Dependencies

${dependencies_list or "None"}

---
*Generated by nightgauge-modernize-plan*
EOF
)" \
    --label "type:chore" \
    --label "priority:${mapped_priority}" \
    --label "size:${task.effort}"
done
```

**Priority Mapping:**

| Task Risk  | Issue Priority Label |
| ---------- | -------------------- |
| `critical` | `priority:critical`  |
| `high`     | `priority:high`      |
| `medium`   | `priority:medium`    |
| `low`      | `priority:low`       |

#### Step 8.5: Report Created Issues

```text
ISSUES CREATED: 24
================================================================
  #301 [Phase 0] Add characterization tests for critical paths
  #302 [Phase 0] Configure coverage reporting
  ...

Issues added to project board via: nightgauge project add <issue-number>
```

Update the JSON output with created issue numbers:

```json
{
  "issues_created": [
    { "task_id": "task-001", "issue_number": 301 },
    { "task_id": "task-002", "issue_number": 302 }
  ]
}
```

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Output Format

### JSON Schema

See Phase 7 Step 7.1 for the complete JSON report structure.

### Report Files

| File                                  | Format   | When Written            |
| ------------------------------------- | -------- | ----------------------- |
| `.nightgauge/modernization-plan.json` | JSON     | `--format json/both`    |
| `.nightgauge/MODERNIZATION_PLAN.md`   | Markdown | `--format summary/both` |
| Console output                        | Text     | Always                  |

---

## Error Handling

| Condition                       | Action                                                |
| ------------------------------- | ----------------------------------------------------- |
| No assessment outputs found     | Error listing expected files and run commands         |
| Invalid JSON in report          | Skip that input, warn, continue with others           |
| Dependency cycle detected       | Error with cycle path, suggest manual resolution      |
| `--create-issues` no forge auth | Error with `nightgauge forge auth login` instructions |
| `--skip-phase` invalid value    | Error with valid phase numbers (0-5)                  |
| `--team-size` not a number      | Error with usage instructions                         |
| `--format` invalid value        | Error with valid formats: summary, json, both         |
| All phases skipped              | Error: at least one phase must be included            |
| jq not installed                | Error with install instructions                       |
| Zero tasks generated            | Warning: assessments found no actionable items        |

---

## Input Schemas

### Health Check (`health-report.json`)

```json
{
  "schema_version": "1.0",
  "summary": {
    "overall_health_score": 72,
    "status": "good"
  },
  "dimensions": {
    "dependency_health": {
      "score": 65,
      "status": "good",
      "findings": [{ "severity": "...", "title": "...", "recommendation": "..." }],
      "metrics": { "..." }
    },
    "test_coverage": { "..." },
    "code_quality": { "..." },
    "documentation": { "..." },
    "build_system": { "..." },
    "tech_debt": { "..." }
  }
}
```

### Security Audit (`security-audit.json`)

```json
{
  "schema_version": "1.0",
  "summary": {
    "overall_security_score": 65,
    "status": "fair"
  },
  "dimensions": {
    "dependency_vulnerabilities": {
      "score": 65,
      "findings": [{ "severity": "...", "title": "...", "cwe": "...", "recommendation": "..." }],
      "metrics": { "..." }
    },
    "secret_detection": { "..." },
    "owasp_top10": { "..." },
    "cryptographic_health": { "..." },
    "input_validation": { "..." },
    "auth_authz": { "..." },
    "config_security": { "..." }
  }
}
```

### Test Scaffold (`test-scaffold-report.json`)

```json
{
  "schema_version": "1.0",
  "summary": {
    "source_files": 120,
    "test_files": 45,
    "coverage_before": 62.5,
    "critical_gaps_found": 8
  },
  "gaps": [
    {
      "file": "src/services/PaymentService.ts",
      "functions": ["processPayment"],
      "priority": "critical",
      "risk_score": 95
    }
  ],
  "recommendations": [{ "priority": 1, "action": "...", "effort": "M", "risk_reduction": "high" }]
}
```

---

## Pipeline Position

```text
UTILITIES (not part of main pipeline)

/nightgauge:health-check ────────────────────┐
       |                                           |
  Writes: .nightgauge/health-report.json      |
                                                   |
/nightgauge:security-audit ──────────────────┤
       |                                           |
  Writes: .nightgauge/security-audit.json     |
                                                   ├──► /nightgauge:modernize-plan
/nightgauge:test-scaffold ───────────────────┤         |
       |                                           |    Reads: all three reports
  Writes: .nightgauge/test-scaffold-report.json    Writes: modernization-plan.json
                                                        Writes: MODERNIZATION_PLAN.md
                                                        Optional: creates GitHub issues
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs.

---

## Configuration

This skill reads configuration from `.nightgauge/config.yaml` if present:

| Config Key                      | Default | Description                 |
| ------------------------------- | ------- | --------------------------- |
| `modernize_plan.default_format` | `both`  | Default `--format` value    |
| `modernize_plan.team_size`      | `1`     | Default `--team-size` value |
| `modernize_plan.sprint_length`  | `2`     | Default `--sprint-length`   |
| `modernize_plan.skip_phases`    | -       | Default phases to skip      |
| `modernize_plan.output_path`    | auto    | Default JSON output path    |

---

**Author:** nightgauge **License:** Apache-2.0
