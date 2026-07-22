# Changelog

All notable changes to this skill are documented here.

## [1.3.0]

### Changed

- Restructure to ADR-010 progressive disclosure (#3850, epic #3811). SKILL.md is now a concise <500-line navigational skeleton (overview + per-phase Read directives + a "Supporting files (load on demand)" TOC); the 14 procedural phase bodies (bash, GraphQL, config templates, tables) moved verbatim into 8 on-demand `_includes/*.md` reference files. Pure structural refactor — no behavior change.

## [Unreleased]

### Changed

- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.
