#!/usr/bin/env bash
#
# test-go-test-shard.sh — scripts/go-test-shard.py, the partitioner behind the
# local gate's sharded Go test steps (#3209):
#
#   - `steps` lists plain and race shard steps from the config alone;
#   - `plan` buckets tests by crc32(name) % N, the same way every run, forces a
#     `serial` pin into shard 1, ignores benchmarks, and fails naming a stale pin,
#     a package with no tests, and a malformed config line;
#   - `run` runs one shard with `-run '^(...)$'`, passes on an empty shard, and
#     adds -count=1 only under CI_LOCAL_NO_TEST_CACHE=1;
#   - `rest` runs every package the config does not shard;
#   - `check` fails naming a planned test that ran in no shard or in two.
#
# A stub `go` that records its argv and reads each package's tests from a file,
# so seconds. scripts/test-ci-local-go-pass-stamp.sh covers the gate's
# prepare/finalize flow over the same pieces.
# Run: bash scripts/test-go-test-shard.sh   (also run by scripts/ci-local.sh)
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SHARD="$REPO_ROOT/scripts/go-test-shard.py"
PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

check() { # $1 label; status of the previous command
  if [ "$2" -eq 0 ]; then
    echo "  ✓ $1"
    PASS=$((PASS + 1))
  else
    echo "  ✗ $1"
    FAIL=$((FAIL + 1))
  fi
}

# ── stub go ─────────────────────────────────────────────────────────────────
# `go list ./...` prints $STUB_DIR/packages. `go test -list . P` prints
# $STUB_DIR/<P with / as _>.tests. `go test ... -run '^(A|B)$' P` emits a run and
# a pass event per name, skipping $STUB_DROP and doubling $STUB_DUP. Any other
# `go test` emits a pass event per package argument.
mkdir -p "$TMP/bin" "$TMP/stub"
cat > "$TMP/bin/go" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$STUB_ARGV"
if [ "$1" = list ]; then
  cat "$STUB_DIR/packages"
  exit 0
fi
args=("$@")
run=""
pkgs=()
for ((i = 1; i < ${#args[@]}; i++)); do
  case "${args[$i]}" in
    -list) cat "$STUB_DIR/$(printf '%s' "${args[$((i + 2))]}" | tr / _).tests"
      echo "ok  	${args[$((i + 2))]}	0.01s"; exit 0 ;;
    -run) i=$((i + 1)); run="${args[$i]}" ;;
    -timeout) i=$((i + 1)) ;;
    -*) ;;
    *) pkgs+=("${args[$i]}") ;;
  esac
done
for pkg in "${pkgs[@]}"; do
  if [ -n "$run" ]; then
    names="${run#^(}"; names="${names%)\$}"
    IFS='|' read -ra list <<<"$names"
    for t in "${list[@]}"; do
      [ "$t" = "${STUB_DROP:-}" ] && continue
      reps=1; [ "$t" = "${STUB_DUP:-}" ] && reps=2
      for ((r = 0; r < reps; r++)); do
        echo "{\"Action\":\"run\",\"Package\":\"$pkg\",\"Test\":\"$t\"}"
        echo "{\"Action\":\"run\",\"Package\":\"$pkg\",\"Test\":\"$t/sub\"}"
        echo "{\"Action\":\"pass\",\"Package\":\"$pkg\",\"Test\":\"$t\"}"
      done
    done
  fi
  echo "{\"Action\":\"pass\",\"Package\":\"$pkg\"}"
done
exit "${STUB_EXIT:-0}"
STUB
chmod +x "$TMP/bin/go"
export PATH="$TMP/bin:$PATH"
export STUB_ARGV="$TMP/argv" STUB_DIR="$TMP/stub"
printf '%s\n' example/a example/b example/rest > "$STUB_DIR/packages"
printf '%s\n' TestAlpha TestBravo TestCharlie TestDelta TestEcho TestFoxtrot TestGolf \
  TestHotel TestIndia TestJuliet FuzzKilo ExampleLima BenchmarkMike > "$STUB_DIR/example_a.tests"
printf '%s\n' TestOnly > "$STUB_DIR/example_b.tests"
: > "$STUB_DIR/example_empty.tests"

CONFIG="$TMP/shards.txt"
cat > "$CONFIG" <<'CFG'
# header
shard example/a 3   # three processes
shard example/b 2
CFG
shard() { python3 "$SHARD" --config "$CONFIG" "$@"; }
shards_of() { # <plan> <pkg> — one line per shard, names space-separated
  python3 -c 'import json,sys
for b in json.load(open(sys.argv[1]))["packages"][sys.argv[2]]["shards"]: print(" ".join(b))' "$1" "$2"
}

cd "$TMP" || { echo "HARNESS ERROR: no fixture directory"; exit 1; }

# ── steps ───────────────────────────────────────────────────────────────────
: > "$STUB_ARGV"
steps="$(shard steps)"
check "steps succeeds" $?
[ "$steps" = "$(printf '%s\n' \
  'plain example/a 1 3' 'race example/a 1 3' 'plain example/a 2 3' 'race example/a 2 3' \
  'plain example/a 3 3' 'race example/a 3 3' 'plain example/b 1 2' 'race example/b 1 2' \
  'plain example/b 2 2' 'race example/b 2 2')" ]
check "steps lists a plain and a race step per shard, in config order" $?
[ ! -s "$STUB_ARGV" ]
check "steps runs no go command" $?

# ── plan ────────────────────────────────────────────────────────────────────
shard plan --out "$TMP/plan.json" >/dev/null 2>&1
check "plan succeeds" $?
# crc32(name) % 3 + 1, pinned here so a change to the hash cannot pass unseen:
# moving every test to a new shard at once defeats the per-shard test cache.
[ "$(shards_of "$TMP/plan.json" example/a)" = "$(printf '%s\n' \
  'ExampleLima TestBravo TestGolf TestHotel TestIndia' \
  'TestAlpha TestCharlie TestDelta TestEcho TestJuliet' \
  'FuzzKilo TestFoxtrot')" ]
check "tests land in shard crc32(name) % N + 1, examples and fuzz targets included" $?
! grep -q BenchmarkMike "$TMP/plan.json"
check "benchmarks are not planned" $?
shard plan --out "$TMP/plan2.json" >/dev/null 2>&1 && cmp -s "$TMP/plan.json" "$TMP/plan2.json"
check "the plan is identical on a second run" $?
[ "$(shards_of "$TMP/plan.json" example/b)" = "$(printf 'TestOnly\n')" ] ||
  [ "$(shards_of "$TMP/plan.json" example/b)" = "$(printf '\nTestOnly')" ]
check "a package with fewer tests than shards plans an empty shard" $?

cp "$CONFIG" "$TMP/pinned.txt"
echo 'serial example/a TestFoxtrot shares a fixed port with TestGolf' >> "$TMP/pinned.txt"
python3 "$SHARD" --config "$TMP/pinned.txt" plan --out "$TMP/pinned.json" >/dev/null 2>&1
check "plan with a serial pin succeeds" $?
pinned_shards="$(shards_of "$TMP/pinned.json" example/a)"
grep -qw TestFoxtrot <<<"$(sed -n 1p <<<"$pinned_shards")" &&
  ! grep -qw TestFoxtrot <<<"$(sed -n 3p <<<"$pinned_shards")"
check "a serial pin moves its test into shard 1" $?

cp "$CONFIG" "$TMP/stale.txt"
echo 'serial example/a TestRenamed was renamed' >> "$TMP/stale.txt"
err="$(python3 "$SHARD" --config "$TMP/stale.txt" plan --out "$TMP/stale.json" 2>&1 >/dev/null)"
[ $? -eq 1 ] && [ ! -e "$TMP/stale.json" ]
check "a serial pin naming no test fails the plan and writes none" $?
grep -qF 'example/a: TestRenamed' <<<"$err"
check "the stale pin is named" $?

printf 'shard example/empty 2\n' > "$TMP/empty.txt"
err="$(python3 "$SHARD" --config "$TMP/empty.txt" plan --out "$TMP/empty.json" 2>&1 >/dev/null)"
[ $? -eq 1 ] && grep -qF 'example/empty lists no tests' <<<"$err"
check "a sharded package with no tests fails the plan, named" $?

for bad in 'shard example/a' 'shard example/a 0' 'serial example/a TestAlpha' \
  'serial example/zzz TestAlpha reason' 'shuffle example/a 2'; do
  printf '%s\n' "$bad" > "$TMP/bad.txt"
  python3 "$SHARD" --config "$TMP/bad.txt" steps >/dev/null 2>&1
  [ $? -eq 2 ]
  check "a malformed config line is refused: $bad" $?
done

# ── run ─────────────────────────────────────────────────────────────────────
: > "$STUB_ARGV"
shard run --plan "$TMP/plan.json" --pkg example/a --shard 3 --timeout 7m \
  --json "$TMP/a3.json" >/dev/null 2>&1
check "run succeeds" $?
grep -qx -- 'test -json -timeout 7m -run ^(FuzzKilo|TestFoxtrot)$ example/a' "$STUB_ARGV"
check "run executes one shard with an anchored -run and no -count=1 by default" $?
grep -q '"Test":"TestFoxtrot"' "$TMP/a3.json"
check "run writes the go test -json events" $?

: > "$STUB_ARGV"
CI_LOCAL_NO_TEST_CACHE=1 shard run --race --plan "$TMP/plan.json" --pkg example/a --shard 3 \
  --timeout 7m --json "$TMP/a3r.json" >/dev/null 2>&1
grep -qx -- 'test -json -race -count=1 -timeout 7m -run ^(FuzzKilo|TestFoxtrot)$ example/a' "$STUB_ARGV"
check "CI_LOCAL_NO_TEST_CACHE=1 adds -count=1, and --race adds -race" $?

STUB_EXIT=1 shard run --plan "$TMP/plan.json" --pkg example/a --shard 1 --timeout 7m \
  --json "$TMP/fail.json" >/dev/null 2>&1
[ $? -eq 1 ]
check "a failing go test fails run" $?

empty_index=1
[ -z "$(shards_of "$TMP/plan.json" example/b | sed -n 1p)" ] || empty_index=2
: > "$STUB_ARGV"
out="$(shard run --plan "$TMP/plan.json" --pkg example/b --shard "$empty_index" --timeout 7m \
  --json "$TMP/empty-shard.json" 2>&1)"
[ $? -eq 0 ] && [ ! -s "$STUB_ARGV" ] && [ -e "$TMP/empty-shard.json" ] && grep -q 'has no tests' <<<"$out"
check "an empty shard passes without running go, and says so" $?

shard run --plan "$TMP/plan.json" --pkg example/a --shard 4 --timeout 7m --json "$TMP/x.json" >/dev/null 2>&1
[ $? -eq 2 ]
check "a shard index outside the package's shards is refused" $?

# ── rest ────────────────────────────────────────────────────────────────────
: > "$STUB_ARGV"
shard rest --timeout 7m --json "$TMP/rest.json" >/dev/null 2>&1
check "rest succeeds" $?
grep -qx -- 'test -json -timeout 7m example/rest' "$STUB_ARGV"
check "rest runs every package the config does not shard, and only those" $?

# ── check ───────────────────────────────────────────────────────────────────
for i in 1 2 3; do
  shard run --plan "$TMP/plan.json" --pkg example/a --shard "$i" --timeout 7m \
    --json "$TMP/full-a$i.json" >/dev/null 2>&1
done
shard run --plan "$TMP/plan.json" --pkg example/b --shard $((3 - empty_index)) --timeout 7m \
  --json "$TMP/full-b.json" >/dev/null 2>&1
python3 "$SHARD" check --plan "$TMP/plan.json" "$TMP"/full-*.json >/dev/null 2>&1
check "check passes when every planned test ran exactly once (subtests ignored)" $?

err="$(python3 "$SHARD" check --plan "$TMP/plan.json" "$TMP"/full-a1.json "$TMP"/full-a2.json \
  "$TMP"/full-b.json 2>&1 >/dev/null)"
[ $? -eq 1 ] && grep -qF 'example/a TestFoxtrot ran in no shard' <<<"$err" &&
  grep -qF 'example/a FuzzKilo ran in no shard' <<<"$err"
check "check fails naming every test of a shard whose output is missing" $?

STUB_DROP=TestGolf shard run --plan "$TMP/plan.json" --pkg example/a --shard 1 --timeout 7m \
  --json "$TMP/full-a1.json" >/dev/null 2>&1
err="$(python3 "$SHARD" check --plan "$TMP/plan.json" "$TMP"/full-*.json 2>&1 >/dev/null)"
[ $? -eq 1 ] && grep -qF 'example/a TestGolf ran in no shard' <<<"$err"
check "check fails naming a planned test that did not run" $?

STUB_DUP=TestGolf shard run --plan "$TMP/plan.json" --pkg example/a --shard 1 --timeout 7m \
  --json "$TMP/full-a1.json" >/dev/null 2>&1
err="$(python3 "$SHARD" check --plan "$TMP/plan.json" "$TMP"/full-*.json 2>&1 >/dev/null)"
[ $? -eq 1 ] && grep -qF 'example/a TestGolf ran 2 times' <<<"$err"
check "check fails naming a test that ran twice" $?

# ── names ───────────────────────────────────────────────────────────────────
python3 "$SHARD" names "$TMP/a3.json" "$TMP/a3.json" > "$TMP/names.txt"
[ "$(cat "$TMP/names.txt")" = "$(printf '%s\n' 'example/a FuzzKilo' 'example/a FuzzKilo/sub' \
  'example/a TestFoxtrot' 'example/a TestFoxtrot/sub')" ]
check "names prints the sorted unique tests and subtests that ran" $?

# ── the real config ─────────────────────────────────────────────────────────
python3 "$SHARD" steps >/dev/null
check "scripts/go-test-shards.txt parses" $?

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
