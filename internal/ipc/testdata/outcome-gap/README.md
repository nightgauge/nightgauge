# `outcome-gap` fixtures — provenance

Captured by `scripts/capture-outcome-gap-fixture.sh` from the real pipeline
telemetry of a live Nightgauge workspace. Nothing here is hand-authored: the
shapes under test in `internal/ipc/server_learning_outcome_test.go` are
redacted copies of records this machine's pipeline actually wrote.

## Captured

- **Date (UTC)**: 2026-08-07
- **History window scanned**: `2026-05-09` … `2026-08-02`
- **Run records in that window**: **2008**
- **…identified as extension-path**: **66**
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
- **8/8** have `predictedSize: "small"`, because the pre-#304 writer
  ran an unknown complexity score straight through `SizeBucketForScore`, which
  maps 0 onto the same label as a genuinely small issue.
- **8/8** have `complexityScore: 0`.
- **8/8** have no `actualSize` at all — the field every
  calibration consumer compares `predictedSize` against had no production
  writer, so size accuracy had never once been measured.

`run-record.json` and `run-record-failed.json` are the counter-evidence: a real
extension-path run record already carries `stages[*].model_selection.model`,
per-stage cost, token totals and duration. `issue-context.json` carries the
routing prediction — complexity score, predicted dev model, size label — that
the same run's outcome was written without.

## Files

- `run-record.json` — real extension-path run record, `outcome: complete`.
- `run-record-failed.json` — real extension-path run record, `outcome: failed`.
- `outcome.json` — real scheduler-path learning outcome, the degenerate shape.
- `issue-context.json` — real `issue-{N}.json` routing classification,
  projected down to the four fields `loadIssueClassification` reads.

## Redaction — what is and is NOT preserved

This is a public repository, and the live corpus these fixtures come from mixes
private repositories' telemetry. Redaction is therefore **deny-by-default on
string values**, not an allowlist of known-sensitive field names:

- Every string anywhere in a captured record is dropped to `"REDACTED"` unless
  it is a bare machine token — `^[A-Za-z0-9][A-Za-z0-9._:+-]*$`, i.e. no
  spaces, slashes, `#` or `@`.
- **Free-text diagnostic fields are NOT preserved verbatim.** Stage `error`
  strings, multi-word `punt_reason` values, issue titles and bodies are prose
  and are replaced. An earlier allowlist-shaped version of this script kept
  them, and a real `PR #187` from the source workspace shipped in the failed
  run record as a result.
- Identity fields are then overwritten with stable placeholders: `acme/widget`,
  issue `1001`/`1002`/`1003`, `feat/<n>-redacted`, `main`.
- `labels` keep only closed-vocabulary namespaces (`type:`, `size:`,
  `priority:`, `status:`); `component:`/`area:` labels can name private
  subsystems and are dropped.
- `issue-context.json` is a strict **projection** — only `issue_number`,
  `type`, `labels` and `routing.{complexity_score,pickup_recommendation.dev_model}`
  are emitted; the issue body, acceptance criteria and routing rationale are
  never read into the output at all.

What survives is exactly the record **shape** the tests assert on: enums,
statuses, stage names, model ids, adapters, run ids, ISO timestamps, and every
numeric field (tokens, per-stage cost, durations, complexity score).

The script re-walks each emitted document and **aborts** if any string is
neither a bare token nor one of its own placeholders. The publication-boundary
guard scans the tracked tree but does not inspect fixture string contents, so
this check is the only mechanical gate — it fails closed by design, and it
self-tests against a poison document (a forge reference inside a stage error, a
real repo slug, a home path, an e-mail) before it is trusted to accept anything.

## Regenerating

```bash
scripts/capture-outcome-gap-fixture.sh [WORKSPACE_ROOT ...]
```

Pass several roots for a multi-repo workspace — run history and issue context
files commonly live in different repos. Selection is deterministic (first match
in sorted root/file/line order) and the output is Prettier-normalized before it
lands, so re-running against the same roots reproduces byte-identical fixtures
that pass `npm run format:check`. Against a different workspace the numbers
above change — update this README from the script's output rather than editing
it by hand.
