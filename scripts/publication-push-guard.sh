#!/usr/bin/env bash
#
# Publication push guard (#2365): the publication boundary, checked before a
# push to the public repository rather than after it.
#
# scripts/publication-boundary-check.py runs in CI on pull_request and
# merge_group, and in scripts/ci-local.sh. Neither sees a push. A branch or tag
# that never becomes a pull request is never checked, and one that does is
# checked only after GitHub has stored and started serving every object the
# push carried. Deleting the ref or rewriting history afterwards does not take
# them back (docs/PUBLIC_CORE_BOUNDARY.md, "A history rewrite does not
# redact"). So .husky/pre-push runs this before git sends anything:
#
#   scripts/publication-push-guard.sh <remote-name> <remote-url> < ref-lines
#
# with git's pre-push input, one line per ref the push updates:
#
#   <local-ref> SP <local-sha> SP <remote-ref> SP <remote-sha>
#
# Only a push whose URL names the public repository is examined: the URL's path
# ends in nightgauge/nightgauge, whatever the transport, host or host alias.
# For each ref it updates:
#
#   * A deletion publishes nothing and passes.
#   * Only branches and tags are verified. Any other ref, or a tag that names
#     no commit, is refused.
#   * UNRELATED HISTORY is refused. A root commit (one with no parents) that
#     main cannot reach means the ref carries a history main does not share:
#     an orphan branch, another repository's history, or a merge made with
#     --allow-unrelated-histories. Every root main can reach is one of main's
#     own roots, so "pushed roots main cannot reach" is exactly "pushed roots
#     that are not main's".
#   * NEW COMMITS are the ones the public repository does not already have:
#     not reachable from main, from the ref's old value there, or from any
#     other remote-tracking ref of that remote. If there are none (a release
#     tag on main, or a branch at a commit already pushed), nothing new is
#     published and nothing is scanned.
#   * Otherwise the boundary checker runs on the tip, with
#     NG_BOUNDARY_DIFF_BASE at the tip's merge base with main. That is the
#     verdict CI would give, from the commit's own checker and manifest. A
#     pushed ref publishes every commit it reaches, not only its tip, so an
#     EARLIER new commit is scanned too when its tree holds a file version
#     that no scanned tree, no merge base with main and not the ref's old
#     value holds. Content that one commit adds and a later one deletes is
#     caught; a commit whose every file version reappears in a scanned tree
#     costs nothing.
#
# A scanned commit is checked out in a scratch repository that borrows this
# repository's objects through objects/info/alternates. The checkout being
# pushed is never touched, and no worktree is registered in it.
#
# Fails closed when it cannot tell: no main to compare with, a shallow boundary
# inside a history it walks, no python3, or a checker that cannot run.
#
# Exit codes: 0 the push may proceed; 1 refused, a violation; 2 refused, the
# push could not be verified.
#
# LIMITS. This is a client-side hook. `git push --no-verify` and HUSKY=0 skip
# it, and a checkout of another repository that pushes to the public URL never
# runs it. docs/PUBLIC_CORE_BOUNDARY.md ("Checked before it is pushed") says
# what covers those cases.
#
# Written for bash 3.2, which is what /bin/bash still is on macOS.

set -uo pipefail

# Walk the objects the push sends: transfer ignores refs/replace/*, so must this.
export GIT_NO_REPLACE_OBJECTS=1

say() { printf 'publication guard: %s\n' "$*" >&2; }

# is_public_url <url>: does this URL name the public repository? Any transport
# (ssh, scp-like, https with or without credentials, file paths) and any host or
# SSH host alias; only the trailing owner/name is compared, case-insensitively,
# as GitHub compares it.
is_public_url() {
  local u
  u="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  while [ "${u%/}" != "$u" ]; do u="${u%/}"; done
  u="${u%.git}"
  while [ "${u%/}" != "$u" ]; do u="${u%/}"; done
  case "$u" in
    nightgauge/nightgauge | */nightgauge/nightgauge | *:nightgauge/nightgauge) return 0 ;;
  esac
  return 1
}

is_zero() {
  case "$1" in *[!0]*) return 1 ;; esac
  return 0
}

is_oid() {
  case "$1" in "" | *[!0-9a-f]*) return 1 ;; esac
  [ "${#1}" -eq 40 ] || [ "${#1}" -eq 64 ]
}

REMOTE=""
URL=""
WORK=""
TREE=""
PUBLIC_REMOTE=""
MAIN_NAME=""
MAIN_SHA=""
VIOLATION=0
UNVERIFIED=0
QUEUE=()     # commits to scan, in scan order
QUEUE_REF=() # the ref each one was queued for
QUEUE_TIP=() # that ref's tip

cleanup() {
  if [ -n "$WORK" ]; then rm -rf "$WORK"; fi
}

ensure_work() {
  [ -n "$WORK" ] && return 0
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/ng-publication-guard.XXXXXX")" || {
    WORK=""
    say "cannot verify this push: no temporary directory."
    return 2
  }
  : >"$WORK/scanned"
}

# resolve_main: the public repository's main as this checkout last fetched it.
#
# It comes from a remote whose FETCH url is the public repository, so that its
# remote-tracking refs are known to be public: they are excluded from the new
# commits below, and a private remote's refs must never be. That is the remote
# being pushed to when it is one, else origin, else any other. Sets
# PUBLIC_REMOTE, MAIN_NAME and MAIN_SHA.
resolve_main() {
  [ -n "$MAIN_SHA" ] && return 0
  local name="" r cut
  if [ "$REMOTE" != "$URL" ] &&
    is_public_url "$(git config --get "remote.$REMOTE.url" 2>/dev/null)"; then
    name="$REMOTE"
  elif is_public_url "$(git config --get remote.origin.url 2>/dev/null)"; then
    name="origin"
  else
    for r in $(git remote); do
      if is_public_url "$(git config --get "remote.$r.url" 2>/dev/null)"; then
        name="$r"
        break
      fi
    done
  fi
  if [ -z "$name" ]; then
    say "cannot verify this push: no remote of this checkout fetches the public"
    say "  repository, so there is no main to compare it with. Add one and fetch"
    say "  its main, then push again."
    return 2
  fi
  if ! MAIN_SHA="$(git rev-parse --verify --quiet "refs/remotes/$name/main^{commit}")"; then
    MAIN_SHA=""
    say "cannot verify this push: $name/main is missing, so there is no main to"
    say "  compare its history with. Run 'git fetch $name main' and push again."
    return 2
  fi
  if cut="$(shallow_cut "$MAIN_SHA")"; then
    MAIN_SHA=""
    say "cannot verify this push: this clone is shallow at ${cut:0:12}, inside the"
    say "  history of $name/main. Run 'git fetch --unshallow $name' and push again."
    return 2
  fi
  PUBLIC_REMOTE="$name"
  MAIN_NAME="$name/main"
}

# shallow_cut <commit>: succeeds, printing the boundary, when a shallow boundary
# of this clone lies inside <commit>'s history; its roots and merge bases are
# then not the real ones. A boundary elsewhere is harmless, so the repository-
# wide shallow flag is not the test: a depth-limited fetch of an unrelated
# branch sets that flag in a full clone.
shallow_cut() {
  local file s
  if ! file="$(git rev-parse --path-format=absolute --git-path shallow)"; then
    printf 'unknown\n'
    return 0
  fi
  [ -s "$file" ] || return 1
  while IFS= read -r s; do
    [ -n "$s" ] || continue
    if git merge-base --is-ancestor "$s" "$1" 2>/dev/null; then
      printf '%s\n' "$s"
      return 0
    fi
  done <"$file"
  return 1
}

# entries <commit>: prints the path of a file listing "<object> TAB <path>" for
# every entry of <commit>'s tree, sorted for comm. Modes are dropped: a mode
# change alone publishes no new content.
entries() {
  local f="$WORK/entries.$1"
  if [ ! -f "$f" ]; then
    git ls-tree -r --full-tree "$1" | sed 's/^[^ ]* [^ ]* //' | LC_ALL=C sort >"$f.tmp" ||
      return 1
    mv "$f.tmp" "$f"
  fi
  printf '%s\n' "$f"
}

# union_into <file> <list>...: <file> becomes the sorted union of the lists.
union_into() {
  local out="$1"
  shift
  LC_ALL=C sort -u "$@" >"$out.tmp" && mv "$out.tmp" "$out"
}

queue() { # queue <commit> <ref> <tip>
  local i=0
  while [ "$i" -lt "${#QUEUE[@]}" ]; do
    [ "${QUEUE[$i]}" = "$1" ] && return 0
    i=$((i + 1))
  done
  QUEUE+=("$1")
  QUEUE_REF+=("$2")
  QUEUE_TIP+=("$3")
}

# plan <ref> <tip> <old-remote-value> <new commits, oldest first>: queue the
# tip, and every earlier new commit that holds content no scan would see.
plan() {
  local ref="$1" tip="$2" old="$3" new="$4" c mb e known novel
  e="$(entries "$tip")" || return 2
  union_into "$WORK/scanned" "$WORK/scanned" "$e" || return 2
  known="$WORK/known"
  cp "$WORK/scanned" "$known" || return 2
  if ! is_zero "$old" && git cat-file -e "$old^{commit}" 2>/dev/null; then
    e="$(entries "$old")" || return 2
    union_into "$known" "$known" "$e" || return 2
  fi
  for c in $new; do
    [ "$c" = "$tip" ] && continue
    mb="$(git merge-base "$MAIN_SHA" "$c")" || return 2
    e="$(entries "$mb")" || return 2
    union_into "$WORK/known.c" "$known" "$e" || return 2
    e="$(entries "$c")" || return 2
    # A comm that failed would print nothing, which reads as "nothing new".
    novel="$(LC_ALL=C comm -23 "$e" "$WORK/known.c")" || return 2
    if [ -n "$novel" ]; then
      queue "$c" "$ref" "$tip"
      union_into "$WORK/scanned" "$WORK/scanned" "$e" || return 2
      union_into "$known" "$known" "$e" || return 2
    fi
  done
  queue "$tip" "$ref" "$tip"
}

# in_scratch <command...>: run against the scratch repository, isolated from
# the one being pushed. git exports GIT_DIR to a hook that runs in a linked
# worktree; without this, every git call the checker makes would read the real
# checkout instead of the commit under test.
in_scratch() {
  (
    unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY \
      GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_NAMESPACE GIT_PREFIX \
      GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT
    export GIT_DIR="$TREE/.git" GIT_WORK_TREE="$TREE"
    # The verdict must not depend on this machine's git config; CI has none.
    export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
    cd "$TREE" && "$@"
  )
}

make_scratch() {
  local objects format
  objects="$(git rev-parse --path-format=absolute --git-path objects)" &&
    format="$(git rev-parse --show-object-format)" || return 1
  TREE="$WORK/tree"
  mkdir -p "$TREE" &&
    in_scratch git init -q --template= --initial-branch=main \
      --object-format="$format" >/dev/null 2>&1 &&
    printf '%s\n' "$objects" >"$TREE/.git/objects/info/alternates" &&
    # The checker reads origin/main for its reference ceiling.
    in_scratch git update-ref refs/remotes/origin/main "$MAIN_SHA"
}

# scan <commit> <ref> <tip>: run the commit's own boundary checker on it.
scan() {
  local c="$1" ref="$2" tip="$3" base rc
  if ! in_scratch git read-tree --reset -u "$c" >"$WORK/checker.log" 2>&1 ||
    ! in_scratch git update-ref --no-deref HEAD "$c" >>"$WORK/checker.log" 2>&1; then
    cat "$WORK/checker.log" >&2
    say "cannot verify $ref: commit ${c:0:12} could not be checked out for the scan."
    return 2
  fi
  if [ ! -f "$TREE/scripts/publication-boundary-check.py" ]; then
    say "cannot verify $ref: commit ${c:0:12} has no"
    say "  scripts/publication-boundary-check.py, so its boundary cannot be checked."
    return 2
  fi
  base="$(git merge-base "$MAIN_SHA" "$c")" || {
    say "cannot verify $ref: commit ${c:0:12} has no merge base with $MAIN_NAME."
    return 2
  }
  in_scratch env NG_BOUNDARY_DIFF_BASE="$base" PYTHONDONTWRITEBYTECODE=1 \
    python3 scripts/publication-boundary-check.py >"$WORK/checker.log" 2>&1
  rc=$?
  [ "$rc" -eq 0 ] && return 0
  cat "$WORK/checker.log" >&2
  if [ "$rc" -eq 1 ]; then
    say "refusing $ref: commit ${c:0:12} breaks the publication boundary (report above)."
    if [ "$c" != "$tip" ]; then
      say "  It is not the tip. A later commit changes or removes that content, but"
      say "  every commit a pushed ref reaches is published, this one included."
    fi
    say "  Nothing was sent. Remove the content from the commit that adds it, then"
    say "  push again."
    return 1
  fi
  say "cannot verify $ref: the boundary checker could not run on commit ${c:0:12}"
  say "  (exit $rc, report above). Nothing was sent."
  return 2
}

main() {
  if [ "$#" -lt 2 ]; then
    say "usage: publication-push-guard.sh <remote-name> <remote-url> < pre-push ref lines"
    exit 2
  fi
  REMOTE="$1"
  URL="$2"
  if ! is_public_url "$URL"; then
    cat >/dev/null # git's ref lines; nothing here is ours to check
    exit 0
  fi

  local lines=() excl=() line lsha rref rsha extra commit cut roots new r
  local deleted=0 unchanged=0 refs=0 i rc
  while IFS= read -r line || [ -n "$line" ]; do
    [ -n "$line" ] && lines+=("$line")
  done
  [ "${#lines[@]}" -eq 0 ] && exit 0

  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  for line in "${lines[@]}"; do
    refs=$((refs + 1))
    lsha="" rref="" rsha="" extra=""
    read -r _ lsha rref rsha extra <<<"$line"
    if [ -n "$extra" ] || [ -z "$rref" ] || ! is_oid "$lsha" || ! is_oid "$rsha"; then
      say "cannot verify this push: unexpected input from git: $line"
      exit 2
    fi
    if is_zero "$lsha"; then
      deleted=$((deleted + 1))
      continue
    fi
    case "$rref" in
      refs/heads/?* | refs/tags/?*) ;;
      *)
        say "refusing $rref: only branches and tags are verified for the public repository."
        UNVERIFIED=1
        continue
        ;;
    esac
    if ! commit="$(git rev-parse --verify --quiet "$lsha^{commit}")"; then
      say "refusing $rref: ${lsha:0:12} names no commit, and only commits can be verified."
      UNVERIFIED=1
      continue
    fi
    resolve_main || exit 2
    if cut="$(shallow_cut "$commit")"; then
      say "cannot verify $rref: this clone is shallow at ${cut:0:12}, inside the pushed"
      say "  history. Run 'git fetch --unshallow $PUBLIC_REMOTE' and push again."
      UNVERIFIED=1
      continue
    fi
    if ! roots="$(git rev-list --max-parents=0 "$commit" --not "$MAIN_SHA")"; then
      say "cannot verify $rref: git could not walk its history."
      UNVERIFIED=1
      continue
    fi
    if [ -n "$roots" ]; then
      say "refusing $rref: its history is unrelated to $MAIN_NAME. It has a root"
      say "  commit $MAIN_NAME does not have:"
      for r in $roots; do say "    ${r:0:12}"; done
      say "  Pushing it would publish every commit it reaches, and deleting the ref"
      say "  afterwards would not unpublish them (docs/PUBLIC_CORE_BOUNDARY.md)."
      VIOLATION=1
      continue
    fi
    excl=("$MAIN_SHA" "--remotes=$PUBLIC_REMOTE")
    if ! is_zero "$rsha" && git cat-file -e "$rsha^{commit}" 2>/dev/null; then
      excl+=("$rsha")
    fi
    if ! new="$(git rev-list --topo-order --reverse "$commit" --not "${excl[@]}")"; then
      say "cannot verify $rref: git could not list its new commits."
      UNVERIFIED=1
      continue
    fi
    if [ -z "$new" ]; then
      unchanged=$((unchanged + 1))
      continue
    fi
    ensure_work || exit 2
    plan "$rref" "$commit" "$rsha" "$new"
    rc=$?
    if [ "$rc" -ne 0 ]; then
      say "cannot verify $rref: its commits could not be compared."
      UNVERIFIED=1
    fi
  done

  if [ "$VIOLATION" -eq 0 ] && [ "$UNVERIFIED" -eq 0 ] && [ "${#QUEUE[@]}" -gt 0 ]; then
    if ! command -v python3 >/dev/null 2>&1; then
      say "cannot verify this push: python3 is not on PATH, and the boundary checker"
      say "  needs it (with PyYAML: pip install -r .github/requirements-ci.txt)."
      exit 2
    fi
    if ! make_scratch; then
      say "cannot verify this push: the scratch repository for the scan could not be made."
      exit 2
    fi
    say "scanning ${#QUEUE[@]} commit(s) for the publication boundary..."
    i=0
    while [ "$i" -lt "${#QUEUE[@]}" ]; do
      scan "${QUEUE[$i]}" "${QUEUE_REF[$i]}" "${QUEUE_TIP[$i]}"
      rc=$?
      if [ "$rc" -eq 1 ]; then
        VIOLATION=1
        break
      elif [ "$rc" -ne 0 ]; then
        UNVERIFIED=1
        break
      fi
      i=$((i + 1))
    done
  fi

  if [ "$VIOLATION" -ne 0 ]; then exit 1; fi
  if [ "$UNVERIFIED" -ne 0 ]; then exit 2; fi
  say "ok: $refs ref(s) ($deleted deletion(s), $unchanged with nothing new)," \
    "${#QUEUE[@]} commit(s) scanned in ${SECONDS}s."
  exit 0
}

# Sourced by its regression suite for is_public_url; run by .husky/pre-push.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
