---
name: backlog-preflight
description: Validate backlog issues are pipeline-ready before processing. Checks required
  labels, acceptance criteria quality, dependency cycles, and greenfield
  readiness. Use before starting pipeline runs on a new or unfamiliar repo.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.2.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: pipeline-researcher
model: haiku
disable-model-invocation: true
---

# Backlog Preflight

> Validate backlog issues are pipeline-ready before processing

## Description

This skill validates that issues in the backlog meet the minimum requirements
for pipeline processing. It extends the analysis from `/backlog-groom` with
greenfield-specific checks to catch issues that would fail or produce poor
results in the pipeline.

**Use Cases:**

- Before running the pipeline on a new repo for the first time
- After bulk-importing issues from another tracker
- Periodic quality gate for backlog health
- Pre-sprint validation to ensure issues are actionable

## Invocation

| Tool           | Command                                      |
| -------------- | -------------------------------------------- |
| Claude Code    | `/nightgauge:backlog-preflight` (via plugin) |
| OpenAI Codex   | `$nightgauge-backlog-preflight`              |
| GitHub Copilot | Invoke via Agent Skills                      |
| Cursor         | Invoke via Agent Skills                      |

## Arguments

| Argument            | Description                           | Default   |
| ------------------- | ------------------------------------- | --------- |
| `--fix`             | Auto-fix issues where possible        | `false`   |
| `--status <status>` | Filter issues by project board status | `"Ready"` |
| `--focus <type>`    | Focus on specific check type          | `"all"`   |

### Focus Areas

| Focus Type     | Checks                             |
| -------------- | ---------------------------------- |
| `all`          | All validation checks (default)    |
| `labels`       | Missing required labels only       |
| `criteria`     | Acceptance criteria quality only   |
| `dependencies` | Dependency cycle detection only    |
| `greenfield`   | Greenfield readiness checks only   |
| `drift`        | Documentation drift detection only |

### Examples

```bash
# Full preflight check on Ready issues
/nightgauge:backlog-preflight

# Check only Ready issues for missing labels
/nightgauge:backlog-preflight --focus labels

# Auto-fix missing labels where deterministic
/nightgauge:backlog-preflight --fix

# Check In progress issues
/nightgauge:backlog-preflight --status "In progress"

# Greenfield readiness only
/nightgauge:backlog-preflight --focus greenfield

# Documentation drift detection only
/nightgauge:backlog-preflight --focus drift
```

## Philosophy

### Deterministic vs Probabilistic Split

| Operation              | Type          | Rationale                                      |
| ---------------------- | ------------- | ---------------------------------------------- |
| Label existence check  | Deterministic | Compare against known required labels          |
| Body length check      | Deterministic | Character count comparison                     |
| AC checkbox detection  | Deterministic | Regex for `- [ ]` pattern                      |
| Dependency cycle check | Deterministic | Graph traversal on blocking relationships      |
| AC quality assessment  | Probabilistic | AI evaluates whether ACs are specific enough   |
| Doc drift detection    | Probabilistic | Task subagent compares issue body against docs |

### Context Isolation

This is a **standalone utility skill**, not part of the main pipeline. It:

- Does NOT read pipeline context files (`nightgauge layout path pipeline`)
- Does NOT write pipeline handoff files
- Generates standalone reports in this checkout's `reports/`
  (`nightgauge layout path checkout reports`)
- Can be run at any time without affecting pipeline state

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Workflow

### Phase 0: Setup

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

```bash
# Verify prerequisites
if ! command -v gh &>/dev/null; then
  echo "ERROR: gh CLI not installed — https://cli.github.com"
  exit 1
fi
if ! command -v jq &>/dev/null; then
  echo "ERROR: jq not installed — brew install jq"
  exit 1
fi

REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner)
OWNER="${REPO%/*}"
echo "Repository: $REPO"

# Parse arguments
FIX_MODE=false
STATUS_FILTER="Ready"
FOCUS="all"
for i in "$@"; do
  case $i in
    --fix)          FIX_MODE=true ;;
    --status)       shift; STATUS_FILTER="$1" ;;
    --status=*)     STATUS_FILTER="${i#*=}" ;;
    --focus)        shift; FOCUS="$1" ;;
    --focus=*)      FOCUS="${i#*=}" ;;
  esac
done
```

### Phase 1: Data Collection

Fetch issues from the project board filtered by status:

```bash
# Get project number from config (prefer the deterministic Go binary; fall
# back to the legacy grep|awk pattern so older binaries keep working).
PROJECT_NUMBER=$(nightgauge config show --key project.number --raw 2>/dev/null \
  || grep "number:" .nightgauge/config.yaml 2>/dev/null | head -1 | awk '{print $2}')
if [ -z "$PROJECT_NUMBER" ]; then
  echo "ERROR: No project number in .nightgauge/config.yaml"
  echo "Run /nightgauge:repo-init first"
  exit 1
fi

# Fetch issues with status filter via project board API
ISSUES=$(gh api graphql -f query='
{
  organization(login: "'"$OWNER"'") {
    projectV2(number: '"$PROJECT_NUMBER"') {
      items(first: 100, query: "status:'"$STATUS_FILTER"' is:open") {
        nodes {
          content {
            ... on Issue {
              number title body
              labels(first: 20) { nodes { name } }
            }
          }
          fieldValues(first: 15) {
            nodes {
              ... on ProjectV2ItemFieldSingleSelectValue {
                name
                field { ... on ProjectV2SingleSelectField { name } }
              }
            }
          }
        }
      }
    }
  }
}')

ISSUE_COUNT=$(printf '%s\n' "$ISSUES" | jq '[.data.organization.projectV2.items.nodes[] | select(.content.number != null)] | length')
echo "Found $ISSUE_COUNT issues with status: $STATUS_FILTER"
```

### Phase 2: Validation Checks (Checks 2.1–2.5)

Checks 2.1–2.5 run in the Go binary (audit row B26, #3084). The binary reads
each board item's whole `blockedBy` list, page by page, and fails rather than
check cycles on part of it (#1701). There is no shell fallback: a fixed-size
`blockedBy` read stops at its first page with no sign that more exist, so a
cycle through a later blocker would go unreported.

```bash
MISSING_TYPE=()
MISSING_SIZE=()
MISSING_PRIORITY=()
WEAK_AC=()
CYCLES=()
GREENFIELD_WARNINGS=()

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
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin"
  echo "Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest"
  exit 1
fi

# Exit 0 = no findings, 1 = findings, 2 = could not run (the reason is on
# stderr). Only stdout is the report, so stderr is never merged into it.
PREFLIGHT_JSON=$("$BINARY" backlog preflight \
  --owner "$OWNER" --status "$STATUS_FILTER" --focus "$FOCUS" --json)
PREFLIGHT_EXIT=$?

if [ $PREFLIGHT_EXIT -gt 1 ] || ! printf '%s\n' "$PREFLIGHT_JSON" \
     | jq -e 'type == "object" and has("findings") and has("summary")' >/dev/null 2>&1; then
  echo "ERROR: backlog preflight could not run (exit $PREFLIGHT_EXIT) — see the error above"
  exit 1
fi

# Extract per-category finding arrays for downstream reporting. The binary
# writes findings as null when there are none.
finding_lines() {
  printf '%s\n' "$PREFLIGHT_JSON" | jq -r --arg t "$1" \
    "(.findings // [])[] | select(.finding_type == \$t) | $2"
}
while IFS= read -r line; do [ -n "$line" ] && MISSING_TYPE+=("$line"); done < <(finding_lines missing_type_label '"#\(.issue_number): \(.issue_title)"')
while IFS= read -r line; do [ -n "$line" ] && MISSING_SIZE+=("$line"); done < <(finding_lines missing_size_field '"#\(.issue_number): \(.issue_title)"')
while IFS= read -r line; do [ -n "$line" ] && MISSING_PRIORITY+=("$line"); done < <(finding_lines missing_priority_field '"#\(.issue_number): \(.issue_title)"')
while IFS= read -r line; do [ -n "$line" ] && WEAK_AC+=("$line"); done < <(finding_lines weak_acceptance_criteria '"#\(.issue_number): \(.issue_title) (\(.detail))"')
while IFS= read -r line; do [ -n "$line" ] && CYCLES+=("$line"); done < <(finding_lines dependency_cycle '.detail')
while IFS= read -r line; do [ -n "$line" ] && GREENFIELD_WARNINGS+=("$line"); done < <(finding_lines greenfield_warning '.detail')
```

#### Check 2.6: Documentation Drift Detection

Compare issue body technology references against repo documentation to detect
contradictions (e.g., issue says "add PostgreSQL support" but docs say "uses
SQLite").

This check uses a Task subagent for semantic comparison. It is probabilistic —
only definitive contradictions supported by direct text evidence are reported.

```bash
DOC_DRIFT=()

if [ "$FOCUS" = "all" ] || [ "$FOCUS" = "drift" ]; then
  # Collect issues that have non-trivial bodies for drift analysis
  DRIFT_CANDIDATES="[]"
  for i in $(seq 0 $((ISSUE_COUNT - 1))); do
    NUMBER=$(printf '%s\n' "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.number")
    TITLE=$(printf '%s\n' "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.title")
    BODY=$(printf '%s\n' "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.body // \"\"")
    BODY_LEN=${#BODY}

    # Only send issues with substantial bodies (skip trivial stubs)
    if [ "$BODY_LEN" -gt 200 ]; then
      DRIFT_CANDIDATES=$(printf '%s\n' "$DRIFT_CANDIDATES" | jq \
        --arg num "$NUMBER" --arg title "$TITLE" --arg body "$BODY" \
        '. + [{number: $num, title: $title, body: $body}]')
    fi
  done

  CANDIDATE_COUNT=$(printf '%s\n' "$DRIFT_CANDIDATES" | jq 'length')
  if [ "$CANDIDATE_COUNT" -gt 0 ]; then
    echo "Checking $CANDIDATE_COUNT issues for documentation drift..."

    # Read docs for comparison (first 100 lines each to stay within context)
    DOCS_CONTENT=""
    for doc_file in docs/ARCHITECTURE.md docs/CODE_STANDARDS.md CLAUDE.md AGENTS.md; do
      if [ -f "$doc_file" ]; then
        DOCS_CONTENT="${DOCS_CONTENT}\n\n### $doc_file\n$(head -100 "$doc_file")"
      fi
    done

    # Spawn Task subagent for semantic drift detection
    # The AI agent executing this skill invokes the Task tool here with:
    #   model: sonnet
    #   prompt: |
    #     Analyze these issues for technology contradictions against the repo docs.
    #     Only report DEFINITIVE contradictions supported by direct text evidence.
    #     Do NOT report speculative or ambiguous mismatches.
    #     Issues: [DRIFT_CANDIDATES JSON]
    #     Docs: [DOCS_CONTENT]
    #     Return one line per contradiction:
    #       DRIFT: #<number> <title> — issue says "<X>" but docs say "<Y>"
    #     If no contradictions found, return: NO_DRIFT
    echo "DRIFT_CHECK_REQUIRED: $CANDIDATE_COUNT issues need semantic drift analysis"
    # The Task result lines are parsed below:
    # DOC_DRIFT+=("each DRIFT: line from subagent output")
  fi
fi
```

### Phase 3: Report Generation

Generate the preflight report:

```bash
# Generated reports live in this checkout's reports/ inside the git directory
# (ADR-024 § 7); they are written only through `nightgauge layout write`.
DATE=$(date +%Y-%m-%d)
REPORT_NAME="reports/preflight-${DATE}.md"
REPORT_JSON_NAME="reports/preflight-${DATE}.json"
REPORT="$(nightgauge layout path checkout "$REPORT_NAME")"
REPORT_JSON="$(nightgauge layout path checkout "$REPORT_JSON_NAME")"

# Count totals
MISSING_TYPE_COUNT=${#MISSING_TYPE[@]}
MISSING_SIZE_COUNT=${#MISSING_SIZE[@]}
MISSING_PRIORITY_COUNT=${#MISSING_PRIORITY[@]}
WEAK_AC_COUNT=${#WEAK_AC[@]}
CYCLE_COUNT=${#CYCLES[@]}
GREENFIELD_COUNT=${#GREENFIELD_WARNINGS[@]}
DOC_DRIFT_COUNT=${#DOC_DRIFT[@]}
TOTAL_ISSUES_NEEDING_ATTENTION=$((MISSING_TYPE_COUNT + MISSING_SIZE_COUNT + MISSING_PRIORITY_COUNT + WEAK_AC_COUNT + CYCLE_COUNT))
READY_COUNT=$((ISSUE_COUNT - TOTAL_ISSUES_NEEDING_ATTENTION))
[ "$READY_COUNT" -lt 0 ] && READY_COUNT=0

# Write markdown report
nightgauge layout write checkout "$REPORT_NAME" > /dev/null << REPORTEOF
## Backlog Preflight Report — ${DATE}

### Summary

- ${ISSUE_COUNT} issues scanned (status: ${STATUS_FILTER})
- ${READY_COUNT} issues ready for pipeline
- ${TOTAL_ISSUES_NEEDING_ATTENTION} issues need attention

### Repo Readiness

$(if [ ${#GREENFIELD_WARNINGS[@]} -eq 0 ]; then
  echo "All repo prerequisites met."
else
  for w in "${GREENFIELD_WARNINGS[@]}"; do echo "- WARNING: $w"; done
fi)

### Issues Needing Attention

| # | Title | Problem | Fix |
|---|-------|---------|-----|
$(for issue in "${MISSING_TYPE[@]}"; do
  NUM=$(printf '%s\n' "$issue" | grep -oE '#[0-9]+' | tr -d '#')
  TTITLE=$(echo "$issue" | sed 's/#[0-9]*: //')
  echo "| #$NUM | $TTITLE | Missing type label | Add type:feature/bug/docs/refactor/chore |"
done)
$(for issue in "${MISSING_SIZE[@]}"; do
  NUM=$(printf '%s\n' "$issue" | grep -oE '#[0-9]+' | tr -d '#')
  TTITLE=$(echo "$issue" | sed 's/#[0-9]*: //')
  echo "| #$NUM | $TTITLE | Missing Size field | Set Size on project board |"
done)
$(for issue in "${MISSING_PRIORITY[@]}"; do
  NUM=$(printf '%s\n' "$issue" | grep -oE '#[0-9]+' | tr -d '#')
  TTITLE=$(echo "$issue" | sed 's/#[0-9]*: //')
  echo "| #$NUM | $TTITLE | Missing Priority field | Set Priority on project board |"
done)
$(for issue in "${WEAK_AC[@]}"; do
  NUM=$(printf '%s\n' "$issue" | grep -oE '#[0-9]+' | tr -d '#')
  TTITLE=$(echo "$issue" | sed 's/#[0-9]*: //')
  echo "| #$NUM | $TTITLE | Weak acceptance criteria | Add at least 2 checkbox ACs (- [ ] ...) |"
done)

### Dependency Cycles

$(if [ ${#CYCLES[@]} -eq 0 ]; then
  echo "No dependency cycles detected."
else
  for c in "${CYCLES[@]}"; do echo "- CYCLE: $c"; done
fi)

### Documentation Drift

$(if [ ${#DOC_DRIFT[@]} -eq 0 ]; then
  echo "No documentation drift detected."
else
  for d in "${DOC_DRIFT[@]}"; do echo "- DRIFT: $d"; done
fi)

### Foundation Tasks

$(if [ ${#FOUNDATION_ISSUES[@]} -eq 0 ]; then
  echo "No foundation tasks detected."
else
  for f in "${FOUNDATION_ISSUES[@]}"; do echo "- $f"; done
fi)
REPORTEOF

echo "Report written: $REPORT"

# Write structured JSON report
jq -n \
  --arg date "$DATE" \
  --arg status "$STATUS_FILTER" \
  --argjson issue_count "$ISSUE_COUNT" \
  --argjson ready_count "$READY_COUNT" \
  --argjson missing_type "$(printf '%s\n' "${MISSING_TYPE[@]}" | jq -R . | jq -s '.')" \
  --argjson missing_size "$(printf '%s\n' "${MISSING_SIZE[@]}" | jq -R . | jq -s '.')" \
  --argjson missing_priority "$(printf '%s\n' "${MISSING_PRIORITY[@]}" | jq -R . | jq -s '.')" \
  --argjson weak_ac "$(printf '%s\n' "${WEAK_AC[@]}" | jq -R . | jq -s '.')" \
  --argjson cycles "$(printf '%s\n' "${CYCLES[@]}" | jq -R . | jq -s '.')" \
  --argjson greenfield "$(printf '%s\n' "${GREENFIELD_WARNINGS[@]}" | jq -R . | jq -s '.')" \
  --argjson doc_drift "$(printf '%s\n' "${DOC_DRIFT[@]}" | jq -R . | jq -s '.')" \
  --arg report_md "$REPORT" \
  '{
    schema_version: "1.0",
    generated_at: ($date + "T00:00:00Z"),
    status_filter: $status,
    issue_count: $issue_count,
    ready_count: $ready_count,
    findings: {
      missing_type_label: $missing_type,
      missing_size_field: $missing_size,
      missing_priority_field: $missing_priority,
      weak_acceptance_criteria: $weak_ac,
      dependency_cycles: $cycles,
      greenfield_warnings: $greenfield,
      documentation_drift: $doc_drift
    },
    report_md: $report_md,
    pipeline_ready: ($ready_count == $issue_count and ($cycles | length) == 0)
  }' | nightgauge layout write checkout "$REPORT_JSON_NAME" > /dev/null

echo "JSON report written: $REPORT_JSON"
```

### Phase 4: Auto-Fix (Optional)

When `--fix` is provided, apply deterministic fixes:

- **Missing `type:*` label**: Delegate classification to
  `nightgauge issue infer-type <number> --apply --json`, which mirrors
  the canonical keyword rules (label > body > title > default `type:feature`)
  and adds the inferred label. The verb skips apply silently when
  `source == "default"` unless `--apply-default` is also passed — that
  safety net replaces the prior "Confirm before applying" prose. See
  [docs/GO_BINARY.md](https://github.com/nightgauge/nightgauge/blob/main/docs/GO_BINARY.md#issue-operations) and audit row
  B12 in [docs/SKILL_DETERMINISM_AUDIT.md](https://github.com/nightgauge/nightgauge/blob/main/docs/SKILL_DETERMINISM_AUDIT.md).
- **Missing Size/Priority fields**: Cannot be auto-fixed via labels — report the
  fix command for the project board.

```bash
if [ "$FIX_MODE" = "true" ]; then
  FIXES_APPLIED=0
  for issue in "${MISSING_TYPE[@]}"; do
    NUMBER=$(printf '%s\n' "$issue" | grep -oE '#[0-9]+' | tr -d '#')
    TITLE=$(echo "$issue" | sed 's/#[0-9]*: //')
    RESULT=$(nightgauge issue infer-type "$NUMBER" --apply --json 2>/dev/null)
    if [ -z "$RESULT" ]; then
      echo "  ERROR: infer-type failed for #$NUMBER"
      continue
    fi
    INFERRED_TYPE=$(printf '%s\n' "$RESULT" | jq -r '.type')
    SOURCE=$(printf '%s\n' "$RESULT" | jq -r '.source')
    APPLIED=$(printf '%s\n' "$RESULT" | jq -r '.applied')
    echo "Applying $INFERRED_TYPE to #$NUMBER ($TITLE)..."
    if [ "$APPLIED" = "true" ]; then
      echo "  Applied $INFERRED_TYPE to #$NUMBER (source: $SOURCE)"
      FIXES_APPLIED=$((FIXES_APPLIED + 1))
    elif [ "$SOURCE" = "default" ]; then
      echo "  Skipped #$NUMBER — keyword classification fell back to default; re-run with --apply-default to force"
    else
      echo "  ERROR: Failed to apply label to #$NUMBER"
    fi
  done
  echo "$FIXES_APPLIED labels auto-applied."
fi
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

## Error Handling

| Error                             | Cause                                          | Resolution                          |
| --------------------------------- | ---------------------------------------------- | ----------------------------------- |
| `gh: command not found`           | GitHub CLI not installed                       | Install from https://cli.github.com |
| `No project number`               | Missing .nightgauge/                           | Run /nightgauge:repo-init first     |
| `403 rate limit`                  | Too many API requests                          | Wait or use authenticated token     |
| `No issues found`                 | No issues with target status                   | Try different --status value        |
| `nightgauge binary not found`     | Binary not installed                           | Install it (see the error message)  |
| `backlog preflight could not run` | A board read failed, e.g. a whole blocker list | Read the error above, then re-run   |

## Integration

### Relationship to Other Skills

| Skill                       | Relationship                                            |
| --------------------------- | ------------------------------------------------------- |
| `/nightgauge:repo-init`     | Run repo-init first to set up project config            |
| `/nightgauge:backlog-groom` | Preflight extends groom with pipeline-specific checks   |
| `/nightgauge:issue-pickup`  | Preflight validates issues before pickup processes them |
| `/nightgauge:smart-setup`   | Greenfield checks warn when smart-setup hasn't been run |

### Recommended Usage

```
1. /nightgauge:repo-init         ← Set up repo
2. /nightgauge:smart-setup       ← Generate docs
3. /nightgauge:backlog-preflight ← Validate backlog ← This skill
4. /nightgauge:issue-pickup      ← Start pipeline
```

---

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) —
AI-Augmented SDLC Framework.
