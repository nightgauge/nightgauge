#!/usr/bin/env bash
# Self-test for scripts/verify-darwin-universal.sh (#3161).
#
# Builds a tiny Go program for darwin/amd64 and darwin/arm64, joins them with
# lipo, and lays out fixture VSIXs and bin/ the way the workflows do. Asserts:
# all universal -> 0; a thin VSIX binary -> 1; a thin bin/ binary -> 1; a
# different universal build -> 1; a missing VSIX, a missing universal file or
# wrong arguments -> 2 (HARNESS ERROR, nothing asserted). Without lipo (a
# non-macOS host) nothing can be asserted, so it prints HARNESS ERROR, exit 2.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT="$SCRIPT_DIR/verify-darwin-universal.sh"
PASS=0
FAIL=0
ok()  { echo "  ok   $1"; PASS=$(( PASS + 1 )); }
bad() { echo "  FAIL $1" >&2; FAIL=$(( FAIL + 1 )); }

for tool in lipo go zip unzip; do
  command -v "$tool" >/dev/null 2>&1 ||
    { echo "HARNESS ERROR: $tool not found; this suite needs macOS with Go" >&2; exit 2; }
done

WORK="$(mktemp -d)" || { echo "HARNESS ERROR: mktemp failed" >&2; exit 2; }
trap 'rm -rf "$WORK"' EXIT

# Two tiny programs, each built for both darwin arches and joined: "a" is the
# shipped build, "b" is another universal build with different bytes.
mkdir -p "$WORK/src"
printf 'module fixture\n\ngo 1.21\n' > "$WORK/src/go.mod"
for prog in a b; do
  printf 'package main\n\nfunc main() { println("%s") }\n' "$prog" > "$WORK/src/main.go"
  for arch in amd64 arm64; do
    (cd "$WORK/src" && CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" GOFLAGS='' \
      go build -o "$WORK/$prog-$arch" .) ||
      { echo "HARNESS ERROR: could not build the $prog/$arch fixture" >&2; exit 2; }
  done
  lipo -create "$WORK/$prog-amd64" "$WORK/$prog-arm64" -output "$WORK/$prog-universal" ||
    { echo "HARNESS ERROR: lipo -create failed" >&2; exit 2; }
done

# vsix DIR TARGET BINARY: a VSIX for TARGET whose bundled binary is BINARY.
vsix() {
  local stage="$WORK/stage-$RANDOM$RANDOM"
  mkdir -p "$stage/extension/dist/bin"
  cp "$3" "$stage/extension/dist/bin/nightgauge"
  (cd "$stage" && zip -q -r "$1/nightgauge-1.2.3-$2.vsix" extension) ||
    { echo "HARNESS ERROR: could not build fixture VSIX" >&2; exit 2; }
}

# fixture NAME: bin/ and VSIXs that all carry the universal "a" build.
fixture() {
  local root="$WORK/$1"
  mkdir -p "$root/vsix" "$root/bin"
  cp "$WORK/a-universal" "$root/bin/nightgauge-darwin-universal"
  cp "$WORK/a-universal" "$root/bin/nightgauge-darwin-amd64"
  cp "$WORK/a-universal" "$root/bin/nightgauge-darwin-arm64"
  vsix "$root/vsix" darwin-arm64 "$WORK/a-universal"
  vsix "$root/vsix" darwin-x64 "$WORK/a-universal"
  vsix "$root/vsix" linux-x64 "$WORK/a-amd64"
  echo "$root"
}

run() { bash "$SUT" "$1/vsix" "$1/bin" > "$WORK/out" 2>&1; echo $?; }

echo "verify-darwin-universal self-test"

# 1. Everything is the universal file -> 0. The linux VSIX is not checked.
root=$(fixture same)
rc=$(run "$root")
if [[ "$rc" == 0 ]] && grep -q 'Both darwin VSIXs and bin/ binaries carry the universal file' "$WORK/out"; then
  ok "all universal -> exit 0"
else
  bad "all universal -> exit 0 (got $rc)"; cat "$WORK/out" >&2
fi

# 2. The darwin-x64 VSIX bundles a thin amd64 binary -> 1, naming it.
root=$(fixture thinvsix)
rm "$root/vsix/nightgauge-1.2.3-darwin-x64.vsix"
vsix "$root/vsix" darwin-x64 "$WORK/a-amd64"
rc=$(run "$root")
if [[ "$rc" == 1 ]] && grep -q 'darwin-x64.vsix is not the universal binary' "$WORK/out" \
   && grep -q 'ok   nightgauge-1.2.3-darwin-arm64.vsix' "$WORK/out"; then
  ok "thin VSIX binary -> exit 1, naming that VSIX"
else
  bad "thin VSIX binary -> exit 1 (got $rc)"; cat "$WORK/out" >&2
fi

# 3. bin/nightgauge-darwin-arm64 was left thin (the copy step did not run) -> 1.
root=$(fixture thinbin)
cp "$WORK/a-arm64" "$root/bin/nightgauge-darwin-arm64"
rc=$(run "$root")
if [[ "$rc" == 1 ]] && grep -q 'nightgauge-darwin-arm64 is not the universal binary' "$WORK/out"; then
  ok "thin bin/ binary -> exit 1"
else
  bad "thin bin/ binary -> exit 1 (got $rc)"; cat "$WORK/out" >&2
fi

# 4. The darwin-arm64 VSIX is universal but a different build -> 1.
root=$(fixture otherbuild)
rm "$root/vsix/nightgauge-1.2.3-darwin-arm64.vsix"
vsix "$root/vsix" darwin-arm64 "$WORK/b-universal"
rc=$(run "$root")
if [[ "$rc" == 1 ]] && grep -q 'darwin-arm64.vsix is a universal binary other than' "$WORK/out"; then
  ok "different universal build -> exit 1"
else
  bad "different universal build -> exit 1 (got $rc)"; cat "$WORK/out" >&2
fi

# 5. A darwin VSIX is missing -> 2.
root=$(fixture novsix)
rm "$root/vsix/nightgauge-1.2.3-darwin-x64.vsix"
rc=$(run "$root")
if [[ "$rc" == 2 ]] && grep -q 'HARNESS ERROR: expected one \*-darwin-x64.vsix' "$WORK/out"; then
  ok "missing VSIX -> exit 2 (HARNESS ERROR)"
else
  bad "missing VSIX -> exit 2 (got $rc)"; cat "$WORK/out" >&2
fi

# 6. The universal file was never built -> 2.
root=$(fixture nouniversal)
rm "$root/bin/nightgauge-darwin-universal"
rc=$(run "$root")
if [[ "$rc" == 2 ]] && grep -q 'HARNESS ERROR: missing .*nightgauge-darwin-universal' "$WORK/out"; then
  ok "missing universal file -> exit 2 (HARNESS ERROR)"
else
  bad "missing universal file -> exit 2 (got $rc)"; cat "$WORK/out" >&2
fi

# 7. Wrong argument count -> 2.
rc=$( bash "$SUT" "$WORK" > "$WORK/out" 2>&1; echo $?)
if [[ "$rc" == 2 ]] && grep -q 'usage' "$WORK/out"; then
  ok "wrong arguments -> exit 2"
else
  bad "wrong arguments -> exit 2 (got $rc)"; cat "$WORK/out" >&2
fi

echo
echo "verify-darwin-universal: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] || exit 1
