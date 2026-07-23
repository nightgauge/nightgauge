---
name: nightgauge-queue
description: Manage the issue queue for sequential and batch pipeline processing. Add,
  list, remove, clear, and reorder queued issues. Supports epic expansion and
  label-based queuing. Use when staging which issues the pipeline processes next,
  or when reordering or clearing the queue.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Bash Glob Grep
---

# Nightgauge Queue

## Description

Manages the issue queue for pipeline processing. Provides operations to add,
list, remove, clear, and query queued issues. Integrates with the Go binary's
IPC-based queue system and the VSCode extension's queue sidebar. Supports epic
detection with automatic sub-issue expansion via `EpicBatchAssessor`.

**Use Cases:**

- Queuing multiple issues for overnight batch processing
- Reviewing current queue state before starting a pipeline run
- Removing issues that are no longer needed from the queue
- Clearing the queue after a sprint change or priority shift
- Queuing all issues with a specific label (e.g., `priority:high`)
- Queuing an epic to automatically expand and order its sub-issues

**When to Use:**

- Before starting a pipeline session to pre-load work items
- When multiple issues need sequential processing
- To manage queue during active pipeline execution
- After backlog grooming to queue newly prioritized issues

## Invocation

| Tool        | Command                                           |
| ----------- | ------------------------------------------------- |
| Claude Code | `/nightgauge:queue <args>`                        |
| Copilot     | Invoke via Agent Skills extension                 |
| Cursor      | Run via Agent Skills or direct SKILL.md           |
| Standalone  | `claude --skill skills/nightgauge-queue/SKILL.md` |

## Arguments

| Argument             | Description                             | Required | Default |
| -------------------- | --------------------------------------- | -------- | ------- |
| `<issue-numbers...>` | One or more issue numbers to add        | No\*     | -       |
| `--list` / `-l`      | Show current queue state                | No       | -       |
| `--clear`            | Clear all items from queue              | No       | -       |
| `--remove <number>`  | Remove specific issue from queue        | No       | -       |
| `--label <label>`    | Add issues matching label               | No       | -       |
| `--limit <N>`        | Limit issues added from `--label` query | No       | -       |

\*At least one operation (issue numbers, `--list`, `--clear`, `--remove`, or
`--label`) is required.

### Examples

```bash
# Queue specific issues for processing
/nightgauge:queue 42 43 44

# List current queue
/nightgauge:queue --list

# Remove an issue from queue
/nightgauge:queue --remove 43

# Clear entire queue
/nightgauge:queue --clear

# Queue issues by label (issues should have Ready status on the project board)
/nightgauge:queue --label "priority:high"

# Queue issues by label with limit
/nightgauge:queue --label "priority:high" --limit 5
```

---

## Prerequisites

- `nightgauge` binary installed and forge authenticated
  (`nightgauge forge auth login`)
- `jq` installed for JSON processing
- Git repository with GitHub remote
- For VSCode integration: the Go binary IPC server must be running

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Parse Arguments

#### Step 1.1: Determine Operation

```bash
OPERATION=""
ISSUE_NUMBERS=""
REMOVE_NUMBER=""
LABEL=""
LIMIT=""

# Parse arguments
while [ $# -gt 0 ]; do
  case "$1" in
    --list|-l)
      OPERATION="list"
      shift
      ;;
    --clear)
      OPERATION="clear"
      shift
      ;;
    --remove)
      OPERATION="remove"
      REMOVE_NUMBER="${2:?ERROR: --remove requires an issue number}"
      shift 2
      ;;
    --label)
      OPERATION="from-label"
      LABEL="${2:?ERROR: --label requires a label string}"
      shift 2
      ;;
    --limit)
      LIMIT="$2"
      shift 2
      ;;
    *)
      # Numeric arguments are issue numbers
      if echo "$1" | grep -qE '^[0-9]+$'; then
        OPERATION="add"
        ISSUE_NUMBERS="$ISSUE_NUMBERS $1"
      else
        echo "ERROR: Unknown argument: $1"
        echo "Usage: /nightgauge:queue <issue-numbers...> | --list | --clear | --remove <N> | --label <label>"
        exit 1
      fi
      shift
      ;;
  esac
done

if [ -z "$OPERATION" ]; then
  echo "ERROR: No operation specified."
  echo "Usage: /nightgauge:queue <issue-numbers...> | --list | --clear | --remove <N> | --label <label>"
  exit 1
fi
```

---

### Phase 2: Execute Operation (Deterministic)

#### Step 2.1: Locate Go Binary

```bash
# Require the Go binary for queue operations
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

if [ -z "$BINARY" ] || [ ! -x "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found." >&2
  echo "  Build with: go build -o bin/nightgauge ./cmd/nightgauge" >&2
  exit 1
fi
```

#### Step 2.2: Add Issues

When `OPERATION="add"`:

```bash
for ISSUE_NUM in $ISSUE_NUMBERS; do
  # Validate issue exists and is open
  ISSUE_JSON=$(nightgauge forge issue view "$ISSUE_NUM" --repo "$REPO" --json 2>&1)
  if [ $? -ne 0 ]; then
    echo "SKIP: Issue #$ISSUE_NUM not found"
    continue
  fi

  ISSUE_STATE=$(echo "$ISSUE_JSON" | jq -r '.state')
  if [ "$ISSUE_STATE" != "OPEN" ]; then
    echo "SKIP: Issue #$ISSUE_NUM is $ISSUE_STATE (not open)"
    continue
  fi

  ISSUE_TITLE=$(echo "$ISSUE_JSON" | jq -r '.title')

  # Binary auto-detects type:epic and expands sub-issues; plain issues are queued directly
  "$BINARY" queue add "$ISSUE_NUM"
  echo "ADDED: #$ISSUE_NUM - $ISSUE_TITLE"
done
```

#### Step 2.3: List Queue

When `OPERATION="list"`:

```bash
QUEUE_STATE=$("$BINARY" queue list --json 2>/dev/null)

ITEM_COUNT=$(echo "$QUEUE_STATE" | jq '.items | length' 2>/dev/null || echo "0")
QUEUE_STATUS=$(echo "$QUEUE_STATE" | jq -r '.status // "idle"' 2>/dev/null)

echo ""
echo "QUEUE STATUS"
echo "============"
echo ""
echo "Status: $QUEUE_STATUS"
echo "Items: $ITEM_COUNT"
echo ""

if [ "$ITEM_COUNT" -gt 0 ]; then
  echo "Position  Issue  Title"
  echo "───────────────────────────────────────────────────"
  echo "$QUEUE_STATE" | jq -r '.items[] |
    "   \(.position)      #\(.issueNumber)    \(.title)"'
fi
```

#### Step 2.4: Remove Issue

When `OPERATION="remove"`:

```bash
"$BINARY" queue remove "$REMOVE_NUMBER"

echo "Removed #$REMOVE_NUMBER from queue"
```

#### Step 2.5: Clear Queue

When `OPERATION="clear"`:

```bash
"$BINARY" queue clear

echo "Queue cleared"
```

#### Step 2.6: Queue by Label

When `OPERATION="from-label"`:

```bash
# Fetch issues matching label that are open
LIMIT_ARG=""
if [ -n "$LIMIT" ]; then
  LIMIT_ARG="--limit $LIMIT"
fi

MATCHING_ISSUES=$(nightgauge forge issue list --repo "$REPO" --label "$LABEL" --state open --json number,title,labels $LIMIT_ARG 2>&1)
if [ $? -ne 0 ]; then
  echo "ERROR: Failed to fetch issues with label '$LABEL'"
  echo "$MATCHING_ISSUES"
  exit 1
fi

MATCH_COUNT=$(echo "$MATCHING_ISSUES" | jq 'length')
if [ "$MATCH_COUNT" -eq 0 ]; then
  echo "No open issues found with label '$LABEL'"
  exit 0
fi

echo "Found $MATCH_COUNT issues with label '$LABEL'"

# Filter out epics from label-based queuing (epics must be queued explicitly)
echo "$MATCHING_ISSUES" | jq -r '.[] |
  select(.labels | map(.name) | index("type:epic") | not) |
  "\(.number) \(.title)"' | while read -r NUM TITLE; do
  "$BINARY" queue add "$NUM" "$TITLE" ""
  echo "ADDED: #$NUM - $TITLE"
done
```

---

### Phase 3: Display Results

#### Step 3.1: Show Queue Summary

After any mutating operation (add, remove, clear), display the updated queue
state:

```bash
# Fetch updated queue state
if [ "$OPERATION" != "list" ]; then
  UPDATED_STATE=$("$BINARY" queue list --json 2>/dev/null)

  FINAL_COUNT=$(echo "$UPDATED_STATE" | jq '.items | length' 2>/dev/null || echo "0")
  AUTO_START=$(echo "$UPDATED_STATE" | jq -r '.autoStart // true' 2>/dev/null)

  echo ""
  echo "Queue: $FINAL_COUNT items | Auto-start: $AUTO_START"

  if [ "$FINAL_COUNT" -gt 0 ] && [ "$AUTO_START" = "true" ]; then
    NEXT_ISSUE=$(echo "$UPDATED_STATE" | jq -r '.items[0].issueNumber' 2>/dev/null)
    echo "Next: #$NEXT_ISSUE will start when pipeline is idle"
  fi
fi
```

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

| Condition                   | Action                                                |
| --------------------------- | ----------------------------------------------------- |
| No arguments provided       | Error with usage hint                                 |
| Issue not found             | Skip with "not_found" reason in output                |
| Issue closed                | Skip with "not_open" reason in output                 |
| Already queued              | Skip with "already_queued" reason                     |
| Queue full                  | Skip with "queue_full" reason                         |
| Forge CLI not authenticated | Error with `nightgauge forge auth login` instructions |
| Invalid issue number        | Error with validation message                         |
| Go binary not found         | Error with build instructions                         |
| Label query returns nothing | Exit 0 with informational message                     |
| Epic expansion failure      | Warn and skip epic, continue with other issues        |

---

## Pipeline Position

```
UTILITIES (not part of main pipeline)

/nightgauge:queue <args>
       |
  Queue management tool — feeds issues into the pipeline
  Reads:  GitHub API (issue validation, label queries)
  Writes: .nightgauge/pipeline/queue-state.json (via Go IPC)
  Calls:  /nightgauge:assess-epic (when epic detected)
```

This is a standalone utility skill. It manages the queue that the pipeline
scheduler draws from but does not directly execute pipeline stages. Changes are
immediately reflected in the VSCode extension's Issue Queue sidebar.

---

## Integration

### VSCode Extension

This skill uses the same queue state as the VSCode extension's Issue Queue
feature. The queue state is managed by the Go binary via IPC, and the extension's
`IssueQueueService` relays `queue.changed` events to update the UI.

**Operations available via VSCode:**

- `enqueue(issueNumber, title, labels)` - Add issue to queue
- `remove(issueNumber)` - Remove issue from queue
- `clear()` - Clear all queue items
- `getQueue()` - Get current queue state
- `peek()` - View next item without removing
- `dequeueIndependent(maxSlots, runningIssues)` - Dequeue non-blocked items
- `drainEpicItems(epicNumber)` - Remove all sub-issues of an epic

### Auto-Start Behavior

When issues are queued and no pipeline is running:

- If `autoStart` is enabled (default): First queued issue starts automatically
- If `autoStart` is disabled: Issues wait until manually started

### Epic Queuing

When an epic issue number is queued, the system automatically:

1. Detects the `type:epic` label via `nightgauge forge issue view`
2. Runs `EpicBatchAssessor` to determine optimal processing strategy
3. Expands into queue entries based on assessment:
   - **Batch strategy**: Creates a single batch queue item containing all
     sub-issues
   - **Sequential strategy**: Enqueues each sub-issue individually
   - **Hybrid strategy**: Creates batch items for overlapping groups, individual
     items for the rest
4. Shows estimated savings from batching in queue view

---

## Related Skills

- **`/nightgauge:assess-epic`** - Epic batch strategy assessment (invoked
  automatically when queuing epics)
- **`skills/_shared/EPIC_HANDLING.md`** - Shared epic detection and handling
- **`skills/_shared/DEPENDENCY_CHECKING.md`** - Shared dependency checking logic

---

**Author:** nightgauge **License:** Apache-2.0
