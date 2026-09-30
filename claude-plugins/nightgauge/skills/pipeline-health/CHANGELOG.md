# Changelog

All notable changes to the **nightgauge-pipeline-health** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- Health trends and skill-usage telemetry are read from this checkout's
  directory (`nightgauge layout path checkout health/trends.jsonl`,
  `skills/usage.jsonl`), not from `.nightgauge/` (#2037, ADR-024 § 7).
- Post-pipeline analysis and A/B experiment records are read from this
  checkout's `analysis/` (`nightgauge layout path checkout analysis`), not
  `.nightgauge/analysis/` (#2037, ADR-024 § 7).
- Per-clone pipeline state, plans, retros and logs are read at
  `$(nightgauge layout path <class> <name>)` and written through
  `nightgauge layout write|append`, not by `.nightgauge/...` path (#2037,
  ADR-024 § 7).
- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

## [1.1.0] - 2026-04

### Changed

- Phase 2.2: Replace Python `health_extract_scores.py` script with
  `nightgauge health trends --limit 50 --json` Go binary call. Eliminates
  Python dependency; malformed lines are skipped non-fatally by the binary.
- Phase 2.6: Replace jq `group_by` aggregation with
  `nightgauge health gate-metrics --json` Go binary call. Hit-rate
  calculation and deterministic sorting now handled by the binary.

## [1.0.0] - 2026-02

### Added

- Initial skill definition with full SKILL.md
- Command registration for `/nightgauge:pipeline-health`
- All 10 CLI arguments defined
- 7 analysis dimension specifications
- 9-phase workflow definition
- Output format schema (JSON + Markdown)
- Data source documentation for 7 telemetry sources
- Relationship documentation with pipeline-audit skill
