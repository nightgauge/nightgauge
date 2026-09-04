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

`ceiling_marks()` then decides WHICH first-parent line to read (#1291). That is
a second, separable question, and getting it wrong is silent: it LOWERS the
ceiling, a lower ceiling leaves more numbers above it, and the same tree
measures a higher unresolvable count. The ratchet then reports a violation that
names nothing the branch did.

── Why a throwaway repository and not the shell suite ───────────────────────

Same reason as `test-publication-boundary-rename.py`: exercising this means
writing commits with controlled subjects, and the shell suite is re-run inside
sandboxes and deliberately SIGKILLed mid-run by the hermeticity suite. A test
that mutates shared history cannot be made crash-safe by being careful.

For the #1291 cases the point is sharper still: they need a branch with a real
merge commit and a lagging first-parent line, which cannot be built in a
worktree of the real repository at all.
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


# ── Which line the ceiling is read from (#1291) ──────────────────────────────

_seq = [0]


def commit_own_file(tmp: str, subject: str) -> None:
    """A commit touching its OWN file, so a merge is never a content conflict.

    The topology is what these cases are about; `commit` above appends to one
    shared file, which would make every merge below a conflict.
    """
    _seq[0] += 1
    name = f"f{_seq[0]}.md"
    Path(tmp, name).write_text(subject + "\n")
    git("add", name, cwd=tmp)
    git("commit", "--quiet", "-m", subject, cwd=tmp)


def sha_of_subject(tmp: str, needle: str) -> str:
    out = subprocess.run(
        ["git", "log", "--format=%H %s", "main"], cwd=tmp,
        capture_output=True, check=True,
    ).stdout.decode()
    return next(line.split()[0] for line in out.splitlines() if needle in line)


def build_merge_updated_branch(tmp: str) -> None:
    """The exact shape that breaks: fork at #110, then merge the mainline in.

    The branch's own commit is what makes the merge a REAL merge rather than a
    fast-forward. A fast-forward would leave HEAD on the mainline and hide the
    defect entirely — which is how a first attempt at reproducing this failed.
    """
    git("branch", "-M", "main", cwd=tmp)
    for m in (100, 110, 120):
        commit_own_file(tmp, f"feat: mainline ({'#'}{m})")

    git("checkout", "--quiet", "-b", "feature", sha_of_subject(tmp, "(#110)"), cwd=tmp)
    commit_own_file(tmp, "chore: branch-local work with no marker")

    git("checkout", "--quiet", "main", cwd=tmp)
    for m in (130, 140):
        commit_own_file(tmp, f"feat: later ({'#'}{m})")

    git("checkout", "--quiet", "feature", cwd=tmp)
    git("merge", "--quiet", "--no-edit", "--no-ff", "main", cwd=tmp)


def case_merge_updated_branch_reads_the_mainline(tmp: str) -> None:
    """THE #1291 case, with the diff base pinned the way the suite pins it.

    `git merge origin/main` puts the mainline on the SECOND parent, so the
    branch's own first-parent line still ends where it forked. `ceiling_marks`
    compensates by also reading the base ref — but the regression suite exports
    `NG_BOUNDARY_DIFF_BASE=HEAD`, so the base ref IS "HEAD", both reads are the
    same lagging line, and the compensation cancels itself out.

    Measured on the real repository before the fix: a branch forked at #1110
    with `origin/main` merged in reported 6572 references against a 5768
    baseline, where the same tree without the pin reported 5295. Force-push and
    rebase are forbidden and merging is the only permitted way to update a
    branch, so such a branch had no way out of the false red.
    """
    build_merge_updated_branch(tmp)

    # Precondition: without it this case could pass while proving nothing.
    lagging = pbcheck.derived_high_water()
    check(lagging == 110,
          f"precondition: the branch's own line lags at {lagging} (want 110)")

    mark, source = pbcheck.ceiling_marks("HEAD")
    check(mark == 140,
          f"a merge-updated branch reads the ceiling off the mainline (got {mark} from {source})")
    check(source != "HEAD", f"...and names where it came from (source={source!r})")


def case_pinned_base_does_not_suppress_the_mainline(tmp: str) -> None:
    """The diff base and the ceiling source are different questions.

    Pinning the base to an older commit is legitimate — it is what a pull
    request against an older base looks like — and it must not drag the ceiling
    back with it.
    """
    build_merge_updated_branch(tmp)
    # The real fork point, NOT `git merge-base HEAD main`: after the merge that
    # resolves to main's own tip, which would make this case pass vacuously.
    fork = sha_of_subject(tmp, "(#110)")
    check(pbcheck.derived_high_water(fork) == 110,
          "precondition: the pinned base's own line ends at 110")

    mark, source = pbcheck.ceiling_marks(fork)
    check(mark == 140,
          f"an older pinned base does not lower the ceiling (got {mark} from {source})")


def case_branch_ahead_of_mainline_keeps_its_own_mark(tmp: str) -> None:
    """`max` must never LOWER the ceiling below what the branch itself proves.

    Without this, "consult the mainline" could be implemented as "use the
    mainline", regressing a branch that legitimately carries a higher mark.
    """
    git("branch", "-M", "main", cwd=tmp)
    commit_own_file(tmp, "feat: mainline (#110)")
    git("checkout", "--quiet", "-b", "feature", cwd=tmp)
    commit_own_file(tmp, "feat: a newer merge landed here (#200)")

    mark, source = pbcheck.ceiling_marks("HEAD")
    check(mark == 200,
          f"a branch ahead of the mainline keeps its own mark (got {mark} from {source})")


def case_missing_mainline_ref_is_not_an_error(tmp: str) -> None:
    """An unfetched mainline contributes nothing rather than raising.

    A detached sandbox or a shallow clone has no `main` to read. That must
    degrade to the branch's own line — and must NOT invent a mark, which is the
    security constraint the whole guard rests on.
    """
    git("branch", "-M", "not-main", cwd=tmp)
    commit_own_file(tmp, "feat: only line (#110)")

    mark, source = pbcheck.ceiling_marks(None)
    check(mark == 110,
          f"no mainline ref falls back to the branch's own line (got {mark} from {source})")


def case_no_marker_anywhere_still_derives_nothing(tmp: str) -> None:
    """Fail-closed, restated for the multi-ref path.

    `case_a_repo_with_no_markers_derives_nothing` covers `derived_high_water`.
    This covers `ceiling_marks`, because consulting MORE refs is exactly the
    change that could turn "cannot tell" into a guess.
    """
    git("branch", "-M", "main", cwd=tmp)
    commit_own_file(tmp, "chore: still no marker")

    mark, _ = pbcheck.ceiling_marks("HEAD")
    check(mark == 0,
          f"more refs consulted, still no invented mark (got {mark})")


def case_a_non_mainline_base_ref_supplies_the_mark(tmp: str) -> None:
    """Pins the BASE-REF arm, isolated from the hardcoded mainline entries.

    Every other #1291 case names a base ref that is already in
    CEILING_MAINLINE_REFS ("HEAD" is seeded, "main" is an entry), so the
    `not in candidates` guard skips the base-ref arm entirely and the mainline
    entry satisfies the assertion. Deleting the arm therefore left every suite
    green — an arm no test could kill.

    A pull request against a release branch or a stacked base is the shape that
    actually reaches it, and it is the shape CI never runs.
    """
    commit_own_file(tmp, "feat: shared root (#100)")
    git("branch", "-M", "main", cwd=tmp)
    git("checkout", "--quiet", "-b", "release/1.0", cwd=tmp)
    git("checkout", "--quiet", "-b", "feature", cwd=tmp)
    commit_own_file(tmp, "chore: branch-local work with no marker")
    git("checkout", "--quiet", "release/1.0", cwd=tmp)
    commit_own_file(tmp, "feat: landed on the release line (#400)")
    git("checkout", "--quiet", "feature", cwd=tmp)

    check(pbcheck.derived_high_water() == 100,
          "precondition: the branch's own line lags at 100")
    check(pbcheck.derived_high_water("main") == 100,
          "precondition: the MAINLINE is not the source of 400, so only the base ref can supply it")

    mark, source = pbcheck.ceiling_marks("release/1.0")
    check((mark, source) == (400, "release/1.0"),
          f"a non-mainline base ref supplies the mark (got {mark} from {source}, want 400 from release/1.0)")


def case_the_remote_tracking_mainline_is_consulted(tmp: str) -> None:
    """Pins `origin/main` specifically — the entry that matters in CI.

    No fixture in any boundary suite creates a git remote, so every existing
    case is satisfied by the LOCAL `main` entry and reducing
    CEILING_MAINLINE_REFS to ("main",) left them all green. That is backwards
    from production: publication-boundary.yml checks out with fetch-depth 0 on
    pull_request, which produces a remote-tracking `origin/main` and NO local
    `main`. So the one entry the real gate depends on was the one nothing
    exercised.
    """
    commit_own_file(tmp, "feat: shared root (#100)")
    git("branch", "-M", "main", cwd=tmp)

    # A real remote whose main carries a higher mark, then a branch that lags.
    upstream = os.path.join(tmp, "upstream.git")
    git("init", "--quiet", "--bare", upstream, cwd=tmp)
    git("remote", "add", "origin", upstream, cwd=tmp)
    commit_own_file(tmp, "feat: landed upstream (#500)")
    git("push", "--quiet", "origin", "main", cwd=tmp)

    git("checkout", "--quiet", "-b", "feature", "HEAD~1", cwd=tmp)
    commit_own_file(tmp, "chore: branch-local work with no marker")
    # Delete the LOCAL main so only origin/main can supply the mark — exactly
    # the ref shape actions/checkout produces on a pull_request run.
    git("branch", "--quiet", "-D", "main", cwd=tmp)

    check(pbcheck.derived_high_water() == 100,
          "precondition: the branch's own line lags at 100")
    check(not pbcheck._rev_ok("main"),
          "precondition: there is no local main, as on a CI pull_request checkout")

    mark, source = pbcheck.ceiling_marks("HEAD")
    check((mark, source) == (500, "origin/main"),
          f"the remote-tracking mainline supplies the mark (got {mark} from {source}, want 500 from origin/main)")


def case_a_pinned_base_reports_no_false_crossing(tmp: str) -> None:
    """The crossing BAND must be measured the same way as the ceiling (#1291).

    Making `ceiling` mainline-aware while `base_ceiling` stayed on the bare
    `derived_high_water(base)` split one comparison across two definitions, and
    the guard then manufactured a crossing out of nothing: with the diff base
    pinned to HEAD there is no commit between `base` and now, and the band still
    read 100 -> 300. The report is confidently false, which is worse than a
    missed one.
    """
    commit_own_file(tmp, "feat: seed (#100)")
    git("branch", "-M", "main", cwd=tmp)
    git("checkout", "--quiet", "-b", "feature", cwd=tmp)
    commit_own_file(tmp, "chore: branch-local work with no marker")
    git("checkout", "--quiet", "main", cwd=tmp)
    commit_own_file(tmp, "feat: later (#300)")
    git("checkout", "--quiet", "feature", cwd=tmp)
    git("merge", "--quiet", "--no-edit", "--no-ff", "main", cwd=tmp)

    ceiling, _ = pbcheck.ceiling_marks("HEAD")
    base = subprocess.run(["git", "rev-parse", "HEAD"], cwd=tmp,
                          capture_output=True, check=True).stdout.decode().strip()

    # Drives the SHIPPED floor, not a copy of its arithmetic. The first version
    # of this case recomputed the merge-base walk here, so reverting the fix
    # left it green — a mirror, not a guard.
    floor = pbcheck.base_band_floor(base, 0, ceiling)

    check(floor >= ceiling,
          f"the band is empty when base IS head (floor {floor}, ceiling {ceiling}) — "
          "a non-empty band here is a crossing report for commits that do not exist")


def main() -> int:
    print("publication-boundary derived ceiling (#1078, #1291)")
    for name, body in [
        ("reads the squash marker", case_reads_the_squash_marker),
        ("crafted title cannot raise the ceiling", case_crafted_title_cannot_raise_the_ceiling),
        ("unmarked subjects are ignored", case_unmarked_subjects_are_ignored),
        ("trailing whitespace still counts", case_trailing_whitespace_still_counts),
        ("no markers derives nothing", case_a_repo_with_no_markers_derives_nothing),
        ("shallow clone is detected", case_shallow_clone_is_detected),
        # #1291 — which line the ceiling is read from.
        ("merge-updated branch reads the mainline", case_merge_updated_branch_reads_the_mainline),
        ("a pinned diff base does not suppress the mainline", case_pinned_base_does_not_suppress_the_mainline),
        ("a branch ahead of the mainline keeps its own mark", case_branch_ahead_of_mainline_keeps_its_own_mark),
        ("a missing mainline ref is not an error", case_missing_mainline_ref_is_not_an_error),
        ("no marker anywhere still derives nothing", case_no_marker_anywhere_still_derives_nothing),
        ("a non-mainline base ref supplies the mark", case_a_non_mainline_base_ref_supplies_the_mark),
        ("the remote-tracking mainline is consulted", case_the_remote_tracking_mainline_is_consulted),
        ("a pinned base reports no false crossing", case_a_pinned_base_reports_no_false_crossing),
    ]:
        run_case(name, body)

    if FAILURES:
        print(f"\n\033[31m{len(FAILURES)} failure(s)\033[0m")
        return 1
    print("\n\033[32mall derived-ceiling cases pass\033[0m")
    return 0


if __name__ == "__main__":
    sys.exit(main())
