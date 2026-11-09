#!/usr/bin/env bash
# Offline self-test for scripts/vt-scan.sh (#2946).
#
# The scanner talks to a local stand-in for the VirusTotal v3 API
# (scripts/testdata/vt-scan/stub-api.py) on 127.0.0.1, so no arm reaches
# VirusTotal and no arm needs a real key. The stub picks each file's verdict
# from a marker in its bytes and logs every request, so an arm can assert the
# verdict, the exit code, which endpoint was used and that the key travelled
# only as a header.
#
# The arms that matter most are the fail-closed ones: every answer that is not
# a parsed, clean report must be exit 2 with a HARNESS ERROR line, never 0.
#
# Run: bash scripts/test-vt-scan.sh   (also run by scripts/ci-local.sh)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT="$SCRIPT_DIR/vt-scan.sh"
FIXTURES="$SCRIPT_DIR/testdata/vt-scan"
PASS=0
FAIL=0
ok() { echo "  ok   $1"; PASS=$((PASS + 1)); }
bad() { echo "  FAIL $1" >&2; FAIL=$((FAIL + 1)); }

# A key the stub accepts. It is distinctive so a leak is easy to grep for.
KEY="StubKey0123456789LeakCanaryAbCdEf0123456789"

WORK="$(mktemp -d)"
STUB_PID=""
cleanup() {
  if [ -n "$STUB_PID" ]; then
    kill "$STUB_PID" 2>/dev/null
    wait "$STUB_PID" 2>/dev/null
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

command -v python3 >/dev/null 2>&1 || {
  echo "HARNESS ERROR: python3 is required to run the VirusTotal stub"
  exit 2
}

STATE="$WORK/state"
mkdir -p "$STATE" "$WORK/art"
STUB_VT_KEY="$KEY" python3 -I "$FIXTURES/stub-api.py" "$FIXTURES" "$STATE" &
STUB_PID=$!
for _ in $(seq 1 100); do
  [ -s "$STATE/port" ] && break
  sleep 0.1
done
[ -s "$STATE/port" ] || {
  echo "HARNESS ERROR: the VirusTotal stub did not start"
  exit 2
}
BASE="http://127.0.0.1:$(cat "$STATE/port")/api/v3"

art() { printf '%s\n' "$2" >"$WORK/art/$1"; echo "$WORK/art/$1"; }
CLEAN="$(art clean.js 'console.log("clean")')"
CLEAN2="$(art clean-copy.js 'console.log("clean")')"
MAL="$(art mal.bin 'payload VT-STUB-MALICIOUS')"
SUSP="$(art susp.bin 'payload VT-STUB-SUSPICIOUS')"
YARA="$(art yara.js 'payload VT-STUB-YARA')"
AISUSP="$(art ai.js 'payload VT-STUB-AI-SUSP')"
AIABSENT="$(art elf.bin 'payload VT-STUB-AI-ABSENT')"
AIODD="$(art odd.js 'payload VT-STUB-AI-ODD')"
WINAPI="$(art winapi.bin 'payload VT-STUB-WINAPI-INQUEST')"
WINAPI_YARA="$(art winapi-yara.bin 'payload VT-STUB-WINAPI-INQUEST VT-STUB-YARA')"
WINAPI_ELSE="$(art winapi-else.bin 'payload VT-STUB-WINAPI-ELSEWHERE')"
WINAPI_MAL="$(art winapi-mal.bin 'payload VT-STUB-WINAPI-INQUEST VT-STUB-MALICIOUS')"
MAL_SHA="$(shasum -a 256 "$MAL" | cut -d' ' -f1)"

REPORT="$WORK/report.json"
OUT=""
RC=0
ALL_OUT="$WORK/all-output.log"
: >"$ALL_OUT"

# scan [env assignments...] -- files...
scan() {
  local envs=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do envs+=("$1"); shift; done
  shift
  rm -f "$REPORT" "$STATE/requests.log" "$STATE/uploads.log"
  OUT="$(env VT_API_KEY="$KEY" VT_MIN_INTERVAL=0 VT_ANALYSIS_TIMEOUT=2 \
    ${envs[@]+"${envs[@]}"} bash "$SUT" --report "$REPORT" --api-base "$BASE" "$@" 2>&1)"
  RC=$?
  printf '%s\n' "$OUT" >>"$ALL_OUT"
  [ -f "$REPORT" ] && cat "$REPORT" >>"$ALL_OUT"
}

# expect NAME RC [substring]
expect() {
  local name="$1" want="$2" needle="${3:-}"
  if [ "$RC" -ne "$want" ]; then
    bad "$name: want exit $want, got $RC"
    printf '%s\n' "$OUT" | sed 's/^/        /' >&2
  elif [ -n "$needle" ] && ! grep -qF -- "$needle" <<<"$OUT"; then
    bad "$name: output lacks '$needle'"
    printf '%s\n' "$OUT" | sed 's/^/        /' >&2
  else
    ok "$name"
  fi
}

report_status() { jq -r '.status' "$REPORT" 2>/dev/null; }

echo "vt-scan.sh"

# 1. Clean: exit 0, report says clean, Code insights read as benign.
scan -- "$CLEAN"
expect "clean file -> exit 0" 0 "every file clean"
[ "$(report_status)" = "clean" ] && [ "$(jq -r '.files[0].code_insights[0].verdict' "$REPORT")" = "benign" ] &&
  ok "clean report records status and the Code insights verdict" ||
  bad "clean report: want status clean with a benign verdict, got $(cat "$REPORT" 2>/dev/null)"
grep -q 'key=ok' "$STATE/requests.log" && ! grep -qE 'key=(none|bad)' "$STATE/requests.log" &&
  ok "every request carries the key as an x-apikey header" ||
  bad "requests without the key header: $(cat "$STATE/requests.log")"

# 2-6. Each finding kind: exit 1, naming file, sha256 and engine or rule.
scan -- "$CLEAN" "$MAL"
expect "one malicious engine -> exit 1" 1 "::error::$MAL ($MAL_SHA): engine StubEngineA: malicious Stub.Forced.Detection"
[ "$(report_status)" = "findings" ] && ok "malicious: report status findings" || bad "malicious: report status $(report_status)"
scan -- "$SUSP"
expect "one suspicious engine -> exit 1" 1 "engine StubEngineB: suspicious Stub.Heuristic"
scan -- "$YARA"
expect "a YARA match -> exit 1, naming the rule" 1 "YARA rule Stub_Forced_Rule (stub_rules)"
scan -- "$AISUSP"
expect "Code insights suspicious -> exit 1" 1 "Code insights (Code Insight): suspicious"

# 6a-6d. The allowlisted YARA rule (#3019): recorded, never counted; nothing else exempt.
scan -- "$WINAPI"
expect "allowlisted YARA rule alone -> exit 0, named" 0 "YARA allowlisted: Windows_API_Function (https://github.com/InQuest/yara-rules-vt)"
jq -e '.status == "clean" and .files[0].yara == [] and .files[0].findings == []
  and .files[0].yara_allowlisted[0].rule_name == "Windows_API_Function"' "$REPORT" >/dev/null &&
  ok "allowlisted match recorded under yara_allowlisted, not as a finding" ||
  bad "allowlisted report: got $(jq -c '.files[0]' "$REPORT" 2>/dev/null)"
scan -- "$WINAPI_YARA"
expect "allowlisted rule plus another rule -> exit 1" 1 "YARA rule Stub_Forced_Rule (stub_rules)"
scan -- "$WINAPI_ELSE"
expect "allowlisted rule name from another source -> exit 1" 1 "YARA rule Windows_API_Function (Windows_API_Function)"
scan -- "$WINAPI_MAL"
expect "allowlisted rule plus a malicious engine -> exit 1" 1 "engine StubEngineA: malicious"

# 7. Absent Code insights is not a verdict: exit 0, recorded as not-produced.
scan -- "$AIABSENT"
expect "Code insights absent -> exit 0" 0 "Code insights not-produced"
[ "$(jq -r '.files[0].code_insights' "$REPORT")" = "not-produced" ] &&
  ok "absent Code insights recorded as not-produced" ||
  bad "absent Code insights: got $(jq -c '.files[0].code_insights' "$REPORT" 2>/dev/null)"

# 8. A Code insights field in an unknown shape fails closed.
scan -- "$AIODD"
expect "Code insights in an unknown shape -> exit 2" 2 "HARNESS ERROR: file object"

# 9-14. Harness errors: every one is exit 2 with a HARNESS ERROR line.
OUT="$(env -u VT_API_KEY bash "$SUT" --report "$REPORT" --api-base "$BASE" "$CLEAN" 2>&1)"
RC=$?
expect "missing key -> exit 2" 2 "HARNESS ERROR: VT_API_KEY is not set"
[ "$(report_status)" = "error" ] && ok "missing key: report status error" || bad "missing key: report $(report_status)"
scan 'VT_API_KEY=bad"key' -- "$CLEAN"
expect "malformed key -> exit 2" 2 "HARNESS ERROR: VT_API_KEY is malformed"
touch "$STATE/fail-upload"
scan -- "$CLEAN"
rm -f "$STATE/fail-upload"
expect "HTTP 500 -> exit 2" 2 "HARNESS ERROR: POST /api/v3/files: HTTP 500"
touch "$STATE/quota"
scan -- "$CLEAN"
rm -f "$STATE/quota"
expect "HTTP 429 -> exit 2, not retried" 2 "HTTP 429"
[ "$(grep -c . "$STATE/requests.log")" -eq 1 ] && ok "a 429 is not retried" ||
  bad "a 429 was retried: $(grep -c . "$STATE/requests.log") requests"
touch "$STATE/never-complete"
scan -- "$CLEAN"
rm -f "$STATE/never-complete"
expect "analysis never completes -> exit 2" 2 "did not complete within 2s"
touch "$STATE/garble-file"
scan -- "$CLEAN"
rm -f "$STATE/garble-file"
expect "unparseable JSON -> exit 2" 2 "the answer is not JSON"
touch "$STATE/wrong-sha"
scan -- "$CLEAN"
rm -f "$STATE/wrong-sha"
expect "analysis about another file -> exit 2" 2 "not $CLEAN"
scan --
expect "zero files -> exit 2" 2 "HARNESS ERROR: no files given"
scan -- "$WORK/art/does-not-exist"
expect "unreadable file -> exit 2" 2 "HARNESS ERROR: not a readable file"
OUT="$(env VT_API_KEY="$KEY" VT_MIN_INTERVAL=0 bash "$SUT" --report "$REPORT" --api-base "http://127.0.0.1:1/api/v3" "$CLEAN" 2>&1)"
RC=$?
expect "unreachable API -> exit 2" 2 "HARNESS ERROR: POST /api/v3/files: curl exit"

# 15. Deduplication: two paths with one digest are uploaded once, both reported.
scan -- "$CLEAN" "$CLEAN2"
expect "duplicate content -> exit 0" 0 "2 file(s), 1 unique by sha256"
[ "$(grep -c . "$STATE/uploads.log")" -eq 1 ] && [ "$(jq '.files[0].files | length' "$REPORT")" -eq 2 ] &&
  ok "identical files uploaded once and both reported" ||
  bad "dedup: uploads $(grep -c . "$STATE/uploads.log"), report $(jq -c '.files[0].files' "$REPORT")"

# 16. Over 32 MB takes the upload_url path (a sparse file, not 32 MB of disk).
BIG="$WORK/art/big.bin"
dd if=/dev/zero of="$BIG" bs=1 count=0 seek=33554433 2>/dev/null
scan -- "$BIG"
expect "over 32 MB -> exit 0 via upload_url" 0 "every file clean"
grep -q '^GET /api/v3/files/upload_url' "$STATE/requests.log" && grep -q '^large ' "$STATE/uploads.log" &&
  ok "over 32 MB uploaded through upload_url" ||
  bad "over 32 MB: requests $(tr '\n' ' ' <"$STATE/requests.log")"

# 16a. Any other upload URL is refused before anything is posted with the key.
for u in http://bigfiles.virustotal.com/upload http://www.virustotal.com/upload \
  https://bigfiles.virustotal.com.example.net/upload \
  https://bigfiles.virustotal.com@example.net/upload https://example.net/upload; do
  printf '%s\n' "$u" >"$STATE/upload-url"
  scan -- "$BIG"
  expect "upload_url $u -> exit 2" 2 "HARNESS ERROR: upload_url for"
  grep -q '^POST' "$STATE/requests.log" &&
    bad "upload_url $u: a POST was sent" || ok "upload_url $u: nothing posted"
done

# 16b. The large-file host is accepted. A refused proxy keeps the POST offline,
# so curl fails after the origin check passed; the token stays out of output.
printf '%s\n' "https://bigfiles.virustotal.com/_ah/upload/stub-token/" >"$STATE/upload-url"
scan https_proxy=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 NO_PROXY= no_proxy= \
  ALL_PROXY= all_proxy= -- "$BIG"
expect "upload_url on bigfiles.virustotal.com -> origin accepted" 2 "curl exit"
if grep -qF "is not on" <<<"$OUT"; then
  bad "bigfiles.virustotal.com upload_url was refused"
elif grep -qF "stub-token" <<<"$OUT"; then
  bad "the upload URL's token appears in output"
else
  ok "bigfiles.virustotal.com accepted; token not printed"
fi
rm -f "$STATE/upload-url"

# 18. Poll backoff: waits double from VT_MIN_INTERVAL and stop at the cap.
# Its own file: the stub counts polls per analysis for the whole suite.
SLOW="$(art slow.js 'console.log("slow")')"
printf '3\n' >"$STATE/pending-polls"
scan VT_MIN_INTERVAL=1 VT_POLL_MAX_INTERVAL=4 VT_ANALYSIS_TIMEOUT=60 -- "$SLOW"
rm -f "$STATE/pending-polls"
expect "pending analysis polled to completion" 0 "every file clean"
waits="$(sed -n 's/^  polling analysis of .* after \([0-9]*\)s$/\1/p' <<<"$OUT" | tr '\n' ' ')"
[ "$waits" = "1 2 4 4 " ] && [ "$(grep -c '^GET /api/v3/analyses/' "$STATE/requests.log")" -eq 4 ] &&
  ok "poll waits double and cap: 1 2 4 4" || bad "poll waits: want '1 2 4 4 ', got '$waits'"

# 19. Every report counts the requests the run sent: POST, analysis, file.
scan -- "$CLEAN"
[ "$(jq -r '.requests' "$REPORT")" = "3" ] && ok "report records requests: 3" ||
  bad "requests field: want 3, got $(jq -r '.requests' "$REPORT" 2>/dev/null)"

# 20. The request budget stops the run before it sends past VT_MAX_REQUESTS.
scan VT_MAX_REQUESTS=1 -- "$CLEAN"
expect "request budget reached -> exit 2" 2 "HARNESS ERROR: request budget spent: VT_MAX_REQUESTS=1"
[ "$(wc -l <"$STATE/requests.log" | tr -d ' ')" = "1" ] && [ "$(report_status)" = "error" ] &&
  [ "$(jq -r '.requests' "$REPORT")" = "1" ] &&
  ok "budget: one request sent, report error with requests 1" ||
  bad "budget: requests $(tr '\n' ' ' <"$STATE/requests.log"), report $(cat "$REPORT" 2>/dev/null)"

# 21. Invalid knobs are harness errors before any request.
scan VT_MAX_REQUESTS=0 -- "$CLEAN"
expect "VT_MAX_REQUESTS=0 -> exit 2" 2 "HARNESS ERROR: VT_MAX_REQUESTS"
scan VT_MIN_INTERVAL=10 VT_POLL_MAX_INTERVAL=5 -- "$CLEAN"
expect "VT_POLL_MAX_INTERVAL below VT_MIN_INTERVAL -> exit 2" 2 "HARNESS ERROR: VT_POLL_MAX_INTERVAL"

# 22. The stub binds without a reverse-DNS lookup (#2984): with
# socket.getfqdn made to raise, it must still publish a port and answer.
DNS_STATE="$WORK/dns-state"
mkdir -p "$DNS_STATE"
STUB_VT_KEY="$KEY" python3 -I -c "$(printf 'import runpy, socket, sys\ndef no_dns(*a, **k): raise RuntimeError(\"socket.getfqdn called at bind (#2984)\")\nsocket.getfqdn = no_dns\nsys.argv = sys.argv[1:]\nrunpy.run_path(sys.argv[0], run_name=\"__main__\")')" \
  "$FIXTURES/stub-api.py" "$FIXTURES" "$DNS_STATE" 2>"$WORK/dns.err" &
DNS_PID=$!
for _ in $(seq 1 50); do
  [ -s "$DNS_STATE/port" ] && break
  sleep 0.1
done
if [ -s "$DNS_STATE/port" ] &&
  curl -s -o /dev/null "http://127.0.0.1:$(cat "$DNS_STATE/port")/"; then
  ok "stub starts and answers with socket.getfqdn raising"
else
  bad "stub needs socket.getfqdn at bind: $(tail -1 "$WORK/dns.err")"
fi
kill "$DNS_PID" 2>/dev/null
wait "$DNS_PID" 2>/dev/null

# 17. The key never appears in any output or report written above.
if grep -qF "$KEY" "$ALL_OUT"; then
  bad "the API key appears in stdout, stderr or a report"
else
  ok "the API key never appears in stdout, stderr or a report"
fi

echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
