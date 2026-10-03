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
# redact"). So the pre-push hook runs this before git sends anything:
#
#   scripts/publication-push-guard.sh <remote-name> <remote-url> < ref-lines
#
# with git's pre-push input, one line per ref the push updates:
#
#   <local-ref> SP <local-sha> SP <remote-ref> SP <remote-sha>
#
# WHERE IT RUNS. scripts/install-publication-push-hook.sh, which npm install
# runs after husky, points core.hooksPath at a hook directory in the clone's
# shared git directory and installs there the public main's copy of this
# script, as the clone last fetched it. Its pre-push hook runs that copy for a
# push from any worktree of the clone, whatever the worktree has checked out, so
# neither an older copy in a worktree nor one being edited decides; the
# checked-out tree's copy runs only when none is installed. husky
# alone is not enough. Its hooks path is relative, so a worktree where npm
# install never ran has no hooks, and its runner skips a hook the checked-out
# tree does not have: an orphan branch, another repository's history or a
# commit older than this guard. .husky/pre-push still runs this, for a clone
# where only husky is installed. Nothing below may depend on the checkout:
# everything comes from the commits being pushed and from the public repository
# itself.
#
# Only a push whose URL names the public repository is examined: the URL's path
# ends in nightgauge/nightgauge, whatever the transport, host or host alias. It
# is the URL git pushes to, after insteadOf and pushurl.
#
# WHAT THE PUBLIC REPOSITORY ALREADY HAS is asked of it, with `git ls-remote`
# on that URL. This checkout's remote-tracking refs are not evidence of it. They
# go stale when a branch is deleted or purged upstream; they are fetched from a
# remote's first url while a push goes to its pushurl or to every url; and a
# push to a pushurl writes them. The main the URL advertises is MAIN. When this
# clone does not have that commit it is fetched from the URL into the object
# store, and no ref changes.
#
# WHOSE RULES. Nothing from the commits being pushed runs. The boundary checker
# and CI's allowlist-isolation script are MAIN's, read from MAIN's tree. A
# pushed commit's own checker would decide its own verdict, and it would run
# with the pusher's credentials: `git push <sha>:<ref>` of a commit nobody
# checked out, a fork's say, must not run that commit's code. So a ref that
# changes the checker is judged by MAIN's; land a checker change before the
# content it lets through, as with the allowlist. git diff, whose added lines
# the checker reads, takes MAIN's .gitattributes, and every file is checked out
# as stored, so no pushed attribute changes what the checker sees. The manifest
# is the one rule a ref's commits supply, and only as CI's allowlist-isolation
# check allows.
#
# For each ref the push updates:
#
#   * A deletion publishes nothing and passes.
#   * Only branches and tags are verified. Any other ref, or a tag that names
#     no commit, is refused.
#   * UNRELATED HISTORY is refused. A root commit (one with no parents) that
#     MAIN cannot reach means the ref carries a history main does not share:
#     an orphan branch, another repository's history, or a merge made with
#     --allow-unrelated-histories. Every root main can reach is one of main's
#     own roots, so "pushed roots main cannot reach" is exactly "pushed roots
#     that are not main's".
#   * NEW COMMITS are the ones the public repository does not have: no commit
#     it advertises can reach them (MAIN, the ref's old value, and every other
#     advertised commit this clone has). If there are none (a release tag on
#     main, or a branch at commits already pushed), nothing new is published
#     and nothing is scanned.
#   * THE ALLOWLIST CHANGES ALONE (#1970). A new commit whose
#     .github/publication-boundary.yaml differs from its merge base with MAIN,
#     and from MAIN's own, must change nothing else since that merge base,
#     which is what CI requires of a pull request. MAIN's copy of CI's script
#     judges each such commit, so loosening the manifest in one commit and
#     adding what it lets through in the next is refused as well as doing both
#     in one. A commit that carries MAIN's own manifest is judged by MAIN's
#     rules anyway, and one whose manifest change MAIN already has (a stacked
#     branch whose allowlist change was then merged on its own) changes nothing
#     MAIN allows.
#   * ONE SCAN covers the ref. The boundary checker runs on a synthetic commit:
#     the merge of the ref's tip into MAIN, as git merge-tree computes it, which
#     is what CI's pull-request run checks. It is diffed against MAIN, so
#     MAIN's manifest applies unless the ref changes it. A pushed ref publishes
#     every commit it reaches, not only its tip. So every file version an
#     EARLIER new commit holds that the tip, MAIN, that commit's merge base with
#     MAIN and the ref's old value do not hold is merged into the same path,
#     line by line, and the checker sees every line the push publishes, under
#     the path it is published at. Content that one commit adds and a later one
#     rewrites or deletes is caught, for the cost of one scan rather than one
#     per commit. That holds for rules that judge a path or a line on its own,
#     which is all but two: a forbidden-content rule with a file_baseline
#     counts the files that match, and the issue-reference rule also counts
#     references tree-wide. In the combined scan a file the tip deletes can
#     offset one an earlier commit added, though that commit's own count was
#     over.
#   * EARLIER COMMITS ARE JUDGED BY THEIR OWN MANIFEST. The combined scan reads
#     the earlier versions by the tip's manifest. When that is not MAIN's (the
#     ref changes the allowlist), or when MAIN's manifest has a file_baseline
#     rule, every earlier commit that holds a version merged into the scan is
#     scanned on its own as well, and must pass. Otherwise a commit could add
#     content and a later one take it out while loosening the allowlist, and
#     the content would be judged by the looser one. A reference the tree-wide
#     count could miss the same way is one the same file already carries on
#     MAIN, which the issue-reference rule lets through as carried over, so it
#     publishes no new number.
#   * A ref that does not merge cleanly into MAIN is scanned as that merge with
#     every conflict settled the ref's way: MAIN's tree with the ref's version
#     of every path it changed since its merge base with MAIN (and the same
#     earlier versions merged in), diffed against MAIN's tree with the merge
#     base's version of those paths. The lines read as added are the ref's own,
#     and everything it did not change is MAIN's, the manifest included: a ref
#     forked before MAIN tightened its rules is held to the tighter ones, and
#     files it never touched, which MAIN may since have dropped along with the
#     rules that classified them, are not judged again.
#   * If that scan fails, the commits are scanned one at a time, the tip first,
#     each the same way, to name the commit that breaks the boundary. The
#     combined scan can fail where no commit does: a line one commit inherited
#     from an older main that the current main has since dropped, or a count
#     summed across commits. When every commit passes on its own, each scan
#     running MAIN's own manifest, the push proceeds. Otherwise a commit was
#     judged by a manifest of its own, the failure cannot be pinned on any of
#     them, and the push is refused as unverified. A checker that could not run
#     (exit 2) blames no commit: the push is refused as unverified at once.
#   * A version of the manifest, which the checker reads its rules from, cannot
#     join the combined scan. Each commit that first holds one is scanned on its
#     own as well, and the rest of its content still joins the combined scan.
#     Versions that cannot be merged into one file for another reason (text and
#     binary at one path, a link, a submodule, a file where another version has
#     a directory) send the ref to one scan per commit from the start.
#
# Each scan checks the synthetic commit out in a scratch repository that
# borrows this repository's objects through objects/info/alternates. The
# checkout being pushed is never touched, and no worktree is registered in it.
# Every file is checked out as stored, whatever a .gitattributes in the pushed
# tree asks for (working-tree-encoding, a filter, ident, end-of-line
# conversion), since the checker reads the checked-out bytes, and git diff
# shows a file as binary only where MAIN's root .gitattributes says so (a
# pushed -diff would hide a file's added lines from the issue-reference rule).
# Paths that differ only in case or Unicode normalization become one file on a
# filesystem that folds them, which would hide a version from the checker. When
# only the combined scan has such paths, because an earlier version was merged
# in at a path a later commit renamed by case alone, the commits are scanned one
# at a time instead; a commit whose own scan collides is refused.
#
# Fails closed when it cannot tell: the public repository cannot be listed or
# has no main, MAIN cannot be fetched or has no checker, a shallow boundary lies
# inside a history it walks, no python3 that can import yaml (PyYAML), or a
# checker that cannot run.
#
# A scan stops when the git push that started it exits (a caller's timeout, or
# a killed terminal), rather than running on as an orphan.
#
# Exit codes: 0 the push may proceed; 1 refused, a violation; 2 refused, the
# push could not be verified.
#
# LIMITS. This is a client-side hook, and `git push --no-verify` skips it.
# docs/PUBLIC_CORE_BOUNDARY.md ("What the hook cannot cover") lists the
# checkouts it never runs in and what covers them.
#
# Written for bash 3.2, which is what /bin/bash still is on macOS.

set -uo pipefail

# Walk the objects the push sends: transfer ignores refs/replace/*, so must this.
export GIT_NO_REPLACE_OBJECTS=1
# Read only what this clone has. In a partial clone, asking whether it has an
# advertised commit would otherwise fetch it.
export GIT_NO_LAZY_FETCH=1

MANIFEST=".github/publication-boundary.yaml"
CHECKER="scripts/publication-boundary-check.py"
ISOLATION="scripts/check-boundary-allowlist-isolation.sh"

say() { printf 'publication guard: %s\n' "$*" >&2; }

# redact <url>: the URL without the credentials it may carry, for messages.
redact() { printf '%s' "$1" | sed -E 's#^([A-Za-z][A-Za-z0-9+.-]*://)[^/@]*@#\1#'; }

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
MAIN_SHA=""
GIT_PID=""
CHILD=""
RC=0
SYNTH=""
SYNTH_MODE=""
SYNTH_BASE=""
VIOLATION=0
UNVERIFIED=0
SCANS=0
NEW_TOTAL=0
COUNTS="" # does MAIN's manifest count files? yes or no, once asked
R_REF=() # the pushed refs that publish new commits
R_TIP=() # each one's tip
R_OLD=() # each one's value on the remote before the push

# stop_child: end a running checker, and the processes it started.
stop_child() {
  [ -n "$CHILD" ] || return 0
  pkill -TERM -P "$CHILD" 2>/dev/null
  kill -TERM "$CHILD" 2>/dev/null
  wait "$CHILD" 2>/dev/null
  CHILD=""
}

cleanup() {
  stop_child
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

# find_git_pid: the git process running this hook. It is the parent, or a few
# shells up when a hook shim did not exec. Left empty when there is none, as
# when the guard is run by hand, and then nothing is watched.
find_git_pid() {
  local p="$PPID" n=0 comm
  while [ "$n" -lt 5 ]; do
    case "$p" in "" | *[!0-9]* | 0 | 1) return 0 ;; esac
    comm="$(ps -o comm= -p "$p" 2>/dev/null | sed 's/[[:space:]]*$//')" || return 0
    case "${comm##*/}" in
      git | git.exe)
        GIT_PID="$p"
        return 0
        ;;
    esac
    p="$(ps -o ppid= -p "$p" 2>/dev/null | tr -d ' ')"
    n=$((n + 1))
  done
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

# remote_state: what the public repository has, from the URL being pushed to.
# Sets MAIN_SHA, fetching that commit by URL when this clone lacks it, and
# lists in $WORK/have every advertised commit this clone has.
remote_state() {
  [ -n "$MAIN_SHA" ] && return 0
  local main cut hint
  if [ "$REMOTE" != "$URL" ]; then hint="$REMOTE"; else hint="$(redact "$URL")"; fi
  if ! git ls-remote "$URL" >"$WORK/advertised" 2>"$WORK/ls-remote.log"; then
    cat "$WORK/ls-remote.log" >&2
    say "cannot verify this push: the public repository's refs could not be listed"
    say "  from $(redact "$URL"), so there is nothing to compare the push with."
    return 2
  fi
  main="$(awk '$2 == "refs/heads/main" { print $1; exit }' "$WORK/advertised")"
  if ! is_oid "$main"; then
    say "cannot verify this push: $(redact "$URL") advertises no main to compare it with."
    return 2
  fi
  if ! git cat-file -e "$main^{commit}" 2>/dev/null; then
    say "fetching the public main (${main:0:12}) to compare this push with..."
    git -c maintenance.auto=false -c gc.auto=0 -c fetch.writeCommitGraph=false \
      fetch --quiet --no-tags --no-write-fetch-head --no-recurse-submodules \
      "$URL" refs/heads/main >"$WORK/fetch.log" 2>&1
    if ! git cat-file -e "$main^{commit}" 2>/dev/null; then
      cat "$WORK/fetch.log" >&2
      say "cannot verify this push: the public main ${main:0:12} is not in this clone,"
      say "  and fetching it failed. Run 'git fetch $hint main' and push again."
      return 2
    fi
  fi
  if cut="$(shallow_cut "$main")"; then
    say "cannot verify this push: this clone is shallow at ${cut:0:12}, inside the"
    say "  history of the public main. Run 'git fetch --unshallow $hint' and push again."
    return 2
  fi
  MAIN_SHA="$main"
  # A tag is advertised peeled (^{}) too, so commits are all this keeps.
  if ! awk '{ print $1 }' "$WORK/advertised" | LC_ALL=C sort -u |
    git cat-file --batch-check='%(objectname) %(objecttype)' |
    awk '$2 == "commit" { print $1 }' >"$WORK/have"; then
    say "cannot verify this push: the advertised commits could not be looked up."
    return 2
  fi
}

# new_commits <tip> <old>: the commits the push publishes, oldest first.
new_commits() {
  {
    printf '%s\n^%s\n' "$1" "$MAIN_SHA"
    if ! is_zero "$2" && git cat-file -e "$2^{commit}" 2>/dev/null; then
      printf '^%s\n' "$2"
    fi
    sed 's/^/^/' "$WORK/have"
  } | git rev-list --stdin --topo-order --reverse
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

# sets_the_rules: from "<object> TAB <path>" lines, keep those whose path is the
# manifest, which the checker reads its rules from. With -v, keep the others.
# The checker, the isolation script and the .gitattributes that decide what
# the checker reads are MAIN's whatever a commit holds, so a version of them
# is content like any other.
sets_the_rules() {
  awk -F '\t' -v m="$MANIFEST" -v keep="${1:-}" '{
    r = ($2 == m)
    if ((keep == "-v") != r) print
  }'
}

# main_has_change <base> <blob> <main>: does MAIN's manifest, blob <main>,
# already have the change from <base> to <blob>? It does when merging that
# change into it leaves it as it is: a commit of a stacked branch whose
# allowlist change was then merged on its own. An empty name reads as no file.
main_has_change() {
  local f="$WORK/manifest" i=0 b
  for b in "$1" "$2" "$3"; do
    i=$((i + 1))
    if [ -n "$b" ]; then
      git cat-file blob "$b" >"$f.$i" || return 1
    else
      : >"$f.$i"
    fi
  done
  git merge-file -p "$f.3" "$f.1" "$f.2" >"$f.merged" 2>/dev/null && cmp -s "$f.merged" "$f.3"
}

# plan <i> <tip> <old>: everything about ref i that needs no scan. Its new
# commits are in $WORK/new.<i>. Writes:
#   isolate.<i>   new commits whose manifest differs from both their merge base
#                 with MAIN and MAIN's own, by a change MAIN does not already
#                 have: each must change nothing else
#   novel.<i>     the file versions earlier new commits hold that nothing
#                 scanned or already public holds, for the combined scan, as
#                 "<merge base> <object> TAB <path>": the merge base of the
#                 commit that holds it with MAIN
#   earlier.<i>   the commits that first hold one of those
#   separate.<i>  the commits that first hold a version of the manifest, which
#                 cannot join the combined scan: each is scanned on its own as
#                 well
plan() {
  local i="$1" tip="$2" old="$3" c mb e known novel others m_c m_mb m_main
  : >"$WORK/isolate.$i"
  : >"$WORK/novel.$i"
  : >"$WORK/earlier.$i"
  : >"$WORK/separate.$i"
  m_main="$(git rev-parse --verify --quiet "$MAIN_SHA:$MANIFEST")"
  known="$WORK/known"
  e="$(entries "$tip")" || return 2
  union_into "$known" "$WORK/scanned" "$e" || return 2
  e="$(entries "$MAIN_SHA")" || return 2
  union_into "$known" "$known" "$e" || return 2
  if ! is_zero "$old" && git cat-file -e "$old^{commit}" 2>/dev/null; then
    e="$(entries "$old")" || return 2
    union_into "$known" "$known" "$e" || return 2
  fi
  while IFS= read -r c; do
    [ -n "$c" ] || continue
    mb="$(git merge-base "$MAIN_SHA" "$c")" || return 2
    m_c="$(git rev-parse --verify --quiet "$c:$MANIFEST")"
    m_mb="$(git rev-parse --verify --quiet "$mb:$MANIFEST")"
    if [ "$m_c" != "$m_mb" ] && [ "$m_c" != "$m_main" ] &&
      ! main_has_change "$m_mb" "$m_c" "$m_main"; then
      printf '%s\n' "$c" >>"$WORK/isolate.$i"
    fi
    [ "$c" = "$tip" ] && continue
    e="$(entries "$mb")" || return 2
    union_into "$WORK/known.c" "$known" "$e" || return 2
    e="$(entries "$c")" || return 2
    # A comm that failed would print nothing, which reads as "nothing new".
    novel="$(LC_ALL=C comm -23 "$e" "$WORK/known.c")" || return 2
    [ -n "$novel" ] || continue
    if [ -n "$(printf '%s\n' "$novel" | sets_the_rules)" ]; then
      printf '%s\n' "$c" >>"$WORK/separate.$i"
    fi
    others="$(printf '%s\n' "$novel" | sets_the_rules -v)"
    if [ -n "$others" ]; then
      # Each with the merge base its lines are measured against.
      printf '%s\n' "$others" | sed "s/^/$mb /" >>"$WORK/novel.$i"
      printf '%s\n' "$c" >>"$WORK/earlier.$i"
    fi
    union_into "$known" "$known" "$e" || return 2
  done <"$WORK/new.$i"
  # A later ref of the same push need not cover what this one's scan does.
  e="$(entries "$tip")" || return 2
  sed 's/^[^ ]* //' "$WORK/novel.$i" >"$WORK/novel.plain" || return 2
  union_into "$WORK/scanned" "$WORK/scanned" "$e" "$WORK/novel.plain"
}

# scratch_env: isolate a subshell's git from the repository being pushed. git
# exports GIT_DIR to a hook that runs in a linked worktree; without this, every
# git call the checker makes would read the real checkout instead of the
# commit under test.
scratch_env() {
  unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY \
    GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_NAMESPACE GIT_PREFIX \
    GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT
  export GIT_DIR="$TREE/.git" GIT_WORK_TREE="$TREE"
  # The verdict must not depend on this machine's git config; CI has none.
  export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
}

in_scratch() {
  (
    scratch_env
    cd "$TREE" && "$@"
  )
}

# make_scratch: the scratch repository, and MAIN's rules in $WORK/rules: its
# checker, and its allowlist-isolation script when it has one. No scan runs
# code from the commits being pushed.
#
# The scratch repository's info/attributes outranks every .gitattributes in a
# tree, so a pushed one changes nothing the checker reads. Its first line and
# MAIN's root .gitattributes after it make git diff, whose added lines the
# checker reads, show a file as binary only where MAIN says so (a pushed -diff
# would hide a file's lines), and merge-tree merge as MAIN would. Its last
# line checks each file out as stored, since the checker reads the checked-out
# files: working-tree-encoding would re-encode one (to UTF-16, say, which no
# rule's text matches), and a filter, ident or end-of-line conversion rewrite
# it. A .gitattributes MAIN has below its root is not read: the files it marks
# are shown as text, which can only show the checker more.
make_scratch() {
  local objects format
  objects="$(git rev-parse --path-format=absolute --git-path objects)" &&
    format="$(git rev-parse --show-object-format)" || return 1
  mkdir -p "$WORK/rules" &&
    git cat-file blob "$MAIN_SHA:$CHECKER" >"$WORK/rules/${CHECKER##*/}" || return 1
  if git cat-file -e "$MAIN_SHA:$ISOLATION" 2>/dev/null; then
    git cat-file blob "$MAIN_SHA:$ISOLATION" >"$WORK/rules/${ISOLATION##*/}" || return 1
  fi
  TREE="$WORK/tree"
  mkdir -p "$TREE" &&
    in_scratch git init -q --template= --initial-branch=main \
      --object-format="$format" >/dev/null 2>&1 &&
    printf '%s\n' "$objects" >"$TREE/.git/objects/info/alternates" &&
    mkdir -p "$TREE/.git/info" &&
    {
      printf '%s\n' '* !diff !merge'
      if git cat-file -e "$MAIN_SHA:.gitattributes" 2>/dev/null; then
        git cat-file blob "$MAIN_SHA:.gitattributes" && printf '\n'
      fi
      printf '%s\n' '* !working-tree-encoding -filter -ident -text'
    } >"$TREE/.git/info/attributes" &&
    # The checker reads origin/main for its reference ceiling.
    in_scratch git update-ref refs/remotes/origin/main "$MAIN_SHA" &&
    write_synthesizer
}

# write_synthesizer: the program that builds a synthetic commit. It is Python,
# like the checker, because it reads and writes file contents byte for byte.
#
#   synth.py merge|own <main> <head> <index-file> <versions-file> [<base>]
#
# Builds, in a private index of the scratch repository, the tree to scan:
#
#   merge  <head> merged into <main> by `git merge-tree --write-tree`, which is
#          what CI's pull-request run checks. Exit 4 when they conflict.
#   own    <main>'s tree with <head>'s version of every path <head> changed
#          since <base>, its merge base with <main>, and without the paths it
#          deleted: a merge with every conflict settled <head>'s way. Every
#          other path is <main>'s, the manifest included, so a ref forked
#          before <main> tightened its rules is judged by the tightened ones,
#          by its own only where it changes them, and files it never touched
#          are not judged again.
#
# <versions-file> may be empty.
#
# Then, for each path in <versions-file> ("<base> <blob> TAB <path>" lines, the
# path as ls-tree quotes it), it appends to that path every line of every
# listed version that the path does not already hold, in order, except the
# lines <base> already had at that path: those were public before the push,
# and an older main's lines that the current main has since dropped would
# otherwise read as new. The tree's own version of the path, when it has one,
# stays first and verbatim, and a path is never dropped, however few lines it
# keeps. Prints a commit with that tree whose parents are <main> and <head>
# (merge), so the checker reads the right first-parent history for its
# reference ceiling. In own mode its parent is the commit to diff it against,
# which is printed next: <main>'s tree with <base>'s version of the paths <head>
# changed, a child of <main>. The lines the checker reads as added are then
# <head>'s and the versions', its diff is as small as a merge's, and its
# ceiling is <main>'s. (The checker diffs against the merge base of the base it
# is given and HEAD, so a base that is not HEAD's ancestor would not be read.)
#
# Exit 3 when the versions cannot be merged into one file: a path holding both
# UTF-8 text and other bytes, a link or a submodule, a file where the tree or
# another version has a directory, or a version of the manifest, which the
# checker reads its rules from.
write_synthesizer() {
  cat >"$WORK/synth.py" <<'PY'
import os
import subprocess
import sys

SETS_THE_RULES = {b".github/publication-boundary.yaml"}
ESCAPES = {b"a": 7, b"b": 8, b"t": 9, b"n": 10, b"v": 11, b"f": 12, b"r": 13,
           b'"': 34, b"\\": 92}


def git(args, data=None, env=None, ok=(0,)):
    r = subprocess.run(["git"] + args, input=data, stdout=subprocess.PIPE,
                       stderr=subprocess.PIPE, env=env)
    if r.returncode not in ok:
        sys.stderr.write(r.stderr.decode("utf-8", "replace"))
        sys.exit(2)
    return r


def unquote(path):
    """A path as ls-tree prints it, back to its bytes."""
    if len(path) < 2 or path[:1] != b'"' or path[-1:] != b'"':
        return path
    s, out, i = path[1:-1], bytearray(), 0
    while i < len(s):
        ch = s[i:i + 1]
        if ch != b"\\":
            out += ch
            i += 1
        elif s[i + 1:i + 2] in ESCAPES:
            out.append(ESCAPES[s[i + 1:i + 2]])
            i += 2
        else:
            out.append(int(s[i + 1:i + 4], 8))
            i += 4
    return bytes(out)


def unmergeable(path):
    return path in SETS_THE_RULES


def read_objects(names, env):
    """The contents of each named blob, or None where the name is missing or
    is not a blob."""
    if not names:
        return []
    out = git(["cat-file", "--batch"], data=b"\n".join(names) + b"\n", env=env).stdout
    found, pos = [], 0
    for _ in names:
        end = out.index(b"\n", pos)
        header = out[pos:end].split(b" ")
        pos = end + 1
        if len(header) == 3 and header[2].isdigit():
            size = int(header[2])
            found.append(out[pos:pos + size] if header[1] == b"blob" else None)
            pos += size + 1
        else:
            found.append(None)  # "<name> missing", or "ambiguous"
    return found


def parents_of(path):
    parts = path.split(b"/")
    return [b"/".join(parts[:k]) for k in range(1, len(parts))]


def own_trees(main_c, head, base, env):
    """Own mode. Leaves in the index <main>'s tree with <head>'s version of every
    path <head> changed since <base>, without the paths it deleted, and returns
    the tree to diff that against: <main>'s with <base>'s version of the same
    paths. The difference is exactly <head>'s own change, while every path it
    did not change, the manifest included, is <main>'s."""
    out = git(["diff-tree", "-r", "-z", "--no-renames", base, head], env=env).stdout
    fields = out.split(b"\0")
    sides = ([], [])  # (<base>'s, <head>'s): (mode, object, path); mode 0 removes
    for meta, path in zip(fields[0::2], fields[1::2]):
        parts = meta[1:].split(b" ")
        if meta[:1] != b":" or len(parts) != 5:
            sys.stderr.write("synth.py: unexpected diff-tree output\n")
            sys.exit(2)
        old_mode, new_mode, old, new = parts[:4]
        for side, mode_, oid in ((sides[0], old_mode, old), (sides[1], new_mode, new)):
            side.append((b"0", oid, path) if oid.strip(b"0") == b"" else (mode_, oid, path))

    def index_main_with(side):
        git(["read-tree", main_c], env=env)
        # Removals first, so that a path that became a file where a directory
        # was, or the other way round, is free when it is added.
        info = bytearray()
        for gone in (True, False):
            for mode_, oid, path in side:
                if (mode_ == b"0") == gone:
                    info += mode_ + b" " + oid + b"\t" + path + b"\0"
        if info:
            git(["update-index", "-z", "--index-info"], data=bytes(info), env=env)

    index_main_with(sides[0])
    base_tree = git(["write-tree"], env=env).stdout.strip().decode()
    index_main_with(sides[1])
    return base_tree


def main():
    mode, main_c, head, index = sys.argv[1:5]
    versions_file = sys.argv[5] if len(sys.argv) > 5 else ""
    base = sys.argv[6] if len(sys.argv) > 6 else ""
    env = dict(os.environ, GIT_INDEX_FILE=index)

    if mode == "merge":
        r = git(["merge-tree", "--write-tree", "--no-messages", main_c, head], env=env,
                ok=(0, 1))
        if r.returncode == 1:
            sys.exit(4)
        tree = r.stdout.split(b"\n", 1)[0].strip().decode()
        parents = [main_c, head]
    elif mode == "own" and base:
        tree = None
        parents = []
    else:
        sys.stderr.write("synth.py: unknown mode %r, or own without a base\n" % mode)
        sys.exit(2)
    if tree:
        git(["read-tree", tree], env=env)
        base_tree = None
    else:
        base_tree = own_trees(main_c, head, base, env)

    groups, order, base_of = {}, [], {}
    if versions_file:
        with open(versions_file, "rb") as f:
            for line in f.read().split(b"\n"):
                if not line:
                    continue
                entry, _, quoted = line.partition(b"\t")
                base, _, blob = entry.rpartition(b" ")
                path = unquote(quoted)
                if unmergeable(path):
                    sys.exit(3)
                if path not in groups:
                    groups[path] = []
                    order.append(path)
                if blob not in groups[path]:
                    groups[path].append(blob)
                if base:
                    base_of.setdefault((path, blob), base)
    if order:
        current = {}
        for rec in git(["ls-files", "-s", "-z"], env=env).stdout.split(b"\0"):
            if rec:
                meta, _, path = rec.partition(b"\t")
                mode_, blob, _stage = meta.split(b" ")
                current[path] = (mode_, blob)
        # A file where another path needs a directory cannot be added without
        # dropping that path from the scan.
        everything = set(current) | set(order)
        dirs = set()
        for path in everything:
            dirs.update(parents_of(path))
        if dirs & everything:
            sys.exit(3)
        for path in order:
            if path in current:
                mode_, blob = current[path]
                if mode_ not in (b"100644", b"100755"):
                    sys.exit(3)
                if blob in groups[path]:
                    groups[path].remove(blob)
                groups[path].insert(0, blob)
        wanted = []
        for path in order:
            for blob in groups[path]:
                if blob not in wanted:
                    wanted.append(blob)
        found = read_objects(wanted, env)
        if any(t is None for t in found):
            sys.exit(3)
        contents = dict(zip(wanted, found))
        # What each version's merge base already had at the same path. A path
        # that cannot be named on one line of input keeps every line.
        keys = [k for k in base_of if b"\n" not in k[0]]
        had = read_objects([base_of[k] + b":" + k[0] for k in keys], env)
        inherited = {k: set(t.split(b"\n")) for k, t in zip(keys, had) if t is not None}
        info = bytearray()
        for path in order:
            texts = [contents[b] for b in groups[path]]
            kinds = set()
            for t in texts:
                try:
                    t.decode("utf-8")
                    kinds.add(True)
                except UnicodeDecodeError:
                    kinds.add(False)
            if len(kinds) > 1:
                sys.exit(3)
            body, seen = bytearray(), set()
            for n, blob in enumerate(groups[path]):
                t = contents[blob]
                if n == 0 and path in current:
                    body += t
                    if body and not body.endswith(b"\n"):
                        body += b"\n"
                    seen.update(t.split(b"\n"))
                    continue
                skip = inherited.get((path, blob), ())
                for line in t.split(b"\n"):
                    if line not in seen and line not in skip:
                        seen.add(line)
                        body += line + b"\n"
            blob = git(["hash-object", "-w", "--stdin"], data=bytes(body), env=env).stdout.strip()
            mode_ = current[path][0] if path in current else b"100644"
            info += mode_ + b" " + blob + b"\t" + path + b"\0"
        git(["update-index", "-z", "--index-info"], data=bytes(info), env=env)

    tree = git(["write-tree"], env=env).stdout.strip().decode()
    who = dict(env, GIT_AUTHOR_NAME="publication guard", GIT_AUTHOR_EMAIL="guard@invalid",
               GIT_AUTHOR_DATE="2000-01-01T00:00:00Z", GIT_COMMITTER_NAME="publication guard",
               GIT_COMMITTER_EMAIL="guard@invalid", GIT_COMMITTER_DATE="2000-01-01T00:00:00Z")
    if base_tree:
        # The checker diffs against the merge base of the base it is given and
        # HEAD, so the base must be the scan's parent to be the one it reads.
        base_commit = git(["commit-tree", base_tree, "-p", main_c, "-m",
                           "publication guard: synthetic base"], env=who).stdout
        parents = [base_commit.decode().strip()]
    args = ["commit-tree", tree]
    for p in parents:
        args += ["-p", p]
    commit = git(args + ["-m", "publication guard: synthetic scan"], env=who).stdout
    sys.stdout.write(commit.decode().strip() + "\n")
    if base_tree:
        sys.stdout.write(parents[0] + "\n")


main()
PY
}

# synthesize <commit> [<versions-file>]: the synthetic commit that scans
# <commit>, with the versions merged in. Sets SYNTH to it, SYNTH_MODE to merge
# or own, and SYNTH_BASE to the commit the checker diffs it against: MAIN when
# <commit> merges cleanly into MAIN, else own mode's synthetic base.
# Returns 0, 3 when the versions cannot be merged into one scan, or 2.
synthesize() {
  local c="$1" versions="${2:-}" rc mb out
  SYNTH_MODE="merge"
  SYNTH_BASE="$MAIN_SHA"
  rm -f "$WORK/synth.index"
  SYNTH="$(in_scratch python3 "$WORK/synth.py" merge "$MAIN_SHA" "$c" "$WORK/synth.index" \
    "$versions" 2>"$WORK/synth.log")"
  rc=$?
  [ "$rc" -eq 4 ] || return "$rc"
  SYNTH_MODE="own"
  mb="$(git merge-base "$MAIN_SHA" "$c")" || return 2
  rm -f "$WORK/synth.index"
  out="$(in_scratch python3 "$WORK/synth.py" own "$MAIN_SHA" "$c" "$WORK/synth.index" \
    "$versions" "$mb" 2>"$WORK/synth.log")"
  rc=$?
  [ "$rc" -eq 0 ] || return "$rc"
  SYNTH="$(printf '%s\n' "$out" | sed -n 1p)"
  SYNTH_BASE="$(printf '%s\n' "$out" | sed -n 2p)"
  is_oid "$SYNTH" && is_oid "$SYNTH_BASE" || return 2
}

# checkout <commit>: the scratch working tree becomes <commit>'s tree.
# Returns 2 when it cannot, 4 when two of its paths are one file here.
#
# A checkout writes only what changed since the last one. When the last one had
# two paths that are one file here, removing one of them removes the file the
# other path still names, and git does not write that again. So a checkout that
# does not come out clean is repeated into an empty tree before two of its own
# paths are blamed.
checkout() {
  local try
  for try in again last; do
    if ! in_scratch git read-tree --reset -u "$1" >"$WORK/checkout.log" 2>&1 ||
      ! in_scratch git update-ref --no-deref HEAD "$1" >>"$WORK/checkout.log" 2>&1; then
      cat "$WORK/checkout.log" >&2
      return 2
    fi
    in_scratch git update-index -q --refresh >/dev/null 2>&1
    in_scratch git diff-files --quiet && return 0
    [ "$try" = last ] && break
    rm -f "$TREE/.git/index" &&
      find "$TREE" -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} + || return 2
  done
  in_scratch git diff-files --name-only | sed 's/^/    /' >&2
  return 4
}

# run_checker <base>: MAIN's boundary checker on the checked-out tree, diffed
# against <base>. Sets RC. Stops it, and exits, if the git push it serves goes
# away.
run_checker() {
  (
    scratch_env
    export NG_BOUNDARY_DIFF_BASE="$1" PYTHONDONTWRITEBYTECODE=1
    cd "$TREE" && exec python3 "$WORK/rules/${CHECKER##*/}"
  ) >"$WORK/checker.log" 2>&1 &
  CHILD=$!
  while kill -0 "$CHILD" 2>/dev/null; do
    if [ -n "$GIT_PID" ] && ! kill -0 "$GIT_PID" 2>/dev/null; then
      stop_child
      say "stopped: the git push this check was for has exited."
      exit 2
    fi
    sleep 0.2
  done
  wait "$CHILD"
  RC=$?
  CHILD=""
  SCANS=$((SCANS + 1))
}

# squash_hint <i> <last line>: a way out that the workspace rules allow (no
# force-push, no interactive rebase, no hard reset): squash the unpushed
# commits, so that the push publishes only what the tip holds.
squash_hint() {
  local ref="${R_REF[$1]}" old="${R_OLD[$1]}" base
  case "$ref" in
    refs/tags/*)
      say "  Nothing was sent. Tag a commit whose history does not hold the content."
      return 0
      ;;
  esac
  if ! is_zero "$old" && git cat-file -e "$old^{commit}" 2>/dev/null &&
    git merge-base --is-ancestor "$old" "${R_TIP[$1]}" 2>/dev/null; then
    base="$old"
  else
    base="$(git merge-base "$MAIN_SHA" "${R_TIP[$1]}")"
  fi
  say "  Rewrite the unpushed commits. With ${ref#refs/heads/} checked out and nothing"
  say "  else uncommitted:"
  say "    git reset --soft ${base:0:12}"
  say "  $2"
}

# fix_hint <i>: what to do about a violation.
fix_hint() {
  case "${R_REF[$1]}" in
    refs/tags/*) squash_hint "$1" "" ;;
    *)
      say "  Nothing was sent. A new commit that removes the content does not help:"
      say "  the commit that adds it is still published."
      squash_hint "$1" "then take the content out of the files and commit again."
      ;;
  esac
}

# by_main_rules <synthetic commit>: is the scan of it judged by MAIN's own
# manifest? The checker and the attributes it reads by are always MAIN's, so
# the manifest is the one rule a commit can bring.
by_main_rules() {
  local a b
  # The synthetic commit is in the scratch repository, which can read MAIN too.
  a="$(in_scratch git rev-parse --verify --quiet "$1:$MANIFEST")" || return 1
  b="$(git rev-parse --verify --quiet "$MAIN_SHA:$MANIFEST")" || return 1
  [ "$a" = "$b" ]
}

# counts_files: does MAIN's manifest have a rule that counts files, a
# forbidden_content rule with a file_baseline? A manifest that cannot be read
# is taken to have one, which costs scans and misses nothing.
counts_files() {
  local blob
  if [ -z "$COUNTS" ]; then
    COUNTS=yes
    if blob="$(git rev-parse --verify --quiet "$MAIN_SHA:$MANIFEST")" &&
      ! git cat-file blob "$blob" | python3 -c '
import sys

import yaml

try:
    rules = yaml.safe_load(sys.stdin).get("forbidden_content") or []
    counts = any(rule.get("file_baseline") is not None for rule in rules)
except Exception:
    counts = True
sys.exit(0 if counts else 1)
' >/dev/null 2>&1; then
      COUNTS=no
    fi
  fi
  [ "$COUNTS" = yes ]
}

# isolation <i>: CI's "allowlist changes alone" check (#1970) on each new commit
# of ref i whose manifest changed, by MAIN's copy of CI's script: a ref that
# changes the script is still judged by MAIN's.
isolation() {
  local i="$1" c
  [ -s "$WORK/isolate.$i" ] || return 0
  if [ ! -f "$WORK/rules/${ISOLATION##*/}" ]; then
    say "cannot verify ${R_REF[$i]}: it changes $MANIFEST, and the public"
    say "  main has no $ISOLATION to judge that with."
    return 2
  fi
  while IFS= read -r c; do
    if ! in_scratch bash "$WORK/rules/${ISOLATION##*/}" "$MAIN_SHA" "$c" \
      >"$WORK/isolation.log" 2>&1; then
      cat "$WORK/isolation.log" >&2
      say "refusing ${R_REF[$i]}: commit ${c:0:12} fails CI's allowlist-isolation check"
      say "  (#1970, report above). A pull request that changes $MANIFEST"
      say "  changes nothing else, and every commit a pushed ref reaches is published,"
      say "  so each new commit is held to that."
      fix_hint "$i"
      return 1
    fi
  done <"$WORK/isolate.$i"
}

# collision <i> <what>: refuse ref i, because <what> has paths that are one
# file on this filesystem.
collision() {
  say "cannot verify ${R_REF[$1]}: in $2, the paths above differ only in case or"
  say "  Unicode normalization, so they are one file on this filesystem and the"
  say "  checker would read only one of them. Push from a filesystem that tells"
  say "  them apart, or give them names that differ by more."
}

# scan <i> <what>: check SYNTH out and run the checker on it against
# SYNTH_BASE. Returns 0, 2 when it could not, 3 when the checker failed (RC
# says how, $WORK/checker.log says why), or 4 when two of its paths are one
# file on this filesystem, which the caller judges.
scan() {
  local i="$1" what="$2" rc
  checkout "$SYNTH"
  rc=$?
  if [ "$rc" -eq 4 ]; then
    return 4
  elif [ "$rc" -ne 0 ]; then
    say "cannot verify ${R_REF[$i]}: $what could not be checked out for the scan."
    return 2
  fi
  run_checker "$SYNTH_BASE"
  [ "$RC" -eq 0 ] && return 0
  return 3
}

# check_ref <i>: scan ref i. Returns 0, 1 (violation) or 2 (unverified).
check_ref() {
  local i="$1" ref="${R_REF[$1]}" tip="${R_TIP[$1]}" n rc c k total list each
  local combined=0 main_rules=1
  n="$(wc -l <"$WORK/new.$i" | tr -d ' ')"
  NEW_TOTAL=$((NEW_TOTAL + n))
  isolation "$i" || return $?
  # Every commit that may need a scan of its own, each once, tip first; and
  # the same without the tip, whose own content the combined scan judges.
  printf '%s\n' "$tip" | cat - "$WORK/earlier.$i" "$WORK/separate.$i" |
    awk '!seen[$0]++' >"$WORK/each.$i"
  sed 1d "$WORK/each.$i" >"$WORK/apart.$i"
  synthesize "$tip" "$WORK/novel.$i"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    if [ -s "$WORK/earlier.$i" ]; then
      say "scanning $ref: $n new commit(s), with the earlier ones' content merged in..."
    else
      say "scanning $ref: $n new commit(s)..."
    fi
    if [ "$SYNTH_MODE" = "own" ]; then
      say "  it does not merge cleanly into the public main, so the files it changes are"
      say "  scanned in the public main's tree, by main's $MANIFEST"
      say "  unless it changes that."
    fi
    scan "$i" "the scan of $ref"
    rc=$?
    if [ "$rc" -eq 0 ] && [ -s "$WORK/earlier.$i" ] && ! by_main_rules "$SYNTH"; then
      # The scan read the earlier versions by the tip's manifest, not the one
      # each commit that holds them carries: content one commit adds and a
      # later one takes out while loosening the manifest would pass.
      list="$WORK/apart.$i"
      say "  it read the earlier commits' content by the ref's own"
      say "  $MANIFEST, not the one each of them carries;"
      say "  scanning each earlier commit on its own as well..."
    elif [ "$rc" -eq 0 ] && [ -s "$WORK/earlier.$i" ] && counts_files; then
      # A file the tip deletes can offset, in the count, one that an earlier
      # commit added and the tip no longer holds.
      list="$WORK/apart.$i"
      say "  the public main's $MANIFEST counts files"
      say "  (file_baseline), and what the commits add up to can hide one commit's count;"
      say "  scanning each earlier commit on its own as well..."
    elif [ "$rc" -eq 0 ]; then
      # Versions of the manifest could not join it.
      list="$WORK/separate.$i"
      if [ -s "$list" ]; then
        say "  and, on its own, each commit with its own version of"
        say "  $MANIFEST..."
      fi
    elif [ "$rc" -eq 3 ] && [ "$RC" -ne 1 ]; then
      # No commit can be blamed for a checker that cannot run.
      cat "$WORK/checker.log" >&2
      say "cannot verify $ref: the boundary checker could not run on the scan of it"
      say "  (exit $RC, report above). Nothing was sent."
      return 2
    elif [ "$rc" -eq 3 ]; then
      combined=1
      list="$WORK/each.$i"
      cp "$WORK/checker.log" "$WORK/combined.log"
      say "  that scan failed; scanning the new commits one at a time to find the one"
      say "  that breaks the boundary..."
    elif [ "$rc" -eq 4 ] && [ -s "$WORK/novel.$i" ]; then
      # An earlier version, merged in at its old path, can meet a later path
      # that differs only in case, as after a rename by case alone, where no
      # commit holds both.
      list="$WORK/each.$i"
      say "  in that scan the paths above are one file on this filesystem; scanning"
      say "  the new commits one at a time instead..."
    elif [ "$rc" -eq 4 ]; then
      collision "$i" "the scan of $ref"
      return 2
    else
      return "$rc"
    fi
  elif [ "$rc" -eq 3 ]; then
    list="$WORK/each.$i"
    say "scanning $ref: $n new commit(s), one at a time (their versions of some"
    say "  files cannot be merged into one scan)..."
  else
    cat "$WORK/synth.log" >&2
    say "cannot verify $ref: its commits could not be combined for the scan."
    return 2
  fi
  # Indexed, not "${each[@]}": bash 3.2 calls an empty array unbound under -u.
  each=()
  while IFS= read -r c; do
    [ -n "$c" ] && each+=("$c")
  done <"$list"
  total="${#each[@]}"
  k=0
  while [ "$k" -lt "$total" ]; do
    c="${each[$k]}"
    k=$((k + 1))
    say "  commit ${c:0:12} ($k of $total)..."
    if ! synthesize "$c"; then
      cat "$WORK/synth.log" >&2
      say "cannot verify $ref: commit ${c:0:12} could not be prepared for the scan."
      return 2
    fi
    by_main_rules "$SYNTH" || main_rules=0
    scan "$i" "commit ${c:0:12}"
    rc=$?
    [ "$rc" -eq 0 ] && continue
    if [ "$rc" -eq 4 ]; then
      collision "$i" "commit ${c:0:12}"
      return 2
    fi
    [ "$rc" -eq 3 ] || return "$rc"
    cat "$WORK/checker.log" >&2
    if [ "$RC" -eq 1 ]; then
      say "refusing $ref: commit ${c:0:12} breaks the publication boundary (report above)."
      if [ "$c" != "$tip" ]; then
        say "  It is not the tip. A later commit changes or removes that content, but"
        say "  every commit a pushed ref reaches is published, this one included."
      fi
      fix_hint "$i"
      return 1
    fi
    say "cannot verify $ref: the boundary checker could not run on commit ${c:0:12}"
    say "  (exit $RC, report above). Nothing was sent."
    return 2
  done
  [ "$combined" -eq 1 ] || return 0
  if [ "$main_rules" -eq 1 ]; then
    say "  every new commit passes on its own, by the public main's rules; the"
    say "  combined scan failed only on what the commits add up to, so $ref may go."
    return 0
  fi
  cat "$WORK/combined.log" >&2
  say "cannot verify $ref: the combined scan failed (report above). Every commit"
  say "  passes on its own, but some were judged by a manifest of their own, not the"
  say "  public main's $MANIFEST, so the failure"
  say "  cannot be pinned on any of them. Nothing was sent."
  squash_hint "$i" "and commit again: one commit publishes only what the tip holds."
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

  local lines=() line lsha rref rsha extra commit cut roots r i rc
  local deleted=0 unchanged=0 refs=0
  while IFS= read -r line || [ -n "$line" ]; do
    [ -n "$line" ] && lines+=("$line")
  done
  [ "${#lines[@]}" -eq 0 ] && exit 0

  trap cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 141' PIPE
  trap 'exit 143' TERM
  find_git_pid

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
    ensure_work || exit 2
    remote_state || exit 2
    if cut="$(shallow_cut "$commit")"; then
      say "cannot verify $rref: this clone is shallow at ${cut:0:12}, inside the pushed"
      say "  history. Run 'git fetch --unshallow' and push again."
      UNVERIFIED=1
      continue
    fi
    if ! roots="$(git rev-list --max-parents=0 "$commit" --not "$MAIN_SHA")"; then
      say "cannot verify $rref: git could not walk its history."
      UNVERIFIED=1
      continue
    fi
    if [ -n "$roots" ]; then
      say "refusing $rref: its history is unrelated to the public main. It has a"
      say "  root commit main does not have:"
      for r in $roots; do say "    ${r:0:12}"; done
      say "  Pushing it would publish every commit it reaches, and deleting the ref"
      say "  afterwards would not unpublish them (docs/PUBLIC_CORE_BOUNDARY.md)."
      VIOLATION=1
      continue
    fi
    i="${#R_REF[@]}"
    if ! new_commits "$commit" "$rsha" >"$WORK/new.$i"; then
      say "cannot verify $rref: git could not list its new commits."
      UNVERIFIED=1
      continue
    fi
    if [ ! -s "$WORK/new.$i" ]; then
      unchanged=$((unchanged + 1))
      continue
    fi
    R_REF+=("$rref")
    R_TIP+=("$commit")
    R_OLD+=("$rsha")
    if ! plan "$i" "$commit" "$rsha"; then
      say "cannot verify $rref: its commits could not be compared."
      UNVERIFIED=1
    fi
  done

  if [ "$VIOLATION" -eq 0 ] && [ "$UNVERIFIED" -eq 0 ] && [ "${#R_REF[@]}" -gt 0 ]; then
    if ! command -v python3 >/dev/null 2>&1; then
      say "cannot verify this push: python3 is not on PATH, and the boundary checker"
      say "  needs it (with PyYAML: pip install -r .github/requirements-ci.txt)."
      exit 2
    fi
    if ! python3 -c 'import yaml' >/dev/null 2>&1; then
      say "cannot verify this push: $(command -v python3) cannot import yaml, and the"
      say "  boundary checker needs PyYAML: pip install -r .github/requirements-ci.txt."
      exit 2
    fi
    if [ "$(git cat-file -t "$MAIN_SHA:$CHECKER" 2>/dev/null)" != "blob" ]; then
      say "cannot verify this push: the public main (${MAIN_SHA:0:12}) has no $CHECKER,"
      say "  and a push is judged by the public main's own checker."
      exit 2
    fi
    if ! make_scratch; then
      say "cannot verify this push: the scratch repository for the scan could not be made."
      exit 2
    fi
    i=0
    while [ "$i" -lt "${#R_REF[@]}" ]; do
      check_ref "$i"
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
    "$NEW_TOTAL new commit(s) in $SCANS scan(s), ${SECONDS}s."
  exit 0
}

# Sourced by its regression suite for its functions; run by the pre-push hook.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
