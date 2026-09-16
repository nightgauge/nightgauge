#!/usr/bin/env bash
# Publish every per-target VSIX to one registry, idempotently and with retries.
#
# Usage:
#   publish-vsix-set.sh open-vsx   <version> [extra ovsx args...]
#   publish-vsix-set.sh marketplace <version> [extra vsce args...]
#
# Run from the directory holding the *.vsix files.
#
# ## Why this is not a bare `for … do publish … done`
#
# The v0.4.2 Open VSX publish aborted midway: darwin-arm64 reported success,
# darwin-x64 got `503 Service Unavailable`, and linux-x64 was never attempted
# because `set -e` killed the loop. That leaves a release advertising a version
# only some platforms can install, and re-running could not repair it, because
# a target that did land answers with a conflict and aborts the loop again at
# the first VSIX.
#
# So the contract here is:
#
#   - **Transient failures retry.** A 5xx, a timeout or a reset is the registry
#     having a bad minute, not a bad artifact. Same posture the verify-pat steps
#     in this workflow already take.
#   - **An already-published target counts as success.** That is what makes a
#     re-run complete a partial release instead of tripping over its own
#     earlier progress. Publishing is the goal; having-published is the goal
#     already met.
#   - **Every target is attempted before the step fails.** One permanently bad
#     target must not hide the status of the others, and a partial release is
#     worth knowing about precisely.
#   - **The step still fails if any target never landed.** Idempotence must not
#     become silence: exit non-zero with the list.

set -uo pipefail

REGISTRY="${1:?registry required: open-vsx|marketplace}"
VERSION="${2:?version required}"
shift 2
EXTRA=("$@")

# Overridable so the self-test can exercise the retry path without sleeping
# through a real backoff. Production never sets these.
MAX_ATTEMPTS="${PUBLISH_MAX_ATTEMPTS:-5}"
BACKOFF_SECONDS="${PUBLISH_BACKOFF_SECONDS:-20}"

# Registry responses that mean "this exact version+target is already there".
# Matched case-insensitively against combined stdout+stderr.
ALREADY_PUBLISHED_RE='already (exists|published)|is already|conflict|409|same version|version already'

# Responses worth retrying: the registry is unavailable, not refusing us.
TRANSIENT_RE='50[0234]|service unavailable|bad gateway|gateway time-?out|timed? ?out|ETIMEDOUT|ECONNRESET|EAI_AGAIN|socket hang up|network|temporarily'

publish_one() {
  local vsix="$1" out rc
  local attempt=1
  while :; do
    if [[ "$REGISTRY" == "open-vsx" ]]; then
      out="$(npx --yes ovsx@1.2.0 publish "$vsix" ${EXTRA[@]+"${EXTRA[@]}"} 2>&1)"; rc=$?
    else
      out="$(npx @vscode/vsce@3.9.2 publish --no-dependencies --packagePath "$vsix" ${EXTRA[@]+"${EXTRA[@]}"} 2>&1)"; rc=$?
    fi
    printf '%s\n' "$out"

    if [[ $rc -eq 0 ]]; then
      return 0
    fi
    if grep -qiE "$ALREADY_PUBLISHED_RE" <<<"$out"; then
      echo "::notice::$vsix is already published at $VERSION; treating as success"
      return 0
    fi
    if grep -qiE "$TRANSIENT_RE" <<<"$out" && (( attempt < MAX_ATTEMPTS )); then
      echo "::warning::$vsix: transient registry failure (attempt $attempt/$MAX_ATTEMPTS); retrying in ${BACKOFF_SECONDS}s"
      sleep "$BACKOFF_SECONDS"
      attempt=$(( attempt + 1 ))
      continue
    fi
    return "$rc"
  done
}

shopt -s nullglob
VSIXES=(*.vsix)
(( ${#VSIXES[@]} > 0 )) || { echo "::error::no .vsix files to publish"; exit 1; }

PUBLISHED=()
FAILED=()
for VSIX in "${VSIXES[@]}"; do
  echo "::group::Publishing $VSIX to $REGISTRY"
  if publish_one "$VSIX"; then
    PUBLISHED+=("$VSIX")
  else
    # Keep going: every target gets an attempt, so the summary below is the
    # whole truth rather than "everything up to the first problem".
    FAILED+=("$VSIX")
    echo "::error::$VSIX did not publish to $REGISTRY"
  fi
  echo "::endgroup::"
done

echo "Published ${#PUBLISHED[@]}/${#VSIXES[@]} to $REGISTRY:"
for v in ${PUBLISHED[@]+"${PUBLISHED[@]}"}; do echo "  ok   $v"; done
for v in ${FAILED[@]+"${FAILED[@]}"};    do echo "  FAIL $v"; done

if (( ${#FAILED[@]} > 0 )); then
  echo "::error::$REGISTRY release of $VERSION is INCOMPLETE — ${#FAILED[@]} target(s) did not publish. Re-running this workflow is safe: targets that already landed are treated as success."
  exit 1
fi
echo "All ${#VSIXES[@]} targets are live on $REGISTRY at $VERSION."
