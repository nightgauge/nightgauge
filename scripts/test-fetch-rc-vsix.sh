#!/usr/bin/env bash
#
# test-fetch-rc-vsix.sh — hermetic regression suite for scripts/fetch-rc-vsix.sh
# (#2722). A stub `gh` on PATH stands in for the forge.
#
# Run: bash scripts/test-fetch-rc-vsix.sh   (also run by scripts/ci-local.sh)
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/fetch-rc-vsix.sh"
PASS=0
FAIL=0
TMP="$(mktemp -d)" || { echo "HARNESS ERROR: mktemp failed."; exit 2; }
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"

cat >"$TMP/bin/gh" <<'STUB' || { echo "HARNESS ERROR: cannot write the gh stub."; exit 2; }
#!/usr/bin/env bash
case "$1 $2" in
  "run list")
    [ -n "${STUB_NO_RUN:-}" ] || echo 4242 ;;
  "run download")
    [ -z "${STUB_EXPIRED:-}" ] || { echo "no valid artifacts found to download"; exit 1; }
    while [ $# -gt 0 ]; do [ "$1" = --dir ] && d="$2"; shift; done
    mkdir -p "$d/packages/nightgauge"
    for t in ${STUB_TARGETS-linux-x64 darwin-arm64}; do
      echo "$t" >"$d/packages/nightgauge/nightgauge-$t-0.8.1-rc.4.vsix"
    done ;;
  *) echo "stub: unexpected: $*"; exit 1 ;;
esac
STUB
chmod +x "$TMP/bin/gh" || { echo "HARNESS ERROR: cannot install the gh stub."; exit 2; }

# expect NAME EXIT PATTERN TAG [ENV...]
expect() {
  local name="$1" want="$2" pattern="$3" tag="$4" got
  shift 4
  rm -rf "$TMP/dest"
  env "$@" PATH="$TMP/bin:$PATH" bash "$SCRIPT" "$tag" "$TMP/dest" >"$TMP/out" 2>&1
  got=$?
  if [ "$got" -eq "$want" ] && grep -q -- "$pattern" "$TMP/out"; then
    PASS=$((PASS + 1)); echo "PASS: $name"
  else
    FAIL=$((FAIL + 1)); echo "FAIL: $name (exit $got, wanted $want matching '$pattern')"
    sed 's/^/  /' "$TMP/out"
  fi
}

T=v0.8.1-rc.4
expect "happy path prints the vsix" 0 "^vsix=.*linux-x64-0.8.1-rc.4.vsix$" "$T"
expect "happy path prints the run id" 0 "^run_id=4242$" "$T"
expect "happy path prints the digest" 0 "^sha256=[0-9a-f]\{64\}$" "$T"
expect "no successful run" 1 "no rc-artifacts-$T — no successful" "$T" STUB_NO_RUN=1
expect "expired artifact" 1 "no rc-artifacts-$T — download" "$T" STUB_EXPIRED=1
expect "no linux-x64 vsix" 1 "no rc-artifacts-$T — .* 0 linux-x64" "$T" STUB_TARGETS=darwin-arm64
expect "two linux-x64 vsixes" 1 "no rc-artifacts-$T — .* 2 linux-x64" "$T" \
  "STUB_TARGETS=linux-x64 linux-x64-dup"
expect "bad tag shape" 1 "no rc-artifacts-v0.8.1 — 'v0.8.1' is not an RC tag" v0.8.1
expect "injection-shaped tag" 1 "is not an RC tag" 'v1.2.3-rc.1;rm'

echo "fetch-rc-vsix suite: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
