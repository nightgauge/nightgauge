#!/usr/bin/env bash
# Mutation suite for scripts/check-agent-guidance.sh.
#
# One valid fixture must pass; then every check is proven by a mutation that
# breaks exactly that rule and must produce a non-zero exit AND the check's own
# failure message. A check that cannot go red here is decoration.
#
# Fixtures live in a mktemp directory removed on exit. Drives the working-tree
# copy of the check. Works on bash 3.2 (macOS) and bash 5.
#
# Run: bash scripts/test-agent-guidance-check.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.

# shellcheck disable=SC2016 # fixtures deliberately hold literal backticks and $(...)
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
CHECK="$HERE/check-agent-guidance.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
FIX="$TMP_DIR/repo"
SCRATCH="$TMP_DIR/cwd"
mkdir -p "$SCRATCH"

PASS=0
FAIL=0
OUT=""
RC=0

if command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 | awk '{print $1}'; }
else
  sha256() { sha256sum | awk '{print $1}'; }
fi

BLOCK_BODY='
## Workspace-wide rules

- Shared rule one.
- Shared rule two.
'

# write_agents <heading> — root AGENTS.md with a correctly hashed block.
write_agents() {
  local heading="${1:-# Project Contract}" hash
  hash=$(printf '%s\n' "$BLOCK_BODY" | sha256)
  {
    printf '%s\n\nNested files: pkg/AGENTS.md\n\n' "$heading"
    printf '<!-- nightgauge-workspace-rules:begin v1 -->\n'
    printf '%s\n' "$BLOCK_BODY"
    printf '<!-- nightgauge-workspace-rules:end sha256=%s -->\n' "$hash"
    printf '\n## Local rules\n\n- Run the local gate.\n'
  } >"$FIX/AGENTS.md"
}

valid_fixture() {
  rm -rf "$FIX"
  mkdir -p "$FIX/docs" "$FIX/.github" "$FIX/pkg" "$FIX/.claude/rules"
  write_agents
  printf '@AGENTS.md\n\n## Claude Code\n\nClaude-only behavior.\n' >"$FIX/CLAUDE.md"
  cat >"$FIX/docs/AGENT_GUIDANCE.md" <<'EOF'
# Guidance

## Documentation routing

| Topic | Primary Docs                        | Keywords |
| ----- | ----------------------------------- | -------- |
| Arch  | docs/ARCH.md                        | arch     |
| Git   | [git](GIT.md#branches), `docs/README.md` | git, branch |
| Web   | https://example.invalid/guide       | web      |
EOF
  printf '# Arch\n' >"$FIX/docs/ARCH.md"
  printf '# Git\n' >"$FIX/docs/GIT.md"
  printf '# Docs\n\n- [Guidance](AGENT_GUIDANCE.md#routing)\n' >"$FIX/docs/README.md"
  printf '# Copilot\n\nFollow AGENTS.md.\n' >"$FIX/.github/copilot-instructions.md"
  printf '# Package rules\n\n- Additive rule.\n' >"$FIX/pkg/AGENTS.md"
  printf '@AGENTS.md\n' >"$FIX/pkg/CLAUDE.md"
  printf -- '---\npaths:\n  - "pkg/**"\n---\n\n# Rule\n' >"$FIX/.claude/rules/pkg.md"
}

run_check() {
  OUT=$(cd "$SCRATCH" && bash "$CHECK" --root "$FIX" "$@" 2>&1)
  RC=$?
}

ok() {
  echo "ok    $1"
  PASS=$((PASS + 1))
}

bad() {
  echo "FAIL  $1"
  echo "      exit=$RC output:"
  printf '%s\n' "$OUT" | sed 's/^/        /'
  FAIL=$((FAIL + 1))
}

# expect_pass <name> [check args...]
expect_pass() {
  local name="$1"
  shift
  run_check "$@"
  if [ "$RC" -eq 0 ]; then ok "$name"; else bad "$name (want exit 0)"; fi
}

# expect_fail <name> <message substring> [check args...]
expect_fail() {
  local name="$1" want="$2"
  shift 2
  run_check "$@"
  if [ "$RC" -ne 1 ]; then
    bad "$name (want exit 1)"
    return
  fi
  case "$OUT" in
  *"$want"*) ok "$name" ;;
  *) bad "$name (want message: $want)" ;;
  esac
}

# expect_fail_without <name> <message substring> <forbidden substring> [check args...]
# Like expect_fail, and the output must not contain the forbidden substring.
expect_fail_without() {
  local name="$1" want="$2" unwanted="$3"
  shift 3
  run_check "$@"
  if [ "$RC" -ne 1 ]; then
    bad "$name (want exit 1)"
    return
  fi
  case "$OUT" in
  *"$unwanted"*) bad "$name (output must not contain: $unwanted)" ;;
  *"$want"*) ok "$name" ;;
  *) bad "$name (want message: $want)" ;;
  esac
}

# expect_usage <name> [check args...]
expect_usage() {
  local name="$1"
  shift
  run_check "$@"
  if [ "$RC" -eq 2 ]; then ok "$name"; else bad "$name (want exit 2)"; fi
}

STRICT="--workspace-block required --require-claude yes"

# --- the valid fixture ------------------------------------------------------
valid_fixture
# shellcheck disable=SC2086 # STRICT is a deliberate flag list
expect_pass "valid fixture passes under the strictest flags" $STRICT
valid_fixture
printf '# Docs\n' >"$FIX/docs/README.md"
expect_pass "--docs-index none accepts a routing file that is its own index" --docs-index none
valid_fixture
rm -f "$FIX/CLAUDE.md"
expect_pass "--require-claude auto accepts a repository without CLAUDE.md"

# --- size budgets -----------------------------------------------------------
valid_fixture
expect_fail "AGENTS.md over budget" "exceeds --agents-max-lines 3" --agents-max-lines 3
valid_fixture
expect_fail "CLAUDE.md over budget" "exceeds --claude-max-lines 1" --claude-max-lines 1
valid_fixture
expect_fail "copilot adapter over budget" "exceeds --copilot-max-lines 1" --copilot-max-lines 1
valid_fixture
root_bytes=$(wc -c <"$FIX/AGENTS.md" | tr -d ' ')
expect_fail "nested chain over budget" \
  "instruction chain exceeds --chain-max-bytes $((root_bytes + 1)): pkg/AGENTS.md" \
  --chain-max-bytes $((root_bytes + 1))
valid_fixture
expect_fail "root AGENTS.md alone over the chain budget" \
  "instruction chain exceeds --chain-max-bytes 10: AGENTS.md" --chain-max-bytes 10

# --- root AGENTS.md ---------------------------------------------------------
valid_fixture
rm -f "$FIX/AGENTS.md"
expect_fail "missing AGENTS.md" "AGENTS.md: missing"
valid_fixture
mv "$FIX/AGENTS.md" "$FIX/CONTRACT.md"
ln -s CONTRACT.md "$FIX/AGENTS.md"
expect_fail "AGENTS.md symlink" "AGENTS.md: is a symlink"
valid_fixture
write_agents "# Claude Code Configuration"
expect_fail "AGENTS.md first heading names a tool" "AGENTS.md: first heading names a tool"
valid_fixture
printf '\n@CLAUDE.md\n' >>"$FIX/AGENTS.md"
expect_fail "AGENTS.md imports CLAUDE.md" "AGENTS.md: defers to CLAUDE.md"
valid_fixture
printf '\nRead `CLAUDE.md` first for the real rules.\n' >>"$FIX/AGENTS.md"
expect_fail "AGENTS.md tells agents to read CLAUDE.md first" "AGENTS.md: defers to CLAUDE.md"

# --- CLAUDE.md adapter ------------------------------------------------------
valid_fixture
rm -f "$FIX/CLAUDE.md"
expect_fail "CLAUDE.md required but missing" "CLAUDE.md: missing" --require-claude yes
valid_fixture
rm -f "$FIX/CLAUDE.md"
ln -s AGENTS.md "$FIX/CLAUDE.md"
expect_fail "CLAUDE.md symlink (adapter check)" "CLAUDE.md: is a symlink"
expect_fail "CLAUDE.md symlink (inventory check)" "instruction file is a symlink: CLAUDE.md"
valid_fixture
printf '# Claude\n\n@AGENTS.md\n' >"$FIX/CLAUDE.md"
expect_fail "import on line 3" "CLAUDE.md: line 1 must be exactly @AGENTS.md"
valid_fixture
printf '```text\n@AGENTS.md\n```\n' >"$FIX/CLAUDE.md"
expect_fail "import only inside a fence" "CLAUDE.md: line 1 must be exactly @AGENTS.md"
valid_fixture
printf '\n### documentation map\n' >>"$FIX/CLAUDE.md"
expect_fail "lowercase documentation map heading" "CLAUDE.md: owns a documentation routing heading"
valid_fixture
printf '\n| Doc | Keywords |\n| --- | --- |\n| a.md | a |\n' >>"$FIX/CLAUDE.md"
expect_fail "routing-style table with no heading" "CLAUDE.md: contains a documentation routing table"
valid_fixture
printf '\n```bash\nnpm test\n```\n' >>"$FIX/CLAUDE.md"
expect_fail "fenced shell block" "CLAUDE.md: contains a fenced shell block"
valid_fixture
printf '\nSee @../other/AGENTS.md\n' >>"$FIX/CLAUDE.md"
expect_fail "external @ import" "import resolves outside --root: CLAUDE.md -> @../other/AGENTS.md"
valid_fixture
printf '\n@~/.claude/shared.md\n' >>"$FIX/CLAUDE.md"
expect_fail "home-directory @ import" "import resolves outside --root: CLAUDE.md -> @~/.claude/shared.md"
valid_fixture
printf '\n`@../other/AGENTS.md` is only mentioned.\n' >>"$FIX/CLAUDE.md"
expect_pass "an @ path inside a code span is not an import"

# --- routing document and index ---------------------------------------------
valid_fixture
rm -f "$FIX/docs/AGENT_GUIDANCE.md"
expect_fail "routing file missing" "routing file missing: docs/AGENT_GUIDANCE.md"
valid_fixture
sed 's/^## Documentation routing$/## Topics/' "$FIX/docs/AGENT_GUIDANCE.md" >"$FIX/docs/g.md"
mv "$FIX/docs/g.md" "$FIX/docs/AGENT_GUIDANCE.md"
expect_fail "routing heading missing" "routing file has no documentation routing heading"
valid_fixture
printf '# Guidance\n\n## Documentation Map\n\nNo table here.\n' >"$FIX/docs/AGENT_GUIDANCE.md"
expect_fail "routing table missing" "routing table not found"
valid_fixture
printf '| Lost | docs/MISSING.md | lost |\n' >>"$FIX/docs/AGENT_GUIDANCE.md"
expect_fail "unresolved routing path" "routing path does not resolve: docs/MISSING.md"
valid_fixture
printf '| Lost | [x](NOPE.md#a) | lost |\n' >>"$FIX/docs/AGENT_GUIDANCE.md"
expect_fail "unresolved routing link" "routing path does not resolve: NOPE.md"
valid_fixture
printf '| Out | ../../etc/passwd | out |\n' >>"$FIX/docs/AGENT_GUIDANCE.md"
expect_fail "routing path leaving the root" "routing path does not resolve: ../../etc/passwd (leaves --root)"

# Link text is prose: only the destination is a path, and a whole link
# (text, destination and title) is removed before the cell is tokenised.
# add_row <path-column cell> — routing row plus a resolvable sub/x.md.
add_row() {
  mkdir -p "$FIX/sub"
  printf 'x\n' >"$FIX/sub/x.md"
  printf '| Row | %s | row |\n' "$1" >>"$FIX/docs/AGENT_GUIDANCE.md"
}
valid_fixture
add_row '[sub/thing](../sub/x.md)'
expect_pass "routing link text containing a slash is not a path"
valid_fixture
add_row '[AGENTS.md](../AGENTS.md)'
expect_pass "routing link text containing a dot is not a path"
valid_fixture
add_row '[sub/thing](../sub/missing.md)'
expect_fail_without "unresolved routing link reports its destination, not its text" \
  "routing path does not resolve: ../sub/missing.md" "[sub/thing"
valid_fixture
add_row '`sub/x.md`'
expect_pass "bare backticked routing path resolves from the root"
valid_fixture
add_row '`sub/nope.md`'
expect_fail "unresolved bare backticked routing path" "routing path does not resolve: sub/nope.md"
valid_fixture
add_row '[x](../sub/x.md "Title with a/slash.md")'
expect_pass "routing link with a title resolves its destination"
valid_fixture
add_row '[x](../sub/nope.md "Some title")'
expect_fail_without "unresolved routing link with a title reports the destination only" \
  "routing path does not resolve: ../sub/nope.md" '"Some'
valid_fixture
add_row '[x](<../sub/x.md>)'
expect_pass "routing link with an angle-bracket destination resolves"
valid_fixture
add_row '![a/logo.png](../sub/x.md) [![b/c.md](../sub/x.md)](../sub/x.md), `sub/x.md`'
expect_pass "image and linked-image routing cells parse as whole links"
valid_fixture
add_row '![logo](../sub/nope.png)'
expect_fail_without "unresolved routing image reports its destination" \
  "routing path does not resolve: ../sub/nope.png" "![logo"

valid_fixture
printf '# Docs\n\n- [Guidance](<AGENT_GUIDANCE.md> "Routing")\n' >"$FIX/docs/README.md"
expect_pass "docs index link with angle brackets and a title"
valid_fixture
printf '# Docs\n\n- [AGENT_GUIDANCE.md](ARCH.md)\n' >"$FIX/docs/README.md"
expect_fail "docs index naming the routing file only as link text" \
  "docs index does not link the routing file"
valid_fixture
printf '# Docs\n\n```md\n[Guidance](AGENT_GUIDANCE.md)\n```\n' >"$FIX/docs/README.md"
expect_fail "docs index link only inside a fence" "docs index does not link the routing file"
valid_fixture
printf '# Docs\n\nWrite `[Guidance](AGENT_GUIDANCE.md)` in the index.\n' >"$FIX/docs/README.md"
expect_fail "docs index link only inside a code span" "docs index does not link the routing file"
valid_fixture
printf '# Docs\n\n- [Other](ARCH.md)\n' >"$FIX/docs/README.md"
expect_fail "docs index missing the link" "docs index does not link the routing file"
valid_fixture
rm -f "$FIX/docs/README.md"
expect_fail "docs index missing" "docs index missing: docs/README.md"

# --- nested instruction files -----------------------------------------------
valid_fixture
write_agents
sed 's|Nested files: pkg/AGENTS.md|Nested files: none|' "$FIX/AGENTS.md" >"$FIX/a.tmp"
mv "$FIX/a.tmp" "$FIX/AGENTS.md"
expect_fail "nested AGENTS.md not indexed" "nested AGENTS.md not listed in root AGENTS.md: pkg/AGENTS.md"
# list_nested <text> — replace the fixture's nested-file listing line.
list_nested() {
  sed "s|Nested files: pkg/AGENTS.md|Nested files: $1|" "$FIX/AGENTS.md" >"$FIX/a.tmp"
  mv "$FIX/a.tmp" "$FIX/AGENTS.md"
}
for other in mypkg/AGENTS.md lib/pkg/AGENTS.md pkg/AGENTS.mdx; do
  valid_fixture
  list_nested "$other"
  expect_fail "nested AGENTS.md listed only inside a longer path ($other)" \
    "nested AGENTS.md not listed in root AGENTS.md: pkg/AGENTS.md"
done
valid_fixture
list_nested './pkg/AGENTS.md.'
expect_pass "nested AGENTS.md listed with a ./ prefix"
valid_fixture
list_nested '[pkg rules](pkg/AGENTS.md)'
expect_pass "nested AGENTS.md listed as a link destination"
valid_fixture
rm -f "$FIX/pkg/CLAUDE.md"
expect_fail "nested AGENTS.md without sibling CLAUDE.md" "nested AGENTS.md has no sibling CLAUDE.md: pkg/AGENTS.md"
valid_fixture
printf '# Package\n@AGENTS.md\n' >"$FIX/pkg/CLAUDE.md"
expect_fail "nested CLAUDE.md not starting with the import" \
  "nested CLAUDE.md line 1 must be exactly @AGENTS.md: pkg/CLAUDE.md"
valid_fixture
rm -f "$FIX/.claude/rules/pkg.md"
ln -s ../../docs/ARCH.md "$FIX/.claude/rules/pkg.md"
expect_fail "symlinked scoped rule" "instruction file is a symlink: .claude/rules/pkg.md"

# --- Copilot adapter --------------------------------------------------------
valid_fixture
printf '# Copilot\n\nBe helpful.\n' >"$FIX/.github/copilot-instructions.md"
expect_fail "copilot adapter without AGENTS.md reference" "copilot-instructions: does not reference AGENTS.md"

# --- workspace block --------------------------------------------------------
valid_fixture
sed 's/Shared rule two\./Shared rule 2./' "$FIX/AGENTS.md" >"$FIX/a.tmp"
mv "$FIX/a.tmp" "$FIX/AGENTS.md"
expect_fail "workspace block tampered" "workspace block: hash mismatch"
expect_fail "workspace block tampered is caught even when optional" "workspace block: hash mismatch" \
  --workspace-block optional
valid_fixture
grep -v 'nightgauge-workspace-rules' "$FIX/AGENTS.md" >"$FIX/a.tmp"
mv "$FIX/a.tmp" "$FIX/AGENTS.md"
expect_fail "workspace block missing when required" "workspace block: missing" --workspace-block required
expect_pass "no workspace block is fine when optional" --workspace-block optional
valid_fixture
expect_fail "workspace block present when forbidden" "workspace block: present but --workspace-block forbidden" \
  --workspace-block forbidden
valid_fixture
printf '<!-- nightgauge-workspace-rules:begin v1 -->\n' >>"$FIX/AGENTS.md"
expect_fail "duplicate begin marker" "workspace block: malformed markers" --workspace-block required
valid_fixture
want_hash=$(printf '%s\n' "$BLOCK_BODY" | sha256)
run_check --print-block-hash
if [ "$RC" -eq 0 ] && [ "$OUT" = "$want_hash" ]; then
  ok "--print-block-hash prints the hash of the exact body bytes"
else
  bad "--print-block-hash (want $want_hash)"
fi

# --- every failure is reported, not only the first --------------------------
valid_fixture
printf '# Claude\n@AGENTS.md\n' >"$FIX/CLAUDE.md"
rm -f "$FIX/pkg/CLAUDE.md"
run_check
case "$OUT" in
*"line 1 must be exactly @AGENTS.md"*"no sibling CLAUDE.md"*)
  if [ "$RC" -eq 1 ]; then ok "all failures are reported"; else bad "all failures are reported"; fi
  ;;
*) bad "all failures are reported" ;;
esac

# --- usage errors -----------------------------------------------------------
valid_fixture
expect_usage "unknown flag is a usage error" --bogus
expect_usage "invalid --workspace-block value is a usage error" --workspace-block sometimes
expect_usage "invalid --require-claude value is a usage error" --require-claude maybe
expect_usage "non-integer budget is a usage error" --agents-max-lines many
expect_usage "routing path outside the root is a usage error" --routing ../x.md
expect_usage "missing flag value is a usage error" --root
OUT=$(cd "$SCRATCH" && bash "$CHECK" --root "$TMP_DIR/does-not-exist" 2>&1)
RC=$?
if [ "$RC" -eq 2 ]; then ok "a missing --root is a usage error"; else bad "a missing --root is a usage error"; fi

# --- environment fallbacks --------------------------------------------------
valid_fixture
OUT=$(cd "$SCRATCH" && AGENT_GUIDANCE_ROOT="$FIX" AGENT_GUIDANCE_AGENTS_MAX_LINES=3 bash "$CHECK" 2>&1)
RC=$?
if [ "$RC" -eq 1 ] && case "$OUT" in *"exceeds --agents-max-lines 3"*) true ;; *) false ;; esac; then
  ok "environment fallbacks are honored"
else
  bad "environment fallbacks are honored"
fi

# --- file content and names are data ----------------------------------------
# A directory name with a space and a command substitution, a body containing
# one, and an import naming one. Nothing may execute: no file named pwned may
# appear anywhere, and the odd path must be reported verbatim.
valid_fixture
odd='has space/$(touch pwned)'
mkdir -p "$FIX/$odd"
printf '# Odd\n\n$(touch pwned) `$(touch pwned)`\n' >"$FIX/$odd/AGENTS.md"
printf '@AGENTS.md\n\n@$(touch pwned).md\n' >"$FIX/$odd/CLAUDE.md"
printf '\n| Odd | $(touch pwned) | odd |\n' >>"$FIX/docs/AGENT_GUIDANCE.md"
expect_fail "hostile names are reported as data" \
  "nested AGENTS.md not listed in root AGENTS.md: $odd/AGENTS.md"
printf '\nAlso: %s/AGENTS.md\n' "$odd" >>"$FIX/AGENTS.md"
sed '/| Odd |/d' "$FIX/docs/AGENT_GUIDANCE.md" >"$FIX/g.tmp"
mv "$FIX/g.tmp" "$FIX/docs/AGENT_GUIDANCE.md"
expect_pass "hostile names pass once listed"
if [ -z "$(find "$TMP_DIR" -name pwned -print)" ] && [ ! -e "$HERE/pwned" ] && [ ! -e "$PWD/pwned" ]; then
  ok "no command embedded in a file name or body was executed"
else
  echo "FAIL  a command embedded in a file name or body was executed"
  FAIL=$((FAIL + 1))
fi

# --- git inventory: tracked plus untracked, and the check is read-only ------
valid_fixture
git -C "$FIX" init -q
git -C "$FIX" add -A
git -C "$FIX" -c user.name=t -c user.email=t@example.invalid commit -qm fixture
expect_pass "git-backed fixture passes"
before=$(git -C "$FIX" status --porcelain)
mkdir -p "$FIX/extra"
printf '# Extra\n' >"$FIX/extra/AGENTS.md"
expect_fail "untracked nested AGENTS.md is still checked" \
  "nested AGENTS.md not listed in root AGENTS.md: extra/AGENTS.md"
rm -rf "$FIX/extra"
run_check
after=$(git -C "$FIX" status --porcelain)
if [ "$before" = "$after" ] && [ -z "$after" ]; then
  ok "the check never writes to the tree"
else
  echo "FAIL  the check wrote to the tree: $after"
  FAIL=$((FAIL + 1))
fi

echo
if [ "$FAIL" -gt 0 ]; then
  echo "$FAIL agent-guidance check test(s) failed, $PASS passed"
  exit 1
fi
echo "all $PASS agent-guidance check tests passed"
