# Undetermined-branch fixtures (#397)

History records that the **real Go binary** emits for a run whose feature branch
could not be determined from any source. #397 removed a `feat/{N}` fabrication
from two writers, so there is one capture per writer.

| File                         | Written by                                                         |
| ---------------------------- | ------------------------------------------------------------------ |
| `completed-run-record.jsonl` | `state.HistoryWriter.BuildV2Record` (the primary site), via IPC    |
| `completed-run-index.json`   | the `index.json` the same write produced                           |
| `crash-record.jsonl`         | `orchestrator.SynthesizeOrchestratorCrashRecord` (the second site) |

- **Captured**: 2026-08-10, from the `fix/397-no-branch-fabrication` worktree at
  `main` @ `5a365fb1` plus this issue's Go changes (the removal of the
  `feat/{N}` fabrication in `internal/state/history.go` and
  `internal/orchestrator/failure_handler.go`).
- **Regenerate**: `scripts/capture-undetermined-branch-fixture.sh` (no
  arguments; writes back to this directory).
- **Read by**:
  `packages/nightgauge-vscode/tests/views/dashboard/undeterminedBranch.record.test.ts`.

## `completed-run-record.jsonl` / `completed-run-index.json`

`nightgauge serve` driven over stdin/stdout exactly as the extension's
`IpcClient` drives it: the `pipeline.notifyStageTransition` sequence for a
two-stage run, then `pipeline.notifyComplete`, whose handler builds the
authoritative record through `BuildV2Record`. The JSONL line is **verbatim**,
and so is the index: Go already writes `index.json` 2-space indented, so the
capture's decode/re-encode round trip reproduces its bytes and adds only the
trailing newline Go omits.

The run's branch is never named. The `initialized` transition carries an **empty
`branch`**, which is production-shaped — `SeedRunContext` seeds the runtime's
branch only when the field is non-empty, so a caller with no branch to give
leaves it unset — and that is precisely the input pre-#397 `BuildV2Record` turned
into `feat/{N}`.

One request at a time, response awaited before the next is sent: the IPC server
dispatches each request on its own goroutine, so a fire-and-forget driver races
its own transitions. `IpcClient` awaits every response; so does the script.

## `crash-record.jsonl`

`nightgauge queue list`, run in a throwaway workspace holding one artifact: a
`.nightgauge/pipeline/current-run.json` sidecar naming a **dead** pid. That is
the on-disk state a killed orchestrator leaves behind, and constructing a
`Scheduler` over it runs the real startup recovery (`loadQueue` →
`recoverOrchestratorCrash` → `SynthesizeOrchestratorCrashRecord` →
`state.HistoryWriter.WriteRecord`). The line is verbatim.

## Why the verbatim lines, not JSON objects

The whole contract is **key present, value empty**. A decoder cannot represent
that: `JSON.parse` gives the same `""` for a key written empty and a key that was
never there, and re-serializing could silently drop it. So the files are the byte
sequences the writer emitted, and the tests assert on those bytes before parsing
them.

## Why captures rather than literals

The assertion these files carry is "the record Go writes for an undetermined
branch survives the strict TS schema and is imported rather than dropped".

That is one contract. The Runs tab's `(branch not determined)` label is a
**second, separate** one: that cell renders a platform-served row
(`platform.getAnalyticsRuns`), whose branch comes from the `stage_started`
event's runtime value, not from this record — the local record's branch has no
rendering surface today. The two are asserted separately in the test file and
are not a single causal chain.

Hand-writing `{"branch": ""}` would be the test author
asserting their own belief about the writer — the #166 failure mode, and
precisely the belief that was wrong before #397: the writers emitted
`"branch":"feat/{N}"` for every run they could not resolve, and no reader,
dashboard, upload or human could tell that apart from a branch the run really
used. The only honest evidence is a record the binary actually produced.

Note what the captures show and could not have been guessed:

- The crash synthesizer knows the issue number, title, stage and timings, and
  still writes an empty branch — the sidecar it reconstructs from never carried
  one. Its issue number is deliberately **positive**: that is exactly the case
  the pre-#397 synthesizer turned into `feat/397`.
- The completed run carries `stages.<s>.model_selection.source: "scheduler"`,
  a value `schemas/executionHistory.ts` did not list in its `source` enum when
  this capture was taken. So every record with a `model_selection` failed
  `safeParse` and landed in `executionHistoryReader`'s lenient fallback —
  nothing in the tree revealed that until a real record was read by the real
  schema. #446 fixed it by making the SDK's `MODEL_SELECTION_SOURCES` the one
  vocabulary authority (Go mirrors it under a cross-language pin), and the
  capture now parses strictly, model_selection included. The test that used to
  delete the field to reach the schema asserts the whole record instead.

## Content is synthetic — nothing to redact

The repo slug (`acme/widgets`), issue numbers, titles, run ids, pid and every
token/cost number are invented by the capture script and typed into empty
temporary workspaces. Nothing needed redaction and no redaction pass was
applied.

The capture isolates itself three ways, because one redirect is not enough:

| Redirect                                | What it covers                                                                                                                                  |
| --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `NIGHTGAUGE_CONFIG_HOME` → temp tree    | the machine-tier config the binary reads                                                                                                        |
| `HOME` → temp tree                      | everything else `$HOME`-derived — notably `nightgauge serve`'s claim file under `~/.nightgauge/serve`, which the config variable does not reach |
| `GITHUB_TOKEN`/`GH_TOKEN` → placeholder | the forge client both paths construct; set unconditionally so an ambient token is never inherited and `gh auth token` is never consulted        |

Neither workspace has a project config, so no real workspace, pipeline history,
config home or project board is read, and **no network call is made** — the
committed files were regenerated with `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY`
pointed at a closed port, and with the operator's `~/.nightgauge/serve` unchanged
before and after.

Timestamps and durations are stamped by the binary from the wall clock, so
regenerating produces different-but-equivalent files. Nothing asserts on them;
the fixtures exist for their **shape**.
