#!/usr/bin/env bash
# Staging platform smoke — calls every platform-backed surface against a real
# deployment with a real signed-in credential and asserts on status codes
# plus the Health response's live PipelineHealthScore keys. A 200 whose body
# is the wrong contract still blanks the VSCode Health tab.
#
# Why this exists (nightgauge/nightgauge#754, part of epic #741): every other
# test tier for the platform integration runs against a stub, and stubs only
# encode what we BELIEVE the API does. The epic's root cause was exactly that
# belief being wrong: user-scoped routes (/v1/analytics/health, /trends,
# /cost, /v1/audit/reports) were called with an account-scoped license key and
# answered 401, while the extension told the user to upgrade their plan. A 401
# or 403 from a signed-in credential on ANY of these routes is that same class
# of defect and must fail the run loudly — never a quiet per-endpoint skip.
#
# Credential precedence exercised: the Go daemon's platform.setSessionToken
# IPC method (#742) makes a signed-in session JWT win over
# NIGHTGAUGE_API_KEY and the license key (see internal/platform/client.go
# bearer()). This script sends that JWT directly as the bearer, the same
# credential shape the daemon forwards.
#
# Required environment:
#   STAGING_PLATFORM_BASE_URL  Base URL of the staging platform API
#                               (e.g. https://staging-api.nightgauge.dev).
#   STAGING_SESSION_TOKEN      A signed-in session JWT for a DEDICATED staging
#                               test account. Never a personal credential.
#
# A missing value for either fails the job immediately (exit 1) — it does not
# skip. A silently skipped canary reads as green, which is worse than none
# (see #732, #744: tests that "passed" by never running).
#
# Optional environment:
#   STAGING_SMOKE_MACHINE_ID   machine_id used for agent registration.
#                               Default: ci-staging-smoke. Registration upserts
#                               by machine_id, so reusing the same id across
#                               runs is idempotent (re-register = revival),
#                               not a growing pile of agent rows.
#
# The dedicated staging account MUST be on a plan tier that has access to
# EVERY surface under test. Two routes — GET /v1/audit/retention and
# POST /v1/audit/integrity — are intentionally enterprise-plan-gated on
# the platform (see internal/platform/audit_retention.go: both return 403 with
# "enterprise only" for a non-enterprise account, by product design, not by
# bug). This script does not special-case that 403 away, on purpose: the whole
# point of this canary is that a 401/403 is never something to route around
# silently. If the staging account is not on an enterprise plan, provision one
# that is, or those two rows will legitimately and correctly fail every run.
#
# Exit code: 0 only if every endpoint returned a 2xx. Non-zero otherwise.
set -uo pipefail

BASE_URL="${STAGING_PLATFORM_BASE_URL:-}"
TOKEN="${STAGING_SESSION_TOKEN:-}"
MACHINE_ID="${STAGING_SMOKE_MACHINE_ID:-ci-staging-smoke}"

fail_missing() {
  echo "::error::$1 is not set. This job MUST fail rather than skip when a required credential is missing." >&2
  exit 1
}

[ -n "$BASE_URL" ] || fail_missing "STAGING_PLATFORM_BASE_URL"
[ -n "$TOKEN" ] || fail_missing "STAGING_SESSION_TOKEN"

# Mask immediately, before the token is used anywhere, so GitHub Actions
# redacts every subsequent occurrence of it in the log stream. This is
# defense in depth on top of the Actions runner's automatic secret masking
# (any string sourced from `secrets.*` in the workflow YAML is masked
# already): the script is also written so the token is never echoed,
# printed, or written into the summary by any other path (see the `call`
# function — only the URL, method, label and status code are ever recorded).
#
# Guarded to GITHUB_ACTIONS=true (set automatically by the Actions runner,
# never set for a local/test invocation): the `::add-mask::` line is a
# workflow command the real runner intercepts and never renders as log text,
# but outside the runner it is just `echo`, and printing it unconditionally
# would put the raw token on stdout on every local run and in this script's
# own test suite — the opposite of the goal.
if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
  echo "::add-mask::${TOKEN}"
fi

command -v curl >/dev/null 2>&1 || { echo "::error::curl is required" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "::error::jq is required" >&2; exit 1; }

WORKDIR="$(mktemp -d)"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

RESULTS_FILE="$WORKDIR/results.tsv"
: > "$RESULTS_FILE"

OVERALL_STATUS=0
LAST_STATUS=""
LAST_BODY_FILE=""
CALL_INDEX=0

# call METHOD PATH LABEL [JSON_BODY]
# Populates LAST_STATUS and LAST_BODY_FILE for callers that need the response
# (agent registration -> agent id). Never prints $TOKEN.
call() {
  local method="$1" path="$2" label="$3" data="${4:-}"
  local url="${BASE_URL%/}${path}"
  CALL_INDEX=$((CALL_INDEX + 1))
  local body_file="$WORKDIR/body_${CALL_INDEX}.json"
  local status

  local -a curl_args=(
    -sS -o "$body_file" -w '%{http_code}'
    -X "$method"
    -H "Authorization: Bearer ${TOKEN}"
    -H "Accept: application/json"
    --max-time 20
    --retry 0
  )
  if [ -n "$data" ]; then
    curl_args+=(-H "Content-Type: application/json" -d "$data")
  fi
  curl_args+=("$url")

  if ! status="$(curl "${curl_args[@]}" 2>"$WORKDIR/curl_err_${CALL_INDEX}")"; then
    status="000"
  fi

  local result
  case "$status" in
    2??)
      result="PASS"
      ;;
    401 | 403)
      result="FAIL (auth)"
      OVERALL_STATUS=1
      echo "::error::AUTH FAILURE — ${label} (${method} ${path}) returned ${status}. A signed-in credential must not be rejected on a platform-backed surface. See internal/platform/client.go bearer() and epic nightgauge/nightgauge#741."
      ;;
    000)
      result="FAIL (no response)"
      OVERALL_STATUS=1
      echo "::error::${label} (${method} ${path}) — request failed (curl could not complete it; see job log)."
      ;;
    *)
      result="FAIL"
      OVERALL_STATUS=1
      echo "::warning::${label} (${method} ${path}) returned ${status}"
      ;;
  esac

  printf '%s\t%s %s\t%s\t%s\n' "$label" "$method" "$path" "$status" "$result" >> "$RESULTS_FILE"

  LAST_STATUS="$status"
  LAST_BODY_FILE="$body_file"
}

# assert_object_keys LABEL KEY [KEY...]
# After a 2xx, require the JSON object to carry these top-level keys. A 200
# whose body is the wrong contract is the blank-dashboard-tab class of bug:
# status-only probes stay green while the Health tab never renders scores.
#
# Assert the keys the CLIENT DECODES, sourced from the platform's route rather
# than from its published OpenAPI document. The two can disagree — for
# /v1/analytics/runs they do — and a canary written from the document would
# then confirm the spec while production stays broken (#801).
assert_object_keys() {
  local label="$1"
  shift
  case "$LAST_STATUS" in
    2??) ;;
    *) return 0 ;;
  esac
  if ! jq -e 'type == "object"' "$LAST_BODY_FILE" >/dev/null 2>&1; then
    OVERALL_STATUS=1
    echo "::error::${label} returned ${LAST_STATUS} but the body is not a JSON object. The dashboard maps this payload; a 200 with the wrong shape is a blank tab."
    printf '%s\t%s\t%s\t%s\n' "${label} body shape" "-" "$LAST_STATUS" "FAIL (shape)" >> "$RESULTS_FILE"
    return 0
  fi
  local missing=()
  local key
  for key in "$@"; do
    if ! jq -e --arg k "$key" 'has($k)' "$LAST_BODY_FILE" >/dev/null 2>&1; then
      missing+=("$key")
    fi
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    OVERALL_STATUS=1
    echo "::error::${label} returned ${LAST_STATUS} but is missing required keys: ${missing[*]}. Status-only probes miss this; the VSCode Health tab maps compositeScore from GET /v1/analytics/health."
    printf '%s\t%s\t%s\t%s\n' "${label} body shape" "-" "$LAST_STATUS" "FAIL (shape)" >> "$RESULTS_FILE"
  fi
}

record_skipped() {
  local label="$1" endpoint="$2" reason="$3"
  printf '%s\t%s\t-\tFAIL (%s)\n' "$label" "$endpoint" "$reason" >> "$RESULTS_FILE"
  OVERALL_STATUS=1
  echo "::error::${label} (${endpoint}) — ${reason}"
}

echo "Staging platform smoke — target: ${BASE_URL}"
echo ""

# --- Agent registration + heartbeat -----------------------------------------
call POST "/v1/agents/register" "Agent registration" \
  "$(jq -nc --arg mid "$MACHINE_ID" '{machine_id: $mid, capabilities: ["attention_resolve"]}')"

AGENT_ID=""
if [ "$LAST_STATUS" = "200" ] || [ "$LAST_STATUS" = "201" ]; then
  AGENT_ID="$(jq -r '.agentId // empty' "$LAST_BODY_FILE" 2>/dev/null || true)"
fi

if [ -n "$AGENT_ID" ]; then
  ENCODED_AGENT_ID="$(jq -nr --arg id "$AGENT_ID" '$id|@uri')"
  call PUT "/v1/agents/${ENCODED_AGENT_ID}/heartbeat" "Agent heartbeat"
else
  record_skipped "Agent heartbeat" "PUT /v1/agents/:id/heartbeat" "no agent id returned by registration"
fi

# --- Analytics ---------------------------------------------------------------
call GET "/v1/analytics/dashboard?range=7d" "Analytics dashboard / usage summary"
call GET "/v1/analytics/health" "Analytics health"
assert_object_keys "Analytics health" compositeScore compositeGrade computedAt periodDays totalRunsAnalyzed
# Runs and trends are called the way the extension calls them, and their
# bodies are shape-checked. Both were previously probed for status only, and
# both returned 200 with a body no client key matched — the Runs tab and the
# Trends tab rendered nothing while this canary stayed green (#801).
#
# The keys asserted below are the ones the Go client decodes. They are taken
# from the platform's ROUTE, not from its published OpenAPI document: the
# document declares this operation's body as {items, has_more, next_cursor}
# and the route returns {runs, nextCursor}. Asserting the documented shape
# here would have made the canary agree with the spec and disagree with
# production — exactly the failure the canary exists to catch.
call GET "/v1/analytics/runs?limit=1" "Analytics runs"
assert_object_keys "Analytics runs" runs nextCursor

# `period` is not a parameter of /trends and never was; the route's query
# schema is .passthrough(), so sending it produced a 200 built from the
# server's default window instead of a 422. The canary now sends the documented
# parameters, one call per metric, since the endpoint answers exactly one.
TRENDS_FROM="$(date -u -v-30d '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d '30 days ago' '+%Y-%m-%dT%H:%M:%SZ')"
TRENDS_TO="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
call GET "/v1/analytics/trends?metric=tokens&granularity=daily&dateFrom=${TRENDS_FROM}&dateTo=${TRENDS_TO}" \
  "Analytics trends (tokens, as the Trends tab sends)"
assert_object_keys "Analytics trends (tokens)" granularity dateFrom dateTo repos data
call GET "/v1/analytics/trends?metric=success_rate&granularity=daily&dateFrom=${TRENDS_FROM}&dateTo=${TRENDS_TO}" \
  "Analytics trends (success_rate, as the Trends tab sends)"
assert_object_keys "Analytics trends (success_rate)" granularity dateFrom dateTo repos targetSuccessRate data

# Call /cost the way the VSCode Cost tab actually calls it — with the window
# bounds the extension sends, not bare. A bare call was the whole coverage gap
# behind #800: startDate/endDate are declared `format: date-time`, the extension
# sent bare calendar dates, and the server answered 422 on every request while
# this canary stayed green because it never sent the parameters at all. An
# endpoint that is only ever exercised without its query string is not covered.
COST_START="$(date -u -v-30d '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d '30 days ago' '+%Y-%m-%dT%H:%M:%SZ')"
COST_END="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
call GET "/v1/analytics/cost" "Analytics cost (no window)"
call GET "/v1/analytics/cost?startDate=${COST_START}&endDate=${COST_END}" \
  "Analytics cost (windowed, as the Cost tab sends)"

# --- Audit --------------------------------------------------------------------
# The Compliance tab's list. `limit` was never a parameter of this endpoint —
# it takes none and returns the account's newest 50 rows — and the probe was
# status-only, so it stayed green while the tab decoded {reports, nextCursor,
# hasMore} against a body that carries only `items` and rendered "no reports"
# for every account (#803). The key asserted is the one the Go client decodes,
# taken from the route.
call GET "/v1/audit/reports" "Audit reports"
assert_object_keys "Audit reports" items
call GET "/v1/audit/retention" "Audit log retention config"
# The Retention & Integrity panel's verify buttons. Until #822 this probe sent
# {windowDays: 30} to /v1/audit/integrity/verify — a path the platform has never
# mounted, with a body its schema has never accepted. Neither could ever have
# returned a useful 2xx; it went unnoticed because this workflow has never run
# against a real staging deployment. Send what the client sends: the mounted
# path, and the RFC 3339 bounds VerifyIntegritySchema requires.
INTEGRITY_START="$(date -u -v-30d '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d '30 days ago' '+%Y-%m-%dT%H:%M:%SZ')"
INTEGRITY_END="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
call POST "/v1/audit/integrity" "Audit integrity verify" \
  "$(jq -nc --arg s "$INTEGRITY_START" --arg e "$INTEGRITY_END" \
    '{startDate: $s, endDate: $e}')"
# The keys internal/platform.IntegrityResult decodes, taken from the route.
# A status-only probe cannot see the response-shape half of #822: the client
# decoded windowDays/message/checkedAt, which this endpoint has never sent.
assert_object_keys "Audit integrity verify" valid checkedCount brokenLinks

# --- Attention Center sync ----------------------------------------------------
# agent_id is omitted (not sent as an empty string) when registration above
# didn't yield one — attentionSyncBody's `agent_id` is `omitempty` on the
# platform side, and an empty string is not a valid id.
call PUT "/v1/attention/sync" "Attention sync" \
  "$(jq -nc --arg agent "$AGENT_ID" --arg mid "$MACHINE_ID" \
    '{machine_id: $mid, requests: []} + (if $agent != "" then {agent_id: $agent} else {} end)')"

# --- Report --------------------------------------------------------------------
echo ""
echo "=== Results ==="
{
  printf '%-42s %-46s %-6s %s\n' "ENDPOINT" "METHOD PATH" "HTTP" "RESULT"
  while IFS=$'\t' read -r label endpoint status result; do
    printf '%-42s %-46s %-6s %s\n' "$label" "$endpoint" "$status" "$result"
  done < "$RESULTS_FILE"
} | tee "$WORKDIR/table.txt"

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### Staging platform smoke"
    echo ""
    echo "Target: \`${BASE_URL}\`"
    echo ""
    echo "| Endpoint | Method + path | HTTP | Result |"
    echo "| --- | --- | --- | --- |"
    while IFS=$'\t' read -r label endpoint status result; do
      echo "| ${label} | \`${endpoint}\` | ${status} | ${result} |"
    done < "$RESULTS_FILE"
    echo ""
    if [ "$OVERALL_STATUS" -eq 0 ]; then
      echo "All surfaces returned 2xx."
    else
      echo "**One or more surfaces failed.** A 401/403 here means a signed-in credential was rejected — see epic nightgauge/nightgauge#741."
    fi
  } >> "$GITHUB_STEP_SUMMARY"
fi

exit "$OVERALL_STATUS"
