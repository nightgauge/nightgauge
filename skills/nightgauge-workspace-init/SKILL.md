---
name: nightgauge-workspace-init
description: Scaffold a multi-repo workspace manifest
  (.vscode/nightgauge-workspace.yaml) for the N:1 shared-project topology,
  where several member repositories share a single GitHub Project. Detects member
  repos under the parent folder, derives the shared project, generates the
  manifest with repositories/routing/epic blocks, and verifies the result via
  `workspace sync-payload`. Idempotent — re-running refreshes the manifest
  without duplicating entries. Use once when onboarding a parent folder that
  groups multiple pipeline repos, or to repair an empty Repositories view.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Bash AskUserQuestion
---

# Nightgauge Workspace Init

> Scaffold a multi-repo workspace manifest for the Nightgauge SDLC pipeline

## Description

`repo-init` primes a single repository. **`workspace-init` primes the layer
above it**: the parent folder that groups several member repos which share one
GitHub Project (the N:1 topology). Without this manifest, opening the parent
folder renders an empty Repositories/board panel in the VSCode extension.

This skill:

1. Detects member repositories under the parent folder (each directory with a
   `.nightgauge/config.yaml`).
2. Derives the shared GitHub Project (from each member's `project.number`, or via
   `nightgauge workspace repos-from-project`).
3. Generates `.vscode/nightgauge-workspace.yaml` with `workspace`,
   `repositories`, `routing`, and `epic` blocks.
4. Verifies success by running `workspace sync-payload` and asserting a non-empty
   `repos` array.

**Run this once when onboarding a multi-repo parent folder. It is idempotent —
safe to re-run to add newly-onboarded members or refresh routing.**

## Invocation

| Tool           | Command                                   |
| -------------- | ----------------------------------------- |
| Claude Code    | `/nightgauge:workspace-init` (via plugin) |
| OpenAI Codex   | `$nightgauge-workspace-init`              |
| GitHub Copilot | Invoke via Agent Skills                   |
| Cursor         | Invoke via Agent Skills                   |

## Arguments

| Argument        | Description                                | Default                  |
| --------------- | ------------------------------------------ | ------------------------ |
| `--dry-run`     | Preview the manifest without writing it    | `false`                  |
| `--name <name>` | Workspace display name                     | (derived or prompted)    |
| `--project <N>` | Shared GitHub Project number               | (from members or prompt) |
| `--root <path>` | Parent folder to scan (the workspace root) | current directory        |

## Prerequisites

- **Parent folder** containing 2+ member repos, each already onboarded with
  `repo-init` (so each has a `.nightgauge/config.yaml`).
- **Go binary** — `nightgauge` on PATH (verified in Phase 0).

> **CRITICAL**: When invoked headless (no TTY), do NOT use AskUserQuestion.
> Derive values from member configs or fail with a clear error.

## Philosophy

All manifest assembly is **deterministic** — member detection reads on-disk
configs, project derivation reuses the `repos-from-project` Go verb, and
verification reuses `workspace sync-payload`. The skill prefers Go-binary verbs
over reimplementing logic, consistent with `repo-init`.

| Operation                | Type          | Source                                          |
| ------------------------ | ------------- | ----------------------------------------------- |
| Member detection         | Deterministic | Filesystem scan for `.nightgauge/config.yaml`   |
| Project derivation       | Deterministic | `workspace repos-from-project` / member configs |
| Manifest generation      | Deterministic | Templated YAML from detected members            |
| Verification             | Deterministic | `workspace sync-payload` (non-empty `repos`)    |
| Workspace name / prompts | Probabilistic | AI formats questions and summaries              |

## Supporting files (load on demand)

This skill follows ADR-010 progressive disclosure: the skeleton below carries
the overview and per-phase Read directives; the procedural detail (bash, YAML
templates, verification logic) lives in `_includes/` and is loaded only when its
phase fires.

- `skills/nightgauge-workspace-init/_includes/member-detection.md` — read in Phases 1 and 2 (scan members, derive shared project)
- `skills/nightgauge-workspace-init/_includes/manifest-generation.md` — read in Phases 3 and 4 (existing-manifest handling, generate/merge the YAML)
- `skills/nightgauge-workspace-init/_includes/verification.md` — read in Phases 5 and 6 (sync-payload verification, summary report)

---

## Workflow

### Phase 0: Prerequisites

<!-- include: ../_shared/PREFLIGHT.md -->

---

Verify the `nightgauge` binary is available, resolve the workspace root
(`--root` or CWD), and parse arguments.

> **Read `skills/nightgauge-workspace-init/_includes/member-detection.md` now and follow its instructions before continuing this phase.**

---

### Phase 1: Detect Member Repositories

Scan the workspace root for member repos — each immediate subdirectory that
contains `.nightgauge/config.yaml`. Read each member's `owner`, `repo`, and
`project.number`. Fail with a clear error if fewer than 2 members are found
(a single repo does not need a workspace manifest — use `repo-init` instead).

> **Read `skills/nightgauge-workspace-init/_includes/member-detection.md` now and follow its instructions before continuing this phase.**

---

### Phase 2: Derive the Shared Project

Determine the shared GitHub Project number. Prefer an explicit `--project`; else
read it from the members' configs (they should agree); else query
`nightgauge workspace repos-from-project`. Pick a `default_repository`
(the member whose role is primary, or the first detected).

> **Read `skills/nightgauge-workspace-init/_includes/member-detection.md` now and follow its instructions before continuing this phase.**

---

### Phase 3: Existing Manifest Handling

If `.vscode/nightgauge-workspace.yaml` already exists, read it and MERGE
(idempotent) rather than overwrite — preserve existing routing patterns and the
workspace description, add only newly-detected members. In `--dry-run`, print
the would-be manifest and stop.

> **Read `skills/nightgauge-workspace-init/_includes/manifest-generation.md` now and follow its instructions before continuing this phase.**

---

### Phase 4: Generate the Manifest

Write `.vscode/nightgauge-workspace.yaml` with `workspace` (name +
description), `repositories[]` (name/path/role/project_number per member),
`routing` (default_repository + any patterns), and an `epic` block. Match the
verified reference shape documented in the include.

> **Read `skills/nightgauge-workspace-init/_includes/manifest-generation.md` now and follow its instructions before continuing this phase.**

---

### Phase 5: Verify

Run `nightgauge workspace sync-payload` from the workspace root and assert
the `repos` array is non-empty and contains every detected member. A non-empty
payload proves the extension will render the shared board. Run `workspace
doctor` and surface any fatal validation errors.

> **Read `skills/nightgauge-workspace-init/_includes/verification.md` now and follow its instructions before continuing this phase.**

---

### Phase 6: Summary Report

Output a clear summary: workspace name, members registered, shared project,
manifest path, and `sync-payload` verification result. Then the recommended next
step (open the parent folder in VSCode).

> **Read `skills/nightgauge-workspace-init/_includes/verification.md` now and follow its instructions before continuing this phase.**

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

| Error                                   | Cause                                 | Resolution                                                                |
| --------------------------------------- | ------------------------------------- | ------------------------------------------------------------------------- |
| `nightgauge: command not found`         | Binary not installed                  | Build with `go build ./cmd/nightgauge` or install via releases            |
| Fewer than 2 members detected           | Parent folder has 0–1 onboarded repos | Run `repo-init` in each member first; a single repo needs no manifest     |
| Members disagree on project number      | Configs point at different projects   | Pass `--project <N>` explicitly, or align the members' `project.number`   |
| `sync-payload` returns empty `repos`    | Member config missing owner/repo      | Re-run `repo-init` in the offending member, or check its `config.yaml`    |
| `workspace doctor` reports fatal errors | Forge misconfiguration                | Fix the reported `forges:`/`autonomous.repositories` entries, then re-run |

---

## Idempotency

This skill is **fully idempotent**:

- **Manifest**: re-running MERGES — existing routing patterns and description are
  preserved; only newly-detected members are appended. No duplicate
  `repositories[]` entries.
- **Verification**: `sync-payload` is read-only.

Safe to re-run after onboarding a new member repo into the parent folder.

---

## Integration

### Relationship to Other Skills

| Skill                      | Relationship                                                 |
| -------------------------- | ------------------------------------------------------------ |
| `/nightgauge:repo-init`    | Run per-member FIRST — each member needs its own config.yaml |
| `/nightgauge:project-sync` | Bulk-sync existing issues after the workspace is registered  |
| `/nightgauge:issue-pickup` | Start the pipeline once the workspace renders                |

### Recommended Onboarding Order

```
1. /nightgauge:repo-init       ← per member repo (run N times)
2. /nightgauge:workspace-init  ← This skill (parent folder manifest)
3. Open the parent folder in VSCode ← shared board renders
```

---

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) —
AI-Augmented SDLC Framework.
