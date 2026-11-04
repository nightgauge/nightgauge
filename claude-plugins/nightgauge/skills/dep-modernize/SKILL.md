---
name: dep-modernize
description: Dependency Modernization Engine — safely identifies and updates outdated or
  vulnerable dependencies with compatibility analysis, breaking change
  detection, and staged rollout recommendations. Use when auditing or upgrading
  dependencies, or after a security advisory flags a vulnerable package.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.3.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
orchestration:
  mode: phased
  phase: modernize
  ceiling: fanout
  units:
    - id: breaking-change-analysis
      role: phase-worker
      promptRef: SKILL.md#phase-3-breaking-change-analysis-ai-assisted
    - id: staged-update-groups
      role: phase-worker
      promptRef: SKILL.md#phase-4-staged-update-groups-deterministic
    - id: apply-updates
      role: phase-worker
      promptRef: SKILL.md#phase-6-apply-updates-optional-auto-fix-mode
  judge:
    mode: gate
    gate: true
    quorum: 1
    promptRef: SKILL.md#phase-6-apply-updates-optional-auto-fix-mode
disable-model-invocation: true
---

# Nightgauge Dep Modernize

## Description

Dependency Modernization Engine that safely identifies and updates outdated or
vulnerable dependencies across multiple ecosystems. Uses deterministic tool
commands for data collection and inventory; AI interprets raw data for breaking
change analysis, replacement suggestions, and staged update group planning.

**Use Cases:**

- Bulk dependency updates after a period of neglect or version freeze
- Pre-release dependency hygiene (clear CVEs and major-version lag)
- Identifying and replacing unmaintained or deprecated packages
- Generating a staged rollout plan to reduce regression risk
- Automated safe updates via `--auto-fix` with built-in test verification

**When to Use:**

- Before a major release or security review
- When `npm audit`, `cargo audit`, or `pip-audit` surfaces critical CVEs
- On a regular cadence (monthly recommended) for dependency hygiene
- After running `health-check` or `security-audit` and finding dependency gaps
- When planning a brownfield modernization effort (as input to `modernize-plan`)

**Relationship to Other Skills:**

| Skill          | Focus                                | Relationship              |
| -------------- | ------------------------------------ | ------------------------- |
| Health Check   | Codebase quality across 6 dimensions | Optional input (upstream) |
| Security Audit | Security posture across 7 dimensions | Optional input (upstream) |
| Dep Modernize  | Dependency-specific update engine    | **This skill**            |
| Modernize Plan | Phased roadmap from all assessments  | Consumer (downstream)     |
| Test Scaffold  | Test coverage gap analysis           | Optional peer context     |

**Architecture Pattern:**

This skill follows the **deterministic vs probabilistic** principle:

- **Deterministic**: Ecosystem detection, dependency inventory, outdated/audit
  commands, dependency graph construction, topological sort for update groups,
  test execution, and rollback decisions are all deterministic and reproducible
- **Probabilistic**: Breaking change analysis from changelogs/release notes,
  risk assessment of major updates, and replacement suggestions for deprecated
  or unmaintained packages use AI interpretation via `Task` with
  `model: "haiku"`

## Invocation

| Tool        | Command                                 |
| ----------- | --------------------------------------- |
| Claude Code | `/nightgauge:dep-modernize [options]`   |
| Copilot     | Invoke via Agent Skills extension       |
| Cursor      | Run via Agent Skills or direct SKILL.md |
| Headless    | `claude -p "/nightgauge:dep-modernize"` |

## Arguments

| Argument                   | Type   | Default | Description                                  |
| -------------------------- | ------ | ------- | -------------------------------------------- |
| `--path`                   | DIR    | `.`     | Root directory to analyze                    |
| `--package`                | PKG    | all     | Specific monorepo package to target          |
| `--format`                 | FORMAT | `both`  | `summary`, `json`, or `both`                 |
| `--dry-run`                | flag   | true    | Preview changes without applying them        |
| `--auto-fix`               | flag   | false   | Apply safe updates automatically             |
| `--staged`                 | flag   | false   | Create per-group branches/PRs                |
| `--output`                 | FILE   | auto    | Custom JSON output path                      |
| `--severity`               | LEVEL  | `low`   | Minimum severity to report                   |
| `--ecosystems`             | LIST   | auto    | Comma-separated ecosystems to check          |
| `--skip-breaking-analysis` | flag   | false   | Skip AI changelog analysis for major updates |

### Examples

```bash
# Full dependency modernization analysis (dry-run by default)
/nightgauge:dep-modernize

# Analyze a specific directory
/nightgauge:dep-modernize --path /path/to/project

# Show only high and critical severity issues
/nightgauge:dep-modernize --severity high

# Apply safe (patch/minor) updates automatically, verify with tests
/nightgauge:dep-modernize --auto-fix

# Generate per-group branches and PRs for staged rollout
/nightgauge:dep-modernize --auto-fix --staged

# Target only Node.js and Python ecosystems
/nightgauge:dep-modernize --ecosystems nodejs,python

# Target a specific monorepo package
/nightgauge:dep-modernize --package packages/api-server

# JSON output only, skip AI changelog analysis (faster)
/nightgauge:dep-modernize --format json --skip-breaking-analysis

# Custom output path
/nightgauge:dep-modernize --output /tmp/dep-report.json
```

---

## Prerequisites

- Bash shell
- `jq` installed (for JSON processing)
- Ecosystem package manager tools (detected automatically):
  - Node.js: `npm` or `yarn`
  - Python: `pip`, `pip-audit` (optional), `poetry` (optional)
  - Go: `go`, `govulncheck` (optional)
  - Rust: `cargo`, `cargo-audit` (optional), `cargo-outdated` (optional)
  - Java/JVM: `mvn` or `gradle` (optional)
- `gh` CLI authenticated (only required if `--staged` is used for PR creation)

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with a clear error message.

---

## Orchestration

This skill declares an `orchestration:` frontmatter block (`mode: phased`)
modelling modernization as ordered phases — breaking-change analysis (Phase 3,
parallelized per major-update package), staged update groups (Phase 4), and
apply-updates (Phase 6) — closed by a **gate** judge that runs the post-update
regression check (`judge.gate: true`) before any change is accepted. The block
is consumed by the capability-routed `WorkflowEngine` (epic #3899); see
[docs/WORKFLOW_ORCHESTRATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/WORKFLOW_ORCHESTRATION.md). Each
unit's `promptRef` points at the SAME phase the prose **Workflow** below walks,
so providers without an orchestration capability run the phases sequentially in
one agent — the prose stays the portability floor.

## Workflow

### Phase 0: Setup & Detection (Deterministic)

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

#### Step 0.1: Parse Arguments

Extract options from invocation:

```bash
ASSESS_PATH="."
PACKAGE_FILTER=""
OUTPUT_FORMAT="both"
DRY_RUN=true
AUTO_FIX=false
STAGED=false
OUTPUT_FILE=""
SEVERITY_FILTER="low"
ECOSYSTEMS_FILTER=""
SKIP_BREAKING_ANALYSIS=false

# Parse arguments from invocation:
# --path DIR: set ASSESS_PATH
# --package PKG: set PACKAGE_FILTER
# --format FORMAT: set OUTPUT_FORMAT
# --dry-run: set DRY_RUN=true (default is already true)
# --auto-fix: set AUTO_FIX=true, DRY_RUN=false
# --staged: set STAGED=true
# --output FILE: set OUTPUT_FILE
# --severity LEVEL: set SEVERITY_FILTER
# --ecosystems LIST: set ECOSYSTEMS_FILTER (comma-separated)
# --skip-breaking-analysis: set SKIP_BREAKING_ANALYSIS=true

# Validate AUTO_FIX/DRY_RUN relationship
if [ "$AUTO_FIX" = true ]; then
  DRY_RUN=false
fi

# Validate severity
case "$SEVERITY_FILTER" in
  low|medium|high|critical) ;;
  *)
    echo "ERROR: Invalid --severity value: $SEVERITY_FILTER"
    echo "Valid values: low, medium, high, critical"
    exit 1
    ;;
esac
```

#### Step 0.2: Detect Ecosystems

Scan for ecosystem indicators in the target path:

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

# Apply --ecosystems filter if provided
if [ -n "$ECOSYSTEMS_FILTER" ]; then
  FILTERED_ECOSYSTEMS=()
  IFS=',' read -ra REQUESTED <<< "$ECOSYSTEMS_FILTER"
  for req in "${REQUESTED[@]}"; do
    for detected in "${ECOSYSTEMS[@]}"; do
      [ "$req" = "$detected" ] && FILTERED_ECOSYSTEMS+=("$detected")
    done
  done
  ECOSYSTEMS=("${FILTERED_ECOSYSTEMS[@]}")
fi

if [ ${#ECOSYSTEMS[@]} -eq 0 ]; then
  echo "ERROR: No supported ecosystem detected (or none matched --ecosystems filter)."
  echo ""
  echo "Supported ecosystems detected by:"
  echo "  nodejs  — package.json"
  echo "  python  — pyproject.toml, setup.py, requirements.txt"
  echo "  go      — go.mod"
  echo "  rust    — Cargo.toml"
  echo "  java    — pom.xml, build.gradle, build.gradle.kts"
  exit 1
fi

echo "Ecosystems detected: ${ECOSYSTEMS[*]}"
```

#### Step 0.3: Detect Monorepo Workspaces

Check for workspace/monorepo indicators:

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

# Apply --package filter
if [ -n "$PACKAGE_FILTER" ]; then
  if [ ! -d "$PACKAGE_FILTER" ]; then
    echo "ERROR: Package directory not found: $PACKAGE_FILTER"
    exit 1
  fi
  PACKAGES=("$PACKAGE_FILTER")
  IS_MONOREPO=false  # Treat as single-package run
fi

echo "Monorepo: $IS_MONOREPO"
if [ "$IS_MONOREPO" = true ]; then
  echo "Packages: ${PACKAGES[*]}"
fi
```

#### Step 0.4: Read Optional Integration Data

Check for upstream assessment reports and extract relevant dimensions:

```bash
HEALTH_REPORT="$(nightgauge layout path checkout reports/health-report.json)"
SECURITY_REPORT="$(nightgauge layout path checkout reports/security-audit.json)"
HEALTH_AVAILABLE=false
SECURITY_AVAILABLE=false

if [ -f "$HEALTH_REPORT" ]; then
  HEALTH_AVAILABLE=true
  HC_DEP_SCORE=$(jq -r \
    '.dimensions.dependency_health.score // "N/A"' \
    "$HEALTH_REPORT" 2>/dev/null || echo "N/A")
  HC_OUTDATED=$(jq -r \
    '.dimensions.dependency_health.metrics.outdated_count // "N/A"' \
    "$HEALTH_REPORT" 2>/dev/null || echo "N/A")
  echo "Health check available: dep_health_score=$HC_DEP_SCORE outdated=$HC_OUTDATED"
fi

if [ -f "$SECURITY_REPORT" ]; then
  SECURITY_AVAILABLE=true
  SA_VULN_CRIT=$(jq -r \
    '.dimensions.dependency_vulnerabilities.metrics.vulnerability_count.critical // 0' \
    "$SECURITY_REPORT" 2>/dev/null || echo 0)
  SA_VULN_HIGH=$(jq -r \
    '.dimensions.dependency_vulnerabilities.metrics.vulnerability_count.high // 0' \
    "$SECURITY_REPORT" 2>/dev/null || echo 0)
  echo "Security audit available: critical_vulns=$SA_VULN_CRIT high_vulns=$SA_VULN_HIGH"
fi

# Check for renovate/dependabot configs
RENOVATE_CONFIG=false
DEPENDABOT_CONFIG=false
[ -f renovate.json ] || [ -f .renovaterc ] || \
  [ -f .renovaterc.json ] && RENOVATE_CONFIG=true
[ -f .github/dependabot.yml ] && DEPENDABOT_CONFIG=true

echo "Renovate config: $RENOVATE_CONFIG"
echo "Dependabot config: $DEPENDABOT_CONFIG"
```

#### Step 0.5: Validate Prerequisites

```bash
# jq is required
if ! command -v jq &>/dev/null; then
  echo "ERROR: jq is required but not installed."
  echo "Install: brew install jq (macOS) | apt-get install jq (Linux)"
  exit 1
fi

# If --staged, the nightgauge forge CLI must be available and authenticated
if [ "$STAGED" = true ]; then
  if ! command -v nightgauge &>/dev/null; then
    echo "ERROR: --staged requires the nightgauge binary (forge subcommand)."
    echo "Install: see nightgauge docs"
    exit 1
  fi
  if ! nightgauge forge auth status &>/dev/null; then
    echo "ERROR: forge CLI not authenticated. Run: nightgauge forge auth login"
    exit 1
  fi
fi

echo "Prerequisites validated."
```

---

### Phase 1: Dependency Inventory (Deterministic)

Run ecosystem-specific commands to enumerate all dependencies. Categorize by
direct, dev, peer, and optional. Build a dependency tree structure.

#### Step 1.1: Node.js Inventory

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " nodejs " ]]; then
  echo "=== Node.js Dependency Inventory ==="

  # Check for lockfile
  LOCKFILE_PRESENT=false
  LOCKFILE_NAME=""
  [ -f package-lock.json ] && LOCKFILE_PRESENT=true && \
    LOCKFILE_NAME="package-lock.json"
  [ -f yarn.lock ] && LOCKFILE_PRESENT=true && \
    LOCKFILE_NAME="yarn.lock"
  [ -f pnpm-lock.yaml ] && LOCKFILE_PRESENT=true && \
    LOCKFILE_NAME="pnpm-lock.yaml"

  if [ "$LOCKFILE_PRESENT" = false ]; then
    echo "WARNING: No lockfile detected (package-lock.json / yarn.lock / pnpm-lock.yaml)"
    echo "  Analysis will use package.json manifest only. Results may be incomplete."
  fi

  # Count manifest dependencies
  DIRECT_COUNT=$(jq -r '.dependencies // {} | length' \
    package.json 2>/dev/null || echo 0)
  DEV_COUNT=$(jq -r '.devDependencies // {} | length' \
    package.json 2>/dev/null || echo 0)
  PEER_COUNT=$(jq -r '.peerDependencies // {} | length' \
    package.json 2>/dev/null || echo 0)
  OPT_COUNT=$(jq -r '.optionalDependencies // {} | length' \
    package.json 2>/dev/null || echo 0)

  echo "  Direct: $DIRECT_COUNT"
  echo "  Dev: $DEV_COUNT"
  echo "  Peer: $PEER_COUNT"
  echo "  Optional: $OPT_COUNT"

  # Full dependency tree (installed packages)
  if command -v npm &>/dev/null; then
    npm ls --json --all 2>/dev/null > /tmp/dm_npm_ls.json || true
    echo "  npm ls: completed"
  fi
fi
```

#### Step 1.2: Python Inventory

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " python " ]]; then
  echo "=== Python Dependency Inventory ==="

  # Check for lockfile
  PY_LOCKFILE=""
  [ -f poetry.lock ] && PY_LOCKFILE="poetry.lock"
  [ -f uv.lock ] && PY_LOCKFILE="uv.lock"
  [ -z "$PY_LOCKFILE" ] && echo \
    "WARNING: No Python lockfile (poetry.lock / uv.lock) detected."

  # Enumerate installed packages
  if command -v pip &>/dev/null; then
    pip list --format json 2>/dev/null > /tmp/dm_pip_list.json || true
    echo "  pip list: completed"
  fi

  if command -v poetry &>/dev/null && [ -f pyproject.toml ]; then
    poetry show --no-ansi 2>/dev/null > /tmp/dm_poetry_show.txt || true
    echo "  poetry show: completed"
  fi
fi
```

#### Step 1.3: Go Inventory

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " go " ]]; then
  echo "=== Go Dependency Inventory ==="

  [ ! -f go.sum ] && echo \
    "WARNING: go.sum not found. Run 'go mod tidy' first."

  if command -v go &>/dev/null; then
    go list -m all 2>/dev/null > /tmp/dm_go_modules.txt || true
    echo "  go list -m all: completed"
  fi
fi
```

#### Step 1.4: Rust Inventory

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " rust " ]]; then
  echo "=== Rust Dependency Inventory ==="

  [ ! -f Cargo.lock ] && echo \
    "WARNING: Cargo.lock not found. Run 'cargo build' first."

  if command -v cargo &>/dev/null; then
    cargo metadata --format-version 1 2>/dev/null \
      > /tmp/dm_cargo_meta.json || true
    echo "  cargo metadata: completed"
  fi
fi
```

#### Step 1.5: Java/JVM Inventory

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " java " ]]; then
  echo "=== Java/JVM Dependency Inventory ==="

  if [ -f pom.xml ] && command -v mvn &>/dev/null; then
    mvn dependency:tree 2>/dev/null > /tmp/dm_mvn_tree.txt || true
    echo "  mvn dependency:tree: completed"
  elif [ -f build.gradle ] && command -v gradle &>/dev/null; then
    gradle dependencies 2>/dev/null > /tmp/dm_gradle_deps.txt || true
    echo "  gradle dependencies: completed"
  elif [ -f build.gradle.kts ] && command -v gradle &>/dev/null; then
    gradle dependencies 2>/dev/null > /tmp/dm_gradle_deps.txt || true
    echo "  gradle dependencies (Kotlin DSL): completed"
  else
    echo "WARNING: Maven or Gradle not found. Skipping Java inventory."
  fi
fi
```

---

### Phase 2: Outdated & Vulnerability Analysis (Deterministic)

Run ecosystem-specific commands to detect outdated packages and known CVEs.
Cross-reference with the GitHub Advisory Database when available. Categorize
each dependency into one of five buckets.

#### Step 2.1: Node.js Analysis

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " nodejs " ]]; then
  echo "=== Node.js Outdated & Vulnerability Analysis ==="

  # Outdated packages
  if command -v npm &>/dev/null; then
    npm outdated --json 2>/dev/null > /tmp/dm_npm_outdated.json || true
    NPM_OUTDATED_COUNT=$(jq -r 'length' \
      /tmp/dm_npm_outdated.json 2>/dev/null || echo 0)
    echo "  npm outdated: $NPM_OUTDATED_COUNT packages outdated"

    # Vulnerability audit
    npm audit --json 2>/dev/null > /tmp/dm_npm_audit.json || true
    NPM_CRIT=$(jq -r \
      '.metadata.vulnerabilities.critical // 0' \
      /tmp/dm_npm_audit.json 2>/dev/null || echo 0)
    NPM_HIGH=$(jq -r \
      '.metadata.vulnerabilities.high // 0' \
      /tmp/dm_npm_audit.json 2>/dev/null || echo 0)
    NPM_MED=$(jq -r \
      '.metadata.vulnerabilities.moderate // 0' \
      /tmp/dm_npm_audit.json 2>/dev/null || echo 0)
    NPM_LOW=$(jq -r \
      '.metadata.vulnerabilities.low // 0' \
      /tmp/dm_npm_audit.json 2>/dev/null || echo 0)
    echo "  npm audit: critical=$NPM_CRIT high=$NPM_HIGH" \
         "medium=$NPM_MED low=$NPM_LOW"
  else
    echo "WARNING: npm not found — skipping Node.js outdated/audit analysis."
  fi
fi
```

#### Step 2.2: Python Analysis

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " python " ]]; then
  echo "=== Python Outdated & Vulnerability Analysis ==="

  if command -v pip &>/dev/null; then
    pip list --outdated --format json 2>/dev/null \
      > /tmp/dm_pip_outdated.json || true
    PY_OUTDATED=$(jq -r 'length' \
      /tmp/dm_pip_outdated.json 2>/dev/null || echo 0)
    echo "  pip outdated: $PY_OUTDATED packages"
  fi

  if command -v pip-audit &>/dev/null; then
    pip-audit --format json 2>/dev/null \
      > /tmp/dm_pip_audit.json || true
    PY_VULNS=$(jq -r '[.[] | .vulns[]] | length' \
      /tmp/dm_pip_audit.json 2>/dev/null || echo 0)
    echo "  pip-audit: $PY_VULNS vulnerabilities"
  else
    echo "WARNING: pip-audit not found — skipping Python vulnerability analysis."
    echo "  Install: pip install pip-audit"
  fi
fi
```

#### Step 2.3: Go Analysis

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " go " ]]; then
  echo "=== Go Outdated & Vulnerability Analysis ==="

  if command -v go &>/dev/null; then
    go list -m -u all 2>/dev/null > /tmp/dm_go_updates.txt || true
    GO_OUTDATED=$(grep -c '\[' /tmp/dm_go_updates.txt 2>/dev/null || echo 0)
    echo "  go list -u: $GO_OUTDATED modules with updates"
  fi

  if command -v govulncheck &>/dev/null; then
    govulncheck -json ./... 2>/dev/null > /tmp/dm_go_vuln.json || true
    GO_VULNS=$(jq -r '[.[] | select(.finding)] | length' \
      /tmp/dm_go_vuln.json 2>/dev/null || echo 0)
    echo "  govulncheck: $GO_VULNS findings"
  else
    echo "WARNING: govulncheck not found — skipping Go vulnerability analysis."
    echo "  Install: go install golang.org/x/vuln/cmd/govulncheck@latest"
  fi
fi
```

#### Step 2.4: Rust Analysis

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " rust " ]]; then
  echo "=== Rust Outdated & Vulnerability Analysis ==="

  if command -v cargo &>/dev/null; then
    if command -v cargo-outdated &>/dev/null; then
      cargo outdated --format json 2>/dev/null \
        > /tmp/dm_cargo_outdated.json || true
      RUST_OUTDATED=$(jq -r '.dependencies | length' \
        /tmp/dm_cargo_outdated.json 2>/dev/null || echo 0)
      echo "  cargo outdated: $RUST_OUTDATED packages outdated"
    else
      echo "WARNING: cargo-outdated not installed — skipping Rust outdated check."
      echo "  Install: cargo install cargo-outdated"
    fi

    if command -v cargo-audit &>/dev/null; then
      cargo audit --json 2>/dev/null > /tmp/dm_cargo_audit.json || true
      RUST_VULNS=$(jq -r '.vulnerabilities.count // 0' \
        /tmp/dm_cargo_audit.json 2>/dev/null || echo 0)
      echo "  cargo audit: $RUST_VULNS vulnerabilities"
    else
      echo "WARNING: cargo-audit not installed — skipping Rust vulnerability analysis."
      echo "  Install: cargo install cargo-audit"
    fi
  fi
fi
```

#### Step 2.5: Java/JVM Analysis

```bash
if [[ " ${ECOSYSTEMS[*]} " =~ " java " ]]; then
  echo "=== Java/JVM Outdated & Vulnerability Analysis ==="

  if [ -f pom.xml ] && command -v mvn &>/dev/null; then
    mvn versions:display-dependency-updates 2>/dev/null \
      > /tmp/dm_mvn_updates.txt || true
    JAVA_OUTDATED=$(grep -c 'update available' \
      /tmp/dm_mvn_updates.txt 2>/dev/null || echo 0)
    echo "  mvn versions: $JAVA_OUTDATED updates available"
  fi

  if command -v dependency-check &>/dev/null; then
    dependency-check --scan . --format JSON \
      --out /tmp/dm_depcheck.json 2>/dev/null || true
    echo "  OWASP dependency-check: completed"
  else
    echo "WARNING: dependency-check not installed — skipping Java CVE analysis."
  fi
fi
```

#### Step 2.6: Categorize Dependencies

For each dependency identified across all ecosystems, categorize into one of
five buckets based on the analysis results:

| Bucket       | Criteria                                                  |
| ------------ | --------------------------------------------------------- |
| Critical     | Known CVEs with severity high or critical                 |
| Major Update | 2+ major versions behind current installed version        |
| Minor/Patch  | Behind on minor or patch versions only (non-breaking)     |
| Unmaintained | No commits in 2+ years (730 days), no releases in 1+ year |
| Deprecated   | Officially deprecated or archived by maintainer           |

```bash
# Build unmaintained check list: for npm packages, use npm show to check
# last publish date. For packages where last_publish > 730 days ago,
# flag as unmaintained.
UNMAINTAINED_THRESHOLD_DAYS=730

# Read from config if available
if [ -f .nightgauge/config.yaml ]; then
  CFG_THRESHOLD=$(grep -A2 'dep_modernize:' \
    .nightgauge/config.yaml 2>/dev/null | \
    grep 'unmaintained_threshold_days:' | \
    awk '{print $2}')
  [ -n "$CFG_THRESHOLD" ] && UNMAINTAINED_THRESHOLD_DAYS=$CFG_THRESHOLD
fi

echo "Unmaintained threshold: ${UNMAINTAINED_THRESHOLD_DAYS} days"
```

---

### Phase 3: Breaking Change Analysis (AI-Assisted)

For each dependency in the Critical or Major Update buckets, analyze changelog
or release notes to summarize breaking changes, migration requirements, and risk
level. Minor/Patch updates are automatically classified as Low risk without AI
analysis.

If `--skip-breaking-analysis` is set, skip this phase entirely and mark all
major updates as risk: "unanalyzed".

```bash
if [ "$SKIP_BREAKING_ANALYSIS" = true ]; then
  echo "Skipping breaking change analysis (--skip-breaking-analysis set)."
else
  echo "=== Phase 3: Breaking Change Analysis ==="
  echo "Spawning parallel subagents for changelog analysis..."

  # For each major-update dependency, use Task tool with model: "haiku"
  # to analyze the changelog/release notes between the installed version
  # and the latest version.
  #
  # Each subagent task receives:
  #   - Package name
  #   - Installed version
  #   - Latest available version
  #   - Changelog URL or npm/PyPI release notes URL
  #
  # Each subagent returns:
  #   - breaking_changes: string[] — plain-language list of breaking changes
  #   - migration_steps: string[] — required steps to migrate
  #   - risk: "Low" | "Medium" | "High" | "Critical"
  #   - confidence: "low" | "medium" | "high"
  #
  # Use Task tool in parallel for all major-update deps simultaneously.
  # Collect results and merge into the dependency objects.

  echo "Breaking change analysis: completed"
fi
```

---

### Phase 4: Staged Update Groups (Deterministic)

Build a dependency graph (who depends on whom), sort topologically, and assign
each dependency to a numbered update group. Groups are processed sequentially to
minimize cascading failures.

#### Step 4.1: Build Dependency Graph

```bash
echo "=== Phase 4: Staged Update Groups ==="

# For Node.js: parse npm ls --json output to build the dep tree
# For Rust: parse cargo metadata --format-version 1 resolve graph
# For Go: parse go list -m all with replace directives
# For Python: use pip show <package> to get Requires/Required-by
# For Java: parse mvn dependency:tree output

echo "Building dependency graph..."
```

#### Step 4.2: Topological Sort

```bash
# Perform Kahn's algorithm (BFS topological sort) on the dependency graph:
#
# 1. Compute in-degree for each dependency node
# 2. Add all zero-in-degree nodes to the start queue (leaf dependencies)
# 3. Process queue: remove node, decrement in-degrees of dependents
# 4. Add newly zero-in-degree nodes to queue
# 5. Result is a topologically sorted list (leaves first)
#
# This ensures leaf dependencies are updated first, minimizing
# downstream impact from breaking changes.

echo "Topological sort: completed"
```

#### Step 4.3: Assign Update Groups

Assign dependencies to update groups following these rules:

| Group   | Contents                                              | Strategy          |
| ------- | ----------------------------------------------------- | ----------------- |
| Group 1 | Patch and minor updates with no breaking changes      | Safe, apply first |
| Group 2 | Single major updates for leaf dependencies            | Low risk          |
| Group 3 | Major updates requiring coordinated changes           | Medium risk       |
| Group 4 | Deprecated package replacements (swap to alternative) | High risk         |

```bash
# Respect peer dependency constraints when grouping:
# If package A requires package B@^2.x, and B is being updated to 3.x,
# A must also be updated in the same group as B.
#
# For monorepos: shared dependencies (hoisted to root) must be updated
# consistently across all workspace packages in the same group.

echo "Update groups assigned."
```

---

### Phase 5: Replacement Suggestions (AI-Assisted)

For deprecated and unmaintained packages, suggest modern alternatives. Skip this
phase if all packages are in the Critical, Major Update, or Minor/Patch buckets
only.

```bash
echo "=== Phase 5: Replacement Suggestions ==="

# For each deprecated or unmaintained package, use the Task tool with
# model: "haiku" to research modern alternatives.
#
# Each subagent receives:
#   - Package name and current version
#   - Ecosystem
#   - Deprecation/unmaintained reason (if known)
#
# Each subagent returns:
#   - alternatives: [{ name, description, weekly_downloads, compatibility }]
#   - migration_effort: "low" | "medium" | "high"
#   - notes: string — compatibility and migration notes
#
# Spawn all subagents in parallel using the Task tool.
# Collect and merge replacement suggestions into the dependency objects.

echo "Replacement suggestions: completed"
```

---

### Phase 6: Apply Updates (Optional, Auto-Fix Mode)

Only runs if `--auto-fix` is set. If `--dry-run` is true (default), this phase
is skipped entirely and the report only describes what would change.

#### Step 6.1: Check Baseline Test Suite

```bash
if [ "$AUTO_FIX" = true ]; then
  echo "=== Phase 6: Apply Updates ==="
  echo "Running baseline test suite before any changes..."

  BASELINE_PASS=false

  # Detect and run test command per ecosystem
  if [[ " ${ECOSYSTEMS[*]} " =~ " nodejs " ]]; then
    if jq -e '.scripts.test' package.json &>/dev/null; then
      npm test 2>&1 > /tmp/dm_baseline_test.log && BASELINE_PASS=true || true
    else
      echo "WARNING: No test script in package.json — cannot verify test baseline."
      BASELINE_PASS=true  # No tests = no baseline to fail
    fi
  fi

  if [ "$BASELINE_PASS" = false ]; then
    echo "ERROR: Baseline test suite failed before any changes."
    echo "  Fix existing test failures before using --auto-fix."
    echo "  Test output: /tmp/dm_baseline_test.log"
    exit 1
  fi

  echo "Baseline tests: PASSED"
fi
```

#### Step 6.2: Apply Groups Sequentially

```bash
if [ "$AUTO_FIX" = true ]; then
  for GROUP_NUM in 1 2 3 4; do
    echo "Applying Group $GROUP_NUM..."

    # If --staged, create a new branch for this group
    if [ "$STAGED" = true ]; then
      BRANCH_NAME="chore/dep-modernize-group-${GROUP_NUM}"
      export GITHUB_TOKEN=$(nightgauge forge auth token 2>/dev/null || echo "")
      BRANCH_RESULT=$("$BINARY" git branch-create "$BRANCH_NAME" --json 2>/dev/null || \
        echo '{"success":false,"error":"binary not found"}')
      if [ "$(printf '%s\n' "$BRANCH_RESULT" | jq -r '.success')" != "true" ]; then
        echo "ERROR: Branch creation failed: $(printf '%s\n' "$BRANCH_RESULT" | jq -r '.error')" >&2
        exit 1
      fi
      echo "Branch: $BRANCH_NAME"
    fi

    # Apply group updates using ecosystem-specific commands:
    # Node.js Group 1 (patch/minor):
    #   npm update  (respects semver ranges in package.json)
    # Node.js Group 2+ (major):
    #   npm install pkg@latest for each package in the group
    # Python:
    #   pip install --upgrade <pkg> for each package
    # Go:
    #   go get <module>@latest
    # Rust:
    #   cargo update --precise <version> for each crate

    # Install dependencies after update
    echo "Installing dependencies..."
    # npm install / pip install / cargo build / go mod tidy

    # Run test suite
    echo "Running test suite after Group $GROUP_NUM..."
    GROUP_PASS=false
    # npm test / pytest / go test ./... / cargo test

    if [ "$GROUP_PASS" = true ]; then
      echo "Group $GROUP_NUM: PASSED — proceeding to next group"

      if [ "$STAGED" = true ]; then
        # Commit and create PR via deterministic binary
        git add -A
        git commit -m "chore(deps): update group ${GROUP_NUM} dependencies"
        PR_RESULT=$("$BINARY" pr create \
          --title "chore(deps): dependency modernization group ${GROUP_NUM}" \
          --body "Automated dependency update group ${GROUP_NUM} from /nightgauge:dep-modernize" \
          --head "$BRANCH_NAME" \
          --base "main" \
          --json 2>&1) || {
          echo "ERROR: PR creation failed: $PR_RESULT" >&2
          exit 1
        }
        PR_URL=$(printf '%s\n' "$PR_RESULT" | jq -r '.url // empty')
        PR_NUMBER=$(printf '%s\n' "$PR_RESULT" | jq -r '.number // empty')
        echo "PR created: ${PR_URL:-"#${PR_NUMBER}"}"
      fi
    else
      echo "Group $GROUP_NUM: FAILED — rolling back"
      # Restore original lockfile and manifest from git
      git checkout -- package.json package-lock.json \
        Cargo.toml Cargo.lock go.mod go.sum \
        pyproject.toml poetry.lock 2>/dev/null || true
      echo "  Group $GROUP_NUM marked as 'needs manual intervention' in report."

      if [ "$STAGED" = true ]; then
        git checkout main 2>/dev/null || git checkout master 2>/dev/null
        git branch -D "$BRANCH_NAME" 2>/dev/null || true
      fi
    fi
  done
fi
```

#### Step 6.3: Ecosystem Test Commands Reference

| Ecosystem   | Test Command    | Install Command          |
| ----------- | --------------- | ------------------------ |
| Node.js     | `npm test`      | `npm install`            |
| Python      | `pytest`        | `pip install -r ...`     |
| Go          | `go test ./...` | `go mod download`        |
| Rust        | `cargo test`    | `cargo build`            |
| Java/Maven  | `mvn test`      | `mvn dependency:resolve` |
| Java/Gradle | `gradle test`   | `gradle dependencies`    |

---

### Phase 7: Output (Deterministic + AI Summary)

#### Step 7.1: Ensure Output Directory

The markdown report (Step 7.3) is written to `.nightgauge/`:

```bash
mkdir -p .nightgauge
```

#### Step 7.2: Write JSON Report

Write the structured report to this checkout's
`reports/dep-modernize-report.json`
(`nightgauge layout path checkout reports/dep-modernize-report.json`, inside the
git directory, ADR-024 § 7), or to the custom `--output` path. See the Output
Format section for the full JSON schema. Build the JSON in a temporary file and
validate it before writing; the default report goes through
`nightgauge layout write`, never a write under `.git` by path:

```bash
REPORT_TMP="$(mktemp)"
# ... write the JSON report to "$REPORT_TMP" ...
python3 -m json.tool "$REPORT_TMP" > /dev/null || { echo "Invalid report JSON" >&2; exit 1; }
if [ -n "$OUTPUT_FILE" ]; then
  cp "$REPORT_TMP" "$OUTPUT_FILE"
  OUTPUT_PATH="$OUTPUT_FILE"
else
  OUTPUT_PATH="$(nightgauge layout write checkout reports/dep-modernize-report.json --from "$REPORT_TMP")"
fi
rm -f "$REPORT_TMP"
echo "Report written: $OUTPUT_PATH"
```

#### Step 7.3: Write Markdown Report

If `--format` includes `summary`, write
`.nightgauge/DEP_MODERNIZE_REPORT.md`:

```text
DEPENDENCY MODERNIZATION REPORT
================================================================

Project: project-name
Ecosystems: nodejs, python
Analysis Date: 2026-02-21
Mode: dry-run (no changes applied)
Monorepo: No

SUMMARY
================================================================
  Total dependencies:    150
  Outdated:               42 (28%)
  Vulnerable (CVE):        5
  Deprecated:              2
  Unmaintained:            3
  Auto-fixable:           28
  Needs manual:           14

CATEGORY BREAKDOWN
----------------------------------------------------------------
  [CRITICAL]   CVEs requiring immediate attention        5
  [MAJOR]      2+ major versions behind                 12
  [MINOR/PATCH] Non-breaking updates available          20
  [UNMAINTAINED] No activity in 2+ years                3
  [DEPRECATED]  Officially deprecated                   2

UPDATE GROUPS
----------------------------------------------------------------
  Group 1: 20 packages (patch/minor, safe)        [AUTO-FIXABLE]
  Group 2: 8 packages (major leaf deps)           [REVIEW REQUIRED]
  Group 3: 12 packages (coordinated majors)       [MANUAL]
  Group 4: 2 packages (replacements)             [MANUAL]

TOP CRITICAL ITEMS
----------------------------------------------------------------
  [CRITICAL CVE] lodash < 4.17.21 — Prototype Pollution (CVE-2020-8203)
    -> Update to 4.17.21
    -> Run: npm install lodash@latest

  [MAJOR] react 17.x → 19.x (2 major versions behind)
    -> Breaking: New JSX transform required
    -> Breaking: StrictMode behavior changes
    -> Risk: High — Migration guide: https://react.dev/blog/2024/04/25/react-19-upgrade-guide

  [DEPRECATED] request (no longer maintained, 0 commits since 2020)
    -> Replacement: axios (4.2M weekly downloads, API compatible)

REPLACEMENTS SUGGESTED
----------------------------------------------------------------
  request → axios or node-fetch
  moment  → date-fns or dayjs

----------------------------------------------------------------
To apply safe updates: /nightgauge:dep-modernize --auto-fix
Report saved: .git/nightgauge-worktree/reports/dep-modernize-report.json
```

#### Step 7.4: Console Summary

Output a concise summary to the console regardless of `--format`:

```text
Dep Modernize: 150 deps | 5 critical CVEs | 42 outdated | 4 groups
Run with --auto-fix to apply 28 safe updates automatically.
Full report: .git/nightgauge-worktree/reports/dep-modernize-report.json
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

`dep-modernize-report.json`:

```json
{
  "schema_version": "1.0",
  "generated_at": "ISO-8601",
  "codebase": {
    "name": "string",
    "root_path": "string",
    "ecosystems": ["nodejs", "python"],
    "is_monorepo": false,
    "packages": []
  },
  "summary": {
    "total_dependencies": 150,
    "outdated_count": 42,
    "vulnerable_count": 5,
    "deprecated_count": 2,
    "unmaintained_count": 3,
    "categories": {
      "critical": 5,
      "major_updates": 12,
      "minor_patch": 20,
      "unmaintained": 3,
      "deprecated": 2
    },
    "update_groups": 4,
    "auto_fixable": 28,
    "needs_manual": 14
  },
  "dependencies": [
    {
      "name": "lodash",
      "ecosystem": "nodejs",
      "installed_version": "4.17.11",
      "latest_version": "4.17.21",
      "wanted_version": "4.17.21",
      "type": "direct",
      "category": "critical",
      "severity": "critical",
      "cves": [
        {
          "id": "CVE-2020-8203",
          "severity": "high",
          "description": "Prototype Pollution in lodash",
          "fixed_in": "4.17.21"
        }
      ],
      "breaking_changes": [],
      "migration_steps": ["Run: npm install lodash@latest"],
      "risk": "Low",
      "update_group": 1,
      "auto_fixable": true,
      "replacement": null
    },
    {
      "name": "request",
      "ecosystem": "nodejs",
      "installed_version": "2.88.2",
      "latest_version": "2.88.2",
      "wanted_version": "2.88.2",
      "type": "direct",
      "category": "deprecated",
      "severity": "medium",
      "cves": [],
      "breaking_changes": [],
      "migration_steps": [],
      "risk": "High",
      "update_group": 4,
      "auto_fixable": false,
      "replacement": {
        "name": "axios",
        "version": "latest",
        "weekly_downloads": 4200000,
        "compatibility": "API-compatible with minor changes",
        "migration_effort": "medium",
        "notes": "Replace require('request') with require('axios'). Callback API differs from promise-based axios."
      }
    }
  ],
  "update_groups": [
    {
      "group": 1,
      "name": "Patch and Minor Updates",
      "description": "Safe non-breaking updates",
      "risk": "low",
      "auto_fixable": true,
      "dependencies": ["lodash", "moment"],
      "estimated_effort": "XS",
      "branch": "chore/dep-modernize-group-1",
      "status": "pending"
    },
    {
      "group": 2,
      "name": "Major Leaf Dependencies",
      "description": "Major updates for packages with no dependents",
      "risk": "medium",
      "auto_fixable": false,
      "dependencies": ["vitest", "eslint"],
      "estimated_effort": "S",
      "branch": "chore/dep-modernize-group-2",
      "status": "pending"
    },
    {
      "group": 3,
      "name": "Coordinated Major Updates",
      "description": "Major updates requiring coordinated changes across dependents",
      "risk": "high",
      "auto_fixable": false,
      "dependencies": ["react", "react-dom"],
      "estimated_effort": "L",
      "branch": "chore/dep-modernize-group-3",
      "status": "pending"
    },
    {
      "group": 4,
      "name": "Deprecated Replacements",
      "description": "Packages to replace with modern alternatives",
      "risk": "high",
      "auto_fixable": false,
      "dependencies": ["request"],
      "estimated_effort": "M",
      "branch": "chore/dep-modernize-group-4",
      "status": "pending"
    }
  ],
  "replacements": [
    {
      "package": "request",
      "ecosystem": "nodejs",
      "reason": "deprecated",
      "alternatives": [
        {
          "name": "axios",
          "weekly_downloads": 4200000,
          "compatibility": "Similar API, promise-based",
          "migration_effort": "medium"
        },
        {
          "name": "node-fetch",
          "weekly_downloads": 8000000,
          "compatibility": "Fetch API standard",
          "migration_effort": "medium"
        }
      ]
    }
  ],
  "integration": {
    "health_report_available": true,
    "security_audit_available": false,
    "renovate_config_found": false,
    "dependabot_config_found": false
  }
}
```

### Report Files

| File                                         | Format   | When Written            |
| -------------------------------------------- | -------- | ----------------------- |
| `checkout reports/dep-modernize-report.json` | JSON     | `--format json/both`    |
| `.nightgauge/DEP_MODERNIZE_REPORT.md`        | Markdown | `--format summary/both` |
| Console summary                              | Text     | Always                  |

`checkout` paths are inside this checkout's git directory (ADR-024 § 7):
`nightgauge layout path checkout reports/dep-modernize-report.json` prints the
resolved path.

---

## Error Handling

| Condition                          | Action                                                           |
| ---------------------------------- | ---------------------------------------------------------------- |
| No ecosystem detected              | Error with list of supported ecosystems and manifest files       |
| Package manager not installed      | Skip that ecosystem, warn, continue with others                  |
| Audit tool unavailable             | Skip vulnerability check for ecosystem, note in report           |
| Network unavailable                | Use cached/local data only, warn about incomplete results        |
| Lock file missing                  | Warn, proceed with manifest-only analysis                        |
| Test suite fails before updates    | Abort --auto-fix, report baseline failures                       |
| `--auto-fix` group fails tests     | Roll back group, mark as "needs manual intervention", continue   |
| `--staged` requires forge CLI      | Error with install instructions if forge not found/authenticated |
| `--ecosystems` filter matches none | Error: no matching ecosystems found in target path               |
| `--severity` invalid value         | Error with valid levels: low, medium, high, critical             |
| `--package` directory not found    | Error with directory not found message                           |
| `jq` not installed                 | Error with install instructions                                  |
| Cycle in dependency graph          | Warn, break cycle by treating as independent group               |

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
                                                   ├──► /nightgauge:dep-modernize
                                                   |         |
                                                   |    Reads: health-report.json (optional)
                                                   |    Reads: security-audit.json (optional)
                                                   |    Writes: dep-modernize-report.json
                                                   |    Writes: DEP_MODERNIZE_REPORT.md (optional)
                                                   |         |
                                                   ├──► /nightgauge:modernize-plan
                                                   |
/nightgauge:test-scaffold ─────────────────────────┘
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs.

---

## Configuration

This skill reads configuration from `.nightgauge/config.yaml` if present:

```yaml
# .nightgauge/config.yaml
dep_modernize:
  default_format: both
  severity_filter: low
  auto_fix: false
  skip_ecosystems: []
  output_path: auto
  max_major_versions_behind: 2
  unmaintained_threshold_days: 730
```

| Config Key                                  | Default | Description                                    |
| ------------------------------------------- | ------- | ---------------------------------------------- |
| `dep_modernize.default_format`              | `both`  | Default `--format` value                       |
| `dep_modernize.severity_filter`             | `low`   | Default `--severity` value                     |
| `dep_modernize.auto_fix`                    | `false` | Default for `--auto-fix`                       |
| `dep_modernize.skip_ecosystems`             | `[]`    | Ecosystems to always skip                      |
| `dep_modernize.output_path`                 | `auto`  | Default JSON output path                       |
| `dep_modernize.max_major_versions_behind`   | `2`     | Major version lag threshold for categorization |
| `dep_modernize.unmaintained_threshold_days` | `730`   | Days without activity to flag as unmaintained  |

## Integration Points

**Consumes** (optional):

- `checkout reports/health-report.json` — reads
  `dimensions.dependency_health` for outdated count context and score baseline
- `checkout reports/security-audit.json` — reads
  `dimensions.dependency_vulnerabilities` for CVE cross-reference

`checkout reports/<name>` is this checkout's `reports/` directory inside the git
directory: `nightgauge layout path checkout reports/<name>`.

**Produces**:

- `checkout reports/dep-modernize-report.json` — full structured report
- `.nightgauge/DEP_MODERNIZE_REPORT.md` — human-readable summary (when
  format includes summary)

**Consumed by**:

- `modernize-plan` — Phase 2 task classification and Foundation tasks use the
  dep-modernize report to create dependency-update work items

---

**Author:** nightgauge **License:** Apache-2.0
