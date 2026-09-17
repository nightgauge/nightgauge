#!/usr/bin/env bash
# Self-test for scripts/sign-macos-binaries.sh.
#
# The behaviour that matters is what happens when things are NOT ideal, because
# that is what runs today: the Apple credentials do not exist yet, and the
# script must be a clean no-op rather than a release-breaking failure. It also
# must never report success for a signature that does not verify, since a
# broken signature is worse than no signature.
#
# `codesign`, `security`, `xcrun`, `ditto` and `uname` are stubbed on PATH.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT="$SCRIPT_DIR/sign-macos-binaries.sh"
PASS=0
FAIL=0
ok()  { echo "  ok   $1"; PASS=$(( PASS + 1 )); }
bad() { echo "  FAIL $1" >&2; FAIL=$(( FAIL + 1 )); }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin" "$WORK/art"
export RUNNER_TEMP="$WORK/art"

# --- stubs -------------------------------------------------------------------
# Each honours a *_RC variable so a test can choose the outcome.
cat > "$WORK/bin/uname" <<'S'
#!/usr/bin/env bash
[[ "${1:-}" == "-s" ]] && echo "${FAKE_UNAME:-Darwin}" || echo "${FAKE_UNAME:-Darwin}"
S
cat > "$WORK/bin/security" <<'S'
#!/usr/bin/env bash
exit 0
S
cat > "$WORK/bin/uuidgen" <<'S'
#!/usr/bin/env bash
echo deadbeef-0000-0000-0000-000000000000
S
cat > "$WORK/bin/codesign" <<'S'
#!/usr/bin/env bash
for a in "$@"; do
  case "$a" in
    --verify) exit "${VERIFY_RC:-0}" ;;
    --display) echo "Authority=Developer ID Application: Edibu LLC (RZJPN7Y7BG)"; exit 0 ;;
  esac
done
exit "${SIGN_RC:-0}"
S
cat > "$WORK/bin/ditto" <<'S'
#!/usr/bin/env bash
# `ditto -c -k --keepParent SRC DST`: the destination is the LAST argument, not
# $3. Indexing it wrong wrote a stray file named "--keepParent" into the repo
# root, which the publication-boundary gate correctly rejected.
dst="${@: -1}"
: > "$dst"; exit 0
S
cat > "$WORK/bin/xcrun" <<'S'
#!/usr/bin/env bash
echo "notarytool stub"; exit "${NOTARY_RC:-0}"
S
chmod +x "$WORK"/bin/*
export PATH="$WORK/bin:$PATH"

BIN="$WORK/nightgauge-darwin-arm64"
: > "$BIN"

run() { ( unset APPLE_CERT_P12 APPLE_CERT_PASSWORD APPLE_SIGNING_IDENTITY \
                APPLE_ID APPLE_TEAM_ID APPLE_APP_PASSWORD
          "$@" bash "$SUT" "$BIN" ) > "$WORK/out" 2>&1; echo $?; }

signing_env=(env APPLE_CERT_P12=Zm9v APPLE_CERT_PASSWORD=pw
             APPLE_SIGNING_IDENTITY="Developer ID Application: Edibu LLC (RZJPN7Y7BG)")
notary_env=("${signing_env[@]}" APPLE_ID=a@b.c APPLE_TEAM_ID=RZJPN7Y7BG APPLE_APP_PASSWORD=x)

echo "sign-macos-binaries self-test"

# 1. The state that exists today: no credentials. Must exit 0, not break a release.
rc=$(run env)
if [[ "$rc" == 0 ]] && grep -q 'credentials absent' "$WORK/out"; then
  ok "no credentials -> clean no-op, exit 0"
else
  bad "no credentials -> exit 0 (got $rc)"; cat "$WORK/out" >&2
fi

# 2. Partial credentials must also no-op, not half-sign.
rc=$(run env APPLE_CERT_P12=Zm9v)
if [[ "$rc" == 0 ]] && grep -q 'credentials absent' "$WORK/out"; then
  ok "partial credentials -> clean no-op, exit 0"
else
  bad "partial credentials -> exit 0 (got $rc)"; cat "$WORK/out" >&2
fi

# 3. Happy path.
rc=$(run "${signing_env[@]}")
if [[ "$rc" == 0 ]] && grep -q 'Authority=Developer ID' "$WORK/out"; then
  ok "credentials present -> signs and reports the authority"
else
  bad "credentials present -> signs (got $rc)"; cat "$WORK/out" >&2
fi

# 4. A signature that does not verify must fail. Worse than unsigned, because
#    it looks deliberate.
rc=$(run "${signing_env[@]}" VERIFY_RC=1)
if [[ "$rc" != 0 ]] && grep -q 'does not verify' "$WORK/out"; then
  ok "signature fails verification -> non-zero exit"
else
  bad "signature fails verification -> non-zero (got $rc)"; cat "$WORK/out" >&2
fi

# 5. codesign itself failing must fail.
rc=$(run "${signing_env[@]}" SIGN_RC=1)
if [[ "$rc" != 0 ]] && grep -q 'codesign failed' "$WORK/out"; then
  ok "codesign failure -> non-zero exit"
else
  bad "codesign failure -> non-zero (got $rc)"; cat "$WORK/out" >&2
fi

# 6. A missing binary is an error, not a silent skip.
rc=$( ( unset APPLE_ID; "${signing_env[@]}" bash "$SUT" "$WORK/does-not-exist" ) >"$WORK/out" 2>&1; echo $?)
if [[ "$rc" != 0 ]] && grep -q 'does not exist' "$WORK/out"; then
  ok "missing binary -> non-zero exit"
else
  bad "missing binary -> non-zero (got $rc)"; cat "$WORK/out" >&2
fi

# 7. Notary outage must NOT fail the release: the signature is load-bearing.
rc=$(run "${notary_env[@]}" NOTARY_RC=1)
if [[ "$rc" == 0 ]] && grep -q 'notarization did not complete' "$WORK/out"; then
  ok "notary failure -> warning, release still succeeds"
else
  bad "notary failure -> non-fatal (got $rc)"; cat "$WORK/out" >&2
fi

# 8. Notarization success is reported, with the stapling caveat stated.
rc=$(run "${notary_env[@]}")
if [[ "$rc" == 0 ]] && grep -q 'cannot be stapled' "$WORK/out"; then
  ok "notary success -> reports the un-staplable caveat"
else
  bad "notary success -> reports caveat (got $rc)"; cat "$WORK/out" >&2
fi

# 9. Refuse to run on a non-macOS runner rather than emitting a fake success.
rc=$(run "${signing_env[@]}" FAKE_UNAME=Linux)
if [[ "$rc" != 0 ]] && grep -q 'requires macOS' "$WORK/out"; then
  ok "non-macOS runner with credentials -> non-zero exit"
else
  bad "non-macOS runner -> non-zero (got $rc)"; cat "$WORK/out" >&2
fi

# 10. No binaries at all is an error.
rc=$( bash "$SUT" >"$WORK/out" 2>&1; echo $?)
if [[ "$rc" != 0 ]] && grep -q 'no binaries given' "$WORK/out"; then
  ok "no arguments -> non-zero exit"
else
  bad "no arguments -> non-zero (got $rc)"; cat "$WORK/out" >&2
fi

echo
echo "sign-macos-binaries: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] || exit 1
