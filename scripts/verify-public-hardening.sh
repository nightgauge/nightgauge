#!/usr/bin/env bash
# Read-only verification for the private staging candidate or final public repo.
set -uo pipefail

REPO="${1:-nightgauge/nightgauge}"
EXPECTED_VISIBILITY="${2:-PUBLIC}"
FAIL=0

pass() { printf 'PASS  %s\n' "$1"; }
fail() { printf 'FAIL  %s (got: %s)\n' "$1" "$2"; FAIL=1; }
check() { [[ "$3" == "$2" ]] && pass "$1" || fail "$1" "$3"; }
api() { gh api "$@" 2>/dev/null; }

REPOSITORY="$(gh repo view "$REPO" --json id,nameWithOwner,visibility,defaultBranchRef,isArchived 2>/dev/null)" \
  || { echo "FATAL: cannot read $REPO" >&2; exit 1; }
check "repository identity" "$REPO" "$(jq -r .nameWithOwner <<<"$REPOSITORY")"
check "visibility" "$EXPECTED_VISIBILITY" "$(jq -r .visibility <<<"$REPOSITORY")"
check "not archived" "false" "$(jq -r .isArchived <<<"$REPOSITORY")"
check "default branch" "main" "$(jq -r '.defaultBranchRef.name // ""' <<<"$REPOSITORY")"

SETTINGS="$(api "repos/$REPO")"
check "squash merge enabled" "true" "$(jq -r .allow_squash_merge <<<"$SETTINGS")"
check "merge commits disabled" "false" "$(jq -r .allow_merge_commit <<<"$SETTINGS")"
check "rebase merge disabled" "false" "$(jq -r .allow_rebase_merge <<<"$SETTINGS")"
check "delete branch on merge" "true" "$(jq -r .delete_branch_on_merge <<<"$SETTINGS")"

PERMISSIONS="$(api "repos/$REPO/actions/permissions/workflow")"
check "default workflow token is read-only" "read" "$(jq -r .default_workflow_permissions <<<"$PERMISSIONS")"

RUNNERS="$(api "repos/$REPO/actions/runners")"
check "repository runner count" "0" "$(jq -r .total_count <<<"$RUNNERS")"

WORKFLOW_HITS="$(gh api "repos/$REPO/git/trees/main?recursive=1" --jq '.tree[] | select(.path|startswith(".github/workflows/")) | .path' 2>/dev/null | while read -r path; do gh api "repos/$REPO/contents/$path" --jq .content | tr -d '\n' | base64 --decode; done | grep -E 'self-hosted|runs-on:[[:space:]]*\[' || true)"
[[ -z "$WORKFLOW_HITS" ]] && pass "workflow runner guard" || fail "workflow runner guard" "$WORKFLOW_HITS"

ENVIRONMENTS="$(api "repos/$REPO/environments")"
jq -e '.environments[] | select(.name == "production")' <<<"$ENVIRONMENTS" >/dev/null \
  && pass "production environment exists" || fail "production environment exists" "absent"

VARIABLES="$(api "repos/$REPO/actions/variables")"
MARKETPLACE="$(jq -r '.variables[]? | select(.name == "MARKETPLACE_PUBLISH") | .value' <<<"$VARIABLES")"
check "Marketplace publication disabled" "false" "${MARKETPLACE:-absent}"

if [[ "$EXPECTED_VISIBILITY" == "PUBLIC" ]]; then
  SECURITY="$(api "repos/$REPO")"
  check "secret scanning" "enabled" "$(jq -r '.security_and_analysis.secret_scanning.status // "absent"' <<<"$SECURITY")"
  check "push protection" "enabled" "$(jq -r '.security_and_analysis.secret_scanning_push_protection.status // "absent"' <<<"$SECURITY")"
  PVR="$(api "repos/$REPO/private-vulnerability-reporting")"
  check "private vulnerability reporting" "true" "$(jq -r .enabled <<<"$PVR")"
fi

RULESETS="$(api "repos/$REPO/rulesets")"
for expected in "main-branch-protection" "release-tag-protection"; do
  jq -e --arg name "$expected" '.[] | select(.name == $name)' <<<"$RULESETS" >/dev/null \
    && pass "ruleset: $expected" || fail "ruleset: $expected" "absent"
done

if [[ "$FAIL" -eq 0 ]]; then
  echo "ALL APPLICABLE HARDENING ASSERTIONS PASSED"
else
  echo "HARDENING ASSERTIONS FAILED"
fi
exit "$FAIL"
