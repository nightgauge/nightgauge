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
- **Most recent outcome record**: `2026-07-22T22:52:01-06:00`

Extension-path identification (positive signal only, see the script): at least
one stage carrying `execution_path`, which only the TypeScript
HeadlessOrchestrator produces (#309). It deliberately does not also require
`outcome_prediction` to be absent — that held only for pre-#304 records, so the
selector would have matched nothing once those aged out of the scanned window.
The gap #304 fixes is the whole distance between those last two numbers: every
extension-path run wrote a run record and no outcome.

## The corpus was not just small — it was degenerate

Of the 8 outcome records that exist:

- **8/8** have `predictedModel: ""` **and** `actualModel: ""` — no model
  attribution at all, so the model-routing calibration had nothing to calibrate
  on. Worse, the reader counted `"" == ""` as a routing HIT, so `learn tune`
  reported `modelAccuracy 1.0` from eight rows that measured nothing.
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
  Documentation-only; no test reads it.
- `issue-context.json` — real `issue-{N}.json` routing classification,
  projected down to the four fields `loadIssueClassification` reads.

## The MATCH/MISS pair these fixtures exist to make possible

`issue-context.json` predicts `dev_model: sonnet`; `run-record.json`'s
`feature-dev` stage served `claude-sonnet-5`. Normalized onto registry bands
those agree, so the canonical run records a routing **HIT** —
`TestLearningOutcomeFor_ModelPairMatchesWhenRoutedRight_MissesWhenNot` then
substitutes an opus served model on the same record and requires a **MISS**.

Both verdicts have to be reachable or the field is not a measurement, and this
pair is what fails if either round-2 defect returns: a tautological
`actualModel := predictedModel` reports a match for the mis-routed case, and
attributing the run to its dominant-cost stage does too (this record's most
expensive stage is `feature-planning`, not `feature-dev`).

The same fixtures pin the size pair's honesty from the other direction. The
captured context is `size:M` + `priority:critical`, which scores 5 — so the
label-derived "actual" the round-2 code wrote produced `predicted medium` vs
`actual small`, a permanent MISS for a run the router sized exactly right. The
tests now require `actualSize` to be **absent**.

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
- **High-resolution join keys are blunted, because the string allowlist cannot
  see them.** An RFC3339 timestamp and a UUID are bare machine tokens, so
  `SAFE_TOKEN` accepts both by construction, and numbers were never inspected at
  all — leaving a microsecond `completed_at` plus a millisecond `duration_ms` and
  the platform's `run_id` as an exact pointer back to one run in the source
  workspace. Sub-second precision is dropped, durations are rounded to the
  second, and every `run_id` becomes the fixed placeholder
  `00000000-0000-7000-8000-000000000000`. The verifier now rejects a sub-second
  timestamp or any other UUID **before** the token check, and both shapes are in
  its poison self-test.

What survives is exactly the record **shape** the tests assert on: enums,
statuses, stage names, model ids, adapters, second-precision ISO timestamps, and
the numeric fields (tokens, per-stage cost, complexity score).

The script re-walks each emitted document and **aborts** if any string is
neither a bare token nor one of its own placeholders. The publication-boundary
guard scans the tracked tree but does not inspect fixture string contents, so
this check is the only mechanical gate — it fails closed by design, and it
self-tests against a poison document (a forge reference inside a stage error, a
real repo slug, a home path, an e-mail, a sub-second timestamp and a real
`run_id`) before it is trusted to accept anything.

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
