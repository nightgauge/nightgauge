# Demo Mode Architecture — a scripted daemon in the binary seam drives the real extension

**Date:** 2026-09-28
**Author:** nightgauge
**Status:** Decided
**Issue:** #2104 (epic #2102)
**Implemented by:** #2105 (daemon), #2106 (scenario and player), #2107 (fixture workspace),
#2108 (demo badge and UI-step hook), #2109 (drift guard), #2110 (one command)
**Builds on:** #2103 (`packages/nightgauge-vscode/demo/ipc-stub.cjs` and
`demo/ipc-inventory.json`)

---

## Executive Summary

Demo mode replays a fictional workspace through the **real** VS Code extension so recordings
and screenshots can be regenerated with one command. The extension reaches its backend only
through `<binary> serve --workspace <root>` speaking line-delimited JSON
(`src/services/IpcClientBase.ts`), and the binary path is already overridable
(`nightgauge.backend.binaryPath`, `NIGHTGAUGE_GO_BINARY_PATH`). A demo daemon in that slot
drives every view with no change to the extension's data path.

Decisions, each with the alternative rejected:

| #   | Topic                 | Decision                                                                 | Rejected                                                    |
| --- | --------------------- | ------------------------------------------------------------------------ | ----------------------------------------------------------- |
| 1   | Where the fake lives  | A demo daemon in the `binaryPath` seam                                   | Extension-side mock; real daemon against a fake GitHub      |
| 2   | Language and location | Node, grown from the #2103 stub under `packages/nightgauge-vscode/demo/` | Go under `cmd/`                                             |
| 3   | Scenario format       | JSON: seed state plus a timeline of steps on a scenario clock            | Recorded real-run IPC logs replayed verbatim                |
| 4   | Announcing demo mode  | `ipc.ready` carries `demo: true`; the extension shows a persistent badge | A VS Code setting or environment flag read by the extension |
| 5   | Driving views         | A `demo.uiStep` event naming a command from a fixed allowlist            | Arbitrary `executeCommand` from the scenario                |
| 6   | Platform surfaces     | Served from scenario state over IPC; no platform network in demo mode    | Pointing the extension at a fake platform HTTP server       |
| 7   | Protocol drift        | CI fails when the extension calls a method the daemon does not answer    | Periodic manual re-inventory                                |

## 1. The daemon sits in the binary seam

**Decision.** The demo is a separate process the extension spawns exactly as it spawns
`nightgauge serve`. It answers requests from in-memory scenario state and emits the events the
UI subscribes to (`stage.*`, `phase.*`, `pipeline.*`, `queue.changed`, `attention.event`,
`autonomous.*`). It never emits or honours `pipeline.runStage` or `pipeline.abort`, never
spawns a process, opens a network connection, or reads a token, credential or keychain entry.
Only the scenario path, a speed factor and the #2103 log path are read from its arguments and
environment.

**Rejected: an extension-side mock.** It would need a code path inside the shipped extension
that bypasses `IpcClient`, so the demo would no longer exercise the real data path and the
mock would ship to every user. **Rejected: the real daemon against a fake GitHub.** It would
start real stages and agents, needs a GitHub API double wide enough for the board, PR and
checks calls, and is not deterministic in its timing.

## 2. Node beside the extension

**Decision.** The daemon is CommonJS Node under `packages/nightgauge-vscode/demo/`, grown from
`ipc-stub.cjs`. Result shapes are checked against the extension's own types
(`src/services/IpcClient.generated.ts` and `tests/mocks/*`) by type-guard tests, and the
existing `tests/demo/ipc-stub.test.ts` module-graph test (no `net`, `http`, `https`,
`child_process`) extends to the whole daemon. The daemon ships in no package: `demo/` stays in
`.vscodeignore`.

**Rejected: Go under `cmd/`.** It would duplicate every result type in a second language with
no compiler link to the TypeScript caller, which is exactly the drift the demo has to avoid,
and would couple a marketing tool to the release binary's build.

## 3. Scenario format

**Decision.** A scenario is one JSON file with:

- `seed`: an integer, the only source of variation;
- `state`: the initial repositories, board items by status, queue, attention items, run
  history, knowledge entries and platform run/cost/trend rows;
- `steps`: an ordered list, each `{ "at": <ms from scenario start>, "kind": ..., ... }`, where
  `kind` is either a state mutation (`board.move`, `queue.set`, `attention.raise`,
  `attention.resolve`, `history.append`) or an event emission (`event` with a method name and
  payload), or `ui` (decision 5).

Time is a scenario clock that starts at zero; the player schedules `at / speed` on a monotonic
timer and stamps every emitted timestamp from the scenario clock, never the wall clock. Steps
with equal `at` run in file order. `--speed` scales delays only, never order. A scenario is
validated in full at load; the first error names the step index and the reason, and the
daemon exits non-zero before emitting `ipc.ready`. All names, numbers and titles in shipped
scenarios are fictional.

**Rejected: replaying recorded real-run IPC logs.** They carry real repository names, issue
titles and timings, are hours long, and cannot be edited to show a specific story.

## 4. `ipc.ready` announces demo mode

**Decision.** The daemon's `ipc.ready` payload is `{ "protocolVersion": <current>, "demo": true }`.
The protocol version must equal `IPC_PROTOCOL_VERSION`, or the extension disconnects as it does
for any mismatched binary. On `demo: true` the extension sets a `nightgauge.demoMode` context
key and shows a status bar badge reading "Demo" for the life of the connection (#2108). The
badge cannot be hidden by a setting: a recording must never pass for a real run.

**Rejected: a setting or environment variable the extension reads.** It can disagree with what
is actually connected; the handshake cannot.

## 5. The UI-step hook

**Decision.** A `ui` step makes the daemon emit `demo.uiStep` with `{ command, args }`. The
extension handles it only when `nightgauge.demoMode` is set and only for commands in a fixed
allowlist in the extension source (opening the dashboard, switching a dashboard tab, revealing
or focusing a Nightgauge view). Anything else is logged and dropped.

**Rejected: arbitrary `executeCommand` from the scenario.** A binary in the `binaryPath` slot
would then be able to run any VS Code command, including terminal and file commands.

## 6. Platform-backed tabs, SSE and heartbeat

**Decision.** The Runs, Cost and Trends tabs read the platform through IPC methods; the demo
daemon answers those from scenario state. The platform SSE stream and agent heartbeat are HTTP
from the extension, gated on `platform.enabled`; the demo fixture workspace (#2107) sets
`platform.enabled: false`, and in demo mode the extension does not start them regardless. A
`vscode-host` test runs the demo with outbound HTTP stubbed to throw.

**Rejected: a fake platform HTTP server.** It adds a second fake with its own auth flow for
three tabs whose data already arrives over IPC.

## 7. Protocol drift

**Decision.** `demo/ipc-inventory.json` is the contract. The #2109 CI job runs the
`vscode-host` inventory suite against the daemon and fails when the extension calls a method
the inventory does not list or the daemon answers with the protocol's empty result, and when
`IPC_PROTOCOL_VERSION` changes without the daemon's constant. Unknown methods at run time are
answered empty and logged, never fatal, so a demo still plays while CI reports the gap.

**Rejected: periodic manual re-inventory.** The demo would silently show empty views after any
IPC change until someone noticed.

## Consequences

- The demo exercises the shipped extension unchanged except for the badge and the allowlisted
  UI-step handler (#2108), both inert unless `ipc.ready` says `demo: true`.
- Every new IPC method the UI calls needs a demo answer or CI fails; that cost is the point.
- The demo cannot show agent output, terminal content or real PRs; those stay out of scope.
