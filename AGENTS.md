# Nightgauge Agent Instructions

This is the tool-neutral operating contract for agents working in this
repository. Compatible tools read it directly; Claude Code loads it through the
`@AGENTS.md` import on line 1 of `CLAUDE.md`.

## Repository scope

Nightgauge is an AI-powered Issue-to-PR pipeline with a Go deterministic layer,
a VS Code extension, a TypeScript SDK, portable Agent Skills, and tool-specific
adapters.

Before substantive work, use
[docs/AGENT_GUIDANCE.md](docs/AGENT_GUIDANCE.md) to select the relevant
documentation. Keep durable explanations and procedures in `docs/`, not in
agent configuration files.

This repository has no nested `AGENTS.md` files. Add one only as described in
[docs/AGENT_GUIDANCE.md](docs/AGENT_GUIDANCE.md#nested-instruction-files), and
list its path here.

<!-- nightgauge-workspace-rules:begin v1 -->

## Workspace-wide rules

These rules bind every repository in the Nightgauge workspace. The root
`AGENTS.md` of `nightgauge/nightgauge` is canonical: change the block there
first, then copy it byte-for-byte into each repository. The end marker records
the SHA-256 of the lines between the markers.

- **Start the session in the repository that owns the issue.** One repository
  per session; cross-repository work is one session per changed repository.
  Reading a sibling is fine; changing it needs that repository's rules and gate.
- **Handoffs** live in `nightgauge-internal/runbooks/handoffs/<repo>.md`.
  Verify a handoff against the repository before trusting it.
  `.nightgauge/session-handoff.md` is per-machine runtime state, not a handoff.
- **GitHub identity:** scope each command with
  `GH_TOKEN=$(gh auth token --user <account>) gh ...`. Never `gh auth switch`.
  That form stays correct for actions the maintainer owns (merges, their
  comments). Pipeline traffic authenticates as the GitHub App installation
  when `github_auth.app` is configured, on the installation's own quota.
- Use a feature branch and a pull request; never push to `main`. Run this
  repository's complete local gate, defined in this file, once before pushing.
- **Merge** with `gh pr merge --squash` once required checks are green. Never
  `--auto`. `--admin` bypasses the entire ruleset and is an emergency hatch.
  Green checks are the go signal: merge instead of stopping to ask.
- Never dismiss a failing test as flaky without root-causing it.
- **After merge**, run `scripts/post-merge-check.sh <merge-sha>` and read its
  exit code without a pipe: `0` green; `1` red, so fix `main` now and never
  re-run hoping for a better answer; `2` not yet observable, so wait and re-run.
- **Roll up the board:**
  `nightgauge hook post-merge --issue <N> --owner nightgauge --repo <repo> --pr <PR> --project <board>`,
  where `<board>` is this repository's project number from
  `nightgauge project resolve --repo nightgauge/<repo> --json`. The hook is
  non-blocking, so read its output; exit `0` is not evidence.
- **Clean up** branch and worktree on both sides with
  `nightgauge-internal/scripts/branch-cleanup.sh`; judge one branch with
  `scripts/branch-merged-check.sh` (only exit `0` authorizes deletion). Never
  hand-write deletion. The pipeline removes the branches it creates.
- "The tool is missing", "the tool is blocked" and "this invocation form is
  blocked" are different diagnoses. Try the existing tool before recording a
  chore as undoable.
- Before concurrent work, compare likely file sets and sequence overlaps. Broad
  mechanical sweeps (renames, redactions, codemods) land alone, over settled
  code.
- Capture every background process PID at spawn, kill that PID, and verify it
  is dead. Never rely on `jobs` from a later shell.
- Keep context lean: finish the scope, delegate bounded searches, and start a
  fresh session for new work.
- **Keep sessions short, and bound what each one is handed.** A turn is billed
  mostly for re-reading the context it inherits. Within one 732-turn session
  mean context per turn climbed 103k to 631k with no plateau, so cumulative
  spend is roughly quadratic in turn count and the last decile paid ~6x the
  first for the same work. Compaction is not a fix; it firing is the signal the
  ceiling was reached. Accumulation is not the only shape: across 155 pipeline
  stages none accumulated (median 1.8x first-to-last turn, bounded by a
  ~10-turn exit), yet first-turn context tracked the pasted diff's size at
  r=0.99 — 162k mean context per turn against 30k for the rest. So finish the
  scope and start fresh, spend fewer turns while in one (batch calls, do not
  poll, do not re-read context), and bound the payload a stage opens with. Run
  `nightgauge-internal/scripts/ws spend`; it reports context per turn per session.
- Instruction files are regular files. Never symlink them and never import
  across repositories.

<!-- nightgauge-workspace-rules:end sha256=31ef89af7ea5177c92587f1330dc2a9f882880f932e4d114f08e0f0c7e429df1 -->

## Repository operating contract

- This project is pre-customer. Remove superseded paths instead of adding
  compatibility shims, migration fallbacks, aliases, or deprecation knobs.
- A behavior-changing pull request includes a reader-facing entry under
  `## [Unreleased]` in `CHANGELOG.md`; also update the extension changelog for
  user-visible VS Code changes. Follow
  [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#changelog).
- Use conventional commits. The complete local gate is
  `bash scripts/ci-local.sh`: run it exactly once, after focused checks pass.
  It is the gate, not the iteration loop. The ordered requirements live in
  [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#pre-submission-validation-critical).
- **Concurrent `scripts/ci-local.sh` runs are supported — do not serialise on
  the gate.** Branches are written in parallel and each one gates itself; only
  merges serialise. The gate's heavy concurrent steps draw on a MACHINE-WIDE
  budget of `CI_LOCAL_JOBS` slots (default 4), keyed on the repository's shared
  git directory, so every worktree of one repository shares one budget. Several
  gates therefore take longer in wall clock and do not oversubscribe the box.
  Before #1983 that bound was per process, so three gates ran twelve heavy steps
  on twelve cores at load 58 and children were killed mid-step; the gate said
  `exit 1` and named nothing. If you need the old behaviour for a measurement,
  raise `CI_LOCAL_JOBS`, and know what you are buying.
- A step the gate marks `!` `[INFRASTRUCTURE — the check could not run]` has
  asserted nothing about your change: re-run it. That is not licence to dismiss
  a red as flaky — a red nobody can explain is a bug in the gate and wants an
  issue. A suite declares this by printing a `HARNESS ERROR` line; if you write
  a gate suite, distinguish "an arm asserted false" from "the arm could not
  run" and say which.
- The merge, post-merge and cleanup rules above are expanded in
  [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#after-merge).
- Ship the strongest in-scope solution. Do not create menus of inferior and
  preferred fixes when the repository evidence selects one answer.

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
belong in `nightgauge-internal`. The publication guard scans files, not issue
or epic bodies, so keep that material out of those too. If classification is
uncertain, keep the work private until the boundary is resolved.

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
