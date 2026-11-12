#!/usr/bin/env python3
"""Run the Go tests the test cache cannot track, always uncached (#3208).

scripts/ci-local.sh runs both Go test passes without -count=1, so an unchanged
package replays its last result from Go's test cache. The cache cannot see a
file a child process reads on the test's behalf, so the tests named in
scripts/go-test-uncached.txt (membership rule in its header) run again here
with -count=1.

Each package's entries run as one `go test -json -count=1 -run '^(A|B)$'`.
The JSON events are appended to --json FILE when given, so the skip accounting
covers them, and are echoed readably through go-test-json-echo.py.

Exit status is non-zero when a listed test fails, when `go test` fails, or
when a listed test produced no `run` event: an entry whose test was renamed or
deleted guards nothing, so it fails the gate and is named.

Usage:
    python3 scripts/go-test-uncached.py [--race] [--timeout 20m]
        [--json go-test.json] [--list scripts/go-test-uncached.txt]
"""

import argparse
import json
import os
import re
import subprocess
import sys

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DEFAULT_LIST = os.path.join(SCRIPT_DIR, "go-test-uncached.txt")
ECHO = os.path.join(SCRIPT_DIR, "go-test-json-echo.py")
TEST_NAME = re.compile(r"^Test[A-Za-z0-9_]*$")


def read_list(path):
    """Return an ordered {package: [test, ...]} map, or raise ValueError."""
    entries = {}
    with open(path) as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.split("#", 1)[0].strip()
            if not line:
                continue
            fields = line.split()
            if len(fields) != 2 or not TEST_NAME.match(fields[1]):
                raise ValueError(
                    "%s:%d: want `<import path> <TestName>`, got %r" % (path, lineno, line)
                )
            pkg, test = fields
            tests = entries.setdefault(pkg, [])
            if test not in tests:
                tests.append(test)
    return entries


def run_package(pkg, tests, args, sink):
    """Run one package's listed tests. Return (go exit code, set of tests seen)."""
    cmd = ["go", "test", "-json", "-count=1"]
    if args.race:
        cmd.append("-race")
    cmd += ["-timeout", args.timeout, "-run", "^(%s)$" % "|".join(tests), pkg]
    print("go-test-uncached: %s" % " ".join(cmd), flush=True)

    seen = set()
    echo = subprocess.Popen([sys.executable, ECHO], stdin=subprocess.PIPE, text=True)
    go = subprocess.Popen(cmd, stdout=subprocess.PIPE, text=True)
    for line in go.stdout:
        if sink is not None:
            sink.write(line)
        echo.stdin.write(line)
        stripped = line.strip()
        if not stripped.startswith("{"):
            continue
        try:
            ev = json.loads(stripped)
        except json.JSONDecodeError:
            continue
        if ev.get("Action") == "run" and ev.get("Package") == pkg and ev.get("Test"):
            seen.add(ev["Test"])
    code = go.wait()
    echo.stdin.close()
    echo.wait()
    return code, seen


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("--race", action="store_true", help="add -race")
    parser.add_argument("--timeout", default="20m", help="go test -timeout")
    parser.add_argument("--json", help="append the JSON events to this file")
    parser.add_argument("--list", default=DEFAULT_LIST, help="the always-run list")
    args = parser.parse_args()

    try:
        entries = read_list(args.list)
    except (OSError, ValueError) as err:
        print("go-test-uncached: %s" % err, file=sys.stderr)
        return 2
    if not entries:
        print("go-test-uncached: %s lists no tests" % os.path.relpath(args.list))
        return 0

    failed_pkgs = []
    stale = []
    sink = open(args.json, "a") if args.json else None
    try:
        for pkg, tests in entries.items():
            code, seen = run_package(pkg, tests, args, sink)
            if code != 0:
                failed_pkgs.append(pkg)
            # A package that did not compile ran nothing; that is its failure,
            # not a stale entry.
            if code == 0 or seen:
                stale += ["%s %s" % (pkg, t) for t in tests if t not in seen]
    finally:
        if sink is not None:
            sink.close()

    total = sum(len(t) for t in entries.values())
    print(
        "go-test-uncached: %d test(s) in %d package(s)%s"
        % (total, len(entries), " with -race" if args.race else "")
    )
    rel = os.path.relpath(args.list)
    if failed_pkgs:
        print(
            "FAIL: always-uncached tests failed in:\n  %s" % "\n  ".join(failed_pkgs),
            file=sys.stderr,
        )
    if stale:
        print(
            "FAIL: %d entr%s in %s ran no test, so %s nothing:\n  %s\n"
            "Rename the entry to the test's new name, or delete it if the test is gone."
            % (
                len(stale),
                "y" if len(stale) == 1 else "ies",
                rel,
                "it guards" if len(stale) == 1 else "they guard",
                "\n  ".join(stale),
            ),
            file=sys.stderr,
        )
    return 1 if failed_pkgs or stale else 0


if __name__ == "__main__":
    sys.exit(main())
