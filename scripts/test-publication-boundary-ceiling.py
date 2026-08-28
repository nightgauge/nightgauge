#!/usr/bin/env python3
"""Derived-reference-ceiling tests for the publication-boundary guard (#1078).

The ceiling used to be a hand-maintained integer in the manifest. It cost 21
chore commits and it failed CLOSED every time it fell behind: the rule started
rejecting legitimate references and turned `main` red. `derived_high_water()`
reads it from the repository's own squash-merge markers instead.

That moves a number a human curated into a number the guard infers, so the
inference is now load-bearing and these tests pin it. The case that matters is
`case_crafted_title_cannot_raise_the_ceiling`: the mark is derived from text
that a pull request author partly controls, and raising the ceiling WEAKENS the
guard silently. Anchoring to the end of the subject is the whole defence, and an
unanchored regex passes every other test in this file.

── Why a throwaway repository and not the shell suite ───────────────────────

Same reason as `test-publication-boundary-rename.py`: exercising this means
writing commits with controlled subjects, and the shell suite is re-run inside
sandboxes and deliberately SIGKILLed mid-run by the hermeticity suite. A test
that mutates shared history cannot be made crash-safe by being careful.
"""
from __future__ import annotations

import importlib.util
import os
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent

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


def commit(tmp: str, subject: str) -> None:
    """One commit whose SUBJECT is exactly `subject`."""
    p = Path(tmp, "log.txt")
    p.write_text(p.read_text() + subject + "\n" if p.exists() else subject + "\n")
    git("add", "log.txt", cwd=tmp)
    git("commit", "--quiet", "-m", subject, cwd=tmp)


def make_repo(tmp: str) -> str:
    git("init", "--quiet", cwd=tmp)
    git("config", "user.email", "test@example.com", cwd=tmp)
    git("config", "user.name", "Test", cwd=tmp)
    commit(tmp, "chore: initialize")
    return tmp


def run_case(name: str, body) -> None:
    with tempfile.TemporaryDirectory() as tmp:
        prev = os.getcwd()
        try:
            os.chdir(make_repo(tmp))
            body(tmp)
        finally:
            os.chdir(prev)


def case_reads_the_squash_marker(tmp: str) -> None:
    """The ordinary path: GitHub's appended `(#N)` is the mark."""
    commit(tmp, "fix(#12): a thing (#340)")
    commit(tmp, "feat: another thing (#341)")
    check(pbcheck.derived_high_water() == 341,
          f"the trailing squash marker is the mark (got {pbcheck.derived_high_water()})")


def case_crafted_title_cannot_raise_the_ceiling(tmp: str) -> None:
    """THE security case. A raised ceiling silently weakens the rule.

    A pull request titled `feat: thing (#99999)` squash-merges to
    `feat: thing (#99999) (#342)`. The author controls the first marker and the
    forge controls the last, so only the last may count. An UNANCHORED search
    reads 99999 here and would let any contributor disable the guard by titling
    a pull request.
    """
    commit(tmp, "feat: sneaky (#99999) (#342)")
    got = pbcheck.derived_high_water()
    check(got == 342,
          f"an author-controlled marker mid-subject does not raise the ceiling (got {got})")


def case_unmarked_subjects_are_ignored(tmp: str) -> None:
    """A direct-push commit carries no marker and must not zero the mark."""
    commit(tmp, "feat: merged properly (#77)")
    commit(tmp, "docs: pushed straight to main with no marker")
    check(pbcheck.derived_high_water() == 77,
          f"a subject with no marker leaves the mark alone (got {pbcheck.derived_high_water()})")


def case_trailing_whitespace_still_counts(tmp: str) -> None:
    """`git log` can hand back a subject with trailing space; it is still a merge."""
    commit(tmp, "feat: spaced (#55) ")
    check(pbcheck.derived_high_water() == 55,
          f"trailing whitespace does not hide the marker (got {pbcheck.derived_high_water()})")


def case_a_repo_with_no_markers_derives_nothing(tmp: str) -> None:
    """Fail-closed input: main() turns this into exit 2 rather than a ceiling of 0."""
    check(pbcheck.derived_high_water() == 0,
          "a history with no merge markers derives 0, so the caller can fail closed")


def case_shallow_clone_is_detected(tmp: str) -> None:
    """A shallow clone would derive a mark far too low and reject the whole tree."""
    check(pbcheck.repository_is_shallow() is False,
          "a full clone is not reported shallow")
    Path(tmp, ".git", "shallow").write_text("0" * 40 + "\n")
    check(pbcheck.repository_is_shallow() is True,
          "a shallow clone is detected, so the guard can refuse to derive a mark")


def main() -> int:
    print("publication-boundary derived ceiling (#1078)")
    for name, body in [
        ("reads the squash marker", case_reads_the_squash_marker),
        ("crafted title cannot raise the ceiling", case_crafted_title_cannot_raise_the_ceiling),
        ("unmarked subjects are ignored", case_unmarked_subjects_are_ignored),
        ("trailing whitespace still counts", case_trailing_whitespace_still_counts),
        ("no markers derives nothing", case_a_repo_with_no_markers_derives_nothing),
        ("shallow clone is detected", case_shallow_clone_is_detected),
    ]:
        run_case(name, body)

    if FAILURES:
        print(f"\n\033[31m{len(FAILURES)} failure(s)\033[0m")
        return 1
    print("\n\033[32mall derived-ceiling cases pass\033[0m")
    return 0


if __name__ == "__main__":
    sys.exit(main())
