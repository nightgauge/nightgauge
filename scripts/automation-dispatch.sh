#!/usr/bin/env bash
# automation-dispatch.sh — Route automation actions to handler scripts and log results.
#
# Usage: automation-dispatch.sh <issue-number> <trigger-name> <action-type> <action-json>
#
# Routes actions to handler scripts in scripts/handlers/ and appends a
# JSONL audit entry to the automation log file.
#
# Environment variables (set by automation-trigger.sh):
#   AUTOMATION_DRY_RUN  — "true" to skip execution
#   AUTOMATION_LOG_FILE — path to audit log file
#
# @see Issue #137 — Workflow Automation Triggers
# @see docs/AUTOMATIONS.md — Audit log format
set -euo pipefail

# ---------------------------------------------------------------------------
# Arguments
# ---------------------------------------------------------------------------
ISSUE_NUMBER="${1:?Usage: automation-dispatch.sh <issue-number> <trigger-name> <action-type> <action-json>}"
TRIGGER_NAME="${2:?Missing trigger-name}"
ACTION_TYPE="${3:?Missing action-type}"
ACTION_JSON="${4:?Missing action-json}"

# Validate issue number is numeric (required for jq --argjson)
if [[ ! "$ISSUE_NUMBER" =~ ^[0-9]+$ ]]; then
  echo "ERROR: issue-number must be numeric, got: $ISSUE_NUMBER" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HANDLERS_DIR="${SCRIPT_DIR}/handlers"

DRY_RUN="${AUTOMATION_DRY_RUN:-false}"
LOG_FILE="${AUTOMATION_LOG_FILE:-}"

# ---------------------------------------------------------------------------
# Logging function — appends JSONL to audit log
# ---------------------------------------------------------------------------
log_entry() {
  local status="$1"
  local message="$2"
  local duration_ms="${3:-0}"

  if [ -z "$LOG_FILE" ]; then
    return
  fi

  local timestamp
  timestamp=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)

  local is_dry_run="false"
  if [ "$DRY_RUN" = "true" ]; then
    is_dry_run="true"
  fi

  local entry
  entry=$(jq -n \
    --arg timestamp "$timestamp" \
    --arg trigger "$TRIGGER_NAME" \
    --arg action "$ACTION_TYPE" \
    --arg status "$status" \
    --argjson issue "$ISSUE_NUMBER" \
    --arg message "$message" \
    --argjson dry_run "$is_dry_run" \
    --argjson duration_ms "$duration_ms" \
    '{timestamp: $timestamp, trigger: $trigger, action: $action, status: $status, issue: $issue, message: $message, dry_run: $dry_run, duration_ms: $duration_ms}')

  echo "$entry" >> "$LOG_FILE"
}

# ---------------------------------------------------------------------------
# Dry-run check
# ---------------------------------------------------------------------------
if [ "$DRY_RUN" = "true" ]; then
  echo "    [DRY-RUN] Would execute: $ACTION_TYPE for #$ISSUE_NUMBER"
  log_entry "success" "[DRY-RUN] $ACTION_TYPE skipped (dry-run mode)" 0
  exit 0
fi

# ---------------------------------------------------------------------------
# Dispatch to handler
# ---------------------------------------------------------------------------
# Detect nanosecond support once for consistent timing
USE_NS=false
if date +%s%N >/dev/null 2>&1 && [[ "$(date +%s%N)" =~ ^[0-9]+$ ]]; then
  USE_NS=true
fi
if [ "$USE_NS" = "true" ]; then
  START_TIME=$(date +%s%N)
else
  START_TIME=$(date +%s)
fi
EXIT_CODE=0
OUTPUT=""

case "$ACTION_TYPE" in
  post_slack)
    OUTPUT=$("${HANDLERS_DIR}/post-slack.sh" "$ISSUE_NUMBER" "$ACTION_JSON" 2>&1) || EXIT_CODE=$?
    ;;
  assign_reviewers)
    OUTPUT=$("${HANDLERS_DIR}/assign-reviewers.sh" "$ISSUE_NUMBER" "$ACTION_JSON" 2>&1) || EXIT_CODE=$?
    ;;
  add_label)
    OUTPUT=$("${HANDLERS_DIR}/add-label.sh" "$ISSUE_NUMBER" "$ACTION_JSON" 2>&1) || EXIT_CODE=$?
    ;;
  remove_label)
    OUTPUT=$("${HANDLERS_DIR}/remove-label.sh" "$ISSUE_NUMBER" "$ACTION_JSON" 2>&1) || EXIT_CODE=$?
    ;;
  notify)
    OUTPUT=$("${HANDLERS_DIR}/notify.sh" "$ISSUE_NUMBER" "$ACTION_JSON" 2>&1) || EXIT_CODE=$?
    ;;
  run_script)
    OUTPUT=$("${HANDLERS_DIR}/run-script.sh" "$ISSUE_NUMBER" "$ACTION_JSON" 2>&1) || EXIT_CODE=$?
    ;;
  *)
    echo "ERROR: Unknown action type: $ACTION_TYPE" >&2
    log_entry "error" "Unknown action type: $ACTION_TYPE" 0
    exit 1
    ;;
esac

if [ "$USE_NS" = "true" ]; then
  END_TIME=$(date +%s%N)
  DURATION_MS=$(( (END_TIME - START_TIME) / 1000000 ))
else
  END_TIME=$(date +%s)
  DURATION_MS=$(( (END_TIME - START_TIME) * 1000 ))
fi

# ---------------------------------------------------------------------------
# Log result
# ---------------------------------------------------------------------------
if [ "$EXIT_CODE" -eq 0 ]; then
  log_entry "success" "${OUTPUT:-Action completed}" "$DURATION_MS"
  echo "    Action $ACTION_TYPE: success"
else
  log_entry "error" "${OUTPUT:-Action failed with exit code $EXIT_CODE}" "$DURATION_MS"
  echo "    Action $ACTION_TYPE: FAILED (exit $EXIT_CODE)" >&2
  exit "$EXIT_CODE"
fi
