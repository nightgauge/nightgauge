# Changelog

All notable changes to this skill are documented here.

## [Unreleased]

### Changed

- The focus lens is read from this checkout's `focus.yaml`
  (`nightgauge layout path checkout focus.yaml`), not `.nightgauge/focus.yaml`
  (#2037, ADR-024 § 7).
- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.
