#!/usr/bin/env bash
#
# test-go-test-cache.sh — the local gate's use of Go's test cache (#3208).
#
#   - check-go-test-skips.py counts cached packages, still accounts the skips a
#     cached package replays, and never reports a cached package as skipped;
#   - ci-local-go-test.sh passes -count=1 only under CI_LOCAL_NO_TEST_CACHE=1,
#     and runs the always-uncached list otherwise (plain and --race);
#   - go-test-uncached.py runs each package's entries with -count=1 -run, fails
#     on a failing listed test, and fails naming a listed test that ran nothing.
#
# A stub `go` that records its argv, so seconds.
# Run: bash scripts/test-go-test-cache.sh   (also run by scripts/ci-local.sh)
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKIPS="$REPO_ROOT/scripts/check-go-test-skips.py"
GO_TEST="$REPO_ROOT/scripts/ci-local-go-test.sh"
UNCACHED="$REPO_ROOT/scripts/go-test-uncached.py"
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

# An allowlisted skip, so the cached replay of a skip is accountable.
ALLOWED_SKIP="$(sed -e 's/#.*//' -e 's/[[:space:]]*$//' "$REPO_ROOT/scripts/go-test-skip-allowlist.txt" \
  | grep -v '^$' | sed -n 1p)"
if [ -z "$ALLOWED_SKIP" ]; then
  echo "HARNESS ERROR: go-test-skip-allowlist.txt has no entry to replay"
  exit 1
fi
SKIP_PKG="${ALLOWED_SKIP%.*}"
SKIP_TEST="${ALLOWED_SKIP##*.}"

# ── skip accounting over a stream with cached packages ─────────────────────
cat > "$TMP/cached.json" <<JSON
{"Action":"start","Package":"$SKIP_PKG"}
{"Action":"run","Package":"$SKIP_PKG","Test":"$SKIP_TEST"}
{"Action":"skip","Package":"$SKIP_PKG","Test":"$SKIP_TEST","Elapsed":0}
{"Action":"output","Package":"$SKIP_PKG","Output":"ok  \t$SKIP_PKG\t(cached)\n"}
{"Action":"pass","Package":"$SKIP_PKG","Elapsed":0}
{"Action":"run","Package":"example/cached","Test":"TestA"}
{"Action":"pass","Package":"example/cached","Test":"TestA","Elapsed":0}
{"Action":"output","Package":"example/cached","Output":"ok  \texample/cached\t(cached)\n"}
{"Action":"pass","Package":"example/cached","Elapsed":0}
{"Action":"run","Package":"example/ran","Test":"TestB"}
{"Action":"pass","Package":"example/ran","Test":"TestB","Elapsed":0.1}
{"Action":"output","Package":"example/ran","Output":"ok  \texample/ran\t0.2s\n"}
{"Action":"pass","Package":"example/ran","Elapsed":0.2}
{"Action":"output","Package":"example/notests","Output":"?   \texample/notests\t[no test files]\n"}
{"Action":"skip","Package":"example/notests","Elapsed":0}
JSON
out="$(python3 "$SKIPS" "$TMP/cached.json" 2>&1)"
check "accounting passes on a cached stream" $?
grep -qx 'Go test packages: 1 ran, 2 cached' <<<"$out"
check "it counts 1 package ran and 2 cached (no-test-files package is neither)" $?
grep -qF "SKIP $ALLOWED_SKIP" <<<"$out"
check "a skip replayed from the cache is still accounted" $?
! grep -q 'example/cached' <<<"$out"
check "a cached package is not reported as skipped" $?

cp "$TMP/cached.json" "$TMP/cached-unlisted.json"
printf '%s\n' '{"Action":"skip","Package":"example/cached","Test":"TestUnlisted","Elapsed":0}' \
  >> "$TMP/cached-unlisted.json"
python3 "$SKIPS" "$TMP/cached-unlisted.json" >/dev/null 2>&1
[ $? -eq 1 ]
check "an unlisted skip replayed from the cache still fails" $?

# ── stub go: argv recorder ─────────────────────────────────────────────────
mkdir -p "$TMP/bin" "$TMP/repo"
cat > "$TMP/bin/go" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$STUB_ARGV"
pkg="${!#}"
run=""
for ((i = 1; i < $#; i++)); do
  [ "${!i}" = "-run" ] && { j=$((i + 1)); run="${!j}"; }
done
if [ -n "$run" ]; then
  # Emit a run event for each name in ^(A|B)$, except STUB_MISSING.
  names="${run#^(}"; names="${names%)\$}"
  IFS='|' read -ra list <<<"$names"
  for t in "${list[@]}"; do
    [ "$t" = "${STUB_MISSING:-}" ] && continue
    echo "{\"Action\":\"run\",\"Package\":\"$pkg\",\"Test\":\"$t\"}"
    echo "{\"Action\":\"pass\",\"Package\":\"$pkg\",\"Test\":\"$t\"}"
  done
fi
echo "{\"Action\":\"pass\",\"Package\":\"$pkg\"}"
exit "${STUB_EXIT:-0}"
STUB
chmod +x "$TMP/bin/go"
export PATH="$TMP/bin:$PATH"
export STUB_ARGV="$TMP/argv"
cat > "$TMP/list.txt" <<'LIST'
# header comment
example/one TestAlpha   # reason
example/one TestBeta
example/two TestGamma
LIST
export GO_TEST_UNCACHED_LIST="$TMP/list.txt"

cd "$TMP/repo" || { echo "HARNESS ERROR: no fixture directory"; exit 1; }
if ! { git init -q && git config user.email t@example.com && git config user.name t &&
  printf 'go-test.json\n' > .gitignore && git add -A && git commit -qm init; }; then
  echo "HARNESS ERROR: fixture repository could not be created"
  exit 1
fi

: > "$STUB_ARGV"
bash "$GO_TEST" 20m "$TMP/pass.tree" >/dev/null 2>&1
check "the cached plain pass succeeds" $?
FIRST_ARGV="$(sed -n 1p "$STUB_ARGV")"
grep -q -- '-json \./\.\.\.' <<<"$FIRST_ARGV" && ! grep -q -- '-count=1' <<<"$FIRST_ARGV"
check "the main plain pass runs without -count=1 by default" $?
grep -qx -- "test -json -count=1 -timeout 20m -run ^(TestAlpha|TestBeta)\$ example/one" "$STUB_ARGV"
check "the listed tests run per package with -count=1 -run '^(A|B)\$'" $?
grep -q '"Package":"example/two","Test":"TestGamma"' go-test.json
check "the uncached run's events are appended to go-test.json" $?

: > "$STUB_ARGV"
CI_LOCAL_NO_TEST_CACHE=1 bash "$GO_TEST" 20m "$TMP/pass.tree" >/dev/null 2>&1
check "the uncached plain pass succeeds" $?
grep -q -- '-json \./\.\.\. -count=1' "$STUB_ARGV" && [ "$(wc -l < "$STUB_ARGV")" -eq 1 ]
check "CI_LOCAL_NO_TEST_CACHE=1 adds -count=1 and skips the separate list run" $?

: > "$STUB_ARGV"
bash "$GO_TEST" --race 20m >/dev/null 2>&1
check "the cached race pass succeeds" $?
[ "$(sed -n 1p "$STUB_ARGV")" = 'test -race -timeout 20m ./...' ]
check "the race pass runs go test -race without -count=1" $?
grep -qx -- "test -json -count=1 -race -timeout 20m -run ^(TestGamma)\$ example/two" "$STUB_ARGV"
check "the race pass runs the list with -race" $?

: > "$STUB_ARGV"
CI_LOCAL_NO_TEST_CACHE=1 bash "$GO_TEST" --race 20m >/dev/null 2>&1
grep -qx -- 'test -race -count=1 -timeout 20m ./...' "$STUB_ARGV" && [ "$(wc -l < "$STUB_ARGV")" -eq 1 ]
check "CI_LOCAL_NO_TEST_CACHE=1 race pass is -count=1 with no list run" $?

# ── the runner's failure modes ─────────────────────────────────────────────
err="$(STUB_MISSING=TestBeta python3 "$UNCACHED" --list "$TMP/list.txt" 2>&1 >/dev/null)"
[ $? -eq 1 ]
check "a listed test that ran nothing fails the runner" $?
grep -qF 'example/one TestBeta' <<<"$err"
check "the stale entry is named" $?

STUB_EXIT=1 python3 "$UNCACHED" --list "$TMP/list.txt" >/dev/null 2>&1
[ $? -eq 1 ]
check "a failing go test fails the runner" $?

printf 'example/one\n' > "$TMP/bad.txt"
python3 "$UNCACHED" --list "$TMP/bad.txt" >/dev/null 2>&1
[ $? -eq 2 ]
check "a malformed list line is refused" $?

# The real list parses, and every entry names a test that exists in its package.
cd "$REPO_ROOT" || exit 1
python3 - "$REPO_ROOT/scripts/go-test-uncached.txt" <<'PY'
import glob, os, re, sys
mod = "github.com/nightgauge/nightgauge/"
bad = []
for line in open(sys.argv[1]):
    line = line.split("#", 1)[0].strip()
    if not line:
        continue
    pkg, test = line.split()
    files = glob.glob(os.path.join(pkg[len(mod):], "*_test.go")) if pkg.startswith(mod) else []
    pat = re.compile(r"^func %s\(t \*testing\.T\)" % re.escape(test), re.M)
    if not any(pat.search(open(f).read()) for f in files):
        bad.append(line)
if bad:
    print("missing:\n  " + "\n  ".join(bad))
    sys.exit(1)
PY
check "every entry in scripts/go-test-uncached.txt names an existing test" $?

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
