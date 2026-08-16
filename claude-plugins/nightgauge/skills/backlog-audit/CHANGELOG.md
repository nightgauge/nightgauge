# Changelog

All notable changes to the **nightgauge-backlog-audit** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and
this project adheres to [Semantic Versioning](https://semver.org/).

## [1.0.0] - 2026-08-16

### Added

- Initial release of the periodic backlog re-assessment skill (#613),
  encoding the 2026-08-16 reference run (103 open issues, 10 theme
  batches, adversarial closure verification).
- Seven-phase workflow: inventory, theme batching, assessment fan-out,
  adversarial closure verification, apply, stale-sync sweep, report —
  scored against the fixed P0–P3 priority rubric.
- Hard rules stated as non-negotiable: evidence-cited closures only,
  additive premise corrections (never rewrites), unverifiable claims stay
  open, cross-board listings never deduped, publication-boundary redaction
  applied even in dry-run.
- Dry-run by default; `--apply` executes verifier-confirmed closures,
  duplicate folds, premise corrections, board field corrections via the Go
  binary, typed `nightgauge forge label add`/`remove` mutations, and
  stale-sync status repairs.
