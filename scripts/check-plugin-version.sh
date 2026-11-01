#!/usr/bin/env bash
#
# check-plugin-version.sh — the Claude plugin's version ↔ release contract
# (docs/GIT_WORKFLOW.md § Changelog, #2724).
#
# Claude Code decides whether an installed plugin has an update by comparing
# the plugin's version, so a release that changes the plugin without changing
# the version delivers nothing. plugin.json sat at 1.24.0 through every release
# from 2026-08-08 onward, and nothing mechanical noticed.
#
# Modes:
#   check-plugin-version.sh
#       plugin.json has an X.Y.Z version; the marketplace entry has none, so
#       there is one source of truth; every plugin skill's frontmatter parses
#       as strict YAML (`claude plugin validate` is absent in CI, and the CLI
#       drops an unparseable skill's whole frontmatter). Needs mikefarah yq.
#   check-plugin-version.sh --tag vX.Y.Z[-rc.N]
#       The first two arms, then compare the tag with the highest stable tag
#       below it: a changed plugin needs a different version, and the version
#       never goes down. A first release, with stable tags but none below it,
#       passes; a clone with no stable tag at all could not run. An unpushed tag
#       is read as HEAD, so this works before tagging. Needs full history and
#       tags.
#
# Exit codes: 0 contract holds; 1 a violation was found (each named on stderr);
# 2 usage error, or the check could not run.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 2

PLUGIN_DIR="claude-plugins/nightgauge"
PLUGIN_JSON="$PLUGIN_DIR/.claude-plugin/plugin.json"
MARKETPLACE=".claude-plugin/marketplace.json"
SEMVER_RE='^[0-9]+\.[0-9]+\.[0-9]+$'
GATE_TAG=""

usage() {
  sed -n '2,/^set -uo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --tag) GATE_TAG="${2:-}"; shift 2 || usage ;;
    -h|--help) usage ;;
    *) echo "check-plugin-version: unknown argument: $1" >&2; usage ;;
  esac
done

# HARNESS ERROR: ci-local.sh reports the step as INFRASTRUCTURE, not a finding.
cannot_run() { echo "HARNESS ERROR: check-plugin-version: $1" >&2; exit 2; }

FAIL=0
violation() { echo "VIOLATION: $1" >&2; FAIL=1; }

# semver_lt A B — true when X.Y.Z A sorts strictly before B. Not `sort -V`:
# this also runs on a macOS runner.
semver_lt() {
  local a1 a2 a3 b1 b2 b3
  IFS=. read -r a1 a2 a3 <<<"$1"
  IFS=. read -r b1 b2 b3 <<<"$2"
  if [ "$((10#$a1))" -ne "$((10#$b1))" ]; then [ "$((10#$a1))" -lt "$((10#$b1))" ]; return; fi
  if [ "$((10#$a2))" -ne "$((10#$b2))" ]; then [ "$((10#$a2))" -lt "$((10#$b2))" ]; return; fi
  [ "$((10#$a3))" -lt "$((10#$b3))" ]
}

# --- One version, in plugin.json --------------------------------------------
[ -r "$PLUGIN_JSON" ] || cannot_run "cannot read $PLUGIN_JSON"
[ -r "$MARKETPLACE" ] || cannot_run "cannot read $MARKETPLACE"
VERSION="$(jq -r '.version // empty' "$PLUGIN_JSON" 2>/dev/null)" ||
  cannot_run "$PLUGIN_JSON is not JSON"
[[ "$VERSION" =~ $SEMVER_RE ]] ||
  violation "$PLUGIN_JSON version must be X.Y.Z, got '${VERSION:-<none>}'"
ENTRIES="$(jq -r '.plugins[] | select(.version != null) | .name' "$MARKETPLACE" 2>/dev/null)" ||
  cannot_run "$MARKETPLACE is not JSON"
if [ -n "$ENTRIES" ]; then
  violation "$MARKETPLACE sets a version for: $ENTRIES (plugin.json is the only source)"
fi

if [ -z "$GATE_TAG" ]; then
  # --- Every skill's frontmatter parses ---------------------------------------
  # Not under --tag: the release runners are macOS, with no yq, and the same
  # tree already passed this arm on the pull request.
  YQ_VERSION="$(yq --version 2>/dev/null)" || YQ_VERSION=""
  [[ "$YQ_VERSION" == *mikefarah* ]] ||
    cannot_run "mikefarah yq is required for the frontmatter check"
  SKILLS=0
  for f in "$PLUGIN_DIR"/skills/*/SKILL.md; do
    [ -f "$f" ] || continue
    SKILLS=$((SKILLS + 1))
    if ! awk 'NR == 1 { next } /^---[[:space:]]*$/ { exit } { print }' "$f" |
      yq -e '.name' >/dev/null 2>&1; then
      violation "$f: frontmatter is not valid YAML with a name (a ': ' in a plain scalar?)"
    fi
  done
  [ "$SKILLS" -gt 0 ] || cannot_run "no skills under $PLUGIN_DIR/skills (run install-agent-skills.sh --generate-only)"
else
  # --- Release gate -----------------------------------------------------------
  case "$GATE_TAG" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *) cannot_run "--tag expects vX.Y.Z[-rc.N], got '$GATE_TAG'" ;;
  esac
  BASE="${GATE_TAG#v}"
  BASE="${BASE%%-*}"
  [[ "$BASE" =~ $SEMVER_RE ]] || cannot_run "--tag expects vX.Y.Z[-rc.N], got '$GATE_TAG'"

  PREV=""
  STABLE_TAGS=0
  while IFS= read -r t; do
    v="${t#v}"
    [[ "$v" =~ $SEMVER_RE ]] || continue
    STABLE_TAGS=$((STABLE_TAGS + 1))
    semver_lt "$v" "$BASE" || continue
    if [ -z "$PREV" ] || semver_lt "$PREV" "$v"; then PREV="$v"; fi
  done < <(git tag -l 'v*')
  # A tagless clone would pass vacuously, skipping the comparison entirely.
  [ "$STABLE_TAGS" -gt 0 ] || cannot_run "no stable tag in this clone (git fetch --tags)"

  TARGET="HEAD"
  git rev-parse -q --verify "refs/tags/$GATE_TAG" >/dev/null && TARGET="refs/tags/$GATE_TAG"
  if [ -n "$PREV" ]; then
    # Read the version from the tag's tree: --tag may run from another commit.
    VERSION="$(git show "$TARGET:$PLUGIN_JSON" 2>/dev/null | jq -r '.version // empty')" ||
      cannot_run "cannot read $PLUGIN_JSON at $TARGET"
    PREV_VERSION="$(git show "v$PREV:$PLUGIN_JSON" 2>/dev/null | jq -r '.version // empty')" || PREV_VERSION=""
    git diff --quiet "v$PREV" "$TARGET" -- "$PLUGIN_DIR" .claude-plugin
    DIFF=$?
    [ "$DIFF" -le 1 ] || cannot_run "cannot diff v$PREV against $TARGET"
    [[ "$PREV_VERSION" =~ $SEMVER_RE ]] ||
      cannot_run "no X.Y.Z version in $PLUGIN_JSON at v$PREV, got '${PREV_VERSION:-<none>}'"
    if ! [[ "$VERSION" =~ $SEMVER_RE ]]; then
      violation "$PLUGIN_JSON version at $GATE_TAG must be X.Y.Z, got '${VERSION:-<none>}'"
    else
      if semver_lt "$VERSION" "$PREV_VERSION"; then
        violation "plugin version went down: $PREV_VERSION at v$PREV, $VERSION at $GATE_TAG"
      elif [ "$DIFF" -eq 1 ] && [ "$VERSION" = "$PREV_VERSION" ]; then
        violation "plugin files changed since v$PREV but the version is still $VERSION: bump $PLUGIN_JSON in the rollover PR, or installed plugins receive nothing"
      fi
    fi
  fi
fi

if [ "$FAIL" -ne 0 ]; then
  echo "check-plugin-version: FAILED — see docs/GIT_WORKFLOW.md § Changelog" >&2
  exit 1
fi
echo "check-plugin-version: OK (plugin $VERSION${GATE_TAG:+, $GATE_TAG})"
exit 0
