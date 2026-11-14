#!/usr/bin/env bash
# Static wiring test: one stable tag publishes to every channel (#3062).
#
# release workflows cannot run in PR CI, so this asserts their shape instead:
# release.yml calls marketplace-publish.yml under the same condition as the
# `channels` job, the preflight requires every publish credential, and both
# workflows verify the registry tokens with the one shared script. It also
# asserts that staging.yml scans an RC with VirusTotal only when dispatched on
# it, and that release.yml still requires that scan's record (#3213).
#
# Exit codes: 0 every assertion held; 1 an assertion failed; 2 HARNESS ERROR
# (the check could not run, so it asserted nothing).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RELEASE="${RELEASE_WORKFLOW:-$ROOT/.github/workflows/release.yml}"
PUBLISH="${PUBLISH_WORKFLOW:-$ROOT/.github/workflows/marketplace-publish.yml}"
STAGING="${STAGING_WORKFLOW:-$ROOT/.github/workflows/staging.yml}"
PASS=0
FAIL=0

command -v yq >/dev/null 2>&1 || { echo "HARNESS ERROR: yq (mikefarah) is not on PATH."; exit 2; }
for f in "$RELEASE" "$PUBLISH" "$STAGING"; do
  [ -f "$f" ] || { echo "HARNESS ERROR: $f does not exist."; exit 2; }
  yq -e '.jobs' "$f" >/dev/null 2>&1 || { echo "HARNESS ERROR: yq cannot parse $f."; exit 2; }
done

q() { yq -r "$1" "$2" 2>/dev/null; }

check() {
  local name="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo "PASS: $name"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $name (expected '$expected', got '$actual')"
    FAIL=$((FAIL + 1))
  fi
}

# marketplace-publish.yml: callable and dispatchable, same input on both.
check "marketplace-publish accepts workflow_call" "string" \
  "$(q '.on.workflow_call.inputs.registries.type' "$PUBLISH")"
check "marketplace-publish keeps workflow_dispatch" "choice" \
  "$(q '.on.workflow_dispatch.inputs.registries.type' "$PUBLISH")"
# shellcheck disable=SC2016 # a literal GitHub expression, not a shell one
check "the stable-tag guard validates the registries input" "true" \
  "$(q '.jobs.publish.steps[0].env.REGISTRIES == "${{ inputs.registries }}"' "$PUBLISH")"

# release.yml: the registries job.
check "registries job calls marketplace-publish.yml" "./.github/workflows/marketplace-publish.yml" \
  "$(q '.jobs.registries.uses' "$RELEASE")"
check "registries job publishes to both registries" "both" \
  "$(q '.jobs.registries.with.registries' "$RELEASE")"
check "registries job passes the release's secrets" "inherit" \
  "$(q '.jobs.registries.secrets' "$RELEASE")"
check "registries job grants contents: read" "read" \
  "$(q '.jobs.registries.permissions.contents' "$RELEASE")"
check "registries job grants the callee's attestations: read" "read" \
  "$(q '.jobs.registries.permissions.attestations' "$RELEASE")"
CHANNELS_IF="$(q '.jobs.channels.if' "$RELEASE")"
check "channels job has a condition" "true" "$([ -n "$CHANNELS_IF" ] && [ "$CHANNELS_IF" != null ] && echo true || echo false)"
check "registries runs under the channels condition" "$CHANNELS_IF" \
  "$(q '.jobs.registries.if' "$RELEASE")"
check "registries needs what channels needs" \
  "$(q '.jobs.channels.needs | @json' "$RELEASE")" "$(q '.jobs.registries.needs | @json' "$RELEASE")"

# release.yml: every publish credential is checked before the draft.
PREFLIGHT='.jobs.release.steps[] | select(.id == "preflight")'
for token in HOMEBREW_TAP_GITHUB_TOKEN VSCE_PAT OVSX_PAT; do
  check "preflight receives $token" "\${{ secrets.$token }}" \
    "$(q "$PREFLIGHT | .env.$token" "$RELEASE")"
  check "preflight requires $token" "true" \
    "$(q "$PREFLIGHT | .run | contains(\"$token\")" "$RELEASE")"
done
check "preflight verifies the Marketplace PAT" "true" \
  "$(q "$PREFLIGHT | .run | contains(\"verify-registry-pat.sh marketplace\")" "$RELEASE")"
check "preflight verifies the Open VSX PAT" "true" \
  "$(q "$PREFLIGHT | .run | contains(\"verify-registry-pat.sh open-vsx\")" "$RELEASE")"
PREFLIGHT_AT="$(q '.jobs.release.steps | to_entries | map(select(.value.id == "preflight")) | .[0].key' "$RELEASE")"
GORELEASER_AT="$(q '.jobs.release.steps | to_entries | map(select(.value.name == "Run GoReleaser")) | .[0].key' "$RELEASE")"
check "the preflight runs before GoReleaser drafts the release" "true" \
  "$([[ "$PREFLIGHT_AT" =~ ^[0-9]+$ && "$GORELEASER_AT" =~ ^[0-9]+$ ]] && [ "$PREFLIGHT_AT" -lt "$GORELEASER_AT" ] && echo true || echo false)"

HOMEBREW_SKIPS="$(grep -c -- '--skip=homebrew' "$RELEASE" "$PUBLISH" | awk -F: '{s += $2} END {print s + 0}')"
check "no workflow skips the Homebrew pipe" "0" "$HOMEBREW_SKIPS"

# marketplace-publish.yml verifies with the same shared script.
check "marketplace-publish verifies VSCE_PAT with the shared script" "true" \
  "$(q '.jobs.publish.steps[] | select(.name == "Verify VSCE_PAT against the publisher") | .run | contains("verify-registry-pat.sh marketplace")' "$PUBLISH")"
check "marketplace-publish verifies OVSX_PAT with the shared script" "true" \
  "$(q '.jobs.publish.steps[] | select(.name == "Verify OVSX_PAT against the namespace") | .run | contains("verify-registry-pat.sh open-vsx")' "$PUBLISH")"

# staging.yml (#3213): an RC tag push builds and runs ClamAV; the VirusTotal
# scan and the margin builds run only in a run dispatched on the chosen RC tag.
DISPATCH="github.event_name == 'workflow_dispatch'"
check "staging is dispatchable" "true" "$(q '.on | has("workflow_dispatch")' "$STAGING")"
check "staging still runs on an RC tag push" "true" \
  "$(q '.on.push.tags | any_c(. == "v[0-9]+.[0-9]+.[0-9]+-rc.[0-9]+")' "$STAGING")"
check "rc-scan runs only when dispatched" "$DISPATCH" "$(q '.jobs.rc-scan.if' "$STAGING")"
for step in "Build the margin scan binaries (docs/RELEASE_BINARIES.md)" "Upload margin scan inputs"; do
  check "'$step' runs only when dispatched" "$DISPATCH" \
    "$(q ".jobs.dry-run.steps[] | select(.name == \"$step\") | .if" "$STAGING")"
done
check "the ClamAV scan runs on every staging run" "null" \
  "$(q '.jobs.dry-run.steps[] | select(.name == "Malware scan (ClamAV)") | .if' "$STAGING")"
check "a dispatch is refused off an RC tag ref" "$DISPATCH" \
  "$(q '.jobs.dry-run.steps[0].if' "$STAGING")"
check "staging's concurrency group keeps a dispatch apart from the tag push" "true" \
  "$(q '.concurrency.group | contains("github.event_name")' "$STAGING")"
check "no staging condition still keys the scan on a tag push" "0" \
  "$(grep -c "startsWith(github.ref, 'refs/tags/v')" "$STAGING")"
check "release.yml still requires the RC's margin scan record" "true" \
  "$(grep -q 'scripts/release-rc-margin-check.sh' "$RELEASE" && echo true || echo false)"
check "the margin gate reads the dispatched staging run" "1" \
  "$(grep -c 'staging.yml/runs?event=workflow_dispatch&' "$ROOT/scripts/release-rc-margin-check.sh")"

# The shared script refuses an unknown registry and a missing token without
# calling any registry.
VERIFY="$ROOT/scripts/verify-registry-pat.sh"
env -u VSCE_PAT -u OVSX_PAT bash "$VERIFY" bogus >/dev/null 2>&1
check "verify-registry-pat.sh rejects an unknown registry" "2" "$?"
OUT="$(env -u VSCE_PAT bash "$VERIFY" marketplace 2>&1)"
check "verify-registry-pat.sh fails on a missing VSCE_PAT" "1:true" \
  "$?:$([[ "$OUT" == *"VSCE_PAT secret is not set"* ]] && echo true || echo false)"
OUT="$(env -u OVSX_PAT bash "$VERIFY" open-vsx 2>&1)"
check "verify-registry-pat.sh fails on a missing OVSX_PAT" "1:true" \
  "$?:$([[ "$OUT" == *"OVSX_PAT secret is not set"* ]] && echo true || echo false)"

# The downloaded verifier sees only the token it checks: a stub npx reports
# which credentials reached it.
STUB="$(mktemp -d)"
cat >"$STUB/npx" <<'STUB_EOF'
#!/bin/sh
for n in VSCE_PAT OVSX_PAT HOMEBREW_TAP_GITHUB_TOKEN GITHUB_TOKEN GH_TOKEN; do
  eval "[ -n \"\${$n:-}\" ] && echo SEEN:$n"
done
exit 0
STUB_EOF
chmod +x "$STUB/npx"
for REG in marketplace:VSCE_PAT open-vsx:OVSX_PAT; do
  SEEN="$(PATH="$STUB:$PATH" VSCE_PAT=a OVSX_PAT=b HOMEBREW_TAP_GITHUB_TOKEN=c \
    GITHUB_TOKEN=d GH_TOKEN=e bash "$VERIFY" "${REG%%:*}" 2>&1 | grep '^SEEN:' | tr '\n' ' ')"
  check "verify-registry-pat.sh ${REG%%:*} passes only ${REG#*:} to the verifier" \
    "SEEN:${REG#*:} " "$SEEN"
done
rm -rf "$STUB"

echo ""
echo "Release channel wiring: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
