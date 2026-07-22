# Skills Usage Guide

Complete reference for all Nightgauge skills — what they do, when to use them, and how they fit together.

## Quick Reference

| Skill                               | Category         | Purpose                                       | Version | Invocation                                     |
| ----------------------------------- | ---------------- | --------------------------------------------- | ------- | ---------------------------------------------- |
| `nightgauge-issue-pickup`           | Core Pipeline    | Claim an issue and set up development         | 1.19.0  | `/nightgauge-issue-pickup [#]`                 |
| `nightgauge-feature-planning`       | Core Pipeline    | Plan implementation with docs-first approach  | 1.16.0  | `/nightgauge-feature-planning`                 |
| `nightgauge-feature-dev`            | Core Pipeline    | Implement features following approved plan    | 1.10.0  | `/nightgauge-feature-dev`                      |
| `nightgauge-feature-validate`       | Core Pipeline    | Validate with integration/E2E tests           | 1.14.0  | `/nightgauge-feature-validate`                 |
| `nightgauge-pr-create`              | Core Pipeline    | Create PR with validation summary             | 1.19.0  | `/nightgauge-pr-create`                        |
| `nightgauge-pr-merge`               | Core Pipeline    | Wait for reviews and merge PR                 | 1.13.0  | `/nightgauge-pr-merge`                         |
| `nightgauge-assess-epic`            | Project Ops      | Analyze epic for batch vs sequential strategy | 1.0.0   | `/nightgauge:assess-epic <#>`                  |
| `nightgauge-backlog-groom`          | Project Ops      | Perform backlog triage and hygiene            | 1.1.0   | `/nightgauge:backlog-groom [options]`          |
| `nightgauge-backlog-preflight`      | Project Ops      | Validate backlog before processing            | 1.1.0   | `/nightgauge:backlog-preflight [options]`      |
| `nightgauge-config-show`            | Project Ops      | Display effective configuration               | 1.1.0   | `/nightgauge-config-show`                      |
| `nightgauge-epic-validate`          | Project Ops      | Post-creation epic validation                 | 1.0.0   | `/nightgauge:epic-validate <#>`                |
| `nightgauge-issue-create`           | Project Ops      | Create well-structured GitHub issues          | 1.15.0  | `/nightgauge:issue-create [options]`           |
| `nightgauge-project-sync`           | Project Ops      | Bulk-sync issues to project board             | 1.1.0   | `/nightgauge:project-sync [options]`           |
| `nightgauge-queue`                  | Project Ops      | Manage issue queue for pipeline processing    | 1.0.0   | `/nightgauge:queue [args]`                     |
| `nightgauge-health-check`           | Quality & Audit  | Codebase health assessment (6 dimensions)     | 1.1.0   | `/nightgauge:health-check [options]`           |
| `nightgauge-security-audit`         | Quality & Audit  | Security posture assessment (7 dimensions)    | 1.1.0   | `/nightgauge:security-audit [options]`         |
| `nightgauge-product-audit`          | Quality & Audit  | 8-dimension cross-repo quality audit          | 1.0.0   | `/nightgauge:product-audit [options]`          |
| `nightgauge-integration-audit`      | Quality & Audit  | Cross-repository integration health           | 1.0.0   | `/nightgauge:integration-audit [options]`      |
| `nightgauge-pipeline-audit`         | Quality & Audit  | Pipeline efficiency snapshot (quick check)    | 1.3.0   | `/nightgauge:pipeline-audit [options]`         |
| `nightgauge-pipeline-health`        | Quality & Audit  | Pipeline health analysis (comprehensive)      | 1.1.0   | `/nightgauge:pipeline-health [options]`        |
| `nightgauge-retro`                  | Quality & Audit  | Post-failure root cause analysis              | 1.3.0   | `/nightgauge:retro [options]`                  |
| `nightgauge-dep-modernize`          | Modernization    | Dependency modernization engine               | 1.1.0   | `/nightgauge:dep-modernize [options]`          |
| `nightgauge-refactor-rewrite`       | Modernization    | Refactor vs rewrite decision analysis         | 1.1.0   | `/nightgauge:refactor-rewrite [options]`       |
| `nightgauge-modernize-plan`         | Modernization    | Phased roadmap from assessments               | 1.1.0   | `/nightgauge:modernize-plan [options]`         |
| `nightgauge-test-gen`               | Modernization    | Generate comprehensive test suites            | 1.0.0   | `/nightgauge:test-gen [options]`               |
| `nightgauge-test-scaffold`          | Modernization    | Safety net for refactoring                    | 1.2.0   | `/nightgauge:test-scaffold [options]`          |
| `nightgauge-doc-gen`                | Documentation    | Auto-generate and update API documentation    | 1.1.0   | `/nightgauge:doc-gen [options]`                |
| `nightgauge-docs-watch`             | Documentation    | Monitor Claude Code documentation changes     | 1.0.0   | `/nightgauge:docs-watch [options]`             |
| `nightgauge-docs-write`             | Documentation    | Write narrative documentation sections        | 1.1.0   | `/nightgauge:docs-write [options]`             |
| `nightgauge-continuous-improvement` | Self-Improvement | Unified continuous improvement review         | 1.0.0   | `/nightgauge:continuous-improvement [options]` |
| `pr-preflight`                      | Portable         | Universal PR pre-flight validation            | 1.1.0   | `/pr-preflight`                                |
| `smart-setup`                       | Portable         | Make repository AI-ready                      | 4.7.1   | `/smart-setup`                                 |
| `update-docs`                       | Portable         | Verify and update documentation               | 1.7.0   | `/update-docs [options]`                       |
| `nightgauge-repo-init`              | Project Ops      | Prime repository for Nightgauge               | 1.3.0   | `/nightgauge:repo-init [options]`              |
| `nightgauge-workspace-init`         | Project Ops      | Scaffold multi-repo workspace manifest        | 1.0.0   | `/nightgauge:workspace-init [options]`         |

---

## Core Pipeline Skills

The **6-stage pipeline** runs sequentially from issue to merged PR:

```
Issue Pickup
     ↓
Feature Planning
     ↓
Feature Dev
     ↓
Feature Validate
     ↓
PR Create
     ↓
PR Merge
```

### `/nightgauge-issue-pickup`

**Version:** 1.19.0 | **Purpose:** Start development on a GitHub issue

**Description:**
Claim a GitHub issue, extract requirements, and set up the development environment. This is the entry point for all issue-based work.

**When to Use:**

- At the start of any issue-based development
- To create a feature branch with proper naming
- To establish structured requirements before coding

**Invocation:**

| Tool        | Command                                     |
| ----------- | ------------------------------------------- |
| Claude Code | `/nightgauge-issue-pickup [#]` (via plugin) |
| Copilot     | Invoke via Agent Skills                     |
| Cursor      | Agent Skills or direct SKILL.md             |

**Input:** GitHub issue number (or auto-select highest priority)
**Output:** Feature branch + context file `.nightgauge/pipeline/issue-{N}.json`

**Runs Before:** feature-planning

### `/nightgauge-feature-planning`

**Version:** 1.16.0 | **Purpose:** Documentation-first feature planning

**Description:**
Design a complete implementation plan by reading docs before source code exploration. Produces a PLAN.md file, PRD enrichment, decisions.md, and planning context for downstream stages.

**When to Use:**

- After issue-pickup to plan implementation approach
- To document design decisions before coding
- To ensure all requirements are understood

**Invocation:**

| Tool        | Command                                     |
| ----------- | ------------------------------------------- |
| Claude Code | `/nightgauge-feature-planning` (via plugin) |
| Copilot     | Invoke via Agent Skills                     |
| Cursor      | Agent Skills or direct SKILL.md             |

**Input:** Context from issue-pickup (`.nightgauge/pipeline/issue-{N}.json`)
**Output:** Plan file + planning context `.nightgauge/pipeline/planning-{N}.json`

**Runs Before:** feature-dev | **Runs After:** issue-pickup

### `/nightgauge-feature-dev`

**Version:** 1.10.0 | **Purpose:** Implement features with quality review

**Description:**
Implement features following the approved PLAN.md. Includes code generation, test writing, documentation updates, and quality review against documented standards.

**When to Use:**

- After approved PLAN.md from feature-planning
- To implement planned features
- To generate tests and documentation alongside code

**Invocation:**

| Tool        | Command                                |
| ----------- | -------------------------------------- |
| Claude Code | `/nightgauge-feature-dev` (via plugin) |
| Copilot     | Invoke via Agent Skills                |
| Cursor      | Agent Skills or direct SKILL.md        |

**Input:** Plan file + planning context
**Output:** Code + tests + dev context `.nightgauge/pipeline/dev-{N}.json`

**Arguments:**

- `--plan FILE` — Specify plan file explicitly
- `--sequential` — Disable parallel file creation
- `--skip-review` — Skip quality review (not recommended)

**Runs Before:** feature-validate | **Runs After:** feature-planning

### `/nightgauge-feature-validate`

**Version:** 1.14.0 | **Purpose:** Validate implementation with tests and checklists

**Description:**
Validate feature implementation using integration/E2E tests with Ralph Loop self-healing (up to 3 auto-fix attempts). Generates manual validation checklists and produces validation context for PR creation.

**When to Use:**

- After code from feature-dev
- To verify feature works end-to-end
- Before creating pull request

**Invocation:**

| Tool        | Command                                     |
| ----------- | ------------------------------------------- |
| Claude Code | `/nightgauge-feature-validate` (via plugin) |
| Copilot     | Invoke via Agent Skills                     |
| Cursor      | Agent Skills or direct SKILL.md             |

**Input:** Dev context from feature-dev
**Output:** Validation context `.nightgauge/pipeline/validate-{N}.json`

**Arguments:**

- `--skip-manual` — Skip manual testing prompts
- `--e2e-only` — Only run E2E tests
- `--checklist-only` — Generate checklist without tests
- `--auto-pass` — Auto-pass all checklist items (CI mode)

**Runs Before:** pr-create | **Runs After:** feature-dev

### `/nightgauge-pr-create`

**Version:** 1.19.0 | **Purpose:** Create pull request with validation summary

**Description:**
Create a high-quality pull request with correct base/head, issue linkage, validation summary, and reviewer assignment. Reads prior pipeline context to write a meaningful PR description.

**When to Use:**

- After validated code
- To open a PR for review
- When feature-dev and validate are complete

**Invocation:**

| Tool        | Command                              |
| ----------- | ------------------------------------ |
| Claude Code | `/nightgauge-pr-create` (via plugin) |
| Copilot     | Invoke via Agent Skills              |
| Cursor      | Agent Skills or direct SKILL.md      |

**Input:** Dev + optional validation context
**Output:** Opened PR + context `.nightgauge/pipeline/pr-{N}.json`

**Runs Before:** pr-merge | **Runs After:** feature-dev (or feature-validate)

### `/nightgauge-pr-merge`

**Version:** 1.13.0 | **Purpose:** Wait for reviews and merge PR

**Description:**
Complete the Issue-to-PR pipeline by waiting for CI checks and reviews, addressing feedback, and merging the PR. Handles both auto-merging and review feedback integration.

**When to Use:**

- After PR is opened and reviewed
- When CI checks pass
- To complete the pipeline and close the issue

**Invocation:**

| Tool        | Command                             |
| ----------- | ----------------------------------- |
| Claude Code | `/nightgauge-pr-merge` (via plugin) |
| Copilot     | Invoke via Agent Skills             |
| Cursor      | Agent Skills or direct SKILL.md     |

**Input:** Open PR context (auto-detected or via `--pr N`)
**Output:** Merged PR + closed issue + branch cleanup

**Arguments:**

- `--pr N` — Specify PR number
- `--timeout N` — CI check timeout in minutes (default: 10)
- `--auto-fix` — Auto-fix minor issues without confirmation
- `--no-cleanup` — Skip branch cleanup
- `--merge` / `--rebase` — Merge strategy
- `--skip-ci-gate` — Skip CI check (emergencies only)

**Runs After:** pr-create

---

## Project Setup & Operations

Skills for repository initialization, issue management, and board synchronization.

### `/nightgauge:repo-init`

**Version:** 1.3.0 | **Purpose:** Prime repository for Nightgauge

**Description:**
Set up everything a repository needs to work with the Nightgauge pipeline: standard labels, GitHub Project board fields, repo-to-project linking, and `.nightgauge/config.yaml` with all field IDs pre-populated.

**When to Use:**

- Once when onboarding a new repository (single-repo pipeline setup)
- To validate and refresh field IDs
- When setting up a project board for the first time
- **Multi-repo workspace?** Run this in EACH member repo first, then run
  [`/nightgauge:workspace-init`](#nightgaugeworkspace-init) once at the parent
  folder

**Invocation:**

| Tool        | Command                              |
| ----------- | ------------------------------------ |
| Claude Code | `/nightgauge:repo-init` (via plugin) |
| Copilot     | Invoke via Agent Skills              |
| Cursor      | Agent Skills or direct SKILL.md      |

**Input:** Repository path + GitHub auth
**Output:** Labels + project board fields + config.yaml

**Arguments:**

- `--dry-run` — Preview changes without applying
- `--project N` — Specify GitHub Project number
- `--skip-docs` — Skip documentation phase
- `--seed-from PATH` — Seed complexity model from another repo

**Idempotent:** Safe to re-run at any time

See [full specification](../skills/nightgauge-repo-init/SKILL.md)

---

### `/nightgauge:workspace-init`

**Version:** 1.0.0 | **Purpose:** Scaffold a multi-repo workspace manifest

**Description:**
Primes the layer above `repo-init`: a parent folder that groups several member
repositories sharing one GitHub Project (the N:1 topology). Detects member
repos under the parent folder (each with a `.nightgauge/config.yaml`), derives
the shared project, and generates `.vscode/nightgauge-workspace.yaml` with
`repositories`/`routing`/`epic` blocks. Verifies the result via `workspace
sync-payload`. Without this manifest, opening the parent folder renders an
empty Repositories/board panel in the VSCode extension.

**When to Use:**

- Once when onboarding a parent folder that groups multiple pipeline repos
  sharing a single GitHub Project
- To repair an empty Repositories view after adding a new member repo
- **Run after `repo-init` in each member** — a single repo does not need a
  workspace manifest

**Invocation:**

| Tool        | Command                                   |
| ----------- | ----------------------------------------- |
| Claude Code | `/nightgauge:workspace-init` (via plugin) |
| Copilot     | Invoke via Agent Skills                   |
| Cursor      | Agent Skills or direct SKILL.md           |

**Input:** Parent folder path with 2+ member repos already primed by `repo-init`
**Output:** `.vscode/nightgauge-workspace.yaml` + `workspace sync-payload` verification

**Arguments:**

- `--dry-run` — Preview the manifest without writing it
- `--name <name>` — Workspace display name
- `--project N` — Shared GitHub Project number
- `--root <path>` — Parent folder to scan (default: current directory)

**Idempotent:** Re-running merges newly-detected members without duplicating entries

See [full specification](../skills/nightgauge-workspace-init/SKILL.md)

---

### `/nightgauge:issue-create`

**Version:** 1.15.0 | **Purpose:** Create pipeline-ready GitHub issues

**Description:**
Create well-structured GitHub issues with SDLC metadata, project board sync, and optional parent/child linking. Ensures consistent labeling and milestone assignment.

**When to Use:**

- To create new backlog issues
- To structure spikes vs implementation work
- To ensure issues are pipeline-ready from creation

**Invocation:**

| Tool        | Command                                 |
| ----------- | --------------------------------------- |
| Claude Code | `/nightgauge:issue-create` (via plugin) |
| Copilot     | Invoke via Agent Skills                 |
| Cursor      | Agent Skills or direct SKILL.md         |

**Input:** Issue description + type (implementation or spike)
**Output:** Created issue + board sync + optional knowledge directory

**Arguments:**

- `--with-knowledge` — Scaffold knowledge directory at creation
- Accepts title, description, labels, and milestone via prompts

See [full specification](../skills/nightgauge-issue-create/SKILL.md)

---

### `/nightgauge:project-sync`

**Version:** 1.1.0 | **Purpose:** Bulk-sync issues to project board

**Description:**
Bulk-synchronize existing repository issues to GitHub Project boards with proper field mappings. Syncs dates from milestones and Status field state.

**When to Use:**

- During repository onboarding
- After milestone changes
- When field state gets out of sync

**Invocation:**

| Tool        | Command                                 |
| ----------- | --------------------------------------- |
| Claude Code | `/nightgauge:project-sync` (via plugin) |
| Copilot     | Invoke via Agent Skills                 |
| Cursor      | Agent Skills or direct SKILL.md         |

**Arguments:**

- `--mode MODE` — `full`, `dates-only`, `status-only`, or `report` (default: `full`)
- `--dry-run` — Preview changes without applying
- `--milestone NAME` — Filter by milestone
- `--label PATTERN` — Filter by label

**Idempotent:** Safe to re-run

See [full specification](../skills/nightgauge-project-sync/SKILL.md)

---

### `/nightgauge:config-show`

**Version:** 1.1.0 | **Purpose:** Display effective configuration

**Description:**
Show the merged Nightgauge configuration from all sources (defaults, global, project, environment) with annotations indicating where each value came from.

**When to Use:**

- To debug configuration issues
- To understand configuration precedence
- To verify config has been applied

**Invocation:**

| Tool        | Command                                |
| ----------- | -------------------------------------- |
| Claude Code | `/nightgauge-config-show` (via plugin) |
| Copilot     | Invoke via Agent Skills                |
| Cursor      | Agent Skills or direct SKILL.md        |

**Arguments:**

- `--section SECTION` — Show specific configuration section
- `--source SOURCE` — Show only values from specific source
- `--paths` — Show config file locations only
- `--json` — Output as JSON (for scripting)

See [full specification](../skills/nightgauge-config-show/SKILL.md)

---

### `/nightgauge:queue`

**Version:** 1.0.0 | **Purpose:** Manage issue queue for pipeline processing

**Description:**
Manage the issue queue for sequential and batch pipeline processing. Add, list, remove, clear, and reorder queued issues. Supports epic expansion with automatic sub-issue ordering.

**When to Use:**

- Before starting a pipeline session to pre-load work
- When multiple issues need sequential processing
- To queue an epic and auto-expand sub-issues
- To review and manage queue during pipeline execution

**Invocation:**

| Tool        | Command                         |
| ----------- | ------------------------------- |
| Claude Code | `/nightgauge:queue [args]`      |
| Copilot     | Invoke via Agent Skills         |
| Cursor      | Agent Skills or direct SKILL.md |

**Arguments:**

- `<issue-numbers>` — Add specific issues (e.g., `42 43 44`)
- `--list` / `-l` — Show current queue
- `--clear` — Remove all items
- `--remove <number>` — Remove specific issue
- `--label <label>` — Queue issues matching label
- `--limit <N>` — Limit issues from label query

**Examples:**

```bash
/nightgauge:queue 42 43 44        # Queue specific issues
/nightgauge:queue --list          # Show queue
/nightgauge:queue --label "priority:high"  # Queue by label
/nightgauge:queue --remove 43     # Remove one issue
/nightgauge:queue --clear         # Clear queue
```

See [full specification](../skills/nightgauge-queue/SKILL.md)

---

### `/nightgauge:assess-epic`

**Version:** 1.0.0 | **Purpose:** Analyze epic for batch vs sequential strategy

**Description:**
Analyze an epic's sub-issues to determine whether they should be processed via batch, sequential, or hybrid pipeline execution. Extracts file overlap, size variance, and dependency signals.

**When to Use:**

- Before queuing an epic for pipeline processing
- When an epic has 3+ sub-issues and you want to optimize execution
- After sub-issues change to refresh strategy

**Invocation:**

| Tool        | Command                         |
| ----------- | ------------------------------- |
| Claude Code | `/nightgauge:assess-epic <#>`   |
| Copilot     | Invoke via Agent Skills         |
| Cursor      | Agent Skills or direct SKILL.md |

**Input:** Epic issue number
**Output:** Strategy recommendation (batch/sequential/hybrid) with confidence and savings estimate

**Example:**

```bash
/nightgauge:assess-epic 799
```

See [full specification](../skills/nightgauge-assess-epic/SKILL.md)

---

### `/nightgauge:epic-validate`

**Version:** 1.0.0 | **Purpose:** Post-creation epic validation

**Description:**
Validate that an epic and its sub-issues are correctly structured, linked, and ready for pipeline execution. Checks sub-issue linking, project board assignment, blockedBy relationships, and cross-repo dependencies.

**When to Use:**

- After creating an epic with `/nightgauge:issue-create`
- Before queueing epic sub-issues for pipeline
- When sub-issues are failing (suspects structural problems)

**Invocation:**

| Tool        | Command                                      |
| ----------- | -------------------------------------------- |
| Claude Code | `/nightgauge:epic-validate <#>` (via plugin) |
| Copilot     | Invoke via Agent Skills                      |
| Cursor      | Agent Skills or direct SKILL.md              |

**Input:** Epic issue number
**Output:** Validation report + fix commands for any gaps

**Example:**

```bash
/nightgauge:epic-validate 799
/nightgauge:epic-validate 799 --repo owner/repo
```

**Validates:**

- All sub-issues linked via `addSubIssue`
- All issues on project board with Status set
- `blockedBy` relationships match dependencies
- API endpoint assumptions
- Documentation references

See [full specification](../skills/nightgauge-epic-validate/SKILL.md)

---

### `/nightgauge:backlog-groom`

**Version:** 1.1.0 | **Purpose:** Periodic backlog triage

**Description:**
Automated backlog hygiene: identify stale issues, detect duplicates, validate priorities, and discover hidden dependency chains.

**When to Use:**

- Weekly/monthly for backlog maintenance
- Post-sprint cleanup
- Pre-release grooming
- When onboarding new team members

**Invocation:**

| Tool        | Command                               |
| ----------- | ------------------------------------- |
| Claude Code | `/nightgauge:backlog-groom [options]` |
| Copilot     | Invoke via Agent Skills               |
| Cursor      | Agent Skills or direct SKILL.md       |

**Arguments:**

- `--apply` — Apply recommended changes (default: dry-run)
- `--stale-days N` — Mark inactive issues as stale (default: 60)
- `--focus TYPE` — Focus on specific area: `all`, `stale`, `duplicates`, `priorities`, `dependencies`

**Example:**

```bash
/nightgauge:backlog-groom               # Dry run
/nightgauge:backlog-groom --apply --stale-days 90  # Apply with 90-day threshold
/nightgauge:backlog-groom --focus duplicates       # Focus on duplicates
```

See [full specification](../skills/nightgauge-backlog-groom/SKILL.md)

---

### `/nightgauge:backlog-preflight`

**Version:** 1.1.0 | **Purpose:** Validate backlog before processing

**Description:**
Validate that issues meet minimum requirements for pipeline processing. Extends backlog-groom analysis with greenfield-specific checks (docs drift, API assumptions, etc.).

**When to Use:**

- Before running pipeline on a new repo for the first time
- After bulk-importing issues from another tracker
- Pre-sprint validation
- After major backlog changes

**Invocation:**

| Tool        | Command                                      |
| ----------- | -------------------------------------------- |
| Claude Code | `/nightgauge:backlog-preflight` (via plugin) |
| Copilot     | Invoke via Agent Skills                      |
| Cursor      | Agent Skills or direct SKILL.md              |

**Arguments:**

- `--fix` — Auto-fix issues where possible
- `--status <status>` — Filter by project board status (default: `Ready`)
- `--focus <type>` — Focus on specific check: `all`, `labels`, `criteria`, `dependencies`, `greenfield`, `drift`

**Examples:**

```bash
/nightgauge:backlog-preflight                    # Check Ready issues
/nightgauge:backlog-preflight --focus labels     # Only check labels
/nightgauge:backlog-preflight --fix              # Auto-fix where deterministic
/nightgauge:backlog-preflight --status "In progress"  # Check In Progress items
```

See [full specification](../skills/nightgauge-backlog-preflight/SKILL.md)

---

## Quality & Audit Skills

Comprehensive assessment and improvement tools for codebases and pipeline execution.

### `/nightgauge:health-check`

**Version:** 1.1.0 | **Purpose:** Codebase health assessment (6 dimensions)

**Description:**
Comprehensive codebase health assessment producing quantitative scores across 6 dimensions: code quality, test coverage, dependencies, documentation, security posture, and technical debt. Useful for understanding baseline health before modernization.

**When to Use:**

- When inheriting or auditing an unfamiliar codebase
- Monthly for maintenance tracking
- Before deciding whether to modernize or rewrite
- After major refactoring to measure improvement

**Invocation:**

| Tool        | Command                              |
| ----------- | ------------------------------------ |
| Claude Code | `/nightgauge:health-check [options]` |
| Copilot     | Invoke via Agent Skills              |
| Cursor      | Agent Skills or direct SKILL.md      |

**Arguments:**

- `--path DIR` — Root directory to assess (default: `.`)
- `--package PKG` — Assess specific monorepo package
- `--dimensions DIMS` — Comma-separated dimensions (default: `all`)
- `--format FORMAT` — Output: `summary`, `json`, or `both` (default: `both`)
- `--skip-audit` — Skip dependency audit
- `--output FILE` — Custom output path

**6 Dimensions:**

1. Code Quality
2. Test Coverage
3. Dependencies
4. Documentation
5. Security
6. Technical Debt

**Output:** Quantitative scores (0-100) + narrative findings + recommendations

See [full specification](../skills/nightgauge-health-check/SKILL.md)

---

### `/nightgauge:security-audit`

**Version:** 1.1.0 | **Purpose:** Security posture assessment (7 dimensions)

**Description:**
Comprehensive codebase security assessment producing quantitative scores across 7 dimensions: hardcoded secrets, OWASP Top 10, cryptography, input validation, authentication, authorization, and configuration.

**When to Use:**

- Before public launch or major release
- When onboarding unfamiliar codebase
- After adding authentication or input-handling
- Monthly for security hygiene

**Invocation:**

| Tool        | Command                                |
| ----------- | -------------------------------------- |
| Claude Code | `/nightgauge:security-audit [options]` |
| Copilot     | Invoke via Agent Skills                |
| Cursor      | Agent Skills or direct SKILL.md        |

**Arguments:**

- `--path DIR` — Root directory to assess
- `--package PKG` — Assess specific package
- `--dimensions DIMS` — Comma-separated dimensions
- `--format FORMAT` — Output format (default: `both`)
- `--skip-audit` — Skip dependency audit
- `--severity LEVEL` — Minimum severity to report (default: `low`)

**7 Dimensions:**

1. Hardcoded Secrets & API Keys
2. OWASP Top 10 Vulnerabilities
3. Cryptography & Hashing
4. Input Validation & Sanitization
5. Authentication Mechanisms
6. Authorization & Access Control
7. Configuration & Misconfiguration

**Output:** Scores (0-100) + specific vulnerabilities + remediation guidance

See [full specification](../skills/nightgauge-security-audit/SKILL.md)

---

### `/nightgauge:pipeline-audit`

**Version:** 1.3.0 | **Purpose:** Pipeline efficiency snapshot (quick check)

**Description:**
Fast point-in-time efficiency analysis of pipeline execution history. Computes token usage, stage performance, cost metrics, quality correlation, and trends. **For comprehensive health analysis, use pipeline-health instead.**

**When to Use:**

- Post-sprint efficiency review
- Quick cost analysis
- When you need a fast snapshot
- Identifying bottleneck stages

**Invocation:**

| Tool        | Command                                |
| ----------- | -------------------------------------- |
| Claude Code | `/nightgauge:pipeline-audit [options]` |
| Copilot     | Invoke via Agent Skills                |
| Cursor      | Agent Skills or direct SKILL.md        |

**Arguments:**

- `--runs N` — Analyze last N runs (default: 10)
- `--since DATE` — Analyze runs since date (YYYY-MM-DD)
- `--issue N` — Analyze runs for specific issue
- `--create-issues` — Auto-create GitHub issues for findings
- `--severity LEVEL` — Minimum severity for issue creation (default: `high`)
- `--format FORMAT` — Output format (default: `both`)
- `--compare DATE` — Compare before/after date (YYYY-MM-DD)

**8 Analysis Categories:**

- Token efficiency
- Cost per stage
- Stage performance
- Model routing quality
- Success rates
- Common failure modes
- Time trends
- Cost trends

**Output:** Metrics + findings + recommendations

See [full specification](../skills/nightgauge-pipeline-audit/SKILL.md)

---

### `/nightgauge:pipeline-health`

**Version:** 1.1.0 | **Purpose:** Pipeline health analysis (comprehensive)

**Description:**
Comprehensive pipeline health analysis across 7 dimensions with cross-referenced data sources. Tracks trends over time, compares against baselines, and auto-creates improvement issues. **Use this for in-depth analysis; use pipeline-audit for quick snapshots.**

**When to Use:**

- Weekly/bi-weekly reviews
- After significant pipeline changes
- When costs or failure rates seem abnormal
- Before planning optimization sprints

**Invocation:**

| Tool        | Command                                 |
| ----------- | --------------------------------------- |
| Claude Code | `/nightgauge:pipeline-health [options]` |
| Copilot     | Invoke via Agent Skills                 |
| Cursor      | Agent Skills or direct SKILL.md         |

**Arguments:**

- `--period N` — Analyze last N days (default: 7)
- `--since DATE` — Start date (YYYY-MM-DD)
- `--until DATE` — End date (YYYY-MM-DD)
- `--compare [DATE|LAST]` — Compare with previous audit
- `--create-issues` — Auto-create improvement issues
- `--format FORMAT` — Output format

**7 Dimensions:**

1. Token Economics
2. Cost Health
3. Stage Effectiveness
4. Model Routing
5. Reliability & Failures
6. Self-Improvement Loop
7. Pipeline Velocity

**Output:** Dimension scores + trends + baseline comparison + actionable issues

See [full specification](../skills/nightgauge-pipeline-health/SKILL.md)

---

### `/nightgauge:product-audit`

**Version:** 1.0.0 | **Purpose:** 8-dimension cross-repo quality audit

**Description:**
Orchestrates an 8-dimension quality audit across all Nightgauge repositories (VSCode extension, SDK, platform API, Flutter, Angular). Produces scored report with findings, trends, and optional automated fixes.

**When to Use:**

- Weekly for continuous health monitoring
- Before major releases
- After cross-repo platform changes
- Before sprint planning
- When CI/CD reports anomalies

**Invocation:**

| Tool        | Command                               |
| ----------- | ------------------------------------- |
| Claude Code | `/nightgauge:product-audit [options]` |
| Copilot     | Invoke via Agent Skills               |
| Cursor      | Agent Skills or direct SKILL.md       |

**Arguments:**

- `--create-issues` — Auto-create GitHub issues (default: `false`)
- `--fix` — Auto-fix safe findings (default: `false`)
- `--dimensions DIMS` — Specific dimensions to run
- `--compare [DATE|LAST]` — Compare with previous audit
- `--quick` — Skip slow operations, use cached data
- `--ci` — CI mode: exit 1 if score below threshold
- `--threshold N` — Minimum score for CI mode (default: 75)
- `--severity LEVEL` — Minimum severity for issues (default: `high`)

**8 Dimensions:**

1. API Alignment (client/platform)
2. Epic Lifecycle
3. Documentation Accuracy
4. Feature Parity
5. Test Coverage
6. Security Posture
7. Dependency Health
8. CI/CD Integrity

**Output:** 0-100 overall score + dimension scores + trend analysis + issues

See [full specification](../skills/nightgauge-product-audit/SKILL.md)

---

### `/nightgauge:integration-audit`

**Version:** 1.0.0 | **Purpose:** Cross-repository integration health

**Description:**
Validate that client API calls match platform endpoints, auth flows are aligned, documentation is current, and cross-repo dependencies are tracked. Catches mismatches where client code calls non-existent platform endpoints.

**When to Use:**

- Before creating epics spanning multiple repositories
- After major platform changes
- Periodically (weekly) before sprint planning
- When client apps report unexpected 404/401 errors

**Invocation:**

| Tool        | Command                                   |
| ----------- | ----------------------------------------- |
| Claude Code | `/nightgauge:integration-audit [options]` |
| Copilot     | Invoke via Agent Skills                   |
| Cursor      | Agent Skills or direct SKILL.md           |

**Prerequisites:**

- Docker containers running (platform API healthy)
- All 4 repos cloned: nightgauge, acme-platform, acme-dashboard, acme-mobile
- `gh` CLI authenticated

**Output:** API gap report + auth alignment matrix + stale docs + missing cross-repo links + issue suggestions

See [full specification](../skills/nightgauge-integration-audit/SKILL.md)

---

### `/nightgauge:retro`

**Version:** 1.3.0 | **Purpose:** Post-failure root cause analysis

**Description:**
Scrub session logs and pipeline context to surface failure events, classify them into 7 categories, and generate root-cause retrospective with remediation recommendations. Records outcome data to knowledge base when available.

**When to Use:**

- After batch runs where one or more issues failed
- When a single issue's pipeline stalled unexpectedly
- Post-sprint to review accumulated patterns
- Before planning reliability improvements

**Invocation:**

| Tool        | Command                         |
| ----------- | ------------------------------- |
| Claude Code | `/nightgauge:retro [options]`   |
| Copilot     | Invoke via Agent Skills         |
| Cursor      | Agent Skills or direct SKILL.md |

**Arguments:**

- `--issue N` — Analyze failures for specific issue
- `--since DATE` — Analyze failures since date (YYYY-MM-DD)
- `--period N` — Analyze last N days (default: 7)
- `--all-failures` — Include all failures, not just last batch
- `--format FORMAT` — Output format (default: `both`)
- `--dry-run` — Preview issues without creating (default: `true`)
- `--create-issues` — Actually create GitHub issues
- `--severity LEVEL` — Minimum severity for creation (default: `high`)
- `--record-outcome` — Record outcome to knowledge base
- `--epic N` — Post-epic synthesis (aggregate patterns across epic #N)
- `--skill-feedback` — Run skill self-assessment synthesis

**7 Failure Categories:**

1. Budget Exceeded
2. State Management
3. CI Infrastructure
4. Model Capability
5. Timeout
6. Validation Failure
7. Unknown

**Output:** Failure summary + root causes + remediation guidance + optional GitHub issues

See [full specification](../skills/nightgauge-retro/SKILL.md)

---

## Modernization & Refactoring

Skills for assessing and planning modernization efforts.

### `/nightgauge:health-check` → `/nightgauge:dep-modernize` → `/nightgauge:modernize-plan`

**Typical Workflow:**

1. Run `/nightgauge:health-check` to understand baseline
2. Run `/nightgauge:security-audit` for security posture
3. Run `/nightgauge:dep-modernize` for dependency analysis
4. Run `/nightgauge:modernize-plan` to generate phased roadmap

---

### `/nightgauge:dep-modernize`

**Version:** 1.1.0 | **Purpose:** Dependency modernization engine

**Description:**
Safely identify and update outdated or vulnerable dependencies across npm, Python, Ruby, Go, and Rust. Uses deterministic tools for inventory; AI interprets breaking changes and generates staged rollout plans.

**When to Use:**

- Before major releases for dependency hygiene
- When `npm audit` / `cargo audit` surfaces CVEs
- Monthly for routine maintenance
- After running health-check or security-audit

**Invocation:**

| Tool        | Command                               |
| ----------- | ------------------------------------- |
| Claude Code | `/nightgauge:dep-modernize [options]` |
| Copilot     | Invoke via Agent Skills               |
| Cursor      | Agent Skills or direct SKILL.md       |

**Arguments:**

- `--path DIR` — Root directory (default: `.`)
- `--package PKG` — Specific monorepo package
- `--format FORMAT` — Output: `summary`, `json`, or `both`
- `--dry-run` — Preview without applying (default: `true`)
- `--auto-fix` — Apply safe updates automatically

**Key Features:**

- Ecosystem auto-detection
- Breaking change analysis
- Dependency graph construction
- Topological sort for update groups
- Test execution with rollback
- Risk assessment

**Output:** Update groups + breaking changes + staged rollout plan + estimated effort

See [full specification](../skills/nightgauge-dep-modernize/SKILL.md)

---

### `/nightgauge:refactor-rewrite`

**Version:** 1.1.0 | **Purpose:** Refactor vs rewrite decision analysis

**Description:**
Data-driven decision engine evaluating brownfield codebases across 8 dimensions. Produces recommendation (Refactor, Rewrite, or Hybrid) with confidence levels, risk/benefit matrices, and hybrid approach suggestions.

**When to Use:**

- When deciding modernization approach for aging codebase
- Evaluating individual modules for mixed-strategy approach
- Providing stakeholders with data-driven recommendations
- Before committing to major rewrite effort

**Invocation:**

| Tool        | Command                                  |
| ----------- | ---------------------------------------- |
| Claude Code | `/nightgauge:refactor-rewrite [options]` |
| Copilot     | Invoke via Agent Skills                  |
| Cursor      | Agent Skills or direct SKILL.md          |

**Arguments:**

- `--path DIR` — Root directory
- `--package PKG` — Monorepo package filter
- `--module MODULE` — Specific module/component
- `--dimensions DIMS` — Comma-separated dimensions
- `--format FORMAT` — Output format
- `--skip-coverage-run` — Skip coverage tools
- `--team-size N` — Team size for estimates
- `--timeline WEEKS` — Available timeline

**8 Dimensions:**

1. Maintainability
2. Code Quality
3. Test Coverage
4. Documentation
5. Dependency Health
6. Security Posture
7. Feature Completeness
8. Architecture Fitness

**Output:** Recommendation + confidence + risk/benefit matrix + hybrid approach suggestions + effort estimates

See [full specification](../skills/nightgauge-refactor-rewrite/SKILL.md)

---

### `/nightgauge:modernize-plan`

**Version:** 1.1.0 | **Purpose:** Phased modernization roadmap

**Description:**
Consume structured JSON output from assessment skills (health-check, security-audit, test-scaffold) and produce a prioritized, phased modernization roadmap with effort estimates and risk assessment.

**When to Use:**

- After running assessment skills
- When planning brownfield modernization
- When creating stakeholder-facing roadmaps
- To convert findings into tracked work items

**Invocation:**

| Tool        | Command                                |
| ----------- | -------------------------------------- |
| Claude Code | `/nightgauge:modernize-plan [options]` |
| Copilot     | Invoke via Agent Skills                |
| Cursor      | Agent Skills or direct SKILL.md        |

**Arguments:**

- `--path DIR` — Root directory with assessment outputs
- `--format FORMAT` — Output format (default: `both`)
- `--create-issues` — Generate GitHub issues (default: `false`)
- `--dry-run` — Preview issues without creating
- `--team-size N` — Team size for timeline estimates (default: 1)
- `--sprint-length N` — Sprint length in weeks (default: 2)

**Produces:**

- Phase breakdown (Quick Wins, Foundation, Modernization, Polish)
- Per-phase tasks with effort estimates
- Dependency ordering
- Risk/impact assessment
- Timeline and velocity projection
- Optional GitHub issues for tracked work

**Output:** Roadmap JSON + markdown + optional issues

See [full specification](../skills/nightgauge-modernize-plan/SKILL.md)

---

### `/nightgauge:test-scaffold`

**Version:** 1.2.0 | **Purpose:** Safety net for refactoring

**Description:**
Characterization test generator that creates a safety net capturing current behavior before refactoring begins. Creates baseline tests that pin existing behavior (not ideal behavior) to prevent regression.

**When to Use:**

- Before starting any refactoring
- When inheriting code with low coverage
- Before running health-check on brownfield
- When refactoring legacy code

**Invocation:**

| Tool        | Command                               |
| ----------- | ------------------------------------- |
| Claude Code | `/nightgauge:test-scaffold [options]` |
| Copilot     | Invoke via Agent Skills               |
| Cursor      | Agent Skills or direct SKILL.md       |

**Key Differences from `/nightgauge:test-gen`:**

| Aspect            | test-gen                  | test-scaffold                      |
| ----------------- | ------------------------- | ---------------------------------- |
| Purpose           | Comprehensive test suites | Safety net before refactoring      |
| Test Type         | Unit/integration/E2E      | Characterization (capture current) |
| When              | After feature-dev         | Before refactoring                 |
| Modifies Existing | May improve               | NEVER modifies                     |
| Location          | `tests/`                  | `tests/scaffold/`                  |

**Output:** Characterization test files + coverage gap analysis + risk scoring

See [full specification](../skills/nightgauge-test-scaffold/SKILL.md)

---

### `/nightgauge:test-gen`

**Version:** 1.0.0 | **Purpose:** Generate comprehensive test suites

**Description:**
Generate comprehensive test suites with parallel subagents. Supports Jest, Pytest, dotnet test, and Gradle. Auto-detects test framework and generates unit, integration, and E2E tests.

**When to Use:**

- After feature-dev to improve coverage
- When any codebase needs better test coverage
- To fill coverage gaps identified by assessments
- As part of modernization efforts

**Invocation:**

| Tool        | Command                          |
| ----------- | -------------------------------- |
| Claude Code | `/nightgauge:test-gen [options]` |
| Copilot     | Invoke via Agent Skills          |
| Cursor      | Agent Skills or direct SKILL.md  |

**Arguments:**

- `--files GLOB` — Target specific files
- `--target-coverage N` — Coverage target percentage
- `--types TYPE,TYPE` — Specific test types (unit, integration, e2e)
- `--skip-e2e` — Skip E2E tests (faster)
- `--dry-run` — Preview what would be generated

**Output:** Generated test files + coverage report + running test suite

See [full specification](../skills/nightgauge-test-gen/SKILL.md)

---

## Documentation Skills

Skills for writing, generating, and maintaining documentation.

### `/nightgauge:doc-gen`

**Version:** 1.1.0 | **Purpose:** Auto-generate API documentation

**Description:**
Auto-generate and update documentation for public APIs. Detects undocumented functions, generates JSDoc/docstrings, identifies signature changes, and suggests README updates.

**When to Use:**

- After feature-dev to document new APIs
- Standalone to audit API documentation
- To keep docs in sync with code

**Invocation:**

| Tool        | Command                         |
| ----------- | ------------------------------- |
| Claude Code | `/nightgauge:doc-gen [options]` |
| Copilot     | Invoke via Agent Skills         |
| Cursor      | Agent Skills or direct SKILL.md |

**Arguments:**

- `--files GLOB` — Scan specific files (or auto-detect from dev context)
- `--report-only` — Report without making changes
- `--skip-readme` — Skip README suggestions
- `--all` — Generate for all public APIs (not just undocumented)

**Output:** Generated docstrings + signature change report + README suggestions

See [full specification](../skills/nightgauge-doc-gen/SKILL.md)

---

### `/nightgauge:docs-watch`

**Version:** 1.0.0 | **Purpose:** Monitor Claude Code documentation changes

**Description:**
Monitor Claude Code documentation at https://code.claude.com/docs/ for new pages, removed pages, and content changes. Categorizes findings by relevance to Nightgauge pipeline.

**When to Use:**

- After new Claude Code version releases
- Weekly or bi-weekly monitoring
- When you suspect new features may be available

**Invocation:**

| Tool        | Command                            |
| ----------- | ---------------------------------- |
| Claude Code | `/nightgauge:docs-watch [options]` |
| Copilot     | Invoke via Agent Skills            |
| Cursor      | Agent Skills or direct SKILL.md    |

**Arguments:**

- `--create-issues` — Auto-create GitHub issues for high-relevance changes
- `--dry-run` — Preview what issues would be created
- `--force-refresh` — Re-fetch all pages

**Output:** Change report + relevance classification + optional GitHub issues

See [full specification](../skills/nightgauge-docs-watch/SKILL.md)

---

### `/nightgauge:docs-write`

**Version:** 1.1.0 | **Purpose:** Write narrative documentation sections

**Description:**
Write narrative architecture documentation sections by reading source files and synthesizing validated content. Does NOT require a PLAN.md approval cycle — it's self-contained and standalone.

**When to Use:**

- For documentation issues that require reading code + writing sections
- To update architecture docs after implementation
- To document previously undocumented components
- Standalone (no pipeline approval needed)

**Invocation:**

| Tool        | Command                            |
| ----------- | ---------------------------------- |
| Claude Code | `/nightgauge:docs-write [options]` |
| Copilot     | Invoke via Agent Skills            |
| Cursor      | Agent Skills or direct SKILL.md    |

**Arguments:**

- `--target FILE` — Documentation file to write to (e.g., `docs/ARCHITECTURE.md`)
- `--section NAME` — Section heading to write/update
- `--source GLOB` — Source files to read
- `--dry-run` — Preview without writing
- `--knowledge` — Generate architecture knowledge entries (ADRs + notes)

**Output:** Written documentation section + self-verified accuracy + optional knowledge entries

**Key Features:**

- Self-checks that all referenced classes/functions exist
- Verifies all links resolve correctly
- Generates knowledge base entries from source analysis

See [full specification](../skills/nightgauge-docs-write/SKILL.md)

---

## Self-Improvement

Skills for analyzing and improving the pipeline itself.

### `/nightgauge:continuous-improvement`

**Version:** 1.0.0 | **Purpose:** Unified continuous improvement review

**Description:**
Orchestrates all self-improvement mechanisms (skill assessments, health analysis, calibration, recommendations) into a periodic review cycle. Evaluates whether the self-improvement system is actually working and proposes next steps.

Two modes: **dogfood** (internal product changes) and **customer** (config/workflow adjustments).

**When to Use:**

- Weekly/bi-weekly self-improvement reviews
- Post-sprint retrospectives on pipeline effectiveness
- After shipping batch of skill changes
- When pipeline health scores are declining
- Verifying whether past recommendations improved metrics

**Invocation:**

| Tool        | Command                                        |
| ----------- | ---------------------------------------------- |
| Claude Code | `/nightgauge:continuous-improvement [options]` |
| Copilot     | Invoke via Agent Skills                        |
| Cursor      | Agent Skills or direct SKILL.md                |

**Arguments:**

- `--mode MODE` — `dogfood` (internal) or `customer` (external) — auto-detects
- `--period N` — Analyze last N days (default: 14)
- `--create-issues` — Auto-create improvement issues
- `--severity LEVEL` — Minimum severity for issues

**Dogfood Mode** (Nightgauge team):

- Can propose skill SKILL.md fixes
- Can propose Go binary changes
- Can propose SDK improvements
- Can propose documentation updates
- Generates `continuous-improvement` GitHub issues

**Customer Mode** (External teams):

- Can only propose configuration adjustments
- Can only suggest workflow recommendations
- Never modifies source code
- Never proposes internal product changes

**Output:** Improvement summary + proposal categories + optional GitHub issues

See [full specification](../skills/nightgauge-continuous-improvement/SKILL.md)

---

## Portable Skills

Tool-agnostic skills that work on any repository.

### `/pr-preflight`

**Version:** 1.1.0 | **Purpose:** Universal PR pre-flight validation

**Description:**
Perform universal validation checks before submitting a pull request. Catches common issues that automated reviewers flag. Works on any repository (not Nightgauge-specific).

**When to Use:**

- Before submitting PR to any repository
- To catch common issues proactively
- To validate documentation quality

**Invocation:**

| Tool        | Command                        |
| ----------- | ------------------------------ |
| Claude Code | `/pr-preflight`                |
| Copilot     | Invoke via Agent Skills        |
| Manual      | Run bash scripts from SKILL.md |

**Validates:**

- Broken relative links in markdown
- JSON and YAML syntax
- Semantic versioning format
- Sensitive data (API keys, passwords)
- TODO/FIXME comments
- Documentation completeness
- Large files

**Output:** Validation report + issues found + remediation guidance

See [full specification](../skills/pr-preflight/SKILL.md)

---

### `/smart-setup`

**Version:** 4.7.1 | **Purpose:** Make repository AI-ready

**Description:**
Analyze a repository and create minimal, focused documentation (AGENTS.md, CLAUDE.md) optimized for both humans and AI agents. Uses tiered approach to avoid bloating repositories.

**When to Use:**

- Setting up new repository for AI assistance
- Configuring GitHub Project board integration
- When repository missing AI configuration files
- Onboarding project to use AI tools

**Invocation:**

| Tool        | Command                         |
| ----------- | ------------------------------- |
| Claude Code | `/smart-setup`                  |
| Copilot     | Invoke via Agent Skills         |
| Cursor      | Agent Skills or direct SKILL.md |

**Philosophy:**

- Keep it minimal — only what teams actually need
- Document what IS — current practices and patterns
- Note what SHOULD BE — flag deviations from best practices
- Leave room for WHY — mark sections requiring human input
- Don't bloat repositories — skip files for unused tools

**Output:** AGENTS.md + CLAUDE.md + optional config docs

See [full specification](../skills/smart-setup/SKILL.md)

---

### `/update-docs`

**Version:** 1.7.0 | **Purpose:** Verify and update documentation

**Description:**
Proactively verify that documentation accurately reflects current codebase. Detect documentation drift, deprecated references, and inconsistencies between docs and code.

**When to Use:**

- After significant codebase changes
- When documentation may be stale
- To audit documentation accuracy
- Before major releases

**Invocation:**

| Tool        | Command                         |
| ----------- | ------------------------------- |
| Claude Code | `/update-docs [options]`        |
| Copilot     | Invoke via Agent Skills         |
| Cursor      | Agent Skills or direct SKILL.md |

**Arguments:**

- `--audit-only` — Report discrepancies without fixing
- `--fix-all` — Auto-fix all detected issues
- `--scope <path>` — Limit audit to specific directory
- `--check-deprecated` — Focus on deprecated terms

**Output:** Drift report + fixes applied + re-validation summary

See [full specification](../skills/update-docs/SKILL.md)

---

## Choosing the Right Skill

### I'm starting work on an issue

→ Use `/nightgauge-issue-pickup` to claim the issue and extract requirements

### I've picked up an issue and need to plan

→ Use `/nightgauge-feature-planning` for documentation-first design

### I've approved a plan and need to implement

→ Use `/nightgauge-feature-dev` to write code, tests, and docs

### I've implemented and need to verify it works

→ Use `/nightgauge-feature-validate` to run tests and checklists

### I've validated and need to open a PR

→ Use `/nightgauge-pr-create` to create the PR with full context

### My PR is open and ready to merge

→ Use `/nightgauge-pr-merge` to handle reviews and merge

### I need to create a new backlog issue

→ Use `/nightgauge:issue-create` to structure it for the pipeline

### I want to understand codebase health

→ Use `/nightgauge:health-check` for baseline assessment

### I'm concerned about security

→ Use `/nightgauge:security-audit` to identify vulnerabilities

### I want to assess modernization needs

→ Use `/nightgauge:health-check` → `/nightgauge:dep-modernize` → `/nightgauge:modernize-plan`

### I want to decide between refactoring vs rewriting

→ Use `/nightgauge:refactor-rewrite` for data-driven recommendation

### I need a safety net before refactoring

→ Use `/nightgauge:test-scaffold` to pin current behavior

### I want to improve test coverage

→ Use `/nightgauge:test-gen` to generate comprehensive tests

### I need to diagnose why a pipeline run failed

→ Use `/nightgauge:retro` for root cause analysis

### I want pipeline efficiency insights

→ Use `/nightgauge:pipeline-audit` for quick snapshot or `/nightgauge:pipeline-health` for comprehensive analysis

### I want to validate an epic before queueing

→ Use `/nightgauge:epic-validate` to check all structural requirements

### I want to assess whether to batch or run sequentially

→ Use `/nightgauge:assess-epic` for execution strategy

### I need to manage the pipeline queue

→ Use `/nightgauge:queue` to add/remove/list queued issues

### I want to perform backlog maintenance

→ Use `/nightgauge:backlog-groom` for triage and `/nightgauge:backlog-preflight` for validation

### I want to validate a PR before submitting

→ Use `/pr-preflight` (works on any repository)

### I want to set up a new repo or workspace

→ **Single repo, pipeline onboarding:** `/nightgauge:repo-init` (labels, board
fields/views, config) — use once when onboarding a fresh repository, or to
fill gaps and refresh field IDs.
→ **Multi-repo parent folder:** run `/nightgauge:repo-init` in each member
repo, then `/nightgauge:workspace-init` once at the parent (scaffolds the N:1
shared-project manifest) — use when several repos share one GitHub Project.
→ **Any repo needing AI-ready docs (pipeline or not):** `/smart-setup` — use
when a repository is missing AGENTS.md/CLAUDE.md, regardless of whether it
uses the Nightgauge pipeline.

### I want to make my repository AI-ready

→ Use `/smart-setup` (works on any repository)

### I need to document new APIs

→ Use `/nightgauge:doc-gen` to auto-generate documentation

### I need to write architecture documentation

→ Use `/nightgauge:docs-write` to synthesize narrative docs from source

### I want to check for Claude Code feature updates

→ Use `/nightgauge:docs-watch` to monitor documentation changes

---

## Pipeline Flow Diagram

```
START
  ↓
/nightgauge-issue-pickup
  ↓ (creates issue-{N}.json)
  ↓
/nightgauge-feature-planning
  ↓ (creates planning-{N}.json + PLAN.md)
  ↓
/nightgauge-feature-dev
  ↓ (creates dev-{N}.json + code changes)
  ↓
/nightgauge-feature-validate
  ↓ (creates validate-{N}.json + test results)
  ↓
/nightgauge-pr-create
  ↓ (creates pr-{N}.json + opens PR)
  ↓
/nightgauge-pr-merge
  ↓ (handles reviews, merges PR, closes issue)
  ↓
COMPLETE
```

### Parallel / Supporting Skills

While running the core pipeline, you can use:

- `/nightgauge:doc-gen` — Document any new APIs
- `/nightgauge:test-scaffold` — Create safety net before refactoring
- `/nightgauge:backlog-groom` — Maintain backlog hygiene
- `/nightgauge:queue` — Manage queue for batch processing

### Assessment & Improvement

After running pipeline, analyze with:

- `/nightgauge:pipeline-audit` — Quick efficiency check
- `/nightgauge:pipeline-health` — Comprehensive health review
- `/nightgauge:retro` — Root cause analysis on failures
- `/nightgauge:continuous-improvement` — Meta-review of improvement system

---

## Common Patterns

### Modernizing a Brownfield Codebase

1. **Assess baseline:** `/nightgauge:health-check` + `/nightgauge:security-audit`
2. **Plan improvements:** `/nightgauge:refactor-rewrite` (for individual modules)
3. **Create safety net:** `/nightgauge:test-scaffold` before any refactoring
4. **Modernize dependencies:** `/nightgauge:dep-modernize`
5. **Plan roadmap:** `/nightgauge:modernize-plan`
6. **Execute:** Use pipeline skills to implement modernization issues

### Starting on a New Project

1. **Initialize repository:** `/nightgauge:repo-init` (one-time setup)
   - **Multi-repo parent folder?** Run `repo-init` in each member repo, then
     `/nightgauge:workspace-init` once at the parent
2. **Make AI-ready:** `/smart-setup` (if not using Nightgauge-specific setup)
3. **Backlog validation:** `/nightgauge:backlog-preflight`
4. **Assessment:** `/nightgauge:health-check` (baseline)
5. **First issue:** `/nightgauge-issue-pickup` → full 6-stage pipeline

### Onboarding to Unfamiliar Codebase

1. **Understand structure:** `/smart-setup` or `/nightgauge:docs-write`
2. **Health baseline:** `/nightgauge:health-check` + `/nightgauge:security-audit`
3. **Test coverage:** `/nightgauge:test-scaffold` for safety net
4. **Pick first issue:** `/nightgauge-issue-pickup`

### Running Batch Pipeline Work

1. **Assess epics:** `/nightgauge:assess-epic` (for each epic)
2. **Queue issues:** `/nightgauge:queue` (pre-load work)
3. **Run pipeline:** Invoke skills for each queued issue in sequence
4. **Monitor efficiency:** `/nightgauge:pipeline-audit`
5. **Post-batch analysis:** `/nightgauge:retro` (on failures)

---

## Architecture Notes

- **Deterministic vs Probabilistic:** Skills separate deterministic tool commands (bash, jq, graphql) from probabilistic AI interpretation. Deterministic steps are reproducible and debuggable.
- **Context Handoff:** Core pipeline skills communicate via JSON context files in `.nightgauge/pipeline/`, not conversation history.
- **No Reinvention:** Skills reuse the Go binary (`cmd/nightgauge/`) for deterministic operations rather than reimplementing logic.
- **Headless by Default:** Skills can run in automated/headless mode. Some support `AskUserQuestion` for interactive mode when needed.
- **Phase Markers:** Skills emit structured HTML comments to track progress (`<!-- phase:start ... -->`).

---

## References

- **Full specification:** See individual SKILL.md files referenced throughout
- **Pipeline context schema:** [docs/CONTEXT_ARCHITECTURE.md](./CONTEXT_ARCHITECTURE.md)
- **Git workflow & validation:** [docs/GIT_WORKFLOW.md](./GIT_WORKFLOW.md)
- **Configuration:** [docs/CONFIGURATION.md](./CONFIGURATION.md)
- **Architecture:** [docs/ARCHITECTURE.md](./ARCHITECTURE.md)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)
- **Decision trees (which skill for a given intent):** [docs/DECISION_TREES.md](./DECISION_TREES.md)
- **Full skill catalog:** [skills/README.md](../skills/README.md)

---

**Generated:** 2026-03-24
**Total Skills:** 35
**Categories:** 7 (Core Pipeline, Project Ops, Quality & Audit, Modernization, Documentation, Self-Improvement, Portable)

Last updated by analyzing all SKILL.md files in the `skills/` directory. For changes or corrections, see the individual SKILL.md files.
