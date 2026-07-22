# Changelog

All notable changes to the **nightgauge-pr-merge** skill will be documented
in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Removed

- **`--admin` argument and `pr.admin_merge` config deleted** (#186). The skill
  advertised an admin bypass the deterministic layer never supported; a
  pipeline agent improvised `gh pr merge --admin` against branch protection
  (bowlsheet#233 incident). A blocked merge is terminal — report the blocker
  and escalate. Deterministic guards: `preflight skill-anti-patterns` flags
  admin/auto merge flags in skill text, and the PreToolUse stage-gate hook
  rejects `gh pr merge --admin|--auto` during pipeline sessions.

### Changed

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
