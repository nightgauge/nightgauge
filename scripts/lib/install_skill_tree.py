#!/usr/bin/env python3
"""Copy Nightgauge skills into an agent's skills directory, safely (#1694).

Used by scripts/install-agent-skills.sh. The rules are the VS Code extension's
copySkillsTree (packages/nightgauge/src/services/bundledSkills.ts), so
either installer may replace what the other installed and neither touches
anything else:

  install --source SKILLS --dest ROOT --checkout REPO --label NAME [--adopt]
      Install every skill directory (one with a SKILL.md) and _shared/ from
      SKILLS into ROOT. A destination directory is replaced only when it is
      absent or is a real directory holding the install marker; anything else
      (an operator's own `update-docs`, another pack's `_shared`, a symlink, a
      file) is left untouched, named in a warning and counted as skipped. A
      real directory in ROOT that holds the marker but has no skill in SKILLS
      is removed. ROOT is refused when it resolves into REPO or contains it.

      Prints `LABEL: installed N of M, skipped K`, and `, adopted A` and
      `, removed R` when they are not zero. M is every eligible source
      directory and N includes adopted ones. Exits 0 when K is 0, 3 when the
      install finished with K > 0, and 1 when it refused or crashed.

      --adopt also replaces a marker-less real directory when its content is
      proven to be a version this repository shipped: its SKILL.md (for
      _shared, every file) is a blob in REPO's git history. The old copy moves
      to ROOT.nightgauge-adopted/<UTC-stamp>/<name>, outside ROOT, because an
      agent loads every directory in ROOT as a live skill. Anything unproven
      is left alone, named and counted as skipped. Missing or shallow history
      proves nothing, so it exits 1 before anything is written.

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
import subprocess
import sys
import time

INSTALL_MARKER = ".nightgauge-installed"
ADOPTED_SUFFIX = ".nightgauge-adopted"
EXIT_SKIPPED = 3
GIT_LOCATION_ENV = ("GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE")
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


def is_real_dir(path):
    try:
        return stat.S_ISDIR(os.lstat(path).st_mode)
    except FileNotFoundError:
        return False


def holds_marker(path):
    """True when path is a real directory holding the install marker."""
    if not is_real_dir(path):
        return False
    try:
        return stat.S_ISREG(os.lstat(os.path.join(path, INSTALL_MARKER)).st_mode)
    except FileNotFoundError:
        return False


def may_replace(target):
    """True when nothing is at target, or it is a real directory holding the
    install marker. A symlink or a file is always someone else's."""
    return not os.path.lexists(target) or holds_marker(target)


def git(checkout, *argv):
    """git's stdout, or None when it failed. A pre-push hook exports GIT_DIR,
    which would answer for the hook's repository instead of checkout."""
    env = {k: v for k, v in os.environ.items() if k not in GIT_LOCATION_ENV}
    try:
        done = subprocess.run(["git", "-C", checkout, *argv], env=env, text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=False)
    except OSError:
        return None
    return done.stdout if done.returncode == 0 else None


def shipped_blobs(checkout, label):
    """Every blob id any commit recorded for a SKILL.md or a _shared file, at
    any path, so canonical and mirror copies both match. None (with a message)
    when history cannot prove anything: a shallow clone would misreport shipped
    copies as local edits."""
    shallow = git(checkout, "rev-parse", "--is-shallow-repository")
    if shallow is None or shallow.strip() != "false":
        reason = "not a git checkout" if shallow is None else "this clone is shallow"
        print(f"==> {label}: refusing --adopt: {reason}, so {checkout} cannot prove which "
              "versions were shipped. Nothing was written.", file=sys.stderr)
        return None
    log = git(checkout, "log", "--format=", "--raw", "--no-abbrev", "--no-renames", "--",
              "*SKILL.md", "*/_shared/*")
    blobs = set()
    for line in (log or "").splitlines():
        fields = line.split()
        if len(fields) >= 4 and line.startswith(":"):
            blobs.update(fields[2:4])
    blobs = {blob for blob in blobs if blob.strip("0")}
    if not blobs:
        print(f"==> {label}: refusing --adopt: no skill history in {checkout}, so nothing can "
              "be proven shipped. Nothing was written.", file=sys.stderr)
        return None
    return blobs


def shipped_paths(target, name):
    """The files whose blobs must all be shipped for target to be adopted, or
    an empty list when target cannot be proven. _shared has no SKILL.md, so
    every file in it must match, which leaves another pack's _shared alone."""
    if name != "_shared":
        path = os.path.join(target, "SKILL.md")
        return [path] if os.path.isfile(path) and not os.path.islink(path) else []
    paths = []
    for dirpath, dirnames, filenames in os.walk(target):
        for entry in dirnames + filenames:
            path = os.path.join(dirpath, entry)
            if os.path.islink(path):
                return []
            if not os.path.isdir(path):
                paths.append(path)
    return paths


def is_shipped(target, name, blobs, checkout):
    paths = shipped_paths(target, name)
    if not paths:
        return False
    ids = git(checkout, "hash-object", "--no-filters", "--", *paths)
    return ids is not None and set(ids.split()) <= blobs


def move_aside(target, name, aside_root):
    """Rename target out of the skills root. A same-parent rename is atomic, so
    the old copy is never half gone, and it happens before the replacement."""
    os.makedirs(aside_root, exist_ok=True)
    aside = os.path.join(aside_root, name)
    if os.path.lexists(aside):
        raise FileExistsError(f"{aside} already exists; {target} was not moved")
    os.rename(target, aside)
    return aside


def prune_retired(dest, eligible, label):
    """Remove each real directory holding the marker whose skill has left the
    source. The marker is the only proof of ownership, so an unmarked stranger
    and a symlink stay."""
    removed = 0
    for name in sorted(os.listdir(dest)):
        target = os.path.join(dest, name)
        if name not in eligible and holds_marker(target):
            shutil.rmtree(target)
            print(f"    {label}: removed {name}: it is no longer in the source.", file=sys.stderr)
            removed += 1
    return removed


def summary(label, installed, total, skipped, adopted, removed):
    line = f"    {label}: installed {installed} of {total}, skipped {skipped}"
    if adopted:
        line += f", adopted {adopted}"
    if removed:
        line += f", removed {removed}"
    return line


def install(args):
    root = os.path.realpath(args.source)
    checkout = os.path.realpath(args.checkout)
    real_dest = realpath_nearest(args.dest)
    for other in (checkout, root):
        if within(other, real_dest) or within(real_dest, other):
            print(f"==> {args.label}: refusing to install into {args.dest}: it resolves to "
                  f"{real_dest}, which overlaps {other}. Nothing was written.", file=sys.stderr)
            return 1
    blobs = shipped_blobs(checkout, args.label) if args.adopt else None
    if args.adopt and blobs is None:
        return 1
    aside_root = os.path.join(real_dest + ADOPTED_SUFFIX,
                              time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()))
    os.makedirs(args.dest, exist_ok=True)
    eligible = set()
    installed = skipped = adopted = 0
    for name in sorted(os.listdir(root)):
        entry_path = os.path.join(root, name)
        entry = resolve_contained(entry_path, root, args.label)
        if entry is None or not stat.S_ISDIR(entry[1].st_mode):
            continue
        if name != "_shared":
            skill = resolve_contained(os.path.join(entry_path, "SKILL.md"), root, args.label)
            if skill is None or not stat.S_ISREG(skill[1].st_mode):
                continue
        eligible.add(name)
        target = os.path.join(args.dest, name)
        if not may_replace(target):
            if args.adopt and is_real_dir(target):
                if not is_shipped(target, name, blobs, checkout):
                    warn(args.label, f"skipped {name}: {target} has local edits (it matches "
                         "nothing this repository shipped), so it is left untouched. Remove "
                         f"it to let Nightgauge install {name}.")
                    skipped += 1
                    continue
                aside = move_aside(target, name, aside_root)
                print(f"    {args.label}: adopted {name}: old copy moved to {aside}.",
                      file=sys.stderr)
                adopted += 1
            else:
                warn(args.label, f"skipped {name}: {target} is not a directory Nightgauge "
                     f"installed (no {INSTALL_MARKER}), so it is left untouched. Remove it, or "
                     f"run with --adopt if it is an old install, to let Nightgauge install "
                     f"{name}.")
                skipped += 1
                continue
        if os.path.lexists(target):
            shutil.rmtree(target)
        os.mkdir(target)
        # The marker goes in first, so even a partial install is recognised
        # as Nightgauge's next time.
        with open(os.path.join(target, INSTALL_MARKER), "w", encoding="utf-8") as f:
            f.write(INSTALL_MARKER_TEXT)
        copy_contained(entry[0], target, root, args.label)
        installed += 1
    # With no eligible source the tree is broken, not empty of skills: pruning
    # against it would delete every Nightgauge skill.
    removed = prune_retired(args.dest, eligible, args.label) if eligible else 0
    print(summary(args.label, installed, len(eligible), skipped, adopted, removed))
    return EXIT_SKIPPED if skipped else 0


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
    p_install.add_argument("--adopt", action="store_true")
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
