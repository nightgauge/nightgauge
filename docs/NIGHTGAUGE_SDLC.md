# Nightgauge

> AI-powered Issue-to-PR pipeline with enforced quality gates

This document provides a comprehensive guide for teams adopting Nightgauge.
A new team should be able to set up and use the pipeline from this documentation
alone.

## Why GitHub-Native?

Nightgauge uses GitHub as the single platform for the entire development
lifecycle:

| Traditional Approach                | GitHub-Native (Nightgauge)           |
| ----------------------------------- | ------------------------------------ |
| Issues in Jira, code in GitHub      | Everything in one place              |
| Manual status syncing between tools | Automatic status updates             |
| AI can't access external tools      | AI reads issues, PRs, project boards |
| Context switching slows development | One interface for everything         |
| Expensive per-seat licensing        | Included with GitHub (mostly free)   |

### Benefits

- **Zero context switching** — Issues, code, PRs, and project boards in one
  place
- **AI integration** — Claude Code and Codex CLI can read/write GitHub data via
  `gh` CLI
- **Automatic linking** — PRs close issues, status flows through pipeline
- **Velocity tracking** — GitHub Projects provides sprint burndown and insights
- **No extra tooling** — Just GitHub and Claude Code

---

## Quick Start (5 Minutes)

### Prerequisites

| Tool                 | Purpose                     | Install                                               |
| -------------------- | --------------------------- | ----------------------------------------------------- |
| **GitHub CLI (gh)**  | Issue/PR management         | `brew install gh` then `gh auth login`                |
| **Claude Code**      | AI assistant                | [claude.ai/code](https://claude.ai/code)              |
| **OpenAI Codex CLI** | AI assistant (beta adapter) | [OpenAI Codex docs](https://platform.openai.com/docs) |
| **Git**              | Version control             | Usually pre-installed                                 |

### Step 1: Create `.nightgauge/config.yaml`

Create this file in your repository:

```yaml
# .nightgauge/config.yaml - Minimal configuration
project:
  number: 10 # Your GitHub Project number (from URL)
```

### Step 2A: Install Nightgauge Plugin (Claude Code)

```bash
# Add the marketplace
claude plugin marketplace add https://github.com/nightgauge/nightgauge.git

# Install the Nightgauge plugin
claude plugin install nightgauge@nightgauge-plugins
```

### Step 2B: Codex CLI Setup (Beta)

```bash
# Build Nightgauge SDK CLI output once
npm run -w @nightgauge/sdk build
```

Run stages through the unified stage runner:

```bash
scripts/run-stage.sh codex issue-pickup 42
scripts/run-stage.sh codex feature-planning 42
scripts/run-stage.sh codex feature-dev 42
scripts/run-stage.sh codex feature-validate 42
scripts/run-stage.sh codex pr-create 42
scripts/run-stage.sh codex pr-merge 42
```

### Step 3: Run Your First Pipeline

Choose one CLI path:

Claude Code:

```bash
# Pick up an existing issue (or create one first)
/nightgauge:issue-pickup 42

# Design the implementation
/nightgauge:feature-planning

# Implement the feature
/nightgauge:feature-dev

# Validate the implementation
/nightgauge:feature-validate

# Create the pull request
/nightgauge:pr-create

# Merge after review
/nightgauge:pr-merge
```

Codex CLI (beta):

```bash
scripts/run-stage.sh codex issue-pickup 42
scripts/run-stage.sh codex feature-planning 42
scripts/run-stage.sh codex feature-dev 42
scripts/run-stage.sh codex feature-validate 42
scripts/run-stage.sh codex pr-create 42
scripts/run-stage.sh codex pr-merge 42
```

That's it. The pipeline guides you through each step.

### Codex Beta Limitations

- All six pipeline stages are available via `scripts/run-stage.sh codex <stage> <issue>`
- Queue/project-sync and some plugin-level automations remain Claude-first
- Codex stages require built SDK output:
  `npm run -w @nightgauge/sdk build`

### Claude <-> Codex Adapter Switching

Use the same issue number and feature branch while switching adapters. The
pipeline handoff contract is shared through `.nightgauge/pipeline/*.json`
and `.nightgauge/plans/*.md`.

```bash
# Codex stages
scripts/run-stage.sh codex issue-pickup 42
scripts/run-stage.sh codex feature-planning 42

# Switch to Claude on the same branch
/nightgauge:feature-dev
/nightgauge:feature-validate
/nightgauge:pr-create
/nightgauge:pr-merge
```

Fallback if a Codex stage fails:

```bash
# Rebuild SDK CLI target
npm run -w @nightgauge/sdk build

# Verify stage contract parity
npx -w @nightgauge/sdk vitest run tests/cli/stageParity.test.ts
```

If Codex remains blocked, continue with Claude commands and complete the same
issue on the same branch.

### Current Parity Status (March 2026)

| Capability Group                                         | Status    | Notes                                                                          |
| -------------------------------------------------------- | --------- | ------------------------------------------------------------------------------ |
| Core six stages (`issue-pickup` → `pr-merge`)            | Supported | Claude and Codex adapter paths available; Codex tested through CLI v0.111      |
| Gemini CLI adapter                                       | Supported | `gemini` CLI 0.29+; native token tracking                                      |
| Gemini SDK adapter                                       | Supported | API key auth (`GEMINI_API_KEY` / `GOOGLE_API_KEY`)                             |
| GitHub Copilot adapter                                   | Supported | Premium request tracking; no interactive mode                                  |
| Utility commands (`test-gen`, `issue-create`, `doc-gen`) | Beta      | Helpful accelerators, not required for core delivery                           |
| Queue/project sync/backlog orchestration                 | Deferred  | Claude-first automation; use manual GitHub updates in non-Claude adapter flows |

See [decisions/003-codex-adapter-feature-parity.md](./decisions/003-codex-adapter-feature-parity.md)
for the detailed per-stage mapping and dependency migration notes.

---

## Complete Setup Guide

For complete setup instructions, see these documents:

| Document                                   | Purpose                                |
| ------------------------------------------ | -------------------------------------- |
| [PROJECT_SETUP.md](./PROJECT_SETUP.md)     | GitHub Project views, fields, insights |
| [SPRINT_WORKFLOW.md](./SPRINT_WORKFLOW.md) | Sprint/iteration configuration         |
| [ESTIMATION.md](./ESTIMATION.md)           | Story points, velocity tracking        |

### GitHub Project Setup Summary

1. **Create a GitHub Project** (Kanban board) at `/orgs/your-org/projects`
2. **Add custom fields**: Status, Priority, Size, Start date, Target date
3. **Configure views**: Board (primary), Roadmap, My Items, Sprint
4. **Note the project number** from the URL (e.g., `/projects/10` → `10`)

### Repository Documentation Structure

Nightgauge skills expect this documentation structure:

```
your-repo/
├── .nightgauge/
│   └── config.yaml           # Nightgauge configuration
├── docs/
│   ├── ARCHITECTURE.md       # System patterns
│   ├── CODE_STANDARDS.md     # Coding conventions
│   ├── GIT_WORKFLOW.md       # Git workflow rules
│   └── TESTING.md            # Test patterns (optional)
└── CLAUDE.md                 # Claude Code configuration
```

**Missing docs/?** Run `/smart-setup` to create the documentation structure.

---

## The Pipeline

The Nightgauge pipeline transforms issues into merged pull requests:

```
┌─────────────┐   ┌──────────────────┐   ┌─────────────┐   ┌───────────────────┐   ┌───────────┐   ┌──────────┐
│ issue-pickup│ → │ feature-planning │ → │ feature-dev │ → │ feature-validate  │ → │ pr-create │ → │ pr-merge │
└─────────────┘   └──────────────────┘   └─────────────┘   └───────────────────┘   └───────────┘   └──────────┘
     ↓                    ↓                      ↓                   ↓                    ↓               ↓
 Claim issue        Read docs/            Implement per       Build & test         Pre-flight      Address
 Create branch      Design PLAN.md        approved plan       validation           checks          reviews
 Extract specs      Get approval                                                   Link issue      Merge
```

### Stage Descriptions

#### Stage Invocations by Tool

| Stage              | Claude Code                    | Codex CLI (Beta Adapter)                         | GitHub Copilot                                         | Cursor IDE                                             |
| ------------------ | ------------------------------ | ------------------------------------------------ | ------------------------------------------------------ | ------------------------------------------------------ |
| `issue-pickup`     | `/nightgauge-issue-pickup 42`  | `scripts/run-stage.sh codex issue-pickup 42`     | `Use skill nightgauge-issue-pickup for issue #42.`     | `Use skill nightgauge-issue-pickup for issue #42.`     |
| `feature-planning` | `/nightgauge-feature-planning` | `scripts/run-stage.sh codex feature-planning 42` | `Use skill nightgauge-feature-planning for issue #42.` | `Use skill nightgauge-feature-planning for issue #42.` |
| `feature-dev`      | `/nightgauge-feature-dev`      | `scripts/run-stage.sh codex feature-dev 42`      | `Use skill nightgauge-feature-dev for issue #42.`      | `Use skill nightgauge-feature-dev for issue #42.`      |
| `feature-validate` | `/nightgauge-feature-validate` | `scripts/run-stage.sh codex feature-validate 42` | `Use skill nightgauge-feature-validate for issue #42.` | `Use skill nightgauge-feature-validate for issue #42.` |
| `pr-create`        | `/nightgauge-pr-create`        | `scripts/run-stage.sh codex pr-create 42`        | `Use skill nightgauge-pr-create for issue #42.`        | `Use skill nightgauge-pr-create for issue #42.`        |
| `pr-merge`         | `/nightgauge-pr-merge`         | `scripts/run-stage.sh codex pr-merge 42`         | `Use skill nightgauge-pr-merge for issue #42.`         | `Use skill nightgauge-pr-merge for issue #42.`         |

Cross-reference:

- Adapter capability matrix: [ADAPTER_MATRIX.md](./ADAPTER_MATRIX.md)
- Codex parity status:
  [decisions/003-codex-adapter-feature-parity.md](./decisions/003-codex-adapter-feature-parity.md)
- Skills catalog: [../skills/README.md](../skills/README.md)
- Codex runtime details:
  [../configs/codex/README.md](../configs/codex/README.md)

| Stage                | Command                        | What It Does                                        |
| -------------------- | ------------------------------ | --------------------------------------------------- |
| **Issue Pickup**     | `/nightgauge:issue-pickup`     | Claims issue, creates branch, extracts requirements |
| **Feature Planning** | `/nightgauge:feature-planning` | Reads docs/, creates PLAN.md, gets approval         |
| **Feature Dev**      | `/nightgauge:feature-dev`      | Implements code following plan and standards        |
| **Feature Validate** | `/nightgauge:feature-validate` | (Optional) Runs integration/E2E tests               |
| **PR Create**        | `/nightgauge:pr-create`        | Creates PR with pre-flight validation               |
| **PR Merge**         | `/nightgauge:pr-merge`         | Waits for CI, addresses feedback, merges            |

### Quality Gates

Every stage enforces quality:

| Gate                        | Stage            | Enforcement                             |
| --------------------------- | ---------------- | --------------------------------------- |
| **Requirements captured**   | issue-pickup     | Extracts acceptance criteria from issue |
| **Docs read first**         | feature-planning | Reads docs/ before exploring code       |
| **Plan approved**           | feature-planning | User must approve PLAN.md               |
| **Implementation reviewed** | feature-dev      | Quality review against standards        |
| **Tests pass**              | pr-create        | Pre-flight validation                   |
| **No plan drift**           | pr-create        | Compares implementation to plan         |

For detailed stage documentation, see
[ISSUE_TO_PR_WORKFLOW.md](./ISSUE_TO_PR_WORKFLOW.md).

---

## Configuration Reference

Nightgauge uses a 6-tier configuration system. The primary project config
is `.nightgauge/config.yaml`. All options can be overridden via environment
variables or higher tiers. See [CONFIGURATION.md](./CONFIGURATION.md) for
complete reference.

### Quick Reference

| Setting                  | Default  | Description                                             |
| ------------------------ | -------- | ------------------------------------------------------- |
| `project.number`         | —        | GitHub Project number (required)                        |
| `project.auto_dates`     | `false`  | Auto-populate Start/Target dates                        |
| `pr.merge_strategy`      | `squash` | Merge strategy for sub-issue PRs: squash, merge, rebase |
| `pr.epic_merge_strategy` | `merge`  | Merge strategy for epic→main PRs: merge, squash, rebase |
| `pr.delete_branch`       | `true`   | Delete branch after merge                               |
| `branch.base`            | `main`   | Default base branch                                     |
| `pipeline.auto_fix`      | `true`   | Auto-fix linting issues                                 |

### Example Configuration

```yaml
# .nightgauge/config.yaml
project:
  number: 10
  owner: nightgauge
  auto_dates: true
  sprint:
    enabled: true
    auto_assign: true

pr:
  merge_strategy: squash
  epic_merge_strategy: merge
  delete_branch: true
  reviewers:
    - lead-dev

branch:
  base: main
  prefixes:
    feature: feat/
    bugfix: fix/

pipeline:
  ci_timeout: 300
  auto_fix: true
```

**Full reference**: [CONFIGURATION.md](./CONFIGURATION.md)

---

## Skill Reference

### Core Pipeline Skills

| Skill                          | Purpose                                          |
| ------------------------------ | ------------------------------------------------ |
| `/nightgauge:issue-pickup`     | Claim issue, create branch, extract requirements |
| `/nightgauge:feature-planning` | Read docs/, design approach, create PLAN.md      |
| `/nightgauge:feature-dev`      | Implement following plan and standards           |
| `/nightgauge:feature-validate` | Run integration/E2E tests, manual checklists     |
| `/nightgauge:pr-create`        | Create PR with linking and pre-flight checks     |
| `/nightgauge:pr-merge`         | Wait for CI, address feedback, merge             |

### Utility Skills

| Skill                      | Purpose                                         |
| -------------------------- | ----------------------------------------------- |
| `/nightgauge:issue-create` | Create well-structured issues with metadata     |
| `/nightgauge:doc-gen`      | Generate docs for public APIs                   |
| `/nightgauge:test-gen`     | Generate comprehensive test suites              |
| `/smart-setup`             | Make any repo AI-ready with AGENTS.md and docs/ |
| `/update-docs`             | Verify and update documentation                 |

**Full reference**: [skills/README.md](../skills/README.md)

---

## Context-Isolated Architecture

Nightgauge uses **context isolation** to prevent token exhaustion:

```
Traditional:  Step 1 (5K) → Step 2 (15K) → Step 3 (35K) → Step 4 (70K) → 💥

Nightgauge:      Step 1 (5K) → JSON → Step 2 (5K) → JSON → Step 3 (5K) → ✅
```

Each pipeline stage:

1. **Starts fresh** — Only loads skill instructions and context file
2. **Reads minimal context** — Structured JSON from previous stage
3. **Writes handoff** — Outputs context file for next stage
4. **Cleans up** — After merge, all context files are removed

### Context Files

| File                | Created By       | Contains                             |
| ------------------- | ---------------- | ------------------------------------ |
| `issue-{N}.json`    | issue-pickup     | Issue metadata, requirements, branch |
| `planning-{N}.json` | feature-planning | Plan location, patterns, files       |
| `dev-{N}.json`      | feature-dev      | Implementation summary, test results |
| `pr-{N}.json`       | pr-create        | PR number, URL, review requirements  |

**Full reference**: [CONTEXT_ARCHITECTURE.md](./CONTEXT_ARCHITECTURE.md)

---

## Troubleshooting

### Common Issues

| Problem                    | Solution                                            |
| -------------------------- | --------------------------------------------------- |
| `gh` not authenticated     | Run `gh auth login`                                 |
| No docs/ folder            | Run `/smart-setup` first                            |
| Tests failing              | Fix tests before `/nightgauge:pr-create`            |
| Missing context file       | Run pipeline stages in order                        |
| Branch already exists      | Delete: `git branch -D <branch>`                    |
| Project board not updating | Check `project.number` in `.nightgauge/config.yaml` |

Codex beta note: if wrapper scripts fail, confirm SDK build output exists:
`npm run -w @nightgauge/sdk build`

### Debugging

```bash
# Enable debug logging
NIGHTGAUGE_HOOKS_DEBUG=1 /nightgauge:issue-pickup 42

# Check GitHub CLI auth
gh auth status

# Verify project access
gh project list --owner nightgauge
```

**Full reference**: [TROUBLESHOOTING.md](./TROUBLESHOOTING.md)

---

## Adoption Checklist

Use this checklist when setting up Nightgauge for a new team:

### Prerequisites

- [ ] GitHub repository with write access
- [ ] GitHub CLI installed and authenticated (`gh auth login`)
- [ ] Claude Code installed
- [ ] Nightgauge plugin installed (for Claude path)
- [ ] Codex CLI installed (for Codex path)

### GitHub Project Setup

- [ ] Create GitHub Project (Kanban board)
- [ ] Add Status field: Backlog, Ready, In progress, In review, Done
- [ ] Add Priority field: P0, P1, P2
- [ ] Add Size field: XS, S, M, L, XL
- [ ] Add date fields: Start date, Target date
- [ ] (Optional) Add Sprint iteration field
- [ ] Note project number from URL

### Repository Setup

- [ ] Create `.nightgauge/config.yaml` with project number
- [ ] Run `/smart-setup` if docs/ missing
- [ ] Verify CLAUDE.md references docs/ files
- [ ] Create issue templates (optional)

### First Pipeline Run

- [ ] Create or pick up a test issue
- [ ] Run through full pipeline: pickup → planning → dev → pr-create
- [ ] Verify project board status updates automatically
- [ ] Verify PR links to issue correctly

---

## Related Documentation

| Document                                             | Purpose                          |
| ---------------------------------------------------- | -------------------------------- |
| [ARCHITECTURE.md](./ARCHITECTURE.md)                 | System architecture and patterns |
| [CONFIGURATION.md](./CONFIGURATION.md)               | Full config reference            |
| [ISSUE_TO_PR_WORKFLOW.md](./ISSUE_TO_PR_WORKFLOW.md) | Detailed workflow documentation  |
| [PROJECT_SETUP.md](./PROJECT_SETUP.md)               | GitHub Project configuration     |
| [SPRINT_WORKFLOW.md](./SPRINT_WORKFLOW.md)           | Sprint/iteration support         |
| [TROUBLESHOOTING.md](./TROUBLESHOOTING.md)           | Common issues and solutions      |
| [skills/README.md](../skills/README.md)              | Complete skill reference         |

---

## Author

nightgauge
