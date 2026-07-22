# Spike Artifact Contract

> Defines the structure of a `type:spike` artifact and the rules the
> `nightgauge spike materialize` subcommand uses to convert recommendations
> into actionable follow-up issues.

## Why this exists

Spikes (`type:spike` issues) historically produced unstructured Markdown
recommendations in `docs/spikes/{N}-*.md`. Nothing in the pipeline read those
recommendations and turned them into actionable backlog items, so a maintainer
had to manually re-read the artifact and file every follow-up issue by hand.

This contract makes spike output machine-parseable so the pipeline's
`spike-materialize` stage can deterministically file the follow-up issues.

## File location and naming

A spike artifact MUST live at:

```
docs/spikes/{issue-number}-{kebab-case-slug}.md
```

The materializer locates the artifact by scanning the merged spike PR's
changed files for entries matching `docs/spikes/{N}-*.md`. The number prefix
must match the spike issue number.

## Required structure

````markdown
# Spike #<N>: <Title>

**Issue**: #<N>
**Status**: Complete
**Date**: YYYY-MM-DD

## Executive Summary

<One-paragraph top-level recommendation. Reviewers read this before anything
else; keep it dense and actionable.>

## 1. Findings

<Free-form findings sections. Add as many numbered sections as needed.>

## Recommendations

```yaml recommendations
spike: <N>
recommendations:
  - id: <kebab-case-id>
    action: adopt
    title: "<issue title>"
    type: feature
    priority: high
    size: M
    labels: ["component:scheduler"]
    body: |
      Optional Markdown body. If absent, the materializer generates a stub
      that links back to the spike.
    depends_on: []
```
````

````

The materializer reads exactly **one** fenced block of language
`yaml recommendations` from the artifact. Any additional fenced YAML blocks
without the `recommendations` info string are ignored.

## YAML schema

### Top-level fields

| Field             | Required | Type     | Notes                                                 |
| ----------------- | -------- | -------- | ----------------------------------------------------- |
| `spike`           | yes      | integer  | Must match the spike issue number.                    |
| `recommendations` | yes      | sequence | Each entry is a Recommendation (see below).           |

### Recommendation entry

| Field        | Required | Type   | Allowed values / Notes                                                                |
| ------------ | -------- | ------ | ------------------------------------------------------------------------------------- |
| `id`         | yes      | string | Kebab-case (`[a-z0-9-]+`). Unique within the spike. Stable identity for idempotency.  |
| `action`     | yes      | enum   | `adopt` \| `defer` \| `skip`. `skip` records the recommendation but creates no issue. |
| `title`      | yes      | string | Non-empty. Becomes the issue title.                                                   |
| `type`       | yes      | enum   | `feature` \| `bug` \| `docs` \| `chore` \| `spike`. Becomes the `type:*` label.       |
| `priority`   | yes      | enum   | `critical` \| `high` \| `medium` \| `low`. Maps to project board Priority field.      |
| `size`       | yes      | enum   | `XS` \| `S` \| `M` \| `L` \| `XL`. Maps to project board Size field.                  |
| `labels`     | no       | seq    | Additional labels to apply (besides the `type:` label).                               |
| `body`       | no       | string | Markdown body. If absent, the materializer generates a stub.                          |
| `depends_on` | no       | seq    | List of `id`s of *other recommendations in this same spike*. Must not cycle.          |

Any field not listed above is rejected by the validator. This strict allowlist
is a security boundary — it prevents a malicious YAML block from injecting
unexpected fields into the issue creation flow.

## Validation rules

The validator (`validateSchema`) rejects an artifact if any of these are true:

1. `spike` is missing or not an integer.
2. `recommendations` is missing or empty.
3. Any required field on a recommendation is missing.
4. `id` is not kebab-case or duplicates another `id` in the same spike.
5. `action`, `type`, `priority`, or `size` is outside the allowed enum.
6. `depends_on` references an `id` that is not present in the spike.
7. `depends_on` produces a cycle (detected by topological sort).

A failed validation aborts materialization with a non-zero exit code and a
human-readable error pointing to the offending entry.

## Choosing Between Path A, B, and C

When `/nightgauge-issue-create` decomposes an epic that contains a spike
and implementation work depending on that spike's output, it must choose one of
three supported shapes.

| Path                                          | Default when                                                                                                              | Issue creation behavior                                                                                                                                                                                                                                  |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Path A — Materialization**                  | Same-repo epic; dependents can wait until the spike PR merges                                                             | Create the spike now; place dependents in the spike's `yaml recommendations` block so `spike materialize` creates them after the spike PR merges                                                                                                         |
| **Path C — Spike-with-implementation**        | **Default for cross-repo epics.** The architectural question is concrete enough to be answered while writing initial code | No standalone spike issue. The first dependent ticket's PR commits an ADR (`docs/decisions/{NNN}-{slug}.md`) alongside its initial implementation. Subsequent dependents block on the first ticket via native `blockedBy` and read the ADR after merge.  |
| **Path B — Concurrent implementation tickets** | Cross-repo epic where the design space is genuinely too open to commit code without a separate research pass (opt-in)     | Create the spike and dependent siblings now; prepend each sibling with `## Prerequisite Artifact` and wire `blockedBy` to the spike. Surfaces a single-point-of-failure risk if the spike's ADR is never authored — the issue-create skill warns first. |

Selection criteria, in order:

1. **Same-repo epic, dependents can wait** → Path A.
2. **Cross-repo epic, design specifiable in a ticket body, natural first
   ticket exists** → **Path C** (default for cross-repo).
3. **Cross-repo epic, design genuinely too open to commit code without an
   upfront research pass** → Path B (opt-in, with guard).

For mixed groups, the skill should surface the routing in its decomposition
preview. Same-repo dependents can use Path A; cross-repo dependents default to
Path C unless the user explicitly opts into Path B for a true upfront research
pass.

> **Why Path C is the default for cross-repo**: Path B turns a single
> human-only spike into a single point of failure for the entire cross-repo
> epic. If the spike's ADR is never written, every dependent ticket remains
> blocked. Path C eliminates the separate spike issue. See the
> [Path C worked example](#path-c-worked-example).

## Path A — Materialization

For each recommendation, in topological order by `depends_on`:

| Action  | Behavior                                                                                                          |
| ------- | ----------------------------------------------------------------------------------------------------------------- |
| `adopt` | Create issue with Status=Ready. File as sub-issue of the spike. Apply `blockedBy` chain.                          |
| `defer` | Create issue with Status=Backlog. File as sub-issue of the spike. Apply `blockedBy` chain.                        |
| `skip`  | No issue is created. The recommendation is logged in the materializer's output and in the spike PR's comment.     |

Each created issue body begins with the idempotency marker:

```html
<!-- spike-recommendation: id=<id> spike=#<N> -->
````

Followed by the `body` from the YAML entry (or a generated stub linking back to
the spike).

## Idempotency

`spike materialize` is safe to re-run. Before creating an issue, the
materializer queries the spike's sub-issues and checks each issue body for the
marker `<!-- spike-recommendation: id=<id> spike=#<N> -->`. If a match is
found, the recommendation is skipped (already materialized).

Re-runs will:

- Skip recommendations whose marker is already present (no duplicate issues).
- Create any recommendations that are missing (e.g., a partial earlier run).
- Re-apply `blockedBy` chains where one side already exists (idempotent at the
  GraphQL layer — GitHub silently no-ops duplicates).

## Path C — Spike-with-implementation

Path C is the default route for cross-repo epics. There is **no standalone
`type:spike` issue**. Instead, the architectural decision is committed as an
ADR (`docs/decisions/{NNN}-{slug}.md`) inside the **first dependent
implementation ticket's PR**, alongside that ticket's initial implementation.
Subsequent dependent tickets block on the first ticket via native `blockedBy`
and read the ADR file once it lands.

### When to use Path C

Path C is the right choice when **all** of these hold:

- The architectural question is concrete enough to be answered while writing
  real code — i.e., the decision can be specified, justified, and exercised in
  the first ticket's body and PR.
- A natural "first ticket" exists that produces both the initial code surface
  and the ADR (typically the foundational data model, schema, or contract that
  later dependents consume).
- The remaining dependents can sensibly block on that first ticket via native
  `blockedBy`.

### When NOT to use Path C

- The design space is genuinely too open to commit any code without a separate
  research/design pass → use [Path B](#path-b--concurrent-implementation-tickets)
  with the issue-create skill's guard.
- The epic is fully same-repo and the dependents can wait until a spike PR
  merges → use [Path A](#path-a--materialization).

### Mechanics

The first dependent implementation ticket's body MUST begin with:

```markdown
## Architectural Decision Required

This ticket's PR is responsible for committing
`docs/decisions/{NNN}-{slug}.md`. The ADR records the decision needed by the
rest of this epic. Subsequent dependent tickets block on this ticket via
native `blockedBy` and read the ADR file once this PR has merged.

**Question(s) to answer in the ADR**:

- <one-line statement of each architectural question>
```

All other dependent ticket bodies MUST begin with:

```markdown
## Prerequisite ADR

**`docs/decisions/{NNN}-{slug}.md`** — produced by `#<first-ticket>`'s PR.
The pipeline planning stage MUST read this file before drafting a plan. If
the file does not exist on disk, the first ticket has not merged and this
ticket is not actionable.
```

The skill MUST wire native GitHub `blockedBy` from each subsequent dependent
to the first dependent ticket (not to a spike — there is no spike). Body text
alone is not enough; the pipeline scheduler reads GitHub's native dependency
graph.

Path C does **not** use `nightgauge spike materialize`. There is no
spike issue, no `yaml recommendations` block, and no materializer step. The
materializer's contract is unchanged.

### Trade-offs

Path C couples the architectural decision to real code that exercises it —
the property that makes it robust against the Path B failure mode. The
trade-off is that the first dependent ticket's PR review becomes overweight:
reviewers see the ADR plus initial implementation in a single review.
Reviewers should treat the ADR as a separate reviewable artifact within the
PR, calling it out explicitly in PR review comments.

### No retroactive migration

Existing Path B epics in the wild are grandfathered. The change is
forward-only — only future decompositions adopt Path C as the cross-repo
default. Re-routing an in-progress Path B epic to Path C is not supported and
should not be attempted.

## Path B — Concurrent Implementation Tickets

> **Path B is opt-in.** The default for cross-repo epics is
> [Path C](#path-c--spike-with-implementation). Choose Path B only when the
> design space is genuinely too open to commit code in the same PR — that is,
> when the architectural decisions truly require a separate, upfront research
> pass before any implementation can begin. The issue-create skill surfaces a
> single-point-of-failure warning before applying Path B and requires explicit
> confirmation; see the [#328 incident](#path-c-worked-example) for the
> failure mode this guard exists to prevent.

Path B is for cases where dependent implementation tickets must exist before
the spike PR merges and the design is too open to bundle into a first-ticket
ADR. Common reasons:

- The dependent tickets live in another repository, **and** the architectural
  decisions cannot be specified inline as part of an initial implementation.
- The team needs an explicit research/design phase before any code is written.
- A user explicitly overrides the cross-repo Path C default.

Path B does **not** use `nightgauge spike materialize` for the dependent
siblings. Those siblings are real GitHub issues from the moment the epic is
decomposed, so the materializer must not create duplicates for them later.

Each Path B dependent issue body MUST begin with:

```markdown
## Prerequisite Artifact

**`<spike-artifact-path>`** — produced by `<spike issue ref>`.
The pipeline planning stage MUST read this file before drafting a plan. If
the file does not exist on disk, the spike has not landed and this ticket
is not actionable.
```

The skill MUST also wire native GitHub `blockedBy` from each dependent sibling
to the spike issue. Body text alone is not enough; the pipeline scheduler reads
GitHub's native dependency graph.

Path B has weaker idempotency than Path A. The idempotency marker only applies
to materializer-created issues, so re-running issue creation against the same
epic intent can create duplicate siblings unless the skill detects existing
matching titles and skips them in the decomposition preview.

## Dry-run mode

`nightgauge spike materialize <N> --dry-run` performs all parsing,
validation, and idempotency lookups but issues **no GraphQL mutations**. It
prints (or, with `--json`, emits) the plan: which issues would be created, which
would be skipped (already exist or `action: skip`), and which `blockedBy` edges
would be added.

The `feature-validate` stage uses dry-run as a contract gate — it fails the
validation if the artifact doesn't have a parseable YAML block, so a spike
cannot reach `pr-merge` without a valid recommendations block.

## Worked example

A complete spike with a two-step migration plan where the second step depends
on the first:

````markdown
# Spike #4042: Evaluate switching the scheduler to event-driven dispatch

**Issue**: #4042
**Status**: Complete
**Date**: 2026-04-12

## Executive Summary

Event-driven dispatch reduces idle polling cost by ~70%. Adopt incrementally:
add a Dispatcher interface first, then migrate the scheduler.

## 1. Findings

…

## Recommendations

```yaml recommendations
spike: 4042
recommendations:
  - id: dispatcher-interface
    action: adopt
    title: "scheduler: introduce Dispatcher interface for event-driven dispatch"
    type: feature
    priority: high
    size: M
    labels: ["component:scheduler"]
    body: |
      Define a Dispatcher interface and adapt the existing polling scheduler to
      use it (no behavior change). Sets up the seam for the event-driven impl.
    depends_on: []
  - id: event-driven-impl
    action: adopt
    title: "scheduler: implement event-driven dispatcher"
    type: feature
    priority: high
    size: L
    labels: ["component:scheduler"]
    body: |
      Replace the polling loop with an event-driven Dispatcher impl using the
      interface from #<dispatcher-interface>.
    depends_on: ["dispatcher-interface"]
  - id: deprecate-polling
    action: defer
    title: "scheduler: remove legacy polling code path"
    type: chore
    priority: low
    size: S
    body: |
      After event-driven dispatch has been stable for one release cycle, remove
      the legacy polling code path.
    depends_on: ["event-driven-impl"]
```
````

`nightgauge spike materialize 4042` would create three issues (two with
Status=Ready, one with Status=Backlog), link all three as sub-issues of #4042,
and add `blockedBy` edges so `event-driven-impl` is blocked by
`dispatcher-interface` and `deprecate-polling` is blocked by `event-driven-impl`.

### Path B worked example

A cross-repo dashboard epic needs a workspace data model spike in the dashboard
repo and two dependent implementation tickets in companion repos. The skill
creates the spike issue and both siblings immediately, but each sibling starts
with the prerequisite artifact section and is blocked by the spike.

Spike issue:

````markdown
# spike: design workspace data model

## Spike Outputs

**Artifact**: [`docs/spikes/329-design-workspace-data-model.md`](docs/spikes/329-design-workspace-data-model.md)

## Recommendations

```yaml recommendations
spike: 329
recommendations:
  - id: workspace-model-decision
    action: skip
    title: "workspace data model decision recorded in prerequisite artifact"
    type: docs
    priority: high
    size: S
    body: |
      Path B created the dependent implementation tickets as concurrent
      siblings. See the artifact for the recommendation they must read.
    depends_on: []
```
````

Dependent sibling in another repo:

```markdown
## Prerequisite Artifact

**`docs/spikes/329-design-workspace-data-model.md`** — produced by
`acme/acme-web#200`.
The pipeline planning stage MUST read this file before drafting a plan. If
the file does not exist on disk, the spike has not landed and this ticket
is not actionable.

## Summary

Implement the workspace CRUD API using the data model chosen by the spike.
```

The sibling is created as a sub-issue of the epic and has native `blockedBy`
pointing at the spike. `internal/cmd/spike/materialize.go` is unchanged for this
path because Path B bypasses the materializer for dependent siblings.

### Path C worked example

Consider a synthetic cross-repository workspace epic that would otherwise use a
single `type:spike` issue and several dependent implementation tickets. Under
Path C:

- **No spike issue is created.** `#329` does not exist.
- The epic's first dependent ticket is selected as the **ADR-bearing
  ticket** — for example, `dashboard: workspace CRUD API (#330)`. Its body
  begins with the `## Architectural Decision Required` section listing the
  workspace data model questions the ADR must answer (entity shape,
  ownership boundary, multi-tenant isolation strategy).
- The remaining dependent tickets each begin with a `## Prerequisite ADR`
  section pointing to `docs/decisions/042-workspace-data-model.md` and have
  native `blockedBy` wired to **`#330`** (not to a spike).
- When `#330` is implemented, the ADR is committed inside its PR alongside
  the initial CRUD implementation. The ADR becomes a load-bearing artifact
  exercised by real code, not a separate document that can be skipped.
- After `#330` merges, the other 10 tickets unblock and read
  `docs/decisions/042-workspace-data-model.md` from disk during their own
  planning stages.

ADR-bearing first ticket body:

```markdown
## Architectural Decision Required

This ticket's PR is responsible for committing
`docs/decisions/042-workspace-data-model.md`. The ADR records the workspace
data model decision needed by the rest of this epic. Subsequent dependent
tickets block on this ticket via native `blockedBy` and read the ADR file
once this PR has merged.

**Question(s) to answer in the ADR**:

- What is the workspace entity shape (single table vs. workspace + member
  join)?
- Where does the ownership boundary live (workspace owns members, or member
  owns workspace memberships)?
- How is multi-tenant isolation enforced at the query layer?

## Summary

Implement the dashboard workspace CRUD API using the data model decided in
this ticket's ADR.
```

Subsequent dependent ticket body (in another repo):

```markdown
## Prerequisite ADR

**`docs/decisions/042-workspace-data-model.md`** — produced by `#330`'s PR.
The pipeline planning stage MUST read this file before drafting a plan. If
the file does not exist on disk, the first ticket has not merged and this
ticket is not actionable.

## Summary

Implement the platform workspace API surface using the data model defined in
the ADR.
```

The 10 dependents have native `blockedBy` pointing to `#330` (not a spike).
`internal/cmd/spike/materialize.go` is unchanged — Path C bypasses the
materializer entirely, exactly as Path B does for siblings.

## Spike Creation (issue-create skill)

The `nightgauge-issue-create` skill (Phase 2.X) scaffolds every new
`type:spike` issue to be contract-conformant by default. You do not need to
author the artifact path or YAML block manually when using the skill.

### What the skill provides at creation time

| What                                     | Where in the issue body               |
| ---------------------------------------- | ------------------------------------- |
| Pre-declared artifact path link          | After the Acceptance Criteria section |
| Placeholder `yaml recommendations` block | At the bottom of the issue body       |

The placeholder YAML block is valid, parseable, and satisfies the schema
defined in this document — `feature-validate` will not block the merge due to
a missing or malformed block, even before the spike author fills in real
recommendations.

### Artifact path auto-numbering

| Artifact type     | Path pattern                             | Prefix source                                   |
| ----------------- | ---------------------------------------- | ----------------------------------------------- |
| `spike` (default) | `docs/spikes/{issue-number}-{slug}.md`   | GitHub issue number (required by this contract) |
| `adr`             | `docs/decisions/{NNN}-{slug}.md`         | Sequential scan of `docs/decisions/`            |
| `research`        | `docs/research/{issue-number}-{slug}.md` | GitHub issue number                             |

The slug is auto-generated from the issue title (kebab-case, 60-char max). Use
`--spike-slug` to override.

### Validation at create time

The skill rejects issue creation if the artifact path:

1. **Collides** with an existing file — use a different slug or check for
   a duplicate spike with the same issue number
2. **Sits outside** the three allowed directories (`docs/spikes/`,
   `docs/decisions/`, `docs/research/`)
3. **Is malformed** — does not match `{N}-{slug}.md` pattern

### Spike + concurrent implementation tickets

When a single epic decomposition contains both a spike and implementation work
that depends on the spike output (or, for Path C, contains dependent
implementation work where the architectural decision can be committed inline),
the skill uses the routing rules in
[Choosing Between Path A, B, and C](#choosing-between-path-a-b-and-c).

- **Path A** inserts same-repo dependents into the spike's YAML block as
  `action: adopt` recommendations and does not create them as siblings yet.
- **Path C** (default for cross-repo) creates no standalone spike issue. The
  first dependent ticket bears the `## Architectural Decision Required`
  section; subsequent dependents carry `## Prerequisite ADR` and have native
  `blockedBy` wired to the first ticket.
- **Path B** (opt-in) creates the dependent siblings now, prepends
  `## Prerequisite Artifact`, and wires native `blockedBy` from each
  dependent to the spike. The skill surfaces a single-point-of-failure
  warning before applying Path B and requires explicit confirmation.

The decomposition preview must record the chosen path, the spike artifact path
(Path A/B) or the planned ADR path and ADR-bearing ticket (Path C), and the
dependent tickets affected before the skill creates any issues.

---

## Grandfathered artifacts

Spike artifacts that pre-date this contract (`#1065`, `#1665`, `#1666`,
`#1669`, `#2053`) are grandfathered: the `spike-materialize` stage only fires
on the post-merge path going forward, so historical spikes are not
re-processed. To materialize follow-ups for a historical spike, add a YAML
recommendations block in a follow-up PR; the next merge will trigger the
stage.

## Security

The YAML block comes from a merged PR (already passed code review), but the
materializer still validates against the strict allowlist above and never
`exec`s strings from YAML. The set of accepted top-level fields is closed —
unknown fields cause validation failure.
