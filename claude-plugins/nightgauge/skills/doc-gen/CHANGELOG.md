# Changelog

All notable changes to the **nightgauge-doc-gen** skill will be documented in this
file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.0.0] - 2026-02

### Added

- Initial release as part of the Issue-to-PR pipeline
- 6-phase documentation generation workflow:
  - Context loading (dev-{N}.json or project scan)
  - Project analysis (language/framework detection)
  - API detection (public vs private, documented vs undocumented)
  - Signature change detection
  - Documentation generation with user confirmation
  - README suggestions
- Multi-language support:
  - TypeScript/JavaScript (JSDoc)
  - Python (docstrings)
  - Go (GoDoc comments)
  - Rust (doc comments)
  - Java (Javadoc)
  - C# (XML comments)
- Public API detection strategies per language
- Signature change detection for existing documentation
- Partial documentation detection (missing @param tags, etc.)
- README update suggestions for new features
- Non-destructive mode (asks before overwriting)
- CLI arguments: `--files`, `--report-only`, `--skip-readme`, `--all`
- Pipeline integration via context files
- Multi-tool support (Claude Code, OpenAI Codex, GitHub Copilot, Cursor)

---

[Unreleased]: https://github.com/nightgauge/nightgauge/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/nightgauge/nightgauge/releases/tag/v1.0.0
