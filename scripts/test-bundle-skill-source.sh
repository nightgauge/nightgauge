#!/usr/bin/env bash
# Regression tests for docker/clean-install/bundle-skill-source.sh (#3036).
#
# The G1 bundle row passes only when every "[skillRunner] skill_path=" line in
# the slot output channels names a file under the installed bundle's
# dist/skills, whether or not any tool call read it. Drives the WORKING-TREE
# copy of the script.
#
# Run: bash scripts/test-bundle-skill-source.sh
# Also run by scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

SCRIPT="$PWD/docker/clean-install/bundle-skill-source.sh"
COUNTER="$PWD/docker/clean-install/count-bundle-skill-reads.sh"
PASS=0
FAIL=0
TMP=""

cleanup() { [ -n "$TMP" ] && rm -rf "$TMP"; return 0; }
trap cleanup EXIT

ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }

# expect <jq_filter> <desc> <logs_dir>: the filter must yield true on the output.
expect() {
  local got
  got="$("$SCRIPT" "$3" "$BUNDLE" 2>/dev/null)"
  if [ "$(jq -r "$1" <<<"$got" 2>/dev/null)" = "true" ]; then ok "$2"; else bad "$2 (got '${got}')"; fi
}

# slot <logs_dir>: path of a slot channel log, shaped as VS Code persists it.
slot() {
  local d="$1/20261106T000000/window1/exthost/output_logging_20261106T000000"
  mkdir -p "$d"
  printf '%s' "$d/1-Nightgauge Slot 1 (o_r#1).log"
}

# frame: the slot banner, a stage header and an error line around stdin.
frame() {
  echo "=== Nightgauge Slot 1 — o/r#1 ==="
  echo "--- Stage: feature-dev ---"
  cat
  echo "[ERROR] something on stderr"
}

TMP="$(mktemp -d)"
BUNDLE="/root/.vscode/extensions/nightgauge.nightgauge-1.0.0/dist/skills/"
OLD="/root/.vscode/extensions/nightgauge.nightgauge-1.0.0/dist/skills-old/"

echo "▶ bundle-skill-source"

# Fixture A: every skill_path under the bundle, one behind a timestamp prefix.
{
  echo "[skillRunner] skill_path=${BUNDLE}nightgauge-feature-dev/SKILL.md"
  echo "2026-11-06 00:00:00.000 [info] [skillRunner] skill_path=${BUNDLE}nightgauge-pr-create/SKILL.md"
} | frame > "$(slot "$TMP/a")"
expect '.pass == true and (.skill_paths | length) == 2 and .outside_bundle == []' \
  "every skill_path under the bundle passes" "$TMP/a"

# Fixture B: one skill_path from a repo checkout, one from a sibling dir.
{
  echo "[skillRunner] skill_path=${BUNDLE}nightgauge-feature-dev/SKILL.md"
  echo "[skillRunner] skill_path=/repo/skills/nightgauge-feature-validate/SKILL.md"
  echo "[skillRunner] skill_path=${OLD}nightgauge-pr-create/SKILL.md"
} | frame > "$(slot "$TMP/b")"
expect '.pass == false and .outside_bundle == ["/repo/skills/nightgauge-feature-validate/SKILL.md", "'"${OLD}"'nightgauge-pr-create/SKILL.md"]' \
  "a skill_path outside the bundle fails and is named" "$TMP/b"

# Fixture C: a slot log with no skill_path line.
jq -cn --arg p "$BUNDLE" '{type:"assistant",message:{content:[{type:"tool_use",name:"Read",
  input:{file_path:($p + "x/SKILL.md")}}]}}' | frame > "$(slot "$TMP/c")"
expect '.pass == false and .skill_paths == []' "a slot log with no skill_path fails" "$TMP/c"

# Fixture D: the skill is rendered into the prompt, so no tool call reads it.
{
  echo "[skillRunner] skill_path=${BUNDLE}nightgauge-feature-dev/SKILL.md"
  jq -cn '{type:"result",subtype:"success"}'
} | frame > "$(slot "$TMP/d")"
expect '.pass == true' "zero tool-call reads with every skill_path under the bundle passes" "$TMP/d"
if [ "$("$COUNTER" "$TMP/d" "$BUNDLE")" = "0" ]; then
  ok "the read counter reports 0 for the same fixture"
else
  bad "the read counter reports 0 for the same fixture"
fi

# Fixture E: a skill_path line in a non-slot channel is ignored.
E="$TMP/e/20261106T000000/window1/exthost/output_logging_20261106T000000"
mkdir -p "$E"
echo "[skillRunner] skill_path=${BUNDLE}x/SKILL.md" > "$E/2-Nightgauge.log"
expect '.pass == false and .skill_paths == []' "a non-slot channel is ignored" "$TMP/e"

mkdir -p "$TMP/empty"
expect '.pass == false' "an empty logs dir fails" "$TMP/empty"
expect '.pass == false' "an absent logs dir fails" "$TMP/absent"

echo ""
echo "bundle-skill-source: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
