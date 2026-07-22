# Changelog

All notable changes to the **nightgauge-issue-create** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [1.22.0] - 2026-06-24

### Changed

- **Deterministic file-overlap wave serialization** (#4074, part of epic #4067)
  — the previously-advisory "if A and B modify the same file they MUST have a
  dependency" guidance (Phase 2.6 / Phase 3.5) is now computed by the Go wave
  planner, not left to author judgment. `nightgauge epic plan-waves`
  extracts each sub-issue's predicted target files from its body and, for any two
  same-wave sub-issues sharing a top-level EXACT target file, **auto-injects a
  `blockedBy` edge** (later issue number depends on earlier) before computing
  waves, surfacing each as an `error`-severity entry in the `conflicts` array.
  Phase 3.5 MUST apply every injected edge via `nightgauge issue
add-blocked-by <later> <earlier>`. Directory-only overlaps stay parallel
  (`warning` severity). This is the authoring-side root-cause fix for the
  `#143`/`#144` collision in epic `#142` (both edited `journal_entry_page.dart`
  in the same wave). The file-path extractor was also widened to recognise
  `.dart` (Flutter) and other source-file forms.

## [1.21.0] - 2026-05-30

### Added

- **Phase 2.85: Oversized-Scope Hard-Gate** (#3851, #3835) — runs for **every**
  issue type (not just epics, unlike Phase 2.9) and blocks creation when the
  issue bundles many independent units of work into a single executable ticket.
  Triggers when any of: ≥6 distinct top-level target files referenced, predicted
  size `XL`, or ≥6 independent refactor/migration acceptance-criteria groups.
  Exempted when the issue is a decomposed epic (`type:epic` with sub-issues) or
  carries the `<!-- nightgauge:oversized-scope-accepted -->` override marker
  (mirrors the existing Phase 2.9 marker pattern). Defense #5 against the
  #3811-class runaway ($112.77 of feature-dev churn on one issue that secretly
  meant "refactor ~18 skills").

## [1.19.0] - 2026-05-06

### Added

- **Phase 4.9: Write Creation Manifest** (#3237) — emits a strict-mode
  contract at `.nightgauge/pipeline/issue-create-manifest-<ts>.json`
  conforming to `CreationManifestSchema`. The manifest declares every
  issue created (epic + sub-issues + standalone) with its repo, type,
  priority, size, status, parent epic, sub-issues, blockedBy edges, body
  sections, component labels, knowledge path, and spike artifact path.
- **Phase 6: Terminal Audit Pass** (#3237) — invokes
  `/nightgauge:issue-audit --manifest <path>` after every creation
  flow. Audit exit code propagates: NEEDS FIXES (exit 1) blocks the skill
  from reporting success. The new `--no-audit` flag is the only opt-out,
  reserved for autonomous orchestrator flows that batch creations and run
  a single look-back audit terminally.

### Changed

- Completion checklist expanded with Phase 4.9 + Phase 6 gates.

## [1.18.0] - 2026-05-06

### Added

- **Phase 2.4: Multi-Repo Sub-Issue Routing** (#3232) — closes the silent-misroute
  bug where epic sub-issues whose content matched a `routing.patterns[]`
  `preferred_repo` were filed in the primary repo regardless of the workspace
  config. The new phase:
  - Reads `.vscode/nightgauge-workspace.yaml` `routing.patterns[]` AND the
    new per-repo `project_number` field. Falls back to `gh project list`
    discovery when `project_number` is absent.
  - Scores each sub-issue's title+body against every pattern's keywords; the
    highest-scoring pattern wins (default repo if no match).
  - Emits a deterministic routing manifest at
    `.nightgauge/pipeline/issue-create-routing-<epic-number>.json` that
    Phase 3 and Phase 4 read as the single source of truth.
  - Hard-gates skill progression when content matches a non-default
    `preferred_repo` but the manifest routes elsewhere; requires `--no-route`
    - `--confirm-default-repo` to override.
- **Phase 4.8: Cross-Repo Project Membership Audit** (#3232) — runs
  unconditionally for every epic with sub-issues. Queries each created issue's
  actual project memberships via GraphQL and asserts each one is in the project
  matching its repo per the manifest. Audit failure produces an actionable
  error and exits non-zero — the skill never reports success on a misrouted
  issue.
- `.vscode/nightgauge-workspace.yaml` extended with `repositories[].project_number`
  per-repo mapping (workspace-level addition outside the skill).
- `tests/cross-repo-routing.test.md` test fixture covering same-repo,
  cross-repo, hard-gate, single-repo-fallback, and project-discovery cases.

### Changed

- **Phase 3** now reads the routing manifest from Phase 2.4 and dispatches
  cross-repo sub-issues through `gh issue create --repo <target>` with
  `Part of <owner>/<repo>#<epic>` body annotation (since GitHub does not
  support cross-repo native sub-issue links).
- **Phase 4** now requires explicit `--repo` and `--project` on every
  `nightgauge project add` call, derived from the manifest. Defaults are
  forbidden — the prior implicit-default behavior was the #3232 footgun.
- Command README (`claude-plugins/nightgauge/commands/issue-create.md`)
  updated to accurately describe the routing flow. The prior README was
  aspirational and described behavior the skill did not implement.

### Fixed

- Cross-repo sub-issues no longer silently land in the primary repo's project
  board (#3232).

## [1.17.0] - 2026-05

### Added

- Phase 2.7 Path C — Spike-with-implementation routing (#3190)
  - New Step 2.7.7 applies Path C: drops the standalone spike issue, marks a
    first dependent ticket as the ADR-bearing ticket, inserts
    `## Architectural Decision Required` into its body, prepends
    `## Prerequisite ADR` to every subsequent dependent, and wires native
    `blockedBy` from each subsequent dependent to the first ticket (not a
    spike).
  - New Step 2.7.2a Path B Guard: surfaces the single-point-of-failure risk
    identified during cross-repository validation and requires
    explicit confirmation (interactive `yes` or headless
    `--accept-path-b-risk`) before applying Path B.

### Changed

- **BEHAVIOR**: Cross-repo epic decompositions now default to Path C instead
  of Path B. Path B is opt-in and reserved for cases where the design space
  is genuinely too open to commit code without a separate research pass.
- Step 2.7.2 decision tree rewritten to select among A/B/C in the order
  defined in
  [docs/SPIKE_CONTRACT.md](../../docs/SPIKE_CONTRACT.md#choosing-between-path-a-b-and-c).
- Step 2.7.3 preview format extended to record the ADR-bearing first ticket
  and planned ADR path for Path C, and to record Path B guard
  acknowledgement.
- Step 2.7.6 wave-analysis section updated to describe how Path C interacts
  with execution waves (ADR-bearing ticket pinned to wave 1, subsequent
  dependents in wave 2+ via `blockedBy`).

## [1.16.0] - 2026-04

### Changed

- **BREAKING**: Migrated all `gh` CLI calls to `nightgauge` Go binary
  commands (#2667)
  - Phase 1: Replaced `gh auth status` / `gh repo view` with binary + token
    validation
  - Phase 2.8: Replaced `gh issue list --search` with
    `nightgauge issue list --search`
  - Phase 3: Replaced `gh issue create` with `nightgauge issue create`
  - Phase 3.5: Removed GraphQL fallback sections for blocking relationships;
    verification now uses `nightgauge issue view --json`
  - Phase 4: Removed entire GraphQL Fallback section; binary is now required
    (no silent fallback)
  - Phase 4.5: Replaced `gh issue edit` / `gh issue view` with
    `nightgauge issue edit --append-body`
  - Phase 4.7: Replaced GraphQL field introspection with exit-code-based
    verification

### Added

- New Go binary command: `nightgauge issue list --search "<keywords>"
--limit N` for duplicate detection
- New Go binary command: `nightgauge issue edit <number> --body "..." |
--append-body "..."` for body updates

## [Unreleased]

### Added

- Issue quality refinement for implementation vs spike separation
  - Added mandatory issue-intent classification before drafting
  - Added spike guidance with `adopt` / `defer` / `skip` outcomes
  - Added epic refinement rules to separate execution-ready work from research
  - Added acceptance-criteria guardrails for upstream CLI or API uncertainty

- Issue #543: Deterministic sub-issue linking hook
  - Added `claude-plugins/nightgauge/hooks/lib/create-sub-issue.sh`
  - Script enforces canonical `Part of #<parent>` body text exactly once
  - Script links parent/child with GraphQL `addSubIssue`
  - Script syncs new child issue via `add-to-project.sh`
  - Added `claude-plugins/nightgauge/hooks/tests/test-create-sub-issue.sh`

### Changed

- Updated `skills/nightgauge-issue-create/SKILL.md` to route sub-issue
  creation through deterministic `create-sub-issue.sh`
- Updated `claude-plugins/nightgauge/commands/issue-create.md` to document
  deterministic sub-issue flow

## [1.9.0] - 2026-02

### Added

- Phase 2.5: Parallel Decomposition for Agent Teams (#661)
  - Dependency detection with three heuristics: shared file paths, import chain
    references, sequential workflow keywords
  - Execution wave calculation via topological sort
  - Wave-based tree preview for user review before issue creation
  - Dependency metadata embedding in sub-issue bodies (HTML-comment-wrapped
    YAML)
  - Complexity estimation and teammate model suggestions per sub-issue
  - `--parallel` flag for one-time override when `agent_teams.enabled` is false
- Guard rails integration (#663)
  - `validateWaveExecution()` — skip agent teams if too few independent issues
  - `detectFileConflicts()` — flag blocking file ownership conflicts
  - Uses `@nightgauge/nightgauge-sdk` agent-teams module

## [1.8.0] - 2026-02

### Added

- Phase 2.9: Negative Assertion AC (#481)
  - Suggests acceptance criteria for what should NOT exist after work completes
  - Deterministic templates for refactor issues (dead code removal, deprecated
    API cleanup)
  - Deterministic templates for epic issues (feature flag cleanup, duplicate
    implementation removal)
  - Only triggers for `type:refactor` and `type:epic` issues
  - User can select which suggestions to include via multiSelect

- Phase 2.10: Consumer Impact Analysis (#481)
  - Greps codebase for consumers of deprecated/replaced systems
  - Hybrid approach: AI extracts search patterns, grep executes
    deterministically
  - Excludes test files, node_modules, build artifacts from consumer count
  - For 1-10 consumers: offers AC items or per-file tracking
  - For 11+ consumers: offers migration sub-issue creation grouped by directory
  - Sub-issues reuse existing Phase 4.3 flow for epic sub-issue creation

## [1.7.0] - 2026-02

### Added

- Step 2.6: Parent Epic suggestion for all non-epic issues
  - Lists open epics and suggests linking new issues
  - Ensures issues appear grouped in tree view via "Part of #X" pattern

## [1.6.0] - 2026-02

### Added

- Phase 2.8: AC Quality Review
  - Detects project type (VSCode extension, CLI, API, UI) from repo analysis
  - Suggests project-type-specific technical acceptance criteria commonly missed
  - For VSCode extensions: command argument validation, invocation context
    handling
  - For CLI tools: argument validation, exit codes, help output
  - For APIs: request validation, error response format
  - Uses AskUserQuestion to let user select which suggestions to include
  - Addresses class of bugs where generic AC misses component-specific concerns
    (e.g., PR #395 batch pipeline argument handling)

## [1.5.0] - 2026-02

### Added

- Phase 2.1.5: AI-powered epic decomposition in multi-repo workspaces (#325)
  - Detects multi-repo workspace from `.vscode/nightgauge-workspace.yaml`
  - Loads routing patterns for keyword-to-repository mapping
  - Deterministic pattern matching with AI gap-filling for unmatched items
  - Presents decomposition preview with confidence scores

## [1.4.0] - 2026-02

### Added

- Phase 4.3: Cross-repository issue creation and linking (#326)
  - Creates linked child issues across multiple repositories
  - Verifies write permissions before creating cross-repo issues
  - Updates parent epic body with cross-repo sub-issue references

## [1.3.0] - 2026-02

### Added

- Phase 2.5: Adaptive complexity model size suggestions (#222)
  - Uses historical data and pattern matching for size estimation
  - Deterministic scoring with time-decay weighted observations
  - User confirmation via AskUserQuestion

## [1.2.1] - 2026-02

### Documentation

- Added **CRITICAL** section emphasizing mandatory project board sync
- Updated CLAUDE.md with explicit issue creation guidance
- Updated plugin command with project board sync step in workflow summary
- Made project board sync requirement more prominent and non-optional

## [1.2.0] - 2026-02

### Added

- Automatic project board integration (Step 4.5)
- Issues are automatically added to GitHub Project board after creation
- Label-to-field mapping for Status, Priority, and Size fields
- Uses deterministic script (`hooks/lib/add-to-project.sh`) for reliability
- Graceful skip when no project is configured in `.nightgauge/config.yaml`

### Documentation

- Added reference to Deterministic vs Probabilistic architecture principle

## [1.1.0] - 2026-01

### Added

- Complete label taxonomy with type, priority, size, status, and component
  categories
- Required milestone assignment for all issues (enforces SDLC organization)
- Epic issue type with sub-issue creation support
- Parent/child issue linking (`--parent` flag)
- Priority flag (`--priority high|medium|low|critical`)
- Automatic `status:ready` label on new issues
- Label creation commands for repository setup
- Required metadata section in documentation

### Changed

- Type labels now use `type:` prefix (e.g., `type:feature` instead of
  `enhancement`)
- Priority labels now use `priority:` prefix
- Milestones are now required, not optional
- Issue preview now shows all metadata including milestone

## [1.0.0] - 2025-01

### Added

- Initial release as part of the Issue-to-PR pipeline
- Interactive mode for guided issue creation
- Quick mode with description argument
- Issue type hints (`--type feature`, `--type bug`, `--type docs`)
- Structured issue format with Summary, User Story, Acceptance Criteria sections
- Automatic label suggestions based on issue type
- Support for GitHub issue templates
- Pipeline integration footer for downstream skills
- Multi-tool support (Claude Code, OpenAI Codex, GitHub Copilot, Cursor)

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v1.9.0...HEAD
[1.9.0]: https://github.com/nightgauge/nightgauge/compare/v1.8.0...v1.9.0
[1.8.0]: https://github.com/nightgauge/nightgauge/compare/v1.7.0...v1.8.0
[1.7.0]: https://github.com/nightgauge/nightgauge/compare/v1.6.0...v1.7.0
[1.6.0]: https://github.com/nightgauge/nightgauge/compare/v1.5.0...v1.6.0
[1.5.0]: https://github.com/nightgauge/nightgauge/compare/v1.4.0...v1.5.0
[1.4.0]: https://github.com/nightgauge/nightgauge/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/nightgauge/nightgauge/compare/v1.2.1...v1.3.0
[1.2.1]: https://github.com/nightgauge/nightgauge/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/nightgauge/nightgauge/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/nightgauge/nightgauge/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/v1.0.0
