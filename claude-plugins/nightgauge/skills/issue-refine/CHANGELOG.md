# Changelog — nightgauge-issue-refine

All notable changes to this skill are documented here.

## [Unreleased]

### Changed

- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

## [1.0.0] — 2026-04-07

### Added

- Initial release of the issue refinement skill
- Phase 1: GitHub auth validation and issue number resolution (from argument or
  branch)
- Phase 2: Issue type detection from labels with keyword-based fallback
- Phase 3: Codebase-aware analysis using `Glob`/`Grep` to find related files
- Phase 4: Structured body construction with Summary, User Story (features),
  Acceptance Criteria, Technical Notes, Root Cause Analysis (bugs), Complexity
  Estimate, and Related Issues sections
- Phase 5: GitHub issue update via `gh issue edit`, type label addition, and
  graceful `mark-refined` binary call (depends on #2533)
- Phase 6: Self-assessment epilogue
- All file references sourced exclusively from actual codebase search results
- Original issue content preserved verbatim in `<details>` block
- Issue title never modified
