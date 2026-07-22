# Changelog

All notable changes to the **nightgauge-test-gen** skill will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.0.0] - 2026-01

### Added

- Initial release as part of the Issue-to-PR pipeline
- 10-phase comprehensive test generation workflow
- Multi-framework support (Jest, Pytest, dotnet test, Gradle)
- Parallel subagents for unit, integration, and E2E tests
- Coverage analysis with gap identification
- Edge case test generation (string, numeric, collection, async)
- Non-destructive mode (asks before overwriting existing tests)
- CLI arguments: `--files`, `--target-coverage`, `--types`, `--skip-e2e`, `--dry-run`
- Framework-specific test patterns and assertions
- Automatic mocking library detection
- Test execution and self-correction loop
- Multi-tool support (Claude Code, OpenAI Codex, GitHub Copilot, Cursor)

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/v1.0.0
