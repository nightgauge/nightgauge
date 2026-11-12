#!/usr/bin/env bash
#
# test-ci-local-go-pass-stamp.sh — when scripts/ci-local-go-test.sh records a
# passing tree (#2700), across the gate's sharded Go steps (#3209): plan, rest
# and shard steps, then finalize. Runs against a throwaway repository with a
# stub `go`.
# TestTreeFingerprintMatchesCILocal pins the Go side to the same tree.
#
# Run: bash scripts/test-ci-local-go-pass-stamp.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/ci-local-go-test.sh"
PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/bin" "$TMP/repo"
# `go list ./...` names two packages; `go test -list` lists two tests; a `-run`
# emits a run event per name except $STUB_DROP; $STUB_EDIT is appended to during
# any test run, and $STUB_FAIL_RUN fails a run whose -run names it.
cat > "$TMP/bin/go" <<'STUB'
#!/usr/bin/env bash
if [ "$1" = list ]; then printf '%s\n' example/sharded example/rest; exit 0; fi
args=("$@")
run=""
for ((i = 1; i < ${#args[@]}; i++)); do
  case "${args[$i]}" in
    -list) printf '%s\n' TestOne TestTwo; echo "ok  	example/sharded	0.01s"; exit 0 ;;
    -run) i=$((i + 1)); run="${args[$i]}" ;;
  esac
done
pkg="${args[$((${#args[@]} - 1))]}"
if [ -n "$run" ]; then
  for t in TestOne TestTwo; do
    case "$run" in *"$t"*) ;; *) continue ;; esac
    [ "$t" = "${STUB_DROP:-}" ] && continue
    echo "{\"Action\":\"run\",\"Package\":\"$pkg\",\"Test\":\"$t\"}"
    echo "{\"Action\":\"pass\",\"Package\":\"$pkg\",\"Test\":\"$t\"}"
  done
fi
echo "{\"Action\":\"pass\",\"Package\":\"$pkg\"}"
[ -n "${STUB_EDIT:-}" ] && echo edited >> "$STUB_EDIT"
case "$run" in *"${STUB_FAIL_RUN:-none}"*) exit 1 ;; esac
exit 0
STUB
chmod +x "$TMP/bin/go"
export PATH="$TMP/bin:$PATH"
# The stub emits no events for the list's tests, so the always-uncached runner
# (#3208) gets an empty list here; scripts/test-go-test-cache.sh covers it.
: > "$TMP/uncached.txt"
export GO_TEST_UNCACHED_LIST="$TMP/uncached.txt"
printf 'shard example/sharded 2\n' > "$TMP/shards.txt"
export GO_TEST_SHARD_CONFIG="$TMP/shards.txt"

cd "$TMP/repo" || { echo "HARNESS ERROR: no fixture repository"; exit 1; }
if ! { git init -q && git config user.email t@example.com && git config user.name t &&
  printf 'go-test.json\n.logs/\n' > .gitignore && echo a > tracked.txt &&
  git add -A && git commit -qm init; }; then
  echo "HARNESS ERROR: fixture repository could not be created"
  exit 1
fi

check() { # $1 label, $2 expected, $3 actual
  if [ "$2" = "$3" ]; then
    echo "  ✓ $1"
    PASS=$((PASS + 1))
  else
    echo "  ✗ $1 (got: $3, want: $2)"
    FAIL=$((FAIL + 1))
  fi
}
recorded() { [ -f .logs/pass.tree ] && echo yes || echo no; }

# The gate's sequence: plan, every grouped step, then finalize after the drain.
# Prints finalize's output and returns its exit code.
gate_go_steps() {
  bash "$SCRIPT" --prepare .logs/go-test .logs/pass.tree >/dev/null 2>&1 || return 9
  bash "$SCRIPT" --rest 20m .logs/go-test >/dev/null 2>&1
  bash "$SCRIPT" --rest --race 20m .logs/go-test >/dev/null 2>&1
  local i
  for i in 1 2; do
    bash "$SCRIPT" --shard 20m .logs/go-test example/sharded "$i" >/dev/null 2>&1
    bash "$SCRIPT" --shard --race 20m .logs/go-test example/sharded "$i" >/dev/null 2>&1
  done
  [ -z "${SKIP_EXITCODE:-}" ] || rm -f .logs/go-test/race/shard-example-sharded-2.exitcode
  bash "$SCRIPT" --finalize .logs/go-test .logs/pass.tree 2>&1
}

mkdir .logs

echo dirty >> tracked.txt
echo new > untracked.txt
gate_go_steps >/dev/null
check "a passing run exits 0" "0" "$?"
check "a passing run records the dirty tree, untracked files included" \
  "$(bash "$SCRIPT" --fingerprint)" "$(cat .logs/pass.tree 2>/dev/null)"
check "go-test.json holds the rest's and every plain shard's events" "3" \
  "$(grep -c -e '"Action":"run"' -e '"Package":"example/rest"' go-test.json)"

out="$(STUB_FAIL_RUN=TestOne gate_go_steps)"
check "a failing shard fails finalize" "1" "$?"
check "a failing shard clears the record" "no" "$(recorded)"
check "finalize names the shard that failed" "yes" \
  "$(grep -q 'shard-example-sharded-' <<<"$out" && echo yes || echo no)"

gate_go_steps >/dev/null
out="$(SKIP_EXITCODE=1 gate_go_steps)"
check "a step that left no exit code fails finalize" "1" "$?"
check "a step that left no exit code clears the record" "no" "$(recorded)"
check "finalize names the step with no exit code" "yes" \
  "$(grep -qF 'race/shard-example-sharded-2 (no exit code)' <<<"$out" && echo yes || echo no)"

out="$(STUB_DROP=TestTwo gate_go_steps)"
check "a sharded test that ran nowhere fails finalize" "1" "$?"
check "a coverage defect records nothing" "no" "$(recorded)"
check "the missing test is named" "yes" \
  "$(grep -qF 'example/sharded TestTwo ran in no shard' <<<"$out" && echo yes || echo no)"

STUB_EDIT=tracked.txt gate_go_steps >/dev/null
check "an edit during the run passes but records nothing" "no" "$(recorded)"

gate_go_steps >/dev/null
printf 'shard example/sharded 2\nserial example/sharded TestGone renamed\n' > "$TMP/shards.txt"
gate_go_steps >/dev/null
check "a failing plan step fails before any test runs" "9" "$?"
check "a failing plan step clears the record" "no" "$(recorded)"
printf 'shard example/sharded 2\n' > "$TMP/shards.txt"

echo tracked.txt >> .git/info/exclude
echo ignored > tracked-twin.txt
echo tracked-twin.txt >> .git/info/exclude
listed="$(git ls-tree -r --name-only "$(bash "$SCRIPT" --fingerprint)")"
check "a tracked file matching an exclude stays in the tree" \
  "yes" "$(grep -qx tracked.txt <<<"$listed" && echo yes || echo no)"
check "an untracked file matching an exclude stays out" \
  "no" "$(grep -qx tracked-twin.txt <<<"$listed" && echo yes || echo no)"

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
