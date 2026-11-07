#!/usr/bin/env bash
# Regression tests for docker/clean-install/count-bundle-skill-reads.sh (#2722, #3032).
#
# The G1 bundle-read row must count every tool call that reads the installed
# bundle's dist/skills (Read, Grep/Glob, a Bash command naming the path) —
# never a log line that merely mentions the path. Drives the
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

# expect <want> <desc> <logs_dir>
expect() {
  local got
  got="$("$SCRIPT" "$3" "$BUNDLE" 2>/dev/null)"
  if [ "$got" = "$1" ]; then ok "$2"; else bad "$2 (want $1, got '${got}')"; fi
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

echo "▶ count-bundle-skill-reads"

# Fixture A: the path is mentioned everywhere, but no tool call reads it.
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
} | frame > "$(slot "$TMP/a")"
expect 0 "mention-only slot log counts no reads" "$TMP/a"

# Fixture B: one real Read under the bundle.
jq -cn --arg p "$BUNDLE" '{type:"assistant",message:{content:[{type:"tool_use",name:"Read",
  input:{file_path:($p + "nightgauge-feature-dev/SKILL.md")}}]}}' | frame > "$(slot "$TMP/b")"
expect 1 "a Read under the bundle counts once" "$TMP/b"

# Fixture C: a stage reads its includes with Bash and Grep, never Read.
{
  jq -cn --arg p "$BUNDLE" '{type:"assistant",message:{content:[{type:"tool_use",name:"Bash",
    input:{command:("sed -n 1,40p " + $p + "nightgauge-feature-validate/_includes/ci.md")}}]}}'
  jq -cn --arg p "$BUNDLE" '{type:"assistant",message:{content:[{type:"tool_use",name:"Grep",
    input:{pattern:"2.7.3",path:($p + "nightgauge-feature-validate/_includes")}}]}}'
} | frame > "$(slot "$TMP/c")"
expect 2 "Bash and Grep reads under the bundle count" "$TMP/c"

# Fixture D: one stdout chunk ended mid-line, so the extension's per-chunk
# appendLine split the tool_use line across two log lines.
LINE="$(jq -cn --arg p "$BUNDLE" '{type:"assistant",message:{content:[{type:"tool_use",name:"Read",
  input:{file_path:($p + "nightgauge-feature-dev/_includes/x.md")}}]}}')"
{ printf '%s\n%s\n' "${LINE:0:40}" "${LINE:40}"; } | frame > "$(slot "$TMP/d")"
expect 1 "a tool_use line split across log lines counts once" "$TMP/d"

# Fixture E: a stage that made no tool calls.
jq -cn '{type:"result",subtype:"success"}' | frame > "$(slot "$TMP/e")"
expect 0 "a slot log with no tool calls counts no reads" "$TMP/e"

# Fixture F: a bundle Read in a non-slot channel (the extension logger).
F="$TMP/f/20261106T000000/window1/exthost/output_logging_20261106T000000"
mkdir -p "$F"
jq -cn --arg p "$BUNDLE" '{type:"assistant",message:{content:[{type:"tool_use",name:"Read",
  input:{file_path:($p + "x/SKILL.md")}}]}}' > "$F/2-Nightgauge.log"
expect 0 "a non-slot channel counts no reads" "$TMP/f"

mkdir -p "$TMP/empty"
expect 0 "an empty logs dir counts no reads" "$TMP/empty"
expect 0 "an absent logs dir counts no reads" "$TMP/absent"

echo ""
echo "count-bundle-skill-reads: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
