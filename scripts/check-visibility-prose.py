#!/usr/bin/env python3
"""Stale-visibility-prose reintroduction gate (#697).

This repository has been `visibility: public, private: false` since
2026-07-22T00:33:02Z. #697 fixed four tracked artifacts that unconditionally
asserted the repository is PRIVATE in prose -- including one that reassured
an operator a detected publication-boundary violation was harmless, at the
exact moment it wasn't.

The generalizable lesson from #697: an artifact that EVALUATES visibility at
runtime (`github.event.repository.visibility == 'public'`, as codeql.yml and
cla.yml already do) cannot go stale; one that ASSERTS it in prose always
will, the moment visibility changes and nothing re-reviews the sentence.

This gate is the "test or gate that pins non-divergence" #697 asked for. It
fails CI if a tracked file states, as an unconditional fact, that THIS
repository is private -- the exact shape all four #697 instances took. It
does NOT flag legitimate CONDITIONAL prose ("this job stays skipped while
the repo is private", codeql.yml/release.yml) -- describing behavior that is
conditional on visibility is correct at any visibility; asserting the
current value is what goes stale.

Detection: `(repo|repository) is (currently )?private` (case-insensitive),
excluding matches preceded within ~24 characters by a conditional/temporal
qualifier (`while`, `until`, `unless`, `so long as`, `as long as`) -- the
three surviving legitimate uses in codeql.yml/release.yml all take this
shape and must keep passing.

FAIL-CLOSED. Exit codes:
  0  clean
  1  a stale unconditional privacy assertion was found
  2  the gate itself could not run (treated as failure by CI)
"""

from __future__ import annotations

import re
import subprocess
import sys

# The declarative shape: "repo"/"repository" (optionally "this"/"the" before
# it, not required) immediately followed by "is [currently] private".
CLAIM = re.compile(
    r"\b(?:repo|repository)\b\.?\s+is\s+(?:currently\s+)?private\b", re.IGNORECASE
)

# Qualifiers that make the surrounding clause CONDITIONAL rather than a flat
# assertion of the current value ("while the repo is private" is fine at any
# visibility), OR meta-commentary ABOUT the banned shape rather than an
# instance of it (this gate's own wiring comments, e.g. in ci-local.sh and
# lint.yml, necessarily describe "unconditionally asserting this repository
# is private" as the thing being caught). Checked against a window
# immediately before the match.
CONDITIONAL_QUALIFIERS = re.compile(
    r"\b(while|until|unless|so long as|as long as|assert(?:s|ing|ed)?)\s+[\w'-]*\s*$",
    re.IGNORECASE,
)
LOOKBACK = 40  # characters of context checked for a preceding qualifier

# Binary/generated/vendored content is out of scope; this is a prose gate.
SKIP_SUFFIXES = (
    ".png",
    ".jpg",
    ".jpeg",
    ".gif",
    ".svg",
    ".webp",
    ".ico",
    ".lock",
    ".map",
    ".woff",
    ".woff2",
    ".ttf",
)


def die(code: int, msg: str) -> None:
    print(f"\n\033[31mvisibility-prose: {msg}\033[0m", file=sys.stderr)
    sys.exit(code)


def tracked_paths() -> list[str]:
    out = subprocess.run(
        ["git", "ls-files", "-z"], capture_output=True, check=True
    ).stdout.decode()
    return [p for p in out.split("\0") if p]


def scan_file(path: str) -> list[str]:
    try:
        text = open(path, encoding="utf-8", errors="replace").read()
    except OSError as e:
        die(2, f"cannot read tracked file {path}: {e}")
    violations: list[str] = []
    lines = text.splitlines()
    for i, line in enumerate(lines, 1):
        for m in CLAIM.finditer(line):
            context = line[max(0, m.start() - LOOKBACK) : m.start()]
            if CONDITIONAL_QUALIFIERS.search(context):
                continue  # legitimate: "while the repo is private" etc.
            violations.append(
                f"{path}:{i}: unconditional privacy assertion: {line.strip()!r}\n"
                f"    This repository has been public since 2026-07-22 (#697).\n"
                f"    Evaluate visibility at runtime instead of asserting it in prose\n"
                f"    (see .github/workflows/codeql.yml / cla.yml for the pattern), or\n"
                f"    drop the claim entirely."
            )
    return violations


def main() -> int:
    try:
        paths = tracked_paths()
    except Exception as e:  # noqa: BLE001 -- fail closed
        die(2, f"git ls-files failed: {e}")

    violations: list[str] = []
    for path in paths:
        if path.endswith(SKIP_SUFFIXES):
            continue
        if path == "scripts/check-visibility-prose.py":
            continue  # this file necessarily quotes the banned shape in its docstring
        violations.extend(scan_file(path))

    if violations:
        print(
            f"\n\033[31m✗ visibility-prose: {len(violations)} stale privacy assertion(s)\033[0m\n",
            file=sys.stderr,
        )
        for v in violations:
            print(f"  • {v}\n", file=sys.stderr)
        return 1

    print(
        f"\033[32m✓ visibility-prose clean\033[0m — {len(paths)} tracked paths, no stale privacy assertions."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
