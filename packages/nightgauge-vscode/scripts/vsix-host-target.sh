#!/usr/bin/env bash
# vsix-host-target.sh — print the release VSIX target for a host (#2309).
#
# A release ships one VSIX per target, named
# nightgauge-vscode-<target>-<version>.vsix. Each bundles a CLI built for that
# target, so a VSIX for another target installs cleanly and then cannot run
# its own binary. dev-install.sh --from-release asks this script which one to
# download instead of taking whichever file sorts first.
#
# Usage: vsix-host-target.sh [<uname -s> <uname -m>]
#   With no arguments it reads the running host. Prints the target on stdout
#   and exits 0, or names the host on stderr and exits 1 when no release
#   target exists for it. Keep the table in step with release.yml's VSIX set.
set -euo pipefail

os="${1:-$(uname -s)}"
arch="${2:-$(uname -m)}"

case "$os/$arch" in
  Darwin/arm64 | Darwin/aarch64) echo "darwin-arm64" ;;
  Darwin/x86_64) echo "darwin-x64" ;;
  Linux/x86_64 | Linux/amd64) echo "linux-x64" ;;
  *)
    echo "no release VSIX is built for this host ($os/$arch); release targets are darwin-arm64, darwin-x64 and linux-x64" >&2
    exit 1
    ;;
esac
