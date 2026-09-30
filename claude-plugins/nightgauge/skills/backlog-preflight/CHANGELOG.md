# Changelog

All notable changes to the **nightgauge-backlog-preflight** skill are documented
in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- Per-clone pipeline state, plans, retros and logs are read at
  `$(nightgauge layout path <class> <name>)` and written through
  `nightgauge layout write|append`, not by `.nightgauge/...` path (#2037,
  ADR-024 § 7).

### Fixed

- Missing complexity models now point to the supported `nightgauge outcome init`
  command (#1590).
