---
name: backlog-audit
description: Periodic semantic re-assessment of every open issue — validity against
  current main, worth under the product lens, and priority/size fit against a fixed
  rubric — with adversarially verified closures, additive premise corrections, and
  a stale-sync sweep for closed issues whose board status lags. Dry-run by default;
  use after an epic completes or on a monthly cadence to keep the board truthful.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
disable-model-invocation: true
---

# Backlog Audit

Semantic re-assessment of the open backlog: is each issue still **valid**
against current `main`, still **worth doing**, and **ranked** where it
belongs? Structural checks (labels, board fields, headings, `blockedBy`
wiring) are `/nightgauge:issue-audit`'s job — see
[docs/ISSUE_AUDIT.md](../../docs/ISSUE_AUDIT.md#boundary-issue-audit-vs-backlog-audit)
for the boundary. This skill reuses issue-audit's deterministic checks as
riders rather than duplicating them; its own verdicts are judgment calls that
issue-audit never makes.

The shape mirrors the 2026-08-16 reference run (103 open issues, 10 theme
batches, adversarial closure verification): a mostly-clean backlog still hid
under-prioritized correctness bugs, issues whose premises merged work had
falsified, duplicates, and a live publication-boundary leak. Every one of
those wastes pipeline cycles when dispatched as-filed.

## When to Use

- **After an epic completes** — merged work falsifies premises and unblocks
  re-ranking across the whole board
- **Monthly cadence** — pairs with the `/nightgauge:pipeline-health` review
- **Before a planning session** — the report ends with a ranked
  what-to-work-next and a single recommended next epic
- **Not** for post-creation validation of new issues — that is
  `/nightgauge:issue-audit`, which runs automatically after `issue-create`

## Outcomes

- Markdown report at `.nightgauge/triage/backlog-audit-<timestamp>.md`
- JSON findings at `.nightgauge/triage/backlog-audit-<timestamp>.json`
- Per-issue verdict: `keep` | `close-obsolete` | `close-duplicate` |
  `correct-premise` | `re-rank` | `redact-leak` | `unverifiable-keep`
- Dry-run by default: every verdict is reported, nothing is mutated —
  **except** publication-boundary redaction, which is live exposure and is
  applied immediately in every mode, with an explicit report line
- With `--apply`: verifier-confirmed closures with evidence comments, board
  field corrections, additive premise-correction body sections, stale-sync
  status repairs

## Input

```
/nightgauge:backlog-audit [--repo <owner/repo>] [--apply]
```

| Flag           | Behavior                                                  | Default      |
| -------------- | --------------------------------------------------------- | ------------ |
| `--repo <o/r>` | Target repo for this repo-scoped run                      | current repo |
| `--apply`      | Execute verified closures, corrections, and field changes | dry-run      |

## Hard Rules (non-negotiable)

1. **Closures require independently verified evidence.** A closure lands only
   after the Phase 4 adversarial pass confirms it, citing the specific PR,
   commit, or current file/function state that proves the issue is obsolete
   or done. "The assessor said so" is not evidence.
2. **Premise corrections are additive, never rewrites.** When merged work has
   falsified an issue's premise, append a dated `## Reassessment correction`
   section to the body. Never silently rewrite or delete the human-authored
   original — the same never-rewrite rule issue-audit enforces.
3. **Unverifiable claims stay open.** If neither the assessor nor the
   verifier can ground a verdict in current code or merged history, the issue
   keeps its `keep` status and the report says why. Doubt always resolves
   toward keeping the issue open.
4. **Cross-board listings are deliberate.** An issue appearing on more than
   one board (e.g. the Community Roadmap) is intentional curation, never a
   duplicate — do not "dedupe" across boards.
5. **Boundary-leak redaction happens even in dry-run.** A publication-boundary
   leak in an issue body is live exposure the moment it is found. Redact it
   immediately per the neutral-referent pattern (#140) — replace the private
   detail with a neutral capability-level reference — regardless of mode, and
   report the redaction explicitly. See
   [docs/PUBLIC_CORE_BOUNDARY.md](../../docs/PUBLIC_CORE_BOUNDARY.md).

## Priority Rubric (fixed)

Re-rank against this rubric only — never invent intermediate levels:

| Priority | Meaning                                           |
| -------- | ------------------------------------------------- |
| P0       | Breaking runs / corrupting data today             |
| P1       | Core-path correctness or unblocks high-value work |
| P2       | Real value, not urgent                            |
| P3       | Polish                                            |

## Model Guidance

- **Assessment batches (Phase 3)**: fan out via `Task` subagents on a
  mid-tier model — per-batch validity/worth/rank is parallel, scoped work.
- **Closure verification and synthesis (Phases 4 and 7)**: run on the strong
  tier. A wrongly closed issue is lost work; the refutation pass and the
  final ranking are where judgment quality pays for itself.

## Gotchas

- **First page forever** — GitHub-CLI-style auto-pagination (`--paginate`)
  injects the next cursor ONLY into a variable named `$endCursor`. Name it
  anything else and every request silently re-fetches page one: a 300-item
  board reads as 100 items and the run "completes" against a third of the
  backlog. `nightgauge forge graphql` has no auto-pagination — use the
  explicit cursor loop in Phase 1 and keep the variable named `$endCursor`
  so the query stays drop-in compatible with auto-paginating clients.
- **Multi-repo boards cross-contaminate** — a shared project board streams
  items from every member repo. Assessing (or worse, closing) a sibling
  repo's issue from a repo-scoped run is out of scope by definition — filter
  on `.content.repository.nameWithOwner` before batching (Phase 1).
- **Label mutations need node IDs** — `addLabelsToLabelable` takes label node
  IDs, not names. Resolve them once with `nightgauge label list --json`; a
  name passed where an ID belongs fails opaquely.

<!-- include: ../_shared/GOTCHAS.md -->

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 0.5: Run Reflection

Load the previous audit so this run leads with **what changed** (newly
obsolete, newly falsified premises, rank movement) instead of re-dumping the
whole backlog.

```bash
SKILL_NAME="nightgauge-backlog-audit"
RUN_LOG=".nightgauge/triage/backlog-audit-runs.jsonl"
```

<!-- include: ../_shared/RUN_REFLECTION.md -->

Set `RUN_COUNTS` (e.g. `{"open":N,"closed":N,"corrected":N,"reranked":N}`)
and `RUN_SUMMARY` from the Phase 7 report before the append step.

---

### Phase 1: Inventory

Collect the full board item stream (all states — Phase 6 reuses it) and
every OPEN issue for the target repo with its board fields. Board inventory
pages through the project with an explicit cursor loop — the cursor variable
is named `$endCursor` (see Gotchas):

```bash
QUERY='query($owner:String!,$project:Int!,$endCursor:String){
  organization(login:$owner){
    projectV2(number:$project){
      items(first:100,after:$endCursor){
        pageInfo{hasNextPage endCursor}
        nodes{
          id
          content{... on Issue{
            number title state updatedAt
            repository{nameWithOwner}
            labels(first:20){nodes{name}}
          }}
          fieldValues(first:20){nodes{
            ... on ProjectV2ItemFieldSingleSelectValue{
              name field{... on ProjectV2SingleSelectField{name}}}
          }}
        }
      }
    }
  }
}'
# User-owned projects: swap organization(login:) for user(login:).

CURSOR=""
: > /tmp/board_items.jsonl
while :; do
  PAGE=$(nightgauge forge graphql -f query="$QUERY" \
    -f owner="$OWNER" -F project="$PROJECT_NUMBER" \
    ${CURSOR:+-f endCursor="$CURSOR"})
  echo "$PAGE" | jq -c '.data.organization.projectV2.items.nodes[]' \
    >> /tmp/board_items.jsonl
  HAS_NEXT=$(echo "$PAGE" \
    | jq -r '.data.organization.projectV2.items.pageInfo.hasNextPage')
  [ "$HAS_NEXT" = "true" ] || break
  CURSOR=$(echo "$PAGE" \
    | jq -r '.data.organization.projectV2.items.pageInfo.endCursor')
done
```

Filter multi-repo board items to the target repo — sibling-repo items are out
of scope for a repo-scoped run:

```bash
jq -c --arg repo "$OWNER_REPO" \
  'select(.content.repository.nameWithOwner == $repo)' \
  /tmp/board_items.jsonl > /tmp/repo_items.jsonl
```

Cross-check against `nightgauge forge issue list --state open --json` so open
issues missing from the board are still assessed (their absence is itself an
issue-audit rider finding, Phase 3).

---

### Phase 2: Theme Batching

Group the inventory into theme batches (~8–15 issues) so assessors can spot
duplicates and judge epics as units. Batch by, in priority order:

1. **Epic membership** — an epic and its open sub-issues always share a batch
2. **Component labels** and shared file-path citations
3. **Title/body keyword clusters** (the duplicate-detection surface)

Issues that fit no cluster form a residual batch. Record the batch manifest
in the report so the fan-out is reproducible.

---

### Phase 3: Assessment (fan-out)

Dispatch one `Task` subagent per batch (mid-tier model). Each assessor
returns a per-issue verdict with a one-line reason and evidence pointers,
judging three axes:

- **Validity against current `main`** — verify cited files, functions, and
  behaviors still exist via `Grep`/`Read` on the working tree. **Never trust
  the issue's snapshot of the code**: the issue was written against a `main`
  that may no longer exist. An issue whose premise merged work has falsified
  gets `correct-premise`; one whose subject code is gone gets
  `close-obsolete` (pending Phase 4).
- **Worth (product lens)** — would doing this now move the product? The
  no-backwards-compat doctrine means dead-code chores whose code is already
  deleted close as obsolete; polish on a surface being replaced drops to P3
  or closes.
- **Priority/size fit** — score against the fixed rubric above. Both
  directions matter: the reference run's highest-value findings were
  under-prioritized P1 correctness bugs sitting at P2/P3.

Assessors also run issue-audit's deterministic checks as riders (type label,
board membership, required headings) and report those findings verbatim —
they do not re-derive them. Duplicates propose a fold direction:
`close-duplicate-of-#N` naming the survivor.

Any publication-boundary leak found during assessment escalates immediately
to Phase 5's redaction path — it does not wait for `--apply`.

---

### Phase 4: Adversarial Closure Verification

Every proposed `close-obsolete` and `close-duplicate` is re-verified by a
second, independent pass (strong tier) **instructed to refute the closure**:

- Re-derive the evidence from scratch: does the cited PR/commit actually
  cover the issue's acceptance criteria? Is the "deleted" code really gone,
  or moved?
- For duplicates: do the two issues truly share acceptance criteria, or do
  they overlap on one axis while diverging on another?
- A wrongly closed issue is lost work. **Doubt rejects the closure** — the
  issue reverts to `keep` (or `unverifiable-keep`) with the verifier's
  objection recorded in the report.

Only verifier-confirmed closures proceed to Phase 5. Field corrections and
premise corrections do not require this pass (they are additive or
reversible), but the verifier spot-checks a sample of `re-rank` verdicts.

---

### Phase 5: Apply

**Dry-run (default)**: report every verdict; mutate nothing — except
boundary-leak redaction, which is applied in every mode and reported with an
explicit `REDACTED (applied in dry-run)` line.

**With `--apply`**, execute in this order:

1. **Duplicate folds** — comment on the survivor folding in the duplicate's
   unique acceptance criteria, then close the duplicate with an evidence
   comment linking the survivor.
2. **Obsolete closures** — close with the verifier's evidence comment citing
   the PR/commit/file that proves obsolescence.
3. **Premise corrections** — append the additive dated section to the body:

   ```markdown
   ## Reassessment correction (<YYYY-MM-DD>)

   <what changed, which PR/commit falsified the original premise, and what
   remains true>
   ```

4. **Field corrections** — via the Go binary, never raw mutations:

   ```bash
   nightgauge project set-field <num> --priority P1 --size M
   nightgauge project sync-status <num> <status>
   ```

5. **Label changes** — resolve node IDs first with
   `nightgauge label list --json`, then mutate via
   `nightgauge forge graphql` (`addLabelsToLabelable` /
   `removeLabelsFromLabelable`).

Every mutation appends one line to the report's applied-actions table:
issue, action, evidence pointer.

---

### Phase 6: Stale-Sync Sweep

Catch CLOSED issues whose board Status never advanced to Done — the
#522/#370 failure mode that ghosts items in the VSCode extension. Runs
against the full (unfiltered-by-state) board inventory from Phase 1:

```bash
jq -c '
  select(.content.state == "CLOSED")
  | (([.fieldValues.nodes[]?
       | select(.field.name == "Status") | .name] | first) // "unset")
    as $status
  | select($status != "Done")
  | {number: .content.number, title: .content.title, status: $status}
' /tmp/repo_items.jsonl > /tmp/stale_sync.jsonl
```

Under `--apply`, repair each with `nightgauge project sync-status <num> done`;
in dry-run, list them in the report with their current status.

---

### Phase 7: Report

Write `.nightgauge/triage/backlog-audit-<timestamp>.md` (and `.json`) with:

1. **Outcome counts** — issues assessed, kept, closed (obsolete/duplicate),
   corrected, re-ranked, redacted, stale-synced, unverifiable-kept
2. **Per-class listings** — every non-`keep` verdict with its one-line reason
   and evidence pointer; rejected closures listed with the verifier's
   objection
3. **Field-correction table** — issue, field, old → new, reason
4. **Boundary redactions** — explicit lines, flagged as applied even in
   dry-run
5. **What to work next** — ranked list under the fixed rubric, ending with a
   **single recommended next epic** and the reasoning

In dry-run, close with the exact `--apply` invocation that would execute the
verified verdicts.

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

## Decision Rules

- **Semantic vs structural**: rank, worth, validity, premise truth → this
  skill. Labels, board fields, headings, `blockedBy` wiring → issue-audit
  riders; report them, repair them only through issue-audit's primitives.
- **Closure bar**: independent evidence citing PR/commit/file, confirmed by
  the adversarial pass. Anything less stays open.
- **Epic verdicts**: an epic is judged as a unit with its sub-issues in one
  batch; never close an epic while it has open sub-issues.
- **Repo scope**: sibling-repo items on a shared board are inventory-filtered
  out, never assessed, never mutated.
- **Redaction scope**: redact only the leaked span (neutral-referent
  replacement); the rest of the body is untouched human-authored content.
- **Idempotency**: re-running after `--apply` must find nothing to re-apply —
  closures, corrections, and field states are all convergent.

## Failure Conditions (exit 2)

Distinct from a clean run that simply finds nothing:

- Go binary missing (Phase 0 preflight cascade exhausts)
- Forge unauthenticated, or the project board number cannot be resolved from
  `.nightgauge/config.yaml`
- Board inventory pagination fails mid-run (partial inventory would produce
  false "missing from board" riders — abort instead)

## Completion Checklist

- [ ] Inventory paginated to exhaustion (`hasNextPage` false), filtered to
      the target repo (Phase 1)
- [ ] Theme batches recorded in the report (Phase 2)
- [ ] Every open issue carries a verdict with a one-line reason (Phase 3)
- [ ] Every closure/merge verdict adversarially verified or rejected
      (Phase 4)
- [ ] Dry-run mutated nothing except boundary redactions; `--apply` actions
      each carry an evidence pointer (Phase 5)
- [ ] Stale-sync sweep ran over CLOSED board items (Phase 6)
- [ ] Report written with outcome counts, per-class listings, field table,
      and a single recommended next epic (Phase 7)
- [ ] Run record appended to `RUN_LOG` (skipped in dry-run)
