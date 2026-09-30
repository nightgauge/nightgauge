# Changelog

All notable changes to this skill are documented here.

## [Unreleased]

### Changed

- Phase 6.8 bootstraps this checkout's `complexity-model.yaml` inside the git
  directory (`nightgauge layout path checkout complexity-model.yaml`); a
  `--seed-from` model is staged in a temp file and written through
  `nightgauge layout write checkout complexity-model.yaml`, not into
  `.nightgauge/` (#2037, ADR-024 § 7). The write uses `--no-clobber`, so a
  model created while seeding ran is never overwritten.

## [1.3.2] - 2026-09-27

### Changed

- Description states the skill is user-invoked (#2195).

## [1.3.0]

### Changed

- Restructure to ADR-010 progressive disclosure (#3850, epic #3811). SKILL.md is now a concise <500-line navigational skeleton (overview + per-phase Read directives + a "Supporting files (load on demand)" TOC); the 14 procedural phase bodies (bash, GraphQL, config templates, tables) moved verbatim into 8 on-demand `_includes/*.md` reference files. Pure structural refactor — no behavior change.

## [Unreleased]

### Changed

- The `.nightgauge/.gitignore` block is template version 17, deny-by-default:
  `/*` ignores everything under `.nightgauge/` and `!` rules re-include only
  team config, `audit/`, `skill-smoke/`, `skill-evals/baseline.jsonl` and
  `model-evals/evidence/` (#2043, ADR-024 § 13).
- No longer creates `.nightgauge/pipeline/history`, `.nightgauge/plans`,
  `.nightgauge/logs` or their `.gitkeep` files: per-clone data lives under the
  git directory and the binary creates it on first write (#2037, ADR-024 § 7).
- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.

### Fixed

- Initialize the canonical complexity baseline through `nightgauge outcome init`
  instead of maintaining a duplicate inline YAML template. Failed seed transforms
  now reach the same supported fallback path (#1590).
