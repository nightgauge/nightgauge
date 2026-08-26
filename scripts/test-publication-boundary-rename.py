#!/usr/bin/env python3
"""Rename carry-over tests for the publication-boundary guard (#837).

`base_file_numbers()` decides whether an unresolvable `#N` on an edited line is
PRE-EXISTING (charged to the tree-wide burn-down) or NEWLY INTRODUCED (a hard
violation). It answers with `git show <base>:<path>` using the POST-change
path, which cannot resolve for a file that was moved -- so before #837 every
pre-existing dead reference in a renamed file was charged to whoever moved it.

── Why this is a Python test and not another case in the shell suite ─────────

The obvious place for these was `test-publication-boundary.sh`, next to the
existing carry-over cases. That was tried and it is WRONG, for a reason worth
recording: exercising a rename means `git mv`-ing a real tracked file, and that
suite is re-run inside sandboxes and deliberately SIGKILLed mid-run by
`test-publication-boundary-hermeticity.sh`. A kill between the `git mv` and the
restore leaves the checkout missing a source file and the index rewritten, so
the hermeticity suite's reclaiming run went from "completes cleanly" to
"exited 2". The tests passed; the suite around them broke.

A test that mutates shared state cannot be made crash-safe by being careful.
This one builds its own throwaway repository in a temp dir, so there is nothing
to restore and nothing a kill can damage.
"""
from __future__ import annotations

import importlib.util
import os
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
CEILING = 1000

_spec = importlib.util.spec_from_file_location(
    "pbcheck", HERE / "publication-boundary-check.py"
)
pbcheck = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(pbcheck)  # safe: the script guards main() behind __main__

FAILURES: list[str] = []


def ok(msg: str) -> None:
    print(f"  \033[32m✓\033[0m {msg}")


def bad(msg: str) -> None:
    FAILURES.append(msg)
    print(f"  \033[31m✗\033[0m {msg}")


def check(cond: bool, msg: str) -> None:
    ok(msg) if cond else bad(msg)


def git(*args: str, cwd: str) -> None:
    subprocess.run(["git", *args], cwd=cwd, check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def make_repo(tmp: str) -> str:
    """A repository with one committed file carrying two dead references."""
    git("init", "--quiet", cwd=tmp)
    git("config", "user.email", "test@example.com", cwd=tmp)
    git("config", "user.name", "Test", cwd=tmp)
    Path(tmp, "old.md").write_text(
        "Superseded by #2742.\nAlso see #3009 for context.\nNo number here.\n"
    )
    git("add", "old.md", cwd=tmp)
    git("commit", "--quiet", "-m", "base", cwd=tmp)
    return tmp


def run_case(name: str, body) -> None:
    with tempfile.TemporaryDirectory() as tmp:
        prev = os.getcwd()
        try:
            os.chdir(make_repo(tmp))
            pbcheck.rename_map.cache_clear()  # the map is cached per base
            body(tmp)
        finally:
            os.chdir(prev)


def case_rename_carries_over(tmp: str) -> None:
    """The defect: a moved file must keep its pre-existing references."""
    git("mv", "old.md", "new.md", cwd=tmp)
    nums = pbcheck.base_file_numbers("HEAD", "new.md", CEILING)
    check(nums == {2742, 3009},
          f"a renamed file carries its references over (got {sorted(nums)})")


def case_new_file_is_charged(tmp: str) -> None:
    """The arm that stops the rename lookup being made unconditional."""
    Path(tmp, "brand-new.md").write_text("Superseded by #4242.\n")
    git("add", "brand-new.md", cwd=tmp)
    nums = pbcheck.base_file_numbers("HEAD", "brand-new.md", CEILING)
    check(nums == set(),
          f"a genuinely NEW file yields nothing to carry over (got {sorted(nums)})")


def case_rename_is_not_a_blanket_exemption(tmp: str) -> None:
    """A number absent at base is still new, even in a renamed file."""
    git("mv", "old.md", "new.md", cwd=tmp)
    Path(tmp, "new.md").write_text(
        Path(tmp, "new.md").read_text() + "Brand new: #4242.\n"
    )
    git("add", "new.md", cwd=tmp)
    nums = pbcheck.base_file_numbers("HEAD", "new.md", CEILING)
    check(4242 not in nums,
          "a NEW reference added to a RENAMED file is not carried over")
    check(nums == {2742, 3009},
          f"...while the file's pre-existing ones still are (got {sorted(nums)})")


def case_unrenamed_missing_file(tmp: str) -> None:
    """A path that never existed and was never renamed carries nothing."""
    nums = pbcheck.base_file_numbers("HEAD", "does-not-exist.md", CEILING)
    check(nums == set(), "a path absent at base and unrenamed carries nothing")


def case_rename_map_shape(tmp: str) -> None:
    git("mv", "old.md", "new.md", cwd=tmp)
    m = pbcheck.rename_map("HEAD")
    check(m.get("new.md") == "old.md",
          f"rename_map maps new path -> old path (got {m})")


def case_reused_path_reads_its_own_base_content(tmp: str) -> None:
    """Rename A->B, then create a NEW A: the new A reads A's base content.

    This does NOT pin the order in which base_file_numbers consults the rename
    map -- an earlier version of this test claimed it did, and a mutation
    proved otherwise. The map is keyed by the rename DESTINATION, so a reused
    source path is never a key and both orders give the same answer. It is kept
    because the scenario is real and the answer is worth asserting, not because
    it discriminates an implementation choice.
    """
    git("mv", "old.md", "new.md", cwd=tmp)
    Path(tmp, "old.md").write_text("A different file that reuses the path.\n")
    git("add", "old.md", cwd=tmp)
    nums = pbcheck.base_file_numbers("HEAD", "old.md", CEILING)
    check(nums == {2742, 3009},
          f"a REUSED path still reads its own base content (got {sorted(nums)})")


def main() -> int:
    print("publication-boundary — rename carry-over tests (#837)")
    for name, body in [
        ("rename carries over", case_rename_carries_over),
        ("new file charged", case_new_file_is_charged),
        ("rename is not a blanket exemption", case_rename_is_not_a_blanket_exemption),
        ("absent and unrenamed", case_unrenamed_missing_file),
        ("rename_map shape", case_rename_map_shape),
        ("reused path", case_reused_path_reads_its_own_base_content),
    ]:
        run_case(name, body)
    print()
    if FAILURES:
        print(f"\033[31m{len(FAILURES)} FAILED\033[0m")
        return 1
    print("\033[32mall rename carry-over tests passed\033[0m")
    return 0


if __name__ == "__main__":
    sys.exit(main())
