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
#      the reader, including one behind a wrapper such as timeout, in a
#      program holding an expansion, a loop that can break, an `until read`, a
#      negated `!(...)` subshell, and a `((` or `$((` that bash reads as a
#      subshell because it does not end in `))`; any command of a piped group,
#      if, case or for, not only the first; a function the file defines,
#      named at the call; a grep whose output a group, if, loop or function
#      body around it sends to /dev/null; another shell's literal -c script;
#      and mapfile -n and dd count=. It keeps reading past an arithmetic `<<`, an
#      assignment's subscript included, which is no heredoc. It stays green on
#      the look-alikes it must not flag: quoted text, comments, heredoc bodies,
#      case patterns, regex alternations inside [[ ]] on any of its lines,
#      `||`, `exit` in awk text or END, `head -n -N`, the end of an input
#      process substitution, readers that read to the end, a loop's own read
#      inside a group, a function only defined there, or called through
#      `command`, which runs a program, a redirection that does not reach the
#      grep (a pipe, its own, a substitution or a function it only defines),
#      and a pipe inside another shell's -c script. It reads workflow `run:`
#      blocks and husky hooks, a step whose shell is a path or a command line
#      (`/usr/bin/bash -eo pipefail {0}`), skips one whose shell is another
#      language such as pwsh or python, and exits 2
#      rather than passing when it cannot read a file, such as one whose
#      heredoc never ends.
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
  hazard "cat | bash -c grep -q" 'cat "$1" | bash -c "grep -q needle"'
  hazard "cat | mapfile -n 1" 'cat "$1" | mapfile -n 1 first'
  hazard "cat | dd count=1" 'cat "$1" | dd count=1 bs=1 2>/dev/null >/dev/null'
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
# The arithmetic lines up top hold a `<<` that is no heredoc: misread as one,
# the rest of the file would go unread and every # BAD line after them missed.
cat >"$FIX/bad.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
(( n = 1 << 4 ))
(( x <<= 1 ))
echo $[x<<1]
if (( x << 1 > 2 )); then :; fi
for (( i = 1 << 1; i < 3; i++ )); do :; done
(( y = (x << 3) | head ))
a[1<<2]=5
x=1 a[1 << 2]=5
echo $(( (1 << 2) | 1 ))
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
cmd | { grep -q needle; } # BAD
cmd | ( grep -q needle ) # BAD
cmd | if grep -q needle; then :; fi # BAD
cmd | timeout 5 grep -q needle # BAD
cmd | timeout -k 1 -s TERM 5 head -1 # BAD
cmd | stdbuf -oL grep -m1 needle # BAD
cmd | sudo -u nobody grep -q needle # BAD
cmd | nice -n 5 head -1 # BAD
cmd | ghead -1 # BAD
cmd | gsed 5q # BAD
cmd | awk "/$pat/ { print; exit }" # BAD
cmd | sed -n "/$pat/{p;q;}" # BAD
cmd | sed "${n}q" # BAD
cmd | awk 'BEGIN { getline line; print line }' # BAD
cmd | awk 'BEGIN { print "reads no input" }' # BAD
cmd | awk 'END { if (n) { if (n > 1) { print n } } } { n++; if (n > 9) exit }' # BAD
cmd | awk '{ if (/x/) nextfile }' # BAD
cmd | perl -ne 'print; exit' # BAD
cmd | perl -lne 'last if /x/' # BAD
cmd | while read -r l; do [ "$l" = x ] && break; done # BAD
cmd | while IFS= read -r l; do # BAD
  case "$l" in
    stop) break ;;
  esac
done
cmd | while read -r l; do for x in a b; do break 2; done; done # BAD
cmd | until false; do read -r l; exit 0; done # BAD
diff <(cmd | head -5; echo more) "$f" # BAD
tee >(cmd | head -1) <"$f" # BAD
x=`cmd | head -1` # BAD
cmd | grep --quie needle # BAD
if !(cmd | grep -q needle); then :; fi # BAD
((cmd | head -1); echo) # BAD
x=$((cmd) | head -1) # BAD
cmd | until read -r l; do :; done # BAD
cmd | awk 'BEGIN { while ((getline line < "f") > 0) n++; print n }' # BAD
cmd | awk 'BEGIN { while (("sort" | getline line) > 0) n++ }' # BAD
cmd | LC_ALL=$loc grep -q needle # BAD
cmd | env X="$(id)" head -1 # BAD
a[$i]=1 cmd | sed 1q # BAD
cmd | (cd /tmp && grep -q needle) # BAD
cmd | { echo header; head -5; } # BAD
cmd | if true; then grep -q needle; fi # BAD
cmd | if [ -n "$v" ]; then cat; else head -1; fi # BAD
cmd | case "$v" in
  a) cat ;;
  *) grep -q needle ;; # BAD
esac
cmd | for x in a b; do read -r w; done # BAD
cmd | ! { head -1; } # BAD
cmd | { { echo; sed 1q; } | cat; } # BAD
cmd | { while read -r l; do [ "$l" = x ] && break; done; } # BAD
# Flagged although cat reads the rest: the gate's doc says why.
cmd | { read -r first; cat; } # BAD
has() { grep -qF -- "$1"; }
printf '%s\n' "$v" | has needle # BAD
cmd | LC_ALL=C has needle # BAD
function first_line { head -1; }
cmd | first_line # BAD
outer() { inner; }
inner() { awk '{ exit }'; }
cmd | outer # BAD
{ cmd | grep needle; } >/dev/null # BAD
cmd | { grep needle; } >/dev/null # BAD
if cmd | grep needle; then :; fi >/dev/null # BAD
while read -r l; do cmd | grep needle; done <"$f" >/dev/null # BAD
quiet_out() { cmd | grep needle; } >/dev/null # BAD
{ { cmd | grep needle; }; } &>/dev/null # BAD
cmd | bash -c 'grep -q needle' # BAD
cmd | sh -c 'head -1' # BAD
cmd | sh -ec 'read -r first' # BAD
cmd | env LC_ALL=C bash -o pipefail -c 'grep needle' >/dev/null # BAD
cmd | mapfile -n 1 lines # BAD
cmd | readarray -t -n 2 lines # BAD
cmd | dd count=1 bs=1 # BAD
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
if [[ $a == x &&
  $b =~ ^(a|head)$ ]]; then :; fi
let "x <<= 2"
(( x |= 1 ))
cmd | while :; do IFS= read -r line || break; echo "$line"; done
cmd | while read -r line; do for x in a b; do break; done; done
cmd | awk '/exit code/ { print }'
cmd | awk '{ print "exit" }'
cmd | awk '$1 == "exit" { n++ } END { print n }'
cmd | awk 'END { if (n) { if (n > 1) { exit 1 } } }'
cmd | awk -v x=1 'BEGIN { FS = ":" } { print $1 }'
cmd | awk 'BEGIN { while ((getline line) > 0) n++; print n }'
cmd | awk 'function f(a) { return a } { print f($1) }'
cmd | awk "{ print \$1 }"
cmd | head -n -1
cmd | head --lines=-2
cmd | ghead -c -5
diff <(cmd | head -5) "$f"
diff <(cmd | head -5 | sort) "$f"
while read -r line; do :; done < <(cmd | head -5)
cmd | perl -ne 'print if /x/'
cmd | perl -pe 's/a/b/'
cmd | timeout 5 grep needle
cmd | env - grep -c needle
y=`cmd | sed -n 1p`
cmd | until ! read -r l; do :; done
if ! (cmd | grep needle); then :; fi
(( (x) | 1 ))
echo $(( (1 + 2) | 4 ))
cmd | { while read -r line; do :; done; }
cmd | case "$v" in read) cat ;; head | grep) wc -l ;; esac
cmd | if [ -n "$v" ]; then cat; elif [ -z "$w" ]; then wc -l; else sort; fi
cmd | ( cd /tmp && sort )
cmd | { echo header; cat; } | sed -n 1p
cmd | { quiet() { grep -q needle; }; a=(); cat; }
drain() { cat; }
cmd | drain
again() { again; }
cmd | again
quiet() { grep -q needle; }
cmd | command quiet
quiet <<<"$v"
{ cmd | grep needle | wc -l; } >/dev/null
{ cmd | grep needle >"$out"; } >/dev/null
{ v=$(cmd | grep needle); } >/dev/null
{ cmd | grep needle; } | cat >/dev/null
{ cmd | grep needle; } 2>/dev/null
cmd | { { grep needle; } | wc -l; } >/dev/null
cmd | { { grep needle; } >"$out"; } >/dev/null
{ g() { cmd | grep needle; }; } >/dev/null
cmd | sh -c 'cat; echo done'
cmd | bash -c "$script"
cmd | sh -c 'printf "%s\n" x | grep -q x'
cmd | bash
cmd | mapfile -t lines
cmd | mapfile -n 0 lines
cmd | readarray -u 3 -n 1 lines
cmd | dd of=/dev/null bs=1
SH

# With extglob on, `!(a|head)` at a command's start is a pattern, so its `|`
# is no pipe. The same line in a file without it is a negated subshell.
cat >"$FIX/extglob.sh" <<'SH'
#!/usr/bin/env bash
shopt -s extglob
!(a|head) || true
for f in !(a|head); do :; done
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
      - name: bash by path, with options
        shell: /usr/bin/bash -eo pipefail {0}
        run: cmd | head -1 # BAD
      - name: a quoted command line
        shell: "bash -e {0}"
        run: |
          cmd | grep -q needle # BAD
      - name: powershell
        shell: pwsh
        run: |
          Get-Content f | head -1
      - name: python by command line
        shell: python3 {0}
        run: |
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

for fixture in "$FIX/good.sh" "$FIX/wf/good.yml" "$FIX/extglob.sh"; do
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

# A heredoc whose delimiter never comes swallows the rest of the file, in bash
# and in the gate. `let x<<=1` is one: bash reads a heredoc there too. So is a
# `[[` never closed. Each would hide the pipe after it, so each is exit 2.
printf '#!/bin/bash\ncat <<EOF\nbody\ncmd | grep -q needle\n' >"$FIX/open-heredoc.sh"
printf '#!/bin/bash\nx=1\nlet x<<=1\ncmd | grep -q needle\n' >"$FIX/let-shift.sh"
printf '#!/bin/bash\nif [[ -n $x &&\n  -z $y; then :; fi\ncmd | grep -q needle\n' >"$FIX/open-test.sh"
for fixture in "$FIX/open-heredoc.sh" "$FIX/let-shift.sh" "$FIX/open-test.sh"; do
  gate "$fixture"
  if [ "$GATE_RC" -eq 2 ]; then
    ok "$(basename "$fixture"): exit 2, not a pass"
  else
    bad "$(basename "$fixture"): exit 2 (got $GATE_RC)" "$GATE_OUT"
  fi
done

# Tree mode reads what it claims to: the scripts this issue fixed, a library,
# a plugin hook, a husky Git hook (no shebang), and the release workflow.
listed="$(python3 "$GATE" --list-files 2>&1)"
missing=""
for want in scripts/state-backstop.sh scripts/test-state-backstop.sh scripts/check-changelog.sh \
  scripts/lib/ci_local_failures.sh claude-plugins/nightgauge/hooks/test-quality.sh \
  .github/workflows/release.yml .github/workflows/staging.yml .husky/pre-commit; do
  grep -qxF -- "$want" <<<"$listed" || missing="$missing $want"
done
if [ -z "$missing" ]; then
  ok "the tree scan includes shell scripts, a library, the hooks and the workflows"
else
  bad "the tree scan misses:$missing"
fi

echo ""
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
