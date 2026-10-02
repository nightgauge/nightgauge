#!/usr/bin/env bash
#
# Regression suite for the publication push guard (#2365), the pre-push half of
# the publication boundary: scripts/publication-push-guard.sh, and
# scripts/install-publication-push-hook.sh, which installs it for every
# worktree of a clone.
#
# Every push case is a REAL `git push` through the hook, from a throwaway clone
# to a throwaway bare repository whose path ends in nightgauge/nightgauge, so
# the guard takes it for the public repository. Each refusal is then confirmed
# on the remote: the ref is absent AND the pushed commit's object never arrived.
# A guard that printed the right refusal while git sent the objects anyway
# would be exactly the failure it exists to prevent, so the words alone prove
# nothing.
#
# The hooks are installed the way `npm install` installs them: husky first,
# with its relative .husky/_ hooks path, then the publication hook installer.
# husky's own installer and runner come from node_modules when this checkout
# has them. Where it does not (the publication-boundary workflow runs no
# npm ci), a stand-in writes the same layout, with a runner that does the two
# things that matter here: it runs .husky/<hook> with sh -e, and it exits 0
# when the checked-out tree has no such file. The throwaway history carries
# this checkout's checker, manifest, guard, installer and hook, so the code
# under test is the working copy, uncommitted edits included.
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
unset HUSKY NG_PUBLICATION_GUARD_RAN

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
#    is really shallow. Later cases advance its main from $seed, which has no
#    hooks, the way other people's merges arrive.
{
  g init -q -b main "$seed" &&
    mkdir -p "$seed/scripts" "$seed/.github" "$seed/.husky" "$seed/docs" &&
    cp "$REPO/scripts/publication-boundary-check.py" \
      "$REPO/scripts/publication-push-guard.sh" \
      "$REPO/scripts/install-publication-push-hook.sh" \
      "$REPO/scripts/check-boundary-allowlist-isolation.sh" "$seed/scripts/" &&
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

seq=2 # the seed's (#N) markers; every later merge to main takes the next one

# publish <message> <path> <content>: a commit on the public main, made the way
# someone else's merge arrives.
publish() {
  mkdir -p "$seed/$(dirname "$2")" &&
    printf '%s' "$3" >"$seed/$2" &&
    g -C "$seed" add -- "$2" &&
    g -C "$seed" commit -q -m "$1" &&
    g -C "$seed" push -q "$public" main
}

# ── An unrelated history, two commits long.
{
  g init -q -b main "$other" &&
    printf 'another project\n' >"$other/other.md" &&
    g -C "$other" add -A &&
    g -C "$other" commit -q -m "init" &&
    printf 'more\n' >>"$other/other.md" &&
    g -C "$other" commit -q -am "more"
} || harness "could not build the unrelated history"

# ── Hooks, installed as npm install installs them ───────────────────────────
HUSKY_JS="$REPO/node_modules/husky/index.js"
HOOK_NAMES="pre-commit pre-merge-commit prepare-commit-msg commit-msg post-commit
applypatch-msg pre-applypatch post-applypatch pre-rebase post-rewrite
post-checkout post-merge pre-push pre-auto-gc"
if [ -f "$HUSKY_JS" ] && command -v node >/dev/null 2>&1; then
  echo "# husky: its own installer and runner, from node_modules"
  real_husky=1
else
  echo "# husky: a stand-in runner (this checkout has no node_modules/husky)"
  real_husky=0
fi

# husky_layout <checkout>: what husky's part of `npm install` leaves behind.
husky_layout() {
  local dir="$1" h
  if [ "$real_husky" -eq 1 ]; then
    (cd "$dir" && node --input-type=module -e '
      import { pathToFileURL } from "node:url";
      const { default: install } = await import(pathToFileURL(process.argv[1]).href);
      const said = install();
      if (said) { console.error(said); process.exit(1); }' "$HUSKY_JS") >/dev/null 2>&1
    return
  fi
  mkdir -p "$dir/.husky/_" &&
    printf '*' >"$dir/.husky/_/.gitignore" &&
    cat >"$dir/.husky/_/h" <<'EOF' &&
#!/usr/bin/env sh
# Stand-in for husky 9's runner: run .husky/<hook> with sh -e, and do nothing
# when the checked-out tree has no such file.
n=$(basename "$0")
s=$(dirname "$(dirname "$0")")/$n
[ ! -f "$s" ] && exit 0
[ "${HUSKY-}" = "0" ] && exit 0
sh -e "$s" "$@"
EOF
    for h in $HOOK_NAMES; do
      # shellcheck disable=SC2016 # the shim expands $0 when git runs it
      printf '#!/usr/bin/env sh\n. "$(dirname "$0")/h"' >"$dir/.husky/_/$h" &&
        chmod 755 "$dir/.husky/_/$h" || return 1
    done &&
    g -C "$dir" config core.hooksPath .husky/_
}

# npm_install_hooks <checkout>: husky, then the publication hook installer, the
# order package.json's "prepare" runs them in.
npm_install_hooks() {
  husky_layout "$1" && (cd "$1" && bash scripts/install-publication-push-hook.sh) >/dev/null 2>&1
}

clone_hooked() { # clone_hooked <dir> [clone args...] <url>
  local dir="$1"
  shift
  g clone -q "$@" "$dir" && npm_install_hooks "$dir"
}
clone_hooked "$work" "$public" || harness "could not clone the public repository and install its hooks"

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

# first_line <file> <text>: replace the file's first line.
first_line() {
  { printf '%s\n' "$2" && tail -n +2 "$1"; } >"$1.tmp" && mv "$1.tmp" "$1"
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

# ── 2. The installer ─────────────────────────────────────────────────────────
#    It points core.hooksPath, by absolute path, at a hook directory in the
#    clone's shared git directory, so every worktree has the guard whatever it
#    has checked out.
dispatch="$(git -C "$work" rev-parse --path-format=absolute --git-common-dir)/nightgauge-hooks"
LOG=""
if [ "$(git -C "$work" config --get core.hooksPath)" = "$dispatch" ] &&
  [ -x "$dispatch/pre-push" ] && [ -x "$dispatch/pre-commit" ] &&
  sh -n "$dispatch/pre-push" && sh -n "$dispatch/pre-commit" &&
  cmp -s "$dispatch/publication-push-guard.sh" "$work/scripts/publication-push-guard.sh"; then
  ok "npm install points every worktree at the publication hook, with a copy of the guard"
else
  bad "npm install points every worktree at the publication hook, with a copy of the guard"
fi

if (cd "$work" && bash scripts/install-publication-push-hook.sh) >/dev/null 2>&1 &&
  [ "$(git -C "$work" config --get core.hooksPath)" = "$dispatch" ]; then
  ok "installing again changes nothing"
else
  bad "installing again changes nothing"
fi

fresh="$tmp/fresh"
g clone -q "$public" "$fresh" || harness "could not clone for the installer cases"
(cd "$fresh" && HUSKY=0 bash scripts/install-publication-push-hook.sh) >/dev/null 2>&1
rc=$?
if [ "$rc" -eq 0 ] && [ -z "$(git -C "$fresh" config --get core.hooksPath)" ] &&
  [ ! -e "$fresh/.git/nightgauge-hooks" ]; then
  ok "HUSKY=0 installs nothing, as it installs no husky hooks"
else
  bad "HUSKY=0 installs nothing, as it installs no husky hooks (exit $rc)"
fi

g -C "$fresh" config core.hooksPath "$tmp/someone-elses-hooks" || harness "git config failed"
(cd "$fresh" && bash scripts/install-publication-push-hook.sh) >/dev/null 2>&1
rc=$?
if [ "$rc" -ne 0 ] && [ "$(git -C "$fresh" config --get core.hooksPath)" = "$tmp/someone-elses-hooks" ]; then
  ok "a hooks path that is not husky's is left alone, and the install says so"
else
  bad "a hooks path that is not husky's is left alone, and the install says so (exit $rc)"
fi

# ── 3. A feature branch off main passes, in one scan ─────────────────────────
#    The tip rewrote the file the first commit added, and that first version is
#    published too, so it is merged into the scan. The guard runs once, though
#    both the publication hook and husky's .husky/pre-push would run it.
{
  g -C "$work" checkout -q -b feat/ok origin/main &&
    commit_file "$work" docs/ok.md "A public note." "docs: a public note" &&
    commit_file "$work" docs/ok.md "A public note, revised." "docs: revise the note"
} || harness "could not build feat/ok"
push "$work" feat-ok -u origin feat/ok
if logged "2 new commit(s) in 1 scan(s)" && logged "content merged in" &&
  [ "$(grep -c 'publication guard: ok:' "$LOG")" -eq 1 ]; then
  expect_pass "a feature branch passes in one scan, its rewritten first version included" \
    refs/heads/feat/ok "$(head_of "$work" feat/ok)"
else
  bad "a feature branch passes in one scan, its rewritten first version included"
fi

commit_file "$work" docs/ok2.md "Another note." "docs: another note" ||
  harness "could not extend feat/ok"
push "$work" feat-ok-2 origin feat/ok
if logged "1 new commit(s) in 1 scan(s)"; then
  expect_pass "a second push scans only the commit the remote lacks" refs/heads/feat/ok \
    "$(head_of "$work" feat/ok)"
else
  bad "a second push scans only the commit the remote lacks"
fi

push "$work" feat-ok-copy origin feat/ok:refs/heads/feat/ok-copy
if logged "0 new commit(s) in 0 scan(s)"; then
  expect_pass "a new branch at commits the remote already has scans nothing" \
    refs/heads/feat/ok-copy "$(head_of "$work" feat/ok)"
else
  bad "a new branch at commits the remote already has scans nothing"
fi

# ── 4. Several commits whose content all reaches the tip: one scan ───────────
{
  g -C "$work" checkout -q -b feat/multi origin/main &&
    commit_file "$work" docs/a.md "A." "docs: a" &&
    commit_file "$work" docs/b.md "B." "docs: b"
} || harness "could not build feat/multi"
push "$work" feat-multi origin feat/multi
if logged "2 new commit(s) in 1 scan(s)" && ! logged "content merged in"; then
  expect_pass "commits whose content all reaches the tip cost one scan" \
    refs/heads/feat/multi "$(head_of "$work" feat/multi)"
else
  bad "commits whose content all reaches the tip cost one scan"
fi

# ── 5. A remote-tracking ref is not evidence of what the remote has ──────────
#    A tracking ref goes stale when a branch is deleted or purged upstream, and
#    a push to a pushurl writes one. Only what the remote advertises counts.
{
  g -C "$work" checkout -q -b feat/ghost origin/main &&
    commit_file "$work" docs/strategy/ghost.md "A plan." "docs: a plan" &&
    g -C "$work" update-ref refs/remotes/origin/feat/ghost HEAD
} || harness "could not build feat/ghost"
ghost="$(head_of "$work" feat/ghost)"
push "$work" ghost origin feat/ghost
expect_refused "a commit a remote-tracking ref says is pushed is scanned all the same" \
  refs/heads/feat/ghost "$ghost" "PRIVATE path is present: docs/strategy/ghost.md"

# ── 6. An unrelated history is refused before anything is scanned ────────────
g -C "$work" fetch -q "$other" main:refs/heads/stray || harness "could not fetch the stray history"
stray="$(head_of "$work" stray)"
push "$work" stray origin stray
expect_refused "a history unrelated to main is refused" refs/heads/stray "$stray" \
  "its history is unrelated to the public main"
if logged "scanning"; then bad "the unrelated history was scanned before it was refused"; fi

# ── 7. ...also while it is checked out, with no hook and no guard in the tree.
#    husky's runner skips a hook the checked-out tree does not have, so this is
#    the push that only the installed hook stops.
{
  g -C "$work" checkout -q stray &&
    [ ! -e "$work/.husky/pre-push" ] && [ ! -e "$work/scripts/publication-push-guard.sh" ]
} || harness "could not check the unrelated history out"
push "$work" stray-checked-out origin stray:refs/heads/stray-checked-out
expect_refused "an unrelated history is refused while it is checked out" \
  refs/heads/stray-checked-out "$stray" "its history is unrelated to the public main"

{
  g -C "$work" switch -q --orphan pages &&
    printf 'A page.\n' >"$work/index.html" &&
    g -C "$work" add index.html &&
    g -C "$work" commit -q -m "pages"
} || harness "could not build an orphan branch"
pages="$(head_of "$work" pages)"
push "$work" pages origin pages
expect_refused "an orphan branch is refused while it is checked out" refs/heads/pages "$pages" \
  "its history is unrelated to the public main"
g -C "$work" checkout -q -f feat/ok || harness "could not leave the orphan branch"

# ── 8. ...including one merged into an ordinary branch ───────────────────────
{
  g -C "$work" checkout -q -b feat/merged origin/main &&
    g -C "$work" merge -q --allow-unrelated-histories -m "merge another project" stray
} || harness "could not build feat/merged"
push "$work" merged origin feat/merged
expect_refused "an unrelated history merged into a branch is refused" refs/heads/feat/merged \
  "$stray" "its history is unrelated to the public main"

# ── 9. A boundary violation at the tip is refused, with a way out ────────────
{
  g -C "$work" checkout -q -b feat/leak origin/main &&
    commit_file "$work" docs/strategy/plan.md "A plan." "docs: a plan"
} || harness "could not build feat/leak"
leak="$(head_of "$work" feat/leak)"
push "$work" leak origin feat/leak
expect_refused "a boundary violation is refused" refs/heads/feat/leak "$leak" \
  "PRIVATE path is present: docs/strategy/plan.md"
if ! logged "git reset --soft"; then
  bad "the refusal names no way to rewrite the commit that the workspace rules allow"
fi

# ── 10. ...and so is one a later commit in the same push deletes ─────────────
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

# ── 11. The allowlist changes alone (#1970) ──────────────────────────────────
#    An exception for docs/strategy/plan.md, added to the deny rule that would
#    refuse it, together with the file: the checker alone would pass that.
loosen() { # loosen <checkout>
  local m="$1/.github/publication-boundary.yaml"
  awk '{ print } $0 == "  - path: \"docs/strategy/**\"" {
         print "    except: [\"docs/strategy/plan.md\"]" }' "$m" >"$m.tmp" &&
    mv "$m.tmp" "$m" &&
    grep -qF 'except: ["docs/strategy/plan.md"]' "$m"
}
{
  g -C "$work" checkout -q -b feat/allow-and-add origin/main &&
    loosen "$work" &&
    mkdir -p "$work/docs/strategy" &&
    printf 'A plan.\n' >"$work/docs/strategy/plan.md" &&
    g -C "$work" add -A &&
    g -C "$work" commit -q -m "docs: allow a plan, and add it"
} || harness "could not build feat/allow-and-add"
both="$(head_of "$work" feat/allow-and-add)"
push "$work" allow-and-add origin feat/allow-and-add
expect_refused "a commit that loosens the allowlist and adds what it lets in is refused" \
  refs/heads/feat/allow-and-add "$both" "allowlist-isolation"

{
  g -C "$work" checkout -q -b feat/allow-then-add origin/main &&
    loosen "$work" &&
    g -C "$work" commit -q -am "chore: allow a plan" &&
    commit_file "$work" docs/strategy/plan.md "A plan." "docs: add the plan"
} || harness "could not build feat/allow-then-add"
then_add="$(head_of "$work" feat/allow-then-add)"
push "$work" allow-then-add origin feat/allow-then-add
expect_refused "loosening the allowlist and then adding what it lets in is refused" \
  refs/heads/feat/allow-then-add "$then_add" "allowlist-isolation"

{
  g -C "$work" checkout -q -b feat/allow-only origin/main &&
    printf '# A reviewed note.\n' >>"$work/.github/publication-boundary.yaml" &&
    g -C "$work" commit -q -am "chore: a note in the allowlist"
} || harness "could not build feat/allow-only"
push "$work" allow-only origin feat/allow-only
expect_pass "an allowlist change on its own passes" refs/heads/feat/allow-only \
  "$(head_of "$work" feat/allow-only)"

# ── 12. Two paths that differ only in case cannot hide a version ─────────────
#    On a filesystem that folds case they are one file in the scan's checkout,
#    which is refused; elsewhere the checker reads both. The reference is put
#    together here so that this file does not carry one.
{
  g -C "$work" checkout -q -f -b feat/case origin/main &&
    case_bad="$(printf 'Tracked in nightgauge-%s#%s.\n' sibling 97031 |
      git -C "$work" hash-object -w --stdin)" &&
    case_ok="$(printf 'Harmless.\n' | git -C "$work" hash-object -w --stdin)" &&
    g -C "$work" update-index --add --cacheinfo "100644,$case_bad,docs/Case.md" &&
    g -C "$work" update-index --add --cacheinfo "100644,$case_ok,docs/case.md" &&
    g -C "$work" commit -q -m "docs: two notes"
} || harness "could not build feat/case"
case_sha="$(head_of "$work" feat/case)"
push "$work" case origin feat/case
if logged "differ only in case"; then
  phrase="differ only in case"
else
  phrase="FORBIDDEN CONTENT [private-repository-issue-reference]"
fi
expect_refused "paths that differ only in case are refused or both read" refs/heads/feat/case \
  "$case_sha" "$phrase"
g -C "$work" checkout -q -f feat/ok || harness "could not leave feat/case"

# ── 13. A commit that cannot be checked fails closed ─────────────────────────
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

# ── 14. Deleting a branch publishes nothing ──────────────────────────────────
push "$work" delete origin --delete feat/multi
if [ "$RC" -eq 0 ] && ! remote_ref refs/heads/feat/multi >/dev/null && logged "1 deletion(s)"; then
  ok "a branch deletion passes"
else
  bad "a branch deletion passes (push exit $RC)"
fi

# ── 15. A release tag on main passes without a scan ──────────────────────────
g -C "$work" tag -a v0.0.1 -m "Release 0.0.1" origin/main || harness "could not tag"
push "$work" tag origin v0.0.1
if logged "0 new commit(s) in 0 scan(s)"; then
  expect_pass "an annotated tag on main passes without a scan" refs/tags/v0.0.1 \
    "$(git -C "$work" rev-parse v0.0.1)"
else
  bad "an annotated tag on main passes without a scan"
fi

# ── 16. Refs the guard cannot verify are refused ─────────────────────────────
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

# ── 17. A remote that is not the public repository is not checked ────────────
push "$work" elsewhere "$elsewhere" feat/leak
if [ "$RC" -eq 0 ] && ! logged "publication guard" &&
  [ "$(git --git-dir="$elsewhere" rev-parse --verify --quiet refs/heads/feat/leak)" = "$leak" ]; then
  ok "a push to another repository is not checked"
else
  bad "a push to another repository is not checked (push exit $RC)"
fi

# ── 18. A linked worktree where npm install never ran still runs the guard ───
#    Every worktree the pipeline creates is one. The guard scans the pushed
#    commit, not the worktree: git exports GIT_DIR to a hook that runs in a
#    linked worktree, and a guard that leaked it into the scan would read the
#    worktree instead.
wt="$tmp/wt"
{
  g -C "$work" worktree add -q -b feat/from-worktree "$wt" origin/main &&
    [ ! -e "$wt/.husky/_" ] &&
    commit_file "$wt" docs/wt.md "From a worktree." "docs: from a worktree" &&
    mkdir -p "$wt/docs/strategy" &&
    printf 'Not staged.\n' >"$wt/docs/strategy/untracked.md"
} || harness "could not build the linked worktree"
wt_state() { git -C "$wt" rev-parse HEAD && git -C "$wt" status --porcelain=v1; }
wt_before="$(wt_state)" || harness "could not read the worktree's state"
push "$wt" wt-clean origin feat/from-worktree
if logged "publication guard: ok:"; then
  expect_pass "a worktree without husky runs the guard, and passes a clean push despite its untracked files" \
    refs/heads/feat/from-worktree "$(head_of "$wt" feat/from-worktree)"
else
  bad "a worktree without husky runs the guard, and passes a clean push despite its untracked files"
fi
push "$wt" wt-leak origin feat/leak
expect_refused "a violating branch pushed from a worktree without husky is refused" \
  refs/heads/feat/leak "$leak" "PRIVATE path is present: docs/strategy/plan.md"
LOG=""
if [ "$(wt_state)" = "$wt_before" ]; then
  ok "the pushing worktree's HEAD, index and files are untouched"
else
  bad "the pushing worktree's HEAD, index and files are untouched"
fi

# ── 19. With husky alone, .husky/pre-push runs the guard ─────────────────────
#    A clone where the publication hook was never installed, for instance after
#    an npm install in a checkout that predates it. An inherited
#    NG_PUBLICATION_GUARD_RAN does not skip the guard: only the publication
#    hook's own process id, which is the hook's parent, does.
honly="$tmp/husky-only"
{
  g clone -q "$public" "$honly" &&
    husky_layout "$honly" &&
    g -C "$honly" checkout -q -b feat/husky-only origin/main &&
    commit_file "$honly" docs/strategy/honly.md "A plan." "docs: a plan"
} || harness "could not build the husky-only clone"
export NG_PUBLICATION_GUARD_RAN=1
push "$honly" husky-only origin feat/husky-only
unset NG_PUBLICATION_GUARD_RAN
expect_refused "with husky alone the tracked hook runs the guard, whatever the environment says" \
  refs/heads/feat/husky-only "$(head_of "$honly" feat/husky-only)" \
  "PRIVATE path is present: docs/strategy/honly.md"

# ── 20. Updating a branch by merging main, the permitted update path, passes ─
publish "docs: on main (#$((++seq)))" docs/main.md "On main.
" || harness "could not advance the public main"
{
  g -C "$work" fetch -q origin &&
    g -C "$work" checkout -q -f feat/ok &&
    g -C "$work" merge -q --no-edit origin/main
} || harness "could not merge main into feat/ok"
push "$work" merge-main origin feat/ok
if logged "1 new commit(s) in 1 scan(s)"; then
  expect_pass "a branch that merged main passes, scanning only the merge" refs/heads/feat/ok \
    "$(head_of "$work" feat/ok)"
else
  bad "a branch that merged main passes, scanning only the merge"
fi

# ── 21. A line an older main had, kept by one commit and since dropped by main,
#    is not new: that commit's version of the file joins the combined scan
#    without the lines its merge base already had there, so the push costs one
#    scan. The number is put together here so that this file cites none.
dead="$(printf '#%s' 99999)"
note="Old note.
One.
Two.
Three.
Four.
Five.
Six.
"
publish "docs: a note (#$((++seq)))" docs/x.md "${note}See $dead for history.
" || harness "could not publish the note"
{
  g -C "$work" fetch -q origin &&
    g -C "$work" checkout -q -f -b feat/inherit origin/main &&
    first_line "$work/docs/x.md" "Old note, edited." &&
    g -C "$work" commit -q -am "docs: edit the note"
} || harness "could not build feat/inherit"
publish "docs: drop the dead reference (#$((++seq)))" docs/x.md "$note" ||
  harness "could not drop the reference on main"
{
  g -C "$work" fetch -q origin &&
    g -C "$work" merge -q --no-edit origin/main &&
    first_line "$work/docs/x.md" "Old note, edited twice." &&
    g -C "$work" commit -q -am "docs: edit the note again" &&
    ! grep -qF "$dead" "$work/docs/x.md"
} || harness "could not merge main into feat/inherit"
push "$work" inherit origin feat/inherit
if logged "3 new commit(s) in 1 scan(s)" && logged "content merged in"; then
  expect_pass "a line main has since dropped, kept by an earlier commit, is not new" \
    refs/heads/feat/inherit "$(head_of "$work" feat/inherit)"
else
  bad "a line main has since dropped, kept by an earlier commit, is not new"
fi

# ── 21b. A branch behind main is judged merged into the current main, as CI
#    judges it: a dead reference main has since dropped, in a file the branch
#    never touched, is not the branch's to answer for.
{
  publish "docs: an old note (#$((++seq)))" docs/old.md "Old, see $dead.
" &&
    g -C "$work" fetch -q origin &&
    g -C "$work" checkout -q -f -b feat/behind origin/main &&
    commit_file "$work" docs/behind.md "Behind." "docs: behind" &&
    publish "docs: drop the old note's dead reference (#$((++seq)))" docs/old.md "Old.
" &&
    grep -qF "$dead" "$work/docs/old.md"
} || harness "could not build feat/behind"
push "$work" behind origin feat/behind
expect_pass "a branch behind main is judged merged into it, not by main's old lines" \
  refs/heads/feat/behind "$(head_of "$work" feat/behind)"

# ── 22. A version that cannot join the combined scan is scanned on its own ──
#    A file where the tip has a directory cannot be added to the combined scan
#    without dropping the directory's files from it, and with them whatever
#    they hold. So the commits are scanned one at a time instead.
{
  g -C "$work" fetch -q origin &&
    g -C "$work" checkout -q -f -b feat/file-then-dir origin/main &&
    commit_file "$work" docs/strategy "Not a directory yet." "docs: a file named strategy" &&
    g -C "$work" rm -q docs/strategy &&
    commit_file "$work" docs/strategy/plan.md "A plan." "docs: a plan where the file was"
} || harness "could not build feat/file-then-dir"
fdir="$(head_of "$work" feat/file-then-dir)"
push "$work" file-then-dir origin feat/file-then-dir
if logged "one at a time"; then
  expect_refused "a file where the tip has a directory is scanned commit by commit" \
    refs/heads/feat/file-then-dir "$fdir" "PRIVATE path is present: docs/strategy/plan.md"
else
  bad "a file where the tip has a directory is scanned commit by commit"
fi

# ── 22b. A version of the checker cannot join the combined scan: the commit
#    that holds it is scanned on its own as well, and nothing else is.
{
  g -C "$work" checkout -q -f -b feat/checker-notes origin/main &&
    printf '# A note.\n' >>"$work/scripts/publication-boundary-check.py" &&
    g -C "$work" commit -q -am "chore: a note in the checker" &&
    printf '# A second note.\n' >>"$work/scripts/publication-boundary-check.py" &&
    g -C "$work" commit -q -am "chore: another note in the checker"
} || harness "could not build feat/checker-notes"
push "$work" checker-notes origin feat/checker-notes
if logged "2 new commit(s) in 2 scan(s)" && logged "on its own, each commit with its own version"; then
  expect_pass "an earlier version of the checker costs one more scan, not one per commit" \
    refs/heads/feat/checker-notes "$(head_of "$work" feat/checker-notes)"
else
  bad "an earlier version of the checker costs one more scan, not one per commit"
fi

# ── 23. A branch that does not merge cleanly into main is scanned as itself ──
{
  g -C "$work" checkout -q -f -b feat/conflict origin/main &&
    first_line "$work/docs/notes.md" "Public notes, ours." &&
    g -C "$work" commit -q -am "docs: our notes"
} || harness "could not build feat/conflict"
publish "docs: their notes (#$((++seq)))" docs/notes.md "Public notes, theirs.
" || harness "could not publish a conflicting main"
push "$work" conflict origin feat/conflict
if logged "does not merge cleanly"; then
  expect_pass "a branch that conflicts with main is scanned as its own tip, and passes" \
    refs/heads/feat/conflict "$(head_of "$work" feat/conflict)"
else
  bad "a branch that conflicts with main is scanned as its own tip, and passes"
fi
commit_file "$work" docs/strategy/conflict.md "A plan." "docs: a plan" ||
  harness "could not extend feat/conflict"
push "$work" conflict-leak origin feat/conflict:refs/heads/feat/conflict-leak
expect_refused "a violation on a branch that conflicts with main is refused" \
  refs/heads/feat/conflict-leak "$(head_of "$work" feat/conflict)" \
  "PRIVATE path is present: docs/strategy/conflict.md"

# ── 24. The combined scan can fail where no commit does: a count ratchet. A
#    second public repository carries a forbidden-content rule with
#    `file_baseline: 2`, and main holds one marked file. On each branch below
#    two commits each add a marked file that a later commit removes, so every
#    commit holds two and passes, and the combined scan holds three and fails.
#    Each commit is then scanned alone. One merged into main, or scanned as
#    itself with main's own checker and manifest, clears the failure; one
#    judged by a manifest of its own cannot.
mark="$(printf 'RATCHET-%s' MARKER)"
seed2="$tmp/seed2"
public2="$tmp/remote3/nightgauge/nightgauge.git"
work2="$tmp/work2"
{
  g clone -q "$public" "$seed2" &&
    awk -v m="$mark" '{ print } $0 == "forbidden_content:" {
      print "  - id: suite-ratchet"
      print "    pattern: \"" m "\""
      print "    file_baseline: 2"
      print "    rationale: \"A count ratchet for this regression suite.\""
      print "    allow_paths:"
      print "      - \".github/publication-boundary.yaml\""
    }' "$seed2/.github/publication-boundary.yaml" >"$seed2/manifest.tmp" &&
    mv "$seed2/manifest.tmp" "$seed2/.github/publication-boundary.yaml" &&
    grep -q '^  - id: suite-ratchet$' "$seed2/.github/publication-boundary.yaml" &&
    printf '%s one.\n' "$mark" >"$seed2/docs/r1.md" &&
    printf 'Shared notes.\n' >"$seed2/docs/shared.md" &&
    g -C "$seed2" add -A &&
    g -C "$seed2" commit -q -m "chore: a suite ratchet (#$((++seq)))" &&
    mkdir -p "$tmp/remote3/nightgauge" &&
    g clone -q --bare "$seed2" "$public2" &&
    clone_hooked "$work2" "$public2"
} || harness "could not build the ratchet repository"

# ratchet_branch <branch> [<shared line>]: two marked files added and removed in
# turn; with a shared line, every commit also rewrites docs/shared.md.
ratchet_branch() {
  local b="$1" line="${2:-}"
  g -C "$work2" fetch -q origin &&
    g -C "$work2" checkout -q -f -b "$b" origin/main &&
    { [ -z "$line" ] || printf '%s\n' "$line" >"$work2/docs/shared.md"; } &&
    printf '%s two.\n' "$mark" >"$work2/docs/r2.md" &&
    g -C "$work2" add -A && g -C "$work2" commit -q -m "docs: r2" &&
    g -C "$work2" rm -q docs/r2.md &&
    printf '%s three.\n' "$mark" >"$work2/docs/r3.md" &&
    g -C "$work2" add -A && g -C "$work2" commit -q -m "docs: r3 for r2" &&
    g -C "$work2" rm -q docs/r3.md &&
    printf 'Plain.\n' >"$work2/docs/plain-$b.md" &&
    g -C "$work2" add -A && g -C "$work2" commit -q -m "docs: plain for r3"
}
# publish2 <message> <path> <content>: a commit on the second public main.
publish2() {
  printf '%s' "$3" >"$seed2/$2" &&
    g -C "$seed2" add -- "$2" &&
    g -C "$seed2" commit -q -m "$1" &&
    g -C "$seed2" push -q "$public2" HEAD:main
}
push2() { # push2 <name> <branch>
  LOG="$tmp/$1.log"
  git -C "$work2" push origin "$2" >"$LOG" 2>&1
  RC=$?
}
remote2_ref() { git --git-dir="$public2" rev-parse --verify --quiet "$1" 2>/dev/null; }

ratchet_branch ratchet-merged || harness "could not build ratchet-merged"
push2 ratchet-merged ratchet-merged
if [ "$RC" -eq 0 ] && logged "that scan failed" && logged "every new commit passes on its own" &&
  logged "3 new commit(s) in 4 scan(s)" &&
  [ "$(remote2_ref refs/heads/ratchet-merged)" = "$(head_of "$work2" ratchet-merged)" ]; then
  ok "a combined failure every commit clears, merged into main, lets the push go"
else
  bad "a combined failure every commit clears, merged into main, lets the push go (exit $RC)"
fi

{
  ratchet_branch ratchet-own "Ours, own." &&
    publish2 "docs: their shared notes (#$((++seq)))" docs/shared.md "Theirs, own.
"
} || harness "could not build ratchet-own"
push2 ratchet-own ratchet-own
if [ "$RC" -eq 0 ] && logged "does not merge cleanly" &&
  logged "every new commit passes on its own" &&
  [ "$(remote2_ref refs/heads/ratchet-own)" = "$(head_of "$work2" ratchet-own)" ]; then
  ok "commits scanned as themselves, with main's own checker and manifest, clear it too"
else
  bad "commits scanned as themselves, with main's own checker and manifest, clear it too (exit $RC)"
fi

# A commit that merges cleanly into main but carries a checker of its own is
# judged by that checker, so it cannot clear the failure either.
{
  g -C "$work2" fetch -q origin &&
    g -C "$work2" checkout -q -f -b ratchet-checker origin/main &&
    printf '# A note.\n' >>"$work2/scripts/publication-boundary-check.py" &&
    printf '%s two.\n' "$mark" >"$work2/docs/r2.md" &&
    g -C "$work2" add -A && g -C "$work2" commit -q -m "docs: r2, and a note in the checker" &&
    g -C "$work2" rm -q docs/r2.md &&
    printf '%s three.\n' "$mark" >"$work2/docs/r3.md" &&
    g -C "$work2" add -A && g -C "$work2" commit -q -m "docs: r3 for r2" &&
    g -C "$work2" rm -q docs/r3.md &&
    g -C "$work2" checkout -q origin/main -- scripts/publication-boundary-check.py &&
    g -C "$work2" commit -q -m "chore: drop r3 and the note"
} || harness "could not build ratchet-checker"
with_checker="$(head_of "$work2" ratchet-checker)"
push2 ratchet-checker ratchet-checker
if [ "$RC" -ne 0 ] && logged "cannot be pinned on any of them" &&
  [ -z "$(remote2_ref refs/heads/ratchet-checker)" ] &&
  ! git --git-dir="$public2" cat-file -e "$with_checker" 2>/dev/null; then
  ok "a commit judged by a checker of its own cannot clear it, merged into main or not"
else
  bad "a commit judged by a checker of its own cannot clear it, merged into main or not (exit $RC)"
fi

{
  ratchet_branch ratchet-own-rules "Ours, own rules." &&
    publish2 "docs: their shared notes again (#$((++seq)))" docs/shared.md "Theirs, own rules.
" &&
    printf '# A reviewed note.\n' >>"$seed2/.github/publication-boundary.yaml" &&
    g -C "$seed2" commit -q -am "chore: a note in the allowlist (#$((++seq)))" &&
    g -C "$seed2" push -q "$public2" HEAD:main
} || harness "could not build ratchet-own-rules"
own_rules="$(head_of "$work2" ratchet-own-rules)"
push2 ratchet-own-rules ratchet-own-rules
if [ "$RC" -ne 0 ] && logged "cannot be pinned on any of them" && logged "git reset --soft" &&
  [ -z "$(remote2_ref refs/heads/ratchet-own-rules)" ] &&
  ! git --git-dir="$public2" cat-file -e "$own_rules" 2>/dev/null; then
  ok "commits judged by a manifest of their own cannot clear it, and the push is refused"
else
  bad "commits judged by a manifest of their own cannot clear it, and the push is refused (exit $RC)"
fi

# ── 24b. A commit that carries the public main's own manifest is judged by
#    main's rules, so it is not held to the allowlist-isolation check: here a
#    branch made the same allowlist change main did, alongside other work.
{
  g -C "$work" fetch -q origin &&
    g -C "$work" checkout -q -f -b feat/same-manifest origin/main &&
    printf '# Another reviewed note.\n' >>"$seed/.github/publication-boundary.yaml" &&
    g -C "$seed" commit -q -am "chore: another note in the allowlist (#$((++seq)))" &&
    g -C "$seed" push -q "$public" main &&
    printf '# Another reviewed note.\n' >>"$work/.github/publication-boundary.yaml" &&
    mkdir -p "$work/docs" && printf 'Same.\n' >"$work/docs/same.md" &&
    g -C "$work" add -A &&
    g -C "$work" commit -q -m "docs: same, with main's allowlist note" &&
    [ "$(git -C "$work" rev-parse HEAD:.github/publication-boundary.yaml)" = \
      "$(git -C "$seed" rev-parse HEAD:.github/publication-boundary.yaml)" ]
} || harness "could not build feat/same-manifest"
push "$work" same-manifest origin feat/same-manifest
expect_pass "a commit carrying main's own manifest is not held to allowlist isolation" \
  refs/heads/feat/same-manifest "$(head_of "$work" feat/same-manifest)"

# ── 25. A clone behind the public main fetches it, and changes no ref ────────
publish "docs: newer (#$((++seq)))" docs/newer.md "Newer.
" || harness "could not advance the public main"
stale="$(head_of "$work" origin/main)"
{
  g -C "$work" checkout -q -f -b feat/stale origin/main &&
    commit_file "$work" docs/stale.md "From a stale main." "docs: from a stale main"
} || harness "could not build feat/stale"
push "$work" stale origin feat/stale
if logged "fetching the public main" && [ "$(head_of "$work" origin/main)" = "$stale" ]; then
  expect_pass "a clone behind the public main fetches it to compare, and changes no ref" \
    refs/heads/feat/stale "$(head_of "$work" feat/stale)"
else
  bad "a clone behind the public main fetches it to compare, and changes no ref"
fi

# ── 26. A public repository with no main fails closed ────────────────────────
nomain="$tmp/remote2/nightgauge/nightgauge.git"
{
  mkdir -p "$tmp/remote2/nightgauge" &&
    g init -q --bare "$nomain" &&
    g -C "$seed" push -q "$nomain" main:refs/heads/dev
} || harness "could not build a public repository with no main"
push "$work" no-main "$nomain" feat/stale
if [ "$RC" -ne 0 ] && logged "advertises no main" &&
  ! git --git-dir="$nomain" rev-parse --verify --quiet refs/heads/feat/stale >/dev/null; then
  ok "a public repository with no main is refused"
else
  bad "a public repository with no main is refused (push exit $RC)"
fi

# ── 27. A shallow history fails closed; a shallow fetch elsewhere does not ───
shallow="$tmp/shallow"
{
  clone_hooked "$shallow" --depth 1 "file://$public" &&
    g -C "$shallow" checkout -q -b feat/shallow &&
    commit_file "$shallow" docs/s.md "S." "docs: s"
} || harness "could not build the shallow clone"
push "$shallow" shallow origin feat/shallow
expect_refused "a push from a shallow clone is refused" refs/heads/feat/shallow \
  "$(head_of "$shallow" feat/shallow)" "this clone is shallow"

{
  g -C "$work" fetch -q --depth 1 "file://$other" main:refs/unrelated/main &&
    [ "$(git -C "$work" rev-parse --is-shallow-repository)" = "true" ] &&
    g -C "$work" fetch -q origin &&
    g -C "$work" checkout -q -f -b feat/after-shallow-fetch origin/main &&
    commit_file "$work" docs/d.md "D." "docs: d"
} || harness "could not make the full clone shallow elsewhere"
push "$work" after-shallow origin feat/after-shallow-fetch
expect_pass "a shallow fetch of an unrelated branch does not block a push" \
  refs/heads/feat/after-shallow-fetch "$(head_of "$work" feat/after-shallow-fetch)"

# ── 28. A scan stops when the git push it serves has exited ──────────────────
#    A caller that times git out kills git, not the hook. The checker here is a
#    stand-in that records its process id and sleeps; "git" is a sleep the case
#    kills. The case waits on the guard, never on a clock: a guard that kept
#    scanning would finish the stand-in's sleep and return its exit 0, not 2.
stop="$tmp/stop"
{
  mkdir -p "$stop/tree/scripts" "$stop/work" &&
    cat >"$stop/tree/scripts/publication-boundary-check.py" <<'PY'
import os
import time

with open(os.path.join(os.environ["STOP_DIR"], "checker.pid"), "w") as f:
    f.write(str(os.getpid()))
time.sleep(120)
PY
} || harness "could not build the stand-in checker"
sleep 300 &
fake_git=$!
(
  export STOP_DIR="$stop"
  TREE="$stop/tree" WORK="$stop/work" GIT_PID="$fake_git"
  run_checker HEAD
  exit 0
) 2>"$stop/guard.log" &
guard_pid=$!
n=0
while [ ! -s "$stop/checker.pid" ] && [ "$n" -lt 600 ]; do
  kill -0 "$guard_pid" 2>/dev/null || break
  sleep 0.1
  n=$((n + 1))
done
if [ ! -s "$stop/checker.pid" ]; then
  kill "$fake_git" "$guard_pid" 2>/dev/null
  harness "the stand-in checker never started"
fi
kill "$fake_git"
wait "$fake_git" 2>/dev/null
wait "$guard_pid"
rc=$?
checker_pid="$(cat "$stop/checker.pid")"
LOG="$stop/guard.log"
if [ "$rc" -eq 2 ] && ! kill -0 "$checker_pid" 2>/dev/null &&
  logged "the git push this check was for has exited"; then
  ok "a scan stops, and stops its checker, when the git push it serves has exited"
else
  kill "$checker_pid" 2>/dev/null
  bad "a scan stops, and stops its checker, when the git push it serves has exited (exit $rc)"
fi

LOG=""
if [ "$fails" -ne 0 ]; then
  echo "$fails case(s) failed"
  exit 1
fi
echo "all cases passed"
