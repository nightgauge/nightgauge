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
  `pipeline.notifyStageTransition` sequence for a six-stage run with one
  failure-then-retry, awaiting each response before sending the next, and
  writes the final snapshot here. It aborts rather than writing if the captured
  snapshot does not actually show the recovery (recovered stage still in
  `stageErrors`, or the transitions raced).
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

Note what the capture shows and could not have been guessed: `feature-validate`
appears **twice** in `completedStages` (`exitCode: 1` then `exitCode: 0` — both
attempts' spend is booked), and its failed attempt still leaves a
`terminatingStageTokens` entry behind. The contract cleared is `stageErrors`,
and only `stageErrors`.
