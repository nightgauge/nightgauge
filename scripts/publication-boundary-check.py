#!/usr/bin/env python3
"""Publication boundary guard for nightgauge/nightgauge.

Enforces .github/publication-boundary.yaml against the tracked tree PLUS the
untracked, non-ignored files git would add next (#716).

FAIL-CLOSED. Every exit path that is not an explicit, fully-verified pass is a
failure:

  * manifest missing, unreadable, or malformed  -> FAIL (never "skip")
  * a path matching no `allow` rule             -> FAIL (not warn, not ignore)
  * a path matching a `deny` rule               -> FAIL
  * forbidden content outside its allow_paths   -> FAIL
  * a NEWLY ADDED line citing an issue number
    above the repository's high-water mark      -> FAIL
  * a non-empty `needs_decision` bucket         -> FAIL
  * an unexpected exception anywhere            -> FAIL

The failure mode that matters is the one where the guard cannot tell. A guard
that passes when it cannot tell is worse than no guard, because it manufactures
confidence -- which is exactly how the previous control (a written rule in
nightgauge-internal/CLAUDE.md, plus a local-only .git/info/exclude line) failed.

THE REFERENCE CEILING IS PER-BRANCH, AND `git merge origin/main` DOES NOT RAISE
IT (#1129). It is derived from the trailing `(#N)` of squash-merge subjects on
FIRST-PARENT history, and a merge puts `main` on the second parent -- so a
branch's own line stops at the commit it forked from. Since AGENTS.md forbids
force-push and rebase, merging is the only permitted update path, and it is the
one that does not help. The guard therefore takes the LARGER of the branch's
mark and its base ref's (`ceiling_marks`), which needs the base ref fetched. A
count taken at a lower ceiling is larger, and `tree_baseline` is one global
integer, so a count is only ever comparable against the ceiling it was taken at
-- which is why the burn-down report names that ceiling and refuses to advise a
lowering it cannot attribute to references actually removed.

Exit codes:
  0  clean
  1  boundary violation(s)
  2  the guard itself could not run (treated as failure by CI)
"""

from __future__ import annotations

import bisect
import functools
import hashlib
import locale
import os
import re
import subprocess
import sys
from concurrent.futures import ProcessPoolExecutor
from pathlib import Path

MANIFEST = Path(".github/publication-boundary.yaml")


def die(code: int, msg: str) -> None:
    print(f"\n\033[31mpublication-boundary: {msg}\033[0m", file=sys.stderr)
    sys.exit(code)


def tracked_paths() -> list[str]:
    out = subprocess.run(
        ["git", "ls-files", "-z"], capture_output=True, check=True
    ).stdout.decode()
    return [p for p in out.split("\0") if p]


def untracked_paths() -> list[str]:
    """Files git would add next: untracked, and not excluded by .gitignore.

    The guard used to read `git ls-files` alone, which meant the newest content
    in a change -- the files the author has just written and not yet staged --
    was the one thing it never examined (#716). It printed an unqualified
    "clean" and CI then failed on exactly that content, which is the
    vacuous-pass shape this guard exists to prevent.

    `--exclude-standard` applies .gitignore/.git/info/exclude, so build output
    and scratch space stay out of scope; the target is untracked source that is
    on its way into a commit.
    """
    out = subprocess.run(
        ["git", "ls-files", "-z", "--others", "--exclude-standard"],
        capture_output=True,
        check=True,
    ).stdout.decode()
    return [p for p in out.split("\0") if p]


def matches(path: str, pattern: str) -> bool:
    """Glob match where '**' spans directory separators.

    fnmatch alone treats '*' as crossing '/', which would make "docs/*" match
    "docs/a/b.md". We anchor a real regex instead so that a rule means what it
    looks like it means.
    """
    rx = re.escape(pattern)
    rx = rx.replace(r"\*\*/", "(?:.*/)?")  # 'a/**/b' -> optional intermediate dirs
    rx = rx.replace(r"\*\*", ".*")  # 'a/**'   -> anything below
    rx = rx.replace(r"\*", "[^/]*")  # 'a/*'    -> one segment only
    rx = rx.replace(r"\?", "[^/]")
    return re.fullmatch(rx, path) is not None


def _stem_prefixes(token):
    """Yield the lowercased leading segments of a hyphen/dot-delimited token.

    "acmeapp-flutter" -> "acmeapp-flutter", "acmeapp". A private workspace
    identifier is denied once and every repository built on that stem is then
    denied for free, so the manifest never has to enumerate them — including
    the suffixes nobody has coined yet.
    """
    tok = token.lower()
    yield tok
    for i, ch in enumerate(tok):
        if ch in "-." and i:
            yield tok[:i]


def _line_has_denied_token(line, word, salt, token_hashes, memo):
    """True if any token on this line (or its stem) hashes into the denylist.

    `memo` caches token -> verdict. The denylist is stored as salted SHA-256 so
    a PUBLIC manifest never spells out the private names in plaintext; that
    property is untouched here — same salt, same digests, same set membership,
    just hashed once per DISTINCT token instead of once per occurrence.

    Source text repeats itself enormously: on this tree the scan tokenises
    ~1.38M lines into ~11.5M stem candidates and hashed every one, for a few
    tens of thousands of distinct tokens. Profiling put 58% of this checker's
    runtime in this function, 6.3M of the calls in sha256 alone. The checker
    runs ~153 times per CI job (once per suite assertion, and the suite itself
    runs 4x inside the hermeticity harness), so this constant factor reaches
    the wall clock multiplied by two orders of magnitude. See #850.

    The cache is a PARAMETER, not module state, so its lifetime is exactly the
    lifetime of the (salt, token_hashes) pair it was built for. A second
    denylist with a different salt would get its own cache by construction
    rather than silently inheriting verdicts computed against the first — the
    failure a module-global memo would make invisible.
    """
    for tok in word.findall(line):
        denied = memo.get(tok)
        if denied is None:
            denied = False
            for cand in _stem_prefixes(tok):
                if hashlib.sha256((salt + cand).encode()).hexdigest() in token_hashes:
                    denied = True
                    break
            memo[tok] = denied
        if denied:
            return True
    return False


# ── Unresolvable issue references (#673) ─────────────────────────────────────
# A "#N" above the number this repository has actually issued cannot resolve at
# the moment it is written. That is a definition, not a heuristic: it holds at
# every repository size, which is why the rule anchors on the high-water mark
# recorded in the manifest rather than on digit count. Digit count is the wrong
# axis in both directions -- 951 distinct unresolvable numbers here are below
# 1000, and #000000 / &#8635; are six digits and are not references at all.
#
# The scan is DIFF-SCOPED: only lines this working tree ADDS over its base are
# considered. The rule is "no NEWLY INTRODUCED unresolvable reference", so a
# diff is the honest scope. The tree's ~6,800 pre-existing occurrences are a
# separate, mechanical sweep; gating on them here would mean shipping the sweep
# before the guard, which is the ordering that lets the queue keep growing.

# A bare "#N", or one qualified with this repository's own name. An
# "owner/repo#N" for any OTHER repository is deliberately not matched: those
# numbers belong to a sequence this manifest knows nothing about, and the
# private ones are already covered by `private-repository-issue-reference`.
ISSUE_REF = re.compile(
    r"(?<![0-9A-Za-z_/&#-])(?:nightgauge/nightgauge|nightgauge)?#([0-9]+)"
)
HEX_RUN = re.compile(r"[0-9a-fA-F]+")

# Widths a CSS/SVG hex colour can take that this repository cannot produce as an
# issue number: "#252526", "#101830", "#12345678". Three- and four-digit colours
# are NOT excluded -- those collide with real issue numbers, and an all-digit
# three-digit colour is below the mark anyway. Remove this once the repository
# is anywhere near 100,000 issues.
HEX_COLOR_WIDTHS = frozenset({6, 8})


def _unresolvable_in_text(text: str, ceiling: int):
    """Yield (number, matched_text, start_offset) for each unresolvable #N.

    The single implementation of the rule. `unresolvable_refs` (line-scoped) and
    the tree-wide burn-down (file-scoped) both route through it, so the
    hex-colour exclusions cannot drift apart between the two scopes -- which is
    the whole reason this is one function and not two.
    """
    for m in ISSUE_REF.finditer(text):
        digits = m.group(1)
        # "#5865f2" -> ISSUE_REF sees "#5865". A reference is never followed by
        # a hex letter, so a longer hex run means this is a colour.
        run = HEX_RUN.match(text, m.start(1))
        if run and run.end() > m.end(1):
            continue
        if len(digits) in HEX_COLOR_WIDTHS:
            continue
        # GitHub never issues a zero-padded number, so "#000000" and the
        # "#0000" head of "#0000ff" are not references.
        if len(digits) > 1 and digits[0] == "0":
            continue
        n = int(digits)
        if n > ceiling:
            yield n, m.group(0), m.start()


def unresolvable_refs(line: str, ceiling: int):
    """Yield (number, matched_text) for each #N on `line` that cannot resolve."""
    for n, token, _ in _unresolvable_in_text(line, ceiling):
        yield n, token


def _file_unresolvable_numbers(text: str, ceiling: int) -> set[int]:
    """Every unresolvable #N appearing anywhere in `text`."""
    out: set[int] = set()
    for line in text.splitlines():
        for num, _ in unresolvable_refs(line, ceiling):
            out.add(num)
    return out


@functools.lru_cache(maxsize=None)
def rename_map(base: str) -> dict[str, str]:
    """new path -> old path, for every file this tree RENAMED since `base`.

    Without this, `base_file_numbers` asks `git show base:<new path>`, which
    cannot resolve for a file that was moved, and the function falls through to
    its "genuinely new file" answer. The whole file's pre-existing references
    are then charged to whoever moved it (#837).

    That is not a rounding error. This repository carries thousands of
    unresolvable references spread over most of its files, so the expected cost
    of a rename is several dead references per file moved, charged to a change
    that did not write any of them.

    Fails OPEN to an empty map on any git error, which restores exactly the
    previous behaviour: renames stop carrying over, nothing is wrongly
    exempted. This helper only ever ADDS carry-over, so a failure here cannot
    let a genuinely new reference through.
    """
    try:
        out = subprocess.run(
            [
                "git",
                "diff",
                "--no-color",
                "--no-ext-diff",
                "--find-renames",
                "--name-status",
                "--diff-filter=R",
                base,
            ],
            capture_output=True,
            check=True,
        ).stdout.decode(errors="ignore")
    except Exception:
        return {}
    renames: dict[str, str] = {}
    for raw in out.splitlines():
        # R<score>\told\tnew -- the score suffix varies, the R does not.
        parts = raw.split("\t")
        if len(parts) >= 3 and parts[0].startswith("R"):
            renames[parts[2]] = parts[1]
    return renames


def base_file_numbers(base: str, path: str, ceiling: int) -> set[int]:
    """Unresolvable #N present in `path` AS OF `base`.

    This is what separates "you wrote a dead reference" from "you edited a line
    that already carried one". Touching a doc-comment whose text contains a
    pre-existing dead number is not the introduction of a dead number, and
    failing the build for it turns 9,003 pre-existing references into a toll on
    every future edit that happens to land near one (#822 paid it).

    A file that did not exist at `base` yields the empty set, so every reference
    in a genuinely new file is genuinely new.

    A RENAMED file is not a new file, and the lookup follows the rename to its
    pre-rename path before giving up (#837).

    The rename map is consulted only AFTER the direct lookup fails. That order
    is for readability, not behaviour: git does not report an overwriting move
    as a rename (replacing A with B reports `M A` + `D B`, verified), so a
    rename DESTINATION can never be a path that also exists at `base`, and the
    two orderings cannot disagree. Do not write a test claiming otherwise --
    one was tried, and it could not be made to fail against the other order.
    """
    r = subprocess.run(["git", "show", f"{base}:{path}"], capture_output=True)
    if r.returncode != 0:
        old = rename_map(base).get(path)
        if old is None:
            return set()
        r = subprocess.run(["git", "show", f"{base}:{old}"], capture_output=True)
        if r.returncode != 0:
            return set()
    try:
        return _file_unresolvable_numbers(r.stdout.decode("utf-8", "replace"), ceiling)
    except Exception:
        return set()


# ── The scan worker (#850) ───────────────────────────────────────────────────
#
# Module level and configured through an initializer so the parent can run it
# either in-process or across a process pool. Everything it returns is plain
# data -- no formatted strings -- because the report's wording depends on
# `untracked_note`, which is a closure over the parent's path sets.
_SCAN: dict = {}


def _scan_init(
    rule_specs,
    token_hashes,
    salt,
    token_allow,
    manifest_path,
    ref_exempt,
    ceiling,
    enc,
    tracked,
    band_floor=None,
    band_top=None,
):
    """Compile per-worker state once, not once per chunk."""
    global _SCAN
    _SCAN = {
        # Patterns are shipped as SOURCE and compiled here: a compiled pattern
        # survives pickling, but sending the source keeps the worker's regex
        # flags explicit and identical to the serial path.
        "rules": [
            (rid, re.compile(src, re.IGNORECASE), exempt)
            for rid, src, exempt in rule_specs
        ],
        "token_hashes": token_hashes,
        "salt": salt,
        "token_allow": token_allow,
        "manifest": manifest_path,
        "ref_exempt": ref_exempt,
        "ceiling": ceiling,
        # THE CROSSING BAND, `(band_floor, band_top]` (#1080).
        #
        # `band_floor` is the CEILING the diff base was measured at: above it is
        # what the burn-down counted then. `band_top` is the MARK, which is the
        # largest number this repository has actually issued: at or below it is
        # what resolves now.
        #
        # The two edges are deliberately not the same quantity. Using the
        # ceiling for both would sweep in the slack window -- numbers between
        # the mark and the ceiling, which the ratchet stops counting but which
        # the forge has not issued yet. Those are still dead links, so calling
        # them "now resolves to unrelated work" is a false report, and it is a
        # confident one: checked against the live forge, #1201 and #1205 sat in
        # that window and 404 to this day.
        #
        # `None`/equal edges mean the mark did not move and the band is empty,
        # so the worker scans exactly as it did before -- same reads, same hits.
        "band_floor": ceiling if band_floor is None else min(band_floor, ceiling),
        "band_top": 0 if band_top is None else band_top,
        "enc": enc,
        "tracked": tracked,
        "word": re.compile(r"[A-Za-z0-9_.-]+"),
        # Per-worker memo. Each worker sees a slice of the tree, and source
        # tokens repeat inside any slice, so the cache still pays for itself.
        "memo": {},
    }


def _scan_chunk(chunk):
    """Scan a contiguous slice of paths. Returns plain tuples, in path order."""
    c = _SCAN
    rule_hits = []
    token_hits = []
    tree_hits = []
    band_hits = []
    for p in chunk:
        active = [r for r in c["rules"] if not any(matches(p, e) for e in r[2])]
        want_tokens = bool(c["token_hashes"]) and not (
            p == c["manifest"]
            or p == "scripts/publication-boundary-check.py"
            or p in c["token_allow"]
        )
        want_tree = p in c["tracked"] and not any(
            matches(p, e) for e in c["ref_exempt"]
        )
        if not active and not want_tokens and not want_tree:
            continue
        try:
            raw = Path(p).read_bytes()
        except OSError:
            continue  # unreadable; every rule skipped it before, too

        if active or want_tokens:
            lines = raw.decode(c["enc"], errors="ignore").splitlines()
            for rid, pattern, _exempt in active:
                for n, line in enumerate(lines, 1):
                    if pattern.search(line):
                        rule_hits.append((rid, p, n, line.strip()[:100]))
                        break  # one hit per file is enough to fail it
            if want_tokens:
                for n, line in enumerate(lines, 1):
                    if _line_has_denied_token(
                        line, c["word"], c["salt"], c["token_hashes"], c["memo"]
                    ):
                        token_hits.append((p, n))
                        break  # one hit per file is enough to fail it

        if want_tree:
            try:
                strict = raw.decode("utf-8")
            except UnicodeDecodeError:
                continue  # binary or unreadable: not prose, not a citation
            if "#" not in strict:
                continue  # cheap reject before the line-by-line walk
            # Scanned at the BAND FLOOR, which is <= the ceiling, so one pass
            # yields both populations: above the ceiling is the burn-down, and
            # inside the band is a crossing candidate (#1080). When the ceiling
            # did not move, band_floor == ceiling and this is the old scan
            # exactly -- same reads, same hits, no extra cost.
            hits = list(_unresolvable_in_text(strict, c["band_floor"]))
            if not hits:
                continue
            starts = [0]
            for i, ch in enumerate(strict):
                if ch == "\n":
                    starts.append(i + 1)
            for num, token, pos in hits:
                line = bisect.bisect_right(starts, pos)
                if num > c["ceiling"]:
                    tree_hits.append((p, line, num, token))
                elif num <= c["band_top"]:
                    band_hits.append((p, line, num, token))
                # else: the slack window. Not counted by the ratchet, not
                # issued by the forge either -- neither population.
    return rule_hits, token_hits, tree_hits, band_hits


def _scan_jobs(path_count: int) -> int:
    """How many worker processes to use. 1 means "run in this process"."""
    override = os.environ.get("NG_BOUNDARY_JOBS")
    if override:
        try:
            n = int(override)
        except ValueError:
            die(
                2,
                f"NG_BOUNDARY_JOBS must be an integer, got {override!r}. Failing closed.",
            )
        return max(1, n)
    # Below this the pool costs more than it saves, and the suite runs this
    # guard dozens of times in a row.
    if path_count < 500:
        return 1
    return max(1, min(os.cpu_count() or 1, 8))


# `tree_unresolvable_refs` used to live here, walking the whole tree itself.
# The tree-wide burn-down is now collected in main()'s single pass, which reads
# each file once for every rule instead of once per rule (#850).


def _rev_ok(ref: str) -> bool:
    return (
        subprocess.run(
            ["git", "rev-parse", "--verify", "--quiet", f"{ref}^{{commit}}"],
            capture_output=True,
        ).returncode
        == 0
    )


def resolve_diff_base() -> tuple[str, str] | tuple[None, None]:
    """Resolve the commit this working tree is measured against.

    NG_BOUNDARY_DIFF_BASE overrides everything (the regression suite pins it so
    its cases do not depend on branch topology). Otherwise: the pull request's
    base branch, the commit a push replaced, then the default branch.
    """
    candidates: list[str] = []
    override = os.environ.get("NG_BOUNDARY_DIFF_BASE")
    if override:
        # An explicit base that does not resolve is an error, not an invitation
        # to quietly measure against something else.
        if not _rev_ok(override):
            die(
                2,
                f"NG_BOUNDARY_DIFF_BASE={override!r} does not resolve to a commit. "
                "Failing closed rather than silently diffing against a different base.",
            )
        candidates.append(override)
    base_ref = os.environ.get("GITHUB_BASE_REF")
    if base_ref:
        candidates += [f"origin/{base_ref}", base_ref]
    if os.environ.get("GITHUB_EVENT_NAME") == "push":
        candidates.append("HEAD^")
    candidates += ["origin/main", "main"]

    for cand in candidates:
        if not _rev_ok(cand):
            continue
        found = subprocess.run(["git", "merge-base", cand, "HEAD"], capture_output=True)
        if found.returncode == 0 and found.stdout.strip():
            return cand, found.stdout.decode().strip()
    return None, None


def added_lines(base: str):
    """Yield (path, lineno, text) for every line this tree ADDS over `base`.

    `git diff <commit>` compares the commit against the WORKING TREE, so staged
    and unstaged edits count. That matters: the regression suite plants its
    violations with `git add` and never commits them.
    """
    out = subprocess.run(
        [
            "git",
            "diff",
            "--no-color",
            "--no-ext-diff",
            "--unified=0",
            "--diff-filter=ACMR",
            base,
        ],
        capture_output=True,
        check=True,
    ).stdout.decode(errors="ignore")

    path: str | None = None
    lineno = 0
    for raw in out.splitlines():
        if raw.startswith("+++ "):
            target = raw[4:]
            path = (
                None
                if target == "/dev/null"
                else target[2:]
                if target.startswith("b/")
                else target
            )
        elif raw.startswith("@@"):
            hunk = re.match(r"@@ -\S+ \+(\d+)", raw)
            lineno = int(hunk.group(1)) if hunk else 0
        elif raw.startswith("+"):
            if path is not None:
                yield path, lineno, raw[1:]
            lineno += 1


def untracked_added_lines(paths):
    """Yield (path, lineno, text) for every line of an untracked file.

    A file that is not in the index appears in no diff, so `added_lines` cannot
    see it. Every one of its lines is nevertheless a line this change adds --
    that is what "new file" means -- so the diff-scoped rules get them here.
    """
    for p in paths:
        try:
            text = Path(p).read_text(errors="ignore")
        except (OSError, UnicodeDecodeError):
            continue  # binary or unreadable; line rules are text-only
        for n, line in enumerate(text.splitlines(), 1):
            yield p, n, line


def derived_high_water(rev: str = "HEAD") -> int:
    """The high-water mark on `rev`'s own first-parent line.

    GitHub APPENDS `(#N)` to the subject when it squash-merges a pull request,
    so the TRAILING marker on a first-parent subject is the number the forge
    issued. This is the authority for the ceiling -- there is no recorded mark
    to bump and therefore none to go stale (#1078).

    Two properties make this safe inside a fail-closed guard:

    * OFFLINE. No network call. `git log` on a full clone answers it, so the
      guard cannot be turned red by a third party being unreachable -- the
      failure mode that #1004 is about.
    * NOT AUTHOR-CONTROLLED. Only the trailing marker counts. A pull request
      titled `feat: thing (#99999)` squash-merges to
      `feat: thing (#99999) (#1080)`, and this reads 1080. Anchoring to the end
      of the subject is what keeps a crafted title from raising the ceiling and
      silently weakening the rule; an unanchored search would take the bait.

    MERGING `main` INTO A BRANCH DOES NOT RAISE THIS (#1129). `--first-parent`
    is what makes the second property hold -- it stays on the mainline and
    never descends into a merged-in branch, whose subjects the author writes
    freely. The cost is that `git merge origin/main`, which AGENTS.md makes the
    ONLY permitted way to update a branch (no force-push, no rebase), puts
    `main` on the SECOND parent. So a branch's own line still ends at the
    commit it forked from, and the mark derived here goes staler the longer the
    branch lives. `main` at `(#1125)` was read as 1110 on a branch that had
    already merged it twice.

    The caller therefore derives this on BOTH the branch and its base ref and
    takes the larger; see `ceiling_marks`.
    """
    out = subprocess.run(
        ["git", "log", "--first-parent", "--format=%s", rev],
        capture_output=True,
        check=True,
    ).stdout.decode(errors="ignore")
    trailing = (re.search(r"\(#(\d+)\)\s*$", line) for line in out.splitlines())
    return max((int(m.group(1)) for m in trailing if m), default=0)


def ceiling_marks(base_ref: str | None) -> tuple[int, str]:
    """The mark to use, and the ref it came from: the LARGER of two lines.

    Why alignment rather than a ceiling-independent count (#1129). The
    tree-wide count is a function of the ceiling -- a LOWER ceiling leaves more
    numbers above it and so measures MORE -- while the ratchet it is compared
    against is one global integer in the manifest. Two ways out:

      (a) pin the ceiling the baseline was measured at, in the manifest, and
          always count against that. This makes the count ceiling-independent,
          and it re-creates exactly the hand-maintained integer #1078 deleted:
          it has to be bumped, it goes stale in silence, and while it is stale
          the guard is measuring against a ceiling the repository has long
          passed.
      (b) stop the measuring branch's ceiling from lagging in the first place.

    (b) is simpler, needs no new knob, and is free: `resolve_diff_base` has
    already found the base ref, whose first-parent line is the same
    forge-controlled mainline the branch's line diverged from. Reading the mark
    there costs one extra `git log` and removes the lag entirely for any branch
    whose base ref is fetched -- which is every branch CI can diff at all,
    since the diff-scoped rules need that same ref.

    `max` is deliberate. This can only ever RAISE the ceiling toward the
    mainline's, never lower it below what the branch itself proves was issued,
    and both inputs are first-parent lines, so the anti-crafted-title property
    of `derived_high_water` is preserved on both.
    """
    mark, source = derived_high_water(), "HEAD"
    if base_ref:
        base_mark = derived_high_water(base_ref)
        if base_mark > mark:
            mark, source = base_mark, base_ref
    return mark, source


def _file_unresolvable_count(text: str, ceiling: int) -> int:
    """Occurrences (not distinct numbers) of unresolvable #N in `text`.

    The tree-wide burn-down counts occurrences, so an attribution that compares
    against it must count the same thing -- `_file_unresolvable_numbers`
    de-duplicates and would under-count a file citing one dead number twice.
    """
    return sum(1 for _ in _unresolvable_in_text(text, ceiling))


def _changed_sides(base):
    """`(base_side, cur_side, ok)` -- the paths a diff against `base` touches.

    `base_side` is every path whose BASE blob exists (so it can be read at
    `base`); `cur_side` is every path that still exists here. An untouched file
    is in neither, which is what makes both callers below read a handful of
    blobs rather than the tree: it counts identically on both sides and cannot
    contribute to a difference.

    Extracted so the count (`references_removed_since`) and the set
    (`retired_reference_set`) walk the diff the SAME way. Two independent walks
    of the same `-z` stream is how a rename gets parsed as a removal on one path
    and not the other, and the two numbers then disagree with no way to tell
    which is right.
    """
    r = subprocess.run(
        [
            "git",
            "diff",
            "--no-color",
            "--no-ext-diff",
            "--name-status",
            "-z",
            "--find-renames",
            base,
        ],
        capture_output=True,
    )
    if r.returncode != 0:
        return [], set(), False
    fields = r.stdout.decode("utf-8", "replace").split("\0")
    base_side: list[str] = []
    cur_side: set[str] = set()
    try:
        i = 0
        while i < len(fields):
            status = fields[i]
            if not status:
                i += 1
                continue
            if status[0] in ("R", "C"):
                old, new = fields[i + 1], fields[i + 2]
                i += 3
                # A copy leaves its source in place; only a rename moves it.
                if status[0] == "R":
                    base_side.append(old)
                cur_side.add(new)
            else:
                path = fields[i + 1]
                i += 2
                if status[0] != "A":
                    base_side.append(path)
                if status[0] != "D":
                    cur_side.add(path)
    except IndexError:
        return [], set(), False  # truncated -z record: refuse to guess
    return base_side, cur_side, True


def _blob_text(base, path):
    """The utf-8 text of `path` at `base`, or None when there is none to read."""
    blob = subprocess.run(["git", "show", f"{base}:{path}"], capture_output=True)
    if blob.returncode != 0:
        return None
    try:
        return blob.stdout.decode("utf-8")
    except UnicodeDecodeError:
        return None  # binary: the tree-wide scan skips these too


def baseline_at(base, fallback):
    """`issue_references.tree_baseline` as recorded at `base`, or `fallback`.

    The advice below is `baseline - removed`, and it has to be measured from the
    baseline the BASE COMMIT recorded, not the one in this working tree.

    Both operands are otherwise measured against different points and the advice
    stops being idempotent: lower the manifest to what a run names, re-run, and
    the next run subtracts the SAME `removed` again from the number just
    written. Following it twice records a sweep that happened once, which is the
    precise thing AGENTS.md forbids -- and it is an easy mistake to make,
    because the second run looks exactly as authoritative as the first.

    Reading it from `base` makes the advice a fixed point: the same branch
    against the same base names the same number however often it is run.
    """
    text = _blob_text(base, str(MANIFEST))
    if text is None:
        return fallback
    try:
        import yaml  # noqa: PLC0415

        recorded = (yaml.safe_load(text) or {}).get("issue_references", {})
    except Exception:  # noqa: BLE001 — a manifest we cannot parse is not advice
        return fallback
    value = recorded.get("tree_baseline") if isinstance(recorded, dict) else None
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        return fallback
    return value


def retired_reference_set(base, ceiling, ref_exempt, tree_hits):
    """`(path, number)` pairs that left the tree since `base` -- the SET (#1080).

    The companion to `references_removed_since`, which answers the same question
    as a count. Both are needed and neither substitutes for the other: the
    ratchet advice needs OCCURRENCES (a file citing one dead number twice owes
    two), while the report needs identities (naming the same number twice is
    noise).

    KEYED BY (path, number), NEVER BY LINE. A line-keyed set is rewritten by
    every unrelated edit above a reference, so the diff of the set becomes
    unreadable and the retired-vs-crossed classification turns to noise for
    reasons that have nothing to do with references. The line is a run-time
    display detail, and `tree_hits` carries it for the references that are still
    here; a retired reference has no current line to report, which is why the
    format below names only the path.
    """
    base_side, cur_side, comparable = _changed_sides(base)
    if not comparable:
        return [], False
    now = {(h[0], h[2]) for h in tree_hits}
    retired: set[tuple[str, int]] = set()
    for path in base_side:
        if any(matches(path, e) for e in ref_exempt):
            continue
        text = _blob_text(base, path)
        if text is None:
            continue
        for num in _file_unresolvable_numbers(text, ceiling):
            # A renamed file's references are not retired: they are present on
            # the current side under the NEW path, and `_changed_sides` reports
            # both. So a pair is only retired when the number is absent from
            # every current-side path.
            if not any((c, num) in now for c in cur_side) and (path, num) not in now:
                retired.add((path, num))
    return sorted(retired), True


def crossed_reference_set(base, band_hits, ref_exempt):
    """`(path, line, number)` for references the RISING MARK passed over (#1080).

    `band_hits` are the references in `(base_ceiling, ceiling]`: numbers that
    were above the ceiling at the diff base and are below it now. Two very
    different things land in that band, and only one of them is a finding:

    * a reference that was ALREADY IN THE FILE at base. It was counted in the
      burn-down then and is not counted now, and nobody touched it. It did not
      get fixed -- it stopped 404-ing and started resolving, confidently, to
      unrelated live work. That is strictly worse than the dead link it was,
      and it is what makes a falling `tree_baseline` unreadable as progress.
    * a reference this change WROTE. The number is legal now (it is at or below
      the ceiling), the author is citing live work on purpose, and reporting it
      would be pure noise.

    The base blob is what separates them, and it is read only for the handful of
    paths that have a hit in the band at all -- normally zero, since the band is
    empty whenever the ceiling did not move.
    """
    if not band_hits:
        return [], True
    wanted: dict[str, set[int]] = {}
    for p, _line, num, _tok in band_hits:
        if any(matches(p, e) for e in ref_exempt):
            continue
        wanted.setdefault(p, set()).add(num)
    at_base: dict[str, set[int]] = {}
    for p in wanted:
        text = _blob_text(base, p)
        if text is None:
            continue  # not in the base tree: every number in it is newly written
        at_base[p] = _file_unresolvable_numbers(text, 0)
    # Keyed by (path, number) like the retired set, with the FIRST line kept for
    # display. A file citing one crossed number on three lines is one finding
    # with one fix, and listing it three times only buries the other two.
    first_line: dict[tuple[str, int], int] = {}
    for p, line, num, _tok in band_hits:
        if num not in at_base.get(p, ()):
            continue
        key = (p, num)
        if key not in first_line or line < first_line[key]:
            first_line[key] = line
    crossed = sorted((p, first_line[(p, num)], num) for (p, num) in first_line)
    return crossed, True


def references_removed_since(base, ceiling, ref_exempt, tree_hits):
    """How many unresolvable references this tree REMOVED relative to `base`.

    Returns `(removed, comparable)`; `comparable` is False when git could not
    be asked, and the caller must then advise nothing.

    This is what separates the two reasons the tree-wide count can fall
    (#1129). A fall is only evidence of CLEANUP if references actually left the
    tree; it is otherwise erosion -- the ceiling rose over numbers that were
    already there and were already counted, and the tree did not change at all.
    Recording an eroded fall as a lower baseline claims a sweep that never
    happened, and the next branch (measuring at its own, lower ceiling) is then
    blocked by references it never wrote.

    Both sides are measured at the SAME `ceiling`, which is the whole trick:
    the difference is then independent of where the ceiling sits, even though
    each side's absolute count is not. Only files the diff reports as changed
    can contribute to the difference -- an untouched file counts identically on
    both sides -- so this reads a handful of blobs, not the tree.
    """
    base_side, cur_side, comparable = _changed_sides(base)
    if not comparable:
        return 0, False

    at_base = 0
    for path in base_side:
        if any(matches(path, e) for e in ref_exempt):
            continue
        text = _blob_text(base, path)
        if text is None:
            continue
        at_base += _file_unresolvable_count(text, ceiling)

    # The current side is already measured: `tree_hits` is the same corpus,
    # same ceiling, same exemptions. Re-reading the files would risk drift.
    now = sum(1 for h in tree_hits if h[0] in cur_side)
    return at_base - now, True


def repository_is_shallow() -> bool:
    """True when this clone lacks the history the derivation needs.

    A shallow clone answers `git log` with a truncated range, so the derived
    mark would be far too LOW and the guard would reject every legitimate
    reference above it. That must fail loudly rather than produce a tree's worth
    of false positives.
    """
    out = (
        subprocess.run(
            ["git", "rev-parse", "--is-shallow-repository"],
            capture_output=True,
            check=True,
        )
        .stdout.decode(errors="ignore")
        .strip()
    )
    return out == "true"


def main() -> int:
    if not MANIFEST.exists():
        die(
            2,
            f"manifest not found: {MANIFEST}\n  The guard cannot verify anything. Failing closed.",
        )

    try:
        import yaml  # noqa: PLC0415
    except ImportError:
        die(
            2,
            "PyYAML is not available. The guard cannot parse the manifest. Failing closed.",
        )

    try:
        manifest = yaml.safe_load(MANIFEST.read_text())
    except yaml.YAMLError as exc:
        die(2, f"manifest is malformed YAML -- failing closed, not skipping.\n\n{exc}")

    if not isinstance(manifest, dict):
        die(2, "manifest did not parse to a mapping. Failing closed.")

    allow = manifest.get("allow") or []
    deny = manifest.get("deny") or []
    forbidden = manifest.get("forbidden_content") or []
    # (rule id, matching file count, baseline) for every ratcheted rule, so the
    # clean report still states the outstanding debt rather than hiding it.
    content_notes: list[tuple[str, int, int]] = []
    pending = manifest.get("needs_decision") or []

    if not allow:
        die(
            2,
            "manifest has no `allow` rules. Every path would be rejected; this is "
            "almost certainly a broken manifest rather than an empty repo. Failing closed.",
        )

    refs_rule = manifest.get("issue_references")
    if not isinstance(refs_rule, dict):
        die(
            2,
            "manifest has no `issue_references` block. The unresolvable-reference rule "
            "cannot know this repository's high-water mark, so it would silently check "
            "nothing. Failing closed.",
        )
    if "high_water_mark" in refs_rule:
        die(
            2,
            "issue_references.high_water_mark is no longer read -- the mark is derived\n"
            "  from this repository's own merge history (#1078). A recorded mark can go\n"
            "  stale; a derived one cannot. Delete the key from "
            f"{MANIFEST}.",
        )
    slack = refs_rule.get("slack")
    if isinstance(slack, bool) or not isinstance(slack, int) or slack < 0:
        die(2, "issue_references.slack must be a non-negative integer. Failing closed.")

    # THE MARK IS DERIVED, NOT RECORDED (#1078). It was a hand-maintained integer
    # for 21 chore commits, and it fails CLOSED: every time it fell behind, the
    # rule began rejecting legitimate references and turned `main` red. Reading
    # it from the forge's own squash-merge markers keeps the entire guarantee and
    # removes the counter that nobody was watching.
    if repository_is_shallow():
        die(
            2,
            "the reference ceiling is derived from merge history, and this is a SHALLOW\n"
            "  clone -- the derived mark would be far too low and every reference above\n"
            "  it would be reported as unresolvable.\n"
            "  Fetch full history (`git fetch --unshallow`, or `fetch-depth: 0` in CI).",
        )
    # Resolved BEFORE the mark, not with the diff-scoped rules below, because
    # the base ref is half of the ceiling now (#1129).
    base_ref, base = resolve_diff_base()
    if base is None:
        die(
            2,
            "cannot resolve a base commit to diff against, so the "
            "unresolvable-reference rule would check nothing.\n"
            "  Fetch the default branch, or set NG_BOUNDARY_DIFF_BASE to a "
            "revision. Failing closed.",
        )
    mark, mark_source = ceiling_marks(base_ref)
    if mark < 1:
        die(
            2,
            "no `(#N)` merge marker found on the first-parent history, so the reference\n"
            "  ceiling cannot be derived. The guard will not check references against a\n"
            "  mark it had to invent. Failing closed.",
        )
    ceiling = mark + slack
    # The ceiling the DIFF BASE was measured at (#1080). A reference between the
    # two edges was counted in the burn-down at base and is not counted now, and
    # that fall is not a fix: the repository issued a number past it, so it
    # stopped 404-ing and started resolving to unrelated live work.
    #
    # Derived from the base commit rather than recorded anywhere, for the reason
    # #1078 deleted the recorded mark: a snapshot of the set would be one more
    # integer-shaped artifact to keep current, stale in silence, and wrong in
    # exactly the situations it exists for. git already holds both sides.
    #
    # This is why the report fires where the crossing actually HAPPENS. On a
    # pull request the base is `origin/main`, whose mark the branch also borrows
    # (`ceiling_marks` takes the larger), so the two edges coincide and there is
    # nothing to report -- correctly, because a branch that merges nothing
    # crosses nothing. On the `push: [main]` run the base is `HEAD^`, the mark
    # rose by exactly the merge that just landed, and the references it crossed
    # are named. That is the run that observed 5801 -> 5796 in #1080.
    base_ceiling = min(derived_high_water(base) + slack, ceiling)

    violations: list[str] = []
    tracked = tracked_paths()
    untracked = untracked_paths()
    untracked_set = set(untracked)
    # One corpus. Every path rule below asks the same question of both halves:
    # "would this content be a violation once committed?" -- and for an
    # untracked file the answer is knowable now, which is the whole point of a
    # PRE-push gate.
    paths = tracked + untracked

    def untracked_note(p: str) -> str:
        """Suffix, never an infix: `path:line` stays clickable and greppable.

        Not named `mark` -- that is already this scope's high_water_mark, and
        shadowing it silently rendered a function object into the ceiling
        message instead of the number.
        """
        return "  [UNTRACKED -- not yet `git add`-ed]" if p in untracked_set else ""

    def where(p: str) -> str:
        return p + untracked_note(p)

    # ── 1. Denied paths ──────────────────────────────────────────────────────
    deny_except = {e for r in deny for e in (r.get("except") or [])}
    for rule in deny:
        pat = rule["path"]
        for p in paths:
            if p in deny_except:
                continue
            if matches(p, pat):
                violations.append(
                    f"PRIVATE path is present: {where(p)}\n"
                    f"    matched deny rule: {pat}\n"
                    f"    {(rule.get('rationale') or '').strip().splitlines()[0] if rule.get('rationale') else ''}"
                )

    # ── 2. Unclassified paths (the fail-closed core) ─────────────────────────
    allow_pats = [r["path"] for r in allow]
    deny_pats = [r["path"] for r in deny]
    for p in paths:
        if any(matches(p, pat) for pat in deny_pats):
            continue  # already reported above
        if not any(matches(p, pat) for pat in allow_pats):
            violations.append(
                f"UNCLASSIFIED path: {where(p)}\n"
                f"    No allow rule matches it, so it is rejected by default.\n"
                f"    Classify it in {MANIFEST} before adding it."
            )

    # ── 3. Forbidden content ─────────────────────────────────────────────────
    # Rules are COMPILED here and the tree is scanned once, further down, in the
    # single pass shared with the token rule and the tree-wide reference count.
    # Scanning per rule meant re-reading and re-decoding all ~4,150 files once
    # per rule -- seven full passes over the tree for five rules (#850).
    compiled_rules = []
    for rule in forbidden:
        rid = rule.get("id", "<unnamed>")
        try:
            # Case-insensitive: a forbidden string must not slip through on
            # casing alone (e.g. an all-caps env-var name vs the title-case
            # brand). Rules that need case sensitivity can pin it with an
            # inline (?-i:...) group.
            pattern = re.compile(rule["pattern"], re.IGNORECASE)
        except re.error as exc:
            die(
                2,
                f"forbidden_content rule '{rid}' has an invalid regex: {exc}. Failing closed.",
            )
        exempt = rule.get("allow_paths") or []
        # A rule may declare `file_baseline: N` to land while the tree still
        # violates it. The gate then becomes MONOTONIC over the number of
        # matching FILES rather than absolute: it fails when the count rises and
        # says so when it falls. A rule WITHOUT the key behaves exactly as
        # before -- every matching file is a violation -- so this changes no
        # existing rule's semantics.
        #
        # This exists because a rule written against a brand NAME does not match
        # the brand's abbreviated forms, and narrowing the rule to keep the
        # build green is how a gate ends up green for months while 248 files
        # violate its intent.
        rule_baseline = rule.get("file_baseline")
        if rule_baseline is not None and (
            not isinstance(rule_baseline, int) or rule_baseline < 0
        ):
            die(
                2,
                f"forbidden_content rule '{rid}' has a non-integer "
                f"file_baseline. Failing closed.",
            )
        compiled_rules.append((rid, pattern, exempt, rule_baseline))

    # ── 3b. Forbidden tokens, matched by HASH ────────────────────────────────
    # The portfolio identifiers cannot be listed in plaintext: this manifest is
    # published, so naming them would leak exactly what the rule protects. They
    # are stored as salted SHA-256 instead. Enforcement is identical; disclosure
    # is zero.
    tokens_rule = manifest.get("forbidden_tokens") or {}
    token_hashes = set(tokens_rule.get("hashes") or [])
    if token_hashes:
        salt = tokens_rule.get("salt")
        if not isinstance(salt, str) or not salt:
            die(2, "forbidden_tokens has hashes but no salt. Failing closed.")
        token_allow_paths = set(tokens_rule.get("allow_paths") or [])
        word = re.compile(r"[A-Za-z0-9_.-]+")
        # Built here so its lifetime matches this (salt, token_hashes) pair.
        token_memo: dict[str, bool] = {}
    else:
        salt = None
        token_allow_paths = set()
        word = None
        token_memo = {}

    # ── 3c. Unresolvable issue references, on NEWLY ADDED lines ──────────────
    # "#N above the high-water mark" is a definition, not a heuristic: such a
    # number cannot resolve at the moment it is written, at any repository size.
    # See the module header for why the scope is a diff rather than the tree.
    # `base_ref`/`base` were resolved above, with the ceiling.

    ref_exempt = refs_rule.get("allow_paths") or []

    # ── THE SINGLE PASS ──────────────────────────────────────────────────────
    # Every content rule, the token rule and the tree-wide reference count each
    # used to walk the whole tree themselves, so the tree was read and decoded
    # SEVEN times for five rules (#850). They are all per-file and independent,
    # so one read feeds all of them.
    #
    # Two decodes, deliberately, because the rules do not agree on encoding and
    # collapsing them would silently change what is scanned: the content and
    # token rules used `read_text(errors="ignore")`, which yields text for a
    # binary file, while the tree-wide scan used a STRICT utf-8 open and skipped
    # anything that failed to decode. Both behaviours are preserved here against
    # the same bytes.
    #
    # Violations are collected per section and appended below in the original
    # section order, so the report is byte-identical to the multi-pass form.
    _enc = locale.getpreferredencoding(False)
    tracked_set = set(tracked)
    _rule_specs = [
        (rid, pat.pattern, exempt) for rid, pat, exempt, _b in compiled_rules
    ]
    _init_args = (
        _rule_specs,
        token_hashes,
        salt,
        token_allow_paths,
        str(MANIFEST),
        ref_exempt,
        ceiling,
        _enc,
        tracked_set,
        base_ceiling,
        mark,
    )

    rule_hits: dict[str, list] = {rid: [] for rid, _p, _e, _b in compiled_rules}
    token_violations: list[str] = []
    tree_hits: list = []
    band_hits: list = []

    jobs = _scan_jobs(len(paths))
    if jobs > 1:
        # Contiguous slices, merged in slice order, so the report is identical
        # to the serial walk. Striding would interleave and reorder it.
        size = (len(paths) + jobs - 1) // jobs
        chunks = [paths[i : i + size] for i in range(0, len(paths), size)]
        try:
            with ProcessPoolExecutor(
                max_workers=jobs, initializer=_scan_init, initargs=_init_args
            ) as pool:
                results = list(pool.map(_scan_chunk, chunks))
        except Exception as exc:  # noqa: BLE001
            # A worker that dies must never look like a clean tree. main()'s
            # caller turns this into exit 2; the one thing it must not do is
            # fall through to a report built from partial results.
            die(
                2,
                f"the parallel scan failed ({exc!r}). Failing closed.\n"
                f"  Re-run with NG_BOUNDARY_JOBS=1 to scan in this process.",
            )
    else:
        _scan_init(*_init_args)
        results = [_scan_chunk(paths)]

    for chunk_rules, chunk_tokens, chunk_tree, chunk_band in results:
        for rid, hp, hn, snippet in chunk_rules:
            rule_hits[rid].append((hp, hn, snippet))
        for hp, hn in chunk_tokens:
            token_violations.append(
                f"FORBIDDEN TOKEN: {hp}:{hn}{untracked_note(hp)}\n"
                f"    A token on this line is on the private-identifier denylist.\n"
                f"    It is matched by hash, so it is not named here. See\n"
                f"    nightgauge-internal (strategy/) for the plaintext list."
            )
        tree_hits.extend(chunk_tree)
        band_hits.extend(chunk_band)

    # ── 3 (emit). Forbidden content ──────────────────────────────────────────
    for rid, _pattern, _exempt, rule_baseline in compiled_rules:
        hits = rule_hits[rid]
        if rule_baseline is None:
            for hp, hn, snippet in hits:
                violations.append(
                    f"FORBIDDEN CONTENT [{rid}]: {hp}:{hn}{untracked_note(hp)}\n"
                    f"    {snippet}"
                )
        elif len(hits) > rule_baseline:
            sample = "\n".join(f"      {h[0]}:{h[1]}  {h[2]}" for h in sorted(hits)[:8])
            violations.append(
                f"FORBIDDEN CONTENT COUNT ROSE [{rid}]: "
                f"{len(hits)} file(s) > baseline {rule_baseline}\n"
                f"    This rule's matching-file count may only go DOWN.\n"
                f"    First few:\n{sample}\n"
                f"    Fix the new ones. Do not raise `file_baseline`."
            )
        else:
            content_notes.append((rid, len(hits), rule_baseline))

    # ── 3b (emit). Forbidden tokens ──────────────────────────────────────────
    violations.extend(token_violations)
    # `git diff <commit>` compares the commit to the working tree via the index,
    # so it covers staged-new files but never untracked ones. Their lines are
    # appended explicitly -- every line of a new file is an added line.
    added = list(added_lines(base)) + list(untracked_added_lines(untracked))
    # Cache per path: `git show base:path` once, not once per matching line.
    _base_nums: dict[str, set[int]] = {}
    carried_over = 0
    for p, n, line in added:
        if any(matches(p, e) for e in ref_exempt):
            continue
        for num, token in unresolvable_refs(line, ceiling):
            if p not in _base_nums:
                _base_nums[p] = base_file_numbers(base, p, ceiling)
            if num in _base_nums[p]:
                # Pre-existing in this same file at base: the edit moved or
                # rewrapped a line that already carried it. Counted in the
                # tree-wide burn-down below, not charged to this change.
                carried_over += 1
                continue
            violations.append(
                f"UNRESOLVABLE ISSUE REFERENCE: {p}:{n}{untracked_note(p)}\n"
                f"    {token} is above this repository's high-water mark "
                f"({mark} + slack {slack} = {ceiling}), so it cannot resolve here.\n"
                f"    {line.strip()[:100]}\n"
                f"    Cite the real issue, drop the '#', or qualify it as `owner/repo#{num}`\n"
                f"    if it belongs to another repository. The ceiling is DERIVED from merge\n"
                f"    history (#1078), so there is no mark to bump: if #{num} genuinely exists\n"
                f"    here, this branch simply predates the merge that issued it.\n"
                f"    (measured against {base_ref} @ {base[:12]})"
            )

    # ── 3d. Unresolvable issue references, TREE-WIDE (burn-down) ─────────────
    # 3c answers "did this change introduce one?". This answers "how many are
    # there?" -- the question the guard could not previously ask at all, because
    # its scope was the diff. docs/ADAPTER_MATRIX.md carried `#2595` against a
    # high-water mark of 789 and the guard printed "clean".
    #
    # The gate is MONOTONIC, not absolute: the count may not rise above the
    # recorded baseline. That is what lets the guard ship before the sweep --
    # the ordering the module header correctly refuses to invert -- while still
    # making the debt visible and one-directional.
    tree_baseline = refs_rule.get("tree_baseline")
    # Collected in the single pass above (#850).
    tree_count = len(tree_hits)
    tree_files = len({h[0] for h in tree_hits})

    if tree_baseline is None:
        die(
            2,
            "issue_references.tree_baseline is missing. The tree-wide burn-down\n"
            f"  cannot be enforced without it. Observed right now: {tree_count} "
            f"reference(s) across {tree_files} file(s).\n"
            f"  Record `tree_baseline: {tree_count}` in {MANIFEST}. Failing closed.",
        )
    if not isinstance(tree_baseline, int) or tree_baseline < 0:
        die(
            2,
            "issue_references.tree_baseline must be a non-negative integer. Failing closed.",
        )

    if tree_count > tree_baseline:
        worst = sorted(tree_hits, key=lambda h: (h[0], h[1]))[:10]
        sample = "\n".join(f"      {h[0]}:{h[1]}  {h[3]}" for h in worst)
        violations.append(
            f"UNRESOLVABLE REFERENCE COUNT ROSE: {tree_count} > baseline {tree_baseline}\n"
            f"    The tree-wide count of references above the high-water mark "
            f"({ceiling}) may only go DOWN.\n"
            f"    {tree_count - tree_baseline} more than the recorded baseline, "
            f"across {tree_files} file(s). First few:\n{sample}\n"
            f"    Fix the new ones. Do not raise `tree_baseline`.\n"
            f"    This count is measured at ceiling {ceiling} (mark {mark} from "
            f"{mark_source} + slack {slack}). A LOWER ceiling leaves more numbers\n"
            f"    above it and so measures MORE: if the sample above is entirely\n"
            f"    pre-existing, this branch's ceiling is behind the mainline's and the\n"
            f"    fix is `git fetch origin main`, not an edit (#1129)."
        )

    # ── 4. NEEDS-DECISION must be empty ──────────────────────────────────────
    for item in pending:
        violations.append(
            f"NEEDS-DECISION unresolved: {item.get('path', item)}\n"
            f"    {item.get('rationale', '')}\n"
            f"    This bucket must be empty. It is a work-list, not a parking lot."
        )

    # ── Report ───────────────────────────────────────────────────────────────
    if violations:
        print(
            f"\n\033[31m✗ publication boundary: {len(violations)} violation(s)\033[0m\n",
            file=sys.stderr,
        )
        for v in violations:
            print(f"  • {v}\n", file=sys.stderr)
        print(
            "This repository is maintained as a public-safe tree regardless of its current",
            file=sys.stderr,
        )
        print(
            "visibility setting -- treat every violation above as a real leak. Fix the above,",
            file=sys.stderr,
        )
        print(
            f"or classify the path in {MANIFEST} if it is genuinely publishable.\n",
            file=sys.stderr,
        )
        return 1

    scope = f"{len(tracked)} tracked path(s)"
    if untracked:
        scope += f" + {len(untracked)} untracked, not-yet-added path(s)"
    print(
        f"\033[32m✓ publication boundary clean\033[0m — {scope}, "
        f"all classified; no denied paths, no forbidden content, no open decisions."
    )
    # NOT `base` — that name holds the diff base commit and is read again
    # below. Shadowing it here made the report crash into the fail-closed
    # handler with an unhelpful TypeError.
    for rid, count, rule_base in content_notes:
        if count < rule_base:
            print(
                f"  {rid}: {count} file(s), BELOW the recorded baseline of {rule_base}.\n"
                f"    Lower `file_baseline` to {count} in {MANIFEST} so the ratchet holds."
            )
        else:
            print(f"  {rid}: {count} file(s) still match, at the recorded baseline.")

    at_ceiling = f"measured at ceiling {ceiling} (mark {mark} from {mark_source} + slack {slack})"
    if tree_count < tree_baseline:
        # A fall has two possible causes and only one of them may be ratcheted
        # (#1129). `removed` is the part attributable to references that
        # actually left the tree; the rest is the ceiling having risen over
        # numbers that were already there, which is not a cleanup and must not
        # be recorded as one -- the baseline is global, and the next branch
        # measuring at a lower ceiling would be blocked by references it never
        # wrote.
        removed, comparable = references_removed_since(
            base, ceiling, ref_exempt, tree_hits
        )
        # Subtract from the baseline recorded AT BASE, and never advise a number
        # above the one already recorded here -- so re-running after taking the
        # advice names the same value instead of walking the baseline down by
        # `removed` on every run.
        anchor = baseline_at(base, tree_baseline) if comparable else tree_baseline
        floor = (
            max(tree_count, min(tree_baseline, anchor - removed))
            if comparable
            else tree_baseline
        )
        if comparable and removed > 0:
            done = floor >= tree_baseline
            print(
                f"  issue references: tree-wide count is {tree_count}, BELOW the recorded "
                f"baseline of {tree_baseline} ({at_ceiling}).\n"
                f"    {removed} reference(s) were genuinely removed from the tree since "
                f"{base_ref} ({base[:12]}), measured on both sides at that same ceiling.\n"
                + (
                    f"    `issue_references.tree_baseline` is ALREADY at the attributable "
                    f"floor of {floor}; the removals are recorded. Change nothing."
                    if done
                    else f"    Lower `issue_references.tree_baseline` to {floor} in "
                    f"{MANIFEST} so the ratchet holds."
                )
            )
            if floor > tree_count:
                print(
                    f"    Do NOT lower it to {tree_count}. The remaining "
                    f"{floor - tree_count} is not attributable to anything being "
                    f"removed --\n"
                    f"    the ceiling rose over references that were already there. "
                    f"Recording that erosion\n"
                    f"    would claim a sweep that did not happen and block the next "
                    f"branch (#1129)."
                )
        else:
            why = (
                "nothing was removed from the tree"
                if comparable
                else "the two causes could not be separated (git would not answer)"
            )
            print(
                f"  issue references: tree-wide count is {tree_count}, below the recorded "
                f"baseline of {tree_baseline} ({at_ceiling}).\n"
                f"    Do NOT lower `issue_references.tree_baseline`: {why}, so this fall "
                f"is not a cleanup.\n"
                f"    A count is only comparable against the ceiling it was taken at, and "
                f"the baseline is\n"
                f"    global -- ratcheting an unattributed fall blocks branches that "
                f"measure lower (#1129)."
            )
    else:
        print(
            f"  issue references: {tree_count} unresolvable reference(s) tree-wide "
            f"across {tree_files} file(s), at the recorded baseline ({at_ceiling})."
        )
    # ── Which direction did the number actually move? (#1080) ────────────────
    # `tree_count` is one scalar over a MOVING threshold, so a fall has two
    # causes that look identical in it:
    #
    #   * a reference RETIRED by an edit -- genuine progress; and
    #   * a reference CROSSED by the rising mark -- not progress at all. It was
    #     a dead link and is now a live link to unrelated work, which is the
    #     exact hazard the rule exists to prevent. The burn-down improves as the
    #     problem gets worse.
    #
    # Both populations are named here, as a REPORT and never a gate. Gating a
    # crossing would fail the pull request that merely raised the mark -- whose
    # author introduced nothing and can fix nothing by editing their own diff --
    # and a gate nobody can satisfy is a gate that gets bypassed. The ratchet on
    # `tree_count` above is untouched and remains the hard failure.
    retired, retired_ok = retired_reference_set(base, ceiling, ref_exempt, tree_hits)
    crossed, _crossed_ok = crossed_reference_set(base, band_hits, ref_exempt)
    _LIST_CAP = 25
    if crossed:
        print(
            f"  issue references: {len(crossed)} reference(s) CROSSED the ceiling "
            f"since {base_ref} ({base[:12]}).\n"
            f"    The mark rose from {base_ceiling - slack} to {mark}; these were above "
            f"the old ceiling of {base_ceiling} and are at or below the mark now.\n"
            f"    Nobody edited them. Each one has stopped 404-ing and now resolves, "
            f"confidently, to UNRELATED live work --\n"
            f"    which reads as a correct citation and is worse than the dead link it "
            f"replaced. Reported, never gated (#1080):"
        )
        for p, line, num in crossed[:_LIST_CAP]:
            print(f"    crossed (now resolves to unrelated work): {p}:{line} #{num}")
        if len(crossed) > _LIST_CAP:
            print(f"    ... and {len(crossed) - _LIST_CAP} more")
    if retired:
        print(
            f"  issue references: {len(retired)} reference(s) RETIRED by an edit "
            f"since {base_ref} ({base[:12]}) -- this is the progress kind:"
        )
        for p, num in retired[:_LIST_CAP]:
            print(f"    retired: {p} #{num}")
        if len(retired) > _LIST_CAP:
            print(f"    ... and {len(retired) - _LIST_CAP} more")
    if not retired_ok:
        print(
            "  issue references: git would not answer the diff, so retired and crossed\n"
            "    references could not be told apart. Read any fall in the count as "
            "unattributed."
        )
    if carried_over:
        print(
            f"    {carried_over} pre-existing reference(s) on edited lines were "
            f"carried over, not charged to this change."
        )
    print(
        f"  issue references: {len(added)} added line(s) over {base_ref} "
        f"({base[:12]}) carry no #N above {ceiling}."
    )
    if untracked:
        # Naming them is the point: the pass is only as good as the corpus, and
        # a reader who cannot see the corpus cannot judge the pass (#716).
        print(
            f"  scanned as new content (untracked, not ignored): "
            f"{', '.join(sorted(untracked)[:10])}"
            f"{f' and {len(untracked) - 10} more' if len(untracked) > 10 else ''}"
        )
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except subprocess.CalledProcessError as exc:
        die(2, f"git failed: {exc}. Failing closed.")
    except Exception as exc:  # noqa: BLE001 — fail closed on ANYTHING
        die(2, f"unexpected error: {exc!r}. Failing closed rather than passing blind.")
