# Phases 3 & 3.5: Create Issue & Set Dependencies — Procedural Detail

Detail bodies for Phase 3 (Create Issue Deterministically) and Phase 3.5 (Set Dependency Relationships) of the `nightgauge-issue-create` skill.

## Contents

- [Phase 3: Create Issue Deterministically](#phase-3-create-issue-deterministically)
- [Phase 3.5: Set Dependency Relationships](#phase-35-set-dependency-relationships)

## Phase 3: Create Issue Deterministically

Create issue with `nightgauge issue create` using final title/body and
labels. Capture issue number from command output.

**MANDATORY ROUTING SOURCE OF TRUTH**: For epics with sub-issues, this phase
reads the routing manifest written by Phase 2.4
(`.nightgauge/pipeline/issue-create-routing-<epic-number>.json`). EVERY
sub-issue creation MUST use the manifest's `target_repo` for that sub-issue.
Never re-derive routing here — drift between Phase 2.4 and Phase 3 reintroduces
the #3232 silent-misroute bug. If the manifest file is missing for an epic,
this phase fails fatally with `routing-manifest-missing` and refuses to create
any sub-issue.

#### Phase 3 Hard-Gate: type:spike Pre-Creation Validation

Before calling `nightgauge issue create-sub` for any sub-issue with the
`type:spike` label, validate the assembled body. This gate prevents creating
spike issues with missing contracts (historical incident: #3331).

First, verify the `nightgauge spike validate` command is available:

```bash
if ! nightgauge spike validate --help >/dev/null 2>&1; then
  echo "ERROR: nightgauge spike validate not available."
  echo "Update the binary: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest"
  exit 1
fi
```

For each `type:spike` sub-issue body assembled into a temp file `$BODY_FILE`:

```bash
LABEL_LIST="<comma-separated label string for this sub-issue>"

if echo "$LABEL_LIST" | grep -q "type:spike"; then
  if ! nightgauge spike validate --body-file "$BODY_FILE"; then
    echo ""
    echo "ERROR: type:spike sub-issue body failed contract validation."
    echo "Refusing to create the issue. Fix the body before retrying."
    echo "See docs/SPIKE_CONTRACT.md for the required structure:"
    echo "  1. A fenced \`\`\`yaml recommendations block"
    echo "  2. A '## Spike Contract (Path A/B/C)' heading"
    echo "  3. An artifact path: docs/spikes/<N>-<slug>.md or docs/decisions/<NNN>-<slug>.md"
    exit 1
  fi
fi
```

For each sub-issue, two cases:

1. **Same-repo sub-issue** (manifest's `target_repo` == epic's repo):
   `nightgauge issue create-sub <parent>` works as-is — native GitHub
   `addSubIssue` linking, single-repo project sync.

2. **Cross-repo sub-issue** (manifest's `target_repo` != epic's repo):
   Native GitHub `addSubIssue` works across repos within the same org
   (verified 2026-07-12: nightgauge#40 ← acme-api#100), so
   cross-repo children get the SAME native link as same-repo ones — without
   it they are invisible to the pipeline's epic grouping (see CRITICAL note
   below). Use:

   ```bash
   gh issue create \
     --repo "nightgauge/<target_repo>" \
     --title "<title>" \
     --body "<body>" \
     --label "<labels>"
   # The body MUST still contain a `Part of nightgauge/<epic-repo>#<epic-number>`
   # line so the relationship reads in the issue text itself.
   # Use --body-file with a temp file (heredoc bodies trigger local hooks).

   nightgauge project add <new-sub-number> \
     --repo "<target_repo>" \
     --project <target_project_number>

   # Native cross-repo sub-issue link (same mutation as the same-repo path):
   gh api graphql -f query='mutation { addSubIssue(input: {
     issueId: "<epic_node_id>", subIssueId: "<sub_issue_node_id>"
   }) { clientMutationId } }'
   ```

   The cross-repo body annotation must use the FULL `owner/repo#number` form so
   GitHub renders the cross-link correctly. Example:
   `Part of nightgauge/nightgauge#3211`.

**CRITICAL — Native Sub-Issues Required**: All parent/child relationships MUST
use GitHub's native sub-issue feature (via `addSubIssue` GraphQL mutation). The
pipeline's project board groups issues under epics using the GraphQL
`subIssues(first: 50)` field on each issue — issues with sub-issues are marked
as epics, and parent relationships are derived from the sub-issue list.
Body-based "Part of #X" text alone is NOT sufficient for pipeline display. After
creating sub-issues, they MUST also be added to the project board via
`addProjectV2ItemById` and have their Status field set, or they will be
invisible in the extension's tree views.

**Standalone issue**:

```bash
nightgauge issue create \
  --owner <OWNER> --repo <REPO> \
  --title "<title>" \
  --body "<body>" \
  --labels "type:feature,priority:high" \
  --json
```

**Sub-issue under a parent epic (new issue)**: Use the deterministic hook
script:

```bash
nightgauge issue create-sub <parent-issue-number> \
  --title "<title>" --body "<body>" [--labels "<label-id>"] \
  [--repo <owner/repo>] [--blocked-by <N>,<M>] \
  [--wave <wave-number>] [--depends-on <N>,<M>]
```

This command handles:

- Canonical parent text insertion (`Part of #<parent>`)
- Native parent/child linkage via GraphQL `addSubIssue`
- Optional `blockedBy` relationship wiring via `--blocked-by` or `--depends-on`
- Optional wave annotation embedded in the issue body via `--wave`

**Declaring blocking dependencies at creation time**: Use `--blocked-by` or
the semantic alias `--depends-on` with a comma-separated list of blocker issue
numbers:

```bash
nightgauge issue create-sub 295 --title "Task" --blocked-by 280,290
nightgauge issue create-sub 295 --title "Task" --wave 2 --depends-on 280,290
```

Both `--blocked-by` and `--depends-on` create `addBlockedBy` relationships.
`--depends-on` expresses ordering intent; `--blocked-by` is the existing
mechanical flag. Both can be used simultaneously — their blocker lists are
merged.

> **IMPORTANT**: Writing "Blocked by #N" in the issue body is purely cosmetic
> text. The pipeline reads GitHub's native `blockedBy` GraphQL relationships —
> NOT body text. Always use `--blocked-by` (or `nightgauge issue
add-blocked-by` post-hoc) to create pipeline-visible blocking relationships.

If any blocker fails to link, the command reports a partial-success error naming
each failed blocker. The sub-issue and parent link are preserved; re-run
`nightgauge issue add-blocked-by <sub> <blocker>` for any missed blockers.

**NOTE**: `create-sub` syncs to the project board automatically when `--project`
is provided (or `ProjectNumber` is set in config). If the board sync fails, the
issue and sub-issue link are preserved and a partial-success error reports the
issue number. You still MUST run
`nightgauge project sync-status <number> backlog` (then `ready`) in Phase 4
to set the Status field, since board sync only adds the item.

**Epic with multiple sub-issues**: When creating an epic (`type:epic` label):

1. Create the epic issue first with
   `nightgauge issue create --title "..." --body "..." --labels "type:epic,..." --json`
2. Create each sub-issue using `nightgauge issue create-sub --project <N>` (handles
   linking and board item creation; Status field is still set in Phase 4)
3. If sub-issues already exist, link them using `nightgauge issue link-sub`:

Preserve the classification from Phase 2 when creating sub-issues. Do not label
recommendation-only work as implementation-ready.

**Sub-issue body annotations**: Before calling `create-sub`, annotate each
sub-issue body with wave and dependency information from Phase 2.5 or 2.6:

- Append `Part of #<epic> (Wave <N>)` — the `create-sub` command adds
  `Part of #<epic>` automatically, so include the wave number in the body text
  before that line: `Depends on: #<dep1>, #<dep2>` (or `Depends on: None`).
- This satisfies the CONTRIBUTING.md requirement that every sub-issue declares
  its wave and dependencies.

**Expected Deliverables section**: When creating an epic with 3+ sub-issues,
include an "Expected Deliverables" section in the epic body. This section:

1. Helps with planning and tracking expected outcomes
2. Signals `classify-epic-summary-tier.sh` to use "full" tier for summary
   generation on epic completion

```markdown
## Expected Deliverables

- [Deliverable 1 description]
- [Deliverable 2 description]
- [Deliverable 3 description]
```

This section should be placed after the Technical Notes section and before the
sub-issue list. If sub-issues are fewer than 3, the section is optional.

Linking existing sub-issues to an epic:

```bash
nightgauge issue link-sub \
  <parent-epic-number> <child-issue-number> [--repo <owner/repo>]
```

This command links existing issues as sub-issues of an epic via GraphQL
`addSubIssue`. It handles already-linked issues gracefully (idempotent). After
linking, run `nightgauge project add` on each child if not already synced.

## Phase 3.5: Set Dependency Relationships (Epic Sub-Issues)

**Gate**: Always runs when creating an epic with 2+ sub-issues. Uses dependency
edges from Phase 2.5 (agent teams) or Phase 2.6 (lightweight fallback). Step 1
(cross-epic) runs unconditionally. Step 2 (intra-epic) runs when at least one
dependency edge was detected.

After all sub-issues are created, set GitHub's **native blocking relationships**
so the pipeline respects execution ordering. This prevents merge conflicts by
ensuring dependent sub-issues don't run in parallel.

**CRITICAL**: Use the `addBlockedBy` GraphQL mutation — NOT `trackedInIssues`,
NOT body text like "Blocked by #N", NOT labels. The pipeline reads `blockedBy`/
`blocking` fields from the GitHub API. Other approaches are invisible to it.

#### Step 1: Cross-Epic Dependency Detection (MANDATORY for all epics)

Before setting intra-epic dependencies, check whether the **new epic itself**
depends on another epic. This prevents the pipeline from executing sub-issues
whose foundational prerequisites don't exist yet.

1. **Scan the epic title AND body** for references to other epics:
   - Explicit: "builds on #NNN", "depends on epic #NNN", "requires #NNN",
     "depends on #NNN", "after #NNN"
   - Phased: "Phase [2|3|N] of #NNN", "continuation of #NNN", "follow-up to
     #NNN", "Part of #NNN"
   - Structural: "This is the [second|third|next] phase of…"
   - Technical: references to schemas, services, or APIs defined by another epic
   - When a match is found, verify the referenced issue has the `type:epic`
     label before treating it as a cross-epic dependency
2. **For each referenced prerequisite epic**, set epic-to-epic blocking:

**Preferred — use the CLI command:**

```bash
# New epic is blocked by prerequisite epic
nightgauge issue add-blocked-by <NEW_EPIC_NUMBER> <PREREQ_EPIC_NUMBER>
```

If the binary is unavailable, this skill fails with a clear error (see
Phase 1). There is no CLI fallback.

3. **Set sub-issue to sub-issue blocking**: For each root sub-issue of the new
   epic (sub-issues with no intra-epic blockers), block them on the leaf
   sub-issues of the prerequisite epic (sub-issues that nothing else depends
   on). This creates a concrete execution boundary:

```bash
# Get prerequisite epic's leaf sub-issues (those that don't block siblings)
# Get new epic's root sub-issues (those that have no intra-epic blockers)
# For each (root, leaf) pair: root is blocked by leaf
nightgauge issue add-blocked-by <ROOT_NUMBER> <LEAF_NUMBER>
```

**Why both levels?** Epic-to-epic blocking lets the scheduler skip an entire
epic's sub-issues with a single parent check. Sub-issue-to-sub-issue blocking
provides granular control — some sub-issues may be unblockable before the full
prerequisite epic completes.

4. **Verification**: Confirm the epic-level blocking is set:

```bash
nightgauge issue view <NEW_EPIC_NUMBER> --json | jq -r '.blockedBy[] | "#\(.number) \(.title)"'
```

#### Step 2: Intra-Epic Dependencies (within the same epic)

Intra-epic ordering comes from two deterministic sources, both produced by
`nightgauge epic plan-waves` in Phase 2.6:

1. **Wave dependencies** (native `blockedBy` read from GitHub) — sub-issue B
   depends on sub-issue A.
2. **Injected file-overlap serializations** (the `conflicts` array, `error`
   severity) — the planner detected that B and A share a top-level target file
   and injected `blockedBy`. **These are not optional and not author judgment.**

For **every** such edge (from waves AND from each `error` conflict), set the
native blocking relationship:

**Preferred — use the CLI command** (handles node ID resolution internally):

```bash
# B depends on A → A blocks B
nightgauge issue add-blocked-by <B_NUMBER> <A_NUMBER>
```

For an injected serialization, the conflict's `issues` array is sorted ascending
(`[A_NUMBER, B_NUMBER]`): the lower number is the blocker (A), the higher is the
blocked issue (B). Apply **every** `error` conflict — skipping one re-introduces
the exact #143/#144 collision the planner exists to prevent.

If the binary is unavailable, this skill fails with a clear error (see
Phase 1). There is no CLI fallback.

**Verification** — after setting relationships, confirm they're visible:

```bash
nightgauge issue view <B_NUMBER> --json | jq -r '.blockedBy[] | "#\(.number) \(.title)"'
```

**CRITICAL — Never Block a Sub-Issue on Its Own Epic**: A sub-issue must
NEVER have `blockedBy` pointing to the parent epic. This creates a circular
dependency (epic can't close until sub-issue is done, but sub-issue is blocked
by the epic). The binary enforces this server-side — `add-blocked-by` rejects
the relationship when the blocker is the parent epic of the blocked issue:

```bash
# Guard is enforced by the binary — add-blocked-by rejects circular blocker == parent epic
nightgauge issue add-blocked-by "$B_NUMBER" "$A_NUMBER" || echo "SKIP: circular blocker rejected by binary"
```

**CRITICAL — Dependency Ordering for Concurrent Safety (deterministically
enforced)**: Two same-wave sub-issues that both modify the same top-level target
file are a guaranteed merge conflict the moment the first PR lands. This is no
longer left to author judgment — the Go wave planner (`nightgauge epic
plan-waves`) computes it:

1. **File overlap is detected by the binary, not the author.** `plan-waves`
   extracts each sub-issue's predicted target files from its body and, for any
   two SAME-WAVE issues sharing a top-level EXACT file (e.g. both touch
   `lib/pages/journal_entry_page.dart`), **auto-injects a `blockedBy` edge** —
   the later issue number depends on the earlier one — before computing waves.
   Each injected serialization is reported as an `error`-severity entry in the
   `conflicts` array (and printed as `Serialized #B after #A — shared target
file <path>` in human-readable output).
2. **Phase 3.5 MUST apply every injected edge.** For each `error` conflict in the
   `plan-waves` `conflicts` output, run
   `nightgauge issue add-blocked-by <later_number> <earlier_number>` (the
   pair is `conflicts[].issues` sorted ascending → blocker is the lower number).
   Do NOT skip, re-order, or second-guess these edges — they are deterministic.
3. **Directory-only overlaps stay parallel.** Two issues editing different files
   in the same directory are NOT serialized (they surface as `warning`-severity
   conflicts only). This preserves legitimate parallelism.
4. **Citations never serialize (#79).** The extractor counts the change
   surface, not the bibliography: markdown-link destinations are citations
   and are excluded, `docs/**` paths never inject edges (two issues touching
   a doc is not the same-file code collision this guard exists for), and an
   explicit `file_ownership` list in the `nightgauge:dependency-metadata`
   block replaces prose inference entirely. Epic #71's six sub-issues were
   once serialized into six sequential waves behind one shared spike-doc
   citation — cite evidence as markdown links so it can never count as an
   edit target.
5. **Consider splitting only for genuine design problems.** If two issues both
   need to OWN the same file with no natural ordering, that is a decomposition
   smell — restructure the epic to eliminate the overlap. The injected edge is a
   safety net, not a license to author overlapping work.

The pipeline's `ProjectBoardService` reads `blockedBy`/`blocking` from the
GitHub GraphQL API for topological sorting and wave calculation, so the
**native blocking relationship is the authoritative source** — applying the
injected edges via `add-blocked-by` is what makes the serialization real.
