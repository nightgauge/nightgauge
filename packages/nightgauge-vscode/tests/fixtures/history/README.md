# History fixtures

`go-writer-runs.jsonl` — five completed run records **produced by the Go
`HistoryWriter.BuildV2Record`**, not hand-written.

That distinction is the whole point (#1213). The per-(stage, model) calibration
loop reads `tokens.per_stage[*].model`, and for the life of the feature nothing
wrote that field — `PostPipelineAnalyzer`'s `.filter(([, usage]) => usage.model)`
dropped every row, so `stage-model-calibration.json` did not exist in any
workspace after hundreds of runs while the docs described the loop as working.

The existing calibration test could not catch that, because it hand-built its
records **with** a `model` key: it asserted the analyzer's arithmetic against an
input shape no writer produced. A fixture emitted by the real writer is the only
version of that test that can go red when the writer stops emitting the field.

Only the volatile fields (`run_id`, timestamps, duration) are normalised, so the
file is stable under review. To regenerate, add a temporary generator test
against `BuildV2Record` — do not hand-edit the token or model fields.
