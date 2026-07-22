# Changelog

All notable changes to the Nightgauge project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

> **No release has been cut yet.** This repository has zero git tags and zero
> GitHub Releases, so every entry below is unreleased and will ship under the
> **first** semver tag when it is created (see #136). Per
> [docs/GIT_WORKFLOW.md § Versioning](docs/GIT_WORKFLOW.md#versioning), the
> Extension / SDK / Go-binary version is always derived from the git tag at
> release time — the `0.1.0` in `package.json` is a placeholder, never a release
> version. When the first release is cut, rename this heading to that tag
> (e.g. `## [0.2.0] - YYYY-MM-DD`).

### Added

#### Supply-chain hardening for the release path (#136)

- **All GitHub Actions SHA-pinned**: every `uses:` across `.github/workflows/*`
  is pinned to a full 40-char commit SHA with a `# vX.Y.Z` comment;
  `actions/checkout` unified on one major; the `goreleaser-action` tool
  `version:` and every `vsce` invocation pinned to exact versions.
- **SBOMs**: `.goreleaser.yml` now emits one SPDX SBOM per release archive
  (syft), attached to the Release and covered by `attest-build-provenance`.
- **Attribution**: a build-time `THIRD_PARTY_NOTICES` (generated from the
  production dependency closure) ships in the VSIX, the npm tarball, and every
  GoReleaser archive; `NOTICE` is now vendored into both `packages/*`.
- **License metadata fixed**: `.goreleaser.yml` license set to the valid SPDX
  `Apache-2.0` (was the invalid `SEE LICENSE IN LICENSE`).

#### Marketplace publication prep + first-run onboarding (Part of Epic #4155)

- **Marketplace listing metadata**: `packages/nightgauge-vscode/package.json`
  now has accurate `categories` (`Machine Learning`, `Other`), a `galleryBanner`
  matching the the dark brand background, a `homepage` link, a `bugs` link,
  and a corrected `repository.url` casing.
- **Marketplace-facing README**: `packages/nightgauge-vscode/README.md`
  now leads with a install → sign in → claim an issue → watch a PR quickstart
  instead of contributor setup instructions; contributor/npm-auth setup moved
  into a `## Development` section further down. A `## Screenshots Needed`
  checklist tracks the still-missing real screenshots/GIFs — not yet done.
- **Guided first-run onboarding webview** (`nightgauge.showGettingStarted`):
  a new webview panel walks a first-time user through install → claim an
  issue → watch a pipeline run to completion. Opens automatically once per
  install the first time a workspace is _not_ Nightgauge-initialized,
  reusing the existing `repoInitialized` context-key plumbing
  (`src/commands/quickstart.ts`); reopenable any time from the Command
  Palette.
- **Release workflow**: `.github/workflows/release.yml` now has a
  `Publish to VS Code Marketplace` step, gated behind
  `if: ${{ secrets.VSCE_PAT != '' }}` so it stays a guaranteed no-op until the
  real publisher PAT is added as a repository secret.

### Added

#### Settings Architecture — 3-Tier Model Capstone (Epic #3313, Phase 7 — #3340)

Completes the capstone phase for the settings tier model introduced in
Phases 1–6 of epic #3313:

- **`docs/CONFIGURATION.md` rewritten** to reflect the 7-tier precedence chain
  (`defaults → global → project → local → runtime → env → cli`) with a new
  3-tier conceptual model section (Team / Machine / Runtime) and tier placement
  guide. (#3340)
- **Runtime (memento) tier documented**: UI ephemeral state stored in VSCode
  `globalState` / `workspaceState`, never committed to YAML. (#3340)
- **Test coverage matrix verified** across all 7-tier boundary pairs — see
  `packages/nightgauge-vscode/tests/config/tier-boundary.test.ts`. (#3340)
- **Example config pruned**: `jira-config.yaml` relocated to
  `docs/spikes/2568-jira-integration-config-example.yaml` — it contained
  credential patterns not valid for the team tier. (#3340)

#### Slash-Command & SKILL.md Enforcement (Epic #3342)

Three enforcement layers that prevent the failure modes behind incidents #3329
and #3331 (agent reads command file as spec, skips SKILL.md phases):

- **Layer 1 — ADR-007 canonical banner** (#3343/#3344): Applied to all 33
  applicable command files in `claude-plugins/nightgauge/commands/`. Each
  file now opens with a positional banner that invokes the `Skill` tool before
  any other content, plus `disable-model-invocation: true` frontmatter. Verified
  by `nightgauge preflight skill-banners` (14 unit tests + RealTree
  regression in `internal/preflight/skillbanners_test.go`).
- **Layer 2 — Spike-contract hard-gate** (#3345): `nightgauge spike validate`
  rejects `type:spike` issue creation unless the body contains a valid fenced
  ` ```yaml recommendations ``` ` block (schema-validated), a Spike Contract
  path declaration heading, and an artifact path reference. Gate enforced by
  8 tests in `internal/cmd/spike/validate_test.go`.
- **Layer 3 — Epic-decomposition hard-gate** (#3346/#3347): Phase 2.9 of
  `skills/nightgauge-issue-create/SKILL.md` rejects `type:epic` creation
  unless the body declares one of three valid shapes: Path A (sub-issues
  planned), Path B (`<!-- nightgauge:decompose-later -->`), or Path C
  (`<!-- nightgauge:standalone-epic -->`). Classification logic mirrored
  in `internal/cmd/epicgate` (pure Go, 9 tests). Path B auto-creates a
  follow-up `type:chore` to track decomposition.
- **Documentation**: `CONTRIBUTING.md` extended with a "Slash-Command Contract
  (ADR-007)" section covering the canonical banner template, authoring
  checklist, epic-creation paths, and enforcement gate reference.
- **ADR**: `docs/decisions/007-slash-command-skill-invocation-contract.md`
  reconciled against final implementation.

### Changed

#### 15 skills migrated from direct `gh` calls to `nightgauge forge` (#3363)

Wave 4 of the forge-abstraction epic (#3349). Every direct `gh` invocation in
the 15 top-consumer skills (`repo-init`, `retro`, `project-sync`,
`issue-pickup`, `release-watch`, `issue-refine`, `pipeline-audit`,
`pipeline-health`, `issue-audit`, `pr-merge`, `dep-modernize`,
`modernize-plan`, `smart-setup`, `queue`, `pr-create`) is replaced with the
forge-agnostic `nightgauge forge` Cobra surface. `IB_FORGE=gitlab` now
works end-to-end across these skills.

- **New binary surfaces**: `nightgauge forge auth whoami`,
  `nightgauge forge repo view`, `nightgauge forge graphql` (raw
  GraphQL pass-through for the four GitHub-specific carve-outs documented in
  ADR-008).
- **Deprecation linter**: `scripts/lint-skills/no-direct-gh.sh` (mirrored as
  `nightgauge preflight skill-no-direct-gh`) gates regressions in CI via
  the new `.github/workflows/lint.yml` workflow. The allowlist
  (`scripts/lint-skills/allowlist.txt`) tracks the un-migrated tail (~10
  skills with ≤4 calls each), filed as a follow-up under #3349.
- **Smoke matrix**: `.github/workflows/skills-smoke.yml` runs a
  `forge × skill` matrix (15 skills × 2 forges) — the GitLab slot consumes
  the W5-2 Dockerized GitLab CE harness once it lands.
- **JSON shape parity**: `cmd/nightgauge/forge/skill_parity_test.go`
  asserts every `gh ... --json` path the migrated skills extract is also
  present in the corresponding `forge ... --json` output.

See [ADR-008](docs/decisions/008-skill-forge-cli.md) for the full migration
table, carve-out rationale, and consequence analysis.

### Fixed

#### Empty epics invisible in Repositories tree view (#3329)

- A freshly-created epic (`type:epic` label, zero sub-issues) was filtered out
  of the flat list and rendered no group header, making it invisible in the
  Repositories view until it was decomposed.
- **Go binary** (`internal/github/board.go`): `IsEpic` now treats the
  `type:epic` label as the canonical marker, in addition to native sub-issue
  presence. A label-only epic returns `IsEpic: true`.
- **TypeScript view** (`packages/nightgauge-vscode/src/views/items/EpicGroupTreeItem.ts`):
  `groupIssuesByEpic()` now creates a (possibly empty) group entry for every
  `type:epic` issue in the current status filter, and back-fills missing
  metadata from the epic row itself. `EpicGroupTreeItem` renders empty epics
  as leaf items (no expand chevron) with a tooltip prompting `issue create-sub`.
- **Slash-command help** (`claude-plugins/nightgauge/commands/issue-create.md`):
  the project-board sync example now passes `--status Ready` and explicitly
  sets `--size`, matching the full SKILL.md guidance and preventing accidental
  Backlog placement.

### Added

#### nightgauge-feature-dev Expanded Review Subagents (#10)

- **6 parallel review subagents** in Phase 5 Quality Review (up from 3):
  - Code quality reviewer (existing)
  - Security reviewer (existing)
  - Test reviewer (existing)
  - Documentation reviewer (new) — checks API docs, inline comments, README updates
  - Performance reviewer (new) — checks N+1 queries, memory leaks, hot-path costs
  - Accessibility reviewer (new) — checks ARIA labels, keyboard nav, color contrast
- **Parallel execution**: all 6 reviewers spawn in a single `Task` message for
  maximum throughput
- **Aggregate quality report**: consolidated findings table after all reviewers
  complete, with critical issues surfaced for Phase 6 self-correction

#### update-docs Skill Improvements (v1.6.0)

- **Lessons Learned Section**: Added comprehensive documentation of known false
  positive patterns discovered in real-world usage
  - Template content in skill/command files
  - Educational examples showing correct vs incorrect patterns
  - Subdirectory paths vs top-level paths
- **Improved Validation Guidance**:
  - Made AWK-based code block filtering MANDATORY (not optional)
  - Added context-aware directory checking to avoid flagging valid subdirectory
    references
  - Added specific exclusion patterns for skill and command files
- **Known Patterns Documentation**:
  - Grep patterns that work reliably vs unreliable patterns
  - Common validation failure modes and prevention strategies
  - Relative path resolution best practices

### Changed

- **update-docs Skill**:
  - Phase 4.5 now requires AWK-based code block filtering to prevent false
    positives
  - Enhanced directory structure mismatch detection with context awareness
  - Version bumped from 1.5.0 to 1.6.0

#### GitHub Sub-Issues Integration (Issue #38)

- **VSCode Extension**:
  - Added `GitHubService` for interacting with GitHub's native sub-issues API
  - Added `subIssueProgress` utility for calculating epic completion percentages
  - New methods: `fetchSubIssues()`, `linkSubIssueToParent()`,
    `fetchIssueMetadata()`

- **Context Schema** (v1.3):
  - Added `child_issues` field for tracking sub-issue numbers
  - Added `sub_issue_progress` field for epic progress statistics
  - Added `native_parent` field for GitHub native parent references

- **Scripts**:
  - Enhanced `check-epic-completion.sh` to query GitHub's native sub-issues API
  - Maintains backward compatibility with body-text reference parsing
  - Added progress percentage calculation in epic status logs

- **Documentation**:
  - Added `docs/SUB_ISSUES.md` - Comprehensive guide for using sub-issues with
    Nightgauge
  - Covers creation workflows, epic progress tracking, and PR parent linking
  - Includes troubleshooting section for common issues

- **Tests**:
  - Added `subIssueProgress.test.ts` - Unit tests for progress calculations
  - Added `GitHubService.subIssues.test.ts` - Tests for GitHub API interactions
  - Added mock factories in `tests/mocks/sub-issues.ts` for test data generation

### Changed

- **Epic Completion**: Now queries native sub-issues first before falling back
  to body parsing
- **Progress Tracking**: Epic completion now shows percentage in addition to
  fraction (e.g., "60% (3/5)")

---

## Author

nightgauge
