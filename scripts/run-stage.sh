#!/usr/bin/env bash
# run-stage.sh — SDK-based stage runner for non-Claude adapters.
# Usage: run-stage.sh <adapter> <stage> <issue-number>
# Invoked by skillRunner.ts for non-Claude execution paths.
#
# @see Issue #2057 - Route pipeline stage execution through LM Studio
# @see Issue #1947 - Add Copilot adapter routing
set -euo pipefail

ADAPTER="${1:?Adapter name required (e.g. lm-studio, codex, gemini-sdk, copilot)}"
STAGE="${2:?Stage name required (e.g. feature-planning)}"
ISSUE_NUMBER="${3:?Issue number required}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Honor the adapter arg as the source of truth. resolveAdapter() in the SDK
# reads NIGHTGAUGE_ADAPTER, so without this a direct invocation
# (`run-stage.sh codex ...`) would silently fall back to claude-headless — the
# adapter arg was effectively ignored. skillRunner.ts already sets the same env
# before spawning, so this is consistent there and fixes standalone/documented
# invocation. Default the output format to JSON unless the caller overrides it.
export NIGHTGAUGE_ADAPTER="${ADAPTER}"
export NIGHTGAUGE_OUTPUT_FORMAT="${NIGHTGAUGE_OUTPUT_FORMAT:-json}"

# Resolve SDK CLI: prefer installed npm binary, fall back to built dist
SDK_CLI="${REPO_ROOT}/node_modules/.bin/nightgauge-sdk"
if [ ! -f "${SDK_CLI}" ]; then
  SDK_CLI="${REPO_ROOT}/packages/nightgauge-sdk/dist/cli/index.js"
fi

if [ ! -f "${SDK_CLI}" ]; then
  echo "Error: SDK CLI not found at ${SDK_CLI}" >&2
  echo "Run: npm run -w @nightgauge/sdk build" >&2
  exit 1
fi

# Adapter-specific pre-flight checks and environment setup.
# NIGHTGAUGE_ADAPTER is set by skillRunner.ts; resolveAdapter() in the SDK
# reads it. This case block handles auth forwarding and CLI availability checks
# for adapters that require them before the SDK takes over.
case "${ADAPTER}" in
  copilot)
    # GitHub Copilot CLI adapter (@see Issue #1941, #1947)
    # Auth: prefer GH_TOKEN, fall back to GITHUB_TOKEN, then COPILOT_GITHUB_TOKEN.
    # Forward whichever token is present so the `copilot` CLI can authenticate.
    if [ -n "${GH_TOKEN:-}" ]; then
      export GH_TOKEN="${GH_TOKEN}"
    elif [ -n "${GITHUB_TOKEN:-}" ]; then
      # Normalize to GH_TOKEN which the copilot CLI prefers
      export GH_TOKEN="${GITHUB_TOKEN}"
    elif [ -z "${COPILOT_GITHUB_TOKEN:-}" ]; then
      echo "Warning: No GitHub auth token found (GH_TOKEN, GITHUB_TOKEN, or COPILOT_GITHUB_TOKEN)." >&2
      echo "Copilot CLI will attempt interactive auth. Set GH_TOKEN for unattended execution." >&2
    fi

    # Verify the copilot CLI binary is available
    if ! command -v copilot >/dev/null 2>&1; then
      echo "Error: 'copilot' CLI not found in PATH." >&2
      echo "Install via: npm install -g @github/copilot" >&2
      exit 1
    fi
    ;;
esac

# NIGHTGAUGE_ADAPTER is already set by skillRunner.ts before this script
# is spawned; resolveAdapter() in the SDK will pick it up.
exec node "${SDK_CLI}" stage "${STAGE}" "${ISSUE_NUMBER}"
