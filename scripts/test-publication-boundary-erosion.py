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
  slack: {slack}
  tree_baseline: {baseline}
"""


def write_manifest(tmp: str, baseline: int, slack: int = 0) -> None:
    p = Path(tmp, MANIFEST_REL)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(MANIFEST.format(baseline=baseline, slack=slack))


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


# ── 3. Retired vs crossed: which direction did the number move? (#1080) ──────
#
# Section 2 proves the checker will not RATCHET an unattributed fall. That is
# the safety property, and on its own it leaves the operator with a number that
# fell for a reason nobody can name. This section is the diagnosis: a fall is
# either references RETIRED by an edit (progress) or references CROSSED by the
# rising mark (the opposite of progress -- a dead link that has become a
# confident live link to unrelated work), and the two are named separately.
#
# Why here and not in the shell suite, which is where #1080's acceptance
# criteria proposed them: every case below needs a controlled MARK that MOVES
# between the base and HEAD. The shell suite's sandbox is a detached worktree at
# HEAD with its diff base pinned to HEAD, so the two ceilings are equal by
# construction and no crossing can exist in it. That is the same reason this
# file exists at all, stated in its module docstring.


def _crossed_lines(out: str) -> list[str]:
    return [
        ln.strip()
        for ln in out.splitlines()
        if "crossed (now resolves to unrelated work):" in ln
    ]


def case_a_crossed_reference_is_detected_with_no_file_change(tmp: str) -> None:
    """THE MUTATION: the mark rises past a reference and no file is touched.

    This is #1080 reduced to its smallest form. `doc.md` is byte-identical at
    base and at HEAD. The only thing that happened is that the repository
    issued a number past the reference the file carries -- so the burn-down
    count FELL, and what it measured was the citation turning from a 404 into a
    confident pointer at unrelated live work.

    Nothing here can be fixed by editing this diff, so the report must not gate.
    But it must be a report: the fall is otherwise indistinguishable from a
    sweep retiring a reference, which is precisely the ambiguity that made
    `tree_baseline` unreadable as progress.
    """
    Path(tmp, "doc.md").write_text(f"see {ref(200)} for the rationale\n")
    write_manifest(tmp, 1)
    commit(tmp, "feat: seed (#100)")
    # A later merge issues #300. Nothing else changes.
    commit(tmp, "feat: later (#300)")

    code, out = run_check(tmp, "HEAD~1")
    check(code == 0, f"a crossing is never a failure (exit {code})")
    check(
        "crossed (now resolves to unrelated work): doc.md:1 #200" in out,
        "the crossed reference is named with path, line and number",
    )
    check(
        "Do NOT lower `issue_references.tree_baseline`" in out,
        "the fall it caused is still refused as a ratchet (section 2 holds)",
    )


def case_a_retired_reference_is_reported_as_progress(tmp: str) -> None:
    """The other population: an edit removed it, and that IS progress.

    Same base, same reference, same fall in the count -- and a different cause.
    The mark does not move; the line is deleted. The report must say `retired`
    and must NOT say `crossed`, because a run that calls a genuine sweep an
    erosion is as useless as one that calls an erosion a sweep.
    """
    Path(tmp, "doc.md").write_text(f"see {ref(200)} for the rationale\n")
    write_manifest(tmp, 1)
    commit(tmp, "feat: seed (#100)")
    Path(tmp, "doc.md").write_text("see the rationale\n")

    code, out = run_check(tmp, "HEAD")
    check(code == 0, f"a genuine retirement passes (exit {code})")
    check("retired: doc.md #200" in out, "the retired reference is named")
    check(not _crossed_lines(out), "a retirement is NOT reported as a crossing")
    check(
        "Lower `issue_references.tree_baseline` to 0" in out,
        "and it is the ratchetable kind, so the lowering is offered",
    )


def case_the_ratchet_still_fires_while_a_crossing_is_present(tmp: str) -> None:
    """A crossing never gates -- and never disarms the gate that does.

    The concern with a report that is deliberately toothless is that it becomes
    an excuse: "references leaving the count is expected now". So this pins the
    hard failure alongside it. One reference crosses AND two new dead ones are
    written; the count rises above the baseline and the build fails, exactly as
    it did before this report existed.
    """
    Path(tmp, "doc.md").write_text(f"see {ref(200)}\n")
    write_manifest(tmp, 1)
    commit(tmp, "feat: seed (#100)")
    # One commit that both raises the mark past #200 and TRACKS two fresh dead
    # references. `new.md` has to be tracked: the tree-wide burn-down counts the
    # tracked tree, so an untracked fixture would trip only the diff-scoped rule
    # and this case would pass on the wrong failure.
    Path(tmp, "new.md").write_text(f"{ref(9001)}\n{ref(9002)}\n")
    commit(tmp, "feat: later (#300)")

    code, out = run_check(tmp, "HEAD~1")
    check(code == 1, f"the ratchet still fails the build (exit {code})")
    check(
        "UNRESOLVABLE REFERENCE COUNT ROSE" in out,
        "and it fails for the ratchet's own reason, not the report's",
    )


def case_the_set_is_keyed_by_path_and_number_not_by_line(tmp: str) -> None:
    """AC 5, as behaviour rather than as a file diff.

    #1080 asked for the recorded set to be line-independent so that an
    unrelated edit above a reference does not rewrite it. Nothing is recorded
    here -- the two sides are read from git, for the same reason #1078 deleted
    the recorded mark -- so the property is asserted where it is observable: the
    same reference, displaced by an inserted line, is still ONE finding with the
    SAME (path, number) identity, and only the run-time line moves.
    """
    Path(tmp, "doc.md").write_text(f"see {ref(200)}\n")
    write_manifest(tmp, 1)
    commit(tmp, "feat: seed (#100)")
    commit(tmp, "feat: later (#300)")

    _, before = run_check(tmp, "HEAD~1")
    Path(tmp, "doc.md").write_text(f"\nsee {ref(200)}\n")
    code, after = run_check(tmp, "HEAD~1")

    check(code == 0, f"an unrelated edit above a crossing still passes (exit {code})")
    check(
        _crossed_lines(before)
        == ["crossed (now resolves to unrelated work): doc.md:1 #200"],
        "before the insertion the crossing is reported at line 1",
    )
    check(
        _crossed_lines(after)
        == ["crossed (now resolves to unrelated work): doc.md:2 #200"],
        "after it, the SAME single finding is reported at line 2 -- identity unchanged",
    )
    check(
        "retired: doc.md" not in after,
        "and displacing a line does not fake a retirement",
    )


def case_the_slack_window_is_not_a_crossing(tmp: str) -> None:
    """A reference the ratchet stopped counting has not necessarily crossed.

    `slack` lets a change cite a number the forge has issued but not yet
    merged, so the ratchet stops counting at `mark + slack` while the forge has
    only reached `mark`. A reference in that gap is invisible to the burn-down
    AND still a dead link -- reporting it as "now resolves to unrelated work"
    would be a false statement made confidently, which is the failure mode this
    whole issue is about.

    Not hypothetical: the first cut of the report used the ceiling for both
    edges of the band and named #1201 and #1205 in the live tree. Both 404.
    """
    Path(tmp, "doc.md").write_text(f"see {ref(200)}\n")
    write_manifest(tmp, 1, slack=25)
    commit(tmp, "feat: seed (#100)")
    # mark 190, ceiling 215: #200 is no longer COUNTED, and does not RESOLVE.
    commit(tmp, "feat: later (#190)")

    code, out = run_check(tmp, "HEAD~1")
    check(code == 0, f"the slack window is not a failure (exit {code})")
    check(
        not _crossed_lines(out),
        "a reference between the mark and the ceiling is not called crossed",
    )


def case_a_newly_written_in_range_reference_is_not_a_crossing(tmp: str) -> None:
    """The band's other inhabitant: a citation this change deliberately wrote.

    A number at or below the mark that is NOT in the base blob was written on
    purpose against live work. It is the normal case for every pull request
    that cites an issue, and reporting it would bury the real findings under
    every correct citation in the repository.
    """
    write_manifest(tmp, 0)
    commit(tmp, "feat: seed (#100)")
    commit(tmp, "feat: later (#300)")
    Path(tmp, "doc.md").write_text(f"implements {ref(200)}\n")

    code, out = run_check(tmp, "HEAD~1")
    check(code == 0, f"citing live work passes (exit {code})")
    check(
        not _crossed_lines(out),
        "a reference absent from the base blob is not a crossing",
    )


def case_the_lowering_advice_is_a_fixed_point(tmp: str) -> None:
    """Taking the advice must not produce more advice.

    The advice is `baseline - removed`. If `baseline` is read from the WORKING
    TREE, then lowering the manifest to what a run names and re-running
    subtracts the same `removed` from the number just written -- and the second
    run looks exactly as authoritative as the first. Following it twice records
    a sweep that happened once, which is the erosion AGENTS.md forbids, arrived
    at by obeying the tool rather than by ignoring it.

    Three at base, one deleted, baseline 6: the advice is 5 and must stay 5.
    """
    dead = "\n".join(f"line {i}: {ref(9000 + i)}" for i in (1, 2, 3)) + "\n"
    Path(tmp, "doc.md").write_text(dead)
    write_manifest(tmp, 6)
    commit(tmp, "feat: seed (#100)")
    Path(tmp, "doc.md").write_text(
        "\n".join(f"line {i}: {ref(9000 + i)}" for i in (1, 2)) + "\n"
    )

    _, first = run_check(tmp, "HEAD")
    check(
        "Lower `issue_references.tree_baseline` to 5" in first,
        "the first run names the attributable floor",
    )

    # Take the advice, exactly as an operator would, and ask again.
    write_manifest(tmp, 5)
    code, second = run_check(tmp, "HEAD")
    check(code == 0, f"the tree is still clean after taking the advice (exit {code})")
    check(
        "Lower `issue_references.tree_baseline` to 4" not in second,
        "the second run does not subtract the same removals again",
    )
    check(
        "ALREADY at the attributable floor of 5" in second,
        "it reports the baseline as settled instead of advising another lowering",
    )


def main() -> int:
    print("publication-boundary ceiling lag and baseline erosion (#1129, #1080)")
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
        (
            "deleting a file removes everything in it",
            case_deleting_a_file_removes_everything_in_it,
        ),
        (
            "the two sides are measured at one ceiling",
            case_the_two_sides_are_measured_at_one_ceiling,
        ),
        (
            "a crossed reference is detected with no file change (#1080)",
            case_a_crossed_reference_is_detected_with_no_file_change,
        ),
        (
            "a retired reference is reported as progress (#1080)",
            case_a_retired_reference_is_reported_as_progress,
        ),
        (
            "the ratchet still fires while a crossing is present (#1080)",
            case_the_ratchet_still_fires_while_a_crossing_is_present,
        ),
        (
            "the set is keyed by path and number, not by line (#1080)",
            case_the_set_is_keyed_by_path_and_number_not_by_line,
        ),
        (
            "the slack window is not a crossing (#1080)",
            case_the_slack_window_is_not_a_crossing,
        ),
        (
            "a newly written in-range reference is not a crossing (#1080)",
            case_a_newly_written_in_range_reference_is_not_a_crossing,
        ),
        (
            "the lowering advice is a fixed point (#1080)",
            case_the_lowering_advice_is_a_fixed_point,
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
