# Changelog

All notable changes to the Nightgauge Framework plugin will be documented in
this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.9.0] - 2026-02-02

### Added

- **nightgauge-doc-gen skill** - Auto-generate documentation for public APIs
  - Detects undocumented functions, classes, and methods
  - Generates JSDoc, docstrings, GoDoc, Javadoc, and XML comments
  - Multi-language support: TypeScript/JS, Python, Go, Rust, Java, C#
  - Signature change detection for existing documentation
  - README update suggestions for new features
  - Pipeline integration via `docgen-{N}.json` context files
  - CLI arguments: `--files`, `--report-only`, `--skip-readme`, `--all`
- Pipeline now includes 9 skills with doc-gen between feature-validate and
  test-gen

## [1.7.0] - 2026-02-01

### Changed

- Repository renamed from `ai-agent-plugins` to `nightgauge`
- Updated all source URLs and references to use new repository name
- Updated all comparison links in changelog

## [1.6.0] - 2026-01-31

### Added

- **Status Label Transitions** - Pipeline skills now automatically update status
  labels
  - `issue-pickup` changes `status:ready` → `status:in-progress`
  - `pr-create` changes `status:in-progress` → `status:in-review`
  - Keeps project board in sync with development workflow automatically
- **GitHub Project Board Sync** - Skills now sync the Project Status field
  automatically
  - Detects if issue is in a GitHub Project
  - Updates Status field to match label transitions ("In progress", "In review")
  - No manual project board updates needed

## [1.5.0] - 2026-01-31

### Added

- **Complete Label Taxonomy** for SDLC organization
  - Type labels: `type:feature`, `type:bug`, `type:docs`, `type:refactor`,
    `type:chore`, `type:epic`
  - Priority labels: `priority:critical`, `priority:high`, `priority:medium`,
    `priority:low`
  - Size labels: `size:XS`, `size:S`, `size:M`, `size:L`, `size:XL`
  - Status labels: `status:ready`, `status:blocked`, `status:needs-info`,
    `status:in-review`
  - Component labels: `component:nightgauge`, `component:smart-setup`,
    `component:configs`, `component:standards`
- **Epic/Sub-issue Support** in issue-create
  - Create parent epic issues with `type:epic` label
  - Link child issues to parent via `--parent` flag
  - Automatic sub-issue creation from epic task lists
- **Required Milestone Assignment** - All issues must be assigned to a milestone
- **Label Creation Commands** - Automatic creation of missing labels in new
  repositories

### Changed

- `issue-create` now requires type and priority selection (not optional)
- `issue-create` now requires milestone selection (enforced)
- Default `status:ready` label applied to all new issues

## [1.4.0] - 2025-01-31

### Added

- **Claude Code Hooks Integration** - Enforceable quality gates that run
  automatically
  - `notify.sh` - Desktop notifications for permission prompts and idle states
    (macOS, Linux, Windows)
  - `format-on-save.sh` - Auto-format code after Edit/Write operations
  - `version-check.sh` - Warn on plugin.json/SKILL.md version mismatch
  - `workflow-gate.sh` - Block dangerous operations (push to main, force push,
    secrets access)
  - `inject-context.sh` - Restore plan/issue context on session resume or
    compact
- **Hook Utilities** (`lib/common.sh`)
  - OS detection for cross-platform support
  - JSON parsing with jq (graceful fallback when not installed)
  - Git helpers for branch/issue extraction
  - Debug logging with `NIGHTGAUGE_HOOKS_DEBUG=1`
  - Performance timing in debug mode
- **Testing Infrastructure**
  - 37 unit tests for common.sh utilities
  - 32 integration tests for hook scenarios
  - `run-all-tests.sh` test runner
- **Validation & Setup Tools**
  - `validate-hooks.sh` - Check hook syntax, permissions, and configuration
  - `check-dependencies.sh` - Verify required and optional dependencies
- **Documentation**
  - Complete README rewrite with SDLC positioning
  - "Using Nightgauge in Any Repository" section
  - Hook customization via environment variables
  - Known limitations table
  - Troubleshooting guide with debug mode instructions

### Changed

- Plugin description updated to "AI-Augmented SDLC Framework"
- README restructured around the hooks-enforced workflow

### Security

- PowerShell injection prevention in Windows notifications
- osascript escaping for macOS notifications
- Sensitive file protection (.env, _.key, _.pem, _secret_)
- Git internals (.git/) modification blocked
- Force push detection including +refspec syntax

## [1.3.0] - 2025-01-30

### Added

- Base branch sync before PR creation
- Plan drift detection in pr-create command

## [1.2.0] - 2025-01-29

### Added

- Auto-priority issue selection in issue-pickup
- Parallel documentation reading in feature-planning

## [1.1.0] - 2025-01-28

### Added

- Plan location clarification
- Cleanup after PR creation

## [1.0.0] - 2025-01-27

### Added

- Initial release of Nightgauge Framework
- Pipeline commands: issue-create, issue-pickup, feature-planning, feature-dev,
  test-gen, pr-create
- Documentation-first planning approach
- Branch naming conventions with issue tracking
- PR workflow with issue linking

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/nightgauge-v.9.0...HEAD
[1.9.0]: https://github.com/nightgauge/nightgauge/compare/nightgauge-v.7.0...nightgauge-v1.9.0
[1.7.0]: https://github.com/nightgauge/nightgauge/compare/nightgauge-v.6.0...nightgauge-v1.7.0
[1.6.0]: https://github.com/nightgauge/nightgauge/compare/nightgauge-v.5.0...nightgauge-v1.6.0
[1.5.0]: https://github.com/nightgauge/nightgauge/compare/nightgauge-v.4.0...nightgauge-v1.5.0
[1.4.0]: https://github.com/nightgauge/nightgauge/compare/nightgauge-v.3.0...nightgauge-v1.4.0
[1.3.0]: https://github.com/nightgauge/nightgauge/compare/nightgauge-v.2.0...nightgauge-v1.3.0
[1.2.0]: https://github.com/nightgauge/nightgauge/compare/nightgauge-v.1.0...nightgauge-v1.2.0
[1.1.0]: https://github.com/nightgauge/nightgauge/compare/nightgauge-v.0.0...nightgauge-v1.1.0
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/nightgauge-v.0.0
