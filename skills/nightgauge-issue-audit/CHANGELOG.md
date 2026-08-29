# Changelog

All notable changes to the **nightgauge-issue-audit** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and
this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.2.0] - 2026-08-29

### Changed

- **`Verification` is a required heading for every issue type.** The
  2026-08-29 workspace backlog groom found 167 of 224 open issues with no
  falsifiable way to tell the feature working from the feature claimed; the
  groom appended one to each, and this makes the next creation fail its own
  terminal audit instead. Same three-copy contract, same
  `check-issue-body-contract.py` pin.

### Added

- **`WEAK_VERIFICATION` (WARNING)** — the section exists but names no test,
  command, or file (nothing in backticks, no test runner).
- **`SECURITY_SURFACE_UNADDRESSED` (WARNING)** — the body touches auth,
  secrets, endpoints, retries, spawning, shell, symlinks, uploads or
  permissions and carries neither a `## Security` section nor a
  security-shaped acceptance criterion. Heuristic; never auto-fixed.

### Changed

- **`MISSING_REQUIRED_HEADING` is CRITICAL in strict (`--manifest`) mode**
  (#711), and stays WARNING in inferential modes. Phase 5's required-heading
  table and `issue-create`'s authoring rules disagreed on every row, so the
  finding fired on 100% of issues the pipeline authored while the verdict —
  which turns only on CRITICAL count — still printed `READY`. The contracts are
  now reconciled, which makes the finding discriminating, and strict mode
  blocks on it because there the body was authored seconds earlier against this
  exact table. Reasoning recorded in `docs/ISSUE_AUDIT.md` § Severity Tiers.
- **Phase 5's table is declared canonical.** `issue-create` cites it instead of
  restating a shape of its own, `docs/ISSUE_AUDIT.md` mirrors it for readers,
  and `scripts/check-issue-body-contract.py` fails CI when the three copies
  drift. Phase 5 also now states that matching is exact and case-sensitive —
  `## Acceptance criteria` never satisfied `Acceptance Criteria`, and nothing
  said so.

### Added

- **`authoring-contract-round-trip` test fixture** (#711) — pins that a body
  authored per `issue-create`'s rules produces zero `MISSING_REQUIRED_HEADING`
  for `feature`, `bug`, and `epic`. No fixture pinned the round trip before,
  which is why the contradiction survived to ship.

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
