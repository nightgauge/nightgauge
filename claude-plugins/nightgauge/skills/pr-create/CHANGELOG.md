# Changelog

All notable changes to the **nightgauge-pr-create** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.
- **Now the LLM fallback for the deterministic-first pr-create stage.** Issue
  #3265 landed a Go-native pr-create runner at
  `internal/orchestrator/stages/prcreate.go` that handles the rich-context
  majority (validation passed, no security/scope/dead-code blocks, not a spike
  or batch run, no unverified manual checklist). On punt, the scheduler falls
  through to this skill unchanged — every behavior in this CHANGELOG continues
  to apply on the LLM path. See `docs/PR_CREATE_STAGE.md` for the decision
  matrix and the punt reasons that route here.

## [1.21.0] - 2026-04-16

### Added

- **Proactive Main Branch Merge** (Phase 2.3) — merges `origin/{base_branch}`
  into the feature branch **before** creating the PR, ensuring PRs are always
  tested against the latest base branch state
  - Runs after Phase 2 (preflight confirms clean working tree) and before
    Phase 2.5 (security re-scan scans the merged state)
  - Fetches latest base branch, counts commits behind, and merges if stale
  - Uses `git merge` (not rebase) to preserve both lineages — correct for
    batch scenarios where sibling sub-issues merge concurrently
  - On conflicts: exits with outcome `stale-branch-merge-conflict`, aborts
    the merge to restore clean working tree; PR is NOT created
  - On success: pushes merged branch; CI re-runs on merged commit automatically
  - Base branch resolved from `issue-{N}.json` → config → `main` (same as
    Phase 2 preflight)
  - Behavior extracted into `skills/_shared/STALE_BRANCH_MERGE.md` for reuse
    across skills
  - Go binary `internal/intelligence/failure/taxonomy.go` recognizes
    `stale-branch-merge-conflict` in stderr and maps it to
    `CatStaleBranchMergeConflict` — non-retryable, escalates to human
  - Regression test: `tests/freshness-check.test.ts` covers the batch
    scenario where sibling A merges a breaking change before sibling B's
    `pr-create` runs
  - Closes #2781

## [1.20.0] - 2026-04-01

### Added

- **CI status monitoring** (Phase 3.5) — polls CI check results after PR
  creation and reports failures with remediation context
  - Runs `nightgauge ci wait` with configurable timeout (default: 15
    minutes) and poll interval (default: 30 seconds)
  - Skips gracefully when binary or PR number is unavailable (`CI_MONITORED=false`)
  - **Deterministic failure classification** — categorizes each failed check by
    type using regex patterns (no AI inference required):
    - `lint` — eslint, pylint, rubocop, flake8, style, code quality
    - `test` — vitest, jest, pytest, rspec, unit, e2e, integration, coverage
    - `build` — compile, bundle, webpack, vite, esbuild, rollup, package
    - `typecheck` — tsc, typescript, mypy
    - `security` — codeql, snyk, dependabot, vulnerability, sast
    - `format` — prettier, black, formatting
    - `unknown` — fallback for unmatched patterns
  - **Transient detection** — marks timeout/network/flaky failures as
    `is_transient: true` for automatic retry eligibility in pr-merge RALPH Loop
  - **detailsUrl enrichment** — supplements Go binary output with
    `gh pr checks` to provide log links per failure
  - **Quick fix suggestions** — reports `npm run format && npm run lint --fix`
    for auto-fixable format/lint failures
  - **CI monitoring block** in `pr-{N}.json` — `ci_monitoring` object with
    `monitored`, `monitor_duration_secs`, `final_status`, `checks_summary`,
    `failures[]`, `timestamp`, and `notes` for pr-merge to consume
  - Headless safe: no interactive prompts; all output is informational
  - Closes #16

## [1.19.0] - 2026-03-19

### Added

- **Parallel PR context gathering** (Phase 1) — executes independent operations
  simultaneously for ~70% faster context load
  - **Step 1.2**: Base branch resolved early (before parallel phase) — required
    by `git diff` in Group B
  - **PTC path** (preferred): single `PTCContextGatherer` session batch-reads all
    context files and git operations in one round-trip when `ANTHROPIC_API_KEY`
    is available
  - **Fallback path**: three bash groups run simultaneously when PTC is
    unavailable:
    - Group A — branch info (100-200ms)
    - Group B — git operations: `diff`, `log`, `status` (~1-2s)
    - Group C — context file reads: `issue`, `planning`, `dev`, `validate`,
      `PLAN.md` (~1-2s)
  - All groups execute concurrently; results merged in Step 1.5 before Phase 1.5
  - Graceful fallback: partial group failures continue with available data
  - Inline ASCII timeline diagram documents the before/after timing model
  - Timing: 8-10 seconds sequential → 2-3 seconds parallel (~70% improvement)
  - Closes #11

## [1.18.0] - 2026-03

### Added

- **Security re-scan implementation** (Phase 2.5) — implements the security
  scanning behavior planned in v1.8.0 into the SKILL.md workflow
  - Hybrid detection: gitleaks (preferred) + grep pattern fallback
  - Critical findings (private keys, AWS credentials, connection strings)
    block PR creation with exit 1
  - Warning-level findings (API keys, tokens, passwords) allow proceed with
    acknowledgement note added to PR description
  - Excludes common false-positive patterns (example, YOUR\_, test, mock, etc.)
  - Scans only changed files (git diff base...HEAD) for efficiency
  - Populates `preflight_results.security_scan` in `pr-{N}.json`
  - Closes #12

## [1.16.0] - 2026-03

### Added

- **Knowledge section in PR descriptions** (Phase 1.7) — reads `knowledge_path`
  and `knowledge_entries` from planning or dev context and builds a
  `## Knowledge` section with relative links to PRD.md, decisions.md, and any
  other knowledge files
  - Section is omitted entirely when no knowledge entries exist
  - Well-known files (PRD.md, decisions.md) get descriptive labels; other files
    use a title-cased label derived from the filename
  - Links use relative paths that GitHub renders as clickable links
  - Part of epic #1678

## [1.12.0] - 2026-02

### Changed

- **Auto-commit uncommitted changes** (Step 1.2) - automatically commits any
  uncommitted changes before creating PR
  - No longer prompts user to choose between commit/stash/cancel
  - Enables seamless transition from `/nightgauge-feature-dev` to
    `/nightgauge-pr-create`
  - Generates descriptive commit message including issue number and changed
    files
  - Supports context isolation pipeline architecture
- Updated Prerequisites section to clarify that manual commits are not required

## [1.9.0] - 2026-02

### Added

- **Target date check** (Step 1.5.7) - warns if issue is missing Target date in
  project
  - Non-blocking warning during pre-flight checks
  - Only active when `auto_dates: true` in `.nightgauge/config.yaml`
  - Provides guidance on how to set manually
  - Improves Roadmap view visibility

## [1.8.0] - 2026-02

### Added

- **Security re-scan** (Step 1.5.6) - scans changed files for hardcoded secrets
  before PR creation
  - Hybrid detection: uses gitleaks if installed, falls back to grep patterns
  - Pattern categories: API keys, secrets, passwords, tokens, AWS credentials,
    private keys, connection strings
  - Critical findings (private keys, AWS creds, connection strings) block PR
    creation
  - Warning findings allow proceed after user verification
  - Excludes common placeholders (example, YOUR\_, xxx, test, mock)
  - Security note added to PR description if warnings were acknowledged

## [1.7.0] - 2026-01

### Added

- **Coverage regression check** (Step 1.5.5) - compares current test coverage
  against baseline stored in PLAN.md
  - Reads coverage baseline from plan file (captured during feature-planning)
  - Runs same coverage detection logic as feature-planning
  - Compares current metrics (statements, branches, lines) against baseline
  - Warns if any metric dropped below baseline
  - User options: Add tests, Continue anyway (acknowledge), Cancel
  - Acknowledged drops noted in PR description

### Fixed

- **Project board Status sync** (Step 6.4) - fixed broken `gh project item-list`
  command that was missing required project number parameter
  - Now dynamically discovers which project contains the issue
  - Iterates through org projects to find the issue by number and repository
  - Works for any org/user-owned projects, not repo-specific
  - Proper error handling with informative messages
  - **Optional `.nightgauge/config.yaml` config** for faster lookup when
    project is known
    - Specify `project.number` to skip iteration
    - Falls back to dynamic discovery if issue not in configured project

## [1.6.0] - 2026-01

### Added

- **Acceptance criteria checklist** (Step 4.1.5) - extracts and verifies
  acceptance criteria from linked issue
  - Parses `- [ ]` and `- [x]` patterns from issue body
  - Interactive checklist for user to mark items as complete/incomplete
  - Support for deferring items to follow-up issues with `(deferred to #X)`
    notation
  - Option to auto-create follow-up issues for deferred items
  - Checklist included in PR description for reviewer visibility
- Updated Step 4.8 to include Acceptance Criteria section in PR body template

## [1.5.0] - 2026-01

### Added

- **Parallel context gathering** (Phase 2) - runs independent operations
  simultaneously for ~40% faster PR creation
  - Group A: Git history (commits + diff) runs in parallel
  - Group B: Plan file reading runs in parallel
  - Group C: Issue details fetching runs in parallel
  - All groups execute simultaneously using multiple tool calls
- **Fallback to sequential** - gracefully handles failures in parallel execution
  - Logs which command failed and re-runs sequentially
  - Continues with available context if partial failure
- ASCII diagram showing parallel execution flow

### Changed

- Phase 2 restructured from 6 sequential steps to parallel groups
- Step numbering updated: 2.1 (branch info), 2.2 (parallel gathering), 2.3
  (merge), 2.4 (fallback)

## [1.4.0] - 2026-01

### Added

- **Status label transitions** - automatically changes `status:in-progress` →
  `status:in-review` when creating PR
  - Keeps project board in sync with PR workflow
  - Gracefully handles missing `status:in-progress` label
- **Project board Status sync** - automatically updates GitHub Project Status
  field to "In review"
  - Detects if issue is in a project and syncs Status field
  - Fails silently if not in a project (labels still work)

## [1.3.0] - 2026-01

### Added

- **Base branch sync** (Step 1.6) - rebases from base branch before creating PR
  - Catches version conflicts from other merged PRs at creation time
  - Fetches latest from origin and checks if behind
  - Automatic rebase with conflict detection
  - Clear error message explaining common causes (version bumps, concurrent
    changes)
  - Works with GitHub branch protection "require up-to-date" for full coverage

## [1.2.0] - 2026-01

### Added

- **Pre-submission validation** (Step 1.5) - validates files before creating PR
  - JSON syntax validation for all `.json` files
  - YAML syntax validation for all `.yaml`/`.yml` files
  - **Auto-detection of repository type** - works for any repo, not just
    nightgauge
    - Claude plugin repos: checks SKILL.md vs command file versions
    - Node.js repos: checks package.json vs package-lock.json versions
    - Python repos: detected for future version checks
  - Optional `.nightgauge-validation.yaml` config file for custom
    validation rules
  - Detailed report showing which files have mismatched versions
  - User prompt with options: Fix now, Continue anyway, Cancel
  - Automatic version fix workflow when user chooses to fix

## [1.1.0] - 2026-01

### Added

- **Plan drift detection** (Phase 2.5) - compares implemented files against
  plan's "Files to Create/Modify" section
  - Extracts expected files from plan using regex patterns
  - Reports drift in both directions (missing from implementation, not in plan)
  - User prompt with options: Update plan, Continue anyway, Review changes
  - Drift section added to PR description when user continues with drift
- Base branch auto-detection - reads `.nightgauge/plans/.branch-context` or
  detects from git history
- Multi-issue support - can close multiple issues in single PR (e.g.,
  `Closes #13, Closes #14`)
- Dynamic base branch in `gh pr create --base` flag
- Issue detection from plan files using grep for "Closes/Fixes/Resolves #X"
- Plan file cleanup after PR creation - removes
  `.nightgauge/plans/{issue}-*.md` and `PLAN.md`

### Changed

- Diff and log commands now use detected base branch instead of hardcoded `main`

## [1.0.0] - 2025-01

### Added

- Initial release as part of the Issue-to-PR pipeline
- Pre-flight checks (tests, uncommitted changes, branch status)
- Automatic PR description generation from commits and PLAN.md
- Issue linking with `Closes #X` syntax
- `--issue` flag to specify source issue
- `--draft` flag for draft PRs
- `--reviewer` flag to request specific reviewers
- PR template detection and usage
- Structured PR format (Summary, Changes, Test Plan, Checklist)
- Multi-tool support (Claude Code, OpenAI Codex, GitHub Copilot, Cursor)

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v1.21.0...HEAD
[1.21.0]: https://github.com/nightgauge/nightgauge/compare/v1.20.0...v1.21.0
[1.20.0]: https://github.com/nightgauge/nightgauge/compare/v1.19.0...v1.20.0
[1.19.0]: https://github.com/nightgauge/nightgauge/compare/v1.18.0...v1.19.0
[1.18.0]: https://github.com/nightgauge/nightgauge/compare/v1.16.0...v1.18.0
[1.16.0]: https://github.com/nightgauge/nightgauge/compare/v1.12.0...v1.16.0
[1.12.0]: https://github.com/nightgauge/nightgauge/compare/v1.9.0...v1.12.0
[1.9.0]: https://github.com/nightgauge/nightgauge/compare/v1.8.0...v1.9.0
[1.8.0]: https://github.com/nightgauge/nightgauge/compare/v1.7.0...v1.8.0
[1.7.0]: https://github.com/nightgauge/nightgauge/compare/v1.6.0...v1.7.0
[1.6.0]: https://github.com/nightgauge/nightgauge/compare/v1.5.0...v1.6.0
[1.5.0]: https://github.com/nightgauge/nightgauge/compare/v1.4.0...v1.5.0
[1.4.0]: https://github.com/nightgauge/nightgauge/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/nightgauge/nightgauge/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/nightgauge/nightgauge/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/nightgauge/nightgauge/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/v1.0.0
