# Changelog

All notable changes to the Nightgauge VS Code Extension will be documented in this
file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Rebranded the product from Nightgauge to Nightgauge: new extension
  name and display name, new Nightgauge dial icons, and the command
  namespace moved from `nightgauge.*` to `nightgauge.*`
- Ready Issues view now defaults to intelligent "smart" sort (Priority →
  Unblocked → Size → Age) instead of GitHub board order for better workflow
  prioritization

## [0.1.0] - 2026-01-15

### Added

- Pipeline orchestration sidebar with stage visualization
- Ready Issues view with GitHub Project board integration
- Dashboard with pipeline metrics and time savings tracking
- Context file viewer for inspecting pipeline state
- Auto-refresh capability for Ready Issues list
- Notification system with macOS alert sounds and system notifications
- Dock badge bounce for user attention (macOS)
- Output window with configurable verbosity levels
- Token usage and cost estimation display
- Automatic Claude Code plugin setup prompt
- Commands: Run Pipeline, Stop Pipeline, Refresh Pipeline
- Commands: Pick Up Issue, View Issue on GitHub
- Commands: Setup Claude Code Plugins, Show Dashboard
- Settings for authentication provider, model selection, and paths
- Settings for notification sounds, volume, and Do Not Disturb respect

<!--
Compare/release links intentionally omitted: the pre-rebrand
nightgauge-vscode-v0.1.0 tag does not exist in the nightgauge/nightgauge
repository, and no release tags have been published there yet. Restore
tag-based links here once the first Nightgauge release is tagged.
-->
