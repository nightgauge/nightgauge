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
expect "gate: writes GITHUB_OUTPUT" 1 "$(grep -c '^reason=no CLAUDE_CODE_OAUTH_TOKEN' "$GH_OUT")"

node "$CLI" gate --event pull_request > /dev/null 2>&1
expect "gate: missing --pr-json is a usage error" 2 $?

# The review step's turn limit. The real step runs against stub claude and npm
# binaries: what differs between the arms is the CLI's result, and the assertions
# read the step's exit code, annotation and job summary.
WORKFLOW=".github/workflows/pr-review.yml"
STEP_SELECT='.jobs.review.steps[] | select(.name == "Run the review passes")'
if ! command -v yq > /dev/null 2>&1; then
  echo "HARNESS ERROR: yq is not installed; the review step could not run"
  exit 1
fi
STEP_RUN="$(yq "$STEP_SELECT | .run" "$WORKFLOW")"
MAX_TURNS="$(yq "$STEP_SELECT | .env.REVIEW_MAX_TURNS" "$WORKFLOW")"
if [ -z "$STEP_RUN" ] || [ "$STEP_RUN" = "null" ] || [ "$MAX_TURNS" = "null" ]; then
  echo "HARNESS ERROR: could not extract the review step from $WORKFLOW"
  exit 1
fi

expect "limit: the turn limit is 80" 80 "$MAX_TURNS"
expect "trigger: manual only" "workflow_dispatch" "$(yq '.on | keys | .[]' "$WORKFLOW")"
expect "billing: no pay-per-use key reaches the model" 0 \
  "$(yq "$STEP_SELECT | .env | keys | .[]" "$WORKFLOW" | grep -c ANTHROPIC_API_KEY)"
expect "limit: one --max-turns in the workflow" 1 "$(grep -c -e '--max-turns' "$WORKFLOW")"

STUBS="$TMP/stubs"
mkdir -p "$STUBS"
printf '#!/bin/sh\nexit 0\n' > "$STUBS/npm"
cat > "$STUBS/claude" <<'STUB'
#!/bin/sh
cat > /dev/null
printf '%s' "$STUB_OUTPUT"
exit "$STUB_STATUS"
STUB
chmod +x "$STUBS/npm" "$STUBS/claude"

run_step() {
  # run_step <case> <stub stdout> <stub exit> -> sets STEP_STATUS; output in $TMP/<case>.*
  local name="$1" dir="$TMP/step-$1"
  mkdir -p "$dir"
  : > "$dir/summary"
  PATH="$STUBS:$PATH" RUNNER_TEMP="$dir" GITHUB_STEP_SUMMARY="$dir/summary" \
    FINDINGS_FILE="$dir/findings.json" REVIEW_MAX_TURNS="$MAX_TURNS" \
    STUB_OUTPUT="$2" STUB_STATUS="$3" bash -c "$STEP_RUN" > "$dir/log" 2>&1
  STEP_STATUS=$?
}
count() { grep -c -e "$1" "$2"; }

LIMIT_JSON='{"type":"result","subtype":"error_max_turns","is_error":true,"result":null,
  "errors":["Reached maximum number of turns (80)"]}'

run_step limit1 "$LIMIT_JSON" 1
expect "limit reached: step is red" 1 "$STEP_STATUS"
expect "limit reached: annotation names the limit" 1 \
  "$(count '^::error title=Review did not finish::.*did not finish.*limit of 80 turns' "$TMP/step-limit1/log")"
expect "limit reached: summary names the limit" 1 \
  "$(count 'did not finish: it reached the limit of 80 turns' "$TMP/step-limit1/summary")"

run_step limit0 "$LIMIT_JSON" 0
expect "limit reached, CLI exit 0: step is still red" 1 "$STEP_STATUS"
expect "limit reached, CLI exit 0: annotation" 1 \
  "$(count '^::error title=Review did not finish' "$TMP/step-limit0/log")"

run_step ok '{"type":"result","subtype":"success","is_error":false,"result":"All clear."}' 0
expect "success: step is green" 0 "$STEP_STATUS"
expect "success: closing message stays in the log" 1 "$(count '^All clear\.$' "$TMP/step-ok/log")"
expect "success: no annotation" 0 "$(count '::error' "$TMP/step-ok/log")"
expect "success: empty summary" 0 "$(wc -c < "$TMP/step-ok/summary" | tr -d ' ')"

run_step crash '' 1
expect "other failure: step keeps the CLI's exit" 1 "$STEP_STATUS"
expect "other failure: no did-not-finish text" 0 \
  "$(cat "$TMP/step-crash/log" "$TMP/step-crash/summary" | grep -c 'did not finish')"

run_step other 'Reached max turns, said the model' 3
expect "other text: exit code preserved" 3 "$STEP_STATUS"
expect "other text: not read as the limit" 0 \
  "$(cat "$TMP/step-other/log" "$TMP/step-other/summary" | grep -c 'did not finish')"

echo
echo "pr-review: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
