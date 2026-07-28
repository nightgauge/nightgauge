# Changelog

All notable changes to the Nightgauge VS Code Extension will be documented in this
file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-07-28

First public release on the VS Code Marketplace.

### Added

- **Action Center** — repo-scoped attention sweep surfacing decision requests,
  default-branch health, and human gates as cards you can act on, with
  standing-condition semantics and fingerprint-based auto-resolve
- **Repository-aware project settings** — per-repository configuration in
  multi-repo workspaces, with drift detection between local and team layers
- **Stage-exit forensics** — every stage records the last ten Bash commands,
  exit codes, and stderr tail, so a stalled or failed run can be diagnosed
  after the fact instead of re-run blind
- **Worktree reclamation** — a reconcile sweep reclaims worktrees left behind
  by squash-merged branches, using content comparison rather than ancestry
- **Write containment** — writes that escape the run's worktree are captured
  and attributed instead of silently landing in a sibling repository
- **Model registry updates** — Claude Opus 5 and the full reasoning-effort tier
  range, with a registry-driven thinking/effort interlock

### Changed

- Ready Issues view now defaults to a "smart" sort (Priority → Unblocked →
  Size → Age) instead of GitHub board order
- Cost calibration now engages from run history, so budget estimates reflect
  observed spend rather than static defaults
- Token and cost telemetry is recorded on IPC stage exits, closing the gap
  where extension-driven runs reported zero usage

### Fixed

- Runaway detection no longer terminates stages that are still making
  progress, and every termination names the ceiling it hit
- Forked branches are detected before a run starts rather than at push time,
  so work is not lost to a rejected non-fast-forward push
- A branch is never reported safe to delete while a worktree still holds it,
  and a prune that cannot run fails loudly instead of silently authorizing
  cleanup
- Run records have a single authoritative writer, ending duplicate and
  cross-contaminated history entries
- Both assistant tool-call delivery shapes are handled by one code path, fixing
  silently dropped prompt detection and missing command capture
- Codex-routed stages report token usage on all paths
- Board membership is keyed on (repository, issue number) rather than the issue
  number alone, fixing false matches across repositories

### Security

- Credentials moved into VS Code secure storage
- Output channel and configuration inspection are redacted; merged settings
  stay local
- Configuration paths that escape the workspace are rejected
- Resolved Go and JavaScript CodeQL findings, including two polynomial-ReDoS
  regexes, and cleared four high-severity npm advisories

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
Compare/release links intentionally omitted: only release-candidate tags
(v0.2.0-rc.*) have been pushed to nightgauge/nightgauge, so a v0.1.0...v0.2.0
compare link would 404. Add tag-based links here once v0.2.0 is tagged.
-->
