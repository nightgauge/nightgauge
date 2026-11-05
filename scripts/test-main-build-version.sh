#!/usr/bin/env bash
#
# test-main-build-version.sh — regression suite for
# scripts/main-build-version.sh (#2952). Every case runs in a throwaway git
# repository, so the suite never depends on this clone's tags.
#
# Run: bash scripts/test-main-build-version.sh
# Also run by scripts/ci-local.sh.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/main-build-version.sh"
CHANNEL="$REPO_ROOT/scripts/marketplace-channel.sh"
PASS=0
FAIL=0
TMP="$(mktemp -d)" || { echo "HARNESS ERROR: mktemp failed"; exit 2; }
trap 'rm -rf "$TMP"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

ok() { echo "PASS: $1"; PASS=$((PASS + 1)); }
bad() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

g() { git -C "$TMP/r" -c user.name=t -c user.email=t@t "$@"; }
git init -q -b main "$TMP/r" || { echo "HARNESS ERROR: git init failed"; exit 2; }
g commit -q --allow-empty -m one

out="$(cd "$TMP/r" && bash "$SCRIPT" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && [[ "$out" == *"HARNESS ERROR"* ]]; then
  ok "no stable tag could not run (exit 2, HARNESS ERROR)"
else
  bad "no stable tag: exit $rc, want 2 with HARNESS ERROR: $out"
fi

g tag v1.0.0
g commit -q --allow-empty -m two
g tag v1.2.3
g tag v1.10.0-rc.1
g tag v1.3.0-rc.1
g tag vnext
SHA="$(g rev-parse HEAD)"
out="$(cd "$TMP/r" && bash "$SCRIPT" 2>&1)"; rc=$?
if [ "$rc" -eq 0 ] && [ "$out" = "1.2.4-main.g${SHA:0:7}" ]; then
  ok "highest stable tag plus one patch, rc tags ignored: $out"
else
  bad "want 1.2.4-main.g${SHA:0:7}, got exit $rc: $out"
fi

g tag v1.10.0 HEAD~1
out="$(cd "$TMP/r" && bash "$SCRIPT" HEAD~1 2>&1)"; rc=$?
if [ "$rc" -eq 0 ] && [ "$out" = "1.10.1-main.g$(g rev-parse --short=7 HEAD~1)" ]; then
  ok "numeric, not lexical, ordering and a named commit: $out"
else
  bad "want 1.10.1-main.g<sha7 of HEAD~1>, got exit $rc: $out"
fi

out="$(cd "$TMP/r" && bash "$SCRIPT" no-such-ref 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && [[ "$out" == *"unknown commit"* ]]; then
  ok "an unknown commit could not run"
else
  bad "unknown commit: exit $rc, want 2: $out"
fi

# Valid semver even for an all-digit short sha with a leading zero, and on the
# release channel the packaging step asks for.
SEMVER='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-main\.g[0-9a-f]{7}$'
for v in "$(cd "$TMP/r" && bash "$SCRIPT")" 0.8.1-main.g0123456; do
  if [[ "$v" =~ $SEMVER ]] && [ "$(bash "$CHANNEL" "$v" 2>/dev/null)" = release ]; then
    ok "$v is valid semver on the release channel"
  else
    bad "$v is not valid semver or not accepted by marketplace-channel.sh"
  fi
done

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
