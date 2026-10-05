#!/usr/bin/env bash
# Regression tests for scripts/platform-smoke.sh.
#
# Runs the WORKING-TREE copy of the script against a small stdlib-only Python
# mock server (no live network, no real credential — see the caution
# in nightgauge/nightgauge#754: this suite proves the script's own logic, not
# that the platform itself is healthy; that only a real dispatch can prove).
#
# Covers:
#   1. All-green run: every mocked endpoint returns 2xx -> exit 0.
#   2. A 401 on one endpoint fails the whole run loudly (::error::, non-zero
#      exit, summary marks it FAIL (auth)) — the epic's core assertion.
#   3. A missing credential SKIPS the signed-in tier with a ::notice:: and a
#      summary line, and the anonymous tier still runs and decides the exit
#      code (#2425): no request carries a bearer, an anonymous route that
#      answers 2xx fails the run, and so does an API health that is down.
#   4. No credential value ever appears in the script's stdout, stderr, or
#      $GITHUB_STEP_SUMMARY output, across a normal run.
#
# Run: bash scripts/test-platform-smoke.sh
# Also run by scripts/ci-local.sh.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

SCRIPT="$PWD/scripts/platform-smoke.sh"
PASS=0
FAIL=0
TMP=""
SERVER_PID=""

cleanup() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    for _ in 1 2 3 4 5; do
      kill -0 "$SERVER_PID" 2>/dev/null || break
      sleep 0.2
    done
    if kill -0 "$SERVER_PID" 2>/dev/null; then
      kill -9 "$SERVER_PID" 2>/dev/null || true
    fi
  fi
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

check() {
  local desc="$1" cond="$2"
  if [ "$cond" = "0" ]; then
    echo "PASS: $desc"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $desc"
    FAIL=$((FAIL + 1))
  fi
}

contains() { # contains <haystack-file> <needle>
  grep -qF -- "$2" "$1"
}

not_contains() {
  ! grep -qF -- "$2" "$1"
}

TMP="$(mktemp -d)"

MOCK_SERVER="$TMP/mock_server.py"
cat > "$MOCK_SERVER" <<'PYEOF'
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

config_path, port_path, log_path = sys.argv[1], sys.argv[2], sys.argv[3]
with open(config_path) as f:
    routes = json.load(f)

def find(method, path):
    for r in routes:
        if r["method"] == method and r["path"] == path:
            return r
    return None

class Handler(BaseHTTPRequestHandler):
    def _handle(self, method):
        length = int(self.headers.get("Content-Length", 0) or 0)
        body = self.rfile.read(length) if length else b""
        auth = self.headers.get("Authorization", "")
        path_only = self.path.split("?", 1)[0]
        with open(log_path, "a") as lf:
            lf.write(json.dumps({
                "method": method,
                "path": self.path,
                "auth": auth,
                "body": body.decode("utf-8", "replace"),
            }) + "\n")
        route = find(method, path_only)
        # Like the platform, refuse a caller with no credential unless the
        # route is marked anonymous ("anon": true).
        if not auth and not (route and route.get("anon")):
            route = {"status": 401, "body": {"error": "unauthorized"}}
        status = route["status"] if route else 200
        resp_body = json.dumps(route["body"]) if route and "body" in route else "{}"
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(resp_body.encode("utf-8"))

    def do_GET(self): self._handle("GET")
    def do_POST(self): self._handle("POST")
    def do_PUT(self): self._handle("PUT")
    def do_DELETE(self): self._handle("DELETE")

    def log_message(self, fmt, *args):
        pass  # silence default access logging on stderr

# Port 0: the kernel picks a free port, so concurrent gates never collide on
# one. The chosen port is published through port_path once the socket listens.
server = HTTPServer(("127.0.0.1", 0), Handler)
with open(port_path + ".tmp", "w") as pf:
    pf.write(str(server.server_address[1]))
import os
os.replace(port_path + ".tmp", port_path)
server.serve_forever()
PYEOF

FAKE_TOKEN="sk-SMOKE-TEST-TOKEN-DO-NOT-LEAK-7f3a9c21"
# Set by start_server to the port the mock server bound. Before the first
# start it names no server: the scenarios that use it then make no request.
PORT=0

all_green_routes() {
  cat <<EOF
[
  {"method":"GET","path":"/v1/health","status":200,"anon":true,"body":{"status":"ok","version":"test"}},
  {"method":"POST","path":"/v1/agents/register","status":201,"body":{"agentId":"agent-123","commandsUrl":"/v1/agents/agent-123/commands","ttl_seconds":90}},
  {"method":"PUT","path":"/v1/agents/agent-123/heartbeat","status":204,"body":{}},
  {"method":"GET","path":"/v1/agents/agent-123/throttles","status":200,"body":{"workspaces":[]}},
  {"method":"GET","path":"/v1/analytics/dashboard","status":200,"body":{}},
  {"method":"GET","path":"/v1/analytics/health","status":200,"body":{"compositeScore":87.5,"compositeGrade":"B","computedAt":"2026-04-16T12:00:00Z","periodDays":30,"totalRunsAnalyzed":24}},
  {"method":"GET","path":"/v1/analytics/runs","status":200,"body":{"runs":[],"nextCursor":null}},
  {"method":"GET","path":"/v1/analytics/trends","status":200,"body":{"granularity":"daily","dateFrom":"2026-07-23T00:00:00.000Z","dateTo":"2026-08-22T00:00:00.000Z","repos":[],"targetSuccessRate":95,"data":[]}},
  {"method":"GET","path":"/v1/analytics/cost","status":200,"body":{}},
  {"method":"GET","path":"/v1/audit/reports","status":200,"body":{"items":[]}},
  {"method":"GET","path":"/v1/audit/retention","status":200,"body":{}},
  {"method":"POST","path":"/v1/audit/integrity","status":200,"body":{"valid":true,"checkedCount":12,"brokenLinks":[]}},
  {"method":"PUT","path":"/v1/attention/sync","status":200,"body":{}}
]
EOF
}

start_server() {
  local routes_json="$1"
  local routes_file="$TMP/routes.json"
  printf '%s' "$routes_json" > "$routes_file"
  : > "$TMP/server.log"
  rm -f "$TMP/port"
  python3 "$MOCK_SERVER" "$routes_file" "$TMP/port" "$TMP/server.log" &
  SERVER_PID=$!
  for _ in $(seq 1 50); do
    [ -s "$TMP/port" ] && break
    sleep 0.1
  done
  if [ ! -s "$TMP/port" ]; then
    # The arms below would assert against no server: say the suite could not
    # run rather than report their failures as the script's.
    echo "HARNESS ERROR: the mock server did not start" >&2
    exit 2
  fi
  PORT="$(cat "$TMP/port")"
}

stop_server() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    if kill -0 "$SERVER_PID" 2>/dev/null; then
      echo "WARN: mock server PID $SERVER_PID still alive after kill" >&2
    fi
    SERVER_PID=""
  fi
}

# --- Scenario 1: all-green run -> exit 0 ------------------------------------
start_server "$(all_green_routes)"

OUT="$TMP/scenario1.out"
SUMMARY="$TMP/scenario1.summary"
: > "$SUMMARY"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "all-green run exits 0" "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"
check "all-green summary reports all-2xx" "$(contains "$SUMMARY" "Every probe that ran passed." && echo 0 || echo 1)"
check "all-green output has no FAIL rows" "$(not_contains "$OUT" $'\tFAIL' && echo 0 || echo 1)"
check "all-green output shows agent heartbeat resolved the registered id" "$(contains "$OUT" "agent-123" && echo 0 || echo 1)"

stop_server

# --- Scenario 2: a 401 fails the run loudly ---------------------------------
ROUTES_401=$(all_green_routes | python3 -c '
import json, sys
routes = json.load(sys.stdin)
for r in routes:
    if r["method"] == "GET" and r["path"] == "/v1/analytics/health":
        r["status"] = 401
        r["body"] = {"error": "unauthorized"}
print(json.dumps(routes))
')
start_server "$ROUTES_401"

OUT="$TMP/scenario2.out"
SUMMARY="$TMP/scenario2.summary"
: > "$SUMMARY"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "a 401 makes the run exit non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "a 401 emits a loud ::error:: AUTH FAILURE annotation" "$(contains "$OUT" "::error::AUTH FAILURE" && echo 0 || echo 1)"
check "a 401 names the offending status in the message" "$(contains "$OUT" "returned 401" && echo 0 || echo 1)"
check "summary marks the 401 endpoint FAIL (auth)" "$(contains "$SUMMARY" "FAIL (auth)" && echo 0 || echo 1)"
check "summary calls out that surfaces failed" "$(contains "$SUMMARY" "One or more probes failed" && echo 0 || echo 1)"

stop_server

# --- Scenario 3: a missing credential skips the signed-in tier only -------
# Nobody provisions the token: production is smoke-tested with a license key
# after every platform deploy, in the platform repository (#2425). An unset
# token therefore skips the signed-in tier with a notice, and the anonymous
# tier still runs and decides the exit code.
start_server "$(all_green_routes)"
OUT="$TMP/scenario3.out"
SUMMARY="$TMP/scenario3.summary"
: > "$SUMMARY"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "missing token with a healthy anonymous tier exits 0" "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"
check "missing token prints a ::notice:: that the signed-in probes were skipped" \
  "$(contains "$OUT" "::notice title=Platform smoke: signed-in probes skipped::PLATFORM_SMOKE_SESSION_TOKEN is not set" && echo 0 || echo 1)"
check "missing token: the summary says the signed-in probes were skipped" \
  "$(contains "$SUMMARY" "Signed-in probes skipped." && echo 0 || echo 1)"
check "missing token: the anonymous API health probe ran" \
  "$(contains "$TMP/server.log" '"path": "/v1/health"' && echo 0 || echo 1)"
check "missing token: user-scoped routes were probed anonymously" \
  "$(contains "$OUT" "Analytics health refuses anonymous" && echo 0 || echo 1)"
check "missing token: no signed-in surface was called" \
  "$(not_contains "$TMP/server.log" "/v1/agents/register" && not_contains "$TMP/server.log" "/v1/attention/sync" && echo 0 || echo 1)"
check "missing token: no request carried a bearer" \
  "$(not_contains "$TMP/server.log" "Bearer" && echo 0 || echo 1)"
stop_server

# A user-scoped route that answers an anonymous caller is open to the world.
ROUTES_OPEN=$(all_green_routes | python3 -c '
import json, sys
routes = json.load(sys.stdin)
for r in routes:
    if r["method"] == "GET" and r["path"] == "/v1/analytics/health":
        r["anon"] = True
print(json.dumps(routes))
')
start_server "$ROUTES_OPEN"
OUT="$TMP/scenario3b.out"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "an anonymous 2xx on a user-scoped route exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "an anonymous 2xx is reported as an open route" \
  "$(contains "$OUT" "::error::OPEN ROUTE" && contains "$OUT" "FAIL (open)" && echo 0 || echo 1)"
stop_server

# API health down: the anonymous tier fails the run even with no token.
ROUTES_DOWN=$(all_green_routes | python3 -c '
import json, sys
routes = json.load(sys.stdin)
for r in routes:
    if r["path"] == "/v1/health":
        r["status"] = 503
        r["body"] = {"status": "degraded"}
print(json.dumps(routes))
')
start_server "$ROUTES_DOWN"
OUT="$TMP/scenario3c.out"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "API health 503 with no token exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "API health 503 names the expected status" "$(contains "$OUT" "returned 503; expected 200" && echo 0 || echo 1)"
stop_server

# No base URL means the public API the daemon uses (#2401).
check "the default target is the public API the daemon uses" \
  "$(grep -qF 'PLATFORM_SMOKE_BASE_URL:-https://api.nightgauge.dev}' "$SCRIPT" \
    && grep -qF 'BaseURL:      "https://api.nightgauge.dev"' internal/platform/client.go && echo 0 || echo 1)"

# --- Scenario 4: the credential never appears in any output ----------------
start_server "$(all_green_routes)"

OUT="$TMP/scenario4.out"
SUMMARY="$TMP/scenario4.summary"
: > "$SUMMARY"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1

# Sanity: the mock server DID receive the real token as the bearer — otherwise
# "the token never leaks" would be true only because it was never sent.
check "the mock server actually received the bearer token" "$(contains "$TMP/server.log" "Bearer ${FAKE_TOKEN}" && echo 0 || echo 1)"

cat "$OUT" "$SUMMARY" > "$TMP/scenario4.combined"
check "the token never appears in stdout/stderr/summary" "$(not_contains "$TMP/scenario4.combined" "$FAKE_TOKEN" && echo 0 || echo 1)"

stop_server

# --- Scenario 5: a 200 with the fictional Health-tab shape fails ------------
# Production GET /v1/analytics/health returns PipelineHealthScore
# (compositeScore, ...). The VSCode Health tab used to decode overall_score /
# dimensions, so a 200 of that invented body still left the tab blank. A
# status-only probe cannot catch that; this canary must.
ROUTES_SHAPE=$(all_green_routes | python3 -c '
import json, sys
routes = json.load(sys.stdin)
for r in routes:
    if r["method"] == "GET" and r["path"] == "/v1/analytics/health":
        r["body"] = {
            "overall_score": 78.4,
            "dimensions": [],
            "generated_at": "2026-08-11T17:04:22Z",
            "period_days": 30,
            "total_runs": 214,
        }
print(json.dumps(routes))
')
start_server "$ROUTES_SHAPE"

OUT="$TMP/scenario5.out"
SUMMARY="$TMP/scenario5.summary"
: > "$SUMMARY"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "wrong health body shape exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "wrong health body shape emits ::error:: naming compositeScore" "$(contains "$OUT" "compositeScore" && echo 0 || echo 1)"
check "summary marks the shape failure FAIL (shape)" "$(contains "$SUMMARY" "FAIL (shape)" && echo 0 || echo 1)"

stop_server

# --- Scenario 6: a 200 with the PUBLISHED-SPEC Runs shape fails -------------
# The platform's OpenAPI document declares GET /v1/analytics/runs as
# {items, has_more, next_cursor}; the route returns {runs, nextCursor}, and
# the platform's own route tests assert the latter. The Go client decodes the
# route's shape, so this canary must reject the documented one — a canary
# written from the spec would agree with the spec and stay green while the
# Runs tab renders nothing (#801).
#
# This is the scenario-5 lesson one level up: there, the client had invented a
# shape; here, the SPEC has.
ROUTES_RUNS_SPEC=$(all_green_routes | python3 -c '
import json, sys
routes = json.load(sys.stdin)
for r in routes:
    if r["method"] == "GET" and r["path"] == "/v1/analytics/runs":
        r["body"] = {
            "items": [{"id": "00000000-0000-4000-8000-000000000001", "issueNumber": 123}],
            "has_more": False,
            "next_cursor": None,
        }
print(json.dumps(routes))
')
start_server "$ROUTES_RUNS_SPEC"

OUT="$TMP/scenario6.out"
SUMMARY="$TMP/scenario6.summary"
: > "$SUMMARY"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "spec-shaped runs body exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "spec-shaped runs body emits ::error:: naming runs" "$(contains "$OUT" "missing required keys: runs" && echo 0 || echo 1)"

stop_server

# --- Scenario 7: a trends 200 built from the ignored `period` fails ---------
# /trends has no `period` parameter and its query schema is .passthrough(), so
# the old canary call (`?period=week`) got a 200 built from the server default
# and asserted nothing about the body. A body missing the envelope keys the Go
# client decodes must now fail.
ROUTES_TRENDS_BAD=$(all_green_routes | python3 -c '
import json, sys
routes = json.load(sys.stdin)
for r in routes:
    if r["method"] == "GET" and r["path"] == "/v1/analytics/trends":
        r["body"] = {"current": [], "previous": [], "period": "week"}
print(json.dumps(routes))
')
start_server "$ROUTES_TRENDS_BAD"

OUT="$TMP/scenario7.out"
SUMMARY="$TMP/scenario7.summary"
: > "$SUMMARY"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "pre-#801 trends body exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "pre-#801 trends body emits ::error:: naming the envelope keys" "$(contains "$OUT" "missing required keys" && echo 0 || echo 1)"

stop_server

# --- Scenario 8: the pre-#803 compliance-list body fails ---------------------
# GET /v1/audit/reports returns {items: [...]}. The Go client decoded
# {reports, nextCursor, hasMore} — three keys the route has never emitted — so
# a 200 rendered an empty Compliance tab while this canary, which probed the
# endpoint for status only, stayed green (#803).
ROUTES_REPORTS_BAD=$(all_green_routes | python3 -c '
import json, sys
routes = json.load(sys.stdin)
for r in routes:
    if r["method"] == "GET" and r["path"] == "/v1/audit/reports":
        r["body"] = {
            "reports": [{"id": "rpt-1", "status": "ready"}],
            "nextCursor": None,
            "hasMore": False,
        }
print(json.dumps(routes))
')
start_server "$ROUTES_REPORTS_BAD"

OUT="$TMP/scenario8.out"
SUMMARY="$TMP/scenario8.summary"
: > "$SUMMARY"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "pre-#803 compliance body exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "pre-#803 compliance body emits ::error:: naming items" "$(contains "$OUT" "missing required keys: items" && echo 0 || echo 1)"

stop_server

# --- Scenario 9: the pre-#822 integrity body fails ---------------------------
# POST /v1/audit/integrity returns {valid, checkedCount, brokenLinks}. The Go
# client decoded {valid, checkedCount, windowDays, message, checkedAt} — three
# of those five have never been sent — and it posted to /v1/audit/integrity/
# verify, a path the platform never mounted. The probe checked status only, so
# neither half was visible here (#822).
ROUTES_INTEGRITY_BAD=$(all_green_routes | python3 -c '
import json, sys
routes = json.load(sys.stdin)
for r in routes:
    if r["method"] == "POST" and r["path"] == "/v1/audit/integrity":
        r["body"] = {
            "valid": True,
            "checkedCount": 12,
            "windowDays": 30,
            "message": "All entries valid",
            "checkedAt": "2026-05-01T00:00:00Z",
        }
print(json.dumps(routes))
')
start_server "$ROUTES_INTEGRITY_BAD"

OUT="$TMP/scenario9.out"
SUMMARY="$TMP/scenario9.summary"
: > "$SUMMARY"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "pre-#822 integrity body exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "pre-#822 integrity body emits ::error:: naming brokenLinks" "$(contains "$OUT" "missing required keys: brokenLinks" && echo 0 || echo 1)"

stop_server

# --- Scenario 10: a platform mounting only the old verify path fails ---------
# The arm that goes red if the path is ever reverted. The mock answers an
# unmapped path with an empty 200 — the friendliest possible wrong answer, and
# still redder than what the real platform would send — so what catches this is
# the shape assertion, not the status. That is the point: a status-only probe
# would call an empty 200 from a path nobody mounts a PASS.
ROUTES_NO_VERIFY_ALIAS=$(all_green_routes | python3 -c '
import json, sys
routes = json.load(sys.stdin)
for r in routes:
    if r["method"] == "POST" and r["path"] == "/v1/audit/integrity":
        r["path"] = "/v1/audit/integrity/verify"
print(json.dumps(routes))
')
start_server "$ROUTES_NO_VERIFY_ALIAS"

OUT="$TMP/scenario10.out"
SUMMARY="$TMP/scenario10.summary"
: > "$SUMMARY"
PLATFORM_SMOKE_BASE_URL="http://127.0.0.1:${PORT}" \
PLATFORM_SMOKE_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "a platform that mounts only the old verify path exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "the integrity row names every key the empty body is missing" "$(contains "$OUT" "missing required keys: valid checkedCount brokenLinks" && echo 0 || echo 1)"

stop_server

# --- Scenario 11: the workflow is dispatch-only and targets production -----
# The daily cron failed 9/9 scheduled runs (08-20..08-28) at the credential
# guard while no credential was provisioned, and a scheduled run attaches its
# check-run to `main`'s HEAD, so it also made the post-merge `check-runs` query
# AGENTS.md mandates report a failure against unrelated merges (#1087). The
# canary is dispatched after each production deploy instead (#2401).
#
# This asserts the trigger set directly, on the parsed YAML rather than on a
# grep of the file, which cannot tell a comment from a trigger. Scenario 3
# above proves a missing credential skips only the signed-in tier.
WF=".github/workflows/platform-smoke.yml"
TRIGGERS="$(python3 - "$WF" <<'PYEOF'
import sys, json

# Stdlib-only: no PyYAML on a bare runner. `on:` is a top-level mapping whose
# keys are the trigger names; take the keys of that block, ignoring comments.
lines = open(sys.argv[1], encoding="utf-8").read().splitlines()
triggers, inside = [], False
for line in lines:
    stripped = line.split("#", 1)[0].rstrip()
    if not stripped:
        continue
    if not line.startswith((" ", "\t")):
        inside = stripped.rstrip(":") in ("on", '"on"', "'on'") and stripped.endswith(":")
        continue
    if inside and len(line) - len(line.lstrip()) == 2:
        key = stripped.strip().split(":", 1)[0].strip()
        if key:
            triggers.append(key)
print(" ".join(triggers))
PYEOF
)"

check "the smoke workflow still has a workflow_dispatch trigger" \
  "$(grep -qw workflow_dispatch <<<"$TRIGGERS" && echo 0 || echo 1)"
check "the smoke workflow has NO schedule trigger (#1087)" \
  "$(grep -qw schedule <<<"$TRIGGERS" && echo 1 || echo 0)"
# shellcheck disable=SC2016 # a literal GitHub expression, not a shell one
check "the workflow passes the production test account's token" \
  "$(contains "$WF" 'PLATFORM_SMOKE_SESSION_TOKEN: ${{ secrets.PLATFORM_SMOKE_SESSION_TOKEN }}' && echo 0 || echo 1)"
check "the workflow targets no other deployment (#2401)" \
  "$(grep -qi 'staging' "$WF" && echo 1 || echo 0)"

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
