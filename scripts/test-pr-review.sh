#!/usr/bin/env bash
# Regression tests for the PR review gate and renderer
# (`scripts/pr-review.mjs`) — the model-free halves of pr-review.yml.
#
# The renderer is the gate between the read-only model job and the job that
# posts. Each reject case plants one shape it exists to refuse and asserts the
# exit code goes red; the valid fixture proves it is not simply "always 1".
# Every case runs the real CLI as a subprocess, so the exit codes and outputs
# the workflow branches on are what is asserted.
#
# Run: bash scripts/test-pr-review.sh
# Also run by scripts/ci-local.sh.

# shellcheck disable=SC2016 # jq programs and literal backticks are meant to stay unexpanded
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

CLI="scripts/pr-review.mjs"
PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

expect() {
  local name="$1" want="$2" got="$3"
  if [ "$got" = "$want" ]; then
    PASS=$((PASS + 1))
    echo "PASS: $name"
  else
    FAIL=$((FAIL + 1))
    echo "FAIL: $name (got $got, want $want)"
  fi
}

VALID='{
  "schema": 1,
  "findings": [
    {"pass": "correctness", "file": "internal/forge/pr.go", "line": 42,
     "claim": "Nil author dereferenced.", "severity": "major", "fix": "Check for nil."},
    {"pass": "security", "file": "scripts/x.sh", "line": null,
     "claim": "Unquoted input.", "severity": "blocker", "fix": "Quote it."}
  ]
}'
fixture() {
  printf '%s' "$VALID" | jq "$2" > "$TMP/$1.json"
  echo "$TMP/$1.json"
}
render() {
  # render <file> -> stdout of render as an outside author
  node "$CLI" render --author octocat --reviewer 'github-actions[bot]' "$1"
}
check() {
  # check <name> <want-exit> <file>
  render "$3" > /dev/null 2>&1
  expect "$1" "$2" $?
}

# --- render: accept ---------------------------------------------------------
check "valid findings render" 0 "$(fixture valid .)"
check "empty findings render" 0 "$(fixture empty '.findings = []')"

# --- render: reject ---------------------------------------------------------
check "unknown top-level key" 1 "$(fixture topkey '.extra = 1')"
check "wrong schema version" 1 "$(fixture schema '.schema = 2')"
check "unknown finding key" 1 "$(fixture fkey '.findings[0].url = "x"')"
check "missing finding key" 1 "$(fixture nofix 'del(.findings[0].fix)')"
check "bad severity" 1 "$(fixture sev '.findings[0].severity = "critical"')"
check "bad pass" 1 "$(fixture pass '.findings[0].pass = "style"')"
check "absolute path" 1 "$(fixture abs '.findings[0].file = "/etc/passwd"')"
check "dot-dot path" 1 "$(fixture dotdot '.findings[0].file = "a/../../b"')"
check "line zero" 1 "$(fixture line0 '.findings[0].line = 0')"
check "line string" 1 "$(fixture linestr '.findings[0].line = "4"')"
check "multi-line claim" 1 "$(fixture claimnl '.findings[0].claim = "a\nb"')"
check "oversize claim" 1 "$(fixture claimbig '.findings[0].claim = ("x" * 301)')"
check "oversize fix" 1 "$(fixture fixbig '.findings[0].fix = ("x" * 1001)')"
check "anthropic key shape" 1 "$(fixture skant '.findings[0].fix = "use sk-ant-api03-abc"')"
check "github token shape" 1 "$(fixture ghp '.findings[0].claim = "token ghp_abcdef"')"
check "fine-grained pat shape" 1 "$(fixture pat '.findings[0].fix = "github_pat_11ABC"')"
check "over finding cap" 1 "$(fixture cap '.findings = [range(31) as $i | .findings[0]]')"
printf 'not json' > "$TMP/garbage.json"
check "not JSON" 1 "$TMP/garbage.json"

node "$CLI" render --author octocat --reviewer x "$TMP/missing.json" > /dev/null 2>&1
expect "unreadable file is a usage error" 2 $?

# --- render: identity refusal (AC: reviewer is never the author) -------------
node "$CLI" render --author 'nightgauge-app[bot]' --reviewer 'nightgauge-app[bot]' \
  "$(fixture same .)" > "$TMP/same.out" 2>/dev/null
expect "reviewer == author refuses" 3 $?
expect "refusal prints no comment" 0 "$(wc -c < "$TMP/same.out" | tr -d ' ')"
node "$CLI" render --author 'Octocat' --reviewer 'octocat' "$(fixture case .)" > /dev/null 2>&1
expect "identity check ignores case" 3 $?

# --- render: output shape ---------------------------------------------------
OUT="$(render "$(fixture valid .)")"
expect "starts with marker" "<!-- nightgauge:pr-review -->" "${OUT%%$'\n'*}"
FIRST="$(sed -nE '/^- \*\*/{s/^- \*\*([a-z]+)\*\*.*/\1/;p;q;}' <<<"$OUT")"
expect "blocker ranked first" "blocker" "$FIRST"
expect "file-level finding has no line" 1 "$(printf '%s\n' "$OUT" | grep -c '`scripts/x.sh` ')"

NITS="$(fixture nits '.findings = [range(8) as $i | {pass: "correctness",
  file: "a.go", line: ($i + 1), claim: "n", severity: "nitpick", fix: "f"}]')"
OUT="$(render "$NITS")"
expect "nitpick cap shows 5" 5 "$(printf '%s\n' "$OUT" | grep -c '^- \*\*nitpick')"
expect "omitted nitpicks counted" 1 "$(printf '%s\n' "$OUT" | grep -c '^3 more nitpicks omitted\.$')"

OUT="$(render "$(fixture empty '.findings = []')")"
expect "empty review says so" 1 "$(printf '%s\n' "$OUT" | grep -c '^No findings')"

INJ="$(fixture inj '.findings[0].claim = "<!-- nightgauge:pr-review --> ping @octocat"')"
OUT="$(render "$INJ")"
expect "forged marker neutralized" 1 "$(printf '%s\n' "$OUT" | grep -c '<!-- nightgauge:pr-review')"
expect "mention neutralized" 0 "$(printf '%s\n' "$OUT" | grep -c '@octocat')"

# --- gate -------------------------------------------------------------------
pr() {
  # pr <name> <jq-filter>
  jq -n '{author: {login: "octocat"}, isDraft: false, isCrossRepository: false, state: "OPEN"}' \
    | jq "$2" > "$TMP/pr-$1.json"
  echo "$TMP/pr-$1.json"
}
gate() {
  # gate <pr-file> <event> [authors] -> the run= value
  node "$CLI" gate --pr-json "$1" --event "$2" --pipeline-authors "${3:-}" --has-key "${4:-true}" \
    | sed -n 's/^run=//p'
}

expect "gate: outside author runs" true "$(gate "$(pr ok .)" pull_request 'nightgauge-app[bot]')"
expect "gate: pipeline identity skipped" false \
  "$(gate "$(pr bot '.author.login = "nightgauge-app[bot]"')" pull_request 'x, nightgauge-app[bot]')"
expect "gate: pipeline branch name is not identity" true \
  "$(gate "$(pr br '.headRefName = "feat/12-x"')" pull_request 'nightgauge-app[bot]')"
expect "gate: empty author list reviews everyone" true \
  "$(gate "$(pr ok2 '.author.login = "nightgauge-app[bot]"')" pull_request '')"
expect "gate: dependabot skipped" false "$(gate "$(pr dep '.author.login = "dependabot[bot]"')" pull_request)"
expect "gate: draft skipped" false "$(gate "$(pr draft '.isDraft = true')" pull_request)"
expect "gate: fork skipped on pull_request" false "$(gate "$(pr fork '.isCrossRepository = true')" pull_request)"
expect "gate: fork reviewed on dispatch" true "$(gate "$(pr fork2 '.isCrossRepository = true')" workflow_dispatch)"
expect "gate: closed skipped" false "$(gate "$(pr closed '.state = "CLOSED"')" workflow_dispatch)"
expect "gate: no key skipped" false "$(gate "$(pr nokey .)" pull_request '' false)"

GH_OUT="$TMP/gh-output"
GITHUB_OUTPUT="$GH_OUT" node "$CLI" gate --pr-json "$(pr out .)" --event pull_request > /dev/null
expect "gate: writes GITHUB_OUTPUT" 1 "$(grep -c '^reason=no ANTHROPIC_API_KEY' "$GH_OUT")"

node "$CLI" gate --event pull_request > /dev/null 2>&1
expect "gate: missing --pr-json is a usage error" 2 $?

echo
echo "pr-review: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
