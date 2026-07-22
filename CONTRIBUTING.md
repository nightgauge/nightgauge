# Contributing to Nightgauge

Thank you for your interest in contributing! This guide will help you add new
features, skills, or improvements to Nightgauge.

## Scope — what belongs in this project

Everything in this repository is Apache-2.0 and runs locally with user-managed
model and forge credentials. Contributions are welcome across the VS Code
extension, SDK, skills, Claude plugin, and Go binary.

Private service implementations, commercial plans, and company-internal
operations are outside this repository. If a proposal requires one of those
surfaces, open an issue to discuss the public integration contract first. See
[GOVERNANCE.md](GOVERNANCE.md#project-boundary),
[docs/PUBLIC_CORE_BOUNDARY.md](docs/PUBLIC_CORE_BOUNDARY.md), and
[VISION.md](VISION.md).

## Your first contribution

Nightgauge is developed on GitHub with a **fork-and-pull-request** workflow. You
do **not** need push access to the canonical repository — external contributors
work from a personal fork.

> **Maintainers** push branches directly to `nightgauge/nightgauge`.
> **Contributors** push to their own fork and open a pull request against the
> canonical repository. A `git push` straight to `nightgauge/nightgauge` that
> returns `403` is expected when you are not a maintainer — use the fork flow
> below instead.

```bash
# 1. Fork and clone (creates your fork, clones it, and sets `origin` to the fork)
gh repo fork nightgauge/nightgauge --clone

# 2. Add the canonical repo as `upstream` so you can stay in sync
cd nightgauge
git remote add upstream https://github.com/nightgauge/nightgauge.git

# 3. Branch from an up-to-date main
git fetch upstream
git checkout -b feat/short-description upstream/main

# 4. Make your changes, then push the branch to YOUR fork (`origin`)
git push -u origin feat/short-description

# 5. Open a PR against the canonical repo
gh pr create --repo nightgauge/nightgauge --base main
```

On your first pull request the CLA Assistant bot prompts you to agree to the
individual [Contributor License Agreement](CLA/individual.md) — a one-time step. See
[Prerequisites](#prerequisites) for the toolchain, and
[Finding something to work on](#finding-something-to-work-on) for good starter
issues. Please also read the [Code of Conduct](CODE_OF_CONDUCT.md); for
security-sensitive reports follow [SECURITY.md](SECURITY.md) and never open a
public issue for a vulnerability.

## Prerequisites

Nightgauge is a Go + TypeScript monorepo — install the full toolchain before
building:

| Requirement                          | Why                                                                             |
| ------------------------------------ | ------------------------------------------------------------------------------- |
| **Go ≥ 1.26** (see `go.mod`)         | The deterministic core is a compiled Go binary (`cmd/nightgauge/`, `internal/`) |
| **Node.js 24** (pinned in `.nvmrc`)  | VSCode extension, SDK, and generated-type tooling                               |
| **git** + **gh CLI** (authenticated) | Branching, forks, and pull requests                                             |

Bootstrap from the repository root:

```bash
nvm use            # selects Node 24 from .nvmrc
npm install        # root install — no registry auth needed; platform types are vendored
go build ./...     # compiles the Go binary and all Go packages
```

- Go build/test targets live in the [`Makefile`](Makefile) — `make build-cli`,
  `make test-go`, `make generate-ipc-client`.
- Run the full CI-parity suite locally before every push:
  `bash scripts/ci-local.sh` (Go build/tests → generated-file sync → lint →
  Prettier → workspace builds → tests). The authoritative, ordered command list
  lives in
  [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#pre-submission-validation-critical).

## Finding something to work on

- Browse
  [`good first issue`](https://github.com/nightgauge/nightgauge/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)
  for self-contained starter tasks, or
  [`help wanted`](https://github.com/nightgauge/nightgauge/issues?q=is%3Aissue+is%3Aopen+label%3A%22help+wanted%22)
  for larger asks.
- Have a question or an idea? Start in
  [GitHub Discussions](https://github.com/nightgauge/nightgauge/discussions)
  rather than opening an issue.
- **File an issue before starting a large refactor or a new feature**, so the
  approach can be agreed up front and you avoid reworking a PR later. Small
  fixes (typos, docs, obvious bugs) can go straight to a PR.

### What to expect after you open a PR

- Issues, discussions, and PRs are triaged **weekly**. This is a solo-maintained
  project, so there is no guaranteed review turnaround — clear, reproducible,
  focused PRs are reviewed fastest. See [SUPPORT.md](SUPPORT.md).
- Releases ship **as needed** from `main` and are tagged `vX.Y.Z`; the release
  version is derived from the git tag and applied uniformly to the Go binary and
  the extension (see [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#versioning)).

## Overview

Nightgauge is an **AI-powered Issue-to-PR pipeline** with three main
components:

| Component            | Location                       | Purpose                               |
| -------------------- | ------------------------------ | ------------------------------------- |
| **VSCode Extension** | `packages/nightgauge-vscode/`  | Primary UI for pipeline orchestration |
| **Go Binary**        | `cmd/nightgauge/`, `internal/` | Deterministic layer (compiled CLI)    |
| **SDK**              | `packages/nightgauge-sdk/`     | Programmatic access and core logic    |
| **Skills**           | `skills/`                      | Portable instruction files            |
| **Claude Plugins**   | `claude-plugins/`              | CLI wrappers for Claude Code          |

### Core Principle: Single Source of Truth

**All skills must follow this pattern when generating documentation for target
repositories:**

- **docs/ folder is authoritative** — Create docs/ files FIRST (GIT_WORKFLOW.md,
  CODE_STANDARDS.md, etc.)
- **AI configs reference docs/** — AGENTS.md and CLAUDE.md reference docs/
  files, not duplicate them
- **Human developers and AI agents share the same source of truth**

See
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#documentation-architecture-philosophy)
for the complete explanation.

---

## Contributing to the VSCode Extension

The VSCode extension is the primary user interface for Nightgauge.

### Setup

```bash
cd packages/nightgauge-vscode
npm install
npm run build
```

### Key Areas

| Directory                                 | Purpose                                   |
| ----------------------------------------- | ----------------------------------------- |
| `src/services/`                           | Pipeline orchestration, state management  |
| `src/services/HeadlessOrchestrator.ts`    | Multi-stage pipeline execution            |
| `src/services/InteractiveOrchestrator.ts` | Single-stage conversational mode          |
| `src/utils/skillRunner.ts`                | Process spawning for both execution modes |
| `src/views/`                              | Tree views, dashboard, webviews           |
| `src/models/`                             | Data models (Repository, etc.)            |
| `src/utils/`                              | Token parsing, notifications, workspace   |
| `package.json`                            | Commands, settings, keybindings           |

**Note**: When implementing features that spawn Claude CLI processes, be aware
of the two execution modes. See
[docs/INTERACTIVE_MODE.md](docs/INTERACTIVE_MODE.md) for the architectural
differences between headless (automated, token-tracked) and interactive
(conversational, stdin open) modes.

### Multi-Repository Workspace Features

When contributing to multi-repo workspace features, key files are:

| Component                 | Location                                  |
| ------------------------- | ----------------------------------------- |
| `WorkspaceManager`        | `src/services/WorkspaceManager.ts`        |
| `RepositoryContextLoader` | `src/services/RepositoryContextLoader.ts` |
| `Repository`              | `src/models/Repository.ts`                |
| `RepositorySwitcher`      | `src/views/RepositorySwitcher.ts`         |
| Workspace detection       | `src/utils/workspaceDetection.ts`         |

See [docs/MULTI_REPO_WORKSPACE.md](docs/MULTI_REPO_WORKSPACE.md) for
architecture details.

### Adding a New Command

1. Add command definition in `package.json` under `contributes.commands`
2. Register handler in `src/extension.ts`
3. Add menu contribution if needed under `contributes.menus`
4. Update keybindings if applicable

### Adding a New Setting

1. Add to `package.json` under `contributes.configuration.properties`
2. Access via `vscode.workspace.getConfiguration('nightgauge')`

### Testing

```bash
npm run test        # Run tests in watch mode
npm run test:run    # Run tests once
```

---

## Contributing to the SDK

The SDK provides programmatic access to Nightgauge pipeline functionality.

### Setup

```bash
cd packages/nightgauge-sdk
npm install
npm run build
```

### Key Areas

| Directory       | Purpose                    |
| --------------- | -------------------------- |
| `src/pipeline/` | Pipeline execution engine  |
| `src/context/`  | Context file I/O           |
| `src/tracking/` | Token usage tracking       |
| `src/cli/`      | CLI interface and adapters |

#### Execution Adapters

The SDK includes adapters for multiple AI backends in `src/cli/adapters/`. When
adding a new adapter, implement the `ICliAdapter` interface and register it in
`AdapterRegistry`. See existing adapters (`ClaudeSdkAdapter`, `GeminiAdapter`,
`GeminiSdkAdapter`) for patterns.

### Adding SDK Capabilities

1. Create module in appropriate directory
2. Export from `src/index.ts`
3. Add tests in `src/__tests__/`
4. Document in package README

---

## Contributing Skills

Skills are portable instruction files that define what each pipeline stage does.

### Quick Start: Adding a New Skill

#### 1. Create the Skill Directory

```bash
mkdir -p skills/your-skill-name
```

#### 2. Create SKILL.md

Create `skills/your-skill-name/SKILL.md` with proper frontmatter:

```yaml
---
name: your-skill-name
description: Brief description (1-2 sentences). Include when to use it.
license: Apache-2.0
metadata:
  author: Your Name
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
---

# Your Skill Name

## Description

Detailed description of what this skill does.

## Invocation

| Tool        | Command                   |
| ----------- | ------------------------- |
| Claude Code | `/your-skill` (via plugin) |
| Copilot     | Invoke via Agent Skills   |

## What It Does

Step-by-step explanation of the skill's behavior.

## Examples

Show example inputs and outputs.
```

##### Description quality checklist

The `description` is what the model uses to decide whether to invoke the skill,
so it must be precise. Before committing a new or edited skill, confirm its
`description` meets all of these (enforced by
`scripts/validate-skill-metadata.sh`):

- [ ] **Third person / imperative** — "Creates structured issues…", "Analyze…".
      Never "I create…", "We…", "You can…", or "My…".
- [ ] **States what _and_ when** — name what the skill does and the trigger
      context for using it (e.g. "Use after /feature-validate…", "Run weekly…",
      "Use when auditing a codebase…").
- [ ] **Non-empty, ≤ 1024 characters** — concise; no empty descriptions.
- [ ] **No literal XML/HTML tags** — placeholders like `<N>` are fine, but no
      `<tag>…</tag>` pairs.
- [ ] **Consistent terminology with the skill body** — the description and the
      `## Description` section must describe the same capability the same way.

#### 3. Add the Self-Assessment Epilogue (pipeline skills only)

**Pipeline skills** (the 6 core stages) must include the self-assessment
epilogue as their final phase. Add this include directive after your last
workflow phase and before the Error Handling section:

```markdown
### Phase {N}: Self-Assessment Epilogue

Output:
`<!-- phase:start name="self-assessment" index={LAST} total={TOTAL} stage="{stage}" -->`

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->
```

See [docs/SKILL_SELF_ASSESSMENT.md](docs/SKILL_SELF_ASSESSMENT.md) for the full
strategy.

#### 3a. Phase Markers (registry-first authoring)

Skills emit `<!-- phase:start ... -->` HTML comment markers that the
orchestrator parses to drive the dashboard's slot/stage progress. The canonical
inventory of pipeline-stage phases lives in
`packages/nightgauge-sdk/src/events/phaseRegistry.ts` (`PHASE_REGISTRY`).

When **adding, renaming, or removing a phase** in any of the 6 pipeline stages
(`issue-pickup`, `feature-planning`, `feature-dev`, `feature-validate`,
`pr-create`, `pr-merge`):

1. Update `PHASE_REGISTRY` in `phaseRegistry.ts` first — it is the single
   source of truth.
2. Update every `phase:start` marker in the matching `SKILL.md` (both the
   `index=` and the `total=` values).
3. Run `npx tsx scripts/validate-phase-markers.ts` and confirm exit 0.

The lint runs both as a CI step (`scripts/ci-local.sh`) and as a Vitest
assertion (`packages/nightgauge-sdk/tests/events/phaseRegistry.skill-emit.test.ts`).
Drift between the registry and any skill body fails both checks.

**Standalone skills** (skills that are not pipeline execution stages, e.g.
`nightgauge-issue-refine`, `nightgauge-docs-write`,
`nightgauge-continuous-improvement`) opt out of registry validation by
including this annotation near the top of the skill body:

```markdown
<!-- phase-registry: standalone-skill -->
```

Skills emitting a non-registry stage without the annotation fail the lint —
this forces a deliberate decision when introducing a new standalone skill.

#### 4. Update the Skills Catalog

Edit `skills/README.md` to add your skill.

#### 5. Test Your Skill

Test with Claude Code before submitting:

```bash
# Copy to a test repo and verify behavior
```

#### 6. Submit a Pull Request

```bash
git checkout -b feat/add-your-skill-name
git add skills/your-skill-name/ skills/README.md
git commit -m "feat: add your-skill-name skill"
git push -u origin feat/add-your-skill-name
```

### SKILL.md Format Reference

Skills follow the
[Agent Skills specification](https://agentskills.io/specification).

#### Required Frontmatter

```yaml
---
name: skill-name # kebab-case, matches directory name
description: Brief desc # 1-2 sentences, include when to use
license: Your license # Required
metadata:
  author: Author name # Person or organization
  version: "X.Y.Z" # Semantic versioning
  source: URL # GitHub URL or similar
allowed-tools: Tool1 Tool2 # Space-separated list of allowed tools
---
```

#### Allowed Tools

| Tool              | Purpose                |
| ----------------- | ---------------------- |
| `Read`            | Read files             |
| `Write`           | Create/overwrite files |
| `Edit`            | Edit existing files    |
| `Glob`            | Find files by pattern  |
| `Grep`            | Search file contents   |
| `Bash`            | Execute shell commands |
| `Task`            | Spawn subagents        |
| `AskUserQuestion` | Interactive questions  |
| `WebFetch`        | Fetch web content      |
| `WebSearch`       | Search the web         |

MCP server tools are also valid in `allowed-tools` using the
`mcp__<server>__<tool>` naming pattern. See
[docs/MCP_INTEGRATION.md](docs/MCP_INTEGRATION.md) for setup instructions and
recipes for popular MCP servers.

---

## Slash-Command Contract (ADR-007)

**The skill IS the slash command.** A canonical `skills/<name>/SKILL.md` is
bundled into the plugin by `scripts/install-agent-skills.sh` and registers
`/nightgauge:<name>` directly. There are **no** command-wrapper files in
`claude-plugins/nightgauge/commands/` (one exception: `model-routing-report.md`,
a self-contained utility with no skill counterpart). Do NOT add a
`commands/<name>.md` file for a name that has a skill — a plugin registers a
slash entry for both a `commands/<name>.md` AND a `skills/<name>/SKILL.md`, and
Claude Code does not dedupe them, so a wrapper produces a duplicate `/` entry
(the duplicate-slash-entry regression ADR-007 records).

See [docs/decisions/007-slash-command-skill-invocation-contract.md](docs/decisions/007-slash-command-skill-invocation-contract.md)
for the full rationale and the history of the retired command-wrapper banner.

### Why This Matters

Typing `/nightgauge:<name>` loads `SKILL.md` directly — there is no
separate command `.md` for the agent to mistake for the spec, so the "command
file treated as the workflow" failure mode is retired structurally. `scripts/install-agent-skills.sh` injects
`disable-model-invocation: true` into the generated plugin copy for
side-effecting workflows so the user invokes them explicitly and the model
does not auto-run them — canonical `skills/*/SKILL.md` must never set the flag
directly (`scripts/validate-skill-metadata.sh` rejects it). A read-only /
advisory skill that another skill documents chaining into (e.g. issue-create
Phase 6 invoking issue-audit) sets `metadata.chainable: true` to opt out of
the injection, since a parent skill's `Skill()` call is itself a model-issued
invocation and is blocked by the flag exactly like a spontaneous one.

### Authoring Checklist (new slash command)

When adding a new `/nightgauge:<name>` command:

- [ ] Author it as `skills/nightgauge-<name>/SKILL.md` (or a non-prefixed
      `skills/<name>/SKILL.md`) — never as a `commands/<name>.md` wrapper.
- [ ] Do NOT set `disable-model-invocation` in the canonical SKILL.md — it is
      injected automatically. If the skill is read-only/advisory and another
      skill is meant to chain into it via the `Skill` tool, add
      `metadata.chainable: true` instead.
- [ ] Run `bash scripts/install-agent-skills.sh --generate-only` and commit the
      regenerated `claude-plugins/nightgauge/skills/` tree (the drift guard
      in `.github/workflows/skills-smoke.yml` enforces this).

## Code Standards

### Naming Conventions

| Item              | Convention | Example              |
| ----------------- | ---------- | -------------------- |
| Skill directories | kebab-case | `smart-setup/`       |
| SKILL.md files    | UPPERCASE  | `SKILL.md`           |
| TypeScript files  | camelCase  | `pipelineService.ts` |
| React components  | PascalCase | `DashboardView.tsx`  |

### Versioning

Follow semantic versioning:

- **MAJOR** (X.0.0): Breaking changes
- **MINOR** (x.Y.0): New features, non-breaking
- **PATCH** (x.y.Z): Bug fixes, documentation

**CRITICAL**: Never downgrade versions. Check existing version before updating:

```bash
git show main:skills/your-skill/SKILL.md | grep version
```

### Updating Changelogs

When making changes to a skill, update its `CHANGELOG.md`:

1. Add entry to the `[Unreleased]` section
2. Use the appropriate subsection:
   - **Added**: New features
   - **Changed**: Changes to existing functionality
   - **Deprecated**: Features to be removed in future
   - **Removed**: Features removed in this release
   - **Fixed**: Bug fixes
   - **Security**: Security-related changes
3. When releasing, move unreleased items to a new version section

---

## Git Hooks

Nightgauge uses git pre-commit hooks to ensure generated files stay in sync before you push to GitHub.

### Automatic Setup

Hooks are automatically installed when you run `npm install` (via the `prepare` lifecycle script).

### Manual Setup

If you cloned the repo before hooks were added, or hooks are not running:

```bash
npm run setup-hooks
```

### What Hooks Check

The pre-commit hook validates that three generated files match their source inputs:

| Generated File                                                   | Regeneration Command                                                     |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------ |
| `packages/nightgauge-vscode/src/services/IpcClient.generated.ts` | `make generate-ipc-client`                                               |
| `api/generated/ts/platform-api.ts`                               | `npm run generate:types`                                                 |
| `packages/nightgauge-vscode/package.json` (contributions)        | `npx -w nightgauge-vscode tsx scripts/generate-package-contributions.ts` |

If any file is out of sync, the commit is blocked with an error message showing the exact command to fix it.

### Bypassing Hooks (Emergency Only)

```bash
git commit --no-verify
```

Use only as a last resort. CI will still catch out-of-sync files as a backstop.

---

## Pre-Submission Validation (CRITICAL)

**MANDATORY: See
[docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#pre-submission-validation-critical)
for complete validation requirements.**

The validation MUST happen WHILE developing, not after. Key requirements
include:

- JSON/YAML syntax validation
- Version consistency between plugin.json and SKILL.md
- No version downgrades
- No sensitive data or broken links
- TypeScript compiles without errors
- Tests pass

---

## Quality Checklist

Before submitting:

- [ ] Code compiles without errors (`npm run build`)
- [ ] Tests pass (`npm run test:run`)
- [ ] SKILL.md follows agentskills.io specification (if applicable)
- [ ] Version number is appropriate (not downgraded)
- [ ] **Version consistency**: plugin.json matches SKILL.md version
- [ ] **Changelog updated**: CHANGELOG.md has entry for changes
- [ ] No hardcoded secrets or credentials
- [ ] Documentation is clear and complete
- [ ] All JSON/YAML files are valid syntax

---

## Git Workflow

1. **Create feature branch** from `main`:

   ```bash
   git checkout main
   git pull origin main
   git checkout -b feat/your-change
   ```

2. **Make changes** and test

3. **Commit** with conventional commits:

   ```bash
   git commit -m "feat(vscode): add new dashboard widget"
   git commit -m "fix(sdk): correct context file parsing"
   git commit -m "docs: update contributing guide"
   ```

4. **Push and create PR**:
   ```bash
   git push -u origin feat/your-change
   # Then create PR via GitHub
   ```

---

## Pattern Mining and Reuse

When implementing features, the pipeline runs a **pattern mining** step (Phase
2.5 in feature-planning) that discovers existing codebase conventions before
producing the plan.

### How It Works

1. Feature-planning extracts keywords from the issue title and requirements
2. A pattern mining subagent searches the codebase using Glob and Grep
3. Discovered patterns (naming, structural, interface, idiom) are included in
   `planning-{N}.json` as `pattern_mining_results`
4. Feature-dev reads these patterns to follow existing conventions

### Leveraging Discovered Patterns

When patterns are found, feature-dev should:

- **Follow naming conventions** — If the codebase names services `*Service.ts`
  in `src/services/`, name new services the same way
- **Reuse structural patterns** — Place files in the same directories as similar
  implementations
- **Match interface patterns** — Use the same method signatures, return types,
  and error handling as existing code
- **Review similar issues** — If `similar_issues` lists a past issue with high
  relevance, review its plan for approach ideas

### Example

If pattern mining discovers:

```json
{
  "pattern_type": "naming_convention",
  "pattern": "Services named `*Service.ts` in `src/services/`",
  "evidence": ["src/services/PhotoService.ts", "src/services/FileService.ts"],
  "frequency": 12
}
```

Then when implementing a new billing feature, create
`src/services/BillingService.ts` — not `src/billing/service.ts` or
`src/BillingManager.ts`.

### References

- [docs/PATTERN_MINING.md](docs/PATTERN_MINING.md) — Full methodology
- [skills/nightgauge-pattern-mining/SKILL.md](skills/nightgauge-pattern-mining/SKILL.md) — Skill definition
- [packages/nightgauge-sdk/src/context/schemas/pattern-mining.ts](packages/nightgauge-sdk/src/context/schemas/pattern-mining.ts) — Schema

---

## Platform API Types (vendored, read-only)

Nightgauge's open pipeline talks to the **closed-source hosted platform**
(licensing, billing, team analytics) through a typed client. The OpenAPI
contract that defines that API is **owned by the closed platform repository**,
not this one — there is no `api/openapi.yaml` here.

What lives in this repo is the **generated client**, vendored read-only under
`api/generated/` (`api/generated/go/` and `api/generated/ts/`):

- **Do not edit `api/generated/` by hand** — it is generated output, checked
  against its source by the pre-commit hook and CI.
- **Do not add an OpenAPI spec or a codegen step to this repo.** The spec is
  edited and the types are regenerated in the platform repo, then vendored back
  into `api/generated/` here.
- Vendoring is what keeps `npm install` and `go build` working for **everyone
  with no access to the platform repo and no registry auth** — the open pipeline
  builds standalone.

The generated client keeps the open-source pipeline buildable without access to
an optional service implementation or private package registry. See
[VISION.md](VISION.md) for the public project boundary.

---

## Getting Help

- **Questions / getting started**: Start a
  [GitHub Discussion](https://github.com/nightgauge/nightgauge/discussions) — not
  an issue.
- **Bugs**:
  [open an issue](https://github.com/nightgauge/nightgauge/issues/new/choose)
  with reproduction steps.
- **Feature requests**:
  [open an issue](https://github.com/nightgauge/nightgauge/issues/new/choose)
  describing the use case.
- **Security vulnerabilities**: Follow [SECURITY.md](SECURITY.md) — do not open a
  public issue.
- **Support policy & triage cadence**: See [SUPPORT.md](SUPPORT.md).

See also the [Code of Conduct](CODE_OF_CONDUCT.md).

---

## License

Nightgauge is licensed under the Apache License, Version 2.0. Contributing
requires agreeing to the **Contributor License Agreement (CLA)** in
[`CLA/individual.md`](CLA/individual.md) (or
[`CLA/corporate.md`](CLA/corporate.md) if you contribute on behalf of an
employer).

In plain English: you keep the copyright to your work and grant Edibu, LLC a
broad, irrevocable license to use it and to license it onward under any terms.
This is what keeps the project Apache-2.0 today while preserving the option to
offer the same code under different terms later. The full grant is in the CLA
itself.

You don't sign anything up front. When you open your first pull request, the CLA
Assistant bot comments with a one-line phrase; replying to it records your
agreement against your GitHub account for all future contributions, and the
`cla` check goes green.

---

**Author:** nightgauge **Last Updated:** July 2026
