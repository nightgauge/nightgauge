#!/usr/bin/env bash
# The local gate's gofmt step (#3192): run gofmt from the toolchain go.mod
# selects, and refuse to run any other.
#
# Formatting changes between Go releases. CI's setup-go reads go.mod, so CI
# formats with go.mod's toolchain. `go env GOROOT` names the toolchain
# GOTOOLCHAIN=auto switched to, which fixes an OLDER Go on PATH (auto upgrades
# it) but not a NEWER one (auto never downgrades). So the version is checked
# first: a gofmt that is not go.mod's toolchain can accept what CI rejects.
#
# Usage: scripts/check-gofmt.sh [ROOT]   (ROOT defaults to the repository root)
#
# Exit codes: 0 versions match and every file is formatted; 1 the toolchain is
# not go.mod's, or a file is not formatted; 2 HARNESS ERROR (the check could
# not run, so it asserted nothing).
set -uo pipefail

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
GOMOD="$ROOT/go.mod"

[ -f "$GOMOD" ] || { echo "HARNESS ERROR: $GOMOD does not exist."; exit 2; }
command -v go >/dev/null 2>&1 || { echo "HARNESS ERROR: go is not on PATH."; exit 2; }

# go.mod's `toolchain` directive wins; else `go` + the `go` directive.
WANT="$(awk '$1 == "toolchain" { print $2; exit }' "$GOMOD")"
if [ -z "$WANT" ]; then
  GO_DIRECTIVE="$(awk '$1 == "go" { print $2; exit }' "$GOMOD")"
  [ -n "$GO_DIRECTIVE" ] || { echo "HARNESS ERROR: $GOMOD has no go directive."; exit 2; }
  WANT="go$GO_DIRECTIVE"
fi

HAVE="$(cd "$ROOT" && go env GOVERSION 2>/dev/null)"
GOROOT_DIR="$(cd "$ROOT" && go env GOROOT 2>/dev/null)"
[ -n "$HAVE" ] || { echo "HARNESS ERROR: 'go env GOVERSION' printed nothing."; exit 2; }
[ -n "$GOROOT_DIR" ] || { echo "HARNESS ERROR: 'go env GOROOT' printed nothing."; exit 2; }
GOFMT="$GOROOT_DIR/bin/gofmt"
[ -x "$GOFMT" ] || { echo "HARNESS ERROR: $GOFMT is not executable."; exit 2; }

# A two-part `go 1.27` directive names no patch release: setup-go resolves the
# newest go1.27.x, so any go1.27 patch is go.mod's toolchain.
version_matches() {
  case "$WANT" in
    go[0-9]*.[0-9]*.*) [ "$HAVE" = "$WANT" ] ;;
    *) [ "$HAVE" = "$WANT" ] || case "$HAVE" in "$WANT".*) true ;; *) false ;; esac ;;
  esac
}

if ! version_matches; then
  echo "::error::gofmt is $HAVE's, but go.mod selects $WANT; CI formats with $WANT."
  echo "Install $WANT, or run the gate with GOTOOLCHAIN=$WANT."
  exit 1
fi

UNFORMATTED="$(cd "$ROOT" && "$GOFMT" -l ./internal ./cmd)"
GOFMT_EXIT=$?
[ "$GOFMT_EXIT" -eq 0 ] || { echo "HARNESS ERROR: $GOFMT exited $GOFMT_EXIT."; exit 2; }
if [ -n "$UNFORMATTED" ]; then
  echo "::error::Not formatted by $WANT's gofmt (run '$GOFMT -w' on each):"
  printf '%s\n' "$UNFORMATTED"
  exit 1
fi
echo "gofmt ($HAVE, go.mod selects $WANT): every file formatted."
