# Changelog

All notable changes to the **nightgauge-issue-audit** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and
this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **`OVERSIZED_SCOPE` finding** (#3851, #3835) — flags already-created issues
  (including manually-created ones that never pass through `issue-create`) that
  bundle many independent units of work into a single ticket. Mirrors the
  `issue-create` Phase 2.85 gate heuristic: fires (WARNING) when an issue
  references ≥6 distinct top-level target files, OR has predicted size `XL`, OR
  enumerates ≥6 independent refactor/migration acceptance-criteria groups —
  unless it is a decomposed epic or carries the
  `<!-- nightgauge:oversized-scope-accepted -->` override marker. No repair
  primitive; decomposition is a human/planning decision.

### Changed

- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

## [1.0.0] - 2026-05-06

### Added

- Initial release of the post-creation issue audit skill (#3237).
- Eight deterministic audit phases: existence, labels, project board
  membership and fields, body section completeness, sub-issue and parent
  linking, `blockedBy` alignment, cross-repo consistency, knowledge
  scaffold.
- Three invocation modes: `--manifest <path>` (strict),
  `--epic <N>` / `--issues <list>` (inferential), `--all-recent <duration>`
  (look-back).
- Three run modes: dry-run (default), `--fix`, `--fix-interactive`.
- Severity-tiered Markdown report and `--json` machine output to
  `.nightgauge/pipeline/issue-audit-<timestamp>.{md,json}`, plus an
  audit trail JSONL.
- Exit code semantics: 0 READY, 1 NEEDS FIXES (CRITICAL findings remain),
  2 skill-level failure.
- Repair primitive table that maps each finding type to an existing Go
  binary subcommand — no new binary subcommands introduced.
- Hard rules pinned by the negative test fixture: never auto-rewrite
  human-authored content, spike-contract violations stay CRITICAL even with
  `--fix`, `closed-as-not-planned` blocker removal requires
  `--fix-interactive`.
- Golden-file test fixtures covering each major finding category and a
  negative spike-contract fixture.
