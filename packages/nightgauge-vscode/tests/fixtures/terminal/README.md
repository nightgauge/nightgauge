# Terminal-outcome fixtures

## `run-outcomes.json`

Real pipeline run outcomes, captured and redacted from a developer machine's
local pipeline history.

- **Source**: `.nightgauge/pipeline/history/index.json` — written by the Go
  binary on every terminal run. The capture read an index holding **483** real
  records.
- **Capture + redaction**: `scripts/capture-terminal-run-outcomes.mjs`
  (re-runnable: `node scripts/capture-terminal-run-outcomes.mjs`). It selects
  the most recent record for each of the two outcomes the abort-deadline tests
  need (`complete`, `cancelled`) and keeps **only** numeric/structural fields:
  outcome, cost, the four token totals, duration, stage count. Every free-text
  or identity field — title, branch, labels, `run_id`, `issue_number`,
  timestamps — is dropped, so nothing repo-private can reach this public
  fixture.
- **Read by**:
  `packages/nightgauge-vscode/tests/services/ConcurrentPipelineManager.abortTimeout.test.ts`
  (#307).

### Why a fixture rather than literals

The `abortAll` deadline tests settle a force-cleared slot's `runPipeline`
promise and assert on how the outcome is booked. The duration/cost/token shape
those assertions ride on is the shape a real settlement carries; inventing it
(`totalDurationMs: 1_000`, `cost: 0`) means the test keeps passing while the
real shape drifts, which is the #166 failure mode. These numbers come from runs
that actually happened.
