#!/usr/bin/env bash
#
# Regression suite for scripts/publication-push-guard.sh (#2365), the pre-push
# half of the publication boundary.
#
# Every case is a REAL `git push` through the hook, from a throwaway clone to a
# throwaway bare repository whose path ends in nightgauge/nightgauge, so the
# guard takes it for the public repository. Each refusal is then confirmed on
# the remote: the ref is absent AND the pushed commit's object never arrived. A
# guard that printed the right refusal while git sent the objects anyway would
# be exactly the failure it exists to prevent, so the words alone prove nothing.
#
# The throwaway history carries this checkout's checker, manifest, guard and
# hook, so the code under test is the working copy, uncommitted edits included.
# The hook is wired the way husky wires it: `sh -e .husky/pre-push "$@"`.
#
# Exit 0 when every case passes, 1 when a case fails. A fixture that cannot be
# built prints a HARNESS ERROR line and exits 2: that run asserted nothing.
#
# Run: bash scripts/test-publication-push-guard.sh

set -uo pipefail

REPO="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "HARNESS ERROR: run this from inside the nightgauge checkout"
  exit 2
}

# Hermetic git. A global core.hooksPath, commit signing or a missing identity
# would change what these pushes do, and a GIT_DIR inherited from a calling hook
# would point every command below at the wrong repository.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY \
  GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_PREFIX GIT_CONFIG_PARAMETERS \
  GIT_CONFIG_COUNT
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
unset HUSKY

harness() {
  echo "HARNESS ERROR: $*"
  exit 2
}

python3 -c 'import yaml' 2>/dev/null ||
  harness "python3 with PyYAML is required (pip install -r .github/requirements-ci.txt)"

tmp="$(mktemp -d)" || harness "mktemp failed"
trap 'rm -rf "$tmp"' EXIT

g() { git "$@" >/dev/null 2>&1; }

seed="$tmp/seed"
public="$tmp/remote/nightgauge/nightgauge.git"
elsewhere="$tmp/remote/someone/elsewhere.git"
other="$tmp/other"
work="$tmp/work"

# ── The public repository: two commits on main, with (#N) merge markers so the
#    checker can derive its reference ceiling, and two so a depth-1 clone of it
#    is really shallow.
{
  g init -q -b main "$seed" &&
    mkdir -p "$seed/scripts" "$seed/.github" "$seed/.husky" "$seed/docs" &&
    cp "$REPO/scripts/publication-boundary-check.py" \
      "$REPO/scripts/publication-push-guard.sh" "$seed/scripts/" &&
    cp "$REPO/.github/publication-boundary.yaml" "$seed/.github/" &&
    cp "$REPO/.husky/pre-push" "$seed/.husky/" &&
    printf '# seed\n' >"$seed/README.md" &&
    g -C "$seed" add -A &&
    g -C "$seed" commit -q -m "chore: seed (#1)" &&
    printf 'Public notes.\n' >"$seed/docs/notes.md" &&
    g -C "$seed" add -A &&
    g -C "$seed" commit -q -m "docs: notes (#2)" &&
    mkdir -p "$tmp/remote/nightgauge" &&
    g clone -q --bare "$seed" "$public" &&
    g init -q --bare "$elsewhere"
} || harness "could not build the throwaway public repository"

# ── An unrelated history, two commits long.
{
  g init -q -b main "$other" &&
    printf 'another project\n' >"$other/other.md" &&
    g -C "$other" add -A &&
    g -C "$other" commit -q -m "init" &&
    printf 'more\n' >>"$other/other.md" &&
    g -C "$other" commit -q -am "more"
} || harness "could not build the unrelated history"

# ── The hook, as husky's generated shim runs it.
hooks="$tmp/hooks"
mkdir -p "$hooks" || harness "mkdir failed"
cat >"$hooks/pre-push" <<'EOF' || harness "could not write the hook shim"
#!/bin/sh
# What husky's .husky/_/h does: run the tracked hook with sh -e.
exec sh -e "$(git rev-parse --show-toplevel)/.husky/pre-push" "$@"
EOF
chmod +x "$hooks/pre-push" || harness "chmod failed"

clone_with_hook() { # clone_with_hook <dir> [clone args...]
  local dir="$1"
  shift
  g clone -q "$@" "$dir" && g -C "$dir" config core.hooksPath "$hooks"
}
clone_with_hook "$work" "$public" || harness "could not clone the public repository"

# ── Helpers ──────────────────────────────────────────────────────────────────
fails=0
LOG=""
RC=0

ok() { echo "ok   - $1"; }
bad() {
  echo "FAIL - $1"
  [ -n "$LOG" ] && [ -f "$LOG" ] && sed 's/^/       | /' "$LOG"
  fails=$((fails + 1))
}

# push <dir> <name> <git push args...>: sets RC and LOG.
push() {
  local dir="$1" name="$2"
  shift 2
  LOG="$tmp/$name.log"
  git -C "$dir" push "$@" >"$LOG" 2>&1
  RC=$?
}

commit_file() { # commit_file <dir> <path> <content> <message>
  mkdir -p "$1/$(dirname "$2")" &&
    printf '%s\n' "$3" >"$1/$2" &&
    g -C "$1" add -- "$2" &&
    g -C "$1" commit -q -m "$4"
}

head_of() { git -C "$1" rev-parse "$2^{commit}" 2>/dev/null; }
remote_ref() { git --git-dir="$public" rev-parse --verify --quiet "$1" 2>/dev/null; }
remote_has() { git --git-dir="$public" cat-file -e "$1" 2>/dev/null; }
logged() { grep -qF -- "$1" "$LOG"; }

# expect_pass <name> <ref on the remote> <expected sha>
expect_pass() {
  if [ "$RC" -eq 0 ] && [ "$(remote_ref "$2")" = "$3" ]; then
    ok "$1"
  else
    bad "$1 (push exit $RC; remote $2 = '$(remote_ref "$2")', want $3)"
  fi
}

# expect_refused <name> <ref> <commit that must not arrive> <phrase>
expect_refused() {
  if [ "$RC" -eq 0 ]; then
    bad "$1 (the push succeeded)"
  elif remote_ref "$2" >/dev/null; then
    bad "$1 (the push failed, but the remote has $2)"
  elif remote_has "$3"; then
    bad "$1 (the push failed, but commit ${3:0:12} reached the remote)"
  elif ! logged "$4"; then
    bad "$1 (refused without saying: $4)"
  else
    ok "$1"
  fi
}

# ── 1. URLs that name the public repository, and ones that do not ───────────
# shellcheck source=scripts/publication-push-guard.sh
. "$REPO/scripts/publication-push-guard.sh"
url_fails=0
for u in \
  git@github.com:nightgauge/nightgauge.git \
  git@github.com:nightgauge/nightgauge \
  https://github.com/nightgauge/nightgauge \
  https://github.com/nightgauge/nightgauge.git/ \
  https://x-access-token:secret@github.com/nightgauge/nightgauge.git \
  ssh://git@ssh.github.com:443/nightgauge/nightgauge.git \
  https://github.com/NightGauge/NightGauge.git \
  gh-alias:nightgauge/nightgauge.git \
  file:///tmp/x/nightgauge/nightgauge.git \
  /tmp/x/nightgauge/nightgauge.git; do
  is_public_url "$u" || {
    echo "       | not recognised as the public repository: $u"
    url_fails=$((url_fails + 1))
  }
done
for u in \
  git@github.com:nightgauge/nightgauge-sibling.git \
  git@github.com:nightgauge/nightgauge.example.git \
  https://github.com/someone/nightgauge.git \
  https://github.com/nightgauge-fork/nightgauge.git \
  https://github.com/nightgauge/nightgauge/wiki \
  "$elsewhere" \
  ""; do
  if is_public_url "$u"; then
    echo "       | taken for the public repository: $u"
    url_fails=$((url_fails + 1))
  fi
done
LOG=""
if [ "$url_fails" -eq 0 ]; then
  ok "the public repository is recognised by URL, and nothing else is"
else
  bad "URL recognition ($url_fails wrong)"
fi

# ── 2. A feature branch off main passes ───────────────────────────────────
#    Its first push scans both commits, because the tip rewrote the file the
#    first one added, and that first version is published too. The next push
#    scans only what the remote lacks. Only the ref's old value, which git hands
#    the hook, can say what that is: the remote-tracking ref that would also say
#    so is dropped first.
{
  g -C "$work" checkout -q -b feat/ok origin/main &&
    commit_file "$work" docs/ok.md "A public note." "docs: a public note" &&
    commit_file "$work" docs/ok.md "A public note, revised." "docs: revise the note"
} || harness "could not build feat/ok"
push "$work" feat-ok -u origin feat/ok
if logged "2 commit(s) scanned"; then
  expect_pass "a feature branch off main passes; a rewritten first version is scanned too" \
    refs/heads/feat/ok "$(head_of "$work" feat/ok)"
else
  bad "a feature branch off main passes; a rewritten first version is scanned too"
fi

{
  commit_file "$work" docs/ok2.md "Another note." "docs: another note" &&
    g -C "$work" update-ref -d refs/remotes/origin/feat/ok
} || harness "could not extend feat/ok"
push "$work" feat-ok-2 origin feat/ok
if logged "1 commit(s) scanned"; then
  expect_pass "a second push scans only the commit the remote lacks" refs/heads/feat/ok \
    "$(head_of "$work" feat/ok)"
else
  bad "a second push scans only the commit the remote lacks"
fi

push "$work" feat-ok-copy origin feat/ok:refs/heads/feat/ok-copy
if logged "0 commit(s) scanned"; then
  expect_pass "a new branch at commits the remote already has scans nothing" \
    refs/heads/feat/ok-copy "$(head_of "$work" feat/ok)"
else
  bad "a new branch at commits the remote already has scans nothing"
fi

# ── 3. Several commits whose content all reaches the tip: one scan ───────────
{
  g -C "$work" checkout -q -b feat/multi origin/main &&
    commit_file "$work" docs/a.md "A." "docs: a" &&
    commit_file "$work" docs/b.md "B." "docs: b"
} || harness "could not build feat/multi"
push "$work" feat-multi origin feat/multi
if logged "1 commit(s) scanned"; then
  expect_pass "commits whose content all reaches the tip cost one scan" \
    refs/heads/feat/multi "$(head_of "$work" feat/multi)"
else
  bad "commits whose content all reaches the tip cost one scan"
fi

# ── 4. An unrelated history is refused before anything is scanned ────────────
g -C "$work" fetch -q "$other" main:refs/heads/stray || harness "could not fetch the stray history"
stray="$(head_of "$work" stray)"
push "$work" stray origin stray
expect_refused "a history unrelated to main is refused" refs/heads/stray "$stray" \
  "its history is unrelated to origin/main"
if logged "scanning"; then bad "the unrelated history was scanned before it was refused"; fi

# ── 5. ...including one merged into an ordinary branch ───────────────────────
{
  g -C "$work" checkout -q -b feat/merged origin/main &&
    g -C "$work" merge -q --allow-unrelated-histories -m "merge another project" stray
} || harness "could not build feat/merged"
push "$work" merged origin feat/merged
expect_refused "an unrelated history merged into a branch is refused" refs/heads/feat/merged \
  "$stray" "its history is unrelated to origin/main"

# ── 6. A boundary violation at the tip is refused ────────────────────────────
{
  g -C "$work" checkout -q -b feat/leak origin/main &&
    commit_file "$work" docs/strategy/plan.md "A plan." "docs: a plan"
} || harness "could not build feat/leak"
leak="$(head_of "$work" feat/leak)"
push "$work" leak origin feat/leak
expect_refused "a boundary violation is refused" refs/heads/feat/leak "$leak" \
  "PRIVATE path is present: docs/strategy/plan.md"

# ── 7. ...and so is one a later commit in the same push deletes ──────────────
{
  g -C "$work" checkout -q -b feat/scrubbed origin/main &&
    commit_file "$work" docs/strategy/plan.md "A plan to scrub." "docs: a plan to scrub" &&
    g -C "$work" rm -q docs/strategy/plan.md &&
    g -C "$work" commit -q -m "docs: remove the plan"
} || harness "could not build feat/scrubbed"
scrubbed="$(head_of "$work" feat/scrubbed~1)"
push "$work" scrubbed origin feat/scrubbed
expect_refused "a violation a later commit deletes is still refused" refs/heads/feat/scrubbed \
  "$scrubbed" "It is not the tip"

# ── 8. A commit that cannot be checked fails closed ──────────────────────────
{
  g -C "$work" checkout -q -b feat/no-manifest origin/main &&
    g -C "$work" rm -q .github/publication-boundary.yaml &&
    g -C "$work" commit -q -m "chore: drop the manifest"
} || harness "could not build feat/no-manifest"
push "$work" no-manifest origin feat/no-manifest
expect_refused "a commit whose checker cannot run is refused" refs/heads/feat/no-manifest \
  "$(head_of "$work" feat/no-manifest)" "the boundary checker could not run"

{
  g -C "$work" checkout -q -b feat/no-checker origin/main &&
    g -C "$work" rm -q scripts/publication-boundary-check.py &&
    g -C "$work" commit -q -m "chore: drop the checker"
} || harness "could not build feat/no-checker"
push "$work" no-checker origin feat/no-checker
expect_refused "a commit without the checker is refused" refs/heads/feat/no-checker \
  "$(head_of "$work" feat/no-checker)" "has no"

# ── 9. Deleting a branch publishes nothing ───────────────────────────────────
push "$work" delete origin --delete feat/multi
if [ "$RC" -eq 0 ] && ! remote_ref refs/heads/feat/multi >/dev/null && logged "1 deletion(s)"; then
  ok "a branch deletion passes"
else
  bad "a branch deletion passes (push exit $RC)"
fi

# ── 10. A release tag on main passes without a scan ──────────────────────────
g -C "$work" tag -a v0.0.1 -m "Release 0.0.1" origin/main || harness "could not tag"
push "$work" tag origin v0.0.1
if logged "0 commit(s) scanned"; then
  expect_pass "an annotated tag on main passes without a scan" refs/tags/v0.0.1 \
    "$(git -C "$work" rev-parse v0.0.1)"
else
  bad "an annotated tag on main passes without a scan"
fi

# ── 11. Refs the guard cannot verify are refused ─────────────────────────────
if ! blob="$(printf 'a blob\n' | git -C "$work" hash-object -w --stdin)" ||
  ! g -C "$work" tag blob-tag "$blob"; then
  harness "could not tag a blob"
fi
push "$work" blob-tag origin blob-tag
expect_refused "a tag that names no commit is refused" refs/tags/blob-tag "$blob" \
  "names no commit"

push "$work" meta-ref origin "feat/ok:refs/meta/config"
if [ "$RC" -ne 0 ] && ! remote_ref refs/meta/config >/dev/null &&
  logged "only branches and tags are verified"; then
  ok "a ref outside refs/heads and refs/tags is refused"
else
  bad "a ref outside refs/heads and refs/tags is refused (push exit $RC)"
fi

# ── 12. A remote that is not the public repository is not checked ────────────
push "$work" elsewhere "$elsewhere" feat/leak
if [ "$RC" -eq 0 ] && ! logged "publication guard" &&
  [ "$(git --git-dir="$elsewhere" rev-parse --verify --quiet refs/heads/feat/leak)" = "$leak" ]; then
  ok "a push to another repository is not checked"
else
  bad "a push to another repository is not checked (push exit $RC)"
fi

# ── 13. Pushing from a linked worktree scans the pushed commit, not the checkout
#    git exports GIT_DIR to a hook that runs in a linked worktree; a guard that
#    leaked it into the scan would read the worktree instead of the commit.
wt="$tmp/wt"
{
  g -C "$work" worktree add -q -b feat/from-worktree "$wt" origin/main &&
    commit_file "$wt" docs/wt.md "From a worktree." "docs: from a worktree" &&
    mkdir -p "$wt/docs/strategy" &&
    printf 'Not staged.\n' >"$wt/docs/strategy/untracked.md"
} || harness "could not build the linked worktree"
wt_state() { git -C "$wt" rev-parse HEAD && git -C "$wt" status --porcelain=v1; }
wt_before="$(wt_state)" || harness "could not read the worktree's state"
push "$wt" wt-clean origin feat/from-worktree
expect_pass "a clean push from a worktree passes despite its untracked files" \
  refs/heads/feat/from-worktree "$(head_of "$wt" feat/from-worktree)"
push "$wt" wt-leak origin feat/leak
expect_refused "a violating branch pushed from a worktree is refused" refs/heads/feat/leak \
  "$leak" "PRIVATE path is present: docs/strategy/plan.md"
LOG=""
if [ "$(wt_state)" = "$wt_before" ]; then
  ok "the pushing worktree's HEAD, index and files are untouched"
else
  bad "the pushing worktree's HEAD, index and files are untouched"
fi

# ── 14. Updating a branch by merging main, the permitted update path, passes ─
{
  g -C "$work" checkout -q -b main-update origin/main &&
    commit_file "$work" docs/main.md "On main." "docs: on main (#3)"
} || harness "could not build a newer main"
push "$work" main-update origin main-update:main
if [ "$RC" -ne 0 ]; then
  bad "the throwaway main could not be advanced (push exit $RC)"
fi
{
  g -C "$work" fetch -q origin &&
    g -C "$work" checkout -q feat/ok &&
    g -C "$work" merge -q --no-edit origin/main
} || harness "could not merge main into feat/ok"
push "$work" merge-main origin feat/ok
if logged "1 commit(s) scanned"; then
  expect_pass "a branch that merged main passes, scanning only the merge" refs/heads/feat/ok \
    "$(head_of "$work" feat/ok)"
else
  bad "a branch that merged main passes, scanning only the merge"
fi

# ── 15. No main to compare with fails closed ─────────────────────────────────
{
  g -C "$work" update-ref -d refs/remotes/origin/main &&
    g -C "$work" checkout -q -b feat/no-main feat/ok &&
    commit_file "$work" docs/c.md "C." "docs: c"
} || harness "could not build feat/no-main"
push "$work" no-main origin feat/no-main
expect_refused "a missing origin/main is refused" refs/heads/feat/no-main \
  "$(head_of "$work" feat/no-main)" "origin/main is missing"
g -C "$work" fetch -q origin || harness "could not restore origin/main"

# ── 16. A shallow history fails closed; a shallow fetch elsewhere does not ───
shallow="$tmp/shallow"
{
  clone_with_hook "$shallow" --depth 1 "file://$public" &&
    g -C "$shallow" checkout -q -b feat/shallow &&
    commit_file "$shallow" docs/s.md "S." "docs: s"
} || harness "could not build the shallow clone"
push "$shallow" shallow origin feat/shallow
expect_refused "a push from a shallow clone is refused" refs/heads/feat/shallow \
  "$(head_of "$shallow" feat/shallow)" "this clone is shallow"

{
  g -C "$work" fetch -q --depth 1 "file://$other" main:refs/unrelated/main &&
    [ "$(git -C "$work" rev-parse --is-shallow-repository)" = "true" ] &&
    g -C "$work" checkout -q -b feat/after-shallow-fetch origin/main &&
    commit_file "$work" docs/d.md "D." "docs: d"
} || harness "could not make the full clone shallow elsewhere"
push "$work" after-shallow origin feat/after-shallow-fetch
expect_pass "a shallow fetch of an unrelated branch does not block a push" \
  refs/heads/feat/after-shallow-fetch "$(head_of "$work" feat/after-shallow-fetch)"

LOG=""
if [ "$fails" -ne 0 ]; then
  echo "$fails case(s) failed"
  exit 1
fi
echo "all cases passed"
