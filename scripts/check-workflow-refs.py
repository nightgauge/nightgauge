#!/usr/bin/env python3
"""Nonexistent-workflow-reference gate (#545).

Three workflows -- `skills-smoke.yml`, `claude-plugin-validation.yml` and
`synthetic-regression.yml` -- were named across CHANGELOG.md, an ADR, two
shell libraries, a Go command's help text, a Go test comment and two docs
pages as though they enforced something in CI. None of them has ever existed
on any ref. A contributor reading `scripts/lint-skills/README.md` reasonably
concluded CI covered the check and skipped it locally, so the smoke harness
those files describe was effectively never run.

That is the same defect class as #539: documentation asserting an enforcement
that does not exist, which causes the manual step it describes to be skipped.
Deleting the sentences fixes today's instances; this gate is what stops the
class from recurring, and it is deliberately cheap -- glob the workflow
directory, grep the tree for the same paths, diff the two sets.

Not every reference is a claim about this repository: docs teach readers to
add a workflow to THEIR repo, and test fixtures name workflows on purpose.
Those live in `scripts/workflow-refs-allowlist.txt`, which also carries a
`stale` bucket for known-false claims whose correction is a wider sweep than
this gate's own change. An allowlist entry that matches nothing is itself a
failure, so a claim that gets fixed cannot leave a dead exemption behind.

FAIL-CLOSED. Exit codes:
  0  clean
  1  a reference to a nonexistent workflow, or a dead allowlist entry
  2  the gate itself could not run (treated as failure by CI)
"""

from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path

ALLOWLIST = Path("scripts/workflow-refs-allowlist.txt")
WORKFLOW_DIR = Path(".github/workflows")
BUCKETS = ("example", "stale")

# A workflow path as it is written in prose, code comments and YAML.
REF = re.compile(r"\.github/workflows/([A-Za-z0-9._-]+\.ya?ml)")

# Binary/vendored content is out of scope; this gate reads text.
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

# Files that DEFINE or TEST this rule necessarily contain example violations.
SELF = (
    "scripts/check-workflow-refs.py",
    "scripts/test-workflow-refs-check.sh",
    str(ALLOWLIST),
)


def die(code: int, msg: str) -> None:
    print(f"\n\033[31mworkflow-refs: {msg}\033[0m", file=sys.stderr)
    sys.exit(code)


def tracked_paths() -> list[str]:
    out = subprocess.run(
        ["git", "ls-files", "-z"], capture_output=True, check=True
    ).stdout.decode()
    return [p for p in out.split("\0") if p]


def existing_workflows() -> set[str]:
    if not WORKFLOW_DIR.is_dir():
        die(2, f"{WORKFLOW_DIR} does not exist; the gate cannot resolve any reference.")
    return {
        p.name
        for p in WORKFLOW_DIR.iterdir()
        if p.is_file() and p.suffix in (".yml", ".yaml")
    }


def load_allowlist() -> dict[tuple[str, str], str]:
    """Return {(path, workflow): bucket}."""
    if not ALLOWLIST.exists():
        die(2, f"{ALLOWLIST} is missing. Failing closed.")
    entries: dict[tuple[str, str], str] = {}
    for lineno, raw in enumerate(ALLOWLIST.read_text(encoding="utf-8").splitlines(), 1):
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        fields = line.split()
        if len(fields) != 3:
            die(
                2,
                f"{ALLOWLIST}:{lineno}: expected 3 fields "
                f"(<bucket> <path> <workflow>), got {len(fields)}: {raw.strip()!r}",
            )
        bucket, path, workflow = fields
        if bucket not in BUCKETS:
            die(
                2,
                f"{ALLOWLIST}:{lineno}: unknown bucket {bucket!r}; "
                f"expected one of {', '.join(BUCKETS)}.",
            )
        entries[(path, workflow)] = bucket
    return entries


def scan(paths: list[str]) -> dict[tuple[str, str], list[int]]:
    """Return {(path, workflow): [line numbers]} for every reference found."""
    found: dict[tuple[str, str], list[int]] = {}
    for path in paths:
        if path.endswith(SKIP_SUFFIXES) or path in SELF:
            continue
        try:
            text = Path(path).read_text(encoding="utf-8", errors="replace")
        except OSError as e:
            die(2, f"cannot read tracked file {path}: {e}")
        if ".github/workflows/" not in text:
            continue
        for i, line in enumerate(text.splitlines(), 1):
            for m in REF.finditer(line):
                found.setdefault((path, m.group(1)), []).append(i)
    return found


def main() -> int:
    try:
        paths = tracked_paths()
    except Exception as e:  # noqa: BLE001 -- fail closed
        die(2, f"git ls-files failed: {e}")

    workflows = existing_workflows()
    allowed = load_allowlist()
    found = scan(paths)

    violations: list[str] = []

    for (path, workflow), lines in sorted(found.items()):
        if workflow in workflows:
            continue
        if (path, workflow) in allowed:
            continue
        where = ", ".join(str(n) for n in lines)
        violations.append(
            f"{path}:{where}: names .github/workflows/{workflow}, "
            f"which does not exist.\n"
            f"    Either add the workflow, or delete the claim — a sentence\n"
            f"    asserting an enforcement that does not exist makes readers\n"
            f"    skip the manual step it describes (#539, #545).\n"
            f"    If this is an example for the reader's own repository, or a\n"
            f"    test fixture, add it to {ALLOWLIST}."
        )

    for (path, workflow), bucket in sorted(allowed.items()):
        if workflow in workflows:
            violations.append(
                f"{ALLOWLIST}: dead entry `{bucket} {path} {workflow}` — "
                f".github/workflows/{workflow} exists now.\n"
                f"    Remove the entry; the reference resolves on its own."
            )
        elif (path, workflow) not in found:
            violations.append(
                f"{ALLOWLIST}: dead entry `{bucket} {path} {workflow}` — "
                f"{path} no longer names it.\n"
                f"    Remove the entry so a fixed claim leaves no exemption behind."
            )

    if violations:
        print(
            f"\n\033[31m✗ workflow-refs: {len(violations)} problem(s)\033[0m\n",
            file=sys.stderr,
        )
        for v in violations:
            print(f"  • {v}\n", file=sys.stderr)
        return 1

    stale = sum(1 for b in allowed.values() if b == "stale")
    print(
        f"\033[32m✓ workflow-refs clean\033[0m — {len(found)} reference(s) across "
        f"{len(paths)} tracked paths resolve to {len(workflows)} workflow(s); "
        f"{len(allowed)} allowlisted ({stale} in the `stale` burn-down bucket)."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
