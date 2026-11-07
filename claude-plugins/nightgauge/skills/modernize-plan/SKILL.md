---
name: modernize-plan
description: Modernization plan generator that consumes health-check, security-audit, and
  test-scaffold outputs to produce a prioritized, phased roadmap. Use after
  running assessment skills to get an actionable improvement plan.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
disable-model-invocation: true
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

| Tool        | Command                                  |
| ----------- | ---------------------------------------- |
| Claude Code | `/nightgauge:modernize-plan [options]`   |
| Copilot     | Invoke via Agent Skills extension        |
| Cursor      | Run via Agent Skills or direct SKILL.md  |
| Headless    | `claude -p "/nightgauge:modernize-plan"` |

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
- At least one assessment output file must exist in this checkout's `reports/`
  directory inside the git directory (`nightgauge layout path checkout reports`,
  ADR-024 § 7):
  - `reports/health-report.json` (from health-check)
  - `reports/security-audit.json` (from security-audit)
  - `reports/test-scaffold-report.json` (from test-scaffold)
- `gh` CLI authenticated (only required if `--create-issues` is used)

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion unless
`--create-issues` is specified (for user confirmation before creating issues).
Make autonomous decisions or fail with clear error.

---

## Workflow

### Phase 0: Environment Preflight

### Phase 0: Environment Preflight

**PURPOSE**: Verify the nightgauge binary is available and the environment
is healthy before executing any pipeline stage. Skills halt immediately with
actionable instructions when this check fails.

Three-layer check:

1. **Binary discovery** — shell-level, runs before invoking the binary
2. **Doctor check** — binary-level, full environment health via `doctor --json`
3. **Project environment** — the checks the project declares under
   `preflight.environment` (#101); opt-in, silent when none are declared

The binary discovery cascade below is **provider-neutral** (issue 4029): it works
identically under every adapter (Claude, Codex, Gemini, …) and references no
VSCode-extension-specific path. The host that spawns the skill (the VSCode
extension's `skillRunner`, or the Go `nightgauge` binary in auto/CLI mode)
resolves the binary authoritatively and exports `NIGHTGAUGE_BIN`, which
this cascade honors first. The remaining steps (PATH, the development-only repo
`bin/` and worktree canonical-repo `bin/`, `~/go/bin`) are fallbacks for direct
or terminal invocation.

This intentionally **diverges** from
`claude-plugins/nightgauge/hooks/lib/guard.sh` (issue 3262 → issue 4029): guard.sh is
Claude-Code-only and retains a trailing `~/.vscode/extensions/...` glob to serve
the standalone-terminal-Claude case (where no host exports `NIGHTGAUGE_BIN`).
Skills must stay portable Markdown and cannot reference that VSCode-only path —
so the shared resolution order (`NIGHTGAUGE_BIN` → PATH → gated repo → gated
canonical → `~/go/bin`) stays in sync, and only guard.sh carries the extra Claude-only
fallback. The `nightgauge preflight skill-portability` gate enforces that
no skill reintroduces a `.vscode/extensions` path, and that every cascade keeps
the `NIGHTGAUGE_DEV_REPO_BIN` gate (#2758): a foreign repository's
`bin/nightgauge` must never run.

After the cascade resolves `$BINARY`, the preflight prepends
`dirname($BINARY)` to `PATH` so subsequent bare `nightgauge ...`
invocations later in the same skill body resolve through PATH rather than
failing with "command not found" (issue 3262).

```bash
# Layer 1: provider-neutral binary discovery (NIGHTGAUGE_BIN → PATH → gated repo
# bin → gated canonical-repo bin → ~/go/bin). No VSCode-extension path — see issue 4029.
# Repo bins need NIGHTGAUGE_DEV_REPO_BIN=1 and a verified core checkout (#2758).
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  for NG_ROOT in "$(git rev-parse --show-toplevel 2>/dev/null)" \
    "$(cd "$(git rev-parse --git-common-dir 2>/dev/null)/.." 2>/dev/null && pwd)"; do
    [ -x "$NG_ROOT/bin/nightgauge" ] || continue
    grep -qx 'module github.com/nightgauge/nightgauge' "$NG_ROOT/go.mod" 2>/dev/null || continue
    NG_ORIGIN="$(git -C "$NG_ROOT" config --get remote.origin.url 2>/dev/null)"
    case "${NG_ORIGIN%.git}" in
      git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
        ssh://git@github.com/nightgauge/nightgauge) BINARY="$NG_ROOT/bin/nightgauge"; break ;;
    esac
  done
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
  echo "" >&2
  echo "Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest" >&2
  echo "Or download from: https://github.com/nightgauge/nightgauge/releases" >&2
  echo "Run \`nightgauge doctor\` after installing to verify your environment." >&2
  exit 1
fi
# Make the resolved binary callable via bare `nightgauge ...` later in this skill body (issue 3262).
export PATH="$(dirname "$BINARY"):$PATH"

# Per-repo GitHub token: export GH_TOKEN so every `gh` call in this skill
# authenticates as the *current repo's* configured user instead of the machine-
# global `gh auth` active account. `forge auth token` resolves per-repo (config
# github_user/token → GITHUB_TOKEN → `gh auth token --user <github_user>`), so
# concurrent sessions/workspaces owned by different GitHub users each use their
# own token. GH_TOKEN outranks GITHUB_TOKEN and the gh keyring. Only resolve when
# unset (an upstream env value — e.g. the VSCode extension's terminal env —
# wins); never fail preflight over it. Mirrors hooks/lib/guard.sh (sync, issue 3262).
if [ -z "${GH_TOKEN:-}" ]; then
  IB_REPO_TOKEN="$("$BINARY" forge auth token 2>/dev/null || true)"
  if [ -n "$IB_REPO_TOKEN" ]; then
    export GH_TOKEN="$IB_REPO_TOKEN"
    export GITHUB_TOKEN="${GITHUB_TOKEN:-$IB_REPO_TOKEN}"
  fi
  unset IB_REPO_TOKEN
fi

# Layer 2: full environment health check via doctor --json
DOCTOR_RESULT=$("$BINARY" doctor --json 2>/dev/null)
DOCTOR_EXIT=$?
if [ "$DOCTOR_EXIT" -eq 2 ]; then
  echo "ERROR: Environment check failed — nightgauge doctor reports broken environment." >&2
  FAILED_CHECKS=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.failed_checks[]? // empty' 2>/dev/null)
  if [ -n "$FAILED_CHECKS" ]; then
    echo "Failing check(s): $(echo "$FAILED_CHECKS" | tr '\n' ',' | sed 's/,$//')" >&2
  fi
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.errors[]' >&2 2>/dev/null || true
  INSTALL_MSG=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.install_instructions // empty' 2>/dev/null)
  [ -n "$INSTALL_MSG" ] && echo "$INSTALL_MSG" >&2
  exit 1
fi
if [ "$DOCTOR_EXIT" -eq 1 ]; then
  echo "WARNING: Environment has non-critical issues:" >&2
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.warnings[]' >&2 2>/dev/null || true
  # Continue — warnings do not block skill execution
fi

# Layer 3: project-declared environment checks (preflight.environment, #101).
# Opt-in: with none declared this prints nothing and continues. The verb
# writes one `[environment-halt]` line per failed check, with its remedy.
if ! "$BINARY" preflight environment >/dev/null; then
  echo "[environment-halt] Project environment check failed — fix the environment, not the code." >&2
  exit 1
fi
```

**Exit codes from `nightgauge doctor`**:

| Code | Meaning                         | Skill behavior                          |
| ---- | ------------------------------- | --------------------------------------- |
| 0    | Healthy                         | Continue                                |
| 1    | Degraded (warnings only)        | Continue with warning printed to stderr |
| 2    | Broken (required checks failed) | Halt immediately with error details     |

**Layer 3** halts the stage when any check declared under
`preflight.environment` in the workspace's `.nightgauge/config.yaml` fails
(exit 1) or the declaration is invalid (exit 2). The failure is in the
operator's environment, not the code: do not edit code to work around it, do
not run the remedy yourself, and end the stage. The pipeline records it as
`environment_halt` and holds the issue for an operator.

Do not theorize about the cause beyond what is printed here — the failing
check name (`failed_checks`) and its `.errors[]` message are the complete
diagnosis (#277).


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

HEALTH_REPORT="$(nightgauge layout path checkout reports/health-report.json)"
SECURITY_REPORT="$(nightgauge layout path checkout reports/security-audit.json)"
SCAFFOLD_REPORT="$(nightgauge layout path checkout reports/test-scaffold-report.json)"

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

The binary reads, from the checkout's `reports/` directory:

- `health-report.json` — severity mapped from status string
  (`critical`→critical, `poor`→high, `fair`→medium, `good`→low, `excellent`→info)
- `security-audit.json` — severity passed through unchanged
- `test-scaffold-report.json` — priority mapped 1:1 to severity

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

Write the structured plan to this checkout's `reports/modernization-plan.json`
(`nightgauge layout path checkout reports/modernization-plan.json`, inside the
git directory, ADR-024 § 7), or to the custom `--output` path. Build the JSON in
a temporary file (`PLAN_TMP="$(mktemp)"`); Step 7.2 validates and writes it.

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
      "path": ".git/nightgauge-worktree/reports/health-report.json",
      "overall_score": 72
    },
    "security_audit": {
      "available": true,
      "path": ".git/nightgauge-worktree/reports/security-audit.json",
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

#### Step 7.2: Verify and Write JSON

The default plan goes through `nightgauge layout write`; never write under
`.git` by path:

```bash
python3 -m json.tool "$PLAN_TMP" > /dev/null || { echo "Invalid plan JSON" >&2; exit 1; }
if [ -n "$OUTPUT_FILE" ]; then
  cp "$PLAN_TMP" "$OUTPUT_FILE"
  PLAN_PATH="$OUTPUT_FILE"
else
  PLAN_PATH="$(nightgauge layout write checkout reports/modernization-plan.json --from "$PLAN_TMP")"
fi
rm -f "$PLAN_TMP"
echo "Plan written: $PLAN_PATH"
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
Plan saved: .git/nightgauge-worktree/reports/modernization-plan.json
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

REPO="${NIGHTGAUGE_REPO:-$(nightgauge git repo-slug 2>/dev/null)}"
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

### Self-Assessment Epilogue

**PURPOSE**: Evaluate whether this skill's instructions matched reality during
this execution. This phase is **non-blocking** — skip entirely if any main phase
failed. A perfectly working skill produces **no output** from this phase.

> See [docs/SKILL_SELF_ASSESSMENT.md](https://github.com/nightgauge/nightgauge/blob/main/docs/SKILL_SELF_ASSESSMENT.md) for
> the full strategy, synthesis algorithm, and integration architecture.

#### When to Skip

- Any prior phase exited with an error before completing
- The skill was cancelled or timed out
- Running in `--dry-run` mode

#### Step 1: Evaluate Execution Friction

Review the execution that just completed. Answer these questions honestly:

1. **Command failures**: Did any command, script, or binary call in the skill
   instructions fail? (e.g., script not found, wrong flags, missing binary)
2. **Workarounds**: Did you have to deviate from the skill instructions to
   accomplish the goal? (e.g., used `gh` directly because a referenced script
   didn't exist, skipped a step that referenced a nonexistent file)
3. **Stale references**: Did any file path, function name, API endpoint, or tool
   referenced in the instructions not exist in the current codebase?
4. **Unclear instructions**: Were any instructions ambiguous enough that you had
   to guess at the intended behavior?
5. **Missing instructions**: Was there a significant step you had to figure out
   on your own that should have been documented in this skill?

**If ALL answers are "no" — write nothing and complete the skill normally.** The
goal is silence when everything works.

#### Step 2: Write Assessment Record

Only if friction was detected in Step 1. Write a single JSON file,
`assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json` in the clone's pipeline state
directory, through the binary (JSON on stdin; never a direct file write):

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
nightgauge layout write pipeline "assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json" <<'JSON'
{ ... the record below ... }
JSON
```

The assessment record MUST follow this schema:

```json
{
  "schema_version": "1",
  "skill": "{STAGE_NAME}",
  "skill_file": "skills/nightgauge-{STAGE_NAME}/SKILL.md",
  "issue_number": 42,
  "timestamp": "2026-03-10T14:30:00Z",
  "friction": [
    {
      "type": "command_failure",
      "severity": "high",
      "description": "hooks/lib/add-to-project.sh not found — script was deleted",
      "skill_line_hint": "claude-plugins/nightgauge/hooks/lib/add-to-project.sh <issue-number>",
      "actual_resolution": "Used gh api graphql to add issue to project board directly",
      "suggested_fix": "Replace with: nightgauge project add <issue-number>"
    }
  ]
}
```

**Friction types**: `command_failure`, `workaround`, `stale_reference`,
`unclear_instruction`, `missing_instruction`

**Severity levels**:

- `high` — instruction is **broken**. Required manual workaround to complete.
- `medium` — instruction is **misleading**. Agent adapted without user help.
- `low` — instruction is **suboptimal**. No functional impact.

**Rules**:

- **One record per execution** — multiple friction items go in the `friction`
  array, not separate files.
- **Be specific** — quote the exact instruction text that was wrong. Not "some
  commands didn't work" but "Step 5.2 calls `hooks/lib/add-to-project.sh` but
  this script was deleted in commit 65915701."
- **Suggest the fix** — every finding MUST include `suggested_fix` with the
  concrete SKILL.md change needed. Not "update the docs" but "replace
  `hooks/lib/add-to-project.sh <N>` with `nightgauge project add <N>`."
- **Don't invent friction** — only report issues you actually encountered during
  this execution. Do not speculate about potential problems.

#### Step 3: Validate and Complete

```bash
# Validate JSON if written
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
ASSESSMENT_FILE="$(nightgauge layout path pipeline "assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json")"
if [ -f "$ASSESSMENT_FILE" ]; then
  python3 -m json.tool "$ASSESSMENT_FILE" > /dev/null 2>&1 || \
    echo "WARNING: Assessment record is not valid JSON" >&2
fi
```


---

## Output Format

### JSON Schema

See Phase 7 Step 7.1 for the complete JSON report structure.

### Report Files

| File                                       | Format   | When Written            |
| ------------------------------------------ | -------- | ----------------------- |
| `checkout reports/modernization-plan.json` | JSON     | `--format json/both`    |
| `.nightgauge/MODERNIZATION_PLAN.md`        | Markdown | `--format summary/both` |
| Console output                             | Text     | Always                  |

`checkout` paths are inside this checkout's git directory (ADR-024 § 7):
`nightgauge layout path checkout reports/modernization-plan.json` prints the
resolved path.

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

/nightgauge:health-check ──────────────────────────┐
       |                                           |
  Writes: checkout reports/health-report.json      |
                                                   |
/nightgauge:security-audit ────────────────────────┤
       |                                           |
  Writes: checkout reports/security-audit.json     |
                                                   ├──► /nightgauge:modernize-plan
/nightgauge:test-scaffold ─────────────────────────┤         |
       |                                           |    Reads: all three reports
  Writes: checkout reports/test-scaffold-report.json    Writes: reports/modernization-plan.json
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
