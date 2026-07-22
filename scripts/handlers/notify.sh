#!/usr/bin/env bash
# notify.sh — Post a GitHub comment mentioning users.
#
# Usage: notify.sh <issue-number> <action-json>
#
# Creates a comment on the issue mentioning the specified users.
#
# @see Issue #137 — Workflow Automation Triggers
# @see docs/AUTOMATIONS.md — notify action type
set -euo pipefail

ISSUE_NUMBER="${1:?Missing issue-number}"
ACTION_JSON="${2:?Missing action-json}"

USERS_JSON=$(echo "$ACTION_JSON" | jq -r '.users // []')
MESSAGE=$(echo "$ACTION_JSON" | jq -r '.message // ""')
USER_COUNT=$(echo "$USERS_JSON" | jq 'length')

if [ "$USER_COUNT" -eq 0 ]; then
  echo "ERROR: notify action requires non-empty users array" >&2
  exit 1
fi

if [ -z "$MESSAGE" ]; then
  echo "ERROR: notify action requires message field" >&2
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "ERROR: 'gh' CLI not found — required for notifications" >&2
  exit 1
fi

# Build mention list with username validation
MENTIONS=""
while IFS= read -r USER; do
  # Strip @ prefix if present for validation
  CLEAN_USER="${USER#@}"
  # Validate GitHub username format: alphanumeric and hyphens, 1-39 chars
  if [[ ! "$CLEAN_USER" =~ ^[a-zA-Z0-9]([a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$ ]]; then
    echo "WARNING: Skipping invalid username: $USER" >&2
    continue
  fi
  MENTIONS="${MENTIONS} @${CLEAN_USER}"
done < <(echo "$USERS_JSON" | jq -r '.[]' 2>/dev/null)

if [ -z "$MENTIONS" ]; then
  echo "ERROR: No valid usernames to notify" >&2
  exit 1
fi

# Post comment
COMMENT="**Automation:** ${MESSAGE}

cc${MENTIONS}"

gh issue comment "$ISSUE_NUMBER" --body "$COMMENT" 2>&1
echo "Notified${MENTIONS} on issue #$ISSUE_NUMBER"
