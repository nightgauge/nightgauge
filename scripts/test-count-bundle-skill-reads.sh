#!/usr/bin/env bash
# Regression tests for docker/clean-install/count-bundle-skill-reads.sh (#2722).
#
# The G1 bundle-read row must count every tool call that reads the installed
# bundle's dist/skills (Read, Grep/Glob, a Bash command naming the path) —
# never a transcript line that merely mentions the path. Drives the
# WORKING-TREE copy of the script.
#
# Run: bash scripts/test-count-bundle-skill-reads.sh
# Also run by scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

SCRIPT="$PWD/docker/clean-install/count-bundle-skill-reads.sh"
PASS=0
FAIL=0
TMP=""

cleanup() { [ -n "$TMP" ] && rm -rf "$TMP"; return 0; }
trap cleanup EXIT

ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }

# expect <want> <desc> <projects_dir>
expect() {
  local got
  got="$("$SCRIPT" "$3" "$BUNDLE" 2>/dev/null)"
  if [ "$got" = "$1" ]; then ok "$2"; else bad "$2 (want $1, got '${got}')"; fi
}

TMP="$(mktemp -d)"
BUNDLE="/root/.vscode/extensions/nightgauge.nightgauge-1.0.0/dist/skills/"

echo "▶ count-bundle-skill-reads"

# Fixture A: the path is mentioned everywhere, but no tool call reads it.
A="$TMP/a/proj"
mkdir -p "$A"
{
  jq -cn --arg p "$BUNDLE" '{type:"system",message:{content:("Skill directory: " + $p)}}'
  jq -cn --arg p "$BUNDLE" '{type:"user",message:{content:[{type:"text",text:("read " + $p + "x/SKILL.md")}]}}'
  jq -cn --arg p "$BUNDLE" '{permissions:{additionalDirectories:[$p]}}'
  jq -cn --arg p "$BUNDLE" '{type:"user",message:{content:[{type:"tool_result",content:($p + "a\n" + $p + "b")}]}}'
  jq -cn '{type:"assistant",message:{content:[{type:"tool_use",name:"Read",
    input:{file_path:"/root/.vscode/extensions/nightgauge.nightgauge-1.0.0/dist/skills-old/x/SKILL.md"}}]}}'
  jq -cn '{type:"assistant",message:{content:[{type:"tool_use",name:"Bash",
    input:{command:"cat /root/.vscode/extensions/nightgauge.nightgauge-1.0.0/dist/skills-old/x"}}]}}'
  jq -cn --arg p "$BUNDLE" '{type:"assistant",message:{content:[{type:"tool_use",name:"Write",
    input:{file_path:($p + "x/SKILL.md")}}]}}'
} > "$A/session.jsonl"
expect 0 "mention-only transcript counts no reads" "$TMP/a"

# Fixture B: one real Read under the bundle, in a nested project dir.
B="$TMP/b/proj/sub"
mkdir -p "$B"
jq -cn --arg p "$BUNDLE" '{type:"assistant",message:{content:[{type:"tool_use",name:"Read",
  input:{file_path:($p + "nightgauge-feature-dev/SKILL.md")}}]}}' > "$B/session.jsonl"
expect 1 "a Read under the bundle counts once" "$TMP/b"

# Fixture C: a stage reads its includes with Bash and Grep, never Read (the
# shape a real feature-validate stage produced).
C="$TMP/c/proj"
mkdir -p "$C"
{
  jq -cn --arg p "$BUNDLE" '{type:"assistant",message:{content:[{type:"tool_use",name:"Bash",
    input:{command:("sed -n 1,40p " + $p + "nightgauge-feature-validate/_includes/ci.md")}}]}}'
  jq -cn --arg p "$BUNDLE" '{type:"assistant",message:{content:[{type:"tool_use",name:"Grep",
    input:{pattern:"2.7.3",path:($p + "nightgauge-feature-validate/_includes")}}]}}'
} > "$C/session.jsonl"
expect 2 "Bash and Grep reads under the bundle count" "$TMP/c"

mkdir -p "$TMP/empty"
expect 0 "an empty projects dir counts no reads" "$TMP/empty"
expect 0 "an absent projects dir counts no reads" "$TMP/absent"

echo ""
echo "count-bundle-skill-reads: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
