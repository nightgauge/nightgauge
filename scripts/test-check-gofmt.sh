#!/usr/bin/env bash
# Regression suite for scripts/check-gofmt.sh (#3192): the gate's gofmt must be
# the toolchain go.mod selects.
#
# Offline: `go` and `gofmt` are stubs, so no arm needs a particular Go release.
#
# Exit codes: 0 every assertion held; 1 an assertion failed; 2 HARNESS ERROR
# (the check could not run, so it asserted nothing).
set -uo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
CHECK="$REPO/scripts/check-gofmt.sh"
PASS=0
FAIL=0

[ -f "$CHECK" ] || { echo "HARNESS ERROR: $CHECK does not exist."; exit 2; }
TMP="$(mktemp -d)" || { echo "HARNESS ERROR: mktemp failed."; exit 2; }
trap 'rm -rf "$TMP"' EXIT

check() {
  local name="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo "PASS: $name"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $name (expected '$expected', got '$actual')"
    FAIL=$((FAIL + 1))
  fi
}

contains() {
  local name="$1" needle="$2" haystack="$3"
  case "$haystack" in
    *"$needle"*) echo "PASS: $name"; PASS=$((PASS + 1)) ;;
    *) echo "FAIL: $name (output lacks '$needle': $haystack)"; FAIL=$((FAIL + 1)) ;;
  esac
}

# Stub toolchain: `go env GOVERSION|GOROOT` reads STUB_GOVERSION / STUB_GOROOT;
# gofmt prints STUB_GOFMT_OUT and records its arguments.
STUBS="$TMP/stubs"
FAKE_GOROOT="$TMP/goroot"
mkdir -p "$STUBS" "$FAKE_GOROOT/bin" "$TMP/root"
cat >"$STUBS/go" <<'EOF'
#!/usr/bin/env bash
[ "$1" = "env" ] || exit 3
case "$2" in
  GOVERSION) echo "$STUB_GOVERSION" ;;
  GOROOT) echo "$STUB_GOROOT" ;;
  *) exit 3 ;;
esac
EOF
cat >"$FAKE_GOROOT/bin/gofmt" <<'EOF'
#!/usr/bin/env bash
echo "$*" >"$STUB_GOFMT_ARGS"
[ -n "${STUB_GOFMT_OUT:-}" ] && printf '%s\n' "$STUB_GOFMT_OUT"
exit 0
EOF
chmod +x "$STUBS/go" "$FAKE_GOROOT/bin/gofmt"

# PATH for the arms: the stubs plus the system tools the script needs, and no
# real `go` (a /usr/bin/go would shadow nothing: STUBS comes first).
BASE_PATH="$STUBS:/usr/bin:/bin"
export STUB_GOROOT="$FAKE_GOROOT"
export STUB_GOFMT_ARGS="$TMP/gofmt-args"

write_gomod() { printf 'module example.com/x\n\n%s\n' "$1" >"$TMP/root/go.mod"; }

run() {
  OUT="$(PATH="$BASE_PATH" bash "$CHECK" "$TMP/root" 2>&1)"
  CODE=$?
}

# Arm 1: versions match, gofmt prints nothing -> 0, and gofmt ran on the dirs.
write_gomod "go 1.27.4"
rm -f "$STUB_GOFMT_ARGS"
STUB_GOVERSION=go1.27.4 STUB_GOFMT_OUT="" run
check "matching toolchain, formatted tree exits 0" "0" "$CODE"
check "gofmt ran with -l ./internal ./cmd" "-l ./internal ./cmd" \
  "$(cat "$STUB_GOFMT_ARGS" 2>/dev/null)"

# Arm 2: a newer Go (never downgraded by GOTOOLCHAIN=auto) -> 1, naming both.
rm -f "$STUB_GOFMT_ARGS"
STUB_GOVERSION=go1.28.0 STUB_GOFMT_OUT="" run
check "newer toolchain than go.mod exits 1" "1" "$CODE"
contains "mismatch message names the toolchain in use" "go1.28.0" "$OUT"
contains "mismatch message names go.mod's toolchain" "go1.27.4" "$OUT"
contains "mismatch message names the remedy" "GOTOOLCHAIN=go1.27.4" "$OUT"
check "gofmt is not run on a mismatch" "absent" \
  "$([ -f "$STUB_GOFMT_ARGS" ] && echo present || echo absent)"

# Arm 3: an older Go -> 1 (same rule; the check is symmetric).
STUB_GOVERSION=go1.26.11 STUB_GOFMT_OUT="" run
check "older toolchain than go.mod exits 1" "1" "$CODE"

# Arm 4: a toolchain directive wins over the go directive.
write_gomod "go 1.27.4
toolchain go1.27.5"
STUB_GOVERSION=go1.27.5 STUB_GOFMT_OUT="" run
check "toolchain directive matching exits 0" "0" "$CODE"
STUB_GOVERSION=go1.27.4 STUB_GOFMT_OUT="" run
check "go directive alone does not satisfy a toolchain directive" "1" "$CODE"
contains "toolchain-directive mismatch names go1.27.5" "go1.27.5" "$OUT"

# Arm 5: a two-part go directive accepts any patch of that release only.
write_gomod "go 1.27"
STUB_GOVERSION=go1.27.9 STUB_GOFMT_OUT="" run
check "'go 1.27' accepts go1.27.9" "0" "$CODE"
STUB_GOVERSION=go1.270.1 STUB_GOFMT_OUT="" run
check "'go 1.27' rejects go1.270.1" "1" "$CODE"

# Arm 6: an unformatted file -> 1, naming it.
write_gomod "go 1.27.4"
STUB_GOVERSION=go1.27.4 STUB_GOFMT_OUT="internal/x/bad.go" run
check "unformatted file exits 1" "1" "$CODE"
contains "unformatted file is named" "internal/x/bad.go" "$OUT"

# Arm 7: no go on PATH -> 2 with HARNESS ERROR (could not run, not a red).
OUT="$(PATH="/usr/bin:/bin" bash -c 'command -v go >/dev/null && exit 99; bash "$0" "$1"' \
  "$CHECK" "$TMP/root" 2>&1)"
CODE=$?
if [ "$CODE" -eq 99 ]; then
  echo "SKIP: a go on /usr/bin or /bin prevents the missing-go arm"
else
  check "missing go exits 2" "2" "$CODE"
  contains "missing go prints HARNESS ERROR" "HARNESS ERROR" "$OUT"
fi

# Arm 8: GOROOT without a gofmt -> 2.
STUB_GOROOT="$TMP/empty-goroot" STUB_GOVERSION=go1.27.4 run
check "missing gofmt exits 2" "2" "$CODE"
contains "missing gofmt prints HARNESS ERROR" "HARNESS ERROR" "$OUT"

# Arm 9: no go.mod -> 2.
rm -f "$TMP/root/go.mod"
STUB_GOVERSION=go1.27.4 run
check "missing go.mod exits 2" "2" "$CODE"

echo ""
echo "check-gofmt suite: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
