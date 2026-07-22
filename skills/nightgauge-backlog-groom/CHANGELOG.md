# Changelog

All notable changes to the **nightgauge-backlog-groom** skill will be documented in
this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.0.0] - 2026-02-06

### Added

- Initial release of nightgauge-backlog-groom skill
- **Stale issue detection** - Identifies issues inactive beyond configurable
  threshold (default 60 days)
  - Detects based on last comment, last update, or creation date
  - Supports `--stale-days` flag for custom threshold
  - Configurable via `.nightgauge/config.yaml` with `backlog_groom.stale_days`
- **Duplicate issue detection** - Finds potential duplicate issues using keyword
  overlap and AI semantic similarity
  - Keyword extraction from issue title and body
  - Semantic similarity matching via Claude AI
  - Groups duplicates by similarity confidence
  - Suggests which issue should be primary
- **Priority validation** - AI-powered reasoning to validate issue
  prioritization
  - Analyzes issue content against project standards
  - Detects mislabeled priority levels
  - Suggests priority adjustments with reasoning
  - Reviews based on urgency, complexity, and impact
- **Dependency chain discovery** - Identifies issue dependencies and ordering
  constraints
  - Detects explicit dependency markers (#N references, `depends on`, etc.)
  - Analyzes implicit dependencies from content
  - Suggests execution order for dependent issues
  - Identifies circular dependencies
- **Structured triage report** - Generates comprehensive backlog analysis
  - Markdown report with findings, statistics, and recommendations
  - JSON output for programmatic processing
  - Grouped by analysis type (stale, duplicates, priorities, dependencies)
  - Summary statistics for dashboard display
- **Batch application** - `--apply` flag to batch update issues with
  confirmation
  - Close stale issues with custom message
  - Link duplicate issues with relationship markers
  - Update priority labels on flagged issues
  - Shows preview before confirmation
  - Rollback support if issues occur mid-batch
- **Targeted analysis** - `--focus` flag for focused triage
  - Options: `stale`, `duplicates`, `priorities`, `dependencies`, `all`
  - Reduces analysis time and tokens for specific needs
  - Combined with other flags (e.g., `--focus stale --stale-days 30`)
- **Configuration support** - Reads from `.nightgauge/config.yaml`
  - `backlog_groom.enabled` - Enable/disable skill
  - `backlog_groom.stale_days` - Default stale threshold
  - `backlog_groom.duplicate_threshold` - Similarity threshold (0-100)
  - `backlog_groom.focus_areas` - Default analysis focus
  - `backlog_groom.auto_close_stale` - Auto-close without preview
- **Project board integration** - Syncs findings to GitHub Project board
  - Updates Status field for stale issues (Backlog)
  - Adds labels for detected duplicates
  - Updates priority labels based on AI reasoning
  - Maintains project visibility of triage results
- **Context isolation** - Follows Nightgauge context isolation architecture
  - Reads from `.nightgauge/backlog-context.json` if available
  - Writes `backlog-groom-{N}.json` for downstream skills
  - Fresh agent execution with minimal context carryover

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/v1.0.0
