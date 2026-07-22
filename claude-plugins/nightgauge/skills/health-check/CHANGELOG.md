# Changelog

All notable changes to the **nightgauge-health-check** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.0.0] - 2026-02

### Added

- Initial skill definition with full SKILL.md
- Command registration for `/nightgauge:health-check`
- 6 CLI arguments defined (--path, --package, --dimensions, --format,
  --skip-audit, --output)
- 6 health dimension specifications with scoring rubrics
- 9-phase workflow definition (environment detection through monorepo
  aggregation)
- Output format schema (JSON + Markdown)
- Multi-ecosystem support: Node.js, Python, Go, Rust, Java/Maven/Gradle
- Monorepo detection and per-package assessment with aggregation
- Smart-setup integration (opportunistic reuse of existing AGENTS.md/CLAUDE.md)
- Graceful degradation when ecosystem tools are unavailable
