#!/usr/bin/env bash
# Prove both darwin VSIXs, and both darwin bin/ binaries, carry the one signed
# universal (x86_64 + arm64) Mach-O (#3161).
#
# Usage:
#   verify-darwin-universal.sh VSIX_DIR BIN_DIR
#
# A signed thin darwin/amd64 build was flagged by Microsoft's model
# (Trojan:Script/Wacatac.B!ml) at some version strings. The signed universal
# file was clean at all five measured (docs/RELEASE_BINARIES.md), so the release
# ships it for both darwin targets. The workflows sign
# BIN_DIR/nightgauge-darwin-universal once and copy it over
# BIN_DIR/nightgauge-darwin-{amd64,arm64}, which the VSIX loop and the
# .goreleaser.yml copy hook then package. This check fails when any of those
# copies is a thin binary or a different build.
#
# For the one *-darwin-arm64.vsix and the one *-darwin-x64.vsix in VSIX_DIR,
# extracts extension/dist/bin/nightgauge and requires that `lipo -archs` prints
# exactly `x86_64 arm64` and that its sha256 equals the universal file's. Both
# darwin bin/ binaries must equal it too.
#
# Exit codes:
#   0  every darwin VSIX binary and darwin bin/ binary is the universal file
#   1  at least one is thin or differs (each is named)
#   2  HARNESS ERROR: a VSIX, binary or tool is missing, so nothing was asserted

set -uo pipefail

harness() { echo "HARNESS ERROR: $*" >&2; exit 2; }

[[ $# -eq 2 ]] || harness "usage: verify-darwin-universal.sh VSIX_DIR BIN_DIR"
VSIX_DIR="$1"
BIN_DIR="$2"
[[ -d "$VSIX_DIR" ]] || harness "VSIX directory not found: $VSIX_DIR"
[[ -d "$BIN_DIR" ]] || harness "bin directory not found: $BIN_DIR"
command -v lipo >/dev/null 2>&1 || harness "lipo not found (this check needs macOS)"
command -v unzip >/dev/null 2>&1 || harness "unzip not found"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

UNIVERSAL="$BIN_DIR/nightgauge-darwin-universal"
[[ -f "$UNIVERSAL" ]] || harness "missing $UNIVERSAL (did 'make darwin-universal' run?)"
WANT="$(sha256 "$UNIVERSAL")"
[[ -n "$WANT" ]] || harness "could not hash $UNIVERSAL"

WORK="$(mktemp -d)" || harness "mktemp failed"
trap 'rm -rf "$WORK"' EXIT

BAD=0

# check LABEL FILE: FILE must be universal and byte-identical to $UNIVERSAL.
check() {
  local label="$1" file="$2" archs got
  archs="$(lipo -archs "$file" 2>/dev/null)"
  got="$(sha256 "$file")"
  [[ -n "$got" ]] || harness "could not hash $file"
  if [[ "$archs" != "x86_64 arm64" ]]; then
    echo "::error::$label is not the universal binary: lipo -archs gives '${archs:-not a Mach-O}', want 'x86_64 arm64'"
    BAD=1
  elif [[ "$got" != "$WANT" ]]; then
    echo "::error::$label is a universal binary other than $UNIVERSAL: $got, want $WANT"
    BAD=1
  else
    echo "ok   $label == $(basename "$UNIVERSAL") ($got)"
  fi
}

for arch in amd64 arm64; do
  bin="$BIN_DIR/nightgauge-darwin-$arch"
  [[ -f "$bin" ]] || harness "missing $bin (did 'make build-all' run?)"
  check "$(basename "$bin")" "$bin"
done

shopt -s nullglob
for target in darwin-arm64 darwin-x64; do
  vsixes=("$VSIX_DIR"/*-"$target".vsix)
  (( ${#vsixes[@]} == 1 )) ||
    harness "expected one *-$target.vsix in $VSIX_DIR, found ${#vsixes[@]}"
  vsix="${vsixes[0]}"
  mkdir -p "$WORK/$target"
  unzip -q -o "$vsix" extension/dist/bin/nightgauge -d "$WORK/$target" 2>/dev/null ||
    harness "no extension/dist/bin/nightgauge inside $vsix"
  check "$(basename "$vsix")" "$WORK/$target/extension/dist/bin/nightgauge"
done

if (( BAD )); then
  echo "::error::a darwin VSIX or bin/ binary is not the signed universal file (#3161)"
  exit 1
fi
echo "Both darwin VSIXs and bin/ binaries carry the universal file."
