#!/usr/bin/env bash
#
# install-npm-cli.sh — install a pinned npm CLI and prove that it runs.
#
# Usage: scripts/install-npm-cli.sh [--global] [--prefix DIR] <package>@<version> <binary>
#
#   --global               npm install --global; the binary is found on PATH.
#   --global --prefix DIR  global layout under DIR; the binary is DIR/bin/<binary>.
#   --prefix DIR           local layout (--no-save --no-package-lock); the
#                          binary is DIR/node_modules/.bin/<binary>.
#
# npm 11 (ubuntu-26.04's image) skips package install scripts unless they are
# allowed, and CLIs such as @anthropic-ai/claude-code and opencode-ai set up
# their native binary in one. Without it the binary exits "native binary not
# installed" (#3093). So this passes --allow-scripts=<package>; an npm that
# does not know the flag warns and runs scripts by default. If the binary
# still does not run, the package's own preinstall, install and postinstall
# scripts, read from its package.json, are run in npm's order. The binary must
# then exit 0 from --version with output naming <version>.
#
# This script resolves no pin. Callers pass scripts/adapter-cli-pin.sh output
# or a version they validated, so the manifest stays the only source of a pin.
#
# On success prints the binary's absolute path on stdout and exits 0. On any
# failure prints a named ::error line and the CLI's or npm's own output on
# stderr, and exits 1. Bad usage exits 2.
set -euo pipefail

usage() {
  [ -z "${1:-}" ] || echo "install-npm-cli: $1" >&2
  echo "usage: install-npm-cli.sh [--global] [--prefix DIR] <package>@<version> <binary>" >&2
  exit 2
}

global=false
prefix=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --global) global=true; shift ;;
    --prefix)
      [ "$#" -ge 2 ] && [ -n "$2" ] || usage "--prefix needs a directory"
      prefix="$2"
      shift 2
      ;;
    --) shift; break ;;
    -*) usage "unknown option $1" ;;
    *) break ;;
  esac
done
[ "$#" -eq 2 ] || usage
spec="$1"
binary="$2"
pkg="${spec%@*}"
version="${spec##*@}"
# Each becomes an argv element, and the version a regular expression below.
[ "$pkg" != "$spec" ] || usage "'$spec' is not <package>@<version>"
[[ "$pkg" =~ ^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$ ]] ||
  usage "'$pkg' is not an npm package name"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] ||
  usage "'$version' is not a semver"
[[ "$binary" =~ ^[a-z0-9][a-z0-9._-]*$ ]] || usage "'$binary' is not a binary name"
$global || [ -n "$prefix" ] || usage "a local install needs --prefix DIR"

fail() {
  echo "::error title=npm CLI not installed::$pkg: $1" >&2
  [ -z "${2:-}" ] || printf '%s\n' "$2" | tail -n 40 >&2
  exit 1
}

args=(install)
$global && args+=(--global)
[ -z "$prefix" ] || args+=(--prefix "$prefix")
$global || args+=(--no-save --no-package-lock)
args+=(--no-fund --no-audit --loglevel=error "--allow-scripts=$pkg" "$spec")
out="$(npm "${args[@]}" 2>&1)" || fail "npm install of $spec failed" "$out"

if $global && [ -z "$prefix" ]; then
  bin="$binary"
  bin_dir=""
elif $global; then
  bin="$prefix/bin/$binary"
  bin_dir="$prefix/bin"
else
  bin="$prefix/node_modules/.bin/$binary"
  bin_dir="$prefix/node_modules/.bin"
fi

# An npm that ignored the flag and skipped the scripts still left the package
# on disk: run the scripts it would have run, from the package directory.
if ! first_try="$("$bin" --version </dev/null 2>&1)"; then
  if $global; then
    root_args=(root --global)
    [ -z "$prefix" ] || root_args+=(--prefix "$prefix")
    root="$(npm "${root_args[@]}" 2>/dev/null)" ||
      fail "npm root failed, so the installed $pkg cannot be found; $bin does not run" "$first_try"
    [ -n "$root" ] && [ -d "$root" ] ||
      fail "npm root failed: it printed '$root', not a directory; $bin does not run" "$first_try"
  else
    root="$prefix/node_modules"
  fi
  pkg_dir="$root/$pkg"
  [ -f "$pkg_dir/package.json" ] ||
    fail "$bin does not run and $pkg_dir/package.json is missing" "$first_try"
  command -v node >/dev/null || fail "$bin does not run, and node is not on PATH to repair it" "$first_try"
  ran=0
  for stage in preinstall install postinstall; do
    cmd="$(node -e '
      const s = (JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")).scripts || {})[process.argv[2]];
      if (typeof s === "string") process.stdout.write(s);
    ' "$pkg_dir/package.json" "$stage")" || fail "cannot read the scripts in $pkg_dir/package.json"
    [ -n "$cmd" ] || continue
    ran=1
    out="$(cd "$pkg_dir" && PATH="${bin_dir:+$bin_dir:}$PATH" sh -c "$cmd" </dev/null 2>&1)" ||
      fail "the $stage script '$cmd' failed; $bin does not run" "$first_try"$'\n'"$out"
  done
  [ "$ran" -eq 1 ] || fail "$binary does not run and $pkg defines no install script" "$first_try"
fi

got="$("$bin" --version </dev/null 2>&1)" || fail "$bin --version failed after installing $spec" "$got"
# The pin as a whole token, so "v1.2.3" and "codex-cli 1.2.3" match and 11.2.30 does not.
grep -Eq "(^|[^0-9.])${version//./\\.}([^0-9A-Za-z.-]|$)" <<<"$got" ||
  fail "$bin --version does not report $version" "$got"

if [ -z "$bin_dir" ]; then command -v "$binary"; else printf '%s\n' "$bin"; fi
