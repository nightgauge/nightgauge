#!/usr/bin/env bash
# Regression tests for scripts/lib/clean-install-bundle-rule.sh (#3135).
#
# Scheduled runs record the G1 bundle-read rows as SKIP; every other run kind
# hard-fails them; --smoke evaluates neither.
#
# Run: bash scripts/test-clean-install-bundle-rule.sh
# Also run by scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

# shellcheck source=lib/clean-install-bundle-rule.sh
. scripts/lib/clean-install-bundle-rule.sh
PASS=0
FAIL=0

ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
has() { if [[ "$1" == *"$2"* ]]; then ok "$3"; else bad "$3 (got: $1)"; fi; }
lacks() { if [[ "$1" != *"$2"* ]]; then ok "$3"; else bad "$3 (got: $1)"; fi; }

if bundle_rows_skip schedule; then ok "schedule skips the rows"; else bad "schedule skips the rows"; fi
T="$(bundle_rule_text schedule 0)"
has "$T" "this run: schedule" "schedule text names the run kind"
has "$T" "skipped on schedule runs" "schedule text states the skip rule"
lacks "$T" "every run kind" "text drops the every-run-kind rule"

for kind in "manual dispatch" "rc dispatch" "local" "push"; do
  if bundle_rows_skip "$kind"; then bad "$kind does not skip"; else ok "$kind does not skip"; fi
  T="$(bundle_rule_text "$kind" 0)"
  has "$T" "hard-fail on manual dispatch, RC dispatch and local" "$kind text states hard-fail"
  has "$T" "this run: $kind" "$kind text names the run kind"
done
if bundle_rows_skip ""; then bad "empty kind does not skip"; else ok "empty kind does not skip"; fi

T="$(bundle_rule_text schedule 1)"
has "$T" "not evaluated (--smoke)" "smoke text says not evaluated"
has "$T" "this run: schedule" "smoke text names the run kind"

echo
echo "passed: $PASS  failed: $FAIL"
[ "$FAIL" -eq 0 ]
