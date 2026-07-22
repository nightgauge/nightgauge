# Changelog

All notable changes to the **nightgauge-modernize-plan** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

## [1.0.0] - 2026-02-21

### Added

- Initial release of nightgauge-modernize-plan skill
- Consumes health-check, security-audit, and test-scaffold JSON outputs
- Generates 6-phase modernization plan (Safety Net through Optimization)
- Topological sorting for dependency-ordered task execution
- Quick wins identification and highlighting after executive summary
- Timeline estimation based on team size and sprint length
- Optional GitHub issue generation with user confirmation
- Dry-run mode for issue preview
- Human-readable markdown roadmap output (MODERNIZATION_PLAN.md)
- Structured JSON output for programmatic consumption (modernization-plan.json)
- Graceful degradation when input reports are missing
- Phase skip support via `--skip-phase` argument
- Configurable output format (summary, json, both)
