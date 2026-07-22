# Changelog

All notable changes to the nightgauge-retro skill will be documented in
this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

## [1.2.0] - 2026-03

### Added

- **Phase 9: Record Outcome to Knowledge Base** — when `--issue N` is used and
  `knowledge_path` is set in `issue-{N}.json`, the retro skill appends an
  `## Outcome` section to `decisions.md` (or creates `outcomes.md`). This closes
  the knowledge loop: issue-pickup scaffolds → feature-planning writes decisions
  → retro records outcome.
- `--record-outcome` flag — explicit opt-in for outcome recording; auto-detected
  when `knowledge_path` is present in the issue context.
- Knowledge path auto-detection: loads `knowledge_path` from `issue-{N}.json`
  whenever `--issue N` is specified.
- Outcome sections include: pipeline duration, token usage, what went well, what
  didn't go well, and lessons learned (sourced from retro analysis).
- Creates minimal knowledge directory if `knowledge_path` directory is absent.
- Updates `knowledge_entries` in `issue-{N}.json` when `outcomes.md` is newly
  created, so downstream pipeline stages (pr-create) can link to it.

## [1.1.0] - 2026-02

_(See 1.0.0 notes — CHANGELOG retroactively versioned)_

## [1.0.0] - 2026-02

### Added

- Full SKILL.md with agentskills.io specification format
- 7 failure categories for comprehensive pipeline failure analysis:
  - budget-exceeded: Cost threshold violations
  - state-management: Context or state handling errors
  - ci-infrastructure: GitHub Actions or CI system failures
  - model-capability: AI model performance or capability issues
  - timeout: Operation timeout failures
  - validation-failure: Data validation or assertion failures
  - unknown: Unclassified or miscellaneous failures
- 4 data sources for failure detection:
  - Session logs: Real-time execution logs from pipeline runs
  - Daily summary logs: Aggregated failure summaries
  - Pipeline context files: JSON context handoff between pipeline stages
  - Execution history: Historical pipeline execution records
- Command-line arguments:
  - `--issue`: Analyze specific GitHub issue by number
  - `--since`: Filter failures from specific date/time
  - `--period`: Analysis period (hour, day, week, month)
  - `--all-failures`: Include all failure types across all periods
  - `--format`: Output format (markdown, json)
- Deterministic data extraction using Bash and Python3 with AI interpretation
  layer
- Structured failure report output in both markdown and JSON formats
- Smart default scope: analyzes last batch run with automatic 7-day fallback if
  no recent batch found
- Actionable failure insights with recommended remediation steps
