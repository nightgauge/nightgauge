#!/usr/bin/env python3
"""Publication boundary guard for nightgauge/nightgauge.

Enforces .github/publication-boundary.yaml against the tracked tree.

FAIL-CLOSED. Every exit path that is not an explicit, fully-verified pass is a
failure:

  * manifest missing, unreadable, or malformed  -> FAIL (never "skip")
  * a tracked path matching no `allow` rule     -> FAIL (not warn, not ignore)
  * a tracked path matching a `deny` rule       -> FAIL
  * forbidden content outside its allow_paths   -> FAIL
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
                for tok in word.findall(line):
                    h = hashlib.sha256((salt + tok.lower()).encode()).hexdigest()
                    if h in token_hashes:
                        violations.append(
                            f"FORBIDDEN TOKEN: {p}:{n}\n"
                            f"    A token on this line is on the private-identifier denylist.\n"
                            f"    It is matched by hash, so it is not named here. See\n"
                            f"    nightgauge-internal (strategy/) for the plaintext list."
                        )
                        break
                else:
                    continue
                break  # one hit per file is enough to fail it

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
        print("The repository is PRIVATE, so nothing has leaked. This guard exists so that",
              file=sys.stderr)
        print("flipping it to public stays a one-click decision. Fix the above, or classify",
              file=sys.stderr)
        print(f"the path in {MANIFEST} if it is genuinely publishable.\n", file=sys.stderr)
        return 1

    print(f"\033[32m✓ publication boundary clean\033[0m — {len(paths)} tracked paths, "
          f"all classified; no denied paths, no forbidden content, no open decisions.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except subprocess.CalledProcessError as exc:
        die(2, f"git failed: {exc}. Failing closed.")
    except Exception as exc:  # noqa: BLE001 — fail closed on ANYTHING
        die(2, f"unexpected error: {exc!r}. Failing closed rather than passing blind.")
