#!/usr/bin/env bash
# test-opencode-egress-check.sh — unit tests for opencode-egress-check.sh's
# check-trace subcommand (#1644). No root, no network, no namespace, no live
# opencode/stub-provider binary: every case runs the deterministic parser
# against a canned trace file under testdata/opencode-egress/.
#
# Reproducing the negative control from the acceptance criteria's technical
# notes — "a deliberate `curl https://example.com` injected into the fixture
# task" — is NOT automated here: it would make the CI job depend on a real
# DNS/network failure being reachable-but-blocked, which is exactly the
# namespace's job to prevent, and the leaked-connect.trace/declared-endpoint.trace
# fixtures below already prove the same parser behaviour at the parser level.
# To reproduce it by hand: point `stub-provider --script` at a turn script
# whose fixture task includes a `bash` tool call running
# `curl https://example.com`, then run
# `scripts/opencode-egress-check.sh run /tmp/trace.log /tmp/artifacts` against
# it locally (inside the same `unshare --user --map-root-user --net -- strace
# -f ...` wrapper the CI job uses) and confirm the run fails and lists that
# attempt.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$SCRIPT_DIR/opencode-egress-check.sh"
TESTDATA="$SCRIPT_DIR/testdata/opencode-egress"

fail=0
pass=0

assert_exit() {
  local desc="$1" want="$2"
  shift 2
  local out rc
  out="$("$@" 2>&1)"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    echo "FAIL: $desc: exit $rc, want $want"
    echo "  output: $out"
    fail=$((fail + 1))
    return 1
  fi
  pass=$((pass + 1))
  printf '%s\n' "$out"
  return 0
}

assert_contains() {
  local desc="$1" haystack="$2" needle="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    echo "FAIL: $desc: output does not contain '$needle'"
    echo "  output: $haystack"
    fail=$((fail + 1))
    return 1
  fi
  pass=$((pass + 1))
}

assert_not_contains() {
  local desc="$1" haystack="$2" needle="$3"
  if [[ "$haystack" == *"$needle"* ]]; then
    echo "FAIL: $desc: output unexpectedly contains '$needle'"
    echo "  output: $haystack"
    fail=$((fail + 1))
    return 1
  fi
  pass=$((pass + 1))
}

# --- clean.trace: exit 0, "non-loopback attempts: 0" -----------------------

out="$(assert_exit "clean.trace passes" 0 "$CHECK" check-trace "$TESTDATA/clean.trace")"
assert_contains "clean.trace prints the zero-attempts line" "$out" "non-loopback attempts: 0"

# --- netlink-and-ipv6-fallback.trace: exit 0, no false positives -----------
# Reproduces the real CI trace format from #1644's first run: AF_NETLINK
# interface enumeration (RTM_GETLINK/RTM_GETADDR — never leaves the host)
# and a dual-stack bind that fails on ::1 (EADDRNOTAVAIL inside a namespace
# with no IPv6 loopback configured) and falls back to ::ffff:127.0.0.1.
# strace renders an IPv6 address as the inet_pton() call that reconstructs
# it, with no "sin6_addr=" prefix — unlike this suite's other IPv6 fixture
# lines, which predate seeing a real trace and never matched the parser's
# old regex at all, silently fail-closing every IPv6 destination.

out="$(assert_exit "netlink-and-ipv6-fallback.trace passes" 0 \
  "$CHECK" check-trace "$TESTDATA/netlink-and-ipv6-fallback.trace")"
assert_contains "netlink-and-ipv6-fallback.trace prints the zero-attempts line" "$out" "non-loopback attempts: 0"

# --- connected-send.trace: an address-less send is not an unknown one ------
# Reproduces the second real CI trace from #1644: a sendto()/sendmsg() on an
# already-connected socket passes no destination (sendto's dest_addr is NULL,
# sendmsg's msg_name=NULL), and `ip link set lo up`'s netlink write is one of
# them — strace decodes its payload as RTM_NEWLINK/AF_UNSPEC, so the line
# never spells AF_NETLINK either. Reading those as unreadable destinations
# fail-closed flagged OpenCode's own POST to the loopback stub provider.

out="$(assert_exit "connected-send.trace passes" 0 \
  "$CHECK" check-trace "$TESTDATA/connected-send.trace")"
assert_contains "connected-send.trace prints the zero-attempts line" "$out" "non-loopback attempts: 0"

# --- addressless-send.trace: the fail-closed posture is still intact -------
# The rule above resolves an address-less send against what the fd was
# already seen connecting or binding to — it does not excuse the send. An fd
# this trace never saw connect stays "unknown", and one connected off-box is
# named by its real destination, not waved through.

out="$(assert_exit "addressless-send.trace fails" 1 \
  "$CHECK" check-trace "$TESTDATA/addressless-send.trace")"
assert_contains "a send on an fd with no observed connect stays unknown" "$out" "unknown"
assert_contains "an address-less send inherits its fd's real destination" "$out" "192.0.2.30:443"
assert_contains "addressless-send.trace counts all three" "$out" "non-loopback attempts: 3"

# --- leaked-connect.trace: exit 1, names the PID and executable ------------

out="$(assert_exit "leaked-connect.trace fails" 1 "$CHECK" check-trace "$TESTDATA/leaked-connect.trace")"
assert_contains "leaked-connect.trace names the offending PID" "$out" "PID 200"
assert_contains "leaked-connect.trace names the offending executable" "$out" "(opencode)"
assert_contains "leaked-connect.trace names the offending address" "$out" "192.0.2.10"

# An allow-all parser (never checking the destination) would still exit 1
# only if the count line is wrong; the real regression this guards is a
# parser mutated to treat every destination as allowed, which would exit 0
# here instead of 1 — assert_exit above already catches that directly.

# --- dns-lookup.trace: a DNS sendto to a non-loopback resolver fails -------

out="$(assert_exit "dns-lookup.trace fails" 1 "$CHECK" check-trace "$TESTDATA/dns-lookup.trace")"
assert_contains "dns-lookup.trace names the resolver address" "$out" "8.8.8.8"

# --- declared-endpoint.trace: exact allow-list, not a subnet or allow-all --

out="$(assert_exit "declared-endpoint.trace with the declared endpoint allowed" 1 \
  "$CHECK" check-trace "$TESTDATA/declared-endpoint.trace" 192.0.2.20:8080)"
assert_not_contains "the declared endpoint is not named as a violation" "$out" "192.0.2.20"
assert_contains "the undeclared third address is named" "$out" "203.0.113.5"

out="$(assert_exit "declared-endpoint.trace with no allow-list names both" 1 \
  "$CHECK" check-trace "$TESTDATA/declared-endpoint.trace")"
assert_contains "with no allow-list the declared endpoint is named too" "$out" "192.0.2.20"
assert_contains "with no allow-list the third address is named too" "$out" "203.0.113.5"

# --- redaction: exercises the run subcommand's own redact_env function -----
# (not a fixture standing in for it — a broken or removed redact_env must
# fail this, so the input is fed through `redact-env` itself, not compared
# against a second hand-written "expected" file.)

redacted_dir="$(mktemp -d)"
trap 'rm -rf "$redacted_dir"' EXIT

cat >"$redacted_dir/env.unredacted.txt" <<'EOF'
OPENAI_API_KEY=sk-egress-fake
ANTHROPIC_API_KEY=sk-egress-fake
XAI_API_KEY=sk-egress-fake
PATH=/usr/bin:/bin
EOF

"$CHECK" redact-env "$redacted_dir/env.unredacted.txt" "$redacted_dir/env.redacted.txt"
redact_rc=$?
if [ "$redact_rc" -ne 0 ]; then
  echo "FAIL: redact-env exited $redact_rc"
  fail=$((fail + 1))
else
  pass=$((pass + 1))
fi

redacted_out="$(cat "$redacted_dir/env.redacted.txt" 2>/dev/null)"
assert_not_contains "redact-env strips the fake key literal" "$redacted_out" "sk-egress-fake"
assert_contains "redact-env leaves the key name in place" "$redacted_out" "OPENAI_API_KEY=[REDACTED]"
assert_contains "redact-env passes through unrelated vars untouched" "$redacted_out" "PATH=/usr/bin:/bin"

echo
echo "opencode-egress: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
