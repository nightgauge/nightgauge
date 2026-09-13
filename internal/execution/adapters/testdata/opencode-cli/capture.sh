#!/usr/bin/env bash
# Re-captures the OpenCode CLI help that opencode_test.go reads, redacted.
#
#   bash internal/execution/adapters/testdata/opencode-cli/capture.sh
#
# Writes run-help.txt (verbatim `opencode run --help`, redacted as below) and
# version.txt (`opencode --version`). Update README.md's provenance table in the
# same change, and re-run `go test ./internal/execution/adapters/`: the test
# fails when a flag the adapter emits is missing from the new capture.
#
# Redaction, and why each step exists:
#   - ANSI colour codes are stripped, so a capture from a terminal and one from
#     a pipe are byte-identical.
#   - The capturing user's home directory is replaced with `~`, in case a
#     future help text prints a resolved default path.
#   - Trailing whitespace is trimmed and a final newline is ensured (the CLI
#     prints none), matching .editorconfig so editors leave the file alone.
#   - The capture is refused if it names any IPv4 address other than loopback:
#     nothing machine-specific belongs in a committed fixture.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if ! command -v opencode >/dev/null 2>&1; then
  echo "capture.sh: opencode is not on PATH" >&2
  exit 1
fi

# macOS ships no timeout(1); perl's alarm bounds each call the same way on
# every platform. stdin is /dev/null so nothing can block on a prompt.
bounded() {
  perl -e 'alarm 30; exec @ARGV' -- "$@" </dev/null
}

redact() {
  sed -e $'s/\x1b\\[[0-9;]*[A-Za-z]//g' -e "s#${HOME}#~#g" -e 's/[[:space:]]*$//'
}

bounded opencode --version 2>&1 | redact >"$here/version.txt"
bounded opencode run --help 2>&1 | redact >"$here/run-help.txt"

for f in "$here/version.txt" "$here/run-help.txt"; do
  if [ -n "$(tail -c 1 "$f")" ]; then
    printf '\n' >>"$f"
  fi
  if grep -nE '([0-9]{1,3}\.){3}[0-9]{1,3}' "$f" | grep -vE '127\.0\.0\.1'; then
    echo "capture.sh: $f names a non-loopback address; refusing to write a fixture" >&2
    exit 1
  fi
done

printf 'captured opencode %s: %s\n' "$(cat "$here/version.txt")" "$here/run-help.txt"
