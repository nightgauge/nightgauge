#!/usr/bin/env bash
# Verify an extension registry token without publishing anything (#3062).
#
#   VSCE_PAT=... bash scripts/verify-registry-pat.sh marketplace
#   OVSX_PAT=... bash scripts/verify-registry-pat.sh open-vsx
#
# One check, two callers: release.yml's preflight runs it before the draft
# release exists, and marketplace-publish.yml runs it before it touches the
# release's VSIXs. Keeping the pinned tool versions and the retry policy here
# means the two checks cannot drift apart.
#
# `verify-pat` is non-mutating. The token travels in the environment only: it
# is never echoed and never passed on the command line.
#
# Exit codes: 0 verified; 1 token missing or rejected; 2 usage error.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

case "${1:-}" in
  marketplace)
    TOKEN_NAME=VSCE_PAT
    TOKEN="${VSCE_PAT:-}"
    VERIFY=(npx --yes @vscode/vsce@3.9.2 verify-pat)
    ;;
  open-vsx)
    TOKEN_NAME=OVSX_PAT
    TOKEN="${OVSX_PAT:-}"
    VERIFY=(npx --yes ovsx@1.2.0 verify-pat)
    ;;
  *)
    echo "usage: $(basename "$0") <marketplace|open-vsx>" >&2
    exit 2
    ;;
esac

[[ -n "$TOKEN" ]] || { echo "::error::$TOKEN_NAME secret is not set"; exit 1; }
unset TOKEN

# The Marketplace publisher and the Open VSX namespace are the same identifier.
PUBLISHER=$(node -p "require('$ROOT/packages/nightgauge/package.json').publisher")
[[ -n "$PUBLISHER" ]] || { echo "::error::no publisher in packages/nightgauge/package.json"; exit 1; }

# Azure DevOps intermittently times out /_apis/securityroles (seen on the
# v0.4.0 publish, #1599). A transient timeout is not a bad token, so retry
# before failing. Open VSX gets the same policy.
for attempt in 1 2 3 4 5; do
  if "${VERIFY[@]}" "$PUBLISHER"; then
    echo "$TOKEN_NAME verified against '$PUBLISHER'."
    exit 0
  fi
  if [ "$attempt" -lt 5 ]; then
    echo "::warning::$TOKEN_NAME verify-pat attempt $attempt failed; retrying in 15s"
    sleep 15
  fi
done
echo "::error::$TOKEN_NAME verification failed after 5 attempts"
exit 1
