#!/usr/bin/env bash
# Regression tests for scripts/verify-release-channels.sh (#1874).
#
# Every case stubs `gh` with a fake executable placed first on PATH that serves
# fixture files for the three reads (latest release, the tap's cask, the tap's
# open pull requests, the issue list) and records every write it is asked for,
# so a case can assert both the verdict and exactly what was closed or filed.
#
# Run: bash scripts/test-verify-release-channels.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/verify-release-channels.sh"
PASS=0
FAIL=0
FAKE=""

cleanup() {
  [ -n "$FAKE" ] && rm -rf "$FAKE"
  return 0
}
trap cleanup EXIT

# fixture <latest-tag> <served-version> <pulls-json> [<issues-json>]
fixture() {
  [ -n "$FAKE" ] && rm -rf "$FAKE"
  FAKE="$(mktemp -d)"
  mkdir -p "$FAKE/bin"
  printf '{"tag_name": "%s"}\n' "$1" >"$FAKE/latest.json"
  printf '# generated\ncask "nightgauge" do\n  version "%s"\n  sha256 "abc"\nend\n' "$2" >"$FAKE/cask.rb"
  printf '%s\n' "$3" >"$FAKE/pulls.json"
  printf '%s\n' "${4:-[]}" >"$FAKE/issues.json"
  : >"$FAKE/writes.log"
  {
    echo '#!/usr/bin/env bash'
    echo "fake='$FAKE'"
    cat <<'GH_STUB'
case "$1 $2" in
"pr close" | "issue create" | "issue edit" | "issue close")
  echo "$*" >>"$fake/writes.log"
  exit 0
  ;;
esac
[ "$1" = "api" ] || exit 1
for arg in "$@"; do
  case "$arg" in
  */releases/latest) [ -e "$fake/no-release" ] && exit 1; cat "$fake/latest.json"; exit 0 ;;
  */contents/Casks/nightgauge.rb*) cat "$fake/cask.rb"; exit 0 ;;
  */pulls\?*) cat "$fake/pulls.json"; exit 0 ;;
  */issues\?*) cat "$fake/issues.json"; exit 0 ;;
  esac
done
exit 1
GH_STUB
  } >"$FAKE/bin/gh"
  chmod +x "$FAKE/bin/gh"
}

# pr <number> <head-ref> [<head-repo>]
pr() {
  printf '{"number": %s, "head": {"ref": "%s", "repo": {"full_name": "%s"}}}' \
    "$1" "$2" "${3:-nightgauge/homebrew-tap}"
}

OUT=""
RC=0
run() {
  OUT="$(env -u GITHUB_STEP_SUMMARY PATH="$FAKE/bin:$PATH" bash "$SCRIPT" --channel homebrew "$@" 2>&1)"
  RC=$?
}

# expect <name> <want-rc> <want-substring> [<want-writes-substring>|NONE]
expect() {
  local name="$1" want_rc="$2" want_sub="$3" want_write="${4:-}"
  local writes
  writes="$(cat "$FAKE/writes.log")"
  if [ "$RC" -ne "$want_rc" ]; then
    echo "FAIL: $name: exit $RC, want $want_rc"
    echo "      output: $OUT"
    FAIL=$((FAIL + 1))
    return
  fi
  case "$OUT" in
  *"$want_sub"*) ;;
  *)
    echo "FAIL: $name: output lacks '$want_sub'"
    echo "      output: $OUT"
    FAIL=$((FAIL + 1))
    return
    ;;
  esac
  if [ "$want_write" = "NONE" ] && [ -n "$writes" ]; then
    echo "FAIL: $name: expected no writes, got: $writes"
    FAIL=$((FAIL + 1))
    return
  fi
  if [ -n "$want_write" ] && [ "$want_write" != "NONE" ]; then
    case "$writes" in
    *"$want_write"*) ;;
    *)
      echo "FAIL: $name: writes lack '$want_write'; got: ${writes:-<none>}"
      FAIL=$((FAIL + 1))
      return
      ;;
    esac
  fi
  echo "PASS: $name"
  PASS=$((PASS + 1))
}

# 1. The acceptance case: the tap is current -> green, nothing written.
fixture v0.4.4 0.4.4 '[]'
run
expect "tap at the latest release is green" 0 "verdict: current" NONE

# 2. THE DEFECT: a release published, its cask PR open and unmerged -> red,
#    naming the pull request to merge.
fixture v0.4.4 0.2.3 "[$(pr 17 cask-0.4.4)]"
run
expect "unmerged cask PR goes red and names it" 1 "merge nightgauge/homebrew-tap#17 (cask-0.4.4)" NONE

# 3. Behind with no proposal at all: still red, and says so.
fixture v0.4.4 0.4.3 '[]'
run
expect "behind with no cask PR is red" 1 "no open cask PR proposes 0.4.4"

# 4. The tap ahead of the release (a release deleted or demoted) is a mismatch too.
fixture v0.4.3 0.4.4 '[]'
run
expect "tap ahead of the release is red" 1 "BEHIND"

# 5. Release mode: the PR opened seconds ago is the expected state, not a failure.
fixture v0.4.4 0.4.3 "[$(pr 17 cask-0.4.4)]"
run --release --version 0.4.4
expect "release mode accepts the just-opened cask PR" 0 "NOT complete until nightgauge/homebrew-tap#17"

# 6. Release mode fails when nothing was proposed (GoReleaser skipped the tap).
fixture v0.4.4 0.4.3 '[]'
run --release --version 0.4.4
expect "release mode with no cask PR is red" 1 "no open cask PR proposes 0.4.4"

# 7. The pile-up: older open cask PRs are closed as superseded, the newest kept.
fixture v0.4.4 0.2.3 "[$(pr 11 cask-0.3.0), $(pr 16 cask-0.4.3), $(pr 17 cask-0.4.4)]"
run --release --version 0.4.4 --close-superseded
expect "superseded cask PRs are closed" 0 "closed superseded cask PR nightgauge/homebrew-tap#11" "pr close 16"
case "$(cat "$FAKE/writes.log")" in
*"pr close 17"*) echo "FAIL: the newest cask PR was closed"; FAIL=$((FAIL + 1)) ;;
*) echo "PASS: the newest cask PR is kept open"; PASS=$((PASS + 1)) ;;
esac

# 8. Without --close-superseded the old PRs are reported, never closed.
fixture v0.4.4 0.4.4 "[$(pr 16 cask-0.4.3)]"
run
expect "superseded PRs are only reported without the flag" 0 "superseded cask PRs still open: #16" NONE

# 9. A fork's PR cannot pose as the newest cask and get the real one closed.
fixture v0.4.4 0.4.3 "[$(pr 17 cask-0.4.4), $(pr 99 cask-9.9.9 attacker/homebrew-tap)]"
run --release --version 0.4.4 --close-superseded
expect "fork PRs are ignored" 0 "cask-0.4.4" NONE

# 10. Semver, not string order: 0.10.0 is newer than 0.9.0.
fixture v0.10.0 0.10.0 "[$(pr 20 cask-0.9.0)]"
run --close-superseded
expect "0.9.0 is superseded by 0.10.0" 0 "verdict: current" "pr close 20"

# 11. One issue per channel: opened when behind ...
fixture v0.4.4 0.2.3 "[$(pr 17 cask-0.4.4)]"
run --issue-repo nightgauge/nightgauge
expect "behind opens the channel issue" 1 "opened issue" "issue create -R nightgauge/nightgauge --title Release channel behind: homebrew"

# 12. ... refreshed, never duplicated, on the next red run ...
fixture v0.4.4 0.2.3 "[$(pr 17 cask-0.4.4)]" '[{"number": 5, "title": "Release channel behind: homebrew"}]'
run --issue-repo nightgauge/nightgauge
expect "a second red run refreshes, not duplicates" 1 "refreshed issue nightgauge/nightgauge#5" "issue edit 5"
case "$(cat "$FAKE/writes.log")" in
*"issue create"*) echo "FAIL: a duplicate issue was opened"; FAIL=$((FAIL + 1)) ;;
*) echo "PASS: no duplicate issue"; PASS=$((PASS + 1)) ;;
esac

# 13. ... and closed once the channel is current.
fixture v0.4.4 0.4.4 '[]' '[{"number": 5, "title": "Release channel behind: homebrew"}]'
run --issue-repo nightgauge/nightgauge
expect "a green run closes the channel issue" 0 "closed issue nightgauge/nightgauge#5" "issue close 5"

# 14. Cannot determine is exit 2, never a green 0.
fixture v0.4.4 0.4.4 '[]'
touch "$FAKE/no-release"
run
expect "an unreadable release is exit 2" 2 "cannot read the latest release"

fixture v0.4.4 'not-a-version' '[]'
run
expect "an unparseable cask is exit 2" 2 "no X.Y.Z 'version' line"

fixture v0.4.4 0.4.4 '[]'
run --version 0.4.4-rc.1
expect "a prerelease --version is rejected" 2 "must be X.Y.Z"

fixture v0.4.4 0.4.4 '[]'
OUT="$(PATH="$FAKE/bin:$PATH" bash "$SCRIPT" 2>&1)"
RC=$?
expect "a missing --channel is rejected" 2 "--channel is required"

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
