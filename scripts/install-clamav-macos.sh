#!/usr/bin/env bash
#
# install-clamav-macos.sh — install the official, hash-pinned ClamAV package for
# the malware-scan gate on macOS.
#
# WHY NOT HOMEBREW
#
# Homebrew's clamav 1.5.4_1 links OpenSSL 4. On the macos-26-arm64 runner image
# 20261026.0455.x, which carries openssl@4 4.1.0, it rejects every signature
# database with "Incorrect digital signature" (#2740). The same bottle passes on
# images with openssl@4 4.0.3, and the pool mixes both. The official package
# passes on the failing image. The pin also means the scanner cannot change
# underneath the gate: a new version is a reviewed edit here.
# docs/RELEASE_CHECKLIST.md describes how to bump it.
#
# DatabaseDirectory stays unset, so freshclam, clamscan and clamconf all use the
# compiled-in directory, so only that directory's owner has to be decided.
#
# WHO OWNS THE DATABASE DIRECTORY
#
# A CI runner is ephemeral and runs freshclam unprivileged, so with CI=true the
# directory goes to the runner user. On a long-lived machine a user-owned
# directory lets anything running as that user plant or allowlist signatures, so
# it stays root-owned, which also repairs an earlier install that handed it over.
#
# Usage:
#   scripts/install-clamav-macos.sh
#
# Then put /usr/local/clamav/bin on PATH and run freshclam: as yourself with
# CI=true, otherwise as `sudo /usr/local/clamav/bin/freshclam --user=root`.
#
# Exit: 0 installed; non-zero on a failed download, a digest mismatch (nothing
# is installed) or a failed install step.
set -euo pipefail

CLAMAV_VERSION="1.5.4"
PKG_NAME="clamav-${CLAMAV_VERSION}.macos.universal.pkg"
PKG_URL="https://github.com/Cisco-Talos/clamav/releases/download/clamav-${CLAMAV_VERSION}/${PKG_NAME}"
PKG_SHA256="df7fa753e2f9f67f3bc99b2a40a3be7ef559088c68ad6bdf66b4b5764e965bd6"

PREFIX="/usr/local/clamav"
DBDIR="$PREFIX/share/clamav"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
PKG="$WORK/$PKG_NAME"

curl -fsSL --retry 3 -o "$PKG" "$PKG_URL"

ACTUAL="$(shasum -a 256 "$PKG" | awk '{print $1}')"
if [ "$ACTUAL" != "$PKG_SHA256" ]; then
  echo "ERROR: SHA-256 mismatch for $PKG_NAME; refusing to install." >&2
  echo "  expected: $PKG_SHA256" >&2
  echo "  actual:   $ACTUAL" >&2
  exit 1
fi

sudo installer -pkg "$PKG" -target /
sudo mkdir -p "$DBDIR"
if [ "${CI:-}" = "true" ]; then
  sudo chown "$(id -un)" "$DBDIR"
else
  sudo chown -R root:wheel "$DBDIR"
fi
sudo cp "$PREFIX/etc/freshclam.conf.sample" "$PREFIX/etc/freshclam.conf"
sudo sed -i '' 's/^Example/#Example/' "$PREFIX/etc/freshclam.conf"

echo "Installed ClamAV $CLAMAV_VERSION to $PREFIX; database directory: $DBDIR"
if [ "${CI:-}" != "true" ]; then
  echo "Update signatures with: sudo $PREFIX/bin/freshclam --user=root"
fi
