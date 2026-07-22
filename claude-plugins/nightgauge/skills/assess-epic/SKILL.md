---
name: assess-epic
description: Analyze an epic's sub-issues and recommend batch vs sequential pipeline
  processing strategy. Use before starting work on an epic to determine optimal
  execution approach.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
  chainable: true
allowed-tools: Read Bash Glob Grep
---

# Nightgauge Assess Epic

## Description

Analyzes an epic's sub-issues to determine whether they should be processed via
batch, sequential, or hybrid pipeline execution. Extracts file overlap, size
variance, and dependency signals from sub-issues, then computes a deterministic
strategy recommendation with confidence scoring and estimated savings.

**Use Cases:**

- Planning pipeline execution strategy before starting an epic
- Evaluating whether sub-issues have sufficient file overlap for batching
- Estimating token and cost savings from parallel batch execution
- Identifying dependency chains that require sequential processing

**When to Use:**

- Before queuing an epic for pipeline processing
- When an epic has 3+ sub-issues and you want to optimize execution
- After sub-issues change (new issues added, scope changes) to refresh strategy
- When deciding between batch mode and sequential processing

## Invocation

| Tool        | Command                                                 |
| ----------- | ------------------------------------------------------- |
| Claude Code | `/nightgauge:assess-epic <epic-number>`                 |
| Copilot     | Invoke via Agent Skills extension                       |
| Cursor      | Run via Agent Skills or direct SKILL.md                 |
| Standalone  | `claude --skill skills/nightgauge-assess-epic/SKILL.md` |

## Arguments

| Argument        | Description                     | Required | Default |
| --------------- | ------------------------------- | -------- | ------- |
| `<epic-number>` | GitHub issue number of the epic | Yes      | -       |

### Examples

```bash
# Assess epic #799 for batch processing
/nightgauge:assess-epic 799

# Assess a different epic
/nightgauge:assess-epic 1672
```

---

## Prerequisites

- `gh` CLI installed and authenticated (`gh auth login`)
- `jq` installed for JSON processing
- Epic issue must exist with the `type:epic` label
- Sub-issues must be linked via GitHub's native sub-issue feature (GraphQL
  `subIssues` API)
- Git repository with GitHub remote

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Validate Input and Fetch Epic

#### Step 1.1: Validate Epic Number

```bash
EPIC_NUMBER="${1:?ERROR: Epic number is required. Usage: /nightgauge:assess-epic <epic-number>}"

# Validate numeric input
if ! echo "$EPIC_NUMBER" | grep -qE '^[0-9]+$'; then
  echo "ERROR: Epic number must be a positive integer (got: $EPIC_NUMBER)"
  exit 1
fi
```

#### Step 1.2: Fetch Epic Issue

```bash
EPIC_JSON=$(gh issue view "$EPIC_NUMBER" --json number,title,labels,state,body 2>&1)
if [ $? -ne 0 ]; then
  echo "ERROR: Could not fetch issue #$EPIC_NUMBER. Verify the issue exists."
  echo "  Try: gh issue view $EPIC_NUMBER"
  exit 1
fi

# Verify it has the type:epic label
IS_EPIC=$(echo "$EPIC_JSON" | jq -r '.labels[]?.name' | grep -c '^type:epic$' || echo "0")
if [ "$IS_EPIC" -eq 0 ]; then
  echo "ERROR: Issue #$EPIC_NUMBER does not have the 'type:epic' label."
  echo "  This command only works on epic issues."
  exit 1
fi

EPIC_TITLE=$(echo "$EPIC_JSON" | jq -r '.title')
echo "Epic #$EPIC_NUMBER: $EPIC_TITLE"
```

#### Step 1.3: Fetch Sub-Issues via GraphQL

<!-- include: ../_shared/EPIC_HANDLING.md (sub-issue fetch section) -->

```bash
# Detect owner/repo from git remote
REMOTE_URL=$(git remote get-url origin 2>/dev/null)
OWNER=$(echo "$REMOTE_URL" | sed -E 's|.*[:/]([^/]+)/([^/.]+)(\.git)?$|\1|')
REPO=$(echo "$REMOTE_URL" | sed -E 's|.*[:/]([^/]+)/([^/.]+)(\.git)?$|\2|')

SUB_ISSUES_JSON=$(gh api graphql -f query='
  query($owner: String!, $repo: String!, $number: Int!) {
    repository(owner: $owner, name: $repo) {
      issue(number: $number) {
        subIssues(first: 50) {
          nodes { number title state labels(first: 10) { nodes { name } } }
        }
      }
    }
  }
' -f owner="$OWNER" -f repo="$REPO" -F number="$EPIC_NUMBER" 2>&1)

if [ $? -ne 0 ]; then
  echo "ERROR: Failed to fetch sub-issues for epic #$EPIC_NUMBER"
  echo "$SUB_ISSUES_JSON"
  exit 1
fi

SUB_COUNT=$(echo "$SUB_ISSUES_JSON" | jq '.data.repository.issue.subIssues.nodes | length')
if [ "$SUB_COUNT" -eq 0 ]; then
  echo "ERROR: Epic #$EPIC_NUMBER has no sub-issues linked via GitHub's sub-issue feature."
  echo "  Sub-issues must be linked using the addSubIssue GraphQL mutation."
  echo "  Body text references (e.g., '- [ ] #123') do NOT count."
  exit 1
fi

echo "Found $SUB_COUNT sub-issues"
echo "$SUB_ISSUES_JSON" | jq -r '.data.repository.issue.subIssues.nodes[] |
  "  #\(.number) - \(.title) [\(.state)]"'
```

---

### Phase 2: Assess with Go Binary (Deterministic)

```bash
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
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found in PATH or bin/nightgauge"
  echo "Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest"
  exit 1
fi

ASSESS_JSON=$("$BINARY" epic assess "$EPIC_NUMBER" \
  --owner "$OWNER" --repo "$REPO" --json 2>/dev/null)

if [ $? -ne 0 ] || [ -z "$ASSESS_JSON" ]; then
  echo "ERROR: nightgauge epic assess failed for epic #$EPIC_NUMBER"
  exit 1
fi

STRATEGY=$(echo "$ASSESS_JSON" | jq -r '.strategy')
REASONING=$(echo "$ASSESS_JSON" | jq -r '.reasoning')
EST_COST=$(echo "$ASSESS_JSON" | jq -r '.estimatedCostUsd')
EST_MINUTES=$(echo "$ASSESS_JSON" | jq -r '.estimatedMinutes')
OPEN_COUNT=$(echo "$ASSESS_JSON" | jq '.issues | length')

echo "Strategy: $(echo "$STRATEGY" | tr '[:lower:]' '[:upper:]')"
echo "Reasoning: $REASONING"
```

---

### Phase 4: Generate Report

#### Step 4.1: Format Assessment Output

```bash
echo ""
echo "Epic #$EPIC_NUMBER Assessment"
echo "===================="
echo ""
echo "Strategy: $(echo "$STRATEGY" | tr '[:lower:]' '[:upper:]')"
echo "Reasoning: $REASONING"
echo ""
echo "Sub-Issues: $OPEN_COUNT open / $SUB_COUNT total"
echo ""
echo "Estimates:"
echo "  Cost: \$$EST_COST"
echo "  Time: $EST_MINUTES min"
echo ""
echo "Per-Issue Breakdown:"
echo "$ASSESS_JSON" | jq -r '.issues[] |
  "  #\(.issueNumber) complexity=\(.complexityScore) model=\(.recommendedModel)\(if .hasDependencies then " [blocked]" else "" end)"'
echo ""
echo "Recommendation:"
case "$STRATEGY" in
  parallel)
    echo "  Process all $OPEN_COUNT sub-issues in parallel batch mode."
    ;;
  mixed)
    echo "  Group independent sub-issues for parallel batch processing."
    echo "  Run dependency-blocked issues sequentially after their blockers."
    ;;
  sequential)
    echo "  Process sub-issues one at a time in dependency order."
    echo "  High dependency complexity makes batching counterproductive."
    ;;
esac
```

#### Step 4.2: Write JSON Report

```bash
REPORT_DIR=".nightgauge/pipeline"
mkdir -p "$REPORT_DIR"
REPORT_FILE="$REPORT_DIR/epic-assessment-${EPIC_NUMBER}.json"

echo "$ASSESS_JSON" | jq \
  --arg epic_number "$EPIC_NUMBER" \
  --arg epic_title "$(echo "$EPIC_TITLE" | sed 's/"/\\"/g')" \
  --argjson sub_total "$SUB_COUNT" \
  --arg assessed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '. + {
    "schema_version": "2",
    "epic_number": ($epic_number | tonumber),
    "epic_title": $epic_title,
    "sub_issues": { "total": $sub_total, "open": (.issues | length) },
    "assessed_at": $assessed_at
  }' > "$REPORT_FILE"

echo ""
echo "Report written to: $REPORT_FILE"
```

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

| Condition                     | Action                                       |
| ----------------------------- | -------------------------------------------- |
| Missing epic number argument  | Error with usage hint                        |
| Invalid epic number (non-int) | Error with validation message                |
| Epic issue not found          | Error with GitHub issue link suggestion      |
| Issue lacks `type:epic` label | Error explaining epic requirement            |
| No sub-issues linked          | Error explaining GraphQL linking requirement |
| All sub-issues closed         | Exit 0 with "no assessment needed" message   |
| `gh` CLI not authenticated    | Error with `gh auth login` instructions      |
| GraphQL query failure         | Error with raw response for debugging        |
| `bc` not available            | Fall back to integer arithmetic              |
| Sub-issue body fetch failure  | Continue with partial data, warn user        |

---

## Pipeline Position

```
UTILITIES (not part of main pipeline)

/nightgauge:assess-epic <epic-number>
       |
  Standalone analysis tool for epic batch planning
  Reads: GitHub API (epic + sub-issues + blocking)
  Writes: .nightgauge/pipeline/epic-assessment-{N}.json
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs. The queue command
(`/nightgauge:queue`) automatically invokes this assessment when an epic
issue number is queued.

---

## Related Skills

- **`/nightgauge:queue`** - Queue management; auto-invokes assess-epic when
  epic issues are queued
- **`skills/_shared/EPIC_HANDLING.md`** - Shared epic detection and handling
  patterns
- **`skills/_shared/DEPENDENCY_CHECKING.md`** - Shared dependency checking logic

---

**Author:** nightgauge **License:** Apache-2.0
