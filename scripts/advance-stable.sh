#!/usr/bin/env bash
#
# advance-stable.sh OWNER/REPO TAG — move the release-owned `stable` branch to
# TAG's commit, fast-forward only, creating it if absent (#2724, #2763). A
# refusal names its cause: the step must never read as "the branch is missing".
#
# Exit codes: 0 moved; 1 refused (cause on stderr); 2 usage error.
set -uo pipefail

[ $# -eq 2 ] || { echo "usage: advance-stable.sh OWNER/REPO TAG" >&2; exit 2; }
REPO="$1"
TAG="$2"
SHA="$(git rev-parse --verify -q "${TAG}^{commit}")" ||
  { echo "advance-stable: cannot resolve $TAG" >&2; exit 2; }

ERR="$(mktemp)" || { echo "advance-stable: mktemp failed" >&2; exit 2; }
trap 'rm -f "$ERR"' EXIT

if gh api "repos/$REPO/git/ref/heads/stable" >/dev/null 2>"$ERR"; then
  ACTION="update"
  gh api -X PATCH "repos/$REPO/git/refs/heads/stable" \
    -f sha="$SHA" -F force=false >/dev/null 2>"$ERR"
  RC=$?
elif grep -q 'HTTP 404' "$ERR"; then
  ACTION="create"
  gh api "repos/$REPO/git/refs" -f ref=refs/heads/stable -f sha="$SHA" >/dev/null 2>"$ERR"
  RC=$?
else
  echo "advance-stable: cannot read stable: $(cat "$ERR")" >&2
  exit 1
fi

if [ "$RC" -eq 0 ]; then
  echo "stable is now $TAG ($SHA)"
  [ -z "${GITHUB_STEP_SUMMARY:-}" ] ||
    echo "stable is now $TAG ($SHA)" >>"$GITHUB_STEP_SUMMARY"
  exit 0
fi

# GitHub answers 404 when a ref move would change .github/workflows and the
# token lacks workflow permission, although the ref exists.
if grep -qiE 'HTTP 404|workflow' "$ERR"; then
  echo "advance-stable: the token cannot $ACTION a ref across .github/workflows changes (needs workflow permission; see STABLE_BRANCH_TOKEN in docs/GIT_WORKFLOW.md): $(cat "$ERR")" >&2
else
  echo "advance-stable: $ACTION of stable to $SHA failed: $(cat "$ERR")" >&2
fi
exit 1
