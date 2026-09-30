#!/usr/bin/env bash
# test-vsix-host-target.sh — regression suite for
# packages/nightgauge-vscode/scripts/vsix-host-target.sh (#2309).
# dev-install.sh --from-release once installed the linux-x64 VSIX on an arm64
# Mac because it took whichever downloaded file sorted first. Every supported
# host must map to its own target, and an unsupported host must fail instead
# of falling back to some other target.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="$ROOT/packages/nightgauge-vscode/scripts/vsix-host-target.sh"
DEV_INSTALL="$ROOT/packages/nightgauge-vscode/scripts/dev-install.sh"

PASS=0
FAIL=0
pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); }

expect_target() {
  local os="$1" arch="$2" want="$3" got
  if got=$(bash "$TARGET" "$os" "$arch" 2>/dev/null) && [ "$got" = "$want" ]; then
    pass "$os/$arch -> $want"
  else
    fail "$os/$arch: want $want, got '${got:-<error>}'"
  fi
}

expect_refused() {
  local os="$1" arch="$2" out rc
  set +e
  out=$(bash "$TARGET" "$os" "$arch" 2>&1 >/dev/null)
  rc=$?
  set -e
  if [ "$rc" -ne 0 ] && [[ "$out" == *"$os/$arch"* ]]; then
    pass "$os/$arch refused, naming the host"
  else
    fail "$os/$arch: want a non-zero exit naming the host, got rc=$rc '$out'"
  fi
}

echo "=== test-vsix-host-target.sh ==="
expect_target Darwin arm64 darwin-arm64
expect_target Darwin aarch64 darwin-arm64
expect_target Darwin x86_64 darwin-x64
expect_target Linux x86_64 linux-x64
expect_target Linux amd64 linux-x64
expect_refused Linux aarch64
expect_refused MINGW64_NT-10.0 x86_64

# The host itself resolves or is refused; it never prints a blank target.
set +e
host=$(bash "$TARGET" 2>/dev/null)
host_rc=$?
set -e
if { [ "$host_rc" -eq 0 ] && [ -n "$host" ]; } || [ "$host_rc" -ne 0 ]; then
  pass "running host resolves to '${host:-<refused>}'"
else
  fail "running host printed an empty target with exit 0"
fi

# dev-install.sh must download by target, never every *.vsix.
if grep -q 'vsix-host-target.sh' "$DEV_INSTALL" && ! grep -q -- '--pattern "\*\.vsix"' "$DEV_INSTALL"; then
  pass "dev-install.sh --from-release downloads the host target only"
else
  fail "dev-install.sh --from-release still downloads every *.vsix"
fi

echo ""
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
