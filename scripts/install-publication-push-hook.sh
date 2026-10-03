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
#     publication-push-guard.sh   the guard every push runs: the public main's
#     <every other husky hook>    the worktree's husky hook, when there is one
#
# Every hook but pre-push behaves exactly as husky's relative path did: it runs
# the worktree's .husky/_/<hook> when npm install set husky up there, and does
# nothing otherwise. pre-push first runs the guard: the copy installed here, so
# that a worktree checked out at an older commit, or one where the guard is
# being edited, does not decide; the checked-out tree's
# scripts/publication-push-guard.sh only when no copy is installed. It then runs
# the worktree's husky pre-push, if any, which skips the guard it has just run.
#
# The guard it installs is the public main's, as this clone last fetched it:
# the main of a remote whose URL names the public repository. npm install runs
# this in every checkout, the pipeline's worktrees included, and a checkout's
# own copy may be one being edited, one on an unmerged branch, or an older one;
# none of those may judge every push of the clone. A checkout's own copy is
# installed only when no remote's main has a guard and none is installed yet,
# and an installed copy is kept when no remote's main has one. Fetching a newer
# main does not reinstall; the next npm install, or `npm run setup-hooks`, does.
#
# Re-running is safe. An npm install in a checkout that predates this script
# puts husky's relative path back for the whole clone, and the pipeline runs
# npm install in every worktree it creates, so a worktree on a branch older
# than this script (an epic branch cut before it, say) does exactly that. The
# next npm install in a current checkout restores this. The path is absolute,
# so a clone moved to another directory runs no hooks at all until this runs
# again. It is not git's own hooks directory, because `nightgauge pre-push
# install` writes .git/hooks/pre-push. HUSKY=0 skips this, as it skips husky,
# and so does a package that is not the top of its own checkout: inside
# another repository, the hooks path would be that repository's.
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
# husky declines where there is no .git, and so does this: a package copied or
# unpacked into another repository is not a checkout of its own, and setting
# that repository's hooks path would turn its own hooks off.
top="$(git rev-parse --show-toplevel 2>/dev/null)" || top=""
if [ -z "$top" ] || [ "$(cd "$top" && pwd -P)" != "$(pwd -P)" ]; then
  say "$(pwd) is not the top of a git checkout, so nothing to install."
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

# public_guard: the guard on the most recent main, as this clone last fetched
# it, of the remotes whose URL names the public repository (the guard's own
# is_public_url decides that); nothing when none has one.
public_guard() {
  local name url main best=""
  for name in $(git remote); do
    url="$(git remote get-url "$name" 2>/dev/null)" || continue
    # shellcheck source=scripts/publication-push-guard.sh
    (. "$guard" && is_public_url "$url") >/dev/null 2>&1 || continue
    main="$(git rev-parse --verify --quiet "refs/remotes/$name/main^{commit}")" || continue
    git cat-file -e "$main:scripts/publication-push-guard.sh" 2>/dev/null || continue
    if [ -z "$best" ] || git merge-base --is-ancestor "$best" "$main"; then
      best="$main"
    fi
  done
  [ -n "$best" ] && git rev-parse --verify --quiet "$best:scripts/publication-push-guard.sh"
}

if blob="$(public_guard)" && [ -n "$blob" ]; then
  git cat-file blob "$blob" | put publication-push-guard.sh
  which="the public main's guard"
elif [ -f "$dir/publication-push-guard.sh" ]; then
  which="the guard already installed (no remote's main has one)"
else
  put publication-push-guard.sh <"$guard"
  which="this checkout's guard (no remote's main has one)"
fi

for h in $hooks; do
  if [ "$h" = "pre-push" ]; then
    put pre-push <<'EOF'
#!/usr/bin/env sh
# Installed by scripts/install-publication-push-hook.sh (#2365); re-run it, or
# npm install, rather than editing this.
#
# The publication guard for a push from any worktree of this clone, whatever
# is checked out: the copy installed beside this file, which every npm install
# refreshes, so an older or edited copy in the worktree does not decide; the
# worktree's own copy only when none is installed. Then this worktree's husky
# pre-push hook, when npm install set husky up here.
# NG_PUBLICATION_GUARD_RAN, this process's id, tells .husky/pre-push that the
# guard has already run.
guard="$(dirname "$0")/publication-push-guard.sh"
[ -f "$guard" ] || guard=scripts/publication-push-guard.sh
if [ ! -f "$guard" ]; then
  echo "publication guard: no copy is installed beside $0 and the checked-out tree" >&2
  echo "  has none, so this push cannot be checked. Run npm install, or" >&2
  echo "  bash scripts/install-publication-push-hook.sh, in a current checkout." >&2
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
say "installed in $dir for every worktree of this clone, with $which."
