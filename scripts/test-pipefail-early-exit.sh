#!/usr/bin/env bash
#
# test-pipefail-early-exit.sh — regression suite for
# scripts/check-pipefail-early-exit.py (#2360).
#
# Every arm is deterministic. None waits on load, because the input is what
# decides: past a pipe buffer's worth, a writer feeding a reader that stops
# early is still writing when the reader exits, every time, on any machine.
#
#   A. The hazard is real. Under pipefail, each shape the gate forbids fails
#      on a 3 MB input although its reader matched: the writer dies of SIGPIPE
#      (or gets EPIPE where SIGPIPE is ignored) and pipefail reports that. The
#      gate forbids the shape because a test cannot be trusted to lose the race.
#   B. Every replacement the gate recommends succeeds on the same input.
#   C. The gate goes red on each forbidden shape, naming the file and line of
#      the reader, and stays green on the look-alikes it must not flag: quoted
#      text, comments, heredoc bodies, case patterns, regex alternations inside
#      [[ ]], `||`, and readers that read to the end. It reads workflow `run:`
#      blocks, skips a step whose shell is not sh or bash, and exits 2 rather
#      than passing when it cannot read a file.
#
# Run: bash scripts/test-pipefail-early-exit.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$REPO_ROOT/scripts/check-pipefail-early-exit.py"
PASS=0
FAIL=0
TMP="$(mktemp -d "${TMPDIR:-/tmp}/test-pipefail-early-exit.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

ok() { PASS=$((PASS + 1)); echo "  ✓ $1"; }
bad() { FAIL=$((FAIL + 1)); echo "  ✗ $1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/      | /'; }

# A 3 MB input whose first line is the match.
BIG="$TMP/big.txt"
awk 'BEGIN { print "needle"; for (i = 0; i < 100000; i++) printf "filler line %06d of a long input\n", i }' >"$BIG"
big="$(cat "$BIG")"

echo "A. the hazard, reproduced without load"
# hazard <label> <script>: <script> runs in a fresh bash under pipefail with
# $1 = the input file and ends in one pipeline. It must fail although its
# reader (the last stage) succeeded and its writer (the first) did not.
hazard() {
  local label="$1" script="$2" st
  # shellcheck disable=SC2016 # expands in the child shell, by design
  st="$(bash -c "set -o pipefail; $script"'; echo "$? ${PIPESTATUS[*]}"' _ "$BIG" 2>/dev/null)"
  # shellcheck disable=SC2086 # split "<rc> <writer> ... <reader>" into words
  set -- $st
  local rc="$1" writer="$2" reader="${!#}"
  if [ "$rc" -ne 0 ] && [ "$reader" -eq 0 ] && [ "$writer" -ne 0 ]; then
    ok "$label fails with its reader satisfied (statuses: ${st#* })"
  else
    bad "$label: want the pipeline to fail with the reader at 0, got statuses '$st'" \
      "If this stopped reproducing, find out why before trusting the gate's premise."
  fi
}
# shellcheck disable=SC2016 # each script expands in its child shell
{
  hazard "printf | grep -q (the shape in #2360)" 'v="$(cat "$1")"; printf "%s\n" "$v" | grep -q "^needle$"'
  hazard "cat | grep -qxF" 'cat "$1" | grep -qxF needle'
  hazard "cat | head -n 1" 'cat "$1" | head -n 1 >/dev/null'
  hazard "cat | awk exit" 'cat "$1" | awk "{ exit }"'
  hazard "cat | sed q" 'cat "$1" | sed -n "1{p;q;}" >/dev/null'
}

echo "B. the replacements, on the same input, under this suite's pipefail"
# check <label> <command...>: the command succeeds.
check() {
  local label="$1"
  shift
  if "$@"; then ok "$label"; else bad "$label"; fi
}
check "grep -q on a here-string" grep -q '^needle$' <<<"$big"
if [[ $big == needle* ]]; then ok "[[ ]] matching"; else bad "[[ ]] matching"; fi
check "grep -q on the file" grep -q '^needle$' "$BIG"
out="$(cat "$BIG")"
check "a captured command's output, then a here-string" grep -qxF needle <<<"$out"
if first="$(cat "$BIG" | sed -n 1p)" && [ "$first" = needle ]; then
  ok "cmd | sed -n 1p reads to the end"
else
  bad "cmd | sed -n 1p reads to the end (got '$first')"
fi
if first="$(head -n 1 <<<"$big")" && [ "$first" = needle ]; then
  ok "head on a here-string"
else
  bad "head on a here-string (got '$first')"
fi
check "a first line by parameter expansion" [ "${big%%$'\n'*}" = needle ]

echo "C. the gate"
FIX="$TMP/fixtures"
mkdir -p "$FIX"

# Every line the gate must flag carries `# BAD` on the reader's line; the
# comment itself is invisible to the gate.
cat >"$FIX/bad.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$v" | grep -q needle # BAD
echo "$v" | grep -qxF needle # BAD
cmd | grep -Fxq -- needle # BAD
cmd | LC_ALL=C grep -Eiq needle # BAD
cmd | grep -m1 needle # BAD
cmd | grep --max-count=1 needle # BAD
cmd | grep -l needle # BAD
cmd | grep --quiet needle # BAD
cmd | grep needle >/dev/null # BAD
cmd | grep -c needle >/dev/null 2>&1 # BAD
cmd | head -1 # BAD
cmd | head -n 1 # BAD
x="$(cmd | head -n1)" # BAD
echo "first: $(cmd | head -1)" # BAD
cmd | sed -n '1{p;q;}' # BAD
cmd | sed 10q # BAD
cmd | awk '{ print; exit }' # BAD
cmd | read -r first # BAD
cmd |
  grep -q needle # BAD
cmd \
  | grep -q needle # BAD
if cmd | grep -q needle; then :; fi # BAD
! cmd | grep -q needle # BAD
cmd |& grep -q needle # BAD
cmd | command grep -q needle # BAD
cmd | env LC_ALL=C grep -q needle # BAD
f() { cmd | grep -q needle; } # BAD
SH

cat >"$FIX/good.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
grep -q needle <<<"$v"
grep -qxF needle "$file"
[[ $v == *needle* ]]
out="$(cmd)"; grep -q needle <<<"$out"
cmd | sed -n 1p
cmd | sed -n 's/a/q/p'
cmd | sed 's/x/y/'
cmd | grep needle
cmd | grep -c needle
cmd | grep needle 2>/dev/null
cmd | grep needle >"$out"
cmd | awk '{ n++ } END { exit n == 0 }'
cmd | tail -1
cmd || grep -q needle "$file"
[ -n "$v" ] || head -n 1 "$file"
echo "a | grep -q b"
echo 'a | head -1'
# cmd | grep -q needle
sh -c "cmd | grep -q needle"
case "$v" in
  yes | head) echo pattern ;;
  (no | grep) echo pattern ;;
esac
[[ $v =~ ^(a|head)$ ]]
cat <<EOF
cmd | grep -q needle
EOF
cat <<-'EOF'
	x | head
	EOF
cmd | while read -r line; do :; done
x=$((a | b))
y="${v//|/,}"
z="$(case "$v" in a | head) echo x ;; esac)"
SH

mkdir -p "$FIX/wf"
cat >"$FIX/wf/bad.yml" <<'YML'
on: push
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - name: block
        run: |
          set -euo pipefail
          unzip -l "$VSIX" | grep -q build-info.json # BAD
      - run: gh release view "$TAG" | grep -qx "$TAG" # BAD
      - name: not shell
        shell: python
        run: |
          print("a | grep -q b")
          x = 1 | head
YML
cat >"$FIX/wf/good.yml" <<'YML'
on: push
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - name: block
        shell: bash
        run: |
          LISTING="$(unzip -l "$VSIX")"
          grep -q build-info.json <<<"$LISTING"
      - run: echo "${{ github.ref }}" | sed -n 1p
YML

# gate <files...>: run the gate; sets GATE_RC and GATE_OUT.
gate() {
  GATE_OUT="$(python3 "$GATE" "$@" 2>&1)"
  GATE_RC=$?
}

# flags_exactly <fixture>: the gate goes red naming every `# BAD` line of the
# fixture and nothing else.
flags_exactly() {
  local fixture="$1" want got missing="" extra=""
  gate "$fixture"
  want="$(grep -n '# BAD$' "$fixture" | cut -d: -f1 | LC_ALL=C sort -n)"
  got="$(grep -oE "^  [^ ]*$(basename "$fixture"):[0-9]+:" <<<"$GATE_OUT" | sed -E 's/.*:([0-9]+):$/\1/' | LC_ALL=C sort -n)"
  missing="$(LC_ALL=C comm -23 <(printf '%s\n' "$want") <(printf '%s\n' "$got") | tr '\n' ' ')"
  extra="$(LC_ALL=C comm -13 <(printf '%s\n' "$want") <(printf '%s\n' "$got") | tr '\n' ' ')"
  if [ "$GATE_RC" -ne 1 ]; then
    bad "$(basename "$fixture"): the gate exits 1 (got $GATE_RC)" "$GATE_OUT"
  elif [ -n "${missing// /}" ] || [ -n "${extra// /}" ]; then
    bad "$(basename "$fixture"): flags exactly the # BAD lines (missed: ${missing:-none}; extra: ${extra:-none})" "$GATE_OUT"
  else
    ok "$(basename "$fixture"): red, naming each of its $(grep -c . <<<"$want") planted lines and no other"
  fi
}

flags_exactly "$FIX/bad.sh"
flags_exactly "$FIX/wf/bad.yml"

for fixture in "$FIX/good.sh" "$FIX/wf/good.yml"; do
  gate "$fixture"
  if [ "$GATE_RC" -eq 0 ]; then
    ok "$(basename "$fixture"): green on every look-alike"
  else
    bad "$(basename "$fixture"): green on every look-alike (exit $GATE_RC)" "$GATE_OUT"
  fi
done

gate "$FIX/no-such-file.sh"
if [ "$GATE_RC" -eq 2 ]; then
  ok "a path that does not exist is exit 2, not a pass"
else
  bad "a path that does not exist is exit 2 (got $GATE_RC)" "$GATE_OUT"
fi

printf '#!/bin/bash\necho "unterminated\n' >"$FIX/broken.sh"
gate "$FIX/broken.sh"
if [ "$GATE_RC" -eq 2 ]; then
  ok "a file it cannot lex is exit 2, not a pass"
else
  bad "a file it cannot lex is exit 2 (got $GATE_RC)" "$GATE_OUT"
fi

# Tree mode reads what it claims to: the scripts this issue fixed, a library,
# a plugin hook, and the release workflow.
listed="$(python3 "$GATE" --list-files 2>&1)"
missing=""
for want in scripts/state-backstop.sh scripts/test-state-backstop.sh scripts/check-changelog.sh \
  scripts/lib/ci_local_failures.sh claude-plugins/nightgauge/hooks/test-quality.sh \
  .github/workflows/release.yml .github/workflows/staging.yml; do
  grep -qxF -- "$want" <<<"$listed" || missing="$missing $want"
done
if [ -z "$missing" ]; then
  ok "the tree scan includes shell scripts, a library, a hook and the workflows"
else
  bad "the tree scan misses:$missing"
fi

echo ""
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
