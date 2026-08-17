#!/usr/bin/env bash
# ci-change-class.sh — the deterministic CI change-class gate (#647).
#
# Classifies the pull request's ACTUAL DIFF (never the branch name, never the
# PR title) and emits the workflow-job outputs that `.github/workflows/ci.yml`
# uses to gate the expensive steps of its heavy jobs:
#
#   run_heavy=true|false   change_class=<class>   reason=<text>
#
# Classification is delegated to `nightgauge ci classify` so the shell has NO
# opinion of its own about what "docs" means: the globs live once, in
# internal/intelligence/changeClassifier, and are shared with the pipeline
# scheduler and the PR gates. A second copy of those patterns here is exactly
# how CI and the pipeline would drift apart.
#
# ── Two invariants this script exists to hold ────────────────────────────────
#
# 1. IT NEVER FAILS THE JOB. Every exit is 0. `set -e` is deliberately NOT set:
#    an early `exit 1` here would fail the `changes` job, and a required job
#    that `needs:` it would then be SKIPPED rather than run — and a required
#    status check that never reports blocks the PR forever. Every error path
#    below funnels through fail_open(), which emits run_heavy=true.
#
# 2. IT FAILS OPEN, NOT CLOSED. Unknown event, missing SHA, unresolvable ref,
#    no binary, classifier error → run the FULL suite. The only inputs that
#    skip work are a successfully computed `docs_only` or `empty` diff. A
#    change CI could not classify is never a change CI under-tests.
#
# Non-`pull_request` events always run heavy: `push` to main is the merge-skew
# observation (two PRs green apart, broken together — AGENTS.md), and a
# scheduled run exists to catch environment drift. Neither is fast-trackable.
#
# Inputs (environment). Each has exactly ONE source — notably NG_EVENT_NAME does
# NOT fall back to the runner's ambient GITHUB_EVENT_NAME. A gate with two ways
# to learn its own inputs decides on whichever happens to be set, which is not a
# decision anybody made: with the fallback in place, a caller passing an empty
# NG_EVENT_NAME to mean "unknown event" silently got `pull_request` from the
# ambient environment instead, and classified a diff it should have refused to
# fast-track. The workflow always passes NG_EVENT_NAME explicitly.
#   NG_EVENT_NAME    event name (github.event_name)
#   NG_BASE_SHA      base commit of the PR (github.event.pull_request.base.sha)
#   NG_HEAD_SHA      head commit of the PR (github.event.pull_request.head.sha)
#   NIGHTGAUGE_BIN   optional prebuilt binary; built from ./cmd/nightgauge if unset
#   GITHUB_OUTPUT    where to write outputs; stdout when unset (local runs/tests)
#
# Run: bash scripts/ci-change-class.sh
# Regression suite: scripts/test-ci-change-class.sh

set -uo pipefail

OUT_FILE="${GITHUB_OUTPUT:-/dev/stdout}"
BUILT_BIN=""

cleanup() {
  [ -n "$BUILT_BIN" ] && rm -rf "$(dirname "$BUILT_BIN")"
  return 0
}
trap cleanup EXIT

# emit writes one `key=value` workflow output. Values are flattened to a single
# line: GitHub's key=value output form terminates at the first newline, so a
# multi-line reason would silently truncate the output AND leak its tail into
# the output file as an unparsable line.
emit() {
  local key="$1" value="$2"
  value="$(printf '%s' "$value" | tr '\n\r' '  ')"
  printf '%s=%s\n' "$key" "$value" >>"$OUT_FILE"
}

log() {
  printf '%s\n' "$*" >&2
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '%s\n' "$*" >>"$GITHUB_STEP_SUMMARY"
  fi
}

# decide emits the three outputs and exits 0 — the ONLY exit path in this file.
decide() {
  local heavy="$1" class="$2" reason="$3"
  emit run_heavy "$heavy"
  emit change_class "$class"
  emit reason "$reason"
  log "change_class=$class run_heavy=$heavy — $reason"
  exit 0
}

fail_open() {
  decide true unknown "$1"
}

event="${NG_EVENT_NAME:-}"
base="${NG_BASE_SHA:-}"
head="${NG_HEAD_SHA:-}"

if [ "$event" != "pull_request" ]; then
  fail_open "event=${event:-<unset>} is not a pull_request — running the full suite (merge-skew / environment-drift coverage)"
fi

[ -n "$base" ] || fail_open "no base SHA supplied — running the full suite"
[ -n "$head" ] || head="HEAD"

root="$(git rev-parse --show-toplevel 2>/dev/null)"
[ -n "$root" ] || fail_open "not inside a git repository — running the full suite"
cd "$root" || fail_open "cannot enter repo root $root — running the full suite"

# Resolve both endpoints BEFORE classifying. `git diff` on a missing ref is an
# error the classifier would report as an opaque exit code; naming the missing
# endpoint here is what makes a shallow-clone misconfiguration debuggable.
for ref in "$base" "$head"; do
  git rev-parse --verify --quiet "${ref}^{commit}" >/dev/null 2>&1 ||
    fail_open "ref '$ref' is not present in this checkout (shallow clone?) — running the full suite"
done

bin="${NIGHTGAUGE_BIN:-}"
if [ -n "$bin" ]; then
  [ -x "$bin" ] || fail_open "NIGHTGAUGE_BIN='$bin' is not executable — running the full suite"
else
  command -v go >/dev/null 2>&1 || fail_open "no Go toolchain and no NIGHTGAUGE_BIN — running the full suite"
  [ -d "$root/cmd/nightgauge" ] || fail_open "no ./cmd/nightgauge in $root — running the full suite"
  BUILT_BIN="$(mktemp -d)/nightgauge"
  bin="$BUILT_BIN"
  if ! go build -o "$bin" ./cmd/nightgauge 2>&1; then
    fail_open "go build ./cmd/nightgauge failed — running the full suite"
  fi
fi

json="$("$bin" ci classify --base "$base" --head "$head" --workdir "$root" --json 2>&1)" ||
  fail_open "nightgauge ci classify failed: $(printf '%s' "$json" | head -1) — running the full suite"

# printJSON uses json.MarshalIndent, so each key sits on its own line; a
# line-anchored sed needs no jq/python on the runner.
#
# `\([a-z]*\)` rather than `\(true\|false\)`: BSD sed (macOS, where ci-local.sh
# runs) has no alternation in a basic regular expression, so the GNU-only form
# matches nothing there and the gate fails open on every local run — green,
# useless, and invisible. The value is validated against true|false below
# instead, which is where that check belongs anyway.
class="$(printf '%s\n' "$json" | sed -n 's/^[[:space:]]*"change_class"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
heavy="$(printf '%s\n' "$json" | sed -n 's/^[[:space:]]*"run_heavy"[[:space:]]*:[[:space:]]*\([a-z]*\).*/\1/p' | head -1)"
reason="$(printf '%s\n' "$json" | sed -n 's/^[[:space:]]*"reason"[[:space:]]*:[[:space:]]*"\(.*\)".*/\1/p' | head -1)"

[ -n "$class" ] || fail_open "could not parse change_class out of the classifier output — running the full suite"
case "$heavy" in
  true | false) ;;
  *) fail_open "could not parse run_heavy out of the classifier output — running the full suite" ;;
esac

decide "$heavy" "$class" "${reason:-classified from the diff ${base}...${head}}"
