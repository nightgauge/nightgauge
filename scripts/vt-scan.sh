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
# Before anything else, one request reads the key's daily quota (THE DAILY
# QUOTA, below). Every unique file (by sha256) is then looked up: GET
# /files/{sha256}.
#
#   - VirusTotal analysed it within VT_REUSE_MAX_AGE (default 24 hours), and
#     that analysis lists at least VT_MIN_ENGINES engines: its report is judged
#     exactly as a new one would be. 1 request. A release ships the hooks,
#     skill scripts and bundles its RC carried byte for byte, and the RC scan
#     analysed them hours earlier: re-analysing them spent two thirds of every
#     release scan (session 86).
#   - It is a text file, VirusTotal analysed it within VT_REUSE_MAX_AGE_TEXT
#     (default 14 days), that analysis lists at least VT_MIN_ENGINES engines,
#     and it carries no finding by the rules below: its report is judged. 1
#     request. rc.3 spent 63 of its 108 requests re-analysing 21 unchanged,
#     clean hooks and scripts last analysed 37 hours earlier (#3202). A file is
#     text unless its bytes say otherwise: an ELF, Mach-O, PE, zip or gzip
#     header, or a NUL byte in its first 8 KiB, makes it a binary, and so does
#     a .vsix, .zip, .tar.gz, .tgz or .exe name. Binaries, VSIXes and archives
#     keep the 24-hour window: a classifier verdict on them (Microsoft's !ml)
#     moves without a byte changing on our side. A report with a finding is
#     never reused under the long window; it is re-analysed.
#   - VirusTotal knows it, but neither window reuses it: POST
#     /files/{sha256}/analyse re-analyses it without a re-upload. 3 requests:
#     lookup, reanalysis, and at least one poll.
#   - VirusTotal does not know it (404): it is uploaded. 3 requests, or 4 over
#     32 MB.
#
# A submitted file is polled with GET /files/{sha256} until its
# last_analysis_date is later than the one the lookup saw. That one answer
# carries the engine results, the crowdsourced YARA matches and Code insights.
# VirusTotal writes the file's last_analysis_* fields when the analysis
# completes, and a report listing fewer than VT_MIN_ENGINES engines is refused
# as incomplete (exit 2).
#
# A report counts as reanalysed only when its last_analysis_date is later than
# the lookup's AND within the window that applies to the file: 24 hours for a
# binary, VSIX or archive, or for any report with a finding; the long text
# window only for a clean text file. The lookup's own report is never judged as
# the reanalysis, whatever its age. A file poll that shows no newer date is
# followed by GET /analyses/{id}. When that answers `completed` with a date no
# later than the lookup's report (or no date), VirusTotal finished without a
# newer report: on 2026-11-11 such a poll ran to VT_ANALYSIS_TIMEOUT (#3202).
# The scan now stops at once with exit 2, so the step is re-run. A completed
# analysis with a newer date means the file object lags behind it, so the file
# is polled until its date moves or the timeout ends; the old report is never
# judged. `queued` and `in-progress` keep polling; any other status is exit 2.
#
# GET /analyses/{id} can answer 404 for an analysis VirusTotal accepted: on
# rc.11 the upload was accepted, its analysis object 404'd, and the file's
# report later showed a newer date. A 404 there is not fatal. From then on only
# the file object is polled, under the same backoff, until its report is newer
# than the lookup's and inside its window, and that report is judged by the
# same rules as any other. Only VT_ANALYSIS_TIMEOUT ends that poll (exit 2).
# Nothing is accepted on this path that the file's own report does not show.
#
# Every run ends with one line counting its requests by class: the quota read,
# lookups (the first GET of each file), uploads (upload_url and POST /files),
# reanalyses (POST .../analyse) and polls (every later GET). The report carries
# the same counts as requests_by_class.
#
# THE DAILY QUOTA
#
# The first request reads the key's quota: GET /users/{key}/overall_quotas,
# whose data.api_requests_daily.user holds `used` and `allowed`
# (https://docs.virustotal.com/reference/user-overall-quotas). The key is part
# of that URL, so the URL travels in the curl config on stdin like the key's
# header, and every message names it as /users/<key>/overall_quotas.
#
# The scan's cost is estimated before any lookup, without knowing which files
# VirusTotal already has: per unique file one lookup, one upload (two over 32
# MB) and one poll, plus the quota read itself, capped at VT_MAX_REQUESTS
# because the run never sends more. When fewer requests remain today than that
# estimate, the scan stops with exit 2 naming both numbers, before it spends
# anything else: on the free key's 500/day a second scan otherwise ran out
# partway (#3213). An answer that cannot be read (an HTTP error other than 429,
# a body that is not JSON, no numeric used/allowed) is logged and the scan
# continues. A 429 on it is exit 2: the quota is spent. The report records
# quota_remaining (null when unreadable) and quota_estimate.
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
#                          0 re-analyses every file, whatever its kind.
#   VT_REUSE_MAX_AGE_TEXT  The same for a text file whose last analysis carries
#                          no finding. Default 1209600 (14 days). 0 turns the
#                          long window off; otherwise at least VT_REUSE_MAX_AGE.
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
REUSE_MAX_AGE_TEXT="${VT_REUSE_MAX_AGE_TEXT:-1209600}"
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
# The same requests by class. Each caller names the class of its next request
# in REQ_CLASS; vt_request counts it and clears it, so the four always sum to
# REQUESTS.
REQ_LOOKUP=0
REQ_UPLOAD=0
REQ_REANALYSIS=0
REQ_POLL=0
REQ_QUOTA=0
REQ_CLASS=""
QUOTA_REMAINING="" # requests left today, as VirusTotal reported; empty when unread
QUOTA_ESTIMATE=""  # requests this run may send, quota read included

print_summary() {
  echo "vt-scan: $REQUESTS requests (quota $REQ_QUOTA, lookups $REQ_LOOKUP, uploads $REQ_UPLOAD, reanalyses $REQ_REANALYSIS, polls $REQ_POLL)"
}

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
    --argjson reuse_text "$REUSE_MAX_AGE_TEXT" \
    --argjson lookup "$REQ_LOOKUP" --argjson upload "$REQ_UPLOAD" \
    --argjson reanalysis "$REQ_REANALYSIS" --argjson poll "$REQ_POLL" \
    --argjson quota "$REQ_QUOTA" \
    --argjson remaining "${QUOTA_REMAINING:-null}" --argjson estimate "${QUOTA_ESTIMATE:-null}" \
    '{tool: "vt-scan.sh", api: $api, scanned_at: $at, status: $status,
      error: (if $error == "" then null else $error end), requests: $requests,
      requests_by_class: {quota: $quota, lookup: $lookup, upload: $upload, reanalysis: $reanalysis, poll: $poll},
      quota_remaining: $remaining, quota_estimate: $estimate,
      reuse_max_age: $reuse, reuse_max_age_text: $reuse_text,
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
  # stderr: harness can run inside a command substitution, whose stdout is
  # captured, and the summary must still reach the log.
  print_summary >&2
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
[[ "$REUSE_MAX_AGE_TEXT" =~ ^[0-9]{1,8}$ ]] || harness "VT_REUSE_MAX_AGE_TEXT must be a whole number of seconds"
[ "$REUSE_MAX_AGE_TEXT" -eq 0 ] || [ "$REUSE_MAX_AGE_TEXT" -ge "$REUSE_MAX_AGE" ] ||
  harness "VT_REUSE_MAX_AGE_TEXT ($REUSE_MAX_AGE_TEXT) is below VT_REUSE_MAX_AGE ($REUSE_MAX_AGE); set it to 0 to turn the text window off"
[[ "$MIN_ENGINES" =~ ^[0-9]{1,3}$ ]] && [ "$MIN_ENGINES" -gt 0 ] ||
  harness "VT_MIN_ENGINES must be a whole number above 0"
# A cap below the floor would poll faster than the 4/min spacing allows.
[ "$POLL_MAX_INTERVAL" -ge "$MIN_INTERVAL" ] ||
  harness "VT_POLL_MAX_INTERVAL ($POLL_MAX_INTERVAL) is below VT_MIN_INTERVAL ($MIN_INTERVAL)"
[[ "$API_BASE" =~ ^https?://[^/]+(/.*)?$ ]] || harness "API base is not an http(s) URL"
# The quota URL is written into a curl config: a quote, backslash or space in
# the base would change that config.
case "$API_BASE" in *\"* | *\\* | *[[:space:]]*) harness "API base holds a quote, backslash or space" ;; esac
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
# NotFoundError while ALLOW_404=1 (a file VirusTotal does not know), and any
# answer but a 429 while SOFT_FAIL=1, which returns 1 with the reason in
# SOFT_WHY (only the quota read sets it). URL_IN_CONFIG=1 sends the URL in the
# curl config on stdin rather than on the command line, for a URL that holds
# the key. The message names the endpoint, never the key.
ALLOW_404=0
SOFT_FAIL=0
SOFT_WHY=""
URL_IN_CONFIG=0
HTTP_STATUS=""
vt_request() {
  local method="$1" url="$2" now wait code where config
  shift 2
  case "$url" in
  "$API_ORIGIN"/*) where="${url#"$API_ORIGIN"}" ;;
  # An upload URL carries a one-time token in its path: report only its origin.
  *) where="$(sed -E 's#^(https?://[^/]+).*#\1#' <<<"$url")" ;;
  esac
  where="${where%%\?*}"
  where="${where//"$VT_API_KEY"/<key>}"
  [ "$REQUESTS" -lt "$MAX_REQUESTS" ] ||
    harness "request budget spent: VT_MAX_REQUESTS=$MAX_REQUESTS reached before $method $where; re-run later or raise it"
  case "$REQ_CLASS" in
  quota) REQ_QUOTA=$((REQ_QUOTA + 1)) ;;
  lookup) REQ_LOOKUP=$((REQ_LOOKUP + 1)) ;;
  upload) REQ_UPLOAD=$((REQ_UPLOAD + 1)) ;;
  reanalysis) REQ_REANALYSIS=$((REQ_REANALYSIS + 1)) ;;
  poll) REQ_POLL=$((REQ_POLL + 1)) ;;
  *) harness "internal: $method $where has no request class" ;;
  esac
  REQ_CLASS=""
  now="$(date +%s)"
  wait=$((LAST_REQUEST + NEXT_GAP - now))
  [ "$wait" -gt 0 ] && sleep "$wait"
  NEXT_GAP="$MIN_INTERVAL"
  REQUESTS=$((REQUESTS + 1))
  LAST_REQUEST="$(date +%s)"
  : >"$BODY"
  config="$(printf 'header = "x-apikey: %s"' "$VT_API_KEY")"
  if [ "$URL_IN_CONFIG" = 1 ]; then
    config="$config"$'\n'"$(printf 'url = "%s"' "$url")"
  else
    set -- "$@" "$url"
  fi
  code="$(printf '%s\n' "$config" |
    curl -K - -sS -X "$method" --max-time "$HTTP_TIMEOUT" \
      -o "$BODY" -w '%{http_code}' "$@" 2>"$WORK/curl.err")"
  local rc=$?
  if [ "$rc" -ne 0 ]; then
    local err
    err="$(head -c 200 "$WORK/curl.err" | tr '\n' ' ')"
    harness "$method $where: curl exit $rc (${err//"$VT_API_KEY"/<key>})"
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
    if [ "$SOFT_FAIL" = 1 ]; then
      SOFT_WHY="$method $where: HTTP ${code:-none} ${why:-}"
      return 1
    fi
    harness "$method $where: HTTP ${code:-none} ${why:-}"
  fi
  if ! jq -e . "$BODY" >/dev/null 2>&1; then
    if [ "$SOFT_FAIL" = 1 ]; then
      SOFT_WHY="$method $where: the answer is not JSON"
      return 1
    fi
    harness "$method $where: the answer is not JSON"
  fi
}

# kind_of PATH — "binary" or "text", from the bytes first. A file is binary
# when it starts with an ELF, Mach-O (thin or fat), PE, zip (VSIX) or gzip
# header, holds a NUL byte in its first 8 KiB, or is named as an archive or
# executable. Everything else is text. Errors lean to binary, the short window.
kind_of() {
  local head
  case "$1" in *.vsix | *.zip | *.tar.gz | *.tgz | *.exe) echo binary; return ;; esac
  head="$(LC_ALL=C head -c 8 "$1" 2>/dev/null | od -An -tx1 | tr -d ' \n')" || { echo binary; return; }
  case "$head" in
  7f454c46* | feedface* | feedfacf* | cefaedfe* | cffaedfe* | cafebabe* | 4d5a* | 504b0304* | 1f8b*)
    echo binary; return ;;
  esac
  if [ "$(LC_ALL=C head -c 8192 "$1" 2>/dev/null | tr -d '\000' | wc -c | tr -d ' ')" != \
    "$(LC_ALL=C head -c 8192 "$1" 2>/dev/null | wc -c | tr -d ' ')" ]; then
    echo binary
    return
  fi
  echo text
}

# --- Deduplicate by digest ----------------------------------------------------
# Lines of "sha256<TAB>size<TAB>path<TAB>kind"; the first path for a digest is
# scanned, every path is reported.
INDEX="$WORK/index.tsv"
: >"$INDEX"
for f in "${FILES[@]}"; do
  [ -f "$f" ] && [ -r "$f" ] || harness "not a readable file: $f"
  case "$f" in *'"'* | *$'\n'* | *$'\t'* | *';'*) harness "unsupported character in file name: $f" ;; esac
  sha="$(sha256_of "$f")"
  [[ "$sha" =~ ^[0-9a-f]{64}$ ]] || harness "cannot hash $f"
  printf '%s\t%s\t%s\t%s\n' "$sha" "$(size_of "$f")" "$f" "$(kind_of "$f")" >>"$INDEX"
done
UNIQUE="$(cut -f1 "$INDEX" | awk '!seen[$0]++')"
echo "vt-scan: ${#FILES[@]} file(s), $(printf '%s\n' "$UNIQUE" | wc -l | tr -d ' ') unique by sha256"

# --- 0. Will the scan fit in today's quota? -----------------------------------
# The estimate assumes VirusTotal knows none of the files: one lookup, one
# upload (two over 32 MB) and one poll each, plus this quota read, capped at
# VT_MAX_REQUESTS because the run never sends more.
QUOTA_ESTIMATE="$(awk -F'\t' -v big="$LARGE_FILE_BYTES" -v max="$MAX_REQUESTS" '
  !seen[$1]++ { n += ($2 > big) ? 4 : 3 }
  END { n += 1; print (n < max) ? n : max }' "$INDEX")"
REQ_CLASS=quota
SOFT_FAIL=1
URL_IN_CONFIG=1
quota_ok=1
vt_request GET "$API_BASE/users/$VT_API_KEY/overall_quotas" || quota_ok=0
SOFT_FAIL=0
URL_IN_CONFIG=0
if [ "$quota_ok" = 1 ]; then
  QUOTA_REMAINING="$(jq -r '.data.api_requests_daily.user as $u
    | if ($u.used | type) == "number" and ($u.allowed | type) == "number"
      then [($u.allowed - $u.used | floor), 0] | max else empty end' "$BODY" 2>/dev/null)"
  [ -n "$QUOTA_REMAINING" ] ||
    SOFT_WHY="GET ${API_BASE#"$API_ORIGIN"}/users/<key>/overall_quotas: no numeric data.api_requests_daily.user.used and .allowed"
fi
if [ -z "$QUOTA_REMAINING" ]; then
  echo "vt-scan: quota unreadable (${SOFT_WHY:0:200}); continuing"
elif [ "$QUOTA_REMAINING" -lt "$QUOTA_ESTIMATE" ]; then
  harness "daily quota too small: VirusTotal reports $QUOTA_REMAINING requests left today, this scan may need $QUOTA_ESTIMATE; re-run after the quota resets (00:00 UTC)"
else
  echo "vt-scan: daily quota: $QUOTA_REMAINING requests left today, this scan may need $QUOTA_ESTIMATE"
fi

# vt_file SHA CLASS — GET /files/{sha256}, counted as CLASS (lookup or poll);
# a 404 NotFoundError is an answer (HTTP_STATUS=404), not an error.
vt_file() {
  ALLOW_404=1
  REQ_CLASS="$2"
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

# expect_analysis WHAT — the analysis id in $BODY, in ANALYSIS_ID.
ANALYSIS_ID=""
expect_analysis() {
  ANALYSIS_ID="$(jq -r 'if .data.type == "analysis" and (.data.id | type) == "string" then .data.id else empty end' "$BODY")"
  [[ "$ANALYSIS_ID" =~ ^[A-Za-z0-9=_:+/-]+$ ]] || harness "$1 returned no analysis id"
}

# upload PATH SIZE — POST the bytes; over 32 MB through upload_url.
upload() {
  local path="$1" size="$2" target="$API_BASE/files"
  if [ "$size" -gt "$LARGE_FILE_BYTES" ]; then
    REQ_CLASS=upload
    vt_request GET "$API_BASE/files/upload_url"
    target="$(jq -r 'if (.data | type) == "string" then .data else empty end' "$BODY")"
    # The key goes to this URL, so it must be the API's own host or
    # VirusTotal's large-file upload host. The trailing / refuses lookalikes.
    case "$target" in
    "$API_ORIGIN"/* | "$VT_UPLOAD_ORIGIN"/*) ;;
    *) harness "upload_url for $path is not on $API_ORIGIN or $VT_UPLOAD_ORIGIN" ;;
    esac
  fi
  REQ_CLASS=upload
  vt_request POST "$target" -F "file=@\"$path\""
  expect_analysis "POST /files for $path"
}

ALLOW_JSON="$(printf '%s\n' "${YARA_ALLOWLIST[@]}" | jq -R . | jq -sc .)"
FINDINGS=0

# verdict_of SHA SOURCE — the report entry for the file object in $BODY: its
# last analysis's engines, its crowdsourced YARA matches and its Code insights,
# with .findings listing every one that counts. Prints the entry and records
# nothing, so the reuse decision can ask whether a report is clean by the same
# rules that judge it. A report in an unknown shape is exit 2.
verdict_of() {
  local sha="$1" source="$2" path paths_json size verdict facts adate engines
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

  jq -cn --argjson allow "$ALLOW_JSON" \
    --arg file "$path" --arg sha "$sha" --argjson size "$size" --argjson files "$paths_json" \
    --arg source "$source" --arg at "$(utc "$adate")" --argjson adate "$adate" --argjson total "$engines" \
    --argjson v "$verdict" '
    {file: $file, files: $files, sha256: $sha, size: $size,
     source: $source, analysed_at: $at, analysed_epoch: $adate, engines_total: $total,
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
           else [] end))'
}

# window_of SHA — the reuse window this file's kind gets, as
# "<kind><TAB><seconds>": text files the long one, binaries the short one.
window_of() {
  local kind
  kind="$(awk -F'\t' -v s="$1" '$1 == s { print $4; exit }' "$INDEX")"
  if [ "$kind" = text ] && [ "$REUSE_MAX_AGE" -gt 0 ] && [ "$REUSE_MAX_AGE_TEXT" -gt 0 ]; then
    printf 'text\t%s\n' "$REUSE_MAX_AGE_TEXT"
  else
    printf '%s\t%s\n' "${kind:-binary}" "$REUSE_MAX_AGE"
  fi
}

# reanalysis_window SHA — the oldest a newer report in $BODY may be and still
# count as reanalysed: the long text window only for a clean text file, else
# 24 hours (VT_REUSE_MAX_AGE, or 24 hours when reuse is off).
reanalysis_window() {
  local sha="$1" short="$REUSE_MAX_AGE" win entry
  [ "$short" -gt 0 ] || short=86400
  win="$(window_of "$sha")"
  if [ "${win%%$'\t'*}" = text ] && [ "${win##*$'\t'}" -gt "$short" ]; then
    entry="$(verdict_of "$sha" reanalysed)" || exit 2
    if [ "$(jq -r '.findings | length' <<<"$entry")" -eq 0 ]; then
      printf '%s\n' "${win##*$'\t'}"
      return
    fi
  fi
  printf '%s\n' "$short"
}

# judge SHA SOURCE [APPLIED [EXTRA_JSON]] — judge the file object in $BODY and
# record it. Every source (reused, reanalysed, uploaded) is judged by the same
# rules. APPLIED names the reuse window that let a reused report stand ("short"
# or "long"); EXTRA_JSON is merged into the entry.
judge() {
  local sha="$1" source="$2" applied="${3:-}" extra="${4:-"{}"}" entry n allowed path adate engines win max_age yara_note
  path="$(awk -F'\t' -v s="$sha" '$1 == s { print $3; exit }' "$INDEX")"
  entry="$(verdict_of "$sha" "$source")" || exit 2
  win="$(window_of "$sha")"
  max_age="${win##*$'\t'}"
  [ "$applied" != short ] || max_age="$REUSE_MAX_AGE"
  entry="$(jq -c --arg kind "${win%%$'\t'*}" --arg applied "$applied" --argjson max "$max_age" \
    --argjson extra "$extra" '
    . + {reuse_window: {kind: $kind, applied: (if $applied == "" then null else $applied end),
                        max_age: $max}} + $extra' <<<"$entry")"
  ENTRIES="$(jq -c --argjson e "$entry" '. + [$e | del(.analysed_epoch)]' <<<"$ENTRIES")"
  adate="$(jq -r '.analysed_epoch' <<<"$entry")"
  engines="$(jq -r '.engines_total' <<<"$entry")"

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
# looked up. Lines of
# "sha256<TAB>last_analysis_date before<TAB>source<TAB>analysis id".
SUBMITTED="$WORK/submitted.tsv"
: >"$SUBMITTED"
for sha in $UNIQUE; do
  line="$(awk -F'\t' -v s="$sha" '$1 == s { print; exit }' "$INDEX")"
  size="$(cut -f2 <<<"$line")"
  path="$(cut -f3 <<<"$line")"
  vt_file "$sha" lookup
  if [ "$HTTP_STATUS" = "404" ]; then
    upload "$path" "$size"
    printf '%s\t0\tuploaded\t%s\n' "$sha" "$ANALYSIS_ID" >>"$SUBMITTED"
    echo "  uploaded $path ($sha): VirusTotal did not know it"
    continue
  fi
  facts="$(file_facts "$sha" "$path")" || exit 2
  adate="${facts%%$'\t'*}"
  engines="${facts##*$'\t'}"
  age=$(($(date +%s) - adate))
  if [ "$REUSE_MAX_AGE" -gt 0 ] && [ "$adate" -gt 0 ] && [ "$age" -le "$REUSE_MAX_AGE" ] &&
    [ "$engines" -ge "$MIN_ENGINES" ]; then
    judge "$sha" reused short
    continue
  fi
  win="$(window_of "$sha")"
  # The long window: a text file whose report is complete and carries no
  # finding. A report with a finding is re-analysed, never reused here.
  if [ "${win%%$'\t'*}" = text ] && [ "$REUSE_MAX_AGE" -gt 0 ] && [ "$REUSE_MAX_AGE_TEXT" -gt 0 ] &&
    [ "$adate" -gt 0 ] && [ "$age" -le "${win##*$'\t'}" ] &&
    [ "$engines" -ge "$MIN_ENGINES" ]; then
    entry="$(verdict_of "$sha" reused)" || exit 2
    if [ "$(jq -r '.findings | length' <<<"$entry")" -eq 0 ]; then
      judge "$sha" reused long
      continue
    fi
    echo "  $path ($sha): its ${age}s-old report carries a finding, so it is re-analysed, not reused"
  fi
  REQ_CLASS=reanalysis
  vt_request POST "$API_BASE/files/$sha/analyse"
  expect_analysis "POST /files/$sha/analyse for $path"
  printf '%s\t%s\treanalysed\t%s\n' "$sha" "$adate" "$ANALYSIS_ID" >>"$SUBMITTED"
  if [ "$adate" -gt 0 ]; then
    echo "  reanalysing $path ($sha): last analysed $(utc "$adate"), ${age}s ago"
  else
    echo "  reanalysing $path ($sha): VirusTotal has no complete analysis of it"
  fi
done

# File reads allowed after a completed, newer analysis before the scan stops.
LAG_FILE_POLLS=3

# --- 2. Per submitted file: poll its report until the new analysis lands -----
# The first poll reads the file. If its date has not moved, the analysis object
# is polled alone until it completes (VirusTotal can complete an analysis
# without moving the file's date), then the file again: one request per poll.
while IFS=$'\t' read -r sha before source aid; do
  path="$(awk -F'\t' -v s="$sha" '$1 == s { print $3; exit }' "$INDEX")"
  deadline=$(($(date +%s) + ANALYSIS_TIMEOUT))
  gap="$MIN_INTERVAL"
  next="file"
  analysis_newer=0
  analysis_gone=0
  lag_polls=0
  while :; do
    # Exponential backoff: MIN, 2*MIN, 4*MIN ... capped at POLL_MAX_INTERVAL.
    NEXT_GAP="$gap"
    if [ "$next" = file ]; then
      echo "  polling analysis of $path after ${gap}s"
      vt_file "$sha" poll
    else
      echo "  polling analysis object of $path after ${gap}s"
      REQ_CLASS=poll
      ALLOW_404=1
      vt_request GET "$API_BASE/analyses/$(jq -rn --arg a "$aid" '$a | @uri')"
      ALLOW_404=0
    fi
    gap=$((gap * 2))
    [ "$gap" -le "$POLL_MAX_INTERVAL" ] || gap="$POLL_MAX_INTERVAL"
    if [ "$next" = file ]; then
      if [ "$HTTP_STATUS" = "200" ]; then
        facts="$(file_facts "$sha" "$path")" || exit 2
        pdate="${facts%%$'\t'*}"
        if [ "$pdate" -gt "$before" ]; then
          if [ $(($(date +%s) - pdate)) -le "$(reanalysis_window "$sha")" ]; then
            judge "$sha" "$source"
            break
          fi
          echo "  $path ($sha): its report dated $(utc "$pdate") is newer than the lookup's but outside its window; still polling"
        fi
      fi
      if [ "$analysis_gone" = 1 ]; then
        # The analysis object is gone: only the file's own report can decide.
        :
      elif [ "$analysis_newer" = 1 ]; then
        # The analysis completed with a newer date, yet the file has not moved.
        # VirusTotal dates an analysis when it is created, so a folded one looks
        # newer too: read the file a bounded number of times, then stop.
        lag_polls=$((lag_polls + 1))
        [ "$lag_polls" -lt "$LAG_FILE_POLLS" ] ||
          harness "VirusTotal completed the analysis of $path ($sha) but its report stayed at $(utc "$before") after $LAG_FILE_POLLS file reads; that report is not accepted as a reanalysis"
      else
        next="analysis"
      fi
    elif [ "$HTTP_STATUS" = "404" ]; then
      # rc.11 (#3213): the upload was accepted and its analysis object 404'd,
      # yet the file's report later showed a newer date. Poll the file alone.
      echo "  analysis object of $path is gone (404); polling the file report"
      analysis_gone=1
      next="file"
    else
      status="$(jq -r 'if .data.type == "analysis" and (.data.attributes.status | type) == "string"
        then .data.attributes.status else empty end' "$BODY")"
      case "$status" in
      completed)
        # A newer analysis means the file object lags behind it: keep polling
        # the file, never judge the old report. One no newer than the lookup's
        # report (or undated) produced no new report: stop now, never judge it.
        adate="$(jq -r 'if (.data.attributes.date | type) == "number"
          then .data.attributes.date | floor else empty end' "$BODY")"
        if [ -n "$adate" ] && [ "$adate" -gt "$before" ]; then
          analysis_newer=1
          next="file"
        else
          harness "VirusTotal completed the analysis of $path ($sha) without a report newer than the lookup's ($(utc "$before")); that report is not accepted as a reanalysis"
        fi
        ;;
      queued | in-progress) ;;
      *) harness "analysis of $path ($sha) answered status '${status:-none}', not queued, in-progress or completed" ;;
      esac
    fi
    [ "$(date +%s)" -lt "$deadline" ] ||
      harness "analysis of $path ($sha) did not complete within ${ANALYSIS_TIMEOUT}s"
  done
done <"$SUBMITTED"

if [ "$FINDINGS" -gt 0 ]; then
  REPORT_STATUS="findings"
  write_report
  print_summary
  echo "vt-scan: $FINDINGS finding(s). Nothing may publish until the owner decides." >&2
  exit 1
fi
REPORT_STATUS="clean"
write_report
print_summary
echo "vt-scan: every file clean"
exit 0
