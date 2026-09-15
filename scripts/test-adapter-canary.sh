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
#
# Run: bash scripts/test-adapter-canary.sh
# Also run by scripts/ci-local.sh.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/adapter-canary.sh"
CLI_HELP="$REPO_ROOT/internal/execution/adapters/testdata/cli-help"
SCHEMA="$REPO_ROOT/internal/execution/adapters/testdata/opencode-config-schema/opencode-config.schema.json"

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
  # opencode_canary_stub_start fail (bounded), rather than hang forever.
  FAKEDIR="$TMP/fake-stub-bin"
  mkdir -p "$FAKEDIR/bin"
  cat >"$FAKEDIR/bin/stub-provider" <<'FAKE'
#!/usr/bin/env bash
sleep 9999
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
    # The hung fake process is still running under its own pid; clean it up
    # directly (it is not the real stub-provider's PID file contract).
  fi
  pkill -f "$FAKEDIR/bin/stub-provider" 2>/dev/null || true
) >"$TMP/stub-cases.log" 2>&1
cat "$TMP/stub-cases.log"
check "opencode_canary_stub_stop kills a hung process" grep -q '^PASS stub-stop-kills-hung-process' "$TMP/stub-cases.log"
check "opencode_canary_stub_start does not hang forever on a silent stub" grep -q '^PASS stub-start-times-out-on-a-hang' "$TMP/stub-cases.log"

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

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
