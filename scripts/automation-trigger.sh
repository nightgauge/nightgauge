#!/usr/bin/env bash
# automation-trigger.sh — Detect matching automation triggers and dispatch actions.
#
# Usage: automation-trigger.sh <issue-number> <new-status> [previous-status]
#
# Reads .nightgauge/config.yaml for automations.triggers, matches the
# new status against trigger conditions, expands template variables, and
# calls automation-dispatch.sh for each matched action.
#
# Exit codes:
#   0 — Trigger matched, actions dispatched
#   1 — No matching trigger
#   2 — Config error (missing file, parse error)
#   3 — Template expansion error
#
# @see Issue #137 — Workflow Automation Triggers
# @see docs/AUTOMATIONS.md — Configuration reference
set -euo pipefail

# ---------------------------------------------------------------------------
# Arguments
# ---------------------------------------------------------------------------
ISSUE_NUMBER="${1:?Usage: automation-trigger.sh <issue-number> <new-status> [previous-status]}"
NEW_STATUS="${2:?Usage: automation-trigger.sh <issue-number> <new-status> [previous-status]}"
PREV_STATUS="${3:-}"

# Validate issue number is numeric
if [[ ! "$ISSUE_NUMBER" =~ ^[0-9]+$ ]]; then
  echo "ERROR: issue-number must be numeric, got: $ISSUE_NUMBER" >&2
  exit 2
fi

# Validate status arguments contain only safe characters
if [[ ! "$NEW_STATUS" =~ ^[a-zA-Z0-9:_\ -]+$ ]]; then
  echo "ERROR: new-status contains invalid characters: $NEW_STATUS" >&2
  exit 2
fi
if [ -n "$PREV_STATUS" ] && [[ ! "$PREV_STATUS" =~ ^[a-zA-Z0-9:_\ -]+$ ]]; then
  echo "ERROR: previous-status contains invalid characters: $PREV_STATUS" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ---------------------------------------------------------------------------
# Resolve config path (prefer config.yaml, fall back to legacy nightgauge.yaml)
# ---------------------------------------------------------------------------
CONFIG_FILE="${REPO_ROOT}/.nightgauge/config.yaml"
if [ ! -f "$CONFIG_FILE" ]; then
  CONFIG_FILE="${REPO_ROOT}/.nightgauge/nightgauge.yaml"
fi
if [ ! -f "$CONFIG_FILE" ]; then
  echo "ERROR: No config file found at .nightgauge/config.yaml" >&2
  exit 2
fi

# ---------------------------------------------------------------------------
# Check dependencies
# ---------------------------------------------------------------------------
for cmd in yq jq; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "ERROR: Required command '$cmd' not found in PATH" >&2
    exit 2
  fi
done

# ---------------------------------------------------------------------------
# Check master switch
# ---------------------------------------------------------------------------
AUTOMATIONS_ENABLED=$(yq -r '.automations.enabled // true' "$CONFIG_FILE")
if [ "${NIGHTGAUGE_AUTOMATIONS_ENABLED:-}" = "false" ]; then
  AUTOMATIONS_ENABLED="false"
fi
if [ "$AUTOMATIONS_ENABLED" = "false" ]; then
  echo "Automations disabled — skipping"
  exit 0
fi

# ---------------------------------------------------------------------------
# Dry-run mode
# ---------------------------------------------------------------------------
DRY_RUN=$(yq -r '.automations.dry_run // false' "$CONFIG_FILE")
if [ "${NIGHTGAUGE_AUTOMATIONS_DRY_RUN:-}" = "true" ]; then
  DRY_RUN="true"
fi
export AUTOMATION_DRY_RUN="$DRY_RUN"

# ---------------------------------------------------------------------------
# Log file
# ---------------------------------------------------------------------------
LOG_FILE=$(yq -r '.automations.log_file // ".nightgauge/logs/automation.log"' "$CONFIG_FILE")
if [ "${NIGHTGAUGE_AUTOMATIONS_LOG_FILE:-}" != "" ]; then
  LOG_FILE="$NIGHTGAUGE_AUTOMATIONS_LOG_FILE"
fi

# Validate log_file path: reject traversal and absolute paths
if echo "$LOG_FILE" | grep -qE '(^/|\.\.)'; then
  echo "ERROR: log_file path must be relative and cannot contain '..': $LOG_FILE" >&2
  exit 2
fi

export AUTOMATION_LOG_FILE="${REPO_ROOT}/${LOG_FILE}"
mkdir -p "$(dirname "$AUTOMATION_LOG_FILE")"

# ---------------------------------------------------------------------------
# Fetch issue metadata for template expansion
# ---------------------------------------------------------------------------
ISSUE_TITLE=""
ISSUE_URL=""
ISSUE_LABELS=""
ISSUE_ASSIGNEE=""
REPO_OWNER=$(yq -r '.owner // ""' "$CONFIG_FILE")
REPO_NAME=$(yq -r '.repo // ""' "$CONFIG_FILE")

if command -v gh >/dev/null 2>&1; then
  ISSUE_JSON=$(gh issue view "$ISSUE_NUMBER" --json title,url,labels,assignees 2>/dev/null || echo "{}")
  ISSUE_TITLE=$(echo "$ISSUE_JSON" | jq -r '.title // ""' 2>/dev/null || echo "")
  ISSUE_URL=$(echo "$ISSUE_JSON" | jq -r '.url // ""' 2>/dev/null || echo "")
  ISSUE_LABELS=$(echo "$ISSUE_JSON" | jq -r '[.labels[]?.name] | join(",")' 2>/dev/null || echo "")
  ISSUE_ASSIGNEE=$(echo "$ISSUE_JSON" | jq -r '[.assignees[]?.login] | first // ""' 2>/dev/null || echo "")
fi

# ---------------------------------------------------------------------------
# Template expansion function
# Uses bash parameter substitution instead of sed to avoid injection from
# user-controlled values (issue titles, labels, etc.) containing sed
# metacharacters like |, &, or \.
# ---------------------------------------------------------------------------
expand_template() {
  local template="$1"
  local timestamp
  timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)

  template="${template//\{\{issue.number\}\}/$ISSUE_NUMBER}"
  template="${template//\{\{issue.title\}\}/$ISSUE_TITLE}"
  template="${template//\{\{issue.url\}\}/$ISSUE_URL}"
  template="${template//\{\{issue.labels\}\}/$ISSUE_LABELS}"
  template="${template//\{\{issue.assignee\}\}/$ISSUE_ASSIGNEE}"
  template="${template//\{\{status.current\}\}/$NEW_STATUS}"
  template="${template//\{\{status.new\}\}/$NEW_STATUS}"
  template="${template//\{\{status.previous\}\}/$PREV_STATUS}"
  template="${template//\{\{status.old\}\}/$PREV_STATUS}"
  template="${template//\{\{repo.owner\}\}/$REPO_OWNER}"
  template="${template//\{\{repo.name\}\}/$REPO_NAME}"
  template="${template//\{\{timestamp\}\}/$timestamp}"

  echo "$template"
}

# ---------------------------------------------------------------------------
# Match triggers
# ---------------------------------------------------------------------------
TRIGGER_COUNT=$(yq -r '(.automations.triggers // []) | length' "$CONFIG_FILE")
MATCHED=0
FAILED=0

for ((i = 0; i < TRIGGER_COUNT; i++)); do
  TRIGGER_STATUS=$(yq -r ".automations.triggers[$i].trigger // \"\"" "$CONFIG_FILE")
  TRIGGER_FROM=$(yq -r ".automations.triggers[$i].from // \"\"" "$CONFIG_FILE")
  TRIGGER_NAME=$(yq -r ".automations.triggers[$i].name // \"trigger-$i\"" "$CONFIG_FILE")

  # Check trigger match
  if [ "$TRIGGER_STATUS" != "$NEW_STATUS" ]; then
    continue
  fi

  # Check transition filter (from field)
  if [ -n "$TRIGGER_FROM" ] && [ "$TRIGGER_FROM" != "null" ]; then
    if [ "$TRIGGER_FROM" != "$PREV_STATUS" ]; then
      continue
    fi
  fi

  echo "Matched trigger: $TRIGGER_NAME (status=$TRIGGER_STATUS)"
  MATCHED=$((MATCHED + 1))

  # Dispatch each action
  ACTION_COUNT=$(yq -r ".automations.triggers[$i].actions | length // 0" "$CONFIG_FILE")
  for ((j = 0; j < ACTION_COUNT; j++)); do
    ACTION_TYPE=$(yq -r ".automations.triggers[$i].actions[$j].type // \"\"" "$CONFIG_FILE")
    ACTION_JSON=$(yq -o=json ".automations.triggers[$i].actions[$j]" "$CONFIG_FILE")

    # Expand templates in the action JSON
    EXPANDED_JSON=$(expand_template "$ACTION_JSON")

    echo "  Dispatching action: $ACTION_TYPE"
    if ! "${SCRIPT_DIR}/automation-dispatch.sh" \
      "$ISSUE_NUMBER" \
      "$TRIGGER_NAME" \
      "$ACTION_TYPE" \
      "$EXPANDED_JSON"; then
        echo "  WARNING: Action $ACTION_TYPE failed" >&2
        FAILED=$((FAILED + 1))
    fi
  done
done

if [ "$MATCHED" -eq 0 ]; then
  echo "No matching triggers for status '$NEW_STATUS'"
  exit 1
fi

if [ "$FAILED" -gt 0 ]; then
  echo "Automation complete: $MATCHED trigger(s) matched, $FAILED action(s) failed"
  exit 1
fi

echo "Automation complete: $MATCHED trigger(s) matched"
exit 0
