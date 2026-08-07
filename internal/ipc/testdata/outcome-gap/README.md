# `outcome-gap` fixtures — provenance

Captured by `scripts/capture-outcome-gap-fixture.sh` from the real pipeline
telemetry of a live Nightgauge workspace. Nothing here is hand-authored: the
record shapes under test in `internal/ipc/server_learning_outcome_test.go` are
verbatim (redacted) copies of records this machine's pipeline actually wrote.

## Captured

- **Date (UTC)**: 2026-08-07
- **History window scanned**: `2026-05-09` … `2026-08-02`
- **Run records in that window**: **1996**
- **…identified as extension-path**: **54**
- **Learning outcome records in the corpus, all time**: **8**
- **Most recent outcome record**: `2026-07-22T22:52:01.597397-06:00`

Extension-path identification (both signals required, see the script):
no `outcome_prediction` (the Go scheduler always sets one) **and** at least one
stage carrying `execution_path` (only the TypeScript HeadlessOrchestrator
produces that, #309). The gap #304 fixes is the whole distance between those
last two numbers: every extension-path run wrote a run record and no outcome.

## The corpus was not just small — it was degenerate

Of the 8 outcome records that exist:

- **8/8** have `predictedModel: ""` — no model attribution at all,
  so the model-routing calibration had nothing to calibrate on.
- **8/8** have `predictedSize: "small"`, because the scheduler's
  `predictedSizeLabel(0)` maps an unknown complexity score onto the same label
  as a genuinely small issue.
- **8/8** have `complexityScore: 0`.

`run-record.json` and `run-record-failed.json` are the counter-evidence: a real
extension-path run record already carries `stages[*].model_selection.model`,
token totals and cost, so the outcome derived from it at the IPC seam is
non-degenerate. That is what `TestLearningOutcomeFor_FromCapturedRunRecord`
asserts.

## Files

- `run-record.json` — real extension-path run record, `outcome: complete`.
- `run-record-failed.json` — real extension-path run record, `outcome: failed`.
- `outcome.json` — real scheduler-path learning outcome, the degenerate shape.

## Redaction

This is a public repository. The script replaces repo/owner, issue number,
issue title, issue body, branch name, and every absolute `/Users/...` or
`/home/...` path with stable placeholders (`acme/widget`, `1001`/`1002`/`1003`,
`feat/<n>-redacted`, `/REDACTED/HOME`). Record _shape_ — stages,
`model_selection`, token totals, `outcome`, `routing`, `size` — is preserved
untouched, and shape is the only thing the tests assert on.

## Regenerating

```bash
scripts/capture-outcome-gap-fixture.sh [WORKSPACE_ROOT]
```

Selection is deterministic (first match in sorted file/line order), so
re-running against the same history reproduces byte-identical fixtures. Against
a different workspace the numbers above change — update this README from the
script's output rather than editing it by hand.
