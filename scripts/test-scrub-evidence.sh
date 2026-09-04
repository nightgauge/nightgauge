#!/usr/bin/env bash
# Regression tests for docker/clean-install/scrub-evidence.sh (#1335).
#
# The scrub is the SECOND of two layers, and it is different in kind from the
# first: the output-channel sanitizer matches secret SHAPES (so it can only
# cover shapes someone thought of), while this matches the exact VALUES the
# harness was handed. These tests are about that property — a credential in a
# format nobody has a pattern for must still not survive into the artifact.
#
# Drives the WORKING-TREE copy of the script, so editing it and running this
# suite locally actually proves something about the edit.
#
# Run: bash scripts/test-scrub-evidence.sh
# Also run by scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

SCRIPT="$PWD/docker/clean-install/scrub-evidence.sh"
PASS=0
FAIL=0
TMP=""

cleanup() { [ -n "$TMP" ] && rm -rf "$TMP"; return 0; }
trap cleanup EXIT

ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }

# assert_absent <file> <needle> <desc>
assert_absent() {
  if grep -qF -- "$2" "$1" 2>/dev/null; then bad "$3 (value survived in $1)"; else ok "$3"; fi
}
# assert_present <file> <needle> <desc>
assert_present() {
  if grep -qF -- "$2" "$1" 2>/dev/null; then ok "$3"; else bad "$3 (missing from $1)"; fi
}

TMP="$(mktemp -d)"
OUT="$TMP/out"
mkdir -p "$OUT/nested/deeper"

PLANTED="github_pat_11PLANTEDAAAAAAAAAAAA_bbbbbbbbbbbbbbbbbbbbbbbbCCCCCCCCCCC"
OPAQUE="totally-opaque-credential-no-pattern-matches-this-0987654321"
SHORT="abc"

printf 'GH_TOKEN=%s\nHOME=/root\n' "$PLANTED"   > "$OUT/container.log"
printf 'tool_result: %s\n' "$PLANTED"           > "$OUT/nested/vscode.log"
printf 'auth=%s\n' "$OPAQUE"                    > "$OUT/nested/deeper/report.json"
printf 'ordinary evidence line, keep me\nshort=%s\n' "$SHORT" > "$OUT/keep.txt"

echo "▶ scrub-evidence"

# The harness is handed these; the script must scrub their VALUES.
GH_TOKEN="$PLANTED" \
  ANTHROPIC_API_KEY="$OPAQUE" \
  UNRELATED_SHORT_TOKEN="$SHORT" \
  "$SCRIPT" "$OUT" >/dev/null 2>&1
rc=$?

if [ "$rc" -eq 0 ]; then ok "exits 0 on a clean scrub"; else bad "exit $rc, want 0"; fi

assert_absent "$OUT/container.log"            "$PLANTED" "a planted GH_TOKEN does not survive into the artifact"
assert_absent "$OUT/nested/vscode.log"        "$PLANTED" "scrubs nested files, not just the top level"
# The whole point of a value-based layer: no pattern would have caught this.
assert_absent "$OUT/nested/deeper/report.json" "$OPAQUE" "scrubs a credential no shape-matcher would recognise"

assert_present "$OUT/container.log"     "[REDACTED:ENV_SECRET]" "leaves a marker where the secret was"
assert_present "$OUT/keep.txt"          "ordinary evidence line, keep me" "leaves ordinary evidence intact"
# A short value would match everywhere and shred the evidence; an empty one
# would corrupt every file it touched. Both are worse than the leak.
assert_present "$OUT/keep.txt"          "short=$SHORT" "does not scrub values below the length floor"

# An env with nothing secret-shaped must be a no-op success, not a failure —
# the smoke path (E2E_SMOKE=1) runs with no credentials at all.
BEFORE="$(cat "$OUT/keep.txt")"
( unset GH_TOKEN GITHUB_TOKEN ANTHROPIC_API_KEY CLEAN_INSTALL_GH_TOKEN UNRELATED_SHORT_TOKEN
  env -u GH_TOKEN -u GITHUB_TOKEN -u ANTHROPIC_API_KEY "$SCRIPT" "$OUT" ) >/dev/null 2>&1
rc=$?
if [ "$rc" -eq 0 ]; then ok "exits 0 when there is nothing to scrub"; else bad "no-secrets run exit $rc, want 0"; fi
if [ "$BEFORE" = "$(cat "$OUT/keep.txt")" ]; then ok "no-secrets run leaves files byte-identical"; else bad "no-secrets run rewrote a file"; fi

# A missing directory is an operator error and must be loud, not a silent pass:
# a scrub that reported success without looking at anything is exactly the
# "absence of failures is not the presence of successes" shape.
"$SCRIPT" "$TMP/does-not-exist" >/dev/null 2>&1
if [ "$?" -ne 0 ]; then ok "a missing evidence directory fails loudly"; else bad "a missing directory exited 0"; fi

# The scrub must never print the secret it is removing.
output="$(GH_TOKEN="$PLANTED" "$SCRIPT" "$OUT" 2>&1)"
case "$output" in
  *"$PLANTED"*) bad "the scrub printed the secret to stdout" ;;
  *)            ok "the scrub never prints the secret it removes" ;;
esac

# --- Wiring: WHERE the scrub is called from is the whole fix ----------------
#
# The scrub itself being correct is not enough, and the first version of this
# change proved it: the scrub sat in tail position in a script that runs under
# `set -euo pipefail` with several explicit `exit 1`s, while the workflow
# uploads the artifact with `if: always()`. So it ran only when everything else
# had succeeded — and the failure path is exactly where a token is most likely
# to have been printed.
#
# These are source-level assertions, which is weaker than driving the real
# scripts (both need docker). They are here because the alternative is a
# correct scrub that never runs, which is indistinguishable from no scrub at
# all in every artifact anyone would look at.
echo ""
echo "▶ wiring"

CONTAINER_SH="$PWD/docker/clean-install/run-in-container.sh"
HOST_SH="$PWD/scripts/clean-install-e2e.sh"

if grep -q '^trap scrub_evidence_on_exit EXIT' "$CONTAINER_SH"; then
  ok "the container scrub runs from an EXIT trap, not in tail position"
else
  bad "the container scrub is not wired to an EXIT trap — it will be skipped on every abnormal exit"
fi

if grep -q 'scrub-evidence.sh" "\$RUN_DIR"' "$HOST_SH"; then
  ok "the host scrub covers \$RUN_DIR, not just the container's /out"
else
  bad "the host scrub does not cover \$RUN_DIR — host.log carries the same bytes into the artifact"
fi

# The host scrub must sit inside the cleanup trap, which is what makes it run
# on `die`, on a failed docker run, and on ^C.
if awk '/^cleanup\(\)/,/^trap cleanup EXIT/' "$HOST_SH" | grep -q 'scrub-evidence.sh'; then
  ok "the host scrub is inside the cleanup trap"
else
  bad "the host scrub is outside the cleanup trap — it will be skipped on every abnormal exit"
fi

echo ""
if [ "$FAIL" -gt 0 ]; then
  printf '\033[31m%s passed, %s FAILED\033[0m\n' "$PASS" "$FAIL"
  exit 1
fi
printf '\033[32mall %s scrub-evidence tests passed\033[0m\n' "$PASS"
