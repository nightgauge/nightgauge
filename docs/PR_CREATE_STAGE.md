# PR Create Stage — Two-Path Architecture

> Status: shipped in PR #3265.
>
> Predecessors: #3264 (deterministic-first pr-merge), epic #3261.

The `pr-create` pipeline stage runs in one of two modes:

1. **Deterministic path** (default) — a Go-native stage runner that reads
   `dev-{N}.json` / `validate-{N}.json` / `issue-{N}.json` /
   `planning-{N}.json`, evaluates a pure decision rule over a typed snapshot,
   renders a PR title and body from a fixed template, pushes the feature
   branch, and calls `internal/github.PRService.CreatePR` directly. Zero LLM
   tokens, zero subagent processes, completes in seconds.
2. **LLM path** (fallback) — the existing `nightgauge-pr-create` skill
   runs via the normal `StageRunner` → Claude pipeline. Reached whenever the
   deterministic runner punts (sparse context, spike, security or
   scope concern, push failure, …).

The skill's downstream contract is unchanged: it writes `pr-{N}.json` to the
clone's pipeline state directory (`nightgauge layout path pipeline`) with the
same schema today's skill
emits. The deterministic runner produces a superset-compatible payload —
`preflight_results` and `ci_monitoring` are populated with conservative
zero-values that downstream consumers (the pr-merge deterministic runner from
#3264, the VSCode extension's execution-history view) treat as "not run."

## The commit owner (issue #1179)

`pr-create` owns the run's commit. Not "commits when convenient" — it is the
stage the pipeline holds responsible for the branch carrying its work.

`feature-dev` deliberately does not commit (issue 1608), and `feature-validate` is
the stage that commits and pushes. That was a **convention stated in a skill
file**, and routing can skip the stage that holds it: the fast-track path
([GATE_RELAXATION.md](GATE_RELAXATION.md)) emits `skip_stages =
[feature-planning, feature-validate]` for a trivial change. With the only
documented owner skipped, the working tree reached `pr-create` uncommitted,
the branch had zero commits ahead of base, and the PR would have opened empty.
The stage that noticed wrote a `commit_note` in `dev-{N}.json` addressed to a
stage with no instruction to read it, and filed `medium` friction in a
self-assessment nothing consumes.

The owner is now `stages.DecideCommit` +
`DeterministicPRCreateRunner.ensureWorkCommitted`, compiled, running at the
**head of `Run`** — before `DecideCreate` and before the client-wiring check.
Placement is the fix:

- Before `DecideCreate`, so a punt still commits and the LLM skill, when it
  runs, opens the PR from a branch that carries the work. (A route that skips
  feature-validate no longer punts for that reason: see the decision matrix.)
- Before the `prClient == nil` check, so an unwired GitHub client cannot
  silently disable it.
- Inside the runner rather than the scheduler, so the Go scheduler and the
  extension's `nightgauge pr-stage create` path (#300) share one
  implementation instead of drifting.
- In `pr-create`, because `schedulerSkippableStages` honours only
  `feature-planning` and `feature-validate` — `pr-create` can never be routed
  out of the chain, and `feature-validate` can.

### Decision matrix (first match wins)

| Condition                                | Outcome | Reason                             |
| ---------------------------------------- | ------- | ---------------------------------- |
| Branch name unreadable                   | Refuse  | `commit-refused: branch-unknown`   |
| Detached HEAD                            | Refuse  | `commit-refused: detached-head`    |
| Current branch == base branch            | Refuse  | `commit-refused: branch-is-base`   |
| Commits-ahead count unknown              | Refuse  | `commit-refused: ahead-unknown`    |
| Commits ahead of base > 0                | No-op   | `commit-not-needed: commits-ahead` |
| Unmerged index (conflict stages present) | Refuse  | `commit-refused: unmerged-index`   |
| Clean tree                               | No-op   | `commit-not-needed: clean-tree`    |
| Only untracked pipeline exhaust          | No-op   | `commit-not-needed: exhaust-only`  |
| Otherwise                                | COMMIT  | `committed`                        |

The `commits-ahead > 0` arm is what leaves the normal path untouched:
`feature-validate` committed, so the owner declines without touching git's
object store. The base branch is never committed to, and a run stopped at a
conflict is refused rather than having its conflict markers committed (the
#301 reasoning `RecoverUncommittedWork` records).

What lands in the commit is `reclaim.ClassifyStatus`'s **blocking** set —
deliverables plus TRACKED bookkeeping changes (#237/#332). The pipeline's own
UNTRACKED exhaust is unstaged after `git add -A` (never excluded via pathspec,
which exits 1 on gitignored paths). The message is
`stages.CommitMessage(snapshot)`: the same conventional-commit subject
`RenderTitle` produces, plus a fixed provenance line — deterministic, so a
retry over the same tree produces the same message.

The outcome is reported on every path: `PRCreateResult.CommitPerformed /
CommitSHA / CommitReason`, a scheduler log line, `commit_owner` /
`commit_sha` / `commit_made` on the `stage_deterministic` telemetry event, and
`commit_made` / `commit_sha` / `commit_reason` on `nightgauge pr-stage create
--json`. A refusal or a git failure is recorded and non-fatal — it never turns
an empty PR into a crashed stage.

## When the deterministic path runs

The runner evaluates this decision matrix (in priority order — first match
wins):

| Snapshot signal                                                                                        | Result | Reason                                    |
| ------------------------------------------------------------------------------------------------------ | ------ | ----------------------------------------- |
| `dev-{N}.json` missing or unparseable                                                                  | Punt   | `missing-dev-context`                     |
| `issue.type == "spike"`                                                                                | Punt   | `spike-issue`                             |
| Branch missing or equal to base branch                                                                 | Punt   | `branch-is-base`                          |
| `validate-{N}.json` missing, route skipped feature-validate, dev build/tests not failed, files changed | Create | `rich-context: validate-skipped-by-route` |
| `validate-{N}.json` missing (not route-skipped)                                                        | Punt   | `missing-validate-context`                |
| `validation_status` not passed/unverified                                                              | Punt   | `validation-not-passed: <s>`              |
| `errorCategory != ""`                                                                                  | Punt   | `validate-error-category: <c>`            |
| Any `dead_code_warnings[].severity == "error"`                                                         | Punt   | `dead-code-blocked`                       |
| Security scan failed (preflight rerun)                                                                 | Punt   | `security-scan-failed`                    |
| Scope drift gate failed                                                                                | Punt   | `scope-drift-failed`                      |
| Any `manual_checklist[].verified == false`                                                             | Punt   | `manual-checklist-unverified`             |
| `dev.files_changed` empty                                                                              | Punt   | `no-changes`                              |
| Otherwise (rich context)                                                                               | Create | `rich-context`                            |

When the rule says "create," the runner:

1. Calls `prClient.ListOpenPRsForBranch` for idempotency. If an open PR
   already exists for the branch, the runner returns `created` with
   `reason=pr-already-exists` (no second push, no second create call).
2. Pushes the feature branch (`git push -u origin <branch>`). Before pushing
   it removes generated Codex steering from a committed `AGENTS.md` in one
   repair commit, so the pull request's tree never carries it (issue 1675). If the push is
   rejected, the runner is **idempotent on the branch** (#3828): it checks
   whether `origin` already has the branch (feature-dev typically pushed it).
   If so, it proceeds to open the PR from the remote branch with
   `reason=push-failed-remote-branch-exists` — it does **not** punt to the LLM
   path, which would attempt a (correctly blocked) force-push and then dead-end
   on `AskUserQuestion` in headless mode. Only when the branch is genuinely
   absent from `origin` (or existence cannot be determined) does it punt with
   `reason=push-failed: <stderr>`.
3. Looks up the repository node ID and calls `prClient.CreatePR(ctx,
repoID, title, body, head, base)`.
4. Writes `pr-{N}.json` with the issue number, PR number, PR URL, title,
   base branch, knowledge path, and zero-value `preflight_results` /
   `ci_monitoring` blocks.

### Title template

```
<typePrefix>(#<N>): <text>
```

`<typePrefix>` is derived from `issue.type` (feature → `feat`, fix → `fix`,
docs → `docs`, …). `<text>` is `pr_summary` from `dev-{N}.json`, or the issue
title when no summary was recorded. Either source goes on one line, has closing
keywords rewritten to `Refs`, and loses any leading `<knownPrefix>(<scope>)?:`
segment.

The whole title is capped at 72 characters, counted in runes. Longer text is cut
at the last word boundary inside the limit, with no ellipsis; a first word that
alone exceeds it is the one case cut mid-word. Any title that does not use the
summary unchanged writes a stage-log line. `nightgauge pr-stage title <N>` prints
the same title for the LLM skill and the extension's last-resort fallback.

### Body template

Sections appear in fixed order:

```
## Summary

Implements #<N>: <stripped-title>

## Changes

Created:
- <sorted file list>
Modified:
- <sorted file list>
Deleted:
- <sorted file list>

## Validation

- Build: <passed | failed | skipped>
- Unit tests: <label>                    (see "Unit-test label" below)
- Integration tests: passed             (only when integration tests passed)
- Security scan: <result>                (only when not skipped)
- Scope drift:  <result>                 (only when not skipped)

## Acceptance criteria                    (only when at least one AC is met)

- ✅ met: <text> (<source>: <evidence>)

## ⚠️ Unmet acceptance criteria           (when any AC is not met, or there are none)

This PR leaves #<N> open. These criteria are unmet or could not be verified:

- <text>
  _<❌ unmet | ❔ not verifiable> — <source>: <evidence>_

## Knowledge                              (only when knowledge dir is populated)

<rendered knowledge bullets — same Go renderer the skill calls>

Part of #<PARENT>                         (only when issue.native_parent > 0)
Closes #<N>                               (Refs #<N> unless every AC is met)

<!-- nightgauge:pipeline stage=pr-create issue=<N> run=<run-id> -->
```

`RenderTitle` and `RenderBody` are pure functions over `PRCreateSnapshot` —
no `time.Now`, no map iteration, no environment reads, sorted file lists.
The deterministic-property test calls each function 100× and asserts
byte-equal output across calls. (The run id is read from
`NIGHTGAUGE_RUN_ID` by the context loader, not by `RenderBody`.)

### Acceptance criteria and partial delivery (#2595, #2655, #2665)

The criteria come from `ac-reconcile-{N}.json`, which the orchestrator wrote
from the issue body before planning. pr-create first compares the report's
`ac_fingerprint` with `preflight.ACFingerprint` of the issue body as it reads it
now (#3228). On a mismatch it takes the criteria from the body, marks every one
the report never judged `not_judged` (`❔ not verified`, listed under the unmet
heading), and says `Refs`. A body it cannot read keeps the report's list and
also says `Refs`: pr-create never closes an issue whose scope it cannot
confirm. pr-create drops repeated criteria from that list,
so a list a pre-#2655 parser doubled still renders each criterion once.
`ResolveACVerdicts` (`prcreate_ac.go`) gathers up to three answers per
criterion:

- `deterministic`: a `satisfied` or `unsatisfied` from
  `preflight.ReconcileACs`, re-run on the branch tree.
- `diff`: `VerifyACAgainstDiff` (`prcreate_acdiff.go`), run against the
  working tree diffed with the merge base of `origin/<base>` (or `<base>`) and
  HEAD, plus untracked deliverable files. Uncommitted work counts, because
  pr-create reads context before its commit owner runs. Both diffs the judge
  parses, this one and the merged range below, pass `--src-prefix=a/` and
  `--dst-prefix=b/`, so `diff.noprefix` and `diff.mnemonicPrefix` in a git
  config do not change what is read, and their parser decodes a path git
  C-quotes (a quote, backslash or control byte in the name) (#3505).
- `feature-validate`: the verdict in `validate-{N}.json`
  `acceptance_criteria_verdicts`, matched by criterion text, or by `index`
  only when the verdict has no text. Evidence that says the criterion is not
  met resolves `unmet`.

Any `unmet` wins, so a diff heuristic never closes an issue another source
judged unmet. Otherwise any `met` wins, cited from the first of deterministic,
diff and validate. Otherwise the criterion is `not_verifiable`.

A `not_verifiable` criterion is then credited to earlier delivery on the same
issue (#3362). It is `met` with source `prior-pr` and `met_by_pr` when a MERGED
PR's trusted partial-delivery comment lists it under `Met by this PR:`, matched
in the issue body's wording under the writer's tick rule, so a strengthened
or reworded criterion is not credited. The newest merged record that names a
criterion decides: listed under `Met by this PR:` credits that PR, left under
`Remaining acceptance criteria:` (unmet) credits nothing, and listed under
`Not verifiable by the pipeline:` is skipped in favour of older records. It is `met` with source `prior-commit` and
`met_by_commit` when its evidence says a commit delivered it ("delivered by
<sha>", "implemented in commit <sha>", not negated or hedged) and that commit is an ancestor of
`origin/<base>`; a SHA cited for any other reason, or a criterion with a
runtime step, is not credited. A closed-unmerged or open PR, or a commit only on the feature
branch, credits nothing, and neither `unmet` nor `not_judged` is lifted. A
record that cannot be read credits nothing, so a criterion only an earlier PR
met keeps the PR at `Refs`. Each met bullet names what met it: `(this PR —
<source>: <evidence>)`, `(this PR)`, `(met by #N, merged earlier)` or
`(delivered by <sha> on <base>)`.

The diff verifier first rejects criteria that need a runtime or operator step:
a physical device, production, a deploy, a release, a manual or operator step,
a credential, a live API call, a post-merge observation or a live run. They
stay `not_verifiable`, and the row reads `_❔ not verifiable — needs: <step>_`.
Otherwise it splits the criterion at `, and` and `;` and gives each clause to
the first rule that reads it. A clause no rule reads is skipped. The worst
clause decides.

| Rule         | Reads                                                                                                                                                                                                                                                                                                                                  | `met` when                                                                                                                                                                                                                                                                                                                        |
| ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| scope        | "no file other than `X`", "only `X` changes"                                                                                                                                                                                                                                                                                           | every changed file is a named path, or under a named `dir/`                                                                                                                                                                                                                                                                       |
| preservation | a preservation verb ("keeps", "remains", "stays", "still passes", "unchanged", "is not modified", "does not change") governing a code span that is not a path, with no heading (not when the clause asks for a removal or an absence, or names a test `TestX`/`test_x` or a `go test -run` pattern, which keep the test and run rules) | every governed span is on the branch and no non-comment line the diff adds or removes contains it; unmet with `file:line` when one does, or when the span is nowhere on the branch. "Keep `X` and add `Y`" is judged on `Y` alone. Comments are keyed on the file extension: in Markdown a `* ` bullet or `# ` heading is content |
| unchanged    | "unchanged", "not modified"                                                                                                                                                                                                                                                                                                            | a named path is not in the diff; for named headings, the diff deletes nothing and adds nothing under any heading not qualified by "existing"                                                                                                                                                                                      |
| changelog    | "changelog" plus a `## …` heading, no path                                                                                                                                                                                                                                                                                             | `CHANGELOG.md` is in the diff and an added line sits beneath the heading; unmet when `CHANGELOG.md` is not in the diff                                                                                                                                                                                                            |
| removal      | a removal verb governing a code span, for example "`X` is removed", "Delete `X`", "`X` no longer exists" (not when the prose says "covers", "passes", "asserts") plus `TestX`, `test_x`, or a bare `` `name` `` with no path or heading                                                                                                | the diff removes the definition (`Name*` is a prefix) and none remains on the branch; unmet with `file:line` while one remains; skipped when the diff never held it                                                                                                                                                               |
| absence      | "no longer contains", "does not contain", "returns no match" plus code spans                                                                                                                                                                                                                                                           | every span is absent from each named file on the branch (a deleted file holds nothing); with no path, a `git grep` span finds no match; unmet with `file:line` while one remains                                                                                                                                                  |
| run          | `go test -run 'P'` (quoted or not)                                                                                                                                                                                                                                                                                                     | `P`'s top-level element matches a Go test defined on the branch and the test run passed; a name also given outside `-run` uses the test rule instead                                                                                                                                                                              |
| test         | `TestX`, `test_x`, `` `it("…")` ``                                                                                                                                                                                                                                                                                                     | the test is defined on the branch and the stage's test run passed (feature-validate, else a clean feature-dev run); evidence cites the test and `file:line`                                                                                                                                                                       |
| section      | a diffed path plus backticked `## …` headings                                                                                                                                                                                                                                                                                          | an added line in that file sits beneath the headings and carries the clause's code spans and `#NNN` refs                                                                                                                                                                                                                          |
| token/file   | code spans and `#NNN` refs                                                                                                                                                                                                                                                                                                             | every one appears in the added lines or the post-change file (of the named diffed file, when one is named), and at least one is in the added lines                                                                                                                                                                                |

The preservation row has one narrower reading, for a clause that keeps a span's
tests (#3501). Its governing rule: **the test scope may turn a strict `unmet`
into `met` only when the span's tests are the one thing the clause preserves.
Whenever that is not certain, the clause keeps today's strict verdict.** The
strict verdict is the row's own line and presence check; a false `unmet` on an
unusual phrasing is acceptable, a false `met` is not.

- **A qualified test noun is scoped.** The noun ("test", "tests", "suite",
  "spec" or "specs") follows the span directly, or after one or two plain words:
  "The existing `slugify` unit tests are untouched". It must head its phrase
  ("test files" does, "test flag" does not). A qualifier is letters only and is
  not a function word, a verb-like word in "-s" or "-ing" ("existing" is the
  exception), an "-ly" adverb, or a result or preservation word. After qualifiers the noun
  is never "spec", "specs" or a lone "suite", "tests" takes no singular verb,
  and the rest of the clause may say only that the
  tests are kept, with no and/or before it says so. After "keep" or "does not
  change" a qualified noun has no verb of its own: "Keep the `slugify` unit
  tests passing" is scoped, "Keep `slugify` once tests pass" is strict.
- **A clause that preserves anything else is strict.** Nothing is scoped when
  the clause was split off at ", and", or when the text before the span is more
  than these, each optional: a command's result ("`npm test` passes with"), the
  verb keeping the tests ("keep", "does not change") and determiners ("the",
  "its", "all", "existing", "current"). So and, or, any other joiner and any
  other preservation wording before the span are strict. These stay `unmet`
  when `slugify` is rewritten: "The implementation and the associated `slugify`
  tests are untouched" and "Behavior is unchanged with `slugify` tests
  untouched". "Keep the `slugify` tests untouched" is scoped.

A scoped span is judged on test files that contain it at the diff's base, in
path or content. A bare name counts as whole words, and a case change ends a
word (`get` is in `get_test.go` and `getUser`, not in `widget`):

- `unmet` when the diff modifies, empties, deletes or renames one, comments
  included. The evidence names the base path.
- Strict when more than tests may be kept and the diff changes a line
  containing the span or a non-test file naming it. More may be kept when the
  words after the span say more ("tests and behavior", or "spec" or a lone
  "suite", which can be a document or a cipher suite), or when the criterion has another clause that is more than a
  command's result ("; so is the implementation").
- Otherwise `met` when at least one exists, every candidate was read, the diff
  touches none and it touches no test-like file that Rule 2 makes strict: an
  added test file, or a README or source line naming the span, does not count.

Three rules bound that judgement:

1. **The test scope never returns undecided.** When it cannot decide, the whole
   clause gets the strict verdict. That is when no base test file contains the
   span, a candidate is over the 1 MiB read limit, a read fails (`git grep`
   writing to stderr is one), the base or work directory is missing, a path
   is one a diff header cannot carry reliably, or Rule 2 says so.
2. **Files are classified by path only, in two classes.** No file's text
   classifies it, and a path ending `.md`, `.markdown`, `.rst`, `.adoc` or
   `.txt` is in neither class.
   - A **recognised test file** has a `test`, `tests`, `__tests__` or `spec`
     directory, or is named `*_test.*`, `*.test.*`, `*.spec.*` or `test_*.*`.
     A `docs` or `doc` directory does not exclude one:
     `internal/docs/slug_test.go` is a test file.
   - A **test-like path** contains `test`, `spec`, `.cy.`, `e2e` or `cypress`
     when lower-cased. Every recognised test file is test-like;
     `Tests/SlugifyTests.swift`, `src/SlugifyTest.java` and
     `cypress/slugify.cy.js` are test-like and not recognised.

   When the diff edits, empties, deletes or renames a file that is test-like,
   not recognised and holds the span in its path, base content or changed
   lines, or such a file cannot be read, the clause gets the strict verdict,
   also beside a kept test. Holding is the strict check's match, so `get` is in
   `widget` here. A rename counts by either path; an added file never counts.
   The five substrings are the whole definition, closed by decision: any other
   layout is a non-test file, and a false `unmet` from a substring
   (`src/latest.js`) is accepted.

3. **A result-only clause is judged like any other scoped clause.** "`slugify`
   tests still pass" and "the `slugify` tests must remain and pass" are `unmet`
   when a test file containing the span is edited, emptied or deleted, and
   `met` when none is touched. An `unmet` from another verdict, such as a
   failing test run, still outranks that `met`.

A missing token is `unmet` only when the clause names a diffed file. With no
diffed file named, the clause is skipped, because it may be about something
the diff does not hold.

A `name: value` span on a Go file is also matched as a composite-literal
field (#3424): it is met when an added line sets field `name` to `value`,
keyed (`name: value`) or positional, the position taken from the struct
type's field order with `go/ast` on the branch content. Inline struct types,
the elided element types of slice, array and map literals, and named types
declared in the same file resolve. When a positional literal on an added line
has a type declared elsewhere, or the file cannot be read or parsed, the span
is `❔ not verifiable`, never `❌ unmet`. A resolved field set to another value
stays unmet. A YAML file keeps its key-path rule. Any other file (Markdown,
JSON, TypeScript) is matched as text and carries no field order, so a span
only such files could answer is `❔ not verifiable` when the text is absent,
never `❌ unmet`. Only the files the criterion names decide this: an
unrelated changed Go file never turns it into `❌ unmet`, and a file the diff
deletes counts as deciding, since it cannot set the field. A field span is a Go
identifier, a colon, whitespace, then a value: a span containing `://` or a
colon with no whitespace after it (`https://x.io/a`, `localhost:8080`) is no
field span and keeps the text-match verdict, so a missing one stays unmet. A
span the Go matcher does not read as one literal value (`On: a:b`) is judged
in a named Go file by its text.

A criterion naming a CI check (`` `integration-tests` passes ``) cannot be
decided here, because the check runs only once the PR exists. pr-merge judges
it from the check's conclusion after the CI wait and can turn `Refs #<N>`
into `Closes #<N>`: see
[Check-named criteria at merge](PR_MERGE_STAGE.md#check-named-criteria-at-merge-3424).

`IssueClosedBy` writes `Closes #<N>` only when there is at least one criterion
and every one is `met`. Any `unmet` or `not_verifiable` criterion, or no
criteria at all, gives `Refs #<N>`. Met criteria render under
`## Acceptance criteria`; every other one renders once under
`## ⚠️ Unmet acceptance criteria`, with its status on an indented line.
`pr-{N}.json` records the verdicts as `acceptance_criteria` and the same
outcome as `closes_issue`. The ac-reconcile parser ignores criteria inside
`<details>` archives and duplicate criteria.

The post-merge hook reads the merged PR body. When it has no closing keyword
for the issue, the hook skips the close, the Done sync, the epic rollup, the
survival record and the validation marker (`reason: partial_delivery`). It
joins the issue's criteria (checkboxes, plus numbered and plain items under an
`Acceptance criteria` heading) with the PR's verdicts: it ticks each issue
checkbox the PR reported `✅ met`, keeps every criterion the PR left open even
if ticked, and comments the remainder on the issue. The row returns to Ready,
unless every remaining criterion is not verifiable or reserved to the
supervisor or owner, which keeps it In review. A re-run over an
already-merged PR does not tick. The comment lists the criteria the PR met
under `Met by this PR:`, numbered items included, and later remainders on the
issue leave those out unless a later PR reports them unmet again (a later
`not verifiable` is no evidence against them). The remainder is written as
unmet criteria under `Remaining acceptance criteria:` and not-verifiable ones
under `Not verifiable by the pipeline:`; pr-create reads
the same list back to credit a later PR (see above). An
unreadable body keeps the close.

On a closing merge the hook also ticks each issue checkbox the PR reported
`✅ met`, so a PR that pr-merge promoted to `Closes` leaves no box unticked
(#3424). A failed tick there is a warning, not a failed close.

A partial-delivery re-run whose feature-dev writes no changes re-judges the
remainder deterministically (#3423). Each criterion is judged against the
earlier PR's merged diff: the merge commit of the PR the
`nightgauge:partial-delivery pr=N` marker names, against its first parent.
The merge SHA is read from the forge when the run is scoped and stored as
`merge_sha` in `partial-delivery-{N}.json`. Only two rules can answer met there:
a preservation (the merged diff changed no non-comment line containing the span,
and the span exists after the merge; when the span's tests are the one thing
kept, as above, it touched no test file that contained the span at the first
parent, or, if that cannot be told or Rule 2 makes it strict, the line and
presence check decides) and a removal (the span existed at the first parent and
is gone after the merge).
Everything else is unmet, including
a presence criterion, a criterion with no code span, a span on neither side, a
merge commit missing locally, and a read error in the deciding check. A merged
diff with zero files is undecidable, so every criterion judged on it is unmet
(#3505). When every criterion is met,
the issue is closed as completed with an evidence comment, its matching boxes
are ticked and its board row moves to Done; the run ends `issue_closed`, not
as a failure. When any is unmet, the run is booked `not_pipeline_actionable` with
the unmet list and the issue waits in Backlog. What a model wrote is never read.

### Unit-test label

Counts come from feature-dev's `tests_status`; the pass flag comes from
feature-validate's `unit_tests.passed`. `not run` appears only when there
are no counts:

| Counts            | Validate passed | Label                                                                        |
| ----------------- | --------------- | ---------------------------------------------------------------------------- |
| none              | no              | `not run`                                                                    |
| none              | yes             | `passed`                                                                     |
| `F > 0`           | either          | `failed (P passed, F failed)`                                                |
| `P > 0`, `F == 0` | yes             | `passed (P passed, 0 failed)`                                                |
| `P > 0`, `F == 0` | no              | `feature-dev reported P passed, 0 failed; not confirmed by feature-validate` |

### Pipeline stamp (#1479)

Every PR or MR the pipeline opens carries two machine-readable marks, so
pipeline-driven merges can be told apart from interactively-authored ones:

1. **Footer.** The last line of the body is an HTML comment (invisible when
   rendered):

   ```
   <!-- nightgauge:pipeline stage=<stage> issue=<N> run=<run-id> -->
   ```

   - `stage` is the stage that opened the PR (`pr-create`).
   - `issue` is the decimal issue number the PR closes.
   - `run` is the pipeline run id (`NIGHTGAUGE_RUN_ID`), or `-` when none is
     known. Empty values render as `-`; whitespace and `--` inside a value
     become `_`.

   Both the deterministic path (`RenderBody`) and the LLM path (the
   `nightgauge-pr-create` skill's `PR_BODY` template) emit it. The format is
   produced by `forge.PipelineMarker` (`internal/forge/pipeline_marker.go`).

2. **Label `pipeline:created`.** The forge adapters' `CreatePR` apply it to any
   PR whose body contains the footer prefix `<!-- nightgauge:pipeline `. On
   GitHub the label is created on first use (`LabelService.Create` is
   idempotent by name) and then added; a labelling failure is logged but does
   not fail the create, because the PR already exists. On GitLab the label is
   sent with the MR create request, and GitLab creates it if absent.

**Stability contract.** External tooling may rely on: the prefix
`<!-- nightgauge:pipeline `, the space-separated `key=value` grammar, the keys
`stage`, `issue` and `run`, and the label name `pipeline:created`. New keys may
be appended before ` -->`; parsers must ignore unknown keys. Removing or
renaming a key, the prefix or the label is a breaking change and needs a
`### Changed` changelog entry and a deprecation window of at least one minor
release.

### Failure modes

| Reason                                    | Cause                                                       | What happens |
| ----------------------------------------- | ----------------------------------------------------------- | ------------ |
| `missing-dev-context`                     | `dev-{N}.json` missing or unparseable                       | Punt → LLM   |
| `missing-validate-context`                | `validate-{N}.json` missing and the route did not skip it   | Punt → LLM   |
| `rich-context: validate-skipped-by-route` | Route skipped feature-validate (#1968); the PR body says so | Create       |
| `validation-not-passed: <s>`              | Validation reported `failed` / `partial` / `skipped`        | Punt → LLM   |
| `validate-error-category: ↩`              | Hard-gate failure category recorded by feature-validate     | Punt → LLM   |
| `dead-code-blocked`                       | Any error-severity dead-code warning                        | Punt → LLM   |
| `security-scan-failed`                    | Preflight security re-scan failed                           | Punt → LLM   |
| `security-scan-failed` (path `blocked`)   | A recorded `security_scan` gate stop still holds on rescan  | Fail stage   |
| `scope-drift-failed`                      | type:docs / type:chore scope drift gate failed              | Punt → LLM   |
| `manual-checklist-unverified`             | Validation produced an unchecked manual checklist           | Punt → LLM   |
| `spike-issue`                             | `issue.type == "spike"` — defer to skill's spike artifact   | Punt → LLM   |
| `no-changes`                              | `dev.files_changed` is empty                                | Punt → LLM   |
| `branch-is-base`                          | Current branch equals base                                  | Punt → LLM   |
| `push-failed: …`                          | `git push -u origin <branch>` returned non-zero             | Punt → LLM   |
| `pr-client-unavailable`                   | Production scheduler did not wire the GitHub client         | Punt → LLM   |
| `create-call-failed: …`                   | Pre-flight passed but `CreatePR` returned an error          | Punt → LLM   |
| `context-invalid-json`                    | Issue/dev/validate context exists but is malformed JSON     | Punt → LLM   |
| `pr-already-exists`                       | (Not a punt) An open PR already exists for the head branch  | Created      |
| `rich-context`                            | (Not a punt) New PR was created                             | Created      |

### Context resolution (worktree mode) — #275

The runner reads its snapshot, `{issue,dev,validate,planning}-{N}.json`, from
the pipeline state directory resolved for `<workdir>`:
`<git-common-dir>/nightgauge/pipeline/`
([ADR-024 § 7](decisions/024-data-and-state-layout.md#7-per-clone-and-per-checkout-data)).
On worktree-isolated runs (`pipeline.worktree_base` set — the
autonomous/`nightgauge run` default), the run's worktree resolves to the main
clone's directory, so the stages and the runner see the same files whichever
checkout they start from; nothing is written into either working tree. The
scheduler hands the runner the **worktree** path via `stageWorkspace(runtime,
workspaceRoot)` — the same resolution the LLM path and the post-condition gates
use — not the bare `workspaceRoot`.

Before #275 the deterministic dispatch passed `workspaceRoot` directly, so on any
worktree-configured repo the runner found no `dev-{N}.json`, `DecideCreate`
returned `missing-dev-context`, and **every** run fell through to the ~$2 LLM
path (the dogfood workspace and its mobile repo were 0-for-N). The identical fix applies to the
pr-merge runner (reads `pr-{N}.json`, which pr-create writes into the worktree)
and to the recovery registry's `StageFailure.Workspace` (which re-runs both
runners and drives git-op recovery actions). For in-place runs with no worktree
(`runtime.WorktreeDir == ""`, e.g. VSCode/headless), `stageWorkspace` returns
`workspaceRoot` unchanged, so behavior for the non-worktree majority is
byte-identical.

## When the LLM path runs

The skill at `skills/nightgauge-pr-create/SKILL.md` runs unchanged when:

- The deterministic runner punts (any of the reasons above).
- The runner's GitHub client is not wired (production safety: every Run punts
  with `pr-client-unavailable` until the scheduler injects the client).

### Security rescan after a gate stop (#3419)

When pr-create Step 2.5.4 stopped on a secret scan it wrote
`pr-create-gate-stop-{N}.json` (`gate: security_scan`, `scanner`, `head_sha`).
On the retry the runner's `security-rescan` phase, before the decision matrix,
re-judges the stop. Every `security_scan` record takes the same path, whatever
its `scanner`:

- no record, or a record for another gate — no-op;
- the record names no commit (`head_sha` empty) — `blocked`: the history
  cannot be judged;
- the recorded commit is still an ancestor of HEAD
  (`git merge-base --is-ancestor`) — `blocked`, record untouched: a fix
  committed on top leaves the secret in the history the push would publish.
  Rewrite the branch history to drop that commit, then retry. A recorded
  commit that no longer exists in the object database is not an ancestor;
- otherwise gitleaks runs over `<base_branch>..HEAD` (the pre-push hook's
  invocation) and then `nightgauge scan diff-secrets --base <base_branch>`.
  Both clean — the record is removed and the create proceeds;
- any finding — the record is rewritten at the scanned `head_sha` (`scanner`
  `gitleaks` when gitleaks found anything, else `diff-secrets`) and the result
  is the terminal path `blocked` (reason `security-scan-failed`,
  `terminal_detail` the finding counts);
- the record is unreadable, HEAD is not the feature branch, the ancestry check
  fails, gitleaks is not installed or fails to run, or the diff scan cannot
  run — `blocked`, record untouched, fail closed.

`blocked` fails the stage without re-rendering the skill, in both the Go
scheduler and the TS orchestrator. A passing skill scan removes an earlier
`security_scan` record, so a fallback run is not blocked by a fixed secret. The TS orchestrator
keeps a `security_scan` record (the runner owns it) and never opens its
fallback PR around one, even after HEAD moved.

> **TS (VSCode dogfood) path — Issue #300.** The VSCode
> autonomous/concurrent runs execute `HeadlessOrchestrator.runPipeline`, not the
> Go scheduler, so pre-#300 pr-create always ran the LLM skill there. It now
> runs deterministic-first via `nightgauge pr-stage create` — the same runner
> this doc describes, constructed by `orchestrator.NewDefaultPRCreateRunner` —
> invoked with `--repo owner/name --workdir <worktree>`. On `created` the TS
> orchestrator records `execution_path="deterministic"` and skips the skill; on a
> punt it records `execution_path="llm"` + `punt_reason` and falls through to the
> skill; a GitHub rate-limit **defers** (no LLM fallthrough, #3976); `blocked`
> fails the stage with a `[pr-create-gate-stop] security_scan` error (#3419). See
> [PR_MERGE_STAGE.md](PR_MERGE_STAGE.md) for the shared seam and JSON contract.

The skill is responsible for content the deterministic path explicitly does
not author:

- Reviewer assignment (per-issue config; runner punts when reviewer logic is
  required and the configured reviewer differs from the PR author).
- CI monitoring with classified failures (Phase 3.5 of the skill).
- Spike artifact authoring (`type:spike` issues bypass the runner).
- Cross-repo PR bodies.

## Phase reporting on both routes (#1247, #1397)

`pr-create` reports its phases exactly as `pr-merge` does, through the same
`stages.PhaseReporter` on the context, and both of its routes — the Go
scheduler and the extension's `nightgauge pr-stage create --json` subprocess —
are wired the same way. The two verbs are wired **separately**, so one being
instrumented says nothing about the other; `TestPrStageCreate_EmitsPhases`
pins this one.

The contract (the live stderr stream, the `phases` array, the precedence rule
between them, and the cross-language sentinel guard) is documented once, in
[PR_MERGE_STAGE.md § Phase reporting on both routes](PR_MERGE_STAGE.md#phase-reporting-on-both-routes-1247-1397).

## Telemetry

Per-stage `execution_path` is recorded on `V2StageDetail` (Go) /
`HistoryStageDetail` (TS Zod). Values:

- `"deterministic"` — Go runner reported `created`, or `blocked` on a
  security rescan (`punt_reason` `security-scan-failed`, no PR opened).
- `"llm"` — Go runner punted; skill ran.
- _absent_ — record predates PR #3264; readers MUST treat as `unknown`.

`execution_path` is part of the local history record. Optional telemetry
integrations receive only fields present in their documented public schema; the
local value must not be assumed to upload automatically.

When the deterministic path lands a create, the scheduler emits a
`stage_deterministic` `pipeline_event` with metadata identical in shape to
the pr-merge variant so the dashboard panel groups across stage names:

```json
{
  "path": "created",
  "pr_number": 3265,
  "pr_url": "https://github.com/owner/repo/pull/3265",
  "reason": "rich-context",
  "duration_ms": 1234
}
```

## Post-stage gate

The skill's existing post-stage activity (writing `pr-{N}.json`, signaling
project-board move-status) runs after either path. The deterministic runner
writes `pr-{N}.json` with the same schema, so the downstream pr-merge
deterministic runner from #3264 (`readPRContextNumber`) reads it unchanged.

There is no separate "verify created" post-stage gate analogous to
`verifyPRMerged` — pr-create is itself a single-step state transition. The
LLM path's CI-monitoring loop is preserved when the runner punts.

## Decisions (ADRs)

The full ADR set lives in
`.nightgauge/knowledge/features/3265-deterministic-first-pr-create-stage-template/decisions.md`.
Highlights:

- **ADR-001** Deterministic path is the default; LLM is the punt fallback.
  Cost per pr-create run drops from ~$1–$3 to ~$0 for the rich-context
  majority. The skill remains the canonical path for spike/sparse runs.
- **ADR-002** `execution_path` is per-stage, not per-run (mirrors #3264 ADR-002).
  Forward compatible for the next deterministic stage.
- **ADR-003** Decision rule is a pure function over `PRCreateSnapshot`,
  exhaustively unit-tested separately from the GitHub shell-out.
- **ADR-004** Title and body renderers are pure — no `time.Now`, sorted
  file lists, byte-equal output across repeated calls. The
  deterministic-property test asserts this directly.
- **ADR-005** Reviewer assignment punts to the LLM path (per-issue config is
  not derivable from rich-context alone). Bringing reviewer logic into the Go
  runner is a follow-up.

## Migration notes

- Behavior is unchanged for spike or sparse runs (the LLM path
  receives the same input it always did).
- For clean-context runs that previously cost ~$1–$3, the same PR now
  lands in ~1 s with zero tokens.
- `V2StageDetail.execution_path` is additive — older daily JSONLs parse
  unchanged; readers see absence as "unknown" rather than a value.
- The skill stays the source of truth for CI monitoring, reviewer
  assignment, and spike-artifact authoring.
