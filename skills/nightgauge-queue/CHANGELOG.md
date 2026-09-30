# Changelog

All notable changes to this skill are documented here.

## [Unreleased]

### Changed

- `queue-state.json` is a per-checkout file
  (`nightgauge layout path checkout queue-state.json`), not a per-clone one
  (#2037, ADR-024 § 7).
- Per-clone pipeline state, plans, retros and logs are read at
  `$(nightgauge layout path <class> <name>)` and written through
  `nightgauge layout write|append`, not by `.nightgauge/...` path (#2037,
  ADR-024 § 7).
- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.
