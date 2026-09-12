# Nightgauge Claude Code Instructions

@AGENTS.md

This file contains only Claude Code behavior. Repository rules, documentation
routing, and durable technical knowledge are owned by `AGENTS.md` and `docs/`.

## Memory

Disable project auto-memory. Do not create or rely on `MEMORY.md` or per-fact
memory files. Record durable facts in the repository location selected by
`docs/AGENT_GUIDANCE.md`; private session handoffs live in
`nightgauge-internal/runbooks/handoffs/` and must be verified against reality.

## Scoped rules

- `.claude/rules/vscode-extension.md` applies under
  `packages/nightgauge-vscode/**`.
- `.claude/rules/scripts.md` applies under `claude-plugins/**` and `scripts/**`.

## Subagent models

Choose by task shape: Haiku for bounded lookups, Sonnet for focused fixes and
read-only audits, Opus for multi-package or contract changes, and Fable for
architecture decisions. Return conclusions instead of file dumps.

## Compaction

Preserve modified paths, issue and stage, acceptance criteria, branch, pending
tests, and unresolved errors.
