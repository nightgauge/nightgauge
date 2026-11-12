#!/usr/bin/env bash
#
# release-margin-builds.sh — build the margin scan inputs for a release
# candidate (#3192).
#
# The Linux amd64 binary sits at the threshold of Microsoft's model, and its
# verdict flips with the version string alone: a clean scan at one string is
# not evidence that the build is clean. So every RC also builds the shipped
# binaries at three neighbouring version strings, and rc-scan scans them with
# the RC's own files. A finding at any of them fails the RC.
#
#   release-margin-builds.sh X.Y.Z-rc.N OUT_DIR
#   release-margin-builds.sh --print-versions X.Y.Z-rc.N
#
# The three strings are X.Y.Z (the release this RC is promoted to),
# X.Y.Z-rc.(N+1) and X.Y.(Z+1)-rc.1. For each one it runs the Makefile's
# `build-release-targets` and `darwin-universal` targets, so the flags are the
# RC's own, signs the universal file with scripts/sign-macos-binaries.sh (a
# loud no-op without the Apple secrets, exactly as for the RC), and copies
#
#   OUT_DIR/nightgauge-margin_<v>_linux_amd64
#   OUT_DIR/nightgauge-margin_<v>_darwin_universal
#
# as raw, executable binaries. They are not archived: the scan gate scans an
# archive and then the binary inside it, so a .tar.gz cost each build two
# uploads (#3202). OUT_DIR/margin-manifest.txt gets one
# `<version> <file> <binary sha256>` line per build; release.yml reads it
# to prove the RC's margin scan covered the release string.
#
# These are scan inputs only. They are uploaded as their own artifact and
# never reach rc-artifacts-* or a GitHub Release. See docs/RELEASE_BINARIES.md.
#
# Environment (for tests): MAKE (default make), MARGIN_SIGN_SCRIPT (default
# scripts/sign-macos-binaries.sh).
#
# Exit:
#   0  six binaries and the manifest were written
#   1  a build, signature or copy failed, or other than six binaries exist
#   2  HARNESS ERROR: bad arguments; nothing was built
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAKE_CMD="${MAKE:-make}"
SIGN_SCRIPT="${MARGIN_SIGN_SCRIPT:-$ROOT/scripts/sign-macos-binaries.sh}"
EXPECTED_BINARIES=6

harness() { echo "HARNESS ERROR: release-margin-builds: $*" >&2; exit 2; }
fail() { echo "::error::release-margin-builds: $*" >&2; exit 1; }

PRINT_ONLY=false
if [ "${1:-}" = "--print-versions" ]; then
  PRINT_ONLY=true
  shift
fi
RC="${1:-}"
OUT="${2:-}"

# margin_versions X.Y.Z-rc.N → "X.Y.Z X.Y.Z-rc.(N+1) X.Y.(Z+1)-rc.1"
margin_versions() {
  local re='^([0-9]+)\.([0-9]+)\.([0-9]+)-rc\.([0-9]+)$'
  [[ "$1" =~ $re ]] || return 1
  local x="${BASH_REMATCH[1]}" y="${BASH_REMATCH[2]}" z="${BASH_REMATCH[3]}" n="${BASH_REMATCH[4]}"
  # 10# keeps a leading zero from being read as octal.
  echo "$x.$y.$z $x.$y.$z-rc.$((10#$n + 1)) $x.$y.$((10#$z + 1))-rc.1"
}

VERSIONS="$(margin_versions "$RC")" ||
  harness "'$RC' is not an RC version X.Y.Z-rc.N (no leading v)"
if [ "$PRINT_ONLY" = true ]; then
  echo "$VERSIONS"
  exit 0
fi
[ -n "$OUT" ] || harness "usage: release-margin-builds.sh X.Y.Z-rc.N OUT_DIR"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# place SRC DEST: the build, byte for byte, executable.
place() {
  [ -f "$1" ] || fail "missing $1; the build did not produce it"
  cp "$1" "$2" || fail "copying $1 to $2 failed"
  chmod +x "$2"
}

mkdir -p "$OUT"
MANIFEST="$OUT/margin-manifest.txt"
: >"$MANIFEST"

for V in $VERSIONS; do
  BIN="$OUT/build/$V"
  echo "== margin build $V =="
  "$MAKE_CMD" -C "$ROOT" build-release-targets VERSION="$V" BIN_DIR="$BIN" ||
    fail "make build-release-targets failed for $V"
  "$MAKE_CMD" -C "$ROOT" darwin-universal BIN_DIR="$BIN" ||
    fail "make darwin-universal failed for $V"
  bash "$SIGN_SCRIPT" "$BIN/nightgauge-darwin-universal" ||
    fail "signing the universal binary failed for $V"
  for PAIR in "linux-amd64:linux_amd64" "darwin-universal:darwin_universal"; do
    SRC="$BIN/nightgauge-${PAIR%%:*}"
    FILE="nightgauge-margin_${V}_${PAIR#*:}"
    place "$SRC" "$OUT/$FILE"
    echo "$V $FILE $(sha256 "$OUT/$FILE")" >>"$MANIFEST"
  done
done

COUNT=0
for f in "$OUT"/nightgauge-margin_*_*; do
  [ -f "$f" ] && COUNT=$((COUNT + 1))
done
[ "$COUNT" -eq "$EXPECTED_BINARIES" ] ||
  fail "expected $EXPECTED_BINARIES margin binaries in $OUT, found $COUNT"
LINES="$(wc -l <"$MANIFEST" | tr -d ' ')"
[ "$LINES" -eq "$EXPECTED_BINARIES" ] ||
  fail "expected $EXPECTED_BINARIES manifest lines, found $LINES"

echo "Margin builds for $RC: $VERSIONS"
cat "$MANIFEST"
