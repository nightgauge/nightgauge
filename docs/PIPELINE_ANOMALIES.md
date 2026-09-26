# Pipeline Anomalies

An anomaly is a record the scheduler writes onto a stage's run record when the
stage succeeded in a way it should not have needed to. Anomalies never fail a
run; they make a regression measurable. They are defined in
`internal/orchestrator/gates/anomaly.go`.

## `atomic_llm_overrun`

Fires when all of these hold for one stage attempt:

- the stage is **atomic-eligible**: it has a deterministic Go runner that is
  its canonical execution path;
- the stage's recorded `execution_path` is `llm`, meaning the deterministic
  runner punted and the skill ran instead;
- the stage's post-condition gate passed;
- the stage cost more than the anomaly floor (`DefaultAnomalyFloorUSD`,
  $0.01, unless configured).

The record carries the stage, the execution path, the stage cost and the
predicate below, which names the deterministic path that should have matched.
The punt reason itself is recorded separately on the stage
(`punt_reason`) and in the `stage_punt` telemetry event.

### Atomic-eligible stages

| Stage          | Predicate                                                                  | Runner                                                                                |
| -------------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| `issue-pickup` | deterministic issue fetch, branch-create and issue context write available | `internal/orchestrator/issue_pickup_runner.go` (#1904)                                |
| `pr-create`    | deterministic gh pr create available                                       | `internal/orchestrator/stages/prcreate.go` ([PR_CREATE_STAGE.md](PR_CREATE_STAGE.md)) |
| `pr-merge`     | deterministic gh pr merge available                                        | `internal/orchestrator/stages/prmerge.go` ([PR_MERGE_STAGE.md](PR_MERGE_STAGE.md))    |

To add a stage, add an entry to `atomicEligibleStages` in `anomaly.go` and a
row to this table.

### Deterministic issue-pickup

The issue-pickup runner reads the issue, derives the branch name with the same
function `nightgauge git branch-create --issue` uses, creates the branch through
the same code, pushes it (best-effort; pr-create pushes again), and writes
`.nightgauge/pipeline/issue-{N}.json` with a temp-file-plus-rename write, so the
file is never observable empty or partial and `branch` is always a string. The
routing object comes from the same deterministic routing Decision the scheduler
applies to the run's stage list (`nightgauge issue route` computes the same
Decision). It runs for every adapter. The issue-pickup skill runs only when the
runner punts, and that run is what this anomaly flags.
