#!/usr/bin/env bash
# scripts/lint-skills/no-direct-gh.sh — fail when a skill file a stage
# EXECUTES contains a direct `gh ` call. Skills target the
# `nightgauge forge` abstraction (ADR-008); a direct call bypasses the
# cross-forge boundary, and — the reason this gate's scope was widened —
# it is invisible to the API ledger, so the quota it spends can never be
# attributed afterwards.
#
# This script is now a thin wrapper around `nightgauge preflight
# skill-no-direct-gh`, which is the single implementation.
#
# It used to be a second, independent implementation in grep/rg, and the
# two drifted exactly as duplicated gates do: both globbed
# `skills/*/SKILL.md`, so `_includes/` and `_shared/` — where every
# expensive call actually lived, including a whole-board pull per tier in
# _shared/AUTO_SELECTION.md — were never scanned by either. Fixing the
# scope in one place and not the other would have re-created the gap in a
# new shape. One implementation, two entry points.
#
# Scope, allowlist semantics and the fenced-code-only rule are documented
# at internal/preflight/skill_no_direct_gh.go.
#
# Exit codes:
#   0  no direct gh calls in any non-allowlisted executed skill file
#   1  one or more findings (gate fails)
#   2  the check could not be run (binary not found)

set -euo pipefail

ROOT="${1:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"

BINARY="${NIGHTGAUGE_BIN:-}"
if [ -n "$BINARY" ] && [ ! -x "$BINARY" ]; then BINARY=""; fi
if [ -z "$BINARY" ]; then BINARY="$(command -v nightgauge 2>/dev/null || echo "")"; fi
if [ -z "$BINARY" ] && [ -x "$ROOT/bin/nightgauge" ]; then BINARY="$ROOT/bin/nightgauge"; fi

if [ -z "$BINARY" ]; then
  echo "no-direct-gh: cannot run — no nightgauge binary found." >&2
  echo "  Build one with 'make build' or set NIGHTGAUGE_BIN." >&2
  echo "  Refusing to fall back to a second grep implementation: the last one" >&2
  echo "  drifted from the Go gate and left _includes/ and _shared/ unscanned." >&2
  exit 2
fi

exec "$BINARY" preflight skill-no-direct-gh --root "$ROOT"
