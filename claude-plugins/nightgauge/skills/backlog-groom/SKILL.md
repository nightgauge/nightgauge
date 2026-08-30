---
name: backlog-groom
description: Groom the open backlog so nothing dispatched is wasted work — re-assess
  every open issue against current main on five axes (validity, worth, verification
  completeness, security of the proposed approach, epic fit), adversarially verify
  every proposed mutation, then apply append-only corrections, evidence-cited
  closures, epic moves and a stale-sync sweep. Repo-scoped or workspace-wide with
  cross-repo epic batches. Dry-run by default; use after an epic completes, before a
  planning session, or on a monthly cadence.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "2.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task Workflow AskUserQuestion
disable-model-invocation: true
---

# Backlog Groom

Semantic re-assessment of the open backlog: is each issue still **valid**
against current `main`, still **worth doing**, **provable** when done,
**safe** as designed, and **filed under the right epic**? Structural checks
(labels, board fields, headings, `blockedBy` wiring) are
`/nightgauge:issue-audit`'s job — see
[docs/ISSUE_AUDIT.md](../../../../docs/ISSUE_AUDIT.md#boundary-issue-audit-vs-backlog-groom)
for the boundary. This skill reuses issue-audit's deterministic checks as
riders rather than duplicating them; its own verdicts are judgment calls that
issue-audit never makes.

The shape encodes two reference runs. The 2026-08-16 run (103 open issues,
10 theme batches, adversarial closure verification) found that a
mostly-clean backlog still hid under-prioritized correctness bugs, issues
whose premises merged work had falsified, duplicates, and a live
publication-boundary leak. The 2026-08-29 workspace run (224 open issues
across seven repositories, 21 batches, assess → verify → apply fan-out) added
the two failure classes a repo-scoped validity pass cannot see: issues with
**no way to prove the feature works** — dispatched as-filed, the pipeline
self-grants "done" — and issues whose **proposed approach is insecure as
written** (unbounded retries, secrets in settings, unauthenticated mutating
endpoints), which land the vulnerability with green CI. Every one of those
wastes pipeline cycles or ships a defect when dispatched unchanged.

## When to Use

- **After an epic completes** — merged work falsifies premises and unblocks
  re-ranking across the whole board
- **Before a planning session** — the report ends with a ranked
  what-to-work-next and a single recommended next epic
- **Monthly cadence** — pairs with the `/nightgauge:pipeline-health` review
- **After a strategy change** — the worth axis re-judges every issue under the
  current product direction, so drift shows up as `close-not-worth` and
  `clarify` verdicts instead of as wasted runs
- **Not** for post-creation validation of new issues — that is
  `/nightgauge:issue-audit`, which runs automatically after `issue-create`
- **Not** for pre-run readiness of a chosen batch — that is
  `/nightgauge:backlog-preflight`

## Outcomes

- Markdown report at `.nightgauge/triage/backlog-groom-<timestamp>.md`
- JSON findings at `.nightgauge/triage/backlog-groom-<timestamp>.json`
- Per-issue verdict from the [vocabulary](#verdict-vocabulary) below
- Dry-run by default: every verdict is reported, nothing is mutated —
  **except** publication-boundary redaction, which is live exposure and is
  applied immediately in every mode, with an explicit report line
- With `--apply`: verifier-upheld closures with evidence comments, additive
  body sections (clarification, verification, premise correction, security
  constraints), epic re-parenting, board field corrections, stale-sync
  status repairs

## Input

```
/nightgauge:backlog-groom [--repo <owner/repo> | --workspace] [--apply]
```

| Flag           | Behavior                                                  | Default      |
| -------------- | --------------------------------------------------------- | ------------ |
| `--repo <o/r>` | Target repo for a repo-scoped run                         | current repo |
| `--workspace`  | Every repo in the workspace manifest, cross-repo batches  | off          |
| `--apply`      | Execute verified closures, corrections, and field changes | dry-run      |

`--workspace` reads the repo list from the workspace manifest
(`.vscode/nightgauge-workspace.yaml` or `nightgauge workspace list`) and
adds any repository that **parents** a workspace issue through the sub-issue
graph even when it has no local checkout — epics that live in a planning repo
still group work in the checked-out ones. A repo without a checkout is
assessed at the planning level only (its issues can be moved, clarified, or
closed on merged-PR evidence from sibling repos, never on "the code is gone").

## Hard Rules (non-negotiable)

1. **Closures require independently verified evidence.** A closure lands only
   after the Phase 4 adversarial pass confirms it, citing the specific PR,
   commit, or current file/function state that proves the issue is obsolete,
   done, or contrary to doctrine. "The assessor said so" is not evidence.
2. **Body changes are additive, never rewrites.** Every correction is an
   appended, dated `##` section (`Reassessment correction`, `Clarification`,
   `Verification`, `Security constraints`). Never silently rewrite or delete
   the human-authored original — the same never-rewrite rule issue-audit
   enforces. The one exception is redaction, which replaces only the leaked
   span.
3. **Unverifiable claims stay open.** If neither the assessor nor the
   verifier can ground a verdict in current code or merged history, the issue
   keeps its `keep` status and the report says why. Doubt always resolves
   toward keeping the issue open.
4. **Every issue leaves the run with verification.** An issue that cannot say
   how a reviewer would observe it working is not ready for dispatch. When
   the body has no concrete, falsifiable check, the run appends one — named
   test file or package, command, input, expected observable output. "Add
   tests" is not verification.
5. **Security is judged on the proposed design, not the current code.** If
   implementing the issue as written would introduce a hazard, the run
   appends the hazard and its mitigation as an acceptance criterion so the
   pipeline cannot close the issue without it.
6. **Cross-board listings are deliberate.** An issue appearing on more than
   one board (e.g. the Community Roadmap) is intentional curation, never a
   duplicate — do not "dedupe" across boards.
7. **Never close an epic with open sub-issues.** An epic is judged as a unit
   with its children in one batch.
8. **Boundary-leak redaction happens even in dry-run.** A publication-boundary
   leak in a public-repo issue body is live exposure the moment it is found.
   Redact it immediately per the neutral-referent pattern — replace the
   private detail with a neutral capability-level reference — regardless of
   mode, and report the redaction explicitly. Appended text in a public repo
   must itself be public-safe: no private repo issue numbers, customers,
   economics, or deployment topology. See
   [docs/PUBLIC_CORE_BOUNDARY.md](../../../../docs/PUBLIC_CORE_BOUNDARY.md).
9. **Close, never delete.** A closed issue with an evidence comment is
   reversible and auditable; a deleted one is neither.

## Verdict Vocabulary

Exactly one verdict per issue — the dominant one when several actions apply:

| Verdict             | Meaning                                                                         | Mutation                                         |
| ------------------- | ------------------------------------------------------------------------------- | ------------------------------------------------ |
| `keep`              | Valid, worth doing, clearly specified, verifiable                               | none                                             |
| `clarify`           | Valid but the ask is ambiguous or has drifted from the current tree             | append `## Clarification (<date>)`               |
| `add-verification`  | Valid but carries no falsifiable way to prove it works                          | append `## Verification (added <date>)`          |
| `correct-premise`   | Merged work falsified part of the premise; work remains                         | append `## Reassessment correction (<date>)`     |
| `security-concern`  | The proposed approach is insecure as written                                    | append `## Security constraints (<date>)`        |
| `move-to-epic`      | Belongs under a different open parent epic                                      | re-parent via sub-issue API                      |
| `re-rank`           | Priority/size does not match the fixed rubric                                   | board field correction                           |
| `close-obsolete`    | Subject code/behavior is gone or the work already landed                        | evidence comment + close                         |
| `close-duplicate`   | Folds into a named survivor                                                     | survivor comment with unique ACs + close         |
| `close-not-worth`   | Possible but contrary to current doctrine/strategy (compat shims, dead surface) | reason comment + close                           |
| `redact-leak`       | Public-repo body leaks private material                                         | replace leaked span only — applied in every mode |
| `unverifiable-keep` | A non-keep verdict could not be grounded                                        | none; reason recorded                            |

## Priority Rubric (fixed)

Re-rank against this rubric only — never invent intermediate levels:

| Priority | Meaning                                           |
| -------- | ------------------------------------------------- |
| P0       | Breaking runs / corrupting data today             |
| P1       | Core-path correctness or unblocks high-value work |
| P2       | Real value, not urgent                            |
| P3       | Polish                                            |

## Model Guidance

- **Assessment batches (Phase 3)**: fan out — one agent per batch. The
  assessor authors publishable markdown (verification steps, security
  constraints) that the applier pastes verbatim, so this is not a cheap-tier
  job: a wrong verification section sends a future run down a false path.
- **Adversarial verification (Phase 4)**: strong tier, high effort. A wrongly
  closed issue is lost work; a wrong epic move hides work.
- **Apply (Phase 5)**: mechanical — low effort is correct.
- **Synthesis (Phase 7)**: strong tier; the ranking is where judgment pays.

## Gotchas

- **First page forever** — GitHub-CLI-style auto-pagination (`--paginate`)
  injects the next cursor ONLY into a variable named `$endCursor`. Name it
  anything else and every request silently re-fetches page one: a 300-item
  board reads as 100 items and the run "completes" against a third of the
  backlog. `nightgauge forge graphql` has no auto-pagination — use the
  explicit cursor loop in Phase 1 and keep the variable named `$endCursor`
  so the query stays drop-in compatible with auto-paginating clients.
- **Multi-repo boards cross-contaminate** — a shared project board streams
  items from every member repo. In a repo-scoped run, assessing (or worse,
  closing) a sibling repo's issue is out of scope by definition — filter on
  `.content.repository.nameWithOwner` before batching (Phase 1). In a
  workspace run the opposite holds: an epic's children span repos, and
  splitting them by repo hides the duplicates.
- **Stale sibling checkouts assess against a `main` that no longer exists** —
  `git fetch` every checkout in the workspace before Phase 3 and record each
  repo's `origin/main` SHA in the report. A verdict grounded in a checkout
  three weeks behind is a verdict about history.
- **Epics that live in an un-checked-out repo** — the sub-issue graph
  (`issue { parent { number repository { name } } }`) is the only reliable
  membership signal; body text like "Part of #N" drifts. Query it, do not
  parse it.
- **Label mutations need node IDs** — `nightgauge forge label add`/`remove`
  take label node IDs in `--labels`, not names. Resolve them once with
  `nightgauge label list --json`; a name passed where an ID belongs fails
  opaquely.
- **Append, do not re-send** — `gh issue edit --body` replaces the whole body.
  Fetch the current body to a file, append, and send the file; write the
  section via a quoted heredoc so markdown survives shell quoting. Check for
  the heading first so a re-run is idempotent.

<!-- include: ../_shared/GOTCHAS.md -->

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 0.5: Run Reflection

Load the previous run so this one leads with **what changed** (newly
obsolete, newly falsified premises, rank movement, newly unverifiable
issues) instead of re-dumping the whole backlog.

```bash
SKILL_NAME="nightgauge-backlog-groom"
RUN_LOG=".nightgauge/triage/backlog-groom-runs.jsonl"
```

<!-- include: ../_shared/RUN_REFLECTION.md -->

Set `RUN_COUNTS` (e.g.
`{"open":N,"closed":N,"corrected":N,"verified":N,"secured":N,"moved":N,"reranked":N}`)
and `RUN_SUMMARY` from the Phase 7 report before the append step.

---

### Phase 1: Inventory

Collect the full board item stream (all states — Phase 6 reuses it) and
every OPEN issue for each target repo with its board fields. Board inventory
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
  printf '%s\n' "$PAGE" | jq -c '.data.organization.projectV2.items.nodes[]' \
    >> /tmp/board_items.jsonl
  HAS_NEXT=$(echo "$PAGE" \
    | jq -r '.data.organization.projectV2.items.pageInfo.hasNextPage')
  [ "$HAS_NEXT" = "true" ] || break
  CURSOR=$(echo "$PAGE" \
    | jq -r '.data.organization.projectV2.items.pageInfo.endCursor')
done
```

Repo-scoped run: filter multi-repo board items to the target repo —
sibling-repo items are out of scope:

```bash
jq -c --arg repo "$OWNER_REPO" \
  'select(.content.repository.nameWithOwner == $repo)' \
  /tmp/board_items.jsonl > /tmp/repo_items.jsonl
```

Workspace run: repeat per board, then pull the open-issue list of every repo
directly (`gh issue list -R <o/r> --state open --limit 500 --json ...`) and
the parent edge of every open issue:

```bash
gh api graphql --paginate -f query='query($endCursor:String){
  repository(owner:"'"$OWNER"'",name:"'"$REPO"'"){
    issues(first:100,states:OPEN,after:$endCursor){
      pageInfo{hasNextPage endCursor}
      nodes{number parent{number repository{name}}}}}}' \
  -q '.data.repository.issues.nodes[]|select(.parent!=null)
      |"'"$REPO"'#\(.number) -> \(.parent.repository.name)#\(.parent.number)"'
```

Any parent repository not in the manifest joins the run at planning level
(see Input). `git fetch` every local checkout and record each `origin/main`
SHA. Cross-check the board against `nightgauge forge issue list --json` —
the command returns open issues only by design, so open issues missing from
the board are still assessed (their absence is itself an issue-audit rider
finding, Phase 3).

---

### Phase 2: Theme Batching

Group the inventory into theme batches (~8–14 issues) so assessors can spot
duplicates and judge epics as units. Batch by, in priority order:

1. **Epic membership from the sub-issue graph** — an epic and its open
   children always share a batch, **across repositories** in a workspace run
2. **Component labels** and shared file-path citations
3. **Title/body keyword clusters** (the duplicate-detection surface)

Issues that fit no cluster form a residual batch. Assert that every open
issue is in exactly one batch before fanning out, and record the batch
manifest in the report so the fan-out is reproducible.

---

### Phase 3: Assessment (fan-out)

Dispatch one assessor per batch. Prefer the `Workflow` tool when it is
available: a `pipeline()` over the batch list with three stages
(assess → verify → apply) lets batch A be applied while batch B is still
being assessed, and the structured-output schema removes parsing. Without
it, fan out with `Task` subagents per phase. Each assessor fetches every
issue's body and comments and returns a per-issue verdict, a one-line
reason, an evidence pointer, `has_verification`, and a list of proposed
actions (`append_section` with the complete markdown, `comment`, `close`,
`move_to_epic`, `add_label`, `remove_label`, `retitle`, `redact_span`),
judging five axes:

- **Validity against current `main`** — verify cited files, functions, and
  behaviors still exist via `Grep`/`Read` on the fetched working tree, and
  search merged history (`git log -S`, `gh pr list --state merged --search`)
  for work that already did this. **Never trust the issue's snapshot of the
  code**: the issue was written against a `main` that may no longer exist.
- **Worth (product lens)** — would doing this now move the product? The
  no-backwards-compat doctrine means compat shims and "keep both paths" asks
  are `close-not-worth` or clarify-to-single-path; dead-code chores whose code
  is already deleted close as obsolete; polish on a surface being replaced
  drops to P3 or closes.
- **Verification completeness** — does the body name how a reviewer would
  observe the feature working: the test file or package to add or extend, the
  command, the input, the expected observable output? If not, author it —
  concrete, falsifiable, scoped to the issue's acceptance criteria, naming
  files that exist (or clearly marked as to-be-created).
- **Security of the proposed approach** — judged against
  `standards/security.md`: would the design as
  written put secrets in settings or logs, expose an unauthenticated or
  unauthorized mutating path, retry or fan out without bound, skip input
  validation at a boundary, write files outside a contained root, or let
  model-authored text reach a mutating tool unreviewed? Name the hazard and
  the mitigation as an acceptance criterion.
- **Epic fit and duplicates** — read the parent edge from the sub-issue
  graph; propose `move-to-epic` only toward an open epic that is clearly the
  better parent. Duplicates propose a fold direction naming the survivor.

Assessors also run issue-audit's deterministic checks as riders (type label,
board membership, required headings) and report those findings verbatim —
they do not re-derive them. Any publication-boundary leak found during
assessment escalates immediately to Phase 5's redaction path — it does not
wait for `--apply`.

---

### Phase 4: Adversarial Verification

Every proposed mutation is re-checked by a second, independent agent
(strong tier, high effort) **instructed to refute it**:

- **Closures**: re-derive the evidence from scratch. Does the cited
  PR/commit actually cover the acceptance criteria? Is the "deleted" code
  really gone, or moved? Does the epic have open children? Partial evidence
  downgrades to `correct-premise` with an appended section.
- **Appended sections**: is every claim true against current `main`? Are the
  named test files, packages, commands and flags real? Is the verification
  falsifiable? Fix the text if it is close; reject if it is wrong. In a
  public repo, reject any text that leaks private material.
- **Epic moves**: is the target open and genuinely the better parent? Reject
  moves between equally plausible parents.
- **Redactions**: does the find-text occur exactly once, and is the
  replacement neutral?
- **Duplicates**: do the two issues share acceptance criteria, or overlap on
  one axis while diverging on another?

A wrongly closed issue is lost work. **Doubt rejects the mutation** — the
issue reverts to `keep` (or `unverifiable-keep`) with the verifier's
objection recorded in the report. Only verifier-upheld actions, in the
verifier's final wording, proceed to Phase 5.

---

### Phase 5: Apply

**Dry-run (default)**: report every verdict; mutate nothing — except
boundary-leak redaction, which is applied in every mode and reported with an
explicit `REDACTED (applied in dry-run)` line.

**With `--apply`**, one applier per batch executes exactly the upheld action
list, in this order, and confirms each mutation took by re-reading the issue:

1. **Duplicate folds** — comment on the survivor folding in the duplicate's
   unique acceptance criteria, then close the duplicate with an evidence
   comment linking the survivor.
2. **Obsolete / not-worth closures** — post the evidence comment, then close
   (`--reason completed` when the work landed, otherwise `not planned`).
3. **Appended sections** — fetch the current body to a file, append the
   section, send the file back; skip when the heading already exists:

   ```bash
   BODY=$(mktemp)
   gh issue view "$N" -R "$OWNER_REPO" --json body -q .body > "$BODY"
   grep -q "^## Verification (added" "$BODY" || {
     printf '\n\n' >> "$BODY"; cat section.md >> "$BODY"
     gh issue edit "$N" -R "$OWNER_REPO" --body-file "$BODY"
   }
   ```

   Section shapes:

   ```markdown
   ## Reassessment correction (<YYYY-MM-DD>)

   <what changed, which PR/commit falsified the original premise, and what remains true>

   ## Verification (added <YYYY-MM-DD>)

   <named test/command, input, expected observable output — one bullet per AC>

   ## Security constraints (<YYYY-MM-DD>)

   <hazard in the proposed design → mitigation stated as an acceptance criterion>

   ## Clarification (<YYYY-MM-DD>)

   <the single intended reading, and what is explicitly out of scope>
   ```

4. **Epic moves** — resolve both node IDs, then re-parent through the
   sub-issue API (cross-repo parents are allowed):

   ```bash
   gh api graphql -f query='mutation{addSubIssue(input:{
     issueId:"<epicNodeId>",subIssueId:"<issueNodeId>",replaceParent:true
   }){issue{number}}}'
   ```

5. **Field corrections** — via the Go binary, never raw mutations:

   ```bash
   nightgauge project set-field <num> --priority P1 --size M
   nightgauge project sync-status <num> <status>
   ```

6. **Label changes** — resolve node IDs first with
   `nightgauge label list --json`, then mutate via the typed forge
   commands:

   ```bash
   nightgauge forge label add --issue-id <node> --labels <label-ids>
   nightgauge forge label remove --issue-id <node> --labels <label-ids>
   ```

   Never raw `nightgauge forge graphql` label mutations — the ADR-008
   raw-GraphQL carve-out does not cover labels, and raw GraphQL breaks the
   GitLab portability the typed commands preserve.

Every mutation appends one line to the report's applied-actions table:
issue, action, evidence pointer. A failed mutation is recorded as failed,
never retried into a different action.

---

### Phase 6: Stale-Sync Sweep

Catch CLOSED issues whose board Status never advanced to Done — the
failure mode that ghosts items in the VSCode extension. Runs against the
full (unfiltered-by-state) board inventory from Phase 1:

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

Write `.nightgauge/triage/backlog-groom-<timestamp>.md` (and `.json`) with:

1. **Outcome counts** — issues assessed, kept, closed (obsolete / duplicate /
   not-worth), clarified, verification-added, premise-corrected,
   security-constrained, moved, re-ranked, redacted, stale-synced,
   unverifiable-kept
2. **Per-class listings** — every non-`keep` verdict with its one-line reason
   and evidence pointer; rejected mutations listed with the verifier's
   objection
3. **Security constraints table** — issue, hazard, mitigation added
4. **Epic moves table** — issue, old parent → new parent, reason
5. **Field-correction table** — issue, field, old → new, reason
6. **Boundary redactions** — explicit lines, flagged as applied even in
   dry-run
7. **Checkout SHAs** — the `origin/main` each repo was assessed against
8. **What to work next** — ranked list under the fixed rubric, ending with a
   **single recommended next epic** and the reasoning

In dry-run, close with the exact `--apply` invocation that would execute the
verified verdicts.

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

## Decision Rules

- **Semantic vs structural**: rank, worth, validity, premise truth,
  verification completeness, security of design, epic fit → this skill.
  Labels, board fields, headings, `blockedBy` wiring → issue-audit riders;
  report them, repair them only through issue-audit's primitives.
- **Groom vs preflight**: this skill judges the whole open backlog on a
  cadence; `backlog-preflight` judges a chosen batch immediately before a
  run. Preflight does not author verification — it fails an issue that lacks
  it, which is why this skill adds it first.
- **Closure bar**: independent evidence citing PR/commit/file, confirmed by
  the adversarial pass. Anything less stays open.
- **Verification bar**: a reviewer with the tree and the command could tell
  pass from fail without asking the author.
- **Security bar**: judged against `standards/security.md`; a hazard becomes
  an acceptance criterion, never a comment the pipeline can ignore.
- **Epic verdicts**: an epic is judged as a unit with its sub-issues in one
  batch; never close an epic while it has open sub-issues.
- **Repo scope**: in a repo-scoped run, sibling-repo items on a shared board
  are inventory-filtered out, never assessed, never mutated. In a workspace
  run, every repo in the manifest plus every parent repo is in scope.
- **Redaction scope**: redact only the leaked span (neutral-referent
  replacement); the rest of the body is untouched human-authored content.
- **Idempotency**: re-running after `--apply` must find nothing to re-apply —
  closures, appended headings, parents and field states are all convergent.

## Failure Conditions (exit 2)

Distinct from a clean run that simply finds nothing:

- Go binary missing (Phase 0 preflight cascade exhausts)
- Forge unauthenticated, or a project board number cannot be resolved from
  `.nightgauge/config.yaml` / the workspace manifest
- Board inventory pagination fails mid-run (partial inventory would produce
  false "missing from board" riders — abort instead)
- A batch manifest that does not cover every open issue exactly once
  (Phase 2) — a silent gap reads as "assessed and kept"

## Completion Checklist

- [ ] Inventory paginated to exhaustion (`hasNextPage` false), scoped to the
      target repo or the full workspace, checkouts fetched (Phase 1)
- [ ] Theme batches recorded in the report and proven to cover every open
      issue exactly once (Phase 2)
- [ ] Every open issue carries a verdict, a one-line reason, and a
      `has_verification` flag (Phase 3)
- [ ] Every proposed mutation adversarially verified or rejected (Phase 4)
- [ ] Dry-run mutated nothing except boundary redactions; `--apply` actions
      each carry an evidence pointer and a re-read confirmation (Phase 5)
- [ ] No open issue left without verification steps (Phase 5)
- [ ] Stale-sync sweep ran over CLOSED board items (Phase 6)
- [ ] Report written with outcome counts, per-class listings, security and
      epic-move tables, checkout SHAs, and a single recommended next epic
      (Phase 7)
- [ ] Run record appended to `RUN_LOG` (skipped in dry-run)
