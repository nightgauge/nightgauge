# Feature-Dev — Context Write & Status Sync

Procedural detail for Phase 7 (Write Dev Context), Phase 8 (Sync Project Board
Status), and Phase 9 (Output Summary).

## Contents

- [Phase 7: Write Dev Context](#phase-7-write-dev-context)
- [Phase 8: Sync Project Board Status](#phase-8-sync-project-board-status)
- [Phase 9: Output Summary](#phase-9-output-summary)

---

## Phase 7: Write Dev Context

> **No commit or push in feature-dev.** Code is committed and pushed by
> `/nightgauge-feature-validate` after validation passes. This ensures only
> validated code reaches the remote branch and RALPH loop fixes are included.
> See Issue #1608.

**PURPOSE**: Write structured context file for downstream pipeline skills.

**CRITICAL**: This phase MUST execute before the output summary. Moving this
after the "IMPLEMENTATION COMPLETE" message causes the AI to stop executing
before the context file is written.

### Step 7.1: Write dev-{N}.json inline

All variables are set by this point. Write the context using `jq -n` for safe
JSON construction, then validate with `jq`:

```bash
CONTEXT_FILE=".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json"
TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
mkdir -p .nightgauge/pipeline

jq -n \
  --argjson issue_number "$ISSUE_NUMBER" \
  --argjson files_created "$(echo "${FILES_CREATED_JSON:-[]}" | jq -c .)" \
  --argjson files_modified "$(echo "${FILES_MODIFIED_JSON:-[]}" | jq -c .)" \
  --argjson files_deleted "$(echo "${FILES_DELETED_JSON:-[]}" | jq -c .)" \
  --argjson build_ran "${BUILD_RAN:-false}" \
  --arg build_status "${BUILD_STATUS:-skipped}" \
  --argjson build_commands "$(echo "${BUILD_COMMANDS_JSON:-[]}" | jq -c .)" \
  --argjson build_timestamp "$(if [ -n "${BUILD_TIMESTAMP:-}" ]; then echo "\"$BUILD_TIMESTAMP\""; else echo "null"; fi)" \
  --argjson tests_passed "${TESTS_PASSED:-0}" \
  --argjson tests_failed "${TESTS_FAILED:-0}" \
  --argjson coverage "${COVERAGE_PERCENT:-null}" \
  --arg test_command "${TEST_COMMAND:-}" \
  --argjson includes_integration "${INCLUDES_INTEGRATION:-false}" \
  --argjson includes_e2e "${INCLUDES_E2E:-false}" \
  --argjson test_files_run "${TEST_FILES_RUN:-0}" \
  --arg e2e_framework "${E2E_FRAMEWORK:-}" \
  --argjson e2e_tests_generated "${E2E_TESTS_GENERATED:-false}" \
  --arg code_standards "${CODE_STANDARDS_RESULT:-skipped}" \
  --arg security_review "${SECURITY_REVIEW_RESULT:-skipped}" \
  --arg type_check "${TYPE_CHECK_RESULT:-skipped}" \
  --arg dead_code_scan "${DEAD_CODE_RESULT:-not_run}" \
  --argjson feedback "$(echo "${FEEDBACK_JSON:-[]}" | jq -c .)" \
  --argjson retry_count "${RETRY_COUNT:-0}" \
  --argjson retry_reasons "$(echo "${RETRY_REASONS_JSON:-[]}" | jq -c .)" \
  --argjson knowledge_path "$(if [ -n "${KNOWLEDGE_PATH:-}" ]; then echo "\"$KNOWLEDGE_PATH\""; else echo "null"; fi)" \
  --argjson architectural_constraints "$(echo "${ARCH_CONSTRAINTS:-null}" | jq -c 'if . == "[]" then [] elif . == null then null else . end')" \
  --arg created_at "$TIMESTAMP" \
  '{
    schema_version: "1.8",
    issue_number: $issue_number,
    commit_sha: null,
    files_changed: {
      created: $files_created,
      modified: $files_modified,
      deleted: $files_deleted
    },
    build_verification: {
      ran: $build_ran,
      status: $build_status,
      commands_run: $build_commands,
      timestamp: $build_timestamp
    },
    tests_status: {
      passed: $tests_passed,
      failed: $tests_failed,
      coverage: $coverage,
      test_command: $test_command,
      includes_integration: $includes_integration,
      includes_e2e: $includes_e2e,
      test_files_run: $test_files_run,
      e2e_framework: (if $e2e_framework != "" then $e2e_framework else null end),
      e2e_tests_generated: $e2e_tests_generated
    },
    quality_checks: {
      code_standards: $code_standards,
      security_review: $security_review,
      type_check: $type_check,
      dead_code_scan: $dead_code_scan
    },
    feedback: $feedback,
    retry_count: $retry_count,
    retry_reasons: $retry_reasons,
    knowledge_path: $knowledge_path,
    architectural_constraints: $architectural_constraints,
    created_at: $created_at
  }' > "$CONTEXT_FILE"

jq . "$CONTEXT_FILE" > /dev/null && \
  echo "Dev context written: $CONTEXT_FILE" || \
  { echo "ERROR: dev context JSON invalid" >&2; exit 1; }
```

---

## Phase 8: Sync Project Board Status

Sync project board to "In progress" via Go binary `project sync-status`
(idempotent).

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
  "$BINARY" project sync-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true
fi
```

---

## Phase 9: Output Summary

Report implementation results: branch, files changed, quality check results,
context file path, and next step (`/nightgauge-feature-validate`).

> **Note**: No commit SHA is reported because code is not committed until
> feature-validate passes. The output summary should report files on disk, not a
> commit.

### Step 9.1: Signal Stage Complete

```bash
# Go binary: project move-status
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
  "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true
fi
```
