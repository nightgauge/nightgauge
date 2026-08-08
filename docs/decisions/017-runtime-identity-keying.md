# Runtime Identity Keying — the pipeline runtime registry keys on a run, not on an issue number

**Date:** 2026-08-08
**Author:** nightgauge
**Status:** Decided
**Issue:** #370
**Builds on:** #44 (orphan reconciliation), #215 (per-repo state roots),
#228 (SDK trace recorder run id), #304 (outcome recording), #305
(`attention.raise` corroboration), #307 (force-clear terminal bookkeeping),
#313/#316 (history record identity and the single-authoritative-writer rule),
#3557 (`runId` threaded from runstate into `IpcRunStageParams`), #375
(run-scoped raise verb), ADR 013 "Run Lifecycle Trace Schema" (per-run trace
keyed by `run_id` — indexed in [README.md](README.md)), and
[ADR 015 §N](015-decision-requests.md), which defers **socket identity** to
this ADR. §N is amended in the same PR: this ADR gives the socket a run
identity (closing the half of §N's residual where a caller addresses a run it
did not start) and explicitly does **not** authenticate it (leaving the half
where a caller mints and drives a run of its own).

---

## Executive Summary

`internal/ipc/server.go` holds the runtime registry for every pipeline the
VSCode extension drives:

```go
// activeRuntimes holds RuntimeState for HeadlessOrchestrator-initiated pipelines.
// Keyed by "repo#issueNumber". Protected by runtimesMu.
activeRuntimes map[string]*state.RuntimeState
```

**The comment is false.** All seven key-construction sites build
`fmt.Sprintf("%d", p.IssueNumber)`. There is no repo, and there is no run — the
key is an issue number, which is not an identity. An issue number is reused by
every dispatch of that issue, by every repo in a multi-repo workspace that
happens to number an issue the same, and by the zombie of a force-cleared run
that unwedges an hour later.

Everything downstream inherits that. A dead run adopts a live run's
`RuntimeState` and books its costs, stage errors, phases and terminating-stage
tokens; writes the authoritative history record and the calibration-corpus row
under the live run's `RunID`; then deletes the live run's registry entry and
`os.Remove`s its crash-recovery snapshot — all after seconds of unlocked I/O,
with no identity check at the write site. Orphan reconciliation is dead at the
root-switch call site for exactly the runs that need it, because a force-cleared
run's entry pins its own reconciliation shut for the life of the server.

We re-key the registry on a **run identity** that is:

1. **minted by the dispatcher**, before any I/O can fail, and never by the
   server, and never taken from the network;
2. **carried on every `pipeline.*` call**, not latched on one at-most-once
   message;
3. **adopted on sight** by the run-progress verbs when the server has never
   seen it and it is not provably closed — so a restarted server, a lost
   `initialized`, or a slow socket never locks a live run out of its own
   records;
4. **resolved but never invented** by the administrative verbs (`setPaused`,
   `abandonRun`), which may not manufacture a target;
5. **refused** only for a run that is provably terminal, where "terminal" is a
   **durable** fact and not an in-memory one;
6. **compared at every destructive write, under the lock that resolved it**,
   with the snapshot filename carrying the identity so the path _is_ the check.

The issue number is demoted to a **derived index** for lookup and UX. Two
dispatches of one issue coexist under distinct keys and corrupt nothing; the
index names one of them "current" for issue-addressed reads, ranked by the
server's own last-seen stamp so the ranking re-settles on the next message from
either run.

The #307 per-dispatch generation token and this run identity are **the same
concept minted in the same instant**, so they become one value rather than two
that must be kept in sync.

**What this ADR does not do:** it does not close a run because a dispatch died.
`pipeline.abandonRun` terminates a **dispatch** — it emits the run's terminal
platform event and frees the local bookkeeping — while leaving the run's
identity open, so a wedged process that unwedges an hour later still books its
own honest record under its own id. That distinction is the correction design
review forced, and it is what keeps the "a live run is never rejected"
invariant true (see [Revision history](#revision-history--what-design-review-changed)).

---

## Context

### The registry as it stands

| Aspect           | On `main` today                                                                                                                                                                                |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Key              | `fmt.Sprintf("%d", issueNumber)` at `server.go:2354, 2432, 2463, 2634, 2663, 2986` and `attention_raise.go:256`                                                                                |
| Created by       | `notifyStageTransition` mint-on-miss (`server.go:2466-2476`, mints a fresh `uuid.NewString()` on **any** status), and `setPaused` mint-on-miss (`server.go:2358`, with no repo and no `RunID`) |
| Mutated by       | `notifyStageTransition`, `setPaused`, `notifyComplete`, `notifyPhaseTransition`                                                                                                                |
| Read by          | `getState`, `notifyStageProgress`, `recordedRunSpendUSD` (#305), `reconcileOrphanedRuns`' `skipIssue` (#44)                                                                                    |
| Destroyed by     | `notifyComplete` only (`server.go:2957-2966`) — and process exit                                                                                                                               |
| Per-run artifact | `{repoRoot}/.nightgauge/pipeline/runtime-{issue}.json`                                                                                                                                         |

Two facts make this worse than a naming bug. First, the same on-disk namespace
is written by **five writers in three processes**: the IPC server (two sites),
the Go scheduler (three sites), and `nightgauge gate verify --record` running as
a separate CLI process, all through `RuntimeState.Persist`, which marshals a
**whole snapshot** — last write wins, no merge. Second, an unrelated second
registry (`internal/orchestrator/scheduler.go:900-928`, `map[int]`) persists
into that same namespace, so any filename change is a two-registry change.

### Why an issue number stopped being adequate

It never was, but three shipped features made the gap load-bearing:

- **#307** established that an issue number is not a run identity on the
  extension side, minted a per-dispatch generation, and proved with probes that
  a force-cleared dispatch settles late and books terminal outcomes twice. It
  fixed the extension and stated the Go-side exposure as
  [a KNOWN EXPOSURE paragraph](../GO_BINARY.md#force-clear-terminal-bookkeeping-issue-307)
  deferred here.
- **#305** had to re-check `rt.Repo == repo` at one reader
  (`attention_raise.go:282`) precisely because the key carries no repo. A
  compensating check at a single reader is the signature of a wrong key.
- **#313/#316** made history record identity `"run:" + RunID`. When a zombie
  steals a live run's `RunID`, both runs produce the same ledger key and one
  record is silently dropped or overwritten. `repair-history` cannot recover it,
  because after the fact the two runs are indistinguishable.

### The two designs this ADR replaces

Two earlier designs were built and refuted with executable probes. Their
refutations are the binding constraints here, not background:

- **Latch-on-`initialized`.** `TestProbe_SuccessorWithoutInitializedIsLockedOutForever`
  showed a live, successful run writing zero run records, zero learning
  outcomes and zero telemetry with a frozen UI, because one message was lost.
- **Side-store supersede (`abandonedRuntimes`).** The parked copy was consulted
  only on a `notifyComplete` miss while `notifyStageTransition` still minted on
  miss, so a parked run that emitted one more transition silently acquired a new
  identity and resolved its completion through the live map anyway.

Both are analysed in [Alternatives Considered](#alternatives-considered).

---

## Failure catalogue

The design-phase enumeration, inlined here because this is the permanent record
and every coverage claim below cites it. F1–F20 are the defects that motivated
#370; F21–F25 were found by design review of this ADR's first draft and are
closed by the decisions that review produced.

| ID  | Failure                                                                                                                                                                                                                            | Where it lives today                                                      |
| --- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| F1  | **Identity laundering.** A producer that supplies no identity acquires a runtime it did not start, because the server mints one on miss under a key the producer does not own.                                                     | `server.go:2466-2476`                                                     |
| F2  | **Cross-run cost/token booking.** A zombie's transitions accumulate onto a live successor's `RuntimeState`.                                                                                                                        | issue-keyed `activeRuntimes`                                              |
| F3  | **Snapshot destruction by a zombie.** The `failed`-transition `os.Remove` deletes a live successor's crash snapshot.                                                                                                               | `server.go:2592`                                                          |
| F4  | **Wrong authoritative record.** A zombie's `notifyComplete` writes the V2 record and the calibration row under the successor's `RunID`.                                                                                            | `server.go` notifyComplete                                                |
| F5  | **Registry eviction of a successor.** The issue-keyed delete after seconds of unlocked I/O removes an entry installed during that window.                                                                                          | `server.go:2670`, `:2957-2966`                                            |
| F6  | **History-record collision.** Two runs of one issue sharing a `RunID` produce one ledger key; one record is dropped or overwritten, and `repair-history` cannot recover it after the fact.                                         | #313/#316 `run:<id>` key                                                  |
| F7  | **Fallback-key collision.** The `issue:{N}\|{whole UTC second}` fallback makes two dispatches started in the same second one run to the ledger.                                                                                    | history fallback key                                                      |
| F8  | **Cross-repo collision.** Repo A #42 and repo B #42 share a registry key, a snapshot filename and a record key, with no force-clear involved.                                                                                      | issue-keyed everything                                                    |
| F9  | **`setPaused` stub.** Mints a runtime with no repo and no `RunID`; can pause a **live successor**; and because `collectOrphanedRuns` skips paused snapshots, pins the issue against #44 forever.                                   | `server.go:2358`                                                          |
| F10 | **Phase/progress cross-run writes.** `notifyPhaseTransition` and `notifyStageProgress` resolve by issue, so a zombie's phases land in a live run's `PhaseHistory` and authoritative record.                                        | `server.go:2634, 2663, 2986`                                              |
| F11 | **Reconcile dead at the root switch.** A force-cleared run's own entry makes `skipIssue` true for the life of the server, so `workspace.setRoot` never reconciles exactly the runs that need it.                                   | `pipeline_orphan_reconcile.go:138-144`                                    |
| F12 | **Confidently-wrong issue-addressed reads.** `getState` serves a dead run's snapshot for that issue indefinitely.                                                                                                                  | `getState` resolution                                                     |
| F13 | **Shared-namespace last-write-wins.** Five writers in three processes marshal whole snapshots into one filename with no merge.                                                                                                     | `RuntimeState.Persist`                                                    |
| F14 | **The force-clear card has no run id.** `runTraceRef` resolves to nothing, so the card cannot reach the ADR-013 trace of the run it describes.                                                                                     | `attention_wiring.go` `BuildAbandonedDispatch`                            |
| F15 | **SDK trace recorder guesses the run id** from `runtime-{issue}.json`, which is wrong after any adoption or steal.                                                                                                                 | `traceRecorder.ts:288`                                                    |
| F16 | **Permanent lockout from one lost message** (the refuted latch design): a live, successful run records nothing at all.                                                                                                             | Alternative A                                                             |
| F17 | **Token-less-producer steal.** A guard gated on `token != ""` is bypassed by every producer that has no token, which then steals a live run.                                                                                       | refuted round-2 design                                                    |
| F18 | **Extension-side terminal double-book.** A force-cleared dispatch settles late and books its terminal outcome twice.                                                                                                               | #307 PROBE-X / PROBE-Y                                                    |
| F19 | **Zombie-driven `stateChanged` applied to a successor's slot UI.**                                                                                                                                                                 | `PipelineSlotsTracker`, `PipelineStateService`                            |
| F20 | **Two locks, one comparator.** `RunID` is written under `Server.runtimesMu` and read under `RuntimeState.mu` in `snapshotLocked`/`Persist` — a torn read a sequential `-race` test would not catch.                                | `server.go` / `runtime_state.go`                                          |
| F21 | **Live-run reconciliation across registries.** The reconcile skip consults only the IPC registry, so every `workspace.setRoot` emits a terminal `pipeline_done` for each live **Go-scheduler** run and deletes its crash snapshot. | `pipeline_orphan_reconcile.go:138-144` vs `scheduler.go:4043, 4460, 6271` |
| F22 | **Cross-process resurrection.** `AppendStageGateResultToDisk`'s create-on-miss recreates a snapshot the terminal claim just removed, from a process with no registry and no latch.                                                 | `runtime_state.go:784-791`                                                |
| F23 | **Shared-holder minting.** Three producers stamp an identity onto one singleton `PipelineStateService`, so the last minter relabels a live run's remaining traffic — F1 reproduced through the client.                             | `retryFailedIssue.ts:94-123`, `bootstrap/services.ts`                     |
| F24 | **Local-only inertness.** Reconciliation and retention are gated on `analyticsSvc != nil`, so a workspace with no platform account collects nothing and `.nightgauge/pipeline/` grows without bound.                               | `pipeline_orphan_reconcile.go` first line                                 |
| F25 | **CLI-run discovery by issue filename.** `CliPipelineReconciliationService` reads `runtime-{issue}.json`, and its `current-run.json` sidecar carries no run id — so a filename change makes it silently blind.                     | `CliPipelineReconciliationService.ts:137`                                 |

## Constraints

| ID  | Constraint                                                                                                                                                                                                                                                                     |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| C1  | #44's existing reconciler skips must be preserved: **paused** snapshots (they power #2008) and **identity-less** snapshots are never reconciled into a bogus terminal event.                                                                                                   |
| C2  | #304's outcome recording must stay **at most once per run**.                                                                                                                                                                                                                   |
| C3  | #305's corroboration rules must hold: exact repo match on both arms, and spend summed only over stages the daemon watched **begin**.                                                                                                                                           |
| C4  | #313/#316's history identity and **single authoritative writer** must hold: one serialized idempotent append path, first-write-wins, richer-upgrade-only, skeletons never overwrite.                                                                                           |
| C5  | #2008 pause-restore must keep working, including across an IPC-server restart.                                                                                                                                                                                                 |
| C6  | The snapshot must stay: (a) discoverable by directory scan with no index, (b) parseable by a process with no registry, (c) atomically written, (d) rooted in the run's **target** repo (#215/#307), and (e) never written for an unattributed runtime (the `repo != ""` gate). |
| C7  | A successor entry installed during `notifyComplete`'s unlocked window must survive that window.                                                                                                                                                                                |
| C8  | The IPC socket's trust model (ADR 015 §N) may not be **widened**: no new verb may give an unauthenticated caller a capability against a run it did not start that it does not already have.                                                                                    |
| C9  | **Fail-open.** A refused or lost IPC call must never kill a live run; the run continues on its local cache. The two new client-side run-id filters are the migration surface for this.                                                                                         |
| C10 | #307's guarantees: permanent tombstones, `await`-free check-and-claim boundaries, `stillOwnsIssue` reading `slots ∪ reservedSlots`, and the sha256-pinned terminal-parity fences.                                                                                              |
| C11 | The Go scheduler's `map[int]*RuntimeState` registry is **out of re-keying scope** for this ADR — but it is not out of **correctness** scope (see F21, and Decision 11).                                                                                                        |
| C12 | Third-process seams (`nightgauge gate verify --record`, the SDK `TraceRecorder`) must get the identity **threaded**, never guessed.                                                                                                                                            |

---

## Decisions

### 1. The run identity is a client-minted `run_id`, and it _is_ `RuntimeState.RunID`

There is **one** identity, not two bound together.

- **Format:** canonical lowercase **UUIDv7** — a time-ordered UUID. Uniqueness
  comes from the random tail; the leading millisecond timestamp makes ids sort
  by mint time in logs, ledger files and trace filenames. **No correctness rule
  decodes it.** UUIDv7 is chosen for readability and for a stable order over
  snapshots found on disk by a process with no registry; it is not a covert
  protocol. `runstate.NewRunID()` already produces exactly this value on the Go
  side (`scheduler.go:2773`), so both minters agree by construction.
- **Validated at the wire boundary, before the value is used for anything.**
  Every `runId` on every `pipeline.*` call is checked against
  `^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
  **before** it becomes a map key, a filename component, or a trace path.
  Failure is a JSON-RPC error `run_id_invalid`. This is not decoration: the
  identity is interpolated into `runtime-{issue}-{runId}.json` for `Persist`
  and `os.Remove` on a socket this ADR documents as unauthenticated (R-2), so a
  value containing `/` or `..` would be an arbitrary-path write. The validation
  rule and the discovery regex (Decision 8) are pinned to each other by a test.
  The SDK's broader `RUN_ID_PATTERN` (`/^[A-Za-z0-9_-]{8,128}$/`,
  `traceRecorder.ts:64`, mirroring `internal/trace/store.go`) stays as-is: it is
  the **trace store's** filesystem guard and is a strict superset of the wire
  rule, so a wire-valid id is always trace-valid.
- **Minted by the dispatcher, always client-side, and never taken from the
  network.** The server **never** mints a run identity.
  `notifyStageTransition`'s lazy `rt.RunID = uuid.NewString()` is deleted
  outright, and so is `setPaused`'s mint-on-miss. This single rule is what kills
  identity laundering (F1) and the token-less-producer steal (F17) together: a
  producer that must supply an identity it minted itself cannot address a run it
  did not start.
- **`state.NewRuntimeState` takes the run id as a constructor argument.**
  `RunID` becomes **immutable after construction**. There is no setter. An
  immutable field written once before the value is shared with any goroutine
  needs no lock, which dissolves the two-locks-one-comparator hazard (F20).
- **`Persist` refuses to write without an identity.** A `RuntimeState` with an
  empty `RunID` has no correct filename and no correct owner; writing
  `runtime-42-.json` would recreate the shared-namespace collision under a new
  name. This is the same call #307 made when it refused to persist a repo-less
  runtime.

**Why client-minted and not a server-minted `pipeline.openRun` response.** A
server-minted identity does not exist until a round trip completes, and the only
client is one whose IPC calls are individually wrapped in a swallowing
`try/catch` with a 30-second timeout on a socket whose wedging is the very
condition that produces this failure class. A lost response is a run with no
identity — the lockout of F16 in a new costume. Minting on the client makes the
identity exist unconditionally, before any I/O that can fail.

### 2. The #307 generation token becomes the run identity; `remoteRunId` stays a correlation attribute

`ConcurrentPipelineManager.mintDispatchGeneration` already mints a per-dispatch
token at exactly the right instant — synchronously inside `startSlot`, before
the reservation and before any `await` — and stamps it on the slot and the
reservation. That is the run identity, described in different words. It becomes
one value:

| #307 today                                                    | After this ADR                                                                   |
| ------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `generation: "${issue}:${counter}:${Date.now()}"`             | `runId: <uuidv7>`, minted at the same statement                                  |
| `forceClearedGenerations: Set<string>` (permanent tombstones) | `forceClearedRunIds: Set<string>` — same set, same permanence, no release path   |
| `stillOwnsIssue` compares `slot.generation`                   | compares `slot.runId` — same predicate, same slots-or-reservation ownership test |
| Three await-free check-and-claim terminal boundaries          | unchanged, byte for byte in structure                                            |
| Token never sent to Go                                        | Sent on every `pipeline.*` call, and on `pipeline.abandonRun`                    |

**Justification for unify over bridge.** A bridge means two ids and a mapping,
and a mapping is a thing that can be stale, lost, or disagreed about — which is
the class of defect this ADR exists to delete. More concretely: the force-clear
must be able to tell Go _which run_ it killed. With a bridge it would have to
look that up, and the lookup is exactly as stale as the wedge that caused the
force-clear. #307's own analysis observed that the generation "embeds a
monotonic counter that would make this decidable; nothing parses it" — making it
the Go-side key is what turns that latent evidence into a usable one.

**#307's guarantees are preserved, not weakened (C10).** The tombstone stays
permanent (a revocable tombstone expires exactly when the wedge is worst). The
check-and-claim pairs stay adjacent and `await`-free. `stillOwnsIssue` keeps
reading both `slots` and `reservedSlots`. The sha256-pinned fences in
`internal/orchestrator/testdata/terminal_behaviors.json` are re-pinned
deliberately, in the same commit that adds `abandonRun` to the force-clear
funnel, with the parity test's failure treated as the intended signal it is.

**`PipelineSlot.runId` is repurposed and its old meaning is renamed.** Today it
is "platform run ID from ack — used to route cancel commands to the right slot",
applied from `pendingRunIds` when the slot opens. That becomes `remoteRunId`,
which continues to do only cancel-command routing.

**The identity is ALWAYS minted locally. `RemoteRunID` never seeds it.** The
first draft said a dispatch originating from a platform `PendingCommand`
"seeds `runId` from `RemoteRunID` rather than minting". That is withdrawn, and
the reason is Decision 1's own safety argument: a producer that must supply an
identity **it minted itself** cannot address a run it did not start. A value
chosen by a remote server and delivered over an at-least-once command channel
(`executor.go:269` → `queue.add`'s `remoteRunId`, `protocol.go:411` →
`TriggerCommandHandler.ts:171`) satisfies neither half of that. Two concrete
failures it would have allowed:

- a **redelivered** `PendingCommand` produces two dispatches carrying the same
  `runId`; the second adopts the first's live entry, and both runs share one
  registry key, one snapshot file and one history-record key — the exact
  corruption this ADR exists to delete, now reachable with no force-clear;
- a remote command can name an id that collides with a **live local run**,
  handing an external caller a selector over a run it did not start — precisely
  the power Decision 9 refuses to give `attention.raise`.

So `remoteRunId` is carried as an **attribute** of the run: it rides on the
platform payload as a correlation field and continues to route cancel commands.
The platform materialises its row from the local `runId`, exactly as #1047 does
today with the Go-minted UUID, and joins to its own trigger through
`remote_run_id`. **The Go scheduler's preference for `RemoteRunID` over
`runstate.RunID` (`scheduler.go:2758-2762`) is deleted for the same reason**;
the scheduler keeps minting locally and carries the remote id alongside.

### 3. Every `pipeline.*` call carries the identity; there are two verb classes

| Method                           | `runId`      | Class          | Notes                                                                            |
| -------------------------------- | ------------ | -------------- | -------------------------------------------------------------------------------- |
| `pipeline.notifyStageTransition` | **required** | run-progress   | `initialized` loses all special status — it is one transition among many         |
| `pipeline.notifyPhaseTransition` | **required** | run-progress   |                                                                                  |
| `pipeline.notifyStageProgress`   | **required** | run-progress   |                                                                                  |
| `pipeline.notifyComplete`        | **required** | terminal claim | Decision 5                                                                       |
| `pipeline.setPaused`             | **required** | administrative | gains `repo` + `issueNumber`; **resolves, never creates** (Decision 7)           |
| `pipeline.abandonRun`            | **required** | administrative | new verb; gains `repo` + `issueNumber`; **resolves, never creates** (Decision 7) |
| `pipeline.getState`              | _optional_   | lookup         | not a run message; issue-addressed reads stay supported (Decision 6)             |

There is no separate handshake message and no state machine over message types.
**The identity on the current message is the whole handshake.** Losing any one
message costs exactly that message's content and nothing else — which is the
property the latch-on-`initialized` design failed to have.

The server's rule is five lines, and the **last** line is the one that differs
by class:

```
if runId == ""                      → error  run_id_required   (an old client, or a producer that has none)
if !matchesIdentityRegex(runId)     → error  run_id_invalid    (Decision 1 — checked BEFORE any use)
if closedRuns.has(runId)            → error  run_closed        (durable — Decision 4)
if resolve(runId) != nil            → serve  (Decision 11 picks WHICH registry)
run-progress   → ADOPT and serve                               (Decision 4)
administrative → resolve from disk, else error run_not_found; NEVER create
```

The two id errors are distinct on purpose: `run_id_required` is what a
version-skewed client produces and is the signal the Migration hard-fail exists
to make impossible, while `run_id_invalid` is what a malformed or hostile value
produces and is a security check, not a compatibility one.

**Why the blanket "unknown → adopt" rule is wrong for administrative verbs.**
Adoption is safe for a run-progress verb because the caller is the run,
re-creating **its own** run under **its own** key, where every write it makes
lands on its own record. An administrative verb is a caller making an assertion
**about** a run — and a terminal or pause claim that invents its own target can
assert something about a run nobody has ever seen. Concretely: the force-clear's
second arm, `bookForceClearedReservation`, fires for a dispatch that wedged
inside `git worktree add` and never reached Go at all. Under a blanket adopt
rule, `abandonRun` on that id would create an empty runtime, emit a
`pipeline_done` with `IssueNumber: 0` and no stages, and — in the first draft —
write the id into `closedRuns`, so that when the dispatch unwound (the
documented case: a `.git/config` lock that clears) its very first message would
be refused forever. That is F16's lockout reopened through adoption. The
administrative class exists to make that unrepresentable.

**Rejection shape.** A rejection is a **JSON-RPC error** with a machine-readable
`code`, never a success response carrying a status field. Nothing in
`packages/` reads a non-error status field — a `{"status":"stale"}` object is
discarded today, which is how the earlier design's rejections became invisible.
Fail-open (C9) is preserved by construction: the run continues on its local
cache.

**The rejection's audience is the Go side, and the ADR does not pretend
otherwise.** The first draft claimed "an error rejects the promise, so every
existing `try/catch` at minimum logs it". That is **false**, and the correction
matters because it was load-bearing: `PipelineStateService.ts:717, 741, 787,
828` and `notifyPipelineComplete`'s catch at `:966` are **bare, non-logging**
`catch {}` blocks that fabricate local state and fire `_onStateChanged`, so the
UI shows a healthy run while the server refused everything. Two consequences,
both normative:

1. **No design property in this ADR may depend on the TypeScript side observing
   a rejection.** Every rejection is logged, counted and traced **in Go**
   (`log.Printf` per rejected run id plus an ADR-013 trace node), rate-limited
   to once per `(method, runId)` per minute on the high-frequency
   `notifyStageProgress` path. A call rejected for a missing or malformed
   identity has no id to key on, so its limiter key is
   `(method, issueNumber, peer)` — an id-less flood still has a key. Silence on
   a refusal is how #304's corpus stayed empty for the life of the product.
2. **Those five bare catches gain a `logger.warn`** carrying the stage, the run
   id and the JSON-RPC error code. This is required by this ADR and listed in
   Migration; it is an observability fix, not a mechanism.

**No rejection is actionable, and none needs to be.** There is deliberately no
re-handshake instruction and no `reHandshakeRequired` flag, because there is no
actor on the TypeScript side to act on one — inventing a recipient for a
rejection is how the permanent lockout was designed in the first place. The
invariant that makes this safe: **a live run can never be rejected**, and it
holds because `run_closed` is set by exactly one thing — a run's own
`notifyComplete` terminal claim — and `run_not_found` is reachable only for
administrative verbs, which never carry run content. The first draft broke this
invariant by having `abandonRun` write to `closedRuns`; Decision 7 is the
correction.

### 4. Adoption is the answer to "unknown identity" for run-progress verbs, and it rehydrates

When the server sees a `runId` it has no entry for, that is not in `closedRuns`,
and that arrives on a **run-progress** verb, it **creates the entry and serves
the call**. This is safe in a way that mint-on-miss never was, because the
identity came from the caller: an adopting zombie re-creates **its own** run
under **its own** key, where every write it makes lands on its own record and
touches no other run.

Adoption **rehydrates from disk when it can.** The snapshot path is fully
derivable from the call's own parameters (`repo` → repo root, plus `issue` and
`runId`), so adoption reads `runtime-{issue}-{runId}.json` and restores the run's
accumulated history rather than starting empty. This turns the ordinary case —
an IPC server restarted mid-run — from lossy into very nearly lossless.

**"Closed" is a durable fact, not an in-memory one, and rehydration honours
it.** The first draft leaned on a self-verifying property — "a terminal run has
no snapshot, so the file's presence is itself evidence the run was not closed" —
which was an **unenforced invariant**: `closedRuns` is in-memory, so the only
cross-restart evidence was the _absence_ of a file, deleted best-effort and
unverified. A failed `os.Remove`, a crash between the record write and the
removal, or a removal aimed at the wrong directory left a terminal run's
snapshot on disk; a restarted server then adopted it, **rehydrated its full
history**, and its next `notifyComplete` produced a record strictly richer by
one stage — which `appendAndIndex` accepts as an upgrade, replacing the correct
authoritative record's index entry with the zombie's outcome. The fix has three
parts:

1. **The terminal claim writes the fact before it removes the file.** Step 3.5
   of Decision 5 stamps `terminal: true`, `terminal_at`, and the booked outcome
   into the snapshot through the same `AtomicWriteFile`, and only then removes
   it. A snapshot that survives its own removal is self-describing.
2. **Adoption refuses a terminal snapshot.** Loading a snapshot with
   `terminal: true` does not rehydrate; it re-populates `closedRuns` with that
   id and the call is refused `run_closed`. This is what makes `closedRuns`
   durable across a restart for every case where the file survives.
3. **Removal derives its directory from the claimed snapshot's own `Repo`,**
   never from the call's `p.Repo`. A `notifyComplete` whose `repo` param
   disagrees with the run's persisted repo can no longer leave the real file
   behind while deleting nothing.

Adoption without a snapshot is still allowed, because a run whose transitions
have not yet carried a repo has no snapshot yet (the `repo != ""` persist gate,
C6e). Such a run adopts with empty history; its eventual record is a skeleton,
and #313's "skeletons never overwrite / richer-upgrade-only" rule already does
the right thing with it — dropped if a richer record for that `run:<id>` exists,
written if it is all we have. Because rehydration from a terminal snapshot is
now impossible, a post-restart zombie's record is **always** the poorer one, so
C4's rule resolves it correctly rather than accidentally.

### 5. Terminal is a latch — in-process for the registry, durable on disk for everyone else

`notifyComplete` today unlocks at `server.go:2670`, does seconds of unlocked
I/O — ground-truth reconciliation, classification file reads, `BuildV2Record`,
`WriteV2Record`, the learning corpus append, the platform push — and then
deletes `activeRuntimes[<issue>]` and `os.Remove`s `runtime-{issue}.json` with
no re-read and no identity check.

The replacement is a **claim**, not a longer critical section:

1. **Claim, under `runtimesMu`:** resolve by `runId`; absent → adopt; already
   `terminal` → `run_closed` error; otherwise set `entry.terminal = true` and
   take the `*RuntimeState` **snapshot inside the same critical section**.
   Release.
2. **Work, unlocked,** against the snapshot taken under the lock — never
   against the live pointer.
3. **Compare-and-delete, under `runtimesMu`:** delete `activeRuntimes[runId]`
   **only if the entry stored there is the same pointer that was claimed**, and
   record the id in `closedRuns`.
4. **Stamp the terminal marker** into `runtime-{issue}-{runId}.json` under the
   directory derived from the **claimed snapshot's** `Repo` (Decision 4).
5. **Remove that same path.** **The path is the identity**, so this cannot take
   a successor's file even in principle — the strongest available form of an
   identity-checked destructive write.

Once `terminal` is set, the entry refuses every further mutation _and every
further `Persist`_. **That latch is in-process, and this ADR says so rather
than over-claiming.** The IPC server's registry cannot latch a write made by a
different OS process, and one such writer exists: `nightgauge gate verify
--record` (`cmd/nightgauge/gate.go:155` → `state.AppendStageGateResultToDisk`,
`runtime_state.go:784-791`), which today does `LoadPersistedState` → **on any
error `NewRuntimeState("", issueNumber, "")`** → `Persist` straight to disk.
That create-on-miss would resurrect a snapshot the terminal claim had just
removed, from a process with no registry and no `closedRuns` — falsifying
Decision 4's evidence and producing a second, contradictory `pipeline_done` at
the next server start (F22). Three changes close it:

- **`AppendStageGateResultToDisk` becomes load-or-skip.** The create-on-miss
  fallback is **deleted**. With no snapshot for `(stateDir, runID)` the gate's
  **verdict still runs and is returned**; only the `--record` write is skipped,
  with a loud error. A skipped gate record is an annoyance; a gate record
  written into a resurrected or guessed file is corruption.
- **It refuses a snapshot marked `terminal`.** The durable marker from
  Decision 4 is exactly the cross-process latch the in-memory one cannot be.
- **It writes through `PersistExisting`, not `Persist`.** `PersistExisting`
  fails if the target file is absent, so the read-modify-write cannot re-create
  a file that was removed between the load and the write. The residual is a
  narrow rename race, named in R-1.

The unlocked window is no longer a hazard for the registry: the only thing it
can produce is another call for the same `runId` — refused by the latch — or
calls for other run ids, which are on different keys (C7). A failed compare in
step 3 is not expected to be reachable and is treated as an assertion: log
loudly, **keep** the record and the outcome (they were written under a valid
claim at the time), and do not delete.

**The `failed`-transition snapshot removal (`server.go:2592`) is deleted.** It
is a second, redundant terminal path — `notifyComplete` fires immediately after
with `Success=false`, as the adjacent comment already says — and it is what let
a zombie destroy a live run's crash snapshot (F3). Worse, it was wrong on its
own terms: if the host dies between the `failed` transition and
`notifyComplete`, the run genuinely never reached a terminal event and deserves
reconciliation, which the removal prevented. After this ADR a snapshot is
removed by exactly **two actors**: a terminal claim (`notifyComplete`), and the
reconciler — which emits the run's terminal event first **unless** the snapshot
already carries a `terminal` or `abandoned` marker, and which also applies the
14-day cap (7.3, 7.4). `abandonRun` is deliberately not one of them.

**The force-clear funnel must never call `notifyComplete`.** A dispatch the
abort deadline gave up on is abandoned, not completed; routing it through the
terminal claim is what would close a run that is still alive. It calls
`abandonRun`. This is a normative rule on the extension side and is inside the
`force-clear-funnel` fence.

### 6. The issue number becomes a derived index; concurrent dispatches coexist

**Concurrent dual dispatch of one issue is not _supported_; it is merely
non-corrupting.** The three existing guards remain the policy and are unchanged:
the extension's #188 duplicate-dispatch guard (`slots ∪ reservedSlots`), the
queue `processing` mark (#254), and `AutonomousScheduler.isRunning`. The
worktree namespace stays one path per issue number. Nothing in the Go layer
newly permits concurrency — it simply stops corrupting when the guards are
bypassed, which they demonstrably are: force-clear plus re-queue, a second
extension host, a dashboard trigger racing a local dispatch.

**There is no second map.** The issue index is a **derived scan** of
`activeRuntimes` under `runtimesMu`, keyed conceptually by `repo#issue`. The
registry is small by construction (entries are evicted at terminal and reaped by
the lease, Decision 7), so a scan is cheaper than a second map's synchronisation
invariant — and a derived index cannot drift from its source.

**"Current" = the non-abandoned, non-terminal entry for `repo#issue` with the
newest `LastSeen`** — the server-observed lease stamp that every accepted call
refreshes. Server-observed, so no caller clock is trusted.

The first draft ranked on `FirstSeen` and claimed the ranking was
"self-correcting: the next transition from either run re-ranks it". It is not:
`FirstSeen` is written once at entry creation, so nothing a later transition
does can move it. The failure that exposes this is ordinary — run A
(force-cleared, wedged) and run B (live successor) both exist; the IPC server
restarts; A unwedges and adopts at T+5s while B, mid-stage and silent, adopts at
T+90s; A now has the newer `FirstSeen` and is "current", so the tree, the
dashboard and every issue-addressed `getState` serve the **dead** run for the
remainder of B's run. That is F12's confidently-wrong answer reintroduced
through the index. Ranking on `LastSeen` makes the sentence literally true —
the next message from either run re-ranks it — and an entry marked `Abandoned`
(Decision 7) drops out of the ranking entirely.

Repo is **an attribute of the run and a component of the index key, not part of
the identity.** The identity is globally unique on its own, which is what
actually fixes the cross-repo same-issue-number collision (F8) that needs no
force-clear to occur. Putting repo in the index key means issue-addressed
lookups cannot cross repos either, and #305's exact-repo re-check at the reader
stays exactly as it is (Decision 9).

**UX disambiguation:**

- `pipeline.getState` keeps its three-tier resolution and gains a tier-0:
  `execMgr` (`owner/repo#N`, unchanged) → the index's current run for
  `repo#issue` → the persisted snapshots for that issue in that repo's dir,
  newest by `started_at`. The response carries the resolved `runId` and, when
  more than one run exists for the issue, the other run ids — so a caller can
  tell that it is looking at one of several.
- `pipeline.stateChanged`, `phase.start` and `phase.complete` gain `runId` in
  the envelope. `PipelineStateService` filters on `d.runId === this.runId`
  (Decision 10 gives it exactly one identity to compare against);
  `PipelineSlotsTracker` routes by `runId` → slot. These two filters are the
  migration surface named in C9, and they close F19.
- **An event carrying an EMPTY `runId` is not dropped.** Both event families
  reach these subscribers — the notify handlers (`server.go:2386, 2602, 3000,
3015`) and the Go scheduler / execMgr callbacks (`server.go:374`
  `sched.OnStateChanged`, `:394` `sched.OnPhaseDetected`) — and
  `PipelineSlotsTracker` subscribes to both
  (`PipelineSlotsTracker.ts:134, :143`). The scheduler emitters **do** gain the
  runstate `RunID` (they have it: `scheduler.go:2756-2778` stamps it on every
  run), so the ordinary case is a strict match. The empty-id fallback exists so
  that a strict-equality filter can never be the reason a dashboard slot goes
  dark: an event with no id falls back to the issue-number pre-filter and is
  counted in a log. Fail-open on a UX surface, per C9.
- The tree and dashboard show the **current** run for an issue. A second,
  older run is reachable only through the slot card that owns it — which is the
  honest presentation, because a second run of one issue is an anomaly the
  operator should see as one.

Issue-addressed reads may now return **nothing** where they previously returned
a dead run's snapshot indefinitely (F12). That is an accepted improvement: no
answer is better than a confidently wrong one.

### 7. Liveness, abandonment, and reconciliation (#44)

Presence in the registry has never been evidence of liveness — which is why the
`workspace.setRoot` reconcile call site is dead today for exactly the runs that
need it (F11). Four changes fix it.

#### 7.1 `pipeline.abandonRun {runId, repo, issueNumber, reason, stage?}` closes a DISPATCH, not a run

This is the correction design review forced, and it is the most consequential
change from the first draft. The first draft made `abandonRun` "the explicit
counterpart of `notifyComplete`: same terminal claim, same compare-and-delete,
same snapshot removal" — which **closes a run that is alive by definition**. The
premise of #307 is that the wedged adapter is still running: the promise never
settled because the child ignored the `AbortController`, and #266 exists
precisely because such a run can still merge its PR. Under the first draft the
sequence was: operator Stop → 30s `ABORT_ALL_TIMEOUT_MS` →
`forceClearStuckSlots` tombstones the run → `abandonRun` marks it terminal and
writes it into `closedRuns` → the adapter unwedges and emits `feature-validate
complete`, `pr-create`, `pr-merge`, `notifyComplete{prMerged:true}` → **every
one refused** → a run that merged a PR writes zero history records, zero
learning outcomes and zero stage telemetry, and its platform row is closed as an
abandoned failure. That is byte-for-byte the outcome of Alternative A, and a
**regression** against shipped #307, whose ledger records the current behaviour
as honest: "Go still holds the run's own runtime with its own token, so its
notifyComplete is ACCEPTED and it writes its record and learning outcome under
its OWN RunID."

So `abandonRun` terminates the **dispatch**:

| It does                                                                              | It does NOT                                                                              |
| ------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------- |
| resolve the entry by `runId` (never create — Decision 3's administrative class)      | set the terminal latch                                                                   |
| set `entry.Abandoned = true`, `entry.AbandonedAt = now`                              | add the id to `closedRuns`                                                               |
| take the snapshot under the lock and emit the run's terminal `pipeline_done` from it | delete the registry entry                                                                |
| stamp `abandoned: true` + `abandoned_at` durably into `runtime-{issue}-{runId}.json` | remove the snapshot                                                                      |
| drop the entry out of the issue index's "current" ranking (Decision 6)               | write a learning-corpus row (an abandoned dispatch measures nothing about model routing) |

The entry stays **adoptable and mutable**. A late honest completion from the
same process is accepted, performs the ordinary terminal claim, and books its
record and learning outcome **under its own identity** — the #307 behaviour,
preserved. The platform sees `pipeline_done(success=false)` at abandon time and
then the run's real terminal event; `pipeline_runs` is keyed by `run_id` and
last-writer-wins on outcome, so the row converges on the truth. Emitting early
and correcting later is the right trade: the ordinary case is that the wedge is
terminal and #44's whole point is not stranding a `running` row.

**Resolution when the registry has no entry** (the modal case — the force-clear
fires because something is wedged, and a restarted or replaced IPC binary leaves
an empty registry):

1. `runtime-{issue}-{runId}.json` exists under `pipelineStateDir(repo)` → load
   it, emit its `pipeline_done`, stamp `abandoned` into it. No registry entry is
   created.
2. No snapshot → **no-op success** (`{status: "no_run"}`), logged once. This is
   `bookForceClearedReservation`'s case: a dispatch that wedged inside
   `git worktree add` never reached Go, so the platform has no row to close and
   there is nothing to abandon. Critically, **nothing is written to
   `closedRuns`**, so when that dispatch unwinds (`startSlot` returning
   `abandoned`) its first message is served normally.

   This is the one place a `pipeline.*` handler returns a status field, and it
   does not contradict Decision 3's "a rejection is an error, never a status
   field": `no_run` is a **successful no-op**, not a refusal. The force-clear has
   nothing to do differently on receiving it, which is precisely why it is not
   an error — an error here would be a rejection with no actionable recipient,
   the shape this ADR refuses everywhere else.

`repo` and `issueNumber` are **required parameters**, and this is not
bookkeeping. Without them an adopted or disk-resolved target has neither, and
`buildPipelineDoneEvent` (`pipeline_telemetry.go:135`) only checks
`runID != ""` — it would happily emit a `pipeline_done` with `IssueNumber: 0`
and no stages for a run the platform never saw start; and `pipelineStateDir(repo)`
could not be resolved, so no file could be found or stamped. The server
additionally requires that the resolved run's own `Repo` and `IssueNumber`
**equal** the supplied ones; a mismatch is `run_not_found`. That is the
corroboration discipline #305 spent three rounds building, applied to the one
new verb (C8).

#### 7.2 A lease, fed by BOTH registries

Every accepted call stamps `entry.LastSeen`. The reconciler's skip predicate is
re-derived from _"this issue has an entry"_ to **"this run is live"**:

```
skipRun(runID) =
      ipcRegistry.has(runID)      && !terminal && !abandoned && LastSeen within 30m
   || scheduler.IsRunLive(runID)                                    // Decision 11
```

The liveness window is 30 minutes; `notifyStageProgress` refreshes it
continuously during a long stage, so it is generous by an order of magnitude.
The lease is a backstop for a lost `abandonRun`, not the primary mechanism — it
can only fire for a run that both lost its abandon call and went silent for half
an hour, which is indistinguishable from dead.

**The second arm is not optional, and it fixes a live defect rather than a
hypothetical one (F21).** `reconcileOrphanedRuns` builds its skip from
`s.activeRuntimes` alone. The Go scheduler persists into the same directories
(`scheduler.go:4043, 4460, 6271`, covered by `pipelineStateScanRoots`) and
always stamps a non-empty `RunID`, and `collectOrphanedRuns` skips only paused
and `RunID`-less snapshots. Scheduler runs are **never** in `activeRuntimes` —
`PipelineBridge.ts:265-267` says so verbatim. So every `workspace.setRoot`
(`server.go:757`, fired from `bootstrap/services.ts:2441` on
`onWorkspaceChanged`) emits a terminal `pipeline_done` for every **live**
scheduler run and `os.Remove`s its crash snapshot. Renaming `skipIssue` to
`skipRun` would have made that predicate look rigorous while leaving it blind to
half the product's runs. `Scheduler.IsRunLive(runID)` — a scan of its
`map[int]*state.RuntimeState` for a matching `RunID`, under
`activeRuntimesMu` — is the arm that closes it.

#### 7.3 `collectOrphanedRuns` keys on the run

It parses the identity out of the filename. Its existing skips are preserved
(C1) with one bounded change:

- **paused** snapshots are skipped by the emit-and-remove path (they power the
  #2008 pause-restore prompt) — but they are **not** exempt from the age cap
  (see Retention);
- **identity-less** snapshots are skipped, never reconciled, never deleted here;
- **terminal** snapshots (Decision 4's durable marker) are **removed without
  emitting** — their event was already emitted by the terminal claim;
- **abandoned** snapshots whose `abandoned_at` is older than the liveness window
  are **removed without emitting** — their event was already emitted by
  `abandonRun`;
- emit-then-remove idempotency across activations is unchanged.

Both call sites then work: `Server.Run` (fresh process, empty IPC registry, but
a possibly-populated scheduler registry — hence 7.2) and `workspace.setRoot`
(populated registry — now skips only runs that are genuinely live, in either
registry). That is the #370 acceptance criterion.

#### 7.4 Retention, and it must not depend on telemetry

Today there is none, which is why identity-less debris accumulates. The rules:

| Rule                                                                                                            | Applies to                                                           |
| --------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------- |
| A terminal claim removes its own snapshot                                                                       | every run that completes                                             |
| The reconciler removes each snapshot after emitting                                                             | orphans with an identity, not paused, not terminal, not abandoned    |
| The reconciler removes without emitting                                                                         | terminal snapshots, and abandoned snapshots past the liveness window |
| A startup pass removes any snapshot older than 14 days, reconciling first if it has not already been reconciled | **everything, including paused snapshots**                           |

**Paused snapshots are no longer exempt from the age cap**, and that is a
deliberate change from the first draft, which exempted them from all three
rules. Combined with an administrative verb that could adopt, that exemption
made an adopted-then-paused stub for a dead run **immortal on disk** while its
platform row stayed `running` forever — F9's third defect wearing a run id. A
paused snapshot is still never reconciled while it is fresh (C5 intact: the
restore prompt reads it on the next activation), but a pause nobody resumed in
two weeks is debris.

**None of this may be gated on the platform.** `reconcileOrphanedRuns` today
begins `if s.analyticsSvc == nil { return }`, and AGENTS.md states the product
"runs fully locally against your own model keys with no account and no server" —
so on a first-class supported configuration the reconciler, the retention rules
and the legacy sweep are all dead code, while the scheme moves from **one file
per issue** (overwritten by every re-dispatch) to **one file per run**, i.e.
monotonic growth exactly where nothing collects it (F24). **Emission and
removal are split**: the scan and every removal run unconditionally; emission is
skipped when `analyticsSvc == nil`. A local-only workspace collects its
snapshots on the same schedule as a connected one and simply tells nobody.

### 8. Snapshot layout: `runtime-{issue}-{runId}.json`

```
{repoRoot}/.nightgauge/pipeline/runtime-{issueNumber}-{runId}.json
```

Everything C6 requires survives, and the filename becomes the identity check for
destructive writes:

| Requirement                               | How                                                                                           |
| ----------------------------------------- | --------------------------------------------------------------------------------------------- |
| Discoverable by directory scan, no index  | `^runtime-(\d+)-([0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})\.json$` |
| Parseable with no registry                | unchanged — a full `RuntimeState` snapshot, now also carrying `terminal` / `abandoned`        |
| Atomic                                    | unchanged — `state.AtomicWriteFile`                                                           |
| Rooted in the run's target repo           | unchanged (#215/#307)                                                                         |
| Never written for an unattributed runtime | the `repo != ""` gates stay, and `Persist` now also refuses an empty `RunID` (Decision 1)     |

The discovery regex is the **same** expression as Decision 1's wire validation,
shared as one constant on each side rather than written twice. Because the
identity is always locally minted (Decision 2), there is no id shape that can
pass validation and fail discovery — the mismatch that would have stranded
dashboard-triggered runs outside `collectOrphanedRuns` while
`classifyRuntimeStub` deleted their live crash snapshots.

**API changes, named because five callers depend on them:**
`LoadPersistedState(stateDir, issueNumber)` becomes
`LoadPersistedState(stateDir, runID)`, plus a new
`FindPersistedStatesForIssue(stateDir, issueNumber) []*RuntimeState` for the
issue-addressed readers (`getState`'s fallback tier, `attention_raise.go`,
`local_state_service.go`, `wave_orchestrator.go`).

**Rejected layouts.** A per-run _directory_ multiplies inode churn and
complicates the atomic-write and scan story for no gain — there is exactly one
file per run. A _sidecar index_ is a second writer over the same state, which is
precisely the #316 lesson ADR 015 Decision C already paid for; the directory
listing is the index.

#### The `current-run.json` sidecar gains `run_id`

`CliPipelineReconciliationService` (`CliPipelineReconciliationService.ts:137`)
is how every `nightgauge run` CLI pipeline becomes visible in the extension. It
reads the `current-run.json` sidecar and then
`runtime-{sidecar.issue_number}.json`, inside a bare `catch { return null }`
whose comment declares every failure "transient/non-actionable". Under the new
filename that read ENOENTs on every 1s poll forever: `onDiscovered` never
fires, so the CLI run gets no tree slot, no `PipelineStateService` and no
`applyRuntimeSnapshot`; and because `active` never gains an entry, `onSettled`
never fires either. No log, no error, no test failure — the entire
CLI-observability feature dies with zero diagnostics (F25).

A mechanical rename cannot fix it: the sidecar carries only
`{issue_number, repo, pid}`, and `FindPersistedStatesForIssue` is a Go API that
does not help a TypeScript consumer. **So the sidecar gains `run_id`**, written
by the same scheduler site that writes it today
(`internal/orchestrator/failure_handler.go:468-508`, the struct behind
`currentRunSidecarFile`), and `readActiveRun` composes the filename from it. The
sidecar is already the TS side's index into a run; it now carries the run's
identity, which is also what lets the `pid` liveness check corroborate a
specific run rather than an issue.

#### Third-process seams (C12) thread the identity — and this ADR names who exports it

`NIGHTGAUGE_RUN_ID` has **no producer anywhere in the tree today**; a grep
matches only this ADR. The first draft named two consumers, deleted the only
working resolution path in favour of it, and specified both consumers to degrade
to "record nothing" — which would have shipped every stage-gate result silently
unrecorded (#210) and ADR-013 traces losing their run id on the interactive path
entirely, which in turn falsifies the F14 benefit Decision 9 claims. The
exporters are therefore named, and **sequenced before** the consumers change:

| Exporter                                                                                                                                                                           | Scope                                                                                                                                                                          |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `internal/execution/adapters/*.go` stage env builders (`claude.go:69`, `codex.go:160`, `gemini.go:65`, `gemini_sdk.go`, `copilot.go`, `claude_sdk.go`, `ollama.go`, `lmstudio.go`) | every scheduler-driven stage child process — alongside the `NIGHTGAUGE_ISSUE_NUMBER` / `NIGHTGAUGE_REPO` / `NIGHTGAUGE_STAGE` they already export. `RunOptions` gains `RunID`. |
| `packages/nightgauge-vscode/src/services/SkillRunner.ts`                                                                                                                           | every extension-driven stage child process. It **already receives** `params.runId` (threaded for #228) and passes it down; it now also exports it into the child env.          |
| `HeadlessOrchestrator`'s four `nightgauge gate verify` spawns (`:2070, :2607, :2847, :3008`)                                                                                       | passes `--run-id` **explicitly** — the orchestrator holds the id, so the gate CLI never has to fall back to the environment on this path.                                      |

With those in place:

- `nightgauge gate verify --record` gains `--run-id`, defaulting to
  `NIGHTGAUGE_RUN_ID` from the stage environment. Without an identity the
  **verdict still runs and is returned**; only the `--record` write is skipped,
  with a loud error. Combined with Decision 5's load-or-skip, a gate record is
  written only into a snapshot that exists, is non-terminal, and belongs to the
  run that produced the verdict.
- `packages/nightgauge-sdk/src/events/traceRecorder.ts` stops reading
  `runtime-${issue}.json` (`:288`) to discover a `RunID` and reads
  `NIGHTGAUGE_RUN_ID` from its environment. This deletes a cross-process file
  read that was wrong after any adoption or steal (F15) and that would have
  broken on the filename change anyway. Because the exporters above cover both
  execution paths, the recorder resolves an id in every configuration that
  writes a trace today.
- The Go scheduler's three `Persist` sites (C11) need no code change —
  `Persist` derives the filename from the snapshot — but they **do** need the
  new filename, and the scheduler already threads `RunID` from runstate, so it
  satisfies the non-empty-identity rule. The two registries stay separate
  (Decision 11); they simply stop sharing a filename with each other's runs.

### 9. Interactions with the systems that consume run state

**#304 outcome recording (C2).** The current at-most-once guarantee is an
emergent property of a bare-issue delete, stated in a comment: _"the runtime is
deleted at the end of this handler, so a repeated `notifyComplete` finds none and
records nothing."_ Adoption makes that sentence false, so it is replaced with an
explicit one: **`learning.Recorder.Record` gains an idempotency key equal to the
run id and skips a row whose id is already present.** In-process, the terminal
latch already makes the handler body run at most once per run; the corpus dedup
is the cross-process/restart backstop, and it is **durable** — it reads the
corpus — which is what makes R-4 hold without `closedRuns` surviving a restart.
The corpus root stays `s.repoRoot(p.Repo)` — the run's target repo, never
`s.workspaceRoot` — and every loud log for a missing predicted/actual model or a
missing complexity/size keeps firing unchanged.

**#305 corroboration (C3), with an explicit multi-run rule.**
`recordedRunSpendUSD` (`attention_raise.go:255-273`) has two arms — the live
registry entry and the persisted snapshot — and today both resolve exactly one
run because both are issue-keyed. This ADR deliberately creates the multi-run
case, so the selection rule is stated rather than left to "current":

> Corroborate against the entry (or persisted snapshot) whose **`RunID` equals
> the raise's `RunID`**, whose `Repo` equals the raise's `repo`, and whose
> `IssueNumber` equals the raise's `issue`. If the raise carries no `RunID`, or
> no run matches all three, **corroboration fails** — the card is still raised,
> without the raise-and-retry option, exactly as §N specifies for a failed
> corroboration.

Falling back to "the current run" would let `ProposedCeilingUSD`'s
`max(enforced, spent) * 1.5` — which persists a privileged ceiling override —
be derived from the wrong run of the same issue. Both existing rules are
otherwise unchanged: exact repo match on **both** arms, and real progression
(only `CompletedStages` entries with a `BeginStage`-stamped `StartedAt` and a
non-empty `Stage`). The second arm now calls `FindPersistedStatesForIssue` and
filters it by the same triple.

**`attention.raise`'s caller-supplied `RunID` is a label that disambiguates,
never a selector across runs the caller did not name.** ADR 015 §N deferred
socket identity here; the answer for the raise verb is that its `RunID` does not
become the run's identity and cannot address a run outside the `(repo, issue)`
pair the caller already supplies. Within that pair it selects which of two runs
to corroborate against — which is **strictly narrower** than today, where the
server picks one implicitly and the caller has no say and no way to be right.
The `runId` on a raise continues to ride onto the card as a label and a trace
ref.

**#313/#316 history identity (C4).** The run identity maps 1:1 onto
`RunRecord.RunID` by construction — it is the same value. `runRecordKey` stays
`"run:" + RunID`; `appendAndIndex` is untouched (one serialized, idempotent
path; coordinator lock across the JSONL append and the index RMW;
first-write-wins; richer-upgrade-only; skeletons never overwrite). No second
writer is introduced anywhere. The **fallback key path** (`issue:{N}|{whole
UTC second}`, F7) becomes **unreachable** on the extension path, because the
identity now exists before the first transition — strictly better than today,
where two dispatches starting in the same second are one run to the ledger.

**Pause-restore (#2008, C5) is migrated, not merely re-regexed.** The first
draft changed only the discovery regex and `classifyRuntimeStub`, which leaves
the resume path structurally unable to supply the `runId` Decision 3 requires:
`bootstrap/services.ts:1195-1215` discovers a paused snapshot **on disk** and
calls the **singleton** `pipelineStateService.resumePipeline()` — a service that
never dispatched that run and minted no id — then
`headlessOrchestrator.runPipeline(runtime.issueNumber)`, discarding the identity
entirely. The full migration:

1. The discovery regex becomes the shared identity regex; the run id is parsed
   out of the filename (and cross-checked against the snapshot's own `runId`).
2. The prompt is **per issue, not per file**. When more than one paused snapshot
   exists for an issue, one QuickPick lists them by short run id, `started_at`
   and last completed stage; the chosen one is resumed and the others are
   offered for discard. The first draft's behaviour — N identical prompts naming
   the same issue with nothing to tell them apart — is not a UX detail, it is
   the reason the un-chosen files became permanent.
3. Resume threads the identity: `pipelineStateService.beginRun(runId, repo,
issueNumber)` (Decision 10) **before** `resumePipeline()`, and
   `headlessOrchestrator.runPipeline(issueNumber, { runId })`. The resumed run
   continues **under the snapshot's own identity**, so the snapshot is consumed
   rather than orphaned.
4. `setPaused` resolves through the on-disk snapshot when the registry has no
   entry (Decision 3's administrative class), so a pause or resume issued after
   an IPC-server restart lands instead of being dropped.
5. `classifyRuntimeStub`'s two existing rules are unchanged (empty repo/stage →
   delete; repo mismatch against the containing repo → delete) and it gains a
   third: **a new-scheme file with no identity in its name or body → delete**.
   It does **not** classify legacy `runtime-{N}.json` at all — see Migration.

**#375 attention cards: the run id goes on the card, the idempotency key does
NOT change (F14).** The first draft made `BuildAbandonedDispatch`'s
`IdempotencyKey` run-scoped (`producer:repo#issue@runId`) so two dispatches
would produce two cards. That is withdrawn. `attention_wiring.go:950-953`
states the reason for the `(repo, issue)` key, and it was never the
standing-condition/fingerprint argument the first draft rebutted:

> _"Keyed per (repo, issue), NOT per generation: a wedged slot that
> force-clears, gets re-queued, and wedges again is one condition an operator
> has to deal with once, not a new card per attempt. Store.Raise's open-record
> dedup is what collapses them."_

The card is deliberately **event-shaped** (`attention_wiring.go:955-978`), so a
key that is unique per raise can never be updated in place: stopping 8 wedged
slots and retrying twice would yield 24 cards —
`attention_wiring.go:990-999` names that outcome by name, "the inbox-destroying
pattern ADR-015 §D/§L exist to prevent". The benefit the first draft actually
wanted needs no key change: the force-clear now **has** the real run id (it is
the id it tombstoned), so the card carries it as a payload field and a
`runTraceRef(runID)` that resolves to the ADR-013 trace instead of nothing. When
a second dispatch of the same issue wedges while the card is open, the card is
updated in place and its run-id field names the latest dispatch, with the
predecessor's id retained in the card body's history line.

**Stage progress and phase transitions (F10) — both arms.** The first draft
claimed "cross-run corruption is structurally gone rather than guarded
against". That was false for `notifyPhaseTransition`, which does **two** things
per event (`server.go:2972-3026`): `rt.BeginPhase(...)` on
`activeRuntimes[issue]` — the arm the re-keying fixes — and, unconditionally,
`s.scheduler.RecordPhaseStart(p.IssueNumber, ...)` (`:2998`, `:3013`), which
resolves `Scheduler.activeRuntimes map[int]*state.RuntimeState` **by issue
number** (`scheduler.go:900, :935`). Re-keying one arm while leaving the other
issue-keyed closes F10 in one place and leaves it open in the same handler.
Decision 11 gates the scheduler arm on identity. With both arms identity-gated:
neither is silently dropped — a closed run's progress is refused with
`run_closed` (rate-limited in the log), and a merely-unknown run adopts and
keeps streaming — and nothing legitimate goes dark, because Decision 10 gives
every producer an identity to send.

### 10. Producers and consumers of the identity — the complete list

Every producer mints its own identity at its own dispatch point, **or receives
one**; the table is exhaustive by construction, because the review that found
its omissions enumerated the call sites.

| Producer / consumer                                                                                                                    | Identity source                                                                                                                                                                                                                                                                |
| -------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `ConcurrentPipelineManager.startSlot`                                                                                                  | **mints** (Decision 2) and hands the id to the slot's own `PipelineStateService`                                                                                                                                                                                               |
| `PipelineStateService.{startStage,completeStage,failStage,initializePipeline,notifyPipelineComplete}`                                  | the service's single installed identity (see "one identity per service" below)                                                                                                                                                                                                 |
| `PipelineStateService.{pausePipeline,resumePipeline}` → `setPaused`                                                                    | the service's installed identity **and** `repo` + `issueNumber`; never creates a runtime                                                                                                                                                                                       |
| `commands/retryFailedIssue.ts`                                                                                                         | **mints.** It is a dispatch; it was simply never treated as one — and it must `beginRun` (see below)                                                                                                                                                                           |
| `bootstrap/services.ts` singleton state service                                                                                        | **mints** per dispatch, via `beginRun`                                                                                                                                                                                                                                         |
| `commands/{resumePipeline,pausePipeline,runStage,runInteractiveStage,retryFromPhase,retryStage}.ts`, `bootstrap/services.ts:3345,3355` | operate on the singleton service's **installed** identity; they never mint and never construct a raw call                                                                                                                                                                      |
| `HeadlessOrchestrator` direct entry points                                                                                             | receive the slot's id when manager-driven; `beginRun` at their own entry otherwise                                                                                                                                                                                             |
| `HeadlessOrchestrator.ts:13323` raw `notifyStageProgress`                                                                              | the orchestrator's own run context — **and its `repo`**, not `""`                                                                                                                                                                                                              |
| `bootstrap/services.ts:3094` raw `notifyStageProgress`                                                                                 | the singleton service's identity — **and its `repo`**, not `""`                                                                                                                                                                                                                |
| **`PipelineBridge.ts:280` (`notifyPhaseTransition`) and `:355` (`notifyStageProgress`)**                                               | **`ipcParams.runId`, which already exists** — `IpcRunStageParams.runId`, "UUID v7 run ID threaded from runstate for correlation (#3557)", already forwarded to SkillRunner at `:216`. Served by the **scheduler** registry, never adopted into `activeRuntimes` (Decision 11). |
| Go scheduler (`scheduler.go`)                                                                                                          | mints from runstate; **stops preferring `RemoteRunID`** (Decision 2); registers under its own registry                                                                                                                                                                         |
| `nightgauge gate verify --record`                                                                                                      | `--run-id`, else `NIGHTGAUGE_RUN_ID`; records nothing without one, and never creates a file (Decision 5)                                                                                                                                                                       |
| SDK `traceRecorder`                                                                                                                    | `NIGHTGAUGE_RUN_ID`; the `runtime-${issue}.json` read is deleted (Decision 8)                                                                                                                                                                                                  |
| `CliPipelineReconciliationService`                                                                                                     | **consumer** — reads `run_id` from the `current-run.json` sidecar (Decision 8)                                                                                                                                                                                                 |
| `bootstrap/services.ts` pause-restore prompt                                                                                           | **consumer** — parses the id from the filename and installs it via `beginRun` (Decision 9)                                                                                                                                                                                     |
| `PipelineSlotsTracker`, `PipelineStateService` event filters                                                                           | **consumers** — route/filter on the envelope's `runId`, empty id falls back (Decision 6)                                                                                                                                                                                       |

This is the structural answer to F17. The refuted design's guard was gated on
`token != ""`, so the token-less producers bypassed it, adopted a live run's
runtime, wrote the authoritative record and outcome under it, deleted the
runtime — after which the live run's own completion was refused and it recorded
nothing at all. When _everyone_ mints or receives, there is no token-less
producer to bypass anything.

**One identity per service, and minting onto a live one is refused (F23).**
"Every producer mints at its own dispatch point" is not structural on its own:
`retryFailedIssue.ts`, `bootstrap/services.ts` and the `HeadlessOrchestrator`
direct entry points drive the **same singleton** `PipelineStateService`, whose
identity would live in one mutable slot exactly as `setDispatchToken` does
today. `retryFailedIssue.ts:94-123` auto-clears only when
`existingState.issue_number !== issueNumber`; retrying the **same** issue falls
straight through to `initializePipeline` while the first run is still executing.
The last minter would win, R1's remaining transitions would be sent under R2's
identity, R1's entry would go silent (never terminal, leaked, later reconciled
with a spurious `pipeline_done`), and R1's stages would be booked into R2's
runtime and R2's authoritative record — F1 and F2 reproduced through the ADR's
own minting rule. Therefore:

- The identity is installed by an explicit **`beginRun(runId, repo,
issueNumber)`**, not stamped as a side effect of `initializePipeline`.
- **`beginRun` on a service that already holds a non-terminal identity is a hard
  error**, surfaced to the operator: _"#N is already running (run `01927f…`).
  Stop or clear it before retrying."_ This is the same guard #870 already
  applies **across** issues, now applied **within** one — the case it always
  should have covered.
- `endRun` (fired by `notifyPipelineComplete`, `clearPipeline`, and the slot
  teardown) releases the slot.
- Every operator-initiated method (`pausePipeline`, `resumePipeline`,
  `runStage`, `retryStage`, …) reads the installed identity. None of them
  mints, and none of them may run with no identity installed.

The general rule this encodes: **identity is not ambient.** A producer may not
stamp an identity onto a holder that already has a live one, and a holder with
no identity is not a valid target for a run-bearing call.

`setPaused` deserves its own line, because it is three defects in one call
today (F9): it mints a runtime with no repo and no `RunID`, so the next real
dispatch adopts an identity-less stub; it can call `SetPaused` + `Persist` on a
**live successor's** runtime; and the unattributed entry pins the issue against
#44 forever. Requiring `runId` + `repo` + `issueNumber`, forbidding creation,
and subjecting paused snapshots to the age cap kills all four.

### 11. Two registries: which one serves a call

`activeRuntimes` (IPC server, re-keyed by run id) and
`Scheduler.activeRuntimes` (`map[int]`, C11) both exist and both stay. This ADR
re-keys the first and **does not** re-key the second — but it does make the
boundary between them explicit, because three defects lived in the gap.

**Resolution order for every identity-bearing call:**

```
1. closedRuns.has(runId)               → run_closed
2. ipcRegistry[runId]                  → serve from the IPC registry
3. scheduler.LookupRunByID(runId)      → serve from the SCHEDULER's runtime; NEVER adopt
4. run-progress verb                   → adopt into the IPC registry
   administrative verb                 → resolve from disk, else run_not_found
```

Step 3 is what keeps "the two registries stay separate" true. Without it,
`PipelineBridge`'s two calls — the product's **primary** execution path, since
all pipeline orchestration decisions flow through the Go scheduler — would carry
the scheduler's `RunID` into step 4 and **adopt**, manufacturing a second
in-memory entry for a run the scheduler registry already owns. That entry would
hold a lease, would never be terminal-claimed (the scheduler path never calls
`notifyComplete`), would leak for the life of the server, and — via Decision 6's
derived index — would become the "current" run for its `repo#issue`, so #305's
`recordedRunSpendUSD` would read a near-empty runtime instead of the scheduler's
real one.

**Consequences of step 3, spelled out:**

- `notifyPhaseTransition`'s scheduler arm becomes
  `Scheduler.RecordPhaseStartForRun(runID, issueNumber, …)`, which resolves by
  issue and then **no-ops unless the registered runtime's `RunID` equals
  `runID`**. This is an identity guard at the write site rather than a re-key,
  and it is sound here for a reason the IPC registry cannot claim: the scheduler
  registry has a single writer per issue by construction
  (`registerRuntime`/`unregisterRuntime` bracket `runPipeline` in one
  goroutine), so the only cross-run hazard is a write arriving **from outside**
  over IPC — which is exactly what the guard rejects. Re-keying that registry is
  named as a follow-up (R-5), not smuggled in here.
- `Scheduler.IsRunLive(runID)` feeds the reconciler's skip predicate (7.2).
- The scheduler's `stateChanged` / `phase.*` emitters carry its runstate `RunID`
  (Decision 6).
- Scheduler-driven runs are **not** in the IPC issue index and are never
  "current" there; issue-addressed `getState` continues to resolve them through
  its `execMgr` tier-0, unchanged.

### 12. Where the identity lives, and under which lock

- `RuntimeState.RunID` — the identity. **Immutable after construction**,
  persisted, needs no lock (Decision 1).
- `runEntry` — a new wrapper struct owned **exclusively** by `runtimesMu`,
  holding the registry's mutable bookkeeping: `Terminal bool`,
  `Abandoned bool`, `AbandonedAt`, `FirstSeen`, `LastSeen`, and the
  `*RuntimeState` pointer. **Never persisted, never read under
  `RuntimeState.mu`.**
- `terminal` / `abandoned` also exist as **persisted fields** on the snapshot —
  the durable facts of Decisions 4 and 7. They are written only by the claim
  path and read by adoption, the reconciler, and the gate CLI.

`RuntimeState` owns the run's _content_ behind its own mutex; `runEntry` owns
the registry's _bookkeeping_ behind the server's. No field is written under one
lock and read under another, which is the specific hazard F20 raised — a
comparator whose torn read a sequential `-race` test would not catch.

---

## Migration

**Pre-customer, no compatibility.** The issue-keyed paths are deleted outright:
no dual-read, no legacy filename support in `LoadPersistedState`, no compat
knob, no optional-`runId` grace period.

**The wire change is required, so `ProtocolVersion` goes 1 → 2 — and this ADR
ships the hard-fail rather than claiming one exists.** The first draft asserted
that "the TypeScript client already validates `protocolVersion` in the
`ipc.ready` event and rejects a mismatch". **It does not.**
`IpcClient.ts:57-70` writes `WARNING: Binary protocol version … does not match
expected …` to an output channel and continues — no throw, no rejected
connection, no modal, no disconnect. So the degrade path the first draft called
impossible is exactly what would have shipped: an old extension against a new
binary keeps running and sends identity-less `pipeline.*` calls, every one
refused `run_id_required`, producing a live run with zero records, zero learning
outcomes and zero telemetry, discovered hours later — F16's shape, two sentences
from where the draft named F16 as the reason lockstep is correct. Therefore:

> On a `protocolVersion` mismatch the `ipc.ready` handler **disconnects the
> client, marks it permanently unusable for this activation, and raises a
> blocking modal** naming both versions and the required action. Every
> subsequent `call()` rejects immediately with `protocol_mismatch` rather than
> reaching the socket. This is specified here because it is the mechanism the
> whole migration rests on.

Lockstep is the _correct_ behaviour here rather than a regrettable one: the
degrade-instead-of-fail alternative is the silent lockout above.

**In-flight runs across the upgrade: quiesce is required, and that is
acceptable — stated explicitly.** Upgrading the binary or the extension
terminates runs that are in flight. This ADR makes no attempt to carry a run
across the upgrade, and there is no hidden cost in saying so: an extension host
update already tears down the IPC server and every slot today. The worktree,
branch, and queue state survive; the run does not, and is re-queued.

**Legacy `runtime-{N}.json` on disk: reconcile-then-delete, once, and the Go
sweep owns it exclusively.** The alternative — ignore-and-delete — creates
phantom `running` platform rows for runs that were live moments before the
upgrade, which is precisely the defect #44 exists to prevent. A one-shot startup
pass inside `Server.Run` matches the legacy pattern, emits each snapshot's
terminal `pipeline_done` through the existing #44 path (when analytics is
present — the removal is unconditional either way, per 7.4), and deletes the
file. It **never** adopts a legacy file into a live run and `LoadPersistedState`
never reads one — it is a sweep, not a shim.

**Exactly one sweep owns legacy disposition, and it is that one.** The first
draft also required `runtimeStubSweep.test.ts` to show legacy `runtime-\d+.json`
classifying as `delete` — i.e. the TypeScript activation sweep at
`bootstrap/services.ts:1171` performing exactly the ignore-and-delete the
migration forbids. Both run at activation, nothing orders them, and the TS sweep
is `void`-launched from an async IIFE with no `await`, so whichever won would
decide nondeterministically, per activation, whether the platform row was closed
or stranded `running`. **The TS sweep's `/^runtime-\d+\.json$/` filter is
narrowed to the new-scheme regex**, so it never sees a legacy file; its
`classifyRuntimeStub` job over new-scheme stubs is unchanged.

To keep the Go sweep from becoming an open-ended compatibility path, it ships in
the same PR as the re-keying and its **removal is filed as a follow-up issue at
merge time**, to land one release later. A legacy **paused** snapshot cannot be
restored (it has no identity), so it is reported once in the log with its issue
number and deleted; the operator re-queues the issue, and its worktree is
untouched.

**Observability fixes that ship with the migration** (Decision 3): a
`logger.warn` in each of the five bare `catch {}` blocks at
`PipelineStateService.ts:717, 741, 787, 828, 966`, carrying the stage, the run
id and the JSON-RPC error code.

---

## Testing Strategy

The #307 probes become the committed regression suite. Every case below must be
**verified failing against pre-fix code first**, as #307 round 1 required — a
regression test that has never been red is a test of the fix's spelling, not of
the defect. The two named probes lived in a scratchpad directory that no longer
exists and the extension probes were never committed; both are reconstructed
here as permanent tests.

### Go — `internal/ipc/`

| Test                                                                                                                          | Must show                                                                                                                                                                                                                                                                                                                                 |
| ----------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TestRunIdentity_AbandonedRunStillBooksItsOwnCompletion` (from `TestProbe_ForceClearedRunResurrectsAfterSuccessorCompletes`)  | After `abandonRun`, the same run's late `pr-create running` + `notifyComplete{prMerged:true}` are **accepted**, produce exactly **one** run record and **one** learning-corpus row under that run's own id, and its `pipeline_done` supersedes the abandon event. A **successor** run of the same issue is untouched. Covers F1, F4, F18. |
| `TestRunIdentity_SuccessorWithoutInitializedRecordsNormally` (from `TestProbe_SuccessorWithoutInitializedIsLockedOutForever`) | A run whose first message the server never saw has **every** subsequent transition accepted by adoption, and its completion writes exactly one record, one outcome, and full telemetry. This is the test that fails against the refuted design (F16) and must never be deleted.                                                           |
| `TestRunIdentity_AbandonRunNeverCreates`                                                                                      | `abandonRun` for an id with no entry **and** no snapshot returns `no_run`, writes nothing, adds nothing to `closedRuns`, emits no `pipeline_done`, and a subsequent first message from that id is served normally. Covers the `bookForceClearedReservation` case.                                                                         |
| `TestRunIdentity_AbandonRunRequiresMatchingRepoAndIssue`                                                                      | A mismatched `repo` or `issueNumber` yields `run_not_found` and mutates nothing; no `pipeline_done` with `IssueNumber: 0` is ever emitted. Covers C8.                                                                                                                                                                                     |
| `TestRunIdentity_ZombieCannotMutateSuccessor`                                                                                 | Interleaving two run ids on one issue: the successor's `TotalCostUSD`, per-stage tokens, `StageErrors`, `PhaseHistory` and `RunRecord` are byte-identical to a solo run. Covers F2, F4, F10.                                                                                                                                              |
| `TestRunIdentity_TerminalDeleteIsIdentityChecked`                                                                             | A successor's entry installed during `notifyComplete`'s unlocked window survives: its registry entry is intact and its snapshot file still exists on disk. Covers F5 and C7.                                                                                                                                                              |
| `TestRunIdentity_TerminalSnapshotIsNeverRehydrated`                                                                           | A terminal-marked snapshot left on disk (removal failed) makes a later call `run_closed`, not an adoption; no record is written and the authoritative index entry for `run:<id>` is unchanged. Covers the R-4 interleaving.                                                                                                               |
| `TestRunIdentity_TerminalRemovalUsesSnapshotRepo`                                                                             | A `notifyComplete` whose `repo` param differs from the run's persisted repo still removes the correct file and leaves no other repo's file touched.                                                                                                                                                                                       |
| `TestRunIdentity_CrossRepoSameIssueNumberDoNotCollide`                                                                        | Repo A #42 and repo B #42, no force-clear involved, keep separate runtimes, snapshots and records. Covers F8.                                                                                                                                                                                                                             |
| `TestRunIdentity_SetPausedNeverCreatesARuntime`                                                                               | `setPaused` for a closed id errors `run_closed`; for an unknown id with **no** snapshot errors `run_not_found` and writes **no file**; for an unknown id **with** a snapshot resolves through disk without creating a registry entry. Covers F9.                                                                                          |
| `TestRunIdentity_InvalidRunIdIsRejectedBeforeUse`                                                                             | Table-driven over `../`, `/`, `%2e%2e`, uppercase, a UUIDv4, and a 36-char non-UUID: every one returns `run_id_invalid`; an **empty** id returns `run_id_required`. In no case is a file created, read or removed anywhere under the state dirs. Pins Decision 1's regex to Decision 8's discovery regex as the same constant.            |
| `TestRunIdentity_ClosedRunIsRefusedOnEveryRunProgressMethod`                                                                  | Table-driven across the four run-progress methods: each returns `run_closed` as a JSON-RPC **error**, and mutates nothing.                                                                                                                                                                                                                |
| `TestRunIdentity_SchedulerRunIsServedNotAdopted`                                                                              | A `notifyStageProgress` / `notifyPhaseTransition` carrying a **live scheduler** run's id creates **no** entry in `activeRuntimes`, records onto the scheduler's runtime, and does not become "current" in the issue index. Covers the PipelineBridge path and Decision 11.                                                                |
| `TestRunIdentity_PhaseTransitionSchedulerArmIsIdentityGated`                                                                  | A phase event whose `runId` does not match the scheduler's registered runtime for that issue records **nothing** in that runtime's `PhaseHistory`. Covers the second arm of F10.                                                                                                                                                          |
| `TestOrphanReconcile_ClosesAbandonedRunAtRootSwitch`                                                                          | With an abandoned entry in the registry, the `workspace.setRoot` call site emits `pipeline_done` and removes the snapshot — the case that is dead on main (F11). The fresh-start case continues to pass unchanged.                                                                                                                        |
| `TestOrphanReconcile_LiveSchedulerRunIsNotReconciled`                                                                         | A live Go-scheduler run's snapshot is **skipped** at `workspace.setRoot`: no `pipeline_done`, file intact. Verified failing against `main`, where it is reconciled. Covers F21.                                                                                                                                                           |
| `TestOrphanReconcile_PausedAndIdentityLessSnapshotsStillSkipped`                                                              | C1/C5 preservation: paused snapshots and snapshots with no identity are skipped, not reconciled, not deleted — until the 14-day cap, which does remove a paused one.                                                                                                                                                                      |
| `TestOrphanReconcile_LiveLeaseIsNotReconciled`                                                                                | A run whose `LastSeen` is inside the window is skipped; the same run outside the window is reconciled.                                                                                                                                                                                                                                    |
| `TestOrphanReconcile_RunsAndRemovesWithoutAnalytics`                                                                          | With `analyticsSvc == nil`, the scan still runs, every removal rule still fires, and no event is emitted. Covers F24.                                                                                                                                                                                                                     |

### Go — `internal/state`, `internal/learning`, `internal/orchestrator`

| Test                                             | Must show                                                                                                                                                                                                                             |
| ------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TestPersist_RefusesEmptyRunID`                  | No `runtime-42-.json` is ever created; the call errors.                                                                                                                                                                               |
| `TestAdoption_RehydratesFromSnapshot`            | An unknown id whose non-terminal snapshot exists on disk adopts with its full stage history, not an empty runtime.                                                                                                                    |
| `TestGateRecord_NeverCreatesASnapshot`           | `AppendStageGateResultToDisk` with no existing file records nothing and errors loudly; with a `terminal` file it records nothing; `PersistExisting` fails on a removed file. Covers F22.                                              |
| `TestHistory_TwoRunsOfOneIssueProduceTwoRecords` | Covers F6 and F7, including two dispatches starting within one UTC second.                                                                                                                                                            |
| `TestLearningRecorder_RecordIsIdempotentByRunID` | Two `Record` calls for one run id append one row, **including across a process restart** (the corpus is the durable dedup). Explicit replacement for the deleted "the runtime is deleted so a repeat records nothing" guarantee (C2). |
| `TestScheduler_MintsLocallyIgnoringRemoteRunID`  | A queue item carrying a `RemoteRunID` produces a locally-minted `RunID`; the remote id is carried as a correlation attribute only. Covers Decision 2's reversal.                                                                      |
| `TestSidecar_CarriesRunID`                       | `current-run.json` written by the scheduler contains `run_id` matching the runtime snapshot's filename component.                                                                                                                     |

### TypeScript — `packages/nightgauge-vscode/tests/`

| Test                                                                                  | Must show                                                                                                                                                                                                                                                            |
| ------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ConcurrentPipelineManager.terminalDoubleBook.test.ts` (PROBE-X / PROBE-Y, committed) | The pre-fix sequences `after force-clear: failed=1 completed=0 \| final: failed=1 completed=1` and `afterForceClear=1 final onSlotFailed=2` become `failed=1 completed=0` at **both** checkpoints, with `onSlotFailed` called exactly once. Covers F18 and pins C10. |
| `ConcurrentPipelineManager.runIdentity.test.ts`                                       | The minted `runId` reaches the slot, the reservation, the `PipelineStateService`, and `pipeline.abandonRun` on force-clear; `forceClearedRunIds` holds run ids and has no release path; the funnel calls `abandonRun` and **never** `notifyComplete`.                |
| `PipelineStateService.identityIsNotAmbient.test.ts`                                   | `beginRun` on a service holding a live identity throws and mutates nothing; `retryFailedIssue` against an in-flight **same** issue surfaces the error instead of relabelling the live run. Covers F23.                                                               |
| `PipelineStateService.stateChangedRouting.test.ts`                                    | A `stateChanged` for a **different** run id on the **same** issue is ignored; one with an **empty** run id is still applied via the issue pre-filter. Covers F19 and Decision 6's fallback.                                                                          |
| `PipelineBridge.identity.test.ts`                                                     | Both raw IPC calls carry `ipcParams.runId`; neither is rejected; the ≥5s progress cadence produces no rejection log.                                                                                                                                                 |
| `CliPipelineReconciliationService.test.ts` (extended)                                 | A sidecar carrying `run_id` discovers `runtime-{issue}-{runId}.json` and fires `onDiscovered`; a sidecar without `run_id` is skipped **with a log**, not silently. Covers F25.                                                                                       |
| `pauseRestore.test.ts`                                                                | Two paused snapshots for one issue produce **one** QuickPick listing both by run id and `started_at`; resuming threads the parsed `runId` into `beginRun` + `runPipeline`; the un-chosen file is discarded, not left to re-prompt. Covers C5.                        |
| `runtimeStubSweep.test.ts` (extended)                                                 | Legacy `runtime-\d+.json` files are **left untouched** by the TS sweep (Go owns them); identity-less new-scheme files classify as `delete`; paused, identified files classify as `keep`.                                                                             |
| `IpcClient.protocolMismatch.test.ts`                                                  | A mismatched `ipc.ready` `protocolVersion` disconnects the client, raises the modal, and makes every subsequent `call()` reject with `protocol_mismatch` without touching the socket.                                                                                |

### Fences

`internal/orchestrator/testdata/terminal_behaviors.json` holds sha256-pinned
`terminal-parity:begin/end runSlotPipeline-finally` and `force-clear-funnel`
fences. Adding `abandonRun` to the force-clear funnel breaks the parity test **on
purpose**; the re-pin is a reviewed line in the same commit, never a
mechanical refresh. The prose-enforced `accounting` row gains the run identity
and the rule that the funnel never calls `notifyComplete`.

---

## Alternatives Considered

### A — Latch the identity on `initialized`, reject anything that does not match

The intuitive design: the first message of a dispatch establishes the run, and
every later message must match it or be refused as stale.

**Refuted by `TestProbe_SuccessorWithoutInitializedIsLockedOutForever`.** A
live, ultimately **successful** run had every transition rejected —
issue-pickup, feature-dev, feature-validate, pr-create, pr-merge — and its
completion rejected too. It wrote zero run records, zero learning outcomes and
zero telemetry, and its UI stayed frozen for the entire run. All four enabling
conditions are still on main and none is fixable in isolation: the sole
slot-path emitter of `initialized` sits inside the manager's `try/catch`;
`PipelineStateService.initializePipeline` swallows the IPC error internally and
fabricates local state; `HeadlessOrchestrator`'s `!existingState` fallback can
never fire for a slot, because `startSlotInner` already called `initEmpty` and
`getState()` returns the local cache; and nothing in `packages/` reads a
`{"status":"stale"}` response, so the rejection is invisible. The IPC client
bounds each request at 30 seconds — and a wedged socket is precisely the
condition that produces the abort deadline in the first place.

Making one at-most-once message load-bearing for a run's entire server-side
existence trades a **loud, occasional** corruption for a **silent, total** data
loss. Decision 3's re-assertion on every message is the direct answer.

### B — Side-store for superseded runtimes (`abandonedRuntimes`) + supersede-by-message-type

Park the displaced runtime in a second map so a late `notifyComplete` can still
resolve it, and treat an incoming `initialized` as proof that a new dispatch has
started.

**Refuted on four independent counts.** The side store was consulted **only** on
a `notifyComplete` miss, while `notifyStageTransition` still minted on miss and
never consulted it — so a parked run that emitted one more transition silently
acquired a **new** identity, re-created its snapshot, and resolved its
completion through the live map anyway; the parked copy was read only by a run
that emitted zero further transitions, the narrow case. It had a single prune
site, so a successor's completion discarded a live predecessor's parked runtime,
and a run that never completed leaked one entry forever. Supersede-by-message-type
is not ordering evidence: five call sites emit `initialized`, so a stale one
arriving after a successor claimed the key **evicts the live run**, emits a
bogus terminal `pipeline_done` for it, and rejects everything it sends
thereafter. And emitting a terminal event at supersede time double-closed the
run — the persist is gated on `repo != ""` and a slot's first `initialized`
carries no repo, so a restart in that window re-emitted the same run's
`pipeline_done` and then removed the file, taking the successor's crash snapshot
with it.

A side store is a second registry with weaker invariants than the first. The
correct move is to make the _first_ registry's key correct.

### C — Repo-qualify the key (`repo#issue`) — implement what the comment claims

Cheap, and it is what the struct comment has asserted since the map was
introduced.

**Rejected as insufficient.** It closes F8 and nothing else. Two dispatches of
the _same_ repo's issue still collide, which is every force-clear/zombie window
(F1–F5, F10–F12) — the majority of the failure catalogue and the only reason
#370 exists.

### D — Server-minted identity via a `pipeline.openRun` request/response

An explicit open verb whose **response** carries a server-minted id, making the
handshake request/response rather than fire-and-forget.

**Rejected.** The identity would not exist until a round trip completed, and the
sole client wraps each IPC call in a swallowing `try/catch` behind a 30-second
timeout on a socket whose wedging is the condition being defended against. A
lost response yields a run with no identity — F16 in a new costume, with an
extra verb. Client-side minting (Decision 1) makes the identity exist
unconditionally, before any I/O that can fail.

### E — Two identities: an extension dispatch id plus a derived `RunID`

Keep the #307 generation as the wire identity and bind Go's `RunID` to it on
first sight.

**Rejected.** A binding table is a thing that can be stale, lost, or disagreed
about, which is the defect class this ADR deletes. It would also leave the
force-clear unable to name the Go-side run without a lookup that is exactly as
stale as the wedge that caused the force-clear. See Decision 2.

### F — `abandonRun` as a full terminal claim (the first draft)

Make `abandonRun` the exact counterpart of `notifyComplete`: terminal latch,
`closedRuns` insert, compare-and-delete, snapshot removal.

**Rejected in design review, and it is the most important rejection here.** The
verb's population is by definition runs that are **still alive** — a wedged
adapter that ignored its `AbortController`. Closing them makes every later
message `run_closed`, all of it swallowed by bare TypeScript catches that
fabricate a healthy UI, so a run that merges a PR records nothing at all and its
platform row closes as an abandoned failure. That is Alternative A's outcome and
a regression against shipped #307. Decision 7.1's dispatch-terminal design keeps
the platform row honest **and** keeps the run's own books open.

### G — Blanket "unknown identity → adopt" for every verb (the first draft)

One universal three-line rule for all identity-bearing calls.

**Rejected.** Adoption is correct for a caller describing **its own** run and
wrong for a caller asserting something **about** a run: an administrative verb
that adopts can terminate or pause a run the server has never seen, invent a
`pipeline_done` with `IssueNumber: 0`, and — with `closedRuns` — lock out a
dispatch that had not yet reached Go. Decision 3's two verb classes are the
answer.

### H — Seed `runId` from the platform's `RemoteRunID` (the first draft)

When a dispatch originates from a `PendingCommand` carrying `RemoteRunID`, use
that value as the identity so the local and platform ids coincide.

**Rejected.** It hands the registry key, the snapshot filename and the
history-record key to a value chosen by a remote server over an at-least-once
channel, with no validation anywhere in the current path. A redelivered command
produces two dispatches under one key; a crafted one collides with a live local
run. Decision 2 keeps `remoteRunId` as a correlation attribute.

### I — Run-scope `BuildAbandonedDispatch`'s `IdempotencyKey` (the first draft)

Key the force-clear card `producer:repo#issue@runId` so two dispatches produce
two cards.

**Rejected.** `attention_wiring.go:950-953` chose the `(repo, issue)` key
deliberately, for a reason the first draft never engaged: repeated wedges of one
issue are one condition an operator handles once, and the card is event-shaped,
so a per-raise-unique key can never update in place. Stopping 8 slots and
retrying twice would produce 24 cards — the pattern ADR-015 §D/§L exist to
prevent. The run id rides as a payload field and a trace ref, which is what was
actually wanted.

---

## Revision history — what design review changed

Recorded because a Decided ADR is the permanent record and several of these
reverse a sentence a reader may have seen.

| #   | First draft                                                                           | Decided                                                                                                                                            |
| --- | ------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | `abandonRun` is a full terminal claim, `closedRuns` insert included                   | `abandonRun` terminates the **dispatch**; the run stays adoptable and books its own honest completion (7.1, Alternative F)                         |
| 2   | Universal "unknown → adopt"                                                           | Two verb classes: run-progress adopts, administrative resolves-or-refuses (Decision 3, Alternative G)                                              |
| 3   | `abandonRun {runId, reason}`                                                          | `{runId, repo, issueNumber, reason, stage?}`, with all three corroborated against the resolved run (7.1)                                           |
| 4   | Seed `runId` from `RemoteRunID`                                                       | Always mint locally; `remoteRunId` is a correlation attribute; the scheduler's `RemoteRunID` preference is deleted (Decision 2, Alternative H)     |
| 5   | "A terminal run has no snapshot, so the file's presence is evidence"                  | A **durable** `terminal` marker is stamped before removal; adoption refuses a terminal snapshot; removal uses the snapshot's own repo (Decision 4) |
| 6   | Terminal latch guarantees no resurrection                                             | Restated as in-process; the gate CLI's create-on-miss is **deleted** and it writes through `PersistExisting` (Decision 5)                          |
| 7   | Index "current" ranked by `FirstSeen`, "self-correcting"                              | Ranked by `LastSeen`; abandoned entries drop out (Decision 6)                                                                                      |
| 8   | `skipRun` consults the IPC registry                                                   | Consults **both** registries; `Scheduler.IsRunLive` added (7.2)                                                                                    |
| 9   | Paused snapshots exempt from all retention                                            | Exempt from reconciliation while fresh; **subject to the 14-day cap** (7.4)                                                                        |
| 10  | Retention/reconciliation as-is                                                        | Emission and removal split so both run on a local-only workspace (7.4)                                                                             |
| 11  | `NIGHTGAUGE_RUN_ID` consumed, exporter unnamed                                        | Three exporters named and sequenced first; the gate CLI also gets `--run-id` explicitly from `HeadlessOrchestrator` (Decision 8)                   |
| 12  | Producer table omits `PipelineBridge`, the raw progress sites, and the CLI reconciler | All enumerated; `PipelineBridge` uses the `ipcParams.runId` it already receives (Decision 10)                                                      |
| 13  | "Every producer mints at its own dispatch point"                                      | Plus: identity is not ambient — `beginRun` refuses to overwrite a live identity (Decision 10)                                                      |
| 14  | Pause-restore = a new regex                                                           | Full migration: parsed id, one QuickPick per issue, id threaded into resume, `setPaused` resolvable from disk (Decision 9)                         |
| 15  | Legacy files swept by both Go and TypeScript                                          | Go owns legacy disposition exclusively; the TS sweep's filter is narrowed (Migration)                                                              |
| 16  | "The TypeScript client already validates `protocolVersion` and rejects a mismatch"    | It does not — this ADR specifies the disconnect + modal + `protocol_mismatch` hard-fail (Migration)                                                |
| 17  | "An error rejects the promise, so every existing try/catch at minimum logs it"        | False — five bare catches named; rejection observability is Go-side, and the catches gain a `warn` (Decision 3, Migration)                         |
| 18  | #375 `IdempotencyKey` becomes run-scoped                                              | Key unchanged; the run id rides as a payload field and trace ref (Decision 9, Alternative I)                                                       |
| 19  | `notifyPhaseTransition` "structurally" fixed                                          | Its second, scheduler-keyed arm is identity-gated (Decision 11)                                                                                    |
| 20  | `runId` format mandated but unvalidated                                               | Validated at the wire boundary against the same constant the discovery regex uses (Decisions 1, 8)                                                 |
| 21  | #305 corroboration "unchanged"                                                        | Explicit multi-run selection rule: match run id + repo + issue, else corroboration fails (Decision 9)                                              |
| 22  | Failure catalogue and constraints cited but undefined                                 | Both inlined as tables above                                                                                                                       |

---

## Consequences

**The registry becomes bounded.** Entries are evicted at terminal, reaped by the
lease, and `closedRuns` is an LRU-capped set with a durable on-disk backing
(Decision 4). Today every force-cleared run and every `setPaused` stub is
retained for the life of the server, and `getState` serves the dead run's
snapshot for that issue indefinitely (F12).

**The snapshot directory becomes bounded too, for the first time.** One file per
run instead of one per issue is strictly more files; the reconciler's
emit-and-remove, the terminal claim's removal, the abandon-grace removal and the
14-day cap are what keep that from being a leak — and all four now run without a
platform account (7.4).

**Issue-addressed reads can now return nothing.** Accepted, and better than the
current confidently-wrong answer.

**Every `pipeline.*` param type changes.** That means IPC codegen regeneration
and the TypeScript client regen — both already steps in the pre-submission
validation sequence — plus `ProtocolVersion` 2 and a specified hard-fail on a
mismatched pair.

**In-flight runs do not survive the upgrade.** Stated as a decision, not
discovered as a symptom.

**A retry of an already-running issue now fails loudly.** `beginRun`'s refusal
turns a silent identity swap into an operator-visible error. This is a
behaviour change to a shipped command and is intended: the silent path was F23.

**F13 is narrowed, not closed — residual risk R-1.** Five writers in three
processes still share the `Persist` whole-file last-write-wins contract. After
re-keying they can no longer clobber _another run's_ file — the failure shrinks
from cross-run corruption to same-run field loss between the IPC server and
`nightgauge gate verify --record`, and Decision 5's load-or-skip +
`PersistExisting` + terminal-refusal narrows it further to a rename race.
Closing it properly means giving the snapshot a single authoritative writer,
mirroring the #316 discipline (the gate CLI posting its result through IPC when
a server is reachable). That is a separate change with its own blast radius and
is filed as a follow-up, not silently absorbed here.

**The IPC socket remains unauthenticated — residual risk R-2, and ADR 015 §N is
amended rather than left over-claiming.** §N says the socket's residual is
"closed by giving the socket an identity (#370)". That is half right, and the
amendment says which half: an identity closes the **addressing** half — after
this ADR a caller must supply an identity it minted, so it can no longer
address, mutate or terminate a run it did not start, and `abandonRun` in
particular corroborates `runId` + `repo` + `issueNumber` against the resolved
run and can at worst emit an early `pipeline_done` the run's own completion
supersedes. It does **not** close the **forgery** half: a writer that can reach
the socket can still mint its own id and drive a run of its own with two calls
(`running`, then `complete`), and #305's corroboration rules (exact repo, real
progression) remain the only mitigation. Socket authentication is the named
successor, filed at merge alongside R-1. No new capability against a run the
caller did not start is introduced by this ADR (C8).

**A false-positive lease expiry can re-emit a terminal event — residual risk
R-3.** A live-but-silent run reconciled after 30 minutes gets a `pipeline_done`
it will later contradict. The event is keyed by run id, so the platform side is
idempotent and last-writer-wins on outcome; the local snapshot removal is
identity-safe; and the run re-adopts and re-persists on its next message.
Bounded by the window and by `abandonRun` carrying the load in the common case.

**`closedRuns` does not survive a server restart when the snapshot was removed
cleanly — residual risk R-4.** After such a restart, a genuinely-closed run's
late call adopts rather than being refused, re-creates a snapshot, and is closed
again by the next reconcile — one spurious `pipeline_done`. It **cannot**
double-book: the adopted runtime starts empty (rehydration from a terminal
snapshot is refused, and there is no snapshot when removal succeeded), so
#313's richer-upgrade-only rule drops its skeleton record, and Decision 9's
corpus dedup — which reads the durable corpus, not memory — drops the duplicate
outcome. Only the noise remains.

**The scheduler registry is still issue-keyed — residual risk R-5.** Decision 11
guards its one externally-reachable write site on identity rather than re-keying
it. That is sound because it has a single in-process writer per issue, but it is
a compensating check, which this ADR elsewhere calls the signature of a wrong
key. Re-keying `Scheduler.activeRuntimes` on `RunID` is filed as a follow-up.

**One deleted sentence is worth naming.** `server.go:2875-2880` currently reads:
_"Idempotency is inherited, not enforced here … the runtime is deleted at the end
of this handler, so a repeated `notifyComplete` finds none and records nothing."_
That sentence is the only thing standing between the calibration corpus and
silent doubling, and it is an emergent property of a bare-issue delete. This ADR
deletes the property and replaces it with an explicit, tested one (Decision 9).
Any future change that keeps a runtime resolvable past completion must supply
its own replacement, not assume this one.

---

## Implementation tracking

Tracked under issue #370. Suggested sequencing, each step independently
mergeable and independently testable. **Step 0 is first because two consumers in
step 5 are inert without it.**

0. `NIGHTGAUGE_RUN_ID` exporters: `RunOptions.RunID` through the eight
   `internal/execution/adapters/*.go` stage env builders, and `SkillRunner`'s
   child env. No consumer changes yet — the variable simply starts existing.
1. `RunID` as a `NewRuntimeState` constructor argument, immutable; `Persist`
   refuses an empty identity; the shared identity regex; the new filename and
   the `LoadPersistedState` / `FindPersistedStatesForIssue` split; the durable
   `terminal` / `abandoned` snapshot fields; `PersistExisting`; the gate CLI's
   create-on-miss deleted; `current-run.json` gains `run_id` (Go only).
2. `runEntry` wrapper, the re-keyed registry, the two verb classes, adoption
   with terminal-refusal, the terminal latch, compare-and-delete, the `LastSeen`
   index ranking, `Scheduler.IsRunLive` / `LookupRunByID` /
   `RecordPhaseStartForRun`, and the reconciler changes (both registries,
   removal split from emission) — with the Go regression suite.
3. The wire change: `runId` on all six methods, `repo` + `issueNumber` on the
   two administrative verbs, `pipeline.abandonRun`, `ProtocolVersion` 2 with the
   specified hard-fail, codegen regen.
4. Extension unification: the generation becomes the run id, `remoteRunId`
   rename, `beginRun`/`endRun` on `PipelineStateService`, the four raw IPC call
   sites given identities, `stateChanged` / phase-event routing by run id with
   the empty-id fallback, `abandonRun` in the force-clear funnel (and the rule
   that the funnel never calls `notifyComplete`), the five `catch` logs, fence
   re-pin.
5. `learning.Recorder` idempotency key; #305's multi-run corroboration rule;
   #375's run-id payload field and trace ref (key unchanged); `--run-id` on the
   gate CLI plus explicit `--run-id` at `HeadlessOrchestrator`'s four spawn
   sites; `NIGHTGAUGE_RUN_ID` in the SDK trace recorder with the
   `runtime-${issue}.json` read deleted.
6. `CliPipelineReconciliationService` reads `run_id` from the sidecar;
   pause-restore migration (parsed id, per-issue QuickPick, id threaded into
   resume); the TS stub sweep's filter narrowed to the new scheme.
7. The one-shot Go legacy sweep, plus the follow-up issue for its own removal.
8. Follow-ups filed at merge: R-1 (single authoritative snapshot writer), R-2
   (IPC socket authentication, with the ADR 015 §N amendment landed in this PR),
   R-5 (re-key `Scheduler.activeRuntimes`).
