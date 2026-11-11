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
# `vsce package --target <t>` names each file nightgauge-<t>-<version>.vsix
# and records <t> as TargetPlatform in its extension.vsixmanifest (#3174). For
# the one nightgauge-darwin-arm64-*.vsix and the one nightgauge-darwin-x64-*.vsix
# in VSIX_DIR, requires the manifest to name that target, extracts
# extension/dist/bin/nightgauge and requires that `lipo -archs` prints
# exactly `x86_64 arm64` and that its sha256 equals the universal file's. Both
# darwin bin/ binaries must equal it too.
#
# Exit codes:
#   0  every darwin VSIX binary and darwin bin/ binary is the universal file
#   1  at least one is thin or differs (each is named)
#   2  HARNESS ERROR: a VSIX, binary or tool is missing, or a darwin VSIX is not
#      named the way vsce names it, so nothing was asserted

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

# The one place the vsce file naming lives (#3174).
vsix_name() { echo "nightgauge-$1-<version>.vsix"; }
vsix_glob() { echo "nightgauge-$1-"; }

# platform VSIX: the TargetPlatform the VSIX's manifest records, or empty.
platform() {
  local manifest
  manifest="$(unzip -p "$1" extension.vsixmanifest 2>/dev/null)"
  [[ "$manifest" =~ TargetPlatform=\"([^\"]*)\" ]] && echo "${BASH_REMATCH[1]}"
}

present() {
  local all=("$VSIX_DIR"/*.vsix) f out=""
  for f in "${all[@]}"; do out+=" $(basename "$f")"; done
  echo "${out:- none}"
}

# Rename tripwire: every darwin-targeted VSIX must carry the vsce name, so a
# change in naming fails loudly here instead of selecting nothing.
for vsix in "$VSIX_DIR"/*.vsix; do
  plat="$(platform "$vsix")"
  case "$plat" in
    darwin-*)
      case "$(basename "$vsix")" in
        "$(vsix_glob "$plat")"*.vsix) ;;
        *) harness "$(basename "$vsix") targets $plat but is not named $(vsix_name "$plat")" ;;
      esac ;;
  esac
done

for target in darwin-arm64 darwin-x64; do
  vsixes=("$VSIX_DIR/$(vsix_glob "$target")"*.vsix)
  (( ${#vsixes[@]} == 1 )) ||
    harness "expected one $(vsix_name "$target") in $VSIX_DIR, found ${#vsixes[@]}; present:$(present)"
  vsix="${vsixes[0]}"
  plat="$(platform "$vsix")"
  [[ -n "$plat" ]] || harness "no TargetPlatform in the extension.vsixmanifest of $vsix"
  [[ "$plat" == "$target" ]] ||
    harness "$(basename "$vsix") is named for $target but its manifest targets $plat"
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
