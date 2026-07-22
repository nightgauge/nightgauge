# Changelog

All notable changes to the **nightgauge-refactor-rewrite** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `orchestration:` frontmatter block (`mode: phased`) fanning the five analysis
  dimensions out as parallel worker units closed by a merge judge that runs the
  decision engine, consumed by the capability-routed `WorkflowEngine` (epic
  #3899). Each unit's `promptRef` points at the same dimension phase the prose
  walks, so the prose stays the portability floor. Skill version bumped to
  1.2.0.

## [1.0.0] - 2026-02-21

### Added

- Initial release
- 8-dimension analysis engine (code quality, test coverage, dependency coupling,
  business logic extraction, tech stack viability, team expertise, risk
  assessment, effort estimation)
- Three-outcome decision model: Refactor, Rewrite, Hybrid
- Deterministic metric collection via bash/grep/git commands
- AI-powered interpretation with 0-100 scoring per dimension
- Weighted composite scoring with configurable dimension weights
- Hybrid approach suggestions (strangler fig, branch by abstraction, parallel
  run)
- Risk/benefit matrix generation
- Per-component analysis for mixed-strategy modernization
- Optional integration with upstream reports (health-check, security-audit,
  test-scaffold)
- JSON + Markdown report output
- Monorepo workspace support with per-package analysis
- Configurable arguments (--path, --dimensions, --team-size, --timeline, etc.)
