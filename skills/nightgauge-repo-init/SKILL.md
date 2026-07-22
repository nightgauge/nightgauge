---
name: nightgauge-repo-init
description: Prime a new GitHub repository and project board for the Nightgauge SDLC
  pipeline. Creates standard labels, validates/creates project board fields,
  creates standard project board views (Backlog, Priority board, Team items,
  Roadmap, My items), links the repo to the project, and generates
  .nightgauge/config.yaml with all field IDs. Discovers and reuses existing
  projects (org-preferred) before creating new ones. Validates token scopes
  upfront. Idempotent — safe to re-run at any time. Use once when onboarding a
  fresh repository for the pipeline, or to fill gaps and refresh field IDs.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.3.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Bash AskUserQuestion
---

# Nightgauge Repo Init

> Prime a GitHub repository for the Nightgauge SDLC pipeline

## Description

This skill sets up everything a repository needs to work with the
Nightgauge pipeline: standard labels, GitHub Project board fields,
standard project board views, repo-to-project linking, and
`.nightgauge/config.yaml` with all field IDs pre-populated for pipeline
board status sync.

**Run this once when onboarding a new repository. It is idempotent — safe to
re-run to fill gaps or refresh field IDs.**

## Invocation

| Tool           | Command                              |
| -------------- | ------------------------------------ |
| Claude Code    | `/nightgauge:repo-init` (via plugin) |
| OpenAI Codex   | `$nightgauge-repo-init`              |
| GitHub Copilot | Invoke via Agent Skills              |
| Cursor         | Invoke via Agent Skills              |

## Arguments

| Argument             | Description                                                                            | Default                 |
| -------------------- | -------------------------------------------------------------------------------------- | ----------------------- |
| `--dry-run`          | Preview changes without applying                                                       | `false`                 |
| `--project N`        | GitHub Project number to link — resolved against org and user ownership, org preferred | (from config or prompt) |
| `--skip-docs`        | Skip smart-setup AI documentation phase                                                | `false`                 |
| `--seed-from <path>` | Seed complexity model from another repo's model file                                   | (none)                  |
| `--skip-knowledge`   | Skip knowledge directory scaffolding                                                   | `false`                 |

## Philosophy

All operations are **deterministic** (no LLM inference, no interpretation):

| Operation               | Type          | Rationale                                        |
| ----------------------- | ------------- | ------------------------------------------------ |
| Label existence check   | Deterministic | `nightgauge label list --json` comparison        |
| Label creation          | Deterministic | `nightgauge label create` (idempotent Go binary) |
| Project field discovery | Deterministic | GraphQL query, fixed field names                 |
| Project field creation  | Deterministic | GraphQL mutation with known schemas              |
| Project view creation   | Deterministic | `nightgauge project view-create` (idempotent)    |
| Config file generation  | Deterministic | Templated YAML with discovered IDs               |
| User prompts            | Probabilistic | AI formats questions and summaries               |

## Forge Abstraction

All forge operations route through `nightgauge forge` (and `nightgauge` Go-binary verbs) so the skill works against GitHub today and GitLab through the same abstraction tomorrow. The table below summarizes the call surface.

| Phase | Operation              | Command                                                                        |
| ----- | ---------------------- | ------------------------------------------------------------------------------ |
| 0     | Token scope validation | `nightgauge forge auth status` / `nightgauge auth check`                       |
| 1     | Owner type detection   | `nightgauge forge api users/$OWNER --jq .type`                                 |
| 1     | Project discovery      | `nightgauge forge graphql -f query='query{user{projectsV2...}}'`               |
| 3     | Label existence check  | `nightgauge label list --json`                                                 |
| 3     | Label creation         | `nightgauge label create`                                                      |
| 4     | Field schema query     | `nightgauge forge graphql -f query='...'`                                      |
| 5     | Repository link        | `nightgauge forge graphql -f query='mutation{linkProjectV2ToRepository(...)}'` |
| 5.2   | View creation          | `nightgauge project view-create`                                               |

## Supporting files (load on demand)

This skill follows ADR-010 progressive disclosure: the skeleton below carries
the overview and per-phase Read directives; the heavy procedural detail (bash,
GraphQL, config templates, tables) lives in `_includes/` and is loaded only when
its phase fires.

- `skills/nightgauge-repo-init/_includes/prerequisites.md` — read in Phase 0 (tool checks, account pin, token scopes, argument parsing, repo identity, existing config)
- `skills/nightgauge-repo-init/_includes/project-selection.md` — read in Phase 1 (link check, project discovery, `--project` resolution, prompt, confirm)
- `skills/nightgauge-repo-init/_includes/labels.md` — read in Phases 2 and 3 (component label selection, label setup)
- `skills/nightgauge-repo-init/_includes/board-fields-and-link.md` — read in Phases 4, 5, and 5.2 (field validation, repo link, standard views)
- `skills/nightgauge-repo-init/_includes/workspace-registration.md` — read in Phase 5.5 (multi-repo workspace registration)
- `skills/nightgauge-repo-init/_includes/config-generation.md` — read in Phases 6 and 6.5 (generate config.yaml, verify field IDs)
- `skills/nightgauge-repo-init/_includes/knowledge-and-complexity.md` — read in Phases 6.7 and 6.8 (scaffold knowledge, bootstrap complexity model)
- `skills/nightgauge-repo-init/_includes/summary-and-migration.md` — read in Phases 7 and 7.5 (summary report, native sub-issue migration check)

---

## Workflow

### Phase 0: Prerequisites

<!-- include: ../_shared/PREFLIGHT.md -->

---

Verify tools, pin the active forge account, validate token scopes, parse
arguments, resolve repo identity, and detect any existing config.

> **Read `skills/nightgauge-repo-init/_includes/prerequisites.md` now and follow its instructions before continuing this phase.**

---

### Phase 1: Project Selection

Discover, resolve, and confirm the GitHub Project to use — org-preferred,
reusing an existing link when present. Five ordered steps gate each other.

> **Read `skills/nightgauge-repo-init/_includes/project-selection.md` now and follow its instructions before continuing this phase.**

---

### Phase 2: Component Label Selection

Ask which component label set to create (the only project-specific label group).

> **Read `skills/nightgauge-repo-init/_includes/labels.md` now and follow its instructions before continuing this phase.**

---

### Phase 3: Label Setup

Create all standard labels plus the selected component labels, skipping any that
already exist (idempotent).

> **Read `skills/nightgauge-repo-init/_includes/labels.md` now and follow its instructions before continuing this phase.**

---

### Phase 4: Project Board Field Validation

Create or ensure all required project board fields exist via the Go binary's
idempotent `project ensure-fields` verb, returning their field IDs.

> **Read `skills/nightgauge-repo-init/_includes/board-fields-and-link.md` now and follow its instructions before continuing this phase.**

---

### Phase 5: Link Repository to Project

Link the repo to the resolved project (skipping if already linked from the
Phase 1 link check).

> **Read `skills/nightgauge-repo-init/_includes/board-fields-and-link.md` now and follow its instructions before continuing this phase.**

---

### Phase 5.2: Standard Project Board Views

Create the standard set of board/table/roadmap views (Backlog, Priority board,
Team items, Roadmap, My items), skipping any that already exist.

> **Read `skills/nightgauge-repo-init/_includes/board-fields-and-link.md` now and follow its instructions before continuing this phase.**

---

### Phase 5.5: Multi-Repo Workspace Registration

If the repo belongs to an existing multi-repo workspace, register it in the
workspace config (handling N:1 shared-project topology). Standalone repos skip.

> **Read `skills/nightgauge-repo-init/_includes/workspace-registration.md` now and follow its instructions before continuing this phase.**

---

### Phase 5.7: Repository Merge Hygiene

Enable `delete_branch_on_merge` so GitHub removes a PR's head branch on merge —
this pairs with the pipeline's post-merge worktree + local-branch teardown
(#3969) so neither remote nor local merged branches accumulate. Deterministic,
idempotent, safe to re-run:

```bash
nightgauge repo enable-delete-branch --owner "$OWNER" --repo "$REPO" --json
```

(GitHub forge only. Skip on GitLab — the forge adapter doesn't expose this REST
setting; the pipeline's local teardown still applies.)

---

### Phase 6: Generate .nightgauge/config.yaml

Create the pipeline directory structure and `.gitignore`, then generate
`config.yaml` (with all field IDs) via the deterministic `config init` verb.

> **Read `skills/nightgauge-repo-init/_includes/config-generation.md` now and follow its instructions before continuing this phase.**

---

### Phase 6.5: Verify field IDs in config.yaml

Verify `project.id` and `project.fields` are present in `config.yaml` — without
them every pipeline stage skips board status sync.

> **Read `skills/nightgauge-repo-init/_includes/config-generation.md` now and follow its instructions before continuing this phase.**

---

### Phase 6.7: Scaffold Knowledge Directory

Create the knowledge base directory structure (idempotent). Skip if
`--skip-knowledge` was passed.

> **Read `skills/nightgauge-repo-init/_includes/knowledge-and-complexity.md` now and follow its instructions before continuing this phase.**

---

### Phase 6.8: Bootstrap Complexity Model

Create `.nightgauge/complexity-model.yaml` with baseline calibration (or
seed it from another repo via `--seed-from`) if it does not already exist.

> **Read `skills/nightgauge-repo-init/_includes/knowledge-and-complexity.md` now and follow its instructions before continuing this phase.**

---

### Phase 7: Summary Report

Output a clear summary of everything created/verified (labels, board, views,
config, workspace, knowledge), then the recommended next steps.

> **Read `skills/nightgauge-repo-init/_includes/summary-and-migration.md` now and follow its instructions before continuing this phase.**

---

### Phase 7.5: Native Sub-Issue Migration Check

Flag any existing issues using the legacy "Part of #X" body pattern that lack
native GitHub sub-issue links (the pipeline requires native sub-issues).

> **Read `skills/nightgauge-repo-init/_includes/summary-and-migration.md` now and follow its instructions before continuing this phase.**

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

| Error                                          | Cause                                           | Resolution                                                      |
| ---------------------------------------------- | ----------------------------------------------- | --------------------------------------------------------------- |
| `nightgauge: command not found`                | Binary not installed                            | Build with `go build ./cmd/nightgauge` or install via releases  |
| `forge auth status` fails                      | Forge auth not configured                       | Run `nightgauge forge auth login` (or set GITHUB_TOKEN env var) |
| Missing `project` scope                        | Token lacks project scope                       | Run `nightgauge forge auth login --scopes project` then retry   |
| `Could not detect active account`              | Forge auth not configured                       | Run `nightgauge forge auth login`                               |
| `403 Resource not found`                       | Project number wrong, wrong owner, or no access | Verify project number and that active account has org access    |
| Project #N found under both org and user       | Ambiguous --project arg                         | Skill defaults to org project; confirm at Step 1.5              |
| `Field already exists`                         | Duplicate field name                            | Skill detects and skips — not an error                          |
| Missing `jq`                                   | jq not installed                                | `brew install jq` (macOS) / `apt install jq`                    |
| Not in a git repo                              | Not run from a repo directory                   | `cd` to your repository root first                              |
| `nightgauge: command not found` (Phase 0)      | Go binary not installed or not in PATH          | Build with `go build ./cmd/nightgauge` or install via releases  |
| `label list: failed to fetch labels` (Phase 3) | Token missing `repo` scope or network error     | Run `nightgauge forge auth login --scopes repo` then retry      |
| `view-create: project not found` (Phase 5.2)   | Project number or owner mismatch                | Verify `--project` and `--owner` match resolved values          |

---

## Idempotency

This skill is **fully idempotent**:

- **Labels**: Fetches `nightgauge label list --json` once and caches the
  result. Skips creation if name already exists in cache.
- **Project fields**: Checks field names before creating. Skips if present.
- **Repo link**: Checks `repositories` in project before linking. Skips if
  already linked.
- **Config file**: Asks before overwriting. Defaults to "update field IDs only".
- **Field mappings**: Updates `project.fields` in `config.yaml`. Deletes legacy
  `project-field-mappings.json` if found.

Safe to re-run after:

- Adding new repos to the org
- Recreating a project board
- Refreshing config after field IDs change

---

## Integration

### Relationship to Other Skills

| Skill                       | Relationship                                       |
| --------------------------- | -------------------------------------------------- |
| `/nightgauge:smart-setup`   | Run after repo-init to generate AI documentation   |
| `/nightgauge:project-sync`  | Run after repo-init to bulk-sync existing issues   |
| `/nightgauge:issue-pickup`  | Start here once repo-init and smart-setup are done |
| `/nightgauge:backlog-groom` | Useful after initial issue import                  |

### Recommended Onboarding Order

```
1. /nightgauge:repo-init    ← This skill (GitHub infra)
2. /nightgauge:smart-setup                  ← AI documentation
3. /nightgauge:project-sync ← Sync existing issues (if any)
4. /nightgauge:issue-pickup ← Start the pipeline
```

---

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) —
AI-Augmented SDLC Framework.
