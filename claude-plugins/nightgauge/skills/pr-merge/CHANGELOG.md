# Changelog

All notable changes to the **nightgauge-pr-merge** skill will be documented
in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **Phase 0.5 has a real body, and batch cleanup actually runs** (#337). The
  phase declared itself and printed its marker, but its entire body was a dead
  `<!-- include: ../_shared/BATCH_MODE.md -->` against a file that never
  existed — the model received the literal HTML comment. The shared contract is
  now written and included at the head-of-file position the other five stages
  use, and the phase body lives in the new
  `_includes/batch-detection.md` (batch detection from git, multi-issue
  `Closes` verification, cleanup delegation). New **Step 7.8** removes the
  epic-keyed batch set — `batch-`, `planning-batch-`, `dev-batch-`,
  `validate-` and `pr-{E}.json` plus `.nightgauge/plans/{E}-*.md` — after Step
  7.7 has read the context files, and only once that epic's PR reports a
  non-empty `mergedAt`, so an in-flight batch is never swept.

### Removed

- **`--admin` argument and `pr.admin_merge` config deleted** (#186). The skill
  advertised an admin bypass the deterministic layer never supported; a
  pipeline agent improvised `gh pr merge --admin` against branch protection
  (bowlsheet#233 incident). A blocked merge is terminal — report the blocker
  and escalate. Deterministic guards: `preflight skill-anti-patterns` flags
  admin/auto merge flags in skill text, and the PreToolUse stage-gate hook
  rejects `gh pr merge --admin|--auto` during pipeline sessions.

### Changed

- **Batch-only merge instructions now load on demand** (#367). Phase 0.5 keeps
  its existing marker and behavior, but runs the cheap `dev-batch-{E}.json`
  probe inline before reading `_includes/batch-detection.md`. The common
  single-issue path now skips that 124-line procedure entirely; batch runs
  still load and follow the same multi-issue merge contract.
- **Unresolvable rebase conflicts now re-dispatch feature-dev instead of discarding the branch** (#4072, epic #4067). Step 6.1.5 captures the conflicting files + both sides into `conflict-context-{N}.json` **before** `git rebase --abort`, emits a `CONFLICT_RESOLUTION_NEEDED` feedback signal targeting feature-dev, and keeps the branch. The deterministic `conflict-recovery-loop` recovery action rewinds the pipeline to feature-dev, which checks out the same PR branch and resolves the conflict (bounded by `pipeline.recovery.conflict_recovery.max_dev_redispatch`), then escalates with the specific files if resolution genuinely fails. Replaces the old blind fresh-branch restart (`conflict-restart-{N}.json` + remote-branch delete) that threw away all dev work.
- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

## [1.3.0] - 2026-02

### Changed

- **Context file cleanup now uses deterministic hook script** - Moved inline
  `rm` commands to `cleanup-context-files.sh` lib script for reliability and
  consistency
- Added cleanup of `.nightgauge/pipeline/` and `.nightgauge/plans/`
  files after successful merge

### Fixed

- **Context files not cleaned after pipeline completion** - `pipeline-finish`
  bookend stage now invokes `cleanup-context-files.sh` deterministically,
  ensuring context files are always removed after merge regardless of whether
  the pr-merge agent reached Step 7.6

## [1.2.1] - 2026-01

### Fixed

- Use reliable `gh api` commands for fetching PR review data instead of `gh pr`
  subcommands with `--json` flags

## [1.0.0] - 2026-01

### Added

- Initial release as the final step in the Issue-to-PR pipeline
- **CI wait phase** - polls for check completion with configurable timeout
  (default 5 min)
- **Review parsing** - fetches and parses both automated and human review
  feedback
- **Issue categorization** - classifies feedback as Critical, Major, or Minor
  based on keywords
- **Auto-fix capability** - can address minor issues automatically with
  `--auto-fix` flag
- **Smart merge** - squash merge by default, with `--merge` and `--rebase`
  options
- **Admin bypass** - `--admin` flag to bypass branch protection when needed
- **Post-merge cleanup**:
  - Updates issue status labels (in-review → done)
  - Syncs GitHub Project board status
  - Deletes feature branch (local and remote)
  - Switches to main and pulls latest
- Comprehensive error handling for common scenarios:
  - No open PR
  - Already merged
  - CI failures
  - Changes requested
  - Merge conflicts
  - Branch protection
- Multi-tool support (Claude Code, OpenAI Codex, GitHub Copilot, Cursor)

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/nightgauge-pr-merge-v1.3.0...HEAD
[1.3.0]: https://github.com/nightgauge/nightgauge/compare/nightgauge-pr-merge-v1.2.1...nightgauge-pr-merge-v1.3.0
[1.2.1]: https://github.com/nightgauge/nightgauge/compare/nightgauge-pr-merge-v1.0.0...nightgauge-pr-merge-v1.2.1
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/nightgauge-pr-merge-v1.0.0
