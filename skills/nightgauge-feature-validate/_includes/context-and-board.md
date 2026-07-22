# Reference: Write Validate Context & Sync Board (Phases 6, 7)

Procedural detail for **Phase 6 (Write Validate Context)** and **Phase 7 (Sync
Project Board Status)**. Read this when those phases fire.

The Exit Contract (write `validate-{N}.json` every run) is defined inline in
`SKILL.md`; this file is the jq write procedure that satisfies it.

## Contents

- [Phase 6: Write Validate Context](#phase-6-write-validate-context)
- [Phase 7: Sync Project Board Status](#phase-7-sync-project-board-status)

---

## Phase 6: Write Validate Context

**PURPOSE**: Write structured context file for downstream
`/nightgauge-pr-create`.

### Vocabulary — exact values required

Use only the canonical values below. The write script normalizes common
truncations (`pass` → `passed`) but never rely on normalization — always use the
full canonical form.

| Variable               | Valid values                                                                                   |
| ---------------------- | ---------------------------------------------------------------------------------------------- |
| `AC_COMPLETION_STATUS` | `passed`, `failed`, `skipped`, `not_applicable`                                                |
| `NOTES`                | A plain string (not a JSON array). Use newlines for multiple notes. Pass empty string if none. |

### Step 6.1–6.3: Write Context Inline

Determine validation status, construct JSON, and validate:

```bash
# Determine validation_status
VALIDATION_STATUS="passed"
if [ "${BUILD_RAN:-false}" = "true" ] && [ "${BUILD_PASSED:-false}" = "false" ]; then
  VALIDATION_STATUS="failed"
elif [ "${UNIT_TESTS_RAN:-false}" = "true" ] && [ "${UNIT_TESTS_PASSED:-false}" = "false" ]; then
  VALIDATION_STATUS="failed"
elif [ "${E2E_RAN:-false}" = "true" ] && [ "${E2E_PASSED:-false}" = "false" ] && [ "${E2E_SKIPPED:-false}" = "false" ]; then
  VALIDATION_STATUS="failed"
elif [ "${DEAD_CODE_BLOCKED:-false}" = "true" ]; then
  VALIDATION_STATUS="failed"
elif [ "${INTEGRATION_GATE_STATUS:-passed}" = "failed" ]; then
  # #2909: integration-test strict gate — see Phase 2.1 / Phase 4.9.
  VALIDATION_STATUS="failed"
elif [ "${MOBILE_MCP_RAN:-false}" = "true" ] && [ "${MOBILE_MCP_PASSED:-false}" = "false" ] && \
     [ "$(yq -r '.validation.mobile_mcp_tests // "strict"' .nightgauge/config.yaml 2>/dev/null || echo strict)" = "strict" ]; then
  # #24: mobile-mcp strict gate — Phase 2.4 set MOBILE_MCP_PASSED=false on a spec failure.
  VALIDATION_STATUS="failed"
elif [ "${VERIFY_UI_RAN:-false}" = "true" ] && [ "${VERIFY_UI_PASSED:-false}" = "false" ] && \
     [ "$(yq -r '.validation.verify_ui_tests // "strict"' .nightgauge/config.yaml 2>/dev/null || echo strict)" = "strict" ]; then
  # #4193: verify-ui strict gate — Phase 2.45 set VERIFY_UI_PASSED=false on a flow failure.
  VALIDATION_STATUS="failed"
fi

CONTEXT_FILE=".nightgauge/pipeline/validate-${ISSUE_NUMBER}.json"
TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)

jq -n \
  --argjson issue_number "$ISSUE_NUMBER" \
  --arg commit_sha "${COMMIT_SHA:-}" \
  --arg validation_status "$VALIDATION_STATUS" \
  --arg error_category "${ERROR_CATEGORY:-}" \
  --argjson build_ran "${BUILD_RAN:-false}" \
  --argjson build_passed "${BUILD_PASSED:-false}" \
  --arg build_command "${BUILD_CMD:-}" \
  --argjson build_duration_ms "${BUILD_DURATION_MS:-0}" \
  --argjson build_exit_code "${BUILD_EXIT_CODE:-0}" \
  --argjson unit_tests_ran "${UNIT_TESTS_RAN:-false}" \
  --argjson unit_tests_passed "${UNIT_TESTS_PASSED:-false}" \
  --arg unit_tests_framework "${UNIT_TEST_FRAMEWORK:-}" \
  --argjson unit_tests_run_count "${UNIT_TESTS_RUN_COUNT:-0}" \
  --argjson unit_tests_passed_count "${UNIT_TESTS_PASSED_COUNT:-0}" \
  --argjson integration_tests_ran "${INTEGRATION_TESTS_RAN:-false}" \
  --argjson integration_tests_passed "${INTEGRATION_TESTS_PASSED:-false}" \
  --arg integration_tests_framework "${INTEGRATION_TEST_FRAMEWORK:-}" \
  --argjson integration_tests_run_count "${INTEGRATION_TESTS_RUN_COUNT:-0}" \
  --argjson integration_tests_passed_count "${INTEGRATION_TESTS_PASSED_COUNT:-0}" \
  --argjson e2e_ran "${E2E_RAN:-false}" \
  --argjson e2e_passed "${E2E_PASSED:-false}" \
  --arg e2e_framework "${E2E_FRAMEWORK:-}" \
  --arg e2e_reason "${E2E_REASON:-}" \
  --argjson mobile_mcp_ran "${MOBILE_MCP_RAN:-false}" \
  --argjson mobile_mcp_passed "${MOBILE_MCP_PASSED:-false}" \
  --argjson mobile_mcp_specs_run "${MOBILE_MCP_SPECS_RUN:-0}" \
  --argjson mobile_mcp_specs_passed "${MOBILE_MCP_SPECS_PASSED:-0}" \
  --argjson mobile_mcp_specs_failed "${MOBILE_MCP_SPECS_FAILED:-0}" \
  --argjson mobile_mcp_results "$(echo "${MOBILE_MCP_RESULTS_JSON:-[]}" | jq -c .)" \
  --arg mobile_mcp_evidence_dir "${MOBILE_MCP_EVIDENCE_DIR:-}" \
  --arg mobile_mcp_skipped_reason "${MOBILE_MCP_SKIPPED_REASON:-}" \
  --argjson mobile_mcp_active "$([ "${MOBILE_MCP_RAN:-false}" = "true" ] || [ -n "${MOBILE_MCP_SKIPPED_REASON:-}" ] && echo true || echo false)" \
  --argjson verify_ui_ran "${VERIFY_UI_RAN:-false}" \
  --argjson verify_ui_passed "${VERIFY_UI_PASSED:-false}" \
  --arg verify_ui_repo "${VERIFY_UI_REPO:-}" \
  --arg verify_ui_flow "${VERIFY_UI_FLOW:-}" \
  --argjson verify_ui_report "$(echo "${VERIFY_UI_REPORT_JSON:-"{}"}" | jq -c .)" \
  --arg verify_ui_artifacts_dir "${VERIFY_UI_ARTIFACTS_DIR:-}" \
  --arg verify_ui_skipped_reason "${VERIFY_UI_SKIPPED_REASON:-}" \
  --argjson verify_ui_active "$([ "${VERIFY_UI_RAN:-false}" = "true" ] || [ -n "${VERIFY_UI_SKIPPED_REASON:-}" ] && echo true || echo false)" \
  --argjson dead_code_warnings "$(echo "${DEAD_CODE_JSON:-[]}" | jq -c .)" \
  --argjson preexisting_failures "$(echo "${PREEXISTING_FAILURES:-[]}" | jq -c .)" \
  --argjson skipped_phases "$(echo "${SKIPPED_PHASES:-[]}" | jq -c .)" \
  --arg ac_status "${AC_COMPLETION_STATUS:-not_applicable}" \
  --argjson ac_checked "${CHECKED:-0}" \
  --argjson ac_unchecked "${UNCHECKED:-0}" \
  --argjson ac_applicable "$([ "${AC_COMPLETION_STATUS:-not_applicable}" = "not_applicable" ] && echo false || echo true)" \
  --argjson manual_checklist "$(echo "${MANUAL_CHECKLIST_JSON:-[]}" | jq -c .)" \
  --argjson feedback "$(echo "${FEEDBACK_JSON:-[]}" | jq -c .)" \
  --arg notes "${NOTES:-}" \
  --arg pre_push_status "${PREPUSH_STATUS:-skipped}" \
  --argjson min_dur_flagged "${MINIMUM_DURATION_FLAGGED:-false}" \
  --argjson min_dur_actual "${MINIMUM_DURATION_ACTUAL_MS:-0}" \
  --argjson min_dur_p10 "${MINIMUM_DURATION_P10_MS:-0}" \
  --arg min_dur_warning "${MINIMUM_DURATION_WARNING:-}" \
  --arg created_at "$TIMESTAMP" \
  '{
    schema_version: "2.4",
    issue_number: $issue_number,
    commit_sha: $commit_sha,
    validation_status: $validation_status,
    errorCategory: (if $error_category != "" then $error_category else null end),
    build: {
      ran: $build_ran,
      passed: $build_passed,
      command: $build_command,
      duration_ms: $build_duration_ms,
      exit_code: $build_exit_code
    },
    minimum_duration_check: (if $min_dur_p10 > 0 then {
      flagged: $min_dur_flagged,
      actual_build_time_ms: $min_dur_actual,
      p10_baseline_ms: $min_dur_p10,
      warning: (if $min_dur_warning != "" then $min_dur_warning else null end)
    } else null end),
    unit_tests: { ran: $unit_tests_ran, passed: $unit_tests_passed, framework: $unit_tests_framework, tests_run: $unit_tests_run_count, tests_passed: $unit_tests_passed_count },
    integration_tests: { ran: $integration_tests_ran, passed: $integration_tests_passed, framework: $integration_tests_framework, tests_run: $integration_tests_run_count, tests_passed: $integration_tests_passed_count },
    e2e_tests: { ran: $e2e_ran, passed: $e2e_passed, framework: $e2e_framework, reason: $e2e_reason },
    mobile_mcp: (if $mobile_mcp_active then {
      ran: $mobile_mcp_ran,
      passed: $mobile_mcp_passed,
      specs_run: $mobile_mcp_specs_run,
      specs_passed: $mobile_mcp_specs_passed,
      specs_failed: $mobile_mcp_specs_failed,
      results: $mobile_mcp_results,
      evidence_dir: $mobile_mcp_evidence_dir,
      skipped_reason: (if $mobile_mcp_skipped_reason != "" then $mobile_mcp_skipped_reason else null end)
    } else null end),
    verify_ui: (if $verify_ui_active then {
      ran: $verify_ui_ran,
      passed: $verify_ui_passed,
      repo: $verify_ui_repo,
      flow: $verify_ui_flow,
      report: $verify_ui_report,
      artifacts_dir: $verify_ui_artifacts_dir,
      skipped_reason: (if $verify_ui_skipped_reason != "" then $verify_ui_skipped_reason else null end)
    } else null end),
    ac_completion_check: { status: $ac_status, checked_count: $ac_checked, unchecked_count: $ac_unchecked, applicable: $ac_applicable },
    dead_code_warnings: $dead_code_warnings,
    preexisting_failures: $preexisting_failures,
    pre_push_status: $pre_push_status,
    skipped_phases: $skipped_phases,
    manual_checklist: $manual_checklist,
    feedback: $feedback,
    notes: $notes,
    created_at: $created_at
  }' > "$CONTEXT_FILE"

jq . "$CONTEXT_FILE" > /dev/null && \
  echo "✓ Context file written: $CONTEXT_FILE [status=$VALIDATION_STATUS]" || \
  { echo "ERROR: validate context JSON invalid" >&2; exit 1; }
```

Schema version 2.4 (adds the `verify_ui` block, Issue #4193). Schema version
2.3 added the `mobile_mcp` block, Issue #24.

---

## Phase 7: Sync Project Board Status

Sync project board to "In progress" via Go binary `project move-status`
(idempotent, best-effort).

Update the project board Status field to "In progress" via
`nightgauge project move-status` (sets the board field, not a label). Never
fail validation if project sync cannot run in the current environment.

```bash
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
if [ -n "$BINARY" ]; then
  "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || \
    echo "WARNING: Project board sync skipped (non-blocking)."
fi
```
