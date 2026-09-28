#!/usr/bin/env bash
#
# The publication-boundary allowlist changes alone (#1970).
#
# .github/publication-boundary.yaml is the allowlist of the fail-closed
# publication-boundary check. Twice the pipeline added an exception to it in
# the same change as the content the exception let through, with a rationale
# it wrote itself. Reviewed together, the content, the exception and the
# argument for it come from one author, so the review is not independent.
#
# This check fails a pull request that changes the allowlist AND anything
# else. An allowlist change is its own pull request, so approving it is a
# deliberate act rather than a hunk scrolled past in a content diff. The
# pipeline cannot author one at all: its post-stage protected-path check
# (internal/orchestrator/gates/protected_paths.go) fails any stage that
# touches the file.
#
# Fails closed: a changed-file list that cannot be computed is a failure.
#
# Usage: scripts/check-boundary-allowlist-isolation.sh <base-sha> [<head-sha>]

set -euo pipefail

MANIFEST=".github/publication-boundary.yaml"
base="${1:?usage: check-boundary-allowlist-isolation.sh <base-sha> [<head-sha>]}"
head="${2:-HEAD}"

if ! changed="$(git diff --name-only --no-renames "${base}...${head}")"; then
  echo "FAIL: could not list the files changed between ${base} and ${head}" >&2
  exit 1
fi

if ! grep -qxF "$MANIFEST" <<<"$changed"; then
  echo "OK: ${MANIFEST} unchanged"
  exit 0
fi

others="$(grep -vxF "$MANIFEST" <<<"$changed" || true)"
if [[ -n "$others" ]]; then
  echo "FAIL: ${MANIFEST} changed together with other files (#1970)." >&2
  echo "An allowlist change must be its own pull request, reviewed apart from" >&2
  echo "the content it lets through. Move these files to a separate PR:" >&2
  while IFS= read -r f; do printf '  %s\n' "$f"; done <<<"$others" >&2
  exit 1
fi

echo "OK: ${MANIFEST} is the only file changed"
