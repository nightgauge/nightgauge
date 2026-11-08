#!/usr/bin/env bash
# Self-test for scripts/verify-archive-binaries.sh (#3064).
#
# Builds fixture tarballs laid out like GoReleaser's dist/ and asserts the
# three outcomes: identical -> 0, one archive differs -> 1, and a missing or
# ambiguous input -> 2 (HARNESS ERROR, nothing asserted).

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT="$SCRIPT_DIR/verify-archive-binaries.sh"
PASS=0
FAIL=0
ok()  { echo "  ok   $1"; PASS=$(( PASS + 1 )); }
bad() { echo "  FAIL $1" >&2; FAIL=$(( FAIL + 1 )); }

WORK="$(mktemp -d)" || { echo "HARNESS ERROR: mktemp failed" >&2; exit 2; }
trap 'rm -rf "$WORK"' EXIT

TARGETS=(darwin_arm64 darwin_amd64 linux_amd64)

# fixture NAME: a fresh dist/ and bin/ whose archives match bin/ exactly.
fixture() {
  local root="$WORK/$1" t os arch
  mkdir -p "$root/dist" "$root/bin" "$root/stage"
  for t in "${TARGETS[@]}"; do
    os="${t%_*}"; arch="${t#*_}"
    printf 'binary %s\n' "$t" > "$root/bin/nightgauge-$os-$arch"
    mkdir -p "$root/stage/$t"
    cp "$root/bin/nightgauge-$os-$arch" "$root/stage/$t/nightgauge"
    printf 'license\n' > "$root/stage/$t/LICENSE"
    tar -czf "$root/dist/nightgauge_1.2.3_${os}_${arch}.tar.gz" \
      -C "$root/stage/$t" nightgauge LICENSE \
      || { echo "HARNESS ERROR: could not build fixture tarball" >&2; exit 2; }
  done
  echo "$root"
}

run() { bash "$SUT" "$1/dist" "$1/bin" > "$WORK/out" 2>&1; echo $?; }

echo "verify-archive-binaries self-test"

# 1. Archives carry the bin/ bytes -> 0.
root=$(fixture same)
rc=$(run "$root")
if [[ "$rc" == 0 ]] && grep -q 'All archive binaries match' "$WORK/out"; then
  ok "identical binaries -> exit 0"
else
  bad "identical binaries -> exit 0 (got $rc)"; cat "$WORK/out" >&2
fi

# 2. One darwin bin/ binary was re-signed after packaging -> 1, naming it.
root=$(fixture differ)
printf 'signed\n' >> "$root/bin/nightgauge-darwin-amd64"
rc=$(run "$root")
if [[ "$rc" == 1 ]] && grep -q 'darwin_amd64.tar.gz carries a binary other than' "$WORK/out" \
   && grep -q 'ok   nightgauge_1.2.3_darwin_arm64.tar.gz' "$WORK/out"; then
  ok "one archive differs -> exit 1, naming that archive"
else
  bad "one archive differs -> exit 1 (got $rc)"; cat "$WORK/out" >&2
fi

# 3. A missing archive -> 2.
root=$(fixture noarchive)
rm "$root/dist/nightgauge_1.2.3_linux_amd64.tar.gz"
rc=$(run "$root")
if [[ "$rc" == 2 ]] && grep -q 'HARNESS ERROR: expected one nightgauge_\*_linux_amd64' "$WORK/out"; then
  ok "missing archive -> exit 2 (HARNESS ERROR)"
else
  bad "missing archive -> exit 2 (got $rc)"; cat "$WORK/out" >&2
fi

# 4. Two archives for one target are ambiguous -> 2.
root=$(fixture twoarchives)
cp "$root/dist/nightgauge_1.2.3_darwin_arm64.tar.gz" "$root/dist/nightgauge_9.9.9_darwin_arm64.tar.gz"
rc=$(run "$root")
if [[ "$rc" == 2 ]] && grep -q 'found 2' "$WORK/out"; then
  ok "ambiguous archives -> exit 2 (HARNESS ERROR)"
else
  bad "ambiguous archives -> exit 2 (got $rc)"; cat "$WORK/out" >&2
fi

# 5. A missing bin/ binary -> 2.
root=$(fixture nobin)
rm "$root/bin/nightgauge-darwin-arm64"
rc=$(run "$root")
if [[ "$rc" == 2 ]] && grep -q 'HARNESS ERROR: missing .*nightgauge-darwin-arm64' "$WORK/out"; then
  ok "missing bin/ binary -> exit 2 (HARNESS ERROR)"
else
  bad "missing bin/ binary -> exit 2 (got $rc)"; cat "$WORK/out" >&2
fi

# 6. An archive without a nightgauge binary -> 2.
root=$(fixture empty)
tar -czf "$root/dist/nightgauge_1.2.3_darwin_arm64.tar.gz" -C "$root/stage/darwin_arm64" LICENSE
rc=$(run "$root")
if [[ "$rc" == 2 ]] && grep -q "no 'nightgauge' binary inside" "$WORK/out"; then
  ok "archive without the binary -> exit 2 (HARNESS ERROR)"
else
  bad "archive without the binary -> exit 2 (got $rc)"; cat "$WORK/out" >&2
fi

# 7. Wrong argument count -> 2.
rc=$( bash "$SUT" "$WORK" > "$WORK/out" 2>&1; echo $?)
if [[ "$rc" == 2 ]] && grep -q 'usage' "$WORK/out"; then
  ok "wrong arguments -> exit 2"
else
  bad "wrong arguments -> exit 2 (got $rc)"; cat "$WORK/out" >&2
fi

echo
echo "verify-archive-binaries: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] || exit 1
