# Changelog

All notable changes to this skill are documented here.

## [Unreleased]

### Changed

- The release-watch state (`last-seen-<provider>.json`, the creation log, the
  backlog, assessments and reports) lives in this checkout's `release-watch/`
  inside the git directory (`nightgauge layout path checkout release-watch`)
  and is written through `nightgauge layout write checkout`, not under
  `.nightgauge/release-watch/` (#2037, ADR-024 § 7). The report and last-seen
  Python blocks read the path from the environment instead of hard-coding the
  Claude Code provider's file.
- The focus lens is read from this checkout's `focus.yaml`
  (`nightgauge layout path checkout focus.yaml`), not `.nightgauge/focus.yaml`
  (#2037, ADR-024 § 7).
- Migrate all direct `gh` invocations to `nightgauge forge` (#3363, Wave 4 of forge-abstraction epic #3349). Skill now works against GitLab as well as GitHub via the forge abstraction.
