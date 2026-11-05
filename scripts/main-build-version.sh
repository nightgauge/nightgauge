#!/usr/bin/env bash
#
# main-build-version.sh — the synthetic version main-build.yml builds under
# (#2952): X.Y.(Z+1)-main.g<sha7>, where vX.Y.Z is the highest stable tag.
#
# Usage: scripts/main-build-version.sh [<commit>]   (default HEAD)
#
# It sorts below the next real release (0.8.1-main.g... < 0.8.1-rc.N < 0.8.1,
# since "main" < "rc"), so a main build can never pass for a release. The `g`
# keeps the prerelease identifier alphanumeric: a short sha can be all digits
# with a leading zero (0123456), which is invalid semver that npm and vsce
# refuse. Release workflows match only vX.Y.Z and vX.Y.Z-rc.N tags, so this
# version is never published.
#
# Needs full history and tags (fetch-depth: 0). Portable bash 3.2: no sort -V.
#
# Exit codes: 0 version printed; 2 the version could not be derived
# (HARNESS ERROR: no stable tag, or an unknown commit).
set -uo pipefail

cannot_run() { echo "HARNESS ERROR: main-build-version: $1" >&2; exit 2; }

COMMIT="${1:-HEAD}"
SHA="$(git rev-parse -q --verify "${COMMIT}^{commit}" 2>/dev/null)" ||
  cannot_run "unknown commit '$COMMIT'"

SEMVER_RE='^[0-9]+\.[0-9]+\.[0-9]+$'
# semver_lt A B — true when X.Y.Z A sorts strictly before B.
semver_lt() {
  local a1 a2 a3 b1 b2 b3
  IFS=. read -r a1 a2 a3 <<<"$1"
  IFS=. read -r b1 b2 b3 <<<"$2"
  if [ "$((10#$a1))" -ne "$((10#$b1))" ]; then [ "$((10#$a1))" -lt "$((10#$b1))" ]; return; fi
  if [ "$((10#$a2))" -ne "$((10#$b2))" ]; then [ "$((10#$a2))" -lt "$((10#$b2))" ]; return; fi
  [ "$((10#$a3))" -lt "$((10#$b3))" ]
}

HIGH=""
while IFS= read -r t; do
  v="${t#v}"
  [[ "$v" =~ $SEMVER_RE ]] || continue
  if [ -z "$HIGH" ] || semver_lt "$HIGH" "$v"; then HIGH="$v"; fi
done < <(git tag -l 'v*')
[ -n "$HIGH" ] || cannot_run "no stable vX.Y.Z tag in this clone (fetch-depth: 0 and tags?)"

IFS=. read -r MAJOR MINOR PATCH <<<"$HIGH"
echo "$((10#$MAJOR)).$((10#$MINOR)).$((10#$PATCH + 1))-main.g${SHA:0:7}"
