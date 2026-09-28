#!/usr/bin/env bash
#
# Tests scripts/check-boundary-allowlist-isolation.sh (#1970) against a
# throwaway repository: an allowlist-only change passes, an allowlist change
# bundled with content fails, a change without the allowlist passes, and an
# unresolvable base fails closed.
#
# Run: bash scripts/test-check-boundary-allowlist-isolation.sh

set -uo pipefail
REPO="$(git rev-parse --show-toplevel)"
CHECK="$REPO/scripts/check-boundary-allowlist-isolation.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cd "$tmp" || exit 1

g() { git -c user.name=t -c user.email=t@example.com "$@" >/dev/null; }
g init -q -b main
mkdir -p .github docs
echo "allow: []" >.github/publication-boundary.yaml
echo base >docs/a.md
g add -A
g commit -qm base
base="$(git rev-parse HEAD)"

fails=0
expect() { # expect <want-exit> <name> <args...>
  local want="$1" name="$2"
  shift 2
  bash "$CHECK" "$@" >/dev/null 2>&1
  local got=$?
  if [[ "$got" -eq "$want" ]]; then
    echo "ok   - $name"
  else
    echo "FAIL - $name (exit $got, want $want)"
    fails=$((fails + 1))
  fi
}

g checkout -qb content
echo more >>docs/a.md
g commit -qam content
expect 0 "content-only change passes" "$base"

g checkout -qb allowlist-only "$base"
echo "allow: [x]" >.github/publication-boundary.yaml
g commit -qam allow
expect 0 "allowlist-only change passes" "$base"

echo more >>docs/a.md
g commit -qam bundled
expect 1 "allowlist bundled with content fails" "$base"

g checkout -qb deletion "$base"
g rm -q .github/publication-boundary.yaml
echo more >>docs/a.md
g commit -qam delete
expect 1 "allowlist deletion bundled with content fails" "$base"

expect 1 "unresolvable base fails closed" "0000000000000000000000000000000000000000"

if [[ "$fails" -ne 0 ]]; then
  echo "$fails case(s) failed"
  exit 1
fi
echo "all cases passed"
