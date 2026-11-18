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

# Thresholds (ADR-023 Decision 8): a moderate advisory fails in a runtime
# dependency and passes in a dev-only one; high fails in either.
cat >"$WORK/moderate.json" <<'JSON'
{"vulnerabilities":{"leftpad":{"severity":"moderate","via":[{"source":4242,"url":"https://example.invalid/a"}]}}}
JSON
printf '%s' '{"vulnerabilities":{}}' >"$WORK/empty.json"
tiers() { # $1 = whole-tree fixture, $2 = runtime fixture
  printf '%s' '{"allowlist":[]}' >"$WORK/allow.json"
  NPM_AUDIT_CHECK_INPUT="$1" NPM_AUDIT_CHECK_RUNTIME_INPUT="$2" \
    NPM_AUDIT_CHECK_ALLOWLIST="$WORK/allow.json" \
    node "$ROOT/scripts/npm-audit-check.js" >"$WORK/out" 2>&1
}

if tiers "$WORK/moderate.json" "$WORK/moderate.json"; then
  bad "a moderate runtime advisory passed"
else
  grep -q 'leftpad' "$WORK/out" && grep -q 'runtime' "$WORK/out" &&
    ok "a moderate runtime advisory fails, naming the runtime tier" ||
    bad "moderate runtime failure output did not name the package and tier"
fi

if tiers "$WORK/moderate.json" "$WORK/empty.json"; then
  ok "a moderate dev-only advisory passes"
else
  bad "a moderate dev-only advisory failed"; cat "$WORK/out" >&2
fi

if tiers "$WORK/audit.json" "$WORK/empty.json"; then
  bad "a high dev-only advisory passed"
else
  ok "a high dev-only advisory fails"
fi

echo "npm-audit-check suite: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
