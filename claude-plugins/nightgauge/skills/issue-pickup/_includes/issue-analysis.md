# Issue Pickup — Phase 3: Issue Analysis

Procedural detail for **Phase 3** (`issue-analysis`, index 5): fetch the full
issue, detect parent epic / epic type / dependencies, parse content, derive the
change-detection and routing decision, and produce the requirements summary.

## Contents

- [Step 3.1: Fetch Full Issue Details](#step-31-fetch-full-issue-details)
- [Step 3.1.4: Detect Parent Epic](#step-314-detect-parent-epic)
- [Step 3.2: Parse Issue Content](#step-32-parse-issue-content)
- [Step 3.2.5: Change Detection and Routing](#step-325-change-detection-and-routing)
- [Step 3.3: Create Requirements Summary](#step-33-create-requirements-summary)

---

## Step 3.1: Fetch Full Issue Details

```bash
nightgauge forge issue view <number> --repo $REPO --json number,title,body,labels,milestone,assignees,comments,state
```

## Step 3.1.4: Detect Parent Epic

Parent epic detection is handled by the Go binary in Phase 5. No
action needed here — the binary performs GraphQL parent lookup, epic branch
detection, and lazy creation atomically.

The `$PARENT_ISSUE_NUMBER` and `$EPIC_BRANCH` variables are extracted from the
script's JSON output in Step 5.4.

> Steps 3.1.5 (Check for Epic Type) and 3.1.6 (Check Dependencies) remain in the
> SKILL.md body — apply them there before continuing with the parsing below.

## Step 3.2: Parse Issue Content

Extract from issue body:

1. **User Story**: Pattern: "As a [user], I want [goal] so that [benefit]"
2. **Acceptance Criteria**: Checkbox lists `- [ ]` or numbered lists
3. **Technical Requirements**: File references, API endpoints, dependencies
4. **Labels**: `bug` → `fix/`, `feature`/`enhancement` → `feat/`,
   `documentation` → `docs/`, `refactor` → `refactor/`

## Step 3.2.5: Change Detection and Routing

**PURPOSE**: Analyze issue labels and content to determine pipeline routing.

### Preferred Path — `nightgauge issue route` (#3062)

When the Go binary is on PATH, derive the routing decision through a single
verb call. The verb wraps the canonical `routing.Derive` function and emits
the same fields the rest of this phase consumes — see
[docs/GO_BINARY.md](../../../docs/GO_BINARY.md#issue-operations) and audit row
**B4** in [docs/SKILL_DETERMINISM_AUDIT.md](../../../docs/SKILL_DETERMINISM_AUDIT.md).

```bash
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [[ -z "$BINARY" ]]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [[ -x "$REPO_ROOT/bin/nightgauge" ]] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"

ROUTE_JSON=""
if [[ -n "$BINARY" ]]; then
  ROUTE_JSON=$("$BINARY" issue route "$ISSUE_NUMBER" --json 2>/dev/null)
fi

if [[ -n "$ROUTE_JSON" ]]; then
  TYPE_LABEL=$(echo "$ROUTE_JSON" | jq -r '.task_type')
  SIZE_LABEL=$(echo "$ROUTE_JSON" | jq -r '.effective_size')
  PRIORITY_LABEL=$(echo "$ROUTE_JSON" | jq -r '.effective_priority')
  CHANGE_TYPE=$(echo "$ROUTE_JSON" | jq -r '.change_type')
  COMPLEXITY_SCORE=$(echo "$ROUTE_JSON" | jq -r '.complexity_score')
  SUGGESTED_ROUTE=$(echo "$ROUTE_JSON" | jq -r '.suggested_route')
  SKIP_STAGES=$(echo "$ROUTE_JSON" | jq -r '.skip_stages | join(",")')
  FOUNDATION_TASK=$(echo "$ROUTE_JSON" | jq -r '.foundation_task')
  RATIONALE=$(echo "$ROUTE_JSON" | jq -r '.rationale')
  echo "Routing decision via Go verb: $RATIONALE"
  # All routing variables are now populated — skip ahead to Step 3.3.
fi
```

### Fallback Path (binary unavailable)

If `$ROUTE_JSON` is empty, fall through to the inline derivation below. The
fallback mirrors the verb's algorithm step-for-step so behaviour is
identical when the binary is present or absent — see ADR-003 in
`.nightgauge/knowledge/features/3062-issue-route-verb-pipeline-stage-routing/decisions.md`.

### Extract Routing Labels

```bash
# type:* stays label-based (labels are authoritative for type)
TYPE_LABEL=$(echo "$LABELS" | grep -oE "type:(feature|bug|docs|refactor|chore)" | cut -d: -f2)

# Read Size and Priority from project board fields (board-first, label fallback)
CONTEXT_FILE=".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"
SIZE_LABEL=""
PRIORITY_BOARD=""
if [[ -f "$CONTEXT_FILE" ]]; then
  SIZE_LABEL=$(jq -r '.board_fields.size // empty' "$CONTEXT_FILE" 2>/dev/null)
  PRIORITY_BOARD=$(jq -r '.board_fields.priority // empty' "$CONTEXT_FILE" 2>/dev/null)
fi

# Fallback: query board directly if context not yet available
if [[ -z "$SIZE_LABEL" || -z "$PRIORITY_BOARD" ]]; then
  REPO=$(nightgauge forge repo view --repo $REPO --json nameWithOwner -q '.nameWithOwner' 2>/dev/null)
  OWNER="${REPO%/*}"
  REPO_NAME="${REPO#*/}"
  BOARD_RESULT=$(nightgauge forge graphql -f query="
    query { repository(owner: \"$OWNER\", name: \"$REPO_NAME\") {
      issue(number: $ISSUE_NUMBER) { projectItems(first: 5) {
        nodes { fieldValues(first: 15) { nodes {
          ... on ProjectV2ItemFieldSingleSelectValue {
            name field { ... on ProjectV2SingleSelectField { name } }
          }
        }}}
      }}
    }}" 2>/dev/null)

  if [[ -z "$SIZE_LABEL" ]]; then
    SIZE_LABEL=$(echo "$BOARD_RESULT" \
      | jq -r '[.data.repository.issue.projectItems.nodes[].fieldValues.nodes[]
         | select(type=="object" and .field.name=="Size") | .name] | first // empty' 2>/dev/null)
    # Label fallback
    if [[ -z "$SIZE_LABEL" ]]; then
      SIZE_LABEL=$(echo "$LABELS" | grep -oE "size:(XS|S|M|L|XL)" | cut -d: -f2)
    fi
  fi

  if [[ -z "$PRIORITY_BOARD" ]]; then
    PRIORITY_BOARD=$(echo "$BOARD_RESULT" \
      | jq -r '[.data.repository.issue.projectItems.nodes[].fieldValues.nodes[]
         | select(type=="object" and .field.name=="Priority") | .name] | first // empty' 2>/dev/null)
  fi
fi

# Map board priority (P0/P1/P2/P3) to level (critical/high/medium/low)
case "$PRIORITY_BOARD" in
  P0) PRIORITY_LABEL="critical" ;;
  P1) PRIORITY_LABEL="high" ;;
  P2) PRIORITY_LABEL="medium" ;;
  P3) PRIORITY_LABEL="low" ;;
  *)  # Fallback to label
      PRIORITY_LABEL=$(echo "$LABELS" | grep -oE "priority:(critical|high|medium|low)" | cut -d: -f2)
      ;;
esac
```

### Foundation Task Detection (#1318)

Foundation tasks are `type:chore` issues whose titles match scaffold/setup
patterns. They have well-defined ACs, no existing patterns to read, and don't
need feature-planning. Force trivial routing to skip planning + validate.

```bash
# Foundation task detection (#1318): type:chore + scaffold title pattern
# → forces trivial routing (skip planning + validate)
FOUNDATION_TASK=false
if [[ "$TYPE_LABEL" == "chore" ]]; then
  TITLE_LOWER=$(echo "$TITLE" | tr '[:upper:]' '[:lower:]')
  if echo "$TITLE_LOWER" | grep -qE '\b(scaffold|setup|bootstrap|initialize|initialise|init|configure)\b'; then
    FOUNDATION_TASK=true
    # Override size to XS (force trivial path, complexity ≤ 2)
    SIZE_LABEL="XS"
    echo "Foundation task detected (type:chore + scaffold title). Routing to trivial path (skip planning + validate)."
  fi
fi
```

### Determine Change Type

| Change Type | Detection Rules                                                   |
| ----------- | ----------------------------------------------------------------- |
| `docs`      | Has `type:docs` or `documentation` label, OR title contains "doc" |
| `config`    | Title/body mentions only config files (yaml, json, env)           |
| `code`      | Default for all other changes                                     |

### Determine Task Type

| Task Type      | Detection Rules                                                              |
| -------------- | ---------------------------------------------------------------------------- |
| `verification` | Has `type:verification` label OR title contains "verify", "confirm", "audit" |
| `docs-only`    | Has `type:docs` label AND no code indicators                                 |
| `bugfix`       | Has `type:bug` or `bug` label                                                |
| `refactor`     | Has `type:refactor` or `refactor` label                                      |
| `chore`        | Has `type:chore` or `chore` label                                            |
| `feature`      | Default for all other changes                                                |

### Calculate Complexity Score

Use Fibonacci scoring (1, 2, 3, 5, 8) based on size and priority:

| Size Label | Base Score | Priority Adjustment        | Final Score Range |
| ---------- | ---------- | -------------------------- | ----------------- |
| XS         | 1          | +1 if critical/high        | 1-2               |
| S          | 2          | +1 if critical/high        | 2-3               |
| M          | 3          | +2 if critical, +1 if high | 3-5               |
| L          | 5          | +3 if critical             | 5-8               |
| XL         | 8          | -                          | 8                 |

### Determine Routing Path and Skip Stages

Complexity-based skipping applies to ALL task types (Issue #1593). Any issue
with complexity ≤ 2 skips planning and validate regardless of task type — a
complexity-2 refactor (delete dead code) doesn't need a PLAN.md or validation.

| Complexity | Stages Run                                                |
| ---------- | --------------------------------------------------------- |
| ≤ 2        | pickup → dev → pr-create → pr-merge                       |
| 3-4        | pickup → planning → dev → validate → pr-create → pr-merge |
| ≥ 5        | All stages with extended context                          |

| Route       | Criteria                                  | Stages Skipped                     |
| ----------- | ----------------------------------------- | ---------------------------------- |
| `trivial`   | docs/config change OR complexity ≤ 2      | feature-planning, feature-validate |
| `standard`  | code change with complexity 3-4           | (depends on task type)             |
| `extensive` | complexity ≥ 5 AND critical/high priority | (uses extended documentation)      |

**Task-Type Stage Mapping (after complexity-based skipping):**

| Task Type            | Standard (complexity 3+)                                                         | Trivial (complexity ≤ 2)                       |
| -------------------- | -------------------------------------------------------------------------------- | ---------------------------------------------- |
| `verification`       | issue-pickup, feature-dev (only 2 stages)                                        | issue-pickup, feature-dev                      |
| `docs-only`          | issue-pickup, feature-planning, feature-dev, pr-create, pr-merge (skip validate) | issue-pickup, feature-dev, pr-create, pr-merge |
| `chore`              | issue-pickup, feature-dev, feature-validate, pr-create, pr-merge (skip planning) | issue-pickup, feature-dev, pr-create, pr-merge |
| `chore` (foundation) | `type:chore` + title matches scaffold/setup/bootstrap/initialize                 | trivial — skip planning + validate             |
| `feature`            | All 6 stages                                                                     | issue-pickup, feature-dev, pr-create, pr-merge |
| `bugfix`             | All 6 stages                                                                     | issue-pickup, feature-dev, pr-create, pr-merge |
| `refactor`           | All 6 stages                                                                     | issue-pickup, feature-dev, pr-create, pr-merge |

### Validate Size Estimate Against Calibration (Optional — Issue #1589)

If `.nightgauge/pipeline/calibration.json` exists, the orchestrator
automatically validates the size estimate against historical cost/duration data
for the same size bucket. Outliers are logged as warnings in the pipeline
output.

This is **informational only** — it does not change the routing decision. The
calibration table is auto-updated after each pipeline completion by the
PostPipelineAnalyzer.

## Step 3.3: Create Requirements Summary

**REQUIRED** — The `requirements` object must always be written to the context
file. Never omit it. Phase 8 writes this inline to `issue-{N}.json`; ensure
the requirements field is always populated before writing.

Generate structured requirements:

```markdown
## Issue #<number>: <title>

### Type

[bug|feature|docs|refactor|spike] (from labels — use `docs`, never
`documentation`)

### Summary

[1-2 sentence summary from issue body]

### Acceptance Criteria

- [ ] [Criterion 1]
- [ ] [Criterion 2]

### Technical Notes

- Files mentioned: [list]
- Components involved: [list]
- Dependencies: [list]
```
