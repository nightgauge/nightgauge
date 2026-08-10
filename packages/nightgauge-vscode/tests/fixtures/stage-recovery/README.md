# Stage-recovery fixtures (#407)

## `recovered-stage-snapshot.json`

The `pipeline.stateChanged` event the **real Go binary** emits at the end of a
run in which `feature-validate` failed and then succeeded on the retry.

- **Source**: `nightgauge serve`, driven over stdin/stdout exactly as the
  extension's `IpcClient` drives it. The file is the last
  `pipeline.stateChanged` envelope the server wrote to the wire, verbatim —
  keys, nesting, number formats, and all.
- **Regenerate**: `scripts/capture-stage-recovery-fixture.sh` (no arguments;
  writes back to this path). The script builds `cmd/nightgauge`, starts `serve`
  in a throwaway `$TMPDIR` workspace with **no** project config — so no
  scheduler attaches and no network call is made — sends the
  `pipeline.notifyStageTransition` sequence for a five-stage run with one
  failure-then-retry, awaiting each response before sending the next, and
  writes the final snapshot here. It aborts rather than writing if the captured
  snapshot does not actually show the recovery (recovered stage still in
  `stageErrors`, no `terminatingStageTokens` proof that the failure landed,
  the wrong completion shape, or raced transitions).
- **The input messages are production-shaped, field for field.** Each
  transition carries exactly what the corresponding `PipelineStateService`
  emitter sends — in particular the `failed` transition carries **no**
  `inputTokens` / `outputTokens` / `cacheReadTokens` / `costUsd`, because
  `failStage` carries none. That is not a detail: the Go server's `failed`
  branch books a completion only when there is spend to book, so with the real
  shape the failing attempt appears in **no** `completedStages` entry at all.
- **Read by**:
  `packages/nightgauge-vscode/tests/services/stageRecovery.appliers.test.ts`.

### Content is synthetic — nothing to redact

The repo slug (`acme/widgets`), issue number, error text, models, and every
token/cost number are invented by the capture script and typed into an empty
temporary workspace. No real workspace, pipeline history, project board, or
GitHub account is read, so unlike the other captured fixtures in this tree this
one needs no redaction pass and none was applied.

Timestamps and durations are captured verbatim, so regenerating produces a
different-but-equivalent file. Nothing asserts on them; the fixture exists for
its **shape**.

### Why a capture rather than literals

The assertion is "the snapshot Go emits for a recovered stage makes the TS
appliers render it complete, `countFailedStages` return 0, and `outcomeDisplay`
say plainly Complete". A hand-written `stageErrors: {}` would be the test
author asserting their own belief about Go's output — the #166 failure mode,
and precisely the belief that was wrong before #407: Go used to emit
`stageErrors: {"feature-validate": "..."}` alongside two `completedStages`
entries for that same stage, and every applier faithfully rendered it failed.
The only honest evidence is a snapshot the binary actually produced.

Note what the capture shows and could not have been guessed: on the extension's
real wire shape the failed attempt books **nothing** in `completedStages` —
`feature-validate` appears there exactly once, `exitCode: 0`, from the retry —
while the failure still leaves a `terminatingStageTokens` entry behind (the one
the `failed` branch writes unconditionally, and the only surviving trace of the
attempt in this snapshot). The contract cleared is `stageErrors`, and only
`stageErrors`.

That entry's token and cost numbers are all **zero**, and that is the honest
picture: the failing attempt's spend never reaches `TotalCostUSD` on this path
because the extension sends none. It is a separate, pre-existing gap — `#407`
neither caused nor fixes it — and the fixture shows it rather than papering
over it with an invented failed-with-cost message.
