---
name: nightgauge-refactor-rewrite
description: Refactor vs rewrite decision analysis engine. Evaluates brownfield codebases
  across 8 dimensions to produce data-driven recommendations with confidence
  levels, risk/benefit matrices, and hybrid approach suggestions. Use when deciding
  whether to refactor or rewrite a legacy component before committing to a modernization path.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.2.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
orchestration:
  mode: phased
  phase: refactor-analysis
  ceiling: fanout
  units:
    - id: code-quality
      role: dimension-worker
      promptRef: SKILL.md#phase-2-code-quality-analysis
    - id: test-coverage
      role: dimension-worker
      promptRef: SKILL.md#phase-3-test-coverage-analysis
    - id: dependency-coupling
      role: dimension-worker
      promptRef: SKILL.md#phase-4-dependency-coupling-analysis
    - id: business-logic
      role: dimension-worker
      promptRef: SKILL.md#phase-5-business-logic--tech-stack-analysis
    - id: team-effort
      role: dimension-worker
      promptRef: SKILL.md#phase-6-team--effort-analysis
  judge:
    mode: merge
    quorum: 1
    promptRef: SKILL.md#phase-7-decision-engine
---

# Nightgauge Refactor vs Rewrite Analysis

## Description

Decision analysis engine that evaluates brownfield codebases across 8 dimensions
to produce a data-driven recommendation: **Refactor**, **Rewrite**, or
**Hybrid**. Uses deterministic tool commands (bash, grep, git) for metric
collection; AI interprets raw data into structured findings with 0-100 scoring,
confidence levels, risk/benefit matrices, and hybrid approach suggestions (e.g.,
strangler fig pattern).

**Use Cases:**

- Deciding whether to refactor or rewrite an aging codebase
- Evaluating individual modules/components for mixed-strategy modernization
- Providing stakeholders with data-driven modernization recommendations
- Producing structured input for `/nightgauge:modernize-plan`
- Comparing refactor vs rewrite trade-offs across monorepo packages

**When to Use:**

- When inheriting a brownfield codebase and deciding the modernization approach
- Before committing to a major rewrite effort
- When teams disagree on refactor vs rewrite — to ground the discussion in data
- After running `/nightgauge:health-check` and
  `/nightgauge:security-audit` for enriched analysis
- When evaluating whether a strangler fig or branch-by-abstraction approach fits

**Relationship to Other Skills:**

| Skill            | Focus                               | Relationship              |
| ---------------- | ----------------------------------- | ------------------------- |
| Health Check     | Codebase quality (6 dims)           | Optional input (upstream) |
| Security Audit   | Security posture (7 dims)           | Optional input (upstream) |
| Test Scaffold    | Coverage gaps + test generation     | Optional input (upstream) |
| Refactor/Rewrite | Refactor vs rewrite decision        | **This skill**            |
| Dep Modernize    | Dependency update engine            | Peer context (optional)   |
| Modernize Plan   | Phased roadmap from all assessments | Consumer (downstream)     |

## Invocation

| Tool        | Command                                                      |
| ----------- | ------------------------------------------------------------ |
| Claude Code | `/nightgauge:refactor-rewrite [options]`                     |
| Copilot     | Invoke via Agent Skills extension                            |
| Cursor      | Run via Agent Skills or direct SKILL.md                      |
| Standalone  | `claude --skill skills/nightgauge-refactor-rewrite/SKILL.md` |

## Arguments

| Argument              | Description                           | Default |
| --------------------- | ------------------------------------- | ------- |
| `--path DIR`          | Root directory to assess              | `.`     |
| `--package PKG`       | Monorepo package filter               | all     |
| `--module MODULE`     | Specific module/component to analyze  | all     |
| `--dimensions DIMS`   | Comma-separated dimensions            | all     |
| `--format FORMAT`     | Output: `summary`, `json`, `both`     | `both`  |
| `--output FILE`       | JSON output path                      | auto    |
| `--skip-coverage-run` | Skip running coverage tools           | `false` |
| `--team-size N`       | Team size for effort estimates        | `1`     |
| `--timeline WEEKS`    | Available timeline for effort context | `12`    |

### Examples

```bash
# Full refactor-vs-rewrite analysis of current directory
/nightgauge:refactor-rewrite

# Assess specific directory
/nightgauge:refactor-rewrite --path /path/to/project

# Analyze a specific module only
/nightgauge:refactor-rewrite --module src/legacy-auth/

# Only analyze code quality and dependency coupling
/nightgauge:refactor-rewrite --dimensions code-quality,dependency-coupling

# JSON output only
/nightgauge:refactor-rewrite --format json

# Estimate effort for a team of 4 over 16 weeks
/nightgauge:refactor-rewrite --team-size 4 --timeline 16

# Assess specific monorepo package
/nightgauge:refactor-rewrite --package packages/api-server
```

---

## Prerequisites

- Bash shell
- `jq` installed (for JSON processing)
- Ecosystem tools are optional — the skill gracefully degrades when audit tools
  or coverage runners are not installed
- Git history is optional but strongly recommended for team expertise and change
  frequency analysis

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Analysis Dimensions

### 1. Code Quality (Weight: 0.15)

Evaluates code hygiene, maintainability indicators, and complexity proxies.

**Deterministic Signals:**

- Linting configuration presence (ESLint, Ruff, clippy, etc.)
- Formatter configuration presence (Prettier, Black, etc.)
- TODO/FIXME/HACK/XXX comment counts
- Average file size (lines of code)
- Largest files (complexity hotspots)
- Cyclomatic complexity proxies (branching construct counts per file)

**AI Interpretation:**

Assess overall maintainability. High complexity + no linting + many TODOs
suggests rewrite. Low complexity + good tooling suggests refactor.

**Scoring:**

| Score Range | Condition                                         |
| ----------- | ------------------------------------------------- |
| 90-100      | Linter + formatter + <5 TODOs + avg file <200 LOC |
| 70-89       | Linter present + <20 TODOs + moderate file sizes  |
| 50-69       | Some tooling + moderate debt markers              |
| 30-49       | No linter + many TODOs + large files              |
| 0-29        | No tooling + >100 TODOs + very large files        |

**Decision Signal:**

- Score > 60: Favors **refactor** (code is maintainable enough to improve
  incrementally)
- Score < 30: Favors **rewrite** (technical debt too deep for incremental
  improvement)

### 2. Test Coverage (Weight: 0.15)

Evaluates the safety net available for refactoring.

**Deterministic Signals:**

- Test framework detection (vitest, jest, pytest, go test, cargo test, junit)
- Test-to-source file ratio
- Coverage configuration presence
- Coverage percentage (if available or runnable)

**AI Interpretation:**

Assess whether existing tests provide adequate safety for refactoring. Low
coverage makes refactoring risky — changes break unknown behavior.

**Scoring:**

| Score Range | Condition                                 |
| ----------- | ----------------------------------------- |
| 90-100      | Framework + coverage config + ratio > 0.8 |
| 70-89       | Framework + some tests + ratio > 0.5      |
| 50-69       | Framework present + ratio > 0.2           |
| 30-49       | Few test files, no coverage config        |
| 0-29        | No test framework or no test files        |

**Decision Signal:**

- Score > 50: Favors **refactor** (adequate safety net exists)
- Score < 20: Favors **rewrite** (unsafe to refactor without tests — too risky
  to change existing code incrementally)

### 3. Dependency Coupling (Weight: 0.15)

Evaluates how entangled modules are and whether isolation is feasible.

**Deterministic Signals:**

- Import/require graph construction (per-file import analysis)
- Circular dependency detection
- Fan-in (files importing a module) and fan-out (modules imported by a file)
- Module boundary violations (cross-layer imports)

**AI Interpretation:**

Assess entanglement level. High circular dependencies and deep coupling make
incremental refactoring extremely difficult — changes cascade unpredictably.

**Scoring:**

| Score Range | Condition                                              |
| ----------- | ------------------------------------------------------ |
| 90-100      | No circular deps, clear module boundaries, low fan-out |
| 70-89       | <5% circular deps, mostly clean boundaries             |
| 50-69       | Some circular deps, moderate coupling                  |
| 30-49       | >15% circular deps, blurred module boundaries          |
| 0-29        | >30% circular deps, no discernible module structure    |

**Decision Signal:**

- Score > 60: Favors **refactor** (modules can be improved in isolation)
- Score 40-60: Favors **hybrid** (some modules refactorable, others need
  rewrite)
- Score < 30: Favors **rewrite** (entanglement prevents incremental change)

### 4. Business Logic Extraction (Weight: 0.10)

Evaluates whether core business rules can be identified and preserved.

**Deterministic Signals:**

- Service/domain layer detection (files matching service, domain, model, entity
  patterns)
- Separation of concerns indicators (distinct layers vs monolithic files)
- Business logic density (ratio of domain files to infrastructure files)
- Configuration externalization (env vars, config files vs hardcoded values)

**AI Interpretation:**

Assess whether business logic is identifiable and extractable. If rules are
scattered across infrastructure code, refactoring risks losing business logic.

**Scoring:**

| Score Range | Condition                                           |
| ----------- | --------------------------------------------------- |
| 90-100      | Clear domain layer, well-separated concerns         |
| 70-89       | Identifiable business logic, some mixing with infra |
| 50-69       | Business logic partially extractable                |
| 30-49       | Logic deeply embedded in infrastructure             |
| 0-29        | No discernible business logic layer                 |

**Decision Signal:**

- Score > 60: Favors **refactor** (business rules can be preserved during
  incremental changes)
- Score < 30: Favors **rewrite** (business logic must be reverse-engineered
  anyway)

### 5. Tech Stack Viability (Weight: 0.15)

Evaluates whether the current technology stack is viable long-term.

**Deterministic Signals:**

- Framework/runtime version detection (package.json engines, go.mod, etc.)
- Major version gap from latest (current vs latest available)
- End-of-life (EOL) status checks (Node.js LTS schedule, Python EOL, etc.)
- Community health indicators (last release date, open issues)

**AI Interpretation:**

Assess whether the stack is still viable. EOL runtimes with no security patches
make refactoring pointless — the entire platform needs replacement.

**Scoring:**

| Score Range | Condition                                         |
| ----------- | ------------------------------------------------- |
| 90-100      | Current LTS, latest major, active community       |
| 70-89       | 1 major behind, still supported, active releases  |
| 50-69       | 2 majors behind, approaching EOL                  |
| 30-49       | EOL approaching within 12 months, sparse releases |
| 0-29        | EOL reached, no security patches, dead community  |

**Decision Signal:**

- Score > 60: Favors **refactor** (stack is viable, worth investing in)
- Score < 30: Favors **rewrite** (stack is dying, refactoring on a dead platform
  is wasted effort)

### 6. Team Expertise (Weight: 0.05)

Evaluates team familiarity with the codebase and technology.

**Deterministic Signals:**

- Git author count (unique contributors in last 90/180/365 days)
- Recent commit frequency (commits per week in last 90 days)
- Language familiarity proxy (files per language, contributor distribution)
- Bus factor estimate (% of code touched by top contributor)

**AI Interpretation:**

Assess whether the team has sufficient familiarity to refactor safely. New teams
with no institutional knowledge may be better served by a clean rewrite.

**Scoring:**

| Score Range | Condition                                              |
| ----------- | ------------------------------------------------------ |
| 90-100      | >3 active contributors, high frequency, low bus factor |
| 70-89       | 2-3 active contributors, moderate frequency            |
| 50-69       | 1-2 contributors, some gaps in knowledge               |
| 30-49       | Single contributor or no recent activity               |
| 0-29        | No contributors in 6+ months, complete team turnover   |

**Decision Signal:**

- Score > 50: Favors **refactor** (team knows the code)
- Score < 30: Neutral — new teams can refactor or rewrite equally

### 7. Risk Assessment (Weight: 0.15)

Synthesizes signals from all other dimensions to evaluate risk.

**Deterministic Signals:**

This dimension combines metrics from dimensions 1-6:

- Critical path coverage (are the riskiest modules tested?)
- Deployment complexity (CI/CD presence, containerization)
- Data migration risk (database schema complexity, migration tooling)
- Rollback capability (feature flags, blue-green deployment indicators)

**AI Interpretation:**

Assess what could go wrong with each approach. Refactoring risks breaking
existing behavior; rewriting risks losing business logic and timeline overruns.

**Scoring:**

| Score Range | Condition                                           |
| ----------- | --------------------------------------------------- |
| 90-100      | Low risk: good tests, CI/CD, rollback capability    |
| 70-89       | Moderate risk: some gaps but manageable             |
| 50-69       | Notable risk: missing safety nets in critical areas |
| 30-49       | High risk: significant gaps in test/deploy/rollback |
| 0-29        | Critical risk: no safety nets, high blast radius    |

**Decision Signal:**

- Score > 60: Either approach is viable — choose based on other factors
- Score < 30: Favors **hybrid** (mitigate risk with gradual migration)

### 8. Effort Estimation (Weight: 0.10)

Estimates relative effort for refactor vs rewrite approaches.

**Deterministic Signals:**

- Total lines of code (LOC)
- Total file count
- Dependency count (direct + transitive)
- Codebase age (first commit date)
- Module count (distinct packages/directories with their own purpose)

**AI Interpretation:**

Estimate relative effort for refactoring (incremental improvement) vs rewriting
(clean-room implementation). Factor in team size and timeline from arguments.

**Scoring:**

| Score Range | Condition                                                |
| ----------- | -------------------------------------------------------- |
| 90-100      | Small codebase (<5K LOC), few deps, refactor trivial     |
| 70-89       | Medium codebase (5-20K LOC), manageable scope            |
| 50-69       | Large codebase (20-100K LOC), significant effort either  |
| 30-49       | Very large (100K-500K LOC), rewrite timeline prohibitive |
| 0-29        | Massive (>500K LOC), rewrite practically infeasible      |

**Decision Signal:**

- Score > 70: Either approach is feasible within typical timelines
- Score 30-70: Favors **refactor** or **hybrid** (rewrite too expensive)
- Score < 30: Favors **refactor** (rewrite is practically infeasible at this
  scale)

---

## Orchestration

This skill declares an `orchestration:` frontmatter block (`mode: phased`) that
fans the five analysis dimensions out as parallel worker units (Phases 2-6:
code quality, test coverage, dependency coupling, business logic & tech stack,
team & effort), then a merge judge runs the decision engine (Phase 7) over their
scores to produce the confidence-rated refactor-vs-rewrite recommendation. For a
monorepo the same fan-out runs per package (Phase 9). The block is consumed by
the capability-routed `WorkflowEngine` (epic #3899); see
[docs/WORKFLOW_ORCHESTRATION.md](../../docs/WORKFLOW_ORCHESTRATION.md). Each
unit's `promptRef` points at the SAME dimension phase the prose **Workflow**
below walks, so providers without an orchestration capability run the dimensions
sequentially in one agent — the prose stays the portability floor.

## Workflow

### Phase 0: Environment Detection

<!-- include: ../_shared/PREFLIGHT.md -->

---

#### Step 0.1: Parse Arguments

Extract options from invocation:

```bash
ASSESS_PATH="."
PACKAGE_FILTER=""
MODULE_FILTER=""
DIMENSIONS="all"
OUTPUT_FORMAT="both"
SKIP_COVERAGE_RUN=false
OUTPUT_FILE=""
TEAM_SIZE=1
TIMELINE_WEEKS=12

# Parse arguments from invocation
# --path DIR: set ASSESS_PATH
# --package PKG: set PACKAGE_FILTER
# --module MODULE: set MODULE_FILTER
# --dimensions DIMS: set DIMENSIONS (comma-separated)
# --format FORMAT: set OUTPUT_FORMAT
# --skip-coverage-run: set SKIP_COVERAGE_RUN=true
# --output FILE: set OUTPUT_FILE
# --team-size N: set TEAM_SIZE
# --timeline WEEKS: set TIMELINE_WEEKS
```

#### Step 0.2: Detect Ecosystems

Scan for ecosystem indicators in the assessment path:

```bash
cd "$ASSESS_PATH"
ECOSYSTEMS=()

# Node.js
[ -f package.json ] && ECOSYSTEMS+=("nodejs")

# Python
([ -f pyproject.toml ] || [ -f setup.py ] || \
 [ -f requirements.txt ]) && ECOSYSTEMS+=("python")

# Go
[ -f go.mod ] && ECOSYSTEMS+=("go")

# Rust
[ -f Cargo.toml ] && ECOSYSTEMS+=("rust")

# Java/JVM
([ -f pom.xml ] || [ -f build.gradle ] || \
 [ -f build.gradle.kts ]) && ECOSYSTEMS+=("java")

if [ ${#ECOSYSTEMS[@]} -eq 0 ]; then
  echo "WARNING: No recognized ecosystem detected."
  echo "  Proceeding with generic file analysis only."
fi

echo "Ecosystems detected: ${ECOSYSTEMS[*]}"
```

#### Step 0.3: Detect Monorepo Structure

```bash
IS_MONOREPO=false
PACKAGES=()

# Node.js workspaces
if [ -f package.json ]; then
  WORKSPACES=$(jq -r '.workspaces // empty' package.json 2>/dev/null)
  if [ -n "$WORKSPACES" ]; then
    IS_MONOREPO=true
    for ws in $(jq -r '.workspaces[]? // empty' package.json 2>/dev/null); do
      for dir in $ws; do
        [ -d "$dir" ] && PACKAGES+=("$dir")
      done
    done
  fi
fi

# Cargo workspace
if [ -f Cargo.toml ]; then
  grep -q '\[workspace\]' Cargo.toml 2>/dev/null && IS_MONOREPO=true
fi

# Go workspace
[ -f go.work ] && IS_MONOREPO=true

echo "Monorepo: $IS_MONOREPO"
if [ "$IS_MONOREPO" = true ]; then
  echo "Packages: ${PACKAGES[*]}"
fi
```

#### Step 0.4: Apply Module Filter

If `--module` is specified, scope all analysis to that directory:

```bash
if [ -n "$MODULE_FILTER" ]; then
  if [ ! -d "$MODULE_FILTER" ]; then
    echo "ERROR: Module directory not found: $MODULE_FILTER"
    exit 1
  fi
  ASSESS_PATH="$MODULE_FILTER"
  echo "Module filter: $MODULE_FILTER"
fi
```

---

### Phase 1: Load Existing Assessments

Optionally read upstream assessment reports to avoid redundant analysis and
enrich scoring with pre-computed data.

```bash
HEALTH_REPORT=".nightgauge/health-report.json"
SECURITY_REPORT=".nightgauge/security-audit.json"
SCAFFOLD_REPORT=".nightgauge/test-scaffold-report.json"

HEALTH_AVAILABLE=false
SECURITY_AVAILABLE=false
SCAFFOLD_AVAILABLE=false

if [ -f "$HEALTH_REPORT" ]; then
  HEALTH_AVAILABLE=true
  HC_OVERALL=$(jq -r '.summary.overall_health_score // "N/A"' \
    "$HEALTH_REPORT" 2>/dev/null)
  HC_TEST_SCORE=$(jq -r '.dimensions.test_coverage.score // "N/A"' \
    "$HEALTH_REPORT" 2>/dev/null)
  HC_QUALITY_SCORE=$(jq -r '.dimensions.code_quality.score // "N/A"' \
    "$HEALTH_REPORT" 2>/dev/null)
  HC_DEBT_SCORE=$(jq -r '.dimensions.tech_debt.score // "N/A"' \
    "$HEALTH_REPORT" 2>/dev/null)
  echo "Health check available: overall=$HC_OVERALL"
fi

if [ -f "$SECURITY_REPORT" ]; then
  SECURITY_AVAILABLE=true
  SA_OVERALL=$(jq -r '.summary.overall_security_score // "N/A"' \
    "$SECURITY_REPORT" 2>/dev/null)
  echo "Security audit available: overall=$SA_OVERALL"
fi

if [ -f "$SCAFFOLD_REPORT" ]; then
  SCAFFOLD_AVAILABLE=true
  TS_GAPS=$(jq -r '.summary.critical_gaps_found // "N/A"' \
    "$SCAFFOLD_REPORT" 2>/dev/null)
  TS_COVERAGE=$(jq -r '.summary.coverage_before // "N/A"' \
    "$SCAFFOLD_REPORT" 2>/dev/null)
  echo "Test scaffold available: gaps=$TS_GAPS coverage=$TS_COVERAGE"
fi

echo "Upstream reports: health=$HEALTH_AVAILABLE" \
     "security=$SECURITY_AVAILABLE" \
     "scaffold=$SCAFFOLD_AVAILABLE"
```

When upstream reports are available, reuse their scores for overlapping
dimensions (code quality, test coverage, dependency health) rather than
re-running the same analysis. This avoids redundant work and ensures
consistency.

---

### Phase 2: Code Quality Analysis

Only runs if `DIMENSIONS` includes `code-quality` or is `all`.

If `HEALTH_AVAILABLE=true` and health-check code_quality score exists, reuse it
and skip deterministic collection for this dimension.

#### Step 2.1: Check Linter and Formatter Configuration

```bash
LINTER_PRESENT=false
FORMATTER_PRESENT=false

# Linters
([ -f .eslintrc ] || [ -f .eslintrc.js ] || [ -f .eslintrc.json ] || \
 [ -f .eslintrc.yml ] || [ -f eslint.config.js ] || \
 [ -f eslint.config.mjs ]) && LINTER_PRESENT=true
([ -f .flake8 ] || [ -f .pylintrc ]) && LINTER_PRESENT=true
[ -f .golangci.yml ] && LINTER_PRESENT=true
[ -f clippy.toml ] && LINTER_PRESENT=true
[ -f ruff.toml ] && LINTER_PRESENT=true
[ -f pyproject.toml ] && grep -q '\[tool.ruff\]' pyproject.toml \
  2>/dev/null && LINTER_PRESENT=true

# Formatters
([ -f .prettierrc ] || [ -f .prettierrc.js ] || \
 [ -f .prettierrc.json ] || [ -f .prettierrc.yml ] || \
 [ -f prettier.config.js ]) && FORMATTER_PRESENT=true
[ -f .editorconfig ] && FORMATTER_PRESENT=true
[ -f pyproject.toml ] && grep -qE '\[tool\.(black|ruff\.format)\]' \
  pyproject.toml 2>/dev/null && FORMATTER_PRESENT=true

echo "Linter: $LINTER_PRESENT"
echo "Formatter: $FORMATTER_PRESENT"
```

#### Step 2.2: Count Debt Markers

```bash
DEBT_MARKERS=$(grep -r --include="*.ts" --include="*.tsx" \
  --include="*.js" --include="*.jsx" --include="*.py" \
  --include="*.go" --include="*.rs" --include="*.java" \
  --include="*.kt" \
  -cE 'TODO|FIXME|HACK|XXX' "$ASSESS_PATH" \
  --exclude-dir=node_modules --exclude-dir=.git \
  --exclude-dir=vendor --exclude-dir=dist \
  --exclude-dir=build 2>/dev/null | \
  awk -F: '{sum+=$NF} END {print sum+0}')

echo "Debt markers (TODO/FIXME/HACK/XXX): $DEBT_MARKERS"
```

#### Step 2.3: Analyze File Sizes and Complexity

Collect source files using parallel Glob calls (faster than `find`):

```
Glob("**/*.ts")    Glob("**/*.tsx")   Glob("**/*.js")    Glob("**/*.jsx")
Glob("**/*.py")    Glob("**/*.go")    Glob("**/*.rs")    Glob("**/*.java")
Glob("**/*.kt")

Exclude: node_modules/, .git/, vendor/, dist/, build/
```

Filter out test files (`*.test.*`, `*.spec.*`). Store as `SOURCE_FILES`.

```bash
# File sizes (top 10 + total)
echo "$SOURCE_FILES" | xargs wc -l 2>/dev/null | sort -rn | head -11

echo "File sizes (top 10 + total):"
echo "$FILE_SIZES"

# Cyclomatic complexity proxy: count branching constructs per file
# Sample first 20 source files from SOURCE_FILES
for f in $(echo "$SOURCE_FILES" | head -20); do
  BRANCHES=$(grep -cE '\b(if|else|switch|case|for|while|try|catch|&&|\|\|)\b' \
    "$f" 2>/dev/null || echo 0)
  LINES=$(wc -l < "$f" | tr -d ' ')
  echo "  $f: branches=$BRANCHES lines=$LINES"
done
```

#### Step 2.4: Compute Score

AI interprets the collected metrics to compute a 0-100 score based on the
scoring rubric above. Write results to `/tmp/rr_dim_quality.json`.

---

### Phase 3: Test Coverage Analysis

Only runs if `DIMENSIONS` includes `test-coverage` or is `all`.

If `HEALTH_AVAILABLE=true` and health-check test_coverage score exists, reuse
it. If `SCAFFOLD_AVAILABLE=true`, use the scaffold report's coverage data.

#### Step 3.1: Detect Test Framework

```bash
TEST_FRAMEWORK="none"

# Node.js
if [ -f package.json ]; then
  jq -r '.devDependencies // {} | keys[]' package.json 2>/dev/null | \
    grep -qE '^(vitest|jest|mocha|ava|tap)$' && \
    TEST_FRAMEWORK=$(jq -r \
      '.devDependencies // {} | keys[]' package.json | \
      grep -E '^(vitest|jest|mocha|ava|tap)$' | head -1)
fi

# Python
([ -f pytest.ini ] || [ -f setup.cfg ] || [ -f pyproject.toml ]) && \
  grep -ql 'pytest' pytest.ini setup.cfg pyproject.toml 2>/dev/null && \
  TEST_FRAMEWORK="pytest"

# Go (built-in)
[[ " ${ECOSYSTEMS[*]} " =~ " go " ]] && TEST_FRAMEWORK="go-test"

# Rust (built-in)
[[ " ${ECOSYSTEMS[*]} " =~ " rust " ]] && TEST_FRAMEWORK="cargo-test"

# Java
[ -f pom.xml ] && grep -q 'junit' pom.xml 2>/dev/null && \
  TEST_FRAMEWORK="junit"

echo "Test framework: $TEST_FRAMEWORK"
```

#### Step 3.2: Count Test Files vs Source Files

Use parallel Glob calls (faster than `find`):

```
# Count test files — run in parallel
Glob("**/*.test.*")   Glob("**/*.spec.*")
Glob("**/*_test.*")   Glob("**/test_*")

Exclude: node_modules/, .git/, vendor/, dist/, build/
```

Collect into `TEST_FILES`. Count: `TEST_FILE_COUNT`.

Reuse `SOURCE_FILES` from Phase 2 Step 2.3 if available, otherwise collect
with the same parallel Glob pattern. Count: `SOURCE_FILE_COUNT`.

```
TEST_RATIO = TEST_FILE_COUNT / SOURCE_FILE_COUNT  (0 if no source files)
```

Report: `Test files: N`, `Source files: N`, `Test-to-source ratio: N`

#### Step 3.3: Check Coverage

```bash
COVERAGE_CONFIG=false

# Node.js
([ -f .nycrc ] || [ -f .nycrc.json ] || [ -f .c8rc.json ]) && \
  COVERAGE_CONFIG=true
[ -f vitest.config.ts ] && grep -q 'coverage' vitest.config.ts \
  2>/dev/null && COVERAGE_CONFIG=true

# Python
[ -f .coveragerc ] && COVERAGE_CONFIG=true
[ -f pyproject.toml ] && grep -q 'coverage' pyproject.toml 2>/dev/null && \
  COVERAGE_CONFIG=true

echo "Coverage config: $COVERAGE_CONFIG"
```

If `--skip-coverage-run` is NOT set and a coverage tool is available, run
coverage and capture the percentage.

#### Step 3.4: Compute Score

AI interprets metrics using the scoring rubric. Write results to
`/tmp/rr_dim_test.json`.

---

### Phase 4: Dependency Coupling Analysis

Only runs if `DIMENSIONS` includes `dependency-coupling` or is `all`.

#### Step 4.1: Build Import Graph

```bash
echo "=== Dependency Coupling: Import Graph ==="

# Node.js/TypeScript: extract import/require statements
if [[ " ${ECOSYSTEMS[*]} " =~ " nodejs " ]]; then
  grep -rn --include="*.ts" --include="*.tsx" --include="*.js" \
    --include="*.jsx" \
    -E "^(import |const .* = require\(|from ['\"])" \
    "$ASSESS_PATH" \
    --exclude-dir=node_modules --exclude-dir=dist \
    --exclude-dir=build --exclude-dir=.git \
    2>/dev/null > /tmp/rr_imports.txt || true
  IMPORT_COUNT=$(wc -l < /tmp/rr_imports.txt | tr -d ' ')
  echo "Import statements found: $IMPORT_COUNT"
fi

# Python: extract import statements
if [[ " ${ECOSYSTEMS[*]} " =~ " python " ]]; then
  grep -rn --include="*.py" \
    -E "^(import |from .+ import )" \
    "$ASSESS_PATH" \
    --exclude-dir=.git --exclude-dir=vendor \
    --exclude-dir=dist --exclude-dir=build \
    2>/dev/null > /tmp/rr_imports_py.txt || true
  PY_IMPORT_COUNT=$(wc -l < /tmp/rr_imports_py.txt | tr -d ' ')
  echo "Python import statements: $PY_IMPORT_COUNT"
fi
```

#### Step 4.2: Detect Circular Dependencies

```bash
echo "=== Dependency Coupling: Circular Dependencies ==="

# For Node.js: detect mutual imports between files
# Sample approach: find files that import each other
CIRCULAR_COUNT=0

# Build a simplified adjacency list and check for mutual imports
if [ -f /tmp/rr_imports.txt ]; then
  # Extract file:import pairs, check for A->B and B->A patterns
  while IFS=: read -r file line_num content; do
    IMPORTING=$(echo "$content" | \
      grep -oE "from ['\"](\./|\.\./)([^'\"]+)" | \
      sed "s/from ['\"]//;s/['\"]//g" | head -1)
    if [ -n "$IMPORTING" ]; then
      echo "$file -> $IMPORTING"
    fi
  done < /tmp/rr_imports.txt > /tmp/rr_edges.txt 2>/dev/null || true

  # Count files involved
  EDGE_COUNT=$(wc -l < /tmp/rr_edges.txt 2>/dev/null | tr -d ' ')
  echo "Import edges analyzed: $EDGE_COUNT"
fi

echo "Circular dependency estimate: $CIRCULAR_COUNT"
```

#### Step 4.3: Compute Fan-In and Fan-Out

```bash
echo "=== Dependency Coupling: Fan-In/Fan-Out ==="

# For each source file, count how many files import it (fan-in)
# and how many files it imports (fan-out)
# Reuse SOURCE_FILES from Phase 2, sample first 30
for f in $(echo "$SOURCE_FILES" | head -30); do
  BASENAME=$(basename "$f" | sed 's/\.[^.]*$//')
  FAN_IN=$(grep -rl --include="*.ts" --include="*.tsx" \
    --include="*.js" --include="*.jsx" --include="*.py" \
    "$BASENAME" "$ASSESS_PATH" \
    --exclude-dir=node_modules --exclude-dir=dist \
    --exclude-dir=build --exclude-dir=.git \
    2>/dev/null | wc -l | tr -d ' ')
  FAN_OUT=$(grep -cE "^(import |from |const .* = require)" "$f" \
    2>/dev/null || echo 0)
  echo "  $f: fan_in=$FAN_IN fan_out=$FAN_OUT"
done
```

#### Step 4.4: Compute Score

AI interprets coupling metrics to compute a 0-100 score. Write results to
`/tmp/rr_dim_coupling.json`.

---

### Phase 5: Business Logic & Tech Stack Analysis

Only runs if `DIMENSIONS` includes `business-logic-extraction` or
`tech-stack-viability` or is `all`.

#### Step 5.1: Detect Domain/Service Layer

```bash
echo "=== Business Logic: Domain Layer Detection ==="

# Count files matching service/domain/model/entity patterns
# Reuse SOURCE_FILES from Phase 2 Step 2.3
DOMAIN_FILES=$(echo "$SOURCE_FILES" | \
  grep -iE '(service|domain|model|entity|repository|usecase|handler)' | \
  wc -l | tr -d ' ')

TOTAL_SOURCE=$(echo "$SOURCE_FILES" | wc -l | tr -d ' ')

echo "Domain/service files: $DOMAIN_FILES of $TOTAL_SOURCE total"

# Check for hardcoded values (magic numbers, hardcoded URLs)
HARDCODED=$(grep -rn --include="*.ts" --include="*.tsx" \
  --include="*.js" --include="*.jsx" --include="*.py" \
  --include="*.go" --include="*.java" \
  -E '(http://|https://)[^"'"'"']+\.(com|io|org|net)' \
  "$ASSESS_PATH" \
  --exclude-dir=node_modules --exclude-dir=.git \
  --exclude-dir=dist --exclude-dir=build \
  --exclude-dir=vendor \
  2>/dev/null | \
  grep -vE '(test|spec|mock|example|README|CHANGELOG|\.md:)' | \
  wc -l | tr -d ' ')
echo "Hardcoded URL patterns: $HARDCODED"
```

#### Step 5.2: Framework Version Checks

```bash
echo "=== Tech Stack: Framework Version Checks ==="

# Node.js
if [[ " ${ECOSYSTEMS[*]} " =~ " nodejs " ]]; then
  NODE_VERSION=$(node --version 2>/dev/null || echo "not installed")
  echo "Node.js runtime: $NODE_VERSION"

  # Check package.json engines field
  ENGINES=$(jq -r '.engines // {}' package.json 2>/dev/null)
  echo "Engines constraint: $ENGINES"

  # Check for major frameworks and their versions
  for pkg in react next express fastify vue angular nestjs; do
    VER=$(jq -r ".dependencies[\"$pkg\"] // .devDependencies[\"$pkg\"] // empty" \
      package.json 2>/dev/null)
    [ -n "$VER" ] && echo "  $pkg: $VER"
  done
fi

# Python
if [[ " ${ECOSYSTEMS[*]} " =~ " python " ]]; then
  PYTHON_VERSION=$(python3 --version 2>/dev/null || echo "not installed")
  echo "Python runtime: $PYTHON_VERSION"

  # Check for requires-python in pyproject.toml
  if [ -f pyproject.toml ]; then
    REQUIRES_PY=$(grep 'requires-python' pyproject.toml 2>/dev/null || true)
    echo "Python constraint: $REQUIRES_PY"
  fi
fi

# Go
if [[ " ${ECOSYSTEMS[*]} " =~ " go " ]]; then
  GO_VERSION=$(grep '^go ' go.mod 2>/dev/null || echo "unknown")
  echo "Go version: $GO_VERSION"
fi

# Rust
if [[ " ${ECOSYSTEMS[*]} " =~ " rust " ]]; then
  RUST_EDITION=$(grep 'edition' Cargo.toml 2>/dev/null | head -1 || true)
  echo "Rust edition: $RUST_EDITION"
fi
```

#### Step 5.3: Compute Scores

AI interprets business logic extraction metrics and tech stack viability data to
compute two separate 0-100 scores. Write results to `/tmp/rr_dim_business.json`
and `/tmp/rr_dim_techstack.json`.

---

### Phase 6: Team & Effort Analysis

Only runs if `DIMENSIONS` includes `team-expertise` or `effort-estimation` or is
`all`.

#### Step 6.1: Git Log Analysis

```bash
echo "=== Team Expertise: Git Analysis ==="

# Active contributors in last 90 days
AUTHORS_90=$(git log --since="90 days ago" --format='%aN' 2>/dev/null | \
  sort -u | wc -l | tr -d ' ')
echo "Active contributors (90 days): $AUTHORS_90"

# Active contributors in last 365 days
AUTHORS_365=$(git log --since="365 days ago" --format='%aN' 2>/dev/null | \
  sort -u | wc -l | tr -d ' ')
echo "Active contributors (365 days): $AUTHORS_365"

# Recent commit frequency (commits per week, last 90 days)
COMMITS_90=$(git log --since="90 days ago" --oneline 2>/dev/null | \
  wc -l | tr -d ' ')
if [ "$COMMITS_90" -gt 0 ]; then
  COMMITS_PER_WEEK=$(echo "scale=1; $COMMITS_90 / 13" | bc)
else
  COMMITS_PER_WEEK="0"
fi
echo "Commits/week (90 days): $COMMITS_PER_WEEK"

# Bus factor: top contributor percentage
TOP_AUTHOR_COMMITS=$(git log --since="365 days ago" --format='%aN' \
  2>/dev/null | sort | uniq -c | sort -rn | head -1 | \
  awk '{print $1}')
TOTAL_COMMITS_YEAR=$(git log --since="365 days ago" --oneline \
  2>/dev/null | wc -l | tr -d ' ')
if [ "$TOTAL_COMMITS_YEAR" -gt 0 ] && [ -n "$TOP_AUTHOR_COMMITS" ]; then
  BUS_FACTOR=$(echo "scale=0; $TOP_AUTHOR_COMMITS * 100 / $TOTAL_COMMITS_YEAR" | bc)
else
  BUS_FACTOR=100
fi
echo "Bus factor (top contributor %): $BUS_FACTOR"

# Codebase age (first commit)
FIRST_COMMIT=$(git log --reverse --format='%ci' 2>/dev/null | \
  head -1 || echo "unknown")
echo "First commit: $FIRST_COMMIT"
```

#### Step 6.2: LOC and Scope Metrics

```bash
echo "=== Effort Estimation: Scope Metrics ==="

# Total LOC (excluding tests, node_modules, etc.)
# Reuse SOURCE_FILES from Phase 2 Step 2.3
TOTAL_LOC=$(echo "$SOURCE_FILES" | \
  xargs wc -l 2>/dev/null | tail -1 | awk '{print $1}')
echo "Total source LOC: $TOTAL_LOC"

# File count
TOTAL_FILES=$SOURCE_FILE_COUNT
echo "Total source files: $TOTAL_FILES"

# Dependency count
if [ -f package.json ]; then
  DEP_COUNT=$(jq -r \
    '(.dependencies // {} | length) + (.devDependencies // {} | length)' \
    package.json 2>/dev/null || echo 0)
  echo "npm dependencies: $DEP_COUNT"
fi

echo "Team size (provided): $TEAM_SIZE"
echo "Timeline (provided): $TIMELINE_WEEKS weeks"
```

#### Step 6.3: Change Frequency Heatmap

```bash
echo "=== Team Expertise: Change Frequency ==="

# Top 10 most frequently changed files in last 6 months
git log --since="6 months ago" --name-only --pretty=format: 2>/dev/null | \
  grep -v '^$' | sort | uniq -c | sort -rn | head -10
```

#### Step 6.4: Compute Scores

AI interprets team metrics and effort data to compute two separate 0-100 scores.
Write results to `/tmp/rr_dim_team.json` and `/tmp/rr_dim_effort.json`.

---

### Phase 7: Decision Engine

AI synthesizes all 8 dimension scores into a recommendation.

#### Step 7.1: Compute Weighted Score

```text
weighted_score = (
  code_quality         * 0.15 +
  test_coverage        * 0.15 +
  dependency_coupling  * 0.15 +
  business_logic       * 0.10 +
  tech_stack_viability * 0.15 +
  team_expertise       * 0.05 +
  risk_assessment      * 0.15 +
  effort_estimation    * 0.10
)
```

If a dimension was skipped, redistribute its weight proportionally among
assessed dimensions.

#### Step 7.2: Apply Decision Rules

**Signals suggesting rewrite:**

- Framework/runtime at end-of-life (no security patches)
- Test coverage < 20% (unsafe to refactor)
- Circular dependency count > 30% of modules
- Performance requirements incompatible with current architecture
- Tech stack viability score < 30

**Signals suggesting refactor:**

- Core business logic identifiable and sound
- Test coverage > 50% (adequate safety net)
- Team actively familiar with codebase (>3 active contributors in 90 days)
- Incremental delivery requirement (can't pause feature work)
- Tech stack viability score > 60

**Signals suggesting hybrid (strangler fig / branch by abstraction):**

- Mixed signals (some components refactorable, others not)
- Large codebase where full rewrite is too risky
- Dependency coupling score moderate (40-70)
- Some components have good test coverage, others don't

#### Step 7.3: Determine Confidence Level

| Confidence | Condition                                   |
| ---------- | ------------------------------------------- |
| Strong     | 6+ dimensions agree on direction            |
| Moderate   | 4-5 dimensions agree, minor dissent         |
| Weak       | 3 or fewer agree, significant mixed signals |

#### Step 7.4: Generate Per-Component Analysis

If the codebase has distinct modules/packages, analyze each independently:

For each component/package:

1. Apply the 8-dimension analysis scoped to that directory
2. Produce a per-component recommendation
3. Identify which components to refactor and which to rewrite

Use the `Task` tool with `model: "haiku"` to spawn parallel subagents for
independent per-component assessments.

#### Step 7.5: Build Risk/Benefit Matrix

For each of the three approaches (refactor, rewrite, hybrid):

- **Benefits**: List expected positive outcomes
- **Risks**: List potential negative outcomes
- **Effort estimate**: Person-weeks based on team size and LOC
- **Timeline estimate**: Weeks based on available timeline

#### Step 7.6: Hybrid Strategy Selection

If recommendation is "hybrid", select the most appropriate strategy:

| Strategy              | Best When                                      |
| --------------------- | ---------------------------------------------- |
| Strangler Fig         | New features can be built alongside old system |
| Branch by Abstraction | Internal modules can be swapped behind APIs    |
| Parallel Run          | Critical path needs verified correctness       |

---

### Phase 8: Report Generation

#### Step 8.1: Ensure Output Directory

```bash
mkdir -p .nightgauge
```

#### Step 8.2: Write JSON Report

Write structured report to `.nightgauge/refactor-rewrite-analysis.json` (or
custom `--output` path):

```json
{
  "schema_version": "1.0",
  "assessment_date": "ISO-8601",
  "codebase": {
    "name": "project-name",
    "root_path": "/path",
    "ecosystems": ["nodejs"],
    "is_monorepo": false,
    "packages": [],
    "total_files": 0,
    "total_loc": 0
  },
  "inputs_available": {
    "health_report": true,
    "security_audit": false,
    "test_scaffold": true
  },
  "dimensions": {
    "code_quality": {
      "score": 65,
      "status": "good",
      "weight": 0.15,
      "findings": [],
      "metrics": {}
    },
    "test_coverage": {
      "score": 45,
      "status": "fair",
      "weight": 0.15,
      "findings": [],
      "metrics": {}
    },
    "dependency_coupling": {
      "score": 55,
      "status": "fair",
      "weight": 0.15,
      "findings": [],
      "metrics": {}
    },
    "business_logic_extraction": {
      "score": 70,
      "status": "good",
      "weight": 0.1,
      "findings": [],
      "metrics": {}
    },
    "tech_stack_viability": {
      "score": 80,
      "status": "good",
      "weight": 0.15,
      "findings": [],
      "metrics": {}
    },
    "team_expertise": {
      "score": 60,
      "status": "fair",
      "weight": 0.05,
      "findings": [],
      "metrics": {}
    },
    "risk_assessment": {
      "score": 50,
      "status": "fair",
      "weight": 0.15,
      "findings": [],
      "metrics": {}
    },
    "effort_estimation": {
      "score": 55,
      "status": "fair",
      "weight": 0.1,
      "findings": [],
      "metrics": {}
    }
  },
  "recommendation": {
    "decision": "refactor|rewrite|hybrid",
    "confidence": "strong|moderate|weak",
    "rationale": "string",
    "hybrid_strategy": "strangler-fig|branch-by-abstraction|parallel-run|null",
    "hybrid_details": "string or null"
  },
  "risk_benefit_matrix": {
    "refactor": {
      "benefits": ["..."],
      "risks": ["..."],
      "effort_estimate": "string",
      "timeline_estimate": "string"
    },
    "rewrite": {
      "benefits": ["..."],
      "risks": ["..."],
      "effort_estimate": "string",
      "timeline_estimate": "string"
    },
    "hybrid": {
      "strategy": "string",
      "benefits": ["..."],
      "risks": ["..."],
      "effort_estimate": "string",
      "timeline_estimate": "string"
    }
  },
  "per_component_analysis": [
    {
      "component": "string",
      "path": "string",
      "recommendation": "refactor|rewrite|hybrid",
      "confidence": "strong|moderate|weak",
      "key_factors": ["..."],
      "scores": {}
    }
  ],
  "organizational_constraints": {
    "team_size": 1,
    "timeline_weeks": 12,
    "notes": ["..."]
  },
  "top_recommendations": [
    {
      "priority": 1,
      "action": "string",
      "effort": "XS|S|M|L|XL",
      "impact": "string"
    }
  ],
  "references": [
    "Martin Fowler - Refactoring",
    "Michael Feathers - Working Effectively with Legacy Code",
    "Sam Newman - Strangler Fig Pattern"
  ],
  "created_at": "ISO-8601"
}
```

#### Step 8.3: Write Markdown Summary

Output a human-readable report:

```text
REFACTOR vs REWRITE ANALYSIS
================================================================

Project: project-name
Ecosystems: nodejs
Assessment Date: 2026-02-21
Team Size: 1 | Timeline: 12 weeks
Monorepo: No

RECOMMENDATION: HYBRID (Strangler Fig) [MODERATE confidence]
================================================================

Rationale: The codebase shows good business logic separation and a
viable tech stack, but low test coverage and moderate coupling make
a full refactor risky. A strangler fig approach allows incremental
migration while preserving working functionality.

DIMENSION SCORES
----------------------------------------------------------------
  Code Quality:            ████████████░░░░ 65  [GOOD]
  Test Coverage:           ████████░░░░░░░░ 45  [FAIR]
  Dependency Coupling:     ██████████░░░░░░ 55  [FAIR]
  Business Logic:          ██████████████░░ 70  [GOOD]
  Tech Stack Viability:    ████████████████ 80  [GOOD]
  Team Expertise:          ████████████░░░░ 60  [FAIR]
  Risk Assessment:         ██████████░░░░░░ 50  [FAIR]
  Effort Estimation:       ██████████░░░░░░ 55  [FAIR]

RISK/BENEFIT MATRIX
----------------------------------------------------------------

  REFACTOR
    Benefits:
      + Preserves existing business logic
      + Incremental delivery — no feature freeze
      + Lower upfront cost
    Risks:
      - Low test coverage (45%) makes changes risky
      - Moderate coupling may cause cascading changes
    Effort: ~8 person-weeks | Timeline: ~10 weeks

  REWRITE
    Benefits:
      + Clean architecture from scratch
      + Eliminate all technical debt
      + Modern patterns and tooling
    Risks:
      - Business logic may be lost in translation
      - Feature freeze during rewrite
      - Timeline overrun risk (historical: 2-3x estimates)
    Effort: ~16 person-weeks | Timeline: ~20 weeks

  HYBRID (Strangler Fig) [RECOMMENDED]
    Benefits:
      + Gradual migration reduces risk
      + New features built on new architecture
      + Old system remains functional during transition
    Risks:
      - Maintaining two systems temporarily
      - Integration complexity at boundaries
    Effort: ~12 person-weeks | Timeline: ~14 weeks

PER-COMPONENT ANALYSIS
----------------------------------------------------------------

  src/auth/         -> REWRITE   [Strong]  (EOL framework, no tests)
  src/api/          -> REFACTOR  [Strong]  (good tests, clean code)
  src/services/     -> HYBRID    [Moderate] (mixed quality)
  src/utils/        -> REFACTOR  [Strong]  (standalone, well-tested)

TOP RECOMMENDATIONS (sorted by impact)
----------------------------------------------------------------
  1. [M] Add characterization tests for src/auth/ before migration
  2. [S] Establish API boundary for strangler fig integration point
  3. [L] Rewrite auth module on modern framework
  4. [M] Refactor src/services/ to reduce circular dependencies
  5. [S] Update tech stack to current LTS versions

UPSTREAM REPORT INTEGRATION
----------------------------------------------------------------
  Health Check:    available (score: 72)
  Security Audit:  not available
  Test Scaffold:   available (8 critical gaps)

----------------------------------------------------------------
Report saved: .nightgauge/refactor-rewrite-analysis.json
```

If `--format json`, write only JSON. If `--format summary`, output only
markdown. If `--format both`, write JSON and output the markdown summary.

#### Step 8.4: Verify JSON Report

```bash
python3 -m json.tool .nightgauge/refactor-rewrite-analysis.json \
  > /dev/null && \
  echo "Report written: .nightgauge/refactor-rewrite-analysis.json"
```

---

### Phase 9: Monorepo Aggregation (Conditional)

Only runs if `IS_MONOREPO=true` and `--package` was NOT specified.

#### Step 9.1: Per-Package Assessment

For each package in `PACKAGES`:

1. Run Phases 2-7 scoped to the package directory
2. Compute per-package scores and recommendations

Use the `Task` tool with `model: "haiku"` to spawn parallel subagents for
independent package assessments. Each subagent receives: package path, ecosystem
detected, dimensions to assess, and team/timeline parameters.

#### Step 9.2: Aggregate Results

Combine per-package results:

- Overall recommendation (weighted by package LOC)
- Per-package recommendation table
- Aggregate risk/benefit matrix
- Combined top recommendations

#### Step 9.3: Per-Package Breakdown in Report

Add per-package section to both JSON and markdown reports:

```json
{
  "packages": [
    {
      "name": "packages/api-server",
      "recommendation": "refactor",
      "confidence": "strong",
      "overall_score": 72,
      "key_factors": ["good test coverage", "viable stack"],
      "dimensions": {}
    },
    {
      "name": "packages/legacy-ui",
      "recommendation": "rewrite",
      "confidence": "moderate",
      "overall_score": 35,
      "key_factors": ["EOL framework", "no tests"],
      "dimensions": {}
    }
  ]
}
```

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Output Format

### JSON Schema

See Phase 8 Step 8.2 for the complete JSON report structure.

### Report Files

| File                                         | Format   | When Written            |
| -------------------------------------------- | -------- | ----------------------- |
| `.nightgauge/refactor-rewrite-analysis.json` | JSON     | `--format json/both`    |
| Console output                               | Markdown | `--format summary/both` |

---

## Error Handling

| Condition                    | Action                                        |
| ---------------------------- | --------------------------------------------- |
| No ecosystem detected        | Proceed with generic file analysis only       |
| Git not available            | Skip team expertise, note in findings         |
| Coverage tool not installed  | Skip coverage run, use ratio-based scoring    |
| `--dimensions` invalid value | Error with valid dimension list               |
| `--module` path not found    | Error with path not found message             |
| `--package` path not found   | Error with path not found message             |
| Assessment path not found    | Error with path not found message             |
| `jq` not installed           | Error with install instructions               |
| Permission denied on files   | Skip inaccessible files, note count in report |
| Large codebase timeout       | Use `head`/`--max-count` limits, sample files |
| Monorepo package not found   | Warning, skip package, continue with others   |
| Upstream report invalid JSON | Skip that report, warn, continue without it   |

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
                                                   ├──► /nightgauge:refactor-rewrite
/nightgauge:test-scaffold ───────────────────┤         |
       |                                           |    Reads: all three reports (optional)
  Writes: .nightgauge/test-scaffold-report.json    Writes: refactor-rewrite-analysis.json
                                                        |
                                                        ├──► /nightgauge:modernize-plan
                                                        |
                                                   /nightgauge:dep-modernize
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs.

---

## Configuration

This skill reads configuration from `.nightgauge/config.yaml` if present:

| Config Key                           | Default | Description               |
| ------------------------------------ | ------- | ------------------------- |
| `refactor_rewrite.default_format`    | `both`  | Default `--format` value  |
| `refactor_rewrite.skip_coverage_run` | `false` | Default for coverage run  |
| `refactor_rewrite.output_path`       | auto    | Default JSON output path  |
| `refactor_rewrite.team_size`         | `1`     | Default team size         |
| `refactor_rewrite.timeline_weeks`    | `12`    | Default timeline in weeks |

---

**Author:** nightgauge **License:** Apache-2.0
