#!/usr/bin/env bash
# remove-label.sh — Remove a label from a GitHub issue.
#
# Usage: remove-label.sh <issue-number> <action-json>
#
# @see Issue #137 — Workflow Automation Triggers
# @see docs/AUTOMATIONS.md — remove_label action type
set -euo pipefail

ISSUE_NUMBER="${1:?Missing issue-number}"
ACTION_JSON="${2:?Missing action-json}"

LABEL=$(echo "$ACTION_JSON" | jq -r '.label // ""')

if [ -z "$LABEL" ]; then
  echo "ERROR: remove_label action requires label field" >&2
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "ERROR: 'gh' CLI not found — required for label operations" >&2
  exit 1
fi

gh issue edit "$ISSUE_NUMBER" --remove-label "$LABEL" 2>&1
echo "Removed label '$LABEL' from issue #$ISSUE_NUMBER"
