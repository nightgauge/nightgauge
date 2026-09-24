# Changelog

All notable changes to the **nightgauge-issue-pickup** skill will be documented in
this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- **Phase 2.7 checks capacity (#1655, 1.21.0).** The size gate now asks
  `nightgauge size-gate capacity --json` for the repository's target adapter
  and model and passes them to `size-gate check`, which rejects an issue
  whose size exceeds that model's capacity. A binary without the verb, or no
  resolvable target, runs the check as before. Step 2.7.3 now says what
  soft-route does: it lets an over-capacity issue through on a fallback
  model, and nothing else.

### Fixed

- **Every shell block derives the pipeline identifiers it reads (#1932).** Each
  `Bash` call is a fresh process, so a block that read `$ISSUE_NUMBER`,
  `$BRANCH` or `$REPO` from an earlier phase read an empty string, and
  `${VAR:-}` turned it into a valid blank. Each block now derives them itself:
  the issue number from `NIGHTGAUGE_ISSUE_NUMBER`, else the branch name; the
  repo from `NIGHTGAUGE_REPO`, else `nightgauge git repo-slug`; the branch from
  `git`. A missing value fails the block instead of writing a blank.
  `nightgauge preflight skill-shell-state` keeps it that way.

- **Phase 8 no longer inherits shell variables from earlier phases (#1919).**
  `BRANCH_NAME`, `ISSUE_JSON`, `TITLE`, `ISSUE_TYPE` and `BASE_BRANCH` were
  assigned in Phase 3 / Phase 5 and read back in Phase 8, in a different shell
  — and a shell variable does not survive between tool calls. `${VAR:-}` then
  wrote a schema-valid `issue-{N}.json` with an empty `branch`, empty title and
  no labels, which the post-condition gate correctly called a no-op. Phase 8
  now re-derives every field in its own block from a source that survives: the
  issue number and repo from the process environment, the branch from the
  worktree's `HEAD`, the issue content from one `forge issue view` fetch. It
  fails loudly when `HEAD` is not this issue's branch, and its final check
  asserts `branch` and `title` are non-empty rather than merely that the file
  is parseable — the old check passed on an empty file.

### Changed

- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

## [1.9.0] - 2026-02

### Fixed

- **Complete context isolation fix** - Extended CONTEXT ISOLATION RULES to
  plugin command file `claude-plugins/nightgauge/commands/issue-pickup.md` (#195)
  - Previous fix (#179) only updated SKILL.md but Claude reads plugin command
    first
  - Added Phase 8.5 completion message to plugin command
  - Added explicit termination rules to prevent "Continue to Feature Planning?"
    prompt
  - Synced version numbers across SKILL.md, plugin command, and plugin.json

## [1.8.1] - 2026-02

### Fixed

- **Context isolation termination** - Added explicit CONTEXT ISOLATION RULES to
  prevent Claude from asking "Continue to Feature Planning?" (#179)
  - Matches established pattern from `nightgauge-feature-planning` SKILL.md
  - New Step 8.5 displays explicit completion message with termination rules
  - Prohibits AskUserQuestion for stage transitions
  - Enforces fresh conversation requirement for pipeline stage transitions

## [1.8.0] - 2026-02

### Added

- **Sprint/iteration assignment** (Phase 6.6) - automatically assigns current
  sprint iteration when picking up an issue (#73)
  - New `sync-project-iteration.sh` deterministic hook script
  - Opt-in via `.nightgauge/config.yaml` with `project.sprint.auto_assign: true`
  - Supports @current, @next, or specific iteration ID
  - Graceful handling when no iteration field exists
  - See [docs/SPRINT_WORKFLOW.md](../../../../docs/SPRINT_WORKFLOW.md) for setup

### Fixed

- **Epic exclusion from auto-selection** - Issues with `type:epic` label are now
  excluded from all 7 tiers of the auto-selection algorithm (#107)
  - Epics are tracking issues, not actionable work items
  - Explicit Epic selection shows sub-issues and offers alternatives
  - Clear error message explains why Epics cannot be picked up
- **Stop hook JSON validation** - Replaced probabilistic prompt-based Stop hook
  with deterministic command hook (`stop-verification.sh`) to ensure reliable
  JSON output (#127)
  - Fixes "Claude outputs text instead of JSON" error during issue pickup
  - Follows deterministic > probabilistic architecture principle
  - Adds PLAN.md completion checking (detects incomplete checkboxes)
  - Skip verification via `NIGHTGAUGE_SKIP_STOP_VERIFICATION=1` env var
- **Signal stage start timing** - Moved `signal-stage-start.sh` call from Phase
  1.5 to Phase 2.5 (after issue selection) (#151)
  - Fixes VSCode extension not showing "running" status during issue-pickup
  - The `$ISSUE_NUMBER` variable was undefined in Phase 1.5, causing the signal
    to fail silently
  - Now signals immediately after issue selection, before issue analysis

## [1.7.1] - 2026-02

### Fixed

- **Argument parsing precedence** - When an issue number is provided via
  `$ARGUMENTS` (e.g., from VS Code extension), the skill now explicitly skips
  all selection logic and proceeds directly to Phase 3
- Added "CRITICAL: Argument Check" section before Phase 1 to ensure arguments
  are parsed first
- Fixed issue where Claude would ask for issue selection even when number was
  passed from UI

## [1.5.0] - 2026-02

### Added

- **Date field automation** (Phase 6.5) - automatically populates GitHub Project
  date fields
  - Sets Start date to today when claiming an issue
  - Sets Target date from milestone due date (if unset)
  - Dynamic field ID lookup (never hardcodes field IDs)
  - Opt-in via `.nightgauge/config.yaml` with `auto_dates: true`
  - Graceful handling when issue not in project or fields don't exist

## [1.4.0] - 2026-01

### Added

- **status:ready filter for auto-selection** - All 7 tiers now require
  `status:ready` label
  - Issues without `status:ready` are never auto-selected
  - Ensures only workable issues are picked up
  - Updated fallback message explains status label states
- Improved "No Issues Available" message with status label explanation and
  options

### Changed

- Updated ASCII diagram to show `status:ready` requirement for all tiers
- Simplified label syntax (removed spaces in label names for consistency)

## [1.3.0] - 2026-01

### Changed

- **Clarified auto-selection as DEFAULT behavior** - when invoked without
  arguments, auto-selection is now explicitly the default (not interactive mode)
- **Improved milestone sorting documentation** - Tiers 3 and 4 now clearly state
  that milestones are sorted by `dueOn` field ascending (soonest deadline first)
- **Enhanced tier diagram** - added explicit "FIRST MATCH WINS" language to
  clarify that the algorithm stops at the first tier returning an issue
- Step 2.2 renamed to "Auto-Selection Mode (Default - MUST USE)" for emphasis

## [1.2.0] - 2026-01

### Added

- **Status label transitions** - automatically changes `status:ready` →
  `status:in-progress` when picking up an issue
  - Keeps project board in sync with development workflow
  - Gracefully handles missing `status:ready` label
- **Project board Status sync** - automatically updates GitHub Project Status
  field to "In progress"
  - Detects if issue is in a project and syncs Status field
  - Fails silently if not in a project (labels still work)
- Base branch detection - detects epic/feature branches and asks which to branch
  from
- Branch context persistence - saves base branch to
  `.nightgauge/plans/.branch-context` for PR creation
- Prevents accidentally branching from main when working on epic branches

## [1.1.0] - 2026-01

### Added

- **Auto-priority issue selection** - When invoked without an issue number,
  automatically selects the highest priority issue using a 7-tier algorithm
- Priority tiers: @me + critical/high → anyone + critical/high → @me + milestone
  → anyone + milestone → @me (any) → priority:medium → oldest
- `-i` / `--interactive` flag to force manual selection (preserves old behavior)
- Shows reasoning for auto-selection and asks for confirmation before proceeding
- Override option to fall back to interactive mode if auto-selection is rejected

## [1.0.0] - 2025-01

### Added

- Initial release as part of the Issue-to-PR pipeline
- Issue selection by number or interactive list
- `--mine` flag to filter issues assigned to current user
- `--label` flag to filter by specific labels
- Automatic branch creation following `docs/GIT_WORKFLOW.md` conventions
- Branch prefix detection from issue labels (feat/, fix/, docs/, refactor/)
- Requirements extraction from issue body (user story, acceptance criteria)
- Self-assignment option with `gh issue edit`
- Clean working directory verification with stash option
- Multi-tool support (Claude Code, OpenAI Codex, GitHub Copilot, Cursor)

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v1.8.1...HEAD
[1.8.1]: https://github.com/nightgauge/nightgauge/compare/v1.8.0...v1.8.1
[1.8.0]: https://github.com/nightgauge/nightgauge/compare/v1.7.1...v1.8.0
[1.7.1]: https://github.com/nightgauge/nightgauge/compare/v1.5.0...v1.7.1
[1.5.0]: https://github.com/nightgauge/nightgauge/compare/v1.4.0...v1.5.0
[1.4.0]: https://github.com/nightgauge/nightgauge/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/nightgauge/nightgauge/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/nightgauge/nightgauge/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/nightgauge/nightgauge/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/v1.0.0
