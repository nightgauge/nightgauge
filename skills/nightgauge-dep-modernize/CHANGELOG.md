# Changelog

All notable changes to the **nightgauge-dep-modernize** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `orchestration:` frontmatter block (`mode: phased`) modelling modernization as
  ordered breaking-change-analysis → staged-update-groups → apply-updates phases
  closed by a gate judge that runs the post-update regression check
  (`judge.gate: true`), consumed by the capability-routed `WorkflowEngine` (epic
  #3899). Each unit's `promptRef` points at the same phase the prose walks, so
  the prose stays the portability floor. Skill version bumped to 1.3.0.

### Changed

- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

## [1.0.0] - 2026-02-21

### Added

- Initial release
- Multi-ecosystem dependency analysis (Node.js, Python, Go, Rust, Java/JVM)
- 5-category classification (Critical, Major Updates, Minor/Patch, Unmaintained,
  Deprecated)
- AI-powered breaking change analysis from changelogs
- Staged update groups with dependency tree awareness
- Replacement suggestions for deprecated/unmaintained packages
- Dry-run and auto-fix modes
- Staged rollout with per-group branches/PRs (`--staged` flag)
- JSON + Markdown report output
- Integration with health-check and security-audit reports
- Monorepo workspace support
- Configurable severity filtering and ecosystem selection
