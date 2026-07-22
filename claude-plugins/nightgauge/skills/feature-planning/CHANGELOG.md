# Changelog

All notable changes to the **nightgauge-feature-planning** skill will be documented
in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.8.0] - 2026-02-04

### Added

- **Adaptive documentation reading** (issue #162) - Intelligently assesses issue
  complexity to optimize documentation reading scope
  - Phase 0.5: Complexity Assessment (deterministic label-based decision tree)
  - Extracts size, type, and priority labels from issue context
  - Computes Fibonacci complexity score (XS=1, S=2, M=3, L=5, XL=8)
  - Determines documentation scope: minimal/targeted/standard/extended
  - Zero LLM tokens consumed for assessment (deterministic bash logic)
- Four documentation scope levels:
  - **Minimal** (~1,500 tokens): GIT_WORKFLOW + SECURITY (for XS bugs)
  - **Targeted** (~2,500 tokens): Core docs + testing (for S bugs/docs)
  - **Standard** (~5,000 tokens): All docs/ (M features - current behavior)
  - **Extended** (~10,000+ tokens): All docs/ + deep exploration (L/XL/critical)
- Token efficiency improvements:
  - 70% reduction for XS bugs
  - 50% reduction for S bugs/docs
  - 35% average reduction across mixed workload
  - 0% change for M+ features (backward compatible)
- `complexity_assessment` field added to `planning-{N}.json` context file:
  - `size_label`, `type_label`, `priority_label`
  - `computed_score`, `documentation_scope`, `rationale`
  - `estimated_token_savings`

### Changed

- Phase 2 renamed from "Documentation Discovery (Parallel)" to "Documentation
  Discovery (Adaptive)"
- Phase 2 now conditionally launches 0-3 subagents based on complexity scope
- Step 2.1: Determine reading strategy based on `DOC_SCOPE` variable
- Step 2.2: Execute adaptive reading (minimal/targeted/standard/extended)
- Step 2.3: Launch standard/extended subagents (3 parallel subagents)
- Steps 2.3→2.4 and 2.4→2.5 renumbered to accommodate adaptive reading logic
- Planning context output (Step 8.3) now includes `complexity_assessment` field

### Documentation

- Added
  [docs/ADAPTIVE_DOCUMENTATION_READING.md](../../docs/ADAPTIVE_DOCUMENTATION_READING.md)
  design doc explaining the feature

## [1.7.0] - 2026-02-02

### Added

- **Codebase pattern mining** (issue #35) - Searches for existing
  implementations before planning to extract patterns and conventions
  - Step 4.1.1: Extract pattern search terms from requirements
  - Step 4.1.2: Launch parallel pattern mining subagents (Domain + Testing)
  - Step 4.1.3: Collect and merge results with graceful failure handling
  - Step 4.1.4: Generate pattern catalog for PLAN.md
  - "Patterns Found" section added to PLAN.md template output
  - Sequential fallback using Glob/Grep if parallel execution fails
- Two specialized mining subagents:
  - Domain Pattern Miner: Services, controllers, repositories, error handling
  - Testing Pattern Miner: Test structure, fixtures, mocking, assertions
- Follows existing parallel subagent pattern from Phase 2 documentation reading

### Changed

- Phase 4 renamed from "Targeted Code Exploration" to "Codebase Pattern Mining &
  Code Exploration"
- Existing Phase 4 steps renumbered (4.1→4.2, 4.2→4.3, etc.) to accommodate new
  pattern mining step

## [1.6.0] - 2026-02-02

### Added

- **Decision Log capture** (issue #36) - Records architectural decisions made
  during planning for future reference
  - Step 5.4: Capture requirement decisions from clarifying questions
  - Step 6.4: Capture architecture approach decisions
  - Decision Log section added to PLAN.md template output
  - Decisions array added to `planning-{N}.json` context file
  - Schema version bumped to 1.1 for backward compatibility tracking
- Each decision captures: topic, options considered, selection, and rationale

## [1.5.0] - 2026-02-02

### Fixed

- **Phase 8: User Approval** - Removed `AskUserQuestion` approval dialog that
  could cause AI to continue implementing in the same conversation, violating
  context isolation (issue #133)
  - Replaced with explicit "PLANNING COMPLETE" output message
  - Clear instructions to start new conversation for `/nightgauge:feature-dev`
  - Context file written before final output to ensure pipeline handoff works
  - Renamed Phase 8 to "Plan Completion" and renumbered steps for clarity

## [1.4.1] - 2026-02-02

### Fixed

- **Context isolation enforcement** - Planning skill now terminates cleanly
  without requesting user approval, preventing accidental continuation into
  implementation phase

## [1.4.0] - 2026-02

### Added

- **Phase 8.5: Sync Project Board Status** - Automatically updates GitHub
  Project board to "In progress" after plan approval
  - Uses deterministic `sync-project-status.sh` hook script
  - Maps `status:in-progress` label to project Status field
  - Graceful handling if issue not in project or hook not available
- Pipeline status synchronization across all skills (issue #103)

## [1.3.0] - 2026-01

### Added

- Context-isolated pipeline support with JSON handoff files
- Phase 0: Read Issue Context from `.nightgauge/pipeline/issue-{N}.json`
- Phase 9: Write Planning Context to `.nightgauge/pipeline/planning-{N}.json`
- Input/output contracts documented in skill

## [1.2.0] - 2026-01

### Added

- **Coverage baseline capture** (Step 4.5) - records test coverage metrics
  during planning for regression detection
  - Auto-detects coverage tools (Jest, pytest, nyc, vitest, go test)
  - Parses coverage output to extract statements, branches, functions, lines
  - Stores baseline in PLAN.md Coverage Baseline section
  - Graceful degradation when no coverage tool detected
- Coverage Baseline section added to PLAN.md template output
- Support for Go coverage tool parsing

## [1.1.0] - 2026-01

### Added

- Parallel documentation reading in Phase 2 using 3 subagents (~40% faster
  wall-clock time)
  - Agent 1: ARCHITECTURE.md + CODE_STANDARDS.md
  - Agent 2: SECURITY_AND_ERROR_HANDLING.md + TESTING.md
  - Agent 3: CLAUDE.md/AGENTS.md + docs/README.md
- Fallback to sequential reading if parallel execution fails
- Stateless branch name inference - extracts issue number from branch name
  (e.g., `feat/42-description` → issue #42)
- Automatic GitHub issue fetching using `gh issue view` when issue number
  detected
- Graceful fallback to user prompt when no issue context available
- Test gap detection subagent in Phase 4 (Step 4.4)
  - Analyzes existing test coverage for files to be modified
  - Identifies untested functions, branches, and edge cases
  - Reports priority test requirements
- "Test Requirements (from Gap Analysis)" section in generated PLAN.md output

### Changed

- Clarified `.nightgauge/plans/` as the explicit default location for plans
  (removed ambiguous "or project root" option)
- Added note that plans are working documents cleaned up by `/nightgauge-pr-create`

## [1.0.0] - 2025-01

### Added

- Initial release as part of the Issue-to-PR pipeline
- Documentation-first planning approach (80-90% token savings)
- Automatic documentation readiness scoring
- Sequential reading of docs/ files (ARCHITECTURE.md, CODE_STANDARDS.md, etc.)
- Requirements-to-patterns mapping
- Targeted code exploration only for undocumented areas
- Subagent delegation for code exploration
- 2-3 implementation approach options
- PLAN.md generation with detailed implementation steps
- User approval workflow before implementation
- `--skip-exploration` flag for clear requirements
- Multi-tool support (Claude Code, OpenAI Codex, GitHub Copilot, Cursor)

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v1.7.0...HEAD
[1.7.0]: https://github.com/nightgauge/nightgauge/compare/v1.6.0...v1.7.0
[1.6.0]: https://github.com/nightgauge/nightgauge/compare/v1.5.0...v1.6.0
[1.5.0]: https://github.com/nightgauge/nightgauge/compare/v1.4.1...v1.5.0
[1.4.1]: https://github.com/nightgauge/nightgauge/compare/v1.4.0...v1.4.1
[1.4.0]: https://github.com/nightgauge/nightgauge/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/nightgauge/nightgauge/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/nightgauge/nightgauge/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/nightgauge/nightgauge/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/v1.0.0
