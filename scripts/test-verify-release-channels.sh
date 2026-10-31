#!/usr/bin/env bash
# Regression tests for scripts/verify-release-channels.sh (#1874, #1769).
#
# Every case stubs `gh` with a fake executable placed first on PATH that serves
# fixture files for the three reads (latest release, the tap's cask, the tap's
# open pull requests, the issue list) and records every write it is asked for,
# so a case can assert both the verdict and exactly what was closed or filed.
# The listing channels (#1769) also stub `curl`, which serves one fixture per
# registry and logs every request, so a case can count the queries made and
# no case ever reaches the VS Code Marketplace or Open VSX.
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
  : >"$FAKE/curl.log"
  printf '%s\n' '{"results": [{"extensions": []}]}' >"$FAKE/marketplace.json"
  printf '%s\n' '{"extensions": []}' >"$FAKE/openvsx.json"
  {
    echo '#!/usr/bin/env bash'
    echo "fake='$FAKE'"
    cat <<'CURL_STUB'
echo "$*" >>"$fake/curl.log"
[ -e "$fake/curl-fail" ] && exit 22
for arg in "$@"; do
  case "$arg" in
  https://marketplace.visualstudio.com/*) cat "$fake/marketplace.json"; exit 0 ;;
  https://open-vsx.org/*) cat "$fake/openvsx.json"; exit 0 ;;
  esac
done
exit 6
CURL_STUB
  } >"$FAKE/bin/curl"
  chmod +x "$FAKE/bin/curl"
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
  */git/ref/heads/stable)
    [ -e "$fake/stable-missing" ] && { echo 'gh: Not Found (HTTP 404)' >&2; exit 1; }
    [ -e "$fake/stable.sha" ] || { echo 'gh: Bad Gateway (HTTP 502)' >&2; exit 1; }
    printf '{"object": {"sha": "%s"}}\n' "$(cat "$fake/stable.sha")"
    exit 0
    ;;
  */commits/v*) printf '{"sha": "tagsha"}\n'; exit 0 ;;
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

fixture v0.4.4 0.4.4 '[]'
OUT="$(PATH="$FAKE/bin:$PATH" bash "$SCRIPT" --channel snap 2>&1)"
RC=$?
expect "an unknown --channel names the channels" 2 "(homebrew, marketplace, open-vsx, claude-plugin)"

# --- The extension listings (#1769) ----------------------------------------------
# The Marketplace and Open VSX are channels of the same script: one query per
# registry per check, per-target, and the same exit codes and issue contract.

# targets <version>:<target>... -> JSON array of {version, targetPlatform}
targets() {
  local out="" pair
  for pair in "$@"; do
    [ -n "$out" ] && out+=","
    out+="{\"version\": \"${pair%%:*}\", \"targetPlatform\": \"${pair#*:}\"}"
  done
  printf '[%s]' "$out"
}
# marketplace_lists <publisher> <name> <version>:<target>...
marketplace_lists() {
  local pub="$1" name="$2"
  shift 2
  printf '{"results": [{"extensions": [{"extensionName": "%s", "publisher": {"publisherName": "%s"}, "versions": %s}]}]}\n' \
    "$name" "$pub" "$(targets "$@")" >"$FAKE/marketplace.json"
}
# openvsx_lists <namespace> <name> <version>:<target>...
openvsx_lists() {
  local ns="$1" name="$2"
  shift 2
  targets "$@" | jq --arg ns "$ns" --arg n "$name" \
    '{extensions: [.[] | {namespace: $ns, name: $n, version, targetPlatform}]}' >"$FAKE/openvsx.json"
}
ALL_072=(0.7.2:darwin-arm64 0.7.2:darwin-x64 0.7.2:linux-x64)

run_listing() { # run_listing <channel> [args...]
  local channel="$1"
  shift
  OUT="$(env -u GITHUB_STEP_SUMMARY PATH="$FAKE/bin:$PATH" CHANNEL_POLL_SECONDS=1 \
    bash "$SCRIPT" --channel "$channel" "$@" 2>&1)"
  RC=$?
}
queries() { wc -l <"$FAKE/curl.log" | tr -d ' '; }

# 15. The Marketplace serves the latest release on every target: green, one query.
fixture v0.7.2 0.7.2 '[]'
marketplace_lists nightgauge nightgauge "${ALL_072[@]}" 0.7.1:linux-x64
run_listing marketplace
expect "marketplace at the latest release is green" 0 "verdict: current" NONE
if [ "$(queries)" = "1" ]; then echo "PASS: one Marketplace query per check"; PASS=$((PASS + 1))
else echo "FAIL: want 1 Marketplace query, got $(queries): $(cat "$FAKE/curl.log")"; FAIL=$((FAIL + 1)); fi
case "$(cat "$FAKE/curl.log")" in
*'"value":"nightgauge.nightgauge"'*) echo "PASS: the query names packages/nightgauge's extension ID"; PASS=$((PASS + 1)) ;;
*) echo "FAIL: the query does not name nightgauge.nightgauge: $(cat "$FAKE/curl.log")"; FAIL=$((FAIL + 1)) ;;
esac

# 16. One target left behind (the v0.4.2 partial publish) is red and names it.
fixture v0.7.2 0.7.2 '[]'
marketplace_lists nightgauge nightgauge 0.7.2:darwin-arm64 0.7.2:darwin-x64 0.7.1:linux-x64
run_listing marketplace
expect "marketplace with one target behind is red" 1 "linux-x64 serves 0.7.1"

# 17. Not listed at all (delisted, taken down, wrong ID) is red, not "cannot determine".
fixture v0.7.2 0.7.2 '[]'
run_listing marketplace
expect "a missing Marketplace listing is red" 1 "is not listed"

# 18. Another extension in the result is not ours.
fixture v0.7.2 0.7.2 '[]'
marketplace_lists someone-else nightgauge "${ALL_072[@]}"
run_listing marketplace
expect "another publisher's extension does not count" 1 "is not listed"

# 19. Open VSX: current, then a missing target.
fixture v0.7.2 0.7.2 '[]'
openvsx_lists nightgauge nightgauge "${ALL_072[@]}"
run_listing open-vsx
expect "open-vsx at the latest release is green" 0 "verdict: current" NONE
case "$(cat "$FAKE/curl.log")" in
*"extensionId=nightgauge.nightgauge"*) echo "PASS: the Open VSX query names the extension ID"; PASS=$((PASS + 1)) ;;
*) echo "FAIL: the Open VSX query does not name the ID: $(cat "$FAKE/curl.log")"; FAIL=$((FAIL + 1)) ;;
esac

fixture v0.7.2 0.7.2 '[]'
openvsx_lists nightgauge nightgauge 0.7.2:darwin-arm64 0.7.2:darwin-x64
run_listing open-vsx
expect "open-vsx with a target never published is red" 1 "linux-x64 serves nothing"

# 20. A listing ahead of the release (a release deleted or demoted) is red too.
fixture v0.7.1 0.7.1 '[]'
openvsx_lists nightgauge nightgauge "${ALL_072[@]}"
run_listing open-vsx
expect "a listing ahead of the release is red" 1 "BEHIND"

# 21. Release mode: the published version is listed on every target -> green,
#     even when a newer version is listed too (re-promoting an older tag).
fixture v0.7.2 0.7.2 '[]'
marketplace_lists nightgauge nightgauge "${ALL_072[@]}" 0.7.1:darwin-arm64 0.7.1:darwin-x64 0.7.1:linux-x64
run_listing marketplace --release --version 0.7.1
expect "release mode accepts a listed older version" 0 "verdict: listed"

# 22. Release mode waits, bounded, then fails when the version never appears.
fixture v0.7.2 0.7.2 '[]'
marketplace_lists nightgauge nightgauge 0.7.1:darwin-arm64 0.7.1:darwin-x64 0.7.1:linux-x64
run_listing marketplace --release --version 0.7.2 --wait 2
expect "release mode fails when the version never lists" 1 "not listed within 2s"
q="$(queries)"
if [ "$q" -ge 2 ] && [ "$q" -le 4 ]; then echo "PASS: the wait polls a bounded number of times ($q)"; PASS=$((PASS + 1))
else echo "FAIL: want 2-4 polls within --wait 2, got $q"; FAIL=$((FAIL + 1)); fi

# 23. Without --wait, release mode queries once.
fixture v0.7.2 0.7.2 '[]'
openvsx_lists nightgauge nightgauge "${ALL_072[@]}"
run_listing open-vsx --release --version 0.7.2
expect "release mode with the version listed is green" 0 "verdict: listed"
if [ "$(queries)" = "1" ]; then echo "PASS: one Open VSX query without --wait"; PASS=$((PASS + 1))
else echo "FAIL: want 1 Open VSX query, got $(queries)"; FAIL=$((FAIL + 1)); fi

# 24. One issue per listing channel: opened when behind, never duplicated, closed when current.
fixture v0.7.2 0.7.2 '[]'
run_listing marketplace --issue-repo nightgauge/nightgauge
expect "a missing listing opens the channel issue" 1 "opened issue" "issue create -R nightgauge/nightgauge --title Release channel behind: marketplace"

fixture v0.7.2 0.7.2 '[]' '[{"number": 8, "title": "Release channel behind: open-vsx"}]'
run_listing open-vsx --issue-repo nightgauge/nightgauge
expect "a second red listing run refreshes, not duplicates" 1 "refreshed issue nightgauge/nightgauge#8" "issue edit 8"
case "$(cat "$FAKE/writes.log")" in
*"issue create"*) echo "FAIL: a duplicate listing issue was opened"; FAIL=$((FAIL + 1)) ;;
*) echo "PASS: no duplicate listing issue"; PASS=$((PASS + 1)) ;;
esac

fixture v0.7.2 0.7.2 '[]' '[{"number": 8, "title": "Release channel behind: open-vsx"}]'
openvsx_lists nightgauge nightgauge "${ALL_072[@]}"
run_listing open-vsx --issue-repo nightgauge/nightgauge
expect "a current listing closes the channel issue" 0 "closed issue nightgauge/nightgauge#8" "issue close 8"

# 25. Cannot determine is exit 2: an unreachable registry or an unparseable answer.
fixture v0.7.2 0.7.2 '[]'
touch "$FAKE/curl-fail"
run_listing marketplace
expect "an unreachable Marketplace is exit 2" 2 "cannot query"

fixture v0.7.2 0.7.2 '[]'
printf '<html>maintenance</html>\n' >"$FAKE/openvsx.json"
run_listing open-vsx
expect "an unparseable Open VSX answer is exit 2" 2 "unparseable"

fixture v0.7.2 0.7.2 '[]'
run_listing marketplace --extension 'not an id'
expect "a malformed --extension is rejected" 2 "--extension must be publisher.name"

# 25b. The plugin channel: stable must sit on the latest release's commit.
fixture v0.7.2 0.7.2 '[]'
echo tagsha >"$FAKE/stable.sha"
run_listing claude-plugin
expect "stable at the release is current" 0 "verdict: current"

fixture v0.7.2 0.7.2 '[]'
echo oldsha >"$FAKE/stable.sha"
run_listing claude-plugin --issue-repo nightgauge/nightgauge
expect "stable left behind is red and files one issue" 1 "fast-forward it" "issue create"

fixture v0.7.2 0.7.2 '[]'
run_listing claude-plugin
expect "an unreadable stable branch is exit 2" 2 "cannot read the stable branch"

fixture v0.7.2 0.7.2 '[]'
touch "$FAKE/stable-missing"
run_listing claude-plugin --issue-repo nightgauge/nightgauge
expect "a missing stable branch is red and files one issue" 1 "does not exist" "issue create"

fixture v0.7.2 0.7.2 '[]' '[{"number": 9, "title": "Release channel behind: claude-plugin"}]'
echo tagsha >"$FAKE/stable.sha"
run_listing claude-plugin --issue-repo nightgauge/nightgauge
expect "a current stable closes the channel issue" 0 "closed issue nightgauge/nightgauge#9" "issue close 9"

# 26. The listing check is the only automated query to either registry host
#     (#1767, #1769): no other workflow or script names one. The link-check
#     gate's ignore list and its suite name them to keep that gate off them.
OFFENDERS="$(cd "$ROOT" && git ls-files -- .github scripts packages/nightgauge/scripts |
  xargs grep -lE 'marketplace\.visualstudio\.com|open-vsx\.org' 2>/dev/null |
  grep -vxE 'scripts/(verify-release-channels|test-verify-release-channels|test-check-md-links)\.sh')"
if [ -z "$OFFENDERS" ]; then echo "PASS: no other workflow or script queries a registry host"; PASS=$((PASS + 1))
else echo "FAIL: these name a registry host: $OFFENDERS"; FAIL=$((FAIL + 1)); fi

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
