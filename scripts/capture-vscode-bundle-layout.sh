#!/bin/bash
# Capture the machine-local VSCode extension bundle layout that guard.sh's
# step-4 fallback globs over, redact the operator's home path, and emit JSON
# on stdout.
#
# Why this exists (#356): guard.sh resolved the LEXICOGRAPHICALLY FIRST
# `~/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge`
# match rather than the newest bundle. A machine with two installed bundles
# is the real artifact that exposes that defect, so the regression fixture is
# captured from a real install rather than hand-authored.
#
# Usage:
#   bash scripts/capture-vscode-bundle-layout.sh \
#     > internal/doctor/testdata/vscode-bundles/bundle-layout.json
#
# Redaction: every occurrence of $HOME in an emitted path is replaced with the
# literal placeholder `~`, so no username or home directory leaks into the
# committed fixture. Nothing else is rewritten — bundle versions, executable
# bits, byte sizes, digests, and `nightgauge version` output are reported
# verbatim, because those are exactly the fields under test.
set -uo pipefail

EXT_GLOB="$HOME/.vscode/extensions/nightgauge.nightgauge-vscode-"

redact() { printf '%s' "${1/#$HOME/\~}"; }

json_escape() {
  # Minimal JSON string escaping (backslash, quote, control chars we can hit).
  printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' | tr -d '\n'
}

sha256_of() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" 2>/dev/null | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" 2>/dev/null | awk '{print $1}'
  fi
}

printf '{\n'
printf '  "captured_at": "%s",\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '  "captured_by": "scripts/capture-vscode-bundle-layout.sh",\n'
printf '  "platform": "%s",\n' "$(json_escape "$(uname -sm)")"
printf '  "bash_version": "%s",\n' "$(json_escape "${BASH_VERSION:-unknown}")"
printf '  "glob": "~/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge",\n'
printf '  "bundles": [\n'

first=1
for dir in "$EXT_GLOB"*; do
  [ -d "$dir" ] || continue
  base="$(basename "$dir")"
  version="${base#nightgauge.nightgauge-vscode-}"
  binary="$dir/dist/bin/nightgauge"

  exists=false
  executable=false
  size=0
  mode=""
  digest=""
  version_output=""
  if [ -f "$binary" ]; then
    exists=true
    [ -x "$binary" ] && executable=true
    size="$(wc -c < "$binary" | tr -d ' ')"
    # BSD stat (macOS) first, GNU stat second — no `ls` parsing.
    mode="$(stat -f '%Sp' "$binary" 2>/dev/null || stat -c '%A' "$binary" 2>/dev/null)"
    digest="$(sha256_of "$binary")"
    if [ -x "$binary" ]; then
      version_output="$("$binary" version 2>&1 | head -1)"
    fi
  fi

  [ $first -eq 1 ] || printf ',\n'
  first=0
  printf '    {\n'
  printf '      "dir": "%s",\n' "$(json_escape "$(redact "$dir")")"
  printf '      "bundle_version": "%s",\n' "$(json_escape "$version")"
  printf '      "binary": "%s",\n' "$(json_escape "$(redact "$binary")")"
  printf '      "binary_exists": %s,\n' "$exists"
  printf '      "binary_executable": %s,\n' "$executable"
  printf '      "binary_mode": "%s",\n' "$(json_escape "$mode")"
  printf '      "binary_size_bytes": %s,\n' "${size:-0}"
  printf '      "binary_sha256": "%s",\n' "$(json_escape "$digest")"
  printf '      "version_output": "%s"\n' "$(json_escape "$version_output")"
  printf '    }'
done

printf '\n  ]\n}\n'
