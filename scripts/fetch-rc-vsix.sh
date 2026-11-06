#!/usr/bin/env bash
#
# fetch-rc-vsix.sh — fetch a release candidate's linux-x64 VSIX from the
# successful staging run for its tag (#2722), so the clean-install gate (G1)
# tests the artifact users receive rather than a VSIX packaged from the tree.
#
# Usage: bash scripts/fetch-rc-vsix.sh <tag> <dest-dir>
#
# Prints, for $GITHUB_OUTPUT:
#   vsix=<path>      the single *linux-x64*.vsix in rc-artifacts-<tag>
#   run_id=<id>      the staging run it came from
#   sha256=<hex>     the VSIX digest
#
# There is no fallback: an invalid tag, no successful staging run, an expired
# or absent artifact, or not exactly one linux-x64 VSIX exits 1 with an error
# naming the tag. Needs `gh` with actions:read on the repository (GH_REPO is
# honoured).
set -euo pipefail

TAG="${1:-}"
DEST="${2:-}"
fail() { echo "ERROR: no rc-artifacts-$TAG — $1" >&2; exit 1; }

[ -n "$DEST" ] || { echo "usage: fetch-rc-vsix.sh <tag> <dest-dir>" >&2; exit 2; }
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]] ||
  fail "'$TAG' is not an RC tag (vX.Y.Z-rc.N)"
command -v gh >/dev/null || fail "gh is required"

RUN_ID="$(gh run list --workflow staging.yml --branch "$TAG" --status success \
  --limit 1 --json databaseId --jq '.[0].databaseId // empty')" ||
  fail "cannot list staging runs for $TAG"
[ -n "$RUN_ID" ] || fail "no successful staging.yml run for $TAG"

mkdir -p "$DEST"
gh run download "$RUN_ID" --name "rc-artifacts-$TAG" --dir "$DEST" >&2 ||
  fail "download from staging run $RUN_ID failed (expired or absent artifact)"

VSIXES=()
while IFS= read -r -d '' f; do VSIXES+=("$f"); done \
  < <(find "$DEST" -type f -name '*linux-x64*.vsix' -print0)
[ "${#VSIXES[@]}" -eq 1 ] ||
  fail "staging run $RUN_ID carries ${#VSIXES[@]} linux-x64 VSIXs, expected exactly 1"

VSIX="${VSIXES[0]}"
if command -v sha256sum >/dev/null; then
  SHA="$(sha256sum "$VSIX" | cut -d' ' -f1)"
else
  SHA="$(shasum -a 256 "$VSIX" | cut -d' ' -f1)"
fi
echo "vsix=$VSIX"
echo "run_id=$RUN_ID"
echo "sha256=$SHA"
