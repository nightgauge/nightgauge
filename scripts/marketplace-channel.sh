#!/usr/bin/env bash
# Resolve a release version to the VS Code Marketplace/Open VSX channel.
#
# Marketplace versions do not support SemVer pre-release suffixes and a version
# uploaded as pre-release cannot later be uploaded as stable. Microsoft therefore
# recommends separate numeric lines: even 0.x minors for stable releases and odd
# 0.x minors for previews. Once Nightgauge reaches 1.x, releases are stable.
set -euo pipefail

VERSION="${1:-}"
if [[ ! "$VERSION" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "ERROR: expected release version X.Y.Z, got '${VERSION:-<empty>}'" >&2
  exit 2
fi

MAJOR="${BASH_REMATCH[1]}"
MINOR="${BASH_REMATCH[2]}"

if [[ "$MAJOR" == "0" && $((10#$MINOR % 2)) -eq 1 ]]; then
  echo "pre-release"
else
  echo "release"
fi
