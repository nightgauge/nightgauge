# Smart Setup — File Templates (Phase 4)

Every template below is a complete file. Replace bracketed placeholders with
facts from THIS repository, keep `[TEAM TO DOCUMENT: …]` markers for anything
only the team can answer, and delete conditional blocks whose condition is
false. A template that itself contains a fenced block is wrapped in a
four-backtick fence, so every template renders completely.

## Contents

- [docs/GIT_WORKFLOW.md](#docsgit_workflowmd)
- [docs/SECURITY_AND_ERROR_HANDLING.md](#docssecurity_and_error_handlingmd)
- [docs/README.md](#docsreadmemd)
- [docs/AGENT_GUIDANCE.md](#docsagent_guidancemd)
- [AGENTS.md (root)](#agentsmd-root)
- [CLAUDE.md (root)](#claudemd-root)
- [Nested AGENTS.md and its sibling CLAUDE.md](#nested-agentsmd-and-its-sibling-claudemd)
- [.github/copilot-instructions.md](#githubcopilot-instructionsmd)
- [Cursor and Kiro](#cursor-and-kiro)

---

## docs/GIT_WORKFLOW.md

````markdown
# Git Workflow

## Critical Rules

**These rules are MANDATORY for ALL contributors, including AI assistants:**

| Rule                              | Description                                                            |
| --------------------------------- | ---------------------------------------------------------------------- |
| **NEVER push directly to `main`** | All changes must go through feature branches                           |
| **ALWAYS use feature branches**   | Create a branch for every change                                       |
| **ALWAYS create pull requests**   | Every merge to main requires a reviewed PR                             |
| **Follow branch naming**          | Use prefixes: `feat/`, `fix/`, `docs/`, `refactor/`, `test/`, `chore/` |

## Branch Naming

- `feat/TICKET-123-description` - New features
- `fix/TICKET-123-description` - Bug fixes
- `docs/TICKET-123-description` - Documentation
- `refactor/TICKET-123-description` - Refactoring
- `test/TICKET-123-description` - Test changes
- `chore/TICKET-123-description` - Maintenance

## Commit Message Format

```text
[TYPE][TICKET-ID] Short summary (50 chars or less)

Detailed explanation if necessary.

Refs: TICKET-ID
```

Types: `[FEAT]`, `[FIX]`, `[DOCS]`, `[STYLE]`, `[REFACTOR]`, `[TEST]`, `[CHORE]`

## Pull Request Process

1. Create feature branch from `main`
2. Make changes with meaningful commits
3. Run the complete local gate named in `AGENTS.md`
4. Push branch and create PR
5. Request review from team member
6. Address feedback
7. Merge after approval (squash or merge per team preference)
````

When the repository already uses conventional commits (check `git log`), show
that format instead of the bracketed one.

---

## docs/SECURITY_AND_ERROR_HANDLING.md

```markdown
# Security and Error Handling

## Security Rules (CRITICAL)

| Rule                                 | Description                                    |
| ------------------------------------ | ---------------------------------------------- |
| **NEVER hardcode secrets**           | Use environment variables or secret management |
| **ALWAYS validate input**            | Sanitize at system boundaries                  |
| **NEVER expose sensitive data**      | Keep credentials out of logs/responses         |
| **ALWAYS use parameterized queries** | Prevent SQL injection                          |
| **NEVER commit secrets**             | Use .gitignore, pre-commit hooks               |

## Input Validation

- Validate all user input at API boundaries
- Use allowlists over denylists where possible
- Sanitize data before database operations
- Encode output appropriately (HTML, URL, SQL)

## Error Handling

- Return generic error messages to users
- Log detailed errors server-side with context
- Include request IDs for correlation
- Never expose stack traces in production

## Logging Guidelines

**Log these:**

- Authentication attempts (success and failure)
- Authorization failures
- Input validation failures
- System errors

**Never log:**

- Passwords or tokens
- Full credit card numbers
- Personal identifiable information (PII)
- Session tokens or API keys
```

---

## docs/README.md

Create it when absent; when it exists, add only the agent-guidance link.

```markdown
# Documentation

| Document                                                         | Purpose                                         |
| ---------------------------------------------------------------- | ----------------------------------------------- |
| [AGENT_GUIDANCE.md](AGENT_GUIDANCE.md)                           | Agent instruction architecture and doc routing  |
| [ARCHITECTURE.md](ARCHITECTURE.md)                               | System structure and key components             |
| [CODE_STANDARDS.md](CODE_STANDARDS.md)                           | Conventions, with examples from this repository |
| [GIT_WORKFLOW.md](GIT_WORKFLOW.md)                               | Branches, commits, pull requests                |
| [SECURITY_AND_ERROR_HANDLING.md](SECURITY_AND_ERROR_HANDLING.md) | Security rules and error handling               |
```

List only documents that exist.

---

## docs/AGENT_GUIDANCE.md

The routing heading must be exactly `## Documentation routing`, and every path
in the table's second column must resolve (`check-agent-guidance.sh` fails on
a dead path). Include the Knowledge row only when `KNOWLEDGE_DIR_EXISTS=true`,
and a Testing row only when `docs/TESTING.md` exists; otherwise delete the row.
Never leave a row outside the table.

```markdown
# Agent Guidance and Documentation Routing

This document is the tool-neutral source for how this repository organizes
agent instructions and routes tasks to documentation.

## Instruction architecture

1. Root `AGENTS.md` is the small operating contract: every rule a session
   started at the repository root must obey, the complete local gate, and a
   pointer to this document.
2. Nested `AGENTS.md` files add rules for one subtree only. Several tools never
   load them, so they never hold a rule a root session needs. Each is listed in
   root `AGENTS.md` and has a sibling `CLAUDE.md` whose line 1 is `@AGENTS.md`.
3. `CLAUDE.md` is an adapter. Line 1 is exactly `@AGENTS.md`; below it is
   Claude Code behavior only, under headings that name Claude Code. Other tools
   also read `CLAUDE.md`, so it carries no commands, rules or routing.
4. `docs/` owns explanations, procedures and reference material.

Instruction files are regular files: no symlinks, no imports from outside the
repository. `scripts/check-agent-guidance.sh` enforces this in CI (job
`agent guidance`).

## Documentation routing

Match the task to a row, then read only the documents it names.

| Topic        | Primary Docs                        | Keywords                                    |
| ------------ | ----------------------------------- | ------------------------------------------- |
| Architecture | docs/ARCHITECTURE.md                | architecture, design, components, structure |
| Git          | docs/GIT_WORKFLOW.md                | git, branch, commit, merge, pull, request   |
| Security     | docs/SECURITY_AND_ERROR_HANDLING.md | security, validation, secrets, auth, input  |
| Testing      | docs/TESTING.md                     | test, coverage, unit, integration, e2e      |
| Standards    | docs/CODE_STANDARDS.md              | naming, style, format, convention           |
| Knowledge    | .nightgauge/knowledge/              | knowledge, prd, decision, adr, provenance   |
```

---

## AGENTS.md (root)

Rules for this file:

- The first heading names the project, never a tool.
- The provenance marker stays on the line below the heading.
- It never tells the reader to read `CLAUDE.md` first and never imports it.
- It names the complete local gate, and every gate command also runs in a
  pull-request CI workflow (reconcile against `.github/workflows/`).
- It lists every nested `AGENTS.md`, or says there are none.
- At most 200 lines. Move explanations to `docs/`.
- It never carries the Nightgauge workspace-rules block: that block belongs to
  the Nightgauge product repositories, and downstream projects fail the check
  with `--workspace-block forbidden` if it is present.

````markdown
# [Project Name]

<!-- nightgauge:agent-guidance v1 -->

[One paragraph: what this repository is, from README and code analysis.]

Before substantive work, use [docs/AGENT_GUIDANCE.md](docs/AGENT_GUIDANCE.md)
to select the relevant documentation. Keep explanations and procedures in
`docs/`; this file holds only rules every session needs.

This repository has no nested `AGENTS.md` files.
<!-- Or: "Nested instruction files: `packages/api/AGENTS.md`, ..." — one per line. -->

## Commands

```bash
[Real install/build/test/lint commands from package.json, Makefile, go.mod, CI]
```

The complete local gate is `[single command, or the ordered list above]`. Run
it before pushing; CI runs the same commands on every pull request.

## Non-obvious project rules

- [Rule an agent cannot infer from code]
- [Repository-specific validation or safety constraint]

## Git workflow

Never push directly to `main`. Use a feature branch and a pull request; see
[docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md).

## Security

Never hardcode secrets. Validate input at system boundaries and use
parameterized queries; see
[docs/SECURITY_AND_ERROR_HANDLING.md](docs/SECURITY_AND_ERROR_HANDLING.md).

## Knowledge base

Issue-specific PRDs and decision logs live under `.nightgauge/knowledge/`
(`epics/{N}-{slug}/` and `features/{N}-{slug}/`, each with `PRD.md` and
`decisions.md`). Read them before implementing that issue.
````

Delete the `## Knowledge base` section when `KNOWLEDGE_DIR_EXISTS=false`.

---

## CLAUDE.md (root)

Create only when Claude Code is a selected tool. Line 1 is exactly
`@AGENTS.md` — nothing above it, not even a title. Cursor, Copilot and VS Code
also load this file, so everything below the import is Claude Code behavior
only, under headings that name Claude Code. No commands, no rules, no routing
table: those live in `AGENTS.md` and `docs/AGENT_GUIDANCE.md`. At most 100
lines; usually under 20.

```markdown
@AGENTS.md

# Claude Code adapter

Other tools also load this file, so everything below applies to Claude Code
only. Repository rules, commands and documentation routing live in `AGENTS.md`
and `docs/AGENT_GUIDANCE.md`; do not add them here.

## Claude Code: [topic]

[Only behavior unique to Claude Code, e.g. `.claude/rules/` scoping, subagent
model choice, what to preserve across `/compact`. Delete this section when
there is none.]
```

Keep only lines whose removal would make Claude Code make a mistake. Never add
directory listings, self-evident advice ("write clean code"), standard language
conventions, or anything discoverable by reading the code.

---

## Nested AGENTS.md and its sibling CLAUDE.md

Propose one only for a subtree with its own toolchain or rules (see the
migration reference for detection). The root `AGENTS.md` lists its path, and
the root-to-nested chain stays under 32 KiB.

````markdown
# [Subtree name]

<!-- nightgauge:agent-guidance v1 -->

Rules for `[path/]` only. They add to the root `AGENTS.md` and never restate,
weaken or override it.

## Commands

```bash
[Commands that run from this directory, taken from its manifest and CI job]
```

## Rules

- [Rule that applies to this subtree and nowhere else]
````

Sibling `CLAUDE.md` in the same directory (a regular file, never a symlink):

```markdown
@AGENTS.md
```

---

## .github/copilot-instructions.md

Create only when GitHub Copilot is a selected tool. Per GitHub's
[custom instructions support matrix](https://docs.github.com/en/copilot/reference/custom-instructions-support),
`AGENTS.md` is read by the Copilot cloud agent, Copilot CLI, code review and
VS Code chat; github.com Chat, Visual Studio, JetBrains, Eclipse and Xcode chat
and IDE code review read only this file. So it is a pointer, at most 30 lines,
that references `AGENTS.md` and restates nothing.

```markdown
# Repository instructions

Read and follow the repository-root `AGENTS.md`; it is the project's operating
contract. Use `docs/AGENT_GUIDANCE.md` to select relevant documentation.

This file restates no rules. It exists for Copilot surfaces that do not load
`AGENTS.md`.
```

---

## Cursor and Kiro

Cursor always applies root and nested `AGENTS.md` (and `CLAUDE.md`), and Kiro
always includes root and nested `AGENTS.md`. Neither needs a generated file.
Create `.cursor/rules/*.mdc` or `.kiro/steering/*.md` only for a rule that is
genuinely specific to that tool (for example a glob-scoped Cursor rule), and
never restate `AGENTS.md` in it. Likewise, do not create `.cursorrules`,
`.windsurfrules` or `GEMINI.md` copies of `AGENTS.md`; Gemini CLI can load
`AGENTS.md` directly through its `context.fileName` setting.
