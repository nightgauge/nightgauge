#!/bin/bash
# Capture the machine-local VSCode extension state that guard.sh's step-4
# fallback depends on, redact anything private, and emit JSON on stdout.
#
# Why this exists (#356): step 4 used to resolve the LEXICOGRAPHICALLY FIRST
# `~/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge`
# match. The repair is not "pick the biggest version number" — that inverts on
# downgrades, ties on RC versions, and can be won by a `.vsctmp` orphan — it is
# "use the bundle VSCode RECORDS as installed", i.e.
# `~/.vscode/extensions/extensions.json`. Both artifacts are therefore captured
# from a real machine rather than hand-authored.
#
# Usage:
#   # 1. the on-disk bundle inventory
#   bash scripts/capture-vscode-bundle-layout.sh \
#     > internal/doctor/testdata/vscode-bundles/bundle-layout.json
#
#   # 2. VSCode's own install record, redacted
#   bash scripts/capture-vscode-bundle-layout.sh --extensions-index \
#     > internal/doctor/testdata/vscode-bundles/extensions-index.json
#
# Redaction:
#   - Bundle layout: every occurrence of $HOME in an emitted path becomes the
#     literal placeholder `~`. Nothing else is rewritten — bundle versions,
#     executable bits, mtimes, byte sizes, digests, and `nightgauge version`
#     output are verbatim, because those are the fields under test.
#   - Extensions index: the nightgauge entry is emitted VERBATIM (minus the
#     home path) because it is the record under test and it is our own
#     extension. Every OTHER publisher's entry is replaced with a structurally
#     identical anonymized placeholder — no third-party ids, versions,
#     marketplace GUIDs, or install timestamps reach the repository. Entry
#     ORDER is preserved, so the nightgauge entry keeps its real position in
#     the array (it is neither first nor last on the capturing machine, which
#     is what the parsers must cope with).
set -uo pipefail

EXT_ROOT="$HOME/.vscode/extensions"
EXT_GLOB="$EXT_ROOT/nightgauge.nightgauge-vscode-"
INDEX_FILE="$EXT_ROOT/extensions.json"

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

# capture_extensions_index emits a redacted copy of VSCode's install record.
#
# The splitter walks the raw text tracking string state and brace depth, so it
# does not care whether VSCode wrote the file minified (it does) or pretty.
# Each top-level object is then classified by its relativeLocation.
capture_extensions_index() {
  if [ ! -r "$INDEX_FILE" ]; then
    echo "capture-vscode-bundle-layout: no readable $INDEX_FILE" >&2
    exit 1
  fi
  awk -v home="$HOME" -v prefix="nightgauge.nightgauge-vscode-" '
    function jstr(s, key,   p, r, q) {
      p = index(s, "\"" key "\"")
      if (p == 0) return ""
      r = substr(s, p + length(key) + 2)
      while (length(r) > 0 && (substr(r, 1, 1) == " " || substr(r, 1, 1) == "\t")) r = substr(r, 2)
      if (substr(r, 1, 1) != ":") return ""
      r = substr(r, 2)
      while (length(r) > 0 && (substr(r, 1, 1) == " " || substr(r, 1, 1) == "\t")) r = substr(r, 2)
      if (substr(r, 1, 1) != "\"") return ""
      r = substr(r, 2)
      q = index(r, "\"")
      if (q == 0) return ""
      return substr(r, 1, q - 1)
    }
    { buf = buf $0 }
    END {
      depth = 0; instr = 0; esc = 0; start = 0; n = 0
      for (i = 1; i <= length(buf); i++) {
        c = substr(buf, i, 1)
        if (instr) {
          if (esc) { esc = 0 }
          else if (c == "\\") { esc = 1 }
          else if (c == "\"") { instr = 0 }
          continue
        }
        if (c == "\"") { instr = 1; continue }
        if (c == "{") { depth++; if (depth == 1) start = i }
        else if (c == "}") { depth--; if (depth == 0) { n++; entries[n] = substr(buf, start, i - start + 1) } }
      }
      printf "["
      other = 0
      for (k = 1; k <= n; k++) {
        e = entries[k]
        rl = jstr(e, "relativeLocation")
        if (k > 1) printf ","
        if (index(rl, prefix) == 1) {
          gsub(home, "~", e)
          printf "%s", e
        } else {
          other++
          id = "publisher" other ".extension" other
          ver = "1." other ".0"
          printf "{\"identifier\":{\"id\":\"%s\"},\"version\":\"%s\",\"location\":{\"$mid\":1,\"path\":\"~/.vscode/extensions/%s-%s\",\"scheme\":\"file\"},\"relativeLocation\":\"%s-%s\",\"metadata\":{\"isApplicationScoped\":false,\"isMachineScoped\":false,\"isBuiltin\":false,\"installedTimestamp\":0,\"pinned\":false,\"source\":\"gallery\"}}", id, ver, id, ver, id, ver
        }
      }
      printf "]\n"
    }
  ' "$INDEX_FILE"
}

if [ "${1:-}" = "--extensions-index" ]; then
  capture_extensions_index
  exit 0
fi

printf '{\n'
printf '  "captured_at": "%s",\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '  "captured_by": "scripts/capture-vscode-bundle-layout.sh",\n'
printf '  "platform": "%s",\n' "$(json_escape "$(uname -sm)")"
printf '  "bash_version": "%s",\n' "$(json_escape "${BASH_VERSION:-unknown}")"
printf '  "glob": "~/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge",\n'
printf '  "extensions_index": "~/.vscode/extensions/extensions.json",\n'

# The recorded install is the SELECTION AUTHORITY (#356), so capture it here
# too: a layout fixture that does not say which bundle VSCode installed cannot
# express the case the resolver is built around.
recorded=""
if [ -r "$INDEX_FILE" ]; then
  recorded="$(awk -v prefix='"relativeLocation"' '
    { buf = buf $0 }
    END {
      p = 1
      while ((q = index(substr(buf, p), prefix)) > 0) {
        p = p + q + length(prefix) - 1
        r = substr(buf, p)
        sub(/^[ \t]*:[ \t]*"/, "", r)
        v = substr(r, 1, index(r, "\"") - 1)
        if (index(v, "nightgauge.nightgauge-vscode-") == 1) print v
      }
    }
  ' "$INDEX_FILE" | head -1)"
fi
printf '  "recorded_relative_location": "%s",\n' "$(json_escape "$recorded")"
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
  mtime=""
  digest=""
  version_output=""
  if [ -f "$binary" ]; then
    exists=true
    [ -x "$binary" ] && executable=true
    size="$(wc -c < "$binary" | tr -d ' ')"
    # BSD stat (macOS) first, GNU stat second — no `ls` parsing.
    mode="$(stat -f '%Sp' "$binary" 2>/dev/null || stat -c '%A' "$binary" 2>/dev/null)"
    # mtime is captured so the "mtime is not a usable newest signal" claim in
    # the README is checkable from the artifact itself rather than asserted.
    mtime="$(TZ=UTC stat -f '%Sm' -t '%Y-%m-%dT%H:%M:%SZ' "$binary" 2>/dev/null || TZ=UTC stat -c '%y' "$binary" 2>/dev/null)"
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
  printf '      "recorded_installed": %s,\n' "$([ "$base" = "$recorded" ] && echo true || echo false)"
  printf '      "binary": "%s",\n' "$(json_escape "$(redact "$binary")")"
  printf '      "binary_exists": %s,\n' "$exists"
  printf '      "binary_executable": %s,\n' "$executable"
  printf '      "binary_mode": "%s",\n' "$(json_escape "$mode")"
  printf '      "binary_mtime": "%s",\n' "$(json_escape "$mtime")"
  printf '      "binary_size_bytes": %s,\n' "${size:-0}"
  printf '      "binary_sha256": "%s",\n' "$(json_escape "$digest")"
  printf '      "version_output": "%s"\n' "$(json_escape "$version_output")"
  printf '    }'
done

printf '\n  ]\n}\n'
