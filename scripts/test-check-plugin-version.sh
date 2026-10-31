#!/usr/bin/env bash
#
# test-check-plugin-version.sh — regression suite for
# scripts/check-plugin-version.sh (#2724). Every arm runs a copy of the gate in
# a throwaway git repository, so the suite never depends on this clone's tags.
#
# Run: bash scripts/test-check-plugin-version.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$REPO_ROOT/scripts/check-plugin-version.sh"
PASS=0
FAIL=0
TMP=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

set_version() {
  printf '{"name": "nightgauge", "version": "%s"}\n' "$1" \
    >"$TMP/claude-plugins/nightgauge/.claude-plugin/plugin.json"
}

commit() {
  git -C "$TMP" add -A
  git -C "$TMP" -c user.name=t -c user.email=t@t commit -qm "$1"
}

# fixture: a valid repository with v1.0.0 tagged at plugin version 1.0.0.
fixture() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  TMP="$(mktemp -d)"
  mkdir -p "$TMP/scripts" "$TMP/.claude-plugin" \
    "$TMP/claude-plugins/nightgauge/.claude-plugin" "$TMP/claude-plugins/nightgauge/skills/a"
  cp "$GATE" "$TMP/scripts/"
  echo '{"plugins": [{"name": "nightgauge"}]}' >"$TMP/.claude-plugin/marketplace.json"
  printf -- '---\nname: a\ndescription: |\n  Fine: here, a block scalar.\n---\nbody\n' \
    >"$TMP/claude-plugins/nightgauge/skills/a/SKILL.md"
  set_version 1.0.0
  git -C "$TMP" init -q -b main
  commit one
  git -C "$TMP" tag v1.0.0
}

touch_plugin() { echo x >"$TMP/claude-plugins/nightgauge/skills/a/extra.md"; }

# expect <description> <exit> <stderr substring> [gate args...]
expect() {
  local desc="$1" want="$2" needle="${3:-}" out rc
  shift 3
  [ "${1:-}" = "--" ] && shift
  out="$(bash "$TMP/scripts/check-plugin-version.sh" "$@" 2>&1)"
  rc=$?
  if [ "$rc" -eq "$want" ] && { [ -z "$needle" ] || [[ "$out" == *"$needle"* ]]; }; then
    echo "PASS: $desc"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $desc (exit $rc, want $want): $out"
    FAIL=$((FAIL + 1))
  fi
}

fixture
expect "a consistent repository passes" 0 ""

fixture
echo '{"plugins": [{"name": "nightgauge", "version": "1.0.0"}]}' >"$TMP/.claude-plugin/marketplace.json"
expect "a marketplace version is a second source" 1 "sets a version"

fixture
printf '{"name": "nightgauge"}\n' >"$TMP/claude-plugins/nightgauge/.claude-plugin/plugin.json"
expect "plugin.json must carry a version" 1 "must be X.Y.Z"

fixture
printf -- '---\nname: a\ndescription: User-invoked: the user types /a.\n---\n' \
  >"$TMP/claude-plugins/nightgauge/skills/a/SKILL.md"
expect "the ': ' frontmatter that shipped in 1.24.0 fails" 1 "not valid YAML"

fixture
rm -rf "$TMP/claude-plugins/nightgauge/skills/a"
expect "no skills could not run" 2 "no skills"

# The bug: plugin files changed across a release and the version did not.
fixture
touch_plugin
commit two
expect "changed plugin with the same version fails" 1 "still 1.0.0" --tag v1.1.0
expect "an rc finds the same defect" 1 "still 1.0.0" --tag v1.1.0-rc.1

fixture
touch_plugin
set_version 1.1.0
commit two
expect "changed plugin with a bump passes" 0 "" --tag v1.1.0

fixture
echo x >"$TMP/other.txt"
commit two
expect "a release that left the plugin alone may keep its version" 0 "" --tag v1.1.0

fixture
set_version 0.9.0
commit two
expect "the version never goes down" 1 "went down" --tag v1.1.0

fixture
expect "no lower stable tag passes" 0 "" --tag v1.0.0

# Only stable tags are a baseline: an rc of the same release is not one.
fixture
touch_plugin
commit two
git -C "$TMP" tag v1.1.0-rc.1
expect "an rc tag is not the baseline" 1 "since v1.0.0" --tag v1.1.0

# The pushed tag's tree is what is judged, not the checked-out one.
fixture
touch_plugin
commit two
git -C "$TMP" tag v1.1.0
set_version 1.1.0
commit three
expect "a pushed tag is read from its own tree" 1 "still 1.0.0" --tag v1.1.0

fixture
expect "a malformed tag is a usage error" 2 "--tag expects" --tag latest

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
