#!/usr/bin/env python3
"""Reprint `go test -json` as the human-readable output it wraps.

The Go suite has to run under -json so the skip accounting in
check-go-test-skips.py has events to read (#474): a package whose tests all
skipped still prints `ok`, so the plain output cannot distinguish a guard that
stopped guarding from one that passed. Raw JSON in a CI log is unreadable, and
an unreadable failure log is its own kind of silent failure — so the stream is
tee'd to a file for the checker and piped through here for people.

    go test -json ./... -count=1 | tee go-test.json | python3 scripts/go-test-json-echo.py

Anything that is not a JSON event (build errors, toolchain noise) is passed
through untouched rather than swallowed.
"""

import json
import sys


def main():
    for line in sys.stdin:
        stripped = line.strip()
        if not stripped.startswith("{"):
            sys.stdout.write(line)
            continue
        try:
            event = json.loads(stripped)
        except json.JSONDecodeError:
            sys.stdout.write(line)
            continue
        out = event.get("Output")
        if out:
            sys.stdout.write(out)
    return 0


if __name__ == "__main__":
    sys.exit(main())
