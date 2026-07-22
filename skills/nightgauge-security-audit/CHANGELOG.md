# Changelog

All notable changes to the **nightgauge-security-audit** skill will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `orchestration:` frontmatter block (`mode: fanout`) declaring the seven
  security dimensions as parallel worker units with a per-unit adversarial
  judge, consumed by the capability-routed `WorkflowEngine` (epic #3899). Each
  unit's `promptRef` points at the same dimension phase the prose body walks, so
  the single-agent prose stays the portability floor for providers without an
  orchestration capability. Skill version bumped to 1.2.0.

## [1.0.0] - 2026-02

### Added

- Initial skill definition with full SKILL.md
- Command registration for `/nightgauge:security-audit`
- 7 CLI arguments defined (--path, --package, --dimensions, --format,
  --skip-audit, --output, --severity)
- 7 security dimension specifications with scoring rubrics
- 9-phase workflow definition (environment detection through scoring & report)
- 5-tier severity classification (Critical/High/Medium/Low/Info)
- CWE/CVE references in findings
- Remediation recommendations with code examples
- Built-in regex patterns for secret detection (offline-capable)
- Output format schema (JSON + Markdown)
- Multi-ecosystem support: Node.js, Python, Go, Rust, Java/Maven/Gradle
- Health-check integration (cross-references health-report.json)
- Graceful degradation when security tools are unavailable
