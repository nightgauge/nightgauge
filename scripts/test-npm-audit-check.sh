#!/usr/bin/env bash
# Regression suite for scripts/npm-audit-check.js, run against fixtures so no
# arm depends on the live advisory database.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
pass=0
fail=0

ok() { echo "PASS: $1"; pass=$((pass + 1)); }
bad() { echo "FAIL: $1"; fail=$((fail + 1)); }

# A root advisory (msal-node) and a package flagged only because it depends on
# it (identity, whose `via` is the package name, not an advisory).
cat >"$WORK/audit.json" <<'EOF'
{"vulnerabilities":{
  "@azure/msal-node":{"severity":"high","via":[{"source":1243111,"url":"https://github.com/advisories/GHSA-xcx6-jx8p-x3h5"}]},
  "@azure/identity":{"severity":"high","via":["@azure/msal-node"]}
}}
EOF

run() { # $1 = allowlist JSON
  printf '%s' "$1" >"$WORK/allow.json"
  NPM_AUDIT_CHECK_INPUT="$WORK/audit.json" NPM_AUDIT_CHECK_ALLOWLIST="$WORK/allow.json" \
    node "$ROOT/scripts/npm-audit-check.js" >"$WORK/out" 2>&1
}

if run '{"allowlist":[]}'; then
  bad "an unallowed advisory passed"
else
  grep -q '@azure/msal-node' "$WORK/out" && grep -q '@azure/identity' "$WORK/out" &&
    ok "an unallowed advisory fails, naming the package and its dependent" ||
    bad "failure output did not name both packages"
fi

if run '{"allowlist":[{"id":"1243111","reason":"r","expires":"2999-01-01","addedBy":"x"}]}'; then
  ok "allow-listing the root advisory also covers a package flagged only through it"
else
  bad "a dependent of an allow-listed advisory still failed"; cat "$WORK/out" >&2
fi

if run '{"allowlist":[{"id":"1243111","reason":"r","expires":"2000-01-01","addedBy":"x"}]}'; then
  bad "an expired entry passed"
else
  grep -q 'expired' "$WORK/out" && ok "an expired entry fails" || bad "expired entry not reported"
fi

echo "npm-audit-check suite: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
