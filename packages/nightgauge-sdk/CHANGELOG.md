# Changelog

All notable changes to the Nightgauge SDK will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-01-20

### Added

- CLI commands for pipeline orchestration (`nightgauge-sdk run`,
  `nightgauge-sdk status`)
- Token usage tracking and cost estimation
- Real-time progress reporting via event emitters
- Pipeline stage result persistence

### Changed

- Improved error handling in PipelineOrchestrator
- Enhanced context file schema validation

## [0.1.0] - 2026-01-10

### Added

- ContextManager for pipeline state persistence
- PipelineOrchestrator for stage coordination
- Pipeline stages: issue-pickup, feature-planning, feature-dev, pr-create,
  pr-merge
- JSON schema validation for context files
- TypeScript type definitions for all public APIs
- Integration with Claude Agent SDK

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/nightgauge-sdk-v0.2.0...HEAD
[0.2.0]: https://github.com/nightgauge/nightgauge/compare/nightgauge-sdk-v0.1.0...nightgauge-sdk-v0.2.0
[0.1.0]: https://github.com/nightgauge/nightgauge/releases/tag/nightgauge-sdk-v0.1.0
