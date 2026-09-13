#!/usr/bin/env bash
#
# adapter-cli-pin.sh — print an npm install pin ("<package>@<version>") for
# the CLI a compat manifest names as its own most-recently-tested release.
#
# Usage: scripts/adapter-cli-pin.sh <adapter>
#
# Reads internal/adaptercompat/manifests/<adapter>.json's max_tested field and
# prints `<install.npm>@<max_tested>`. Workflows use it (release-watchdog.yml,
# continuous-improvement.yml) instead of a hand-maintained version literal, so
# bumping the pin is "edit the manifest and regenerate" (#1621) rather than a
# grep-and-replace across every workflow file.
#
# ADAPTERCOMPAT_DIR overrides the manifest directory; tests use it to point at
# a throwaway fixture instead of the real manifests.
#
# max_tested is validated against a strict MAJOR.MINOR.PATCH pattern BEFORE
# anything is printed and before it is interpolated into the output: the
# manifest is repository data a PR review approves, but this script treats it
# as untrusted input anyway, because its output composes into a shell command
# line (`npm install --global ... "$(adapter-cli-pin.sh ...)"`) in the
# workflows that call it.
set -euo pipefail

adapter="${1:?usage: adapter-cli-pin.sh <adapter>}"
dir="${ADAPTERCOMPAT_DIR:-internal/adaptercompat/manifests}"
manifest="${dir}/${adapter}.json"

if [ ! -f "$manifest" ]; then
  echo "adapter-cli-pin: no manifest for adapter '${adapter}' (${manifest})" >&2
  exit 1
fi

max_tested="$(jq -r '.max_tested' "$manifest")"

if ! [[ "$max_tested" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "adapter-cli-pin: max_tested '${max_tested}' in ${manifest} is not a MAJOR.MINOR.PATCH version" >&2
  exit 1
fi

npm_pkg="$(jq -r '.install.npm' "$manifest")"

if [ -z "$npm_pkg" ] || [ "$npm_pkg" = "null" ]; then
  echo "adapter-cli-pin: ${manifest} has no install.npm package" >&2
  exit 1
fi

printf '%s@%s\n' "$npm_pkg" "$max_tested"
