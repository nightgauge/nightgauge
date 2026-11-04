#!/usr/bin/env python3
"""Expand include directives in the generated plugin skills mirror (#2764).

Canonical skills pull shared procedure in with `<!-- include: ../_shared/X.md -->`.
The pipeline expands those when it renders a stage (`skillrender.ExpandIncludes`),
but Claude Code loads a plugin skill as-is: run interactively, every directive
reached the model as a literal HTML comment where the procedure should have
been, and the one logged attempt to follow one by hand read the file through
`head -80` and lost the rest.

The plugin mirror is never a render source (Claude Code's loader reads it
directly), so the generator expands each directive in place. Semantics follow
`ExpandIncludes`, so a skill reads the same interactively as in a stage:

  - the pattern is `skillrender.IncludePattern`, `<!-- include: (.+?) -->`;
  - one pass: text pulled in by an include is not itself expanded;
  - the target resolves against the skill's own directory (where SKILL.md is),
    including for a directive in a subdirectory file such as `_profiles/`;
  - a target that cannot be read is an error naming every missing target.

Directives inside fenced or inline code are left alone: they are prose about the
syntax (a changelog quoting one), not a request to include.

The included text's relative links are written for the included file's
directory. Before insertion they are re-based to the including file's directory
(both canonical), so the mirror's link rewrite, which runs next, treats them
exactly like the includer's own links.

Run: python3 scripts/lib/mirror_includes.py --dest DIR --name NAME --skills-src DIR
"""

from __future__ import annotations

import argparse
import os
import posixpath
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from mirror_links import FENCE_RE, SKILLS_REL, _inline_code_spans, rebase_text  # noqa: E402

# Keep in step with internal/skillrender/render.go IncludePattern.
INCLUDE_RE = re.compile(r"<!-- include: (.+?) -->")


def _code_free_matches(text: str):
    """Yield include matches that sit outside fenced and inline code."""
    offset = 0
    fence: str | None = None
    for line in text.splitlines(keepends=True):
        stripped = line.rstrip("\n")
        m = FENCE_RE.match(stripped)
        if fence is not None:
            if m and m.group(1)[0] == fence[0] and len(m.group(1)) >= len(fence):
                fence = None
        elif m:
            fence = m.group(1)
        else:
            spans = _inline_code_spans(stripped)
            for im in INCLUDE_RE.finditer(stripped):
                if any(a <= im.start() < b for a, b in spans):
                    continue
                yield offset + im.start(), offset + im.end(), im.group(1).strip()
        offset += len(line)


def expand_text(
    text: str, skill_src: str, skill_canon: str, file_canon_dir: str, missing: list[str]
) -> str:
    """Expand every directive in `text`.

    `skill_src` is the canonical skill directory on disk; `skill_canon` and
    `file_canon_dir` are repo-relative canonical directories of the skill and
    of the file holding the directives. Unreadable targets are appended to
    `missing` and their directive is left in place.
    """
    out: list[str] = []
    cursor = 0
    for start, end, target in _code_free_matches(text):
        path = os.path.normpath(os.path.join(skill_src, target))
        try:
            with open(path, encoding="utf-8") as fh:
                included = fh.read()
        except OSError as err:
            missing.append(f"{skill_canon}: include {target!r}: {err.strerror}")
            continue
        inc_dir = posixpath.dirname(
            posixpath.normpath(posixpath.join(skill_canon, target))
        )
        out.append(text[cursor:start])
        out.append(rebase_text(included, inc_dir, file_canon_dir))
        cursor = end
    out.append(text[cursor:])
    return "".join(out)


def expand_tree(dest_root: str, canonical_name: str, skills_src: str) -> list[str]:
    """Expand directives in every markdown file of one mirrored skill directory.

    Returns the list of missing targets (empty on success).
    """
    skill_src = os.path.join(skills_src, canonical_name)
    skill_canon = posixpath.join(SKILLS_REL, canonical_name)
    missing: list[str] = []
    for root, _dirs, files in os.walk(dest_root):
        rel = os.path.relpath(root, dest_root).replace(os.sep, "/")
        file_dir = skill_canon if rel == "." else posixpath.join(skill_canon, rel)
        for fname in sorted(files):
            if not fname.endswith(".md"):
                continue
            full = os.path.join(root, fname)
            with open(full, encoding="utf-8") as fh:
                text = fh.read()
            new = expand_text(text, skill_src, skill_canon, file_dir, missing)
            if new != text:
                with open(full, "w", encoding="utf-8") as fh:
                    fh.write(new)
    return missing


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--dest", required=True, help="mirrored skill directory on disk"
    )
    parser.add_argument(
        "--name", required=True, help="canonical skills/<name> directory name"
    )
    parser.add_argument(
        "--skills-src", required=True, help="canonical skills/ directory"
    )
    args = parser.parse_args(argv)
    missing = expand_tree(args.dest, args.name, args.skills_src)
    if missing:
        print("x include expansion failed; missing targets:", file=sys.stderr)
        for line in missing:
            print(f"  {line}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
