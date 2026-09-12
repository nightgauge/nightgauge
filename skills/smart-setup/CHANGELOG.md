# Changelog

All notable changes to the smart-setup skill will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `/smart-setup verify`: a read-only conformance report against the DC-01…DC-22
  checklist, running the installed check for the items it covers (issue 1675).
- Installs a byte-identical copy of the bundled `check-agent-guidance.sh` and a
  CI job named `agent guidance` (`--workspace-block forbidden`), and tells the
  user to make it a required status check (issue 1675).
- Generated `AGENTS.md` carries the `<!-- nightgauge:agent-guidance v1 -->`
  provenance marker; nested `AGENTS.md` files with a sibling `CLAUDE.md` are
  proposed for sub-packages with their own toolchain (issue 1675).

### Changed

- A repository in the older layout (rules in `CLAUDE.md`, `AGENTS.md` deferring
  to `CLAUDE.md`, symlinked instruction files, a committed managed-steering
  block) now gets a migration plan listing every moved rule plus a proposed
  diff for review, instead of an additive merge. The additive policy applies
  only to files already in the new layout (issue 1675).
- Copilot's pointer adapter is created only when Copilot is selected, matching
  GitHub's support matrix; Cursor and Kiro get no generated copy of
  `AGENTS.md` (issue 1675).

### Fixed

- Every template renders completely (broken nested fences), the knowledge row
  is part of the routing table, and the `CLAUDE.md` template starts with
  `@AGENTS.md` on line 1 (issue 1675).

- Generate `AGENTS.md` as the universal contract for every repository, keep
  Claude and Copilot files as thin adapters, and store documentation routing in
  `docs/AGENT_GUIDANCE.md` (#1604).
- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

## [4.5.0] - 2026-03-07

### Added

- **Phase 4.5: Complete Tooling Config Scaffold** — Expanded from guidelines to
  full implementation with concrete template content for all supported configs:
  - `tsconfig.json`: TypeScript strict mode, ES2022 target, NodeNext module
    resolution
  - `vitest.config.ts`: globals, node environment, coverage with v8 provider
  - `.github/workflows/ci.yml`: build + test + lint (using `ubuntu-latest`,
    `--if-present`)
  - `eslint.config.js`: ESLint 9+ flat config format (conditional on eslint dep)
  - `.prettierrc`: Standard formatting (conditional on prettier dep)
- Version detection: reads Node.js version from `package.json` engines field,
  detects vitest/eslint/prettier presence from devDependencies
- Brownfield safety: `[ ! -f FILE ]` guards on every write, checks all known
  config file names for eslint/prettier
- Post-scaffold summary with next-steps instructions

## [4.3.0] - 2026-02-03

### Added

- **Phase 5: Project Validation** - New phase for GitHub Project board
  integration
  - Step 5.1: Check for existing `.nightgauge/config.yaml` configuration
  - Step 5.2: Ask user about project integration (opt-in)
  - Step 5.3: Get project number from user if not configured
  - Step 5.4: Discover project fields via `gh project field-list`
    (deterministic)
  - Step 5.5: Validate required fields (Status, Priority, Size, dates, etc.)
  - Step 5.6: Report missing fields with actionable `gh` CLI fix commands
  - Step 5.7: Offer to link repository to project board
  - Step 5.8: Create `.nightgauge/config.yaml` with discovered field IDs

### Changed

- Renumbered existing Phase 5 (Completion) to Phase 6
- Updated Best Practices section to reflect new 6-phase workflow

### Notes

- Phase 5 is optional - users can decline project integration and skip to Phase
  6
- Field validation uses deterministic `gh` CLI commands, not AI interpretation
- Follows graceful degradation pattern - missing fields are reported with fix
  commands rather than blocking setup

## [4.2.0] - 2026-01-15

### Added

- Initial changelog creation for smart-setup skill
- Tiered configuration approach (Essential, Standard, Advanced)
- Conditional AGENTS.md/CLAUDE.md creation based on tool selection
- `[TEAM TO DOCUMENT]` markers for tribal knowledge
- Legacy file integration (.github/copilot-instructions.md migration)

### Notes

- Version history prior to 4.2.0 was not formally tracked

---

_This changelog follows the principles from docs/GIT_WORKFLOW.md._
