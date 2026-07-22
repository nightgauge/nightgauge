# Changelog

All notable changes to the **nightgauge-feature-dev** skill will be documented in
this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.15.0]

### Added

- **Architecture-approval gate (Phase -1, #4098)** — a high-impact architectural
  decision (≥2 trade-off signals in the issue/ADR, or routing `risk_high`) stays
  human-owned: `nightgauge approval-gate <issue>` blocks feature-dev until a
  human grants approval out-of-band (the `approved:architecture` label or an
  approval file). A deterministic hard gate, so it holds even under
  `auto_accept_stages: true`. Config: `pipeline.architecture_approval`
  (default `enabled: true`).

## [1.14.0]

### Added

- **Grounding gate (Phase -1, #4099)** — before loading context or editing,
  `nightgauge ground <issue>` confirms the agent is on the issue's feature
  branch (not the base) with the issue context present, closing the #3863
  "am I on the right issue/branch?" gap. A wrong/protected branch or missing
  context blocks feature-dev (re-ground); a missing-AC premise recommends
  pulling human context. Config: `pipeline.grounding_gate.enabled` (default `true`).

## [Earlier Unreleased]

### Added

- **Conflict-resolution re-dispatch (Phase 0.7.1b)** (#4072, epic #4067). When
  pr-merge cannot land a rebase conflict in-place, it emits a
  `CONFLICT_RESOLUTION_NEEDED` feedback signal targeting feature-dev plus a
  `conflict-context-{N}.json` (conflicting files + both sides). The intake now
  detects this signal, checks out the **existing PR branch** (never a fresh
  branch from main), surfaces the conflicting files and both sides, and resolves
  the conflict preserving both changes before flowing forward to
  feature-validate → pr-create → pr-merge.
- **`orchestration:` frontmatter block (`mode: fanout`, `phase: quality-review`)**
  (epic #3899, issue #3917)
  - Declares the 6 Phase 5 reviewers (code quality, security, test,
    documentation, performance, accessibility) as portable units plus a merge
    judge, each pointing at the same `_includes/review-and-correction.md` the
    prose references
  - Replaces the Claude-only "spawn all 6 reviewers in a single message using the
    Task tool" lock-in with the provider-neutral fan-out intent; the prose body
    stays the single-agent portability floor (Copilot/Cursor run the six reviews
    sequentially)
  - Skill version bumped to 1.12.0
- **E2E test generation and execution guidance (Phase 4.5)** (issue #9)
  - Detect UI files from changed file set (`*.tsx`, `*.jsx`, `*.vue`, `routes/*`)
  - Generate E2E test scenario suggestions via Task subagent for each UI file
  - Execute E2E tests via Playwright or Cypress when framework is configured
  - Non-blocking: E2E failures warn but do not stop the pipeline
  - Sets `INCLUDES_E2E=true` in dev context for `feature-validate` awareness
  - Backend-only changes (no UI files) skip this phase entirely

### Fixed

- **Transient network push failures no longer block context write**
  - Updated Phase 7.5 push guidance to distinguish non-recoverable vs transient
    push errors
  - Allows feature-dev to continue to Phase 8 and write `dev-{N}.json` when
    `git push origin HEAD` fails due to DNS/network outage
  - Requires explicit final summary note that remote push is deferred to
    `git push origin HEAD` (before/during `/nightgauge-pr-create`)
- **Phase reordering to fix dev-{N}.json not being written** (issue #132)
  - Moved "Write Dev Context" from Phase 9 to Phase 8 (immediately after Commit)
  - Renumbered "Sync Project Board Status" from Phase 7.5 to Phase 9
  - Renumbered "Output Summary" from Phase 8 to Phase 10
  - Root cause: When Output Summary displayed "IMPLEMENTATION COMPLETE", AI
    stopped executing before Phase 9 ran
  - Added error handling if context file write fails
  - Added context file confirmation to output summary

## [1.3.0] - 2026-02

### Added

- **Phase 7.5: Sync Project Board Status** - Ensures project board shows "In
  progress"
  - Uses deterministic `sync-project-status.sh` hook script
  - Idempotent safety net if `/nightgauge-feature-planning` was skipped
  - Maps `status:in-progress` label to project Status field
  - Graceful handling if issue not in project or hook not available
- Pipeline status synchronization across all skills (issue #103)

## [1.2.0] - 2026-01

### Added

- Context-isolated pipeline support with JSON handoff files
- Phase 0: Read Planning Context from `.nightgauge/pipeline/planning-{N}.json`
- Phase 9: Write Dev Context to `.nightgauge/pipeline/dev-{N}.json`
- Input/output contracts documented in skill

## [1.1.0] - 2026-01

### Added

- **Multi-file parallel implementation** - Independent files created
  simultaneously via subagents
- Dependency analysis - Automatically detects file dependencies from PLAN.md
- Wave-based execution - Files grouped into parallel waves based on dependencies
- Conflict detection and resolution - Handles naming conflicts in
  parallel-created files
- `--sequential` flag - Force sequential implementation (disable parallel)
- Fallback to sequential - Automatic fallback if parallel execution fails

### Changed

- Phase 3 (Implementation) restructured into multiple steps for
  parallel/sequential execution
- Progress reporting now indicates whether files were created in parallel or
  sequentially

### Previous Unreleased (now in 1.1.0)

- Stateless branch name inference - extracts issue number from branch name
- Issue-based plan discovery - looks for `.nightgauge/plans/{issue-number}-*.md`
  first
- Branch-plan alignment verification - warns if plan's issue doesn't match
  current branch

## [1.0.0] - 2025-01

### Added

- Initial release as part of the Issue-to-PR pipeline
- PLAN.md reading and execution
- `--plan` flag for specifying custom plan file
- Automatic standards loading from docs/ folder
- Implementation following documented patterns
- Test writing alongside implementation
- Quality review against CODE_STANDARDS.md
- Security review against SECURITY_AND_ERROR_HANDLING.md
- Self-correction loop for failed checks
- Commit creation following GIT_WORKFLOW.md format
- `--skip-review` flag (not recommended)
- Multi-tool support (Claude Code, OpenAI Codex, GitHub Copilot, Cursor)

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v1.3.0...HEAD
[1.3.0]: https://github.com/nightgauge/nightgauge/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/nightgauge/nightgauge/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/nightgauge/nightgauge/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/v1.0.0
