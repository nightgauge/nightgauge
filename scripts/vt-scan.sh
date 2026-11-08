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
# The public API accepts POST /files, and GET /files/upload_url for files over
# 32 MB, at 4 requests/min and 500/day:
#   https://docs.virustotal.com/reference/public-vs-premium-api
#   https://docs.virustotal.com/reference/files-scan
#   https://docs.virustotal.com/reference/files-upload-url
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
LARGE_FILE_BYTES=33554432 # 32 MB: above this, POST /files refuses the upload.
# VirusTotal answers GET /files/upload_url with a URL on this host. It is the
# only origin besides the API's own that may receive the key.
VT_UPLOAD_ORIGIN="https://bigfiles.virustotal.com"
# Crowdsourced YARA rules that are recorded but never count as a finding, as
# "rule_name|source", matched exactly. Owner decision (#3019): InQuest's
# Windows_API_Function rule matches every Go binary (os.ReadFile, os.WriteFile)
# and describes itself as "not an indication of malicious behavior". Adding an
# entry here is a reviewed change to the release policy.
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
    '{tool: "vt-scan.sh", api: $api, scanned_at: $at, status: $status,
      error: (if $error == "" then null else $error end), requests: $requests,
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

# vt_request METHOD URL [curl args...] — body lands in $BODY. Anything but
# HTTP 200 is a harness error; the message names the endpoint, never the key.
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

# --- 1. Upload every unique file (analyses run while later files upload) ------
ANALYSES="$WORK/analyses.tsv"
: >"$ANALYSES"
for sha in $UNIQUE; do
  line="$(awk -F'\t' -v s="$sha" '$1 == s { print; exit }' "$INDEX")"
  size="$(cut -f2 <<<"$line")"
  path="$(cut -f3- <<<"$line")"
  target="$API_BASE/files"
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
  aid="$(jq -r 'if .data.type == "analysis" and (.data.id | type) == "string" then .data.id else empty end' "$BODY")"
  [[ "$aid" =~ ^[A-Za-z0-9=_:+/-]+$ ]] || harness "POST /files for $path returned no analysis id"
  printf '%s\t%s\n' "$sha" "$aid" >>"$ANALYSES"
  echo "  uploaded $path ($sha)"
done

ALLOW_JSON="$(printf '%s\n' "${YARA_ALLOWLIST[@]}" | jq -R . | jq -sc .)"

# --- 2. Per file: wait for the analysis, then read the file object -----------
FINDINGS=0
while IFS=$'\t' read -r sha aid; do
  path="$(awk -F'\t' -v s="$sha" '$1 == s { print $3; exit }' "$INDEX")"
  paths_json="$(awk -F'\t' -v s="$sha" '$1 == s { print $3 }' "$INDEX" | jq -R . | jq -sc .)"
  size="$(awk -F'\t' -v s="$sha" '$1 == s { print $2; exit }' "$INDEX")"

  deadline=$(($(date +%s) + ANALYSIS_TIMEOUT))
  gap="$MIN_INTERVAL"
  while :; do
    # Exponential backoff: MIN, 2*MIN, 4*MIN ... capped at POLL_MAX_INTERVAL.
    NEXT_GAP="$gap"
    echo "  polling analysis of $path after ${gap}s"
    vt_request GET "$API_BASE/analyses/$aid"
    gap=$((gap * 2))
    [ "$gap" -le "$POLL_MAX_INTERVAL" ] || gap="$POLL_MAX_INTERVAL"
    status="$(jq -r '.data.attributes.status // empty' "$BODY")"
    case "$status" in
    completed) break ;;
    queued | in-progress) ;;
    *) harness "analysis of $path has unknown status '${status:-missing}'" ;;
    esac
    [ "$(date +%s)" -lt "$deadline" ] ||
      harness "analysis of $path ($sha) did not complete within ${ANALYSIS_TIMEOUT}s"
  done
  reported="$(jq -r '.meta.file_info.sha256 // empty' "$BODY")"
  [ "$reported" = "$sha" ] ||
    harness "analysis $aid describes ${reported:-no file}, not $path ($sha)"
  analysis="$(jq -ce '
    .data.attributes as $a
    | if ($a.stats.malicious | type) == "number" and ($a.stats.suspicious | type) == "number"
         and ($a.results | type) == "object"
      then {malicious: $a.stats.malicious, suspicious: $a.stats.suspicious,
            engines: [$a.results | to_entries[]
                      | select(.value.category == "malicious" or .value.category == "suspicious")
                      | {engine: (.value.engine_name // .key), category: .value.category,
                         result: .value.result}]}
      else error("shape") end' "$BODY" 2>/dev/null)" ||
    harness "analysis of $path has no parseable stats and results"

  vt_request GET "$API_BASE/files/$sha"
  attrs="$(jq -ce '
    .data.attributes as $a
    | if ($a | type) != "object" then error("shape") else . end
    | ($a.crowdsourced_yara_results // []) as $y
    | if ($y | type) != "array" or ($y | any(.[]; (.rule_name | type) != "string"))
      then error("yara") else . end
    | ($a.crowdsourced_ai_results) as $ai
    | if $ai == null or $ai == [] then {code_insights: "not-produced"}
      elif ($ai | type) == "array" and ($ai | all(.[]; (.verdict | type) == "string"))
      then {code_insights: [$ai[] | {source: (.source // "Code Insight"), verdict: .verdict}]}
      else error("ai") end
    | . + {yara: [$y[] | {rule_name, ruleset_name: (.ruleset_name // null), source: (.source // null)}]}
    ' "$BODY" 2>/dev/null)" ||
    harness "file object for $path ($sha) has YARA or Code insights results in an unknown shape"

  entry="$(jq -cn --argjson allow "$ALLOW_JSON" \
    --arg file "$path" --arg sha "$sha" --argjson size "$size" --argjson files "$paths_json" \
    --argjson an "$analysis" --argjson at "$attrs" '
    {file: $file, files: $files, sha256: $sha, size: $size,
     malicious: $an.malicious, suspicious: $an.suspicious, engines: $an.engines,
     yara: [$at.yara[] | select(("\(.rule_name)|\(.source)" | IN($allow[])) | not)],
     yara_allowlisted: [$at.yara[] | select("\(.rule_name)|\(.source)" | IN($allow[]))],
     code_insights: $at.code_insights}
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
  else
    yara_note="no YARA"
    [ -z "$allowed" ] || yara_note="YARA allowlisted: $allowed"
    echo "  clean $path ($sha): 0 malicious, 0 suspicious, $yara_note, Code insights $(jq -r 'if (.code_insights | type) == "string" then .code_insights else "benign" end' <<<"$entry")"
  fi
done <"$ANALYSES"

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
