#!/usr/bin/env bash
# verify-release-channels.sh — does each distribution channel serve the latest
# stable release? (#1874, #1769)
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
# The VS Code Marketplace and Open VSX are channels too (#1769). Their publish
# call can report success while the listing never shows the version (indexing
# delay, a rejected submission, one target lost to a 503), and a listing can
# regress later (delisted, taken down). Each is checked per target platform
# with ONE query to its registry, and this script is the only automated thing
# in the repository that queries either host (#1767 keeps the docs link gate
# off them).
#
# The Claude plugin marketplace is installed at the `stable` branch, which
# release.yml advances last (#2724). A branch left behind means `claude plugin
# update` serves an old release, so it must sit on the release tag's commit.
#
# Usage:
#   verify-release-channels.sh --channel homebrew|marketplace|open-vsx|claude-plugin [options]
#
# Options:
#   --version X.Y.Z       Expected version. Default: the latest non-prerelease
#                         GitHub Release of --source.
#   --source OWNER/REPO   Repository whose releases are the truth.
#                         Default: nightgauge/nightgauge.
#   --tap OWNER/REPO      Homebrew tap. Default: nightgauge/homebrew-tap.
#   --extension PUB.NAME  Extension ID for the listing channels. Default:
#                         publisher.name of packages/nightgauge/package.json.
#   --targets "T ..."     Target platforms every listing must serve. Default:
#                         the three release.yml packages
#                         ("darwin-arm64 darwin-x64 linux-x64").
#   --wait SECONDS        Listing channels, --release only: re-query every
#                         CHANNEL_POLL_SECONDS (default 30) until the version is
#                         listed on every target or SECONDS have passed.
#   --release             Release-run mode. The cask pull request was opened
#                         seconds ago and waits for review, so an unmerged
#                         pull request for --version is expected: pass when the
#                         tap already serves --version OR that pull request is
#                         open, fail only when neither holds (nothing was
#                         proposed). Without --release (watchdog mode) only
#                         the tap's default branch counts. For a listing
#                         channel, --release passes when --version is listed
#                         on every target (a newer version may be listed too);
#                         without it, every target's newest version must BE
#                         the release.
#   --close-superseded    Close every open cask pull request older than the
#                         newest version proposed or served. Needs a token that
#                         can write the tap's pull requests.
#   --issue-repo OWNER/REPO
#                         Keep exactly one open issue per channel in this
#                         repository: opened (or its body refreshed) while the
#                         channel is behind, closed once it is current. Needs
#                         `issues: write` there.
#
# Exit: 0 channel current (or, with --release, proposed or listed); 1 channel
# behind or not listed, with the reason on stderr; 2 cannot determine (usage
# error, API failure, unparseable cask or registry answer).
#
# Every channel is verified the same way, reports through the same exit codes
# and keeps one issue each.
#
# Portable to the macOS runner's bash 3.2: no associative arrays, no mapfile,
# no `sort -V`.
set -uo pipefail

CHANNEL=""
VERSION=""
SOURCE="nightgauge/nightgauge"
TAP="nightgauge/homebrew-tap"
EXTENSION=""
TARGETS="darwin-arm64 darwin-x64 linux-x64"
WAIT=0
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
  --extension) EXTENSION="${2:-}"; shift ;;
  --targets) TARGETS="${2:-}"; shift ;;
  --wait) WAIT="${2:-}"; shift ;;
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

CHANNELS="homebrew, marketplace, open-vsx, claude-plugin"
case "$CHANNEL" in
homebrew | marketplace | open-vsx | claude-plugin) ;;
"") echo "ERROR: --channel is required ($CHANNELS)" >&2; exit 2 ;;
*) echo "ERROR: unknown channel '$CHANNEL' ($CHANNELS)" >&2; exit 2 ;;
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
if [ "$CHANNEL" = "marketplace" ] || [ "$CHANNEL" = "open-vsx" ]; then
  if [ -z "$EXTENSION" ]; then
    MANIFEST="$(cd "$(dirname "$0")/.." && pwd)/packages/nightgauge/package.json"
    EXTENSION="$(jq -r '"\(.publisher).\(.name)"' "$MANIFEST" 2>/dev/null)" || EXTENSION=""
  fi
  # Lowercase publisher.name: it is interpolated into a query string and a
  # JSON body, so nothing outside this shape may reach either.
  if [[ ! "$EXTENSION" =~ ^[a-z0-9][a-z0-9-]*\.[a-z0-9][a-z0-9-]*$ ]]; then
    echo "ERROR: --extension must be publisher.name (lowercase), got '$EXTENSION'" >&2
    exit 2
  fi
  [ -n "${TARGETS// /}" ] || { echo "ERROR: --targets is empty" >&2; exit 2; }
  for t in $TARGETS; do
    [[ "$t" =~ ^[a-z0-9]+-[a-z0-9]+$ ]] || { echo "ERROR: --targets entry '$t' is not os-arch" >&2; exit 2; }
  done
  [[ "$WAIT" =~ ^[0-9]+$ ]] || { echo "ERROR: --wait must be whole seconds, got '$WAIT'" >&2; exit 2; }
  POLL="${CHANNEL_POLL_SECONDS:-30}"
  [[ "$POLL" =~ ^[1-9][0-9]*$ ]] || { echo "ERROR: CHANNEL_POLL_SECONDS must be a positive integer" >&2; exit 2; }
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

# --- The listing channels: what each registry lists, per target ----------------
# One query per call. Prints a "TARGET VERSION" line for every version the
# registry lists for this extension; returns 2 when it cannot be read.
query_listing() {
  local raw filter
  if [ "$CHANNEL" = "marketplace" ]; then
    # filterType 7 is the exact extension ID; flags 1 includes the versions,
    # each of which names its targetPlatform.
    if ! raw="$(curl -fsS --max-time 30 -X POST \
      -H "Accept: application/json;api-version=3.0-preview.1" \
      -H "Content-Type: application/json" \
      -d "{\"filters\":[{\"criteria\":[{\"filterType\":7,\"value\":\"$EXTENSION\"}]}],\"flags\":1}" \
      "https://marketplace.visualstudio.com/_apis/public/gallery/extensionquery")"; then
      echo "ERROR: cannot query the VS Code Marketplace for $EXTENSION" >&2
      return 2
    fi
    filter='.results[0].extensions
      | if type == "array" then . else error("no extensions array") end
      | .[] | select(((.publisher.publisherName + "." + .extensionName) | ascii_downcase) == $id)
      | .versions[] | "\(.targetPlatform // "universal") \(.version)"'
  else
    if ! raw="$(curl -fsS --max-time 30 \
      "https://open-vsx.org/api/-/query?extensionId=$EXTENSION&includeAllVersions=true")"; then
      echo "ERROR: cannot query Open VSX for $EXTENSION" >&2
      return 2
    fi
    filter='.extensions
      | if type == "array" then . else error("no extensions array") end
      | .[] | select(((.namespace + "." + .name) | ascii_downcase) == $id)
      | "\(.targetPlatform // "universal") \(.version)"'
  fi
  if ! jq -r --arg id "$EXTENSION" "$filter" <<<"$raw" 2>/dev/null; then
    echo "ERROR: unparseable answer from the $CHANNEL registry" >&2
    return 2
  fi
}

# newest_for TARGET LISTING — the newest stable version listed for TARGET.
newest_for() {
  local want="$1" best="" tgt ver
  while read -r tgt ver; do
    [ "$tgt" = "$want" ] && [[ "$ver" =~ $SEMVER_RE ]] || continue
    if [ -z "$best" ] || semver_lt "$best" "$ver"; then best="$ver"; fi
  done <<<"$2"
  printf '%s' "$best"
}

# Sets CURRENT, VERDICT and REASON; exits 2 when the registry cannot be read.
# Watchdog mode: every target's newest listed version must be the release.
# Release mode: the release must be listed on every target, polled for up to
# --wait seconds because a registry indexes a publish asynchronously.
check_listing() {
  local name listing t newest missing behind deadline
  if [ "$CHANNEL" = "marketplace" ]; then name="VS Code Marketplace"; else name="Open VSX"; fi
  deadline=$((SECONDS + WAIT))
  while :; do
    listing="$(query_listing)" || exit 2
    missing="" behind=""
    for t in $TARGETS; do
      if [ "$RELEASE_MODE" -eq 1 ]; then
        grep -qxF "$t $VERSION" <<<"$listing" || missing+=" $t"
      else
        newest="$(newest_for "$t" "$listing")"
        [ "$newest" = "$VERSION" ] || behind+="; $t serves ${newest:-nothing}"
      fi
    done
    if [ "$RELEASE_MODE" -eq 0 ] || [ -z "$missing" ] || [ "$SECONDS" -ge "$deadline" ]; then
      break
    fi
    echo "$VERSION not yet listed on $name for:$missing; re-querying in ${POLL}s" >&2
    sleep "$POLL"
  done

  say "- release: \`$VERSION\` ($SOURCE)"
  say "- $name listing \`$EXTENSION\`, newest per target:"
  for t in $TARGETS; do
    newest="$(newest_for "$t" "$listing")"
    say "  - \`$t\`: \`${newest:-not listed}\`"
  done

  VERDICT=1
  if [ -z "$listing" ]; then
    REASON="$EXTENSION is not listed on $name: check the listing and the publisher account, then re-run marketplace-publish.yml on v$VERSION."
  elif [ -n "$missing" ]; then
    REASON="$VERSION is not listed within ${WAIT}s on $name for:$missing. Re-run marketplace-publish.yml on v$VERSION (it is idempotent)."
  elif [ -n "$behind" ]; then
    REASON="the latest release is $VERSION, but on $name ${behind#; }: re-run marketplace-publish.yml on v$VERSION."
  else
    VERDICT=0
  fi
  if [ "$VERDICT" -eq 1 ]; then
    say "- verdict: BEHIND. $REASON"
    echo "::error::$REASON" >&2
  elif [ "$RELEASE_MODE" -eq 1 ]; then
    say "- verdict: listed"
  else
    CURRENT=1
    say "- verdict: current"
  fi
}

# --- One issue per channel -------------------------------------------------------
# Opened (or its body refreshed) while VERDICT is 1, closed once CURRENT is 1.
sync_issue() {
  local ISSUE_TITLE="Release channel behind: $CHANNEL" EXISTING BODY
  [ -n "$ISSUE_REPO" ] || return 0
  if ! EXISTING="$(gh api "repos/$ISSUE_REPO/issues?state=open&per_page=100" |
    jq -r --arg t "$ISSUE_TITLE" '[.[] | select(.pull_request == null) | select(.title == $t)][0].number // empty')"; then
    echo "ERROR: cannot list open issues on $ISSUE_REPO" >&2
    exit 2
  fi
  if [ "$VERDICT" -eq 1 ]; then
    BODY="The \`$CHANNEL\` channel does not serve the latest release.

$SUMMARY
Detected by \`scripts/verify-release-channels.sh --channel $CHANNEL\` (#1874, #1769). This issue is refreshed on every watchdog run while the channel is behind and closed automatically once it is current."
    if [ -n "$EXISTING" ]; then
      gh issue edit "$EXISTING" -R "$ISSUE_REPO" --body "$BODY" >/dev/null || { echo "ERROR: cannot update issue #$EXISTING" >&2; exit 2; }
      say "- refreshed issue $ISSUE_REPO#$EXISTING"
    else
      gh issue create -R "$ISSUE_REPO" --title "$ISSUE_TITLE" --body "$BODY" >/dev/null || { echo "ERROR: cannot open an issue on $ISSUE_REPO" >&2; exit 2; }
      say "- opened issue \"$ISSUE_TITLE\" on $ISSUE_REPO"
    fi
  elif [ -n "$EXISTING" ] && [ "$CURRENT" -eq 1 ]; then
    gh issue close "$EXISTING" -R "$ISSUE_REPO" --comment "The \`$CHANNEL\` channel now serves $VERSION." >/dev/null || { echo "ERROR: cannot close issue #$EXISTING" >&2; exit 2; }
    say "- closed issue $ISSUE_REPO#$EXISTING: channel is current"
  fi
}

CURRENT=0

# --- The plugin channel: where the stable branch points --------------------------
if [ "$CHANNEL" = "claude-plugin" ]; then
  if ! WANT="$(gh api "repos/$SOURCE/commits/v$VERSION" | jq -r '.sha // empty')" || [ -z "$WANT" ]; then
    echo "ERROR: cannot resolve tag v$VERSION on $SOURCE" >&2
    exit 2
  fi
  if ! SERVED="$(gh api "repos/$SOURCE/commits/heads/stable" | jq -r '.sha // empty')" || [ -z "$SERVED" ]; then
    echo "ERROR: cannot read the stable branch of $SOURCE" >&2
    exit 2
  fi
  say "- release: \`$VERSION\` at \`$WANT\` ($SOURCE)"
  say "- \`stable\` serves: \`$SERVED\`"
  if [ "$SERVED" = "$WANT" ]; then
    VERDICT=0
    CURRENT=1
    say "- verdict: current"
  else
    VERDICT=1
    REASON="stable is at $SERVED but the latest release v$VERSION is $WANT, so plugin updates stop there: fast-forward it (gh api -X PATCH repos/$SOURCE/git/refs/heads/stable -f sha=$WANT -F force=false)."
    say "- verdict: BEHIND. $REASON"
    echo "::error::$REASON" >&2
  fi
  sync_issue
  exit "$VERDICT"
fi

if [ "$CHANNEL" != "homebrew" ]; then
  check_listing
  sync_issue
  exit "$VERDICT"
fi

# --- The channel: what the tap's default branch actually serves ------------------
if ! CASK="$(gh api -H "Accept: application/vnd.github.raw" "repos/$TAP/contents/Casks/nightgauge.rb?ref=main")"; then
  echo "ERROR: cannot read Casks/nightgauge.rb on $TAP main" >&2
  exit 2
fi
SERVED="$(sed -n 's/^[[:space:]]*version "\([^"]*\)".*/\1/p' <<<"$CASK" | sed -n 1p)"
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
VERDICT=0
if [ "$SERVED" = "$VERSION" ]; then
  CURRENT=1
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

sync_issue
exit "$VERDICT"
