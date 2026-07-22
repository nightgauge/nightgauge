# Nightgauge Universal Agent Skills

This directory contains **universal AI Agent Skills** that work across multiple AI coding tools following the [Agent Skills specification](https://agentskills.io/specification).

## Forge Abstraction Contract (#3363, ADR-008)

> **Skills target the `nightgauge forge` abstraction — direct `gh` calls are forbidden.**

Every forge operation in `skills/*/SKILL.md` MUST go through the `nightgauge forge` Cobra command surface (`issue`, `pr`, `project`, `label`, `repo`, `auth`, `graphql`). This keeps the skill layer forge-agnostic so `IB_FORGE=gitlab` works without changing any skill file. See [docs/FORGE_ABSTRACTION.md](../docs/FORGE_ABSTRACTION.md) for the canonical design (interface layout, adapter contract, lifecycle, CE-vs-EE matrix) and [docs/decisions/008-skill-forge-cli.md](../docs/decisions/008-skill-forge-cli.md) for the migration table and the carve-out rationale (project view-create / link / list route through `forge graphql`).

The `nightgauge preflight skill-no-direct-gh` gate (mirrored as `scripts/lint-skills/no-direct-gh.sh`) is wired into `.github/workflows/lint.yml` and fails CI when any non-allowlisted `skills/*/SKILL.md` regresses to a direct `gh ` call. Skills tracked for follow-up migration are listed in [scripts/lint-skills/allowlist.txt](../scripts/lint-skills/allowlist.txt) — adding to that list requires PR review justification.

**Wave 4 migrated skills (15)**: `repo-init`, `retro`, `project-sync`, `issue-pickup`, `release-watch`, `issue-refine`, `pipeline-audit`, `pipeline-health`, `issue-audit`, `pr-merge`, `dep-modernize`, `modernize-plan`, `smart-setup`, `queue`, `pr-create`.

## What are Agent Skills?

Agent Skills are portable, reusable capabilities that can be invoked by AI coding assistants. A skill written once (as a `SKILL.md` file) works across all compatible tools without modification.

### Supported AI Tools

| Tool               | Invocation                 | Skill Location                            |
| ------------------ | -------------------------- | ----------------------------------------- |
| **Claude Code**    | `/skill-name` (via plugin) | Via claude-plugins wrapper                |
| **OpenAI Codex**   | `$skill-name`              | `~/.codex/skills/` or `.codex/skills/`    |
| **GitHub Copilot** | Agent Skills UI            | `.github/skills/` or `~/.copilot/skills/` |
| **Cursor IDE**     | Agent Skills               | `.cursor/skills/`                         |
| **Gemini CLI**     | Agent Skills               | `.gemini/skills/`                         |
| **VS Code**        | Copilot Agent Skills       | `.github/skills/`                         |

---

## Quick Start

To use any skill, copy it to your tool's skills directory or invoke via the tool's native mechanisms:

**Claude Code:**

```bash
/nightgauge-issue-pickup 42    # Use skill with arguments
```

**GitHub Copilot / Cursor:**

```
Read the SKILL.md and use the skill name as the Agent Skill invocation
```

**OpenAI Codex:**

```bash
$nightgauge-issue-pickup 42
```

---

## Skill Catalog

### Core Pipeline (6-Stage Issue-to-PR Workflow)

The Nightgauge pipeline automates issue analysis, planning, implementation, validation, PR creation, and merge in sequence.

| Skill                                                               | Version | Description                                                                                                                               |
| ------------------------------------------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| [nightgauge-issue-pickup](nightgauge-issue-pickup/SKILL.md)         | 1.19.0  | **Stage 1: Issue Pickup** — Claim a GitHub issue, extract requirements, and set up development environment. Entry point for the pipeline. |
| [nightgauge-feature-planning](nightgauge-feature-planning/SKILL.md) | 1.16.0  | **Stage 2: Feature Planning** — Read docs before code, propose implementation approach, write plan file for approval.                     |
| [nightgauge-feature-dev](nightgauge-feature-dev/SKILL.md)           | 1.10.0  | **Stage 3: Feature Dev** — Implement features following approved PLAN.md and documented standards. Includes quality review.               |
| [nightgauge-feature-validate](nightgauge-feature-validate/SKILL.md) | 1.14.0  | **Stage 4: Feature Validate** — Validate feature with integration/E2E tests and manual checklists before PR creation.                     |
| [nightgauge-pr-create](nightgauge-pr-create/SKILL.md)               | 1.19.0  | **Stage 5: PR Create** — Create pull request with correct base/head, issue linkage, validation summary, and reviewer assignment.          |
| [nightgauge-pr-merge](nightgauge-pr-merge/SKILL.md)                 | 1.13.0  | **Stage 6: PR Merge** — Wait for reviews, address feedback, and merge. Completes the issue-to-PR pipeline.                                |

---

### Epic Management

Tools for analyzing, assessing, validating, and managing epics and their sub-issues.

| Skill                                                         | Version | Description                                                                                                                                            |
| ------------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| [nightgauge-assess-epic](nightgauge-assess-epic/SKILL.md)     | 1.0.0   | Analyze epic sub-issues and recommend batch vs sequential pipeline processing strategy. Evaluates file overlap, size variance, and dependency signals. |
| [nightgauge-epic-validate](nightgauge-epic-validate/SKILL.md) | 1.0.0   | Post-creation validation for epics. Verifies sub-issue linking, project board assignment, blockedBy relationships, and cross-repo dependencies.        |

---

### Backlog & Queue Management

Tools for backlog triage, prioritization, and issue queue management.

| Skill                                                                 | Version | Description                                                                                                                                               |
| --------------------------------------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [nightgauge-backlog-groom](nightgauge-backlog-groom/SKILL.md)         | 1.1.0   | Perform periodic backlog triage—identify stale issues, detect duplicates, validate priorities, and discover dependencies. Use weekly/monthly for hygiene. |
| [nightgauge-backlog-preflight](nightgauge-backlog-preflight/SKILL.md) | 1.1.0   | Validate backlog issues are pipeline-ready before processing. Checks required labels, acceptance criteria quality, and greenfield readiness.              |
| [nightgauge-queue](nightgauge-queue/SKILL.md)                         | 1.0.0   | Manage the issue queue for sequential and batch pipeline processing. Add, list, remove, clear, and reorder queued issues. Supports epic expansion.        |

---

### Repository Setup & Operations

Tools for initializing repositories and syncing project board state. For the
full setup decision path (single repo vs multi-repo workspace vs AI-ready
docs), see [Tree 5 in docs/DECISION_TREES.md](../docs/DECISION_TREES.md#tree-5-i-want-to-set-up-a-new-repository-or-workspace)
and the [Project Setup & Operations section of docs/SKILLS_USAGE_GUIDE.md](../docs/SKILLS_USAGE_GUIDE.md#project-setup--operations).

| Skill                                                           | Version | Description                                                                                                                                                                                                                                  |
| --------------------------------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [nightgauge-repo-init](nightgauge-repo-init/SKILL.md)           | 1.3.0   | Prime a new GitHub repository for the Nightgauge SDLC pipeline. Creates labels, validates project board fields, and generates .nightgauge/config.yaml.                                                                                       |
| [nightgauge-workspace-init](nightgauge-workspace-init/SKILL.md) | 1.0.0   | Scaffold a multi-repo workspace manifest (.vscode/nightgauge-workspace.yaml) for the N:1 shared-project topology. Detects member repos, derives the shared project, generates and verifies the manifest. Run after repo-init in each member. |
| [nightgauge-project-sync](nightgauge-project-sync/SKILL.md)     | 1.1.0   | Bulk-sync existing repository issues to GitHub Project boards with proper field mappings. Ideal for onboarding or catch-up syncs.                                                                                                            |

---

### Quality Assessment & Auditing

Comprehensive analysis and scoring tools for code quality, security, testing, and product health.

| Skill                                                                   | Version | Description                                                                                                                                                                                                |
| ----------------------------------------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [nightgauge-health-check](nightgauge-health-check/SKILL.md)             | 1.1.0   | Comprehensive codebase health assessment producing quantitative scores across 6 dimensions. Use when auditing or inheriting a codebase.                                                                    |
| [nightgauge-security-audit](nightgauge-security-audit/SKILL.md)         | 1.1.0   | Comprehensive security assessment across 7 dimensions: vulnerabilities, hardcoded secrets, OWASP Top 10, weak crypto, input validation, auth, and misconfiguration.                                        |
| [nightgauge-test-scaffold](nightgauge-test-scaffold/SKILL.md)           | 1.2.0   | Analyze test coverage, identify critical untested paths, and generate characterization tests as safety net before refactoring.                                                                             |
| [nightgauge-test-gen](nightgauge-test-gen/SKILL.md)                     | 1.0.0   | Generate comprehensive test suites with coverage analysis using parallel subagents. Supports Jest, Pytest, dotnet test, and Gradle.                                                                        |
| [nightgauge-product-audit](nightgauge-product-audit/SKILL.md)           | 1.0.0   | Comprehensive 8-dimension product quality audit across all Nightgauge repositories. Validates API alignment, epic lifecycle, docs, feature parity, test coverage, security, dependencies, and CI/CD.       |
| [nightgauge-verify-ui](nightgauge-verify-ui/SKILL.md)                   | 1.0.0   | Drive a running UI through a critical flow with the Playwright MCP, asserting state at each step and capturing screenshots/traces. Use after a UI-affecting change or from feature-validate.               |
| [nightgauge-adversarial-review](nightgauge-adversarial-review/SKILL.md) | 1.0.0   | Fresh-eyes critics attack the current diff from distinct lenses (correctness, security, reuse, tests), then drive a fix loop until findings degrade to nitpicks. Runs on the workflow-orchestration spine. |

---

### Pipeline Monitoring & Optimization

Tools for analyzing pipeline efficiency, health, and continuous improvement.

| Skill                                                                           | Version | Description                                                                                                                                                                     |
| ------------------------------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [nightgauge-pipeline-audit](nightgauge-pipeline-audit/SKILL.md)                 | 1.3.0   | Analyze pipeline execution history for efficiency insights: token usage, stage performance, cost optimization, quality correlation, and trends.                                 |
| [nightgauge-pipeline-health](nightgauge-pipeline-health/SKILL.md)               | 1.1.0   | Comprehensive pipeline health analysis across 7 dimensions: token economics, cost, stage effectiveness, model routing, reliability, self-improvement loop health, and velocity. |
| [nightgauge-retro](nightgauge-retro/SKILL.md)                                   | 1.3.0   | Analyze pipeline failures to identify root causes, recurring patterns, and remediation steps. Classifies failures across 7 categories and records lessons learned.              |
| [nightgauge-continuous-improvement](nightgauge-continuous-improvement/SKILL.md) | 1.0.0   | Unified continuous improvement review—orchestrates all self-improvement mechanisms into a periodic review cycle. Dogfood (internal) and customer (external) modes.              |

---

### Modernization & Refactoring

Tools for strategic code modernization, dependency updates, and refactoring decisions.

| Skill                                                               | Version | Description                                                                                                                                                                      |
| ------------------------------------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [nightgauge-modernize-plan](nightgauge-modernize-plan/SKILL.md)     | 1.1.0   | Modernization plan generator that consumes health-check, security-audit, and test-scaffold outputs to produce a prioritized, phased roadmap.                                     |
| [nightgauge-dep-modernize](nightgauge-dep-modernize/SKILL.md)       | 1.1.0   | Dependency Modernization Engine—safely identifies and updates outdated/vulnerable dependencies. Includes compatibility analysis and breaking change detection.                   |
| [nightgauge-refactor-rewrite](nightgauge-refactor-rewrite/SKILL.md) | 1.1.0   | Refactor vs rewrite decision analysis engine. Evaluates brownfield codebases across 8 dimensions with data-driven recommendations, confidence levels, and risk/benefit matrices. |

---

### Documentation & Monitoring

Tools for generating, maintaining, validating documentation accuracy, and monitoring external systems.

| Skill                                                         | Version | Description                                                                                                                                                                                                                                                                                                                                                                    |
| ------------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| [nightgauge-doc-gen](nightgauge-doc-gen/SKILL.md)             | 1.1.0   | Auto-generate and update API documentation. Detects undocumented functions, generates JSDoc/docstrings, identifies signature changes, and suggests README updates.                                                                                                                                                                                                             |
| [nightgauge-docs-write](nightgauge-docs-write/SKILL.md)       | 1.1.0   | Write narrative architecture documentation sections by reading source files and synthesizing accurate, validated content. Use for documentation-focused issues.                                                                                                                                                                                                                |
| [nightgauge-docs-watch](nightgauge-docs-watch/SKILL.md)       | 1.0.0   | Monitor Claude Code documentation for new features and changes. Detects new pages, changes, and removals; correlates with release versions to provide richer context. Auto-creates issues for high-relevance changes.                                                                                                                                                          |
| [nightgauge-release-watch](nightgauge-release-watch/SKILL.md) | 1.0.0   | Monitor Claude Code GitHub releases for new features and changes. Classifies changes by type (feature, fix, breaking, deprecation) and scores pipeline relevance using the Feature Assessment Engine. Auto-creates issues for high-priority releases.                                                                                                                          |
| [nightgauge-release-notes](nightgauge-release-notes/SKILL.md) | 1.0.0   | Draft user-facing fastlane release notes (iOS/macOS + Android) from a closed epic's sub-issues, sized to pass the store-deploy freshness gate (Android ≤500 chars). Use after an epic closes to produce a human-reviewed "what's new" draft before deploy.                                                                                                                     |
| [nightgauge-version-bump](nightgauge-version-bump/SKILL.md)   | 1.0.0   | Derive the next semantic version (feat→minor, fix→patch, breaking→major; highest across the release) and a Keep-a-Changelog entry from a closed epic's sub-issues, then write `pubspec.yaml` (preserving the store-anchored `+build`) and `CHANGELOG.md`. Use after an epic closes, before release-notes and deploy; idempotent, with a `--bump` override; never auto-submits. |

---

### Integration & Configuration

Tools for validating cross-repo integration health and displaying effective configuration.

| Skill                                                                 | Version | Description                                                                                                                                                                                               |
| --------------------------------------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [nightgauge-integration-audit](nightgauge-integration-audit/SKILL.md) | 1.0.0   | Cross-repository integration audit. Validates client API calls match platform endpoints, auth flows are aligned, docs are current, and cross-repo dependencies are tracked.                               |
| [nightgauge-config-show](nightgauge-config-show/SKILL.md)             | 1.1.0   | Display effective Nightgauge configuration with source annotations. Shows where each value comes from (default, global, project, environment).                                                            |
| [nightgauge-cli-reference](nightgauge-cli-reference/SKILL.md)         | 1.0.0   | Reference for Nightgauge' own surfaces — the `forge` abstraction, the Go binary subcommands, and the SDK API — with gotchas. Use when authoring skills/automations or reaching for a bare `gh`/`glab`.    |
| [nightgauge-careful](nightgauge-careful/SKILL.md)                     | 1.0.0   | Opt-in guardrail that blocks production-destructive commands (docker compose down -v, docker volume rm, kubectl delete, SQL DROP/TRUNCATE) for the session. Use before touching prod; turn off when done. |

---

### Issue & Knowledge Creation

Tools for creating well-structured issues and managing knowledge across teams.

| Skill                                                       | Version | Description                                                                                                                                                                                                                                                  |
| ----------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| [nightgauge-issue-create](nightgauge-issue-create/SKILL.md) | 1.15.0  | Create well-structured GitHub issues with SDLC metadata, project board sync, and optional parent/child linking. Immediately usable by the pipeline.                                                                                                          |
| [nightgauge-issue-refine](nightgauge-issue-refine/SKILL.md) | 1.0.0   | Analyze a raw GitHub issue and rewrite it with structured sections, acceptance criteria, and codebase-grounded guidance — making it immediately pipeline-ready. Used by the autonomous refinement scan to auto-process issues with the `auto-process` label. |

---

### Portable (Tool-Agnostic) Skills

Universal skills that work across any repository and AI tool.

| Skill                                 | Version | Description                                                                                                                              |
| ------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| [smart-setup](smart-setup/SKILL.md)   | 4.7.1   | Make any repository AI-ready with AGENTS.md, CLAUDE.md, and focused documentation. One-time setup for AI readiness.                      |
| [update-docs](update-docs/SKILL.md)   | 1.7.0   | Verify and update documentation to match current codebase. Detects drift, deprecated references, and inconsistencies. Works on any repo. |
| [pr-preflight](pr-preflight/SKILL.md) | 1.1.0   | Universal pre-flight validation for pull requests. Catches common issues like broken links, invalid syntax, and missing documentation.   |

---

## Pipeline Flow

The six-stage pipeline processes issues sequentially through these core stages:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Issue-to-PR Pipeline                         │
├─────────────────────────────────────────────────────────────────┤
│  1. issue-pickup           → Claim issue, extract requirements   │
│  2. feature-planning       → Plan implementation approach        │
│  3. feature-dev            → Implement feature                   │
│  4. feature-validate       → Test end-to-end                    │
│  5. pr-create              → Create pull request                 │
│  6. pr-merge               → Wait, review, and merge             │
└─────────────────────────────────────────────────────────────────┘
```

Each stage:

- **Reads** the previous stage's context file
- **Operates** according to its skill documentation
- **Writes** its output context for the next stage
- **Fails gracefully** with clear error messages

### Operating the Pipeline

**Queue-based batch execution** (multiple issues):

- Use `nightgauge-queue` to add/manage issues
- Pipeline processes sequentially through all 6 stages per issue
- Ideal for overnight runs or backlog processing

**Single-issue interactive execution**:

- Use `/nightgauge-issue-pickup N` in Claude Code
- Run stages individually or in sequence
- Ideal for high-touch development or complex features

**Autonomous cross-repo execution** (all repos, all issues):

- Use `nightgauge autonomous run` to start the autonomous scheduler
- Builds a cross-repo dependency graph, fills pipeline slots with the
  highest-priority unblocked items, and cascades unblocks across repos
- Safety rails (budget ceiling, circuit breaker, rate limit, health gate, epic
  checkpoint) prevent runaway execution
- Use `nightgauge autonomous run --dry-run` to preview without executing
- See [docs/AUTONOMOUS_ORCHESTRATOR.md](../docs/AUTONOMOUS_ORCHESTRATOR.md)
  for full documentation

---

## Skill Lifecycle

### Discovery

Skills are discovered by:

- AI tools scanning the `skills/` directory
- Claude Code loading plugins from `claude-plugins/nightgauge/`
- Manual invocation via `SKILL.md` file paths

### Execution

When invoked, a skill:

1. **Reads** its SKILL.md frontmatter for allowed-tools and configuration
2. **Executes** the skill's instruction phases in sequence
3. **Produces output** to stdout, files, or context JSON
4. **Reports success/failure** to the calling AI tool

### Context Handoff

Pipeline skills communicate via **JSON context files** stored in `.nightgauge/pipeline/`:

```
.nightgauge/pipeline/
├── issue-42.json                  # Initial issue context (issue-pickup output)
├── issue-42-plan.json             # Implementation plan (feature-planning output)
├── issue-42-dev.json              # Implementation state (feature-dev output)
├── issue-42-validate.json         # Validation results (feature-validate output)
├── issue-42-pr.json               # PR metadata (pr-create output)
└── issue-42-merge.json            # Merge status (pr-merge output)
```

Each stage:

- **Reads** the previous stage's file (required input)
- **Validates** the input exists; fails with helpful error if missing
- **Writes** its output to a new numbered file

See [docs/CONTEXT_ARCHITECTURE.md](../docs/CONTEXT_ARCHITECTURE.md) for complete schemas and examples.

---

## Shared Utilities

The `skills/_shared/` directory contains reusable components:

- **PIPELINE_CONTEXT.md** — Common markdown for pipeline stage documentation
- **SELF_ASSESSMENT_EPILOGUE.md** — Epilogue phase for pipeline skill self-assessment
- **GOTCHAS.md** — Cross-cutting gotchas appended to a skill's `## Gotchas` section
- **RUN_REFLECTION.md** — Across-run memory/delta reporting for cadence skills

These are included in pipeline skills via markdown includes (`<!-- include: ... -->`).

---

## Creating Skills

To create a new skill:

1. **Create the directory**: `mkdir -p skills/your-skill-name/`

2. **Write SKILL.md** with required frontmatter:

   ```yaml
   ---
   name: your-skill-name
   description: Brief description (1-2 sentences)
   license: Apache-2.0
   metadata:
     author: Author Name
     version: "1.0.0"
     source: https://github.com/nightgauge/nightgauge
   allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
   ---
   ```

3. **Write the skill content** following the [Agent Skills specification](https://agentskills.io/specification)

4. **For pipeline skills**, include the self-assessment epilogue in the final phase:

   ```markdown
   ### Phase {N}: Self-Assessment Epilogue

   <!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->
   ```

5. **Update `skills/README.md`** to catalog the new skill

6. **Test** with your AI tool before submitting

See [CONTRIBUTING.md](../CONTRIBUTING.md#contributing-skills) for complete skill creation guidelines.

### Gotchas Sections

The highest-signal content in a skill is its `## Gotchas` section — the footguns
Claude hits when using it. Add one (near the top, after `## Arguments`, before
`## Workflow`) once a skill accumulates incident-cited warnings; format each
entry **symptom → why → do-instead** with the `#NNNN` incident, and append
`<!-- include: ../_shared/GOTCHAS.md -->` for the cross-cutting set. Keep it
concise — long detail stays in `_includes/`. See
[.claude/rules/skills.md](../.claude/rules/skills.md#gotchas-sections).

### Run Reflection (cadence skills)

Skills that run on a cadence should remember prior runs and report **deltas, not
full re-dumps**. Include `<!-- include: ../_shared/RUN_REFLECTION.md -->` and set
`RUN_LOG` to an in-repo append-only path before it.
[`nightgauge-release-watch`](nightgauge-release-watch/SKILL.md)
(`last-seen.json`) is the reference pattern.

### Phase Markers and `PHASE_REGISTRY`

Pipeline-stage skills emit `<!-- phase:start ... -->` markers that the
orchestrator parses to drive dashboard progress. The canonical inventory lives
in `packages/nightgauge-sdk/src/events/phaseRegistry.ts`. When adding,
renaming, or removing a phase: **update the registry first**, then update every
matching `phase:start` marker in the skill body, then run
`npx tsx scripts/validate-phase-markers.ts` to confirm. Standalone skills (those
not in the 6-stage pipeline) opt out of registry validation by including
`<!-- phase-registry: standalone-skill -->` near the top of the skill body.
Full guidance lives in
[CONTRIBUTING.md → Phase Markers](../CONTRIBUTING.md#3a-phase-markers-registry-first-authoring).

---

## Skill Specification

All skills follow the [Agent Skills specification](https://agentskills.io/specification), which includes:

- **SKILL.md format** with required frontmatter (name, description, version, allowed-tools)
- **Execution phases** that structure the skill's workflow
- **Tool invocations** using the allowed-tools list
- **Output** for stdout, files, or context

Skills are **tool-agnostic** — they work in Claude Code, Copilot, Cursor, Codex, Gemini, and any other tool that implements the Agent Skills specification.

---

## License

All skills in this directory are subject to:

Skills may be used and modified by authorized users of the Nightgauge project.

---

## Author

nightgauge

**Last Updated:** March 2026
