#!/usr/bin/env bash
#
# test-ci-local-fast-typecheck.sh — `ci-local.sh --fast` typechecks the
# TypeScript workspace packages a change touches (#2847), and reproduces #3312.
#
# #3312's tree (4a3c7da8) differed from main by one line: grep-for-symbol.ts
# declared `applies(text: string)` without its return annotation, a type error
# that only the full gate's build reported, twenty minutes in. That tree is
# reachable from no ref, so the one blob that differs (b098722f) is committed as
# tests/fixtures/ci-local-fast-3312/grep-for-symbol.ts.fixture.
#
# Arms:
#   1. The fixture is byte-for-byte blob b098722f, the file from tree 4a3c7da8.
#   2. Derivation (`--ts-scope-probe`, real code path, no gate): with the
#      fixture overlaid, the SDK is RUN and packages/nightgauge is EXCLUDED with
#      its reason; with no origin/main the SDK is RUN (fail closed); with a
#      change outside every package it is SKIP.
#   3. The reproduction: in a throwaway worktree of the current tree with the
#      fixture overlaid, `ci-local.sh --fast` exits non-zero, reports the
#      grep-for-symbol.ts type error, and fails the SDK typecheck step in under
#      120 s, with no SDK build output present. The total run time is printed.
#
# A setup failure (no node_modules, a worktree that will not create) prints
# HARNESS ERROR and exits 2: it asserted nothing about the gate.
#
# Run: bash scripts/test-ci-local-fast-typecheck.sh
# Also run by scripts/ci-local.sh.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 2

FIXTURE="tests/fixtures/ci-local-fast-3312/grep-for-symbol.ts.fixture"
FIXTURE_BLOB="b098722f9ec6f5e89c988619eb86506749066dc3"
TARGET="packages/nightgauge-sdk/src/preflight/ac-rules/grep-for-symbol.ts"
LIMIT_SECONDS=120
PASS=0
FAIL=0
TMP=""
WT=""

cleanup() {
  if [ -n "$WT" ]; then
    git -C "$REPO_ROOT" worktree remove --force "$WT" >/dev/null 2>&1 || true
    git -C "$REPO_ROOT" worktree prune >/dev/null 2>&1 || true
  fi
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

check() { # check <description> <0-if-ok>
  if [ "$2" = "0" ]; then
    echo "PASS: $1"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $1"
    FAIL=$((FAIL + 1))
  fi
}

harness_error() {
  echo "HARNESS ERROR: $*" >&2
  exit 2
}

TMP="$(mktemp -d)" || harness_error "mktemp -d failed"
[ -x node_modules/.bin/tsc ] || harness_error "node_modules/.bin/tsc is missing; run npm ci"

# --- Arm 1: the fixture is the file from tree 4a3c7da8 -----------------------
check "fixture is blob $FIXTURE_BLOB (grep-for-symbol.ts in tree 4a3c7da8)" \
  "$([ "$(git hash-object "$FIXTURE")" = "$FIXTURE_BLOB" ]; echo $?)"

# --- The throwaway worktree: the current tree, tracked edits and new files ---
# `git stash create` snapshots tracked edits as a commit without touching the
# shared stash stack; untracked files are copied over, so the worktree runs the
# ci-local.sh and the required files under test, not the last commit's.
SNAP="$(git stash create 2>/dev/null)"
[ -n "$SNAP" ] || SNAP="HEAD"
WT="$TMP/wt"
git worktree add --detach --quiet "$WT" "$SNAP" >/dev/null 2>&1 ||
  { WT=""; harness_error "git worktree add failed"; }
git ls-files -z --others --exclude-standard |
  tar --null -T - -cf - 2>/dev/null | tar -C "$WT" -xf - ||
  harness_error "copying untracked files into the worktree failed"
# Installed dependencies only. The lock gets the install record's mtime, or the
# gate's bootstrap would see a "newer" lock and run npm ci through the symlink.
ln -s "$REPO_ROOT/node_modules" "$WT/node_modules" || harness_error "node_modules symlink failed"
# Package-local installs too: the SDK pins its own TypeScript and @types/node.
for _nm in "$REPO_ROOT"/packages/*/node_modules; do
  [ -d "$_nm" ] || continue
  _pkg="${_nm#"$REPO_ROOT"/}"
  ln -s "$_nm" "$WT/$_pkg" || harness_error "$_pkg symlink failed"
done
# Nested gates get their own log and slot roots, so they never touch an outer
# gate's step logs.
export CI_LOCAL_LOG_DIR="$TMP/logs" CI_LOCAL_SLOT_ROOT="$TMP/slots"
mkdir -p "$CI_LOCAL_LOG_DIR" "$CI_LOCAL_SLOT_ROOT"
touch -r "$REPO_ROOT/node_modules/.package-lock.json" "$WT/package-lock.json" ||
  harness_error "cannot align package-lock.json with the install record"
cp "$FIXTURE" "$WT/$TARGET" || harness_error "overlaying the fixture failed"

# --- Arm 2: which packages --fast typechecks -------------------------------
probe="$(cd "$WT" && bash scripts/ci-local.sh --ts-scope-probe 2>&1)"
echo "$probe" | sed 's/^/    probe: /'
check "the overlaid SDK file makes --fast typecheck packages/nightgauge-sdk" \
  "$(grep -q "^packages/nightgauge-sdk: RUN (TypeScript typecheck packages/nightgauge-sdk (tsc --noEmit -p)) — " <<<"$probe"; echo $?)"
check "packages/nightgauge is excluded, with the workspace-dependency reason" \
  "$(grep -q '^packages/nightgauge: EXCLUDED — depends on workspace package @nightgauge/sdk, whose types resolve through its build output' <<<"$probe"; echo $?)"

# A fixture repository for the scope decisions that need a different history.
FX="$TMP/fx"
mkdir -p "$FX/packages/a/src" "$FX/packages/b/src" "$FX/scripts" "$FX/docs"
mkdir -p "$FX/scripts/lib"
cp scripts/ci-local.sh "$FX/scripts/ci-local.sh"
cp scripts/lib/require-bash4.sh "$FX/scripts/lib/require-bash4.sh"
printf '{"name":"root","private":true,"workspaces":["packages/*"]}\n' >"$FX/package.json"
printf '{"name":"a"}\n' >"$FX/packages/a/package.json"
printf '{}\n' >"$FX/packages/a/tsconfig.json"
printf '{"name":"b","scripts":{"typecheck":"tsc"},"dependencies":{"a":"*"}}\n' \
  >"$FX/packages/b/package.json"
printf 'x\n' >"$FX/docs/readme.md"
(
  cd "$FX" &&
    git init -q -b main . &&
    git add -A &&
    git -c user.name=t -c user.email=t@example.invalid commit -q -m base
) || harness_error "fixture repository setup failed"
fx_probe() { (cd "$FX" && bash scripts/ci-local.sh --ts-scope-probe 2>&1); }

out="$(fx_probe)"
check "no origin/main: the eligible package is typechecked (fails closed)" \
  "$(grep -q '^packages/a: RUN .* origin/main is not available' <<<"$out"; echo $?)"
check "a package depending on a workspace package is excluded even with a typecheck script" \
  "$(grep -q '^packages/b: EXCLUDED — depends on workspace package a' <<<"$out"; echo $?)"

git -C "$FX" update-ref refs/remotes/origin/main HEAD
printf 'y\n' >>"$FX/docs/readme.md"
out="$(fx_probe)"
check "a change outside every package skips the package typecheck under --fast" \
  "$(grep -q '^packages/a: SKIP' <<<"$out"; echo $?)"
printf '{"name":"root","private":true,"workspaces":["packages/*"],"x":1}\n' >"$FX/package.json"
out="$(fx_probe)"
check "a root package.json change touches every package" \
  "$(grep -q '^packages/a: RUN .* package.json changed' <<<"$out"; echo $?)"

# --- Arm 3: the #3312 reproduction ------------------------------------------
# The verdict time is when --fast reports the SDK step failed: the moment the
# pre-gate has failed on the type error. The step leads the fast subset. The
# run then finishes its remaining static steps, and that total is reported.
LOG="$TMP/fast.log"
: >"$LOG"
verdict_at=""
started=$SECONDS
exec 3< <(cd "$WT" && exec bash scripts/ci-local.sh --fast 2>&1)
fast_pid=$!
while IFS= read -r line <&3; do
  printf '%s\n' "$line" >>"$LOG"
  case "$line" in
    *"✗ TypeScript typecheck packages/nightgauge-sdk"*)
      [ -n "$verdict_at" ] || verdict_at=$((SECONDS - started))
      # The verdict is in: the remaining fast steps add nothing to the
      # reproduction, so stop the nested run rather than pay for them.
      break
      ;;
  esac
done
if [ -n "$verdict_at" ]; then
  # The process substitution's pid is a subshell; kill its whole tree.
  pkill -TERM -P "$fast_pid" 2>/dev/null || true
  kill -TERM "$fast_pid" 2>/dev/null || true
fi
exec 3<&-
wait "$fast_pid" 2>/dev/null || true
kill -0 "$fast_pid" 2>/dev/null && harness_error "the nested --fast run (pid $fast_pid) did not stop"
echo "    --fast on the #3312 tree: SDK typecheck failed at ${verdict_at:-never}s"
check "--fast ran the SDK typecheck step" \
  "$(grep -qF '▶ TypeScript typecheck packages/nightgauge-sdk (tsc --noEmit -p)' "$LOG"; echo $?)"
check "--fast reports the grep-for-symbol.ts type error" \
  "$(grep -qE 'grep-for-symbol\.ts.*error TS[0-9]+' "$LOG"; echo $?)"
check "--fast failed the SDK typecheck in under ${LIMIT_SECONDS}s (at ${verdict_at:-never}s)" \
  "$([ -n "$verdict_at" ] && [ "$verdict_at" -lt "$LIMIT_SECONDS" ]; echo $?)"
check "the SDK typecheck read no build output (no packages/nightgauge-sdk/dist)" \
  "$([ ! -e "$WT/packages/nightgauge-sdk/dist" ]; echo $?)"
if [ "$FAIL" -gt 0 ]; then
  echo "  --fast output (tail):"
  tail -40 "$LOG" | sed 's/^/    /'
fi

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
