#!/usr/bin/env bash
#
# install-publication-push-hook.sh: make the publication push guard (#2365) run
# for every push from every worktree of this clone, whatever is checked out.
#
# npm install runs this after husky (package.json "prepare"), and so does
# `npm run setup-hooks`. husky alone leaves two gaps:
#
#   * husky sets core.hooksPath to the RELATIVE path .husky/_, which git
#     resolves in each worktree separately. A linked worktree where npm install
#     never ran, such as every worktree the pipeline creates, has no hooks.
#   * husky's runner (.husky/_/h) exits 0 when the checked-out tree has no
#     .husky/<hook>. An orphan branch, another repository's history, or any
#     commit older than the guard has none, and those are exactly the
#     histories the guard exists to stop.
#
# So this writes a hook directory into the clone's SHARED git directory and
# points core.hooksPath at it, by absolute path:
#
#   <git-common-dir>/nightgauge-hooks/
#     pre-push                    the guard, then the worktree's husky pre-push
#     publication-push-guard.sh   the guard to run when the tree has none
#     <every other husky hook>    the worktree's husky hook, when there is one
#
# Every hook but pre-push behaves exactly as husky's relative path did: it runs
# the worktree's .husky/_/<hook> when npm install set husky up there, and does
# nothing otherwise. pre-push first runs the guard: the checked-out tree's
# scripts/publication-push-guard.sh when there is one, else the copy installed
# here. It then runs the worktree's husky pre-push, if any, which skips the
# guard it has just run.
#
# Re-running is safe. An npm install in a checkout that predates this script
# puts husky's relative path back, and the next npm install in a current one
# restores this. The path is absolute, so a clone moved to another directory
# runs no hooks at all until this runs again. It is not git's own hooks
# directory, because `nightgauge pre-push install` writes .git/hooks/pre-push.
# HUSKY=0 skips this, as it skips husky.
#
# Usage: bash scripts/install-publication-push-hook.sh

set -euo pipefail

say() { printf 'publication hook: %s\n' "$*"; }

if [ "${HUSKY:-}" = "0" ]; then
  say "HUSKY=0, so not installed."
  exit 0
fi

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
guard="$here/publication-push-guard.sh"
if [ ! -f "$guard" ]; then
  echo "publication hook: $guard is missing; cannot install." >&2
  exit 1
fi

cd "$here/.."
if [ "$(git rev-parse --is-inside-work-tree 2>/dev/null)" != "true" ]; then
  say "not a git checkout, so nothing to install."
  exit 0
fi

dir="$(git rev-parse --path-format=absolute --git-common-dir)/nightgauge-hooks"
current="$(git config --local --get core.hooksPath || true)"
case "$current" in
  "" | .husky/_ | "$dir") ;;
  *)
    echo "publication hook: core.hooksPath is '$current', not husky's .husky/_," >&2
    echo "  so the publication guard was not installed. Run 'npx husky' and then" >&2
    echo "  this script again." >&2
    exit 1
    ;;
esac

# The hook names husky 9 installs, so that every hook husky would run still runs.
hooks="pre-commit pre-merge-commit prepare-commit-msg commit-msg post-commit
applypatch-msg pre-applypatch post-applypatch pre-rebase post-rewrite
post-checkout post-merge pre-push pre-auto-gc"

mkdir -p "$dir"

# put <name>: install stdin as <dir>/<name>, executable, by rename, so that a
# hook git starts while another npm install runs never reads half a file.
put() {
  local tmp
  tmp="$(mktemp "$dir/.$1.XXXXXX")"
  cat >"$tmp"
  chmod 755 "$tmp"
  mv -f "$tmp" "$dir/$1"
}

put publication-push-guard.sh <"$guard"

for h in $hooks; do
  if [ "$h" = "pre-push" ]; then
    put pre-push <<'EOF'
#!/usr/bin/env sh
# Installed by scripts/install-publication-push-hook.sh (#2365); re-run it, or
# npm install, rather than editing this.
#
# The publication guard for a push from any worktree of this clone, whatever
# is checked out: the tree's own copy when it has one, else the copy installed
# beside this file. Then this worktree's husky pre-push hook, when npm install
# set husky up here. NG_PUBLICATION_GUARD_RAN, this process's id, tells
# .husky/pre-push that the guard has already run.
guard=scripts/publication-push-guard.sh
[ -f "$guard" ] || guard="$(dirname "$0")/publication-push-guard.sh"
if [ ! -f "$guard" ]; then
  echo "publication guard: $guard is missing, so this push cannot be checked." >&2
  echo "  Run npm install, or bash scripts/install-publication-push-hook.sh." >&2
  exit 2
fi
refs=$(cat)
bash "$guard" "$@" <<REFS || exit $?
$refs
REFS
[ -f .husky/_/pre-push ] || exit 0
NG_PUBLICATION_GUARD_RAN=$$
export NG_PUBLICATION_GUARD_RAN
exec sh .husky/_/pre-push "$@" <<REFS
$refs
REFS
EOF
  else
    put "$h" <<'EOF'
#!/usr/bin/env sh
# Installed by scripts/install-publication-push-hook.sh (#2365). Runs this
# worktree's husky hook when npm install set husky up here, as husky's own
# relative hooks path did, and nothing otherwise.
h=".husky/_/${0##*/}"
[ -f "$h" ] || exit 0
exec sh "$h" "$@"
EOF
  fi
done

git config core.hooksPath "$dir"
say "installed in $dir for every worktree of this clone."
