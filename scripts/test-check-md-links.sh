#!/usr/bin/env bash
#
# test-check-md-links.sh — regression suite for scripts/check-md-links.sh
# (#1004).
#
# The defect: `markdown-link-check` reports `Status: 0` when a request never
# completes, and the gate failed the job on that identically to a genuine 404.
# It fired on PR #1003 against two URLs that PR never touched; both answered
# from a developer machine at the same moment. The fix re-probes every
# `Status: 0` URL and sorts it into dead / unreachable-from-runner /
# alive-after-reprobe.
#
# Four cases, all hermetic — no live network beyond one DNS lookup for the
# deliberately-unresolvable `.invalid` host, which RFC 2606 guarantees never
# resolves:
#
#   1. A dead RELATIVE link           -> exit 1, counted `dead`.
#      This is what the gate exists for and must stay unconditionally fatal.
#   2. An unresolvable external host  -> exit 1, counted `dead`.
#      NXDOMAIN is deterministic and is a real broken citation.
#   3. A host that drops the checker  -> exit 0, counted `alive-after-reprobe`.
#      A local stub closes the connection without answering unless the
#      re-probe's own User-Agent is used — a bot filter, reproduced exactly.
#   4. A closed local port            -> exit 0, counted
#      `unreachable-from-runner`.
#
# Prove it can go red by removing the re-probe: cases 3 and 4 then exit 1.
#
# Run: bash scripts/test-check-md-links.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

GATE="scripts/check-md-links.sh"
MLC_VERSION="3.14.2"

PASS=0
FAIL=0
TMP=""
SERVER_PID=""

cleanup() {
  # Reap by the PID captured at spawn (AGENTS.md § background processes) —
  # never `jobs`, whose table belongs to one shell instance.
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

check() { # check <description> <0-if-ok>
  if [ "$2" = "0" ]; then
    echo "PASS: $1"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $1"
    FAIL=$((FAIL + 1))
  fi
}

TMP="$(mktemp -d)"

# One install, reused by every case via MD_LINK_CHECK_BIN.
echo "Installing markdown-link-check@${MLC_VERSION} (throwaway prefix)…"
if ! npm install --no-save --no-audit --no-fund --loglevel=error \
  --prefix "$TMP/mlc" "markdown-link-check@${MLC_VERSION}" >/dev/null 2>&1; then
  echo "✗ Failed to install markdown-link-check@${MLC_VERSION}" >&2
  exit 1
fi
export MD_LINK_CHECK_BIN="$TMP/mlc/node_modules/.bin/markdown-link-check"
# Keep the suite quick: the production defaults (20s, 3 retries) would make the
# transport-failure cases take minutes. The classification logic is identical.
export MD_LINK_CHECK_TIMEOUT=5
export MD_LINK_CHECK_RETRIES=1

# --- The bot-filter stub ------------------------------------------------------
# Answers 200 to the re-probe's User-Agent and hangs up on everything else,
# which is what markdown-link-check sees as `Status: 0`. The stock http.server
# cannot express "no response at all", so this handler closes the connection
# before writing a status line.
cat > "$TMP/stub.py" <<'PYEOF'
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def do_GET(self):  # noqa: N802
        # /status/NNN answers NNN to EVERY user agent, so markdown-link-check
        # gets a real status and the link lands on the ANSWERED path rather
        # than on the Status: 0 path the other cases exercise (#1404).
        if self.path.startswith("/status/"):
            try:
                code = int(self.path.rsplit("/", 1)[1])
            except ValueError:
                code = 500
            self.send_response(code)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        agent = self.headers.get("User-Agent", "")
        if "nightgauge-link-check" in agent:
            body = b"ok"
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        # A bot filter refusing the runner: the connection dies with no reply.
        self.close_connection = True
        try:
            self.connection.close()
        except OSError:
            pass

    do_HEAD = do_GET

    def log_message(self, *_args):
        pass


port = int(sys.argv[1])
HTTPServer(("127.0.0.1", port), Handler).serve_forever()
PYEOF

PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
CLOSED_PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"

python3 "$TMP/stub.py" "$PORT" &
SERVER_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if curl -s -o /dev/null -A nightgauge-link-check --max-time 2 "http://127.0.0.1:${PORT}/x"; then
    break
  fi
  sleep 0.3
done

# --- Fixtures ----------------------------------------------------------------
FIX="$TMP/fixtures"
mkdir -p "$FIX"

printf '# case 1\n\n[a missing sibling](./nope-does-not-exist.md)\n' \
  > "$FIX/dead-relative.md"
printf '# case 2\n\n[an unresolvable host](https://nightgauge-nxdomain.invalid/docs)\n' \
  > "$FIX/nxdomain.md"
printf '# case 3\n\n[a bot-walled host](http://127.0.0.1:%s/docs)\n' "$PORT" \
  > "$FIX/bot-walled.md"
printf '# case 4\n\n[a closed port](http://127.0.0.1:%s/docs)\n' "$CLOSED_PORT" \
  > "$FIX/closed-port.md"
printf '# case 5\n\n[a host having a bad day](http://127.0.0.1:%s/status/500)\n' "$PORT" \
  > "$FIX/server-error.md"
printf '# case 7\n\n[a genuinely missing page](http://127.0.0.1:%s/status/404)\n' "$PORT" \
  > "$FIX/not-found.md"

run_gate() { # run_gate <fixture> -> writes $TMP/out, returns the gate's exit
  MD_LINK_CHECK_FILES="$FIX/$1" bash "$GATE" > "$TMP/out" 2>&1
}

# --- Case 1: a dead relative link is fatal -----------------------------------
run_gate dead-relative.md
RC=$?
check "a dead relative link exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "a dead relative link is classed dead" \
  "$(grep -qE 'Link classes: [1-9][0-9]* dead' "$TMP/out" && echo 0 || echo 1)"
check "the summary names the dead relative link" \
  "$(grep -qF 'nope-does-not-exist.md' "$TMP/out" && echo 0 || echo 1)"

# --- Case 2: an unresolvable external host is fatal --------------------------
run_gate nxdomain.md
RC=$?
check "an unresolvable external host exits non-zero" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "an unresolvable host is classed dead, naming the resolve failure" \
  "$(grep -qF 'could not resolve host' "$TMP/out" && echo 0 || echo 1)"

# --- Case 3: a host that drops the checker is NOT fatal ----------------------
run_gate bot-walled.md
RC=$?
check "a bot-walled host exits ZERO" "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"
check "a bot-walled host is classed alive-after-reprobe" \
  "$(grep -qE 'Link classes: 0 dead, 0 unreachable-from-runner, [1-9]' "$TMP/out" && echo 0 || echo 1)"
check "the summary reports what it could not verify directly" \
  "$(grep -qF 'alive-after-reprobe (NOT fatal' "$TMP/out" && echo 0 || echo 1)"

# --- Case 4: a closed port is NOT fatal, and is reported ---------------------
run_gate closed-port.md
RC=$?
check "a closed port exits ZERO" "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"
check "a closed port is classed unreachable-from-runner" \
  "$(grep -qE 'Link classes: 0 dead, [1-9]' "$TMP/out" && echo 0 || echo 1)"
check "the unreachable report names the curl exit code" \
  "$(grep -qE 'curl exit [0-9]+' "$TMP/out" && echo 0 || echo 1)"

# --- Cases 5-7: an answered status is not automatically a dead link (#1404) --
#
# `Status: 0` was re-probed from the start (#1004), but a link the checker got a
# REAL status for went straight to fatal. A 5xx is the server failing, not the
# document being missing — the same condition #1004 exists to tolerate, only
# carrying a number instead of a zero. A Google 500 on a URL that answered 200
# three times a minute later failed the mandatory pre-submission gate for a
# branch that did not touch the file.
#
# The 404 case is the other half: the exemption must be narrow, or this becomes
# a blanket exemption rather than a classification fix.
#
# There is deliberately NO 429 case. A first draft added one, and mutating the
# 429 arm out of the gate left it passing — `.markdown-link-check.json` already
# lists 401/403/429 in `aliveStatusCodes`, so they never reach the gate's
# classification at all. The arm was unreachable and the test was vacuous; both
# were removed rather than kept as decoration.

run_gate server-error.md
RC=$?
check "a 5xx does not fail the gate" "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"
check "a 5xx is classed alive-after-reprobe, not dead" \
  "$(grep -qE 'Link classes: 0 dead, 0 unreachable-from-runner, [1-9]' "$TMP/out" && echo 0 || echo 1)"

run_gate not-found.md
RC=$?
check "a 404 STILL fails the gate" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "a 404 is still classed dead" \
  "$(grep -qE 'Link classes: [1-9]' "$TMP/out" && echo 0 || echo 1)"

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
