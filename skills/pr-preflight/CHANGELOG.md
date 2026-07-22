# Changelog

All notable changes to the **pr-preflight** skill will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.3.0] - 2026-06

### Added

- **Dependency-guard check (Check 4)** — runs `nightgauge preflight dependency-guard`
  in the gate chain to block a newly-added dependency that 404s on its registry
  (a hallucinated dep) or is within one edit of a popular package (a possible
  slopsquat). Network-inconclusive lookups are warn-only (#4095).

## [1.1.0] - 2026-01

### Added

- **Nightgauge skill version consistency check** (Section 9) - validates SKILL.md versions match command file versions
  - Auto-detects nightgauge plugin repositories (only runs when `skills/nightgauge-*/SKILL.md` exists)
  - Maps each skill to its command file in `claude-plugins/nightgauge/commands/`
  - Reports mismatches with clear error messages showing expected vs actual versions
  - Detects missing version lines in command files
  - Provides auto-fix script to update command files to match SKILL.md versions
  - Added to Quick Validation script as step 6

## [1.0.0] - 2025-01

### Added

- Initial release as universal pre-flight validation skill
- Broken links detection in markdown files
- JSON syntax validation
- YAML syntax validation
- Semantic versioning check for package.json
- Sensitive data detection (API keys, secrets, passwords)
- TODO/FIXME comment detection
- Documentation completeness check
- Large file detection (>1MB)
- Quick validation script combining all checks
- Repository-specific validation guidance

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/nightgauge/nightgauge/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/v1.0.0
