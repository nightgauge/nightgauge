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

Add nested `AGENTS.md` files only when a subtree needs portable instructions
that do not apply to the entire repository. The closest file takes precedence.

---

Generated with [Nightgauge](https://github.com/nightgauge/nightgauge).
