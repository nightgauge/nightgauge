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
# Each scan starts against an empty VirusTotal (a new stub epoch) unless
# KEEP_VT=1, which keeps every file the stub already analysed.
EPOCH=0
scan() {
  local envs=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do envs+=("$1"); shift; done
  shift
  if [ "${KEEP_VT:-0}" != 1 ]; then
    EPOCH=$((EPOCH + 1))
    echo "$EPOCH" >"$STATE/epoch"
  fi
  rm -f "$REPORT" "$STATE/requests.log" "$STATE/uploads.log" "$STATE/analyses.log"
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
expect "unreachable API -> exit 2" 2 "HARNESS ERROR: GET /api/v3/files/"

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

# 18. Poll backoff: waits double from VT_MIN_INTERVAL and stop at the cap. A
# file poll that shows no newer date switches to polling the analysis object
# alone (#3202); the stub's analysis completes on the 4th poll of either kind,
# an analysis poll, so one more file poll reads the report.
SLOW="$(art slow.js 'console.log("slow")')"
printf '3\n' >"$STATE/pending-polls"
scan VT_MIN_INTERVAL=1 VT_POLL_MAX_INTERVAL=4 VT_ANALYSIS_TIMEOUT=60 -- "$SLOW"
rm -f "$STATE/pending-polls"
expect "pending analysis polled to completion" 0 "every file clean"
waits="$(sed -n 's/^  polling analysis \(object \)\{0,1\}of .* after \([0-9]*\)s$/\2/p' <<<"$OUT" | tr '\n' ' ')"
kinds="$(sed -nE 's#^GET /api/v3/(files|analyses)/.*#\1#p' "$STATE/requests.log" | tr '\n' ' ')"
[ "$waits" = "1 2 4 4 4 " ] && [ "$kinds" = "files files analyses analyses analyses files " ] &&
  ok "poll waits double and cap: 1 2 4 4 4; then the analysis object alone, then the file" ||
  bad "poll waits: want '1 2 4 4 4 ' and lookup + file, analysis x3, file, got '$waits' and '$kinds'"

# 19. Every report counts the requests the run sent: lookup, POST, one poll.
scan -- "$CLEAN"
[ "$(jq -r '.requests' "$REPORT")" = "3" ] && ok "report records requests: 3" ||
  bad "requests field: want 3, got $(jq -r '.requests' "$REPORT" 2>/dev/null)"
jq -e '.files[0].source == "uploaded" and .sources == {uploaded: 1} and .files[0].engines_total == 70
  and (.files[0].analysed_at | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T"))' "$REPORT" >/dev/null &&
  ok "an unknown file is uploaded; the report records source, engines and analysis time" ||
  bad "uploaded report: $(jq -c '{sources, f: .files[0] | {source, engines_total, analysed_at}}' "$REPORT" 2>/dev/null)"
# 19-classes. Every run prints its requests by class, and the report's
# requests_by_class sums to requests (#3202).
expect "the run ends with its requests by class" 0 "vt-scan: 3 requests (lookups 1, uploads 1, reanalyses 0, polls 1)"
jq -e '.requests_by_class == {lookup: 1, upload: 1, reanalysis: 0, poll: 1}
  and ([.requests_by_class[]] | add) == .requests' "$REPORT" >/dev/null &&
  ok "report requests_by_class: lookup 1, upload 1, poll 1, summing to requests" ||
  bad "requests_by_class: $(jq -c '{requests, requests_by_class}' "$REPORT" 2>/dev/null)"

# 19a-19h. Reuse (session 86): a file VirusTotal analysed within
# VT_REUSE_MAX_AGE is judged from that analysis in 1 request; an older one is
# re-analysed without a re-upload; every report is judged by the same rules.
CLEAN_SHA="$(shasum -a 256 "$CLEAN" | cut -d' ' -f1)"
KEEP_VT=1 scan -- "$CLEAN"
expect "a fresh analysis is reused -> exit 0" 0 "reused report, analysed"
[ "$(jq -r '.requests' "$REPORT")" = "1" ] && ! grep -q '^POST' "$STATE/requests.log" &&
  jq -e '.files[0].source == "reused" and .sources == {reused: 1}' "$REPORT" >/dev/null &&
  ok "reuse: 1 request, nothing posted, source reused" ||
  bad "reuse: requests $(tr '\n' ' ' <"$STATE/requests.log"), report $(jq -c '.sources' "$REPORT" 2>/dev/null)"
scan -- "$MAL"
KEEP_VT=1 scan -- "$MAL"
expect "a reused report with a detection -> exit 1" 1 "engine StubEngineA: malicious Stub.Forced.Detection"
[ "$(jq -r '.requests' "$REPORT")" = "1" ] && ok "reuse never skips judging: the detection is found in 1 request" ||
  bad "reused detection: requests $(jq -r '.requests' "$REPORT" 2>/dev/null)"
scan -- "$CLEAN"
KEEP_VT=1 scan VT_REUSE_MAX_AGE=0 -- "$CLEAN"
expect "VT_REUSE_MAX_AGE=0 re-analyses a fresh file -> exit 0" 0 "reanalysed report"
grep -q "^POST /api/v3/files/$CLEAN_SHA/analyse " "$STATE/requests.log" && [ ! -s "$STATE/uploads.log" ] &&
  [ "$(jq -r '.requests' "$REPORT")" = "3" ] &&
  ok "VT_REUSE_MAX_AGE=0: reanalysed, not re-uploaded, 3 requests" ||
  bad "reuse off: requests $(tr '\n' ' ' <"$STATE/requests.log")"
# seed_scan AGE FILE [env...] — FILE is known to VirusTotal, last analysed AGE
# seconds ago, and is scanned against that.
seed_scan() {
  local age="$1" f="$2"
  shift 2
  echo "$(shasum -a 256 "$f" | cut -d' ' -f1) $(($(date +%s) - age)) $f" >"$STATE/seed"
  EPOCH=$((EPOCH + 1)); echo "$EPOCH" >"$STATE/epoch"
  KEEP_VT=1 scan "$@" -- "$f"
  rm -f "$STATE/seed"
}
ELF="$(art tool 'payload stub binary')"
printf '\177ELF' | cat - "$ELF" >"$ELF.tmp" && mv "$ELF.tmp" "$ELF"
ELF_SHA="$(shasum -a 256 "$ELF" | cut -d' ' -f1)"
seed_scan 172800 "$ELF"
expect "a binary analysed more than VT_REUSE_MAX_AGE ago is re-analysed -> exit 0" 0 "reanalysing $ELF ($ELF_SHA): last analysed"
grep -qx "$ELF_SHA" "$STATE/analyses.log" && [ ! -s "$STATE/uploads.log" ] &&
  jq -e '.files[0].source == "reanalysed" and .files[0].reuse_window == {kind: "binary", applied: null, max_age: 86400}' "$REPORT" >/dev/null &&
  ok "stale binary: POST /files/{sha256}/analyse, no upload, source reanalysed, kind binary" ||
  bad "stale binary: requests $(tr '\n' ' ' <"$STATE/requests.log"), entry $(jq -c '.files[0].reuse_window' "$REPORT" 2>/dev/null)"
# 19-windows. The long window (#3202): a clean text file VirusTotal analysed
# within VT_REUSE_MAX_AGE_TEXT (14 days) is judged in 1 request; a binary, a
# report with a finding, or an older report is re-analysed.
TEXT_OLD="$(art old-hook.sh 'echo "unchanged hook"')"
seed_scan 259200 "$TEXT_OLD"
expect "a clean text file analysed 3 days ago is reused -> exit 0" 0 "reused report, analysed"
[ "$(jq -r '.requests' "$REPORT")" = "1" ] && ! grep -q '^POST' "$STATE/requests.log" &&
  jq -e '.files[0].source == "reused" and .files[0].reuse_window == {kind: "text", applied: "long", max_age: 1209600}
    and .reuse_max_age_text == 1209600 and (.files[0].analysed_at | test("^[0-9]{4}-"))' "$REPORT" >/dev/null &&
  ok "long window: 1 request, nothing posted, reuse_window text/long and the analysis date recorded" ||
  bad "long window: requests $(tr '\n' ' ' <"$STATE/requests.log"), entry $(jq -c '.files[0] | {source, reuse_window, analysed_at}' "$REPORT" 2>/dev/null)"
seed_scan 259200 "$ELF"
expect "a binary analysed 3 days ago is re-analysed -> exit 0" 0 "reanalysed report"
grep -qx "$ELF_SHA" "$STATE/analyses.log" && ok "long window: never for a binary" ||
  bad "long window applied to a binary: $(tr '\n' ' ' <"$STATE/requests.log")"
TEXT_MAL="$(art old-mal.js 'payload VT-STUB-MALICIOUS old text')"
TEXT_MAL_SHA="$(shasum -a 256 "$TEXT_MAL" | cut -d' ' -f1)"
seed_scan 259200 "$TEXT_MAL"
expect "a text file whose 3-day-old report has a finding -> re-analysed, exit 1" 1 "carries a finding, so it is re-analysed"
grep -qx "$TEXT_MAL_SHA" "$STATE/analyses.log" && jq -e '.files[0].source == "reanalysed"' "$REPORT" >/dev/null &&
  ok "long window: a finding is never reused, it is re-analysed" ||
  bad "finding reused under the long window: $(tr '\n' ' ' <"$STATE/requests.log")"
seed_scan 1296000 "$TEXT_OLD"
expect "a text file analysed 15 days ago is re-analysed -> exit 0" 0 "reanalysed report"
seed_scan 259200 "$TEXT_OLD" VT_REUSE_MAX_AGE_TEXT=86400
expect "VT_REUSE_MAX_AGE_TEXT=86400 re-analyses a 3-day-old text file" 0 "reanalysed report"
seed_scan 259200 "$TEXT_OLD" VT_REUSE_MAX_AGE=0
expect "VT_REUSE_MAX_AGE=0 turns the long window off too" 0 "reanalysed report"
seed_scan 3600 "$TEXT_OLD"
jq -e '.files[0].reuse_window == {kind: "text", applied: "short", max_age: 86400}' "$REPORT" >/dev/null &&
  ok "a report inside 24 hours records the short window" ||
  bad "short window entry: $(jq -c '.files[0].reuse_window' "$REPORT" 2>/dev/null)"
# The kind comes from the bytes: a NUL byte, or an archive name, makes a binary.
NUL_FILE="$WORK/art/data.txt"
printf 'looks like text\000but is not\n' >"$NUL_FILE"
seed_scan 259200 "$NUL_FILE"
jq -e '.files[0].reuse_window.kind == "binary" and .files[0].source == "reanalysed"' "$REPORT" >/dev/null &&
  ok "a NUL byte makes a .txt file binary: short window" ||
  bad "NUL file kind: $(jq -c '.files[0] | {source, reuse_window}' "$REPORT" 2>/dev/null)"
NAMED_VSIX="$(art plain.vsix 'plain text named as a vsix')"
seed_scan 259200 "$NAMED_VSIX"
jq -e '.files[0].reuse_window.kind == "binary" and .files[0].source == "reanalysed"' "$REPORT" >/dev/null &&
  ok "a .vsix name makes a file binary: short window" ||
  bad "vsix kind: $(jq -c '.files[0] | {source, reuse_window}' "$REPORT" 2>/dev/null)"

SEEDED_MAL="$(art seeded-mal.bin 'payload VT-STUB-MALICIOUS seeded')"
SEEDED_MAL_SHA="$(shasum -a 256 "$SEEDED_MAL" | cut -d' ' -f1)"
EPOCH=$((EPOCH + 1)); echo "$EPOCH" >"$STATE/epoch"
echo "$SEEDED_MAL_SHA $(($(date +%s) - 3600)) $SEEDED_MAL" >"$STATE/seed"
KEEP_VT=1 scan -- "$SEEDED_MAL"
rm -f "$STATE/seed"
expect "a detection in a report another client made is still a finding -> exit 1" 1 "engine StubEngineA: malicious"
touch "$STATE/few-engines"
scan -- "$CLEAN"
expect "a report listing too few engines -> exit 2" 2 "fewer than VT_MIN_ENGINES=40: it is incomplete"
rm -f "$STATE/few-engines"
scan -- "$CLEAN"
touch "$STATE/few-engines-once"
KEEP_VT=1 scan -- "$CLEAN"
expect "a fresh report with too few engines is re-analysed, not reused" 0 "reanalysed report"
scan -- "$CLEAN"
touch "$STATE/fail-analyse"
KEEP_VT=1 scan VT_REUSE_MAX_AGE=0 -- "$CLEAN"
rm -f "$STATE/fail-analyse"
expect "HTTP 500 on reanalysis -> exit 2" 2 "HARNESS ERROR: POST /api/v3/files/$CLEAN_SHA/analyse: HTTP 500"

# 19-instant. A reanalysis VirusTotal completes at once ends on the first file
# poll: 3 requests (lookup, reanalysis, one poll), never a poll to the timeout.
scan -- "$CLEAN"
instant_start="$(date +%s)"
KEEP_VT=1 scan VT_REUSE_MAX_AGE=0 VT_ANALYSIS_TIMEOUT=30 -- "$CLEAN"
instant_secs=$(($(date +%s) - instant_start))
expect "a reanalysis that completes at once -> exit 0" 0 "reanalysed report"
[ "$(jq -r '.requests' "$REPORT")" = "3" ] && [ "$instant_secs" -lt 15 ] &&
  jq -e '.files[0].source == "reanalysed" and .requests_by_class.poll == 1' "$REPORT" >/dev/null &&
  ok "instant reanalysis: 3 requests in ${instant_secs}s, source reanalysed" ||
  bad "instant reanalysis: ${instant_secs}s, requests $(tr '\n' ' ' <"$STATE/requests.log")"
# 19-fold. 2026-11-11 (#3202): VirusTotal completed a reanalysis without moving
# the file's last_analysis_date. The scan stops at once (exit 2, re-run) and
# never judges the lookup's report as the reanalysis, however fresh it is.
scan -- "$CLEAN"
touch "$STATE/fold-analyse"
fold_start="$(date +%s)"
KEEP_VT=1 scan VT_REUSE_MAX_AGE=0 VT_ANALYSIS_TIMEOUT=30 -- "$CLEAN"
fold_secs=$(($(date +%s) - fold_start))
rm -f "$STATE/fold-analyse"
expect "a completed analysis with no newer report -> exit 2 at once" 2 "that report is not accepted as a reanalysis"
[ "$(jq -r '.requests' "$REPORT")" -le 5 ] && [ "$fold_secs" -lt 15 ] &&
  jq -e '[.files[] | select(.source == "reanalysed")] | length == 0' "$REPORT" >/dev/null &&
  ok "no newer report: stopped in ${fold_secs}s after $(jq -r '.requests' "$REPORT") requests, nothing judged reanalysed" ||
  bad "no newer report: ${fold_secs}s, report $(jq -c '{requests, files}' "$REPORT" 2>/dev/null)"
# 19-fold-dated. The same fold, with the analysis dated at its creation (newer
# than the lookup): the file is read a bounded number of times, then the scan
# stops; it never polls to the timeout.
scan -- "$CLEAN"
touch "$STATE/fold-analyse" "$STATE/fold-analyse-dated"
fold_start="$(date +%s)"
KEEP_VT=1 scan VT_REUSE_MAX_AGE=0 VT_MIN_INTERVAL=1 VT_POLL_MAX_INTERVAL=1 VT_ANALYSIS_TIMEOUT=60 -- "$CLEAN"
fold_secs=$(($(date +%s) - fold_start))
rm -f "$STATE/fold-analyse" "$STATE/fold-analyse-dated"
expect "a folded analysis dated newer -> exit 2 after bounded file reads" 2 "after 3 file reads"
[ "$fold_secs" -lt 30 ] && [ "$(jq -r '.requests_by_class.poll' "$REPORT")" -le 5 ] &&
  jq -e '[.files[] | select(.source == "reanalysed")] | length == 0' "$REPORT" >/dev/null &&
  ok "dated fold: stopped in ${fold_secs}s after $(jq -r '.requests_by_class.poll' "$REPORT") polls, nothing judged reanalysed" ||
  bad "dated fold: ${fold_secs}s, report $(jq -c '{requests_by_class, files}' "$REPORT" 2>/dev/null)"
# 19-fold-stale. The operator's case: an executable whose report is 3 days old
# is re-analysed, and its old report never passes as reanalysed.
touch "$STATE/fold-analyse"
fold_start="$(date +%s)"
seed_scan 259200 "$ELF" VT_ANALYSIS_TIMEOUT=30
fold_secs=$(($(date +%s) - fold_start))
rm -f "$STATE/fold-analyse"
expect "a 3-day-old executable report with no newer one -> exit 2" 2 "that report is not accepted as a reanalysis"
grep -q "^POST /api/v3/files/$ELF_SHA/analyse " "$STATE/requests.log" && [ "$fold_secs" -lt 15 ] &&
  jq -e '[.files[] | select(.source == "reanalysed")] | length == 0' "$REPORT" >/dev/null &&
  ok "stale executable: re-analysed, stopped in ${fold_secs}s, never judged reanalysed" ||
  bad "stale executable: ${fold_secs}s, requests $(tr '\n' ' ' <"$STATE/requests.log"), report $(jq -c '.files' "$REPORT" 2>/dev/null)"
# 19-window. A newer report still has to fall inside the file's window: an
# executable's reanalysis dated two days ago (newer than its 3-day-old lookup)
# is not judged reanalysed; the poll runs on to the timeout.
printf '172800\n' >"$STATE/analysis-age"
seed_scan 259200 "$ELF" VT_MIN_INTERVAL=1 VT_POLL_MAX_INTERVAL=1 VT_ANALYSIS_TIMEOUT=4
rm -f "$STATE/analysis-age"
expect "a newer executable report outside 24 hours is not judged -> exit 2" 2 "did not complete within 4s"
grep -qF 'newer than the lookup' <<<"$OUT" &&
  jq -e '[.files[] | select(.source == "reanalysed")] | length == 0' "$REPORT" >/dev/null &&
  ok "window guard: a 2-day-old reanalysis of an executable never passes" ||
  bad "window guard: report $(jq -c '.files' "$REPORT" 2>/dev/null)"
# 19-lag. A completed analysis newer than the file's report means the file
# object lags: the old report must never be judged as a folded reanalysis.
scan -- "$CLEAN"
printf '2\n' >"$STATE/file-lag"
KEEP_VT=1 scan VT_REUSE_MAX_AGE=0 VT_MIN_INTERVAL=1 VT_POLL_MAX_INTERVAL=1 VT_ANALYSIS_TIMEOUT=30 -- "$CLEAN"
rm -f "$STATE/file-lag"
expect "a lagging file object is polled until its date moves -> exit 0" 0 "polling analysis of"
[ "$(jq -r '.requests' "$REPORT")" = "6" ] &&
  jq -e '.files[0].source == "reanalysed" and .requests_by_class.poll == 4' "$REPORT" >/dev/null &&
  ok "lagging file object: 6 requests (lookup, reanalysis, file, analysis, file, file), the new report judged" ||
  bad "lagging file object: report $(jq -c '{requests, requests_by_class, f: .files[0]}' "$REPORT" 2>/dev/null), requests $(tr '\n' ' ' <"$STATE/requests.log")"
printf 'bogus\n' >"$STATE/analysis-status"
printf '5\n' >"$STATE/pending-polls"
scan -- "$CLEAN"
rm -f "$STATE/analysis-status" "$STATE/pending-polls"
expect "an analysis in an unknown status -> exit 2" 2 "answered status 'bogus'"
expect "a harness error still prints the requests by class" 2 "vt-scan: 4 requests (lookups 1, uploads 1, reanalyses 0, polls 2)"

# 19-replay. rc.3 (run 43029170580) through the stub (#3202): its 31 files,
# their kinds, recorded sources, ages and polls. Under yesterday's 24-hour
# rule for every file the replay spends rc.3's classes again; under the
# defaults it must spend 60 or fewer.
REPLAY="$WORK/rc3"
mkdir -p "$REPLAY"
REPLAY_FILES=()
REPLAY_SEED="$WORK/rc3-seed"
: >"$REPLAY_SEED"
rm -f "$STATE"/pending-*
rec_lookups=0 rec_upload=0 rec_reanalysis=0 rec_polls=0 rec_slow=0
while read -r name kind size source age polls; do
  case "$name" in '#'* | '') continue ;; esac
  f="$REPLAY/$name"
  if [ "$kind" = text ]; then
    printf 'rc3 replay %s\n' "$name" >"$f"
  else
    case "$name" in
    *.vsix) printf 'PK\003\004' >"$f" ;;
    *.tar.gz) printf '\037\213' >"$f" ;;
    *) printf '\177ELF' >"$f" ;;
    esac
    printf 'rc3 replay %s\n' "$name" >>"$f"
  fi
  [ "$size" -le 33554432 ] || dd if=/dev/zero of="$f" bs=1 count=1 seek=33554432 conv=notrunc 2>/dev/null
  sha="$(shasum -a 256 "$f" | cut -d' ' -f1)"
  rec_lookups=$((rec_lookups + 1))
  rec_polls=$((rec_polls + polls))
  [ "$polls" -le 1 ] || { printf '%s\n' "$((polls - 1))" >"$STATE/pending-$sha"; rec_slow=$((rec_slow + 1)); }
  case "$source" in
  uploaded) rec_upload=$((rec_upload + 1)); [ "$size" -le 33554432 ] || rec_upload=$((rec_upload + 1)) ;;
  reanalysed) rec_reanalysis=$((rec_reanalysis + 1)) ;;
  esac
  [ "$age" = - ] || printf '%s %s %s\n' "$sha" "$age" "$f" >>"$REPLAY_SEED"
  REPLAY_FILES+=("$f")
done <"$FIXTURES/rc3-files.tsv"
rec_total=$((rec_lookups + rec_upload + rec_reanalysis + rec_polls))
replay() {
  awk -v now="$(date +%s)" '{ print $1, now - $2, $3 }' "$REPLAY_SEED" >"$STATE/seed"
  EPOCH=$((EPOCH + 1)); echo "$EPOCH" >"$STATE/epoch"
  KEEP_VT=1 scan "$@" -- "${REPLAY_FILES[@]}"
  rm -f "$STATE/seed"
}
[ "${#REPLAY_FILES[@]}" = 31 ] && [ "$rec_total" = 108 ] &&
  ok "rc.3 fixture: 31 files, 108 recorded requests ($rec_lookups lookups, $rec_upload uploads, $rec_reanalysis reanalyses, $rec_polls polls)" ||
  bad "rc.3 fixture: ${#REPLAY_FILES[@]} files, $rec_total recorded requests"
replay VT_REUSE_MAX_AGE_TEXT=86400
expect "rc.3 replay under the 24-hour rule -> exit 0" 0 "every file clean"
before="$(jq -r '.requests' "$REPORT")"
# Every rc.3 file that took more than one poll completes on an analysis-object
# poll in the replay, so each takes one more file poll than rc.3's file-only
# loop to read the report.
jq -e --argjson l "$rec_lookups" --argjson u "$rec_upload" --argjson r "$rec_reanalysis" --argjson p "$rec_polls" --argjson s "$rec_slow" \
  '.requests_by_class == {lookup: $l, upload: $u, reanalysis: $r, poll: ($p + $s)}' "$REPORT" >/dev/null &&
  ok "rc.3 replay before: $before requests, rc.3's recorded $rec_total plus $rec_slow file reads after a completed analysis" ||
  bad "rc.3 replay before: $(jq -c '{requests, requests_by_class}' "$REPORT" 2>/dev/null)"
replay
expect "rc.3 replay under the defaults -> exit 0" 0 "every file clean"
after="$(jq -r '.requests' "$REPORT")"
[ "$after" -le 60 ] && jq -e '.sources == {reused: 23, uploaded: 8}' "$REPORT" >/dev/null &&
  ok "rc.3 replay after: $after requests (<= 60, from $rec_total recorded): 23 text files reused, 8 uploaded" ||
  bad "rc.3 replay after: $(jq -c '{requests, requests_by_class, sources}' "$REPORT" 2>/dev/null)"
echo "  rc.3 replay: recorded $rec_total requests, replayed before $before, after $after"
rm -f "$STATE"/pending-*

# 20. The request budget stops the run before it sends past VT_MAX_REQUESTS.
scan VT_MAX_REQUESTS=1 -- "$CLEAN"
expect "request budget reached -> exit 2" 2 "HARNESS ERROR: request budget spent: VT_MAX_REQUESTS=1"
[ "$(wc -l <"$STATE/requests.log" | tr -d ' ')" = "1" ] && [ "$(report_status)" = "error" ] &&
  [ "$(jq -r '.requests' "$REPORT")" = "1" ] &&
  jq -e '.requests_by_class.lookup == 1' "$REPORT" >/dev/null &&
  ok "budget: one request sent, report error with requests 1" ||
  bad "budget: requests $(tr '\n' ' ' <"$STATE/requests.log"), report $(cat "$REPORT" 2>/dev/null)"

# 21. Invalid knobs are harness errors before any request.
scan VT_MAX_REQUESTS=0 -- "$CLEAN"
expect "VT_MAX_REQUESTS=0 -> exit 2" 2 "HARNESS ERROR: VT_MAX_REQUESTS"
scan VT_MIN_INTERVAL=10 VT_POLL_MAX_INTERVAL=5 -- "$CLEAN"
expect "VT_POLL_MAX_INTERVAL below VT_MIN_INTERVAL -> exit 2" 2 "HARNESS ERROR: VT_POLL_MAX_INTERVAL"
scan VT_REUSE_MAX_AGE=1d -- "$CLEAN"
expect "VT_REUSE_MAX_AGE not in seconds -> exit 2" 2 "HARNESS ERROR: VT_REUSE_MAX_AGE"
scan VT_REUSE_MAX_AGE_TEXT=2w -- "$CLEAN"
expect "VT_REUSE_MAX_AGE_TEXT not in seconds -> exit 2" 2 "HARNESS ERROR: VT_REUSE_MAX_AGE_TEXT must be"
scan VT_REUSE_MAX_AGE_TEXT=3600 -- "$CLEAN"
expect "VT_REUSE_MAX_AGE_TEXT below VT_REUSE_MAX_AGE -> exit 2" 2 "HARNESS ERROR: VT_REUSE_MAX_AGE_TEXT (3600) is below"
scan VT_MIN_ENGINES=0 -- "$CLEAN"
expect "VT_MIN_ENGINES=0 -> exit 2" 2 "HARNESS ERROR: VT_MIN_ENGINES"

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
