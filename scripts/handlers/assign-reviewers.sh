#!/usr/bin/env bash
# assign-reviewers.sh — Request reviewers on the PR associated with an issue.
#
# Usage: assign-reviewers.sh <issue-number> <action-json>
#
# Finds the open PR for the issue by branch name pattern (feat/<N>-*, fix/<N>-*)
# and requests reviews from the specified users or teams.
#
# @see Issue #137 — Workflow Automation Triggers
# @see docs/AUTOMATIONS.md — assign_reviewers action type
set -euo pipefail

ISSUE_NUMBER="${1:?Missing issue-number}"
ACTION_JSON="${2:?Missing action-json}"

REVIEWERS_JSON=$(echo "$ACTION_JSON" | jq -r '.reviewers // []')
REVIEWER_COUNT=$(echo "$REVIEWERS_JSON" | jq 'length')

if [ "$REVIEWER_COUNT" -eq 0 ]; then
  echo "ERROR: assign_reviewers action requires non-empty reviewers array" >&2
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "ERROR: 'gh' CLI not found — required for reviewer assignment" >&2
  exit 1
fi

# Find PR for this issue by branch pattern, with exact issue number matching
# Use headRefName to verify the branch matches exactly (avoid 12 matching 120, 1200, etc.)
PR_NUMBER=""
for prefix in feat fix docs; do
  PR_JSON=$(gh pr list --search "head:${prefix}/${ISSUE_NUMBER}-" --json number,headRefName -q ".[] | select(.headRefName | test(\"^${prefix}/${ISSUE_NUMBER}-\")) | .number" 2>/dev/null | head -1 || echo "")
  if [ -n "$PR_JSON" ]; then
    PR_NUMBER="$PR_JSON"
    break
  fi
done
if [ -z "$PR_NUMBER" ]; then
  echo "ERROR: No open PR found for issue #$ISSUE_NUMBER" >&2
  exit 1
fi

# Separate users and teams with name validation
USERS=()
TEAMS=()
while IFS= read -r REVIEWER; do
  [ -z "$REVIEWER" ] && continue
  if [[ "$REVIEWER" == @team/* ]]; then
    TEAM_NAME="${REVIEWER#@team/}"
    # Validate team name: org/team format (alphanumeric, hyphens, underscores)
    if [[ ! "$TEAM_NAME" =~ ^[a-zA-Z0-9_-]+(/[a-zA-Z0-9_-]+)?$ ]]; then
      echo "WARNING: Skipping invalid team name: $REVIEWER" >&2
      continue
    fi
    TEAMS+=("$TEAM_NAME")
  else
    # Validate GitHub username: alphanumeric and hyphens, 1-39 chars
    if [[ ! "$REVIEWER" =~ ^[a-zA-Z0-9]([a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$ ]]; then
      echo "WARNING: Skipping invalid reviewer username: $REVIEWER" >&2
      continue
    fi
    USERS+=("$REVIEWER")
  fi
done < <(echo "$REVIEWERS_JSON" | jq -r '.[]' 2>/dev/null)

# Request reviewers via gh CLI
ARGS=()
for user in "${USERS[@]+"${USERS[@]}"}"; do
  ARGS+=("--reviewer" "$user")
done
for team in "${TEAMS[@]+"${TEAMS[@]}"}"; do
  ARGS+=("--reviewer" "$team")
done

if [ ${#ARGS[@]} -gt 0 ]; then
  gh pr edit "$PR_NUMBER" "${ARGS[@]}" 2>&1
  echo "Requested reviewers on PR #$PR_NUMBER: $(echo "$REVIEWERS_JSON" | jq -r 'join(", ")')"
else
  echo "No reviewers to assign"
fi
