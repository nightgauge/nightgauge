#!/usr/bin/env python3
"""Ceiling-lag and erosion tests for the publication-boundary guard (#1129).

Two properties of the guard combine badly, and both are exercised here.

1. The reference ceiling is derived from the trailing `(#N)` of squash-merge
   subjects on FIRST-PARENT history. AGENTS.md forbids force-push and rebase,
   so `git merge origin/main` is the only permitted way to update a branch --
   and it puts the mainline on the SECOND parent, where first-parent traversal
   never looks. A branch's derived ceiling therefore stops at its fork point
   and goes staler the longer the branch lives.

2. `issue_references.tree_baseline` is one global integer, but the count it
   ratchets is a function of the ceiling: a LOWER ceiling leaves more numbers
   above it and so measures MORE. A count taken on a lagging branch is not
   comparable with one taken on the mainline.

Together they produced a real outage of the guard: a fall was reported, the
baseline was lowered to match, and the next branch -- measuring at its own
lower ceiling -- was blocked by references it had never written. The fall had
been pure erosion; nothing had left the tree.

── Why a throwaway repository and not the shell suite ───────────────────────

Same reason as `test-publication-boundary-ceiling.py`: every case here needs
controlled merge topology and controlled commit subjects, and the shell suite
runs against a sandbox of the REAL tree, whose ceiling and reference count move
under it. A test whose expected numbers depend on this month's history cannot
state what it is asserting.
"""

from __future__ import annotations

import importlib.util
import os
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
CHECK = HERE / "publication-boundary-check.py"

_spec = importlib.util.spec_from_file_location("pbcheck", CHECK)
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
    subprocess.run(
        ["git", *args],
        cwd=cwd,
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )


# The fixtures cite numbers that must NOT resolve, and this file is itself part
# of the tree the guard scans. Every such number is therefore assembled from an
# int at runtime and never written as a literal `#N` -- otherwise the test
# fixtures would raise the tree-wide count they exist to reason about.
def ref(n: int) -> str:
    return "#" + str(n)


MANIFEST_REL = ".github/publication-boundary.yaml"

MANIFEST = """version: 1
allow:
  - path: "**"
    class: PUBLIC
    rationale: everything in this throwaway fixture is publishable
issue_references:
  slack: 0
  tree_baseline: {baseline}
"""


def write_manifest(tmp: str, baseline: int) -> None:
    p = Path(tmp, MANIFEST_REL)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(MANIFEST.format(baseline=baseline))


def commit(tmp: str, subject: str) -> None:
    """One commit whose SUBJECT is exactly `subject`.

    Each commit touches its OWN file. Appending to one shared log makes the
    merge in `_branch_that_merged_main` conflict, and the merge is the point of
    these fixtures.
    """
    stem = "".join(c if c.isalnum() else "_" for c in subject)[:40]
    Path(tmp, f"c_{stem}.txt").write_text(subject + "\n")
    git("add", "-A", cwd=tmp)
    git("commit", "--quiet", "-m", subject, cwd=tmp)


def make_repo(tmp: str) -> str:
    git("init", "--quiet", "--initial-branch=main", cwd=tmp)
    git("config", "user.email", "test@example.com", cwd=tmp)
    git("config", "user.name", "Test", cwd=tmp)
    git("config", "commit.gpgsign", "false", cwd=tmp)
    commit(tmp, "chore: initialize")
    return tmp


def run_check(tmp: str, base: str) -> tuple[int, str]:
    """The checker, end to end, with its diff base pinned. Returns (code, out)."""
    env = dict(os.environ)
    env["NG_BOUNDARY_DIFF_BASE"] = base
    env["NG_BOUNDARY_JOBS"] = "1"
    r = subprocess.run(
        [sys.executable, str(CHECK)],
        cwd=tmp,
        env=env,
        capture_output=True,
    )
    return r.returncode, (r.stdout + r.stderr).decode("utf-8", "replace")


def run_case(name: str, body) -> None:
    print(f"\n{name}")
    with tempfile.TemporaryDirectory() as tmp:
        prev = os.getcwd()
        try:
            os.chdir(make_repo(tmp))
            body(tmp)
        finally:
            os.chdir(prev)


# ── 1. The ceiling lags across a merge, and the base ref repairs it ──────────


def _branch_that_merged_main(tmp: str) -> None:
    """main at (#200); a branch forked at (#100) that has since merged main in."""
    commit(tmp, "feat: base work (#100)")
    git("checkout", "--quiet", "-b", "work", cwd=tmp)
    commit(tmp, "wip: local commit with no marker")
    git("checkout", "--quiet", "main", cwd=tmp)
    commit(tmp, "feat: landed later (#200)")
    git("checkout", "--quiet", "work", cwd=tmp)
    # The ONLY permitted update path: a merge, never a rebase or force-push.
    git(
        "merge",
        "--quiet",
        "--no-ff",
        "-m",
        "Merge branch 'main' into work",
        "main",
        cwd=tmp,
    )


def case_merging_main_does_not_advance_the_branch_ceiling(tmp: str) -> None:
    """The trap itself, pinned so nobody rediscovers it the expensive way."""
    _branch_that_merged_main(tmp)
    got = pbcheck.derived_high_water()
    check(
        got == 100,
        f"a branch that merged main still derives its OWN fork-point mark (got {got})",
    )
    check(
        pbcheck.derived_high_water("main") == 200,
        "the mainline's own first-parent line carries the newer mark",
    )


def case_the_base_ref_supplies_the_mainline_mark(tmp: str) -> None:
    """`ceiling_marks` takes the larger of the two first-parent lines."""
    _branch_that_merged_main(tmp)
    mark, source = pbcheck.ceiling_marks("main")
    check(
        (mark, source) == (200, "main"),
        f"the base ref repairs the lagging ceiling (got {mark} from {source})",
    )


def case_the_branch_mark_still_wins_when_it_is_higher(tmp: str) -> None:
    """max(), not "prefer the base ref": the branch may be the one that is ahead."""
    commit(tmp, "feat: base work (#100)")
    git("checkout", "--quiet", "-b", "work", cwd=tmp)
    commit(tmp, "feat: merged onto this line (#300)")
    mark, source = pbcheck.ceiling_marks("main")
    check(
        (mark, source) == (300, "HEAD"),
        f"a lower base ref never lowers the ceiling (got {mark} from {source})",
    )
    fallback = pbcheck.ceiling_marks(None)
    check(
        fallback == (300, "HEAD"),
        f"with no base ref the branch's own line is the mark (got {fallback})",
    )


def case_a_lagging_branch_is_not_blocked_by_the_ratchet(tmp: str) -> None:
    """END TO END: the failure #1129 reports, in its smallest complete form.

    `doc.md` cites a number the mainline has issued and the branch has not seen.
    Measured at the branch's stale ceiling it is unresolvable and the tree-wide
    count rises above the baseline -- blocking a branch over a reference it
    never wrote. Measured at the mainline's, it resolves.
    """
    Path(tmp, "doc.md").write_text(f"see {ref(150)} for the rationale\n")
    write_manifest(tmp, 0)
    commit(tmp, "feat: base work (#100)")
    git("checkout", "--quiet", "-b", "work", cwd=tmp)
    commit(tmp, "docs: an unrelated edit on the branch")
    git("checkout", "--quiet", "main", cwd=tmp)
    commit(tmp, "feat: the merge that issued 150 (#200)")
    git("checkout", "--quiet", "work", cwd=tmp)
    git(
        "merge",
        "--quiet",
        "--no-ff",
        "-m",
        "Merge branch 'main' into work",
        "main",
        cwd=tmp,
    )

    code, out = run_check(tmp, "main")
    check(
        code == 0,
        f"a branch whose first-parent ceiling lags main is not blocked (exit {code})",
    )
    check(
        "COUNT ROSE" not in out,
        "the ratchet does not fire on a pre-existing reference the mainline has resolved",
    )


# ── 2. A fall is only ratchetable when references actually left the tree ─────


def case_an_eroded_fall_does_not_prompt_a_lowering(tmp: str) -> None:
    """END TO END: the count fell, nothing was removed, so advise nothing.

    This is the shape that broke the guard. The baseline is above the current
    count -- as it would be after the ceiling rose over references that were
    already there -- and the working tree is identical to its base. Advising a
    lowering here records a sweep that did not happen, and the number recorded
    is only valid at the ceiling this observer happens to be at.
    """
    Path(tmp, "doc.md").write_text(f"a dead one: {ref(9001)}\n")
    write_manifest(tmp, 4)
    commit(tmp, "feat: seed (#100)")

    code, out = run_check(tmp, "HEAD")
    check(code == 0, f"a fall is not itself a failure (exit {code})")
    check(
        "Do NOT lower `issue_references.tree_baseline`" in out,
        "an unattributed fall is reported as NOT ratchetable",
    )
    check(
        "Lower `issue_references.tree_baseline` to" not in out,
        "an unattributed fall does not prompt for a new baseline",
    )
    check(
        "ceiling 100" in out,
        "the report names the ceiling the count was taken at",
    )


def case_a_real_removal_prompts_only_for_what_was_removed(tmp: str) -> None:
    """END TO END: the attributable part is offered, the eroded part is not.

    Three references at base, one deleted here. The baseline is 6, so the raw
    count (2) is four below it -- but only ONE of those four is a removal. The
    advice must offer 5, and must say why 2 is wrong.
    """
    dead = "\n".join(f"line {i}: {ref(9000 + i)}" for i in (1, 2, 3)) + "\n"
    Path(tmp, "doc.md").write_text(dead)
    write_manifest(tmp, 6)
    commit(tmp, "feat: seed (#100)")

    kept = "\n".join(f"line {i}: {ref(9000 + i)}" for i in (1, 2)) + "\n"
    Path(tmp, "doc.md").write_text(kept)

    code, out = run_check(tmp, "HEAD")
    check(code == 0, f"a genuine sweep passes (exit {code})")
    check(
        "1 reference(s) were genuinely removed" in out,
        "the removal is measured and attributed",
    )
    check(
        "Lower `issue_references.tree_baseline` to 5" in out,
        "the advice offers baseline minus the REMOVALS, not the raw count",
    )
    check(
        "Do NOT lower it to 2" in out,
        "the unattributed remainder is called out explicitly",
    )


def case_deleting_a_file_removes_everything_in_it(tmp: str) -> None:
    """A deleted file's references all left the tree, and all count.

    There is deliberately no separate "a rename is not a removal" case. A
    rename cancels out under ANY parse of the diff -- as `R old new`, or as the
    `D old` + `A new` pair git reports without `--find-renames` -- because both
    sides then carry the same references. An assertion that cannot be made to
    fail is not evidence, so the deletion is what gets pinned: it is the one
    status whose base-side blob has no current-side counterpart.
    """
    Path(tmp, "doc.md").write_text(f"{ref(9001)} and {ref(9002)}\n")
    Path(tmp, "kept.md").write_text(f"{ref(9003)}\n")
    write_manifest(tmp, 9)
    commit(tmp, "feat: seed (#100)")
    Path(tmp, "doc.md").unlink()

    removed, comparable = pbcheck.references_removed_since(
        "HEAD", 100, [], [("kept.md", 1, 9003, ref(9003))]
    )
    check(
        comparable and removed == 2,
        f"a deleted file's references are all removals (got removed={removed}, "
        f"comparable={comparable})",
    )


def case_the_two_sides_are_measured_at_one_ceiling(tmp: str) -> None:
    """The property that makes the difference ceiling-independent.

    Same tree, same diff, two very different ceilings: the ABSOLUTE counts
    differ (that is the whole problem), but the difference does not.
    """
    Path(tmp, "doc.md").write_text(f"{ref(9001)} {ref(9002)} {ref(9003)}\n")
    commit(tmp, "feat: seed (#100)")
    Path(tmp, "doc.md").write_text(f"{ref(9001)} {ref(9002)}\n")
    hits = [("doc.md", 1, 9001, ref(9001)), ("doc.md", 1, 9002, ref(9002))]

    low, _ = pbcheck.references_removed_since("HEAD", 100, [], hits)
    high, _ = pbcheck.references_removed_since("HEAD", 9002, [], [])
    check(
        low == 1,
        f"one reference removed, measured at a low ceiling (got {low})",
    )
    check(
        high == 1,
        f"the same one reference removed, measured at a high ceiling (got {high})",
    )


def main() -> int:
    print("publication-boundary ceiling lag and baseline erosion (#1129)")
    for name, body in [
        (
            "merging main does not advance the branch ceiling",
            case_merging_main_does_not_advance_the_branch_ceiling,
        ),
        (
            "the base ref supplies the mainline mark",
            case_the_base_ref_supplies_the_mainline_mark,
        ),
        (
            "the branch mark still wins when it is higher",
            case_the_branch_mark_still_wins_when_it_is_higher,
        ),
        (
            "a lagging branch is not blocked by the ratchet",
            case_a_lagging_branch_is_not_blocked_by_the_ratchet,
        ),
        (
            "an eroded fall does not prompt a lowering",
            case_an_eroded_fall_does_not_prompt_a_lowering,
        ),
        (
            "a real removal prompts only for what was removed",
            case_a_real_removal_prompts_only_for_what_was_removed,
        ),
        ("deleting a file removes everything in it",
         case_deleting_a_file_removes_everything_in_it),
        (
            "the two sides are measured at one ceiling",
            case_the_two_sides_are_measured_at_one_ceiling,
        ),
    ]:
        run_case(name, body)

    if FAILURES:
        print(f"\n\033[31m{len(FAILURES)} failure(s)\033[0m")
        return 1
    print("\n\033[32mall ceiling-lag and erosion cases pass\033[0m")
    return 0


if __name__ == "__main__":
    sys.exit(main())
