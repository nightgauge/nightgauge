# Context-Isolated Pipeline Architecture

This document describes the context handoff system used by the Nightgauge
pipeline to enable efficient agent execution without context exhaustion.

## Overview

The Nightgauge pipeline uses **structured JSON handoff files** to pass
context between pipeline stages, rather than accumulating conversation history.
Each stage runs as a fresh subagent with minimal prompt, reading exactly what it
needs from files.

### Benefits

- **No context exhaustion**: Each stage starts fresh, preventing context
  compaction
- **Efficient token usage**: Subagents receive file paths, not inline content
- **Clear contracts**: Each skill documents its input/output schemas
- **Debuggable**: Context files can be inspected between stages
- **Resumable**: Pipeline can be restarted from any stage using context files

## Directory Structure

All pipeline files are stored relative to the **git repository root** (not the
VSCode workspace root). This ensures consistent file placement even when VSCode
is opened in a subdirectory.

```
{git_root}/.nightgauge/
├── pipeline/                   # Pipeline handoff files (transient)
│   ├── state.json             # Unified pipeline state (PipelineStateService)
│   ├── issue-{N}.json         # Output of /issue-pickup
│   ├── planning-{N}.json      # Output of /feature-planning
│   ├── dev-{N}.json           # Output of /feature-dev
│   ├── validate-{N}.json      # Output of /feature-validate (optional)
│   ├── pr-{N}.json            # Output of /pr-create
│   ├── batch-{E}.json         # Output of /issue-pickup (batch mode)
│   ├── planning-batch-{E}.json # Output of /feature-planning (batch mode)
│   ├── dev-batch-{E}.json     # Output of /feature-dev (batch mode)
│   ├── feedback-{N}.json      # Written by orchestrator on backtrack
│   └── epic-context-{E}.json  # Epic context accumulator (persistent across sub-issues)
├── plans/                      # Feature plans (cleaned up after PR)
│   └── {N}-{description}.md   # Output of /feature-planning
├── logs/                       # Execution logs (persistent)
│   └── nightgauge-output-{N}.json
└── config.yaml                # Configuration
```

**Note**: Context files are transient working documents. They are cleaned up by
`/pr-merge` after successful merge. The GitHub issue and PR preserve the
permanent record.

## Pipeline Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     CONTEXT-ISOLATED PIPELINE FLOW                           │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  GitHub Issue                                                                │
│       │                                                                      │
│       ▼                                                                      │
│  ┌─────────────────┐     ┌─────────────────────┐                            │
│  │  /issue-pickup  │────▶│ issue-{N}.json      │                            │
│  │                 │     │ • requirements      │                            │
│  └─────────────────┘     │ • branch info       │                            │
│                          │ • labels            │                            │
│                          └──────────┬──────────┘                            │
│                                     │                                        │
│                                     ▼                                        │
│  ┌─────────────────┐     ┌─────────────────────┐     ┌──────────────────┐  │
│  │/feature-planning│◀────│ Read issue-{N}.json │     │ planning-{N}.json│  │
│  │                 │────▶│                     │────▶│ + PLAN.md        │  │
│  └─────────────────┘     └─────────────────────┘     └────────┬─────────┘  │
│                                                                │             │
│                                                                ▼             │
│  ┌─────────────────┐     ┌─────────────────────┐     ┌──────────────────┐  │
│  │  /feature-dev   │◀────│Read planning-{N}.json│    │ dev-{N}.json     │  │
│  │  (no commit/    │────▶│                     │────▶│ • files changed  │  │
│  │   push)         │     └─────────────────────┘     │ • commit_sha=null│  │
│  └─────────────────┘                                 └────────┬─────────┘  │
│                                                                ▼             │
│  ┌─────────────────┐     ┌─────────────────────┐     ┌──────────────────┐  │
│  │/feature-validate│◀────│ Read dev-{N}.json   │     │validate-{N}.json │  │
│  │ (commits+pushes │────▶│                     │────▶│ • test results   │  │
│  │  after passing) │     └─────────────────────┘     │ • commit SHA     │  │
│  └─────────────────┘                                 │ • checklist      │  │
│                                                       └────────┬─────────┘  │
│                                                                ▼             │
│  ┌─────────────────┐     ┌─────────────────────┐     ┌──────────────────┐  │
│  │   /pr-create    │◀────│Read validate-{N}.json│    │ pr-{N}.json      │  │
│  │                 │────▶│ (commit_sha here)   │────▶│ • PR number      │  │
│  └─────────────────┘     └─────────────────────┘     │ • preflight      │  │
│                                                       └────────┬─────────┘  │
│                                                                ▼             │
│  ┌─────────────────┐     ┌─────────────────────┐                            │
│  │   /pr-merge     │◀────│ Read pr-{N}.json    │                            │
│  │                 │────▶│ Cleanup all context │                            │
│  └─────────────────┘     └─────────────────────┘                            │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Context File Schemas

All context files include a `schema_version` field for future compatibility.

### issue-{N}.json

**Created by**: `/nightgauge-issue-pickup` **Read by**:
`/nightgauge-feature-planning`, HeadlessOrchestrator

**Schema Version**: 1.4 (version 1.3 adds sub-issue fields — Issue #38; version
1.4 adds spike issue type — Issue #168)

```json
{
  "schema_version": "1.4",
  "issue_number": 42,
  "title": "Add user photo upload",
  "type": "feature",
  "branch": "feat/42-user-photo-upload",
  "base_branch": "main",
  "requirements": {
    "summary": "Allow users to upload profile photos",
    "user_story": "As a user, I want to upload a profile photo...",
    "acceptance_criteria": ["Users can upload JPG/PNG images", "Images are resized to 200x200"],
    "technical_notes": ["Integrate with FileService", "Use S3 storage"]
  },
  "labels": ["feature", "priority:high", "size:M"],
  "milestone": "v2.0",
  "parent_issue": 295,
  "native_parent": 295,
  "child_issues": [301, 302, 303],
  "sub_issue_progress": {
    "open": 1,
    "closed": 2,
    "total": 3
  },
  "routing": {
    "change_type": "code",
    "task_type": "feature",
    "complexity_score": 3,
    "suggested_route": "standard",
    "skip_stages": [],
    "rationale": "M code change with high priority → standard path",
    "estimated_time_minutes": 30
  },
  "dependencies": {
    "blockedBy": [{ "number": 100, "title": "Foundation", "url": "...", "state": "OPEN" }],
    "blocks": [],
    "enforcement_override": false
  },
  "created_at": "2026-02-01T12:00:00Z"
}
```

| Field                               | Type   | Required | Description                                                                              |
| ----------------------------------- | ------ | -------- | ---------------------------------------------------------------------------------------- |
| `schema_version`                    | string | Yes      | Schema version for compatibility (1.0, 1.1, 1.3, or 1.4)                                 |
| `issue_number`                      | number | Yes      | GitHub issue number                                                                      |
| `title`                             | string | Yes      | Issue title                                                                              |
| `type`                              | string | Yes      | Issue type: feature, bug, docs, refactor, spike                                          |
| `branch`                            | string | Yes      | Feature branch name                                                                      |
| `base_branch`                       | string | Yes      | Target branch for PR                                                                     |
| `requirements`                      | object | Yes      | Extracted requirements                                                                   |
| `requirements.summary`              | string | Yes      | Brief summary                                                                            |
| `requirements.user_story`           | string | No       | User story if present                                                                    |
| `requirements.acceptance_criteria`  | array  | No       | List of criteria                                                                         |
| `requirements.technical_notes`      | array  | No       | Technical details                                                                        |
| `labels`                            | array  | Yes      | GitHub labels                                                                            |
| `milestone`                         | string | No       | Milestone if set                                                                         |
| `parent_issue`                      | number | No       | Parent issue number (legacy field, use native_parent for GitHub API)                     |
| `native_parent`                     | number | No       | Parent issue number from GitHub native sub-issues API (v1.3+)                            |
| `child_issues`                      | array  | No       | Child issue numbers from GitHub native sub-issues API (v1.3+)                            |
| `sub_issue_progress`                | object | No       | Progress statistics for child issues (v1.3+)                                             |
| `sub_issue_progress.open`           | number | Yes      | Number of open child issues                                                              |
| `sub_issue_progress.closed`         | number | Yes      | Number of closed child issues                                                            |
| `sub_issue_progress.total`          | number | Yes      | Total number of child issues                                                             |
| `routing`                           | object | No       | Routing information for stage skipping (v1.1+)                                           |
| `routing.change_type`               | string | Yes      | Detected change type: docs, config, code                                                 |
| `routing.task_type`                 | string | Yes      | Task type for routing: feature, bugfix, verification, docs-only, refactor, chore (v1.3+) |
| `routing.complexity_score`          | number | Yes      | Fibonacci complexity score (1, 2, 3, 5, 8)                                               |
| `routing.suggested_route`           | string | Yes      | Routing path: trivial, standard, extensive                                               |
| `routing.skip_stages`               | array  | Yes      | Stages to skip (feature-planning, feature-validate, pr-create, pr-merge)                 |
| `routing.rationale`                 | string | Yes      | Human-readable explanation of routing decision                                           |
| `routing.estimated_time_minutes`    | number | Yes      | Estimated pipeline time in minutes                                                       |
| `dependencies`                      | object | No       | Dependency tracking information (v1.3+)                                                  |
| `dependencies.blockedBy`            | array  | Yes      | Issues that block this one (from GitHub blockedBy API)                                   |
| `dependencies.blockedBy[].number`   | number | Yes      | Blocking issue number                                                                    |
| `dependencies.blockedBy[].title`    | string | Yes      | Blocking issue title                                                                     |
| `dependencies.blockedBy[].url`      | string | Yes      | Blocking issue URL                                                                       |
| `dependencies.blockedBy[].state`    | string | Yes      | Blocking issue state (OPEN/CLOSED)                                                       |
| `dependencies.blocks`               | array  | Yes      | Issues blocked by this one (from GitHub blocking API)                                    |
| `dependencies.enforcement_override` | bool   | Yes      | True if user acknowledged blockers in warn mode                                          |
| `created_at`                        | string | Yes      | ISO 8601 timestamp                                                                       |
| `knowledge_path`                    | string | No       | Path to knowledge directory for this issue (v1.5+)                                       |

### planning-{N}.json

**Created by**: `/nightgauge-feature-planning` **Read by**:
`/nightgauge-feature-dev`

```json
{
  "schema_version": "1.1",
  "issue_number": 42,
  "plan_file": ".nightgauge/plans/42-user-photo-upload.md",
  "approach": "pragmatic",
  "files_to_create": ["src/services/PhotoService.ts", "tests/photo.test.ts"],
  "files_to_modify": ["src/routes/users.ts"],
  "files_to_read": ["src/services/FileService.ts", "src/types/user.ts"],
  "patterns_applied": {
    "architecture": "Service pattern from ARCHITECTURE.md",
    "security": "Input validation per SECURITY.md"
  },
  "decisions": [
    {
      "topic": "Storage Backend",
      "options": ["S3", "Local", "Both"],
      "selection": "Both",
      "rationale": "Enables local dev without AWS while maintaining production parity"
    },
    {
      "topic": "Implementation Approach",
      "options": ["Minimal", "Clean Architecture", "Pragmatic"],
      "selection": "Pragmatic",
      "rationale": "Balances delivery speed with maintainability"
    }
  ],
  "coverage_baseline": {
    "statements": 85.2,
    "branches": 72.1,
    "lines": 84.8
  },
  "complexity_assessment": {
    "size_label": "M",
    "type_label": "feature",
    "priority_label": "high",
    "computed_score": 3,
    "documentation_scope": "standard",
    "rationale": "Medium-sized feature requires full documentation review",
    "estimated_token_savings": 0
  },
  "docs_consulted": {
    "discovery_method": "keyword-matched",
    "keywords_extracted": ["photo", "upload", "service", "storage"],
    "files_read": [
      { "path": "docs/ARCHITECTURE.md", "reason": "keyword match: service" },
      { "path": "docs/SECURITY.md", "reason": "essential doc" },
      { "path": "docs/GIT_WORKFLOW.md", "reason": "essential doc" }
    ],
    "files_skipped": [{ "path": "docs/TESTING.md", "reason": "no keyword match" }],
    "estimated_tokens_saved": 1500
  },
  "pattern_mining_results": {
    "patterns_found": [
      {
        "pattern_type": "naming_convention",
        "category": "file_naming",
        "pattern": "Services named `*Service.ts` in `src/services/`",
        "evidence": ["src/services/PhotoService.ts", "src/services/FileService.ts"],
        "frequency": 12,
        "example_implementations": ["src/services/PhotoService.ts:1-50"]
      }
    ],
    "similar_issues": [],
    "pattern_classifications": {
      "naming_conventions": 1,
      "structural_patterns": 0,
      "interface_patterns": 0,
      "idioms": 0
    },
    "search_queries_used": ["Service.ts"],
    "coverage_ratio": 0.68,
    "token_cost_estimate": 2400,
    "recommendations": ["Follow service pattern from src/services/ directory"]
  },
  "knowledge_path": null,
  "knowledge_entries": [],
  "cross_repo_knowledge": [
    {
      "repo": "acme-platform",
      "path": "../acme-platform/.nightgauge/knowledge",
      "entries": ["features/1234-auth-design/decisions.md"]
    }
  ],
  "created_at": "2026-02-01T12:30:00Z"
}
```

| Field                                            | Type   | Required | Description                                                                                                                                                                             |
| ------------------------------------------------ | ------ | -------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `schema_version`                                 | string | Yes      | Schema version for compatibility (1.5)                                                                                                                                                  |
| `issue_number`                                   | number | Yes      | GitHub issue number                                                                                                                                                                     |
| `plan_file`                                      | string | Yes      | Path to PLAN.md file                                                                                                                                                                    |
| `approach`                                       | string | Yes      | Selected implementation approach. Special value `"verify-and-close"` signals the issue is already resolved — orchestrator short-circuits remaining stages (Issue #708)                  |
| `files_to_create`                                | array  | Yes      | New files to create                                                                                                                                                                     |
| `files_to_modify`                                | array  | Yes      | Existing files to modify                                                                                                                                                                |
| `files_to_read`                                  | array  | No       | Existing files feature-dev should pre-load for implementation context                                                                                                                   |
| `patterns_applied`                               | object | No       | Documented patterns used                                                                                                                                                                |
| `decisions`                                      | array  | No       | Architectural decisions made during planning                                                                                                                                            |
| `decisions[].topic`                              | string | Yes      | Decision topic (e.g., "Storage Backend")                                                                                                                                                |
| `decisions[].options`                            | array  | Yes      | Options that were considered                                                                                                                                                            |
| `decisions[].selection`                          | string | Yes      | Selected option                                                                                                                                                                         |
| `decisions[].rationale`                          | string | Yes      | Reason for the selection                                                                                                                                                                |
| `coverage_baseline`                              | object | No       | Coverage metrics before changes                                                                                                                                                         |
| `complexity_assessment`                          | object | No       | Complexity assessment from adaptive reading (v1.8.0+)                                                                                                                                   |
| `complexity_assessment.size_label`               | string | Yes      | Size label from issue (XS/S/M/L/XL)                                                                                                                                                     |
| `complexity_assessment.type_label`               | string | Yes      | Issue type (feature/bug/docs/refactor/chore)                                                                                                                                            |
| `complexity_assessment.priority_label`           | string | Yes      | Priority label (critical/high/medium/low)                                                                                                                                               |
| `complexity_assessment.computed_score`           | number | Yes      | Fibonacci complexity score (1/2/3/5/8)                                                                                                                                                  |
| `complexity_assessment.documentation_scope`      | string | Yes      | Documentation scope used (minimal/targeted/standard/extended)                                                                                                                           |
| `complexity_assessment.rationale`                | string | Yes      | Human-readable explanation of scope decision                                                                                                                                            |
| `complexity_assessment.estimated_token_savings`  | number | Yes      | Estimated token savings vs standard scope                                                                                                                                               |
| `docs_consulted`                                 | object | No       | Documentation discovery results (v1.1+)                                                                                                                                                 |
| `docs_consulted.discovery_method`                | string | Yes      | Method used: keyword-matched, scope-fallback, extended-all                                                                                                                              |
| `docs_consulted.keywords_extracted`              | array  | Yes      | Keywords extracted from issue content                                                                                                                                                   |
| `docs_consulted.files_read`                      | array  | Yes      | Files read with path and reason                                                                                                                                                         |
| `docs_consulted.files_read[].path`               | string | Yes      | File path (e.g., "docs/ARCHITECTURE.md")                                                                                                                                                |
| `docs_consulted.files_read[].reason`             | string | Yes      | Why read: "keyword match: X" or "essential doc" or "scope: Y"                                                                                                                           |
| `docs_consulted.files_skipped`                   | array  | No       | Files not read with path and reason                                                                                                                                                     |
| `docs_consulted.estimated_tokens_saved`          | number | Yes      | Estimated tokens saved vs reading all docs                                                                                                                                              |
| `created_at`                                     | string | Yes      | ISO 8601 timestamp                                                                                                                                                                      |
| `knowledge_path`                                 | string | No       | Path to knowledge directory (v1.3+)                                                                                                                                                     |
| `knowledge_entries`                              | array  | No       | Markdown filenames in the knowledge directory (v1.3+)                                                                                                                                   |
| `pattern_mining_results`                         | object | No       | Pattern mining results from Phase 2.5 (v1.5+). Null when skipped or no patterns found.                                                                                                  |
| `pattern_mining_results.patterns_found`          | array  | Yes      | Discovered codebase patterns with evidence (min 2 evidence files each)                                                                                                                  |
| `pattern_mining_results.similar_issues`          | array  | Yes      | Issues with similar pattern overlaps, ranked by relevance                                                                                                                               |
| `pattern_mining_results.pattern_classifications` | object | Yes      | Counts: `{naming_conventions, structural_patterns, interface_patterns, idioms}`                                                                                                         |
| `pattern_mining_results.recommendations`         | array  | Yes      | Actionable recommendations based on discovered patterns                                                                                                                                 |
| `cross_repo_knowledge`                           | array  | No       | Cross-repo knowledge entries read during planning (v1.4+). Each entry: `{repo, path, entries[]}`. Empty when no workspace config or no sibling knowledge exists.                        |
| `ac_reconcile`                                   | object | No       | Deterministic AC reconciliation report (v1.6+, Issue #3003). Same shape as `ac-reconcile-{N}.json`. Null or absent when the issue body had no checkboxes or the binary was unavailable. |

### ac-reconcile-{N}.json

**Created by**: `/nightgauge-feature-planning` (Phase 1.7 — `ac-reconcile`)
**Read by**: `/nightgauge-feature-planning` (embeds into `planning-{N}.json`),
`/nightgauge-feature-dev` (read transitively via `planning.ac_reconcile`)

Output of the deterministic AC reconciliation pre-flight (Issue #3003). The
reconciler parses checkboxes from the issue body, runs the rule library
(`packages/nightgauge-sdk/src/preflight/ac-rules/`) against the working
tree, and classifies each AC. The aggregate determines whether feature-planning
short-circuits to `verify-and-close`, narrows scope, or continues normally.

```json
{
  "schema_version": "1.0",
  "issue_number": 801,
  "main_sha": "0a1b2c3d4e",
  "evaluated_at": "2026-04-25T15:30:00Z",
  "acceptance_criteria": [
    {
      "index": 0,
      "text": "New file `.github/workflows/ci.yml` exists",
      "checkbox_state": "checked",
      "rule_applied": "file-exists",
      "classification": "satisfied",
      "reason": "File present: .github/workflows/ci.yml",
      "evidence": [".github/workflows/ci.yml"]
    }
  ],
  "aggregate_status": "mostly-satisfied",
  "suggested_route": {
    "approach": "narrow-scope",
    "focus_acs": [5],
    "rationale": "5/6 criteria satisfied — narrow plan scope to the remaining 1"
  }
}
```

| Field                                  | Type         | Required | Description                                                                                                |
| -------------------------------------- | ------------ | -------- | ---------------------------------------------------------------------------------------------------------- |
| `schema_version`                       | string       | Yes      | Always `"1.0"` for this schema                                                                             |
| `issue_number`                         | number       | Yes      | GitHub issue number                                                                                        |
| `main_sha`                             | string       | Yes      | Output of `git rev-parse main` at evaluation time                                                          |
| `evaluated_at`                         | string       | Yes      | ISO 8601 timestamp                                                                                         |
| `acceptance_criteria`                  | array        | Yes      | Per-AC reconciliation results in body order                                                                |
| `acceptance_criteria[].index`          | number       | Yes      | 0-based position in the body                                                                               |
| `acceptance_criteria[].text`           | string       | Yes      | Trimmed checkbox text                                                                                      |
| `acceptance_criteria[].checkbox_state` | string       | Yes      | `"checked"` or `"unchecked"` — informational only                                                          |
| `acceptance_criteria[].rule_applied`   | string\|null | Yes      | Name of the rule that classified this AC; null when no rule matched                                        |
| `acceptance_criteria[].classification` | string       | Yes      | `satisfied` \| `partial` \| `unsatisfied` \| `undetectable`                                                |
| `acceptance_criteria[].reason`         | string       | Yes      | Human-readable evidence summary                                                                            |
| `acceptance_criteria[].evidence`       | array        | Yes      | File paths or other evidence references                                                                    |
| `aggregate_status`                     | string       | Yes      | `all-satisfied` \| `mostly-satisfied` \| `partial` \| `unsatisfied` \| `undetectable` \| `no-acs-detected` |
| `suggested_route.approach`             | string       | Yes      | `verify-and-close` \| `narrow-scope` \| `standard`                                                         |
| `suggested_route.focus_acs`            | array        | Yes      | 0-based indices the planner should focus on                                                                |
| `suggested_route.rationale`            | string       | Yes      | Human-readable explanation                                                                                 |

The reconciler consumes zero LLM tokens. Aggregate status of `all-satisfied`
maps `approach` to `verify-and-close`, which short-circuits feature-dev via
the existing Issue #708 short-circuit.

### dev-{N}.json

**Created by**: `/nightgauge-feature-dev` **Read by**:
`/nightgauge-pr-create`, `/nightgauge-feature-validate`

**Schema Version**: 1.6 (version 1.1 adds `build_verification` object, extends
`tests_status` with test detail fields, and extends `quality_checks` with
`type_check` and `dead_code_scan` — issue #867; version 1.2 makes `commit_sha`
always null — commit+push moved to feature-validate — issue #1608; version 1.5
adds `knowledge_path` — issue #1679; version 1.6 adds `cross_repo_knowledge` —
issue #1700)

```json
{
  "schema_version": "1.2",
  "issue_number": 42,
  "commit_sha": null,
  "files_changed": {
    "created": ["src/services/PhotoService.ts"],
    "modified": ["src/routes/users.ts"],
    "deleted": []
  },
  "build_verification": {
    "ran": true,
    "status": "passed",
    "commands_run": ["npm run build"],
    "timestamp": "2026-02-01T12:55:00Z"
  },
  "tests_status": {
    "passed": 15,
    "failed": 0,
    "coverage": 87.5,
    "test_command": "npx vitest run",
    "includes_integration": false,
    "includes_e2e": false,
    "test_files_run": 5
  },
  "quality_checks": {
    "code_standards": "passed",
    "security_review": "passed",
    "type_check": "passed",
    "dead_code_scan": "not_run"
  },
  "created_at": "2026-02-01T13:00:00Z"
}
```

| Field                               | Type   | Required | Description                                                                                                                                    |
| ----------------------------------- | ------ | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `schema_version`                    | string | Yes      | Schema version for compatibility (1.0, 1.1, or 1.2)                                                                                            |
| `issue_number`                      | number | Yes      | GitHub issue number                                                                                                                            |
| `commit_sha`                        | string | No       | Always `null` since v1.2 — commit+push moved to feature-validate (issue #1608). Retained for schema compatibility.                             |
| `files_changed`                     | object | Yes      | Summary of file changes                                                                                                                        |
| `files_changed.created`             | array  | Yes      | New files created                                                                                                                              |
| `files_changed.modified`            | array  | Yes      | Files modified                                                                                                                                 |
| `files_changed.deleted`             | array  | Yes      | Files deleted                                                                                                                                  |
| `build_verification`                | object | No       | Build verification details (v1.1+)                                                                                                             |
| `build_verification.ran`            | bool   | Yes      | Whether build was executed                                                                                                                     |
| `build_verification.status`         | string | Yes      | Build result: `passed`, `failed`, or `skipped`                                                                                                 |
| `build_verification.commands_run`   | array  | Yes      | Build commands executed (e.g., `["npm run build"]`)                                                                                            |
| `build_verification.timestamp`      | string | No       | ISO 8601 timestamp of build completion                                                                                                         |
| `tests_status`                      | object | Yes      | Test results                                                                                                                                   |
| `tests_status.passed`               | number | Yes      | Passing test count                                                                                                                             |
| `tests_status.failed`               | number | Yes      | Failing test count                                                                                                                             |
| `tests_status.coverage`             | number | No       | Coverage percentage                                                                                                                            |
| `tests_status.test_command`         | string | No       | Test command used (v1.1+, e.g., `"npx vitest run"`)                                                                                            |
| `tests_status.includes_integration` | bool   | No       | Whether integration tests were included (v1.1+)                                                                                                |
| `tests_status.includes_e2e`         | bool   | No       | Whether E2E tests were included (v1.1+)                                                                                                        |
| `tests_status.test_files_run`       | number | No       | Number of test files executed (v1.1+)                                                                                                          |
| `tests_status.e2e_framework`        | string | No       | E2E framework used: `"playwright"`, `"cypress"`, or `"selenium"` (v1.7+)                                                                       |
| `tests_status.e2e_tests_generated`  | bool   | No       | Whether E2E test suggestions were generated for UI changes (v1.7+)                                                                             |
| `quality_checks`                    | object | Yes      | Review results                                                                                                                                 |
| `quality_checks.code_standards`     | string | Yes      | Code standards result: `passed`, `failed`, or `skipped`                                                                                        |
| `quality_checks.security_review`    | string | Yes      | Security review result: `passed`, `failed`, or `skipped`                                                                                       |
| `quality_checks.type_check`         | string | No       | TypeScript type check result: `passed`, `failed`, `skipped` (v1.1+)                                                                            |
| `quality_checks.dead_code_scan`     | string | No       | Dead code scan result: `passed`, `failed`, `not_run` (v1.1+)                                                                                   |
| `created_at`                        | string | Yes      | ISO 8601 timestamp                                                                                                                             |
| `knowledge_path`                    | string | No       | Path to knowledge directory for this issue (v1.5+)                                                                                             |
| `cross_repo_knowledge`              | array  | No       | Cross-repo knowledge entries from planning context, threaded to dev for implementation context (v1.6+). Each entry: `{repo, path, entries[]}`. |

### validate-{N}.json

**Created by**: `/nightgauge-feature-validate` **Read by**:
`/nightgauge-pr-create`

**Schema Version**: 1.9 (version 1.1 adds `build` object; version 1.2 adds
`dead_code_warnings` array and `unit_tests` object; version 1.3 adds
`preexisting_failures` array — issue #836; version 1.4 adds `skipped_phases`
array — issue #861; version 1.5 adds `ac_completion_check` object — issue #1274;
version 1.9 adds `commit_sha` field — commit+push moved here from feature-dev —
issue #1608)

```json
{
  "schema_version": "1.9",
  "issue_number": 42,
  "commit_sha": "abc123def456",
  "validation_status": "passed",
  "build": {
    "ran": true,
    "passed": true,
    "command": "npm run build"
  },
  "integration_tests": {
    "ran": true,
    "passed": true,
    "framework": "jest",
    "tests_run": 12,
    "tests_passed": 12
  },
  "e2e_tests": {
    "ran": true,
    "passed": true,
    "framework": "playwright",
    "reason": null
  },
  "dead_code_warnings": [
    {
      "type": "unused-export",
      "name": "unusedHelper",
      "location": "src/utils/helpers.ts:42",
      "severity": "warning"
    }
  ],
  "preexisting_failures": [
    {
      "test_file": "tests/services/IssueQueueService.test.ts",
      "failure_count": 2,
      "baseline_verified": true
    }
  ],
  "skipped_phases": [
    {
      "phase": "build_verification",
      "reason": "build verified by feature-dev (dev context build_verification.status=passed)"
    },
    {
      "phase": "unit_tests",
      "reason": "dev context shows all unit tests passed (passed=5030, failed=0)"
    }
  ],
  "manual_checklist": [
    { "item": "Extension activates in VS Code", "verified": true },
    { "item": "Tree view renders correctly", "verified": true },
    { "item": "Commands work from palette", "verified": true }
  ],
  "ac_completion_check": {
    "applicable": true,
    "status": "passed",
    "checked_count": 5,
    "unchecked_count": 0
  },
  "project_type": "vscode-extension",
  "notes": null,
  "created_at": "2026-02-01T13:15:00Z"
}
```

| Field                                      | Type   | Required | Description                                                                      |
| ------------------------------------------ | ------ | -------- | -------------------------------------------------------------------------------- |
| `schema_version`                           | string | Yes      | Schema version for compatibility (1.0–1.5 or 1.9)                                |
| `issue_number`                             | number | Yes      | GitHub issue number                                                              |
| `commit_sha`                               | string | Yes      | Git commit SHA after validated code is committed and pushed (v1.9+, issue #1608) |
| `validation_status`                        | string | Yes      | Overall status: passed, failed, partial, skipped                                 |
| `build`                                    | object | No       | Build verification results (v1.1+)                                               |
| `build.ran`                                | bool   | Yes      | Whether build was run                                                            |
| `build.passed`                             | bool   | Yes      | Whether build passed                                                             |
| `build.command`                            | string | No       | Build command used (null if not detected)                                        |
| `integration_tests`                        | object | No       | Integration test results                                                         |
| `integration_tests.ran`                    | bool   | Yes      | Whether integration tests were run                                               |
| `integration_tests.passed`                 | bool   | Yes      | Whether tests passed                                                             |
| `integration_tests.framework`              | string | No       | Test framework used                                                              |
| `integration_tests.tests_run`              | number | No       | Number of tests run                                                              |
| `unit_tests`                               | object | No       | Unit test results (v1.2+, same shape as integration_tests)                       |
| `e2e_tests`                                | object | No       | E2E test results                                                                 |
| `e2e_tests.ran`                            | bool   | Yes      | Whether E2E tests were run                                                       |
| `e2e_tests.passed`                         | bool   | Yes      | Whether tests passed                                                             |
| `e2e_tests.framework`                      | string | No       | E2E framework (playwright, cypress)                                              |
| `e2e_tests.reason`                         | string | No       | Reason if not run (e.g., "not configured")                                       |
| `dead_code_warnings`                       | array  | No       | Dead code findings from Phase 1.6 (v1.2+)                                        |
| `dead_code_warnings[].type`                | string | Yes      | Detection category (unused-export, unregistered-command, missing-arg-validation) |
| `dead_code_warnings[].name`                | string | Yes      | Identifier name                                                                  |
| `dead_code_warnings[].location`            | string | Yes      | File path and line (e.g., "src/utils.ts:42")                                     |
| `dead_code_warnings[].severity`            | string | Yes      | `"error"` (current-issue, blocking) or `"warning"` (pre-existing, non-blocking)  |
| `preexisting_failures`                     | array  | No       | Pre-existing test failures detected via baseline comparison (v1.3+)              |
| `preexisting_failures[].test_file`         | string | Yes      | Path to the failing test file                                                    |
| `preexisting_failures[].failure_count`     | number | Yes      | Number of failures in this test file                                             |
| `preexisting_failures[].baseline_verified` | bool   | Yes      | Whether the failure was verified against main via baseline comparison            |
| `skipped_phases`                           | array  | No       | Phases skipped due to redundancy with feature-dev (v1.4+)                        |
| `skipped_phases[].phase`                   | string | Yes      | Phase name (e.g., "build_verification", "unit_tests", "baseline_comparison")     |
| `skipped_phases[].reason`                  | string | Yes      | Human-readable reason for skipping                                               |
| `ac_completion_check`                      | object | No       | AC checkbox verification for type:docs issues (v1.5+)                            |
| `ac_completion_check.applicable`           | bool   | Yes      | Whether this check applies (true only for type:docs issues)                      |
| `ac_completion_check.status`               | string | Yes      | `"passed"`, `"failed"`, or `"skipped"` (skipped when not applicable)             |
| `ac_completion_check.checked_count`        | number | No       | Number of checked `- [x]` boxes in the issue body                                |
| `ac_completion_check.unchecked_count`      | number | No       | Number of unchecked `- [ ]` boxes in the issue body                              |
| `manual_checklist`                         | array  | Yes      | Manual verification items                                                        |
| `manual_checklist[].item`                  | string | Yes      | Checklist item description                                                       |
| `manual_checklist[].verified`              | bool   | Yes      | Whether item was verified                                                        |
| `project_type`                             | string | Yes      | Detected project type                                                            |
| `notes`                                    | string | No       | Additional notes                                                                 |
| `created_at`                               | string | Yes      | ISO 8601 timestamp                                                               |

### pr-{N}.json

**Created by**: `/nightgauge-pr-create` **Read by**:
`/nightgauge-pr-merge`

```json
{
  "schema_version": "1.0",
  "issue_number": 42,
  "pr_number": 87,
  "pr_url": "https://github.com/org/repo/pull/87",
  "title": "[FEAT][#42] Add user photo upload",
  "base_branch": "main",
  "status": "open",
  "reviewers": ["@teammate"],
  "knowledge_path": ".nightgauge/knowledge/features/42-photo-upload",
  "preflight_results": {
    "json_validation": "passed",
    "yaml_validation": "passed",
    "version_consistency": "passed",
    "security_scan": "passed",
    "coverage_check": "passed"
  },
  "ci_monitoring": {
    "monitored": true,
    "monitor_duration_secs": 90,
    "final_status": "success",
    "checks_summary": {
      "total": 4,
      "passed": 4,
      "failed": 0,
      "pending": 0
    },
    "failures": [],
    "timestamp": "2026-02-01T13:31:30Z",
    "notes": ""
  },
  "retrospective_feedback": {
    "what_went_well": ["Smooth execution — no blockers"],
    "what_could_improve": ["Clearer requirements — more detail needed"],
    "captured_at": "2026-02-01T13:35:00Z",
    "execution_mode": "interactive"
  },
  "created_at": "2026-02-01T13:30:00Z"
}
```

| Field                                       | Type   | Required | Description                                                                                    |
| ------------------------------------------- | ------ | -------- | ---------------------------------------------------------------------------------------------- |
| `schema_version`                            | string | Yes      | Schema version for compatibility                                                               |
| `issue_number`                              | number | Yes      | GitHub issue number                                                                            |
| `pr_number`                                 | number | Yes      | Pull request number                                                                            |
| `pr_url`                                    | string | Yes      | Full PR URL                                                                                    |
| `title`                                     | string | Yes      | PR title                                                                                       |
| `base_branch`                               | string | Yes      | Target branch                                                                                  |
| `status`                                    | string | Yes      | PR status: `open`, `draft`                                                                     |
| `reviewers`                                 | array  | Yes      | Requested reviewers                                                                            |
| `knowledge_path`                            | string | No       | Path to knowledge directory for this issue (v1.1+)                                             |
| `preflight_results`                         | object | Yes      | Pre-flight check results from Phase 2 and Phase 2.5                                            |
| `preflight_results.json_validation`         | string | Yes      | JSON context file validation: `passed`, `failed`, `skipped`                                    |
| `preflight_results.yaml_validation`         | string | Yes      | YAML config validation: `passed`, `failed`, `skipped`                                          |
| `preflight_results.version_consistency`     | string | Yes      | Package version consistency check: `passed`, `failed`, `skipped`                               |
| `preflight_results.security_scan`           | string | Yes      | Secret detection scan (gitleaks + grep fallback): `passed`, `failed`, `skipped`                |
| `preflight_results.coverage_check`          | string | Yes      | Test coverage threshold check: `passed`, `failed`, `skipped`                                   |
| `ci_monitoring`                             | object | Yes      | CI check monitoring results from Phase 3.5 (v1.2+)                                             |
| `ci_monitoring.monitored`                   | bool   | Yes      | Whether CI monitoring ran (false when PR number unavailable)                                   |
| `ci_monitoring.monitor_duration_secs`       | number | Yes      | Elapsed seconds from PR creation to final CI state                                             |
| `ci_monitoring.final_status`                | string | Yes      | `success`, `failure`, `error`, `timeout`, `pending`, `unknown`                                 |
| `ci_monitoring.checks_summary`              | object | Yes      | Aggregated check counts                                                                        |
| `ci_monitoring.checks_summary.total`        | number | Yes      | Total CI checks triggered                                                                      |
| `ci_monitoring.checks_summary.passed`       | number | Yes      | Checks that completed successfully                                                             |
| `ci_monitoring.checks_summary.failed`       | number | Yes      | Checks that completed with failure or error                                                    |
| `ci_monitoring.checks_summary.pending`      | number | Yes      | Checks still pending at monitor end                                                            |
| `ci_monitoring.failures`                    | array  | Yes      | Classified failure objects (empty when no failures)                                            |
| `ci_monitoring.failures[].name`             | string | Yes      | CI check name                                                                                  |
| `ci_monitoring.failures[].failure_type`     | string | Yes      | `lint`, `test`, `build`, `typecheck`, `security`, `format`, `unknown`                          |
| `ci_monitoring.failures[].is_transient`     | bool   | Yes      | Whether failure matches transient patterns (timed_out, network errors)                         |
| `ci_monitoring.failures[].conclusion`       | string | Yes      | GitHub check conclusion (`failure`, `timed_out`, `action_required`)                            |
| `ci_monitoring.failures[].details_url`      | string | No       | Link to CI logs for the failed check                                                           |
| `ci_monitoring.timestamp`                   | string | No       | ISO 8601 timestamp when monitoring completed (null if not monitored)                           |
| `ci_monitoring.notes`                       | string | No       | Human-readable note (e.g., quick-fix instructions for lint/format failures)                    |
| `retrospective_feedback`                    | object | No       | Post-merge workflow feedback captured after PR merge (v1.2+, Issue #14). Null if not captured. |
| `retrospective_feedback.what_went_well`     | array  | No       | User-selected or typed items describing what worked well in the workflow                       |
| `retrospective_feedback.what_could_improve` | array  | No       | User-selected or typed items describing areas for workflow improvement                         |
| `retrospective_feedback.captured_at`        | string | Yes      | ISO 8601 timestamp when feedback was captured                                                  |
| `retrospective_feedback.execution_mode`     | string | Yes      | `interactive` (user was prompted) or `headless` (skipped, no terminal)                         |
| `issue_closed`                              | bool   | No       | Whether issue close was called (written by `/pr-merge`, v1.5+, issue #2933)                    |
| `issue_closed_verified`                     | bool   | No       | Whether issue close was verified within 10s retry window (v1.5+, issue #2933)                  |
| `created_at`                                | string | Yes      | ISO 8601 timestamp                                                                             |

### Batch Context Schemas

Batch context files carry requirements, plans, and results for multiple issues
processed together as part of an epic. They live alongside single-issue context
files in `.nightgauge/pipeline/` and use the **epic number** as the file
key (e.g., `batch-799.json`).

> **Note**: Batch context is additive — existing single-issue pipeline paths
> remain unchanged. See Issue #801.

#### batch-{E}.json

**Created by**: `/nightgauge-issue-pickup` (batch mode) **Read by**:
`/nightgauge-feature-planning` (batch mode)

**Schema Version**: 1.0

```json
{
  "schema_version": "1.0",
  "epic_number": 799,
  "issue_numbers": [801, 802, 803],
  "batch_strategy": "batch",
  "branch": "feat/799-epic-batch-pipeline",
  "base_branch": "main",
  "issues": [
    {
      "issue_number": 801,
      "title": "Multi-issue context schema",
      "type": "feature",
      "requirements": {
        "summary": "Add batch context schemas for batched pipeline runs",
        "acceptance_criteria": ["Zod schemas for batch context files"]
      },
      "labels": ["type:feature", "size:M"],
      "size_label": "M"
    }
  ],
  "shared_files": ["packages/sdk/src/context/ContextManager.ts"],
  "groups": [
    {
      "issue_numbers": [801, 802],
      "group_reason": "High file overlap (65.0%) enables batching",
      "shared_files": ["packages/sdk/src/context/ContextManager.ts"],
      "estimated_tokens": 120000
    }
  ],
  "created_at": "2026-02-15T12:00:00Z"
}
```

| Field                       | Type   | Required | Description                                         |
| --------------------------- | ------ | -------- | --------------------------------------------------- |
| `schema_version`            | string | Yes      | Schema version (`1.0`)                              |
| `epic_number`               | number | Yes      | Epic issue number (file key)                        |
| `issue_numbers`             | array  | Yes      | All issue numbers in this batch                     |
| `batch_strategy`            | string | Yes      | Strategy: `batch`, `sequential`, or `hybrid`        |
| `branch`                    | string | Yes      | Shared feature branch                               |
| `base_branch`               | string | Yes      | Target branch for PR                                |
| `issues`                    | array  | Yes      | Per-issue requirements (embedded inline)            |
| `issues[].issue_number`     | number | Yes      | Issue number                                        |
| `issues[].title`            | string | Yes      | Issue title                                         |
| `issues[].type`             | string | Yes      | Issue type: feature, bug, docs, refactor, spike     |
| `issues[].requirements`     | object | Yes      | Extracted requirements (same shape as single-issue) |
| `issues[].labels`           | array  | Yes      | GitHub labels                                       |
| `issues[].size_label`       | string | No       | Size label: XS, S, M, L, XL                         |
| `shared_files`              | array  | Yes      | Files mentioned across multiple issues              |
| `groups`                    | array  | Yes      | Issue groups from EpicBatchAssessor                 |
| `groups[].issue_numbers`    | array  | Yes      | Issue numbers in this group                         |
| `groups[].group_reason`     | string | Yes      | Why these issues are grouped                        |
| `groups[].shared_files`     | array  | Yes      | Files shared within the group                       |
| `groups[].estimated_tokens` | number | Yes      | Token estimate for the group                        |
| `created_at`                | string | Yes      | ISO 8601 timestamp                                  |

#### planning-batch-{E}.json

**Created by**: `/nightgauge-feature-planning` (batch mode) **Read by**:
`/nightgauge-feature-dev` (batch mode)

**Schema Version**: 1.0

```json
{
  "schema_version": "1.0",
  "epic_number": 799,
  "issue_numbers": [801, 802],
  "plan_file": ".nightgauge/plans/799-epic-batch-pipeline.md",
  "approach": "batch",
  "per_issue_plans": [
    {
      "issue_number": 801,
      "files_to_create": ["src/context/schemas/batch.ts"],
      "files_to_modify": ["src/context/schemas/index.ts"]
    }
  ],
  "shared_files_to_modify": ["src/context/ContextManager.ts"],
  "files_to_read": ["src/context/schemas/issue.ts"],
  "decisions": [
    {
      "topic": "Batch Key Identifier",
      "options": ["issue_number", "epic_number"],
      "selection": "epic_number",
      "rationale": "Batches are scoped to epics"
    }
  ],
  "created_at": "2026-02-15T12:30:00Z"
}
```

| Field                               | Type   | Required | Description                                          |
| ----------------------------------- | ------ | -------- | ---------------------------------------------------- |
| `schema_version`                    | string | Yes      | Schema version (`1.0`)                               |
| `epic_number`                       | number | Yes      | Epic issue number                                    |
| `issue_numbers`                     | array  | Yes      | All issue numbers in this batch                      |
| `plan_file`                         | string | Yes      | Path to the batch plan .md file                      |
| `approach`                          | string | Yes      | Implementation approach (e.g., `batch`)              |
| `per_issue_plans`                   | array  | Yes      | Per-issue file change plans                          |
| `per_issue_plans[].issue_number`    | number | Yes      | Issue number                                         |
| `per_issue_plans[].files_to_create` | array  | Yes      | New files for this issue                             |
| `per_issue_plans[].files_to_modify` | array  | Yes      | Modified files for this issue                        |
| `shared_files_to_modify`            | array  | Yes      | Files changed by multiple issues                     |
| `files_to_read`                     | array  | Yes      | Files to pre-load for implementation context         |
| `decisions`                         | array  | Yes      | Architectural decisions (same shape as single-issue) |
| `created_at`                        | string | Yes      | ISO 8601 timestamp                                   |

#### dev-batch-{E}.json

**Created by**: `/nightgauge-feature-dev` (batch mode) **Read by**:
`/nightgauge-pr-create` (batch mode)

**Schema Version**: 1.0

```json
{
  "schema_version": "1.0",
  "epic_number": 799,
  "issue_numbers": [801, 802],
  "commit_sha": "abc123def456",
  "per_issue_results": [
    {
      "issue_number": 801,
      "files_changed": {
        "created": ["src/context/schemas/batch.ts"],
        "modified": ["src/context/schemas/index.ts"],
        "deleted": []
      }
    }
  ],
  "tests_status": {
    "passed": 25,
    "failed": 0,
    "coverage": 92.5
  },
  "quality_checks": {
    "code_standards": "passed",
    "security_review": "passed"
  },
  "created_at": "2026-02-15T13:00:00Z"
}
```

| Field                                        | Type   | Required | Description                     |
| -------------------------------------------- | ------ | -------- | ------------------------------- |
| `schema_version`                             | string | Yes      | Schema version (`1.0`)          |
| `epic_number`                                | number | Yes      | Epic issue number               |
| `issue_numbers`                              | array  | Yes      | All issue numbers in this batch |
| `commit_sha`                                 | string | Yes      | Git commit SHA                  |
| `per_issue_results`                          | array  | Yes      | Per-issue file change results   |
| `per_issue_results[].issue_number`           | number | Yes      | Issue number                    |
| `per_issue_results[].files_changed`          | object | Yes      | Files changed for this issue    |
| `per_issue_results[].files_changed.created`  | array  | Yes      | New files created               |
| `per_issue_results[].files_changed.modified` | array  | Yes      | Files modified                  |
| `per_issue_results[].files_changed.deleted`  | array  | Yes      | Files deleted                   |
| `tests_status`                               | object | Yes      | Test results (aggregated)       |
| `tests_status.passed`                        | number | Yes      | Passing test count              |
| `tests_status.failed`                        | number | Yes      | Failing test count              |
| `tests_status.coverage`                      | number | No       | Coverage percentage             |
| `quality_checks`                             | object | Yes      | Review results                  |
| `created_at`                                 | string | Yes      | ISO 8601 timestamp              |

### Epic Context Accumulator

Epic context files accumulate codebase discoveries across sub-issues within an
epic. When multiple sub-issues run through the pipeline, each starts cold and
must re-discover the same codebase independently. The epic context file solves
this by recording findings from each completed sub-issue so later issues can
read them and skip redundant research.

Unlike batch context (which groups multiple issues into a single pipeline run),
epic context is **persistent across independent pipeline runs** and is
incrementally appended after each sub-issue completes.

**Forward injection (#4096).** The accumulator only _wrote_ this file until
#4096 — nothing read it back into a downstream prompt, so the project-memory
loop was open. Now the Go scheduler, when a sub-issue carries a `ParentNumber`
(set by the wave orchestrator), appends a **bounded, clearly-delimited**
summary of the accumulated `relevant_files` and sibling findings to that
sub-issue's **feature-planning** and **feature-dev** prompts
(`renderEpicContextForPrompt`). The block is labelled **SEMI-TRUSTED** —
sibling findings are influenced by issue/agent text, so a downstream
LLM-as-judge (#4097) must treat it as background, not instructions. Bounds:
≤25 files, ≤12 notes, ≤2.5 KB. Non-epic work and wave-0 sub-issues (no
accumulated context yet) get a byte-identical prompt (the renderer returns "").

> **Note**: Epic context files are NOT cleaned up by `/pr-merge` — they persist
> for the lifetime of the epic. See Issue #2404.

#### epic-context-{E}.json

**Created by**: Wave orchestrator (after each sub-issue completes)
**Read by**: Pipeline stages of subsequent sub-issues

**Schema Version**: 1.0

```json
{
  "schema_version": "1.0",
  "epic_number": 100,
  "last_updated": "2026-03-24T02:00:00Z",
  "sub_issue_findings": {
    "42": {
      "files_touched": ["src/context/ContextManager.ts"],
      "decisions": ["Use Zod for schema validation"],
      "discoveries": ["ContextManager uses atomic writes via temp + rename"],
      "patterns": ["All schemas export both Schema and Type from schemas/"],
      "recorded_at": "2026-03-24T01:00:00Z"
    },
    "43": {
      "files_touched": ["src/context/schemas/epic-context.ts"],
      "decisions": [],
      "discoveries": [],
      "patterns": [],
      "recorded_at": "2026-03-24T02:00:00Z"
    }
  },
  "shared_research": {
    "codebase_notes": ["Monorepo with npm workspaces"],
    "architecture_notes": ["Three-layer architecture: skills, SDK, extension"],
    "relevant_files": ["src/context/ContextManager.ts", "src/context/schemas/epic-context.ts"]
  }
}
```

| Field                                 | Type   | Required | Description                                 |
| ------------------------------------- | ------ | -------- | ------------------------------------------- |
| `schema_version`                      | string | Yes      | Schema version (`1.0`)                      |
| `epic_number`                         | number | Yes      | Epic issue number                           |
| `last_updated`                        | string | Yes      | ISO 8601 timestamp of last update           |
| `sub_issue_findings`                  | object | Yes      | Keyed by issue number (string)              |
| `sub_issue_findings[N].files_touched` | array  | Yes      | Files created or modified by sub-issue      |
| `sub_issue_findings[N].decisions`     | array  | Yes      | Key decisions made during execution         |
| `sub_issue_findings[N].discoveries`   | array  | Yes      | Codebase discoveries for sibling sub-issues |
| `sub_issue_findings[N].patterns`      | array  | Yes      | Architecture patterns identified            |
| `sub_issue_findings[N].recorded_at`   | string | Yes      | ISO 8601 timestamp                          |
| `shared_research`                     | object | Yes      | Aggregated research across all sub-issues   |
| `shared_research.codebase_notes`      | array  | Yes      | Codebase structure notes                    |
| `shared_research.architecture_notes`  | array  | Yes      | Architecture observations                   |
| `shared_research.relevant_files`      | array  | Yes      | Deduplicated file paths from all sub-issues |

### Creation Manifest

The creation manifest is a separate context lifecycle from the
`issue-pickup → pr-merge` pipeline. It is written by every issue-creation
flow and consumed by the `nightgauge-issue-audit` skill as the
strict-mode contract that gates pipeline pickup. See
[docs/ISSUE_AUDIT.md](ISSUE_AUDIT.md) for the full finding taxonomy and
severity rules; the schema lives in
`packages/nightgauge-sdk/src/context/schemas/creation-manifest.ts`.

#### issue-create-manifest-{timestamp}.json

**Created by**: `/nightgauge:issue-create` Phase 4.9 (and any future
creation flow that emits the same shape).
**Read by**: `/nightgauge:issue-audit --manifest <path>` (Phase 6
terminal pass).

**Schema Version**: 1.0

```
GitHub Issue Creation
       │
       ▼
┌──────────────────────┐     ┌──────────────────────────────────┐
│  /issue-create       │────▶│ issue-create-manifest-<ts>.json  │
│  (Phase 4.9)         │     │ • per-issue declarations         │
└──────────────────────┘     └─────────────┬────────────────────┘
                                            │
                                            ▼
┌──────────────────────┐     ┌──────────────────────────────────┐
│  /issue-audit        │◀────│ Read manifest                    │
│  (terminal gate)     │     │ • verify board, body, links,     │
│                      │     │   blockedBy, cross-repo, knowledge│
└──────────────────────┘     └──────────────────────────────────┘
                                            │
                                            ▼
                              ┌──────────────────────────────────┐
                              │ issue-audit-<ts>.{md,json}       │
                              │ + audit.jsonl trail              │
                              └──────────────────────────────────┘
```

```json
{
  "schema_version": "1.0",
  "created_at": "2026-05-06T20:30:00.000Z",
  "created_by_skill": "nightgauge-issue-create",
  "project_number": 1,
  "entries": [
    {
      "repo": "nightgauge/nightgauge",
      "number": 3237,
      "type": "epic",
      "priority": "P1",
      "size": "L",
      "status": "Ready",
      "sub_issues": [3238, 3239],
      "body_sections": ["Summary", "Sub-Issues", "Acceptance Criteria"],
      "component_labels": ["component:skills"],
      "knowledge_path": ".nightgauge/knowledge/features/3237-foo"
    },
    {
      "repo": "nightgauge/nightgauge",
      "number": 3238,
      "type": "feature",
      "priority": "P2",
      "size": "S",
      "status": "Backlog",
      "parent_epic": "nightgauge/nightgauge#3237",
      "blocked_by": [{ "number": 3237 }],
      "body_sections": ["Summary", "Acceptance Criteria"]
    }
  ]
}
```

| Field              | Type   | Required | Description                                                              |
| ------------------ | ------ | -------- | ------------------------------------------------------------------------ |
| `schema_version`   | string | Yes      | Pinned to `"1.0"` for the initial release                                |
| `created_at`       | string | Yes      | ISO 8601 timestamp                                                       |
| `created_by_skill` | string | Yes      | e.g. `nightgauge-issue-create`                                           |
| `project_number`   | number | No       | Single-repo flows; multi-repo flows omit (per-entry mapping is implicit) |
| `entries`          | array  | Yes      | One entry per issue created (epic + every sub-issue + standalone issues) |

Per-entry fields (`CreationManifestEntrySchema`):

| Field              | Type   | Required | Description                                                                  |
| ------------------ | ------ | -------- | ---------------------------------------------------------------------------- |
| `repo`             | string | Yes      | `owner/repo` slug                                                            |
| `number`           | number | Yes      | GitHub issue number                                                          |
| `type`             | string | Yes      | `feature \| bug \| docs \| refactor \| spike \| chore \| epic`               |
| `priority`         | string | Yes      | `P0 \| P1 \| P2 \| P3`                                                       |
| `size`             | string | Yes      | `XS \| S \| M \| L \| XL`                                                    |
| `status`           | string | Yes      | `Backlog \| Ready \| In progress`                                            |
| `parent_epic`      | string | No       | Sub-issues only — `<owner>/<repo>#<n>` or `#<n>` shorthand                   |
| `sub_issues`       | array  | No       | Epic only — declared sub-issue numbers                                       |
| `blocked_by`       | array  | No       | Array of `{ number, repo? }` — same-repo blockers omit `repo`                |
| `body_sections`    | array  | No       | Required body headings (e.g. `["Summary", "Acceptance Criteria"]`)           |
| `component_labels` | array  | No       | `component:*` labels attached at creation time                               |
| `knowledge_path`   | string | No       | Path to scaffolded knowledge directory (when `--with-knowledge` is used)     |
| `spike_artifact`   | object | No       | `{ path, exists }` — required for `type: spike` per `docs/SPIKE_CONTRACT.md` |

**Lifecycle**:

1. `/nightgauge:issue-create` Phase 4.9 writes the manifest after
   every issue + relationship is created and Phase 4.8's cross-repo audit
   passes.
2. `/nightgauge:issue-create` Phase 6 invokes
   `/nightgauge:issue-audit --manifest <path>` as the terminal step.
   Audit exit code propagates: NEEDS FIXES (exit 1) blocks the flow from
   reporting success.
3. The manifest is preserved alongside the audit report (it is the
   historical record of intent at creation time). Manifest and audit
   artifacts are cleaned up by `pr-merge` along with other pipeline
   transients.

### state.json

**Created by**: `PipelineStateService` (VSCode extension) **Read by**:
Dashboard, TreeProvider, OutputWindow

This file is the **authoritative state** for the current pipeline run, owned by
the VSCode extension's `PipelineStateService`. Unlike other context files which
are stage-specific handoffs, this file tracks the entire pipeline's progress and
token usage.

**Schema Version**: 1.0 (version 1.1 adds `retry_count` to stage objects)

```json
{
  "schema_version": "1.0",
  "issue_number": 42,
  "title": "Add user photo upload",
  "branch": "feat/42-user-photo-upload",
  "base_branch": "main",
  "started_at": "2026-02-01T12:00:00Z",
  "updated_at": "2026-02-01T13:30:00Z",
  "execution_mode": "automatic",
  "paused": false,
  "stages": {
    "issue-pickup": {
      "status": "complete",
      "started_at": "2026-02-01T12:00:00Z",
      "completed_at": "2026-02-01T12:05:00Z",
      "duration_ms": 300000,
      "retry_count": 1
    },
    "feature-planning": {
      "status": "complete",
      "started_at": "2026-02-01T12:05:00Z",
      "completed_at": "2026-02-01T12:30:00Z",
      "duration_ms": 1500000,
      "retry_count": 1
    },
    "feature-dev": {
      "status": "running",
      "started_at": "2026-02-01T12:30:00Z",
      "retry_count": 2
    },
    "feature-validate": { "status": "pending" },
    "pr-create": { "status": "pending" },
    "pr-merge": { "status": "pending" }
  },
  "tokens": {
    "total_input": 15234,
    "total_output": 8912,
    "total_cache_read": 2100,
    "total_cache_creation": 5000,
    "estimated_cost_usd": 0.12,
    "per_stage": {
      "issue-pickup": {
        "input": 2000,
        "output": 1500,
        "cache_read": 500,
        "cache_creation": 1000,
        "cost_usd": 0.02
      },
      "feature-planning": {
        "input": 8234,
        "output": 5412,
        "cache_read": 1100,
        "cache_creation": 2500,
        "cost_usd": 0.07
      },
      "feature-dev": {
        "input": 5000,
        "output": 2000,
        "cache_read": 500,
        "cache_creation": 1500,
        "cost_usd": 0.03
      }
    }
  }
}
```

| Field                         | Type   | Required | Description                                 |
| ----------------------------- | ------ | -------- | ------------------------------------------- |
| `schema_version`              | string | Yes      | Schema version for compatibility            |
| `issue_number`                | number | Yes      | GitHub issue number                         |
| `title`                       | string | Yes      | Issue title                                 |
| `branch`                      | string | Yes      | Feature branch name                         |
| `base_branch`                 | string | Yes      | Target branch for PR (e.g., main, develop)  |
| `started_at`                  | string | Yes      | ISO 8601 timestamp of pipeline start        |
| `updated_at`                  | string | Yes      | ISO 8601 timestamp of last update           |
| `execution_mode`              | string | No       | 'automatic' or 'manual' (default: 'manual') |
| `paused`                      | bool   | No       | True if pipeline is paused (default: false) |
| `stages`                      | object | Yes      | Status of each pipeline stage               |
| `stages[].status`             | string | Yes      | pending, running, complete, failed, skipped |
| `stages[].started_at`         | string | No       | ISO 8601 timestamp when stage started       |
| `stages[].completed_at`       | string | No       | ISO 8601 timestamp when stage completed     |
| `stages[].duration_ms`        | number | No       | Stage duration in milliseconds              |
| `stages[].error`              | string | No       | Error message if status is failed           |
| `stages[].retry_count`        | number | No       | Number of times stage has been started      |
| `tokens`                      | object | Yes      | Token usage tracking                        |
| `tokens.total_input`          | number | Yes      | Total input tokens across all stages        |
| `tokens.total_output`         | number | Yes      | Total output tokens across all stages       |
| `tokens.total_cache_read`     | number | Yes      | Total cache read tokens                     |
| `tokens.total_cache_creation` | number | Yes      | Total cache creation tokens                 |
| `tokens.estimated_cost_usd`   | number | Yes      | Estimated total cost in USD                 |
| `tokens.per_stage`            | object | No       | Per-stage token breakdown (optional)        |
| `tokens.per_stage[].input`    | number | Yes      | Input tokens for this stage                 |
| `tokens.per_stage[].output`   | number | Yes      | Output tokens for this stage                |
| `tokens.per_stage[].cost_usd` | number | Yes      | Cost in USD for this stage                  |

**Key Differences from Stage Handoff Files:**

| Aspect        | state.json                   | Stage Handoff Files (issue-N.json, etc.) |
| ------------- | ---------------------------- | ---------------------------------------- |
| **Owner**     | PipelineStateService         | Individual pipeline skills               |
| **Purpose**   | Track overall progress/usage | Pass context between stages              |
| **Lifecycle** | Entire pipeline run          | Created by one stage, read by next       |
| **Updates**   | Continuously during run      | Once when stage completes                |
| **Cleanup**   | Cleared after PR merge       | Deleted by pr-merge skill                |

### Stage Transition Guards

The `PipelineStateService` enforces stage transition guards to prevent pipeline
loops and ensure orderly execution. These guards are implemented as
**deterministic validation** (per the architecture pattern in
[ARCHITECTURE.md](ARCHITECTURE.md)).

#### Validation Rules

1. **Issue Number Locking**: Once a pipeline is initialized with an issue
   number, attempting to run a stage for a different issue number is blocked.

2. **Backward Transition Detection**: Moving from a later stage (e.g.,
   `feature-dev`) back to an earlier stage (e.g., `feature-planning`) requires
   explicit user confirmation.

3. **Retry Limit (Circuit Breaker)**: Each stage can only be started a maximum
   of 3 times before requiring pipeline reset. This prevents infinite loops.

#### Validation Result

```typescript
interface StageTransitionResult {
  allowed: boolean;
  requiresConfirmation?: boolean; // True for backward transitions
  confirmationMessage?: string; // User-friendly message
  error?: string; // Error message if blocked
  retryCount?: number; // Current retry count
  maxRetries?: number; // Maximum allowed (3)
}
```

#### How It Works

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     STAGE TRANSITION VALIDATION FLOW                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  Before startStage(nextStage):                                               │
│       │                                                                      │
│       ▼                                                                      │
│  ┌────────────────────────────────────────────┐                             │
│  │ 1. Validate issue number                   │                             │
│  │    - If mismatch: block with error         │                             │
│  └────────────────────────────────────────────┘                             │
│       │                                                                      │
│       ▼                                                                      │
│  ┌────────────────────────────────────────────┐                             │
│  │ 2. Check retry count (circuit breaker)     │                             │
│  │    - At/over limit (3): block with error   │                             │
│  └────────────────────────────────────────────┘                             │
│       │                                                                      │
│       ▼                                                                      │
│  ┌────────────────────────────────────────────┐                             │
│  │ 3. Detect backward transition              │                             │
│  │    - Forward/same: allowed                 │                             │
│  │    - Backward: requiresConfirmation=true   │                             │
│  └────────────────────────────────────────────┘                             │
│       │                                                                      │
│       ▼                                                                      │
│  ┌────────────────────────────────────────────┐                             │
│  │ 4. Return validation result                │                             │
│  │    - UI decides how to handle              │                             │
│  └────────────────────────────────────────────┘                             │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### Implementation Details

The validation logic is split into two files for testability:

- `stageTransitionValidator.ts` - Pure validation functions (no side effects)
- `PipelineStateService.ts` - State management and validation integration

The validator is **deterministic**: given the same state and target stage, it
always returns the same result. This follows the architectural principle of
separating deterministic logic from probabilistic (AI) components.

## Execution Model

### Event Model: One Node Tree

Stage progress is published on a single canonical **node-tree** event model, not
a flat stage-lifecycle event union. The SDK `EventBus` is a `WorkflowEventSink`
carrying the `schemaVersion: 4` `WorkflowEvent` union — `WorkflowRun` →
`WorkflowPhase` → (`SubAgentNode` | `JudgeVerdict`) — where each emission carries
`nodeId` / `parentId`, a monotonic `seq`, and an ISO-8601 `ts`, so a consumer
folds the stream by `(nodeId, max seq)` into the live tree.

The six linear pipeline stages are **expressed on this same tree**: a pipeline
run is a `WorkflowRun` whose stages are first-level `WorkflowPhase` nodes, each
driving a single depth-1 `SubAgentNode` (a single-agent stage is a depth-1
chain). A stage that fans out through the orchestration engine adds more agent /
judge nodes under its phase — same tree, same sink, same UI. The handoff
**context files** described in this document are unchanged and orthogonal to the
event tree: context is the durable stage-to-stage data contract; the node tree is
the live progress stream. See
[docs/WORKFLOW_ORCHESTRATION.md](WORKFLOW_ORCHESTRATION.md) for the full event
contract and the fan-out engine.

### Full Isolation

Each skill enforces full isolation:

1. **Reads** context from previous stage's JSON file (required)
2. **Fails** with helpful error if context file missing
3. **Writes** its own context file for next stage
4. **Uses subagents** with minimal prompts (file paths only)

### Error Messages

When a required context file is missing, skills display helpful errors:

```
Error: Missing context file for issue #42

Expected: .nightgauge/pipeline/issue-42.json
Created by: /nightgauge-issue-pickup

Please run the pipeline in order:
  /nightgauge-issue-pickup 42
  /nightgauge-feature-planning
  /nightgauge-feature-dev
  /nightgauge-pr-create
  /nightgauge-pr-merge
```

### Cleanup

After successful merge, `/nightgauge-pr-merge` removes all context files
via `cleanup-context-files.sh`:

```bash
rm -f .nightgauge/pipeline/issue-{N}.json
rm -f .nightgauge/pipeline/planning-{N}.json
rm -f .nightgauge/pipeline/dev-{N}.json
rm -f .nightgauge/pipeline/validate-{N}.json
# validate-{N}-*.md covers checklist files (e.g. validate-{N}-checklist.md)
find .nightgauge/pipeline -name "validate-{N}-*.md" -delete
rm -f .nightgauge/pipeline/pr-{N}.json
rm -f .nightgauge/plans/{N}-*.md

# Batch context cleanup (when epic completes)
rm -f .nightgauge/pipeline/batch-{E}.json
rm -f .nightgauge/pipeline/planning-batch-{E}.json
rm -f .nightgauge/pipeline/dev-batch-{E}.json
```

**All file patterns handled by `cleanup-context-files.sh`:**

| Pattern                          | Description                                                    |
| -------------------------------- | -------------------------------------------------------------- |
| `pipeline/issue-{N}.json`        | Issue pickup output                                            |
| `pipeline/planning-{N}.json`     | Feature planning output                                        |
| `pipeline/ac-reconcile-{N}.json` | Deterministic AC reconciliation pre-flight (Issue #3003)       |
| `pipeline/dev-{N}.json`          | Feature dev output                                             |
| `pipeline/validate-{N}.json`     | Feature validate output (JSON)                                 |
| `pipeline/validate-{N}-*.md`     | Feature validate checklists (e.g. `validate-{N}-checklist.md`) |
| `pipeline/pr-{N}.json`           | PR create output                                               |
| `pipeline/running-*-{N}.json`    | Stale running signal files (deprecated pattern)                |
| `plans/{N}-*.md`                 | Feature plan documents                                         |

## Pipeline State Management

The pipeline uses **unified state management** via `state.json` to track when
stages are running, complete, or failed. This enables the VS Code extension to
show real-time status in the pipeline sidebar.

### Purpose

When a user runs a pipeline skill, the skill directly updates `state.json` at
the start and completion of each stage. The VS Code extension's
`PipelineStateService` watches this file to update the UI in real-time.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     PIPELINE STATE LIFECYCLE                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  User runs /feature-dev                                                      │
│       │                                                                      │
│       ▼                                                                      │
│  ┌──────────────────────────┐                                               │
│  │ update-pipeline-state.sh │──▶ Updates state.json                │
│  │ "42" "feature-dev"       │    status: "running"                          │
│  │ "running"                │    current_stage: "feature-dev"               │
│  └──────────────────────────┘                                               │
│       │                                                                      │
│       │  VS Code detects file change                                         │
│       │  Pipeline sidebar shows "running" state (spinning icon)              │
│       │                                                                      │
│       ▼                                                                      │
│  ┌──────────────────────────┐                                               │
│  │ Skill executes...        │                                               │
│  │ (implementation work)    │                                               │
│  └──────────────────────────┘                                               │
│       │                                                                      │
│       ▼                                                                      │
│  ┌──────────────────────────┐                                               │
│  │ update-pipeline-state.sh │──▶ Updates state.json                │
│  │ "42" "feature-dev"       │    stages.feature-dev.status: "complete"      │
│  │ "complete"               │    stages.feature-dev.completed_at: "..."     │
│  └──────────────────────────┘                                               │
│       │                                                                      │
│       │  VS Code detects file change                                         │
│       │  Pipeline sidebar shows "complete" state (checkmark)                 │
│       ▼                                                                      │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Valid Stage Names

| Stage              | Skill                          | Description                     |
| ------------------ | ------------------------------ | ------------------------------- |
| `issue-pickup`     | `/nightgauge-issue-pickup`     | Issue claim and branch creation |
| `feature-planning` | `/nightgauge-feature-planning` | Design and planning             |
| `feature-dev`      | `/nightgauge-feature-dev`      | Implementation                  |
| `feature-validate` | `/nightgauge-feature-validate` | Integration/E2E testing         |
| `pr-create`        | `/nightgauge-pr-create`        | Pull request creation           |
| `pr-merge`         | `/nightgauge-pr-merge`         | Review and merge                |

### Valid Status Values

| Status     | Description                                                |
| ---------- | ---------------------------------------------------------- |
| `pending`  | Stage has not started yet                                  |
| `running`  | Stage is currently executing                               |
| `complete` | Stage finished successfully                                |
| `failed`   | Stage encountered an error (includes `error` field)        |
| `skipped`  | Stage was skipped (e.g., feature-validate when not needed) |

### Go Binary Command

Skills use the `nightgauge project move-status` Go binary command to signal
stage status:

```bash
# Usage: nightgauge project move-status <issue-number> <board-status>
# Valid statuses: ready, in-progress, in-review, done, blocked, needs-info

# Resolve binary (check local bin/ first, then PATH)
if [ -f "bin/nightgauge" ]; then
  BINARY="bin/nightgauge"
else
  BINARY="nightgauge"
fi

# Signal stage start (board status → in-progress)
"$BINARY" project move-status "42" "in-progress" 2>/dev/null || true

# Signal PR created (board status → in-review)
"$BINARY" project move-status "42" "in-review" 2>/dev/null || true

# Signal pipeline done (board status → done)
"$BINARY" project move-status "42" "done" 2>/dev/null || true
```

**Key features:**

- Atomic writes using temp file + rename
- Creates `state.json` if it doesn't exist
- Validates stage names and status values
- Increments `retry_count` on each `running` transition
- Cross-platform (Go binary, not shell-dependent)

### VS Code Extension Integration

The `PipelineStateService` in the VS Code extension watches `state.json`:

1. **FileSystemWatcher** monitors `.nightgauge/pipeline/state.json`
2. **onStateChanged** event fires when the file is modified
3. **TreeProvider** updates stage status based on `stages[stage].status`
4. **Dashboard** shows real-time progress and token usage
5. **OutputWindow** displays current stage activity

### Deprecated: Running Files

**Note**: The previous `running-{stage}-{N}.json` file pattern is deprecated as
of Issue #89. The legacy scripts `signal-stage-start.sh` and
`signal-stage-complete.sh` now delegate to `update-pipeline-state.sh` for
backwards compatibility, but should not be used in new code

## Skill Input/Output Contracts

| Skill                          | Input                       | Output                         |
| ------------------------------ | --------------------------- | ------------------------------ |
| `/nightgauge-issue-pickup`     | GitHub Issue                | `issue-{N}.json`               |
| `/nightgauge-feature-planning` | `issue-{N}.json`            | `planning-{N}.json` + PLAN.md  |
| `/nightgauge-feature-dev`      | `planning-{N}.json`         | `dev-{N}.json` + code          |
| `/nightgauge-feature-validate` | `dev-{N}.json`              | `validate-{N}.json` (optional) |
| `/nightgauge-pr-create`        | `dev-{N}.json` (+ validate) | `pr-{N}.json` + PR             |
| `/nightgauge-pr-merge`         | `pr-{N}.json`               | Cleanup all context            |

**Batch mode contracts** (Issue #801):

| Skill (batch mode)             | Input                     | Output                                  |
| ------------------------------ | ------------------------- | --------------------------------------- |
| `/nightgauge-issue-pickup`     | GitHub Epic + sub-issues  | `batch-{E}.json`                        |
| `/nightgauge-feature-planning` | `batch-{E}.json`          | `planning-batch-{E}.json` + PLAN.md     |
| `/nightgauge-feature-dev`      | `planning-batch-{E}.json` | `dev-batch-{E}.json` + code             |
| `/nightgauge-pr-create`        | `dev-batch-{E}.json`      | `pr-{N}.json` + PR (multi-issue Closes) |

## Schema Versioning

All context files include `schema_version` to enable future evolution:

- **1.0**: Initial schema
- **1.1**: Added `decisions` array to `planning-{N}.json` for capturing
  architectural decisions made during planning (issue #36)
- **1.1**: Added `routing` object to `issue-{N}.json` for complexity-based stage
  routing (issue #216)
- **1.3**: Added `dependencies` object to `issue-{N}.json` for dependency
  tracking enforcement (issue #253). Note: v1.2 was skipped — dependencies and
  task_type both shipped under v1.3.
- **1.3**: Added `task_type` field to `routing` object and expanded
  `skip_stages` to include `pr-create` and `pr-merge` for task-type routing
  (issue #268)
- **1.3**: Added `child_issues`, `sub_issue_progress`, and `native_parent`
  fields to `issue-{N}.json` for GitHub native sub-issues integration (issue
  #38)
- **1.4** (issue): Added `spike` issue type to `issue-{N}.json` for
  research/investigation tasks (issue #168)
- **1.1** (validate): Added `build` object to `validate-{N}.json` for build
  verification results
- **1.2** (validate): Added `dead_code_warnings` array and `unit_tests` object
  to `validate-{N}.json` for dead code gating (issue #719)
- **1.3** (validate): Added `preexisting_failures` array to `validate-{N}.json`
  for baseline comparison of test failures (issue #836)
- **1.4** (validate): Added `skipped_phases` array to `validate-{N}.json` for
  documenting phases skipped due to redundancy with feature-dev (issue #861)
- **1.5** (validate): Added `ac_completion_check` object to `validate-{N}.json`
  for tracking AC checkbox completion on type:docs issues (issue #1274)
- **1.1** (planning): Added optional `files_to_read` array to
  `planning-{N}.json` for pre-loading implementation context (issue #773)
- **1.7** (dev): Added `e2e_framework` and `e2e_tests_generated` to
  `tests_status` in `dev-{N}.json` for E2E test workflow tracking (Issue #9)
- **1.1** (dev): Added `build_verification` object, extended `tests_status` with
  `test_command`, `includes_integration`, `includes_e2e`, `test_files_run`, and
  extended `quality_checks` with `type_check` and `dead_code_scan` to
  `dev-{N}.json` for richer validate handoff (issue #867)
- **1.0** (batch): Initial `batch-{E}.json` schema for batched issue-pickup
  output (issue #801)
- **1.0** (planning-batch): Initial `planning-batch-{E}.json` schema for batched
  feature-planning output (issue #801)
- **1.0** (dev-batch): Initial `dev-batch-{E}.json` schema for batched
  feature-dev output (issue #801)
- **1.0** (epic-context): Initial `epic-context-{E}.json` schema for
  accumulating cross-sub-issue findings within an epic (issue #2404)
- **1.5** (planning): Added optional `pattern_mining_results` object to
  `planning-{N}.json` for codebase pattern mining results from pattern mining subagent
  (issue #20)
- **1.6** (planning): Added optional `ac_reconcile` object to `planning-{N}.json`
  embedding the deterministic AC reconciliation report (issue #3003)
- **1.0** (ac-reconcile): Initial `ac-reconcile-{N}.json` schema for the
  deterministic AC reconciliation pre-flight (issue #3003)
- Future versions will maintain backward compatibility where possible
- Skills should check schema_version and handle unknown versions gracefully

### Schema Migration

The `HeadlessOrchestrator` performs **lazy, in-memory migration** of context
files when loading routing decisions. This approach:

- **Does not write to disk** — Context files are transient (deleted after PR
  merge)
- **Handles unknown versions gracefully** — Returns null for unrecognized
  schemas
- **Provides backward compatibility** — v1.0/v1.1/v1.3 files work seamlessly

#### Migration Behavior

| Source Version | Migration Action                                              |
| -------------- | ------------------------------------------------------------- |
| v1.0           | No routing field - returns context without routing            |
| v1.1           | Adds `task_type: 'feature'` default to routing                |
| v1.3           | Adds default values for v1.4 fields (spike type support)      |
| v1.4           | Current schema - returned as-is                               |
| Unknown        | Returns null - graceful degradation, pipeline runs all stages |

#### Implementation

```typescript
// HeadlessOrchestrator.migrateContextSchema()
// Called when loading routing decision after issue-pickup

const rawContext = JSON.parse(content);
const issueContext = this.migrateContextSchema(rawContext);

if (!issueContext) {
  // Unknown schema - run full pipeline as fallback
  return null;
}
```

This follows the **lazy migration on read** pattern selected in issue #418,
which avoids file write operations while maintaining full backward
compatibility.

@see Issue #418 - Schema migration for pipeline routing

## Model Selection Decision Flow

When a pipeline stage executes, `skillRunner.ts` resolves the AI model to use
via `resolveModel()`. The decision is captured in a `ModelDecision` object and
attached to `SkillRunResult.modelDecision` so downstream consumers (Dashboard,
execution history) can observe which model was used and why.

### ModelDecision Structure

```typescript
interface ModelDecision {
  model: "haiku" | "sonnet" | "opus";
  source: "env" | "config" | "auto" | "default";
  /** Present when effort resolves from env/config/auto rules */
  effort?: "low" | "medium" | "high";
  /** Present when source is 'auto' */
  selectionResult?: ModelSelectionResult;
}
```

The `source` field indicates where the model came from in the resolution chain:

| Source          | Meaning                                                     |
| --------------- | ----------------------------------------------------------- |
| `env`           | Environment variable `NIGHTGAUGE_PIPELINE_STAGE_MODEL_*`    |
| `config`        | Explicit `pipeline.stage_models.<stage>` in config          |
| `auto`          | AutoModelSelector chose the model based on issue complexity |
| `stage-default` | Built-in default for lightweight stages (haiku)             |
| `default`       | Global default or hardcoded fallback (`sonnet`)             |

**Resolution chain** (evaluated top-to-bottom, first match wins):

```
1.   Environment variable       NIGHTGAUGE_PIPELINE_STAGE_MODEL_*  → highest priority
1.5. LIGHTWEIGHT_STAGE_DEFAULTS built-in per-stage defaults for lightweight stages
                                (issue-pickup, pr-create, pr-merge → haiku)
2.   Config stage override      pipeline.stage_models.<stage>           → mode-aware
3.   AutoModelSelector          complexity × stage matrix               → automatic/hybrid only
4.   Global default             pipeline.default_model                  → fallback
5.   Hardcoded fallback         'sonnet'                                → final safety net
```

### Issue Metadata Flow

Issue metadata is passed to the model selection system during pipeline
execution. The `HeadlessOrchestrator` caches issue metadata (labels, title,
size) from `issue-{N}.json` and passes it to `runStageSkillHeadless()` for each
stage:

```text
issue-{N}.json
    ↓
HeadlessOrchestrator (caches metadata)
    ↓
runStageSkillHeadless(stage, issueNumber, callbacks, issueMetadata)
    ↓
resolveModel(stage, workspaceRoot, issueMetadata)
    ↓
AutoModelSelector.selectModel(stage, metadata)
    ↓
ModelDecision { model, source, selectionResult }
    ↓
SkillRunResult.modelDecision
```

### Decision Logging

Model decisions are logged to the pipeline output window during execution:

```
[skillRunner] Stage: feature-dev | Model: sonnet (auto) | Effort: medium | Prompt: ~2.4K tokens
[skillRunner] AutoModelSelector: complexity=M, confidence=0.90, reasoning=Complexity M (score 4) from size label → sonnet for feature-dev stage (dev matrix)
```

This logging enables post-run analysis of model selection behavior without
requiring additional tooling.

## Backward Edges & Feedback Signals

Stage agents can emit structured backward signals to the orchestrator via an
optional `feedback` field in `dev-{N}.json` and `validate-{N}.json`, or via a
standalone `feedback-{N}.json` file for cross-stage signals.

### Signal Types

| Signal Type                     | Description                                                                  |
| ------------------------------- | ---------------------------------------------------------------------------- |
| `PLAN_REVISION_NEEDED`          | The plan must be revised before the stage can complete successfully          |
| `SCOPE_DISCOVERED`              | Previously unknown scope was found during implementation or validation       |
| `COMPLEXITY_UNDERESTIMATED`     | The issue is significantly more complex than the plan estimated              |
| `MODEL_ESCALATION_NEEDED`       | Task complexity exceeds current model capability; escalate to a larger model |
| `ACCEPTANCE_CRITERIA_AMBIGUOUS` | One or more acceptance criteria cannot be deterministically verified         |

### Severity Semantics

- **`warning`** — Informational. The orchestrator logs it but takes no automatic
  action. Useful for flagging future-improvement opportunities.
- **`blocking`** — The orchestrator must act (retry, backtrack, or escalate)
  before the pipeline can progress.

### `backtrack_target_stage` Semantics

Most blocking signals include a `backtrack_target_stage` that tells the
orchestrator which upstream stage to re-run. The exception is
`MODEL_ESCALATION_NEEDED`: this signal has `backtrack_target_stage: null`
because the intent is to retry the _same_ stage with a more capable model, not
to revert to a prior stage.

### Where Feedback Appears

| Location                                 | Field / File         | Available Since      |
| ---------------------------------------- | -------------------- | -------------------- |
| `dev-{N}.json`                           | `feedback` (nullish) | DevContext v1.2      |
| `validate-{N}.json`                      | `feedback` (nullish) | ValidateContext v1.6 |
| `.nightgauge/pipeline/feedback-{N}.json` | `signals` array      | FeedbackContext v1.0 |

### Canonical Schema

```
packages/nightgauge-sdk/src/context/schemas/feedback.ts
```

Exports: `PipelineFeedbackSignalTypeSchema`, `PipelineStageSchema`,
`PipelineFeedbackSignalSchema`, `PipelineFeedbackSchema`,
`FeedbackContextSchema`.

All types are re-exported from the barrel:
`packages/nightgauge-sdk/src/context/schemas/index.ts`

### Example Signal (dev-{N}.json)

```json
{
  "schema_version": "1.2",
  "issue_number": 42,
  "feedback": [
    {
      "signal_type": "PLAN_REVISION_NEEDED",
      "emitted_by_stage": "feature-dev",
      "backtrack_target_stage": "feature-planning",
      "rationale": "External OAuth dependency discovered; not in original plan",
      "evidence": ["src/auth.ts requires OAUTH_CLIENT_ID env var"],
      "severity": "blocking",
      "timestamp": "2026-02-26T10:00:00Z"
    }
  ]
}
```

---

## Trace Lifecycle

Every run accumulates a durable **lifecycle decision trace** — one append-only
JSONL per run at `.nightgauge/pipeline/trace/<run_id>.jsonl` capturing every
stage boundary and every decision with structured rationale and rejected
alternatives. The public schema and CLI surface are documented under
`nightgauge trace show|export` in [GO_BINARY.md](GO_BINARY.md).

### Producers

Two writers interleave into the same per-run file:

| Producer | Writer                                      | Captures                                                                                                                                                    |
| -------- | ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `go`     | `internal/trace` (scheduler emitters, #179) | Stage start/exit, model routing (reasoning + alternatives), change-class/fast-track, stage skips, gates, escalations, backtracks, recovery retries, outcome |
| `sdk`    | `TraceRecorder` (`@nightgauge/sdk`, #180)   | Phase-marker transitions with per-phase durations (from `skillRunner`), SDK-orchestrator backtracks with rationale/evidence, operator stage skips           |

`seq` is monotonic per `(run_id, producer)`; consumers order by
`(ts, producer, seq)` — the two producers never coordinate a shared counter.
Both writers are fail-open: a trace write failure never fails a stage.

### run_id resolution

The join key is the run-state `run_id` (UUID v7, `run-state.json`). The
`TraceRecorder` resolves it at open; a per-stage invocation with no run-state
becomes a silent no-op (a per-stage caller must never invent a run id). The
SDK `PipelineOrchestrator` — which owns a whole run — falls back to a locally
generated UUID v7, mirroring the Go scheduler's fallback.

### Survival across cleanup

Backtrack feedback signals land in transient `feedback-{N}.json` context
files that pr-merge cleanup deletes. Their decision content (signal type,
rationale, evidence) is persisted as `backtrack` trace events at
decision time, so the trace survives cleanup; the trace directory itself is
never touched by context cleanup.

### Upload

`TelemetryUploaderService` (VSCode) uploads the `trace` stream alongside the
existing telemetry streams: per-file watermarks, batches of ≤500 events, POST
`/v1/telemetry/pipeline-trace`, gated on the same consent + license auth as
other streams. Re-upload is idempotent server-side via the
`(run_id, producer, seq)` key. With telemetry disabled, nothing uploads and
local capture still works.

## Related Documentation

- [ISSUE_TO_PR_WORKFLOW.md](ISSUE_TO_PR_WORKFLOW.md) - Full pipeline workflow
- [ARCHITECTURE.md](ARCHITECTURE.md) - Repository architecture
- [CONFIGURATION.md — model_routing](CONFIGURATION.md#model_routing) - Model
  routing settings and migration guide
- [skills/README.md](../skills/README.md) - Skill catalog and documentation

## Author

nightgauge
