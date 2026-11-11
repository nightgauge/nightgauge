#!/usr/bin/env bash
# Self-test for scripts/verify-darwin-universal.sh (#3161).
#
# Builds a tiny Go program for darwin/amd64 and darwin/arm64, joins them with
# lipo, and lays out fixture VSIXs and bin/ the way the workflows do. Asserts:
# all universal -> 0; a thin VSIX binary -> 1; a thin bin/ binary -> 1; a
# different universal build -> 1; a missing, misnamed, duplicated or
# mislabelled darwin VSIX, a missing universal file or
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

# Versions as main and an RC stamp them; vsce names each file
# nightgauge-<target>-<version>.vsix (#3174).
ARM_VER=0.8.6-main.g4757fb4
X64_VER=0.8.6-rc.3

# vsix DIR TARGET BINARY [NAME [PLATFORM]]: a VSIX whose bundled binary is
# BINARY and whose manifest records PLATFORM (default TARGET), named NAME
# (default the vsce name).
vsix() {
  local stage="$WORK/stage-$RANDOM$RANDOM" ver="$ARM_VER"
  [[ "$2" == darwin-x64 ]] && ver="$X64_VER"
  local name="${4:-nightgauge-$2-$ver.vsix}" plat="${5:-$2}"
  mkdir -p "$stage/extension/dist/bin"
  cp "$3" "$stage/extension/dist/bin/nightgauge"
  printf '<PackageManifest><Metadata><Identity Language="en-US" Id="nightgauge" Version="%s" Publisher="nightgauge" TargetPlatform="%s"/></Metadata></PackageManifest>\n' \
    "$ver" "$plat" > "$stage/extension.vsixmanifest"
  (cd "$stage" && zip -q -r "$1/$name" extension.vsixmanifest extension) ||
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
rm "$root/vsix/nightgauge-darwin-x64-$X64_VER.vsix"
vsix "$root/vsix" darwin-x64 "$WORK/a-amd64"
rc=$(run "$root")
if [[ "$rc" == 1 ]] && grep -q "nightgauge-darwin-x64-$X64_VER.vsix is not the universal binary" "$WORK/out" \
   && grep -q "ok   nightgauge-darwin-arm64-$ARM_VER.vsix" "$WORK/out"; then
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
rm "$root/vsix/nightgauge-darwin-arm64-$ARM_VER.vsix"
vsix "$root/vsix" darwin-arm64 "$WORK/b-universal"
rc=$(run "$root")
if [[ "$rc" == 1 ]] && grep -q "nightgauge-darwin-arm64-$ARM_VER.vsix is a universal binary other than" "$WORK/out"; then
  ok "different universal build -> exit 1"
else
  bad "different universal build -> exit 1 (got $rc)"; cat "$WORK/out" >&2
fi

# 5. A darwin VSIX is missing -> 2.
root=$(fixture novsix)
rm "$root/vsix/nightgauge-darwin-x64-$X64_VER.vsix"
rc=$(run "$root")
if [[ "$rc" == 2 ]] && grep -q 'HARNESS ERROR: expected one nightgauge-darwin-x64-<version>.vsix' "$WORK/out"; then
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

# 8. A darwin VSIX under the old name (version before target) -> 2, naming it.
root=$(fixture oldname)
rm "$root/vsix/nightgauge-darwin-arm64-$ARM_VER.vsix"
vsix "$root/vsix" darwin-arm64 "$WORK/a-universal" nightgauge-1.2.3-darwin-arm64.vsix
rc=$(run "$root")
if [[ "$rc" == 2 ]] && grep -q 'nightgauge-1.2.3-darwin-arm64.vsix targets darwin-arm64 but is not named nightgauge-darwin-arm64-<version>.vsix' "$WORK/out"; then
  ok "renamed darwin VSIX -> exit 2 (HARNESS ERROR)"
else
  bad "renamed darwin VSIX -> exit 2 (got $rc)"; cat "$WORK/out" >&2
fi

# 9. Two darwin-arm64 VSIXs -> 2.
root=$(fixture twoarm)
vsix "$root/vsix" darwin-arm64 "$WORK/a-universal" nightgauge-darwin-arm64-0.8.7.vsix
rc=$(run "$root")
if [[ "$rc" == 2 ]] && grep -q 'expected one nightgauge-darwin-arm64-<version>.vsix .* found 2' "$WORK/out"; then
  ok "two darwin-arm64 VSIXs -> exit 2"
else
  bad "two darwin-arm64 VSIXs -> exit 2 (got $rc)"; cat "$WORK/out" >&2
fi

# 10. Named darwin-arm64, manifest says darwin-x64 -> 2.
root=$(fixture liar)
rm "$root/vsix/nightgauge-darwin-arm64-$ARM_VER.vsix"
vsix "$root/vsix" darwin-arm64 "$WORK/a-universal" "nightgauge-darwin-arm64-$ARM_VER.vsix" darwin-x64
rc=$(run "$root")
if [[ "$rc" == 2 ]] && grep -q "nightgauge-darwin-arm64-$ARM_VER.vsix .*targets darwin-x64" "$WORK/out"; then
  ok "name and manifest disagree -> exit 2"
else
  bad "name and manifest disagree -> exit 2 (got $rc)"; cat "$WORK/out" >&2
fi

echo
echo "verify-darwin-universal: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] || exit 1
