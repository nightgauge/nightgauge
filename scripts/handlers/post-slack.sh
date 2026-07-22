#!/usr/bin/env bash
# post-slack.sh — Post a message to a Slack incoming webhook.
#
# Usage: post-slack.sh <issue-number> <action-json>
#
# Reads webhook_env, channel, and message from the action JSON.
# The webhook URL is resolved from the environment variable named by webhook_env.
#
# @see Issue #137 — Workflow Automation Triggers
# @see docs/AUTOMATIONS.md — post_slack action type
set -euo pipefail

ISSUE_NUMBER="${1:?Missing issue-number}"
ACTION_JSON="${2:?Missing action-json}"

WEBHOOK_ENV=$(echo "$ACTION_JSON" | jq -r '.webhook_env // ""')
CHANNEL=$(echo "$ACTION_JSON" | jq -r '.channel // ""')
MESSAGE=$(echo "$ACTION_JSON" | jq -r '.message // ""')

if [ -z "$WEBHOOK_ENV" ]; then
  echo "ERROR: post_slack action requires webhook_env field" >&2
  exit 1
fi

# Validate env var name (alphanumeric + underscore only, must start with letter)
if [[ ! "$WEBHOOK_ENV" =~ ^[A-Z_][A-Z0-9_]*$ ]]; then
  echo "ERROR: webhook_env must be a valid environment variable name (uppercase + underscores)" >&2
  exit 1
fi

WEBHOOK_URL="${!WEBHOOK_ENV:-}"
if [ -z "$WEBHOOK_URL" ]; then
  echo "ERROR: Environment variable '$WEBHOOK_ENV' is not set or empty" >&2
  exit 1
fi

# Validate URL pattern (basic check — must start with https://)
if [[ ! "$WEBHOOK_URL" =~ ^https:// ]]; then
  echo "ERROR: Webhook URL must use HTTPS" >&2
  exit 1
fi

if [ -z "$MESSAGE" ]; then
  echo "ERROR: post_slack action requires message field" >&2
  exit 1
fi

# Build Slack payload
PAYLOAD=$(jq -n --arg text "$MESSAGE" '{text: $text}')
if [ -n "$CHANNEL" ]; then
  PAYLOAD=$(echo "$PAYLOAD" | jq --arg channel "$CHANNEL" '. + {channel: $channel}')
fi

# POST to Slack webhook
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  -X POST \
  -H "Content-Type: application/json" \
  -d "$PAYLOAD" \
  --max-time 10 \
  "$WEBHOOK_URL")

if [ "$HTTP_CODE" -ge 200 ] && [ "$HTTP_CODE" -lt 300 ]; then
  echo "Posted to Slack (HTTP $HTTP_CODE)"
else
  echo "ERROR: Slack webhook returned HTTP $HTTP_CODE" >&2
  exit 1
fi
