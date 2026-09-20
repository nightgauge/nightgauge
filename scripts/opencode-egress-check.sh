#!/usr/bin/env bash
# opencode-egress-check.sh — the #1644 egress harness.
#
# Subcommands:
#
#   run <trace-log-path> <artifact-dir>
#     The CI harness. Re-execs itself under
#     `unshare --user --map-root-user --net -- strace -f ...` when not
#     already inside the namespace, starts the #1618 stub provider on
#     loopback, runs the Go egress fixture
#     (TestOpenCodeEgressCheck, internal/execution/opencode_egress_test.go)
#     with the hostile-environment fake cloud keys exported, reaps every
#     spawned PID and confirms each dead, writes a redacted environment dump,
#     and calls check-trace on the produced trace log — check-trace's own
#     exit code and non-loopback-attempts count are the job's authoritative
#     result, not the `go test` exit code alone (AC2: "reads its own trace
#     rather than exit codes").
#
#   check-trace <trace-log-path> [allowed-endpoint ...]
#     The deterministic parser: no namespace, no root, no live binaries.
#     Parses connect/sendto/sendmsg/bind/listen lines from a strace -f
#     -e trace=connect,sendto,sendmsg,bind,listen log. AF_UNIX, AF_NETLINK,
#     127.0.0.0/8 and ::1 destinations are always allowed; each allowed-endpoint
#     argument (an exact "host:port" literal, e.g. "192.0.2.20:8080") is an
#     additional allowed destination for this call only — CI's own
#     invocation passes none, so CI stays loopback-only. Prints
#     "non-loopback attempts: N" always; on any disallowed destination it
#     first prints one "PID <pid> (<executable>): <destination>" line per
#     violation and exits 1, else exits 0.
#
#   redact-env <in> <out>
#     Exposes the `run` subcommand's own env-redaction step (see redact_env
#     below) so it can be exercised directly against a fixture, rather than
#     only indirectly through a full `run`.
#
# Fake key literals never come from a secret: they are hard-coded here and
# in .github/workflows/opencode-egress.yml (AC: "the fake keys must never be
# real").
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

die() {
  echo "opencode-egress-check.sh: $*" >&2
  exit 1
}

# ---------------------------------------------------------------------------
# check-trace
# ---------------------------------------------------------------------------

cmd_check_trace() {
  local trace="${1:?usage: check-trace <trace-log-path> [allowed-endpoint ...]}"
  shift || true
  [ -f "$trace" ] || die "no trace log at $trace"
  python3 - "$trace" "$@" <<'PY'
# The #1644 egress check's deterministic trace parser.
#
# Reads a strace -f -e trace=connect,sendto,sendmsg,bind,listen log and
# asserts every connect/sendto/sendmsg/bind destination, and every listen()'s
# own already-bound address, is AF_UNIX, AF_NETLINK, 127.0.0.0/8, ::1, or one
# of the allowed-endpoint arguments (exact "host:port" literals). Never resolves a
# DNS name: a sendto to a resolver is itself the finding, not something to
# look up and excuse.
#
# listen(fd, backlog) carries no address of its own — the address came from
# an earlier bind() on the same fd — so this parser tracks each (pid, fd)'s
# last bound address and looks it up when a matching listen() is seen; a
# listen() with no recorded bind (e.g. on an inherited fd) is not flagged,
# since there is nothing observed in this trace to call unsafe.
#
# Exit 0 and prints "non-loopback attempts: 0" on a clean trace. Exit 1 and,
# before the count line, one "PID <pid> (<executable>): <destination>" line
# per disallowed destination.
import ipaddress
import re
import sys

SYSCALL_RE = re.compile(r"^(\d+)\s+(connect|sendto|sendmsg|bind|listen)\((\d+)")
EXECVE_RE = re.compile(r'^(\d+)\s+execve\("([^"]+)"')
CLONE_RE = re.compile(r"^(\d+)\s+(clone|clone3|fork|vfork)\(.*=\s*(-?\d+)\s*$")
ADDR4_RE = re.compile(r'sin_addr=inet_addr\("([^"]+)"\)')
# strace renders a sockaddr_in6's address as the inet_pton() call that would
# reconstruct it — literally `inet_pton(AF_INET6, "::1", &sin6_addr)` — not
# as "sin6_addr=<value>"; a `sin6_addr=inet_pton(...)` prefix never appears
# in real output, so requiring it here always failed to match, sending every
# IPv6 destination (::1 loopback included) to the fail-closed "unknown"
# branch below.
ADDR6_RE = re.compile(r'inet_pton\([^,]*,\s*"([^"]+)"')
PORT_RE = re.compile(r"sin_?port6?=htons\((\d+)\)")


def is_loopback(addr):
    try:
        ip = ipaddress.ip_address(addr)
    except ValueError:
        return False
    if ip.is_loopback:
        return True
    # ipaddress.IPv6Address.is_loopback only matches the literal ::1: an
    # IPv4-mapped loopback (::ffff:127.0.0.1 — what a dual-stack bind falls
    # back to after ::1 fails inside a namespace with no IPv6 loopback
    # configured) does not set it, even though it is 127.0.0.1 on the wire.
    mapped = getattr(ip, "ipv4_mapped", None)
    return mapped is not None and mapped.is_loopback


def destination(line):
    if "AF_UNIX" in line or "AF_NETLINK" in line:
        # AF_NETLINK is the kernel/userspace interface-configuration
        # protocol (RTM_GETLINK, RTM_GETADDR, ...): it never leaves the
        # host, so it is not a network destination to fail closed on, same
        # as AF_UNIX above. Node/Go's own network-interface enumeration
        # (os.networkInterfaces(), net.Interfaces()) routinely opens one of
        # these on startup, with no attacker-observable effect off-box.
        return None
    m4 = ADDR4_RE.search(line)
    m6 = ADDR6_RE.search(line)
    addr = m4.group(1) if m4 else (m6.group(1) if m6 else None)
    if addr is None:
        # An AF_INET/AF_INET6 socket op strace could not be parsed for an
        # address: treated as disallowed rather than silently skipped, since
        # a parser that cannot read a destination cannot prove it was
        # loopback (fail closed, matching the namespace's own guarantee).
        addr = "unknown"
    mport = PORT_RE.search(line)
    port = mport.group(1) if mport else None
    label = f"{addr}:{port}" if port else addr
    return addr, port, label


def main(argv):
    if len(argv) < 2:
        print("usage: check-trace <trace-log> [allowed-endpoint ...]", file=sys.stderr)
        return 2
    trace_path = argv[1]
    allowed = set(argv[2:])

    exe_by_pid = {}
    bound_addr_by_pid_fd = {}
    violations = []

    def is_allowed(addr, label):
        if is_loopback(addr):
            return True
        return label in allowed or addr in allowed

    with open(trace_path, "r", errors="replace") as f:
        for raw in f:
            line = raw.rstrip("\n")

            m = EXECVE_RE.match(line)
            if m:
                exe_by_pid[m.group(1)] = m.group(2).rsplit("/", 1)[-1]
                continue

            m = CLONE_RE.match(line)
            if m:
                parent_pid, _, child_pid = m.group(1), m.group(2), m.group(3)
                if child_pid.lstrip("-").isdigit() and int(child_pid) > 0:
                    exe_by_pid[child_pid] = exe_by_pid.get(parent_pid, "unknown")
                continue

            m = SYSCALL_RE.match(line)
            if not m:
                continue
            pid, syscall, fd = m.group(1), m.group(2), m.group(3)

            if syscall == "listen":
                bound = bound_addr_by_pid_fd.get((pid, fd))
                if bound is None:
                    continue
                addr, label = bound
                if is_allowed(addr, label):
                    continue
                exe = exe_by_pid.get(pid, "unknown")
                violations.append((pid, exe, label))
                continue

            dest = destination(line)
            if dest is None:
                continue
            addr, port, label = dest

            if syscall == "bind":
                bound_addr_by_pid_fd[(pid, fd)] = (addr, label)

            if is_allowed(addr, label):
                continue
            exe = exe_by_pid.get(pid, "unknown")
            violations.append((pid, exe, label))

    for pid, exe, label in violations:
        print(f"PID {pid} ({exe}): {label}")
    print(f"non-loopback attempts: {len(violations)}")
    return 1 if violations else 0


sys.exit(main(sys.argv))
PY
}

# ---------------------------------------------------------------------------
# run
# ---------------------------------------------------------------------------

# The three fake cloud-provider keys the hostile-environment leg exports
# (AC3): literal strings, never a real credential and never a
# `${{ secrets.* }}` reference in the calling workflow.
FAKE_OPENAI_API_KEY="sk-egress-fake"
FAKE_ANTHROPIC_API_KEY="sk-egress-fake"
FAKE_XAI_API_KEY="sk-egress-fake"

cmd_run() {
  local trace_path="${1:?usage: run <trace-log-path> <artifact-dir>}"
  local artifact_dir="${2:?usage: run <trace-log-path> <artifact-dir>}"
  mkdir -p "$artifact_dir"
  trace_path="$(cd "$(dirname "$trace_path")" && pwd)/$(basename "$trace_path")"
  artifact_dir="$(cd "$artifact_dir" && pwd)"

  if [ "${OPENCODE_EGRESS_INNER:-}" != "1" ]; then
    command -v unshare >/dev/null 2>&1 || die "unshare is required (Linux only; this harness is CI's Linux egress job)"
    command -v strace >/dev/null 2>&1 || die "strace is required"

    # Build both binaries OUTSIDE the namespace, before the module cache is
    # ever needed again: `go build`/`go test -c` may need to fetch a module,
    # and the namespace's whole point is that nothing inside it can reach a
    # registry — a legitimate module fetch must never be mistaken for the
    # egress leak this check exists to catch.
    local prebuild
    prebuild="$(mktemp -d)"
    ( cd "$REPO_ROOT" && go build -o "$prebuild/stub-provider" ./cmd/stub-provider ) \
      || die "building cmd/stub-provider failed"
    ( cd "$REPO_ROOT" && go test -tags canary -c ./internal/execution -o "$prebuild/opencode-egress-test.bin" ) \
      || die "compiling the egress test binary failed"

    exec unshare --user --map-root-user --net -- \
      strace -f -e trace=connect,sendto,sendmsg,bind,listen -o "$trace_path" -- \
      env OPENCODE_EGRESS_INNER=1 \
        OPENCODE_EGRESS_STUB_BIN="$prebuild/stub-provider" \
        OPENCODE_EGRESS_TEST_BIN="$prebuild/opencode-egress-test.bin" \
        "$0" run "$trace_path" "$artifact_dir"
  fi

  bring_loopback_up
  echo "opencode-egress-check.sh: inside the network namespace (only lo is up)"

  local bin="${OPENCODE_EGRESS_STUB_BIN:?OPENCODE_EGRESS_STUB_BIN must be set inside the namespace}"
  local testbin="${OPENCODE_EGRESS_TEST_BIN:?OPENCODE_EGRESS_TEST_BIN must be set inside the namespace}"

  "$bin" --script tool-edit-stop --listen 127.0.0.1:0 \
    --max-requests 20 --idle-timeout 60s >"$artifact_dir/stub.jsonl" &
  local stub_pid=$!
  sleep 1
  kill -0 "$stub_pid" 2>/dev/null || die "stub-provider did not stay up"

  local pidfile="$artifact_dir/pids.txt"
  echo "$stub_pid" >"$pidfile"

  local gotest_rc=0
  (
    cd "$REPO_ROOT" || exit 1
    export OPENAI_API_KEY="$FAKE_OPENAI_API_KEY"
    export ANTHROPIC_API_KEY="$FAKE_ANTHROPIC_API_KEY"
    export XAI_API_KEY="$FAKE_XAI_API_KEY"
    "$testbin" -test.run TestOpenCodeEgressCheck -test.v &
    echo $! >>"$pidfile"
    wait $!
  )
  gotest_rc=$?

  env | grep -v '^_=' > "$artifact_dir/env.raw.txt" || true
  redact_env "$artifact_dir/env.raw.txt" "$artifact_dir/env.redacted.txt"
  rm -f "$artifact_dir/env.raw.txt"

  reap_and_confirm_dead "$pidfile"

  local check_rc=0
  local attempts
  attempts="$(cmd_check_trace "$trace_path")" || check_rc=$?
  printf '%s\n' "$attempts"

  local result="pass"
  local rc=0
  if [ "$gotest_rc" -ne 0 ]; then
    echo "opencode-egress-check.sh: the Go fixture (TestOpenCodeEgressCheck) exited $gotest_rc" >&2
    result="fail"
    rc=1
  fi
  if [ "$check_rc" -ne 0 ]; then
    result="fail"
    rc=1
  fi

  local non_loopback
  non_loopback="$(printf '%s\n' "$attempts" | sed -n 's/^non-loopback attempts: \([0-9]*\)$/\1/p' | tail -n1)"
  [ -n "$non_loopback" ] || non_loopback=-1
  python3 -c "
import json
print(json.dumps({'result': '$result', 'non_loopback_attempts': $non_loopback, 'opencode_version': '${OPENCODE_EGRESS_VERSION:-}'}))
" >"$artifact_dir/summary.json"

  # AC's own literal-key check: the redaction function's own claim, verified
  # independently against the actual artifact directory (not just the
  # redacted env dump above).
  if grep -R -l -F -e "$FAKE_OPENAI_API_KEY" -e "$FAKE_ANTHROPIC_API_KEY" -e "$FAKE_XAI_API_KEY" "$artifact_dir" >/dev/null 2>&1; then
    echo "opencode-egress-check.sh: a fake key literal is present, unredacted, under $artifact_dir" >&2
    rc=1
  fi

  exit "$rc"
}

# redact_env <in> <out>: copies in to out with every value of a name matching
# AWS_SECRET|_TOKEN|_KEY|_PASSWORD (the pattern this codebase's own redaction
# uses elsewhere, internal/config/redact.go) replaced with [REDACTED] — the
# same literal that package's RedactedValue constant carries.
redact_env() {
  local in="$1" out="$2"
  python3 - "$in" "$out" <<'PY'
import re
import sys

src, dst = sys.argv[1], sys.argv[2]
pattern = re.compile(r"^(?P<name>[A-Za-z_][A-Za-z0-9_]*)=(?P<value>.*)$")
name_re = re.compile(r"(AWS_SECRET|_TOKEN|_KEY|_PASSWORD)")
with open(src) as f, open(dst, "w") as out:
    for line in f:
        m = pattern.match(line.rstrip("\n"))
        if m and name_re.search(m.group("name")):
            out.write(f"{m.group('name')}=[REDACTED]\n")
        else:
            out.write(line)
PY
}

# bring_loopback_up: brings the new network namespace's loopback interface up.
#
# `unshare --net` gives the namespace a loopback interface that is DOWN, so
# every connect() to 127.0.0.1 inside it fails with ENETUNREACH even though
# bind() succeeds — the stub provider comes up and stays up, OpenCode then
# cannot reach it, and the stage fails ~95s later with no attributable cause
# while the trace honestly reports "non-loopback attempts: 0". Bringing lo up
# is what makes this namespace loopback-ONLY rather than network-NONE, which
# is what the check is evidence for.
#
# `ip` lives in /usr/sbin on Ubuntu and the calling workflow deliberately
# keeps a narrow PATH, so resolve it from PATH first and fall back to the
# usual absolute locations rather than widening PATH for the whole run. The
# AF_NETLINK socket `ip` opens is already an allowed destination in
# check-trace (it never leaves the host), so this adds nothing the parser
# would flag.
bring_loopback_up() {
  local ip_bin=""
  local cand
  for cand in "$(command -v ip 2>/dev/null || true)" /usr/sbin/ip /sbin/ip /usr/bin/ip /bin/ip; do
    if [ -n "$cand" ] && [ -x "$cand" ]; then
      ip_bin="$cand"
      break
    fi
  done
  [ -n "$ip_bin" ] || die "ip(8) not found: cannot bring the namespace's loopback interface up"
  "$ip_bin" link set lo up || die "bringing the namespace's loopback interface up failed"

  # Verify rather than assume: a silent no-op here reproduces the exact opaque
  # failure this function exists to prevent, so prove a loopback connect works
  # before any live binary depends on it.
  python3 - <<'LOOPBACK_PROBE' || die "loopback is unusable inside the namespace after 'ip link set lo up'"
import socket
import sys

server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
try:
    server.bind(("127.0.0.1", 0))
    server.listen(1)
    client = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    client.settimeout(5)
    client.connect(server.getsockname())
    server.accept()[0].close()
    client.close()
except OSError as err:
    print(f"loopback probe failed: {err}", file=sys.stderr)
    sys.exit(1)
finally:
    server.close()
LOOPBACK_PROBE
}

# reap_and_confirm_dead <pidfile>: kills every PID listed in pidfile, then
# polls `kill -0` on each until it fails or a short deadline elapses,
# printing "dead <pid>" once confirmed (AC5: "the cleanup step prints one
# `dead` line per captured PID").
reap_and_confirm_dead() {
  local pidfile="$1" pid
  [ -f "$pidfile" ] || return 0
  while read -r pid; do
    [ -n "$pid" ] || continue
    kill "$pid" 2>/dev/null || true
  done <"$pidfile"

  while read -r pid; do
    [ -n "$pid" ] || continue
    local waited=0
    while kill -0 "$pid" 2>/dev/null; do
      sleep 0.5
      waited=$((waited + 1))
      if [ "$waited" -gt 20 ]; then
        kill -9 "$pid" 2>/dev/null || true
      fi
      if [ "$waited" -gt 40 ]; then
        break
      fi
    done
    if kill -0 "$pid" 2>/dev/null; then
      echo "opencode-egress-check.sh: pid $pid is still alive after cleanup" >&2
    else
      echo "dead $pid"
    fi
  done <"$pidfile"
}

# ---------------------------------------------------------------------------
# dispatch
# ---------------------------------------------------------------------------

cmd_redact_env() {
  local in="${1:?usage: redact-env <in> <out>}" out="${2:?usage: redact-env <in> <out>}"
  redact_env "$in" "$out"
}

case "${1:-}" in
  run) shift; cmd_run "$@" ;;
  check-trace) shift; cmd_check_trace "$@" ;;
  redact-env) shift; cmd_redact_env "$@" ;;
  *) die "usage: $0 {run|check-trace|redact-env} ..." ;;
esac
