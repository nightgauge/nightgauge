#!/usr/bin/env bash
# Regression tests for scripts/doctor-app-permission-walkthrough.sh (#2094).
#
# The walkthrough is the evidence for a manual verification, so its own logic
# must not read a wrong state as a pass. These arms run it against a fake
# `nightgauge` that prints canned doctor JSON v2, one document per call, and
# feed the Enter presses on stdin. No network, no GitHub App, no real doctor.
#
#   1. The four states as doctor reports them: exit 0, four PASS sections.
#   2. State 1 shows only the raw board failure (NGD013): exit 1, named.
#   3. State 2 still reports NGD036: the states are not told apart, exit 1.
#   4. A baseline that is not clean stops the walk after one doctor run.
#   5. Doctor prints no JSON: exit 2, could not run.
#   6. No binary: exit 2 before anything runs.
#   7. Input ends before a step is confirmed: exit 2, never a pass, and the
#      walk says how to restore and accept the permission it had removed.
#   8. --record with a relative NIGHTGAUGE_BIN: the recorder runs the binary
#      the walk checked, never another `nightgauge` on PATH.
#
# An arm that asserted false prints FAIL; an arm whose fixture could not be
# built prints HARNESS ERROR and the suite exits 2.
#
# Run: bash scripts/test-doctor-app-permission-walkthrough.sh
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2

SCRIPT="$PWD/scripts/doctor-app-permission-walkthrough.sh"
PASS=0
FAIL=0
T="$(mktemp -d "${TMPDIR:-/tmp}/test-app-walkthrough.XXXXXX")" || {
  echo "HARNESS ERROR: mktemp failed"
  exit 2
}
trap 'rm -rf "$T"' EXIT

harness_error() {
  echo "HARNESS ERROR: $*"
  exit 2
}

ok() {
  echo "PASS: $1"
  PASS=$((PASS + 1))
}

bad() {
  echo "FAIL: $1"
  FAIL=$((FAIL + 1))
}

# Canned doctor documents. Links and ids are illustrative.
fixture() {
  local name="$1"
  case "$name" in
    clean)
      echo '{"v":2,"findings":[],"healthy":true,"exit_code":0}'
      ;;
    removed)
      cat <<'JSON'
{"v":2,"findings":[
 {"code":"NGD036","check":"github_identity","severity":"blocker",
  "title":"GitHub App example-app lacks permission Organization → Projects (write)",
  "evidence":{"permission":"organization_projects","declared":"none","granted":"none"},
  "remedies":[{"id":"app-permission","kind":"manual",
   "links":["https://github.com/organizations/example-org/settings/apps/example-app/permissions",
            "https://github.com/organizations/example-org/settings/installations/42"]}]},
 {"code":"NGD041","check":"board_population","severity":"blocker",
  "title":"the identity cannot see example-org's projects","evidence":{"reason":"app-permission"},
  "remedies":[{"id":"app-permission","kind":"manual"}]}],
 "healthy":false,"exit_code":2}
JSON
      ;;
    restored)
      cat <<'JSON'
{"v":2,"findings":[
 {"code":"NGD037","check":"github_identity","severity":"blocker",
  "title":"permission Organization → Projects is pending acceptance on example-org's installation",
  "evidence":{"permission":"organization_projects","declared":"write","granted":"none"},
  "remedies":[{"id":"accept","kind":"manual",
   "links":["https://github.com/organizations/example-org/settings/installations/42"]}]},
 {"code":"NGD041","check":"board_population","severity":"blocker",
  "title":"the identity cannot see example-org's projects","evidence":{"reason":"accept"},
  "remedies":[{"id":"accept","kind":"manual"}]}],
 "healthy":false,"exit_code":2}
JSON
      ;;
    raw-board)
      cat <<'JSON'
{"v":2,"findings":[
 {"code":"NGD013","check":"board_population","severity":"blocker",
  "title":"board population unverified: Could not resolve to a ProjectV2 with the number 3",
  "remedies":[]}],"healthy":false,"exit_code":2}
JSON
      ;;
    still-036)
      fixture removed
      ;;
    not-json)
      echo 'panic: runtime error'
      ;;
    *) harness_error "unknown fixture $name" ;;
  esac
}

# fake <dir> <fixture...>: a `nightgauge` that prints the next fixture per
# call, and logs its arguments.
fake() {
  local dir="$1" i=0 name
  shift
  mkdir -p "$dir/bin" || harness_error "mkdir $dir"
  for name in "$@"; do
    i=$((i + 1))
    fixture "$name" >"$dir/doc.$i" || harness_error "fixture $name"
  done
  cat >"$dir/bin/nightgauge" <<SH || harness_error "write fake"
#!/usr/bin/env bash
if [ "\${1:-}" = "--version" ]; then echo "nightgauge test"; exit 0; fi
echo "\$*" >>"$dir/calls"
n=\$(( \$(cat "$dir/count" 2>/dev/null || echo 0) + 1 ))
echo "\$n" >"$dir/count"
cat "$dir/doc.\$n" 2>/dev/null || echo '{}'
exit 2
SH
  chmod +x "$dir/bin/nightgauge" || harness_error "chmod fake"
}

# walk <dir> <enter presses>: runs the walkthrough against the fake, with
# exactly that many lines on stdin (a here-string would add one).
walk() {
  local dir="$1" presses="$2" input=""
  local i
  for ((i = 0; i < presses; i++)); do input+=$'\n'; done
  printf '%s' "$input" >"$dir/stdin" || harness_error "write $dir/stdin"
  NIGHTGAUGE_BIN="$dir/bin/nightgauge" bash "$SCRIPT" --out "$dir/out" \
    <"$dir/stdin" >"$dir/stdout" 2>"$dir/stderr"
}

count_of() {
  cat "$1/count" 2>/dev/null || echo 0
}

# 1. The four states as doctor reports them.
d="$T/happy"
fake "$d" clean removed restored clean
walk "$d" 3
rc=$?
transcript="$(cat "$d/out/transcript.md" 2>/dev/null)"
passes="$(grep -c '^PASS$' "$d/out/transcript.md" 2>/dev/null)"
calls="$(cat "$d/calls" 2>/dev/null)"
if [ "$rc" -eq 0 ] && [ "$passes" = "4" ] && [[ $transcript == *"Every state read as expected."* ]] &&
  [[ $calls == "doctor --only github_identity,board_population --json"* ]] &&
  ! grep -q "walk stopped before step 3" "$d/stderr"; then
  ok "four states read as expected"
else
  bad "four states: rc=$rc passes=$passes calls=${calls%%$'\n'*}"
fi
if [[ $transcript == *"https://github.com/organizations/example-org/settings/apps/example-app/permissions"* ]] &&
  grep -q "settings/installations/42" "$d/stdout"; then
  ok "doctor's links reach the transcript and the next step's prompt"
else
  bad "doctor's links missing from the transcript or the prompt"
fi

# 2. State 1 shows only the raw board failure.
d="$T/raw"
fake "$d" clean raw-board restored clean
walk "$d" 3
rc=$?
if [ "$rc" -eq 1 ] && grep -q "FAIL: expected NGD036 for organization_projects; a board finding other than NGD041" "$d/out/transcript.md"; then
  ok "a raw ProjectV2 failure is a FAIL, named"
else
  bad "raw board failure: rc=$rc"
fi

# 3. State 2 still reports NGD036.
d="$T/still"
fake "$d" clean removed still-036 clean
walk "$d" 3
rc=$?
if [ "$rc" -eq 1 ] && grep -q "FAIL: expected NGD037 for organization_projects; NGD036 reported too" "$d/out/transcript.md"; then
  ok "states not told apart are a FAIL"
else
  bad "NGD036 in state 2: rc=$rc"
fi

# 4. A baseline that is not clean stops the walk.
d="$T/dirty"
fake "$d" removed
walk "$d" 3
rc=$?
if [ "$rc" -eq 1 ] && [ "$(count_of "$d")" = "1" ] && grep -q "baseline is not clean" "$d/stdout" &&
  ! grep -q "walk stopped before step 3" "$d/stderr"; then
  ok "a dirty baseline stops after one doctor run"
else
  bad "dirty baseline: rc=$rc doctor runs=$(count_of "$d")"
fi

# 5. Doctor prints no JSON.
d="$T/notjson"
fake "$d" not-json
walk "$d" 3
rc=$?
if [ "$rc" -eq 2 ] && grep -q "doctor printed no usable JSON v2" "$d/stdout"; then
  ok "no JSON is could-not-run (2), not a verdict"
else
  bad "no JSON: rc=$rc"
fi

# 6. No binary.
d="$T/nobin"
mkdir -p "$d" || harness_error "mkdir $d"
NIGHTGAUGE_BIN="$d/missing" bash "$SCRIPT" --out "$d/out" </dev/null >"$d/stdout" 2>"$d/stderr"
rc=$?
if [ "$rc" -eq 2 ] && [ ! -e "$d/out" ]; then
  ok "a missing binary is could-not-run before anything is written"
else
  bad "missing binary: rc=$rc"
fi

# 7. Input ends before a step is confirmed.
d="$T/eof"
fake "$d" clean removed restored clean
walk "$d" 1
rc=$?
if [ "$rc" -eq 2 ] && [ "$(count_of "$d")" = "2" ] && grep -q "stopped: no more input" "$d/stderr"; then
  ok "running out of input is could-not-run, never a pass"
else
  bad "early EOF: rc=$rc doctor runs=$(count_of "$d")"
fi
if grep -q "walk stopped before step 3" "$d/stderr" &&
  grep -q "Projects: 'Read and write'" "$d/stderr" &&
  grep -q "organization owner opens the installation" "$d/stderr" &&
  grep -q "settings/apps/example-app/permissions" "$d/stderr"; then
  ok "a walk that stops after step 1 says how to restore and accept the permission"
else
  bad "no restore instructions after stopping in step 2: $(cat "$d/stderr")"
fi

# 8. --record with a relative NIGHTGAUGE_BIN, and another nightgauge on PATH.
d="$T/record"
fake "$d" clean removed restored clean
mkdir -p "$d/decoy" "$d/tools" || harness_error "mkdir $d"
printf '#!/usr/bin/env bash\necho decoy\n' >"$d/decoy/nightgauge" || harness_error "write decoy"
chmod +x "$d/decoy/nightgauge" || harness_error "chmod decoy"
# The fake vhs runs what the tape would type, and writes the outputs it names.
cat >"$d/tools/vhs" <<SH || harness_error "write fake vhs"
#!/usr/bin/env bash
nightgauge --version >"$d/recorded-with"
touch doctor-guided-repair.gif doctor-guided-repair.mp4
SH
chmod +x "$d/tools/vhs" || harness_error "chmod fake vhs"
printf '\n\n\n' >"$d/stdin" || harness_error "write $d/stdin"
(cd "$d" && PATH="$d/decoy:$d/tools:$PATH" NIGHTGAUGE_BIN="bin/nightgauge" \
  bash "$SCRIPT" --record --out "$d/out" <"$d/stdin" >"$d/stdout" 2>"$d/stderr")
rc=$?
if [ "$rc" -eq 0 ] && [ "$(cat "$d/recorded-with" 2>/dev/null)" = "nightgauge test" ] &&
  [ -e "$d/out/doctor-guided-repair.gif" ] && grep -q "#2095 recording" "$d/out/transcript.md"; then
  ok "the recording runs the binary the walk checked"
else
  bad "recording: rc=$rc recorded with '$(cat "$d/recorded-with" 2>/dev/null)'"
fi

echo
echo "doctor-app-permission-walkthrough: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
