# Changelog

All notable changes to this skill are documented here.

## [1.0.0]

### Added

- Initial skill: gathers a vendor model release's pages, runs
  `nightgauge model-watch classify`, and presents the proposed registry patch,
  capability findings, defects and overlay proposals. With `--create-issues` it
  files the emitted issues after confirmation. It never edits the registry
  (#83).
