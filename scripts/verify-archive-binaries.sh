#!/usr/bin/env bash
# Prove each GoReleaser archive carries the exact bin/ binary that the VSIX of
# the same target bundles (#3064).
#
# Usage:
#   verify-archive-binaries.sh DIST_DIR BIN_DIR
#
# For darwin_arm64, darwin_amd64 and linux_amd64, extracts `nightgauge` from
# DIST_DIR/nightgauge_*_<os>_<arch>.tar.gz and compares its sha256 with
# BIN_DIR/nightgauge-<os>-<arch>. release.yml signs bin/ with the Developer ID,
# so a mismatch means an archive shipped a binary other than the signed one.
#
# Exit codes:
#   0  every archive binary is byte-identical to its bin/ binary
#   1  at least one archive binary differs (both digests are printed)
#   2  HARNESS ERROR: an archive or binary is missing, ambiguous or unreadable,
#      so nothing was asserted

set -uo pipefail

harness() { echo "HARNESS ERROR: $*" >&2; exit 2; }

[[ $# -eq 2 ]] || harness "usage: verify-archive-binaries.sh DIST_DIR BIN_DIR"
DIST_DIR="$1"
BIN_DIR="$2"
[[ -d "$DIST_DIR" ]] || harness "dist directory not found: $DIST_DIR"
[[ -d "$BIN_DIR" ]] || harness "bin directory not found: $BIN_DIR"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

WORK="$(mktemp -d)" || harness "mktemp failed"
trap 'rm -rf "$WORK"' EXIT

TARGETS=(darwin_arm64 darwin_amd64 linux_amd64)
MISMATCH=0
shopt -s nullglob
for target in "${TARGETS[@]}"; do
  os="${target%_*}"
  arch="${target#*_}"
  archives=("$DIST_DIR"/nightgauge_*_"${os}_${arch}".tar.gz)
  (( ${#archives[@]} == 1 )) ||
    harness "expected one nightgauge_*_${os}_${arch}.tar.gz in $DIST_DIR, found ${#archives[@]}"
  archive="${archives[0]}"
  bin="$BIN_DIR/nightgauge-${os}-${arch}"
  [[ -f "$bin" ]] || harness "missing $bin (did 'make build-all' run?)"

  mkdir -p "$WORK/$target"
  tar -xzf "$archive" -C "$WORK/$target" nightgauge 2>/dev/null ||
    harness "no 'nightgauge' binary inside $archive"

  got="$(sha256 "$WORK/$target/nightgauge")"
  want="$(sha256 "$bin")"
  [[ -n "$got" && -n "$want" ]] || harness "could not hash $archive or $bin"
  if [[ "$got" == "$want" ]]; then
    echo "ok   $(basename "$archive") == $(basename "$bin") ($got)"
  else
    echo "::error::$(basename "$archive") carries a binary other than $bin: archive $got, bin $want"
    MISMATCH=1
  fi
done

if (( MISMATCH )); then
  echo "::error::release archives do not carry the signed bin/ binaries (#3064)"
  exit 1
fi
echo "All archive binaries match bin/."
