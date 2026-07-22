# Changelog

All notable changes to the **nightgauge-version-bump** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [1.0.0] - 2026-06-25

### Added

- Initial release (#4086, part of epic #4067). The version step the pipeline was
  missing: after an epic fully closes, derives the next semantic version and a
  Keep-a-Changelog entry from the merged sub-issues, then writes them so the app
  version, changelog, and store release notes stay in sync.
- Derives the bump using SemVer + Conventional Commits — `feat` → minor,
  `fix`/`chore`/`docs`/`refactor`/`test` → patch, a breaking marker (`!` or
  `BREAKING CHANGE:`) → major — taking the **highest** bump across all
  sub-issues. Classifies from both the `type:` label and the Conventional-Commit
  title prefix.
- Writes `pubspec.yaml` `version:` **name only**, preserving the store-anchored
  `+build` suffix verbatim (`deploy.sh next_build_number` still owns the build).
- Prepends a `CHANGELOG.md` entry (Added/Fixed/Changed) synthesized from the
  sub-issue titles, newest-on-top.
- Idempotent via a per-epic `<!-- nightgauge:version-bump epic #N -->`
  marker in `CHANGELOG.md` — re-running for an already-bumped epic is a clean
  no-op (the bump is relative to the current version, so the marker, not the
  pubspec value, anchors idempotency).
- `--bump major|minor|patch` human override and `--policy minor-on-feat|always-patch`
  (solo-maintainer cap-at-patch) policy. `--dry-run` prints the plan and writes
  nothing.
- No-ops with a clear message on a repo without a SemVer `pubspec.yaml`
  `version:` name.
- Output is a **reviewable working-tree change** that lands via the normal PR
  flow — the skill never commits, pushes, or dispatches a store deploy. Pairs
  with `nightgauge-release-notes`, which reads the bumped `pubspec.yaml`
  `version:` for its "What's new" header (one source of truth).
