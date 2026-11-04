---
name: queue
description: Manage the issue queue for sequential and batch pipeline processing. Add,
  list, remove, and clear queued issues. Supports epic expansion and
  label-based queuing. Use when staging which issues the pipeline processes next,
  or when clearing the queue.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Bash Glob Grep
disable-model-invocation: true
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

| Tool        | Command                                 |
| ----------- | --------------------------------------- |
| Claude Code | `/nightgauge:queue <args>`              |
| Copilot     | Invoke via Agent Skills extension       |
| Cursor      | Run via Agent Skills or direct SKILL.md |
| Headless    | `claude -p "/nightgauge:queue"`         |

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
      if printf '%s\n' "$1" | grep -qE '^[0-9]+$'; then
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

  ISSUE_STATE=$(printf '%s\n' "$ISSUE_JSON" | jq -r '.state')
  if [ "$ISSUE_STATE" != "OPEN" ]; then
    echo "SKIP: Issue #$ISSUE_NUM is $ISSUE_STATE (not open)"
    continue
  fi

  ISSUE_TITLE=$(printf '%s\n' "$ISSUE_JSON" | jq -r '.title')

  # Binary auto-detects type:epic and expands sub-issues; plain issues are queued directly
  "$BINARY" queue add "$ISSUE_NUM"
  echo "ADDED: #$ISSUE_NUM - $ISSUE_TITLE"
done
```

#### Step 2.3: List Queue

When `OPERATION="list"`:

```bash
QUEUE_STATE=$("$BINARY" queue list --json 2>/dev/null)

ITEM_COUNT=$(printf '%s\n' "$QUEUE_STATE" | jq '.items | length' 2>/dev/null || echo "0")
QUEUE_STATUS=$(printf '%s\n' "$QUEUE_STATE" | jq -r '.status // "idle"' 2>/dev/null)

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
  printf '%s\n' "$QUEUE_STATE" | jq -r '.items[] |
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
# Fetch issues matching label that are open (`forge issue list` returns open issues only)
REPO="${NIGHTGAUGE_REPO:-$(nightgauge git repo-slug)}"
MATCHING_ISSUES=$(nightgauge forge issue list --repo "$REPO" --labels "$LABEL" --json 2>&1)
if [ $? -ne 0 ]; then
  echo "ERROR: Failed to fetch issues with label '$LABEL'"
  echo "$MATCHING_ISSUES"
  exit 1
fi
if [ -n "$LIMIT" ]; then
  MATCHING_ISSUES=$(printf '%s\n' "$MATCHING_ISSUES" | jq --argjson n "$LIMIT" '.[:$n]')
fi

MATCH_COUNT=$(printf '%s\n' "$MATCHING_ISSUES" | jq 'length')
if [ "$MATCH_COUNT" -eq 0 ]; then
  echo "No open issues found with label '$LABEL'"
  exit 0
fi

echo "Found $MATCH_COUNT issues with label '$LABEL'"

# Filter out epics from label-based queuing (epics must be queued explicitly)
printf '%s\n' "$MATCHING_ISSUES" | jq -r '.[] |
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

  FINAL_COUNT=$(printf '%s\n' "$UPDATED_STATE" | jq '.items | length' 2>/dev/null || echo "0")
  AUTO_START=$(printf '%s\n' "$UPDATED_STATE" | jq -r '.autoStart // true' 2>/dev/null)

  echo ""
  echo "Queue: $FINAL_COUNT items | Auto-start: $AUTO_START"

  if [ "$FINAL_COUNT" -gt 0 ] && [ "$AUTO_START" = "true" ]; then
    NEXT_ISSUE=$(printf '%s\n' "$UPDATED_STATE" | jq -r '.items[0].issueNumber' 2>/dev/null)
    echo "Next: #$NEXT_ISSUE will start when pipeline is idle"
  fi
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
  Writes: checkout/queue-state.json (via Go IPC; nightgauge layout path checkout)
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
