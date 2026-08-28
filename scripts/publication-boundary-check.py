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
ISSUE_REF = re.compile(r"(?<![0-9A-Za-z_/&#-])(?:nightgauge/nightgauge|nightgauge)?#([0-9]+)")
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
            ["git", "diff", "--no-color", "--no-ext-diff", "--find-renames",
             "--name-status", "--diff-filter=R", base],
            capture_output=True, check=True,
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
    r = subprocess.run(
        ["git", "show", f"{base}:{path}"], capture_output=True
    )
    if r.returncode != 0:
        old = rename_map(base).get(path)
        if old is None:
            return set()
        r = subprocess.run(
            ["git", "show", f"{base}:{old}"], capture_output=True
        )
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


def _scan_init(rule_specs, token_hashes, salt, token_allow, manifest_path,
               ref_exempt, ceiling, enc, tracked):
    """Compile per-worker state once, not once per chunk."""
    global _SCAN
    _SCAN = {
        # Patterns are shipped as SOURCE and compiled here: a compiled pattern
        # survives pickling, but sending the source keeps the worker's regex
        # flags explicit and identical to the serial path.
        "rules": [(rid, re.compile(src, re.IGNORECASE), exempt)
                  for rid, src, exempt in rule_specs],
        "token_hashes": token_hashes,
        "salt": salt,
        "token_allow": token_allow,
        "manifest": manifest_path,
        "ref_exempt": ref_exempt,
        "ceiling": ceiling,
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
            hits = list(_unresolvable_in_text(strict, c["ceiling"]))
            if not hits:
                continue
            starts = [0]
            for i, ch in enumerate(strict):
                if ch == "\n":
                    starts.append(i + 1)
            for num, token, pos in hits:
                tree_hits.append((p, bisect.bisect_right(starts, pos), num, token))
    return rule_hits, token_hits, tree_hits


def _scan_jobs(path_count: int) -> int:
    """How many worker processes to use. 1 means "run in this process"."""
    override = os.environ.get("NG_BOUNDARY_JOBS")
    if override:
        try:
            n = int(override)
        except ValueError:
            die(2, f"NG_BOUNDARY_JOBS must be an integer, got {override!r}. Failing closed.")
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
            die(2, f"NG_BOUNDARY_DIFF_BASE={override!r} does not resolve to a commit. "
                   "Failing closed rather than silently diffing against a different base.")
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
        found = subprocess.run(
            ["git", "merge-base", cand, "HEAD"], capture_output=True
        )
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
        ["git", "diff", "--no-color", "--no-ext-diff", "--unified=0",
         "--diff-filter=ACMR", base],
        capture_output=True,
        check=True,
    ).stdout.decode(errors="ignore")

    path: str | None = None
    lineno = 0
    for raw in out.splitlines():
        if raw.startswith("+++ "):
            target = raw[4:]
            path = None if target == "/dev/null" else target[2:] if target.startswith("b/") else target
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


def derived_high_water() -> int:
    """The repository's high-water mark, read from its own merge history.

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
    """
    out = subprocess.run(
        ["git", "log", "--first-parent", "--format=%s", "HEAD"],
        capture_output=True,
        check=True,
    ).stdout.decode(errors="ignore")
    trailing = (re.search(r"\(#(\d+)\)\s*$", line) for line in out.splitlines())
    return max((int(m.group(1)) for m in trailing if m), default=0)


def repository_is_shallow() -> bool:
    """True when this clone lacks the history the derivation needs.

    A shallow clone answers `git log` with a truncated range, so the derived
    mark would be far too LOW and the guard would reject every legitimate
    reference above it. That must fail loudly rather than produce a tree's worth
    of false positives.
    """
    out = subprocess.run(
        ["git", "rev-parse", "--is-shallow-repository"],
        capture_output=True,
        check=True,
    ).stdout.decode(errors="ignore").strip()
    return out == "true"


def main() -> int:
    if not MANIFEST.exists():
        die(2, f"manifest not found: {MANIFEST}\n  The guard cannot verify anything. Failing closed.")

    try:
        import yaml  # noqa: PLC0415
    except ImportError:
        die(2, "PyYAML is not available. The guard cannot parse the manifest. Failing closed.")

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
        die(2, "manifest has no `allow` rules. Every path would be rejected; this is "
               "almost certainly a broken manifest rather than an empty repo. Failing closed.")

    refs_rule = manifest.get("issue_references")
    if not isinstance(refs_rule, dict):
        die(2, "manifest has no `issue_references` block. The unresolvable-reference rule "
               "cannot know this repository's high-water mark, so it would silently check "
               "nothing. Failing closed.")
    if "high_water_mark" in refs_rule:
        die(2, "issue_references.high_water_mark is no longer read -- the mark is derived\n"
               "  from this repository's own merge history (#1078). A recorded mark can go\n"
               "  stale; a derived one cannot. Delete the key from "
               f"{MANIFEST}.")
    slack = refs_rule.get("slack")
    if isinstance(slack, bool) or not isinstance(slack, int) or slack < 0:
        die(2, "issue_references.slack must be a non-negative integer. Failing closed.")

    # THE MARK IS DERIVED, NOT RECORDED (#1078). It was a hand-maintained integer
    # for 21 chore commits, and it fails CLOSED: every time it fell behind, the
    # rule began rejecting legitimate references and turned `main` red. Reading
    # it from the forge's own squash-merge markers keeps the entire guarantee and
    # removes the counter that nobody was watching.
    if repository_is_shallow():
        die(2, "the reference ceiling is derived from merge history, and this is a SHALLOW\n"
               "  clone -- the derived mark would be far too low and every reference above\n"
               "  it would be reported as unresolvable.\n"
               "  Fetch full history (`git fetch --unshallow`, or `fetch-depth: 0` in CI).")
    mark = derived_high_water()
    if mark < 1:
        die(2, "no `(#N)` merge marker found on the first-parent history, so the reference\n"
               "  ceiling cannot be derived. The guard will not check references against a\n"
               "  mark it had to invent. Failing closed.")
    ceiling = mark + slack

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
            die(2, f"forbidden_content rule '{rid}' has an invalid regex: {exc}. Failing closed.")
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
            die(2, f"forbidden_content rule '{rid}' has a non-integer "
                   f"file_baseline. Failing closed.")
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
    base_ref, base = resolve_diff_base()
    if base is None:
        die(2, "cannot resolve a base commit to diff against, so the "
               "unresolvable-reference rule would check nothing.\n"
               "  Fetch the default branch, or set NG_BOUNDARY_DIFF_BASE to a "
               "revision. Failing closed.")

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
    _rule_specs = [(rid, pat.pattern, exempt)
                   for rid, pat, exempt, _b in compiled_rules]
    _init_args = (_rule_specs, token_hashes, salt, token_allow_paths,
                  str(MANIFEST), ref_exempt, ceiling, _enc, tracked_set)

    rule_hits: dict[str, list] = {rid: [] for rid, _p, _e, _b in compiled_rules}
    token_violations: list[str] = []
    tree_hits: list = []

    jobs = _scan_jobs(len(paths))
    if jobs > 1:
        # Contiguous slices, merged in slice order, so the report is identical
        # to the serial walk. Striding would interleave and reorder it.
        size = (len(paths) + jobs - 1) // jobs
        chunks = [paths[i:i + size] for i in range(0, len(paths), size)]
        try:
            with ProcessPoolExecutor(
                max_workers=jobs, initializer=_scan_init, initargs=_init_args
            ) as pool:
                results = list(pool.map(_scan_chunk, chunks))
        except Exception as exc:  # noqa: BLE001
            # A worker that dies must never look like a clean tree. main()'s
            # caller turns this into exit 2; the one thing it must not do is
            # fall through to a report built from partial results.
            die(2, f"the parallel scan failed ({exc!r}). Failing closed.\n"
                   f"  Re-run with NG_BOUNDARY_JOBS=1 to scan in this process.")
    else:
        _scan_init(*_init_args)
        results = [_scan_chunk(paths)]

    for chunk_rules, chunk_tokens, chunk_tree in results:
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
            sample = "\n".join(
                f"      {h[0]}:{h[1]}  {h[2]}" for h in sorted(hits)[:8]
            )
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
        die(2, "issue_references.tree_baseline is missing. The tree-wide burn-down\n"
               f"  cannot be enforced without it. Observed right now: {tree_count} "
               f"reference(s) across {tree_files} file(s).\n"
               f"  Record `tree_baseline: {tree_count}` in {MANIFEST}. Failing closed.")
    if not isinstance(tree_baseline, int) or tree_baseline < 0:
        die(2, "issue_references.tree_baseline must be a non-negative integer. Failing closed.")

    if tree_count > tree_baseline:
        worst = sorted(tree_hits, key=lambda h: (h[0], h[1]))[:10]
        sample = "\n".join(f"      {h[0]}:{h[1]}  {h[3]}" for h in worst)
        violations.append(
            f"UNRESOLVABLE REFERENCE COUNT ROSE: {tree_count} > baseline {tree_baseline}\n"
            f"    The tree-wide count of references above the high-water mark "
            f"({ceiling}) may only go DOWN.\n"
            f"    {tree_count - tree_baseline} more than the recorded baseline, "
            f"across {tree_files} file(s). First few:\n{sample}\n"
            f"    Fix the new ones. Do not raise `tree_baseline`."
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
        print(f"\n\033[31m✗ publication boundary: {len(violations)} violation(s)\033[0m\n",
              file=sys.stderr)
        for v in violations:
            print(f"  • {v}\n", file=sys.stderr)
        print("This repository is maintained as a public-safe tree regardless of its current",
              file=sys.stderr)
        print("visibility setting -- treat every violation above as a real leak. Fix the above,",
              file=sys.stderr)
        print(f"or classify the path in {MANIFEST} if it is genuinely publishable.\n", file=sys.stderr)
        return 1

    scope = f"{len(tracked)} tracked path(s)"
    if untracked:
        scope += f" + {len(untracked)} untracked, not-yet-added path(s)"
    print(f"\033[32m✓ publication boundary clean\033[0m — {scope}, "
          f"all classified; no denied paths, no forbidden content, no open decisions.")
    # NOT `base` — that name holds the diff base commit and is read again
    # below. Shadowing it here made the report crash into the fail-closed
    # handler with an unhelpful TypeError.
    for rid, count, rule_base in content_notes:
        if count < rule_base:
            print(f"  {rid}: {count} file(s), BELOW the recorded baseline of {rule_base}.\n"
                  f"    Lower `file_baseline` to {count} in {MANIFEST} so the ratchet holds.")
        else:
            print(f"  {rid}: {count} file(s) still match, at the recorded baseline.")

    if tree_count < tree_baseline:
        print(f"  issue references: tree-wide count is {tree_count}, BELOW the recorded "
              f"baseline of {tree_baseline}.\n"
              f"    Lower `issue_references.tree_baseline` to {tree_count} in {MANIFEST} "
              f"so the ratchet holds.")
    else:
        print(f"  issue references: {tree_count} unresolvable reference(s) tree-wide "
              f"across {tree_files} file(s), at the recorded baseline.")
    if carried_over:
        print(f"    {carried_over} pre-existing reference(s) on edited lines were "
              f"carried over, not charged to this change.")
    print(f"  issue references: {len(added)} added line(s) over {base_ref} "
          f"({base[:12]}) carry no #N above {ceiling}.")
    if untracked:
        # Naming them is the point: the pass is only as good as the corpus, and
        # a reader who cannot see the corpus cannot judge the pass (#716).
        print(f"  scanned as new content (untracked, not ignored): "
              f"{', '.join(sorted(untracked)[:10])}"
              f"{f' and {len(untracked) - 10} more' if len(untracked) > 10 else ''}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except subprocess.CalledProcessError as exc:
        die(2, f"git failed: {exc}. Failing closed.")
    except Exception as exc:  # noqa: BLE001 — fail closed on ANYTHING
        die(2, f"unexpected error: {exc!r}. Failing closed rather than passing blind.")
