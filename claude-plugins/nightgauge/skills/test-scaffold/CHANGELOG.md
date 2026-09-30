# Changelog

All notable changes to the **nightgauge-test-scaffold** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- The JSON report is written to this checkout's
  `reports/test-scaffold-report.json` inside the git directory (`nightgauge
layout path checkout reports/test-scaffold-report.json`) through `nightgauge layout write checkout`,
  not to `.nightgauge/test-scaffold-report.json`; the health-check report is
  read from `reports/health-report.json` there too (#2037, ADR-024 § 7).

## [1.0.0] - 2026-02-21

### Added

- Initial release of nightgauge-test-scaffold skill
- Multi-ecosystem support (JS/TS, Python, Go, Rust, Java)
- Risk-based prioritization with four factors: business criticality, code
  complexity, change frequency, and dependency depth
- Characterization test generation with parallel subagents (model: sonnet)
- Structured JSON output to `.nightgauge/test-scaffold-report.json`
- Monorepo support with per-package aggregation (model: haiku)
- Graceful degradation when coverage tools are unavailable
- Integration with health-check report data when available
- Configurable priority threshold for test generation (`--priority`)
- Configurable maximum test file count (`--max-tests`)
- Analysis-only mode (`--skip-generation`)
- `tests/scaffold/` directory isolation for characterization tests
- `*.scaffold.test.*` naming convention for easy identification and cleanup
