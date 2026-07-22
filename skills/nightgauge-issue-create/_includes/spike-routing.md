# Phases 2.7 & 2.X: Spike Routing & Artifact Path — Procedural Detail

Detail bodies for Phase 2.7 (Spike + Dependent Implementation Routing, Paths A/B/C) and Phase 2.X (Spike Artifact Path Selection) of the `nightgauge-issue-create` skill.

## Contents

- [Phase 2.7: Spike + Dependent Implementation Routing](#phase-27-spike--dependent-implementation-routing)
- [Phase 2.X: Spike Artifact Path Selection](#phase-2x-spike-artifact-path-selection)

## Phase 2.7: Spike + Dependent Implementation Routing

**Gate**: Runs when creating an epic that contains either (a) at least one
`type:spike` sub-issue and at least one non-spike sub-issue, or (b) two or
more cross-repo non-spike sub-issues that share an unstated architectural
decision (Path C may apply even without an explicit spike sub-issue). Run
this phase after Phase 2.5 or 2.6 so dependency heuristics are available, and
before Phase 2.8 so cross-repo assumptions can be reflected in the routing
preview.

This phase prevents the invalid shape where a spike and its dependent
implementation tickets are created as peer sub-issues with no contract wiring,
and prevents the cross-repo single-point-of-failure shape where 11 tickets
block on a single human-only spike that may never be authored.

The skill MUST route each dependent group through exactly one of the three
paths defined in
[docs/SPIKE_CONTRACT.md](../../../docs/SPIKE_CONTRACT.md#choosing-between-path-a-b-and-c):

| Path                                                            | Use when                                                                                                                    | Result                                                                                                                                                                                                                               |
| --------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Path A — Recommendations**                                    | Same-repo dependents; dependents can wait until the spike PR merges                                                         | Dependent implementation tickets are not created now; they become `adopt` entries in the spike's `yaml recommendations` block                                                                                                        |
| **Path C — Spike-with-implementation** (default for cross-repo) | Cross-repo dependents and a natural first ticket exists that can produce both initial code and an ADR                       | No standalone spike issue is created. The first dependent ticket carries `## Architectural Decision Required` and commits the ADR inside its PR. Subsequent dependents carry `## Prerequisite ADR` and `blockedBy` the first ticket. |
| **Path B — Concurrent siblings with auto-cite**                 | Cross-repo dependents where the design space is genuinely too open to commit code without a separate research pass (opt-in) | Dependent implementation tickets are created now, each with a prerequisite artifact section and `blockedBy` pointing at the spike. **Triggers the Path B guard (Step 2.7.2a)** before any issues are created.                        |

#### Step 2.7.1: Detect Spike/Dependent Groups

For each spike sub-issue candidate:

1. Ensure Phase 2.X has already computed the spike artifact path.
2. Compare every non-spike sub-issue against the spike using these signals:
   - Explicit dependency metadata from Phase 2.5 or 2.6 (`depends_on`,
     `blocked_by`, wave ordering)
   - Technical Notes or Acceptance Criteria mention the spike title, spike
     slug, artifact path, ADR path, "read first", "recommendation", or
     "decision"
   - The implementation issue says it is blocked by, follows, assumes, or
     requires the spike output
3. If any signal matches, assign that implementation issue to the spike's
   dependent group.

The heuristic is intentionally conservative. If evidence is weak but plausible,
surface the group in the decomposition preview and ask the user to confirm
routing instead of silently creating siblings.

#### Step 2.7.2: Choose Path A, B, or C

Apply this decision tree in order. The first rule that matches selects the
default. The user may override interactively; headless runs commit to the
default deterministically.

1. **Same-repo epic, dependents can wait until the spike PR merges**
   → default **Path A**.
2. **Cross-repo epic, the architectural decision can be specified inline as
   part of an initial implementation, AND a natural first dependent ticket
   exists that produces both code and the ADR** → default **Path C**.
3. **Cross-repo epic, the design space is genuinely too open to commit code
   without a separate research/design pass** → default **Path B** (triggers
   the Path B guard in Step 2.7.2a).
4. **Mixed same-repo and cross-repo dependents** → default **Path C** for the
   cross-repo subset and **Path A** for the same-repo subset; present the
   split clearly so the user can override.

When running interactively, prompt before creation:

```text
Spike-or-ADR dependency group detected:
  Group: workspace data model decision
  Repo set: dashboard, platform, flutter (cross-repo)
  Dependents: workspace CRUD API (#330 candidate), platform workspace API,
              flutter workspace selector, … (8 more)

Choose routing:
  A) Recommendations: create only the spike now; materialize dependents
     after the spike PR merges (same-repo only)
  C) Spike-with-implementation: skip the spike issue; first dependent
     commits the ADR inside its PR; other dependents blockedBy the first
     dependent
  B) Concurrent siblings: create the spike and dependent siblings now
     with Prerequisite Artifact sections (opt-in — triggers Path B guard)

Default: C (cross-repo, natural first ticket exists)
```

When running headless, choose the default above and record the reason in the
decomposition preview. Do not stop to ask. If the default is Path B (rule 3
matched), still apply the Path B guard in Step 2.7.2a — the guard runs even
in headless mode and requires either an explicit `--accept-path-b-risk` flag
on the invocation or a documented rationale recorded in the preview.

#### Step 2.7.2a: Path B Guard

**Gate**: Runs whenever Path B is selected, whether by default (rule 3) or by
user override.

Path B turns a single human-only spike into a single point of failure for
the entire dependent group. If the spike's ADR is never authored, every
dependent ticket remains blocked. Before applying Path B, this guard surfaces
that risk and requires explicit confirmation.

**Interactive mode** — display the warning and require the user to type
`yes` (or select "Confirm Path B") before proceeding:

```text
WARNING — Path B single-point-of-failure risk

You are about to create a spike issue with N dependent siblings wired via
native blockedBy. The dependents cannot be planned by the autonomous
orchestrator until the spike's prerequisite artifact exists on disk.

If the spike's artifact is never authored, every dependent stalls. This is
the #328 failure mode — 11 cross-repo tickets blocked for weeks because the
spike's ADR was never written.

Path C is the recommended default for cross-repo epics. It eliminates the
spike issue entirely: the first dependent ticket commits the ADR inside
its own PR, and subsequent dependents block on that ticket (not on a
spike).

Choose Path B only if the architectural decisions are genuinely too open
to specify in a ticket body — i.e., a true upfront research/design pass
is required.

Continue with Path B? [yes / switch to Path C]
```

**Headless mode** — the guard fails closed unless the invocation passes
`--accept-path-b-risk` (or sets `NIGHTGAUGE_ACCEPT_PATH_B_RISK=1`).
Without explicit acknowledgement, the skill exits with a non-zero status
and instructs the operator to either re-run with the flag or switch to
Path C. The decomposition preview MUST record the rationale (e.g.,
"design space too open: <one-line reason>") even when the flag is set.

#### Step 2.7.3: Record Routing in the Decomposition Preview

The preview MUST include:

- For Path A/B: spike title and pre-declared artifact path
- For Path C: ADR-bearing first ticket (title, repo, planned issue number)
  and planned ADR path (`docs/decisions/{NNN}-{slug}.md`)
- Detected dependent issue titles and repositories
- Selected path and whether it was the default or an override (for Path B,
  whether the guard was triggered and how it was acknowledged)
- For Path A: the recommendation IDs that will be inserted into the spike YAML
- For Path B: the exact `## Prerequisite Artifact` section that will be
  prepended and the `blockedBy` relationship that will be applied
- For Path C: the exact `## Architectural Decision Required` section that
  will be inserted into the first ticket's body, the `## Prerequisite ADR`
  section that will be prepended to subsequent dependents, and the
  `blockedBy` relationships that will be applied to the first ticket (not a
  spike)

Example preview (Path A):

```markdown
## Spike Routing Preview

- Spike: `spike: design workspace data model`
- Artifact: `docs/spikes/329-design-workspace-data-model.md`
- Selected path: Path A — Recommendations (default: same repo)
- Dependents withheld from sibling creation:
  - `workspace: implement CRUD API` → recommendation id `workspace-crud-api`
  - `dashboard: add workspace selector` → recommendation id `workspace-selector`
```

Example preview (Path C):

```markdown
## Spike Routing Preview

- Selected path: Path C — Spike-with-implementation (default: cross-repo)
- ADR-bearing first ticket: `dashboard: workspace CRUD API`
  (repo `acme/dashboard`)
- Planned ADR path: `docs/decisions/042-workspace-data-model.md`
- ADR questions to answer:
  - workspace entity shape
  - ownership boundary
  - multi-tenant isolation strategy
- Subsequent dependents (block on first ticket):
  - `platform: expose workspace API`
  - `flutter: add workspace selector`
  - … (8 more)
- No standalone spike issue created.
```

#### Step 2.7.4: Apply Path A — Recommendations

For Path A:

1. Remove the dependent implementation issues from the Phase 3 sibling creation
   list.
2. Insert each dependent into the spike's `yaml recommendations` block with:
   - stable kebab-case `id`
   - `action: adopt`
   - original `title`, `type`, `priority`, `size`, and labels
   - original body under `body: |`
   - `depends_on` rewritten to recommendation IDs when dependencies point to
     other recommendations in the same spike
3. Keep non-dependent sub-issues in the normal sibling creation list.

Path A example YAML:

```yaml recommendations
spike: 329
recommendations:
  - id: workspace-crud-api
    action: adopt
    title: "workspace: implement CRUD API"
    type: feature
    priority: high
    size: M
    labels: ["component:api"]
    body: |
      Implement the API using the workspace model selected by the spike.
    depends_on: []
  - id: workspace-selector
    action: adopt
    title: "dashboard: add workspace selector"
    type: feature
    priority: high
    size: M
    labels: ["component:dashboard"]
    body: |
      Add the selector after the workspace CRUD API exists.
    depends_on: ["workspace-crud-api"]
```

#### Step 2.7.5: Apply Path B — Concurrent Siblings with Auto-Cite

For Path B:

1. Keep dependent implementation issues in the Phase 3 sibling creation list.
2. Prepend this section to each dependent issue body:

   ```markdown
   ## Prerequisite Artifact

   **`<spike-artifact-path>`** — produced by `<spike issue ref>`.
   The pipeline planning stage MUST read this file before drafting a plan. If
   the file does not exist on disk, the spike has not landed and this ticket
   is not actionable.
   ```

3. Pass `--blocked-by <spike-number>` when creating each dependent sibling, or
   call `nightgauge issue add-blocked-by <dependent> <spike>` after both
   issues exist.
4. Keep the spike's YAML scaffold present. It may contain `skip` or `defer`
   recommendations for non-materialized findings, but the Path B siblings are
   not represented as materializer-created recommendations.

#### Step 2.7.6: Interaction with Parallel Analysis

`--parallel` and `agent_teams` analysis still run. Their dependency graph feeds
this phase before sibling creation:

- Path A removes dependent implementation issues from the execution waves
  because they do not exist yet.
- Path B keeps the issues in the waves but forces the spike to be in an earlier
  wave and applies native `blockedBy` relationships.
- Path C keeps every dependent in the waves but pins the ADR-bearing first
  ticket to wave 1 and forces all subsequent dependents into wave 2 or later
  via native `blockedBy` to the first ticket.
- File conflict warnings remain valid for Path B and Path C dependents and
  should still be shown in the preview.

#### Step 2.7.7: Apply Path C — Spike-with-implementation

For Path C:

1. **Drop the spike sub-issue from the creation list entirely.** No
   `type:spike` issue is created. No artifact path is declared in
   `docs/spikes/`. The Phase 2.X spike scaffolding is skipped for this
   group — Path C uses an ADR (`docs/decisions/{NNN}-{slug}.md`) instead.
2. **Select the ADR-bearing first ticket.** Use the wave-1 dependent from
   Phase 2.5/2.6 as the default. If multiple wave-1 candidates exist, prefer
   the one in the spike repo (or the dashboard repo for cross-repo dashboard
   epics). Allow the user to override interactively by selecting a different
   dependent.
3. **Compute the ADR path.** Sequentially scan `docs/decisions/` for the next
   available `NNN` prefix (using the same logic as Phase 2.X.3 for
   `artifact_type: adr`). Generate the slug from the architectural question
   (kebab-case, 60-char max). Validate the path is well-formed and does not
   collide with an existing file.
4. **Insert `## Architectural Decision Required` into the first ticket's
   body**, listing the architectural question(s) the ADR must answer:

   ```markdown
   ## Architectural Decision Required

   This ticket's PR is responsible for committing
   `docs/decisions/{NNN}-{slug}.md`. The ADR records the decision needed by
   the rest of this epic. Subsequent dependent tickets block on this ticket
   via native `blockedBy` and read the ADR file once this PR has merged.

   **Question(s) to answer in the ADR**:

   - <one-line statement of each architectural question>
   ```

5. **Insert `## Prerequisite ADR` into every subsequent dependent body**:

   ```markdown
   ## Prerequisite ADR

   **`docs/decisions/{NNN}-{slug}.md`** — produced by `#<first-ticket>`'s PR.
   The pipeline planning stage MUST read this file before drafting a plan.
   If the file does not exist on disk, the first ticket has not merged and
   this ticket is not actionable.
   ```

6. **Wire `blockedBy` from each subsequent dependent to the first ticket**
   (not to a spike — there is no spike). Pass `--blocked-by <first-ticket>`
   when creating each subsequent dependent, or call
   `nightgauge issue add-blocked-by <dependent> <first-ticket>` after
   creation.
7. **Path C does not use the spike materializer.** No `yaml recommendations`
   block is emitted. `internal/cmd/spike/materialize.go` is unchanged.

> **Reviewer note**: Document in the preview that the ADR-bearing first
> ticket's PR review will be larger than a typical implementation review
> (code + ADR in one PR). This is a known and accepted trade-off — see
> [docs/SPIKE_CONTRACT.md#trade-offs](../../../docs/SPIKE_CONTRACT.md#trade-offs).

## Phase 2.X: Spike Artifact Path Selection

**Gate**: Only runs when the issue is classified as `type:spike` in Phase 2.
Skip entirely for non-spike issues.

For spike issues, the artifact path MUST be declared at creation time so
`feature-validate` has a concrete path to check when validating the spike PR.
This phase computes the path, validates it, and prepares the YAML scaffold for
inclusion in the issue body during Phase 3.

#### Step 2.X.1: Determine Artifact Type

Choose the artifact type based on issue content (or ask the user if ambiguous):

| Type       | Path pattern                             | When to use                                |
| ---------- | ---------------------------------------- | ------------------------------------------ |
| `spike`    | `docs/spikes/{issue-number}-{slug}.md`   | Feasibility research, evaluation (default) |
| `adr`      | `docs/decisions/{NNN}-{slug}.md`         | Architecture Decision Records              |
| `research` | `docs/research/{issue-number}-{slug}.md` | Deep-dive research notes                   |

If the issue title contains "ADR", "architecture decision", or "decision record",
default to `adr`. Otherwise default to `spike`.

#### Step 2.X.2: Generate the Artifact Slug

Auto-generate a slug from the issue title:

```bash
# Strip "spike:" prefix, lowercase, replace non-alphanumeric with dashes, truncate
SLUG=$(echo "$ISSUE_TITLE" | \
  sed 's/^spike:[[:space:]]*//' | \
  tr '[:upper:]' '[:lower:]' | \
  sed 's/[^a-z0-9]/-/g' | \
  sed 's/--*/-/g' | \
  sed 's/^-//;s/-$//' | \
  cut -c1-60)
```

The `--spike-slug` argument overrides the auto-generated slug when provided.

#### Step 2.X.3: Construct the Artifact Path

```bash
case "$ARTIFACT_TYPE" in
  spike)
    # Issue number IS the prefix — mandated by the spike contract
    ARTIFACT_PATH="docs/spikes/${ISSUE_NUMBER}-${SLUG}.md"
    ;;
  adr)
    # Sequential NNN prefix: scan existing files to find next available number
    LAST_NUM=$(ls docs/decisions/ 2>/dev/null | \
      grep -oE '^[0-9]+' | sort -n | tail -1)
    NEXT_NUM=$(printf '%03d' $((${LAST_NUM:-0} + 1)))
    ARTIFACT_PATH="docs/decisions/${NEXT_NUM}-${SLUG}.md"
    ;;
  research)
    ARTIFACT_PATH="docs/research/${ISSUE_NUMBER}-${SLUG}.md"
    ;;
esac
```

**Note**: For `docs/spikes/` the prefix is always the GitHub issue number —
the spike-materialize contract requires this. For `docs/decisions/` and
`docs/research/` the prefix is auto-incremented from existing files.

#### Step 2.X.4: Validate the Artifact Path

Run all three checks before proceeding. Any failure stops issue creation with
a clear, actionable error.

**Check 1 — Allowed directory:**

```bash
ALLOWED_DIRS="docs/spikes/ docs/decisions/ docs/research/"
IS_ALLOWED=false
for DIR in $ALLOWED_DIRS; do
  echo "$ARTIFACT_PATH" | grep -q "^$DIR" && IS_ALLOWED=true && break
done
if [ "$IS_ALLOWED" = "false" ]; then
  echo "ERROR: artifact path must be under one of: $ALLOWED_DIRS"
  echo "  Got: $ARTIFACT_PATH"
  exit 1
fi
```

**Check 2 — Path collision:**

```bash
if [ -f "$ARTIFACT_PATH" ]; then
  echo "ERROR: artifact path collision"
  echo "  Computed path: $ARTIFACT_PATH"
  echo "  Already exists: YES"
  echo ""
  echo "Options:"
  echo "  1. Use a different slug: --spike-slug \"<different-slug>\""
  echo "  2. Check if a spike for issue #${ISSUE_NUMBER} already exists"
  exit 1
fi
```

**Check 3 — Well-formed path:**

```bash
if ! echo "$ARTIFACT_PATH" | grep -qE '^docs/(spikes|decisions|research)/[0-9]+-[a-z0-9-]+\.md$'; then
  echo "ERROR: artifact path is not well-formed"
  echo "  Expected: docs/{spikes|decisions|research}/{N}-{slug}.md"
  echo "  Got: $ARTIFACT_PATH"
  exit 1
fi
```

#### Step 2.X.5: Prepare the YAML Scaffold

Store the artifact path and YAML scaffold template for injection into the issue
body during Phase 3:

````
ARTIFACT_PATH_LINE: **Artifact**: [`{ARTIFACT_PATH}`]({ARTIFACT_PATH})

YAML_SCAFFOLD:
​```yaml recommendations
spike: {ISSUE_NUMBER}
recommendations:
  # TODO: Replace this example entry with your real recommendations.
  # See docs/SPIKE_CONTRACT.md for the full schema and allowed values.
  - id: <kebab-case-id>
    action: adopt  # adopt | defer | skip
    title: "<Issue title — becomes the follow-up issue title>"
    type: feature  # feature | bug | docs | chore | spike
    priority: medium  # critical | high | medium | low
    size: M  # XS | S | M | L | XL
    labels: []  # optional: e.g. ["component:scheduler"]
    body: |
      Optional Markdown body. If absent, the materializer generates
      a stub that links back to the spike.
    depends_on: []  # optional: IDs of other recommendations in this spike
​```
````

Phase 3 injects `ARTIFACT_PATH_LINE` and `YAML_SCAFFOLD` into the spike issue
body after the Acceptance Criteria section.

#### Worked Example — Contract-Conformant Spike Issue Body

A spike issue body produced by this skill:

<!-- prettier-ignore -->
````markdown
## Problem Statement

The current scheduler polls every 10 seconds regardless of load. We need to
evaluate whether event-driven dispatch would reduce idle CPU cost.

## Acceptance Criteria

- [ ] Findings documented in `docs/spikes/4042-evaluate-event-driven-dispatch.md`
- [ ] Executive summary includes a clear `adopt`, `defer`, or `skip` verdict
- [ ] `yaml recommendations` block is present and valid per docs/SPIKE_CONTRACT.md

## Technical Notes

- Examine the polling loop in `internal/scheduler/`
- Benchmark event-driven alternative with realistic workloads

**Artifact**: [`docs/spikes/4042-evaluate-event-driven-dispatch.md`](docs/spikes/4042-evaluate-event-driven-dispatch.md)

## Recommendations

​```yaml recommendations
spike: 4042
recommendations:
  # TODO: Replace this example entry with your real recommendations.
  # See docs/SPIKE_CONTRACT.md for the full schema and allowed values.
  - id: <kebab-case-id>
    action: adopt  # adopt | defer | skip
    title: "<Issue title — becomes the follow-up issue title>"
    type: feature  # feature | bug | docs | chore | spike
    priority: medium  # critical | high | medium | low
    size: M  # XS | S | M | L | XL
    labels: []
    body: |
      Optional body text.
    depends_on: []
  ​```
````
