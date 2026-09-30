#!/usr/bin/env bash
# Resolve a release version to the VS Code Marketplace/Open VSX channel.
#
# Every version publishes on the release channel: Nightgauge runs no preview
# channel (owner decision, 2026-09-30, #2305). Until then odd 0.x minor lines
# resolved to "pre-release", which published v0.5.0 to Open VSX as a preview
# while normal users kept receiving 0.4.8. A version once uploaded as
# pre-release cannot be re-uploaded as release, so 0.5.0 stays a preview and
# 0.5.1 supersedes it.
#
# The resolver stays the single place the channel is decided: staging, GitHub
# Release packaging, Marketplace publishing and Open VSX publishing all ask it.
set -euo pipefail

VERSION="${1:-}"
if [[ ! "$VERSION" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)(-rc\.[1-9][0-9]*)?$ ]]; then
  echo "ERROR: expected version X.Y.Z or X.Y.Z-rc.N, got '${VERSION:-<empty>}'" >&2
  exit 2
fi

echo "release"
