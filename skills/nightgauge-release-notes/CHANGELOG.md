# Changelog

All notable changes to the **nightgauge-release-notes** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [1.0.0] - 2026-06-25

### Added

- Initial release (#4075, epic #4067). Drafts user-facing fastlane release notes
  from a closed epic's sub-issue titles and bodies:
  - `fastlane/metadata/en-US/release_notes.txt` (iOS + macOS, Universal
    Purchase, ≤4000 chars)
  - `fastlane/metadata/android/en-US/changelogs/default.txt` (Android, **≤500
    chars hard limit**)
- Confirms the epic is fully closed via `nightgauge epic check-completion
--json` and fetches each sub-issue body via `nightgauge forge issue view
--json title,body` (`SubIssueRef` carries no body).
- Resolves the "What's new in vX.Y.Z" header from the target repo's
  `pubspec.yaml` `version:` field, with a `--version` override.
- Detects the fastlane layout generically and no-ops with a clear message on
  repos without store metadata.
- Byte-counts the Android changelog and re-condenses until ≤500 chars, and
  writes fresh content to both files, so the downstream store-deploy freshness
  gate (BOTH files modified since the last "Bump build number" commit, Android
  ≤500) passes.
- Output is a **human-reviewed draft** — the skill never commits, pushes, or
  dispatches a store deploy.
