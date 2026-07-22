#!/usr/bin/env bash
# run-script.sh — Execute a custom script from the repository.
#
# Usage: run-script.sh <issue-number> <action-json>
#
# Security:
#   - Script path must be relative (no absolute paths)
#   - Path traversal (..) is rejected
#   - Script must exist within the repository
#   - Script must be executable
#
# @see Issue #137 — Workflow Automation Triggers
# @see docs/AUTOMATIONS.md — run_script action type
set -euo pipefail

ISSUE_NUMBER="${1:?Missing issue-number}"
ACTION_JSON="${2:?Missing action-json}"

SCRIPT_PATH=$(echo "$ACTION_JSON" | jq -r '.script // ""')
ARGS_JSON=$(echo "$ACTION_JSON" | jq -r '.args // []')

if [ -z "$SCRIPT_PATH" ]; then
  echo "ERROR: run_script action requires script field" >&2
  exit 1
fi

# Security: reject absolute paths
if [[ "$SCRIPT_PATH" == /* ]]; then
  echo "ERROR: Absolute paths not allowed — use relative path from repo root" >&2
  exit 1
fi

# Security: reject path traversal (match .. as a path component)
if [[ "$SCRIPT_PATH" =~ (^|/)\.\.(/|$) ]]; then
  echo "ERROR: Path traversal (..) not allowed in script path" >&2
  exit 1
fi

# Resolve from repo root — fail hard if not in a git repo
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "ERROR: Not in a git repository — cannot resolve script path" >&2
  exit 1
}

FULL_PATH="${REPO_ROOT}/${SCRIPT_PATH}"

# Security: verify canonical path is within the repo root
CANONICAL_PATH="$(cd "$(dirname "$FULL_PATH")" 2>/dev/null && pwd)/$(basename "$FULL_PATH")" || {
  echo "ERROR: Script path does not resolve to a valid directory: $SCRIPT_PATH" >&2
  exit 1
}
case "$CANONICAL_PATH" in
  "${REPO_ROOT}/"*) ;; # OK — within repo
  *) echo "ERROR: Script resolves outside repository: $SCRIPT_PATH" >&2; exit 1 ;;
esac

if [ ! -f "$FULL_PATH" ]; then
  echo "ERROR: Script not found: $SCRIPT_PATH" >&2
  exit 1
fi

if [ ! -x "$FULL_PATH" ]; then
  echo "ERROR: Script is not executable: $SCRIPT_PATH (run chmod +x)" >&2
  exit 1
fi

# Build args array (single jq invocation)
ARGS=()
while IFS= read -r arg; do
  [ -n "$arg" ] && ARGS+=("$arg")
done < <(echo "$ARGS_JSON" | jq -r '.[]' 2>/dev/null)

# Execute with issue context in environment (respects script's shebang)
export AUTOMATION_ISSUE_NUMBER="$ISSUE_NUMBER"
"$FULL_PATH" "${ARGS[@]+"${ARGS[@]}"}" 2>&1
echo "Script executed: $SCRIPT_PATH"
