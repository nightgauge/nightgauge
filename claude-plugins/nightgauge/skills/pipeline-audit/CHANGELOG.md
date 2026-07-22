# Changelog

All notable changes to the **nightgauge-pipeline-audit** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

## [1.2.0] - 2026-03-04

### Added

- Size Estimate Accuracy analysis (Issue #1591)
  - Per-size accuracy table with median cost and accuracy rate
  - Oversize detection — issues labeled bigger than actual cost suggests
  - Undersize detection — issues labeled smaller than actual cost suggests
  - Weekly sizing accuracy trend analysis
- New analysis category: `size_estimation_accuracy`
- New findings category: `size_estimation_accuracy`
- `size_estimation_accuracy` section in JSON report schema
- SIZE ESTIMATE ACCURACY section in human-readable summary output
- Python test suite for accuracy calculation logic

## [1.0.0] - 2026-02-13

### Added

- Initial pipeline-audit skill
- 5 analysis categories: token efficiency, stage performance, cost optimization,
  quality correlation, trend analysis
- CLI filtering: `--runs`, `--since`, `--issue`
- `--create-issues` flag for auto-creating improvement issues with severity
  filter
- `--severity` flag to control minimum severity for issue creation
- `--format` flag for output format control (summary, json, both)
- Deterministic data extraction via Bash/jq
- Probabilistic AI interpretation for findings and recommendations
- Graceful fallback: JSONL history → state.json → context files
- Human-readable summary output with severity-coded findings
- Structured JSON report output
- Duplicate check before creating issues
