#!/usr/bin/env bash
# Regression tests for scripts/staging-platform-smoke.sh.
#
# Runs the WORKING-TREE copy of the script against a small stdlib-only Python
# mock server (no live network, no real staging credential — see the caution
# in nightgauge/nightgauge#754: this suite proves the script's own logic, not
# that staging itself is healthy; that only a real dispatch can prove).
#
# Covers:
#   1. All-green run: every mocked endpoint returns 2xx -> exit 0.
#   2. A 401 on one endpoint fails the whole run loudly (::error::, non-zero
#      exit, summary marks it FAIL (auth)) — the epic's core assertion.
#   3. A missing credential FAILS the job before any HTTP call is made — it
#      does not skip (the mock server sees zero requests).
#   4. No credential value ever appears in the script's stdout, stderr, or
#      $GITHUB_STEP_SUMMARY output, across a normal run.
#
# Run: bash scripts/test-staging-platform-smoke.sh
# Also run by scripts/ci-local.sh.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

SCRIPT="$PWD/scripts/staging-platform-smoke.sh"
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

config_path, port, log_path = sys.argv[1], int(sys.argv[2]), sys.argv[3]
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

HTTPServer(("127.0.0.1", port), Handler).serve_forever()
PYEOF

FAKE_TOKEN="sk-STAGING-TEST-TOKEN-DO-NOT-LEAK-7f3a9c21"
PORT=18734

all_green_routes() {
  cat <<EOF
[
  {"method":"POST","path":"/v1/agents/register","status":201,"body":{"agentId":"agent-123","commandsUrl":"/v1/agents/agent-123/commands","ttl_seconds":90}},
  {"method":"PUT","path":"/v1/agents/agent-123/heartbeat","status":204,"body":{}},
  {"method":"GET","path":"/v1/analytics/dashboard","status":200,"body":{}},
  {"method":"GET","path":"/v1/analytics/health","status":200,"body":{"compositeScore":87.5,"compositeGrade":"B","computedAt":"2026-04-16T12:00:00Z","periodDays":30,"totalRunsAnalyzed":24}},
  {"method":"GET","path":"/v1/analytics/runs","status":200,"body":{}},
  {"method":"GET","path":"/v1/analytics/trends","status":200,"body":{}},
  {"method":"GET","path":"/v1/analytics/cost","status":200,"body":{}},
  {"method":"GET","path":"/v1/audit/reports","status":200,"body":{}},
  {"method":"GET","path":"/v1/audit/retention","status":200,"body":{}},
  {"method":"POST","path":"/v1/audit/integrity/verify","status":200,"body":{}},
  {"method":"PUT","path":"/v1/attention/sync","status":200,"body":{}}
]
EOF
}

start_server() {
  local routes_json="$1"
  local routes_file="$TMP/routes.json"
  printf '%s' "$routes_json" > "$routes_file"
  : > "$TMP/server.log"
  python3 "$MOCK_SERVER" "$routes_file" "$PORT" "$TMP/server.log" &
  SERVER_PID=$!
  for _ in $(seq 1 30); do
    if curl -sf "http://127.0.0.1:${PORT}/v1/health" >/dev/null 2>&1; then
      break
    fi
    # /v1/health is unmapped in routes -> mock returns 200 {} once serving.
    sleep 0.1
  done
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
STAGING_PLATFORM_BASE_URL="http://127.0.0.1:${PORT}" \
STAGING_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "all-green run exits 0" "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"
check "all-green summary reports all-2xx" "$(contains "$SUMMARY" "All surfaces returned 2xx." && echo 0 || echo 1)"
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
STAGING_PLATFORM_BASE_URL="http://127.0.0.1:${PORT}" \
STAGING_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "a 401 makes the run exit non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "a 401 emits a loud ::error:: AUTH FAILURE annotation" "$(contains "$OUT" "::error::AUTH FAILURE" && echo 0 || echo 1)"
check "a 401 names the offending status in the message" "$(contains "$OUT" "returned 401" && echo 0 || echo 1)"
check "summary marks the 401 endpoint FAIL (auth)" "$(contains "$SUMMARY" "FAIL (auth)" && echo 0 || echo 1)"
check "summary calls out that surfaces failed" "$(contains "$SUMMARY" "One or more surfaces failed" && echo 0 || echo 1)"

stop_server

# --- Scenario 3: missing credential fails, does not skip, calls nothing ----
OUT="$TMP/scenario3.out"
STAGING_PLATFORM_BASE_URL="http://127.0.0.1:${PORT}" \
STAGING_SESSION_TOKEN="" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "missing STAGING_SESSION_TOKEN exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "missing-credential message says it must fail, not skip" "$(contains "$OUT" "MUST fail rather than skip" && echo 0 || echo 1)"
check "missing-credential message does not use the word skip approvingly" "$(not_contains "$OUT" "skipping" && echo 0 || echo 1)"

OUT2="$TMP/scenario3b.out"
STAGING_PLATFORM_BASE_URL="" \
STAGING_SESSION_TOKEN="$FAKE_TOKEN" \
  bash "$SCRIPT" > "$OUT2" 2>&1
RC2=$?
check "missing STAGING_PLATFORM_BASE_URL exits non-zero" "$([ "$RC2" -ne 0 ] && echo 0 || echo 1)"
check "missing-base-url message says it must fail, not skip" "$(contains "$OUT2" "MUST fail rather than skip" && echo 0 || echo 1)"

# --- Scenario 4: the credential never appears in any output ----------------
start_server "$(all_green_routes)"

OUT="$TMP/scenario4.out"
SUMMARY="$TMP/scenario4.summary"
: > "$SUMMARY"
STAGING_PLATFORM_BASE_URL="http://127.0.0.1:${PORT}" \
STAGING_SESSION_TOKEN="$FAKE_TOKEN" \
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
STAGING_PLATFORM_BASE_URL="http://127.0.0.1:${PORT}" \
STAGING_SESSION_TOKEN="$FAKE_TOKEN" \
GITHUB_STEP_SUMMARY="$SUMMARY" \
  bash "$SCRIPT" > "$OUT" 2>&1
RC=$?
check "wrong health body shape exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "wrong health body shape emits ::error:: naming compositeScore" "$(contains "$OUT" "compositeScore" && echo 0 || echo 1)"
check "summary marks the shape failure FAIL (shape)" "$(contains "$SUMMARY" "FAIL (shape)" && echo 0 || echo 1)"

stop_server

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
