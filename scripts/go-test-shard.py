#!/usr/bin/env python3
"""Split the slow Go packages' tests across processes for the local gate (#3209).

scripts/go-test-shards.txt names the packages and their shard counts (format
and membership rule in its header). scripts/ci-local-go-test.sh drives this
script; scripts/ci-local.sh runs each shard as its own grouped step, so each
takes one slot of the machine-wide CI_LOCAL_JOBS budget.

Subcommands:
    steps                 print `<plain|race> <import path> <i> <N>` per shard step,
                          from the config alone (no `go` call), in gate order
    plan --out FILE       list each package's top-level tests with `go test -list`,
                          assign test `name` to shard crc32(name) % N + 1 (a
                          `serial` pin forces shard 1), and fail unless every test
                          is in exactly one shard and every pin names a real test
    run --plan FILE --pkg P --shard I [--race] --timeout T --json FILE
                          `go test -json -run '^(...)$' P` over shard I's tests
    rest [--race] --timeout T --json FILE
                          `go test -json` over every package the config does not shard
    check --plan FILE JSON...
                          fail unless every planned test produced exactly one
                          top-level `run` event across the given shard outputs
    names JSON...         print sorted unique `<package> <test>` from `run` events,
                          to compare the tests one run covered with another's

CI_LOCAL_NO_TEST_CACHE=1 adds -count=1 to `run` and `rest`, as
ci-local-go-test.sh does for the rest of the gate.
"""

import argparse
import concurrent.futures
import json
import os
import re
import subprocess
import sys
import zlib

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DEFAULT_CONFIG = os.path.join(SCRIPT_DIR, "go-test-shards.txt")
ECHO = os.path.join(SCRIPT_DIR, "go-test-json-echo.py")
LISTED = re.compile(r"^(Test|Fuzz|Example)[A-Za-z0-9_]*$")
PROG = "go-test-shard"


class ConfigError(ValueError):
    pass


def read_config(path):
    """Return ({package: N} in file order, {package: [pinned test, ...]})."""
    shards, pins = {}, {}
    with open(path) as fh:
        for lineno, raw in enumerate(fh, 1):
            line = raw.split("#", 1)[0].strip()
            if not line:
                continue
            fields = line.split()
            where = "%s:%d" % (path, lineno)
            if fields[0] == "shard" and len(fields) == 3:
                pkg, count = fields[1], fields[2]
                if not count.isdigit() or int(count) < 1:
                    raise ConfigError("%s: shard count must be a positive integer, got %r" % (where, count))
                if pkg in shards:
                    raise ConfigError("%s: %s is sharded twice" % (where, pkg))
                shards[pkg] = int(count)
            elif fields[0] == "serial" and len(fields) >= 4:
                pkg, test = fields[1], fields[2]
                if not LISTED.match(test):
                    raise ConfigError("%s: %r is not a top-level test name" % (where, test))
                pins.setdefault(pkg, [])
                if test not in pins[pkg]:
                    pins[pkg].append(test)
            else:
                raise ConfigError(
                    "%s: want `shard <import path> <N>` or "
                    "`serial <import path> <TestName> <reason>`, got %r" % (where, line)
                )
    for pkg in pins:
        if pkg not in shards:
            raise ConfigError("%s: `serial` names %s, which has no `shard` line" % (path, pkg))
    return shards, pins


def bucket_of(name, count):
    """0-based bucket of a test name. Stable across runs, machines and Python versions."""
    return zlib.crc32(name.encode("utf-8")) % count


def count_args():
    return ["-count=1"] if os.environ.get("CI_LOCAL_NO_TEST_CACHE") == "1" else []


def list_tests(pkg):
    """Top-level tests, fuzz targets and examples `go test -list` reports for pkg."""
    proc = subprocess.run(
        ["go", "test", "-list", ".", pkg],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    if proc.returncode != 0:
        raise RuntimeError("`go test -list . %s` failed (exit %d):\n%s" % (pkg, proc.returncode, proc.stdout))
    return [line.strip() for line in proc.stdout.splitlines() if LISTED.match(line.strip())]


def partition(pkg, names, count, pinned):
    """Return the buckets for one package, or raise ValueError naming every defect."""
    buckets = [[] for _ in range(count)]
    for name in names:
        index = 0 if name in pinned else bucket_of(name, count)
        buckets[index].append(name)

    defects = []
    stale = [t for t in pinned if t not in names]
    if stale:
        defects += ["`serial` entry names no test in %s: %s" % (pkg, t) for t in stale]
    seen = {}
    for index, bucket in enumerate(buckets):
        for name in bucket:
            seen.setdefault(name, []).append(index + 1)
    defects += [
        "%s %s is in shards %s; a test must be in exactly one" % (pkg, n, ", ".join(map(str, s)))
        for n, s in sorted(seen.items())
        if len(s) > 1
    ]
    defects += ["%s %s is in no shard" % (pkg, n) for n in names if n not in seen]
    if defects:
        raise ValueError("\n  ".join(defects))
    return [sorted(b) for b in buckets]


def cmd_steps(args):
    shards, _ = read_config(args.config)
    for pkg, count in shards.items():
        for index in range(1, count + 1):
            print("plain %s %d %d" % (pkg, index, count))
            print("race %s %d %d" % (pkg, index, count))
    return 0


def cmd_plan(args):
    shards, pins = read_config(args.config)
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, len(shards))) as pool:
        listed = dict(zip(shards, pool.map(lambda p: _try(list_tests, p), shards)))

    plan, defects = {"packages": {}}, []
    for pkg, count in shards.items():
        names = listed[pkg]
        if isinstance(names, Exception):
            defects.append(str(names))
            continue
        if not names:
            defects.append("%s lists no tests; drop its `shard` line" % pkg)
            continue
        try:
            buckets = partition(pkg, names, count, pins.get(pkg, []))
        except ValueError as err:
            defects.append(str(err))
            continue
        plan["packages"][pkg] = {"shards": buckets, "tests": len(names)}
        print(
            "%s: %d tests in %d shards (%s)"
            % (pkg, len(names), count, ", ".join(str(len(b)) for b in buckets))
        )
    if defects:
        print("FAIL: the shard plan is not exact:\n  %s" % "\n  ".join(defects), file=sys.stderr)
        return 1
    with open(args.out, "w") as fh:
        json.dump(plan, fh, indent=1, sort_keys=True)
    return 0


def _try(fn, arg):
    try:
        return fn(arg)
    except Exception as err:  # reported per package by the caller
        return err


def stream(cmd, json_path):
    """Run cmd, write its stdout to json_path and echo it readably. Return the exit code."""
    print("%s: %s" % (PROG, " ".join(cmd)), flush=True)
    with open(json_path, "w") as sink:
        echo = subprocess.Popen([sys.executable, ECHO], stdin=subprocess.PIPE, text=True)
        go = subprocess.Popen(cmd, stdout=subprocess.PIPE, text=True)
        for line in go.stdout:
            sink.write(line)
            echo.stdin.write(line)
        code = go.wait()
        echo.stdin.close()
        echo.wait()
    return code


def go_test_cmd(args):
    cmd = ["go", "test", "-json"]
    if args.race:
        cmd.append("-race")
    return cmd + count_args() + ["-timeout", args.timeout]


def cmd_run(args):
    with open(args.plan) as fh:
        plan = json.load(fh)
    entry = plan["packages"].get(args.pkg)
    if entry is None:
        print("%s: %s is not in the plan %s" % (PROG, args.pkg, args.plan), file=sys.stderr)
        return 2
    if not 1 <= args.shard <= len(entry["shards"]):
        print("%s: %s has shards 1..%d, not %d" % (PROG, args.pkg, len(entry["shards"]), args.shard), file=sys.stderr)
        return 2
    tests = entry["shards"][args.shard - 1]
    if not tests:
        open(args.json, "w").close()
        print("%s: %s shard %d/%d has no tests" % (PROG, args.pkg, args.shard, len(entry["shards"])))
        return 0
    cmd = go_test_cmd(args) + ["-run", "^(%s)$" % "|".join(tests), args.pkg]
    return stream(cmd, args.json)


def cmd_rest(args):
    shards, _ = read_config(args.config)
    proc = subprocess.run(["go", "list", "./..."], stdout=subprocess.PIPE, text=True)
    if proc.returncode != 0:
        print("%s: `go list ./...` failed (exit %d)" % (PROG, proc.returncode), file=sys.stderr)
        return proc.returncode
    rest = [p for p in proc.stdout.split() if p not in shards]
    if not rest:
        open(args.json, "w").close()
        print("%s: every package is sharded; nothing else to run" % PROG)
        return 0
    return stream(go_test_cmd(args) + rest, args.json)


def run_events(paths):
    """Yield (package, test) for every `run` event in the given go test -json files."""
    for path in paths:
        with open(path) as fh:
            for line in fh:
                line = line.strip()
                if not line.startswith("{"):
                    continue
                try:
                    ev = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if ev.get("Action") == "run" and ev.get("Test"):
                    yield ev.get("Package", ""), ev["Test"]


def cmd_check(args):
    with open(args.plan) as fh:
        plan = json.load(fh)
    planned = {
        pkg: {t for bucket in entry["shards"] for t in bucket}
        for pkg, entry in plan["packages"].items()
    }
    counts = {}
    for pkg, test in run_events(args.json):
        if pkg in planned and "/" not in test:
            counts[(pkg, test)] = counts.get((pkg, test), 0) + 1

    defects = []
    for pkg, tests in planned.items():
        for test in sorted(tests):
            n = counts.get((pkg, test), 0)
            if n == 0:
                defects.append("%s %s ran in no shard" % (pkg, test))
            elif n > 1:
                defects.append("%s %s ran %d times; want once" % (pkg, test, n))
    defects += [
        "%s %s ran but is in no shard of the plan" % key
        for key in sorted(counts)
        if key[1] not in planned[key[0]]
    ]
    total = sum(len(t) for t in planned.values())
    if defects:
        print(
            "FAIL: %d of %d sharded test(s) did not run exactly once:\n  %s"
            % (len(defects), total, "\n  ".join(defects)),
            file=sys.stderr,
        )
        return 1
    print("%s: every one of %d sharded test(s) in %d package(s) ran exactly once" % (PROG, total, len(planned)))
    return 0


def cmd_names(args):
    for pkg, test in sorted(set(run_events(args.json))):
        print("%s %s" % (pkg, test))
    return 0


def main():
    parser = argparse.ArgumentParser(prog=PROG, description=__doc__.split("\n", 1)[0])
    parser.add_argument("--config", default=DEFAULT_CONFIG, help="the shard config")
    sub = parser.add_subparsers(dest="command", required=True)

    sub.add_parser("steps")
    p = sub.add_parser("plan")
    p.add_argument("--out", required=True)
    for name in ("run", "rest"):
        p = sub.add_parser(name)
        p.add_argument("--race", action="store_true")
        p.add_argument("--timeout", default="20m")
        p.add_argument("--json", required=True)
        if name == "run":
            p.add_argument("--plan", required=True)
            p.add_argument("--pkg", required=True)
            p.add_argument("--shard", type=int, required=True)
    p = sub.add_parser("check")
    p.add_argument("--plan", required=True)
    p.add_argument("json", nargs="+")
    p = sub.add_parser("names")
    p.add_argument("json", nargs="+")

    args = parser.parse_args()
    handler = {
        "steps": cmd_steps,
        "plan": cmd_plan,
        "run": cmd_run,
        "rest": cmd_rest,
        "check": cmd_check,
        "names": cmd_names,
    }[args.command]
    try:
        return handler(args)
    except (OSError, ConfigError) as err:
        print("%s: %s" % (PROG, err), file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
