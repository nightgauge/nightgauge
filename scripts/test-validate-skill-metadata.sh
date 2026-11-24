#!/usr/bin/env bash
#
# test-validate-skill-metadata.sh — regression suite for
# scripts/validate-skill-metadata.sh (#856).
#
# The defect: the validator reported
# `ERROR: skills/nightgauge-test-scaffold/SKILL.md — missing required field:
# metadata.source` once, during a gate run, on a file that provably had the
# field, was unmodified since its initial import, and validated clean on five
# consecutive runs of the identical tree. The failure never reproduced.
#
# The mechanism the code allowed: the frontmatter reader accumulates lines until
# it sees the closing `---`, and if that line never arrives it runs to EOF and
# hands the field checks a PREFIX of the real frontmatter. Every field past the
# truncation point then reads as absent. A read that could not be completed and
# a file that is genuinely wrong produced the same message and the same exit
# code — so the only available response was "re-run it", which is the same as
# not having the gate.
#
# The fix is a premise check: the block must have closed. It still fails (a
# SKILL.md whose frontmatter never closes is malformed either way, and a torn
# read means something wrote the file underneath the reader) but under its own
# message and its own exit code, so a red run says WHICH of the two it was.
#
# Cases, all against throwaway fixtures via SKILLS_ROOT — this suite never
# touches skills/:
#   1. A well-formed skill validates clean.
#   2. A genuinely missing metadata.source is still an ERROR, exit 1.
#   3. A TRUNCATED file (frontmatter opened, never closed) is UNREADABLE,
#      exit 2, and is NOT reported as a missing field.
#   4. A truncated file does not silently pass.
#
# Prove it can go red by deleting the premise check: case 3 then reports
# `missing required field` and exits 1 — the reported symptom, reproduced.
#
# Run: bash scripts/test-validate-skill-metadata.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

VALIDATOR="scripts/validate-skill-metadata.sh"
PASS=0
FAIL=0
TMP=""

cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

check() { # check <description> <0-if-ok>
  if [ "$2" = "0" ]; then
    echo "PASS: $1"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $1"
    FAIL=$((FAIL + 1))
  fi
}

TMP="$(mktemp -d)"

# write_skill <root> <dir-name> <body-file-relative-content>
good_frontmatter() { # good_frontmatter <name>
  cat <<EOF
---
name: $1
description: Validates a fixture skill for the metadata validator's own regression suite. Use when exercising scripts/validate-skill-metadata.sh.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read
---

# $1
EOF
}

run_validator() { # run_validator <skills-root> -> writes $TMP/out, returns exit
  SKILLS_ROOT="$1" bash "$VALIDATOR" > "$TMP/out" 2>&1
}

# --- Case 1: a well-formed skill validates clean -----------------------------
ROOT_OK="$TMP/ok"
mkdir -p "$ROOT_OK/nightgauge-fixture-ok"
good_frontmatter nightgauge-fixture-ok > "$ROOT_OK/nightgauge-fixture-ok/SKILL.md"

run_validator "$ROOT_OK"
RC=$?
check "a well-formed skill exits zero" "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"
check "a well-formed skill reports 0 unreadable" \
  "$(grep -qF '0 errors, 0 warnings, 0 unreadable' "$TMP/out" && echo 0 || echo 1)"

# --- Case 2: a genuinely missing field is still an ERROR ---------------------
# The other half of the pair. Without it, "stop reporting a torn read as a
# missing field" could be satisfied by never reporting a missing field at all.
ROOT_MISSING="$TMP/missing"
mkdir -p "$ROOT_MISSING/nightgauge-fixture-missing"
good_frontmatter nightgauge-fixture-missing \
  | grep -v '^  source:' \
  > "$ROOT_MISSING/nightgauge-fixture-missing/SKILL.md"

run_validator "$ROOT_MISSING"
RC=$?
check "a genuinely missing metadata.source exits 1" "$([ "$RC" -eq 1 ] && echo 0 || echo 1)"
check "a genuinely missing metadata.source is reported as a missing field" \
  "$(grep -qF 'missing required field: metadata.source' "$TMP/out" && echo 0 || echo 1)"
check "a genuinely missing field is NOT reported as unreadable" \
  "$(grep -qF 'UNREADABLE' "$TMP/out" && echo 1 || echo 0)"

# --- Case 3: a truncated read is UNREADABLE, not a missing field -------------
# The observed failure, reproduced deterministically: the frontmatter is cut off
# immediately before `source:`, exactly where the one real report landed.
ROOT_TORN="$TMP/torn"
mkdir -p "$ROOT_TORN/nightgauge-fixture-torn"
good_frontmatter nightgauge-fixture-torn \
  | sed '/^  source:/,$d' \
  > "$ROOT_TORN/nightgauge-fixture-torn/SKILL.md"

run_validator "$ROOT_TORN"
RC=$?
check "a truncated SKILL.md does not silently pass" "$([ "$RC" -ne 0 ] && echo 0 || echo 1)"
check "a truncated SKILL.md exits 2 (could not run), not 1 (file is wrong)" \
  "$([ "$RC" -eq 2 ] && echo 0 || echo 1)"
check "a truncated SKILL.md is reported UNREADABLE" \
  "$(grep -qF 'UNREADABLE' "$TMP/out" && echo 0 || echo 1)"
check "a truncated SKILL.md names the unclosed frontmatter block" \
  "$(grep -qF 'frontmatter block never closed' "$TMP/out" && echo 0 || echo 1)"
check "a truncated SKILL.md is NOT reported as a missing field" \
  "$(grep -qF 'missing required field' "$TMP/out" && echo 1 || echo 0)"
check "the summary counts it under 'unreadable', not 'errors'" \
  "$(grep -qF '0 errors, 0 warnings, 1 unreadable' "$TMP/out" && echo 0 || echo 1)"

# --- Case 4: a reader that exits early cannot turn a present field into a
# missing one (#1442) ---------------------------------------------------------
# The validator runs under `set -o pipefail`. Its field checks used to be
# `echo "$frontmatter" | grep -q` and its value reads `grep | head -1`; both
# readers exit on the first match, the writer takes SIGPIPE (141) and pipefail
# reports a PRESENT field as missing. That schedule depends on machine load, so
# the gate went red on a busy machine and green on the identical tree a minute
# later. This arm makes the schedule deterministic: a `grep`/`head` shim on
# PATH that behaves as an early-exiting reader — exit 141 without reading
# whenever it is asked to stop at the first match — so any surviving pipeline
# of that shape fails on every run, not one run in fifty.
SHIM="$TMP/shim"
mkdir -p "$SHIM"
REAL_GREP="$(command -v grep)"
REAL_HEAD="$(command -v head)"
{
  echo '#!/usr/bin/env bash'
  echo 'for a in "$@"; do case "$a" in -*q*) exit 141 ;; esac; done'
  echo "exec \"$REAL_GREP\" \"\$@\""
} > "$SHIM/grep"
{
  echo '#!/usr/bin/env bash'
  echo 'for a in "$@"; do case "$a" in -1|-n) exit 141 ;; esac; done'
  echo "exec \"$REAL_HEAD\" \"\$@\""
} > "$SHIM/head"
chmod +x "$SHIM/grep" "$SHIM/head"
PATH="$SHIM:$PATH" run_validator "$ROOT_OK"
RC=$?
check "a well-formed skill still exits zero when every early-exiting reader dies with SIGPIPE (#1442)" \
  "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"
check "no 'missing required field' is reported for a present field under the early-exit shim (#1442)" \
  "$(grep -qF 'missing required field' "$TMP/out" && echo 1 || echo 0)"

# --- Case 5: a fork into an agent that disallows Edit/Write (#3562) ---------
# fork_skill <root> <name> <allowed-tools> [extra frontmatter lines...]
# Writes a well-formed skill whose extra lines sit inside the frontmatter.
fork_skill() {
  local root="$1" name="$2" tools="$3" l
  shift 3
  mkdir -p "$root/$name"
  {
    cat <<EOF
---
name: $name
description: Validates a fixture skill for the metadata validator's own regression suite. Use when exercising scripts/validate-skill-metadata.sh.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: $tools
EOF
    for l in "$@"; do echo "$l"; done
    printf -- '---\n\n# %s\n' "$name"
  } > "$root/$name/SKILL.md"
}
AGENTS="$TMP/agents"
mkdir -p "$AGENTS"
printf -- '---\nname: ro\ndescription: x\ntools: Read\ndisallowedTools: Edit, Write\n---\n' > "$AGENTS/ro.md"
printf -- '---\nname: rw\ndescription: x\ntools: Read, Edit, Write\n---\n' > "$AGENTS/rw.md"
run_fork() { AGENTS_DIR="$AGENTS" run_validator "$1"; }

R="$TMP/fork-bad"
fork_skill "$R" nightgauge-fixture-fork-bad "Read Write Edit" "context: fork" "agent: ro"
run_fork "$R"; RC=$?
check "fork into a read-only agent with Write Edit in allowed-tools exits 1 (#3562)" \
  "$([ "$RC" -eq 1 ] && echo 0 || echo 1)"
check "the error names the SKILL.md and the agent (#3562)" \
  "$(BAD_LINES=$(grep -F 'nightgauge-fixture-fork-bad/SKILL.md' "$TMP/out"); grep -qF "agent 'ro'" <<<"$BAD_LINES" && echo 0 || echo 1)"
check "both Edit and Write are reported (#3562)" \
  "$([ "$(grep -c 'disallows \(Edit\|Write\)' "$TMP/out")" -eq 2 ] && echo 0 || echo 1)"

R="$TMP/fork-rw"
fork_skill "$R" nightgauge-fixture-fork-rw "Read Write Edit" "context: fork" "agent: rw"
run_fork "$R"; RC=$?
check "fork into an agent with no disallowedTools passes (#3562)" "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"

R="$TMP/nofork"
fork_skill "$R" nightgauge-fixture-nofork "Read Write Edit"
run_fork "$R"; RC=$?
check "no fork with Write Edit in allowed-tools passes (#3562)" "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"

R="$TMP/fork-ro-ok"
fork_skill "$R" nightgauge-fixture-fork-ro-ok "Read Glob Grep" "context: fork" "agent: ro"
run_fork "$R"; RC=$?
check "fork into a read-only agent without Edit/Write passes (#3562)" "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"

R="$TMP/fork-missing"
fork_skill "$R" nightgauge-fixture-fork-missing "Read" "context: fork" "agent: nope"
run_fork "$R"; RC=$?
check "fork into a missing agent file exits 1 (#3562)" "$([ "$RC" -eq 1 ] && echo 0 || echo 1)"
check "the missing agent file is named (#3562)" \
  "$(grep -qF "$AGENTS/nope.md does not exist" "$TMP/out" && echo 0 || echo 1)"

printf -- '---\nname: wo\ndescription: x\ndisallowedTools: Write\n---\n' > "$AGENTS/wo.md"
printf -- '---\nname: flow\ndescription: x\ndisallowedTools: [Edit, Write]\n---\n' > "$AGENTS/flow.md"

R="$TMP/fork-wo"
fork_skill "$R" nightgauge-fixture-fork-wo "Read Write Edit" "context: fork" "agent: wo"
run_fork "$R"; RC=$?
check "an agent disallowing only Write reports Write and not Edit (#3562)" \
  "$([ "$RC" -eq 1 ] && grep -qF 'disallows Write' "$TMP/out" && ! grep -qF 'disallows Edit' "$TMP/out" && echo 0 || echo 1)"

R="$TMP/fork-superstring"
fork_skill "$R" nightgauge-fixture-fork-superstring "Read NotebookEdit" "context: fork" "agent: ro"
run_fork "$R"; RC=$?
check "NotebookEdit in allowed-tools does not match Edit (#3562)" "$([ "$RC" -eq 0 ] && echo 0 || echo 1)"

R="$TMP/fork-flow"
fork_skill "$R" nightgauge-fixture-fork-flow "Read" "context: fork" "agent: flow"
run_fork "$R"; RC=$?
check "an unparseable disallowedTools list is an ERROR, not a silent skip (#3562)" \
  "$([ "$RC" -eq 1 ] && grep -qF 'cannot parse disallowedTools' "$TMP/out" && echo 0 || echo 1)"

R="$TMP/fork-badname"
fork_skill "$R" nightgauge-fixture-fork-badname "Read" "context: fork" "agent: ../x"
run_fork "$R"; RC=$?
check "a path-like agent name is rejected (#3562)" \
  "$([ "$RC" -eq 1 ] && grep -qF 'not a valid agent name' "$TMP/out" && echo 0 || echo 1)"

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
