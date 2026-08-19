#!/usr/bin/env python3
"""Publication boundary guard for nightgauge/nightgauge.

Enforces .github/publication-boundary.yaml against the tracked tree.

FAIL-CLOSED. Every exit path that is not an explicit, fully-verified pass is a
failure:

  * manifest missing, unreadable, or malformed  -> FAIL (never "skip")
  * a tracked path matching no `allow` rule     -> FAIL (not warn, not ignore)
  * a tracked path matching a `deny` rule       -> FAIL
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

import hashlib
import os
import re
import subprocess
import sys
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


def _line_has_denied_token(line, word, salt, token_hashes):
    """True if any token on this line (or its stem) hashes into the denylist."""
    for tok in word.findall(line):
        for cand in _stem_prefixes(tok):
            if hashlib.sha256((salt + cand).encode()).hexdigest() in token_hashes:
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


def unresolvable_refs(line: str, ceiling: int):
    """Yield (number, matched_text) for each #N on `line` that cannot resolve."""
    for m in ISSUE_REF.finditer(line):
        digits = m.group(1)
        # "#5865f2" -> ISSUE_REF sees "#5865". A reference is never followed by
        # a hex letter, so a longer hex run means this is a colour.
        run = HEX_RUN.match(line, m.start(1))
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
            yield n, m.group(0)


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


def observed_high_water() -> int:
    """Largest "(#N)" in first-parent commit subjects.

    Squash merges write the pull request number into the subject, so this is an
    OFFLINE lower bound on the numbers this repository has really issued -- no
    network call inside a fail-closed guard. It is used only to detect that the
    recorded mark has gone stale; it never raises the ceiling on its own, so the
    guard can never be silently weakened by a crafted commit subject.
    """
    out = subprocess.run(
        ["git", "log", "--first-parent", "--format=%s", "HEAD"],
        capture_output=True,
        check=True,
    ).stdout.decode(errors="ignore")
    return max((int(n) for n in re.findall(r"\(#(\d+)\)", out)), default=0)


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
    pending = manifest.get("needs_decision") or []

    if not allow:
        die(2, "manifest has no `allow` rules. Every path would be rejected; this is "
               "almost certainly a broken manifest rather than an empty repo. Failing closed.")

    refs_rule = manifest.get("issue_references")
    if not isinstance(refs_rule, dict):
        die(2, "manifest has no `issue_references` block. The unresolvable-reference rule "
               "cannot know this repository's high-water mark, so it would silently check "
               "nothing. Failing closed.")
    mark = refs_rule.get("high_water_mark")
    slack = refs_rule.get("slack")
    if isinstance(mark, bool) or not isinstance(mark, int) or mark < 1:
        die(2, "issue_references.high_water_mark must be a positive integer. Failing closed.")
    if isinstance(slack, bool) or not isinstance(slack, int) or slack < 0:
        die(2, "issue_references.slack must be a non-negative integer. Failing closed.")
    ceiling = mark + slack

    # The recorded mark is the source of truth for the ceiling; git history is
    # only ever consulted to prove that mark has gone stale. Once the repository
    # has demonstrably issued a number ABOVE the ceiling, the rule would start
    # denying legitimate references -- so stop, loudly, instead of manufacturing
    # false positives.
    observed = observed_high_water()
    if observed > ceiling:
        die(2, f"issue_references.high_water_mark ({mark}) is stale.\n"
               f"  This repository has already merged #{observed}, above the ceiling "
               f"{ceiling} (= {mark} + slack {slack}).\n"
               f"  Bump `high_water_mark` in {MANIFEST} to the repository's current highest\n"
               f"  issue/PR number. Until then this rule would reject real references.")

    violations: list[str] = []
    paths = tracked_paths()

    # ── 1. Denied paths ──────────────────────────────────────────────────────
    deny_except = {e for r in deny for e in (r.get("except") or [])}
    for rule in deny:
        pat = rule["path"]
        for p in paths:
            if p in deny_except:
                continue
            if matches(p, pat):
                violations.append(
                    f"PRIVATE path is tracked: {p}\n"
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
                f"UNCLASSIFIED path: {p}\n"
                f"    No allow rule matches it, so it is rejected by default.\n"
                f"    Classify it in {MANIFEST} before adding it."
            )

    # ── 3. Forbidden content ─────────────────────────────────────────────────
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
        for p in paths:
            if any(matches(p, e) for e in exempt):
                continue
            try:
                text = Path(p).read_text(errors="ignore")
            except (OSError, UnicodeDecodeError):
                continue  # binary or unreadable; content rules are text-only
            for n, line in enumerate(text.splitlines(), 1):
                if pattern.search(line):
                    violations.append(
                        f"FORBIDDEN CONTENT [{rid}]: {p}:{n}\n"
                        f"    {line.strip()[:100]}"
                    )
                    break  # one hit per file is enough to fail it

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
        for p in paths:
            if (
                p == str(MANIFEST)
                or p == "scripts/publication-boundary-check.py"
                or p in token_allow_paths
            ):
                continue  # these two describe the rule; they carry no plaintext token
            try:
                text = Path(p).read_text(errors="ignore")
            except (OSError, UnicodeDecodeError):
                continue
            for n, line in enumerate(text.splitlines(), 1):
                if _line_has_denied_token(line, word, salt, token_hashes):
                    violations.append(
                        f"FORBIDDEN TOKEN: {p}:{n}\n"
                        f"    A token on this line is on the private-identifier denylist.\n"
                        f"    It is matched by hash, so it is not named here. See\n"
                        f"    nightgauge-internal (strategy/) for the plaintext list."
                    )
                    break  # one hit per file is enough to fail it

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
    added = list(added_lines(base))
    for p, n, line in added:
        if any(matches(p, e) for e in ref_exempt):
            continue
        for num, token in unresolvable_refs(line, ceiling):
            violations.append(
                f"UNRESOLVABLE ISSUE REFERENCE: {p}:{n}\n"
                f"    {token} is above this repository's high-water mark "
                f"({mark} + slack {slack} = {ceiling}), so it cannot resolve here.\n"
                f"    {line.strip()[:100]}\n"
                f"    Cite the real issue, drop the '#', or -- if #{num} genuinely exists\n"
                f"    now -- bump issue_references.high_water_mark in {MANIFEST}.\n"
                f"    (measured against {base_ref} @ {base[:12]})"
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

    print(f"\033[32m✓ publication boundary clean\033[0m — {len(paths)} tracked paths, "
          f"all classified; no denied paths, no forbidden content, no open decisions.")
    print(f"  issue references: {len(added)} added line(s) over {base_ref} "
          f"({base[:12]}) carry no #N above {ceiling}.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except subprocess.CalledProcessError as exc:
        die(2, f"git failed: {exc}. Failing closed.")
    except Exception as exc:  # noqa: BLE001 — fail closed on ANYTHING
        die(2, f"unexpected error: {exc!r}. Failing closed rather than passing blind.")
