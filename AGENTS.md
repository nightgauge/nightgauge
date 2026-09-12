# Nightgauge Agent Instructions

This is the canonical, tool-neutral contract for agents working in this
repository. Claude Code imports it from `CLAUDE.md`; compatible tools read it
directly.

## Repository scope

Nightgauge is an AI-powered Issue-to-PR pipeline with a Go deterministic layer,
a VS Code extension, a TypeScript SDK, portable Agent Skills, and tool-specific
adapters. Start every session in the repository that owns the issue. Reading a
sibling repository is allowed; changing one requires a separate session using
that repository's rules and validation gate.

Before substantive work, use
[docs/AGENT_GUIDANCE.md](docs/AGENT_GUIDANCE.md) to select the relevant
documentation. Keep durable explanations and procedures in `docs/`, not in
agent configuration files.

## Non-negotiable operating contract

- This project is pre-customer. Remove superseded paths instead of adding
  compatibility shims, migration fallbacks, aliases, or deprecation knobs.
- A behavior-changing pull request includes a reader-facing entry under
  `## [Unreleased]` in `CHANGELOG.md`; also update the extension changelog for
  user-visible VS Code changes. Follow
  [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#changelog).
- Use a feature branch and conventional commits. Never push directly to
  `main`; every change lands through a pull request.
- Before any push, run the complete local gate exactly once after focused
  checks pass: `bash scripts/ci-local.sh`. The ordered requirements live only
  in
  [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#pre-submission-validation-critical).
- Merge manually with `gh pr merge --squash` only after required checks are
  present and green. Never use auto-merge, and treat `--admin` as an emergency
  ruleset bypass, not a routine path.
- After a merge, run `scripts/post-merge-check.sh <merge-sha>` until it returns
  `0`, then run `nightgauge hook post-merge` with the issue, PR, repository, and
  project. Read the hook output. Follow the complete sequence in
  [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#after-merge).
- Finish merge cleanup with the existing workspace sweep
  `nightgauge-internal/scripts/branch-cleanup.sh`. Do not hand-write branch or
  worktree deletion logic.
- Before concurrent issue work, compare likely file sets. Sequence work whose
  file sets overlap or are uncertain.
- Capture every background process PID when spawning it, terminate that PID
  explicitly, and verify it is dead. Never depend on `jobs` from a later shell.
- Ship the strongest in-scope solution. Do not create menus of inferior and
  preferred fixes when the repository evidence selects one answer.

## GitHub identity

Never run `gh auth switch` in a multi-user workspace. Scope each GitHub command
with the token for the repository's configured account:

```bash
GH_TOKEN=$(gh auth token --user <repo-account>) gh <command> ...
```

Git pushes use SSH and are unaffected.

## Security and publication boundary

Before security-sensitive work, read and follow
[standards/security.md](standards/security.md). Never commit secrets, expose
credentials or personal data, interpolate untrusted SQL, or accept unvalidated
input at a system boundary.

Before creating issues, plans, ADRs, or documentation, read
[docs/PUBLIC_CORE_BOUNDARY.md](docs/PUBLIC_CORE_BOUNDARY.md) and
[docs/DOCUMENTATION_IA.md](docs/DOCUMENTATION_IA.md). This is an Apache-2.0
public tree. Hosted-service implementation, commercial context, customer data,
private topology, private issue references, raw research, and execution logs
belong in `nightgauge-internal`. If classification is uncertain, keep the work
private until the boundary is resolved.

Do not manually lower `issue_references.tree_baseline` when the publication
checker says a count fell because the issue ceiling moved. Keep `origin/main`
fetched so the checker can derive the current ceiling.

## Knowledge and documentation

When pipeline context provides `knowledge_path`, read its `PRD.md` and
`decisions.md` before implementation. Record issue-local decisions using the
schema in [docs/KNOWLEDGE_BASE.md](docs/KNOWLEDGE_BASE.md); stable,
cross-cutting decisions belong in `docs/`.

Use existing documentation before creating a new file. The documentation
index and keyword routing table are in
[docs/AGENT_GUIDANCE.md](docs/AGENT_GUIDANCE.md). Documentation must stay
concise, public-safe, and tool-neutral.

## Repository conventions

- Use kebab-case for files and directories, except specified template files in
  `SCREAMING_SNAKE_CASE`.
- Agent Skills use `SKILL.md`; plugin manifests use `plugin.json`.
- Keep lines under 100 characters where practical and add language identifiers
  to fenced code blocks.
- Do not duplicate standards or procedures. Reference their canonical document.
- Do not add a skill without its catalog documentation, or downgrade an
  existing skill or plugin version.

Use [CONTRIBUTING.md](CONTRIBUTING.md) for content-creation patterns and
[docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md) for branch, validation, review,
release, and cleanup procedures.
