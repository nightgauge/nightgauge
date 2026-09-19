#!/usr/bin/env bash
# verify-release-channels.sh — does each distribution channel serve the latest
# stable release? (#1874)
#
# A release is published to several channels, and some of them only PROPOSE
# the new version. The Homebrew cask is one: `.goreleaser.yml` opens a pull
# request on nightgauge/homebrew-tap (the tap's ruleset forbids direct pushes,
# so a leaked release token can only propose a formula). Nothing checked that
# the pull request merged, and the tap served v0.2.3 through seven releases
# while the GitHub Release and Open VSX were at v0.4.4.
#
# This script is the check. It asks the channel what it actually serves and
# compares that with the release, rather than trusting the publish step.
#
# Usage:
#   verify-release-channels.sh --channel homebrew [options]
#
# Options:
#   --version X.Y.Z       Expected version. Default: the latest non-prerelease
#                         GitHub Release of --source.
#   --source OWNER/REPO   Repository whose releases are the truth.
#                         Default: nightgauge/nightgauge.
#   --tap OWNER/REPO      Homebrew tap. Default: nightgauge/homebrew-tap.
#   --release             Release-run mode. The cask pull request was opened
#                         seconds ago and waits for review, so an unmerged
#                         pull request for --version is expected: pass when the
#                         tap already serves --version OR that pull request is
#                         open, fail only when neither holds (nothing was
#                         proposed). Without --release (watchdog mode) only
#                         the tap's default branch counts.
#   --close-superseded    Close every open cask pull request older than the
#                         newest version proposed or served. Needs a token that
#                         can write the tap's pull requests.
#   --issue-repo OWNER/REPO
#                         Keep exactly one open issue per channel in this
#                         repository: opened (or its body refreshed) while the
#                         channel is behind, closed once it is current. Needs
#                         `issues: write` there.
#
# Exit: 0 channel current (or, with --release, proposed); 1 channel behind,
# with the reason and the pull request to merge on stderr; 2 cannot determine
# (usage error, API failure, unparseable cask).
#
# Channels: `homebrew` today. #1769 adds the VS Code Marketplace and Open VSX
# as further channels of this script, so every channel is verified the same
# way, reports through the same exit codes and keeps one issue each.
#
# Portable to the macOS runner's bash 3.2: no associative arrays, no mapfile,
# no `sort -V`.
set -uo pipefail

CHANNEL=""
VERSION=""
SOURCE="nightgauge/nightgauge"
TAP="nightgauge/homebrew-tap"
RELEASE_MODE=0
CLOSE_SUPERSEDED=0
ISSUE_REPO=""

usage() {
  sed -n '2,/^set -uo/p' "$0" | sed -e '$d' -e 's/^# \{0,1\}//' >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
  --channel) CHANNEL="${2:-}"; shift ;;
  --version) VERSION="${2:-}"; shift ;;
  --source) SOURCE="${2:-}"; shift ;;
  --tap) TAP="${2:-}"; shift ;;
  --release) RELEASE_MODE=1 ;;
  --close-superseded) CLOSE_SUPERSEDED=1 ;;
  --issue-repo) ISSUE_REPO="${2:-}"; shift ;;
  -h | --help) usage ;;
  *) echo "ERROR: unknown argument '$1'" >&2; exit 2 ;;
  esac
  shift
done

REPO_RE='^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'
SEMVER_RE='^[0-9]+\.[0-9]+\.[0-9]+$'

case "$CHANNEL" in
homebrew) ;;
"") echo "ERROR: --channel is required (homebrew)" >&2; exit 2 ;;
*) echo "ERROR: unknown channel '$CHANNEL' (homebrew)" >&2; exit 2 ;;
esac
for pair in "source:$SOURCE" "tap:$TAP" "issue-repo:$ISSUE_REPO"; do
  name="${pair%%:*}" value="${pair#*:}"
  [ -z "$value" ] && [ "$name" = "issue-repo" ] && continue
  [[ "$value" =~ $REPO_RE ]] || { echo "ERROR: --$name must be owner/repo, got '$value'" >&2; exit 2; }
done
if [ -n "$VERSION" ] && [[ ! "$VERSION" =~ $SEMVER_RE ]]; then
  echo "ERROR: --version must be X.Y.Z (a stable release), got '$VERSION'" >&2
  exit 2
fi

# semver_lt A B — true when stable version A sorts strictly before B.
semver_lt() {
  local a1 a2 a3 b1 b2 b3
  IFS=. read -r a1 a2 a3 <<<"$1"
  IFS=. read -r b1 b2 b3 <<<"$2"
  if [ "$((10#$a1))" -ne "$((10#$b1))" ]; then [ "$((10#$a1))" -lt "$((10#$b1))" ]; return; fi
  if [ "$((10#$a2))" -ne "$((10#$b2))" ]; then [ "$((10#$a2))" -lt "$((10#$b2))" ]; return; fi
  [ "$((10#$a3))" -lt "$((10#$b3))" ]
}

SUMMARY=""
say() { # say <line> — to stdout and, on a runner, the job summary
  echo "$1"
  SUMMARY+="$1"$'\n'
}
flush_summary() {
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ] && [ -n "$SUMMARY" ]; then
    { echo "### Release channel: $CHANNEL"; echo; printf '%s' "$SUMMARY"; } >>"$GITHUB_STEP_SUMMARY"
  fi
}
trap flush_summary EXIT

# --- The release: what every channel should serve ------------------------------
if [ -z "$VERSION" ]; then
  # /releases/latest is the newest release that is neither a draft nor a
  # prerelease — exactly the version `brew install` ought to be serving.
  if ! TAG="$(gh api "repos/$SOURCE/releases/latest" | jq -r '.tag_name // empty')" || [ -z "$TAG" ]; then
    echo "ERROR: cannot read the latest release of $SOURCE" >&2
    exit 2
  fi
  VERSION="${TAG#v}"
  if [[ ! "$VERSION" =~ $SEMVER_RE ]]; then
    echo "ERROR: latest release tag '$TAG' of $SOURCE is not vX.Y.Z" >&2
    exit 2
  fi
fi

# --- The channel: what the tap's default branch actually serves ------------------
if ! CASK="$(gh api -H "Accept: application/vnd.github.raw" "repos/$TAP/contents/Casks/nightgauge.rb?ref=main")"; then
  echo "ERROR: cannot read Casks/nightgauge.rb on $TAP main" >&2
  exit 2
fi
SERVED="$(printf '%s\n' "$CASK" | sed -n 's/^[[:space:]]*version "\([^"]*\)".*/\1/p' | head -n 1)"
if [[ ! "$SERVED" =~ $SEMVER_RE ]]; then
  echo "ERROR: no X.Y.Z 'version' line in Casks/nightgauge.rb on $TAP main (got '${SERVED:-<none>}')" >&2
  exit 2
fi

# --- The proposals: open cask pull requests --------------------------------------
# Only heads in the tap itself: GoReleaser pushes `cask-<version>` there, and a
# fork's pull request named `cask-99.0.0` must not be able to make this script
# close the real one as superseded.
if ! PULLS="$(gh api "repos/$TAP/pulls?state=open&per_page=100")"; then
  echo "ERROR: cannot list open pull requests on $TAP" >&2
  exit 2
fi
if ! OPEN_CASKS="$(printf '%s' "$PULLS" | jq -r --arg tap "$TAP" '
    .[] | select(.head.repo.full_name == $tap)
        | select(.head.ref | test("^cask-[0-9]+\\.[0-9]+\\.[0-9]+$"))
        | "\(.number) \(.head.ref | ltrimstr("cask-"))"')"; then
  echo "ERROR: unparseable pull request list from $TAP" >&2
  exit 2
fi

PROPOSED_PR=""
NEWEST="$SERVED"
if semver_lt "$NEWEST" "$VERSION"; then NEWEST="$VERSION"; fi
while read -r num ver; do
  [ -n "$num" ] || continue
  [ "$ver" = "$VERSION" ] && PROPOSED_PR="$num"
  if semver_lt "$NEWEST" "$ver"; then NEWEST="$ver"; fi
done <<<"$OPEN_CASKS"

say "- release: \`$VERSION\` ($SOURCE)"
say "- tap \`main\` serves: \`$SERVED\` ($TAP)"

# --- Supersede: an older proposal can never be the one to merge ------------------
SUPERSEDED=""
while read -r num ver; do
  [ -n "$num" ] || continue
  semver_lt "$ver" "$NEWEST" || continue
  if [ "$CLOSE_SUPERSEDED" -eq 1 ]; then
    if gh pr close "$num" -R "$TAP" --comment "Superseded: a newer cask ($NEWEST) is proposed or served. Closed by scripts/verify-release-channels.sh in nightgauge/nightgauge (#1874)." >/dev/null; then
      say "- closed superseded cask PR $TAP#$num ($ver)"
    else
      echo "::warning::could not close superseded cask PR $TAP#$num ($ver)" >&2
      SUPERSEDED+=" #$num"
    fi
  else
    SUPERSEDED+=" #$num"
  fi
done <<<"$OPEN_CASKS"
[ -n "$SUPERSEDED" ] && say "- superseded cask PRs still open:$SUPERSEDED (close them)"

# --- Verdict ---------------------------------------------------------------------
ISSUE_TITLE="Release channel behind: $CHANNEL"
VERDICT=0
if [ "$SERVED" = "$VERSION" ]; then
  say "- verdict: current"
elif [ "$RELEASE_MODE" -eq 1 ] && [ -n "$PROPOSED_PR" ]; then
  say "- verdict: proposed. The release is NOT complete until $TAP#$PROPOSED_PR (cask-$VERSION) is merged; the release watchdog goes red until it is."
  echo "::warning::brew still serves $SERVED. Merge $TAP#$PROPOSED_PR (cask-$VERSION) to finish the $VERSION release." >&2
else
  VERDICT=1
  if [ -n "$PROPOSED_PR" ]; then
    REASON="brew serves $SERVED but the latest release is $VERSION: merge $TAP#$PROPOSED_PR (cask-$VERSION) after checking its sha256s against the release's checksums.txt."
  else
    REASON="brew serves $SERVED but the latest release is $VERSION, and no open cask PR proposes $VERSION: re-run the release's cask step or open the cask PR by hand."
  fi
  say "- verdict: BEHIND. $REASON"
  echo "::error::$REASON" >&2
fi

# --- One issue per channel -------------------------------------------------------
if [ -n "$ISSUE_REPO" ]; then
  if ! EXISTING="$(gh api "repos/$ISSUE_REPO/issues?state=open&per_page=100" |
    jq -r --arg t "$ISSUE_TITLE" '[.[] | select(.pull_request == null) | select(.title == $t)][0].number // empty')"; then
    echo "ERROR: cannot list open issues on $ISSUE_REPO" >&2
    exit 2
  fi
  if [ "$VERDICT" -eq 1 ]; then
    BODY="The \`$CHANNEL\` channel does not serve the latest release.

$SUMMARY
Detected by \`scripts/verify-release-channels.sh --channel $CHANNEL\` (#1874). This issue is refreshed on every watchdog run while the channel is behind and closed automatically once it is current."
    if [ -n "$EXISTING" ]; then
      gh issue edit "$EXISTING" -R "$ISSUE_REPO" --body "$BODY" >/dev/null || { echo "ERROR: cannot update issue #$EXISTING" >&2; exit 2; }
      say "- refreshed issue $ISSUE_REPO#$EXISTING"
    else
      gh issue create -R "$ISSUE_REPO" --title "$ISSUE_TITLE" --body "$BODY" >/dev/null || { echo "ERROR: cannot open an issue on $ISSUE_REPO" >&2; exit 2; }
      say "- opened issue \"$ISSUE_TITLE\" on $ISSUE_REPO"
    fi
  elif [ -n "$EXISTING" ] && [ "$SERVED" = "$VERSION" ]; then
    gh issue close "$EXISTING" -R "$ISSUE_REPO" --comment "The \`$CHANNEL\` channel now serves $VERSION." >/dev/null || { echo "ERROR: cannot close issue #$EXISTING" >&2; exit 2; }
    say "- closed issue $ISSUE_REPO#$EXISTING: channel is current"
  fi
fi

exit "$VERDICT"
