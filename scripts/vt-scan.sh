#!/usr/bin/env bash
#
# vt-scan.sh — submit files to VirusTotal (API v3) and fail on any finding
# (#2946).
#
# WHY THIS EXISTS
#
# v0.8.0 shipped a binary with a 1/63 VirusTotal detection for about eight
# hours. The release path's only pre-publish scan was ClamAV, one engine, and
# the multi-engine check ran by hand after publication against public URLs.
# release-scan-gate.sh runs this script on a DRAFT release's own assets, and
# publishes the release only when this script exits 0.
#
# THE FREE KEY IS ENOUGH
#
# The public API serves GET /files/{sha256}, POST /files, POST
# /files/{sha256}/analyse, and GET /files/upload_url for files over 32 MB, at 4
# requests/min and 500/day:
#   https://docs.virustotal.com/reference/public-vs-premium-api
#   https://docs.virustotal.com/reference/file-info
#   https://docs.virustotal.com/reference/files-scan
#   https://docs.virustotal.com/reference/files-analyse
#   https://docs.virustotal.com/reference/files-upload-url
#
# REQUESTS PER FILE
#
# Every unique file (by sha256) is first looked up: GET /files/{sha256}.
#
#   - VirusTotal analysed it within VT_REUSE_MAX_AGE (default 24 hours), and
#     that analysis lists at least VT_MIN_ENGINES engines: its report is judged
#     exactly as a new one would be. 1 request. A release ships the hooks,
#     skill scripts and bundles its RC carried byte for byte, and the RC scan
#     analysed them hours earlier: re-analysing them spent two thirds of every
#     release scan (session 86).
#   - VirusTotal knows it, but its last analysis is older: POST
#     /files/{sha256}/analyse re-analyses it without a re-upload. 3 requests:
#     lookup, reanalysis, and at least one poll.
#   - VirusTotal does not know it (404): it is uploaded. 3 requests, or 4 over
#     32 MB.
#
# A submitted file is polled with GET /files/{sha256} until its
# last_analysis_date is later than the one the lookup saw. That one answer
# carries the engine results, the crowdsourced YARA matches and Code insights,
# so no separate GET /analyses/{id} is spent. VirusTotal writes the file's
# last_analysis_* fields when the analysis completes, and a report listing
# fewer than VT_MIN_ENGINES engines is refused as incomplete (exit 2).
#
# This script spaces its own requests to stay under 4/min, backs off between
# polls of one analysis, and stops at VT_MAX_REQUESTS requests per run so one
# slow analysis cannot spend the day's quota. It does not retry a
# 429: with the requests already spaced, a 429 means the daily or monthly quota
# is spent or another client shares the key, and waiting inside one job fixes
# neither. It exits 2, and the release stays a draft for a later re-run.
#
# WHAT COUNTS AS A FINDING (exit 1)
#
#   - one or more engines report the file `malicious` or `suspicious`;
#   - any crowdsourced YARA rule matches, except a rule in YARA_ALLOWLIST,
#     matched by rule name and source. An allowlisted match is recorded under
#     `yara_allowlisted` and named in the summary line, not counted;
#   - a Code insights verdict is present and is not `benign`.
#
# VirusTotal does not produce Code insights for every file type (ELF binaries
# among them). An absent field is recorded as "not-produced" and is not a
# verdict. A field present in a shape this script does not know is exit 2.
#
# WHAT IS A HARNESS ERROR (exit 2)
#
# Anything other than a parsed, complete answer: no key, a malformed key, no
# files, an unreadable file, an HTTP error or timeout, a 429, an analysis that
# does not complete in time, an answer about a different file, or a body that
# does not parse. Each prints one `HARNESS ERROR:` line. The gate fails closed.
#
# THE KEY
#
# VT_API_KEY is read from the environment and handed to curl as a config on
# stdin (`curl -K -`), so it never appears on a command line, in `ps`, in
# `set -x` output or in this script's messages and report.
#
# Usage:
#   VT_API_KEY=... scripts/vt-scan.sh --report <out.json> [--api-base URL] FILE...
#
# UPLOAD ORIGINS
#
# A file over 32 MB is posted to the URL GET /files/upload_url returns. The key
# goes with it, so that URL must be on the API's own origin or on
# https://bigfiles.virustotal.com; any other URL is a HARNESS ERROR before the
# key is sent.
#
# Environment (all optional except VT_API_KEY):
#   VT_API_BASE            API root. Default https://www.virustotal.com/api/v3.
#                          Overridden only by the offline test suites.
#   VT_MIN_INTERVAL        Seconds between requests. Default 15 (4/min).
#   VT_ANALYSIS_TIMEOUT    Seconds to wait for one file's analysis. Default 1800.
#   VT_HTTP_TIMEOUT        Seconds per HTTP request. Default 600.
#   VT_POLL_MAX_INTERVAL   Longest wait between polls of one analysis. The
#                          first wait is VT_MIN_INTERVAL and each later one
#                          doubles, up to this. Default 300.
#   VT_MAX_REQUESTS        Most API requests one run may send. Reaching it is a
#                          HARNESS ERROR, not a retry. Default 150.
#   VT_REUSE_MAX_AGE       Seconds within which VirusTotal's last analysis of a
#                          file is judged instead of re-analysed. Default 86400.
#                          0 re-analyses every file.
#   VT_MIN_ENGINES         Fewest engines a judged report may list. Default 40.
#
# Exit: 0 every file clean; 1 any finding; 2 HARNESS ERROR.
#
# Portable to the macOS runner's bash 3.2: no associative arrays, no mapfile.
set -uo pipefail

REPORT=""
API_BASE="${VT_API_BASE:-https://www.virustotal.com/api/v3}"
MIN_INTERVAL="${VT_MIN_INTERVAL:-15}"
ANALYSIS_TIMEOUT="${VT_ANALYSIS_TIMEOUT:-1800}"
HTTP_TIMEOUT="${VT_HTTP_TIMEOUT:-600}"
POLL_MAX_INTERVAL="${VT_POLL_MAX_INTERVAL:-300}"
MAX_REQUESTS="${VT_MAX_REQUESTS:-150}"
REUSE_MAX_AGE="${VT_REUSE_MAX_AGE:-86400}"
MIN_ENGINES="${VT_MIN_ENGINES:-40}"
LARGE_FILE_BYTES=33554432 # 32 MB: above this, POST /files refuses the upload.
# VirusTotal answers GET /files/upload_url with a URL on this host. It is the
# only origin besides the API's own that may receive the key.
VT_UPLOAD_ORIGIN="https://bigfiles.virustotal.com"
# Crowdsourced YARA rules that are recorded but never count as a finding, as
# "rule_name|source", matched exactly. Owner decision (#3019): InQuest's
# Windows_API_Function rule matches every Go binary (os.ReadFile, os.WriteFile)
# and describes itself as "not an indication of malicious behavior". It is no
# engine verdict and is never hidden: each report records it under
# yara_allowlisted and the summary line names it. The owner kept it alongside
# the rule that a finding is never allowlisted (#3106; docs/RELEASE_BINARIES.md).
# Adding an entry here is an owner decision, never a session's.
YARA_ALLOWLIST=("Windows_API_Function|https://github.com/InQuest/yara-rules-vt")
FILES=()

WORK=""
REPORT_STATUS="error"
REPORT_ERROR=""
ENTRIES="[]"
REQUESTS=0 # API requests sent this run, failed ones included: each spends quota.

write_report() {
  [ -n "$REPORT" ] || return 0
  jq -n \
    --arg status "$REPORT_STATUS" \
    --arg error "$REPORT_ERROR" \
    --arg api "$API_BASE" \
    --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson files "$ENTRIES" \
    --argjson requests "$REQUESTS" \
    --argjson reuse "$REUSE_MAX_AGE" \
    '{tool: "vt-scan.sh", api: $api, scanned_at: $at, status: $status,
      error: (if $error == "" then null else $error end), requests: $requests,
      reuse_max_age: $reuse,
      sources: ($files | group_by(.source) | map({key: .[0].source, value: length}) | from_entries),
      files: $files}' \
    >"$REPORT" 2>/dev/null || echo "WARNING: could not write $REPORT" >&2
}

cleanup() {
  [ -n "$WORK" ] && rm -rf "$WORK"
  return 0
}
trap cleanup EXIT

harness() {
  REPORT_STATUS="error"
  REPORT_ERROR="$*"
  echo "HARNESS ERROR: $*" >&2
  write_report
  exit 2
}

need_value() { [ "$2" -ge 2 ] || harness "$1 needs a value"; }

while [ $# -gt 0 ]; do
  case "$1" in
  --report) need_value "$1" $#; REPORT="$2"; shift 2 ;;
  --api-base) need_value "$1" $#; API_BASE="$2"; shift 2 ;;
  -h | --help) sed -n '2,/^set -uo/p' "$0" | sed -e '$d' -e 's/^# \{0,1\}//'; exit 0 ;;
  --) shift; while [ $# -gt 0 ]; do FILES+=("$1"); shift; done ;;
  -*) harness "unknown option '$1'" ;;
  *) FILES+=("$1"); shift ;;
  esac
done

command -v jq >/dev/null 2>&1 || harness "jq is required"
command -v curl >/dev/null 2>&1 || harness "curl is required"
[ -n "$REPORT" ] || harness "--report is required"

for n in "MIN_INTERVAL:$MIN_INTERVAL" "ANALYSIS_TIMEOUT:$ANALYSIS_TIMEOUT" "HTTP_TIMEOUT:$HTTP_TIMEOUT" \
  "POLL_MAX_INTERVAL:$POLL_MAX_INTERVAL"; do
  [[ "${n#*:}" =~ ^[0-9]{1,6}$ ]] || harness "VT_${n%%:*} must be a whole number of seconds"
done
[[ "$MAX_REQUESTS" =~ ^[0-9]{1,6}$ ]] && [ "$MAX_REQUESTS" -gt 0 ] ||
  harness "VT_MAX_REQUESTS must be a whole number above 0"
[[ "$REUSE_MAX_AGE" =~ ^[0-9]{1,7}$ ]] || harness "VT_REUSE_MAX_AGE must be a whole number of seconds"
[[ "$MIN_ENGINES" =~ ^[0-9]{1,3}$ ]] && [ "$MIN_ENGINES" -gt 0 ] ||
  harness "VT_MIN_ENGINES must be a whole number above 0"
# A cap below the floor would poll faster than the 4/min spacing allows.
[ "$POLL_MAX_INTERVAL" -ge "$MIN_INTERVAL" ] ||
  harness "VT_POLL_MAX_INTERVAL ($POLL_MAX_INTERVAL) is below VT_MIN_INTERVAL ($MIN_INTERVAL)"
[[ "$API_BASE" =~ ^https?://[^/]+(/.*)?$ ]] || harness "API base is not an http(s) URL"
API_BASE="${API_BASE%/}"
API_ORIGIN="$(sed -E 's#^(https?://[^/]+).*#\1#' <<<"$API_BASE")"

# The key is never printed, not even in part. Its shape is checked because it
# is written into a curl config: a quote or newline in it would change the config.
[ -n "${VT_API_KEY:-}" ] || harness "VT_API_KEY is not set: no file was scanned, so nothing may publish"
[[ "$VT_API_KEY" =~ ^[A-Za-z0-9]{16,128}$ ]] || harness "VT_API_KEY is malformed (expected 16-128 letters and digits)"

[ "${#FILES[@]}" -gt 0 ] || harness "no files given: a scan of nothing is not a clean scan"

WORK="$(mktemp -d)" || harness "mktemp failed"

sha256_of() { shasum -a 256 "$1" 2>/dev/null | cut -d' ' -f1; }
size_of() { wc -c <"$1" 2>/dev/null | tr -d ' '; }

# --- Requests ---------------------------------------------------------------
LAST_REQUEST=0
# Seconds the next request waits after the previous one. The poll loop raises
# it for one request; vt_request resets it to MIN_INTERVAL after each send.
NEXT_GAP="$MIN_INTERVAL"
BODY="$WORK/body.json"

# vt_request METHOD URL [curl args...] — body lands in $BODY and the status in
# HTTP_STATUS. Anything but HTTP 200 is a harness error, except a 404
# NotFoundError while ALLOW_404=1 (a file VirusTotal does not know). The
# message names the endpoint, never the key.
ALLOW_404=0
HTTP_STATUS=""
vt_request() {
  local method="$1" url="$2" now wait code where
  shift 2
  case "$url" in
  "$API_ORIGIN"/*) where="${url#"$API_ORIGIN"}" ;;
  # An upload URL carries a one-time token in its path: report only its origin.
  *) where="$(sed -E 's#^(https?://[^/]+).*#\1#' <<<"$url")" ;;
  esac
  where="${where%%\?*}"
  [ "$REQUESTS" -lt "$MAX_REQUESTS" ] ||
    harness "request budget spent: VT_MAX_REQUESTS=$MAX_REQUESTS reached before $method $where; re-run later or raise it"
  now="$(date +%s)"
  wait=$((LAST_REQUEST + NEXT_GAP - now))
  [ "$wait" -gt 0 ] && sleep "$wait"
  NEXT_GAP="$MIN_INTERVAL"
  REQUESTS=$((REQUESTS + 1))
  LAST_REQUEST="$(date +%s)"
  : >"$BODY"
  code="$(printf 'header = "x-apikey: %s"\n' "$VT_API_KEY" |
    curl -K - -sS -X "$method" --max-time "$HTTP_TIMEOUT" \
      -o "$BODY" -w '%{http_code}' "$@" "$url" 2>"$WORK/curl.err")"
  local rc=$?
  if [ "$rc" -ne 0 ]; then
    harness "$method $where: curl exit $rc ($(head -c 200 "$WORK/curl.err" | tr '\n' ' '))"
  fi
  if [ "$code" = "429" ]; then
    harness "$method $where: HTTP 429, the VirusTotal quota is spent (4/min, 500/day on the free key); re-run later"
  fi
  HTTP_STATUS="$code"
  if [ "$code" = "404" ] && [ "$ALLOW_404" = 1 ] &&
    [ "$(jq -r '.error.code // empty' "$BODY" 2>/dev/null)" = "NotFoundError" ]; then
    return 0
  fi
  if [ "$code" != "200" ]; then
    local why
    why="$(jq -r '.error | "\(.code // "?"): \(.message // "")"' "$BODY" 2>/dev/null)"
    why="${why:0:200}"
    harness "$method $where: HTTP ${code:-none} ${why:-}"
  fi
  jq -e . "$BODY" >/dev/null 2>&1 || harness "$method $where: the answer is not JSON"
}

# --- Deduplicate by digest ----------------------------------------------------
# Lines of "sha256<TAB>size<TAB>path"; the first path for a digest is scanned,
# every path is reported.
INDEX="$WORK/index.tsv"
: >"$INDEX"
for f in "${FILES[@]}"; do
  [ -f "$f" ] && [ -r "$f" ] || harness "not a readable file: $f"
  case "$f" in *'"'* | *$'\n'* | *';'*) harness "unsupported character in file name: $f" ;; esac
  sha="$(sha256_of "$f")"
  [[ "$sha" =~ ^[0-9a-f]{64}$ ]] || harness "cannot hash $f"
  printf '%s\t%s\t%s\n' "$sha" "$(size_of "$f")" "$f" >>"$INDEX"
done
UNIQUE="$(cut -f1 "$INDEX" | awk '!seen[$0]++')"
echo "vt-scan: ${#FILES[@]} file(s), $(printf '%s\n' "$UNIQUE" | wc -l | tr -d ' ') unique by sha256"

# vt_file SHA — GET /files/{sha256}; a 404 NotFoundError is an answer
# (HTTP_STATUS=404), not an error.
vt_file() {
  ALLOW_404=1
  vt_request GET "$API_BASE/files/$1"
  ALLOW_404=0
}

# file_facts SHA PATH — from the file object in $BODY, after checking it names
# SHA, prints "<last_analysis_date or 0><TAB><engines listed>".
file_facts() {
  local id
  id="$(jq -r '.data.id // empty' "$BODY")"
  [ "$id" = "$1" ] || harness "file object describes ${id:-no file}, not $2 ($1)"
  jq -r '.data.attributes as $a
    | [(if ($a.last_analysis_date | type) == "number" then $a.last_analysis_date | floor else 0 end),
       (if ($a.last_analysis_results | type) == "object" then $a.last_analysis_results | length else 0 end)]
    | @tsv' "$BODY"
}

utc() { date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }

expect_analysis() {
  local aid
  aid="$(jq -r 'if .data.type == "analysis" and (.data.id | type) == "string" then .data.id else empty end' "$BODY")"
  [[ "$aid" =~ ^[A-Za-z0-9=_:+/-]+$ ]] || harness "$1 returned no analysis id"
}

# upload PATH SIZE — POST the bytes; over 32 MB through upload_url.
upload() {
  local path="$1" size="$2" target="$API_BASE/files"
  if [ "$size" -gt "$LARGE_FILE_BYTES" ]; then
    vt_request GET "$API_BASE/files/upload_url"
    target="$(jq -r 'if (.data | type) == "string" then .data else empty end' "$BODY")"
    # The key goes to this URL, so it must be the API's own host or
    # VirusTotal's large-file upload host. The trailing / refuses lookalikes.
    case "$target" in
    "$API_ORIGIN"/* | "$VT_UPLOAD_ORIGIN"/*) ;;
    *) harness "upload_url for $path is not on $API_ORIGIN or $VT_UPLOAD_ORIGIN" ;;
    esac
  fi
  vt_request POST "$target" -F "file=@\"$path\""
  expect_analysis "POST /files for $path"
}

ALLOW_JSON="$(printf '%s\n' "${YARA_ALLOWLIST[@]}" | jq -R . | jq -sc .)"
FINDINGS=0

# judge SHA SOURCE — judge the file object in $BODY: its last analysis's
# engines, its crowdsourced YARA matches and its Code insights. Every source
# (reused, reanalysed, uploaded) is judged by the same rules.
judge() {
  local sha="$1" source="$2" path paths_json size verdict entry n allowed facts adate engines yara_note
  path="$(awk -F'\t' -v s="$sha" '$1 == s { print $3; exit }' "$INDEX")"
  paths_json="$(awk -F'\t' -v s="$sha" '$1 == s { print $3 }' "$INDEX" | jq -R . | jq -sc .)"
  size="$(awk -F'\t' -v s="$sha" '$1 == s { print $2; exit }' "$INDEX")"
  facts="$(file_facts "$sha" "$path")" || exit 2
  adate="${facts%%$'\t'*}"
  engines="${facts##*$'\t'}"
  [ "$engines" -ge "$MIN_ENGINES" ] ||
    harness "report for $path ($sha) lists $engines engine(s), fewer than VT_MIN_ENGINES=$MIN_ENGINES: it is incomplete"
  verdict="$(jq -ce '
    .data.attributes as $a
    | if ($a.last_analysis_stats.malicious | type) == "number"
         and ($a.last_analysis_stats.suspicious | type) == "number"
         and ($a.last_analysis_results | type) == "object"
      then . else error("analysis") end
    | ($a.crowdsourced_yara_results // []) as $y
    | if ($y | type) != "array" or ($y | any(.[]; (.rule_name | type) != "string"))
      then error("yara") else . end
    | ($a.crowdsourced_ai_results) as $ai
    | (if $ai == null or $ai == [] then "not-produced"
       elif ($ai | type) == "array" and ($ai | all(.[]; (.verdict | type) == "string"))
       then [$ai[] | {source: (.source // "Code Insight"), verdict: .verdict}]
       else error("ai") end) as $ci
    | {malicious: $a.last_analysis_stats.malicious, suspicious: $a.last_analysis_stats.suspicious,
       engines: [$a.last_analysis_results | to_entries[]
                 | select(.value.category == "malicious" or .value.category == "suspicious")
                 | {engine: (.value.engine_name // .key), category: .value.category,
                    result: .value.result}],
       yara: [$y[] | {rule_name, ruleset_name: (.ruleset_name // null), source: (.source // null)}],
       code_insights: $ci}' "$BODY" 2>/dev/null)" ||
    harness "file object for $path ($sha) has its analysis, YARA or Code insights results in an unknown shape"

  entry="$(jq -cn --argjson allow "$ALLOW_JSON" \
    --arg file "$path" --arg sha "$sha" --argjson size "$size" --argjson files "$paths_json" \
    --arg source "$source" --arg at "$(utc "$adate")" --argjson total "$engines" \
    --argjson v "$verdict" '
    {file: $file, files: $files, sha256: $sha, size: $size,
     source: $source, analysed_at: $at, engines_total: $total,
     malicious: $v.malicious, suspicious: $v.suspicious, engines: $v.engines,
     yara: [$v.yara[] | select(("\(.rule_name)|\(.source)" | IN($allow[])) | not)],
     yara_allowlisted: [$v.yara[] | select("\(.rule_name)|\(.source)" | IN($allow[]))],
     code_insights: $v.code_insights}
    | .findings = (
        [.engines[] | "engine \(.engine): \(.category) \(.result // "")"]
        + (if (.malicious + .suspicious) > 0 and (.engines | length) == 0
           then ["engines: \(.malicious) malicious, \(.suspicious) suspicious (unnamed)"] else [] end)
        + [.yara[] | "YARA rule \(.rule_name) (\(.ruleset_name // "?"))"]
        + (if (.code_insights | type) == "array"
           then [.code_insights[] | select(.verdict != "benign") | "Code insights (\(.source)): \(.verdict)"]
           else [] end))')"
  ENTRIES="$(jq -c --argjson e "$entry" '. + [$e]' <<<"$ENTRIES")"

  n="$(jq -r '.findings | length' <<<"$entry")"
  allowed="$(jq -r '[.yara_allowlisted[] | "\(.rule_name) (\(.source))"] | join(", ")' <<<"$entry")"
  if [ "$n" -gt 0 ]; then
    FINDINGS=$((FINDINGS + n))
    jq -r --arg f "$path" --arg s "$sha" '.findings[] | "::error::\($f) (\($s)): \(.)"' <<<"$entry"
    [ -z "$allowed" ] || echo "  $path ($sha): YARA allowlisted: $allowed"
    echo "  $path ($sha): $source report, analysed $(utc "$adate") by $engines engines"
  else
    yara_note="no YARA"
    [ -z "$allowed" ] || yara_note="YARA allowlisted: $allowed"
    echo "  clean $path ($sha): 0 malicious, 0 suspicious, $yara_note, Code insights $(jq -r 'if (.code_insights | type) == "string" then .code_insights else "benign" end' <<<"$entry"); $source report, analysed $(utc "$adate") by $engines engines"
  fi
}

# --- 1. Look up every unique file; judge a fresh report, submit the rest -----
# Submissions are made in this pass, so analyses run while later files are
# looked up. Lines of "sha256<TAB>last_analysis_date before<TAB>source".
SUBMITTED="$WORK/submitted.tsv"
: >"$SUBMITTED"
for sha in $UNIQUE; do
  line="$(awk -F'\t' -v s="$sha" '$1 == s { print; exit }' "$INDEX")"
  size="$(cut -f2 <<<"$line")"
  path="$(cut -f3- <<<"$line")"
  vt_file "$sha"
  if [ "$HTTP_STATUS" = "404" ]; then
    upload "$path" "$size"
    printf '%s\t0\tuploaded\n' "$sha" >>"$SUBMITTED"
    echo "  uploaded $path ($sha): VirusTotal did not know it"
    continue
  fi
  facts="$(file_facts "$sha" "$path")" || exit 2
  adate="${facts%%$'\t'*}"
  engines="${facts##*$'\t'}"
  age=$(($(date +%s) - adate))
  if [ "$REUSE_MAX_AGE" -gt 0 ] && [ "$adate" -gt 0 ] && [ "$age" -le "$REUSE_MAX_AGE" ] &&
    [ "$engines" -ge "$MIN_ENGINES" ]; then
    judge "$sha" reused
    continue
  fi
  vt_request POST "$API_BASE/files/$sha/analyse"
  expect_analysis "POST /files/$sha/analyse for $path"
  printf '%s\t%s\treanalysed\n' "$sha" "$adate" >>"$SUBMITTED"
  if [ "$adate" -gt 0 ]; then
    echo "  reanalysing $path ($sha): last analysed $(utc "$adate"), ${age}s ago"
  else
    echo "  reanalysing $path ($sha): VirusTotal has no complete analysis of it"
  fi
done

# --- 2. Per submitted file: poll its report until the new analysis lands -----
while IFS=$'\t' read -r sha before source; do
  path="$(awk -F'\t' -v s="$sha" '$1 == s { print $3; exit }' "$INDEX")"
  deadline=$(($(date +%s) + ANALYSIS_TIMEOUT))
  gap="$MIN_INTERVAL"
  while :; do
    # Exponential backoff: MIN, 2*MIN, 4*MIN ... capped at POLL_MAX_INTERVAL.
    NEXT_GAP="$gap"
    echo "  polling analysis of $path after ${gap}s"
    vt_file "$sha"
    gap=$((gap * 2))
    [ "$gap" -le "$POLL_MAX_INTERVAL" ] || gap="$POLL_MAX_INTERVAL"
    if [ "$HTTP_STATUS" = "200" ]; then
      facts="$(file_facts "$sha" "$path")" || exit 2
      [ "${facts%%$'\t'*}" -gt "$before" ] && break
    fi
    [ "$(date +%s)" -lt "$deadline" ] ||
      harness "analysis of $path ($sha) did not complete within ${ANALYSIS_TIMEOUT}s"
  done
  judge "$sha" "$source"
done <"$SUBMITTED"

if [ "$FINDINGS" -gt 0 ]; then
  REPORT_STATUS="findings"
  write_report
  echo "vt-scan: $FINDINGS finding(s). Nothing may publish until the owner decides." >&2
  exit 1
fi
REPORT_STATUS="clean"
write_report
echo "vt-scan: every file clean"
exit 0
