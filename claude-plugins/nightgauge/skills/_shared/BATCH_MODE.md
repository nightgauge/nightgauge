## Batch Mode

**Batch mode is additive.** Every stage has a single-issue path that is the
default and is never modified by this section. A batch context file is an
_optional overlay_: when the file this stage looks for is absent — the normal
case — continue the single-issue path unchanged and do not mention batch mode
again. Never invent, guess at, or synthesize a batch context file that is not
on disk.

### What batch mode is

An epic whose sub-issues touch overlapping files can be run once instead of N
times. `/nightgauge-assess-epic` makes that call and writes
`.nightgauge/pipeline/epic-assessment-{E}.json` with a `strategy` of
`parallel`, `mixed`, or `sequential`. When the pipeline acts on a `parallel`
(or the independent part of a `mixed`) assessment, it carries **one shared
feature branch and one PR for the whole group**, and each stage writes a batch
context file alongside — never instead of — its normal single-issue output.

**The batch key is the EPIC number `E`, never a sub-issue number.** Every batch
context file is keyed on it and the shared branch is named for it. A stage that
keys a batch file on a sub-issue number produces a file the next stage will
never look for, and the batch silently degrades to a single issue.

### The per-stage contract

| Stage              | Batch input                         | Batch output              |
| ------------------ | ----------------------------------- | ------------------------- |
| `issue-pickup`     | `epic-assessment-{E}.json`          | `batch-{E}.json`          |
| `feature-planning` | `batch-{E}.json`                    | `planning-batch-{E}.json` |
| `feature-dev`      | `planning-batch-{E}.json`           | `dev-batch-{E}.json`      |
| `feature-validate` | `dev-batch-{E}.json`                | `validate-{E}.json`       |
| `pr-create`        | `dev-batch-{E}.json`                | `pr-{E}.json`             |
| `pr-merge`         | `dev-batch-{E}.json`, `pr-{E}.json` | — (removes the batch set) |

All paths are relative to `.nightgauge/pipeline/`. The schemas — every field,
every required key, worked examples — are in `docs/CONTEXT_ARCHITECTURE.md`
under the batch context file sections. Do not improvise a shape: a batch file
that does not match its schema is not detected by the next stage, which
degrades to the single-issue path and silently drops the rest of the batch.

### Detection is a file test, nothing more

Resolve `E`, test for this stage's batch input, and branch:

```bash
# E is the epic number: `.parent_issue` from the issue context when this stage
# has one, otherwise the leading number in the branch name.
BATCH_INPUT=".nightgauge/pipeline/<this stage's batch input, from the table above>"
if [ -f "$BATCH_INPUT" ]; then
  BATCH_MODE=true
else
  BATCH_MODE=false # single-issue path, unchanged — the common case
fi
```

A stage that has a batch phase gives the concrete detection block, the batch
procedure, and its output schema in that phase. This section is the contract
those phases share, not a substitute for any of them.

### Invariants that hold in both paths

- **One PR per batch, not per issue.** The PR body carries an explicit
  `Closes #N` line for every issue in the batch, so no sub-issue is left open
  after the merge.
- **Every gate still runs.** Batch mode changes how many issues a run covers,
  never which validations execute. Build, tests, and stage gates run once over
  the combined change set — they are not skipped or sampled.
- **A batch failure is not a batch-wide abort by default.** When validation
  fails the options are: retry the batch, split it into single-issue runs, or
  stop for a human. Splitting is preferred over discarding completed work.
- **Batch context files are pipeline exhaust.** They are cleaned up at the end
  of the run alongside the single-issue context files. Leaving them behind
  makes the next, unrelated run for that epic number detect a stale batch.
