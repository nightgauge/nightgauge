# Changelog

All notable changes to the **nightgauge-feature-validate** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.18.0]

### Added

- **Adversarial Review Gate (Step 2.7.3, #4097)** — activates the dormant
  `nightgauge-adversarial-review` critics as a default validate gate. A
  four-lens fresh-eyes critique (correctness/security/reuse/tests) runs as a
  single-agent preflight; its verdict is recorded via
  `nightgauge gate record-metric --gate adversarial-review`, and a `catch`
  trips the deterministic `FeatureValidateGate` via gate-metrics — keeping the
  gate pure while gating quality-of-reasoning. Config: `pipeline.adversarial_review.enabled`
  (default `true`).

## [Earlier Unreleased]

### Added

- `orchestration:` frontmatter block (`mode: pipeline`) modelling validation as
  an ordered build → tests → CI-parity pipeline closed by an adversarial gate
  judge (`judge.gate: true`), consumed by the capability-routed `WorkflowEngine`
  and feeding the Go `FeatureValidateGate.Verify()` loop (epic #3899). Each
  stage's `promptRef` points at the same `_includes/*.md` the prose phases read,
  so the prose stays the portability floor. Skill version bumped to 1.17.0.

### Fixed

- Phase 6 project-board sync is now explicitly non-blocking
  - Uses canonical status label `status:in-progress` (not `in progress`)
  - Adds best-effort hook invocation guidance so sync failures do not fail
    `feature-validate` after context write
- Exit contract for `validate-{N}.json` write is now explicit (#3114)
  - Top-of-skill "Exit Contract — Read This First" calls out that the stage
    is incomplete until the context file exists, and that the orchestrator
    fallback is repo-blind so the skill must always write the file itself
  - Phase 8 now performs an explicit `test -s "$CONTEXT_FILE"` before
    declaring completion; missing file → exit 1 with a clear remediation

## [1.15.0] - 2026-04

### Added

- **Integration-test strict gate (#2909)**. Phase 2.1 now delegates detection,
  classification, and gate evaluation to the new `IntegrationTestGate` module
  in `@nightgauge/nightgauge-sdk`.
  - New config key `validation.integration_tests`: `"strict"` (default),
    `"best_effort"`, or `"off"`.
  - Env override: `NIGHTGAUGE_VALIDATION_INTEGRATION_TESTS`.
  - In `strict` mode, a repo that declares integration tests in CI but cannot
    run them locally (missing docker, postgres unreachable, etc.) fails the
    stage with `VALIDATION_STATUS=failed` instead of silently publishing a PR.
- Phase 4.9 and Phase 6 status computation now respect
  `INTEGRATION_GATE_STATUS`.

## [1.5.0] - 2026-02

### Added

- Step 1.6.4: Scope Dead Code Findings — cross-references dead code findings
  against changed files from `dev-{N}.json` to classify as current-issue
  (severity: error) or pre-existing (severity: warning)
- Step 1.6.5 (gating): Dead Code Gating Decision — configurable gate controlled
  by `validation.dead_code` config (default: `"gate"`)
  - `"gate"` — Fail validation if current-issue dead code found
  - `"warn"` — Log warnings only, do not block (backwards-compatible)
  - `"off"` — Skip dead code detection entirely
- Dead code gating integrated into Phase 5.1 status determination
- `build`, `dead_code_warnings`, and `unit_tests` fields added to SDK validate
  context schema (accepts versions 1.0-1.2)

### Changed

- Phase 1.6 upgraded from WARNING to CONFIGURABLE GATE
- Addresses issue #719 where dead code findings were not gating the pipeline

## [1.4.0] - 2026-02

### Added

- Ralph Wiggum Loop integration for self-healing build and test failures

## [1.3.0] - 2026-02

### Added

- Step 1.6.5: VSCode Command Argument Safety Check
  - Detects commands used in view/title and view/item/context menus
  - Checks if command handlers validate argument types before use
  - Warns when handlers lack Array.isArray, typeof, or instanceof checks
  - Addresses class of bugs where toolbar invocation passes unexpected types (PR
    #395)
- Enhanced VSCode extension manual checklist (Phase 3.1)
  - "Commands handle arguments correctly from all invocation contexts"
  - "Command handlers validate input types before processing"
  - "View/title toolbar commands handle non-array first arguments gracefully"

## [1.2.0] - 2026-02

### Added

- Phase 1.6: Dead Code Detection (warning phase)
  - Detects VSCode extension commands declared in package.json but never
    registered
  - Detects exported functions/classes that are never imported internally
  - Warns but does not block validation (addresses issue #125 root cause)
- `dead_code_warnings` field in validate-{N}.json output schema

### Changed

- Schema version bumped from 1.1 to 1.2 for validate-{N}.json

## [1.1.0] - 2026-02

### Added

- Phase 1.5: Build Verification (hard gate)
  - Detects build commands for Node.js, TypeScript, Python, Go, Rust projects
  - Build failure is a hard gate - cannot be bypassed by --auto-pass
  - Addresses issue #125 where build errors were merged

## [1.0.0] - 2026-02

### Added

- Initial release of nightgauge-feature-validate skill
- Reads dev context from /nightgauge-feature-dev
- Detects testing frameworks (Playwright, Cypress, Jest, Vitest, pytest)
- Detects project types (VSCode extension, CLI, API, UI, generic)
- Runs integration tests if configured
- Runs E2E tests if configured (Playwright, Cypress)
- Generates component-specific manual checklists
- Prompts user for manual verification
- Writes validate-{N}.json context for downstream /nightgauge-pr-create
- Supports --skip-manual flag for low-risk changes
- Supports --e2e-only flag for E2E-focused validation
- Supports --checklist-only flag for manual-only validation
- Supports --auto-pass flag for CI/automated pipelines
- Syncs project board status (idempotent)
