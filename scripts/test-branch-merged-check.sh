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
# with one line built from FAKE_PR_* env vars (nothing at all if
# FAKE_PR_STATE is unset — an unauthenticated/no-PR forge) and
# `api repos/{owner}/{repo}/commits/<sha>` with FAKE_PR_PARENTS, one SHA per
# line, when <sha> matches FAKE_PR_SHA.
install_fake_gh() {
  [ -n "$FAKE_BIN" ] && return 0
  FAKE_BIN="$(mktemp -d)"
  cat >"$FAKE_BIN/gh" <<'FAKE_GH'
#!/usr/bin/env bash
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  if [ -n "${FAKE_PR_STATE:-}" ]; then
    printf '%s\t%s\t%s\t%s\n' "$FAKE_PR_STATE" "$FAKE_PR_BRANCH" "$FAKE_PR_SHA" "$FAKE_PR_NUM"
  fi
  exit 0
fi
if [ "$1" = "api" ]; then
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
    printf '%s\n' "$out" | grep -qF -- "$must_contain" || ok=0
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

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf '\033[31m%s passed, %s FAILED\033[0m\n' "$PASS" "$FAIL"
  exit 1
fi
printf '\033[32mall %s branch-merged-check tests passed\033[0m\n' "$PASS"
