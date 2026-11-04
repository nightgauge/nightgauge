#!/usr/bin/env python3
"""Relative-link handling for the generated plugin skills mirror (#831).

`scripts/install-agent-skills.sh` mirrors every canonical skill from
`skills/<name>/` to `claude-plugins/nightgauge/skills/<short>/`. That move
changes two things a verbatim copy cannot survive:

  1. **Depth.** `skills/<name>/SKILL.md` is two levels below the repo root, so
     it reaches docs as `../../docs/X.md`. The mirrored copy is *four* levels
     down, where `../../` lands on `claude-plugins/nightgauge/` — a directory
     with no `docs/` in it. Every such link was dead in the mirror.
  2. **Directory names.** The mirror strips the `nightgauge-` prefix, so a
     canonical sibling reference like `../nightgauge-issue-audit/SKILL.md` names
     a directory that does not exist under the mirror either.

This module is the single implementation of the mapping, shared by the
generator (which rewrites links on the way in) and by
`scripts/check-mirror-links.py` (which asserts, independently of the generator,
that what landed in the mirror resolves on disk).

A third problem is that the shipped tree is not the repository (#2764). An
installed plugin has no `docs/` beside it, so even a correctly re-based
`../../../../docs/X.md` names nothing on a user's machine. Every target that
lands outside the shipped root becomes a GitHub URL instead, pinned to a ref:
`main` for the committed mirror (the marketplace installs it from main; a tag
would make main's committed mirror drift after every release), and the release
tag for the VSIX's `dist/skills` (see `--identity`).

Rewriting is a pure function of paths — it never consults the filesystem — so
the generator produces identical output whether it writes the real mirror or a
throwaway directory for the drift gate.
"""

from __future__ import annotations

import argparse
import os
import posixpath
import re
import subprocess
import sys

# Repo-root-relative location of the generated mirror.
MIRROR_REL = "claude-plugins/nightgauge/skills"
SKILLS_REL = "skills"
# The root an installed plugin carries: anything outside it is not on disk.
PLUGIN_ROOT_REL = "claude-plugins/nightgauge"
REPO_URL = "https://github.com/nightgauge/nightgauge"
DEFAULT_REF = "main"

# A markdown inline link/image target: the `](...)` half. Targets containing
# whitespace or parentheses are left alone — they are almost always prose or a
# regex rather than a path.
LINK_RE = re.compile(r"\]\(([^()\s]+)\)")

# An opening/closing fence, per CommonMark: three or more backticks or tildes,
# indented at most three spaces.
FENCE_RE = re.compile(r"^\s{0,3}(`{3,}|~{3,})")

# Anything with a URI scheme, a page-local anchor, or a root-absolute path is
# not a relative path into this repository.
_NON_RELATIVE_RE = re.compile(r"^([a-zA-Z][a-zA-Z0-9+.\-]*:|#|/|//)")


def short_name(name: str) -> str:
    """Canonical skill directory name -> mirrored directory name."""
    return name[len("nightgauge-"):] if name.startswith("nightgauge-") else name


def mirror_of(repo_rel: str) -> str:
    """Map a repo-root-relative path to where it lives after mirroring.

    Paths under `skills/` are mirrored (with the prefix stripped from the skill
    directory); everything else — `docs/`, `.github/`, root files — is not
    mirrored and keeps its repo path.
    """
    parts = repo_rel.split("/")
    if parts[0] != SKILLS_REL or len(parts) < 2:
        return repo_rel
    parts[1] = short_name(parts[1])
    return posixpath.join(MIRROR_REL, *parts[1:])


def is_relative_target(target: str) -> bool:
    return bool(target) and not _NON_RELATIVE_RE.match(target)


def split_fragment(target: str) -> tuple[str, str]:
    """Split `path#anchor` into (path, '#anchor')."""
    idx = target.find("#")
    if idx < 0:
        return target, ""
    return target[:idx], target[idx:]


def iter_links(text: str):
    """Yield (offset, target) for every link target outside code.

    Fenced code blocks and inline code spans are skipped: they carry regexes and
    templated placeholders that look like links but are not, and rewriting inside
    a fence would corrupt an example the skill is deliberately showing verbatim.
    Both the rewriter and the checker use this, so the two agree by construction
    on what counts as a link.
    """
    offset = 0
    fence: str | None = None
    for line in text.splitlines(keepends=True):
        stripped = line.rstrip("\n")
        m = FENCE_RE.match(stripped)
        if fence is not None:
            if m and m.group(1)[0] == fence[0] and len(m.group(1)) >= len(fence):
                fence = None
            offset += len(line)
            continue
        if m:
            fence = m.group(1)
            offset += len(line)
            continue
        code_spans = _inline_code_spans(stripped)
        for lm in LINK_RE.finditer(stripped):
            start = lm.start(1)
            if any(a <= start < b for a, b in code_spans):
                continue
            yield offset + start, lm.group(1)
        offset += len(line)


def _inline_code_spans(line: str) -> list[tuple[int, int]]:
    """Half-open [start, end) spans covering inline code runs on one line."""
    spans: list[tuple[int, int]] = []
    for m in re.finditer(r"(`+)(?:(?!\1).)*\1", line):
        spans.append((m.start(), m.end()))
    return spans


def within(path: str, root: str) -> bool:
    """True when repo-relative `path` is `root` or below it."""
    return path == root or path.startswith(root.rstrip("/") + "/")


def github_url(repo_rel: str, ref: str) -> str:
    return f"{REPO_URL}/blob/{ref}/{repo_rel}"


def rewrite_target(
    target: str,
    src_dir: str,
    dest_dir: str,
    root: str = PLUGIN_ROOT_REL,
    ref: str = DEFAULT_REF,
    mapper=mirror_of,
) -> str:
    """Rewrite one link target from `src_dir` (canonical) to `dest_dir` (shipped).

    Both directories are repo-root-relative POSIX paths. `mapper` maps a
    canonical repo path to where it ships. A target that ships outside `root`
    becomes a GitHub URL at `ref`. Returns the target unchanged when it is not
    a relative path, or when it escapes the repository altogether.
    """
    if not is_relative_target(target):
        return target
    path, fragment = split_fragment(target)
    if not path:
        return target
    resolved = posixpath.normpath(posixpath.join(src_dir, path))
    if resolved.startswith("../") or resolved == "..":
        return target
    shipped = mapper(resolved)
    if not within(shipped, root):
        return github_url(resolved, ref) + fragment
    return posixpath.relpath(shipped, dest_dir) + fragment


def rebase_text(text: str, src_dir: str, dest_dir: str) -> str:
    """Re-base relative links in `text` from `src_dir` to `dest_dir`.

    Both are canonical repo-relative directories, so nothing is mirrored or
    turned into a URL: this moves text written beside one canonical file to
    beside another, as include expansion does, before the mirror rewrite.
    """
    out: list[str] = []
    cursor = 0
    for offset, target in iter_links(text):
        if not is_relative_target(target):
            continue
        path, fragment = split_fragment(target)
        if not path:
            continue
        resolved = posixpath.normpath(posixpath.join(src_dir, path))
        new = posixpath.relpath(resolved, dest_dir) + fragment
        if new == target:
            continue
        out.append(text[cursor:offset])
        out.append(new)
        cursor = offset + len(target)
    out.append(text[cursor:])
    return "".join(out)


def rewrite_text(
    text: str,
    src_dir: str,
    dest_dir: str,
    root: str = PLUGIN_ROOT_REL,
    ref: str = DEFAULT_REF,
    mapper=mirror_of,
) -> str:
    out: list[str] = []
    cursor = 0
    for offset, target in iter_links(text):
        new = rewrite_target(target, src_dir, dest_dir, root, ref, mapper)
        if new == target:
            continue
        out.append(text[cursor:offset])
        out.append(new)
        cursor = offset + len(target)
    out.append(text[cursor:])
    return "".join(out)


def rewrite_tree(dest_root: str, canonical_name: str) -> int:
    """Rewrite links in every markdown file of one mirrored skill directory.

    `dest_root` is where the copy physically lives (possibly a temp directory);
    the link math uses the skill's *logical* mirror path, so output does not
    depend on where the generator happened to write.
    """
    src_base = posixpath.join(SKILLS_REL, canonical_name)
    dest_base = posixpath.join(MIRROR_REL, short_name(canonical_name))
    changed = 0
    for root, _dirs, files in os.walk(dest_root):
        rel = os.path.relpath(root, dest_root).replace(os.sep, "/")
        rel = "" if rel == "." else rel
        for fname in files:
            if not fname.endswith(".md"):
                continue
            full = os.path.join(root, fname)
            with open(full, encoding="utf-8") as fh:
                text = fh.read()
            new = rewrite_text(
                text,
                posixpath.join(src_base, rel) if rel else src_base,
                posixpath.join(dest_base, rel) if rel else dest_base,
            )
            if new != text:
                with open(full, "w", encoding="utf-8") as fh:
                    fh.write(new)
                changed += 1
    return changed


def _identity(path: str) -> str:
    return path


def rewrite_identity_dir(dest_dir: str, canon_dir: str, ref: str) -> int:
    """Contain links in one directory copied verbatim from `canon_dir`.

    `canon_dir` is the repo-relative canonical path the copy came from
    (`skills/nightgauge-retro`, `skills/_shared`). Links that stay inside
    `skills/` already resolve in a bundle that keeps the canonical layout;
    only targets outside it become URLs at `ref`.
    """
    changed = 0
    for root, _dirs, files in os.walk(dest_dir):
        rel = os.path.relpath(root, dest_dir).replace(os.sep, "/")
        canon = canon_dir if rel == "." else posixpath.join(canon_dir, rel)
        for fname in files:
            if not fname.endswith(".md"):
                continue
            full = os.path.join(root, fname)
            with open(full, encoding="utf-8") as fh:
                text = fh.read()
            new = rewrite_text(text, canon, canon, SKILLS_REL, ref, _identity)
            if new != text:
                with open(full, "w", encoding="utf-8") as fh:
                    fh.write(new)
                changed += 1
    return changed


def rewrite_identity_tree(dest_root: str, ref: str) -> int:
    """Contain links in a whole bundle in the canonical layout (`dist/skills`)."""
    return rewrite_identity_dir(dest_root, SKILLS_REL, ref)


def release_ref(checkout: str) -> str:
    """The tag HEAD sits on exactly, else `main` — the ref shipped links use."""
    # A caller's GIT_DIR (a hook, a worktree script) would answer for another
    # repository; ask about the checkout named.
    env = {k: v for k, v in os.environ.items()
           if k not in ("GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE")}
    try:
        out = subprocess.run(
            ["git", "-C", checkout, "describe", "--tags", "--exact-match", "HEAD"],
            capture_output=True, text=True, check=False, env=env,
        )
    except OSError:
        return DEFAULT_REF
    tag = out.stdout.strip()
    return tag if out.returncode == 0 and tag else DEFAULT_REF


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dest", required=True, help="shipped skill directory on disk")
    parser.add_argument("--name", help="canonical skills/<name> directory name")
    parser.add_argument(
        "--identity",
        action="store_true",
        help="--dest is a whole bundle in the canonical skills/ layout (dist/skills)",
    )
    parser.add_argument("--ref", default=DEFAULT_REF, help="git ref for out-of-root URLs")
    args = parser.parse_args(argv)
    if args.identity:
        rewrite_identity_tree(args.dest, args.ref)
        return 0
    if not args.name:
        parser.error("--name is required without --identity")
    rewrite_tree(args.dest, args.name)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
