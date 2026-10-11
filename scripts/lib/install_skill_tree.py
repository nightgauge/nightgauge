#!/usr/bin/env python3
"""Copy Nightgauge skills into an agent's skills directory, safely (#1694).

Used by scripts/install-agent-skills.sh. The rules are the VS Code extension's
copySkillsTree (packages/nightgauge-vscode/src/services/bundledSkills.ts), so
either installer may replace what the other installed and neither touches
anything else:

  install --source SKILLS --dest ROOT --checkout REPO --label NAME
      Install every skill directory (one with a SKILL.md) and _shared/ from
      SKILLS into ROOT. A destination directory is replaced only when it is
      absent or is a real directory holding the install marker; anything else
      (an operator's own `update-docs`, another pack's `_shared`, a symlink, a
      file) is left untouched and named in a warning. ROOT is refused when it
      resolves into REPO or contains it. Prints the number installed.

  copy --source SKILLS --from DIR --to DIR
      Replace DIR (--to) with a contained copy of DIR (--from), which lies in
      SKILLS. The caller owns --to and has refused a symlink there.

Both copy only what is inside SKILLS: a symlink is installed only when it
resolves to a regular file inside the tree, and it lands as a regular file. A
link that escapes the tree (to ~/.ssh, say), dangles, or names a directory is
skipped and named in a warning. Warnings go to stderr.
"""

import argparse
import os
import shutil
import stat
import sys

INSTALL_MARKER = ".nightgauge-installed"
INSTALL_MARKER_TEXT = (
    "Installed by Nightgauge, which replaces this directory when it installs "
    "its skills again.\n"
)


def warn(label, message):
    print(f"    WARNING ({label}): {message}", file=sys.stderr)


def within(root, candidate):
    rel = os.path.relpath(candidate, root)
    return rel == "." or not (rel == os.pardir or rel.startswith(os.pardir + os.sep))


def realpath_nearest(path):
    """The real path of path, or of its nearest existing ancestor joined with
    the rest: where a write to path would land."""
    rest = []
    current = os.path.abspath(path)
    while not os.path.lexists(current):
        parent = os.path.dirname(current)
        if parent == current:
            break
        rest.insert(0, os.path.basename(current))
        current = parent
    return os.path.join(os.path.realpath(current), *rest)


def resolve_contained(path, root, label):
    """(real path, stat) for a real directory, a regular file, or a link to a
    regular file inside root; None (with a warning) for anything else."""
    try:
        st = os.lstat(path)
    except FileNotFoundError:
        return None
    real = path
    if stat.S_ISLNK(st.st_mode):
        target = os.readlink(path)
        resolved = os.path.realpath(path)
        if not os.path.exists(resolved) or not within(root, resolved):
            warn(label, f"skipped symlink {path} -> {target}: it does not resolve inside {root}.")
            return None
        st = os.stat(resolved)
        if stat.S_ISDIR(st.st_mode):
            warn(label, f"skipped symlink {path} -> {target}: "
                 "Nightgauge copies linked files, never linked directories.")
            return None
        real = resolved
    if not (stat.S_ISREG(st.st_mode) or stat.S_ISDIR(st.st_mode)):
        warn(label, f"skipped {path}: it is not a regular file or directory.")
        return None
    return real, st


def copy_contained(source_dir, target_dir, root, label):
    """Copy a real directory's contents into target_dir (which exists). The
    marker name belongs to the installer, so a source entry by that name is
    never copied over it."""
    for name in sorted(os.listdir(source_dir)):
        if name == INSTALL_MARKER:
            continue
        entry = resolve_contained(os.path.join(source_dir, name), root, label)
        if entry is None:
            continue
        real, st = entry
        target = os.path.join(target_dir, name)
        if stat.S_ISDIR(st.st_mode):
            os.mkdir(target)
            copy_contained(real, target, root, label)
        else:
            shutil.copy2(real, target)


def may_replace(target):
    """True when nothing is at target, or it is a real directory holding the
    install marker. A symlink or a file is always someone else's."""
    try:
        st = os.lstat(target)
    except FileNotFoundError:
        return True
    if not stat.S_ISDIR(st.st_mode):
        return False
    try:
        return stat.S_ISREG(os.lstat(os.path.join(target, INSTALL_MARKER)).st_mode)
    except FileNotFoundError:
        return False


def install(args):
    root = os.path.realpath(args.source)
    checkout = os.path.realpath(args.checkout)
    real_dest = realpath_nearest(args.dest)
    for other in (checkout, root):
        if within(other, real_dest) or within(real_dest, other):
            print(f"==> {args.label}: refusing to install into {args.dest}: it resolves to "
                  f"{real_dest}, which overlaps {other}. Nothing was written.", file=sys.stderr)
            return 1
    os.makedirs(args.dest, exist_ok=True)
    count = 0
    for name in sorted(os.listdir(root)):
        entry_path = os.path.join(root, name)
        entry = resolve_contained(entry_path, root, args.label)
        if entry is None or not stat.S_ISDIR(entry[1].st_mode):
            continue
        if name != "_shared":
            skill = resolve_contained(os.path.join(entry_path, "SKILL.md"), root, args.label)
            if skill is None or not stat.S_ISREG(skill[1].st_mode):
                continue
        target = os.path.join(args.dest, name)
        if not may_replace(target):
            warn(args.label, f"skipped {name}: {target} is not a directory Nightgauge installed "
                 f"(no {INSTALL_MARKER}), so it is left untouched. Remove it to let "
                 f"Nightgauge install {name}.")
            continue
        if os.path.lexists(target):
            shutil.rmtree(target)
        os.mkdir(target)
        # The marker goes in first, so even a partial install is recognised
        # as Nightgauge's next time.
        with open(os.path.join(target, INSTALL_MARKER), "w", encoding="utf-8") as f:
            f.write(INSTALL_MARKER_TEXT)
        copy_contained(entry[0], target, root, args.label)
        count += 1
    print(count)
    return 0


def copy(args):
    root = os.path.realpath(args.source)
    source = os.path.realpath(args.from_dir)
    if not within(root, source):
        print(f"refusing to copy {args.from_dir}: it is not inside {root}.", file=sys.stderr)
        return 1
    if os.path.islink(args.to_dir):
        print(f"refusing to write {args.to_dir}: it is a symlink.", file=sys.stderr)
        return 1
    if os.path.lexists(args.to_dir):
        shutil.rmtree(args.to_dir)
    os.makedirs(args.to_dir)
    copy_contained(source, args.to_dir, root, args.label)
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)
    p_install = sub.add_parser("install")
    p_install.add_argument("--source", required=True)
    p_install.add_argument("--dest", required=True)
    p_install.add_argument("--checkout", required=True)
    p_install.add_argument("--label", required=True)
    p_install.set_defaults(func=install)
    p_copy = sub.add_parser("copy")
    p_copy.add_argument("--source", required=True)
    p_copy.add_argument("--from", dest="from_dir", required=True)
    p_copy.add_argument("--to", dest="to_dir", required=True)
    p_copy.add_argument("--label", default="OpenCode")
    p_copy.set_defaults(func=copy)
    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
