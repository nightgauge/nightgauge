---
name: nightgauge-backlog-preflight
description: Validate backlog issues are pipeline-ready before processing. Checks required
  labels, acceptance criteria quality, dependency cycles, and greenfield
  readiness. Use before starting pipeline runs on a new or unfamiliar repo.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: pipeline-researcher
model: haiku
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

- Does NOT read pipeline context files (`.nightgauge/pipeline/*.json`)
- Does NOT write pipeline handoff files
- Generates standalone reports in `.nightgauge/reports/`
- Can be run at any time without affecting pipeline state

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Workflow

### Phase 0: Setup

<!-- include: ../_shared/PREFLIGHT.md -->

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

ISSUE_COUNT=$(echo "$ISSUES" | jq '[.data.organization.projectV2.items.nodes[] | select(.content.number != null)] | length')
echo "Found $ISSUE_COUNT issues with status: $STATUS_FILTER"
```

### Phase 2: Validation Checks (Checks 2.1–2.5)

Checks 2.1–2.5 are implemented in the Go binary for correctness, testability,
and speed. The binary path is preferred; the legacy shell fallback runs when
the binary is unavailable (e.g., first install, CI without binary).

```bash
# Binary path: preferred. (Audit row B26 — landed in #3084)
PREFLIGHT_JSON=""
MISSING_TYPE=()
MISSING_SIZE=()
MISSING_PRIORITY=()
WEAK_AC=()
CYCLES=()
GREENFIELD_WARNINGS=()

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

if [ -n "$BINARY" ]; then
  PREFLIGHT_JSON=$("$BINARY" backlog preflight \
    --owner "$OWNER" --status "$STATUS_FILTER" --focus "$FOCUS" --json 2>&1)
  PREFLIGHT_EXIT=$?

  if [ $PREFLIGHT_EXIT -eq 2 ]; then
    echo "ERROR: backlog preflight failed — $PREFLIGHT_JSON"
    exit 1
  fi

  # Extract per-category finding arrays for downstream reporting.
  MISSING_TYPE_JSON=$(echo "$PREFLIGHT_JSON" | jq -c '[.findings[] | select(.finding_type == "missing_type_label")]' 2>/dev/null || echo "[]")
  MISSING_SIZE_JSON=$(echo "$PREFLIGHT_JSON" | jq -c '[.findings[] | select(.finding_type == "missing_size_field")]' 2>/dev/null || echo "[]")
  MISSING_PRIORITY_JSON=$(echo "$PREFLIGHT_JSON" | jq -c '[.findings[] | select(.finding_type == "missing_priority_field")]' 2>/dev/null || echo "[]")
  WEAK_AC_JSON=$(echo "$PREFLIGHT_JSON" | jq -c '[.findings[] | select(.finding_type == "weak_acceptance_criteria")]' 2>/dev/null || echo "[]")
  CYCLES_JSON=$(echo "$PREFLIGHT_JSON" | jq -c '[.findings[] | select(.finding_type == "dependency_cycle")]' 2>/dev/null || echo "[]")
  GREENFIELD_JSON=$(echo "$PREFLIGHT_JSON" | jq -c '[.findings[] | select(.finding_type == "greenfield_warning")]' 2>/dev/null || echo "[]")

  # Populate bash arrays for the Phase 3 reporter (backward-compatible interface).
  while IFS= read -r line; do [ -n "$line" ] && MISSING_TYPE+=("$line"); done < <(echo "$MISSING_TYPE_JSON" | jq -r '.[] | "#\(.issue_number): \(.issue_title)"' 2>/dev/null)
  while IFS= read -r line; do [ -n "$line" ] && MISSING_SIZE+=("$line"); done < <(echo "$MISSING_SIZE_JSON" | jq -r '.[] | "#\(.issue_number): \(.issue_title)"' 2>/dev/null)
  while IFS= read -r line; do [ -n "$line" ] && MISSING_PRIORITY+=("$line"); done < <(echo "$MISSING_PRIORITY_JSON" | jq -r '.[] | "#\(.issue_number): \(.issue_title)"' 2>/dev/null)
  while IFS= read -r line; do [ -n "$line" ] && WEAK_AC+=("$line"); done < <(echo "$WEAK_AC_JSON" | jq -r '.[] | "#\(.issue_number): \(.issue_title) (\(.detail))"' 2>/dev/null)
  while IFS= read -r line; do [ -n "$line" ] && CYCLES+=("$line"); done < <(echo "$CYCLES_JSON" | jq -r '.[] | .detail' 2>/dev/null)
  while IFS= read -r line; do [ -n "$line" ] && GREENFIELD_WARNINGS+=("$line"); done < <(echo "$GREENFIELD_JSON" | jq -r '.[] | .detail' 2>/dev/null)

else
  # Legacy shell fallback — runs when binary is not installed.

  #### Check 2.1: Required Labels

  if [ "$FOCUS" = "all" ] || [ "$FOCUS" = "labels" ]; then
    for i in $(seq 0 $((ISSUE_COUNT - 1))); do
      NUMBER=$(echo "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.number")
      TITLE=$(echo "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.title")
      LABELS=$(echo "$ISSUES" | jq -r "[.data.organization.projectV2.items.nodes[$i].content.labels.nodes[].name] | join(\",\")")

      HAS_TYPE=$(echo "$LABELS" | grep -cE "type:(feature|bug|docs|refactor|chore|epic|spike)" || true)
      if [ "$HAS_TYPE" -eq 0 ]; then
        MISSING_TYPE+=("#$NUMBER: $TITLE")
      fi
    done
  fi

  #### Check 2.2: Board Field Validation

  if [ "$FOCUS" = "all" ] || [ "$FOCUS" = "labels" ]; then
    for i in $(seq 0 $((ISSUE_COUNT - 1))); do
      NUMBER=$(echo "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.number")
      TITLE=$(echo "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.title")

      SIZE=$(echo "$ISSUES" | jq -r "[.data.organization.projectV2.items.nodes[$i].fieldValues.nodes[] | select(.field.name==\"Size\") | .name] | first // empty")
      PRIORITY=$(echo "$ISSUES" | jq -r "[.data.organization.projectV2.items.nodes[$i].fieldValues.nodes[] | select(.field.name==\"Priority\") | .name] | first // empty")

      if [ -z "$SIZE" ]; then MISSING_SIZE+=("#$NUMBER: $TITLE"); fi
      if [ -z "$PRIORITY" ]; then MISSING_PRIORITY+=("#$NUMBER: $TITLE"); fi
    done
  fi

  #### Check 2.3: Acceptance Criteria Quality

  if [ "$FOCUS" = "all" ] || [ "$FOCUS" = "criteria" ]; then
    for i in $(seq 0 $((ISSUE_COUNT - 1))); do
      NUMBER=$(echo "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.number")
      TITLE=$(echo "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.title")
      BODY=$(echo "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.body // \"\"")
      BODY_LEN=${#BODY}
      AC_COUNT=$(echo "$BODY" | grep -c "\- \[ \]" 2>/dev/null || echo "0")

      if [ "$BODY_LEN" -lt 100 ] || [ "$AC_COUNT" -lt 2 ]; then
        REASON=""
        if [ "$BODY_LEN" -lt 100 ]; then REASON="body < 100 chars"; fi
        if [ "$AC_COUNT" -lt 2 ]; then
          [ -n "$REASON" ] && REASON="$REASON + "
          REASON="${REASON}fewer than 2 checkbox ACs (found: $AC_COUNT)"
        fi
        WEAK_AC+=("#$NUMBER: $TITLE ($REASON)")
      fi
    done
  fi

  #### Check 2.4: Dependency Cycle Detection

  if [ "$FOCUS" = "all" ] || [ "$FOCUS" = "dependencies" ]; then
    BLOCKING_DATA=$(gh api graphql -f query='
    {
      repository(owner: "'"${OWNER}"'", name: "'"${REPO#*/}"'") {
        issues(first: 100, states: OPEN) {
          nodes {
            number
            blockedBy(first: 10) { nodes { number state } }
            blocking(first: 10) { nodes { number state } }
          }
        }
      }
    }')

    CYCLE_OUTPUT=$(echo "$BLOCKING_DATA" | python3 << 'PYEOF'
import json, sys
data = json.loads(sys.stdin.read())
issues = data["data"]["repository"]["issues"]["nodes"]
graph = {}
for issue in issues:
    num = issue["number"]
    blocked_by = [b["number"] for b in issue.get("blockedBy", {}).get("nodes", []) if b["state"] == "OPEN"]
    if blocked_by:
        graph[num] = blocked_by
def find_cycles(graph):
    visited, path, cycles = set(), set(), []
    def dfs(node, current_path):
        if node in path:
            idx = list(current_path).index(node)
            cycles.append(list(current_path)[idx:] + [node])
            return
        if node in visited: return
        visited.add(node); path.add(node); current_path.append(node)
        for neighbor in graph.get(node, []): dfs(neighbor, current_path)
        path.remove(node); current_path.pop()
    for node in graph:
        if node not in visited: dfs(node, [])
    return cycles
cycles = find_cycles(graph)
if cycles:
    for cycle in cycles: print("CYCLE: " + " → ".join(f"#{n}" for n in cycle))
else: print("NO_CYCLES")
PYEOF
    )
    while IFS= read -r line; do
      if [[ "$line" == CYCLE:* ]]; then CYCLES+=("${line#CYCLE: }"); fi
    done <<< "$CYCLE_OUTPUT"
  fi

  #### Check 2.5: Greenfield Readiness

  if [ "$FOCUS" = "all" ] || [ "$FOCUS" = "greenfield" ]; then
    [ ! -f ".nightgauge/complexity-model.yaml" ] && GREENFIELD_WARNINGS+=("Missing complexity-model.yaml — run /nightgauge:repo-init or repo-init --seed-from <path>")
    [ ! -d "docs" ] && GREENFIELD_WARNINGS+=("Missing docs/ directory — feature-dev will use CLAUDE.md fallback for standards")
    [ ! -f "docs/CODE_STANDARDS.md" ] && GREENFIELD_WARNINGS+=("Missing docs/CODE_STANDARDS.md — feature-dev will fall back to CLAUDE.md or language defaults")
    if [ ! -f "docs/SECURITY_AND_ERROR_HANDLING.md" ] && [ ! -f "docs/SECURITY.md" ]; then
      GREENFIELD_WARNINGS+=("Missing docs/SECURITY*.md — feature-dev will use security defaults")
    fi
  fi

fi  # end binary/legacy branch
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
    NUMBER=$(echo "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.number")
    TITLE=$(echo "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.title")
    BODY=$(echo "$ISSUES" | jq -r ".data.organization.projectV2.items.nodes[$i].content.body // \"\"")
    BODY_LEN=${#BODY}

    # Only send issues with substantial bodies (skip trivial stubs)
    if [ "$BODY_LEN" -gt 200 ]; then
      DRIFT_CANDIDATES=$(echo "$DRIFT_CANDIDATES" | jq \
        --arg num "$NUMBER" --arg title "$TITLE" --arg body "$BODY" \
        '. + [{number: $num, title: $title, body: $body}]')
    fi
  done

  CANDIDATE_COUNT=$(echo "$DRIFT_CANDIDATES" | jq 'length')
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
mkdir -p .nightgauge/reports
DATE=$(date +%Y-%m-%d)
REPORT=".nightgauge/reports/preflight-${DATE}.md"
REPORT_JSON=".nightgauge/reports/preflight-${DATE}.json"

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
cat > "$REPORT" << REPORTEOF
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
  NUM=$(echo "$issue" | grep -oE '#[0-9]+' | tr -d '#')
  TTITLE=$(echo "$issue" | sed 's/#[0-9]*: //')
  echo "| #$NUM | $TTITLE | Missing type label | Add type:feature/bug/docs/refactor/chore |"
done)
$(for issue in "${MISSING_SIZE[@]}"; do
  NUM=$(echo "$issue" | grep -oE '#[0-9]+' | tr -d '#')
  TTITLE=$(echo "$issue" | sed 's/#[0-9]*: //')
  echo "| #$NUM | $TTITLE | Missing Size field | Set Size on project board |"
done)
$(for issue in "${MISSING_PRIORITY[@]}"; do
  NUM=$(echo "$issue" | grep -oE '#[0-9]+' | tr -d '#')
  TTITLE=$(echo "$issue" | sed 's/#[0-9]*: //')
  echo "| #$NUM | $TTITLE | Missing Priority field | Set Priority on project board |"
done)
$(for issue in "${WEAK_AC[@]}"; do
  NUM=$(echo "$issue" | grep -oE '#[0-9]+' | tr -d '#')
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
  }' > "$REPORT_JSON"

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
  [docs/GO_BINARY.md](../../docs/GO_BINARY.md#issue-operations) and audit row
  B12 in [docs/SKILL_DETERMINISM_AUDIT.md](../../docs/SKILL_DETERMINISM_AUDIT.md).
- **Missing Size/Priority fields**: Cannot be auto-fixed via labels — report the
  fix command for the project board.

```bash
if [ "$FIX_MODE" = "true" ]; then
  FIXES_APPLIED=0
  for issue in "${MISSING_TYPE[@]}"; do
    NUMBER=$(echo "$issue" | grep -oE '#[0-9]+' | tr -d '#')
    TITLE=$(echo "$issue" | sed 's/#[0-9]*: //')
    RESULT=$(nightgauge issue infer-type "$NUMBER" --apply --json 2>/dev/null)
    if [ -z "$RESULT" ]; then
      echo "  ERROR: infer-type failed for #$NUMBER"
      continue
    fi
    INFERRED_TYPE=$(echo "$RESULT" | jq -r '.type')
    SOURCE=$(echo "$RESULT" | jq -r '.source')
    APPLIED=$(echo "$RESULT" | jq -r '.applied')
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

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

| Error                   | Cause                        | Resolution                          |
| ----------------------- | ---------------------------- | ----------------------------------- |
| `gh: command not found` | GitHub CLI not installed     | Install from https://cli.github.com |
| `No project number`     | Missing .nightgauge/         | Run /nightgauge:repo-init first     |
| `403 rate limit`        | Too many API requests        | Wait or use authenticated token     |
| `No issues found`       | No issues with target status | Try different --status value        |

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
