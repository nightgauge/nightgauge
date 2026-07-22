# Changelog

All notable changes to the **nightgauge-docs-write** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.1.0] - 2026-03

### Added

- `--knowledge` flag for architecture knowledge generation mode — generates ADRs
  and an architecture overview note from source code analysis
- Phase 1.5: Architecture Pattern Detection — classifies source files into seven
  pattern categories (event-system, auth-security, service-pattern,
  repo-storage, config-system, pipeline-workflow, ipc-transport) using keyword
  grep before reading files
- Phase 5.5: Knowledge Entry Generation — writes one ADR per detected pattern
  (`adr-{slug}.md`) and an overview note (`arch-notes.md`) to
  `.nightgauge/knowledge/architecture/`
- ADR template with `[TEAM TO DOCUMENT]` placeholders for sections requiring
  human knowledge of historical context
- Wiki-link format (`[[relative_path]]`) in generated ADRs and overview note,
  consistent with `docs/KNOWLEDGE_BASE.md` convention
- Knowledge-only mode: `--knowledge` can run without `--target`/`--section`
- Combined mode: `--knowledge` can run alongside `--target`/`--section` to
  produce both a doc section and knowledge entries in one pass
- Knowledge generation summary in done report (Phase 8)
- Updated argument table: `--target` and `--section` now marked as conditionally
  required (required unless `--knowledge` is provided)

### Changed

- Version bumped to `1.1.0`
- Phase count updated from 8 to 10 (added Phase 1.5 and Phase 5.5)
- Phase indices renumbered: Phase 2 → index 3, Phase 3 → index 4, Phase 4 →
  index 5, Phase 5 → index 6, Phase 6 → index 8, Phase 7 → index 9, Phase 8 →
  index 10
- Phase 0 argument validation updated to allow `--knowledge`-only invocation
  without requiring `--target`/`--section`

## [1.0.0] - 2026-02

### Added

- Initial release as a standalone documentation utility skill
- 8-phase workflow:
  - Argument parsing (`--target`, `--section`, `--source`, `--dry-run`)
  - Source file identification (explicit glob or inferred from issue context)
  - Source file reading and content synthesis
  - Target doc file reading and section detection (insert vs replace)
  - Content synthesis with style-aware formatting
  - Section writing with Edit/Write tool
  - Accuracy validation (verify all backtick-referenced names exist in source)
  - Link validation (verify all relative links resolve from target file
    location)
- `--dry-run` flag for previewing content without modifying files
- Auto-correction of inaccurate code references and broken links
- Pipeline context loading from `issue-{N}.json` when available (optional)
- Standalone design — no PLAN.md required, no pipeline context files written
- Multi-tool support (Claude Code, OpenAI Codex, GitHub Copilot, Cursor)

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/nightgauge/nightgauge/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/v1.0.0
