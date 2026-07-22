#!/usr/bin/env bash
# add-label.sh — Add a label to a GitHub issue.
#
# Usage: add-label.sh <issue-number> <action-json>
#
# @see Issue #137 — Workflow Automation Triggers
# @see docs/AUTOMATIONS.md — add_label action type
set -euo pipefail

ISSUE_NUMBER="${1:?Missing issue-number}"
ACTION_JSON="${2:?Missing action-json}"

LABEL=$(echo "$ACTION_JSON" | jq -r '.label // ""')

if [ -z "$LABEL" ]; then
  echo "ERROR: add_label action requires label field" >&2
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "ERROR: 'gh' CLI not found — required for label operations" >&2
  exit 1
fi

gh issue edit "$ISSUE_NUMBER" --add-label "$LABEL" 2>&1
echo "Added label '$LABEL' to issue #$ISSUE_NUMBER"
