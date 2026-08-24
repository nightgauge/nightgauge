#!/usr/bin/env python3
"""Assert that relative links in the plugin skills mirror resolve on disk (#831).

WHY THIS EXISTS ALONGSIDE THE DRIFT GATE
`install-agent-skills.sh --check-mirror` compares the committed mirror against
the generator's own output. When the generator copied links verbatim, both sides
carried the same ~90 dead `../../docs/...` links and the gate was green *by
construction*: it verifies the copy, never the target. This gate asks a question
the generator cannot answer for itself — does the path in the mirrored file name
a file that exists? — so it fails against a mirror the drift gate calls clean.

WHAT IS CHECKED
Every relative markdown link target in `claude-plugins/nightgauge/skills/**/*.md`
outside fenced/inline code, resolved against the file's own directory, must name
an existing path in this repository. Absolute URLs, `mailto:`, page anchors and
image assets are not this gate's business.

Links that deliberately do not name a file here — a placeholder a reader will
create, or template prose emitted into a CONSUMING repository — are listed in
EXEMPT, keyed by (file, exact target) so an exemption cannot leak. Keep that list
short and justified; a wildcard would make this gate as vacuous as the one it
supplements.

Run: python3 scripts/check-mirror-links.py [--mirror DIR]
Wired into .github/workflows/lint.yml and scripts/ci-local.sh.
"""

from __future__ import annotations

import argparse
import os
import posixpath
import subprocess
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"))

from mirror_links import (  # noqa: E402
    MIRROR_REL,
    is_relative_target,
    iter_links,
    split_fragment,
)

# Links that intentionally do not name a file in THIS repository. Keyed by
# (mirror-relative file, exact target) so an exemption cannot leak to another
# file, and kept short — a wildcard here would turn this gate back into the
# vacuous one it replaces.
EXEMPT = {
    # Prose telling the reader where the skill will WRITE an assessment; the
    # bracketed segment is a placeholder, not a path.
    ("release-watch/auto-issue-creation.md", "./assessments/[feature].md"),
    (
        "release-watch/auto-issue-creation.md",
        "../release-watch/assessments/[feature-name].md",
    ),
    # smart-setup emits AGENTS.md content INTO A CONSUMING REPOSITORY. These
    # targets are relative to that repo's root, not to this one, so there is
    # nothing here for them to resolve against.
    ("smart-setup/SKILL.md", "docs/GIT_WORKFLOW.md"),
    ("smart-setup/SKILL.md", "docs/SECURITY_AND_ERROR_HANDLING.md"),
}

# Binary assets are out of scope for a reference-integrity gate, matching
# .markdown-link-check.json.
ASSET_SUFFIXES = (".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp")


def repo_root() -> str:
    return subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], text=True
    ).strip()


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--mirror",
        default=None,
        help="mirror directory to check (default: the committed mirror)",
    )
    args = parser.parse_args(argv)

    root = repo_root()
    mirror = args.mirror or os.path.join(root, MIRROR_REL)
    if not os.path.isdir(mirror):
        print(f"x mirror directory not found: {mirror}", file=sys.stderr)
        return 1

    checked = 0
    dead: list[str] = []
    for dirpath, _dirs, files in os.walk(mirror):
        for fname in sorted(files):
            if not fname.endswith(".md"):
                continue
            full = os.path.join(dirpath, fname)
            # Reported path is always mirror-relative: with --mirror pointing
            # at a temp tree (the self-test), a repo-relative path is a wall of
            # `../` that names nothing a reader can act on.
            mirror_rel = os.path.relpath(full, mirror).replace(os.sep, "/")
            rel = posixpath.join(MIRROR_REL, mirror_rel)
            with open(full, encoding="utf-8") as fh:
                text = fh.read()
            for _offset, target in iter_links(text):
                if not is_relative_target(target):
                    continue
                path, _fragment = split_fragment(target)
                if not path or (mirror_rel, target) in EXEMPT:
                    continue
                if path.lower().endswith(ASSET_SUFFIXES):
                    continue
                checked += 1
                resolved = posixpath.normpath(
                    posixpath.join(os.path.dirname(full).replace(os.sep, "/"), path)
                )
                if not os.path.exists(resolved):
                    dead.append(f"  {rel}: {target}")

    print("")
    print("-" * 73)
    if not dead:
        print(f"+ Mirror link check passed - {checked} relative links, all resolve.")
        return 0

    print(f"x Mirror link check found {len(dead)} dead relative links:")
    for line in dead:
        print(line)
    print("")
    print("These files are GENERATED. Fix the canonical source under skills/ or the")
    print("link rewrite in scripts/lib/mirror_links.py, then regenerate with:")
    print("  bash scripts/install-agent-skills.sh --generate-only")
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
