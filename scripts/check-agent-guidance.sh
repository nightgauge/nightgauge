#!/usr/bin/env bash
# check-agent-guidance.sh — enforce the portable agent-instruction architecture
# documented in docs/AGENT_GUIDANCE.md (nightgauge/nightgauge).
#
# Portability contract: this file is copied byte-for-byte into other
# repositories and installed into downstream projects. It must contain nothing
# specific to one repository; behavior differences are flags. The canonical
# copy lives in nightgauge/nightgauge — change it there first, then re-copy.
#
# Requirements: bash 3.2+ and POSIX tools (grep, awk, sed, wc, find, git,
# and `shasum -a 256` or `sha256sum`). It never writes to the tree, never
# evals or sources anything, and treats every file name and file body as data.
#
# Usage: check-agent-guidance.sh [flags]
#   --root <dir>                  repository to check (default: git top level
#                                 of the current directory)
#   --routing <path>              routing document (default docs/AGENT_GUIDANCE.md)
#   --docs-index <path|none>      documentation index that must link the
#                                 routing document (default docs/README.md)
#   --agents-max-lines <n>        root AGENTS.md budget (default 200)
#   --claude-max-lines <n>        root CLAUDE.md budget (default 100)
#   --copilot-max-lines <n>       .github/copilot-instructions.md budget (default 30)
#   --chain-max-bytes <n>         root-to-deepest AGENTS.md chain budget (default 32768)
#   --workspace-block required|forbidden|optional   (default optional)
#   --require-claude yes|no|auto  yes: root CLAUDE.md must exist; auto: check it
#                                 when present; no: skip the root adapter checks
#                                 (default auto)
#   --print-block-hash            print the computed workspace-block hash of root
#                                 AGENTS.md and exit
#   -h, --help
#
# Every flag has an environment fallback: AGENT_GUIDANCE_ROOT,
# AGENT_GUIDANCE_ROUTING, AGENT_GUIDANCE_DOCS_INDEX,
# AGENT_GUIDANCE_AGENTS_MAX_LINES, AGENT_GUIDANCE_CLAUDE_MAX_LINES,
# AGENT_GUIDANCE_COPILOT_MAX_LINES, AGENT_GUIDANCE_CHAIN_MAX_BYTES,
# AGENT_GUIDANCE_WORKSPACE_BLOCK, AGENT_GUIDANCE_REQUIRE_CLAUDE.
#
# Exit codes: 0 every check passed; 1 at least one check failed (all failures
# are printed); 2 usage or environment error.
#
# Workspace block: root AGENTS.md may carry exactly one block delimited by
#   <!-- nightgauge-workspace-rules:begin v<N> -->
#   <!-- nightgauge-workspace-rules:end sha256=<64 lowercase hex> -->
# The hash covers the exact bytes of the lines strictly between the two marker
# lines, each line including its trailing newline.
#
# "Defers to CLAUDE.md" is deliberately narrow: an `@CLAUDE.md` import, or a
# sentence of the form "read|see|follow|consult [the] CLAUDE.md first|instead".
# Mentioning CLAUDE.md as the adapter that imports AGENTS.md is fine.

set -euo pipefail

usage() {
  sed -n '14,31p' "$0" | sed 's/^# \{0,1\}//' >&2
}

die_usage() {
  printf 'check-agent-guidance: %s\n' "$1" >&2
  exit 2
}

ROOT="${AGENT_GUIDANCE_ROOT:-}"
ROUTING="${AGENT_GUIDANCE_ROUTING:-docs/AGENT_GUIDANCE.md}"
DOCS_INDEX="${AGENT_GUIDANCE_DOCS_INDEX:-docs/README.md}"
AGENTS_MAX="${AGENT_GUIDANCE_AGENTS_MAX_LINES:-200}"
CLAUDE_MAX="${AGENT_GUIDANCE_CLAUDE_MAX_LINES:-100}"
COPILOT_MAX="${AGENT_GUIDANCE_COPILOT_MAX_LINES:-30}"
CHAIN_MAX="${AGENT_GUIDANCE_CHAIN_MAX_BYTES:-32768}"
BLOCK_MODE="${AGENT_GUIDANCE_WORKSPACE_BLOCK:-optional}"
REQUIRE_CLAUDE="${AGENT_GUIDANCE_REQUIRE_CLAUDE:-auto}"
PRINT_HASH=0

need_value() {
  [ "$#" -ge 2 ] || die_usage "$1 needs a value"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
  --root) need_value "$@"; ROOT="$2"; shift 2 ;;
  --routing) need_value "$@"; ROUTING="$2"; shift 2 ;;
  --docs-index) need_value "$@"; DOCS_INDEX="$2"; shift 2 ;;
  --agents-max-lines) need_value "$@"; AGENTS_MAX="$2"; shift 2 ;;
  --claude-max-lines) need_value "$@"; CLAUDE_MAX="$2"; shift 2 ;;
  --copilot-max-lines) need_value "$@"; COPILOT_MAX="$2"; shift 2 ;;
  --chain-max-bytes) need_value "$@"; CHAIN_MAX="$2"; shift 2 ;;
  --workspace-block) need_value "$@"; BLOCK_MODE="$2"; shift 2 ;;
  --require-claude) need_value "$@"; REQUIRE_CLAUDE="$2"; shift 2 ;;
  --print-block-hash) PRINT_HASH=1; shift ;;
  -h | --help) usage; exit 0 ;;
  *) die_usage "unknown argument: $1 (see --help)" ;;
  esac
done

is_uint() {
  case "$1" in
  '' | *[!0-9]*) return 1 ;;
  *) return 0 ;;
  esac
}

for pair in "agents-max-lines:$AGENTS_MAX" "claude-max-lines:$CLAUDE_MAX" \
  "copilot-max-lines:$COPILOT_MAX" "chain-max-bytes:$CHAIN_MAX"; do
  is_uint "${pair#*:}" || die_usage "--${pair%%:*} must be a non-negative integer"
done
case "$BLOCK_MODE" in
required | forbidden | optional) ;;
*) die_usage "--workspace-block must be required, forbidden or optional" ;;
esac
case "$REQUIRE_CLAUDE" in
yes | no | auto) ;;
*) die_usage "--require-claude must be yes, no or auto" ;;
esac
case "$ROUTING" in
'' | /* | ../* | */../* | ..) die_usage "--routing must be a path inside --root" ;;
esac
case "$DOCS_INDEX" in
none) ;;
'' | /* | ../* | */../* | ..) die_usage "--docs-index must be a path inside --root, or none" ;;
esac

if [ -z "$ROOT" ]; then
  ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" ||
    die_usage "not inside a git work tree; pass --root <dir>"
fi
[ -d "$ROOT" ] || die_usage "--root is not a directory: $ROOT"
ROOT="$(cd "$ROOT" && pwd -P)"

SHA_TOOL=""
if command -v shasum >/dev/null 2>&1; then
  SHA_TOOL="shasum"
elif command -v sha256sum >/dev/null 2>&1; then
  SHA_TOOL="sha256sum"
fi

sha256_stdin() {
  if [ "$SHA_TOOL" = "shasum" ]; then
    shasum -a 256 | awk '{print $1}'
  else
    sha256sum | awk '{print $1}'
  fi
}

BEGIN_RE='^<!-- nightgauge-workspace-rules:begin v[0-9]+ -->$'
END_RE='^<!-- nightgauge-workspace-rules:end sha256=[0-9a-f]{64} -->$'
ANY_MARKER='nightgauge-workspace-rules:'

# Print the lines strictly between the begin and end markers, byte for byte.
block_body() {
  LC_ALL=C awk '
    inb && /^<!-- nightgauge-workspace-rules:end / { exit }
    inb { print }
    /^<!-- nightgauge-workspace-rules:begin / { inb = 1 }
  ' "$1"
}

AGENTS="$ROOT/AGENTS.md"

if [ "$PRINT_HASH" -eq 1 ]; then
  [ -f "$AGENTS" ] || die_usage "no AGENTS.md at $ROOT"
  [ -n "$SHA_TOOL" ] || die_usage "neither shasum nor sha256sum is available"
  grep -Eq "$BEGIN_RE" "$AGENTS" || die_usage "AGENTS.md has no workspace-block begin marker"
  block_body "$AGENTS" | sha256_stdin
  exit 0
fi

TAB=$(printf '\t')
FAILURES=""
FAIL_COUNT=0
fail() {
  FAILURES="${FAILURES}  - $1
"
  FAIL_COUNT=$((FAIL_COUNT + 1))
}

line_count() {
  # Count lines the way an editor does: a final line without a newline counts.
  LC_ALL=C awk 'END { print NR }' "$1"
}

byte_count() {
  wc -c <"$1" | tr -d ' '
}

first_line() {
  LC_ALL=C awk 'NR == 1 { print; exit }' "$1"
}

# Lexically normalize a root-relative path. Prints the normalized path, or
# nothing (exit 1) when it climbs above the root. Never touches the filesystem.
normalize_rel() {
  P="$1" LC_ALL=C awk 'BEGIN {
    n = split(ENVIRON["P"], parts, "/"); k = 0
    for (i = 1; i <= n; i++) {
      if (parts[i] == "" || parts[i] == ".") continue
      if (parts[i] == "..") { if (k == 0) exit 1; k--; continue }
      out[++k] = parts[i]
    }
    s = ""
    for (i = 1; i <= k; i++) s = s (i > 1 ? "/" : "") out[i]
    print s
  }'
}

dir_of() {
  case "$1" in
  */*) printf '%s\n' "${1%/*}" ;;
  *) printf '\n' ;;
  esac
}

join_rel() {
  if [ -z "$1" ]; then printf '%s\n' "$2"; else printf '%s/%s\n' "$1" "$2"; fi
}

# ---------------------------------------------------------------------------
# Inventory of candidate files (tracked plus untracked-but-not-ignored), as a
# newline-separated list. Names containing a newline are refused as data we
# cannot represent safely.
# ---------------------------------------------------------------------------
FILE_LIST=""
add_listed() {
  case "$1" in
  *'
'*) fail "file name contains a newline and cannot be checked safely" ;;
  *) FILE_LIST="${FILE_LIST}$1
" ;;
  esac
}

if git -C "$ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  while IFS= read -r -d '' f; do
    add_listed "$f"
  done < <(git -C "$ROOT" ls-files -z --cached --others --exclude-standard)
else
  while IFS= read -r -d '' f; do
    add_listed "${f#./}"
  done < <(cd "$ROOT" && find . \( -name .git -o -name node_modules \) -prune -o \
    \( -type f -o -type l \) -print0)
fi

is_instruction_path() {
  case "$1" in
  AGENTS.md | */AGENTS.md | CLAUDE.md | */CLAUDE.md) return 0 ;;
  .github/copilot-instructions.md) return 0 ;;
  .github/instructions | .github/instructions/*) return 0 ;;
  .claude/rules | .claude/rules/*) return 0 ;;
  .cursor/rules | .cursor/rules/*) return 0 ;;
  esac
  return 1
}

# ---------------------------------------------------------------------------
# Root AGENTS.md
# ---------------------------------------------------------------------------
TOOL_NAMES='claude|copilot|cursor|codex|kiro|gemini|windsurf'
ROUTING_PHRASES='documentation map|documentation routing|doc map'

if [ -L "$AGENTS" ]; then
  fail "AGENTS.md: is a symlink; it must be a regular file"
elif [ ! -f "$AGENTS" ]; then
  fail "AGENTS.md: missing at the repository root"
else
  n=$(line_count "$AGENTS")
  [ "$n" -le "$AGENTS_MAX" ] ||
    fail "AGENTS.md: $n lines exceeds --agents-max-lines $AGENTS_MAX"

  heading=$(LC_ALL=C awk '
    /^[[:space:]]*(```|~~~)/ { fence = !fence; next }
    !fence && /^#+[[:space:]]/ { print; exit }
  ' "$AGENTS")
  if printf '%s\n' "$heading" | LC_ALL=C grep -Eiq -- "($TOOL_NAMES)"; then
    fail "AGENTS.md: first heading names a tool ($heading); the contract is tool-neutral"
  fi

  # shellcheck disable=SC2016 # the backticks are literal Markdown, not expansions
  if LC_ALL=C grep -Eq -- '(^|[[:space:]])@CLAUDE\.md' "$AGENTS" ||
    LC_ALL=C grep -Eiq -- '(read|see|follow|consult)[[:space:]]+(the[[:space:]]+)?`?CLAUDE\.md`?[[:space:]]+(first|instead)' "$AGENTS"; then
    fail "AGENTS.md: defers to CLAUDE.md; AGENTS.md is the contract and CLAUDE.md imports it"
  fi
fi

# ---------------------------------------------------------------------------
# Root CLAUDE.md adapter
# ---------------------------------------------------------------------------
# check_claude_adapter <root-relative path> <label> <max-lines or empty>
check_claude_adapter() {
  local rel="$1" label="$2" max="$3" path="$ROOT/$1" n
  if [ -L "$path" ]; then
    fail "$label: is a symlink; it must be a regular file"
    return
  fi
  if [ "$(first_line "$path")" != "@AGENTS.md" ]; then
    fail "$label: line 1 must be exactly @AGENTS.md"
  fi
  if [ -n "$max" ]; then
    n=$(line_count "$path")
    [ "$n" -le "$max" ] || fail "$label: $n lines exceeds --claude-max-lines $max"
  fi
  if LC_ALL=C awk -v phrases="$ROUTING_PHRASES" '
    /^[[:space:]]*(```|~~~)/ { fence = !fence; next }
    !fence && /^#+[[:space:]]/ {
      if (tolower($0) ~ phrases) { found = 1; exit }
    }
    END { exit found ? 0 : 1 }
  ' "$path"; then
    fail "$label: owns a documentation routing heading; routing belongs in the routing document"
  fi
  if LC_ALL=C awk '
    /^[[:space:]]*(```|~~~)/ { fence = !fence; prev = ""; next }
    fence { next }
    /^[[:space:]]*\|?[[:space:]]*:?---*:?[[:space:]]*(\|[[:space:]]*:?---*:?[[:space:]]*)*\|?[[:space:]]*$/ {
      h = tolower(prev)
      if (prev ~ /\|/ && (h ~ /doc/ || h ~ /topic/) && (h ~ /keyword/ || h ~ /primary/)) { found = 1; exit }
    }
    { prev = $0 }
    END { exit found ? 0 : 1 }
  ' "$path"; then
    fail "$label: contains a documentation routing table; routing belongs in the routing document"
  fi
  if LC_ALL=C grep -Eiq -- '^[[:space:]]*(```|~~~)[[:space:]]*(bash|sh|shell|zsh)([^[:alnum:]_-]|$)' "$path"; then
    fail "$label: contains a fenced shell block; commands belong in AGENTS.md or docs/"
  fi
}

CLAUDE="$ROOT/CLAUDE.md"
case "$REQUIRE_CLAUDE" in
yes)
  if [ -e "$CLAUDE" ] || [ -L "$CLAUDE" ]; then
    check_claude_adapter "CLAUDE.md" "CLAUDE.md" "$CLAUDE_MAX"
  else
    fail "CLAUDE.md: missing at the repository root (--require-claude yes)"
  fi
  ;;
auto)
  if [ -e "$CLAUDE" ] || [ -L "$CLAUDE" ]; then
    check_claude_adapter "CLAUDE.md" "CLAUDE.md" "$CLAUDE_MAX"
  fi
  ;;
no) ;;
esac

# ---------------------------------------------------------------------------
# Routing document and documentation index
# ---------------------------------------------------------------------------
ROUTING_PATH="$ROOT/$ROUTING"
if [ ! -f "$ROUTING_PATH" ]; then
  fail "routing file missing: $ROUTING"
else
  if ! LC_ALL=C awk -v phrases="$ROUTING_PHRASES" '
    /^[[:space:]]*(```|~~~)/ { fence = !fence; next }
    !fence && /^#+[[:space:]]/ {
      t = tolower($0); sub(/^#+[[:space:]]+/, "", t)
      if (t ~ ("^(" phrases ")")) { found = 1; exit }
    }
    END { exit found ? 0 : 1 }
  ' "$ROUTING_PATH"; then
    fail "routing file has no documentation routing heading: $ROUTING"
  else
    # Emit one line per path in the path column of the first table after the
    # routing heading: "L<TAB>target" for Markdown link targets (relative to
    # the routing file) and "R<TAB>path" for bare paths (relative to the root).
    # A line "NOTABLE" means no table with a path column was found.
    routing_dir=$(dir_of "$ROUTING")
    table_paths=$(LC_ALL=C awk -v phrases="$ROUTING_PHRASES" '
      function trim(s) { gsub(/^[[:space:]]+|[[:space:]]+$/, "", s); return s }
      /^[[:space:]]*(```|~~~)/ { fence = !fence; next }
      fence { next }
      !started && /^#+[[:space:]]/ {
        t = tolower($0); sub(/^#+[[:space:]]+/, "", t)
        if (t ~ ("^(" phrases ")")) { started = 1 }
        next
      }
      !started { next }
      intable && $0 !~ /^[[:space:]]*\|/ { exit }
      started && !intable && /^#+[[:space:]]/ { exit }
      $0 ~ /^[[:space:]]*\|/ {
        line = $0
        sub(/^[[:space:]]*\|/, "", line); sub(/\|[[:space:]]*$/, "", line)
        n = split(line, cells, "|")
        if (!intable) {
          intable = 1; col = 0
          for (i = 1; i <= n; i++) {
            h = tolower(trim(cells[i]))
            if (h ~ /keyword/) continue
            if (h ~ /doc|path|link|primary|file/) { col = i; break }
          }
          if (col == 0) { print "NOTABLE"; exit }
          next
        }
        if (line ~ /^[[:space:]]*:?---*/) next
        rows++
        cell = trim(cells[col])
        while (match(cell, /\]\([^)]*\)/)) {
          tgt = substr(cell, RSTART + 2, RLENGTH - 3)
          print "L\t" tgt
          cell = substr(cell, 1, RSTART - 1) " " substr(cell, RSTART + RLENGTH)
        }
        gsub(/\[[^]]*\]/, " ", cell)
        gsub(/`/, " ", cell)
        gsub(/,/, " ", cell)
        m = split(cell, toks, /[[:space:]]+/)
        for (j = 1; j <= m; j++) if (toks[j] != "") print "R\t" toks[j]
      }
      END { if (!intable || rows == 0) print "NOTABLE" }
    ' "$ROUTING_PATH")
    if printf '%s\n' "$table_paths" | grep -qx 'NOTABLE'; then
      fail "routing table not found under the routing heading in $ROUTING"
    fi
    while IFS= read -r entry; do
      [ -n "$entry" ] || continue
      [ "$entry" != "NOTABLE" ] || continue
      kind="${entry%%"$TAB"*}"
      target="${entry#*"$TAB"}"
      target="${target%%#*}"
      case "$target" in
      '' | http://* | https://* | mailto:*) continue ;;
      esac
      # Bare-word cells that are not paths (no slash, no extension) are prose.
      if [ "$kind" = "R" ]; then
        case "$target" in
        */* | *.*) ;;
        *) continue ;;
        esac
        base=""
      else
        base="$routing_dir"
      fi
      case "$target" in
      /*)
        fail "routing path does not resolve: $target (absolute paths are not portable)"
        continue
        ;;
      esac
      if ! resolved=$(normalize_rel "$(join_rel "$base" "$target")"); then
        fail "routing path does not resolve: $target (leaves --root)"
        continue
      fi
      if [ -z "$resolved" ] || [ ! -e "$ROOT/$resolved" ]; then
        fail "routing path does not resolve: $target"
      fi
    done <<EOF
$table_paths
EOF
  fi
fi

if [ "$DOCS_INDEX" != "none" ]; then
  INDEX_PATH="$ROOT/$DOCS_INDEX"
  if [ ! -f "$INDEX_PATH" ]; then
    fail "docs index missing: $DOCS_INDEX (pass --docs-index none if the routing file is the index)"
  else
    index_dir=$(dir_of "$DOCS_INDEX")
    want=$(normalize_rel "$ROUTING") || want=""
    linked=0
    while IFS= read -r tgt; do
      [ -n "$tgt" ] || continue
      tgt="${tgt%%#*}"
      case "$tgt" in '' | /* | *://*) continue ;; esac
      if got=$(normalize_rel "$(join_rel "$index_dir" "$tgt")") && [ "$got" = "$want" ]; then
        linked=1
        break
      fi
    done <<EOF
$(LC_ALL=C grep -Eo '\]\([^) ]+' "$INDEX_PATH" | sed 's/^](//' || true)
EOF
    [ "$linked" -eq 1 ] || fail "docs index does not link the routing file: $DOCS_INDEX -> $ROUTING"
  fi
fi

# ---------------------------------------------------------------------------
# Inventory-driven checks: symlinks, nested files, chain budget, imports
# ---------------------------------------------------------------------------
root_agents_bytes=0
if [ -f "$AGENTS" ] && [ ! -L "$AGENTS" ]; then
  root_agents_bytes=$(byte_count "$AGENTS")
  [ "$root_agents_bytes" -le "$CHAIN_MAX" ] ||
    fail "instruction chain exceeds --chain-max-bytes $CHAIN_MAX: AGENTS.md ($root_agents_bytes bytes)"
fi

# CLAUDE.md import lines: print every @token outside fences and code spans.
claude_imports() {
  LC_ALL=C awk '
    /^[[:space:]]*(```|~~~)/ { fence = !fence; next }
    fence { next }
    {
      line = $0
      gsub(/`[^`]*`/, " ", line)
      n = split(line, toks, /[[:space:]]+/)
      for (i = 1; i <= n; i++) if (toks[i] ~ /^@[^[:space:]]/) print substr(toks[i], 2)
    }
  ' "$1"
}

while IFS= read -r rel; do
  [ -n "$rel" ] || continue
  is_instruction_path "$rel" || continue
  path="$ROOT/$rel"

  if [ -L "$path" ]; then
    fail "instruction file is a symlink: $rel"
    continue
  fi
  [ -f "$path" ] || continue

  case "$rel" in
  CLAUDE.md | */CLAUDE.md)
    dir=$(dir_of "$rel")
    while IFS= read -r imp; do
      [ -n "$imp" ] || continue
      # Trailing prose punctuation is not part of the path.
      imp=$(printf '%s' "$imp" | sed 's/[),.;:]*$//')
      case "$imp" in
      '~'* | /*)
        fail "import resolves outside --root: $rel -> @$imp"
        continue
        ;;
      esac
      if ! normalize_rel "$(join_rel "$dir" "$imp")" >/dev/null; then
        fail "import resolves outside --root: $rel -> @$imp"
      fi
    done <<EOF
$(claude_imports "$path")
EOF
    ;;
  esac

  case "$rel" in
  */AGENTS.md)
    if ! LC_ALL=C grep -Fq -- "$rel" "$AGENTS" 2>/dev/null; then
      fail "nested AGENTS.md not listed in root AGENTS.md: $rel"
    fi
    dir=$(dir_of "$rel")
    sib="$dir/CLAUDE.md"
    if [ -L "$ROOT/$sib" ]; then
      : # reported by the symlink check on its own inventory entry
    elif [ ! -f "$ROOT/$sib" ]; then
      fail "nested AGENTS.md has no sibling CLAUDE.md: $rel"
    elif [ "$(first_line "$ROOT/$sib")" != "@AGENTS.md" ]; then
      fail "nested CLAUDE.md line 1 must be exactly @AGENTS.md: $sib"
    fi
    total=$root_agents_bytes
    walk=""
    rest="$dir"
    while [ -n "$rest" ]; do
      seg="${rest%%/*}"
      if [ "$seg" = "$rest" ]; then rest=""; else rest="${rest#*/}"; fi
      walk=$(join_rel "$walk" "$seg")
      if [ -f "$ROOT/$walk/AGENTS.md" ] && [ ! -L "$ROOT/$walk/AGENTS.md" ]; then
        total=$((total + $(byte_count "$ROOT/$walk/AGENTS.md")))
      fi
    done
    [ "$total" -le "$CHAIN_MAX" ] ||
      fail "instruction chain exceeds --chain-max-bytes $CHAIN_MAX: $rel ($total bytes from the root)"
    ;;
  esac
done <<EOF
$FILE_LIST
EOF

# ---------------------------------------------------------------------------
# Copilot adapter
# ---------------------------------------------------------------------------
COPILOT="$ROOT/.github/copilot-instructions.md"
if [ -f "$COPILOT" ] && [ ! -L "$COPILOT" ]; then
  n=$(line_count "$COPILOT")
  [ "$n" -le "$COPILOT_MAX" ] ||
    fail "copilot-instructions: $n lines exceeds --copilot-max-lines $COPILOT_MAX"
  LC_ALL=C grep -Fq -- 'AGENTS.md' "$COPILOT" ||
    fail "copilot-instructions: does not reference AGENTS.md"
fi

# ---------------------------------------------------------------------------
# Workspace block
# ---------------------------------------------------------------------------
if [ -f "$AGENTS" ] && [ ! -L "$AGENTS" ]; then
  markers=$(LC_ALL=C grep -Fc -- "$ANY_MARKER" "$AGENTS" || true)
  begins=$(LC_ALL=C grep -Ec -- "$BEGIN_RE" "$AGENTS" || true)
  ends=$(LC_ALL=C grep -Ec -- "$END_RE" "$AGENTS" || true)
  if [ "$BLOCK_MODE" = "forbidden" ]; then
    [ "$markers" -eq 0 ] ||
      fail "workspace block: present but --workspace-block forbidden"
  elif [ "$markers" -eq 0 ]; then
    [ "$BLOCK_MODE" != "required" ] ||
      fail "workspace block: missing from AGENTS.md (--workspace-block required)"
  elif [ "$begins" -ne 1 ] || [ "$ends" -ne 1 ] || [ "$markers" -ne 2 ]; then
    fail "workspace block: malformed markers; need exactly one begin and one end marker line"
  else
    begin_line=$(LC_ALL=C grep -En -- "$BEGIN_RE" "$AGENTS" | cut -d: -f1)
    end_line=$(LC_ALL=C grep -En -- "$END_RE" "$AGENTS" | cut -d: -f1)
    if [ "$begin_line" -ge "$end_line" ]; then
      fail "workspace block: malformed markers; the begin marker must precede the end marker"
    elif [ -z "$SHA_TOOL" ]; then
      die_usage "neither shasum nor sha256sum is available to verify the workspace block"
    else
      recorded=$(LC_ALL=C grep -E -- "$END_RE" "$AGENTS" | sed 's/.*sha256=\([0-9a-f]*\).*/\1/')
      computed=$(block_body "$AGENTS" | sha256_stdin)
      [ "$recorded" = "$computed" ] ||
        fail "workspace block: hash mismatch (recorded $recorded, computed $computed); copy the canonical block unchanged"
    fi
  fi
fi

if [ "$FAIL_COUNT" -gt 0 ]; then
  printf 'agent-guidance check failed (%s):\n' "$FAIL_COUNT" >&2
  printf '%s' "$FAILURES" >&2
  exit 1
fi

echo "agent-guidance check passed"
