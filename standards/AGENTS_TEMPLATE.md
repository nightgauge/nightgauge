# AGENTS.md Template

> Keep this file concise. Put architecture, standards, procedures, and
> explanations in `docs/`, then route agents to the relevant documents here.

## Project scope

[TEAM TO DOCUMENT: What does this repository own, and what is explicitly out
of scope?]

Before substantive work, use `docs/AGENT_GUIDANCE.md` and `docs/README.md` to
select the relevant documentation.

## Commands

```bash
# Install
[TEAM TO DOCUMENT]

# Focused test
[TEAM TO DOCUMENT]

# Complete pre-push validation
[TEAM TO DOCUMENT]
```

## Non-obvious operating rules

- [TEAM TO DOCUMENT: Rules an agent cannot infer from code]
- [TEAM TO DOCUMENT: Safety or validation constraints that always apply]
- Follow `docs/GIT_WORKFLOW.md` for branches, commits, pull requests, and merge
  cleanup.
- Follow `docs/CODE_STANDARDS.md` and `docs/TESTING.md` for implementation and
  verification.
- Follow `docs/SECURITY_AND_ERROR_HANDLING.md` for security requirements.

## Knowledge

When pipeline context provides `knowledge_path`, read its `PRD.md` and
`decisions.md` before implementation. See `docs/KNOWLEDGE_BASE.md` when that
feature is enabled.

## Scoped instructions

Tools load nested files differently: some never read them, and some read only
the files between the repository root and the working directory. See
[AGENT_GUIDANCE.md](https://github.com/nightgauge/nightgauge/blob/main/docs/AGENT_GUIDANCE.md#how-tools-load-instruction-files).
So:

- Keep every rule a session started at the repository root must obey in this
  file.
- Add a nested `AGENTS.md` only for additive rules that apply to one subtree,
  and list each one here by path: [TEAM TO DOCUMENT or "none"].
- Give each nested `AGENTS.md` a sibling `CLAUDE.md` whose first line is
  `@AGENTS.md`.
- Keep `CLAUDE.md` a regular file whose first line is `@AGENTS.md`. Other tools
  also read it, so put only Claude Code-specific content below the import.
- Never symlink instruction files.

---

Generated with [Nightgauge](https://github.com/nightgauge/nightgauge).
