"""
clone_layout.py — where per-clone pipeline data lives (ADR-024 § 7).

The per-clone classes (pipeline, plans, retros, logs) live under
``<git-common-dir>/nightgauge/<class>``: a linked worktree resolves to its main
clone's directory. The directory is resolved with git, the command the Go
resolver runs, so the retro engine works without the nightgauge binary.
Outside a git repository resolution raises :class:`NotAGitRepositoryError`;
nothing falls back to a working-tree path.

No external dependencies. Requires Python 3.8+.
"""

from __future__ import annotations

import os
import subprocess

CLASSES = ("pipeline", "plans", "retros", "logs")

# Variables that redirect which repository git reads; cleared for the lookup.
_GIT_LOCATION_ENV = (
    "GIT_DIR",
    "GIT_WORK_TREE",
    "GIT_COMMON_DIR",
    "GIT_INDEX_FILE",
    "GIT_OBJECT_DIRECTORY",
)


class NotAGitRepositoryError(Exception):
    """Raised when a root is not inside a git repository."""


def clone_class_dir(root: str, cls: str) -> str:
    """Return the absolute directory of per-clone class *cls* for *root*."""
    if cls not in CLASSES:
        raise ValueError(f"unknown per-clone class {cls!r}; want one of {CLASSES}")
    env = {k: v for k, v in os.environ.items() if k not in _GIT_LOCATION_ENV}
    try:
        res = subprocess.run(
            ["git", "rev-parse", "--path-format=absolute", "--git-common-dir"],
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
    except OSError as exc:
        raise NotAGitRepositoryError(f"not a git repository: {root}: {exc}") from exc
    common = res.stdout.strip()
    if res.returncode != 0 or not os.path.isabs(common):
        raise NotAGitRepositoryError(
            f"not a git repository: {root} "
            "(per-clone data lives in the git directory, ADR-024 § 7)"
        )
    return os.path.join(os.path.realpath(common), "nightgauge", cls)
