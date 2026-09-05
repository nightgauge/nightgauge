#!/usr/bin/env bash
#
# check-changelog.sh — the changelog ↔ release contract
# (docs/GIT_WORKFLOW.md § Changelog).
#
# Three releases (v0.2.0, v0.2.1, v0.2.2) were cut while the root CHANGELOG.md
# still read "no release has been cut yet" under a lone `## [Unreleased]`
# heading, and nothing mechanical noticed: the release workflow trusted a tag,
# the release checklist trusted a human, and neither looked at the file. This
# script is the observer both were missing. It runs on every PR (lint.yml,
# ci-local.sh) and as a hard gate in release.yml before anything is published.
#
# What it asserts, against the root changelog and the extension's changelog
# (the Marketplace renders the latter, so both must name every release):
#
#   1. Each file has exactly one `## [Unreleased]` heading.
#   2. Every `## [X.Y.Z]` heading carries a date: `## [X.Y.Z] - YYYY-MM-DD`.
#   3. Every released tag (`vX.Y.Z`, no pre-release suffix) has a matching
#      `## [X.Y.Z]` section in BOTH files. Sections may exist without a tag —
#      the rollover PR lands before the tag is pushed — but never the reverse.
#   4. Every version the extension changelog names, the root names too.
#   5. No heading is a bare issue number (`#### #1234`). That is the shape an
#      agent produces when it writes its review log into the changelog instead
#      of an entry a reader can act on; it is rejected by shape because the
#      prose it heads is never a changelog entry.
#
# Modes:
#   check-changelog.sh                 arms 1–5 over the tags in this clone
#   check-changelog.sh --tag vX.Y.Z    arms 1–5, then require that vX.Y.Z has a
#                                      non-empty section in both files (the
#                                      release gate: exits 1 before publishing)
#   check-changelog.sh --extract vX.Y.Z
#                                      print the root section body for vX.Y.Z
#                                      on stdout (the GitHub Release notes)
#   check-changelog.sh --extract Unreleased
#                                      the same for the [Unreleased] section
#                                      (store-notes previews, dry runs)
#
# Options:
#   --root <path>        root changelog (default CHANGELOG.md)
#   --extension <path>   extension changelog
#                        (default packages/nightgauge-vscode/CHANGELOG.md);
#                        `--extension none` for a repository with one changelog
#                        (the platform, the dashboard, the Flutter app all run
#                        this same script that way)
#   --tags-from <cmd>    command printing one tag per line (default
#                        `git tag -l 'v*'`); the self-test uses it, and so can
#                        a shallow CI clone after `git fetch --tags`.
#
# Exit codes: 0 contract holds; 1 a violation was found (each named on stderr);
# 2 usage or unreadable file.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 2

ROOT="CHANGELOG.md"
EXT="packages/nightgauge-vscode/CHANGELOG.md"
TAGS_CMD="git tag -l v*"
GATE_TAG=""
EXTRACT_TAG=""

usage() {
  sed -n '2,/^set -uo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --root) ROOT="$2"; shift 2 ;;
    --extension) EXT="$2"; shift 2 ;;
    --tags-from) TAGS_CMD="$2"; shift 2 ;;
    --tag) GATE_TAG="$2"; shift 2 ;;
    --extract) EXTRACT_TAG="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "check-changelog: unknown argument: $1" >&2; usage ;;
  esac
done

# FILES is every changelog under contract: the root always, the extension
# unless the caller said `none`. Every per-file arm iterates FILES.
#
# `--extract` reads the ROOT only, by definition — it is how a single-changelog
# repository's release workflow gets its notes — so it must not require the
# extension file to exist. A single-changelog repository's first tag failed at
# its own release gate because the extract call omitted `--extension none` and
# this check, applied unconditionally, looked for the VS Code changelog in a
# repository that has none.
FILES=("$ROOT")
if [ -n "$EXTRACT_TAG" ]; then
  EXT="none"
elif [ "$EXT" != "none" ]; then
  FILES+=("$EXT")
fi
for f in "${FILES[@]}"; do
  if [ ! -r "$f" ]; then
    echo "check-changelog: cannot read $f" >&2
    exit 2
  fi
done

FAIL=0
violation() { # violation <message>
  echo "VIOLATION: $1" >&2
  FAIL=1
}

# Section headings, one per line: `## [Unreleased]` or `## [X.Y.Z] - YYYY-MM-DD`.
headings() { grep -E '^## \[' "$1"; }
# The versions a file names, one per line, without brackets or dates.
versions() { headings "$1" | sed -nE 's/^## \[([0-9]+\.[0-9]+\.[0-9]+)\].*/\1/p'; }
# section_body <file> <X.Y.Z|Unreleased>: the lines between that heading and the
# next `## ` heading or the first reference-link line (`[x]: url`), with leading
# and trailing blank lines removed. awk only — BSD and GNU sed disagree on the
# multi-line idioms. A version heading carries a date (`## [X.Y.Z] - ...`);
# the Unreleased heading is the bare `## [Unreleased]`.
section_body() {
  # Match by exact prefix, not regex: `awk -v` re-processes backslash escapes,
  # so a regex with `\[` in it does not survive the trip.
  local hdr
  if [ "$2" = "Unreleased" ]; then hdr='## [Unreleased]'; else hdr="## [$2] "; fi
  awk -v hdr="$hdr" -v exact="$([ "$2" = "Unreleased" ] && echo 1 || echo 0)" '
    found && (/^## / || /^\[[^]]+\]: /) { exit }
    found { buf[n++] = $0 }
    (exact == 1 && $0 == hdr) || (exact == 0 && index($0, hdr) == 1) { found = 1 }
    END {
      s = 0; e = n - 1
      while (s <= e && buf[s] ~ /^[ \t]*$/) s++
      while (e >= s && buf[e] ~ /^[ \t]*$/) e--
      for (i = s; i <= e; i++) print buf[i]
    }
  ' "$1"
}

# --extract prints the section body and exits; it does not run the arms, so the
# release workflow can read notes for a tag the arms already admitted.
if [ -n "$EXTRACT_TAG" ]; then
  v="${EXTRACT_TAG#v}"
  if [ "$v" = "Unreleased" ]; then
    if ! grep -qE '^## \[Unreleased\]$' "$ROOT"; then
      echo "check-changelog: $ROOT has no [Unreleased] section" >&2
      exit 1
    fi
  elif ! headings "$ROOT" | grep -qE "^## \[$v\] "; then
    echo "check-changelog: $ROOT has no section for $EXTRACT_TAG" >&2
    exit 1
  fi
  section_body "$ROOT" "$v"
  exit 0
fi

# --- Arm 1: exactly one [Unreleased] per file -------------------------------
for f in "${FILES[@]}"; do
  n=$(grep -cE '^## \[Unreleased\]$' "$f")
  if [ "$n" -ne 1 ]; then
    violation "$f has $n '## [Unreleased]' headings; exactly one is required"
  fi
done

# --- Arm 2: every version heading is dated ---------------------------------
for f in "${FILES[@]}"; do
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    case "$line" in
      "## [Unreleased]") ;;
      *)
        if ! printf '%s\n' "$line" | grep -qE '^## \[[0-9]+\.[0-9]+\.[0-9]+\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$'; then
          violation "$f: heading '$line' is not '## [X.Y.Z] - YYYY-MM-DD'"
        fi
        ;;
    esac
  done < <(headings "$f")
done

# --- Arm 3: every released tag has a section in both files ------------------
ROOT_VERSIONS="$(versions "$ROOT")"
EXT_VERSIONS=""
[ "$EXT" = "none" ] || EXT_VERSIONS="$(versions "$EXT")"
has_version() { printf '%s\n' "$1" | grep -qxF "$2"; }

while IFS= read -r tag; do
  [ -n "$tag" ] || continue
  case "$tag" in
    v*-*) continue ;;                       # rc/beta/alpha never publish
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *) continue ;;
  esac
  v="${tag#v}"
  has_version "$ROOT_VERSIONS" "$v" || violation "tag $tag was released but $ROOT has no '## [$v]' section"
  if [ "$EXT" != "none" ]; then
    has_version "$EXT_VERSIONS" "$v" || violation "tag $tag was released but $EXT has no '## [$v]' section"
  fi
done < <(eval "$TAGS_CMD" 2>/dev/null)

# --- Arm 4: the extension never names a version the root does not -----------
while IFS= read -r v; do
  [ -n "$v" ] || continue
  has_version "$ROOT_VERSIONS" "$v" || violation "$EXT names [$v] but $ROOT does not"
done <<<"$EXT_VERSIONS"

# --- Arm 5: no bare-issue-number headings -----------------------------------
for f in "${FILES[@]}"; do
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    violation "$f: heading '$line' is a bare issue number, not a changelog entry (write what changed for the reader; cite the number in parentheses)"
  done < <(grep -nE '^#{3,6} +#[0-9]+ *$' "$f" | sed 's/^\([0-9]*\):/line \1: /')
done

# --- Release gate: --tag requires a non-empty section in both files ---------
if [ -n "$GATE_TAG" ]; then
  case "$GATE_TAG" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *) echo "check-changelog: --tag expects vX.Y.Z, got '$GATE_TAG'" >&2; exit 2 ;;
  esac
  v="${GATE_TAG#v}"
  for f in "${FILES[@]}"; do
    if ! headings "$f" | grep -qE "^## \[$v\] "; then
      violation "release gate: $f has no '## [$v]' section for $GATE_TAG — land the rollover PR before pushing the tag"
      continue
    fi
    body=$(section_body "$f" "$v" | grep -cvE '^[[:space:]]*$')
    if [ "$body" -eq 0 ]; then
      violation "release gate: $f's '## [$v]' section is empty"
    fi
  done
fi

if [ "$FAIL" -ne 0 ]; then
  echo "check-changelog: FAILED — see docs/GIT_WORKFLOW.md § Changelog" >&2
  exit 1
fi
echo "check-changelog: OK (${FILES[*]})"
exit 0
