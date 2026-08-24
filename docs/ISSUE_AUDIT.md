# Issue Audit

> Deterministic post-creation gate for every issue-creation flow.

The `nightgauge-issue-audit` skill runs as the terminal step after every
flow that creates GitHub issues — single issues, sub-issues, epics, batch
creations, cross-repo decompositions. It detects gaps left by probabilistic
creation steps (label selection, project board sync, sub-issue linking,
`blockedBy` wiring, body section formatting) and offers `--fix` /
`--fix-interactive` auto-repair using existing Go binary primitives.

## Why This Exists

Today, `nightgauge-epic-validate` covers exactly one shape — a single
epic. There is no universal post-creation contract that says: _the issues you
intended to create exist, are wired correctly, and have every field /
relationship / section the downstream pipeline depends on._ Issue-audit
provides that contract.

`epic-validate` is now a deprecated thin wrapper that delegates to
`issue-audit --epic <N>`. Existing slash-command invocations keep working;
new code should call `issue-audit` directly.

## Boundary: issue-audit vs backlog-audit

`issue-audit` is the **structural** post-creation gate: labels, board
membership and fields, body headings, sub-issue links, `blockedBy` wiring.
It runs at creation time and never judges whether an issue is still worth
doing.

[`nightgauge-backlog-audit`](../skills/nightgauge-backlog-audit/SKILL.md)
(`/nightgauge:backlog-audit`) is the **semantic** pass over the existing
open backlog: validity against current `main`, worth under the product
lens, and priority/size rank — run occasionally (after an epic completes,
or monthly). It reuses issue-audit's deterministic checks as riders rather
than duplicating them, and shares the same hard rule: never auto-rewrite
human-authored content.

## Invocation

```bash
# Strict mode — verify against a creation manifest
/nightgauge:issue-audit --manifest .nightgauge/pipeline/issue-create-manifest-<ts>.json

# Inferential mode — audit a known epic and its sub-issues
/nightgauge:issue-audit --epic 3237

# Inferential mode — audit a list of issues
/nightgauge:issue-audit --issues 3237,3238,3239 [--repo nightgauge/nightgauge]

# Look-back mode — audit every issue created in the last hour
/nightgauge:issue-audit --all-recent 1h
```

### Run Modes

| Flag                | Behavior                                                                 |
| ------------------- | ------------------------------------------------------------------------ |
| (default)           | Dry run — report findings only, never mutate GitHub state                |
| `--fix`             | Apply auto-repair for every safe finding category                        |
| `--fix-interactive` | Prompt before each repair; required for `closed-as-not-planned` blockers |

### Output Flags

| Flag               | Behavior                                                               |
| ------------------ | ---------------------------------------------------------------------- |
| `--json`           | Emit JSON findings to stdout (in addition to the Markdown report file) |
| `--no-audit-trail` | Suppress the JSONL audit trail                                         |
| `--allow-closed`   | Treat closed-as-completed issues as valid audit targets                |
| `--no-audit`       | (Consumer flag — read by `issue-create` only) skip the terminal pass   |

### Exit Codes

| Code | Meaning                                                                         |
| ---- | ------------------------------------------------------------------------------- |
| 0    | All findings are INFO or WARNING (or no findings at all). Verdict: READY.       |
| 1    | At least one CRITICAL finding remains after any requested repairs. NEEDS FIXES. |
| 2    | Skill itself failed (binary missing, manifest unreadable, GitHub auth error).   |

CI gates SHOULD treat exit code 1 as a hard merge block.

## Audit Phases

The skill walks 8 deterministic check phases in order. Each phase emits zero or
more findings into a single findings list with severity tags.

Phase numbers below are the skill's own — `SKILL.md` Phase 0 (environment
preflight) and Phase 1 (argument parsing) resolve the audit set but emit no
findings, so the checks start at Phase 2. Findings carry these numbers in the
`phase` field of the JSON report, so a number seen in a report locates the same
check in
[`skills/nightgauge-issue-audit/SKILL.md`](../skills/nightgauge-issue-audit/SKILL.md)
and here.

### Phase 2 — Existence & repo placement

For each issue in the audit set, fetch via `gh issue view --json
state,labels,repository,body,number`.

| Finding type       | Severity | Trigger                                                                         |
| ------------------ | -------- | ------------------------------------------------------------------------------- |
| `MISSING_ISSUE`    | CRITICAL | Manifest declares issue #N in `owner/repo` but the API returns 404              |
| `WRONG_REPO`       | CRITICAL | Manifest declares issue in repo X but the issue lives in repo Y                 |
| `UNEXPECTED_STATE` | WARNING  | Manifest expects `OPEN` but issue is `CLOSED` (and `--allow-closed` not passed) |

### Phase 3 — Label completeness

| Finding type              | Severity | Trigger                                                                  |
| ------------------------- | -------- | ------------------------------------------------------------------------ |
| `MULTIPLE_TYPE_LABELS`    | CRITICAL | More than one `type:*` label present                                     |
| `MISSING_TYPE_LABEL`      | CRITICAL | No `type:*` label                                                        |
| `MISSING_COMPONENT_LABEL` | INFO     | Issue body references a component path but no `component:*` label is set |

Note: `priority:*` and `size:*` are project board fields, not labels — those
checks live in Phase 4.

### Phase 4 — Project board membership and fields

Query each issue's `projectItems(first: 10)` GraphQL.

| Finding type             | Severity | Trigger                                                                     |
| ------------------------ | -------- | --------------------------------------------------------------------------- |
| `MISSING_FROM_BOARD`     | CRITICAL | Issue is not a member of the expected project (manifest's `project_number`) |
| `WRONG_BOARD`            | CRITICAL | Issue is in a different project than the one matching its repo              |
| `MISSING_STATUS_FIELD`   | CRITICAL | Project item exists but `Status` field is unset                             |
| `MISSING_PRIORITY_FIELD` | WARNING  | Project item exists but `Priority` field is unset                           |
| `MISSING_SIZE_FIELD`     | WARNING  | Project item exists but `Size` field is unset                               |

### Phase 5 — Body section completeness

Per-type required heading table. The canonical copy lives in
[`skills/nightgauge-issue-audit/SKILL.md` Phase 5](../skills/nightgauge-issue-audit/SKILL.md#phase-5-body-section-completeness);
`nightgauge-issue-create` authors against the same table, and
`scripts/check-issue-body-contract.py` fails if any of the three copies drift.

Sections are detected via heading regex (`^##[[:space:]]+<heading>\s*$`);
non-empty content check uses `awk` between heading boundaries. **Matching is
exact and case-sensitive** — `## Acceptance criteria` does not satisfy a
required `Acceptance Criteria`. Headings the table does not list are ignored,
so extra sections are always safe.

| Type     | Required headings                             |
| -------- | --------------------------------------------- |
| feature  | Summary, Acceptance Criteria                  |
| bug      | Summary, Steps to Reproduce, Expected, Actual |
| docs     | Summary, Acceptance Criteria                  |
| refactor | Summary, Acceptance Criteria                  |
| spike    | Summary, Acceptance Criteria, Recommendations |
| chore    | Summary                                       |
| epic     | Summary, Sub-Issues, Acceptance Criteria      |

| Finding type               | Severity                                    | Trigger                                                                                  |
| -------------------------- | ------------------------------------------- | ---------------------------------------------------------------------------------------- |
| `MISSING_REQUIRED_HEADING` | CRITICAL in `--manifest` mode, else WARNING | A heading required by the issue's type is absent — see [Severity Tiers](#severity-tiers) |
| `EMPTY_REQUIRED_HEADING`   | WARNING                                     | A required heading is present but its body is empty                                      |
| `MISSING_SPIKE_RECS_BLOCK` | CRITICAL                                    | Spike issue is missing the `yaml recommendations` block per `docs/SPIKE_CONTRACT.md`     |
| `OVERSIZED_SCOPE`          | WARNING                                     | Issue bundles many independent units of work into a single ticket — see below            |

#### `OVERSIZED_SCOPE` heuristic

Catches issues that bundle many independent refactors into a single executable
ticket, the root cause of pipeline runaways (incident #3811: $112.77 of
feature-dev churn on one issue that meant "refactor ~18 skills"). Runs for **all
issue types** and mirrors the `issue-create` Phase 2.85 oversized-scope gate, so
manually-created issues (which never pass through `issue-create`) are still
flagged post-creation.

The finding fires when **any** of these signals trips:

- **≥6 distinct top-level target files** referenced in the body, OR
- **predicted size `XL`** (`nightgauge size predict <num> --json`
  → `SizeLabel`), OR
- **≥6 independent acceptance-criteria groups** — top-level list items whose
  verb signals an independent unit of work (`refactor`/`migrate`/`convert`/
  `split`/`rewrite`/`extract`/`decompose`/`reduce`/`trim`).

**Exemptions** (finding does not fire):

- The issue is a decomposed epic (`type:epic` with ≥1 native sub-issue) — large
  scope split across sub-issues is the desired shape.
- The body carries the override marker
  `<!-- nightgauge:oversized-scope-accepted -->` (or the phrase
  `oversized scope accepted`).

**No repair primitive.** Decomposition into sub-issues under an epic is a
human/planning decision; `--fix` never auto-applies it. Even under `--fix`,
`OVERSIZED_SCOPE` remains a WARNING.

### Phase 6 — Sub-issue & parent linking

Use `gh api graphql` `subIssues(first: 50)` and the body's `Part of #X` /
`Part of <owner>/<repo>#X` annotations.

| Finding type                 | Severity | Trigger                                                                          |
| ---------------------------- | -------- | -------------------------------------------------------------------------------- |
| `MISSING_SUB_ISSUE_LINK`     | CRITICAL | Manifest declares sub-issue #M but `addSubIssue` was not applied (same-repo)     |
| `MISSING_PART_OF_ANNOTATION` | CRITICAL | Cross-repo sub-issue body lacks `Part of <owner>/<repo>#<epic>`                  |
| `ORPHAN_SUB_ISSUE`           | WARNING  | Issue has `Part of #X` but the parent epic has no matching native sub-issue link |

### Phase 7 — `blockedBy` alignment

Delegate the structural check to `nightgauge epic validate --json` for
circular and stale blockers. Parse body `Depends on:` lines and the
`<!-- nightgauge:dependency-metadata -->` YAML block. Cross-repo blockers
verified via `gh issue view --repo`.

| Finding type                   | Severity | Trigger                                                                        |
| ------------------------------ | -------- | ------------------------------------------------------------------------------ |
| `MISSING_BLOCKED_BY`           | CRITICAL | Body declares `Depends on: #X` but no native `blockedBy` edge exists           |
| `STALE_BLOCKED_BY`             | WARNING  | `blockedBy` points to an issue that is `closed-as-completed`                   |
| `STALE_BLOCKED_BY_NOT_PLANNED` | WARNING  | `blockedBy` points to an issue that is `closed-as-not-planned`                 |
| `CIRCULAR_BLOCKER`             | CRITICAL | Sub-issue → parent-epic blocking edge detected (per `epic validate`)           |
| `CROSS_REPO_BLOCKER_MISSING`   | CRITICAL | Body cites a cross-repo blocker that does not exist or is in a different state |

### Phase 8 — Cross-repo consistency

For epics whose manifest entries span multiple repos (or whose body cites
`<owner>/<repo>#N` cross-repo references), audit body annotations both
directions.

| Finding type                   | Severity | Trigger                                                                                     |
| ------------------------------ | -------- | ------------------------------------------------------------------------------------------- |
| `MISSING_PARENT_BACKREF`       | WARNING  | Cross-repo sub-issue has `Part of <owner>/<repo>#<epic>` but the epic body has no link back |
| `INCONSISTENT_PROJECT_MAPPING` | CRITICAL | Two sub-issues in the same target repo are in different projects                            |

### Phase 9 — Knowledge scaffold

When `knowledge.enabled: true` in `.nightgauge/config.yaml` OR the
manifest entry sets `knowledge_path`, verify the knowledge directory exists
and `PRD.md` is non-empty.

| Finding type            | Severity | Trigger                                                                            |
| ----------------------- | -------- | ---------------------------------------------------------------------------------- |
| `MISSING_KNOWLEDGE_DIR` | WARNING  | Manifest declares `knowledge_path` but the directory is missing                    |
| `MISSING_PRD_FILE`      | WARNING  | Knowledge dir exists but `PRD.md` is missing                                       |
| `EMPTY_PRD_FILE`        | INFO     | `PRD.md` exists but contains only template boilerplate (<30 chars after stripping) |

## Severity Tiers

| Tier     | Definition                                                                           | Exit code impact |
| -------- | ------------------------------------------------------------------------------------ | ---------------- |
| CRITICAL | Pipeline pickup will fail or silently break — blocks merge / blocks autonomous queue | Causes exit 1    |
| WARNING  | Quality is degraded but pipeline can still pick up the issue                         | No exit impact   |
| INFO     | Optional improvement; safe to ignore                                                 | No exit impact   |

The verdict is `READY` when no CRITICAL findings remain (post-repair), `NEEDS
FIXES` otherwise.

### `MISSING_REQUIRED_HEADING` — mode-dependent severity (#711)

This finding is **CRITICAL in strict (`--manifest`) mode and WARNING in every
inferential mode** (`--epic`, `--issues`, `--all-recent`). The split was decided
deliberately in #711; the reasoning is recorded here because a severity that is
not written down drifts back.

Until #711, `issue-create` prescribed one body shape (`Problem statement`,
`Business/user value`, `Acceptance criteria`, `Technical notes`) and this audit
required another, while `issue-create` Phase 6 ran the audit as its own terminal
gate. The two contracts shared no heading, so Phase 5 fired on **every** issue
the skill authored — 15 findings across the 5 issues of epic #702 alone. Being a
WARNING is what let that ship silently for as long as it did: the verdict turns
only on CRITICAL count, so the audit printed `READY` and exited 0 while
reporting that the issue did not match the shape the pipeline declares it needs.

A finding that fires on 100% of inputs is either mis-specified or under-severe.
Reconciling the two contracts fixed the mis-specification; the severity was then
re-decided on its merits:

- **Strict mode is CRITICAL.** The manifest was written moments earlier by
  `issue-create`, which now authors against this exact table. A missing heading
  there is not degraded quality — it is proof the authoring contract was not
  followed on the one run where following it was guaranteed. That matches the
  CRITICAL row above ("pipeline pickup will fail or silently break"), and it is
  what makes `issue-create` Phase 6 a gate capable of failing.
- **Inferential mode stays WARNING.** Those modes routinely target hand-filed
  and pre-#711 issues that no creation flow ever shaped. Promoting there would
  turn every backlog sweep red for defects the operator did not introduce in
  that run; grooming legacy bodies is
  [`backlog-audit`](../skills/nightgauge-backlog-audit/SKILL.md)'s job.

The finding has no repair primitive in either mode and must not acquire one:
required sections are human-authored prose, and synthesizing an `## Actual`
section would fabricate the very evidence the heading exists to carry.

`scripts/check-issue-body-contract.py` pins the reconciliation itself — the three
copies of the table (this document, the audit skill, the authoring rules in
`issue-create`) must agree, and a body authored per `issue-create`'s rules must
produce zero `MISSING_REQUIRED_HEADING` for every type in the table.

## Repair Primitives

Auto-repair (`--fix` / `--fix-interactive`) is gated to existing Go binary
subcommands. The skill never invents new logic; it orchestrates known-good
primitives.

| Finding category                         | Repair primitive                                                            |
| ---------------------------------------- | --------------------------------------------------------------------------- |
| `MISSING_FROM_BOARD` / `WRONG_BOARD`     | `nightgauge project add <num> --repo <owner/repo> --project <num>`          |
| `MISSING_STATUS_FIELD`                   | `nightgauge project sync-status <num> <status>`                             |
| `MISSING_PRIORITY_FIELD`                 | `nightgauge project set-field <num> Priority <P0\|P1\|P2\|P3>`              |
| `MISSING_SIZE_FIELD`                     | `nightgauge project set-field <num> Size <XS\|S\|M\|L\|XL>`                 |
| `MISSING_SUB_ISSUE_LINK` (same-repo)     | `nightgauge issue link-sub <epic> <sub>`                                    |
| `MISSING_PART_OF_ANNOTATION`             | `nightgauge issue edit <num> --append-body "Part of <owner>/<repo>#<epic>"` |
| `MISSING_BLOCKED_BY`                     | `nightgauge issue add-blocked-by <blocked> <blocker>`                       |
| `STALE_BLOCKED_BY` (closed-as-completed) | `nightgauge issue remove-blocked-by <blocked> <blocker>` (auto)             |
| `STALE_BLOCKED_BY_NOT_PLANNED`           | `nightgauge issue remove-blocked-by` (requires `--fix-interactive`)         |
| `CIRCULAR_BLOCKER`                       | `nightgauge issue remove-blocked-by <sub> <epic>` (auto)                    |
| `MISSING_PARENT_BACKREF`                 | `nightgauge issue edit <epic> --append-body "<cross-repo link line>"`       |

### Hard Rules (Auto-fix Constraints)

These rules are encoded in the skill body and the negative test fixture pins
them — they MUST hold regardless of operator flags:

1. **Never auto-rewrite human-authored content**: Summary, Acceptance
   Criteria, User Story, Technical Notes, and spike artifact (`docs/spikes/...`)
   content is never modified by `--fix`. Findings that require human content
   are flagged CRITICAL or WARNING for manual resolution.
2. **Spike contract violations stay CRITICAL even with `--fix`**:
   `MISSING_SPIKE_RECS_BLOCK` and a malformed `yaml recommendations` block are
   not auto-repairable. The negative test fixture `spike-contract-violation`
   pins this invariant.
3. **`closed-as-not-planned` blockers require interactive confirmation**:
   `--fix` alone removes only `closed-as-completed` stale blockers.
   `not-planned` requires `--fix-interactive` so the operator can confirm the
   intent change.
4. **Repair failures become CRITICAL findings**: If a repair primitive exits
   non-zero (e.g., GitHub rate limit, permissions), the original finding is
   re-classified as CRITICAL with the underlying error in `repair_error`. The
   skill does not retry internally — the operator re-runs the audit.
5. **`OVERSIZED_SCOPE` is never auto-fixed**: decomposing a bundled issue into
   sub-issues under an epic is a human/planning decision. `--fix` never applies
   it; the finding stays a WARNING for manual resolution (decompose into an epic
   or add the `<!-- nightgauge:oversized-scope-accepted -->` override).

## Manifest Schema (`CreationManifest`)

The Zod schema is the single source of truth at the write boundary
(`nightgauge-issue-create`). The skill itself parses the manifest with
`jq` since shell skills cannot import TypeScript.

```ts
// packages/nightgauge-sdk/src/context/schemas/creation-manifest.ts
import { CreationManifestSchema, CreationManifestEntrySchema } from "@nightgauge/sdk";
```

### File location

`.nightgauge/pipeline/issue-create-manifest-<timestamp>.json`

### Top-level shape

| Field              | Type   | Required | Notes                                                  |
| ------------------ | ------ | -------- | ------------------------------------------------------ |
| `schema_version`   | string | Yes      | Pinned to `"1.0"` for the initial release              |
| `created_at`       | string | Yes      | ISO 8601 timestamp                                     |
| `created_by_skill` | string | Yes      | e.g. `nightgauge-issue-create`                         |
| `project_number`   | number | No       | Single-repo creation flows; multi-repo flows omit this |
| `entries`          | array  | Yes      | One entry per issue created (epic + every sub-issue)   |

### Entry shape

| Field              | Type   | Required | Notes                                                              |
| ------------------ | ------ | -------- | ------------------------------------------------------------------ |
| `repo`             | string | Yes      | `owner/repo` slug                                                  |
| `number`           | number | Yes      | GitHub issue number                                                |
| `type`             | string | Yes      | `feature \| bug \| docs \| refactor \| spike \| chore \| epic`     |
| `priority`         | string | Yes      | `P0 \| P1 \| P2 \| P3`                                             |
| `size`             | string | Yes      | `XS \| S \| M \| L \| XL`                                          |
| `status`           | string | Yes      | `Backlog \| Ready \| In progress`                                  |
| `parent_epic`      | string | No       | Sub-issues only; `<owner>/<repo>#<n>` or `#<n>` shorthand          |
| `sub_issues`       | array  | No       | Epic only; declared sub-issue numbers                              |
| `blocked_by`       | array  | No       | `[{ number, repo? }]`                                              |
| `body_sections`    | array  | No       | Required body headings (e.g. `["Summary", "Acceptance Criteria"]`) |
| `component_labels` | array  | No       | `component:*` labels attached at creation time                     |
| `knowledge_path`   | string | No       | Absolute or repo-relative path to scaffolded knowledge directory   |
| `spike_artifact`   | object | No       | `{ path, exists }` — required for `type: spike`                    |

### Lifecycle

1. `nightgauge-issue-create` Phase 4.9 writes the manifest after every
   issue + relationship is created and Phase 4.8 (cross-repo audit) passes.
2. `nightgauge-issue-create` Phase 6 invokes
   `nightgauge-issue-audit --manifest <path>` as the terminal step.
   Audit exit code propagates.
3. On `READY` verdict, the manifest is preserved alongside the audit report
   (it is the historical record). On `NEEDS FIXES`, the operator re-runs
   audit until READY. Manifests are cleaned up by `pr-merge` along with other
   pipeline transients.

## Reports

### Markdown report

`.nightgauge/pipeline/issue-audit-<timestamp>.md`

Severity-tiered finding list with the per-finding repair command (or "no
auto-fix available" for human-only items). Final verdict line is
`Verdict: READY` or `Verdict: NEEDS FIXES (n CRITICAL, m WARNING, k INFO)`.

### JSON findings

`.nightgauge/pipeline/issue-audit-<timestamp>.json`

```json
{
  "schema_version": "1.0",
  "verdict": "NEEDS FIXES",
  "summary": { "critical": 1, "warning": 2, "info": 0 },
  "audited": [{ "repo": "nightgauge/nightgauge", "number": 3237 }],
  "findings": [
    {
      "issue": { "repo": "nightgauge/nightgauge", "number": 3238 },
      "phase": 4,
      "type": "MISSING_FROM_BOARD",
      "severity": "CRITICAL",
      "detail": "Sub-issue is not a member of project 1.",
      "repair_command": "nightgauge project add 3238 --repo nightgauge/nightgauge --project 1",
      "repair_status": "not_attempted"
    }
  ],
  "started_at": "2026-05-06T20:30:00Z",
  "completed_at": "2026-05-06T20:30:04Z"
}
```

`repair_status` is one of `not_attempted`, `succeeded`, `failed`. When `--fix`
runs, the field reflects the outcome of the repair primitive call; failures
include `repair_error` with the underlying error string.

### Audit trail

`.nightgauge/pipeline/issue-audit-<timestamp>.audit.jsonl`

One JSON line per repair attempt:

```jsonl
{
  "ts": "2026-05-06T20:30:01Z",
  "issue": "nightgauge/nightgauge#3238",
  "finding": "MISSING_FROM_BOARD",
  "action": "project add",
  "before": {
    "in_project": false
  },
  "after": {
    "in_project": true
  },
  "actor": "nightgauge-issue-audit"
}
```

Trail files are per-run and retention is bounded by the
`.nightgauge/pipeline/` cleanup policy (cleared on PR merge).

## Invocation Matrix

| Mode           | Use case                                                              | Strictness  |
| -------------- | --------------------------------------------------------------------- | ----------- |
| `--manifest`   | Terminal pass after `issue-create` (or future epic decomp flows)      | Strict      |
| `--epic`       | Audit a known epic and its sub-issues (replaces `epic-validate`)      | Inferential |
| `--issues`     | Audit an arbitrary list of issues (e.g. after a manual catch-up flow) | Inferential |
| `--all-recent` | Sweep every issue created in the last hour (CI safety net)            | Inferential |

Strict mode runs every assertion the manifest declares; inferential mode runs
the same phases but uses heuristics (issue type, body sections, project
config) to derive expectations.

## Integration Points

- **Producers consumed**: `gh api graphql`, `gh issue view`,
  `nightgauge epic validate --json`,
  `nightgauge audit lifecycle --json` (cross-repo), Phase 4.9 manifest
  from `issue-create`.
- **Consumers**: `nightgauge-issue-create` (reads exit code; surfaces
  CRITICAL findings), `nightgauge-epic-validate` (delegates to
  `--epic`), CI workflows that gate merges on the JSON report.

## Failure Modes (skill-level, exit 2)

The skill exits 2 — distinct from a finding-driven exit 1 — when the audit
itself cannot run:

- `nightgauge` binary not in `PATH` and not at `bin/nightgauge`
- `gh` not authenticated
- `--manifest <path>` does not exist or fails Zod parse
- `.nightgauge/config.yaml` is unreadable when the project number must
  be discovered

Exit 2 is a hard error; exit 1 is a finding-driven verdict. CI gates should
treat both as failures.
