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
REPO_ROOT="$PWD"

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
check "long token past the fix limit still rejected" 1 \
  "$(fixture skbig '.findings[0].fix = ("x" * 990 + " sk-ant-api03-abcdefghijkl")')"
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

# --- render: over-long fields are per-finding (#2819) ----------------------
LONGFIX="$(fixture fixbig '.findings[0].fix = ("x" * 1078)')"
OUT="$(render "$LONGFIX")"
expect "long fix: renders" 0 $?
expect "long fix: both findings kept" 2 "$(printf '%s\n' "$OUT" | grep -c '^- \*\*')"
FIXLINE="$(printf '%s\n' "$OUT" | grep '^  - Fix: xxx')"
expect "long fix: ends in marker" 1 "$(printf '%s\n' "$FIXLINE" | grep -c '\[truncated\]$')"
expect "long fix: within the limit" 1 "$([ $(( ${#FIXLINE} - 9 )) -le 1000 ] && echo 1 || echo 0)"
expect "long fix: notice names the field" 1 \
  "$(printf '%s\n' "$OUT" | grep -c 'Shortened to fit: findings\[0\]\.fix (1078 characters, limit 1000)')"

OUT="$(render "$(fixture claimbig '.findings[0].claim = ("y" * 301)')")"
expect "long claim: renders" 0 $?
expect "long claim: one line, truncated" 1 \
  "$(printf '%s\n' "$OUT" | grep -c '— yyy.*\[truncated\]$')"

OUT="$(render "$(fixture pathbig '.findings[0].file = ("d/" * 206)')")"
expect "long path: renders" 0 $?
expect "long path: other finding posts" 1 "$(printf '%s\n' "$OUT" | grep -c '^- \*\*')"
expect "long path: dropped finding absent" 0 "$(printf '%s\n' "$OUT" | grep -c 'pr.go')"
expect "long path: notice says dropped" 1 \
  "$(printf '%s\n' "$OUT" | grep -c 'Dropped: findings\[0\] (file path 412 characters, limit 300)')"
expect "long path: counts kept findings" 1 "$(printf '%s\n' "$OUT" | grep -c '^1 blocker, 0 major')"

ERR="$(node "$CLI" validate "$LONGFIX" 2>&1 >/dev/null)"
expect "validate: long fix exits 0" 0 $?
expect "validate: notice on stderr" 1 \
  "$(printf '%s\n' "$ERR" | grep -c '^pr-review: notice: Shortened to fit: findings\[0\]\.fix')"
expect "no over-long field: no notice" 0 "$(render "$(fixture valid .)" | grep -c 'Shortened\|Dropped')"

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
# The step runs scripts/install-npm-cli.sh, which needs `claude --version`
# to report the pin (#3093).
PIN_VERSION="$(bash "$REPO_ROOT/scripts/adapter-cli-pin.sh" claude-headless)"
export PIN_VERSION="${PIN_VERSION##*@}"
cat > "$STUBS/claude" <<'STUB'
#!/bin/sh
[ "${1:-}" = "--version" ] && { echo "$PIN_VERSION (Claude Code)"; exit 0; }
cat > /dev/null
[ -n "${CLAUDE_ARGS_FILE:-}" ] && printf '%s\n' "$@" > "$CLAUDE_ARGS_FILE"
[ -n "${CLAUDE_CALLS_FILE:-}" ] && echo call >> "$CLAUDE_CALLS_FILE"
if [ -n "${STUB_ECHO_CANARY:-}" ]; then
  printf '{"type":"result","result":"env: PR_REVIEW_CANARY=%s"}' "$PR_REVIEW_CANARY"
  exit 0
fi
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
    CLAUDE_ARGS_FILE="$dir/argv" STUB_OUTPUT="$2" STUB_STATUS="$3" bash -c "$STEP_RUN" > "$dir/log" 2>&1
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

# Tool confinement (#3084): the argv the real step passes to the CLI.
ARGV="$TMP/step-ok/argv"
has_arg() { grep -cxF -e "$1" "$ARGV"; }
expect "confine: --restricted" 1 "$(has_arg --restricted)"
expect "confine: --strict-mcp-config" 1 "$(has_arg --strict-mcp-config)"
expect "confine: --permission-mode dontAsk" 1 \
  "$(grep -A1 -xF -e --permission-mode "$ARGV" | grep -cxF dontAsk)"
expect "confine: --settings names the committed file" 1 \
  "$(grep -A1 -xF -e --settings "$ARGV" | grep -cxF .github/pr-review/claude-settings.json)"
expect "confine: --tools is the closed list" 1 \
  "$(grep -A1 -xF -e --tools "$ARGV" | grep -cxF Read,Glob,Grep,Write)"
expect "confine: no --allowedTools" 0 \
  "$(cat "$ARGV" <(grep -v '^ *#' "$WORKFLOW") | grep -c -e '--allowedTools')"
expect "confine: no permission bypass" 0 "$(grep -c -e '--dangerously-skip-permissions' "$WORKFLOW")"
expect "trigger: never pull_request_target" 0 "$(grep -c pull_request_target: "$WORKFLOW")"

SETTINGS=".github/pr-review/claude-settings.json"
expect "settings: defaultMode dontAsk" dontAsk "$(jq -r '.permissions.defaultMode' "$SETTINGS")"
for d in 'Read(//proc/**)' 'Read(//sys/**)' 'Read(~/**)' 'Read(./.git/**)' Bash WebFetch WebSearch; do
  expect "settings: denies $d" true "$(jq --arg d "$d" '.permissions.deny | index($d) != null' "$SETTINGS")"
done
expect "settings: allow is the closed set" \
  '["Edit(./.pr-review/out/findings.json)","Glob","Grep","Read(./.pr-review/**)","Read(./docs/**)","Read(./skills/nightgauge-adversarial-review/**)","Read(./standards/**)","Write(./.pr-review/out/findings.json)"]' \
  "$(jq -c '.permissions.allow | sort' "$SETTINGS")"
expect "settings: only write target is the findings file" '["./.pr-review/out/findings.json"]' \
  "$(jq -c '[.permissions.allow[] | select(test("^(Edit|Write)")) | capture("\\((?<p>.*)\\)").p] | unique' "$SETTINGS")"

# The symlink strip, run on a fixture tree.
STRIP_RUN="$(yq '.jobs.review.steps[] | select(.name == "Strip symlinks from the pull request'"'"'s tree") | .run' "$WORKFLOW")"
SDIR="$TMP/strip"
mkdir -p "$SDIR/.pr-review/head/sub"
echo secret > "$SDIR/environ"
echo code > "$SDIR/.pr-review/head/sub/file.go"
ln -s "$SDIR/environ" "$SDIR/.pr-review/head/sub/leak"
(cd "$SDIR" && bash -c "$STRIP_RUN") > "$SDIR/log" 2>&1
expect "strip: step is green" 0 $?
expect "strip: symlink removed" no "$([ -L "$SDIR/.pr-review/head/sub/leak" ] && echo yes || echo no)"
expect "strip: regular file kept" code "$(cat "$SDIR/.pr-review/head/sub/file.go")"
expect "strip: log counts the removal" 1 "$(count 'Removed 1 symlink' "$SDIR/log")"

# The token canary in the model-free validate step.
VALIDATE_RUN="$(yq '.jobs.review.steps[] | select(.name == "Validate the findings") | .run' "$WORKFLOW")"
expect "canary: runs even when the review step fails" '${{ always() }}' \
  "$(yq '.jobs.review.steps[] | select(.name == "Validate the findings") | .if' "$WORKFLOW")"
VDIR="$TMP/validate"
mkdir -p "$VDIR/.pr-review/out" "$VDIR/scripts"
cp "$CLI" "$VDIR/scripts/pr-review.mjs"
validate_case() {
  # validate_case <findings json> -> VSTATUS; log in $VDIR/log
  printf '%s' "$1" > "$VDIR/.pr-review/out/findings.json"
  (cd "$VDIR" && FINDINGS_FILE=.pr-review/out/findings.json MODEL_TOKEN=tok-xyz \
    bash -c "$VALIDATE_RUN") > "$VDIR/log" 2>&1
  VSTATUS=$?
}
validate_case '{"schema":1,"findings":[]}'
expect "canary: clean findings pass" 0 "$VSTATUS"
validate_case '{"schema":1,"findings":[],"x":"leaked tok-xyz here"}'
expect "canary: token in findings is red" 1 "$VSTATUS"
expect "canary: log names the leak" 1 "$(count 'contains the model token' "$VDIR/log")"
expect "canary: log never echoes the token" 0 "$(count tok-xyz "$VDIR/log")"
expect "canary: leaked file is emptied" 0 "$(wc -c < "$VDIR/.pr-review/out/findings.json" | tr -d ' ')"

# The shared confinement wrapper (#3084): both workflows call it, neither calls
# the CLI directly, and only the caller sets the turn limit.
WRAPPER="scripts/pr-review-claude.sh"
PROBE_WF=".github/workflows/pr-review-confinement.yml"
code_only() { grep -v '^ *#' "$1"; }
expect "wrapper: review step calls it" 1 "$(yq "$STEP_SELECT | .run" "$WORKFLOW" | grep -c "^scripts/pr-review-claude.sh")"
expect "wrapper: probe calls it" 1 "$(code_only "$PROBE_WF" | grep -cE '^ +scripts/pr-review-claude.sh --max-turns')"
for wf in "$WORKFLOW" "$PROBE_WF"; do
  expect "wrapper: no direct claude call in $wf" 0 "$(code_only "$wf" | grep -cE '(^|[[:space:]])claude[[:space:]]+-p')"
done
expect "wrapper: sets no --max-turns" 0 "$(code_only "$WRAPPER" | grep -c -e '--max-turns')"
expect "wrapper: is executable" yes "$([ -x "$WRAPPER" ] && echo yes || echo no)"

# probe-verdict: the model-free decision of the confinement probe.
PV="$TMP/pv"
mkdir -p "$PV"
CANARY="c4n4ry0123456789abcdef"
verdict() {
  # verdict <case> <result text> [canary] -> VSTATUS; output in $PV/<case>.out
  printf '%s' "$2" > "$PV/$1.json"
  PR_REVIEW_CANARY="${3-$CANARY}" node "$CLI" probe-verdict "$PV/$1.json" > "$PV/$1.out" 2>&1
  VSTATUS=$?
}
verdict read '{"result":"denied","permission_denials":[{"tool_name":"Read","tool_input":{"file_path":"/proc/self/environ"}}]}'
expect "verdict: a Read denial alone is unproven" 4 "$VSTATUS"
verdict grep '{"permission_denials":[{"tool_name":"Grep","tool_input":{"pattern":"X","path":"/proc/self"}}]}'
expect "verdict: a Grep denial alone is unproven" 4 "$VSTATUS"
verdict oneread '{"permission_denials":[{"tool_name":"Read","tool_input":{"file_path":"/proc/self/environ"}},{"tool_name":"Grep","tool_input":{"pattern":"X","path":"/proc/self"}}]}'
expect "verdict: one Read denial and a Grep denial are unproven" 4 "$VSTATUS"
verdict dupread '{"permission_denials":[{"tool_name":"Read","tool_input":{"file_path":"/proc/self/environ"}},{"tool_name":"Read","tool_input":{"file_path":"/proc/self/environ"}},{"tool_name":"Grep","tool_input":{"pattern":"X","path":"/proc/self"}}]}'
expect "verdict: the same Read denied twice is unproven" 4 "$VSTATUS"
verdict all '{"permission_denials":[{"tool_name":"Read","tool_input":{"file_path":"/proc/self/environ"}},{"tool_name":"Read","tool_input":{"file_path":"/proc/123/environ"}},{"tool_name":"Grep","tool_input":{"pattern":"X","path":"/proc/self"}}]}'
expect "verdict: every /proc call denied is proven" 0 "$VSTATUS"
verdict leak "{\"result\":\"PR_REVIEW_CANARY=$CANARY\",\"permission_denials\":[]}"
expect "verdict: canary in result is leaked" 1 "$VSTATUS"
expect "verdict: output never echoes the canary" 0 "$(grep -c "$CANARY" "$PV/leak.out")"
verdict leakwins "{\"permission_denials\":[{\"tool_name\":\"Read\",\"tool_input\":{\"file_path\":\"/proc/self/environ\"}},{\"tool_name\":\"Grep\",\"tool_input\":{\"pattern\":\"$CANARY\"}}]}"
expect "verdict: a leak beats a denial" 1 "$VSTATUS"
verdict other '{"permission_denials":[{"tool_name":"Read","tool_input":{"file_path":"/etc/passwd"}}]}'
expect "verdict: non-/proc denials are unproven" 4 "$VSTATUS"
verdict none '{"result":"ok","permission_denials":[]}'
expect "verdict: no denials is unproven" 4 "$VSTATUS"
verdict notjson 'Error: CLI crashed'
expect "verdict: non-JSON is a CLI failure" 5 "$VSTATUS"
verdict empty ''
expect "verdict: an empty result is a CLI failure" 5 "$VSTATUS"
verdict nativebin 'Error: claude native binary not installed. Either postinstall did not run (--ignore-scripts, ...)'
expect "verdict: native-binary error is a CLI failure" 5 "$VSTATUS"
expect "verdict: CLI failure prints the CLI output" 1 "$(grep -c 'native binary not installed' "$PV/nativebin.out")"
verdict iserror '{"type":"result","is_error":true,"result":"Invalid API key · Please run /login"}'
expect "verdict: an is_error result is a CLI failure" 5 "$VSTATUS"
expect "verdict: an is_error result prints the CLI's message" 1 "$(grep -c 'Invalid API key' "$PV/iserror.out")"
verdict iserrorleak "{\"is_error\":true,\"result\":\"PR_REVIEW_CANARY=$CANARY\"}"
expect "verdict: a leak in an is_error result is still leaked" 1 "$VSTATUS"
expect "verdict: a leaked is_error result never echoes the canary" 0 "$(grep -c "$CANARY" "$PV/iserrorleak.out")"
verdict iserrorfalse '{"is_error":false,"result":"ok","permission_denials":[]}'
expect "verdict: is_error false is judged on its denials" 4 "$VSTATUS"
verdict nocanary '{"permission_denials":[]}' ""
expect "verdict: unset canary is a usage error" 2 "$VSTATUS"

# The probe step itself, against the stub CLI: retry, leak and token arms.
PROBE_RUN="$(yq '.jobs.probe.steps[] | select(.name == "Probe the confinement") | .run' "$PROBE_WF")"
ATTEMPTS="$(yq '.jobs.probe.steps[] | select(.name == "Probe the confinement") | .env.PROBE_ATTEMPTS' "$PROBE_WF")"
if [ -z "$PROBE_RUN" ] || [ "$PROBE_RUN" = "null" ]; then
  echo "HARNESS ERROR: could not extract the probe step from $PROBE_WF"
  exit 1
fi
probe_step() {
  # probe_step <case> <token> <stub stdout> [echo canary] -> PSTATUS, PCALLS
  local dir="$TMP/probe-$1"
  mkdir -p "$dir"
  : > "$dir/calls"
  : > "$dir/summary"
  PATH="$STUBS:$PATH" RUNNER_TEMP="$dir" GITHUB_STEP_SUMMARY="$dir/summary" \
    CLAUDE_CODE_OAUTH_TOKEN="$2" PROBE_ATTEMPTS="$ATTEMPTS" CLAUDE_CALLS_FILE="$dir/calls" \
    STUB_OUTPUT="$3" STUB_STATUS=0 STUB_ECHO_CANARY="${4:-}" \
    bash -c "$PROBE_RUN" > "$dir/log" 2>&1
  PSTATUS=$?
  PCALLS="$(wc -l < "$dir/calls" | tr -d ' ')"
}
DENIAL='{"permission_denials":[{"tool_name":"Read","tool_input":{"file_path":"/proc/self/environ"}},{"tool_name":"Read","tool_input":{"file_path":"/proc/123/environ"}},{"tool_name":"Grep","tool_input":{"pattern":"X","path":"/proc/self"}}]}'
probe_step green tok "$DENIAL"
expect "probe: a /proc denial is green" 0 "$PSTATUS"
expect "probe: green after one call" 1 "$PCALLS"
probe_step leak tok "" 1
expect "probe: a leaked canary is red" 1 "$PSTATUS"
expect "probe: leak stops after one call" 1 "$PCALLS"
expect "probe: leak is annotated" 1 "$(count '^::error title=Confinement broken' "$TMP/probe-leak/log")"
probe_step unproven tok '{"permission_denials":[]}'
expect "probe: no attempt is red" 1 "$PSTATUS"
expect "probe: retried to $ATTEMPTS calls" 3 "$PCALLS"
expect "probe: log names unproven" 1 "$(count '^::error title=Confinement unproven::No attempt recorded' "$TMP/probe-unproven/log")"
probe_step clifail tok 'Error: claude native binary not installed.'
expect "probe: a CLI failure is red" 1 "$PSTATUS"
expect "probe: a CLI failure is not retried" 1 "$PCALLS"
expect "probe: a CLI failure is annotated" 1 "$(count '^::error title=Claude CLI failed' "$TMP/probe-clifail/log")"
probe_step clierror tok '{"type":"result","is_error":true,"result":"Invalid API key"}'
expect "probe: an is_error result is red" 1 "$PSTATUS"
expect "probe: an is_error result is not retried" 1 "$PCALLS"
expect "probe: an is_error result is annotated" 1 "$(count '^::error title=Claude CLI failed' "$TMP/probe-clierror/log")"
expect "probe: an is_error result shows the CLI's message" 1 "$(count 'Invalid API key' "$TMP/probe-clierror/log")"
probe_step notoken "" "$DENIAL"
expect "probe: empty token is red" 1 "$PSTATUS"
expect "probe: empty token makes no call" 0 "$PCALLS"
expect "probe: triggers" "workflow_dispatch schedule push" "$(yq '.on | keys | .[]' "$PROBE_WF" | xargs)"
expect "probe: contents read only" "contents: read" "$(yq -o=props '.permissions' "$PROBE_WF" | sed 's/ = /: /')"
for bad in pull_request ANTHROPIC_API_KEY GH_TOKEN GITHUB_TOKEN; do
  expect "probe: no $bad" 0 "$(code_only "$PROBE_WF" | grep -c "$bad")"
done

# No workflow installs the Claude CLI directly; all go through the installer (#3093).
expect "install: no direct npm install of the pinned CLI" 0 \
  "$(grep -l 'npm install --global.*adapter-cli-pin.sh' "$REPO_ROOT"/.github/workflows/*.yml | wc -l | tr -d ' ')"
expect "install: the confinement probe re-runs when the installer changes" 1 \
  "$(yq '.on.push.paths[]' "$REPO_ROOT/.github/workflows/pr-review-confinement.yml" | grep -cx 'scripts/install-npm-cli.sh')"
for wf in pr-review pr-review-confinement continuous-improvement release-watchdog; do
  expect "install: $wf.yml uses scripts/install-npm-cli.sh" 1 \
    "$(grep -c 'scripts/install-npm-cli.sh --global' "$REPO_ROOT/.github/workflows/$wf.yml")"
done

echo
echo "pr-review: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
