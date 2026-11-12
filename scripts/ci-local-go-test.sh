#!/usr/bin/env bash
#
# ci-local-go-test.sh — the Go test steps of scripts/ci-local.sh, plus the record
# that lets `nightgauge pre-push validate` skip repeating them (#2700).
#
# The gate splits the Go suite into steps it runs concurrently (#3209): the slow
# packages in scripts/go-test-shards.txt run as N `go test -run` processes each
# (scripts/go-test-shard.py), every other package runs in one `go test` over the
# rest, and each of those is one grouped step, plain and race alike. One step
# holds one slot of the machine-wide CI_LOCAL_JOBS budget.
#
#   bash scripts/ci-local-go-test.sh --prepare <state-dir> <record-file>
#   bash scripts/ci-local-go-test.sh --shard [--race] <timeout> <state-dir> <pkg> <i>
#   bash scripts/ci-local-go-test.sh --rest [--race] <timeout> <state-dir>
#   bash scripts/ci-local-go-test.sh --finalize <state-dir> <record-file>
#   bash scripts/ci-local-go-test.sh --fingerprint
#
# --prepare clears the record, fingerprints the tree and writes the shard plan,
# failing before any test starts when a test is in no shard or in two.
# --shard and --rest write their `go test -json` events and exit code under the
# state directory. --finalize runs after every one of them has finished: it
# concatenates the plain events into go-test.json, checks that every sharded
# test ran exactly once in each pass, runs the skip accounting, and records the
# tree only when every step passed and the fingerprint still matches --prepare's,
# so an edit during the run records nothing.
#
# Go's test cache is used (#3208): no -count=1, so an unchanged package replays
# its last result. The tests the cache cannot track, listed in
# scripts/go-test-uncached.txt, run again with -count=1 in the --rest steps,
# and the plain events are appended to the rest's JSON so the skip accounting
# covers both runs. CI_LOCAL_NO_TEST_CACHE=1 runs every step with -count=1
# instead, which makes the separate uncached run redundant, so it is skipped.
# GO_TEST_UNCACHED_LIST names a different list and GO_TEST_SHARD_CONFIG a
# different shard config (the regression suites use them).
#
# Run from the repository root.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SHARD="$SCRIPT_DIR/go-test-shard.py"
shard_config=(--config "${GO_TEST_SHARD_CONFIG:-$SCRIPT_DIR/go-test-shards.txt}")
uncached_list="${GO_TEST_UNCACHED_LIST:-$SCRIPT_DIR/go-test-uncached.txt}"

# Must match TreeFingerprint in internal/hooks/pre_push_skip.go. The scratch
# index starts from HEAD so tracked files matching an ignore rule stay (#2889).
tree_fingerprint() {
  local dir tree
  dir="$(mktemp -d)"
  if git rev-parse -q --verify 'HEAD^{tree}' >/dev/null 2>&1; then
    GIT_INDEX_FILE="$dir/index" git read-tree HEAD || { rm -rf "$dir"; return 1; }
  fi
  GIT_INDEX_FILE="$dir/index" git add -A -- . || { rm -rf "$dir"; return 1; }
  tree="$(GIT_INDEX_FILE="$dir/index" git write-tree)" || { rm -rf "$dir"; return 1; }
  rm -rf "$dir"
  printf '%s\n' "$tree"
}

# The basename every step writes under <state-dir>/<plain|race>/.
shard_file() { # <pkg> <i>
  printf 'shard-%s-%s' "$(printf '%s' "$1" | tr -c '[:alnum:]' '-')" "$2"
}

# Run a step's command, keeping its exit code next to its events for --finalize.
record_exit() { # <codefile> <command...>
  local codefile="$1" code=0
  shift
  "$@" || code=$?
  printf '%s\n' "$code" >"$codefile"
  return "$code"
}

mode="${1:-}"
[ "$#" -gt 0 ] && shift
race=()
pass=plain
if [ "${1:-}" = "--race" ]; then
  race=(--race)
  pass=race
  shift
fi

case "$mode" in
  --fingerprint)
    tree_fingerprint
    ;;

  --prepare)
    state="$1"
    record="$2"
    rm -f "$record"
    rm -rf "$state"
    mkdir -p "$state/plain" "$state/race"
    # A tree that cannot be fingerprinted still gets tested; it just records nothing.
    tree_fingerprint >"$state/before.tree" || : >"$state/before.tree"
    python3 "$SHARD" "${shard_config[@]}" plan --out "$state/plan.json"
    ;;

  --shard)
    timeout_arg="$1"
    state="$2"
    pkg="$3"
    index="$4"
    base="$state/$pass/$(shard_file "$pkg" "$index")"
    record_exit "$base.exitcode" python3 "$SHARD" "${shard_config[@]}" run ${race[@]+"${race[@]}"} \
      --plan "$state/plan.json" --pkg "$pkg" --shard "$index" \
      --timeout "$timeout_arg" --json "$base.json"
    ;;

  --rest)
    timeout_arg="$1"
    state="$2"
    base="$state/$pass/rest"
    rest_pass() {
      python3 "$SHARD" "${shard_config[@]}" rest ${race[@]+"${race[@]}"} \
        --timeout "$timeout_arg" --json "$base.json" || return
      [ "${CI_LOCAL_NO_TEST_CACHE:-}" = "1" ] && return 0
      # The race pass keeps no events for the list run: only the plain events
      # feed the skip accounting, and the shard check reads shard files only.
      local json=()
      [ "$pass" = plain ] && json=(--json "$base.json")
      python3 "$SCRIPT_DIR/go-test-uncached.py" ${race[@]+"${race[@]}"} --timeout "$timeout_arg" \
        ${json[@]+"${json[@]}"} --list "$uncached_list"
    }
    record_exit "$base.exitcode" rest_pass
    ;;

  --finalize)
    state="$1"
    record="$2"
    rm -f "$record"
    # Every step the plan implies, in gate order: the rest first, then each shard.
    steps=()
    while read -r step_pass pkg index _; do
      steps+=("$step_pass/$(shard_file "$pkg" "$index")")
    done < <(python3 "$SHARD" "${shard_config[@]}" steps)

    not_passed=()
    for step in plain/rest race/rest "${steps[@]}"; do
      code="$(cat "$state/$step.exitcode" 2>/dev/null || echo "no exit code")"
      [ "$code" = "0" ] || not_passed+=("$step ($code)")
    done
    if [ "${#not_passed[@]}" -gt 0 ]; then
      # Each failed step is reported under its own label. Accounting over a
      # partial run would only add noise, and a partial run must not record.
      echo "FAIL: not every Go test step passed, so the tree is not recorded and the" >&2
      echo "shard coverage and skip accounting did not run:" >&2
      printf '  %s\n' "${not_passed[@]}" >&2
      exit 1
    fi

    plain=("$state/plain/rest.json")
    race_shards=()
    for step in "${steps[@]}"; do
      case "$step" in
        plain/*) plain+=("$state/$step.json") ;;
        race/*) race_shards+=("$state/$step.json") ;;
      esac
    done
    cat "${plain[@]}" >go-test.json

    failed=0
    echo "Shard coverage, plain pass:"
    python3 "$SHARD" check --plan "$state/plan.json" "${plain[@]:1}" || failed=1
    echo "Shard coverage, race pass:"
    python3 "$SHARD" check --plan "$state/plan.json" "${race_shards[@]}" || failed=1
    python3 "$SCRIPT_DIR/check-go-test-skips.py" go-test.json || failed=1
    [ "$failed" -eq 0 ] || exit 1

    before="$(cat "$state/before.tree")"
    after="$(tree_fingerprint || true)"
    if [ -n "$before" ] && [ "$before" = "$after" ]; then
      printf '%s\n' "$after" >"$record"
      echo "Recorded the passing tree $after for pre-push validate."
    else
      echo "The tree changed during the run (or could not be fingerprinted); nothing recorded."
    fi
    ;;

  *)
    echo "usage: ci-local-go-test.sh --prepare|--shard|--rest|--finalize|--fingerprint ..." >&2
    exit 2
    ;;
esac
