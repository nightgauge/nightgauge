#!/usr/bin/env bash
# test-adapter-canary.sh — regression suite for scripts/adapter-canary.sh
# (#1639).
#
# Drives the working-tree copy of the script. Covers the pieces that do not
# need a live install of every manifest CLI (that is the daily/PR workflow
# run itself, scripts/ci-local.sh's job):
#   1. schema-diff reports "unchanged" against an identical copy of the
#      pinned schema, and "changed", listing an added property and invoking
#      the #1634 suite, against a copy with one extra property.
#   2. report, with gh stubbed, files exactly one issue for the first failing
#      summary and comments on the second, for the same adapter+version.
#   3. A stub-provider that never prints its base_url (a hang) is killed at
#      the start timeout; a stub-provider that sleeps forever after starting
#      is killed by opencode_canary_stub_stop, and `kill -0` on its pid fails
#      once the stop function returns.
#   4. flag-contract emits exactly one row per manifest adapter, "pass" for a
#      clean set of committed captures, and "fail" naming the adapter and
#      flag when one capture is missing a flag BuildCommand emits — without
#      mis-attributing the failure to an unrelated adapter's own notes
#      (known-broken/hidden-flag/skip bookkeeping share the same log shape).
#   6. .github/workflows/adapter-canary.yml's own structure: the
#      opencode-canary job's OpenCode leg and schema-diff leg each run
#      regardless of the other's result, and a final step fails the JOB when
#      either one did (#1639 round 3) — a GitHub Actions runner is not
#      available here, so this checks the YAML's own shape with yq rather
#      than executing it; actionlint checks the YAML is otherwise valid.
#   7. process hygiene (#1639 round 3): a pgrep snapshot of every process this
#      run's own $TMP could have spawned (a fake stub-provider's `sleep`
#      included) is empty once this script's own EXIT trap has run — the
#      suite must never be the reason one of those outlives it, whether or
#      not the section that spawned it passed.
#
# Run: bash scripts/test-adapter-canary.sh
# Also run by scripts/ci-local.sh.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/adapter-canary.sh"
CLI_HELP="$REPO_ROOT/internal/execution/adapters/testdata/cli-help"
SCHEMA="$REPO_ROOT/internal/execution/adapters/testdata/opencode-config-schema/opencode-config.schema.json"
WORKFLOW="$REPO_ROOT/.github/workflows/adapter-canary.yml"

PASS=0
FAIL=0
TMP=""
cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

ok() {
  echo "  ✓ $1"
  PASS=$((PASS + 1))
}
bad() {
  echo "  ✗ $1"
  FAIL=$((FAIL + 1))
}
check() { # check <description> <command...>
  local desc="$1"
  shift
  if "$@"; then ok "$desc"; else bad "$desc"; fi
}

TMP="$(mktemp -d)"
TMP="$(cd "$TMP" && pwd -P)"

echo "=== 1. schema-diff ==="

# 1a. unchanged: an identical copy of the pinned schema.
cp "$SCHEMA" "$TMP/unchanged.json"
OUT="$(bash "$SCRIPT" schema-diff --live-file "$TMP/unchanged.json")"
check "unchanged: exits 0" [ $? -eq 0 ]
check "unchanged: result is unchanged" sh -c "echo '$OUT' | jq -e '.result == \"unchanged\"' >/dev/null"

# 1b. changed: the same schema with one extra top-level property.
python3 - "$SCHEMA" "$TMP/changed.json" <<'PY'
import json, sys
doc = json.load(open(sys.argv[1]))
node = doc
for _ in range(8):
    if "properties" in node:
        break
    if "$ref" in node and "$defs" in doc:
        node = doc["$defs"][node["$ref"].split("/")[-1]]
        continue
    break
node["properties"]["nightgaugeCanaryFixtureProp"] = {"type": "string"}
json.dump(doc, open(sys.argv[2], "w"))
PY
OUT="$(bash "$SCRIPT" schema-diff --live-file "$TMP/changed.json")"
check "changed: result is changed" sh -c "echo '$OUT' | jq -e '.result == \"changed\"' >/dev/null"
check "changed: lists the added property" sh -c "echo '$OUT' | jq -r '.detail' | jq -e '.added == [\"nightgaugeCanaryFixtureProp\"]' >/dev/null"
check "changed: invoked the #1634 suite (config_suite present)" sh -c "echo '$OUT' | jq -r '.detail' | jq -e '.config_suite == \"pass\" or .config_suite == \"fail\"' >/dev/null"

echo ""
echo "=== 2. report (gh stubbed) ==="

FAKE_BIN="$TMP/fakebin"
mkdir -p "$FAKE_BIN"
GH_LOG="$TMP/gh.log"
GH_STATE="$TMP/gh-state"
mkdir -p "$GH_STATE"
cat >"$FAKE_BIN/gh" <<EOF
#!/usr/bin/env bash
echo "gh \$*" >> "$GH_LOG"
case "\$1 \$2" in
  "issue list")
    if [ -f "$GH_STATE/created" ]; then
      echo '[{"title":"canary: opencode 1.19.0 drift","url":"https://example.test/issues/9999"}]'
    else
      echo '[]'
    fi
    ;;
  "issue create")
    echo created > "$GH_STATE/created"
    echo "https://example.test/issues/9999"
    ;;
  "issue comment")
    echo commented >> "$GH_STATE/comments"
    ;;
  *) echo "unhandled: \$*" >&2; exit 1 ;;
esac
EOF
chmod +x "$FAKE_BIN/gh"

SUMMARY="$TMP/summary.jsonl"
echo '{"adapter":"opencode","version":"1.19.0","check":"opencode-canary","result":"fail","detail":"event type not-a-type"}' >"$SUMMARY"

PATH="$FAKE_BIN:$PATH" bash "$SCRIPT" report "$SUMMARY" >/dev/null
PATH="$FAKE_BIN:$PATH" bash "$SCRIPT" report "$SUMMARY" >/dev/null

check "exactly one 'gh issue create' ran" sh -c "[ \"\$(grep -c '^gh issue create' '$GH_LOG')\" = 1 ]"
check "exactly one 'gh issue comment' ran" sh -c "[ \"\$(grep -c '^gh issue comment' '$GH_LOG')\" = 1 ]"
check "the comment targets the same issue the create returned" \
  grep -q 'gh issue comment https://example.test/issues/9999' "$GH_LOG"

# A summary with no failing rows files nothing.
CLEAN_SUMMARY="$TMP/clean.jsonl"
echo '{"adapter":"opencode","version":"1.18.30","check":"opencode-canary","result":"pass","detail":""}' >"$CLEAN_SUMMARY"
: >"$GH_LOG"
PATH="$FAKE_BIN:$PATH" bash "$SCRIPT" report "$CLEAN_SUMMARY" >/dev/null
check "a summary with no failing rows calls gh at all: it does not" sh -c "[ ! -s '$GH_LOG' ]"

# Two failing rows for the SAME adapter+version (opencode failing both
# flag-contract and opencode-canary at once is the common trigger) must file
# exactly ONE issue, with both rows folded into its body — not a second, bogus
# issue titled with the second row's own detail text.
GH_STATE2="$TMP/gh-state2"
mkdir -p "$GH_STATE2"
FAKE_BIN2="$TMP/fakebin2"
mkdir -p "$FAKE_BIN2"
GH_LOG2="$TMP/gh2.log"
cat >"$FAKE_BIN2/gh" <<EOF
#!/usr/bin/env bash
echo "gh \$*" >> "$GH_LOG2"
case "\$1 \$2" in
  "issue list") echo '[]' ;;
  "issue create")
    echo created > "$GH_STATE2/created"
    echo "https://example.test/issues/8888"
    ;;
  "issue comment") echo commented >> "$GH_STATE2/comments" ;;
  *) echo "unhandled: \$*" >&2; exit 1 ;;
esac
EOF
chmod +x "$FAKE_BIN2/gh"
MULTI_SUMMARY="$TMP/multi.jsonl"
cat >"$MULTI_SUMMARY" <<'JSONL'
{"adapter":"opencode","version":"1.18.30","check":"flag-contract","result":"fail","detail":"BuildCommand emits -m (with zero RunOptions), which opencode-run-1.18.30.txt does not define"}
{"adapter":"opencode","version":"1.18.30","check":"opencode-canary","result":"fail","detail":"opencode_canary_test.go:420: stderr: event type not-a-type"}
JSONL
PATH="$FAKE_BIN2:$PATH" bash "$SCRIPT" report "$MULTI_SUMMARY" >/dev/null
check "two failing rows, same adapter+version: exactly one 'gh issue create' ran" \
  sh -c "[ \"\$(grep -c '^gh issue create' '$GH_LOG2')\" = 1 ]"
check "the one issue is titled for the adapter+version, not a row's own detail text" \
  grep -q -- "--title canary: opencode 1.18.30 drift" "$GH_LOG2"
check "the one issue's body carries BOTH failing rows" \
  sh -c "grep -q 'flag-contract:' '$GH_LOG2' && grep -q 'opencode-canary:' '$GH_LOG2'"

echo ""
echo "=== 3. stub-provider timeouts and cleanup ==="

(
  source "$SCRIPT"

  # 3a. opencode_canary_stub_stop kills a hung process and confirms it dead.
  sleep 9999 &
  SLEEP_PID=$!
  PIDFILE="$TMP/sleeper.pid"
  echo "$SLEEP_PID" >"$PIDFILE"
  opencode_canary_stub_stop "$PIDFILE" >/dev/null
  if kill -0 "$SLEEP_PID" 2>/dev/null; then
    echo "FAIL stub-stop-kills-hung-process"
  else
    echo "PASS stub-stop-kills-hung-process"
  fi

  # 3b. a "stub-provider" that never prints its base_url makes
  # opencode_canary_stub_start fail (bounded), rather than hang forever — and
  # kills the process it started, including the child that process spawned
  # (bash's own last-command optimization can make the wrapper's `sleep 9999`
  # a separate, reparentable child rather than replacing the wrapper), not
  # just leave it to be reparented to init and outlive the run.
  FAKEDIR="$TMP/fake-stub-bin"
  mkdir -p "$FAKEDIR/bin"
  SLEEP_PIDFILE="$TMP/hang-sleep.pid"
  cat >"$FAKEDIR/bin/stub-provider" <<FAKE
#!/usr/bin/env bash
sleep 9999 &
echo \$! >"$SLEEP_PIDFILE"
wait
FAKE
  chmod +x "$FAKEDIR/bin/stub-provider"
  START=$(date +%s)
  if REPO_ROOT="$FAKEDIR" bash -c '
      source '"$SCRIPT"'
      REPO_ROOT="'"$FAKEDIR"'"
      opencode_canary_stub_start tool-edit-stop "'"$TMP"'/hang.pid" "'"$TMP"'/hang.url"
    ' >/dev/null 2>&1; then
    echo "FAIL stub-start-times-out-on-a-hang (it returned success)"
  else
    ELAPSED=$(( $(date +%s) - START ))
    if [ "$ELAPSED" -lt 15 ]; then
      echo "PASS stub-start-times-out-on-a-hang (${ELAPSED}s)"
    else
      echo "FAIL stub-start-times-out-on-a-hang (took ${ELAPSED}s)"
    fi
  fi
  # opencode_canary_stub_start's own timeout-path cleanup, not a pattern-based
  # pkill here, must already have killed the wrapper's sleep child.
  sleep 0.3
  if [ -f "$SLEEP_PIDFILE" ] && kill -0 "$(cat "$SLEEP_PIDFILE")" 2>/dev/null; then
    echo "FAIL stub-start-kills-its-own-child (pid $(cat "$SLEEP_PIDFILE") still alive)"
  else
    echo "PASS stub-start-kills-its-own-child"
  fi
  # A safety net for this suite itself, independent of the FAIL/PASS above:
  # this test must never be the reason a `sleep 9999` (or its wrapper)
  # outlives the run, whether or not opencode_canary_stub_start's own
  # cleanup did its job.
  [ -f "$TMP/hang.pid" ] && { kill -9 "$(cat "$TMP/hang.pid")" 2>/dev/null || true; }
  [ -f "$SLEEP_PIDFILE" ] && { kill -9 "$(cat "$SLEEP_PIDFILE")" 2>/dev/null || true; }
) >"$TMP/stub-cases.log" 2>&1
cat "$TMP/stub-cases.log"
check "opencode_canary_stub_stop kills a hung process" grep -q '^PASS stub-stop-kills-hung-process' "$TMP/stub-cases.log"
check "opencode_canary_stub_start does not hang forever on a silent stub" grep -q '^PASS stub-start-times-out-on-a-hang' "$TMP/stub-cases.log"
check "opencode_canary_stub_start kills the child it started, not just the wrapper" grep -q '^PASS stub-start-kills-its-own-child' "$TMP/stub-cases.log"

# 3d. resolve_npm_version's `npm view` (AC8: "a bounded timeout on every
# external command the AC names, including npm view") does not hang forever
# on a silent/hung npm — it used to run unwrapped.
FAKE_NPM_DIR="$TMP/fake-npm"
mkdir -p "$FAKE_NPM_DIR"
cat >"$FAKE_NPM_DIR/npm" <<'FAKENPM'
#!/usr/bin/env bash
sleep 9999
FAKENPM
chmod +x "$FAKE_NPM_DIR/npm"
NPM_START=$(date +%s)
(
  export ADAPTER_CANARY_TIMEOUT=2
  export PATH="$FAKE_NPM_DIR:$PATH"
  source "$SCRIPT"
  resolve_npm_version some-package latest
) >/dev/null 2>&1
NPM_RC=$?
NPM_ELAPSED=$(( $(date +%s) - NPM_START ))
check "resolve_npm_version (npm view) times out rather than hanging forever (${NPM_ELAPSED}s)" [ "$NPM_ELAPSED" -lt 15 ]
check "resolve_npm_version (npm view) exits non-zero on a timeout" [ "$NPM_RC" -ne 0 ]
pkill -9 -f "$FAKE_NPM_DIR/npm" 2>/dev/null || true

echo ""
echo "=== 4. flag-contract row attribution ==="

# 4a. the committed captures pass cleanly: one "pass" row per manifest adapter.
OUT="$(bash "$SCRIPT" flag-contract "$CLI_HELP")"
check "flag-contract exits 0 on the committed captures" [ $? -eq 0 ]
ADAPTER_COUNT="$(ls "$REPO_ROOT"/internal/adaptercompat/manifests/*.json | wc -l | tr -d ' ')"
ROW_COUNT="$(echo "$OUT" | wc -l | tr -d ' ')"
check "one row per manifest adapter ($ADAPTER_COUNT)" [ "$ROW_COUNT" -eq "$ADAPTER_COUNT" ]
check "every row is pass" sh -c "! echo '$OUT' | jq -r '.result' | grep -qx fail"

# 4b. break opencode's capture: BuildCommand's -m flag is no longer defined.
BROKEN="$TMP/broken-help"
mkdir -p "$BROKEN"
cp "$CLI_HELP"/*.txt "$CLI_HELP"/*.txt.hidden "$BROKEN/" 2>/dev/null
grep -v -- '-m, --model' "$CLI_HELP/opencode-run-1.18.30.txt" >"$BROKEN/opencode-run-1.18.30.txt"
OUT="$(bash "$SCRIPT" flag-contract "$BROKEN")"
RC=$?
check "flag-contract exits non-zero when a flag is missing" [ "$RC" -ne 0 ]
check "the opencode row fails, naming the -m flag" \
  sh -c "row=\$(echo '$OUT' | jq -c 'select(.adapter==\"opencode\")'); echo \"\$row\" | jq -e '.result == \"fail\"' >/dev/null && echo \"\$row\" | jq -r '.detail' | grep -qF -- '-m'"
check "claude-headless is not mis-attributed the failure" \
  sh -c "echo '$OUT' | jq -c 'select(.adapter==\"claude-headless\")' | jq -e '.result == \"pass\"' >/dev/null"
check "codex is not mis-attributed the failure" \
  sh -c "echo '$OUT' | jq -c 'select(.adapter==\"codex\")' | jq -e '.result == \"pass\"' >/dev/null"
check "grok is not mis-attributed the failure" \
  sh -c "echo '$OUT' | jq -c 'select(.adapter==\"grok\")' | jq -e '.result == \"pass\"' >/dev/null"

# 4c. capture-help carries the committed hidden-flag sidecar over when the
# freshly captured version is the committed capture's own version — the exact
# shape a pull_request run (VERSION_MODE=pinned, every manifest CLI installed
# at its OWN max_tested) and a daily/latest run with no new release produce.
# Without this, claude-headless (--max-turns) and grok (--no-auto-update) fail
# the contract every single day at their own already-approved version.
FRESH="$TMP/fresh-capture"
mkdir -p "$FRESH"
FAKEBINS="$TMP/fakebins"
mkdir -p "$FAKEBINS"
for pair in "claude-headless:claude:" "grok:grok:" "opencode:opencode:run"; do
  adapter="${pair%%:*}"
  rest="${pair#*:}"
  binname="${rest%%:*}"
  sub="${rest#*:}"
  for capture in "$CLI_HELP/$adapter"-*.txt; do break; done
  ver="$(basename "$capture" .txt | grep -oE '[0-9]+\.[0-9]+\.[0-9]+$')"
  fake="$FAKEBINS/$binname"
  if [ -n "$sub" ]; then
    cat >"$fake" <<FAKE
#!/usr/bin/env bash
if [ "\$1" = "$sub" ] && [ "\$2" = "--help" ]; then tail -n +2 "$capture"; fi
FAKE
  else
    cat >"$fake" <<FAKE
#!/usr/bin/env bash
if [ "\$1" = "--help" ]; then tail -n +2 "$capture"; fi
FAKE
  fi
  chmod +x "$fake"
  ADAPTER_CANARY_VERSION="$ver" bash "$SCRIPT" capture-help "$adapter" "$fake" "$FRESH" >/dev/null
done
# codex has no compat-adapter fake bin above; carry its own committed capture
# over unchanged so the flag-contract run below covers every captured
# adapter, not just the three this sidecar fix concerns.
cp "$CLI_HELP/codex-exec-0.145.0.txt" "$FRESH/"
check "capture-help writes claude-headless's committed .hidden sidecar at its own version" \
  [ -f "$FRESH/claude-headless-2.1.258.txt.hidden" ]
check "capture-help writes grok's committed .hidden sidecar at its own version" \
  [ -f "$FRESH/grok-1.0.4.txt.hidden" ]
OUT="$(bash "$SCRIPT" flag-contract "$FRESH" 2>&1)"
check "flag-contract on a directory captured without sidecars, but at each adapter's own version, still exits 0" \
  sh -c "bash '$SCRIPT' flag-contract '$FRESH' >/dev/null 2>&1"
check "claude-headless passes on a freshly captured (sidecar-less) help dir" \
  sh -c "echo '$OUT' | jq -c 'select(.adapter==\"claude-headless\")' | jq -e '.result == \"pass\"' >/dev/null"
check "grok passes on a freshly captured (sidecar-less) help dir" \
  sh -c "echo '$OUT' | jq -c 'select(.adapter==\"grok\")' | jq -e '.result == \"pass\"' >/dev/null"

# 4c-2. capture-help carries the committed hidden-flag sidecar over even when
# the freshly captured version is NEWER than the committed probe's own — the
# daily/latest run's actual shape once a new release has shipped (#1639 round
# 3). Before this fix, only an EXACT version match carried the sidecar over,
# so claude-headless (--max-turns) and grok (--no-auto-update) auto-failed the
# contract on every release after the committed probe's version, judged for
# being newer rather than on their own captured flags.
NEWER="$TMP/newer-capture"
mkdir -p "$NEWER"
for pair in "claude-headless:claude:2.1.999" "grok:grok:9.9.9"; do
  adapter="${pair%%:*}"
  rest="${pair#*:}"
  binname="${rest%%:*}"
  ver="${rest#*:}"
  for capture in "$CLI_HELP/$adapter"-*.txt; do break; done
  fake="$FAKEBINS/newer-$binname"
  cat >"$fake" <<FAKE
#!/usr/bin/env bash
if [ "\$1" = "--help" ]; then tail -n +2 "$capture"; fi
FAKE
  chmod +x "$fake"
  ADAPTER_CANARY_VERSION="$ver" bash "$SCRIPT" capture-help "$adapter" "$fake" "$NEWER" >/dev/null
done
cp "$CLI_HELP/codex-exec-0.145.0.txt" "$NEWER/"
cp "$CLI_HELP/opencode-run-1.18.30.txt" "$NEWER/"
check "capture-help carries claude-headless's sidecar to a version newer than committed" \
  [ -f "$NEWER/claude-headless-2.1.999.txt.hidden" ]
check "capture-help carries grok's sidecar to a version newer than committed" \
  [ -f "$NEWER/grok-9.9.9.txt.hidden" ]
OUT="$(bash "$SCRIPT" flag-contract "$NEWER" 2>&1)"
check "flag-contract at a newer-than-committed version still exits 0" \
  sh -c "bash '$SCRIPT' flag-contract '$NEWER' >/dev/null 2>&1"
check "claude-headless passes at a version newer than the committed probe (not auto-failed for being newer)" \
  sh -c "echo '$OUT' | jq -c 'select(.adapter==\"claude-headless\")' | jq -e '.result == \"pass\"' >/dev/null"
check "grok passes at a version newer than the committed probe (not auto-failed for being newer)" \
  sh -c "echo '$OUT' | jq -c 'select(.adapter==\"grok\")' | jq -e '.result == \"pass\"' >/dev/null"

# 4d. a genuine failure whose t.Error line is not prefixed "<adapter>: " —
# flagContractProblems reports a malformed capture, a header/command/version
# mismatch, or an orphan sidecar this way — is attributed to the REAL adapter
# and version its own message names (<stem>-<version>.txt), not filed under a
# pseudo adapter+version with no version (#1639 round 3): `report` titles a
# drift issue "canary: <adapter> <version> drift", and a pseudo
# adapter="flag-contract" version="" titled it "canary: flag-contract  drift"
# (double space, no version) instead.
TRUNCATED="$TMP/truncated-help"
mkdir -p "$TRUNCATED"
cp "$CLI_HELP"/*.txt "$CLI_HELP"/*.txt.hidden "$TRUNCATED/" 2>/dev/null
head -n 3 "$CLI_HELP/opencode-run-1.18.30.txt" >"$TRUNCATED/opencode-run-1.18.30.txt"
OUT="$(bash "$SCRIPT" flag-contract "$TRUNCATED")"
RC=$?
check "flag-contract exits non-zero on a malformed capture" [ "$RC" -ne 0 ]
check "the opencode row itself fails, naming the malformed capture" \
  sh -c "row=\$(echo '$OUT' | jq -c 'select(.adapter==\"opencode\")'); echo \"\$row\" | jq -e '.result == \"fail\" and .version == \"1.18.30\"' >/dev/null && echo \"\$row\" | jq -r '.detail' | grep -qF 'parsed only'"
check "no pseudo flag-contract row is filed when the real adapter+version already carries it" \
  sh -c "! echo '$OUT' | jq -c 'select(.adapter==\"flag-contract\")' | grep -q ."
check "claude-headless, grok and codex are not mis-attributed the opencode failure" \
  sh -c "for a in claude-headless grok codex; do echo '$OUT' | jq -c \"select(.adapter==\\\"\$a\\\")\" | jq -e '.result == \"pass\"' >/dev/null || exit 1; done"

# 4e. the fallback pseudo row is NOT suppressed just because some OTHER
# adapter already has its own attributed failure (Round-1 finding #6, only
# half-fixed before round 3): break claude-headless's own capture (a directly
# "<adapter>: "-attributed failure) AND leave a help-format break no adapter's
# stem names (an orphan sidecar under a made-up stem no manifest owns), in the
# same run. Before this fix, `any_failed == 1` from claude-headless's own
# failure alone suppressed the fallback, and the orphan-sidecar break was
# filed nowhere.
MIXED="$TMP/mixed-failures"
mkdir -p "$MIXED"
cp "$CLI_HELP"/*.txt "$CLI_HELP"/*.txt.hidden "$MIXED/" 2>/dev/null
grep -v -- '--allowedTools, --allowed-tools' "$CLI_HELP/claude-headless-2.1.258.txt" >"$MIXED/claude-headless-2.1.258.txt"
echo '--some-made-up-flag' >"$MIXED/ghost-adapter-9.9.9.txt.hidden"
OUT="$(bash "$SCRIPT" flag-contract "$MIXED")"
RC=$?
check "flag-contract exits non-zero with two simultaneous breaks" [ "$RC" -ne 0 ]
check "claude-headless's own row fails, naming --allowedTools" \
  sh -c "row=\$(echo '$OUT' | jq -c 'select(.adapter==\"claude-headless\")'); echo \"\$row\" | jq -e '.result == \"fail\"' >/dev/null && echo \"\$row\" | jq -r '.detail' | grep -qF -- '--allowedTools'"
check "the fallback row still fires for the orphan sidecar no manifest's stem names, despite claude-headless's own failure" \
  sh -c "row=\$(echo '$OUT' | jq -c 'select(.adapter==\"flag-contract\")'); [ -n \"\$row\" ] && echo \"\$row\" | jq -e '.result == \"fail\"' >/dev/null && echo \"\$row\" | jq -r '.detail' | grep -qF 'ghost-adapter'"

echo ""
echo "=== 5. opencode-canary and schema-diff propagate a failure's exit code ==="

# 5a. cmd_opencode_canary must return the underlying `go test` failure's exit
# code (it used to end on `rm -f`, whose own exit status masked it), so the
# tee'd workflow step (once pipefail is set) actually goes red.
FAKE_GO_DIR="$TMP/fake-go"
mkdir -p "$FAKE_GO_DIR"
cat >"$FAKE_GO_DIR/go" <<'FAKEGO'
#!/usr/bin/env bash
echo "opencode_canary_test.go:420: opencode: fake stream contract failure" >&2
exit 1
FAKEGO
chmod +x "$FAKE_GO_DIR/go"
FAKE_OPENCODE_DIR="$TMP/fake-opencode"
mkdir -p "$FAKE_OPENCODE_DIR"
cat >"$FAKE_OPENCODE_DIR/opencode" <<'FAKEOC'
#!/usr/bin/env bash
echo "1.19.0"
FAKEOC
chmod +x "$FAKE_OPENCODE_DIR/opencode"
(
  source "$SCRIPT"
  PATH="$FAKE_GO_DIR:$PATH" cmd_opencode_canary "$FAKE_OPENCODE_DIR/opencode" 1.19.0 >"$TMP/opencode-canary-row.jsonl"
  echo "RC=$?" >"$TMP/opencode-canary-rc.txt"
)
check "cmd_opencode_canary's row reports the installed version, not max_tested" \
  sh -c "jq -e '.version == \"1.19.0\"' '$TMP/opencode-canary-row.jsonl' >/dev/null"
check "cmd_opencode_canary's row reports fail" \
  sh -c "jq -e '.result == \"fail\"' '$TMP/opencode-canary-row.jsonl' >/dev/null"
check "cmd_opencode_canary returns non-zero on a failing go test" \
  grep -q '^RC=[1-9]' "$TMP/opencode-canary-rc.txt"

# 5a-2. opencode_canary_failing_line's row detail must be the actual failing
# message, not realOpenCode's own "pin relaxed" t.Logf notice — printed by
# nearly every case here once the installed version differs from the pinned
# 1.18.30 baseline, so it shares the exact "file.go:N: message" shape a real
# t.Error/t.Fatalf line has, and would otherwise be taken as "the offending
# line" AC2 names (#1639 round 3 low, round 4 fix: cmd_opencode_canary's own
# `go test -tags canary ... -count=1` carries NO -v, so go prints the
# "--- FAIL:" summary BEFORE the failing test's own lines, not after — the
# opposite of what the round-3 parser assumed, so it found nothing there and
# fell back to the first "file.go:N:" line in the whole file, which is
# exactly this notice on any non-1.18.30 version). Both fixtures are REAL
# `go test` output (scripts/testdata/adapter-canary-gotest/README.md records
# how they were captured), in the two orders go test actually prints, each
# with the notice preceding the real, multi-line failing message.
GOTEST_FIXTURES="$REPO_ROOT/scripts/testdata/adapter-canary-gotest"
(
  source "$SCRIPT"
  detail_no_v="$(opencode_canary_failing_line "$GOTEST_FIXTURES/opencode-canary-fail-no-v.txt")"
  detail_with_v="$(opencode_canary_failing_line "$GOTEST_FIXTURES/opencode-canary-fail-with-v.txt")"
  echo "$detail_no_v" >"$TMP/detail-no-v.txt"
  echo "$detail_with_v" >"$TMP/detail-with-v.txt"
)
check "no -v layout (--- FAIL: before the log lines): detail is the real failing message" \
  grep -qF 'checkOpenCodeCanaryStream: 1 problem(s) on the stream:' "$TMP/detail-no-v.txt"
check "no -v layout: detail does not carry the pin-relaxed notice instead" \
  sh -c "! grep -qF 'is installed (canary: pin relaxed' '$TMP/detail-no-v.txt'"
check "-v layout (--- FAIL: after the log lines): detail is the real failing message" \
  grep -qF 'checkOpenCodeCanaryStream: 1 problem(s) on the stream:' "$TMP/detail-with-v.txt"
check "-v layout: detail does not carry the pin-relaxed notice instead" \
  sh -c "! grep -qF 'is installed (canary: pin relaxed' '$TMP/detail-with-v.txt'"

# 5a-3. End to end through cmd_opencode_canary itself, with a fake `go` that
# replays the REAL no -v fixture verbatim (the shape cmd_opencode_canary's
# own invocation actually produces) and a fake opencode that reports a
# version newer than max_tested — the "reproduce end to end once with a fake
# opencode that reports a newer version and fails" case.
cat >"$FAKE_GO_DIR/go" <<FAKEGO
#!/usr/bin/env bash
cat "$GOTEST_FIXTURES/opencode-canary-fail-no-v.txt"
exit 1
FAKEGO
chmod +x "$FAKE_GO_DIR/go"
(
  source "$SCRIPT"
  PATH="$FAKE_GO_DIR:$PATH" cmd_opencode_canary "$FAKE_OPENCODE_DIR/opencode" 1.19.0 >"$TMP/opencode-canary-row-2.jsonl"
)
check "cmd_opencode_canary's row reports the newer, fake-reported version" \
  sh -c "jq -e '.version == \"1.19.0\"' '$TMP/opencode-canary-row-2.jsonl' >/dev/null"
check "cmd_opencode_canary's row reports fail on the real no -v fixture" \
  sh -c "jq -e '.result == \"fail\"' '$TMP/opencode-canary-row-2.jsonl' >/dev/null"
check "the row's detail is the actual failing message, not the pin-relaxed notice" \
  sh -c "jq -r '.detail' '$TMP/opencode-canary-row-2.jsonl' | grep -qF 'checkOpenCodeCanaryStream: 1 problem(s) on the stream:'"
check "the row's detail does not carry the pin-relaxed notice instead" \
  sh -c "! jq -r '.detail' '$TMP/opencode-canary-row-2.jsonl' | grep -qF 'is installed (canary: pin relaxed'"

# 5b. cmd_schema_diff must fail (result=fail, non-zero exit) when the live
# schema breaks the #1634 suite — not report=changed and exit 0, which
# silences exactly the alarm TestSecurityKeysPresentAndKnown exists to raise.
DROPPED="$TMP/dropped-schema.json"
python3 - "$SCHEMA" "$DROPPED" <<'PY'
import json, sys
doc = json.load(open(sys.argv[1]))
node = doc
for _ in range(8):
    if "properties" in node:
        break
    if "$ref" in node and "$defs" in doc:
        node = doc["$defs"][node["$ref"].split("/")[-1]]
        continue
    break
node["properties"].pop("share", None)
node["properties"].pop("autoupdate", None)
json.dump(doc, open(sys.argv[2], "w"))
PY
OUT="$(bash "$SCRIPT" schema-diff --live-file "$DROPPED" --version 1.19.0 2>/dev/null)"
RC=$?
check "schema-diff exits non-zero when the live schema breaks the #1634 suite" [ "$RC" -ne 0 ]
check "schema-diff's row reports result=fail, not changed" \
  sh -c "[ -n '$OUT' ] && echo '$OUT' | jq -e '.result == \"fail\"' >/dev/null"
check "schema-diff's row reports the version the caller passed" \
  sh -c "[ -n '$OUT' ] && echo '$OUT' | jq -e '.version == \"1.19.0\"' >/dev/null"
check "schema-diff's detail still carries the property-level diff" \
  sh -c "[ -n '$OUT' ] && echo '$OUT' | jq -r '.detail' | jq -e '.removed == [\"autoupdate\", \"share\"]' >/dev/null"

echo ""
echo "=== 6. adapter-canary.yml: every OpenCode leg runs, and the job goes red on any failure ==="

check "yq is on PATH to read the workflow" sh -c "command -v yq >/dev/null"

OPENCODE_LEG_COE="$(yq -r '.jobs["opencode-canary"].steps[] | select(.id == "opencode_leg") | .["continue-on-error"]' "$WORKFLOW")"
check "the opencode-canary leg step continues on its own error, so it cannot skip the schema-diff leg" \
  [ "$OPENCODE_LEG_COE" = "true" ]

SCHEMA_DIFF_LEG_IF="$(yq -r '.jobs["opencode-canary"].steps[] | select(.id == "schema_diff_leg") | .if' "$WORKFLOW")"
check "the schema-diff leg step's own if: still lets it run when a prior step failed outright" \
  sh -c "echo '$SCHEMA_DIFF_LEG_IF' | grep -q cancelled"
SCHEMA_DIFF_LEG_COE="$(yq -r '.jobs["opencode-canary"].steps[] | select(.id == "schema_diff_leg") | .["continue-on-error"]' "$WORKFLOW")"
check "the schema-diff leg step also continues on its own error" \
  [ "$SCHEMA_DIFF_LEG_COE" = "true" ]

FINAL_STEP_NAME="$(yq -r '.jobs["opencode-canary"].steps[-1].name' "$WORKFLOW")"
check "the opencode-canary job's LAST step is the one that fails the job on any leg's outcome" \
  sh -c "echo '$FINAL_STEP_NAME' | grep -qi 'Fail the job'"
FINAL_STEP_RUN="$(yq -r '.jobs["opencode-canary"].steps[-1].run' "$WORKFLOW")"
check "the final step reads the opencode-canary leg's own outcome" \
  sh -c "echo '$FINAL_STEP_RUN' | grep -q 'steps.opencode_leg.outcome'"
check "the final step reads the schema-diff leg's own outcome" \
  sh -c "echo '$FINAL_STEP_RUN' | grep -q 'steps.schema_diff_leg.outcome'"
FINAL_STEP_COE="$(yq -r '.jobs["opencode-canary"].steps[-1]["continue-on-error"] // "false"' "$WORKFLOW")"
check "the final step does NOT continue on its own error, so its own failure fails the job" \
  [ "$FINAL_STEP_COE" != "true" ]

REPORT_IF="$(yq -r '.jobs["report"].if' "$WORKFLOW")"
check "the report job still runs regardless of a leg failure (always())" \
  sh -c "echo '$REPORT_IF' | grep -q 'always()'"

echo ""
echo "=== 7. process hygiene: this suite leaves nothing running under its own \$TMP ==="

# Every fake binary and script every section above spawned (a hung `sleep`,
# a fake stub-provider, a fake npm) lives under $TMP, a path unique to this
# run — so anything still alive naming it in its own command line, by the
# time every section above has returned, is this suite's own orphan (#1639
# round 3), not another process that happens to share a name. Sections 3 and
# 3d each already assert their own specific cleanup; this is the suite-wide
# backstop pgrep gives across all of them, and it kills what it finds so a
# failure here does not itself leave the orphan behind.
ORPHANS="$(pgrep -f -- "$TMP" 2>/dev/null | grep -v -x "$$" || true)"
if [ -n "$ORPHANS" ]; then
  for pid in $ORPHANS; do
    ps -o pid,command -p "$pid" 2>/dev/null || true
    kill -9 "$pid" 2>/dev/null || true
  done
fi
check "pgrep \"\$TMP\" is empty: no process this run spawned is still alive" [ -z "$ORPHANS" ]

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
