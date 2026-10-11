#!/usr/bin/env bash
# Regression suite for the custom rules in .gitleaks.toml (#1770).
#
# The pinned gitleaks default ruleset has no Azure DevOps PAT rule, and its
# generic-api-key rule fires only when a variable name contains key, token,
# secret, pass and the like. So neither caught `VSCE_PAT=<value>` or
# `OVSX_PAT=<value>`, the two publishing tokens the release workflows use.
# This suite proves both custom rules fire, under their own rule IDs, and stay
# quiet on the references this repository really contains.
#
# The fixtures are built at run time from obviously fake pieces and written to
# a temporary directory. No committed file holds a string either rule matches,
# so the full-history scan (scripts/credential-scan.sh) never sees one.
#
# Run: bash scripts/test-credential-scan-rules.sh
# Also run by scripts/ci-local.sh and .github/workflows/credential-scan.yml.
set -uo pipefail
if ! TOPLEVEL="$(git rev-parse --show-toplevel)" || ! cd "$TOPLEVEL"; then
  echo "FAIL: not inside a git work tree; nothing was tested" >&2
  exit 1
fi

if ! command -v gitleaks >/dev/null 2>&1; then
  # ci-local.sh reports this step as INFRASTRUCTURE, never as a pass.
  echo "HARNESS ERROR: gitleaks is not installed, so the rule suite could not run."
  exit 2
fi

PASS=0
FAIL=0
TMP=""
# shellcheck disable=SC2329 # invoked by the trap below
cleanup() {
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}
trap cleanup EXIT
TMP="$(mktemp -d)"

ok() {
  PASS=$((PASS + 1))
  echo "  ok   $1"
}
nope() {
  FAIL=$((FAIL + 1))
  echo "  FAIL $1"
}

# pad <text> <length>: text right-padded with x to length.
pad() {
  local s="$1"
  while [ "${#s}" -lt "$2" ]; do s="${s}x"; done
  printf '%s' "$s"
}

ADO_PAT="$(pad FAKEadoPATfixtureNOTaSECRET 52)JQQJ""99$(pad FAKEtail 26)"
VSCE_VALUE="FAKEvscePATfixtureNOTaSECRET"
OVSX_VALUE="FAKE-ovsx-pat-fixture-not-a-secret"
if [ "${#ADO_PAT}" -ne 84 ]; then
  echo "HARNESS ERROR: the Azure DevOps fixture is ${#ADO_PAT} characters, not 84"
  exit 2
fi

# --- Fixtures ----------------------------------------------------------------
# One file per expected finding, named for the rule that must catch it.
POS="$TMP/positive"
NEG="$TMP/negative"
mkdir -p "$POS" "$NEG"
printf 'example %s\n' "$ADO_PAT" >"$POS/azure-devops-pat.txt"
printf 'VSCE_PAT=%s\n' "$VSCE_VALUE" >"$POS/vsce-ovsx-pat-assignment.env"
printf 'env:\n  OVSX_PAT: "%s"\n' "$OVSX_VALUE" >"$POS/vsce-ovsx-pat-assignment.yml"
printf "export VSCE_PAT='%s'\n" "$VSCE_VALUE" >"$POS/vsce-ovsx-pat-assignment.sh"

# References and near-misses, each of which must stay silent.
{
  # shellcheck disable=SC2016 # literal workflow expressions, written as text
  printf '%s\n' \
    'VSCE_PAT: ${{ secrets.VSCE_PAT }}' \
    'OVSX_PAT: ${{ secrets.OVSX_PAT }}' \
    '[[ -n "${VSCE_PAT:-}" ]] || exit 1' \
    'VSCE_PAT="$VSCE_PAT" npx vsce verify-pat nightgauge' \
    'VSCE_PAT=<your Azure DevOps token>' \
    'Set VSCE_PAT and OVSX_PAT in the production environment.'
  printf 'VSCE_PAT_FILE=%s\n' "$VSCE_VALUE"
  printf 'MY_OVSX_PAT_NAME=%s\n' "$OVSX_VALUE"
  # 84 characters without the signature, and the signature in a longer run.
  printf 'example %s\n' "$(pad FAKEnoSignatureHere 84)"
  printf 'example %sx\n' "$ADO_PAT"
} >"$NEG/references.txt"

# scan <dir> <report>: gitleaks over a directory with this repository's config.
scan() {
  gitleaks dir --no-banner --config "$TOPLEVEL/.gitleaks.toml" \
    --report-format json --report-path "$2" "$1" >/dev/null 2>&1
}

# findings <report>: "<file basename> <rule id>" per finding, sorted.
findings() {
  python3 - "$1" <<'PY'
import json, os, sys
with open(sys.argv[1], encoding="utf-8") as f:
    report = json.load(f) or []
for r in sorted(report, key=lambda r: (r["File"], r["RuleID"])):
    print(os.path.basename(r["File"]), r["RuleID"])
PY
}

# --- Every positive fixture is found by the rule it is named for -------------
scan "$POS" "$TMP/positive.json"
rc=$?
if [ "$rc" -ne 1 ] || [ ! -s "$TMP/positive.json" ]; then
  echo "HARNESS ERROR: gitleaks exited $rc on the positive fixtures without a report"
  exit 2
fi
POS_FOUND="$(findings "$TMP/positive.json")"
for f in "$POS"/*; do
  name="$(basename "$f")"
  rule="${name%.*}"
  if grep -qxF "$name $rule" <<<"$POS_FOUND"; then
    ok "$rule catches $name"
  else
    nope "$rule does not catch $name (found: $(grep -F "$name" <<<"$POS_FOUND" | tr '\n' ' '))"
  fi
done

# --- The secret group is the value, not the assignment -----------------------
# A finding whose Secret is the whole line would redact the variable name too
# and make the report useless.
SECRETS="$(python3 -c 'import json,sys; print("\n".join(r["Secret"] for r in json.load(open(sys.argv[1]))))' \
  "$TMP/positive.json")"
if grep -qxF "$VSCE_VALUE" <<<"$SECRETS" && grep -qxF "$OVSX_VALUE" <<<"$SECRETS"; then
  ok "the assignment rule reports the value as the secret"
else
  nope "the assignment rule's secret is not the assigned value"
fi

# --- No reference or near-miss is a finding -----------------------------------
scan "$NEG" "$TMP/negative.json"
rc=$?
NEG_FOUND=""
[ -s "$TMP/negative.json" ] && NEG_FOUND="$(findings "$TMP/negative.json")"
if [ "$rc" -eq 0 ] && [ -z "$NEG_FOUND" ]; then
  ok "secret references, placeholders and near-misses are not findings"
else
  nope "a reference or near-miss was reported (exit $rc): $(tr '\n' ' ' <<<"$NEG_FOUND")"
fi

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
