@AGENTS.md

# Claude Code adapter

Other tools also load this file, so everything below applies to Claude Code
only. Repository rules, commands and documentation routing live in `AGENTS.md`
and `docs/AGENT_GUIDANCE.md`; do not add them here.

## Claude Code: memory

Auto memory is disabled for this project by `autoMemoryEnabled: false` in the
committed `.claude/settings.json`. Do not create or rely on `MEMORY.md` or
per-fact memory files. Record durable facts in the repository location that
`docs/AGENT_GUIDANCE.md` selects.

## Claude Code: scoped rules

Each file under `.claude/rules/` loads when Claude reads a matching path:

- `.claude/rules/scripts.md`: `claude-plugins/**`, `scripts/**`, `cmd/**`,
  `internal/**`, `.nightgauge/**`
- `.claude/rules/sdk.md`: `packages/nightgauge-sdk/**`
- `.claude/rules/skills.md`: `skills/**`
- `.claude/rules/vscode-extension.md`: `packages/nightgauge-vscode/**`

## Claude Code: subagent models

Choose by task shape: Haiku for bounded lookups, Sonnet for focused fixes and
read-only audits, Opus for multi-package or contract changes, and Fable for
architecture decisions. Return conclusions instead of file dumps.

## Claude Code: compaction

Only this root file is re-injected after `/compact`. Preserve modified paths,
issue and stage, acceptance criteria, branch, pending tests, and unresolved
errors.
