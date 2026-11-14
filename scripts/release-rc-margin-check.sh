#!/usr/bin/env bash
#
# release-rc-margin-check.sh — refuse a release tag unless a release candidate
# on the same commit passed its margin scan (#3192).
#
#   release-rc-margin-check.sh vX.Y.Z COMMIT_SHA OWNER/REPO
#
# release.yml runs this before it builds anything. It passes when at least one
# RC tag vX.Y.Z-rc.N points at COMMIT_SHA and that tag's latest DISPATCHED
# staging.yml run (gh workflow run staging.yml --ref vX.Y.Z-rc.N; an RC tag
# push no longer scans, #3213)
#
#   - has a successful `VirusTotal scan of the RC` job (rc-scan), and
#   - kept an unexpired `rc-margin-scan-vX.Y.Z-rc.N` artifact whose
#     margin-manifest.txt names X.Y.Z: the margin scan covered the very version
#     string this release ships.
#
# rc-scan uploads that artifact only after a clean scan of the RC's files and
# its margin builds (scripts/release-margin-builds.sh), and keeps it 90 days.
# An expired artifact fails the release: cut a new RC on the commit. There is
# no override. See docs/RELEASE_BINARIES.md.
#
# Needs `gh` with a token that can read the repository's tags, workflow runs
# and artifacts (`actions: read`), and `jq`.
#
# Exit:
#   0  an RC on the commit passed a margin scan covering X.Y.Z
#   1  no RC qualifies; one ::error:: line per RC says why
#   2  HARNESS ERROR: GitHub could not be asked, so nothing was decided
set -uo pipefail

SCAN_JOB="VirusTotal scan of the RC"
harness() { echo "HARNESS ERROR: release-rc-margin-check: $*" >&2; exit 2; }

TAG="${1:-}"
SHA="${2:-}"
REPO="${3:-}"
[ $# -eq 3 ] || harness "usage: release-rc-margin-check.sh vX.Y.Z COMMIT_SHA OWNER/REPO"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || harness "'$TAG' is not a release tag vX.Y.Z"
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || harness "'$SHA' is not a full commit sha"
[[ "$REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || harness "'$REPO' is not OWNER/REPO"
command -v gh >/dev/null 2>&1 || harness "gh is not on PATH"
command -v jq >/dev/null 2>&1 || harness "jq is not on PATH"
VERSION="${TAG#v}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# api PATH: the JSON of a GitHub API GET, or a HARNESS ERROR.
api() {
  local out
  out="$(gh api "$1" 2>"$WORK/api.err")" || harness "gh api $1 failed: $(cat "$WORK/api.err")"
  printf '%s\n' "$out"
}

# The commit a tag ref resolves to; an annotated tag is peeled once.
peel() {
  local type="$1" sha="$2"
  if [ "$type" = "tag" ]; then
    api "repos/$REPO/git/tags/$sha" | jq -r '.object.sha'
  else
    echo "$sha"
  fi
}

# A harness() inside $(...) exits only the subshell, so every substitution
# below passes its status on with `|| exit 2`.
REFS="$(api "repos/$REPO/git/matching-refs/tags/$TAG-rc.")" || exit 2
LIST="$(printf '%s\n' "$REFS" | jq -r '.[] | [.ref, .object.type, .object.sha] | @tsv')" ||
  harness "cannot parse the tag list"
CANDIDATES=()
while IFS=$'\t' read -r REF TYPE OBJ; do
  [ -n "$REF" ] || continue
  RC="${REF#refs/tags/}"
  [[ "${RC#"$TAG"-rc.}" =~ ^[0-9]+$ ]] && [ "$RC" != "${RC#"$TAG"-rc.}" ] || continue
  COMMIT="$(peel "$TYPE" "$OBJ")" || exit 2
  [ "$COMMIT" = "$SHA" ] && CANDIDATES+=("$RC")
done <<<"$LIST"

if [ "${#CANDIDATES[@]}" -eq 0 ]; then
  echo "::error::$TAG: no RC tag $TAG-rc.N points at $SHA. A release is promoted from an RC on the same commit whose rc-scan passed the margin scan; cut one (docs/RELEASE_CHECKLIST.md)."
  exit 1
fi

RUNS="$(api "repos/$REPO/actions/workflows/staging.yml/runs?event=workflow_dispatch&head_sha=$SHA&per_page=100")" ||
  exit 2
for RC in "${CANDIDATES[@]}"; do
  RUN_ID="$(printf '%s\n' "$RUNS" | jq -r --arg rc "$RC" \
    '[.workflow_runs[] | select(.head_branch == $rc)] | sort_by(.run_number) | last | .id // empty')" ||
    harness "cannot parse the staging runs"
  if [ -z "$RUN_ID" ]; then
    echo "::error::$RC: no dispatched staging.yml scan run for this tag on $SHA; dispatch it: gh workflow run staging.yml --ref $RC (docs/RELEASE_CHECKLIST.md)."
    continue
  fi
  CONCLUSION="$(api "repos/$REPO/actions/runs/$RUN_ID/jobs?per_page=100" |
    jq -r --arg job "$SCAN_JOB" '[.jobs[] | select(.name == $job)] | last | .conclusion // "absent"')" ||
    harness "cannot parse the jobs of run $RUN_ID"
  if [ "$CONCLUSION" != "success" ]; then
    echo "::error::$RC: rc-scan ('$SCAN_JOB') in staging run $RUN_ID is '$CONCLUSION', not success."
    continue
  fi
  ARTIFACT="rc-margin-scan-$RC"
  EXPIRED="$(api "repos/$REPO/actions/runs/$RUN_ID/artifacts?per_page=100" |
    jq -r --arg name "$ARTIFACT" '[.artifacts[] | select(.name == $name)] | last | if . == null then "absent" else (.expired | tostring) end')" ||
    harness "cannot parse the artifacts of run $RUN_ID"
  if [ "$EXPIRED" != "false" ]; then
    echo "::error::$RC: artifact $ARTIFACT in staging run $RUN_ID is ${EXPIRED/true/expired}; the margin scan cannot be shown. Cut a new RC on this commit."
    continue
  fi
  DEST="$WORK/$RC"
  gh run download "$RUN_ID" -R "$REPO" -n "$ARTIFACT" -D "$DEST" 2>"$WORK/dl.err" ||
    harness "gh run download $RUN_ID -n $ARTIFACT failed: $(cat "$WORK/dl.err")"
  MANIFEST="$DEST/margin-manifest.txt"
  if [ ! -f "$MANIFEST" ]; then
    echo "::error::$RC: $ARTIFACT holds no margin-manifest.txt."
    continue
  fi
  if ! awk -v v="$VERSION" '$1 == v { found = 1 } END { exit !found }' "$MANIFEST"; then
    echo "::error::$RC: the margin scan did not cover $VERSION (margin-manifest.txt names: $(awk '{print $1}' "$MANIFEST" | sort -u | tr '\n' ' '))."
    continue
  fi
  echo "OK: $RC on $SHA passed rc-scan in staging run $RUN_ID, and its margin scan covered $VERSION."
  exit 0
done

echo "::error::$TAG: no RC on $SHA passed a margin scan covering $VERSION (checked: ${CANDIDATES[*]}). See docs/RELEASE_BINARIES.md."
exit 1
