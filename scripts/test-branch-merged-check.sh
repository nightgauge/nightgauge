#!/usr/bin/env bash
# Regression tests for scripts/branch-merged-check.sh.
#
# Builds real "origin + clone" git fixtures — one branch per linked worktree,
# mirroring the Go sweep's own fixture shape in
# internal/execution/worktree_sweep_test.go, so both decision procedures are
# exercised against the same topology — and drives the WORKING-TREE copy of
# the script under test against them, never a committed copy, so editing the
# script and running this suite locally actually proves something about the
# edit.
#
# The forge-lookup cases stub `gh` with a fake executable placed first on
# PATH, the same "no live network" approach the sweep tests use for
# WorktreeSweepOptions.MergedPRLookup — no real GitHub repo or token needed.
#
# Run: bash scripts/test-branch-merged-check.sh
# Also run by scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

SCRIPT="$PWD/scripts/branch-merged-check.sh"
PASS=0
FAIL=0
TMP=""
FAKE_BIN=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  [ -n "$FAKE_BIN" ] && rm -rf "$FAKE_BIN"
  return 0
}
trap cleanup EXIT

git_in() {
  local dir="$1"
  shift
  if ! git -C "$dir" "$@" >/dev/null 2>&1; then
    echo "git $* (in $dir) failed" >&2
    exit 1
  fi
}

# run_in <dir> <cmd...> — run a command with dir as cwd, the way
# branch-merged-check.sh expects to be invoked (it shells bare `git`, no -C).
run_in() {
  local dir="$1"
  shift
  (cd "$dir" && "$@")
}

# new_fixture builds an origin.git + clone pair with one initial commit on
# main and sets the global $TMP to the fixture root ($TMP/clone is the
# clone). Not called via command substitution: it must mutate the caller's
# $TMP directly for `cleanup`'s trap to find it, and command substitution
# would run it in a subshell where that mutation is lost. The clone stays on
# `main` for the whole fixture's life — every branch under test gets its own
# linked worktree, the same shape addWorktree gives the Go sweep tests.
new_fixture() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  TMP="$(mktemp -d)"
  local origin="$TMP/origin.git" clone="$TMP/clone" seed="$TMP/seed"
  git init -q --bare -b main "$origin"
  mkdir -p "$seed"
  git_in "$seed" init -q -b main
  git -C "$seed" config user.email test@test
  git -C "$seed" config user.name test
  printf 'hello\n' >"$seed/README"
  git_in "$seed" add .
  git_in "$seed" commit -q -m initial
  git_in "$seed" remote add origin "$origin"
  git_in "$seed" push -q -u origin main
  git_in "$TMP" clone -q "$origin" "$clone"
  git -C "$clone" config user.email test@test
  git -C "$clone" config user.name test
}

# add_worktree <root> <name> <branch> — a linked worktree on a new branch cut
# from origin/main, and prints its path.
add_worktree() {
  local root="$1" name="$2" branch="$3"
  local wt="$TMP/wt-$name"
  git_in "$root" worktree add -q "$wt" -b "$branch" origin/main
  printf '%s' "$wt"
}

# commit_in <dir> <relpath> <content> — write, add, commit inside dir.
commit_in() {
  local dir="$1" rel="$2" content="$3"
  printf '%s' "$content" >"$dir/$rel"
  git_in "$dir" add "$rel"
  git_in "$dir" commit -q -m "work: $rel"
}

# commit_to_main <root> <relpath> <content> — commit directly to main (root
# must be checked out on main) and push, so origin/main carries it.
commit_to_main() {
  local root="$1" rel="$2" content="$3"
  printf '%s' "$content" >"$root/$rel"
  git_in "$root" add "$rel"
  git_in "$root" commit -q -m "seed: $rel"
  git_in "$root" push -q origin main
  git_in "$root" fetch -q origin
}

# squash_merge_to_main <root> <ref> — reproduce `gh pr merge --squash`: ref's
# TREE lands on main as one new commit. root must be on main.
squash_merge_to_main() {
  local root="$1" ref="$2"
  git_in "$root" merge --squash "$ref"
  git_in "$root" commit -q -m "squash: $ref"
  git_in "$root" push -q origin main
  git_in "$root" fetch -q origin
}

# install_fake_gh puts a scripted `gh` first on PATH. It answers `pr list`
# with one line built from FAKE_PR_* env vars (FAKE_PR_BASE is the base
# branch, default main), `issue view` with FAKE_ISSUE_STATE (fails if unset), (nothing at all if
# FAKE_PR_STATE is unset — an unauthenticated/no-PR forge) and
# `api repos/{owner}/{repo}/commits/<sha>` with FAKE_PR_PARENTS, one SHA per
# line, when <sha> matches FAKE_PR_SHA. FAKE_PR_FILLER=<n> appends n merged
# PRs for unrelated branches after that line, to make the index larger than
# any pipe buffer (#2360). FAKE_PR_LIST_STATUS=<n> makes `pr list` fail with
# status n, as an unauthenticated or offline gh does.
install_fake_gh() {
  [ -n "$FAKE_BIN" ] && return 0
  FAKE_BIN="$(mktemp -d)"
  cat >"$FAKE_BIN/gh" <<'FAKE_GH'
#!/usr/bin/env bash
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  if [ -n "${FAKE_PR_LIST_STATUS:-}" ]; then
    echo "gh: simulated failure" >&2
    exit "$FAKE_PR_LIST_STATUS"
  fi
  if [ -n "${FAKE_PR_STATE:-}" ]; then
    printf '%s\t%s\t%s\t%s\t%s\n' "$FAKE_PR_STATE" "$FAKE_PR_BRANCH" "$FAKE_PR_SHA" "$FAKE_PR_NUM" "${FAKE_PR_BASE:-main}"
  fi
  awk -v n="${FAKE_PR_FILLER:-0}" 'BEGIN {
    for (i = 1; i <= n; i++) printf "MERGED\tfiller/%05d-branch\t%040d\t%d\tmain\n", i, i, 50000 + i
  }'
  exit 0
fi
if [ "$1" = "issue" ] && [ "$2" = "view" ]; then
  [ -n "${FAKE_ISSUE_STATE:-}" ] || exit 1
  printf '%s\n' "$FAKE_ISSUE_STATE"
  exit 0
fi
if [ "$1" = "api" ]; then
  # commits/<sha>/pulls: FAKE_FOLD_ROWS ("<num>\t<head>\t<base>" per line,
  # already in the --jq shape) when <sha> is FAKE_FOLD_TIP; compare/<a>...<b>:
  # FAKE_FOLD_STATUS. Both empty otherwise (#2313).
  case "$2" in
  */pulls)
    tip="${2%/pulls}"
    tip="${tip##*/}"
    [ "$tip" = "${FAKE_FOLD_TIP:-}" ] && [ -n "${FAKE_FOLD_ROWS:-}" ] && printf '%b\n' "$FAKE_FOLD_ROWS"
    exit 0
    ;;
  */compare/*)
    [ -n "${FAKE_FOLD_STATUS:-}" ] && printf '%s\n' "$FAKE_FOLD_STATUS"
    exit 0
    ;;
  esac
  sha="${2##*/}"
  if [ "$sha" = "${FAKE_PR_SHA:-}" ]; then
    for p in ${FAKE_PR_PARENTS:-}; do
      printf '%s\n' "$p"
    done
  fi
  exit 0
fi
exit 1
FAKE_GH
  chmod +x "$FAKE_BIN/gh"
}

# failing_tool <tool> <needle> -> prints a directory holding a wrapper named
# <tool> that exits 2, as a tool that could not run, when any argument
# contains <needle>, and otherwise runs the real <tool>. Put first on PATH to
# make one lookup fail without touching the others (#2360).
failing_tool() {
  local tool="$1" needle="$2" real dir
  real="$(command -v "$tool")" || {
    echo "HARNESS ERROR: no $tool on PATH to wrap" >&2
    exit 1
  }
  dir="$(mktemp -d "$TMP/fail-$tool.XXXXXX")"
  # shellcheck disable=SC2016 # the wrapper's own "$@" and "$a", written as text
  {
    printf '#!/usr/bin/env bash\n'
    printf 'for a in "$@"; do\n'
    printf '  case "$a" in *%q*) echo "%s: simulated failure" >&2; exit 2 ;; esac\n' "$needle" "$tool"
    printf 'done\n'
    printf 'exec %q "$@"\n' "$real"
  } >"$dir/$tool"
  chmod +x "$dir/$tool"
  printf '%s' "$dir"
}

# expect <want_exit_code> <desc> [must_contain] -- <run...>
expect() {
  local want="$1" desc="$2" must_contain="$3"
  shift 3
  [ "$1" = "--" ] && shift
  local out code ok=1
  out="$("$@" 2>&1)"
  code=$?
  [ "$code" -eq "$want" ] || ok=0
  if [ "$ok" = "1" ] && [ -n "$must_contain" ]; then
    grep -qF -- "$must_contain" <<<"$out" || ok=0
  fi
  if [ "$ok" = "1" ]; then
    printf '  \033[32m✓\033[0m %s\n' "$desc"
    PASS=$((PASS + 1))
  else
    printf '  \033[31m✗\033[0m %s — wanted exit %s%s, got %s\n' \
      "$desc" "$want" "${must_contain:+ naming '$must_contain'}" "$code"
    printf '%s\n' "$out" | sed 's/^/        /'
    FAIL=$((FAIL + 1))
  fi
}

echo "branch-merged-check.sh — regression tests"
echo ""

# ── (a) tip is an ancestor of base — cheapest positive case ────────────────
new_fixture
root="$TMP/clone"
wt="$(add_worktree "$root" 701 fix/701-noop)"
git_in "$root" worktree remove "$wt" --force
expect 0 "an ancestor tip is SAFE-DELETE" "ancestor" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" fix/701-noop origin/main

# ── (b) squash-merged content matches base — no forge needed ───────────────
new_fixture
root="$TMP/clone"
wt="$(add_worktree "$root" 702 fix/702-thing)"
commit_in "$wt" fix.txt "fixed
"
squash_merge_to_main "$root" fix/702-thing
git_in "$root" worktree remove "$wt" --force
expect 0 "squash-merged content identical to base is SAFE-DELETE" "content identical" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" fix/702-thing origin/main

# ── (c) genuinely unmerged content — KEEP ───────────────────────────────────
new_fixture
root="$TMP/clone"
wt="$(add_worktree "$root" 703 fix/703-unmerged)"
commit_in "$wt" fix.txt "not merged anywhere
"
git_in "$root" worktree remove "$wt" --force
expect 1 "unmerged content is KEEP" "" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" fix/703-unmerged origin/main

# ── (d) a branch checked out in a worktree is KEEP no matter what ──────────
new_fixture
root="$TMP/clone"
add_worktree "$root" 704 fix/704-inuse >/dev/null
expect 1 "a branch a worktree holds is KEEP" "checked out in a worktree" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" fix/704-inuse origin/main

# ── (e) ancestry acceptance: update-branch merge commit never reached local ─
# #593 — the residual case: branch cut before P, P evolves a file the branch
# also touches, `gh pr update-branch` + squash-merge. The update-branch merge
# commit lives only on the PR's remote head; this checkout's branch ref never
# advances past its pre-update-branch tip, so content diff alone still reads
# unmerged even though the branch is fully landed.
new_fixture
root="$TMP/clone"
commit_to_main "$root" shared.txt "line1
line2
line3
line4
line5
"
wt="$(add_worktree "$root" 585 fix/585-cost-stamps)"
commit_in "$wt" shared.txt "line1
line2
line3
line4
line5-edited-by-B
"
tip="$(git -C "$wt" rev-parse HEAD)"
commit_to_main "$root" shared.txt "line1-edited-by-P
line2
line3
line4
line5
"
scratch="$TMP/pr-remote-head"
git_in "$root" worktree add -q --detach "$scratch" "$tip"
git_in "$scratch" merge -q origin/main -m "merge origin/main (update-branch)"
pr_head="$(git -C "$scratch" rev-parse HEAD)"
pr_parents="$(git -C "$root" log -1 --format=%P "$pr_head")"
git_in "$root" worktree remove "$scratch" --force
case " $pr_parents " in
*" $tip "*) ;;
*)
  echo "fixture bug: local branch tip $tip is not among simulated PR head parents: $pr_parents" >&2
  exit 1
  ;;
esac
git_in "$root" worktree remove "$wt" --force
squash_merge_to_main "$root" "$pr_head"

expect 1 "content diff alone still reads the update-branch shape as KEEP" "" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" fix/585-cost-stamps origin/main

install_fake_gh
export FAKE_PR_STATE=MERGED FAKE_PR_BRANCH=fix/585-cost-stamps FAKE_PR_NUM=588
export FAKE_PR_SHA="$pr_head" FAKE_PR_PARENTS="$pr_parents"
expect 0 "tip as a parent of the merged PR head is SAFE-DELETE (update-branch)" \
  "parent of the merged head" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" \
  FAKE_PR_STATE="$FAKE_PR_STATE" FAKE_PR_BRANCH="$FAKE_PR_BRANCH" FAKE_PR_NUM="$FAKE_PR_NUM" \
  FAKE_PR_SHA="$FAKE_PR_SHA" FAKE_PR_PARENTS="$FAKE_PR_PARENTS" "$SCRIPT" fix/585-cost-stamps origin/main

# ── (f) …but NO_PR=1 stays conservative even with the same forge data ──────
expect 1 "NO_PR=1 stays conservative (KEEP) even when gh would say SAFE-DELETE" "" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" NO_PR=1 \
  FAKE_PR_STATE="$FAKE_PR_STATE" FAKE_PR_BRANCH="$FAKE_PR_BRANCH" FAKE_PR_NUM="$FAKE_PR_NUM" \
  FAKE_PR_SHA="$FAKE_PR_SHA" FAKE_PR_PARENTS="$FAKE_PR_PARENTS" "$SCRIPT" fix/585-cost-stamps origin/main

# ── (g) a merged PR whose head is neither the tip nor a parent stays KEEP ──
root_sha="$(git -C "$root" rev-parse main~2)"
expect 1 "a merged PR head whose parents exclude the tip stays KEEP" "commits past the merge" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" \
  FAKE_PR_STATE=MERGED FAKE_PR_BRANCH=fix/585-cost-stamps FAKE_PR_NUM=999 \
  FAKE_PR_SHA="$pr_head" FAKE_PR_PARENTS="$root_sha" \
  "$SCRIPT" fix/585-cost-stamps origin/main

unset FAKE_PR_STATE FAKE_PR_BRANCH FAKE_PR_NUM FAKE_PR_SHA FAKE_PR_PARENTS

# ── (h) remote-only ancestor tip is SAFE-DELETE, judged from the remote ────
# (#1990) The concrete repro: a worktree removal deletes the LOCAL branch,
# but a push already updated the local remote-tracking ref
# (refs/remotes/origin/<branch>), which survives. When that remote tip is an
# ancestor of base, this is the "obvious answer" #1990 reports has no
# sanctioned path to today.
new_fixture
root="$TMP/clone"
wt="$(add_worktree "$root" 991 fix/991-remote-safe)"
commit_in "$wt" file.txt "remote-safe content
"
git_in "$wt" push -q origin fix/991-remote-safe
git_in "$root" merge -q fix/991-remote-safe
git_in "$root" push -q origin main
git_in "$root" worktree remove "$wt" --force
git_in "$root" branch -D fix/991-remote-safe
if git -C "$root" rev-parse --verify --quiet refs/heads/fix/991-remote-safe >/dev/null 2>&1; then
  echo "fixture bug: local branch fix/991-remote-safe should be gone" >&2
  exit 1
fi
if ! git -C "$root" rev-parse --verify --quiet refs/remotes/origin/fix/991-remote-safe >/dev/null 2>&1; then
  echo "fixture bug: refs/remotes/origin/fix/991-remote-safe should survive the local branch delete" >&2
  exit 1
fi
expect 0 "remote-only ancestor tip is SAFE-DELETE, judged from the remote tip" \
  "remote-only ref, judged from the remote tip" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" fix/991-remote-safe origin/main

# ── (i) remote-only, NOT an ancestor — stays non-zero (KEEP) ───────────────
# The other half of the same repro: the local branch is gone the same way,
# but the remote tip carries content base does not have. Judging it from the
# remote tip must not turn an unmerged branch safe.
new_fixture
root="$TMP/clone"
wt="$(add_worktree "$root" 992 fix/992-remote-unmerged)"
commit_in "$wt" file.txt "never merged anywhere
"
git_in "$wt" push -q origin fix/992-remote-unmerged
git_in "$root" worktree remove "$wt" --force
git_in "$root" branch -D fix/992-remote-unmerged
expect 1 "remote-only, unmerged content stays KEEP (non-zero), judged from the remote tip" \
  "remote-only ref, judged from the remote tip" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" fix/992-remote-unmerged origin/main

# ── (j) no ref anywhere — still UNKNOWN/2 with the unchanged "no such ref" ─
# text, distinct from the remote-only diagnosis above. #1990 asks these two
# to read as different diagnoses; this pins the untouched one.
new_fixture
root="$TMP/clone"
expect 2 "no ref anywhere (local or remote) stays UNKNOWN/2 with the original text" \
  "no such ref: fix/993-never-existed" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" fix/993-never-existed origin/main

# ── (k) stale cached tracking ref — UNKNOWN/2, not a false SAFE-DELETE ─────
# The remote-only path must not trust a cached
# refs/remotes/origin/<branch>: nothing refreshes it between fetches. A
# SECOND clone pushes a new commit to the same branch AFTER this repo's own
# push/fetch, so this repo's cache is stale relative to the live remote —
# the exact shape the reviewer reproduced (tracking ref 96a7c6b vs remote
# 2e515c3, false SAFE-DELETE).
new_fixture
root="$TMP/clone"
wt="$(add_worktree "$root" 994 fix/994-stale-tracking)"
commit_in "$wt" file.txt "v1 content
"
git_in "$wt" push -q origin fix/994-stale-tracking
git_in "$root" worktree remove "$wt" --force
git_in "$root" branch -D fix/994-stale-tracking
clone2="$TMP/clone2"
git_in "$TMP" clone -q "$TMP/origin.git" "$clone2"
git -C "$clone2" config user.email test@test
git -C "$clone2" config user.name test
git_in "$clone2" checkout -q fix/994-stale-tracking
commit_in "$clone2" file.txt "v2 content — pushed by a second clone after root's own push
"
git_in "$clone2" push -q origin fix/994-stale-tracking
expect 2 "a stale cached tracking ref is UNKNOWN/2, not a false SAFE-DELETE" \
  "STALE" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" fix/994-stale-tracking origin/main

# ── (l) branch is HEAD — refuses unconditionally, in every mode ───────────
new_fixture
root="$TMP/clone"
expect 2 "branch is HEAD refuses rather than judging git's own current-commit pointer" \
  "branch is HEAD" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" HEAD origin/main

# ── (m) remote-only branch name equal to the base's own short name ────────
# A base branch that is only fetched (never checked out) in this repo is
# remote-only under the SAME name as $base — judging it against itself
# would trivially read "ancestor" (SAFE-DELETE). Built with a base other
# than main so this repo's own checkout branch (main) does not shadow it.
new_fixture
root="$TMP/clone"
git_in "$root" push -q origin main:release
git_in "$root" fetch -q origin
if git -C "$root" rev-parse --verify --quiet refs/heads/release >/dev/null 2>&1; then
  echo "fixture bug: refs/heads/release should not exist locally (fetched-only)" >&2
  exit 1
fi
expect 2 "a remote-only branch sharing the base's own short name refuses to compare it to itself" \
  "base branch's own name" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" release origin/release

# ── (n) an open PR whose BASE is the branch is KEEP (#2175) ───────────────
# The live repro: a fresh epic branch (tip == main, an ancestor) that a
# feature branch targets as its base. Local and remote-only.
new_fixture
root="$TMP/clone"
git_in "$root" push -q origin main:stack/2175-base
git_in "$root" fetch -q origin
install_fake_gh
expect 1 "remote-only branch that an open PR targets as base is KEEP" \
  "targets this branch as its base" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" \
  FAKE_PR_STATE=OPEN FAKE_PR_BRANCH=feat/2176-x FAKE_PR_SHA=deadbeef FAKE_PR_NUM=2177 \
  FAKE_PR_BASE=stack/2175-base "$SCRIPT" stack/2175-base origin/main
git_in "$root" branch -q stack/2175-base origin/stack/2175-base
expect 1 "local branch that an open PR targets as base is KEEP" \
  "targets this branch as its base" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" \
  FAKE_PR_STATE=OPEN FAKE_PR_BRANCH=feat/2176-x FAKE_PR_SHA=deadbeef FAKE_PR_NUM=2177 \
  FAKE_PR_BASE=stack/2175-base "$SCRIPT" stack/2175-base origin/main
expect 0 "the same ancestor branch with no PR based on it is still SAFE-DELETE" \
  "ancestor" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" \
  FAKE_PR_STATE=OPEN FAKE_PR_BRANCH=feat/2176-x FAKE_PR_SHA=deadbeef FAKE_PR_NUM=2177 \
  FAKE_PR_BASE=main "$SCRIPT" stack/2175-base origin/main

# ── (o) an epic/<N>-… branch is KEEP while issue N is open (#2175) ─────────
new_fixture
root="$TMP/clone"
git_in "$root" push -q origin main:epic/2085-doctor
git_in "$root" fetch -q origin
expect 1 "remote-only epic branch with its issue open is KEEP" \
  "epic branch for open issue #2085" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_ISSUE_STATE=OPEN \
  "$SCRIPT" epic/2085-doctor origin/main
expect 0 "remote-only epic branch with its issue closed falls through to SAFE-DELETE" \
  "ancestor" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_ISSUE_STATE=CLOSED \
  "$SCRIPT" epic/2085-doctor origin/main
expect 2 "epic branch whose issue state cannot be looked up is UNKNOWN" \
  "cannot confirm the issue is closed" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" epic/2085-doctor origin/main
git_in "$root" branch -q epic/2085-doctor origin/epic/2085-doctor
expect 1 "local epic branch with its issue open is KEEP" \
  "epic branch for open issue #2085" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_ISSUE_STATE=OPEN \
  "$SCRIPT" epic/2085-doctor origin/main

# ── (p) a branch folded into another PR by a merge commit (#2313) ─────────
# The batching shape: fix/801-sub is merged into feat/800-batch, which is
# squash-merged as its own PR; main then changes the same file. The sub-branch
# has no PR, is not an ancestor of main, and its content differs from main.
new_fixture
root="$TMP/clone"
sub="$(add_worktree "$root" 801 fix/801-sub)"
commit_in "$sub" fix.txt "sub-branch work
"
batch="$(add_worktree "$root" 800 feat/800-batch)"
git_in "$batch" merge -q --no-ff fix/801-sub -m "merge fix/801-sub"
commit_in "$batch" other.txt "batch work
"
squash_merge_to_main "$root" feat/800-batch
commit_to_main "$root" fix.txt "main moved on
"
fold_tip="$(git -C "$root" rev-parse fix/801-sub)"
fold_head="$(git -C "$root" rev-parse feat/800-batch)"
git_in "$root" worktree remove "$sub" --force
git_in "$root" worktree remove "$batch" --force
expect 1 "a folded branch without the forge is KEEP" "" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" fix/801-sub origin/main
expect 0 "a tip inside a merged PR's head is SAFE-DELETE (folded)" \
  "folded into PR #900" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_FOLD_TIP="$fold_tip" \
  FAKE_FOLD_ROWS="900\t$fold_head\tmain" FAKE_FOLD_STATUS=ahead \
  "$SCRIPT" fix/801-sub origin/main
expect 1 "a folded tip the PR head does not contain stays KEEP" "" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_FOLD_TIP="$fold_tip" \
  FAKE_FOLD_ROWS="900\t$fold_head\tmain" FAKE_FOLD_STATUS=diverged \
  "$SCRIPT" fix/801-sub origin/main
expect 1 "a PR that merged into another base stays KEEP" "" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_FOLD_TIP="$fold_tip" \
  FAKE_FOLD_ROWS="900\t$fold_head\trelease" FAKE_FOLD_STATUS=ahead \
  "$SCRIPT" fix/801-sub origin/main
expect 1 "a tip the forge knows no PR for stays KEEP" "" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_FOLD_STATUS=ahead \
  "$SCRIPT" fix/801-sub origin/main

# ── (q) an open PR on the first row of a large PR index is KEEP (#2360) ────
# The open-PR lookups piped the index into `awk '… {…; exit}'` under pipefail.
# awk stops reading at its match; with the matching row first and over half
# a megabyte behind it, printf was still writing, died of SIGPIPE, and the
# lookup read as "no open PR". This branch is an ancestor of main, so the rule
# after those lookups answered SAFE-DELETE: exit 0, permission to delete the
# head (or the base) of an open PR, every time, on any machine. The real index
# holds up to 500 PRs, whose rows can outgrow a pipe buffer the same way.
new_fixture
root="$TMP/clone"
git_in "$root" branch -q fix/4100-open-head main
install_fake_gh
expect 1 "an open PR on the first row of a large PR index is KEEP" \
  "deleting this branch would close it" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_PR_FILLER=8000 \
  FAKE_PR_STATE=OPEN FAKE_PR_BRANCH=fix/4100-open-head FAKE_PR_SHA=deadbeef FAKE_PR_NUM=4101 \
  "$SCRIPT" fix/4100-open-head origin/main
expect 1 "an open PR based on the branch, first in a large PR index, is KEEP" \
  "targets this branch as its base" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_PR_FILLER=8000 \
  FAKE_PR_STATE=OPEN FAKE_PR_BRANCH=feat/4102-stacked FAKE_PR_SHA=deadbeef FAKE_PR_NUM=4103 \
  FAKE_PR_BASE=fix/4100-open-head "$SCRIPT" fix/4100-open-head origin/main
# The control: the same large index with no open PR for the branch leaves the
# ancestor rule its SAFE-DELETE, so the KEEPs above come from the open row.
expect 0 "the same branch in a large PR index with no open PR is SAFE-DELETE" "ancestor" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_PR_FILLER=8000 \
  "$SCRIPT" fix/4100-open-head origin/main

# ── (r) a guard lookup that cannot run is UNKNOWN, never SAFE-DELETE (#2360) ─
# The open-PR and worktree lookups guard every SAFE-DELETE. A failed one used
# to read as "none found", and this branch, an ancestor of main, then read
# SAFE-DELETE although the forge had just listed an open PR for it. Each arm
# below breaks one lookup and wants UNKNOWN (exit 2). The (q) arms above are
# the controls: the same fixture and index, with every lookup able to run.
# shellcheck disable=SC2016 # the awk program text to match, not an expansion
awk_dir="$(failing_tool awk '$1=="OPEN"')"
expect 2 "an open-PR lookup whose awk fails is UNKNOWN" \
  "the open-PR lookup did not run (status 2)" \
  -- run_in "$root" env PATH="$awk_dir:$FAKE_BIN:$PATH" \
  FAKE_PR_STATE=OPEN FAKE_PR_BRANCH=fix/4100-open-head FAKE_PR_SHA=deadbeef FAKE_PR_NUM=4101 \
  "$SCRIPT" fix/4100-open-head origin/main
git_dir="$(failing_tool git worktree)"
expect 2 "a worktree list git cannot produce is UNKNOWN" "\`git worktree list\` failed" \
  -- run_in "$root" env PATH="$git_dir:$FAKE_BIN:$PATH" "$SCRIPT" fix/4100-open-head origin/main

# The cause seen in review: bash writes a here-string larger than a pipe
# buffer to a temporary file, and when it cannot (a full or read-only temp
# directory, here a file-size limit of zero) the lookup never runs. Probed
# first, because a bash that needs no file for it cannot fail this way.
# shellcheck disable=SC2016 # expands in the probe's own shell
if bash -c 'trap "" XFSZ; ulimit -f 0
  v="$(awk "BEGIN { for (i = 0; i < 8000; i++) printf \"%070d\\n\", i }")"
  cat <<<"$v" >/dev/null' 2>/dev/null; then
  echo "  - skipped: this bash writes a 560 KB here-string without a temporary file"
else
  # shellcheck disable=SC2016 # expands in the wrapper's own shell
  expect 2 "an open PR the lookup cannot read (no room for its temp file) is UNKNOWN" \
    "lookup did not run" \
    -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_PR_FILLER=8000 \
    FAKE_PR_STATE=OPEN FAKE_PR_BRANCH=fix/4100-open-head FAKE_PR_SHA=deadbeef FAKE_PR_NUM=4101 \
    bash -c 'trap "" XFSZ; ulimit -f 0; exec "$@"' _ "$SCRIPT" fix/4100-open-head origin/main
fi

# ── (s) a content diff that cannot run is UNKNOWN, never SAFE-DELETE ───────
# The residual diff decides "content identical", the SAFE-DELETE for a
# squash-merged branch. Its pipeline's status was ignored, so a `git diff
# --stat` that failed, or an xargs that could not start git, left an empty
# residual, which read as identical content: exit 0 for a branch carrying a
# commit main does not have. The first arm is the control: the same branch,
# every tool able to run, is KEEP.
new_fixture
root="$TMP/clone"
wt="$(add_worktree "$root" 4200 fix/4200-unmerged)"
commit_in "$wt" fix.txt "not merged anywhere
"
git_in "$root" worktree remove "$wt" --force
expect 1 "a branch with a commit main lacks is KEEP (control)" "1 file changed" \
  -- run_in "$root" env NO_PR=1 "$SCRIPT" fix/4200-unmerged origin/main
stat_dir="$(failing_tool git --stat)"
expect 2 "a content diff git cannot produce is UNKNOWN" "the content diff did not run" \
  -- run_in "$root" env PATH="$stat_dir:$PATH" NO_PR=1 "$SCRIPT" fix/4200-unmerged origin/main
xargs_dir="$(failing_tool xargs -0)"
expect 2 "a content diff xargs cannot start is UNKNOWN" "the content diff did not run" \
  -- run_in "$root" env PATH="$xargs_dir:$PATH" NO_PR=1 "$SCRIPT" fix/4200-unmerged origin/main
names_dir="$(failing_tool git --name-only)"
expect 2 "a file list git cannot produce is UNKNOWN, not \"touches no files\"" \
  "the file list did not run" \
  -- run_in "$root" env PATH="$names_dir:$PATH" NO_PR=1 "$SCRIPT" fix/4200-unmerged origin/main

# ── (t) a PR index gh cannot fetch is UNKNOWN, never "no open PR" ─────────
# Both open-PR guards read the index. When gh could not fetch it (not
# installed, unauthenticated, offline), the index was empty, each guard read
# "no open PR", and an ancestor branch read SAFE-DELETE although no guard had
# looked. Only NO_PR=1, asked for by name, judges on content alone.
new_fixture
root="$TMP/clone"
git_in "$root" branch -q fix/4300-ancestor main
install_fake_gh
expect 0 "an ancestor branch with a fetched, empty PR index is SAFE-DELETE (control)" "ancestor" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" "$SCRIPT" fix/4300-ancestor origin/main
expect 2 "a PR index gh pr list could not fetch is UNKNOWN" "gh pr list failed (status 1)" \
  -- run_in "$root" env PATH="$FAKE_BIN:$PATH" FAKE_PR_LIST_STATUS=1 \
  "$SCRIPT" fix/4300-ancestor origin/main
# A PATH with every tool the checker runs, and no gh.
nogh_bin="$(mktemp -d "$TMP/nogh.XXXXXX")"
for tool in bash git awk sed grep xargs cut tail; do
  real="$(command -v "$tool")" || {
    echo "HARNESS ERROR: no $tool on PATH to link" >&2
    exit 1
  }
  ln -s "$real" "$nogh_bin/$tool"
done
expect 2 "with no gh installed, the open-PR guards cannot look: UNKNOWN" "gh is not installed" \
  -- run_in "$root" env PATH="$nogh_bin" "$SCRIPT" fix/4300-ancestor origin/main
expect 0 "with no gh and NO_PR=1, an ancestor branch is SAFE-DELETE on content alone" "ancestor" \
  -- run_in "$root" env PATH="$nogh_bin" NO_PR=1 "$SCRIPT" fix/4300-ancestor origin/main

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf '\033[31m%s passed, %s FAILED\033[0m\n' "$PASS" "$FAIL"
  exit 1
fi
printf '\033[32mall %s branch-merged-check tests passed\033[0m\n' "$PASS"
