#!/usr/bin/env bash
# capture-conflicted-rebase-fixture.sh — build a REAL git repository whose next
# `git rebase origin/main` genuinely fails, as the fixture for the #301
# conflict-capture regression tests.
#
# Issue #301 is an instance of the silent no-op class (#166): the recovery
# action's conflict capture folded three different realities — "captured a
# conflict", "probe returned nothing", "probe itself failed" — into one empty
# slice, then ran `git rebase --abort` and reported the run resumable. The abort
# destroys the `:2:`/`:3:` index blobs permanently, so the one moment the
# evidence was capturable is gone.
#
# The bug lives in what REAL git does under a rebase, and nothing else can prove
# the fix:
#
#   * git DETACHES HEAD for the duration of a rebase, so
#     `git rev-parse --abbrev-ref HEAD` answers the literal string "HEAD" — the
#     branch name is NOT available from the obvious place. A hand-written stub
#     that answers "feat/whatever" describes a state git never produces, and a
#     test built on it passes against broken code. (That is exactly how the
#     "unknown" branch defect shipped green.)
#   * a rebase can fail WITHOUT producing any conflicted path at all (a dirty
#     index makes git refuse before it starts), so the unmerged-path probe
#     succeeds and reports zero files. That empty-but-successful probe is the
#     most reachable route into the bug and cannot be reproduced by stubbing an
#     error.
#
# So this script hand-authors no fixture shape whatsoever. It runs actual git
# commands and hands the tests a working tree; every byte the tests assert on is
# produced by git itself at test time.
#
# ------------------------------------------------------------------------
# USAGE
#
#   scripts/capture-conflicted-rebase-fixture.sh <dest-dir> [mode]
#
#   <dest-dir>  empty (or non-existent) directory to build the fixture in.
#   [mode]      conflict     (default) — the pending rebase hits a genuine
#                            content conflict in f.txt and stops with an
#                            unmerged path in the index.
#               dirty-index  — the pending rebase is REFUSED before it starts
#                            ("cannot rebase: Your index contains uncommitted
#                            changes"). The rebase fails; there is no conflict.
#               detached     — same content conflict, but the clone starts on a
#                            DETACHED HEAD, so no branch exists to record. git
#                            writes the literal string "detached HEAD" into
#                            rebase-merge/head-name instead of a refs/heads/
#                            ref. This is the one state where the branch is
#                            genuinely undeterminable, and it is git's own
#                            answer — not an invented one.
#               unicode-path — a MULTI-FILE content conflict where one path has
#                            non-ASCII bytes ("café.txt"). git C-quotes such a
#                            path in `diff --name-only` output (it prints the
#                            literal 12-char string "caf\303\251.txt"), so a
#                            capture that reads that output verbatim records a
#                            path no filesystem matches and both sides empty —
#                            while the sibling f.txt reads fine and makes the
#                            whole capture look successful. Only real git
#                            produces the quoting.
#               binary       — a MULTI-FILE content conflict where one path is
#                            binary (invalid UTF-8, embedded NUL). encoding/json
#                            substitutes U+FFFD for every invalid byte, so such
#                            a blob cannot round-trip through the context file;
#                            git is what decides the conflict has stages 1/2/3
#                            for a binary path at all.
#               operator-rebase — the worktree is parked mid-`git rebase -i` at
#                            an `edit` step with STAGED, uncommitted work. This
#                            is a rebase the pipeline did not start; `git rebase
#                            --abort` here throws away the operator's work. No
#                            conflict is present, so a capture probe reports zero
#                            unmerged paths.
#               gitlink      — a MULTI-FILE content conflict where one path is a
#                            SUBMODULE POINTER (index mode 160000). Its stage
#                            1/2/3 object ids are COMMITs living in the
#                            submodule's own object store, so
#                            `git cat-file blob <id>` in the superproject exits
#                            128 ("bad file"). Only git decides that a
#                            conflicted gitlink is listed by `ls-files -u` with
#                            mode 160000 alongside ordinary blob entries.
#               symlink      — a content conflict on a SYMLINK (index mode
#                            120000). A symlink IS a blob whose bytes are the
#                            target path, so it must keep reading normally — the
#                            control that stops the gitlink fix from being
#                            written as "anything unusual is metadata-only".
#               latin1-path  — a MULTI-FILE conflict where two of the paths have
#                            NAMES that are not valid UTF-8 ("caf\351.txt" and
#                            "caf\352.txt"). `jq --arg` rewrites every invalid
#                            byte to U+FFFD at exit 0, so such a name ships as a
#                            successful capture of a path nothing can open — and
#                            BOTH names become the same string, so a group_by on
#                            the path silently merges two conflicting files into
#                            one entry. Unlike every other mode this one leaves
#                            the rebase ALREADY PAUSED (see below).
#               linked-worktree — the ordinary content conflict, but the feature
#                            branch is checked out in a LINKED WORKTREE under
#                            .nightgauge/worktrees/issue-301 and the main
#                            checkout stays on main. That is the pipeline's
#                            normal, worktree-isolated shape (#275): the readers
#                            resolve the STAGE worktree, so a writer that
#                            resolves the main one writes where nothing looks.
#                            The path PRINTED for this mode is the linked
#                            worktree, i.e. the stage workspace.
#               mode-only    — an add/add conflict on an EMPTY placeholder file
#                            (.gitkeep / __init__.py / py.typed shape) added on
#                            both sides with DIFFERENT exec bits. git stages it
#                            as `100644 e69de29 2` / `100755 e69de29 3`: two
#                            present blob sides, both legitimately empty, and
#                            the conflict is the MODE. Only git decides that
#                            identical empty content with differing modes is
#                            still an unmerged path, and it is the one real
#                            conflict whose faithful record looks exactly like a
#                            failed blob read.
#
# Prints the path of the working clone on stdout. That clone is left checked out
# on the feature branch with origin/main already advanced, i.e. positioned
# exactly where BranchOutOfDate.Execute picks up: the caller runs the real
# `git fetch` + `git rebase` itself, so the failure under test is produced by
# git, not by the fixture.
#
# Layout:
#   <dest-dir>/origin.git   bare "remote"
#   <dest-dir>/work         clone, on branch feat/301-conflict-fixture
#
# Reproducible: every commit is made with pinned author/committer identity and
# pinned dates, so re-running produces the same history. Nothing is read from
# the ambient environment — no user gitconfig, no signing key, no hooks — so it
# behaves identically on a developer machine and on a bare CI runner.
set -euo pipefail

dest="${1:-}"
mode="${2:-conflict}"

modes="conflict|dirty-index|detached|unicode-path|binary|operator-rebase|gitlink|symlink|mode-only|latin1-path|linked-worktree"
if [ -z "$dest" ]; then
  echo "usage: $0 <dest-dir> [$modes]" >&2
  exit 2
fi
case "$mode" in
  conflict | dirty-index | detached | unicode-path | binary | operator-rebase | gitlink | symlink | mode-only | latin1-path | linked-worktree) ;;
  *)
    echo "unknown mode: $mode (want $modes)" >&2
    exit 2
    ;;
esac

# Pin identity and dates: a CI runner has no user.name/user.email, and an
# unpinned committer date would make the history differ run to run.
export GIT_AUTHOR_NAME="Nightgauge Fixture"
export GIT_AUTHOR_EMAIL="fixture@nightgauge.invalid"
export GIT_COMMITTER_NAME="Nightgauge Fixture"
export GIT_COMMITTER_EMAIL="fixture@nightgauge.invalid"
export GIT_AUTHOR_DATE="2026-01-01T00:00:00Z"
export GIT_COMMITTER_DATE="2026-01-01T00:00:00Z"
# Ignore the ambient gitconfig entirely (signing, hooks, templates, aliases).
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null

BRANCH="feat/301-conflict-fixture"

mkdir -p "$dest"
origin="$dest/origin.git"
work="$dest/work"

git init --quiet --bare --initial-branch=main "$origin"
git clone --quiet "$origin" "$work"

git -C "$work" config commit.gpgsign false
git -C "$work" config core.hooksPath /dev/null

# Extra conflicting path per mode. Its name/content is written the same three
# times (base, feature side, main side) so git produces a genuine three-stage
# conflict for it exactly as it does for f.txt.
#
#   unicode-path — a NON-ASCII filename. git C-quotes it in `diff --name-only`.
#   binary       — invalid UTF-8 with an embedded NUL, so git treats it as
#                  binary and encoding/json cannot represent its bytes.
extra=""
case "$mode" in
  unicode-path) extra="café.txt" ;;
  binary) extra="bin.dat" ;;
  gitlink) extra="sub" ;;
  symlink) extra="link" ;;
  mode-only) extra="n.txt" ;;
esac

# Three real commit objects for the gitlink to point at, one per side. They are
# built with `commit-tree` over the empty tree, so they are genuine commit
# objects in this repository's store with no branch pointing at them — which is
# exactly the shape a submodule pointer has from the superproject's side (the
# superproject never holds the submodule's objects either). Distinct messages
# give distinct object ids; the pinned identity/date keeps them reproducible.
if [ "$mode" = "gitlink" ]; then
  empty_tree=$(git -C "$work" hash-object -t tree -w /dev/null)
  gl_shared=$(git -C "$work" commit-tree "$empty_tree" -m "submodule commit: shared")
  gl_feature=$(git -C "$work" commit-tree "$empty_tree" -m "submodule commit: feature-side")
  gl_main=$(git -C "$work" commit-tree "$empty_tree" -m "submodule commit: main-side")
fi

# write_extra <variant> — emit the extra path's content for one side, for the
# modes whose extra path is a real file in the worktree. Byte sequences are
# written with printf octal escapes rather than /dev/urandom so the fixture is
# reproducible: \377 and \376 are never valid UTF-8 anywhere, and \000 is what
# makes git call the file binary.
#
# The gitlink mode has NO worktree file (an uninitialized submodule is an empty
# directory at most) — it is staged straight into the index by stage_extra.
write_extra() {
  [ -n "$extra" ] || return 0
  case "$mode" in
    unicode-path) printf 'line-1\n%s\nline-3\n' "$1" >"$work/$extra" ;;
    binary) printf '\377\376%s\000\001\002\377' "$1" >"$work/$extra" ;;
    symlink)
      rm -f "$work/$extra"
      ln -s "target-$1" "$work/$extra"
      ;;
    mode-only)
      # An add/add conflict: the path must NOT exist in the base commit, so the
      # shared variant writes nothing. Both sides then add the SAME (empty)
      # content with different exec bits, which is the whole conflict.
      [ "$1" = "shared" ] && return 0
      : >"$work/$extra"
      case "$1" in
        feature-side) chmod 755 "$work/$extra" ;;
        main-side) chmod 644 "$work/$extra" ;;
      esac
      ;;
  esac
}

# stage_extra <variant> — runs AFTER `git add -A`, for extras that exist only as
# an index entry. `git add -A` would otherwise stage the gitlink's DELETION
# (its path has no worktree file), so the order matters.
stage_extra() {
  [ "$mode" = "gitlink" ] || return 0
  local oid
  case "$1" in
    shared) oid="$gl_shared" ;;
    feature-side) oid="$gl_feature" ;;
    main-side) oid="$gl_main" ;;
    *)
      echo "stage_extra: unknown variant $1" >&2
      exit 2
      ;;
  esac
  git -C "$work" update-index --add --cacheinfo "160000,$oid,$extra"
}

# --- base commit on main -------------------------------------------------
printf 'line-1\nshared\nline-3\n' >"$work/f.txt"
write_extra shared
git -C "$work" add -A
stage_extra shared
git -C "$work" commit --quiet -m "base"
git -C "$work" push --quiet origin main

# --- feature branch edits the shared line --------------------------------
git -C "$work" checkout --quiet -b "$BRANCH"
printf 'line-1\nfeature-side\nline-3\n' >"$work/f.txt"
write_extra feature-side
git -C "$work" add -A
stage_extra feature-side
git -C "$work" commit --quiet -m "feat: rewrite shared line on the feature branch"

# --- main advances, editing the SAME line differently --------------------
git -C "$work" checkout --quiet main
printf 'line-1\nmain-side\nline-3\n' >"$work/f.txt"
write_extra main-side
git -C "$work" add -A
stage_extra main-side
git -C "$work" commit --quiet -m "fix: rewrite shared line on main"
git -C "$work" push --quiet origin main

# Rewind the local main so the clone looks like a normal feature checkout whose
# origin/main has moved ahead (that is the BEHIND state the recovery action
# fires on). The remote keeps the advanced main.
git -C "$work" reset --quiet --hard HEAD~1
git -C "$work" checkout --quiet "$BRANCH"

if [ "$mode" = "detached" ]; then
  # No branch is checked out at all. git records "detached HEAD" (not a
  # refs/heads/ ref) in rebase-merge/head-name, so there is genuinely no branch
  # name to put in the conflict context.
  git -C "$work" checkout --quiet --detach HEAD
fi

if [ "$mode" = "dirty-index" ]; then
  # A STAGED, uncommitted change makes git refuse the rebase outright:
  #   error: cannot rebase: Your index contains uncommitted changes.
  # The rebase fails, but no conflict is ever created — the unmerged-path probe
  # will succeed and report zero files. This is the empty-but-successful capture
  # that reads as a successful conflict capture on unfixed code.
  printf 'staged-but-uncommitted\n' >"$work/dirty.txt"
  git -C "$work" add dirty.txt
fi

if [ "$mode" = "operator-rebase" ]; then
  # Park the worktree mid-`git rebase -i` at an `edit` step, holding STAGED work
  # that exists nowhere else. This is the operator's rebase, not the pipeline's:
  # `git rebase --abort` here discards wip.txt permanently and reports nothing.
  #
  # GIT_SEQUENCE_EDITOR rewrites the todo list non-interactively. `ed`-style
  # in-place editing differs between BSD and GNU sed, so the rewrite goes
  # through a temp file instead of `sed -i`.
  seq_editor="$dest/seq-editor.sh"
  cat >"$seq_editor" <<'SEQEOF'
#!/usr/bin/env bash
set -euo pipefail
todo="$1"
sed '1s/^pick/edit/' "$todo" >"$todo.rewritten"
mv "$todo.rewritten" "$todo"
SEQEOF
  chmod +x "$seq_editor"
  GIT_SEQUENCE_EDITOR="$seq_editor" git -C "$work" rebase --quiet -i HEAD~1 >/dev/null
  printf 'operator work in progress\n' >"$work/wip.txt"
  git -C "$work" add wip.txt
fi

if [ "$mode" = "latin1-path" ]; then
  # A path NAME that is not valid UTF-8 is legal on ext4/xfs, but macOS/APFS
  # REFUSES to create one ("illegal byte sequence"), so it can never reach a
  # worktree here — while it is legal in a git INDEX on every platform, and the
  # index is what the capture reads. So this mode does not hand-author a fixture
  # shape either: git runs the real rebase (real content conflict on f.txt, real
  # rebase-merge/head-name, HEAD really detached), and then git's own
  # `update-index -z --index-info` adds stage 1/2/3 records for two names whose
  # bytes are not valid UTF-8. `-z` takes NUL-terminated records, so the raw
  # \351 / \352 bytes survive verbatim — the same way git itself stores them.
  #
  # Two such names, not one: they differ in the index and are the SAME string
  # once jq has replaced the invalid byte with U+FFFD, which is what let a
  # group_by(.path) merge two conflicting files into one entry and drop the
  # second from the document with capture_failed:false (#301).
  #
  # This is the one mode that leaves the rebase ALREADY IN PROGRESS: the extra
  # index records only exist inside the conflicted index, so the caller must not
  # start the rebase itself.
  git -C "$work" fetch --quiet origin main
  git -C "$work" rebase origin/main >/dev/null 2>&1 || true
  l1_base=$(printf 'line-1\nshared\nline-3\n' | git -C "$work" hash-object -t blob -w --stdin)
  l1_main=$(printf 'line-1\nmain-side\nline-3\n' | git -C "$work" hash-object -t blob -w --stdin)
  l1_feat=$(printf 'line-1\nfeature-side\nline-3\n' | git -C "$work" hash-object -t blob -w --stdin)
  # Stage 2 is the base and stage 3 the PR branch's work under a rebase, exactly
  # as git staged the real f.txt conflict a few lines above.
  {
    printf '100644 %s 1\tcaf\351.txt\000' "$l1_base"
    printf '100644 %s 2\tcaf\351.txt\000' "$l1_main"
    printf '100644 %s 3\tcaf\351.txt\000' "$l1_feat"
    printf '100644 %s 1\tcaf\352.txt\000' "$l1_base"
    printf '100644 %s 2\tcaf\352.txt\000' "$l1_main"
    printf '100644 %s 3\tcaf\352.txt\000' "$l1_feat"
  } | git -C "$work" update-index -z --index-info
fi

if [ "$mode" = "linked-worktree" ]; then
  # The pipeline's normal shape (#275): the stage runs in a LINKED worktree
  # under .nightgauge/worktrees/, while the main checkout sits on the default
  # branch. `git worktree list` prints the MAIN worktree first from anywhere, so
  # a writer that resolves it writes into a tree no reader consults — the
  # recovery loop reads <stage worktree>/.nightgauge/pipeline/ and feature-dev's
  # intake uses the relative path from the same cwd.
  #
  # The branch has to leave the main checkout first: git refuses to check out a
  # branch that is already checked out somewhere else.
  git -C "$work" checkout --quiet main
  git -C "$work" worktree add --quiet "$work/.nightgauge/worktrees/issue-301" "$BRANCH"
  # The STAGE worktree is the workspace under test, so that is what is printed.
  echo "$work/.nightgauge/worktrees/issue-301"
  exit 0
fi

echo "$work"
