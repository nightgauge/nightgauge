#!/usr/bin/env python3
"""Make skipped Go tests visible, and refuse new ones (#474).

`go test ./... -count=1` prints `ok` for a package whose tests all skipped, so a
guard that has stopped guarding — a self-cancelling precondition, a fixture that
can no longer be built, a `t.Skip` added to quiet a flake — is indistinguishable
in CI from a test that passed. That is the same silent-success class this tree
keeps hitting, and it is why #427's reaped-pid guard nearly shipped as a
permanent SKIP.

# Why a named allowlist and not a count

#474 asked for a single-integer budget with a two-sided ratchet, the shape the
publication boundary's tree_baseline uses. A count does not survive contact with
this tree: several skips are platform- or environment-conditional, so the number
legitimately differs between a CI runner and a laptop.
internal/e2e.TestRunCmd_Timeout_SurvivorHoldsPipe_ReturnsPromptly skips when
`setsid` is missing — absent on macOS, present on Linux — and the docker- and
network-gated internal/ipc shape tests skip on whatever the host does not have.
A global integer measured on one machine and enforced on another flaps in both
directions, and a flapping gate gets disabled.

The allowlist keeps the ratchet and drops the flap. Every skip must be a named,
reviewed entry; an unlisted skip fails the run and is named, which is the
property #474 actually wants ("a future self-cancelling guard surfaces instead
of printing ok"). An allowlisted test that RAN is reported, not failed —
that is the environment-conditional case, and failing on it is what a count
would wrongly do.

Usage:
    go test -json -count=1 ./... | tee go-test.json
    python3 scripts/check-go-test-skips.py go-test.json
"""

import json
import os
import sys

ALLOWLIST_FILE = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "go-test-skip-allowlist.txt"
)


def read_allowlist():
    allowed = set()
    with open(ALLOWLIST_FILE) as fh:
        for line in fh:
            line = line.split("#", 1)[0].strip()
            if line:
                allowed.add(line)
    return allowed


def collect_skips(path):
    """Return sorted "pkg.Test" names for every skipped test.

    Only events carrying a non-null Test count: `go test -json` also emits an
    Action "skip" at PACKAGE level for a package with no test files at all,
    which is not a skipped test.
    """
    skipped = set()
    with open(path) as fh:
        for line in fh:
            line = line.strip()
            if not line.startswith("{"):
                # A tee'd stream can carry build noise. Ignore anything that is
                # not a JSON event rather than failing on it.
                continue
            try:
                ev = json.loads(line)
            except json.JSONDecodeError:
                continue
            if ev.get("Action") != "skip":
                continue
            test = ev.get("Test")
            if not test:
                continue
            skipped.add("%s.%s" % (ev.get("Package", "?"), test))
    return sorted(skipped)


def main():
    if len(sys.argv) != 2:
        print("usage: check-go-test-skips.py <go-test.json>", file=sys.stderr)
        return 2
    events_path = sys.argv[1]
    if not os.path.exists(events_path):
        print(
            "check-go-test-skips: %s does not exist — did `go test -json` run?"
            % events_path,
            file=sys.stderr,
        )
        return 2

    allowed = read_allowlist()
    skipped = collect_skips(events_path)
    unlisted = [name for name in skipped if name not in allowed]
    ran = sorted(allowed - set(skipped))

    print("Go test skips: %d (allowlist has %d entries)" % (len(skipped), len(allowed)))
    for name in skipped:
        print(
            "  SKIP %s%s" % (name, "" if name in allowed else "   <-- NOT ALLOWLISTED")
        )
    for name in ran:
        print("  ran (allowlisted as a possible skip) %s" % name)

    if unlisted:
        rel = os.path.relpath(ALLOWLIST_FILE)
        print(
            "\nFAIL: %d skipped test(s) are not in %s:\n  %s\n\n"
            "A skipped test is invisible in `go test ./...` output — the package still "
            "prints ok, so a guard that stopped guarding looks exactly like one that "
            "passed. Either fix the test so it runs, or add it to %s with a comment "
            "saying why the skip is correct."
            % (len(unlisted), rel, "\n  ".join(unlisted), rel),
            file=sys.stderr,
        )
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
