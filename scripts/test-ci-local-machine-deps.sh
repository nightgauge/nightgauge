#!/usr/bin/env bash
#
# test-ci-local-machine-deps.sh — the gate's dependencies on its host machine
# (#3127). A newly provisioned macOS machine hit all three:
#
#   1. bash 3.2 (/bin/bash) hung the gate until it was killed. ci-local.sh and
#      validate-skill-metadata.sh must exit 2 at once, naming the fix.
#   2. CI_LOCAL_JOBS was a fixed 4. The default is max(4, ncpu/3), capped at 12,
#      and the gate reports the number and its source.
#   3. A global commit.gpgsign=true with no key loaded failed fixture commits.
#      Every step must run with commit/tag signing off.
#
# Every arm runs through a probe flag, in seconds. A HARNESS ERROR line means an
# arm could not run, which asserts nothing about the change.
#
# Run: bash scripts/test-ci-local-machine-deps.sh
# Also run by scripts/ci-local.sh and .github/workflows/lint.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

PASS=0
FAIL=0
TMP="$(mktemp -d "${TMPDIR:-/tmp}/ci-local-machine-deps.XXXXXX")" || {
  echo "HARNESS ERROR: mktemp failed"
  exit 2
}
trap 'rm -rf "$TMP"' EXIT

check() { # check <description> <0-if-ok>
  if [ "$2" = "0" ]; then
    echo "PASS: $1"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $1"
    FAIL=$((FAIL + 1))
  fi
}

# ── 1. bash >= 4 guard ───────────────────────────────────────────────────────
# shellcheck source=lib/require-bash4.sh
. scripts/lib/require-bash4.sh

if bash4_major_ok 3; then r=1; else r=0; fi
check "bash4_major_ok rejects major 3" $r
bash4_major_ok 4
check "bash4_major_ok accepts major 4" $?
bash4_major_ok 5
check "bash4_major_ok accepts major 5" $?

msg="$( (_NG_BASH_MAJOR_OVERRIDE=3 require_bash4 demo.sh) 2>&1)"
code=$?
if [ "$code" -eq 2 ]; then r=0; else r=1; fi
check "require_bash4 exits 2 under bash major 3 (got $code)" $r
case "$msg" in *"running bash ${BASH_VERSION}"*"brew install bash"*) r=0 ;; *) r=1 ;; esac
check "the message names the running version and brew install bash" $r
( _NG_BASH_MAJOR_OVERRIDE=5 require_bash4 demo.sh ) 2>/dev/null
check "require_bash4 returns under bash major 5" $?

if [ -x /bin/bash ] && ! /bin/bash -c '[ "${BASH_VERSINFO[0]}" -ge 4 ]'; then
  for target in scripts/ci-local.sh scripts/validate-skill-metadata.sh; do
    out="$(perl -e 'alarm 20; exec @ARGV' /bin/bash "$target" --list-steps 2>&1)"
    code=$?
    if [ "$code" -eq 2 ]; then r=0; else r=1; fi
    check "/bin/bash $target exits 2 at once (got $code)" $r
    case "$out" in *"needs bash 4 or newer"*"brew install bash"*) r=0 ;; *) r=1 ;; esac
    check "/bin/bash $target names the fix" $r
  done
else
  echo "SKIP: /bin/bash is bash 4 or newer here; the stubbed-major arms cover the guard"
fi

# ── 2. machine-sized budget ──────────────────────────────────────────────────
# shellcheck source=lib/ci-local-budget.sh
. scripts/lib/ci-local-budget.sh

for pair in 1:4 4:4 12:4 30:10 48:12 96:12; do
  got="$(ci_local_default_jobs "${pair%%:*}")"
  if [ "$got" = "${pair##*:}" ]; then r=0; else r=1; fi
  check "ncpu=${pair%%:*} gives a default budget of ${pair##*:} (got $got)" $r
done
ncpu="$(ci_local_detect_ncpu)"
case "$ncpu" in '' | *[!0-9]*) r=1 ;; *) r=0 ;; esac
check "ci_local_detect_ncpu prints a number (got '$ncpu')" $r

out="$(CI_LOCAL_JOBS=7 bash scripts/ci-local.sh --budget-probe 2>&1)"
if [ "$out" = "Heavy-step budget: CI_LOCAL_JOBS=7 (CI_LOCAL_JOBS env)" ]; then r=0; else r=1; fi
check "an explicit CI_LOCAL_JOBS wins and is reported as such (got '$out')" $r
out="$(env -u CI_LOCAL_JOBS bash scripts/ci-local.sh --budget-probe 2>&1)"
want="Heavy-step budget: CI_LOCAL_JOBS=$(ci_local_default_jobs "$ncpu")"
want="$want (default max(4, ncpu/3) cap 12, ncpu=$ncpu)"
if [ "$out" = "$want" ]; then r=0; else r=1; fi
check "the default budget is derived from ncpu and reported (got '$out')" $r

# ── 3. fixture commits are never signed ──────────────────────────────────────
mkdir -p "$TMP/home"
printf '[commit]\n\tgpgsign = true\n[tag]\n\tgpgsign = true\n' > "$TMP/home/.gitconfig"
HOME="$TMP/home" GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.abbrev GIT_CONFIG_VALUE_0=9 \
  bash scripts/ci-local.sh --env-probe > "$TMP/env-probe.log" 2>&1
code=$?
if [ "$code" -ne 0 ]; then
  echo "HARNESS ERROR: --env-probe exited $code"
  sed 's/^/    /' "$TMP/env-probe.log"
  exit 2
fi
grep -qx 'commit.gpgsign=false' "$TMP/env-probe.log"
check "a step sees commit.gpgsign=false over a global true" $?
grep -qx 'tag.gpgsign=false' "$TMP/env-probe.log"
check "a step sees tag.gpgsign=false over a global true" $?
grep -qx 'core.abbrev=9' "$TMP/env-probe.log"
check "a caller's own GIT_CONFIG_* entries survive (appended, not replaced)" $?

mkdir -p "$TMP/gnupg"
chmod 700 "$TMP/gnupg"
GNUPGHOME="$TMP/gnupg" GIT_CONFIG_COUNT=2 \
  GIT_CONFIG_KEY_0=commit.gpgsign GIT_CONFIG_VALUE_0=true \
  GIT_CONFIG_KEY_1=gpg.program GIT_CONFIG_VALUE_1=false \
  bash scripts/test-agent-guidance-check.sh > "$TMP/guidance.log" 2>&1
code=$?
if [ "$code" -eq 0 ]; then r=0; else r=1; fi
check "test-agent-guidance-check.sh passes with signing forced on and no key (got $code)" $r
[ "$code" -eq 0 ] || tail -20 "$TMP/guidance.log" | sed 's/^/    /'

echo ""
if [ "$FAIL" -gt 0 ]; then
  echo "✗ $PASS passed, $FAIL FAILED"
  exit 1
fi
echo "✓ all $PASS ci-local machine-dependency tests passed"
