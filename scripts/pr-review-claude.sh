#!/usr/bin/env bash
# The one definition of pr-review's model confinement (#3084).
#
# Callers: the review step in .github/workflows/pr-review.yml and the probe in
# .github/workflows/pr-review-confinement.yml. Both must call this script and
# never `claude` directly, so the probe tests exactly what the review runs.
# Callers add only --max-turns, and the probe its output format (the last
# --output-format wins); stdin (the prompt) passes through.
#
# Never --allowedTools: a bare Read or Write there re-grants the whole
# filesystem. --restricted confines the file tools to the checkout; the
# committed settings allow only the review inputs and deny /proc, /sys, /dev,
# ~ and .git; dontAsk denies everything else.
set -euo pipefail

exec claude -p \
  --no-session-persistence \
  --restricted \
  --strict-mcp-config \
  --tools "Read,Glob,Grep,Write" \
  --settings .github/pr-review/claude-settings.json \
  --permission-mode dontAsk \
  --output-format json \
  "$@"
