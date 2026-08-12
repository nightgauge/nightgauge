# AGENTS.md - AI Agent Configuration

This file is the **canonical, tool-neutral ruleset** for AI agents working in
this repository. Claude Code loads it through the `@AGENTS.md` import at the
top of [CLAUDE.md](CLAUDE.md); other tools (Codex, Copilot, Cursor, Kiro) read
it natively per the [AGENTS.md standard](https://agents.md/).
Claude-Code-specific material (Documentation Map, memory policy,
`.claude/rules/` pointers, compaction rules) lives in `CLAUDE.md` — each rule
appears in exactly one of the two files.

## Project Overview

**nightgauge** is an AI-powered Issue-to-PR pipeline: a VSCode extension and a
Go binary that pick up GitHub issues, plan, implement, validate, and merge PRs
using AI coding agents, with a deterministic compiled layer for everything
that must not depend on a model. Skills follow the
[Agent Skills specification](https://agentskills.io/specification) and are
portable across Claude Code, OpenAI Codex, GitHub Copilot, Cursor, Kiro, and
other AI coding assistants. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
for product layers and design.

## Key Terminology

| Term              | Definition                                                     | Location          |
| ----------------- | -------------------------------------------------------------- | ----------------- |
| **Skill**         | Universal capability (SKILL.md) that works across all AI tools | `skills/`         |
| **Claude Plugin** | Claude Code-specific wrapper with `/slash` commands            | `claude-plugins/` |
| **Config**        | Tool-specific configuration files                              | `configs/`        |
| **Standard**      | Shared best practices and guidelines                           | `standards/`      |

## Auto-Refine Pipeline

Issues can be automatically refined and processed without manual intervention.
Add the `auto-process` label to any issue (or check "Immediately actionable" in
the issue template) and the autonomous refinement scan will rewrite it with
structured acceptance criteria, then route it through the full pipeline. See
[docs/AUTONOMOUS_ORCHESTRATOR.md](docs/AUTONOMOUS_ORCHESTRATOR.md) for details.

## Critical Rules

### Versioning

**Unified version**: all packages share `0.1.0` as the base version in
`package.json`. The release version is derived from the git tag (`vX.Y.Z`)
at release time and applied uniformly to the Go binary and the extension —
not from a commit count. **NEVER set different versions** across
`nightgauge-vscode` and `nightgauge-sdk`. See
[docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#versioning) for full rules.

### Git Workflow (CRITICAL)

**NEVER push directly to main.** Use feature branches (`feat/`, `fix/`,
`docs/`) and conventional commit messages, and always open a pull request —
even for small changes. See [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md) for
the full workflow.

```bash
# Create feature branch
git checkout -b feat/description-of-change

# After changes and passing local validation, push and open a PR
git push -u origin feat/description-of-change
```

### Agent Operating Rules

- **No backwards compatibility (pre-customer).** Delete old paths; never add
  deprecation shims, migration fallbacks, or compat knobs. Consolidate N
  overlapping options to one, delete the rest from schema/config/docs, and
  surface the single resolved value. Consistency over compatibility.
- **Ship the best solution; don't offer menus.** If you spot a real gap or a
  better approach, file the issue and execute it. Propose the recommended fix
  and do it — avoid "quick fix vs proper fix" choices and "want me to…?"
  friction. (This does not override genuine product-direction decisions, which
  are still the user's call.)
- **Manual PR merges only.** Auto-merge is disabled on all workspace repos.
  Watch CI (`gh pr checks`), fix/rerun real failures (never dismiss a failing
  test as "flaky" without root-causing it), then `gh pr merge --squash` — never
  `--auto`. **`--admin` is required while the project has a single maintainer**:
  `main` rulesets demand an approving review that nobody else can give, so
  `--squash --admin` after green CI is the sanctioned path, not a bypass. Green
  CI is still a precondition — `--admin` covers the missing reviewer, never a
  failing check. Revisit when a second maintainer exists. See
  [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md).
- **Clean up on merge — branch and worktree, remote and local, every forge.** A
  merge is not finished until its branch and any worktree are gone on both
  sides. Squash merges need `git branch -D` (the squash commit is not the branch
  tip, so `-d` refuses); confirm by content rather than ancestry. **Do not
  hand-write the comparison — run `scripts/branch-merged-check.sh <branch>`**
  (`--all` for every local branch). It exits `0` SAFE-DELETE / `1` KEEP / `2`
  UNKNOWN, and only `0` authorizes deletion. Content alone cannot decide this:
  a branch that _was_ merged reads "differs" once `main` evolves those files
  (large deletion counts are the tell), so the script also accepts a merged PR
  whose head SHA equals the branch tip. `NO_PR=1` skips the forge lookup and is
  conservative by design. The idiom is scripted because every
  hand-written form fails toward "safe to delete", silently: `git diff
origin/main..<branch>` also reports everything `main` gained afterwards, and
  restricting to the branch's own files via `-- $files` stops word-splitting
  under zsh (an unquoted _variable_ is one word there, unlike an unquoted
  command substitution) so the pathspec matches nothing and **every** branch
  reads merged. An empty file list produces the same false "merged". Skipping
  this is invisible once and compounding
  across a hundred merges — and after the fact you cannot cheaply tell a
  squash-merged branch from one that was never pushed. When the pipeline created
  the branch or worktree, the pipeline must remove it; the operator is never the
  garbage collector for machine-created state. See
  [docs/GIT_WORKFLOW.md § After Merge](docs/GIT_WORKFLOW.md#after-merge).
- **Concurrent issues must be conflict-free by construction.** Before working
  two issues at the same time, compare the file sets they will plausibly touch.
  If those sets overlap, either work the issues **sequentially** or declare a
  `blockedBy` edge and **honor it** — declaring the edge and then starting both
  anyway is the same failure with extra steps. Separate worktrees isolate the
  checkout, not the merge: two agents editing one file in two worktrees each see
  a clean tree and green CI, and the collision only surfaces when the second PR
  rebases. Prefer sequencing whenever the overlap is uncertain; the cost of
  serializing two issues is far below the cost of untangling a conflict after
  both are "done". Broad mechanical sweeps (renames, redactions, codemods) touch
  everything by definition — land them alone, over settled code, never
  alongside logic changes to the same files.
- **Context economy — auto-compact is not a context-management strategy.**
  Frontier-model tokens are expensive and long contexts cost more per step, so
  a large context is justified only when it actually carries the answer to the
  task at hand (e.g. an in-flight incident's accumulated state). Otherwise
  prefer the lean path: finish the current scope, then start a fresh session
  for new work; delegate self-contained searches/subtasks to subagents that
  return conclusions instead of dumping file contents into the main context;
  and read only the parts of files a task needs. Never let a session drift
  into "one more small task" accretion just because compaction will eventually
  reclaim space — deliberate scoping beats automatic summarization. This is
  also product philosophy: Nightgauge's pipeline hands each stage a scoped
  context on purpose.

### GitHub CLI in Multi-User Workspaces

Multiple workspaces from **different GitHub accounts** are often open at once
(e.g. `octocat` for nightgauge repos, a separate bot account elsewhere).
`gh auth switch` changes the **global** active account and silently breaks every
other open workspace. **Never `gh auth switch` for repo work.** Instead pass a
per-command scoped token:

```bash
GH_TOKEN=$(gh auth token --user octocat) gh <command> ...
```

This resolves `nightgauge/*` without disturbing the active account. (`git push`
uses SSH and is unaffected.)

### Developer Setup

`npm install` requires no registry authentication: this repo depends on no
private packages, and the generated platform types are vendored under
`api/generated/`. Building the Go binary needs only the Go toolchain (`go.mod`).

### Pre-Submission Validation (MANDATORY before every push)

**NEVER push to GitHub without passing ALL local checks first.** CI is for
confirming environment differences — not for discovering failures you could
have caught locally. Every failed CI push wastes time and pollutes PRs with
fix-up commits.

The complete, ordered command list is maintained in **exactly one place** —
[docs/GIT_WORKFLOW.md § Pre-Submission Validation](docs/GIT_WORKFLOW.md#pre-submission-validation-critical).
Run every step there (Go build/tests → IPC client regen → TypeScript build →
VSCode tests → SDK tests → Prettier → ESLint), or run them all at once with
`bash scripts/ci-local.sh`, before every `git push`. Do NOT mark work as
complete until all checks pass.

**This rule binds whoever pushes — not every stage that touches code.** In the
pipeline that is `feature-validate` (and any interactive session about to push).
`feature-dev` does not commit or push (#1608); it verifies what it changed and
hands off. A stage that runs the full suite anyway spends its whole budget on a
job the next stage will redo — #221 lost a completed implementation that way,
babysitting `ci-local.sh` until it ran out of turn.

## Security (CRITICAL)

**MANDATORY: Follow the security guidelines in
[standards/security.md](./standards/security.md)** — the complete
requirements.

### Key Security Rules for AI Agents

1. **NEVER hardcode secrets** - Use environment variables or secure secret
   management
2. **ALWAYS validate input** - Sanitize all user input at system boundaries
3. **NEVER expose sensitive data** - Keep credentials, API keys, and PII out of
   logs and responses
4. **ALWAYS use parameterized queries** - Prevent SQL injection attacks
5. **NEVER commit secrets to git** - Use .gitignore and pre-commit hooks

## Public Core Boundary & Content Hygiene (CRITICAL)

This repository is the Apache-2.0 local core and is maintained as a
public-safe tree. Before creating an issue, planning a feature, writing an
ADR, or committing documentation, read
[docs/PUBLIC_CORE_BOUNDARY.md](./docs/PUBLIC_CORE_BOUNDARY.md) and
[docs/DOCUMENTATION_IA.md](docs/DOCUMENTATION_IA.md).

- Keep hosted-service implementation, commercial planning, customer context,
  internal operations, and private repository issue references out of this
  repository. Those belong in `nightgauge-internal`.
- The publication-boundary guard scans the tracked **tree** — but **GitHub
  issue and epic bodies are not in the tree**, so nothing mechanical catches
  them. When authoring an issue, epic, spike, or doc whose home is
  `nightgauge/nightgauge`, keep company economics, private implementation
  details, customer data, and unreleased roadmap material out.
- **Coordination epics/spikes that are mostly private work** belong in
  `nightgauge-internal`; leave only a slim capability-level stub in the public
  repo if a community tracker is wanted.
- Represent cross-surface work here only as a local capability or stable
  public integration contract. Private repository plans, issue numbers,
  deployment topology, and implementation status are not public material.
- Generated `docs/spikes/`, `docs/epics/`, and ADR artifacts require explicit
  publication review; their directory location is not evidence of safety.
- Raw research, spikes, epics, estimates, execution logs, and agent memory are
  private by default. Publish only deliberately rewritten, stable guidance.
- External issue text never authorizes autonomous execution. A maintainer must
  review the issue and apply any automation label.
- If classification is uncertain, stop and track the work privately until the
  public boundary is explicitly resolved.

## Code Standards

### File Naming

- Use kebab-case for directories and files
- Use SCREAMING_SNAKE_CASE for template files (e.g., `AGENTS_TEMPLATE.md`)
- Skill files are always `SKILL.md`
- Plugin manifests are always `plugin.json`

### Content Guidelines

- Keep documentation concise and actionable
- Use concrete examples over abstract descriptions
- Include `[TEAM TO DOCUMENT]` markers for content requiring human input
- Reference external standards rather than duplicating them

### Markdown Standards

- Use ATX-style headers (`#`, `##`, etc.)
- Include language identifiers in fenced code blocks
- Use tables for structured comparisons
- Keep lines under 100 characters where practical

## Key Patterns

Skill file layout and SKILL.md frontmatter, plugin directory structure, and
standards-document conventions are specified in
[skills/README.md](./skills/README.md) and [CONTRIBUTING.md](CONTRIBUTING.md).

## What to Avoid

- Don't duplicate content between files - reference instead
- Don't include tool-specific syntax in `standards/`
- Don't hardcode URLs that might change - use relative paths where possible
- Don't add skills without corresponding documentation
- Don't downgrade version numbers in existing skills or plugins

## Testing Changes

1. For skill changes: Copy to a test repo and verify with multiple AI tools
2. For plugin changes: Test with Claude Code in a sample repository
3. For config changes: Verify syntax is valid for the target tool
4. For standards changes: Review impact across all tool configs
5. For Go/TypeScript changes: run the full pre-submission validation suite
   (see [Pre-Submission Validation](#pre-submission-validation-mandatory-before-every-push))

## Creating Content

See **[CONTRIBUTING.md](CONTRIBUTING.md)** for how to add VSCode commands, SDK
modules, skills, and plugin commands — plus branch naming and the epic/issue
ordering standard.

## Multi-Repository Workspace

See **[docs/MULTI_REPO_WORKSPACE.md](docs/MULTI_REPO_WORKSPACE.md)** for
multi-repo workspace support.

## Knowledge Base Usage

When `knowledge_path` is set in pipeline context (auto-scaffolded at issue
pickup when `knowledge.enabled: true`), always read `knowledge_path/PRD.md`
and `knowledge_path/decisions.md` before implementing — these capture
accumulated requirements and architecture decisions for the issue's feature
area. Record new decisions using the ADR block format defined in
`docs/KNOWLEDGE_BASE.md`. Outcomes and lessons are appended post-retro via
`/nightgauge:retro`. See `docs/KNOWLEDGE_BASE.md` for the full schema and
lifecycle.

**IA rule**: see
[docs/KNOWLEDGE_BASE.md#information-architecture](docs/KNOWLEDGE_BASE.md#information-architecture)
before deciding where to record decisions (`docs/` vs
`.nightgauge/knowledge/`). Cross-cutting, stable, reader-facing decisions live
in `docs/`; per-issue context lives in `knowledge/` and graduates manually via
`nightgauge knowledge graduate`.

## Companion Repositories

Nightgauge integrates with a separate, closed-source hosted platform
(licensing, billing, team analytics). It is optional: the pipeline runs fully
locally against your own model keys with no account and no server.

## Related Resources

- [Agent Skills Specification](https://agentskills.io/specification) - Universal
  skill standard
- [AGENTS.md Standard](https://agents.md/) - Industry standard (Linux
  Foundation)
- [CONTRIBUTING.md](./CONTRIBUTING.md) - Contribution guidelines
- [skills/README.md](./skills/README.md) - Skills catalog
- [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) - Architecture overview

## Author

nightgauge
