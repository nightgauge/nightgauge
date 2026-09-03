# Knowledge Base

This document describes the `.nightgauge/knowledge/` directory system — its
structure, file schemas, naming conventions, configuration flags, and how it
integrates with the pipeline.

> **Implementation source**: All schemas, templates, and slug algorithms in this
> document are derived directly from
> `packages/nightgauge-sdk/src/services/KnowledgeService.ts`.

---

## Access Policy

Knowledge base operations follow a three-layer access pattern. Each layer is
appropriate for a specific execution context:

| Context                              | Pattern                                      | Rationale                                               |
| ------------------------------------ | -------------------------------------------- | ------------------------------------------------------- |
| VSCode extension commands            | `KnowledgeService` TypeScript class          | Full Node.js environment; typed, Zod-validated          |
| Go binary (CLI / hooks / IPC)        | `nightgauge knowledge *` subcommands         | Deterministic layer; no Node.js dependency              |
| Pipeline skills (Claude Code agents) | `jq` / `cat` for reads; Go binary for writes | Skills run in isolated agent context without SDK access |

**When to use each layer:**

- Use `KnowledgeService` from TypeScript when you are inside the VSCode
  extension or SDK — it provides the richest API with validation.
- Use `nightgauge knowledge scaffold|prune|index` from Go hooks, CI
  scripts, or any context where Node.js is unavailable.
- Use `jq`/`cat` shell-outs **only for reads** inside pipeline skills. Prefer
  `nightgauge knowledge scaffold` for any write operations to keep
  template content consistent across layers.

This policy is intentional — skills run as isolated Claude Code agents without
access to the Node.js SDK. No migration of existing skills from `jq`/`cat` to
the Go binary is required unless a skill needs to write knowledge files.

---

## Overview

The knowledge base is an optional, per-repository directory that stores
persistent context for GitHub issues — specifically the product requirements and
key architectural decisions recorded during pipeline execution.

When enabled, the pipeline automatically scaffolds a directory for each issue as
it is picked up. AI agents and developers can then read and update these files
throughout the lifecycle of an issue.

**Where the scaffold lands, and who creates it.** The scaffold is created by the
**Go pickup path** — `internal/orchestrator`, immediately after the issue-pickup
stage completes — at the **canonical workspace root** (the main checkout,
resolved with `git rev-parse --git-common-dir`), never at the stage's current
directory. `knowledge_path` is then stamped into the run's issue context file as
an **absolute** path, wherever `execution.IssueContextCandidates` finds that
file; on a worktree-isolated run that is inside the worktree, not at the root.

Absolute is deliberate: the stages read `knowledge_path` from the worktree while
the sidebar and `/nightgauge:retro` read it from the root, and only an absolute
path is correct from both.

This used to be a bash block in the issue-pickup skill that called
`nightgauge knowledge scaffold` from the stage's cwd. On the scheduler path that
cwd is the run's worktree, so the PRD and decisions landed in
`<worktree>/.nightgauge/knowledge/` — gitignored, and deleted with the worktree
at reclamation. Every later reader then found nothing (#1205). The skill's block
is gone; there is one path, not two.

`nightgauge knowledge scaffold` remains for manual use and canonicalizes its
root the same way, so it is safe to run from inside a worktree.

**Benefits:**

- Preserves intent and context across pipeline sessions (no conversation history
  required)
- Provides a human-readable audit trail of decisions made during implementation
- Enables future AI agents to load issue-specific context without querying
  GitHub

**Activation:** The knowledge base is **opt-in** — product default
`knowledge.enabled: false`. Set `knowledge.enabled: true` in your project config
to activate it. See [Configuration](#configuration).

> **Not the same as epic context.** The per-epic `epic-context-{E}.json`
> accumulator and its forward-injection into sibling sub-issue prompts (#4096,
> see [CONTEXT_ARCHITECTURE.md](CONTEXT_ARCHITECTURE.md#epic-context-e-json)) are
> **independent of this flag** — they are always active for epic sub-issues. The
> `knowledge.enabled` switch gates only the durable PRD/decisions/ADR knowledge
> tree described here.

---

## Information Architecture

This section is the **source of truth** for where a given piece of recorded
context belongs. Read it before deciding whether a new decision goes in `docs/`,
in `.nightgauge/knowledge/`, or in both. A small one-line pointer to this
section lives in the repo-root `AGENTS.md` so AI agents read it first.

### Scope rule (the one rule)

| Tree                     | Stable? | Reader-facing? | Authored by   | Lifetime                   |
| ------------------------ | ------- | -------------- | ------------- | -------------------------- |
| `docs/`                  | Yes     | Yes            | Humans        | Long-lived; survives epics |
| `.nightgauge/knowledge/` | No      | No (agents)    | Pipeline / AI | Per-issue; accretive log   |

- **`docs/`** is **stable, reader-facing, hand-authored** prose that an outside
  contributor can pick up and learn from. Anything in `docs/` should still be
  true a year from now without further edits.
- **`.nightgauge/knowledge/`** is **pipeline-scoped, accretive,
  agent-authored** raw context. Decisions accumulate here as the pipeline
  runs and are only meant to be consumed by the next agent in the chain.

If both rules fire — the decision is reusable AND it was discovered during a
specific issue — the decision is born in `knowledge/` and **graduates** to
`docs/` via the [Graduation Workflow](#graduation-workflow).

### The three KB tiers

The knowledge base has three nested tiers. Pick the smallest tier that still
captures the audience.

| Tier            | Path                                                                   | Holds                                               |
| --------------- | ---------------------------------------------------------------------- | --------------------------------------------------- |
| Issue tier      | `.nightgauge/knowledge/features/{N}-{slug}/`                           | Per-issue PRD, ADRs, outcomes (this lifecycle only) |
| Repo-topic tier | `.nightgauge/knowledge/{architecture,glossary,runbooks,post-mortems}/` | Repo-wide reference material the agent built up     |
| Workspace tier  | `.nightgauge/knowledge/{product,cross-repo,architecture}/`             | Cross-repo material at the workspace root           |

> `epics/`, `features/`, `architecture/`, `glossary/`, `runbooks/`, and
> `post-mortems/` are all implemented. See
> [Roadmap / Planned Features](#roadmap--planned-features) for what remains.

### Specificity heuristic

Use this checklist to pick a tier. **Anything that would still be true after
the issue is closed and forgotten belongs higher up.**

1. Is the decision tied to a single issue's implementation? → **Issue tier.**
2. Is it a repo-wide pattern other agents will rediscover? → **Repo-topic tier**
   (or graduate to `docs/`).
3. Does it apply across multiple repos in the workspace? → **Workspace tier**
   (or graduate to a cross-cutting `docs/` page).
4. Is it stable, reader-facing, hand-curated? → **`docs/`** via graduation.

#### Worked examples

- **Decision to use SSE over WebSockets for pipeline events** → graduates to
  `docs/ARCHITECTURE.md` (cross-cutting transport choice that any future agent
  needs to know). It is reader-facing and the trade-off is stable.
- **Decision to set `KnowledgePath=null` when the issue body is empty in
  issue-pickup for #2957** → stays in
  `.nightgauge/knowledge/features/2957-*/decisions.md`. The decision is
  scoped to one issue's bug fix and the rationale is best read in context.
- **Convention: all Go CLI subcommands use `--json` for structured output** →
  graduates to `docs/CODE_STANDARDS.md`. It is a reusable pattern that future
  contributors will look up by topic, not by the issue that introduced it.
- **Decision: `nightgauge knowledge graduate` is semi-manual (no
  auto-distillation)** → starts in this issue's `decisions.md`, graduates to
  this `## Graduation Workflow` section once the workflow is exercised on a
  second epic.

---

## Directory Structure

All knowledge files live under `.nightgauge/knowledge/` relative to the
repository root:

```text
{git_root}/.nightgauge/knowledge/
├── epics/
│   └── {N}-{slug}/
│       ├── PRD.md
│       └── decisions.md
├── features/
│   └── {N}-{slug}/
│       ├── PRD.md
│       └── decisions.md
├── architecture/
│   ├── README.md
│   ├── _template.md
│   └── {slug}.md
├── glossary/
│   ├── README.md
│   ├── _template.md
│   └── {slug}.md
├── runbooks/
│   ├── README.md
│   ├── _template.md
│   └── {slug}.md
└── post-mortems/
    ├── README.md
    ├── _template.md
    └── {slug}.md
```

**Category directories:**

| Directory       | Tier       | Used for                                                        |
| --------------- | ---------- | --------------------------------------------------------------- |
| `epics/`        | Issue      | Issues with the `type:epic` label                               |
| `features/`     | Issue      | All other issues (features, bugs, docs, chores)                 |
| `architecture/` | Repo-topic | Cross-issue architectural principles and patterns (agent notes) |
| `glossary/`     | Repo-topic | One-file-per-term domain vocabulary definitions                 |
| `runbooks/`     | Repo-topic | Operational procedures for recurring maintenance tasks          |
| `post-mortems/` | Repo-topic | Incident write-ups and retrospective analyses                   |

**Issue-tier** entries use `{N}-{slug}/` subdirectories. **Repo-topic** entries
use flat `{slug}.md` files directly inside the category directory. When a new
repo-topic category is first created, `README.md` and `_template.md` are
auto-scaffolded alongside the first entry.

### Directory Naming

Each issue gets a single subdirectory named `{issueNumber}-{slug}`:

```text
1673-knowledge-base-schema-doc/
```

The `{slug}` is derived from the issue title using the algorithm described in
[Naming Conventions](#naming-conventions).

---

## File Schema

Each knowledge directory scaffolds two files on issue pickup:

| File           | Lifecycle                  | Purpose                                       |
| -------------- | -------------------------- | --------------------------------------------- |
| `PRD.md`       | Scaffolded on issue pickup | Product requirements document for the issue   |
| `decisions.md` | Scaffolded on issue pickup | Architectural decision log for implementation |

Both file names are fixed — the directory name provides the issue identity.

The retro stage appends its `## Outcome` block to `decisions.md`. There is no
third file: a former `outcomes.md` fallback split the retro learning loop
across two filenames while `decisions.md` was the only one anything read back.
A missing `decisions.md` is created carrying the frontmatter contract rather
than reported as an error — the outcome is the one durable artifact of a run,
and losing it over a file the binary can create correctly would defeat the
point of recording it.

### PRD.md

The PRD is the **single source of truth** for an issue's requirements. It carries
the product requirements **and** the technical requirements (the embedded "TRD",
in `## Technical Approach`) **and** the quality / non-functional requirements
(the embedded "QRD", in `## Quality & Non-Functional Requirements`). These are
sections, not separate files — splitting them out would create parallel sources
of truth with the same lifetime, audience, and trigger as the PRD. See
[Information Architecture](#information-architecture) for the scope rule that
justifies keeping them inline.

**Template** (produced by `KnowledgeService.renderPrdBody()`, shared by
`generatePRD()` and `regenerateForIssue()`; the Go binary's `generatePRD` emits
the identical section structure):

```markdown
# PRD: #{issueNumber} — {issueTitle}

## Summary

{extracted from issue body ## Summary section, or placeholder comment}

## User Story

{extracted from issue body ## User Story section, or placeholder comment}

## Acceptance Criteria

{extracted from issue body ## Acceptance Criteria section, or placeholder
comment}

## Technical Approach

{embedded TRD — extracted from issue body ## Technical Approach (or legacy

## Technical Notes), or placeholder comment}

## Quality & Non-Functional Requirements

{embedded QRD — extracted from issue body, or placeholder comment}

## Out of Scope

{extracted from issue body ## Out of Scope section, or placeholder comment}

## Status

- [ ] Draft
- [ ] Reviewed
- [ ] Approved
```

**Section extraction:** When scaffolding, `KnowledgeService` extracts content
from standard issue body sections (`## Summary`, `## User Story`,
`## Acceptance Criteria`, `## Technical Approach`, `## Quality & Non-Functional
Requirements`, `## Out of Scope`). For backward compatibility, `## Technical
Approach` also accepts a legacy `## Technical Notes` heading in the issue body so
older issue templates lose no content. If a section is absent, a guided
`<!-- TODO -->` placeholder comment is inserted instead. The Go CLI scaffold path
(`nightgauge knowledge scaffold`) seeds the same section skeleton but fills
only Acceptance Criteria; feature-planning then enriches Technical Approach,
Quality & Non-Functional Requirements, and Out of Scope **in place**.

**Full example** (after feature-planning enrichment):

```markdown
# PRD: #1673 — Define knowledge directory schema and write docs/KNOWLEDGE_BASE.md

## Summary

The knowledge base scaffolding system is fully implemented but has no
documentation. This issue delivers the authoritative schema reference.

## User Story

As a pipeline maintainer, I want an authoritative schema reference so that I can
extend the knowledge base without reverse-engineering the implementation.

## Acceptance Criteria

- [ ] Schema, naming, and config flags documented
- [ ] Template matches `KnowledgeService.renderPrdBody()` output

## Technical Approach

Derive every schema and template in the doc directly from
`KnowledgeService.ts`. No code changes — documentation only. Key files:
`docs/KNOWLEDGE_BASE.md`.

## Quality & Non-Functional Requirements

None beyond the acceptance criteria — documentation-only change. Verified by
markdown lint and a link check.

## Out of Scope

Auto-distillation of decisions and any change to the scaffolding code.

## Status

- [x] Draft
- [ ] Reviewed
- [ ] Approved
```

### decisions.md

The decisions log records architectural choices made during implementation —
what options were considered, what was selected, and why.

**Template** (produced by `KnowledgeService.generateDecisionsTemplate()`):

```markdown
# Decisions: #{issueNumber} — {issueTitle}

## Architecture Decisions

<!-- Record key architectural decisions made during implementation.
     Add one ADR block per decision. -->

## ADR-001: [Decision Title]

**Status**: Proposed
**Context**: [Background and constraints that led to this decision]
**Decision**: [What was decided and why]
**Consequences**: [Expected impact, trade-offs, and follow-up actions]
```

> **Canonical format**: ADR blocks (Status / Context / Decision / Consequences)
> are the canonical `decisions.md` schema. Each architectural decision gets its
> own numbered block (ADR-001, ADR-002, …). An optional summary table at the top
> as a TOC is allowed but not required.

**Full example with a completed entry:**

```markdown
# Decisions: #1673 — Define knowledge directory schema and write docs/KNOWLEDGE_BASE.md

## Architecture Decisions

<!-- Record key architectural decisions made during implementation.
     Add one ADR block per decision. -->

## ADR-001: Doc format

**Status**: Accepted
**Context**: Knowledge files need a structured format for recording decisions. Options were YAML frontmatter or pure Markdown sections.
**Decision**: Use pure Markdown sections — matches existing docs/ formatting and requires no parser.
**Consequences**: No parser required; human and agent readable; structure is enforced by convention only.
```

### The retro outcome block

The retro stage appends this block to `decisions.md` when `knowledge_path` is
set for the issue and outcome recording is enabled (`--record-outcome`, or
auto-enabled because `knowledge_path` was found in the issue context).

**Template:**

```markdown
## Outcome

**Issue**: #{issueNumber}
**Date**: {YYYY-MM-DD}
**Status**: {complete|partial|failed}
**Pipeline Duration**: {optional}
**Token Usage**: {optional}

### What Went Well

{narrative or bullets}

### What Didn't Go Well

{narrative or bullets}

### Lessons Learned

{narrative or bullets}

---
```

**Example:**

```markdown
## Outcome

**Issue**: #890
**Date**: 2026-04-21
**Status**: complete

### What Went Well

- Documentation drift was resolved against the skill files that currently
  drive the pipeline.
- The change remained docs-only and did not require code or schema changes.

### What Didn't Go Well

None — all stages completed successfully.

### Lessons Learned

- Keep `docs/KNOWLEDGE_BASE.md` aligned with skill behavior whenever the
  knowledge pipeline changes.

---
```

Recording an outcome also stamps the entry: a `verified` event from
`process:retro`, and the merged PR as a `sources` entry when `--pr-url` is
given. See [Recording provenance](#recording-provenance-knowledge-stamp).

---

## Frontmatter

Every non-reserved `.md` under the knowledge root carries a YAML frontmatter
block delimited by `---` sentinels. There is **one** contract, shared by the Go
binary (`internal/knowledge/okf`) and the TypeScript SDK
(`KnowledgeEntrySchema`); the two layers had drifted into disagreeing
contracts — repo-topic entries carried `type`/`created`/`tags`/`status`,
issue-tier `PRD.md` and `decisions.md` carried nothing at all, and the SDK
schema's comment claimed knowledge files did not use frontmatter — and this
replaced both.

The field vocabulary is that of the [Open Knowledge Format
v0.2](https://github.com/GoogleCloudPlatform/open-knowledge-format) (Google
Cloud, Apache-2.0). **We adopt the vocabulary, not the reference tooling**: the
spec is v0.2 from a single vendor and already broke compatibility once, three
months after publication. Fields are cheap to carry; tooling is not something to
build around.

### The contract

```yaml
---
type: decisions # required
title: "Decisions: #2090 — Migrate auth"
description: One-line summary used in generated index pages
tags:
  - auth
  - pipeline
related:
  - "#2090"
repos: # workspace-level scope; empty means all repos
  - nightgauge
status: stable # draft | stable | deprecated  (default: stable)
superseded_by: "#2100" # set alongside status: deprecated
generated:
  by: feature-dev/claude-sonnet-5
  at: "2026-09-03T10:00:00Z"
verified:
  - by: process:retro
    at: "2026-09-04T10:00:00Z"
  - by: human:octocat
    at: "2026-09-05T10:00:00Z"
sources:
  - resource: https://github.com/nightgauge/nightgauge/issues/2090
    title: The issue
  - resource: /architecture/go-ts-parity.md
stale_after: "2027-01-01T00:00:00Z"
---
```

| Field           | Type         | Description                                                                  |
| --------------- | ------------ | ---------------------------------------------------------------------------- |
| `type`          | string       | **Required.** Entry kind. Unknown values are accepted (see tolerance below). |
| `title`         | string       | Human-readable title; falls back to the H1 when absent                       |
| `description`   | string       | One-line summary used in generated `index.md` pages                          |
| `tags`          | string[]     | Topic tags for discovery                                                     |
| `related`       | string[]     | Related issue/PR references (display format, e.g. `"#2090"`)                 |
| `repos`         | string[]     | Repository scope for workspace entries; empty means workspace-wide           |
| `status`        | enum         | `draft`, `stable` or `deprecated`. Absent means `stable`.                    |
| `superseded_by` | string       | Reference to the entry that replaces this one; use with `status: deprecated` |
| `generated`     | provenance   | Actor that produced the entry, and when                                      |
| `verified`      | provenance[] | Confirmation events, oldest first                                            |
| `sources`       | source[]     | Material the entry was derived from: `{resource, title?}`                    |
| `stale_after`   | RFC3339      | Instant past which the entry stops being current guidance                    |

`type` is the only required field. Everything else is optional, and unknown
keys are preserved (in `Raw` on the Go side, via `.passthrough()` on the
TypeScript side).

**Consumer tolerance.** Unknown frontmatter keys, unknown `type` values and
missing optional fields never fail parsing. An OKF consumer tolerates what it
does not understand rather than rejecting the entry — and the two layers must
agree on that, or an entry a future producer writes parses in one and fails in
the other.

**`created` and `updated` are deleted, not aliased.** `generated.at` is the
write timestamp — one stamp, not two that drift apart. The lifecycle status
that `deprecated` replaced is likewise rejected outright, with an error naming
the file, so an unmigrated entry surfaces instead of silently ranking as an
unknown status; migrate those to `status: deprecated` alongside
`superseded_by`.

### The actor convention

`generated.by` and every `verified[].by` is an actor string in one of three
forms:

| Form                   | Meaning                      | Example                            |
| ---------------------- | ---------------------------- | ---------------------------------- |
| `<producer>/<version>` | An agent stage and its model | `feature-planning/claude-sonnet-5` |
| `human:<id>`           | A person                     | `human:octocat`                    |
| `process:<id>`         | A deterministic writer       | `process:retro`                    |

Validated against `^([a-z0-9._-]+/[A-Za-z0-9._-]+|human:\S+|process:\S+)$` in
both layers (`okf.ValidActor` / `ActorSchema`).

**Actor strings are constructed, never quoted.** The binary builds them from
the stage name and served model it reads out of the pipeline context file
(`okf.StageActor`), or from a fixed process name (`okf.ProcessActor`). A value
is never taken from model output or issue text — otherwise an entry could claim
any provenance it liked, and the trust tier built on top of it would be
worthless.

### Conformance: enforcing the contract

A contract that is not enforced drifts back into two contracts within a month.
`nightgauge knowledge validate --conformance` is the single place that keeps
every writer honest, including model-driven stages, and it is exactly the
criterion an external Open Knowledge Format consumer would apply: **every
non-reserved `.md` under the knowledge root must carry a parseable frontmatter
block with a non-empty `type`.**

Reserved names are skipped — `index.md`, `log.md`, `README.md` and
`_template.md` are navigation and template files, read structurally rather than
as knowledge. Directories beginning with `.` (the recall cache) hold derived
state, not entries.

Three reason codes:

| Reason                    | Meaning                                                               |
| ------------------------- | --------------------------------------------------------------------- |
| `no_frontmatter`          | The file opens with no `---` block at all                             |
| `unparseable_frontmatter` | Malformed YAML, a missing closing sentinel, or a deleted status value |
| `missing_type`            | A parseable block whose `type` is absent or empty                     |

**Tolerance is deliberate.** Unknown keys, unknown `type` values and missing
optional fields are never violations — the check and the parser have to agree
about what the contract is, or an entry a future producer writes fails in one
and passes in the other.

```bash
# Sweep the whole base
nightgauge knowledge validate --conformance
nightgauge knowledge validate --conformance --json

# Migrate a base written before the contract existed
nightgauge knowledge validate --conformance --fix

# Per-issue: decisions population AND conformance for that issue's directory
nightgauge knowledge validate 42
```

`--fix` stamps a `type` inferred from the entry's path and
`generated.by: process:knowledge-migrate` onto every entry missing them,
leaving the body and any existing frontmatter untouched. An entry whose block
cannot be **parsed** is reported rather than rewritten: repairing malformed
YAML is not something to guess at, and a leftover deleted status value needs a
human decision about what replaced it.

#### Where it is enforced

The check runs **before every merge**, on the issue's own knowledge directory:

- the deterministic pr-merge runner (`internal/orchestrator/stages/prmerge.go`)
  punts with `knowledge-non-conformant` instead of merging;
- the pr-merge skill's Phase 6 include applies the same gate on the fallback
  path.

Both are needed. The deterministic runner is the **default** path — the skill
is reached only when that runner punts — so a gate living only in the skill
would be unenforced on every normal run.

**The gate is scoped to one issue's directory on purpose.** The knowledge base
is local, gitignored, per-machine state, so a stale entry belonging to an
unrelated issue must never block someone's merge. The whole-base sweep is the
standalone audit form.

### Recording provenance: `knowledge stamp`

`nightgauge knowledge stamp <path>` is the **only** writer of the provenance
fields. Skills and stages call it instead of editing frontmatter with `sed` or
a heredoc, so every value that lands in the base has been through the same
validation — an entry can never claim a provenance the binary did not
construct.

```bash
nightgauge knowledge stamp features/42-photo-upload/PRD.md \
  --generated-by feature-planning/claude-sonnet-5 \
  --source https://github.com/nightgauge/nightgauge/issues/42
```

| Flag             | Effect                                               |
| ---------------- | ---------------------------------------------------- |
| `--generated-by` | Replaces `generated` with this actor, stamped at now |
| `--verified-by`  | Appends a `verified` event                           |
| `--source`       | Appends to `sources`; repeatable                     |
| `--status`       | Sets `status` (`draft`, `stable`, `deprecated`)      |
| `--stale-after`  | Sets `stale_after` (RFC3339)                         |
| `--json`         | Prints the merged block and whether anything changed |

**The body is never touched.** The file is split, only the block is rebuilt,
and the two are re-joined.

**Merge rules**, which are what make the verb idempotent:

- `generated` is **replaced**. It names the last producer; it is not a log.
- `verified` appends only when no existing event has the same actor.
  De-duplication is on the actor **alone** — `at` differs on every run, so a
  `by`+`at` key would append forever and make retro non-idempotent.
- `sources` appends only when no existing entry has the same resource.
- `status` and `stale_after` are set only when the flag is passed. An omitted
  flag leaves the value alone; nothing is ever cleared.

**Input containment.** `<path>` must resolve inside `.nightgauge/knowledge/`.
Every other knowledge verb derives its path internally from an issue number or
a slug, so this is the first that could be pointed at an arbitrary file;
without the check, `knowledge stamp ../../../.zshrc --status stable` would
prepend a YAML block to a shell config. A `--source` is an `https://` URL, a
bundle-absolute path beginning with `/`, or a repository-relative path that
resolves inside the repository after cleaning and symlink evaluation — a
symlink pointing out of the tree is rejected. A rejected stamp writes nothing.

### The two automatic verification points

Two moments in the lifecycle append a `verified` event without anyone asking:

| Moment                             | Actor                        | What it means                                                            |
| ---------------------------------- | ---------------------------- | ------------------------------------------------------------------------ |
| `knowledge record-outcome` (retro) | `process:retro`              | Retro runs after the PR merged, so these decisions survived a real merge |
| `knowledge graduate`               | `process:knowledge-graduate` | The decision was distilled into `docs/` and opened as a graduation PR    |

Graduation deliberately does **not** stamp `human:<login>`. The graduation PR
opens with an unchecked reviewer checklist — the review is what the PR is
_for_, so at that moment no person has reviewed anything. Stamping a human
actor there would be a vacuous assertion in the one field whose entire value is
that a person really looked. An operator who did review the ADR records that
deliberately:

```bash
nightgauge knowledge graduate 42 --section docs/ARCHITECTURE.md#sse \
  --adr ADR-001 --verified-by human:octocat
```

### What the scaffold stamps

Every scaffold path — issue-tier `PRD.md` and `decisions.md`, repo-topic
templates, and the workspace scaffolder — writes the block through one
renderer (`okf.ScaffoldFrontmatter` / `scaffoldFrontmatter`), stamping:

```yaml
---
type: prd
title: "PRD: #2090 — Migrate auth"
status: draft
generated:
  by: process:knowledge-scaffold
  at: "2026-09-03T10:00:00Z"
---
```

Scaffolded content is a template nobody has reviewed, so it starts as a
`draft`. A stage or a person promotes it later.

Frontmatter is metadata, never substance: the boilerplate check behind
`knowledge prune` strips the block before measuring content, so a scaffolded
but untouched directory stays prunable.

### Title line convention

Both file types use a consistent H1 title format:

| File           | Title format                                 |
| -------------- | -------------------------------------------- |
| `PRD.md`       | `# PRD: #{issueNumber} — {issueTitle}`       |
| `decisions.md` | `# Decisions: #{issueNumber} — {issueTitle}` |

The em dash (`—`) separator is part of the template — use it verbatim.

### Expected top-level sections (H2)

**PRD.md** sections (in order):

1. `## Summary`
2. `## User Story`
3. `## Acceptance Criteria`
4. `## Technical Approach` (embedded TRD)
5. `## Quality & Non-Functional Requirements` (embedded QRD)
6. `## Out of Scope`
7. `## Status`

**decisions.md** sections:

1. `## Architecture Decisions` (required header)
2. `## ADR-001: [Title]`, `## ADR-002: [Title]`, … (one block per decision)

---

## Wiki-Link Format

Knowledge files may reference other issues or documents using wiki-link syntax.
The runtime resolver (`wikiLinkResolver.ts` / `internal/knowledge/wikilinks.go`)
rewrites these to standard Markdown links when rendering.

### Supported syntaxes

```text
[[relative/path]]         — File relative to the knowledge directory or the containing file
[[#NNNN]]                 — Issue-number reference: scans knowledge/features/ and knowledge/epics/
[[#NNNN#anchor]]          — Same as above with a section anchor (e.g. [[#2090#decisions]])
[[topic:glossary-term]]   — Glossary lookup: resolves to knowledge/glossary/{term}.md
[[product:slug]]          — Workspace product entry: resolves to knowledge/product/{slug}.md
[[cross-repo:slug]]       — Workspace cross-repo entry: resolves to knowledge/cross-repo/{slug}.md
[[architecture:slug]]     — Workspace architecture entry: resolves to knowledge/architecture/{slug}.md
[[repo-name:path]]        — Cross-repo link (TypeScript layer only; requires workspaceConfig)
```

### Resolution order

The resolver dispatches in this order (important: `topic:` is checked before
cross-repo to avoid misrouting):

1. `[[#NNNN]]` / `[[#NNNN#anchor]]` — issue-number scan
2. `[[topic:term]]` — glossary lookup
3. `[[product:slug]]`, `[[cross-repo:slug]]`, `[[architecture:slug]]` — workspace namespaces
4. `[[repo-name:path]]` — cross-repo (TypeScript only)
5. `[[relative/path]]` — local file resolution

### Broken link behavior

Unresolvable links are kept as-is (`[[...]]`) in the rendered output. A warning
is emitted to the pipeline log (not an error — broken links are non-blocking).

### Rendering via CLI

Use `nightgauge knowledge render <path>` to render a knowledge file with
wiki-links resolved to standard Markdown:

```bash
nightgauge knowledge render .nightgauge/knowledge/features/2959-kb-v2/decisions.md
```

Rendered output goes to stdout; warnings go to stderr.

### Display text rules

| Syntax                  | Display text           |
| ----------------------- | ---------------------- |
| `[[#NNNN]]`             | `#NNNN`                |
| `[[#NNNN#anchor]]`      | `#NNNN § anchor`       |
| `[[topic:term]]`        | `term`                 |
| `[[product:slug]]`      | `slug`                 |
| `[[cross-repo:slug]]`   | `slug`                 |
| `[[architecture:slug]]` | `slug`                 |
| `[[repo:path]]`         | `repo:basename`        |
| `[[relative/path]]`     | basename without `.md` |

---

## Roadmap / Planned Features

The items below are intentionally consolidated here so readers can distinguish
implemented behavior from planned behavior.

- **IMPLEMENTED** (#2960): Repo-topic categories `architecture/`, `glossary/`,
  `runbooks/`, and `post-mortems/` — scaffolded via `nightgauge knowledge new <type> <slug>`
  or `KnowledgeService.createRepoTopicEntry(type, slug)`. Each category uses flat
  `{slug}.md` files (no issue-number prefix). First entry auto-creates `README.md`
  and `_template.md` for the category.
- **NOT YET IMPLEMENTED**: `conversations/` subdirectory in the per-issue knowledge tree.
- **IMPLEMENTED** (#2963): Workspace-level KB auto-scaffold — three-category
  tree (`product/`, `cross-repo/`, `architecture/`) seeded via
  `nightgauge knowledge workspace-init`, auto-run at issue-pickup when
  `knowledge.workspace_scoped=true` (default). Wiki-link namespaces
  `[[product:x]]`, `[[cross-repo:x]]`, `[[architecture:x]]` resolve to the
  seeded entries.
- **NOT YET IMPLEMENTED**: Auto-frontmatter injection in `scaffoldForIssue`
  templates — frontmatter is opt-in/additive.
- **NOT YET IMPLEMENTED**: VSCode extension tree view for repo-topic categories
  (`architecture/`, `glossary/`, `runbooks/`, `post-mortems/`).

---

## Naming Conventions

### File names

Both file names are fixed — do not rename them:

| File         | Name (exact, case-sensitive) |
| ------------ | ---------------------------- |
| PRD          | `PRD.md`                     |
| Decision log | `decisions.md`               |

### Directory name

Each issue directory is named `{issueNumber}-{slug}` where:

- `{issueNumber}` is the GitHub issue number (integer)
- `{slug}` is derived from the issue title using the slug algorithm below

### Slug algorithm

The slug is generated by `KnowledgeService.generateSlug(title)`:

```text
1. Lowercase the full title string
2. Replace every run of non-alphanumeric characters ([^a-z0-9]+) with a hyphen
3. Strip any leading or trailing hyphens
4. Truncate to 50 characters
5. Strip any trailing hyphen left by the truncation
```

**Examples:**

| Issue title                                              | Generated slug                               |
| -------------------------------------------------------- | -------------------------------------------- |
| `Define knowledge directory schema`                      | `define-knowledge-directory-schema`          |
| `Fix: broken link in README!`                            | `fix-broken-link-in-readme`                  |
| `[Epic] Q3 Refactor — Multi-repo workspace improvements` | `epic-q3-refactor-multi-repo-workspace-impr` |

### Slug truncation

The slug algorithm truncates output to **50 characters**. This practical limit
ensures directory names remain manageable across different filesystems and tools.

When an issue title's slug exceeds 50 characters, it is silently truncated to
the first 50 characters. Two issues with titles that differ only after character
50 will generate identical slugs.

**The issue number prefix (`{issueNumber}-`) prevents collisions:**

| Issue # | Title                                                    | Slug (50-char limit)             |
| ------- | -------------------------------------------------------- | -------------------------------- |
| 1001    | `Define knowledge base directory schema and write docs`  | `define-knowledge-base-director` |
| 2500    | `Define knowledge base directory structure for features` | `define-knowledge-base-director` |

Despite identical slugs after truncation, the full directory names remain unique:

```text
1001-define-knowledge-base-director/
2500-define-knowledge-base-director/
```

**Implementation source**: `KnowledgeService.generateSlug()` in
`packages/nightgauge-sdk/src/services/KnowledgeService.ts`

### Category directories

| Condition                   | Category directory |
| --------------------------- | ------------------ |
| Issue has `type:epic` label | `epics/`           |
| All other issues            | `features/`        |

---

## Graduation Workflow

Graduation is the manual ritual for moving a distilled, cross-cutting decision
out of a per-issue `decisions.md` and into `docs/`. It is **retro-triggered and
human-judged** — the pipeline never auto-distills. See
[Information Architecture](#information-architecture) for the scope rule.

### When to graduate (identifying candidates)

During the `retro` stage, the reviewer reads `decisions.md` and flags any ADR
block that passes **all three** of these tests:

1. **Cross-cutting** — the decision applies beyond this single issue.
2. **Stable** — the rationale will still hold a year from now.
3. **Reader-facing** — an outside contributor would benefit from finding it
   under `docs/` by topic, not by the issue number that introduced it.

ADR blocks that fail any test stay in `decisions.md` and rely on
`nightgauge knowledge index` for discoverability.

### Where to land it

Pick the destination by category of the decision:

| Decision category                           | Destination                                              |
| ------------------------------------------- | -------------------------------------------------------- |
| System architecture / transport / data flow | `docs/ARCHITECTURE.md`                                   |
| Coding convention / style / pattern         | `docs/CODE_STANDARDS.md`                                 |
| Cross-repo integration pattern              | `docs/INTEGRATION_PATTERNS.md` or the topic-specific doc |
| Operational runbook / incident response     | `docs/HEALTH_MONITORING.md` or the relevant runbook page |
| Security posture                            | `standards/security.md`                                  |
| Process / workflow                          | `docs/GIT_WORKFLOW.md` or the relevant workflow doc      |

If no existing `docs/` page fits, create a new topic file under `docs/` rather
than stretching an unrelated one.

### Bidirectional backlink format

Graduation always writes **two** HTML comments — invisible in rendered Markdown,
greppable in source. They are idempotent markers that anchor the source to the
destination and back.

**In `decisions.md` (written by the CLI, under the source ADR heading):**

```markdown
## ADR-002: Use SSE over WebSockets

<!-- graduated-to: docs/ARCHITECTURE.md#sse-pipeline-events -->

**Status**: Accepted
...
```

**In the `docs/` destination (pasted by the human during distillation):**

```markdown
## SSE pipeline events

<!-- graduated-from: .nightgauge/knowledge/features/1234-foo/decisions.md#adr-002 -->

Distilled prose explaining SSE over WebSockets, with the rationale rewritten for
a long-lived audience ...
```

Comments are the link — the CLI will not overwrite rendered content. This keeps
graduation reversible and lets `grep -r "graduated-from"` audit coverage.

### CLI ritual

Graduation is delivered as two modes of the `nightgauge knowledge graduate`
command:

- **Auto-mode** (`--auto`, the default ritual) — selects a candidate from the
  ranked list produced by `knowledge graduate-candidates`, creates a branch,
  appends the verbatim Decision block to the destination doc, writes both
  marker comments, commits, pushes, opens a PR, applies the three default
  labels, and adds the PR to the project board with `Status=Ready`. The
  reviewer's job becomes "polish the prose in the PR" rather than "perform
  the ritual by hand."
- **Manual override** (no `--auto`) — the legacy ad-hoc path; prints the
  source ADR, writes the source-side backlink, and opens `$EDITOR` on the
  target doc so a human pastes the graduated-from marker and writes
  distilled prose.

#### Auto-mode (recommended)

```bash
# Graduate the top-scoring candidate
nightgauge knowledge graduate 1234 --auto

# Pick a specific candidate by index
nightgauge knowledge graduate 1234 --auto --adr-index 2

# Preview without making changes
nightgauge knowledge graduate 1234 --auto --dry-run --json

# Open one PR per qualifying candidate
nightgauge knowledge graduate 1234 --auto --all-candidates
```

Auto-mode is **idempotent**: re-running on an ADR whose `<!-- graduated-to: -->`
marker is already present returns `status: already_graduated` with the
existing open PR URL (or no PR when the prior PR was merged) without
creating any new artifacts.

When two or more candidates share the top score and no `--adr-index` is
provided, the command exits non-zero with `status: tie_unresolved` and
prints the tied indexes so the reviewer can pick one explicitly.

The verbatim Decision block is copied as-is into the destination doc — the
deterministic Go path never invokes an LLM. Reviewer polish happens in the
PR.

#### Manual override

```bash
nightgauge knowledge graduate 1234 \
  --section docs/ARCHITECTURE.md#sse-pipeline-events \
  --adr ADR-002
```

The command:

1. Prints the source ADR block to stdout so the human can copy distilled prose
   from it.
2. Writes the `<!-- graduated-to: ... -->` backlink under the ADR heading in
   `decisions.md` (idempotent — safe to re-run).
3. Opens `$EDITOR` on the target `docs/` file (or prints the path if `$EDITOR`
   is unset) so the human pastes the `<!-- graduated-from: ... -->` comment and
   writes the distilled prose.

Pass `--json` for a machine-readable output (skips the editor launch) — useful
when scripting bulk graduation reviews.

---

## Configuration

The knowledge base is controlled by flags in `.nightgauge/config.yaml`:

| Key                             | Type    | Default | Description                                                                                           |
| ------------------------------- | ------- | ------- | ----------------------------------------------------------------------------------------------------- |
| `knowledge.enabled`             | boolean | `false` | Master switch. Must be `true` for any scaffolding to occur.                                           |
| `knowledge.auto_scaffold`       | boolean | `true`  | When `true`, scaffold automatically on issue pickup. Requires `enabled: true`.                        |
| `knowledge.auto_index`          | boolean | `true`  | When `true`, regenerate `.nightgauge/knowledge/README.md` after a merge that touched knowledge files. |
| `knowledge.auto_prune_on_merge` | boolean | `true`  | When `true`, prune boilerplate-only knowledge directories after a successful merge.                   |

**Behavior matrix:**

| `enabled` | `auto_scaffold` | Effect                                                                       |
| --------- | --------------- | ---------------------------------------------------------------------------- |
| `false`   | any             | No scaffolding. `knowledge_path` = `null`.                                   |
| `true`    | unset           | Scaffold automatically on issue pickup — `auto_scaffold` defaults to `true`. |
| `true`    | `true`          | Scaffold automatically on issue pickup.                                      |
| `true`    | `false`         | Enabled but not automatic. Run `nightgauge knowledge scaffold` by hand.      |

Both flags are resolved by `config.KnowledgeConfig.IsAutoScaffold()`. Until
#1205 nothing in Go read `auto_scaffold` at all, so the flag had no effect in
either direction.

**Example `.nightgauge/config.yaml`:**

```yaml
knowledge:
  enabled: true
  auto_scaffold: true
```

> **Config placement:** Set `knowledge.enabled` in the project config
> (`.nightgauge/config.yaml`, tier 3) so it is shared with the team. See
> [CONFIGURATION.md](CONFIGURATION.md) for the full 6-tier precedence system.

**Environment variable overrides:**

```bash
export NIGHTGAUGE_KNOWLEDGE_ENABLED=true
export NIGHTGAUGE_KNOWLEDGE_AUTO_SCAFFOLD=false
```

---

## Pipeline Integration

The `knowledge_path` field flows through every pipeline context file. Each stage
receives the path and may enrich the knowledge directory.

### Context field reference

| Context file        | Schema version added | Fields                                                          |
| ------------------- | -------------------- | --------------------------------------------------------------- |
| `issue-{N}.json`    | v1.5                 | `knowledge_path: string \| null`                                |
| `planning-{N}.json` | v1.3                 | `knowledge_path: string \| null`, `knowledge_entries: string[]` |
| `dev-{N}.json`      | v1.5                 | `knowledge_path: string \| null`                                |
| `pr-{N}.json`       | v1.1                 | `knowledge_path: string \| null`                                |

`knowledge_path` is a path relative to the workspace root (e.g.,
`.nightgauge/knowledge/features/1673-knowledge-base-schema-doc`).

`knowledge_entries` (planning context only) is a list of **basenames only** of
markdown files in the knowledge directory when planning ran. For example:
`["PRD.md", "decisions.md"]`. These are **not** relative or absolute paths —
just filenames. The directory path is provided separately in the `knowledge_path`
field.

### Stage integration table

| Stage              | Reads                                                                                                      | Writes / Enriches                                                                                                                                          | Notes                                                                     |
| ------------------ | ---------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| `issue-pickup`     | Config flags and issue body                                                                                | Scaffolds directory, writes `PRD.md` and `decisions.md`, sets `knowledge_path` in `issue-{N}.json`                                                         | First scaffold; idempotent                                                |
| `feature-planning` | `knowledge_path` from issue context; scaffolded `PRD.md`                                                   | Enriches `PRD.md`, populates `decisions.md`, and records `knowledge_path` plus `knowledge_entries` in `planning-{N}.json`                                  | Also scaffolds the directory itself when issue-pickup deferred it         |
| `feature-dev`      | `knowledge_path` from planning or issue context; `PRD.md`; `decisions.md`; optional sibling-repo knowledge | Reads knowledge files before implementation and passes `knowledge_path` through in `dev-{N}.json`                                                          | No writes to the issue knowledge directory in this stage                  |
| `feature-validate` | `knowledge_path` from dev context                                                                          | Passes through                                                                                                                                             | No writes to knowledge directory                                          |
| `pr-create`        | `knowledge_path` and `knowledge_entries` from planning/dev context                                         | Builds a `## Knowledge` section in the PR body linking knowledge files and writes `knowledge_path` into `pr-{N}.json`                                      | Knowledge links are omitted entirely when there are no entries            |
| `pr-merge`         | `knowledge_path` from PR context                                                                           | No writes                                                                                                                                                  | Context files cleaned up after merge                                      |
| `retro`            | `knowledge_path` from issue context; prior pipeline history                                                | Auto-enables outcome recording when `knowledge_path` is present, appends `## Outcome` to `decisions.md` and stamps a `verified` event from `process:retro` | Creates the knowledge directory and a conformant `decisions.md` if absent |

### SDK Method Reference

This table cross-references SDK methods with their documented behaviors and
section locations:

| SDK Method                                    | Behavior                                                                    | Documentation                                                      |
| --------------------------------------------- | --------------------------------------------------------------------------- | ------------------------------------------------------------------ |
| `KnowledgeService.generateSlug(title)`        | Converts issue title to URL-safe slug; truncates to 50 characters           | [Slug truncation](#slug-truncation)                                |
| `KnowledgeService.contentIsSubstantive(text)` | Returns true if content has ≥30 chars of non-boilerplate text               | [Substantiveness Threshold](#substantiveness-threshold)            |
| `KnowledgeService.scaffoldForIssue()`         | Creates knowledge directory with `PRD.md` and `decisions.md`                | [Directory Naming](#directory-naming), [File Schema](#file-schema) |
| Planning context `knowledge_entries` field    | List of basenames of markdown files in knowledge directory at planning time | [Context field reference](#context-field-reference)                |

### Idempotency

Scaffolding is idempotent. If the knowledge directory already exists when
`scaffoldForIssue()` is called, the method returns the existing path without
error and without overwriting any files.

---

## Example Knowledge Entries

### Example PRD.md

For a realistic feature issue (#42 — "Add photo upload to profile page"):

```markdown
# PRD: #42 — Add photo upload to profile page

## Summary

Users currently cannot upload a profile photo. This issue adds a file input,
client-side validation, and S3 upload via the existing `AssetService`.

## User Story

As a registered user, I want to upload a profile photo so that my account feels
personalized and recognizable to teammates.

## Acceptance Criteria

- [ ] User can select a JPEG or PNG up to 5 MB
- [ ] Invalid file types show an inline error
- [ ] Uploaded photo appears in the profile header within 2 seconds
- [ ] Upload failures show a user-friendly error message

## Technical Approach

- Use `AssetService.saveAsset(file, type)` — type: `'profile-photo'`
- Max file size enforced client-side and server-side
- Resize to 256×256 px on upload using `sharp`
- Store path in `users.profile_photo_url` column

## Quality & Non-Functional Requirements

- Unit tests for client + server validation; integration test for the S3 upload
  path against a mock
- p95 upload-to-render latency < 2 s on a 5 MB image
- No EXIF/location metadata persisted from uploaded images (privacy)

## Out of Scope

- Cropping/rotation UI (separate issue)
- Animated formats (GIF/WebP) — JPEG/PNG only for now

## Status

- [x] Draft
- [ ] Reviewed
- [ ] Approved
```

### Example decisions.md

```markdown
# Decisions: #42 — Add photo upload to profile page

## Architecture Decisions

<!-- Record key technical decisions made during implementation -->

| Decision            | Options Considered                                | Selected              | Rationale                                                       |
| ------------------- | ------------------------------------------------- | --------------------- | --------------------------------------------------------------- |
| Storage backend     | S3, local disk, Cloudinary                        | S3 via AssetService   | Existing AssetService already handles S3; avoids new dependency |
| Resize timing       | Client-side, server-side on upload, lazy on serve | Server-side on upload | Consistent stored dimensions; lower serve latency               |
| Validation location | Client only, server only, both                    | Both                  | Client provides UX feedback; server enforces security boundary  |
```

---

## Migration Guide

### Adopting the knowledge base in an existing repository

1. **Enable in config**: Add the following to `.nightgauge/config.yaml`:

   ```yaml
   knowledge:
     enabled: true
     auto_scaffold: true
   ```

2. **Commit the config**: The config file is committed to the repository so the
   setting is shared with the team.

3. **New issues scaffold automatically**: Once enabled, the next time
   `/nightgauge-issue-pickup` runs, it will create
   `.nightgauge/knowledge/` and scaffold a directory for the picked-up
   issue.

4. **Existing issues**: Existing issues do not get knowledge directories
   automatically. To create one for an in-progress issue, re-run issue pickup
   for that issue, or create the directory structure manually following the
   [naming conventions](#naming-conventions) and [file schemas](#file-schema)
   above.

5. **Git tracking (your choice)**: The knowledge directory can be committed to
   git or gitignored — it is your team's choice. To ignore it, add to
   `.gitignore`:

   ```gitignore
   .nightgauge/knowledge/
   ```

   To track it, commit `.nightgauge/knowledge/` alongside your feature
   branches. Committed knowledge files provide a durable record of decisions
   even after issues are closed.

---

## Workspace-Level Knowledge Directory

Workspace-level knowledge lives at the **workspace root** — the directory
containing `.vscode/nightgauge-workspace.yaml`. It is distinct from
per-repository knowledge which lives in each repo's
`.nightgauge/knowledge/`.

Use workspace knowledge for content that spans multiple repositories:

- Product roadmaps and personas (shared across all repos)
- Cross-cutting architecture decisions (affecting two or more repos)

### Directory Structure

```text
<workspace-root>/.nightgauge/knowledge/
├── product/
│   └── {slug}/
│       ├── PRD.md
│       └── decisions.md
└── cross-repo/
    └── {slug}/
        ├── PRD.md
        └── decisions.md
```

**Category directories:**

| Directory     | Purpose                                                            |
| ------------- | ------------------------------------------------------------------ |
| `product/`    | Product roadmaps, personas, and product-wide decisions             |
| `cross-repo/` | Architecture decisions and features spanning multiple repositories |

### Frontmatter: the `repos` field

Workspace knowledge files carry the same single contract as every other entry
(see [Frontmatter](#frontmatter)). The field that matters at this tier is
`repos`, which declares which repositories the entry applies to:

```yaml
---
type: prd
title: "PRD: unified-auth"
status: draft
generated:
  by: process:knowledge-scaffold
  at: "2026-09-03T10:00:00Z"
repos:
  - nightgauge
  - acme-platform
---
```

When `repos` is omitted, the entry is considered workspace-wide (applies to all
repositories). When `repos` is present, it is a list of repository names
matching `repositories[].name` in `nightgauge-workspace.yaml`.

The frontmatter block must appear at the very top of the file (before the H1
title) and must be valid YAML.

> **Note:** `repos` is the only field whose meaning is tier-specific — it is
> machine-readable scope metadata that cannot be expressed as a Markdown
> section, and repo-level entries leave it empty. Workspace and repo tiers
> otherwise share one contract; they used to diverge, and that divergence is
> what the contract removed.

### Parsing

Frontmatter is parsed by `internal/knowledge/okf` (Go deterministic layer),
re-exported from `internal/knowledge` and mirrored by `KnowledgeEntrySchema` in
the SDK.

**`ParseFrontmatter(content string) (*FrontmatterBlock, error)`**

- Returns `nil, nil` when no `---` sentinel is found at the start of the file.
- Returns a `*FrontmatterBlock` with `Repos: nil` when frontmatter is present but the `repos` field is absent (workspace-wide entry).
- Returns an error when the YAML is malformed, the closing `---` sentinel is missing, or `repos` is not a list of strings.

**`ValidateRepos(repoNames []string, config *WorkspaceConfig) error`**

- Validates each repo name against `WorkspaceConfig.Repositories[].Name`.
- Returns a `*ValidationError` listing unknown names: `unknown repository names in frontmatter repos field: foo, bar`.
- Returns `nil` when `repoNames` is empty (workspace-wide entries bypass validation).

**Error messages:**

| Condition                | Error message                                                  |
| ------------------------ | -------------------------------------------------------------- |
| Malformed YAML           | `frontmatter: malformed YAML: <yaml error>`                    |
| Missing closing sentinel | `frontmatter: missing closing '---' sentinel`                  |
| `repos` not a list       | `frontmatter: repos must be a list of strings, got <type>`     |
| Unknown repo names       | `unknown repository names in frontmatter repos field: <names>` |

### File Schema

Workspace knowledge files use the same file names as repo-level files (`PRD.md`,
`decisions.md`) and the same H1 title format, with an optional frontmatter
prefix.

**PRD.md:**

```markdown
---
repos:
  - repo1
  - repo2
---

# PRD: {title}

## Summary

...
```

**decisions.md:**

```markdown
---
type: decisions
title: "Decisions: {title}"
status: draft
generated:
  by: process:knowledge-scaffold
  at: "{timestamp}"
repos:
  - repo1
---

# Decisions: {title}

## Architecture Decisions

...
```

### Naming Conventions

**Directory slug**: Use the same slug algorithm as repo-level entries (see
[Naming Conventions](#naming-conventions)). Slug source can be a topic name (not
necessarily a GitHub issue number) since workspace content may not map 1:1 to a
single issue.

**Conflict prevention**: Workspace categories (`product/`, `cross-repo/`) are
distinct from repo categories (`epics/`, `features/`). Do not create directories
named `epics/` or `features/` under the workspace knowledge root, and do not
create directories named `product/` or `cross-repo/` under a repo knowledge
root.

**Optional issue prefix**: When workspace content is created for a specific
GitHub epic or cross-repo issue, prefix the slug with the issue number for
traceability: `{issueNumber}-{slug}` (e.g., `1695-workspace-knowledge-schema`).

### Schema Comparison: Workspace vs. Repo-Level

| Property             | Repo-Level                                                      | Workspace-Level                                                                                             |
| -------------------- | --------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| Location             | `{repo}/.nightgauge/knowledge/`                                 | `<workspace-root>/.nightgauge/knowledge/`                                                                   |
| Categories           | `epics/`, `features/`                                           | `product/`, `cross-repo/`, `architecture/`                                                                  |
| Frontmatter          | One contract (`type` required; see [Frontmatter](#frontmatter)) | Same contract; `repos` declares the repository scope                                                        |
| Scope                | Single repository                                               | Multiple repositories                                                                                       |
| Pipeline scaffolding | Automatic (when enabled)                                        | Automatic via `nightgauge knowledge workspace-init` (auto-run at issue-pickup when `workspace_scoped=true`) |
| Slug source          | GitHub issue title                                              | Topic name or issue title                                                                                   |

### Anchor Repo

The **anchor repo** is the repository that holds the
`.vscode/nightgauge-workspace.yaml` marker. Workspace-level knowledge
(`.nightgauge/knowledge/{product,cross-repo,architecture}/`) is
physically stored inside the anchor repo and shared with sibling repos via
the anchor repo's git history.

For the current Nightgauge ecosystem, the marker lives inside the
`nightgauge/` repo itself, so `nightgauge/.nightgauge/knowledge/`
IS the workspace KB — tracked normally, no parent-directory git machinery
needed.

If the workspace marker sits in a parent directory that is not itself a repo
(e.g., `/Users/x/workspace/`), `nightgauge knowledge workspace-init`
still writes the tree at the workspace root but it will be untracked. Users
with this layout should either (a) move the marker into a dedicated anchor
repo, or (b) create a lightweight workspace-meta repo at the workspace root
to track the tree.

### Configuration

```yaml
# .nightgauge/config.yaml
knowledge:
  enabled: true # master switch for KB features
  workspace_scoped: true # auto-run workspace-init at issue-pickup (default: true)
```

- `workspace_scoped` defaults to `true` but is **gated** by `enabled`. Setting
  `enabled: false` disables the workspace auto-scaffold regardless of
  `workspace_scoped`.
- The manual CLI (`nightgauge knowledge workspace-init`) always works,
  regardless of config — anyone can bootstrap the tree on demand.

### Git Tracking

Like repo-level knowledge, workspace knowledge can be committed to git or
gitignored. To ignore workspace knowledge:

```gitignore
# In workspace root .gitignore
.nightgauge/knowledge/
```

To track it, commit `.nightgauge/knowledge/` in the workspace root.
Workspace knowledge is separate from each repo's knowledge directories — each
has its own git tracking decision.

## Boilerplate Pruning

Over time, knowledge directories accumulate that were scaffolded during
issue-pickup but never enriched — they contain only the template TODOs, empty
tables, and status checkboxes generated automatically. These boilerplate-only
directories dilute the knowledge base signal without adding value.

### Substantiveness Threshold

A knowledge file is considered **substantive** when it contains **≥ 30
characters of non-boilerplate content** after stripping:

- HTML comments (`<!-- ... -->`)
- Markdown headings (lines beginning with `#`)
- Empty table rows (lines containing only `|`, `-`, and spaces)
- Status checkboxes (`- [ ] ...` and `- [x] ...`)
- Whitespace

This mirrors the `contentIsSubstantive()` method in `KnowledgeService` (TypeScript SDK)
and `knowledgeContentIsSubstantive()` in the Go binary.

A knowledge directory is **boilerplate-only** when **no** `.md` file in the
directory meets the 30-character threshold. Such directories are candidates for
automatic pruning.

### Auto-Prune on Merge

When `knowledge.auto_prune_on_merge: true` (the default), the pr-merge skill
automatically removes boilerplate-only knowledge directories after a PR is
successfully merged and its issue is closed. This keeps the knowledge base tidy
without manual intervention.

To disable auto-pruning (for teams that want to retain full history):

```yaml
# .nightgauge/config.yaml
knowledge:
  auto_prune_on_merge: false
```

The prune step is **non-blocking** — if pruning fails, a warning is logged but
the merge is not affected.

### Auto-Index on Merge

When `knowledge.auto_index: true` (the default), the pr-merge skill
automatically regenerates `.nightgauge/knowledge/README.md` after a PR is
successfully merged that touched any knowledge files. The regenerated index is
committed as part of the merge so the table-of-contents stays browseable on
GitHub without manual maintenance.

The README index includes for each entry:

| Column        | Source                                                        |
| ------------- | ------------------------------------------------------------- |
| Issue         | Directory name (`{N}-{slug}` → `#N` with link)                |
| Type          | Category directory (`epics` → `epic`, `features` → `feature`) |
| Title         | First H1 heading in `PRD.md` (falls back to slug)             |
| Last Modified | Most-recent `.md` file modification date (ISO date)           |

To disable auto-indexing:

```yaml
# .nightgauge/config.yaml
knowledge:
  auto_index: false
```

The auto-index step is **non-blocking** — if regeneration fails, a warning is
logged but the merge is not rolled back.

### CLI: Manual Pruning

You can trigger pruning manually via the Go binary:

```bash
# Prune boilerplate-only directories for a specific issue
nightgauge knowledge prune-empty --issue 2892 --json

# Prune all boilerplate-only directories across the repo
nightgauge knowledge prune-empty --json
```

**Output** (JSON mode):

```json
{ "pruned": [".nightgauge/knowledge/features/2892-my-feature"] }
```

When no boilerplate-only directories are found, `pruned` is an empty array.

---

## Regeneration

When an issue evolves during implementation (requirements change, acceptance criteria are clarified), the PRD.md in the knowledge directory can become stale. The regeneration command refreshes it from the latest GitHub issue body without touching `decisions.md` or any existing frontmatter metadata.

### What Regeneration Does

- **Rewrites** `PRD.md` — re-renders the full PRD structure (Summary, User Story, Acceptance Criteria, Technical Approach, Quality & Non-Functional Requirements, Out of Scope, Status) from the current issue body via the shared `renderPrdBody()`, so regeneration can never drift from the scaffold. Legacy `## Technical Notes` in the issue body is mapped into `## Technical Approach`.
- **Preserves** existing YAML frontmatter (created date, tags, related_issues)
- **Bumps** the `updated` timestamp
- **Never touches** `decisions.md` — manually curated decisions are yours to own

> **Caution:** Regeneration rewrites the **entire** PRD body from the issue. Any
> in-place enrichment feature-planning wrote into `## Technical Approach`,
> `## Quality & Non-Functional Requirements`, or `## Out of Scope` is replaced
> with whatever those sections contain in the current issue body (a placeholder
> if absent). Use `--dry-run` first, and prefer regeneration when the issue body
> is the more current source. Hand-curated planning detail you want to keep
> belongs in `decisions.md`, which regeneration never touches.

### CLI: Manual Regeneration

```bash
# Regenerate from the latest issue body (auto-detects knowledge path)
nightgauge knowledge regenerate 2893

# Specify the knowledge path explicitly
nightgauge knowledge regenerate 2893 --knowledge-path .nightgauge/knowledge/features/2893-my-feature

# Preview changes without writing files
nightgauge knowledge regenerate 2893 --dry-run

# Output result as JSON
nightgauge knowledge regenerate 2893 --json
```

**Output** (JSON mode):

```json
{
  "regenerated": true,
  "files_updated": [".nightgauge/knowledge/features/2893-my-feature/PRD.md"],
  "prd_updated": true,
  "decisions_preserved": true,
  "timestamp": "2026-04-21T05:00:00Z"
}
```

### SDK API

```typescript
import { KnowledgeService } from "@nightgauge/sdk";

const svc = new KnowledgeService(workspaceRoot);
const result = await svc.regenerateForIssue(
  issueNumber,
  issueTitle,
  issueBody, // fresh issue body from GitHub
  knowledgePath // relative path to knowledge directory
);
// result.regenerated, result.filesUpdated, result.prdUpdated, result.decisionsPreserved
```

### When to Regenerate

Regeneration is **manual** by default. Run it when:

- Requirements changed significantly after the initial knowledge scaffold
- New acceptance criteria were added to the issue during review
- The PRD.md still shows TODO placeholders that the issue body now addresses

Automatic regeneration (triggered during `feature-planning` or `feature-dev`) is a future enhancement tracked separately.
