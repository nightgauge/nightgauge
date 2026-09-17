#!/usr/bin/env bash
# Codesign (and optionally notarize) the macOS binaries that ship inside the
# VSIX. No-op, loudly, when the Apple credentials are absent.
#
# Usage:
#   sign-macos-binaries.sh bin/nightgauge-darwin-arm64 bin/nightgauge-darwin-amd64
#
# Credentials, all optional. Signing is skipped entirely unless the first three
# are present; notarization additionally needs the last three.
#
#   APPLE_CERT_P12         base64 of a "Developer ID Application" .p12
#   APPLE_CERT_PASSWORD    its export password
#   APPLE_SIGNING_IDENTITY e.g. "Developer ID Application: Edibu, LLC (RZJPN7Y7BG)"
#   APPLE_ID               Apple ID for notarytool
#   APPLE_TEAM_ID          e.g. RZJPN7Y7BG
#   APPLE_APP_PASSWORD     an app-specific password, not the account password
#
# ## Why this exists
#
# The binary that ships inside the VSIX is a ~28MB statically linked Go
# executable that spawns processes and makes network calls, and until now it
# carried only an ad-hoc (linker) signature, which is cryptographically
# equivalent to unsigned. The Marketplace security-and-trust guidance states
# that incoming packages are scanned "using the same advanced tech found in
# Microsoft Defender" and that packages are rescanned after publication. A
# valid Developer ID signature is the single strongest provenance signal
# available for a native executable, and it also stops Gatekeeper from warning
# users who install from a VSIX or Open VSX.
#
# Note on what is NOT claimed: symbol stripping was investigated as a possible
# false-positive trigger and ruled out. Go retains function names in pclntab
# regardless of `-s -w` (`main.main` and package identifiers are still present
# in a stripped build, and `go tool nm` still works), so the binary is not
# meaningfully obscured and the release keeps `-s -w`.
#
# ## Notarization and bare executables
#
# A bare Mach-O cannot be stapled: `stapler` works on .app, .dmg and .pkg only.
# Submitting a zip of the binary still registers the notarization ticket with
# Apple, which Gatekeeper checks online, so notarization is worth doing even
# though the ticket cannot be attached to the file. This script therefore
# notarizes when it can and never treats an unstaplable binary as a failure.

set -uo pipefail

BINARIES=("$@")
(( ${#BINARIES[@]} > 0 )) || { echo "::error::no binaries given"; exit 1; }

have() { [[ -n "${!1:-}" ]]; }

if ! { have APPLE_CERT_P12 && have APPLE_CERT_PASSWORD && have APPLE_SIGNING_IDENTITY; }; then
  echo "::notice::Apple signing credentials absent — leaving binaries ad-hoc signed."
  echo "  To enable, create a 'Developer ID Application' certificate for the team"
  echo "  and set APPLE_CERT_P12, APPLE_CERT_PASSWORD and APPLE_SIGNING_IDENTITY."
  echo "  See docs/RELEASE_CHECKLIST.md for the full list."
  exit 0
fi

[[ "$(uname -s)" == "Darwin" ]] || {
  echo "::error::codesign requires macOS; this step must run on a macOS runner."
  exit 1
}

KEYCHAIN="${RUNNER_TEMP:-/tmp}/nightgauge-signing.keychain-db"
KEYCHAIN_PASSWORD="$(uuidgen)"
CERT_PATH="${RUNNER_TEMP:-/tmp}/developer-id.p12"

cleanup() {
  # The keychain holds a private key. Remove it whether or not signing worked;
  # a self-hosted runner would otherwise retain it between jobs.
  security delete-keychain "$KEYCHAIN" 2>/dev/null || true
  rm -f "$CERT_PATH" 2>/dev/null || true
}
trap cleanup EXIT

echo "::group::Importing Developer ID certificate"
printf '%s' "$APPLE_CERT_P12" | base64 --decode > "$CERT_PATH"
security create-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN"
security set-keychain-settings -lut 21600 "$KEYCHAIN"
security unlock-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN"
security import "$CERT_PATH" -P "$APPLE_CERT_PASSWORD" -A \
  -t cert -f pkcs12 -k "$KEYCHAIN"
# Without this, codesign prompts for keychain access and hangs a headless job.
security set-key-partition-list -S apple-tool:,apple:,codesign: \
  -s -k "$KEYCHAIN_PASSWORD" "$KEYCHAIN" >/dev/null
security list-keychain -d user -s "$KEYCHAIN" login.keychain-db
echo "::endgroup::"

FAILED=()
for BIN in "${BINARIES[@]}"; do
  echo "::group::Signing $BIN"
  if [[ ! -f "$BIN" ]]; then
    echo "::error::$BIN does not exist"; FAILED+=("$BIN"); echo "::endgroup::"; continue
  fi

  # --options runtime enables the hardened runtime, which notarization requires.
  # --timestamp binds a trusted timestamp so the signature outlives the cert.
  if ! codesign --force --sign "$APPLE_SIGNING_IDENTITY" \
        --options runtime --timestamp --keychain "$KEYCHAIN" "$BIN"; then
    echo "::error::codesign failed for $BIN"; FAILED+=("$BIN"); echo "::endgroup::"; continue
  fi

  # Verify rather than trust the exit code: a signature that does not verify is
  # worse than none, because it looks deliberate.
  if ! codesign --verify --strict --verbose=2 "$BIN"; then
    echo "::error::signature on $BIN does not verify"; FAILED+=("$BIN"); echo "::endgroup::"; continue
  fi
  codesign --display --verbose=2 "$BIN" 2>&1 | grep -E 'Authority|TeamIdentifier|flags' || true
  echo "::endgroup::"
done

if (( ${#FAILED[@]} > 0 )); then
  echo "::error::signing failed for: ${FAILED[*]}"
  exit 1
fi

if ! { have APPLE_ID && have APPLE_TEAM_ID && have APPLE_APP_PASSWORD; }; then
  echo "::notice::Signed, but notarization credentials absent — skipping notarization."
  exit 0
fi

for BIN in "${BINARIES[@]}"; do
  echo "::group::Notarizing $BIN"
  ZIP="${RUNNER_TEMP:-/tmp}/$(basename "$BIN").zip"
  # notarytool takes an archive, never a bare executable.
  ditto -c -k --keepParent "$BIN" "$ZIP"
  if xcrun notarytool submit "$ZIP" \
       --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" \
       --password "$APPLE_APP_PASSWORD" --wait --timeout 30m; then
    echo "::notice::$BIN notarized. A bare Mach-O cannot be stapled; Gatekeeper checks the ticket online."
  else
    # Deliberately non-fatal. The signature is the load-bearing part, and a
    # notary service outage must not fail a release.
    echo "::warning::notarization did not complete for $BIN; the Developer ID signature still applies."
  fi
  rm -f "$ZIP"
  echo "::endgroup::"
done
