---
name: product-audit
description: Comprehensive 8-dimension product quality audit across all Nightgauge
  repositories. Validates API alignment, epic lifecycle, documentation accuracy,
  feature parity, test coverage, security posture, dependency health, and CI/CD
  integrity. Run weekly or before major releases.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.2.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
orchestration:
  mode: fanout
  phase: dimension-audit
  ceiling: fanout
  units:
    - id: api-alignment
      role: dimension-worker
      promptRef: SKILL.md#phase-2-parallel-dimension-audit
    - id: lifecycle
      role: dimension-worker
      promptRef: SKILL.md#phase-2-parallel-dimension-audit
    - id: documentation
      role: dimension-worker
      promptRef: SKILL.md#phase-2-parallel-dimension-audit
    - id: feature-parity
      role: dimension-worker
      promptRef: SKILL.md#phase-2-parallel-dimension-audit
    - id: test-coverage
      role: dimension-worker
      promptRef: SKILL.md#phase-2-parallel-dimension-audit
    - id: security
      role: dimension-worker
      promptRef: SKILL.md#phase-2-parallel-dimension-audit
    - id: dependencies
      role: dimension-worker
      promptRef: SKILL.md#phase-2-parallel-dimension-audit
    - id: ci-cd
      role: dimension-worker
      promptRef: SKILL.md#phase-2-parallel-dimension-audit
  judge:
    mode: merge
    quorum: 1
    promptRef: SKILL.md#phase-3-synthesis
disable-model-invocation: true
---

# Product Audit Skill

Orchestrates an 8-dimension quality audit across all Nightgauge
repositories, producing a scored report with actionable findings, trend
analysis, and optional automated remediation.

## When to Use

- **Weekly** (scheduled via `product_audit.schedule` in config) for continuous
  health monitoring
- **Before major releases** to catch regressions before they ship
- **After cross-repo platform changes** to detect API alignment drift
- **Before sprint planning** to identify the highest-leverage improvements
- **When CI/CD reports anomalies** that need root-cause analysis

## Outcomes

- Scored report (0-100) across 8 dimensions with weighted overall score
- JSON report for programmatic consumption and CI enforcement
- Markdown report for human review (GitHub, Slack, email)
- Trend analysis comparing current vs. previous audits
- Optional: GitHub issues created for findings above severity threshold
- Optional: Auto-fix for safe findings (stale epics, board drift, stale
  blockers)

## Prerequisites

- All 4 repos cloned in the workspace:
  - `nightgauge/` (VSCode extension + SDK)
  - `../acme-platform/` (Cloud API)
  - `../acme-dashboard/` (Web dashboard)
  - `../acme-mobile/` (Mobile app)
- `gh` CLI authenticated (`gh auth status`)
- `jq` installed for JSON processing
- Optional: Platform API running locally for live endpoint probing
  (`docker compose up` in `acme-platform/`)

## Invocation

| Tool           | Command                                 |
| -------------- | --------------------------------------- |
| Claude Code    | `/nightgauge:product-audit [options]`   |
| GitHub Copilot | Invoke via Agent Skills extension       |
| Cursor         | Run via Agent Skills or direct SKILL.md |
| Headless       | `claude -p "/nightgauge:product-audit"` |

## Arguments

### Core Options

| Argument                 | Description                                                                       | Default     |
| ------------------------ | --------------------------------------------------------------------------------- | ----------- |
| `--create-issues`        | Auto-create GitHub issues for findings at or above `--severity`                   | `false`     |
| `--fix`                  | Auto-fix safe findings (STALE_EPIC, BOARD_STATUS_DRIFT, STALE_BLOCKER)            | `false`     |
| `--dimensions DIMS`      | Comma-separated dimension indices or names to run (e.g. `1,2` or `api,lifecycle`) | all         |
| `--compare [DATE\|LAST]` | Compare results with previous audit (YYYY-MM-DD or `last`)                        | `last`      |
| `--quick`                | Skip slow operations; use cached coverage and skip live API probing               | `false`     |
| `--ci`                   | CI mode: exit 1 if overall score below `--threshold`                              | `false`     |
| `--threshold N`          | Minimum acceptable score for `--ci` mode                                          | `75`        |
| `--max-parallel N`       | Max concurrent dimension subagents                                                | `4`         |
| `--output-format FORMAT` | Output format: `json`, `markdown`, or `both`                                      | `both`      |
| `--severity LEVEL`       | Minimum severity for `--create-issues`: `critical`, `high`, `medium`, `low`       | `high`      |
| `--config PATH`          | Custom `.nightgauge/config.yaml` path                                             | auto-detect |
| `--workspace ROOT`       | Workspace root directory                                                          | auto-detect |
| `--verbose`              | Verbose logging                                                                   | `false`     |
| `--dry-run`              | Run analysis but do not create issues or apply fixes                              | `false`     |

### Dimension Names and Aliases

| Index | Name             | Aliases              |
| ----- | ---------------- | -------------------- |
| 1     | `api_alignment`  | `api`, `alignment`   |
| 2     | `lifecycle`      | `epic`, `epics`      |
| 3     | `documentation`  | `docs`, `doc`        |
| 4     | `feature_parity` | `parity`, `features` |
| 5     | `test_coverage`  | `tests`, `coverage`  |
| 6     | `security`       | `sec`                |
| 7     | `dependencies`   | `deps`               |
| 8     | `ci_cd`          | `ci`                 |

### Examples

```bash
# Full audit (report only, no side effects)
/nightgauge:product-audit

# Full audit with issue creation for high+ severity findings
/nightgauge:product-audit --create-issues

# API alignment and lifecycle dimensions only
/nightgauge:product-audit --dimensions 1,2

# Quick check using cached test coverage
/nightgauge:product-audit --quick

# CI mode — fail build if score below 80
/nightgauge:product-audit --ci --threshold 80

# Compare with specific previous audit date
/nightgauge:product-audit --compare 2026-03-16

# Create issues for medium+ severity, verbose output
/nightgauge:product-audit --create-issues --severity medium --verbose

# Auto-fix safe findings (dry run first to preview)
/nightgauge:product-audit --fix --dry-run
/nightgauge:product-audit --fix

# JSON output only for CI artifact
/nightgauge:product-audit --output-format json --ci --threshold 75
```

---

## Orchestration

This skill declares an `orchestration:` frontmatter block (`mode: fanout`) that
fans the eight audit dimensions out as parallel worker units (Phase 2), then a
merge judge synthesizes their per-dimension scores into the weighted composite
(Phase 3). The block is consumed by the capability-routed `WorkflowEngine` (epic
#3899); see
[docs/WORKFLOW_ORCHESTRATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/WORKFLOW_ORCHESTRATION.md). Each
unit's `promptRef` points at the SAME dimension-audit phase the prose
**Workflow** below walks, so providers without an orchestration capability run
the eight dimensions sequentially in one agent (bounded by `--max-parallel`) —
the prose stays the portability floor.

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

### Phase 1: Discovery

**Duration**: ~10s

1. **Detect workspace root**:

   ```bash
   WORKSPACE_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
   if [ -n "$1" ] && [[ "$1" == "--workspace"* ]]; then
     WORKSPACE_ROOT="${1#*=}"
   fi
   ```

2. **Verify repository presence**:

   ```bash
   REPOS=(
     "nightgauge:$WORKSPACE_ROOT"
     "acme-platform:$WORKSPACE_ROOT/../acme-platform"
     "acme-dashboard:$WORKSPACE_ROOT/../acme-dashboard"
     "acme-mobile:$WORKSPACE_ROOT/../acme-mobile"
   )
   REPOS_AVAILABLE=()
   REPOS_MISSING=()
   for REPO_DEF in "${REPOS[@]}"; do
     NAME="${REPO_DEF%%:*}"
     PATH="${REPO_DEF##*:}"
     if [ -d "$PATH/.git" ] || [ -d "$PATH/git" ]; then
       REPOS_AVAILABLE+=("$NAME:$PATH")
     else
       REPOS_MISSING+=("$NAME")
     fi
   done
   ```

3. **Read configuration**:

   ```bash
   CONFIG_FILE="${WORKSPACE_ROOT}/.nightgauge/config.yaml"
   if [ -f "$CONFIG_FILE" ]; then
     # Parse product_audit section with yq or python
     AUDIT_CONFIG=$(python3 -c "
   import sys, yaml
   with open('$CONFIG_FILE') as f:
     c = yaml.safe_load(f)
   import json
   print(json.dumps(c.get('product_audit', {})))
   " 2>/dev/null || echo '{}')
   fi
   ```

4. **Parse CLI arguments**:
   Parse `$@` for all `--` flags. Set default values for unspecified flags.
   Resolve dimension aliases to canonical names.

5. **Load previous audit** (for trend analysis):

   ```bash
   AUDIT_DIR="${WORKSPACE_ROOT}/.nightgauge/audit"
   LATEST_AUDIT=$(ls -t "${AUDIT_DIR}/history/product-audit-*.json" 2>/dev/null | head -1)
   ```

6. **Emit discovery summary**: List repos found/missing, dimensions to run,
   flags active, previous audit date (if found).

---

### Phase 2: Parallel Dimension Audit

**Duration**: ~150s with 4 subagents
**Subagent grouping** (designed to balance runtime):

| Subagent | Dimensions                             | Estimated runtime |
| -------- | -------------------------------------- | ----------------- |
| SA-1     | API Alignment (1) + Feature Parity (4) | ~105s             |
| SA-2     | Lifecycle (2) + CI/CD (8)              | ~55s              |
| SA-3     | Documentation (3) + Security (6)       | ~50s              |
| SA-4     | Test Coverage (5) + Dependencies (7)   | ~150s             |

Spawn all 4 subagents **simultaneously** using the `Task` tool in a single
message. Each subagent writes its findings to
`.nightgauge/audit/dimension-{N}.json`.

**Subagent coordination protocol**:

- Each subagent reads config from `.nightgauge/config.yaml` → `product_audit.dimensions.{name}` section
- Each subagent reads the parity config (if assigned feature_parity)
- Each subagent writes to its dimension file atomically (complete JSON, not streaming)
- Main agent waits for all 4 subagents to finish (they were spawned via the `Task` tool, so their completion is awaited directly — no polling loop, and never a foreground `sleep` wait; see `skills/_shared/GOTCHAS.md`)

#### Subagent 1: API Alignment + Feature Parity

**API Alignment** (Dimension 1):

1. Scan all client repos for API calls:

   ```bash
   # Flutter — Dart HTTP calls
   grep -rn "dio\.\|http\.\|ApiService\|httpClient" \
     "${FLUTTER_PATH}/lib" --include="*.dart" | grep -E "(get|post|put|delete|patch)\(" \
     > /tmp/flutter-calls.txt

   # Angular — HTTP client calls
   grep -rn "http\.\|HttpClient\|apiUrl" \
     "${ANGULAR_PATH}/src" --include="*.ts" | grep -E "\.get\(|\.post\(|\.put\(|\.delete\(" \
     > /tmp/angular-calls.txt

   # VSCode extension — IPC/platform calls
   grep -rn "platform\.\|apiClient\.\|fetch(" \
     "${VSCODE_PATH}/src" --include="*.ts" | grep -E "(/v1/|apiUrl)" \
     > /tmp/vscode-calls.txt
   ```

2. Extract platform routes:

   ```bash
   grep -rn "app\.\(get\|post\|put\|delete\|patch\)\|router\.\(get\|post\|put\|delete\|patch\)" \
     "${PLATFORM_PATH}/src" --include="*.ts" -h \
     | grep -oE '"[^"]*"' | sort -u > /tmp/platform-routes.txt
   ```

3. Compare: for each client call, check if the endpoint exists in platform routes.
   Record PATH_MISMATCH, METHOD_MISMATCH, AUTH_MISMATCH, RESPONSE_SHAPE_MISMATCH.

4. If `--quick` not set and platform is running:

   ```bash
   curl -sf http://localhost:3000/health && PLATFORM_RUNNING=true || PLATFORM_RUNNING=false
   ```

   If running, probe each client endpoint for live validation.

5. Write `.nightgauge/audit/dimension-1.json` using finding schema.

**Feature Parity** (Dimension 4):

1. Load `.nightgauge/audit/parity-config.json`
2. For each feature in the matrix, check each client for implementation evidence:
   ```bash
   FEATURE_ID="auth.login"
   grep -rn "login\|signIn\|authenticate" "${FLUTTER_PATH}/lib" --include="*.dart" | wc -l
   ```
3. Assign coverage level (FULL/PARTIAL/STUB/MISSING) per client based on:
   - FULL: Service implementation + UI + tests all found
   - PARTIAL: Service + UI, no tests
   - STUB: Keyword exists but implementation is minimal
   - MISSING: No evidence of implementation
4. Score = weighted average of all features × coverage level scores
5. Write `.nightgauge/audit/dimension-4.json`

#### Subagent 2: Lifecycle + CI/CD

**Epic Lifecycle** (Dimension 2):

1. Run the lifecycle audit via the Go binary:

   ```bash
   nightgauge epic check-lifecycle --sweep --json > /tmp/lifecycle-raw.json
   ```

   This detects all four lifecycle categories across all open epics and issues:
   - **STALE_EPIC**: Open epic with all sub-issues already closed
   - **BOARD_STATUS_DRIFT**: Issue is closed but board status is not "Done"
   - **PREMATURE_DONE**: Issue is still open but board status is "Done"
   - **ORPHANED_ISSUE**: Open issue with no entry on the project board
   - **STALE_BLOCKER**: Issue blocked by an already-closed issue

2. Parse findings from the JSON output:

   ```bash
   FINDINGS=$(jq '.findings' /tmp/lifecycle-raw.json)
   SUMMARY=$(jq '.summary' /tmp/lifecycle-raw.json)
   TOTAL=$(jq '.summary.total' /tmp/lifecycle-raw.json)
   ```

3. Record auto-fixable findings (STALE_EPIC, BOARD_STATUS_DRIFT, STALE_BLOCKER have
   fix support via `nightgauge audit lifecycle --fix`).

4. Write `.nightgauge/audit/dimension-2.json`

**CI/CD Integrity** (Dimension 8):

1. Scan all `.github/workflows/*.yml` files in all repos:
   ```bash
   for REPO_PATH in "${REPOS_AVAILABLE[@]}"; do
     find "${REPO_PATH##*:}/.github/workflows" -name "*.yml" -o -name "*.yaml" 2>/dev/null
   done
   ```
2. Check each workflow for:
   - **DISABLED_WORKFLOW**: `on:` section missing or commented out
   - **CONTINUE_ON_ERROR**: `continue-on-error: true` in test/build steps
   - **MISSING_COVERAGE_ENFORCE**: No coverage threshold check in CI
   - **MISSING_REQUIRED_CHECK**: Required checks not in workflow
   - **OUTDATED_ACTION**: Actions using `@v1` or `@v2` when `@v4+` is available
3. Write `.nightgauge/audit/dimension-8.json`

#### Subagent 3: Documentation + Security

**Documentation Accuracy** (Dimension 3):

1. Scan documented API endpoints in all doc files:
   ```bash
   grep -rn "POST /\|GET /\|PUT /\|DELETE /\|PATCH /" \
     "${WORKSPACE_ROOT}/docs/" "${WORKSPACE_ROOT}/../acme-platform/docs/" \
     --include="*.md" > /tmp/doc-endpoints.txt
   ```
2. Cross-reference with actual platform routes for drift
3. Check OpenAPI spec freshness:
   ```bash
   SPEC_FILE=$(find "${PLATFORM_PATH}" -name "openapi*.yaml" -o -name "openapi*.json" 2>/dev/null | head -1)
   if [ -f "$SPEC_FILE" ]; then
     SPEC_MODIFIED=$(git -C "${PLATFORM_PATH}" log --oneline -1 -- "$SPEC_FILE" 2>/dev/null)
     ROUTE_MODIFIED=$(git -C "${PLATFORM_PATH}" log --oneline -1 -- "src/routes/" 2>/dev/null)
     # If routes are newer than spec → STALE_OPENAPI_SPEC finding
   fi
   ```
4. Check ECOSYSTEM.md endpoint table accuracy
5. Check CLAUDE.md for stale references
6. Write `.nightgauge/audit/dimension-3.json`

**Security** (Dimension 6):

1. Scan for hardcoded secrets:
   ```bash
   grep -rn "API_KEY\s*=\s*['\"][a-zA-Z0-9]\|SECRET\s*=\s*['\"][a-zA-Z0-9]\|password\s*=\s*['\"]" \
     --include="*.ts" --include="*.dart" --include="*.js" \
     --exclude-dir=node_modules --exclude-dir=.git \
     "${WORKSPACE_ROOT}" "${ANGULAR_PATH}" "${FLUTTER_PATH}" 2>/dev/null
   ```
2. Scan for unvalidated inputs, SQL injection patterns, unsafe eval usage
3. Check for sensitive data in logs or error messages
4. Check dependency vulnerabilities (npm audit, if available):
   ```bash
   npm audit --json 2>/dev/null | jq '.vulnerabilities | to_entries[] | select(.value.severity == "high" or .value.severity == "critical")'
   ```
5. Write `.nightgauge/audit/dimension-6.json`

#### Subagent 4: Test Coverage + Dependencies

**Test Coverage** (Dimension 5):

1. Check for existing coverage reports:
   ```bash
   find "${WORKSPACE_ROOT}" -name "coverage-summary.json" -newer ".nightgauge/audit/.coverage-cache-marker" 2>/dev/null
   ```
2. If `--quick` and coverage reports exist and are fresh (< 24h), use cached.
   Otherwise run coverage (with timeout):
   ```bash
   # Use the project's detected test command (Phase 1) in single-run mode with
   # coverage, once per workspace that has tests. Example for a vitest workspace:
   timeout 90 npx -w <workspace> vitest run --coverage --reporter=json 2>/dev/null
   ```
3. Parse coverage reports for files below threshold:
   ```bash
   jq '.total | {lines: .lines.pct, branches: .branches.pct, functions: .functions.pct}' coverage/coverage-summary.json
   ```
4. Check critical path files against threshold from config
5. Detect test files that are stubs (< 5 assertions)
6. Write `.nightgauge/audit/dimension-5.json`

**Dependencies** (Dimension 7):

1. Run `npm audit` in each Node.js repo:
   ```bash
   npm audit --json 2>/dev/null
   ```
2. Parse outdated packages:
   ```bash
   npm outdated --json 2>/dev/null
   ```
3. Check for packages with known CVEs
4. Check Dart (Flutter) dependencies:
   ```bash
   cd "${FLUTTER_PATH}" && flutter pub outdated --json 2>/dev/null
   ```
5. Write `.nightgauge/audit/dimension-7.json`

---

### Phase 3: Synthesis

**Duration**: ~20s

1. **Collect dimension files**:

   ```bash
   DIMENSION_FILES=()
   for N in 1 2 3 4 5 6 7 8; do
     DIM_FILE=".nightgauge/audit/dimension-${N}.json"
     [ -f "$DIM_FILE" ] && DIMENSION_FILES+=("$DIM_FILE")
   done
   ```

2. **Compute weighted overall score**:

   ```bash
   WEIGHTS='{"api_alignment":0.20,"lifecycle":0.10,"documentation":0.10,"feature_parity":0.15,"test_coverage":0.20,"security":0.15,"dependencies":0.05,"ci_cd":0.05}'

   # Override with config weights if set
   OVERALL_SCORE=$(jq -n \
     --argjson weights "$WEIGHTS" \
     --slurpfile dims ".nightgauge/audit/dimension-*.json" '
     ($dims | map({(.name): .score}) | add) as $scores |
     $weights | to_entries | map(.value * ($scores[.key] // 0)) | add | floor
   ')
   ```

3. **Collect critical findings** (severity=critical across all dimensions):

   ```bash
   jq -s '[.[].findings[] | select(.severity == "critical")] | unique_by(.id)' \
     .nightgauge/audit/dimension-*.json
   ```

4. **Compute trend analysis** (if previous audit exists):
   - Load `LATEST_AUDIT` from Phase 1
   - Compute delta per dimension: `current_score - previous_score`
   - Classify as `improving` (delta > 0), `degrading` (delta < 0), `stable`
   - Generate trend recommendations based on delta direction

5. **Assemble unified report JSON** per `product-audit-report-v1.json` schema.

6. **Clean up transient files**:
   ```bash
   rm -f .nightgauge/audit/dimension-*.json
   ```

---

### Phase 4: Report Generation

**Duration**: ~5s

1. **Write JSON report**:

   ```bash
   TIMESTAMP=$(date -u +%Y-%m-%dT%H%M%SZ)
   REPORT_JSON=".nightgauge/audit/product-audit-${TIMESTAMP}.json"
   REPORT_LATEST=".nightgauge/audit/product-audit-latest.json"

   jq -n ... > "$REPORT_JSON"
   cp "$REPORT_JSON" "$REPORT_LATEST"
   cp "$REPORT_JSON" ".nightgauge/audit/history/product-audit-$(date -u +%Y-%m-%d).json"
   ```

2. **Write Markdown report** (from template or inline):
   - Executive summary with score and emoji indicators
   - Per-dimension sections with scores and top findings
   - Trend table
   - Auto-fix recommendations
   - Next steps (prioritized by severity × impact)

   ```bash
   REPORT_MD=".nightgauge/audit/PRODUCT_AUDIT_REPORT.md"
   ```

3. **If `--output-format json`**: skip markdown generation.
   **If `--output-format markdown`**: skip JSON writing.

4. **Emit summary to stdout** regardless of output-format:
   ```
   ╔══════════════════════════════════════════╗
   ║  Product Audit Report — 2026-03-23        ║
   ╠══════════════════════════════════════════╣
   ║  Overall Score:  78/100  (⬆ +6)          ║
   ╠═══════════════╦═══════╦══════════════════╣
   ║  Dimension    ║ Score ║ Findings         ║
   ╠═══════════════╬═══════╬══════════════════╣
   ║  API Align    ║  85   ║  3 (1 high)      ║
   ║  Lifecycle    ║  72   ║  12 (2 critical) ║
   ║  Docs         ║  68   ║  4 (0 critical)  ║
   ║  Parity       ║  78   ║  5 (0 critical)  ║
   ║  Tests        ║  65   ║  7 (0 critical)  ║
   ║  Security     ║  92   ║  1 (0 critical)  ║
   ║  Deps         ║  88   ║  2 (0 critical)  ║
   ║  CI/CD        ║  81   ║  3 (0 critical)  ║
   ╚═══════════════╩═══════╩══════════════════╝
   ```

---

### Phase 5: Issue Creation (Conditional)

**Skip if**: `--create-issues` not set, or `--dry-run` is set.

For each finding at or above `--severity` threshold:

1. **Deduplicate against existing issues**:

   ```bash
   EXISTING=$(gh issue list --label "audit:${DIMENSION}" --json number,title --jq '.[].title')
   # Skip if finding.id already in an existing issue body
   ```

2. **Create GitHub issue**:

   ```bash
   gh issue create \
     --title "[Audit] ${FINDING_CATEGORY}: ${FINDING_SUMMARY}" \
     --body "$(cat <<EOF
   ## Finding

   **Dimension**: ${DIMENSION_NAME}
   **Severity**: ${SEVERITY}
   **Confidence**: ${CONFIDENCE}%
   **ID**: \`${FINDING_ID}\` (stable across audits)

   ## Detail

   ${FINDING_DETAIL}

   ## Affected Files

   $(for f in "${FINDING_FILES[@]}"; do echo "- \`${f.path}\`:${f.line}"; done)

   ## Suggested Action

   ${SUGGESTED_ACTION}

   ---
   *Detected by \`/nightgauge:product-audit\` on $(date -u +%Y-%m-%d)*
   *Audit run ID: \`${RUN_ID}\`*
   EOF
   )" \
     --label "audit:${DIMENSION}" \
     --label "severity:${SEVERITY}"
   ```

3. Record created issue numbers in `execution_metadata.issues_created`.

---

### Phase 6: Auto-Fix (Conditional)

**Skip if**: `--fix` not set, or `--dry-run` is set.

Only apply fixes to findings in the auto-fix whitelist from config:

- `STALE_EPIC` — close epic:
  ```bash
  nightgauge issue close ${EPIC_NUMBER}
  ```
- `BOARD_STATUS_DRIFT` — move issue to "Done" on project board:
  ```bash
  nightgauge project move-status ${ISSUE_NUMBER} done
  ```
- `STALE_BLOCKER` — remove blocking relationship:
  ```bash
  nightgauge issue remove-blocked-by ${BLOCKED_ISSUE_NUMBER} ${BLOCKER_ISSUE_NUMBER}
  ```

For each fix applied:

1. Log the action taken
2. Increment `execution_metadata.issues_auto_fixed`
3. Update the finding's `auto_fixable` to `false` in the report (to indicate it was applied)

If `--dry-run`: print all fixes that WOULD be applied but do not execute.

---

### Phase 7: CI Exit Code (Conditional)

**Only if `--ci` is set**:

```bash
if [ "$CI_MODE" = "true" ]; then
  THRESHOLD="${THRESHOLD:-75}"
  if [ "$OVERALL_SCORE" -lt "$THRESHOLD" ]; then
    echo "❌ CI FAILED: Overall score ${OVERALL_SCORE} is below threshold ${THRESHOLD}"
    echo "   Report: .nightgauge/audit/PRODUCT_AUDIT_REPORT.md"
    exit 1
  fi
  if [ "$FAIL_ON_CRITICAL" = "true" ]; then
    CRITICAL_COUNT=$(jq '.critical_findings | length' "$REPORT_JSON")
    if [ "$CRITICAL_COUNT" -gt 0 ]; then
      echo "❌ CI FAILED: ${CRITICAL_COUNT} critical findings detected"
      exit 1
    fi
  fi
  echo "✅ CI PASSED: Overall score ${OVERALL_SCORE} ≥ ${THRESHOLD}"
  exit 0
fi
```

---

## Output Files

| File                                                  | Description                                 |
| ----------------------------------------------------- | ------------------------------------------- |
| `.nightgauge/audit/product-audit-{timestamp}.json`    | JSON report (latest and archived)           |
| `.nightgauge/audit/product-audit-latest.json`         | Symlink/copy of the most recent JSON report |
| `.nightgauge/audit/PRODUCT_AUDIT_REPORT.md`           | Human-readable markdown report (latest)     |
| `.nightgauge/audit/history/product-audit-{date}.json` | Historical archive for trend analysis       |

**Transient files** (cleaned up after synthesis):

- `.nightgauge/audit/dimension-{1-8}.json` — per-dimension raw findings

**Managed by user** (not generated by skill):

- `.nightgauge/audit/parity-config.json` — feature parity matrix definitions
- `.nightgauge/audit/REPORT_TEMPLATE.md` — custom report template (optional)

---

## Scoring Formula

```
overall_score = floor(Σ (dimension_score[i] × weight[i]))

Default weights:
  api_alignment:  0.20   # Client-server mismatches are high impact
  test_coverage:  0.20   # Coverage is directly linked to reliability
  security:       0.15   # Security issues are serious
  feature_parity: 0.15   # Feature gaps affect users across clients
  lifecycle:      0.10   # Epic hygiene matters for team velocity
  documentation:  0.10   # Docs drift hurts developer onboarding
  dependencies:   0.05   # Outdated deps are background risk
  ci_cd:          0.05   # CI health is important but self-correcting
```

Weights are overridable via `product_audit.dimension_weights` in
`.nightgauge/config.yaml`. Must sum to 1.0.

---

## Severity Levels

| Level      | Meaning                                              | Example                                                   |
| ---------- | ---------------------------------------------------- | --------------------------------------------------------- |
| `critical` | Production risk, data loss, security breach possible | Hardcoded API key, broken auth flow                       |
| `high`     | Significant impact on users or team velocity         | API path mismatch causing 404, stale epic blocking sprint |
| `medium`   | Notable degradation, fix within sprint               | OpenAPI spec 2 weeks stale, coverage below threshold      |
| `low`      | Cosmetic or minor debt                               | Outdated patch version, cosmetic doc gap                  |

---

## Confidence Scoring

Each finding includes a `confidence` score (0-100):

| Range  | Meaning                                            |
| ------ | -------------------------------------------------- |
| 90-100 | Confirmed via live API probe or exact string match |
| 70-89  | High-confidence static analysis                    |
| 50-69  | Heuristic-based detection (likely correct)         |
| 30-49  | Pattern-matched but ambiguous (review suggested)   |
| 0-29   | Low confidence — informational only                |

Findings with confidence < 30 are not included in scores or issue creation by
default. Override with `--verbose` to surface them.

---

## Error Handling

| Condition                          | Behavior                                                       |
| ---------------------------------- | -------------------------------------------------------------- |
| Repository not found               | Warn and skip; include in `execution_metadata.repos_skipped`   |
| Subagent timeout (> 3 min)         | Mark dimension as `timeout`; use score of 0 for that dimension |
| Platform API not running           | Skip live probing; use static analysis only                    |
| No previous audit found            | Skip trend analysis; emit `"direction": "first_run"`           |
| `parity-config.json` missing       | Skip feature parity dimension; log warning                     |
| JSON parse error in dimension file | Skip that dimension; log error                                 |
| GitHub API rate limit              | Retry with exponential backoff (3 attempts, 5s initial)        |

---

## CI Integration Example

```yaml
# .github/workflows/product-audit.yml
name: Weekly Product Audit

on:
  schedule:
    - cron: "0 9 * * 1" # Monday 9 AM UTC
  workflow_dispatch:
    inputs:
      threshold:
        description: "Minimum score to pass"
        default: "75"

jobs:
  audit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Run Product Audit
        run: |
          claude -p "/nightgauge:product-audit --ci --threshold ${{ github.event.inputs.threshold || '75' }}" \
            --output-format json
      - name: Upload Audit Report
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: product-audit-report
          path: .nightgauge/audit/
```

---

## Author

nightgauge
