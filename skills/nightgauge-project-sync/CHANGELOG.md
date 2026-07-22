# Changelog

All notable changes to the nightgauge-project-sync skill will be documented in this
file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

## [1.0.0] - 2026-02-06

### Added

- Initial release of nightgauge-project-sync skill
- Bulk date sync from milestones to project Start/Target Date fields
- Status label to project board sync for all issues at once
- Dry-run report mode to preview changes without applying
- Idempotent operation - safe to run multiple times
- Filtering by milestone and/or labels
- Multiple sync modes: full, dates-only, status-only, report
- Integration with `.nightgauge/config.yaml` configuration
- GraphQL-based pagination for efficient bulk operations
- Graceful handling of missing milestones and configuration
- Clear output formatting with summary and detailed change logs
- Error handling with actionable recommendations

### Implementation Details

- Created deterministic hook script `sync-all-issues.sh` for all bulk operations
- SKILL.md handles user interaction and output formatting only
- Follows deterministic/probabilistic architecture pattern from
  docs/ARCHITECTURE.md
- Standalone utility skill - not part of Issue-to-PR pipeline
- No pipeline context files created or consumed

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/v1.0.0
