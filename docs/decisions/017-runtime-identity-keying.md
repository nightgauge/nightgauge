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
3. **adopted on sight — exactly once** by the run-progress and terminal verbs
   when the server has never seen it and it is not provably closed, so a
   restarted server, a lost `initialized`, or a slow socket never locks a live
   run out of its own records;
4. **resolved but never invented** by the administrative verbs (`setPaused`,
   `abandonRun`), which may not manufacture a target;
5. **refused for run content only when the run is provably terminal**, where
   "terminal" is a **durable** fact travelling on the run's own state, not an
   in-memory flag on a registry entry;
6. **compared at every destructive write, under the locks that resolved it**,
   with the snapshot filename carrying the identity so the path _is_ the check,
   and with `Persist` itself latch-aware so no in-flight write can undo it.

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
review forced, and it is what keeps the "a live run's own progress is never
rejected" invariant true (see
[Revision history](#revision-history--what-design-review-changed)).

**And it does not let the reconciler close what it cannot prove dead.** Every
destructive or terminal-emitting path in this ADR is biased in one direction: a
false "this run is alive" costs a deferred sweep, bounded by the 14-day cap; a
false "this run is dead" costs a live run its entire record. The **liveness
ladder** (7.2) and the **deferred startup reconcile** (7.3) exist because the
first draft of this ADR got that bias backwards at the one call site that fires
on every Go-backend auto-restart — and the ladder's **per-population coverage
table** exists because the second draft then claimed evidence for a population
that does not produce it. An arm only counts for the runs that write the signal
it reads (C18).

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
#370; F21–F25 were found by design review of this ADR's first draft, F26–F31 by
design review of its second, and F32–F36 by design review of its third, and all
of them are closed by the decisions those reviews produced. F32–F36 are of a
distinct kind and the ADR says so rather than burying them in a list: each one
was **introduced by a fix in the previous revision**, and each was invisible in
prose because the prose described the intent of the fix rather than what the
cited code does.

| ID  | Failure                                                                                                                                                                                                                                                                                                                                                                  | Where it lives today                                                                           |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------- |
| F1  | **Identity laundering.** A producer that supplies no identity acquires a runtime it did not start, because the server mints one on miss under a key the producer does not own.                                                                                                                                                                                           | `server.go:2466-2476`                                                                          |
| F2  | **Cross-run cost/token booking.** A zombie's transitions accumulate onto a live successor's `RuntimeState`.                                                                                                                                                                                                                                                              | issue-keyed `activeRuntimes`                                                                   |
| F3  | **Snapshot destruction by a zombie.** The `failed`-transition `os.Remove` deletes a live successor's crash snapshot.                                                                                                                                                                                                                                                     | `server.go:2592`                                                                               |
| F4  | **Wrong authoritative record.** A zombie's `notifyComplete` writes the V2 record and the calibration row under the successor's `RunID`.                                                                                                                                                                                                                                  | `server.go` notifyComplete                                                                     |
| F5  | **Registry eviction of a successor.** The issue-keyed delete after seconds of unlocked I/O removes an entry installed during that window.                                                                                                                                                                                                                                | `server.go:2670`, `:2957-2966`                                                                 |
| F6  | **History-record collision.** Two runs of one issue sharing a `RunID` produce one ledger key; one record is dropped or overwritten, and `repair-history` cannot recover it after the fact.                                                                                                                                                                               | #313/#316 `run:<id>` key                                                                       |
| F7  | **Fallback-key collision.** The `issue:{N}\|{whole UTC second}` fallback makes two dispatches started in the same second one run to the ledger.                                                                                                                                                                                                                          | history fallback key                                                                           |
| F8  | **Cross-repo collision.** Repo A #42 and repo B #42 share a registry key, a snapshot filename and a record key, with no force-clear involved.                                                                                                                                                                                                                            | issue-keyed everything                                                                         |
| F9  | **`setPaused` stub.** Mints a runtime with no repo and no `RunID`; can pause a **live successor**; and because `collectOrphanedRuns` skips paused snapshots, pins the issue against #44 forever.                                                                                                                                                                         | `server.go:2358`                                                                               |
| F10 | **Phase/progress cross-run writes.** `notifyPhaseTransition` and `notifyStageProgress` resolve by issue, so a zombie's phases land in a live run's `PhaseHistory` and authoritative record.                                                                                                                                                                              | `server.go:2634, 2663, 2986`                                                                   |
| F11 | **Reconcile dead at the root switch.** A force-cleared run's own entry makes `skipIssue` true for the life of the server, so `workspace.setRoot` never reconciles exactly the runs that need it.                                                                                                                                                                         | `pipeline_orphan_reconcile.go:138-144`                                                         |
| F12 | **Confidently-wrong issue-addressed reads.** `getState` serves a dead run's snapshot for that issue indefinitely.                                                                                                                                                                                                                                                        | `getState` resolution                                                                          |
| F13 | **Shared-namespace last-write-wins.** Five writers in three processes marshal whole snapshots into one filename with no merge.                                                                                                                                                                                                                                           | `RuntimeState.Persist`                                                                         |
| F14 | **The force-clear card has no run id.** `runTraceRef` resolves to nothing, so the card cannot reach the ADR-013 trace of the run it describes.                                                                                                                                                                                                                           | `attention_wiring.go` `BuildAbandonedDispatch`                                                 |
| F15 | **SDK trace recorder guesses the run id** from `runtime-{issue}.json`, which is wrong after any adoption or steal.                                                                                                                                                                                                                                                       | `traceRecorder.ts:288`                                                                         |
| F16 | **Permanent lockout from one lost message** (the refuted latch design): a live, successful run records nothing at all.                                                                                                                                                                                                                                                   | Alternative A                                                                                  |
| F17 | **Token-less-producer steal.** A guard gated on `token != ""` is bypassed by every producer that has no token, which then steals a live run.                                                                                                                                                                                                                             | refuted round-2 design                                                                         |
| F18 | **Extension-side terminal double-book.** A force-cleared dispatch settles late and books its terminal outcome twice.                                                                                                                                                                                                                                                     | #307 PROBE-X / PROBE-Y                                                                         |
| F19 | **Zombie-driven `stateChanged` applied to a successor's slot UI.**                                                                                                                                                                                                                                                                                                       | `PipelineSlotsTracker`, `PipelineStateService`                                                 |
| F20 | **Two locks, one comparator.** `RunID` is written under `Server.runtimesMu` and read under `RuntimeState.mu` in `snapshotLocked`/`Persist` — a torn read a sequential `-race` test would not catch.                                                                                                                                                                      | `server.go` / `runtime_state.go`                                                               |
| F21 | **Live-run reconciliation across registries.** The reconcile skip consults only the IPC registry, so every `workspace.setRoot` emits a terminal `pipeline_done` for each live **Go-scheduler** run and deletes its crash snapshot.                                                                                                                                       | `pipeline_orphan_reconcile.go:138-144` vs `scheduler.go:4043, 4460, 6271`                      |
| F22 | **Cross-process resurrection.** `AppendStageGateResultToDisk`'s create-on-miss recreates a snapshot the terminal claim just removed, from a process with no registry and no latch.                                                                                                                                                                                       | `runtime_state.go:784-791`                                                                     |
| F23 | **Shared-holder minting.** Three producers stamp an identity onto one singleton `PipelineStateService`, so the last minter relabels a live run's remaining traffic — F1 reproduced through the client.                                                                                                                                                                   | `retryFailedIssue.ts:94-123`, `bootstrap/services.ts`                                          |
| F24 | **Local-only inertness.** Reconciliation and retention are gated on `analyticsSvc != nil`, so a workspace with no platform account collects nothing and `.nightgauge/pipeline/` grows without bound.                                                                                                                                                                     | `pipeline_orphan_reconcile.go` first line                                                      |
| F25 | **CLI-run discovery by issue filename.** `CliPipelineReconciliationService` reads `runtime-{issue}.json`, and its `current-run.json` sidecar carries no run id — so a filename change makes it silently blind.                                                                                                                                                           | `CliPipelineReconciliationService.ts:137`                                                      |
| F26 | **Startup reconcile closes live runs on every backend auto-restart.** The client restarts the Go binary while the extension host and its in-flight runs survive; the fresh server's registries are empty, so the startup sweep terminates and deletes them.                                                                                                              | `IpcClientBase.ts:1456-1485` vs `server.go:654`                                                |
| F27 | **In-flight `Persist` resurrects a removed snapshot.** A handler that resolved a runtime, unlocked, then persists re-creates the file the terminal claim just removed — non-terminal and rehydratable, from inside the same process.                                                                                                                                     | `server.go:2585-2597` vs the terminal claim                                                    |
| F28 | **Pause-restore replays an identity it did not mint.** The restore prompt reads a paused snapshot on every activation and resumes under its id with nothing consuming the file, so two extension hosts can drive one run id.                                                                                                                                             | `bootstrap/services.ts:1168-1220`                                                              |
| F29 | **Terminal/administrative verbs served from the scheduler registry.** A registry with no `runEntry`, no latch, no lease and no compare-and-delete target degrades the terminal claim to "snapshot and write" — two authoritative writers per `run:<id>`.                                                                                                                 | Decision 11's resolution order, first draft                                                    |
| F30 | **Concurrent adoption of one id.** Every request runs in its own goroutine; two misses for one unknown id both load from disk and both construct a `*RuntimeState`, and the loser's accumulated stages vanish under same-file last-write-wins.                                                                                                                           | `server.go:678` + adoption                                                                     |
| F31 | **`attention.raise`'s run id is guessed from an unrelated file.** `readCurrentRunId` reads the scheduler/CLI `run-state.json`, so on the extension path — the entire population of run-scoped raises — the id is empty or foreign. F15's class, new site.                                                                                                                | `HeadlessOrchestrator.ts:1525-1544`                                                            |
| F32 | **Ladder arms that do not exist for the population they were added for.** Arm 3 reads a `PID` no extension-path run writes; arm 4 is refreshed only at stage boundaries, not by progress. A live extension run silent through a long CI wait fails every arm at grace expiry — F26 narrowed by one condition, not closed.                                                | `manager.go:301` is `SetProcess`' only production caller; `server.go:2623-2673` never persists |
| F33 | **Administrative disk read-modify-write.** With no registry entry — the modal case for a force-clear — `abandonRun`/`setPaused` load, mutate and rewrite a whole snapshot from an object that is not the live `*RuntimeState`, so a concurrent adoption's `Persist` erases the marker, or the administrative write rolls back stages the run booked meanwhile.           | 7.1's no-entry path vs. `server.go:678`                                                        |
| F34 | **The claim artifact is born stale.** `rename(2)` preserves the inode's mtime, so `resuming-*` inherits the age of the pause. An mtime-based release fires under a **live** claimant: it renames stale paused content back over the running run's canonical snapshot and re-advertises `paused: true` for a live id — F28 reached through the rule added to prevent F28. | Decision 9's rename vs. 7.4's age row                                                          |
| F35 | **Plan order strands an emitter population.** The hard `run_id_required` flip was sequenced before the extension side had any identity to send, so every extension-path run dispatched between two merges would write zero records behind five bare catches — F16's shape, inside the migration.                                                                         | old plan steps 3 → 4                                                                           |
| F36 | **The claim sequence self-deadlocks.** It names exported methods that take `rs.mu` themselves, from inside a hold of `rs.mu` nested in the server-global `runtimesMu` — wedging every handler, the index scan and the reconciler permanently.                                                                                                                            | `runtime_state.go:31, 710, 738, 993` vs. Decision 5's step 1                                   |

## Constraints

| ID  | Constraint                                                                                                                                                                                                                                                                                                            |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| C1  | #44's existing reconciler skips must be preserved: **paused** snapshots (they power #2008) and **identity-less** snapshots are never reconciled into a bogus terminal event.                                                                                                                                          |
| C2  | #304's outcome recording must stay **at most once per run**.                                                                                                                                                                                                                                                          |
| C3  | #305's corroboration rules must hold: exact repo match on both arms, and spend summed only over stages the daemon watched **begin**.                                                                                                                                                                                  |
| C4  | #313/#316's history identity and **single authoritative writer** must hold: one serialized idempotent append path, first-write-wins, richer-upgrade-only, skeletons never overwrite.                                                                                                                                  |
| C5  | #2008 pause-restore must keep working, including across an IPC-server restart.                                                                                                                                                                                                                                        |
| C6  | The snapshot must stay: (a) discoverable by directory scan with no index, (b) parseable by a process with no registry, (c) atomically written, (d) rooted in the run's **target** repo (#215/#307), and (e) never written for an unattributed runtime (the `repo != ""` gate).                                        |
| C7  | A successor entry installed during `notifyComplete`'s unlocked window must survive that window.                                                                                                                                                                                                                       |
| C8  | The IPC socket's trust model (ADR 015 §N) may not be **widened**: no new verb may give an unauthenticated caller a capability against a run it did not start that it does not already have.                                                                                                                           |
| C9  | **Fail-open.** A refused or lost IPC call must never kill a live run; the run continues on its local cache. The two new client-side run-id filters are the migration surface for this.                                                                                                                                |
| C10 | #307's guarantees: permanent tombstones, `await`-free check-and-claim boundaries, `stillOwnsIssue` reading `slots ∪ reservedSlots`, and the sha256-pinned terminal-parity fences.                                                                                                                                     |
| C11 | The Go scheduler's `map[int]*RuntimeState` registry is **out of re-keying scope** for this ADR — but it is not out of **correctness** scope (see F21, and Decision 11).                                                                                                                                               |
| C12 | Third-process seams (`nightgauge gate verify --record`, the SDK `TraceRecorder`) must get the identity **threaded**, never guessed.                                                                                                                                                                                   |
| C13 | **Reconciliation may only close a run it can prove dead.** An empty registry is not evidence of death — the Go backend is auto-restarted under a surviving extension host (F26). Liveness evidence is a registry entry, a live recorded PID, or a fresh lease; its absence is decisive only outside the grace window. |
| C14 | **Adoption is exactly-once per identity within a process.** It is the ordinary post-restart path, it performs I/O, and every request runs in its own goroutine, so two concurrent adoptions of one id may never produce two `*RuntimeState` objects (F30).                                                            |
| C15 | **Claiming a paused snapshot must be atomic-exclusive.** Two extension hosts scanning one pipeline dir may both see a paused snapshot; at most one may resume it (F28).                                                                                                                                               |
| C16 | **Every symbol a critical section names must have a caller-holds-the-lock form.** No exported method that acquires lock L may be called from inside a hold of L, and a sequence this ADR declares normative names the `…Locked` variant it actually calls. `sync.Mutex` is not reentrant (F36).                       |
| C17 | **An age test measures the age of the event it names.** A rule that says "older than X" reads a timestamp the actor stamped at the moment the rule is about, never one an unrelated operation happened to leave behind. `rename(2)` preserving mtime is the concrete instance (F34).                                  |
| C18 | **Liveness evidence must exist for the population it is claimed to cover.** A ladder arm counts only for the runs that actually write the signal it reads; an arm no run in a population produces is not a fallback for that population, it is a comment (F32).                                                       |

---

## Decisions

### 1. The run identity is a client-minted `run_id`, and it _is_ `RuntimeState.RunID`

There is **one** identity, not two bound together.

- **Format:** canonical lowercase **UUIDv7** — a time-ordered UUID. Uniqueness
  comes from the random tail; the leading millisecond timestamp makes ids sort
  by mint time in logs, ledger files and trace filenames. **No correctness rule
  decodes the RUN identity.** UUIDv7 is chosen for readability and for a stable
  order over snapshots found on disk by a process with no registry; it is not a
  covert protocol. `runstate.NewRunID()` already produces exactly this value on
  the Go side (`scheduler.go:2773`), so both minters agree by construction.

  **One value in this ADR _is_ decoded, and it is not this one.** Decision 9's
  pause-restore **claim token** is a separate UUIDv7, minted by the claimant at
  claim time, whose embedded millisecond timestamp is parsed by the reconciler
  to age the claim (C17). The two rules do not contradict each other because the
  values answer different questions: a run identity must survive being carried
  across processes, restarts and days, so nothing may depend on _when_ it was
  minted; a claim token exists only to say _when this claim was taken_, and it is
  minted, read and discarded inside one directory. Decision 9 states the parse,
  the skew tolerance and the fail-safe direction explicitly, so "decoded" never
  becomes "decoded somewhere, by someone, for something".

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

### 3. Every `pipeline.*` call carries the identity, and the verb class decides everything

Four classes, and the class is resolved **before** any registry is consulted:
**run-progress** (a caller describing its own run), **terminal** (the claim),
**administrative** (a caller asserting something _about_ a run), and **lookup**.
The first draft had two, and folded the terminal claim into run-progress —
which is how a terminal verb could be served from a registry that cannot latch
it (F29).

| Method                           | `runId`      | Class          | Notes                                                                            |
| -------------------------------- | ------------ | -------------- | -------------------------------------------------------------------------------- |
| `pipeline.notifyStageTransition` | **required** | run-progress   | `initialized` loses all special status — it is one transition among many         |
| `pipeline.notifyPhaseTransition` | **required** | run-progress   |                                                                                  |
| `pipeline.notifyStageProgress`   | **required** | run-progress   |                                                                                  |
| `pipeline.notifyComplete`        | **required** | terminal       | Decision 5                                                                       |
| `pipeline.setPaused`             | **required** | administrative | gains `repo` + `issueNumber`; **resolves, never invents** (Decision 7)           |
| `pipeline.abandonRun`            | **required** | administrative | new verb; gains `repo` + `issueNumber`; **resolves, never invents** (Decision 7) |
| `pipeline.getState`              | _optional_   | lookup         | not a run message; issue-addressed reads stay supported (Decision 6)             |

There is no separate handshake message and no state machine over message types.
**The identity on the current message is the whole handshake.** Losing any one
message costs exactly that message's content and nothing else — which is the
property the latch-on-`initialized` design failed to have.

The server's rule is six lines, and **the verb class is decided on line 3,
before any registry is consulted**:

```
1  if runId == ""                   → error  run_id_required   (an old client, or a producer that has none)
2  if !matchesIdentityRegex(runId)  → error  run_id_invalid    (Decision 1 — checked BEFORE any use)
3  class := classOf(method)         → run-progress | terminal | administrative | lookup
4  if closedRuns.has(runId)         → error  run_closed        (fast path; the DURABLE authority is Decision 4's
                                                                terminal marker — see "closedRuns is a cache")
5  resolve(runId) under the CLASS's registry policy              (the table below; Decision 11)
6  unresolved → run-progress/terminal: adopt, from the snapshot or empty  (Decision 4)
               administrative:        adopt ONLY from an existing snapshot, and
                                      without stamping LastSeen; nothing on disk
                                      → error run_not_found. NEVER adopt-empty.
```

**Line 3 moved, and that is a design change, not a re-ordering of prose.** The
first draft branched by class only after step 3 of Decision 11's resolution
order — a step that never reached the class check when the scheduler registry
happened to hold the id. `notifyComplete` carrying a live scheduler run's id was
therefore **served** from a registry that has no `runEntry`, no terminal latch,
no `LastSeen` and no compare-and-delete target: the terminal claim degraded to
"snapshot and write", writing a V2 record and a learning-corpus row for a run
whose scheduler path writes its own through `OnPipelineComplete` — two
authoritative records under one `run:<id>`, breaking exactly the C4 rule this
ADR promises to preserve (F29). `abandonRun` and `setPaused` reached it the same
way: a scheduler entry _is_ a resolution, so an administrative verb could emit a
terminal `pipeline_done` for a live scheduler run and stamp `abandoned` into its
snapshot. "The scheduler path never calls `notifyComplete`" is a statement about
today's TypeScript, not an invariant of an unauthenticated socket (R-2).

**Per verb class, per registry — the complete disposition:**

| Resolves in →                                  | IPC registry (`activeRuntimes`) | Scheduler registry (`map[int]`, C11)                                 | On-disk snapshot                                                                                               | Nothing                      |
| ---------------------------------------------- | ------------------------------- | -------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- | ---------------------------- |
| **run-progress** (transition, phase, progress) | serve; refresh `LastSeen`       | **serve read-through** onto the scheduler's own runtime; never adopt | **adopt + rehydrate** (refuse a `terminal` snapshot)                                                           | **adopt empty**              |
| **terminal** (`notifyComplete`)                | claim (Decision 5)              | **REFUSE `run_wrong_owner`** — no latch, no lease, no delete target  | **adopt + rehydrate**, then claim                                                                              | **adopt empty**, then claim  |
| **administrative** (`setPaused`, `abandonRun`) | resolve; corroborate repo+issue | **REFUSE `run_wrong_owner`**                                         | **adopt through the same singleflight** — installs the rehydrated entry, `LastSeen` **untouched** (Decision 4) | `run_not_found` / `no_run`   |
| **lookup** (`getState`)                        | read                            | read                                                                 | read                                                                                                           | empty response, not an error |

`run_wrong_owner` is a new, distinct error code, and it is a **refusal to
degrade** rather than a capability check: the scheduler owns that run's terminal
bookkeeping through `OnPipelineComplete`, and the only correct answer to "close
this scheduler run over IPC" is "that is not this socket's run to close".
Refusing it costs nothing that C9 protects — the scheduler still books the run's
record on its own path — and it is logged loudly in Go with the resolved
registry named, because on today's tree it should be unreachable and a
non-zero count is a real signal.

The corollary the ADR states rather than leaves implicit: **there is no IPC
route to abandon or pause a Go-scheduler run.** The scheduler's own cancellation
path is the route, and unifying the two is exactly what R-5's re-key of
`Scheduler.activeRuntimes` buys. Inventing a second one here would mean
duplicating the latch, the lease and the compare-and-delete into a registry this
ADR has explicitly scoped out (C11) — the "two registries with different
invariants" shape Alternative B was refuted for.

The two id errors are distinct on purpose: `run_id_required` is what a
version-skewed client produces and is the signal the Migration hard-fail exists
to make impossible, while `run_id_invalid` is what a malformed or hostile value
produces and is a security check, not a compatibility one.

**Why the blanket "unknown → adopt" rule is wrong for administrative verbs —
and what exactly is forbidden.** The forbidden operation is **adopt-empty**:
manufacturing a runtime for an id no evidence exists for. **Adopting a snapshot
that is already on disk is not that**, and the second revision's "resolve
read-only; never create an entry" conflated the two, which cost it F33. The
distinction the rest of this ADR uses:

| Administrative verb meets…   | Rule                                                                                                 |
| ---------------------------- | ---------------------------------------------------------------------------------------------------- |
| a live registry entry        | serve it; corroborate `repo` + `issueNumber`; **do not** stamp `LastSeen`                            |
| no entry, a snapshot on disk | adopt it through Decision 4's singleflight — the snapshot **is** the evidence; `LastSeen` stays zero |
| no entry, no snapshot        | `run_not_found` (`setPaused`) / `no_run` (`abandonRun`). Nothing is created, nothing is written      |
| a snapshot marked `terminal` | `run_closed`, via the same refusal adoption already applies                                          |

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
administrative class exists to make that unrepresentable — and it still does,
because "never adopt-empty" is the whole of what Alternative G's refutation
needs. Adopting an **existing** snapshot cannot invent a target, cannot produce
an `IssueNumber: 0` event (the snapshot carries both fields, and 7.1
corroborates them), and cannot lock out a dispatch that never reached Go
(`bookForceClearedReservation`'s case has no snapshot, so it lands on the
`no_run` row above).

**Why the administrative path must adopt rather than read-modify-write the file
(F33).** 7.1 names the no-entry case as the **modal** one: the force-clear fires
because something is wedged, and a restarted IPC binary leaves an empty registry.
On that path a raw load-mutate-write operates on an object that is **not** the
live `*RuntimeState`, so `Persist`'s `rs.mu` — the latch Decision 5 added — has
nothing to serialise against, and every request has its own goroutine
(`server.go:678`). Two concrete losses, both inside one process:

- `abandonRun` loads `runtime-42-R.json`, spends the width of a
  `buildPipelineDoneEvent` + analytics push emitting, then writes its stamped
  copy. Meanwhile R unwedges: adoption loads the same **pre-stamp** file and
  installs a live object with `abandoned: false`, whose next `Persist` erases the
  marker. The reconciler then sees an ordinary orphan and emits a **second**
  `pipeline_done` while removing a streaming run's snapshot — Decision 7.1's
  entire "the marker survives the run that keeps overwriting its own snapshot"
  argument, defeated by the one path it does not cover.
- `setPaused` is worse than marker loss: it writes a snapshot **read at T** over
  a file the live run wrote at T+ε, rolling back on disk every stage booked in
  between, and adoption rehydrates the older state after any restart.

Routing the administrative path through the singleflight installs the entry, so
from that instant `rs.mu` serialises the administrative write against every
`Persist` the live run makes, and there is exactly one `*RuntimeState` for the id
to disagree with itself about. **This also repairs a claim in Consequences**:
R-1's residual really is cross-process again, because the same-process,
two-goroutine variant this ADR would otherwise have introduced is now gone.

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
invariant that makes this safe: **a live run's own progress can never be
rejected**, and it holds because `run_closed` is set by exactly one thing — a
run's own `notifyComplete` terminal claim — while `run_not_found` and
`run_wrong_owner` are reachable only for verbs that carry **no run content**:

| Error             | Reachable on                                         | Can it discard a live run's stage/cost/phase data?                              |
| ----------------- | ---------------------------------------------------- | ------------------------------------------------------------------------------- |
| `run_id_required` | any verb, from a producer with no id                 | No — unreachable once **both** emitter populations assert (plan steps 0b and 3) |
| `run_id_invalid`  | any verb, malformed value                            | No — a live run's ids are minted, never typed                                   |
| `run_closed`      | any verb, after that run's own claim                 | No — its content is already booked                                              |
| `run_not_found`   | administrative verbs only                            | No — they carry no run content                                                  |
| `run_wrong_owner` | terminal + administrative verbs, scheduler-owned run | No — the scheduler books that run itself                                        |

The four run-progress verbs — the only ones that carry a live run's data — can
return **none** of the last three. The first draft broke this invariant by
having `abandonRun` write to `closedRuns` (Decision 7 is the correction) and
nearly broke it again by letting `notifyComplete` be served from the scheduler
registry, which would have written a **second** authoritative record rather than
rejecting anything — the inverse failure, and worse.

### 4. Adoption is the answer to "unknown identity" for run-progress verbs, and it rehydrates

When the server sees a `runId` it has no entry for, that is not in `closedRuns`,
and that arrives on a **run-progress** or **terminal** verb, it **creates the
entry and serves the call**. This is safe in a way that mint-on-miss never was,
because the identity came from the caller: an adopting zombie re-creates **its
own** run under **its own** key, where every write it makes lands on its own
record and touches no other run.

Adoption **rehydrates from disk when it can.** The snapshot path is fully
derivable from the call's own parameters (`repo` → repo root, plus `issue` and
`runId`), so adoption reads `runtime-{issue}-{runId}.json` and restores the run's
accumulated history rather than starting empty. This turns the ordinary case —
an IPC server restarted mid-run — from lossy into very nearly lossless.

#### Adoption is exactly-once per identity: a per-id singleflight (C14)

Adoption is a **check → disk read → insert**, and `server.go:678` fans every
request into its own goroutine. The post-restart case this decision names as the
_ordinary_ one is therefore concurrent by default: the extension's
`notifyStageProgress` (≥5s cadence, `HeadlessOrchestrator.ts:13323`) and its next
`notifyStageTransition` arrive for the same unknown id within milliseconds. If
both miss, both load, and both construct a `*RuntimeState`, one wins
`activeRuntimes[runId]` and the loser's handler keeps mutating an orphaned
object whose `Persist` targets the **same filename** — same-run field loss
inside one process, which would falsify R-1's claim that re-keying narrows F13
to a cross-process residual (F30).

The resolution is a **singleflight keyed by `runId`**, with the disk read
outside `runtimesMu` and both the check and the insert inside it:

```
resolveOrAdopt(runId, class, params):                 // caller holds NEITHER lock
  LOCK runtimesMu
    if e := reg[runId];      e != nil { touch(e, class); UNLOCK; return e }
    if f := adopting[runId]; f != nil { UNLOCK; <-f.done; goto SETTLE }     // wait; do not load
    f := newFlight(); adopting[runId] = f                                   // this goroutine owns the load
  UNLOCK

  f.rs, f.terminalOnDisk, f.err = loadSnapshot(stateDir(params.repo), runId) // I/O, unlocked

  LOCK runtimesMu
    delete(adopting, runId)
    if f.terminalOnDisk            { closedRuns.add(runId) }
    else if f.rs != nil            { reg[runId] = newEntry(f.rs); touch(reg[runId], class) }
  UNLOCK
  close(f.done)                                                             // ALWAYS, via defer

SETTLE:                                               // every caller of this id runs this
  if f.err != nil                { return nil, f.err }
  if f.terminalOnDisk            { return nil, run_closed }
  LOCK runtimesMu
    if e := reg[runId]; e != nil  { touch(e, class); UNLOCK; return e }      // the loaded entry, or a peer's empty one
    if class == administrative    { UNLOCK; return nil, run_not_found }      // NEVER adopt-empty
    e := newEntry(empty); reg[runId] = e; touch(e, class)                    // no I/O: lock-guarded is enough
  UNLOCK
  return e

touch(e, class):  if class != administrative { e.LastSeen = now }
```

Exactly one goroutine per id performs the **disk read**, and every other caller
for that id waits and settles against the same result, so two `*RuntimeState`
objects for one identity cannot exist. The flight is closed from a `defer`, so a
load that panics (the handler's `recover` at `server.go:680` would otherwise
swallow it) never strands a waiter.

Three properties of this shape are load-bearing and are stated rather than left
to be inferred:

- **The flight covers the I/O, not the decision.** An **empty** adoption does no
  I/O, so it needs no flight — a check-and-insert under `runtimesMu` is already
  exactly-once. That is why the class rule can be applied per caller in `SETTLE`
  rather than baked into the flight: an administrative call and a run-progress
  call may race for one unknown id, and each must get its own class's answer from
  the one shared load.
- **`resolveOrAdopt` is called holding neither lock, and it takes `runtimesMu`
  three times in the worst case.** It is therefore **not** something a critical
  section may contain — Decision 5's claim sequence calls it as step 0, before
  its own critical section opens (C16).
- **`LastSeen` is stamped by run-progress and terminal calls only.** An
  administrative resolution installs an entry whose `LastSeen` is the zero time.

**A snapshot that exists but cannot be read is a refusal, not an empty
adoption.** `f.err` covers a real I/O failure — a permissions change, a partial
write, a corrupt JSON body — as distinct from "no such file", which is the
ordinary `f.rs == nil` path. Adopting empty there would install a runtime that
believes the run has no history and would let its next `Persist` overwrite the
unreadable-but-present file with a thinner one; refusing costs that one call's
content (C9's fail-open keeps the run itself alive on its local cache) and the
next message retries the load. The bias matches the rest of the ADR: never
destroy a record to serve a call.

#### Administrative adoption installs an entry; it does not vouch for the run

This is the rule that keeps F33's fix from re-creating F9's third defect (a
`setPaused` stub pinning an issue against #44 forever). An entry installed by an
administrative resolution is an ordinary entry in every respect but one:

| Property                                                        | Administratively-adopted entry                                                                                                           |
| --------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| serialises writes through `rs.mu`                               | **yes** — this is the entire point (F33)                                                                                                 |
| `LastSeen`                                                      | **zero** until real run traffic arrives                                                                                                  |
| ladder arm 1 (`entry && !terminal && fresh LastSeen`)           | **false** — a zero `LastSeen` is never "within `LIVENESS_WINDOW`"                                                                        |
| counts as re-assertion in the startup grace re-evaluation (7.3) | **no** — an administrative call is a statement about a run, never from it                                                                |
| issue-index "current" ranking (Decision 6)                      | ranks last by `LastSeen`; it can only be "current" when it is the sole entry, where it is strictly better than the disk read it replaced |
| eviction                                                        | the same as any other entry — terminal claim, or the lease                                                                               |

The one-sentence form: **an administrative verb may install the run's state, and
may never make the run look alive.** Without that split, `abandonRun` — whose
population is by definition runs the operator has given up on — would hand every
one of them a fresh lease and suppress the reconciliation that #44 exists to do.

**The I/O-under-lock tradeoff, stated rather than assumed.** The obvious
alternative is one check-load-insert critical section holding `runtimesMu`
across the read. It is correct, and it is rejected: `runtimesMu` is a
**server-global** lock also taken by Decision 6's derived index scan and by the
reconciler, so holding it across a synchronous file read serialises every
_other_ run's handlers behind one run's disk latency. Adoption is the ordinary
path after a restart, so the pathological case is not exotic — N runs
re-asserting at once would serialise N reads. The singleflight keeps the lock's
hold time at O(map operations) while still giving exactly-once semantics per id;
the price is one more piece of state (`adopting map[string]*flight`) and the
discipline that every exit path closes the flight. Recorded as Alternative K.

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

#### `closedRuns` is a cache; the durable terminal marker is the authority

The first draft stated this three incompatible ways — a "durable on-disk
backing" in Consequences, a snapshot field plus opportunistic repopulation here,
and R-4's flat "does not survive a server restart". A reader implementing from
one built a durable set and a reader implementing from another built a volatile
one, and the two give different answers to a late duplicate `notifyComplete`.
**One story, and every rule that leans on "run_closed" cites it:**

- **`closedRuns` is an in-memory FIFO ring, capped at 1024 run ids.** It is
  never persisted and never loaded. Ids are inserted once and never re-touched,
  so an LRU would degenerate to a FIFO anyway; the cap is stated as a FIFO to
  keep the eviction order decidable by a reader. 1024 × a 36-byte id bounds it
  at ~40 KB and covers weeks of runs at any plausible dispatch rate.
- **The authority is the durable `terminal` marker** stamped into
  `runtime-{issue}-{runId}.json` before removal (fix #1 above). While the file
  exists, "closed" is decidable by any process, across any restart. Adoption
  reading a `terminal` snapshot re-populates `closedRuns` — that is the ring's
  only refill path.
- **Once the file is gone, there is no durable authority, and that is
  deliberate.** The alternative — a persistent closed-run journal — is a second
  writer over run state whose retention has to be managed, revisiting the #316
  lesson ADR 015 Decision C already paid for. Recorded as Alternative L.

**The late-duplicate outcome, specified for every case** — this is what makes
"run_closed is durable" safe to _not_ claim:

| Late duplicate arrives when…                                                                      | Result                                                                                                                                                                                                                                           |
| ------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| id is in the ring                                                                                 | `run_closed` error. Nothing written.                                                                                                                                                                                                             |
| evicted from the ring, snapshot still on disk (removal failed / crashed between write and remove) | adoption reads `terminal: true` → refuses, re-populates the ring → `run_closed`. Nothing written.                                                                                                                                                |
| evicted **and** the snapshot was removed cleanly                                                  | adopt-empty → skeleton record **dropped** by C4's richer-upgrade-only rule → learning row **dropped** by Decision 9's corpus dedup (which reads the durable corpus) → **one spurious `pipeline_done`** and nothing else. This is R-4, unchanged. |
| server restarted (ring empty by construction)                                                     | identical to the two rows above, decided by whether the snapshot survived.                                                                                                                                                                       |

Eviction therefore cannot produce a wrong record, a doubled outcome or a
reopened run — it can only downgrade a cheap in-memory refusal into R-4's
one-event noise. Decision 3's line 4 says so inline, so a reader implementing
the five-line server rule cannot build the durable variant by mistake.

### 5. Terminal is a latch — in-process for the registry, durable on disk for everyone else

`notifyComplete` today unlocks at `server.go:2670`, does seconds of unlocked
I/O — ground-truth reconciliation, classification file reads, `BuildV2Record`,
`WriteV2Record`, the learning corpus append, the platform push — and then
deletes `activeRuntimes[<issue>]` and `os.Remove`s `runtime-{issue}.json` with
no re-read and no identity check.

The replacement is a **claim**, not a longer critical section. The sequence is
normative and complete — a mutation that is not in it is refused, and anything
added later must be added here or it does not happen. It is also **literally
implementable**: every symbol it names inside a hold is the `…Locked` variant,
because `RuntimeState.mu` is a plain `sync.Mutex` (`runtime_state.go:31`) and Go
mutexes are not reentrant (C16, F36).

**0. Resolve — holding neither lock.** `entry := resolveOrAdopt(runId, terminal,
p)` (Decision 4). This is where the class policy of Decision 3 applies (a
scheduler-owned id is `run_wrong_owner` and stops here), where the singleflight
may perform a disk read, and where a `terminal` snapshot yields `run_closed`.
**The singleflight is entered outside the `runtimesMu` hold and returns before
the critical section opens** — it takes and releases `runtimesMu` up to three
times itself, so containing it would deadlock exactly as F36 describes.

1. **Claim** — `runtimesMu`, with `rs.mu` nested inside it:

   - **1a. Re-check.** `e := reg[runId]`. If `e != entry`, our resolution went
     stale between step 0 and here (the entry was compare-and-deleted and
     possibly re-adopted); **retry once from step 0**, and on a second mismatch
     return `run_closed` and log loudly — the same assertion posture step 3's
     failed compare gets. If `e.terminal` is already set, another claim won:
     `run_closed`. **This re-check is what makes the unlocked step 0 safe**; it
     is the only thing between "adopt outside the lock" and the resolve-then-mutate
     window this decision exists to delete.
   - **`rs.mu.Lock()`** — lock order `runtimesMu` → `rs.mu`, never the reverse.
   - **1b. Replay the dispatcher's terminal payload.**
     `rs.recordExecutionPathLocked(stage, path)` for each `p.StageExecutionPaths`
     entry and `rs.recordStagePuntReasonLocked(stage, reason)` for each
     `p.StagePuntReasons` entry (`server.go:2690-2700`, #309). **These are the
     last mutations the run will ever accept**, and they run _inside_ the claim
     rather than before it.
   - **1c. Latch, both halves.** `rs.markTerminalLocked(outcome)` sets the
     persisted `RuntimeState.Terminal` / `TerminalAt` on the **live object**;
     `e.terminal = true` (registry admission) is set in the same
     `runtimesMu` hold.
   - **1d. Snapshot.** `snap := rs.snapshotLocked()` — the unexported form that
     already exists (`runtime_state.go:1000`) and that `Snapshot()` delegates to.
   - **`rs.mu.Unlock()`**, then **`runtimesMu.Unlock()`.**

2. **Work, unlocked,** against `snap` — never against the live pointer.
3. **Compare-and-delete, under `runtimesMu`:** delete `activeRuntimes[runId]`
   **only if the entry stored there is the same pointer that was claimed**, and
   record the id in `closedRuns`.
4. **Seal and remove, under `rs.mu`, as one operation** (`rt.SealAndRemove`,
   which takes `rs.mu` itself and holds **no** registry lock): write the
   terminal-stamped snapshot through `rs.persistLocked` into
   `runtime-{issue}-{runId}.json` under the directory derived from the **claimed
   snapshot's** `Repo` (Decision 4), `os.Remove` that same path, then set
   `rs.sealed = true`. **The path is the identity**, so this cannot take a
   successor's file even in principle — the strongest available form of an
   identity-checked destructive write. Write-then-remove is idempotent if the
   reconciler removed the file first: the write re-creates it as terminal and
   the remove takes it away again, net nothing.

#### The lock discipline, as a table

Three mutexes exist on this path and the ADR names all three, because two of
them were previously described only in passing and the third not at all.

**The order is: `runtimesMu` → `rs.mu`, or `schedulerMu` → `rs.mu`, and
`runtimesMu` and `schedulerMu` are NEVER held at the same time.** `rs.mu` nests
under either registry lock and nests nothing itself. Decision 11's resolution
order releases `runtimesMu` before it consults the scheduler at step 3, which is
what keeps that rule true rather than aspirational; Decision 12's rule that
`runEntry` fields are never read under `rs.mu` is what keeps the whole order
acyclic.

| Symbol                                        | Acquires                                     | Caller-holds form                       | Callable while holding `rs.mu`?            |
| --------------------------------------------- | -------------------------------------------- | --------------------------------------- | ------------------------------------------ |
| `resolveOrAdopt` (Decision 4)                 | `runtimesMu` (×2–3, released across the I/O) | — (never called under a lock)           | **no** — and not under `runtimesMu` either |
| `RuntimeState.RecordExecutionPath` (`:710`)   | `rs.mu`                                      | `recordExecutionPathLocked` **(new)**   | no — call the `Locked` form                |
| `RuntimeState.RecordStagePuntReason` (`:738`) | `rs.mu`                                      | `recordStagePuntReasonLocked` **(new)** | no — call the `Locked` form                |
| `RuntimeState.MarkTerminal`                   | `rs.mu`                                      | `markTerminalLocked` **(new)**          | no — call the `Locked` form                |
| `RuntimeState.MarkAbandoned`                  | `rs.mu`                                      | `markAbandonedLocked` **(new)**         | no — call the `Locked` form                |
| `RuntimeState.SetStageChild` (7.2)            | `rs.mu`                                      | `setStageChildLocked` **(new)**         | no — call the `Locked` form                |
| `RuntimeState.Snapshot` (`:993`)              | `rs.mu`                                      | `snapshotLocked` (**exists**, `:1000`)  | no — call the `Locked` form                |
| `RuntimeState.Persist`                        | `rs.mu` across marshal **and** write         | `persistLocked` **(new)**               | no — call the `Locked` form                |
| `RuntimeState.SealAndRemove` (step 4)         | `rs.mu`; calls `persistLocked`               | —                                       | no; and it holds **no** registry lock      |
| `Scheduler.IsRunLive` / `LookupRunByID`       | `Scheduler.activeRuntimesMu`                 | —                                       | no — and never under `runtimesMu`          |

Every exported method above becomes a two-line wrapper —
`rs.mu.Lock(); defer rs.mu.Unlock(); return rs.xLocked(…)` — so there is exactly
one implementation of each behaviour and no possibility of the locked and
unlocked forms drifting. **A pure refactor with no behaviour change**, which is
why it lands in plan step 1 with the rest of the state layer rather than with the
registry.

**Why the claim holds both locks rather than latching admission and then taking
`rs.mu` separately.** The cheaper form — set `e.terminal = true` under
`runtimesMu`, release it, then take `rs.mu` for 1b–1d — is safe against a second
_claim_ (admission is already latched) but not against an ordinary transition,
which can take `rs.mu` in the gap and append a stage **after** the sequence
declares the replay to be the run's last accepted mutation. Holding both is
microseconds of a server-global lock for two map writes and one struct copy, and
it makes the normative sentence literally true. The disk read that Decision 4
refused to put under this lock is three orders of magnitude more expensive; the
comparison is not close, and the two decisions are consistent rather than in
tension.

**Why #309's replay is step 1b and not a step of its own** (it had no slot at
all in the first draft, which is how it became a silent regression). Today it
mutates the live runtime _before_ snapshotting, because `BuildV2Record` projects
`execution_path` / `punt_reason` off the snapshot. Under a claim sequence that
latches first, the replay would be **refused** — every extension-path history
record silently losing #309's fields, with no test that would notice. Running it
before the claim but outside the lock reintroduces exactly the unlocked
resolve-then-mutate window this decision exists to delete. Inside the critical
section is the only correct place, and it is cheap enough to belong there: both
methods — in their `…Locked` forms, per the discipline table — are pure
in-memory map writes on `rs` with no I/O, so the added hold time is
microseconds. The same argument that made a **disk read** under this lock
unacceptable in Decision 4 makes these map writes fine.

#### The latch has two halves, one owner each, and `Persist` enforces the durable one

The first draft said "once `terminal` is set, the entry refuses every further
mutation _and every further `Persist`_" while putting `Terminal` on `runEntry`
("never persisted, never read under `RuntimeState.mu`") and leaving `Persist` a
`RuntimeState` method with no access to it. Two flags named `terminal`, two
owners, and no decision said who set both — so the latch could not touch a
`Persist` that was already in flight (F27):

> `notifyStageTransition` resolves entry E under the lock, **unlocks**, mutates,
> and calls `rt.Persist(stateDir)` unlocked (`server.go:2585-2597`). Meanwhile
> `notifyComplete` claims, does seconds of unlocked I/O, writes the V2 record,
> compare-and-deletes, stamps `terminal` into the file and removes it. The
> transition's `Persist` then lands and **re-creates** the snapshot with
> `terminal: false` and the full history. Consequences: the reconciler emits a
> second, contradictory `pipeline_done`; and after any restart adoption
> rehydrates it in full, so the next call produces a record strictly richer by
> one stage — which `appendAndIndex` accepts as an upgrade, replacing the
> correct authoritative index entry. That is the precise R-4-falsifying
> overwrite fix #1 of Decision 4 claims to have killed, reachable with a
> **successful** `os.Remove` and no cross-process writer.

The correction has three parts, and together they make `Persist` itself
latch-aware:

- **The durable half lives on `RuntimeState`.** `Terminal` / `TerminalAt` are
  fields of the run's _content_, written under `rs.mu` by `MarkTerminal` in step
  1c and marshalled by `snapshotLocked` like any other field. `runEntry.terminal`
  remains the registry's _admission_ flag under `runtimesMu`. Neither is a copy
  of the other: one gates resolution of new calls, the other travels with the
  object and with every byte it writes.
- **`Persist` holds `rs.mu` across the marshal _and_ the `AtomicWriteFile`.** It
  does not today, and that gap is the hole: a marshal-under-lock followed by an
  unlocked write lets a stale byte slice land after a removal. Holding the run's
  own mutex across its own file write serialises writers within the process —
  which is exactly the property being bought — at a cost of a few milliseconds
  of contention on a mutex whose only other holders are that same run's
  mutations.
- **`Persist` refuses a sealed runtime.** After step 4, `rs.sealed` is true and
  every subsequent `Persist` returns `ErrRunSealed` **without writing**. `sealed`
  is in-memory only (the durable equivalent is the `terminal` field it just
  wrote), and it is checked _inside_ `Persist`'s own `rs.mu` critical section —
  so the check and the write cannot be separated by a scheduler.

The mechanical form of all three: `persistLocked` is the body, `Persist` is the
`Lock`/`defer Unlock` wrapper, and **`SealAndRemove` calls `persistLocked`, not
`Persist`** — re-entering the exported method from inside its own lock is F36 at
a second site, and the lock-discipline table above exists so that this is stated
once for every symbol rather than remembered per call.

**Where every unlocked `Persist` re-checks — the complete list.** No handler
gains its own guard; each one re-checks by virtue of calling `Persist`, which
now cannot be raced:

| Unlocked `Persist` call site                     | Outcome once the claim has run                                                                                         |
| ------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------- |
| `notifyStageTransition` (`server.go:2585-2597`)  | `ErrRunSealed`; logged once per `(method, runId)`; the run is closed and its content already booked                    |
| `notifyPhaseTransition` (IPC arm)                | `ErrRunSealed`                                                                                                         |
| `notifyStageProgress`                            | `ErrRunSealed`                                                                                                         |
| `setPaused` (administrative)                     | resolution already refused by `entry.terminal` → `run_closed`; a sealed object is refused a second time at `Persist`   |
| `abandonRun`'s `MarkAbandoned` + `Persist` (7.1) | `ErrRunSealed` — a run that completed cannot be retro-abandoned                                                        |
| `AppendStageGateResultToDisk` (separate process) | not covered by an in-process latch; covered by load-or-skip + terminal-refusal + `PersistExisting` below, residual R-1 |

The three interleavings that remain are all benign and all decided:

1. A `Persist` that entered `rs.mu` **before** `MarkTerminal` writes a
   non-terminal file, and step 4 runs strictly after it (same mutex), so the
   seal overwrites and removes it.
2. A `Persist` that enters **between** `MarkTerminal` and the seal marshals
   `Terminal: true` — so even if it lands, the file it writes is one adoption
   refuses (Decision 4 fix #2) and the reconciler removes without emitting
   (7.3). A resurrection that cannot rehydrate and cannot re-emit is not a
   resurrection.
3. A `Persist` **after** the seal writes nothing.

The full lock order — including the scheduler's registry mutex, and including
which symbols have `…Locked` twins — is the table under
[The lock discipline](#the-lock-discipline-as-a-table) above.

**The cross-process half is still not latchable, and this ADR says so rather
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
reconciliation, which the removal prevented. After this ADR a canonical snapshot
leaves the directory through exactly **three** doors, and no others:

1. a terminal claim's `SealAndRemove` (`notifyComplete`, Decision 5 step 4);
2. the reconciler — which emits the run's terminal event first **unless** the
   snapshot already carries a `terminal` marker or is a past-window `abandoned`
   one, and which also applies the 14-day cap (7.3, 7.4);
3. the pause-restore **claim rename** (Decision 9), which does not delete the
   state at all — it moves it to `resuming-…` under the claimant's ownership and
   the claimant deletes that artifact once the run is running again.

The `resuming-*` artifact is **not** a canonical snapshot and its lifecycle is
separate: the claimant deletes it on success, and the reconciler's release either
renames it back (canonical absent) or removes it as superseded debris (canonical
present) — never overwriting a canonical file, which is the F34 failure. Adding a
door for the artifact does not add one for the run's state.

`abandonRun` is deliberately not one of them, and neither is the TypeScript stub
sweep (which classifies, and only ever deletes identity-less new-scheme stubs).

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

| It does                                                                                                                                        | It does NOT                                                                              |
| ---------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| resolve by `runId` — an existing entry, else the on-disk snapshot adopted through the singleflight, and **never** an invented one (Decision 3) | set the terminal latch                                                                   |
| call `rt.MarkAbandoned(now)` under `rs.mu` — the fields live on the **live `RuntimeState`**                                                    | add the id to `closedRuns`                                                               |
| mirror `entry.Abandoned` / `entry.AbandonedAt` under `runtimesMu` for the index ranking                                                        | delete the registry entry, or refresh `LastSeen` (Decision 4)                            |
| take the snapshot under the lock and emit the run's terminal `pipeline_done` from it                                                           | remove the snapshot                                                                      |
| `Persist` it, so `abandoned: true` + `abandoned_at` are durable in `runtime-{issue}-{runId}.json`                                              | write a learning-corpus row (an abandoned dispatch measures nothing about model routing) |
| drop the entry out of the issue index's "current" ranking (Decision 6)                                                                         | make the run ineligible for the liveness skip (7.2)                                      |

#### The marker survives the run that keeps overwriting its own snapshot

7.1's whole premise is that an abandoned dispatch is **still alive** and will
keep emitting transitions, each of which calls `Persist`, which marshals the
**whole** snapshot with no merge (`runtime_state.go:909-925`). The first draft
said `abandonRun` "stamps `abandoned: true` durably into the file" without
saying whether the field was set on the live object or only on a written copy,
and both readings lost data (F4 of the second review):

- **stamped on a copy** → the run's very next transition erases the marker, the
  reconciler treats it as an ordinary orphan, and it emits a **second**
  `pipeline_done` and removes the snapshot;
- **set on the live object, with the first draft's `skipRun` predicate** → that
  predicate was `has(runID) && !terminal && !abandoned && LastSeen within 30m`,
  so an abandoned entry was **never** skipped no matter how fresh, and 7.3 would
  "remove without emitting" the crash snapshot of a run that refreshed
  `LastSeen` two seconds ago and is actively streaming stages.

**The decision is persist-through, and abandonment leaves the liveness test
alone.** Concretely:

1. `abandoned` / `abandoned_at` are **fields of `RuntimeState`**, set under
   `rs.mu` by `MarkAbandoned` exactly as `Terminal` is set by `MarkTerminal`
   (Decision 5). Because they live on the live object, every subsequent
   whole-snapshot `Persist` **re-stamps** them. There is no copy to diverge.
2. **There is no `ClearAbandoned`.** No transition, no phase, no progress call
   can unset it; the only state that supersedes it is `terminal`, and a run that
   reaches its own honest completion carries `abandoned_dispatch: true` into its
   V2 record as provenance rather than erasing the fact.
3. **`abandoned` is removed from the liveness predicate entirely** (7.2).
   Abandonment is a statement about the _dispatch_, never evidence about the
   _run_ — that is the whole content of 7.1, and letting it decide liveness
   contradicted it inside two subsections. A fresh abandoned entry is skipped
   like any other fresh entry.
4. Removal of an abandoned snapshot therefore requires **two** independent
   facts: `abandoned_at` older than the liveness window **and** `!skipRun` on
   the full ladder (7.2). The retention table in 7.4 states this per state, so
   "fresh but abandoned" has an explicit row rather than falling out of a
   predicate.
5. The claim's step-4 seal can no longer target a file the reconciler removed
   underneath it: `SealAndRemove` writes before it removes (Decision 5), so the
   worst case is a re-create-then-remove that nets to nothing.

The entry stays **adoptable and mutable**. A late honest completion from the
same process is accepted, performs the ordinary terminal claim, and books its
record and learning outcome **under its own identity** — the #307 behaviour,
preserved. The platform sees `pipeline_done(success=false)` at abandon time and
then the run's real terminal event; the row converges on the truth **if**
`pipeline_runs` is keyed by `run_id` and last-writer-wins on outcome. That is
**Assumption A-1**, it is unverified from this tree, and the
[Assumptions](#assumptions) section carries both the verification task and the
design that applies if it is false. Emitting early and correcting later is the
right trade under A-1: the ordinary case is that the wedge is terminal and #44's
whole point is not stranding a `running` row.

**Resolution when the registry has no entry** (the modal case — the force-clear
fires because something is wedged, and a restarted or replaced IPC binary leaves
an empty registry):

1. `runtime-{issue}-{runId}.json` exists under `pipelineStateDir(repo)` →
   **adopt it through Decision 4's singleflight**, then run the ordinary
   in-registry path against the live object: `rt.MarkAbandoned(now)` (the
   exported form — the handler holds no lock here), snapshot, emit the
   `pipeline_done` from the snapshot, `Persist`. The entry is
   installed **with `LastSeen` untouched**, so it serialises writes without
   vouching for liveness (Decision 4's administrative-adoption table).

   This is deliberately **not** a load-mutate-write against a detached copy. That
   form was the second revision's answer and it is F33: on the modal path there is
   no live object for `rs.mu` to serialise against, so a concurrent adoption
   erases the marker (`abandonRun`) or the administrative write rolls back stages
   the run booked in between (`setPaused`). There is **no raw whole-snapshot
   read-modify-write anywhere in this ADR**; every write to a `runtime-*.json`
   goes through a `*RuntimeState` that the registry holds.

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

#### 7.2 The liveness ladder — registries, process, disk

Every accepted call stamps `entry.LastSeen`. The reconciler's skip predicate is
re-derived from _"this issue has an entry"_ to **"this run is live"**, and it
consults every source of evidence that exists rather than only the one the
reconciler happens to be standing next to:

```
skipRun(runID, snapshot) =
      ipcRegistry.has(runID) && !terminal && LastSeen within LIVENESS_WINDOW   // 1. IPC registry
   || scheduler.IsRunLive(runID)                                              // 2. scheduler registry (Decision 11)
   || processAlive(snapshot.PID)                                              // 3. the run's own child process
   || fileAge(snapshot) < LIVENESS_WINDOW                                     // 4. the disk-side lease
   || withinStartupGrace()                                                    // 5. the reconnect window (7.3)
```

`LIVENESS_WINDOW` is 30 minutes.

**Two sentences the second revision had here were false against the cited code,
and they are corrected rather than softened (F32).** They mattered because
together they asserted that three arms covered the extension-path population,
which is the entire population F26 is about:

| Claimed (rev 2)                                                                                        | Verified                                                                                                                                                                                                                                                                                                                                                                           |
| ------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| "`notifyStageProgress` refreshes arms 1 **and 4** continuously (≥5s cadence, `PipelineBridge.ts:355`)" | It refreshes **arm 1 only.** The handler (`server.go:2623-2673`) reads under the lock, emits telemetry and returns; its own comment forbids mutation, so it never calls `Persist` and never bumps the mtime arm 4 reads. Arm 4 is refreshed by `notifyStageTransition`'s persist alone (`server.go:2585-2597`) — i.e. at **stage boundaries**.                                     |
| the cited site, `PipelineBridge.ts:355`                                                                | That is the **Go-scheduler** bridge (`PipelineBridge.ts:265-272` says `activeRuntimes` is populated only by the `HeadlessOrchestrator` path). The extension-path emitter is `HeadlessOrchestrator.ts:13323`, which does exist and does refresh arm 1.                                                                                                                              |
| "`RuntimeState.PID` / `WorktreeDir` (`SetProcess`) record the stage child"                             | Only for scheduler runs. `SetProcess` has **one** production caller in the tree — `internal/execution/manager.go:301`, inside the Go execution manager. Extension stages are spawned by `runStageSkillHeadless` (`utils/skillRunner.ts:3505`) in the extension host, and no `pid` field exists anywhere in `internal/ipc/protocol.go`. Every `activeRuntimes` runtime has `PID` 0. |

So, as written, arm 3 was **structurally dead** for the population it was added
for, and arm 4 refreshed an order of magnitude less often than claimed.

#### Per-population coverage — which arms actually carry which runs

C18 exists because an arm no run in a population writes is not a fallback for
that population.

| Population                                       | Arm 1 (entry + fresh `LastSeen`)                         | Arm 2 (scheduler)            | Arm 3 (PID)                              | Arm 4 (file age)         | Arm 5 (grace) |
| ------------------------------------------------ | -------------------------------------------------------- | ---------------------------- | ---------------------------------------- | ------------------------ | ------------- |
| **Extension-path runs** (`HeadlessOrchestrator`) | **yes** — every accepted call; ≥5s while tokens flow     | never (not in that registry) | **yes, after the wire field below**      | at stage boundaries only | yes, 120s     |
| **Scheduler-path runs** (`PipelineBridge`)       | never (never adopted into `activeRuntimes`, Decision 11) | **yes** — the primary arm    | yes (`manager.go:301` already writes it) | at stage boundaries only | yes, 120s     |
| **CLI runs** (`nightgauge run`)                  | never                                                    | never                        | **yes** — same scheduler code path       | at stage boundaries only | yes, 120s     |

**The extension path gets a PID, and this ADR specifies the wire field rather
than assuming one.** Without it the surviving F26 trace is still open, and it is
an ordinary profile rather than an exotic one: a run in `pr-merge` polling CI (or
any stage whose last transition persisted more than `LIVENESS_WINDOW` ago) emits
no assistant tokens for the whole grace window, so arm 1 never fires; arm 2 and
arm 3 are structurally false; arm 4 is stale because the last **transition** was
40 minutes ago; arm 5 expires. The backend auto-restarts, and at T+120s a live
run is reconciled — terminal `pipeline_done(success=false)`, snapshot removed,
skeleton record, signal-free learning row.

- **`pipeline.notifyStageTransition` gains `stagePid int`** (`omitempty` on the
  wire is fine — it is advisory, not an identity). `runStageSkillHeadless`
  already holds the child (`const proc = spawn(cmd, args, …)`,
  `utils/skillRunner.ts:3505`); a new `onStageChildSpawned(pid)` callback on
  `SkillRunCallbacks` surfaces it, and `HeadlessOrchestrator` sends it on the
  stage's `running` transition.
- The server records it with **`rt.SetStageChild(pid)`** — a new one-field setter
  under `rs.mu`, deliberately **not** `SetProcess`, which also writes
  `WorktreeDir` and belongs to the scheduler. The value lands in the snapshot
  through the transition handler's existing `Persist`; **no new persist site is
  introduced**, which is what makes this cheap.
- A stage's terminal transition sends `stagePid: 0`, so a finished child cannot
  vouch for the run and the PID-reuse window is bounded by one stage rather than
  by the run. Between stages arm 3 is false and arm 4 is fresh by construction —
  a transition just persisted.
- `PID` is already a persisted field (`runtime_state.go:52`, copied by
  `snapshotLocked`), so a fresh process reads it from the snapshot with no
  registry — which is the whole reason arms 3 and 4 exist.

**Why not a periodic lease touch or a keepalive verb instead.** Both were
considered and rejected — Alternative O. A lease touch on the progress verbs does
not close the trace, because the trace's run emits no progress at all; a
keepalive verb is Alternative M's `declareLiveRuns` at a different cadence, and
C8 forbids a new unauthenticated verb whose only effect is to suppress
reconciliation.

The lease (arm 4) is a coarse backstop for a lost `abandonRun`, not the primary
mechanism — it can only fire for a run that lost its abandon call, crossed a
30-minute stage boundary gap, and has no live process.

**`abandoned` is not on this ladder, deliberately.** The first draft's predicate
included `&& !abandoned`, which made a fresh, actively-streaming abandoned run
_ineligible_ for the skip and handed its crash snapshot to the remover. See 7.1
— abandonment describes a dispatch, not a run.

**Arm 2 is not optional, and it fixes a live defect rather than a hypothetical
one (F21).** `reconcileOrphanedRuns` builds its skip from `s.activeRuntimes`
alone. The Go scheduler persists into the same directories (`scheduler.go:4043,
4460, 6271`, covered by `pipelineStateScanRoots`) and always stamps a non-empty
`RunID`, and `collectOrphanedRuns` skips only paused and `RunID`-less snapshots.
Scheduler runs are **never** in `activeRuntimes` — `PipelineBridge.ts:265-267`
says so verbatim. So every `workspace.setRoot` (`server.go:757`, fired from
`bootstrap/services.ts:2441` on `onWorkspaceChanged`) emits a terminal
`pipeline_done` for every **live** scheduler run and `os.Remove`s its crash
snapshot. Renaming `skipIssue` to `skipRun` would have made that predicate look
rigorous while leaving it blind to half the product's runs.
`Scheduler.IsRunLive(runID)` — a scan of its `map[int]*state.RuntimeState` for a
matching `RunID`, under `activeRuntimesMu` — is the arm that closes it.

**Arms 3 and 4 exist because a fresh process has neither registry.** Both read
the snapshot, which any process can parse: `RuntimeState.PID` names the stage
child (written by `SetProcess` on the scheduler path and by `SetStageChild` on
the extension path, per the wire field above), and every `Persist` bumps the
snapshot's mtime. A live stage child is direct evidence the run is alive; a
snapshot written 40 seconds ago is nearly as good. Arm 3 was previously
described as "the evidence the tree already writes and never reads" — for the
scheduler population that is true, and for the extension population the tree did
not write it at all, which is F32.

**The ladder is deliberately biased, and the bias is stated as a rule (C13).**
Every arm can only ever produce a **skip**, never a close. So:

- a **false positive** (a recycled PID, a snapshot touched by something else)
  costs one deferred sweep — collected on the next activation, and unconditionally
  by the 14-day cap;
- a **false negative** costs a live run its entire record, its learning row and
  its telemetry, silently, behind five bare `catch {}` blocks.

Those are not comparable, so the predicate is not symmetric. PID reuse is the
obvious objection to arm 3 and it is answered by that asymmetry rather than by a
start-time comparison the snapshot does not record: the worst outcome of a
reused PID is that a dead run's platform row closes at the next activation
instead of this one.

#### 7.3 `collectOrphanedRuns` keys on the run, and the startup sweep DEFERS

It parses the identity out of the filename. Its existing skips are preserved
(C1), every candidate is filtered through the 7.2 ladder first, and the
disposition per snapshot state is the table in 7.4 rather than a predicate a
reader has to evaluate in their head. Emit-then-remove idempotency across
activations is unchanged.

**The `Server.Run` call site does not reconcile inline, and that is the fix for
the worst defect in the first draft (F26).** The draft asserted "Both call sites
then work: `Server.Run` (fresh process, empty IPC registry…)". That is wrong,
and the trigger is an **engineered auto-behaviour**, not a rare crash:
`IpcClientBase.ts:1472-1485` restarts the Go backend on process exit (5
attempts, `2000ms · 2^(n-1)` backoff) **while the extension host and all its
in-flight runs survive**. `server.go:654` then runs `reconcileOrphanedRuns()`
before any client can reconnect. Trace, all of it on today's tree:

> Run R is live; `runtime-42-R.json` holds its full history. The backend dies
> and restarts. The IPC registry is empty **and** the scheduler registry is
> empty (extension-path runs are never in the scheduler registry —
> `PipelineBridge.ts:265-267`), so arms 1 and 2 of the ladder are both false.
> The reconciler emits a terminal `pipeline_done(success=false)` for a **live**
> run and `os.Remove`s its snapshot. R's next message adopts and rehydrates
> **nothing**; R's eventual `notifyComplete` writes a **skeleton** authoritative
> record (zero stages, zero cost) and a learning-corpus row with no measurable
> routing signal — and #313's richer-upgrade-only rule has no richer record to
> prefer, because the skeleton is the only one. The 30-minute lease is
> structurally incapable of helping: it is keyed on a `runEntry` a fresh process
> does not have.

So the startup sweep becomes a **deferred, re-evaluated** sweep:

1. `Server.Run` **collects** the candidate set and starts a timer. It emits
   `ipc.ready` immediately; nothing about the handshake is delayed.
2. For `STARTUP_GRACE = 120s`, `withinStartupGrace()` (ladder arm 5) is true and
   **no snapshot is emitted for or removed by the reconciler**. Terminal
   snapshots are the one exception — they carry their own proof and are removed
   without emitting at any time.
3. At expiry the candidate set is **re-evaluated from scratch** against the full
   ladder. A run that reconnected has an entry (arm 1) or a refreshed file
   (arm 4); a run that is busy rather than chatty — the long-CI case — has a live
   stage child (arm 3). Everything still stale is reconciled normally.

   **Only run traffic counts as re-assertion.** An entry installed by an
   administrative resolution (Decision 4) carries a zero `LastSeen`, so it does
   not satisfy arm 1 and does not rescue its snapshot from this re-evaluation.
   `abandonRun` is not evidence that a run is alive; it is the opposite claim.

**120 seconds is derived, not chosen by feel:** the client's five restart
attempts sum to `2+4+8+16+32 = 62s` of backoff, plus process start, plus the
first `notifyStageProgress` at a ≥5s cadence. 120s clears that ladder with
margin. `workspace.setRoot` keeps reconciling inline — a `setRoot` arrives from
a **connected, live** extension host, so arms 1 and 2 carry real information —
except when it fires inside the server's own startup grace, where arm 5 defers
it like any other candidate.

**Backend auto-restart vs cold start is decided by behaviour, not by a flag.**
After the grace window, an auto-restart's live runs have re-asserted — an entry,
a fresh mtime, or a live stage child — and a cold start's have not (its recorded
PIDs died with the machine or the host, and no message arrives). The cost of not
distinguishing them
up front is that a genuinely-dead run's platform row closes two minutes later
than it would have; the cost of getting it wrong in the other direction is F26.
An explicit "the client declares which runs it believes are live" handshake was
considered and rejected — Alternative M.

#### 7.4 Retention: one disposition table, and it must not depend on telemetry

Today there is none, which is why identity-less debris accumulates. Every
snapshot the scan sees resolves to exactly one row, evaluated top to bottom:

| Snapshot state                                                                                                                                  | Reconciler emits?                           | Removes?                                                                                                                                                                     |
| ----------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `terminal: true`                                                                                                                                | **no** — the terminal claim already emitted | **yes**, at any age, grace or no grace                                                                                                                                       |
| identity-less (no id in the filename or the body)                                                                                               | no                                          | **no** — never touched here; the Go legacy sweep owns it (Migration)                                                                                                         |
| `skipRun` true on any ladder arm (7.2), including the startup grace                                                                             | no                                          | no                                                                                                                                                                           |
| `abandoned: true`, `abandoned_at` **inside** the liveness window                                                                                | no — already emitted by `abandonRun`        | **no** — the dispatch is abandoned, the run may still be streaming (7.1)                                                                                                     |
| `abandoned: true`, `abandoned_at` **outside** the window, `!skipRun`                                                                            | no — already emitted                        | **yes**                                                                                                                                                                      |
| `paused: true`, fresh                                                                                                                           | no (C1/C5 — it powers the restore prompt)   | no                                                                                                                                                                           |
| `resuming-*` whose **claim token** is older than `STARTUP_GRACE` (Decision 9) — never the file's mtime, which `rename(2)` does not update (C17) | no                                          | **released, not reconciled**: canonical absent → renamed back to it; canonical present → the artifact is removed as superseded debris and canonical is **never** overwritten |
| `resuming-*` whose claim token is fresh, unparseable, or ahead of the reader's clock                                                            | no                                          | **no** — treated as a live claim (fail-safe)                                                                                                                                 |
| ordinary orphan, `!skipRun`                                                                                                                     | **yes**                                     | **yes**, after emitting                                                                                                                                                      |
| **anything** older than 14 days, including paused                                                                                               | yes, if never reconciled                    | **yes**                                                                                                                                                                      |
| a terminal claim's own snapshot                                                                                                                 | n/a                                         | removed by `SealAndRemove` (Decision 5 step 4)                                                                                                                               |

The two rows that used to be a single "abandoned → remove without emitting"
bullet are the F4 correction: a fresh abandoned run keeps its crash snapshot,
because 7.1's entire premise is that it is still alive.

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

**One sibling filename exists in the same directory:** the pause-restore claim
artifact `resuming-{issue}-{runId}.{claimToken}.json` (Decision 9), matched by
`^resuming-(\d+)-(<identity>)\.(<identity>)\.json$` — **both** components are
the same shared identity constant, because the claim token is itself a UUIDv7
(Decision 9). The reconciler parses the claim time out of the second one; a
token that does not match is not a claim artifact and the file is left alone.
It is a full `RuntimeState` snapshot under a name that means "a host is claiming
this"; the reconciler releases a stale one rather than reconciling it (7.4), and
no other reader touches it.

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
> `IssueNumber` equals the raise's `issue`. If no run matches all three,
> **corroboration fails** — the card is still raised, without the
> raise-and-retry option, exactly as §N specifies for a failed corroboration.

**And the raise's `RunID` is the run's INSTALLED identity, threaded — never
read out of a file.** The first draft promoted `RunID` from an audit label to a
required selector while leaving its only producer a guesser, which would have
converted a working corroboration into a systematically failing one on the
extension path while describing the change as "strictly narrower than today"
(F31). `HeadlessOrchestrator.readCurrentRunId` (`:1525-1544`) reads
`.nightgauge/pipeline/run-state.json` — a **scheduler/CLI runstate artifact**,
not the extension run's identity — and returns `""` on any miss or issue
mismatch. On the extension path, which the raise method's own doc comment names
as the entire population of run-scoped raises, that guess is empty or foreign by
default. It is a run-id guesser of exactly the F15 class, and the first draft's
"exhaustive by construction" producer table did not contain it.

Therefore:

- **`readCurrentRunId` is deleted**, and it is listed in Decision 10's table as
  a deleted guesser so the table's exhaustiveness claim survives contact.
- `raiseRunScopedCard` takes the identity as an **explicit parameter** from the
  orchestrator's own run context — the value `beginRun` installed (Decision 10).
  The orchestrator holds it for the same reason it holds the `--run-id` it
  passes to its four `nightgauge gate verify` spawns.
- Because the value is now always present and always correct on the path that
  raises these cards, **#305's raise-and-retry ceiling option is preserved**
  rather than removed. That is the whole point of threading it: the first
  draft's rule was safe and useless; this one is safe and lands.
- **`RunID` becomes required for run-scoped producers and stays optional for
  repo-scoped ones.** A run-scoped raise with an empty `RunID` is an error
  (`run_id_required`), not a silent corroboration failure — an empty id there
  means a producer forgot to thread it, and a loud refusal is how that gets
  found. Repo-scoped producers (default-branch health and friends) have no run
  and continue to omit it.
- `AttentionRaiseParams`' doc comment at `protocol.go:1524-1530` — _"Empty is a
  handled case … It is an audit back-reference only — nothing is authorized by
  it"_ — is **amended in this PR**. It is accurate for repo-scoped producers and
  false for run-scoped ones once this lands, and leaving a contract comment that
  contradicts the code is how the first draft's protocol-version claim happened.

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
3. **The snapshot is CLAIMED by an atomic rename before anything resumes**, and
   only the winner of that rename resumes. See below.
4. Resume threads the identity: `pipelineStateService.beginRun(runId, repo,
issueNumber)` (Decision 10) **before** `resumePipeline()`, and
   `headlessOrchestrator.runPipeline(issueNumber, { runId })`. The resumed run
   continues **under the snapshot's own identity**.
5. `setPaused` resolves through the on-disk snapshot when the registry has no
   entry — by **adopting** it through Decision 4's singleflight, with `LastSeen`
   untouched, never by rewriting a detached copy (F33) — so a pause or resume
   issued after an IPC-server restart lands instead of being dropped, and cannot
   roll back stages a live run booked in between.
6. `classifyRuntimeStub`'s two existing rules are unchanged (empty repo/stage →
   delete; repo mismatch against the containing repo → delete) and it gains a
   third: **a new-scheme file with no identity in its name or body → delete**.
   It does **not** classify legacy `runtime-{N}.json` at all — see Migration.

#### Consume-on-claim: the rename IS the exclusion (C15)

Decision 1's entire safety argument is that _a producer that must supply an
identity **it minted itself** cannot address a run it did not start_, and
Alternative H rejects `RemoteRunID` seeding because a redelivered command
"produces two dispatches carrying the same `runId`; the second adopts the
first's live entry, and both runs share one registry key, one snapshot file and
one history-record key". The first draft's resume path did **exactly that** with
a locally redeliverable seed: it parsed an id off disk and resumed under it,
while nothing consumed the file. `bootstrap/services.ts:1168-1220` runs the
restore prompt on **every** activation and the file is only mutated later,
best-effort, by `resumePipeline`'s `setPaused` round trip inside a swallowing
`try/catch` (`PipelineStateService.ts:1012-1025`). Two extension hosts on one
workspace — a bypass Decision 6 names by name — both scan, both prompt, and both
resume run id X: two live dispatches under one identity, one `activeRuntimes[X]`
mutated by both (F2), one snapshot under whole-file last-write-wins (F13), one
`run:X` ledger key collapsing two different runs into one record (F6) — with no
force-clear and no zombie in sight (F28). "The snapshot is consumed rather than
orphaned" was asserted and never mechanised.

**The claim artifact is a rename, and it is the only thing that authorises a
resume:**

```
rename( runtime-{issue}-{runId}.json ,  resuming-{issue}-{runId}.{claimToken}.json )
```

- Same directory, therefore the same filesystem, therefore **atomic**; and
  `rename(2)` on a source that no longer exists fails `ENOENT` on POSIX and on
  Windows alike. **Exactly one host's rename can succeed.**
- **Order: prompt → user chooses Resume → rename → resume.** Renaming before the
  prompt would leave a claimed-but-unresumed file behind on every dismissal.
  Two hosts both prompting is a UX wart; two hosts both resuming is corruption,
  and only the second is prevented structurally.
- The loser sees `ENOENT`, logs once, drops that snapshot from its list and
  shows _"#N was resumed elsewhere."_ **It deletes nothing** — the file it
  wanted is now another host's working state.
- On a successful rename the claimant reads the claimed file, calls `beginRun`,
  starts the run, and deletes the `resuming-*` artifact once `beginRun` returns.
  The run's first accepted transition re-`Persist`s under the canonical name, so
  the canonical file reappears owned by exactly one live run.

##### The claim's age comes from the claim token, never from the file's mtime

**`claimToken` is a UUIDv7 minted by the claimant at the instant of the claim**,
and the release rule parses the claim time out of it. This is the one place in
this ADR where a UUIDv7's timestamp is decoded (Decision 1 states the carve-out
and why it is not a contradiction).

The second revision made the token the host's `vscode.env.sessionId` and aged the
claim by file mtime. That is unimplementable, and it fails in the worst
direction (F34, C17): **`rename(2)` updates the file's `st_ctime` and the two
directories' `st_mtime`, but never the renamed file's own `st_mtime`.** The
artifact therefore inherits the mtime of the **paused snapshot**, and a pause is
by construction read at a later activation — minutes to days after it was
written. Every claim is born older than `STARTUP_GRACE = 120s` and is eligible
for "release" the instant it exists. Two consequences, both of which reopen what
C15 exists to forbid:

1. any reconcile pass interleaving between the rename and the claimant's delete —
   `workspace.setRoot` reconciles inline and fires on `onWorkspaceChanged` —
   renames the **stale paused content back over the live run's canonical
   snapshot**, whole-file last-write-wins, destroying the resumed run's
   post-resume history until its next `Persist`;
2. the restored canonical file advertises `paused: true` for a **live** run id,
   so the next host to scan prompts, wins a rename that should have been
   impossible, and dispatches run X while X is executing — F28 verbatim.

The rule, stated so an implementer cannot reconstruct the mtime version:

```
claimAgeOf(resuming-{issue}-{runId}.{claimToken}.json):
  ts := uuidV7Millis(claimToken)              // the 48-bit big-endian Unix-ms prefix
  if !parses                       → treat as FRESH; log once; never release   (fail-safe, C13)
  if ts > now + CLAIM_SKEW_TOLERANCE → treat as FRESH; log once; never release (a future claim is not an old one)
  return now - ts
```

- **`CLAIM_SKEW_TOLERANCE = 60s`**, and it is one-sided on purpose: it is
  tolerance for a token minted slightly _ahead_ of the reader's clock, not a
  grace period added to `STARTUP_GRACE`. A claim releases at
  `claimAge > STARTUP_GRACE`; there is exactly one threshold.
- **Both failure modes bias toward not releasing**, which is C13's bias applied
  to this rule: a claim we cannot age is treated as live. The cost is a paused
  run that needs one more activation to re-prompt; the cost of the other
  direction is two live dispatches under one identity.
- **The claimant is still nameable in a forensic listing** — the property the
  `sessionId` token was chosen for — but through the log rather than the
  filename: the claimant logs `(runId, claimToken, sessionId)` at claim time, and
  the token in the filename joins the two. A filename is a poor place to carry a
  host identifier and a good place to carry a timestamp, because only one of the
  two is read by a rule.

**The release path, complete.** A `resuming-*` artifact whose `claimAge` exceeds
`STARTUP_GRACE`:

| Canonical `runtime-{issue}-{runId}.json` … | Release action                                                                                                                                                                                                                                                        |
| ------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **absent**                                 | `rename` the artifact **back** to canonical. The claim is released, the pause survives, the next activation prompts again.                                                                                                                                            |
| **present**                                | **Never rename back.** The claimant won, resumed, and its run has already re-persisted; the artifact is superseded debris. Remove the artifact, log once. Renaming here is F34's consequence (1) — it would overwrite a live run's state with the pre-pause snapshot. |

The "present" row is not an edge case: it is exactly the window between the run's
first post-resume `Persist` and the claimant's delete, and a host killed inside
that window leaves precisely this pair. The canonical file always wins because
its content is strictly newer than the artifact's by construction.

- **If the claimant dies between the rename and the first persist**, canonical is
  absent and the first row applies. A crashed claim releases its claim; the pause
  is not lost.
- **A claim is not self-renewing and does not need to be.** The claim's whole
  life is `rename → read → beginRun → delete`, with no user interaction inside it
  — the prompt is already resolved before the rename. `STARTUP_GRACE` is
  therefore an outer bound on an operation that normally takes milliseconds, not
  a lease over a long-running one.

**Why not "resume mints a fresh id and carries the paused one as a
predecessor"** — the other structurally-sound option, recorded as Alternative J.
The paused snapshot's accumulated stage history, cost, phase records, ADR-013
trace file and `run:<id>` ledger key are all keyed to the **existing** id. A
fresh id either orphans all of it or requires a predecessor→successor mapping to
stitch it back — and a mapping is a thing that can be stale, lost or disagreed
about, which is Alternative E's refutation verbatim. Consume-on-claim keeps the
ADR's "one run, one identity, for the run's whole life" invariant, and it does
so with a primitive the operating system already makes atomic.

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
| `bootstrap/services.ts` pause-restore prompt                                                                                           | **consumer** — parses the id from the claimed filename and installs it via `beginRun`, after winning the rename (Decision 9)                                                                                                                                                   |
| `IpcStageRunner.RunStage` (`ipc_stage_runner.go:70-73`)                                                                                | `params.RunID`, **explicitly populated** by the scheduler from `runtime.RunID`; `Runtime` and `RunID` are non-optional and the runner **asserts** before emitting `pipeline.runStage` (see below)                                                                              |
| `HeadlessOrchestrator.raiseRunScopedCard`                                                                                              | the orchestrator's **installed** identity, threaded as a parameter (Decision 9)                                                                                                                                                                                                |
| ~~`HeadlessOrchestrator.readCurrentRunId`~~                                                                                            | **DELETED.** It guessed the id from the scheduler/CLI `run-state.json` — F15's class at a new site (F31). Listed here so this table's "exhaustive by construction" claim covers the guessers it removes as well as the producers it names.                                     |
| ~~SDK `traceRecorder`'s `runtime-${issue}.json` read~~                                                                                 | **DELETED** (F15) — replaced by the `NIGHTGAUGE_RUN_ID` row above                                                                                                                                                                                                              |
| `PipelineSlotsTracker`, `PipelineStateService` event filters                                                                           | **consumers** — route/filter on the envelope's `runId`, empty id falls back (Decision 6)                                                                                                                                                                                       |

This is the structural answer to F17. The refuted design's guard was gated on
`token != ""`, so the token-less producers bypassed it, adopted a live run's
runtime, wrote the authoritative record and outcome under it, deleted the
runtime — after which the live run's own completion was refused and it recorded
nothing at all. When _everyone_ mints or receives, there is no token-less
producer to bypass anything.

#### The emitter's `runId` becomes non-optional BEFORE the verbs require it

The revision that replaced invented plumbing with "PipelineBridge already
receives `ipcParams.runId`" was right about the field existing and wrong about
its guarantees. Today the value is **structurally optional and sometimes empty**:
`RunID string \`json:"runId,omitempty"\``on the Go side,`runId?: string`at`PipelineBridge.ts:63`, and `ipc_stage_runner.go:70-73`initialises`runID := ""`, filling it only `if params.Runtime != nil`. A `RunStage`dispatch
with a nil`Runtime`emits an empty id, and under Decision 3 **both**`PipelineBridge` calls (`notifyPhaseTransition` `:280`, `notifyStageProgress`
`:355`) are then hard-rejected `run_id_required`— swallowed by the bridge's`.catch(warn)`, so the product's **primary** execution path silently loses phase
markers and live progress for that stage. Decision 6's "an event carrying an
empty `runId` is not dropped" fallback covers only **outbound** events; there is
no inbound counterpart and there will not be one.

The resolution is sequencing plus an assertion, not an inbound fallback:

1. **`StageRunParams` gains an explicit `RunID string`**, populated at the single
   production construction site (`scheduler.go:3620`) from `runtime.RunID`, and
   `Runtime` is documented **non-optional**. The nil-`Runtime` case is not a
   supported configuration — `runPipeline` constructs the runtime before it can
   reach a stage — so it is a **programming error**, and the only other
   constructors are tests.
2. **`IpcStageRunner.RunStage` asserts `RunID != ""` and returns an error rather
   than emitting `pipeline.runStage`.** The stage fails loudly at the dispatch
   boundary, **before** any child process spawns and before a single token is
   spent.
3. **`RunStageParams.RunID` loses `omitempty` and becomes required on the wire**,
   as does `runId` in the TypeScript type. `SkillRunner` already receives and
   forwards it (`:216`); it stops being able to receive nothing.
4. This lands as **step 0b of the plan** — with step 0's `NIGHTGAUGE_RUN_ID`
   exporters, and strictly **before the step that makes the verbs require the
   id** (step 4). Step 0b covers the **scheduler** emitter population only; the
   **extension** population is step 3, and the flip in step 4 is gated on both
   (see [Implementation tracking](#implementation-tracking)'s per-verb table).
   Once both have landed the inbound empty-id case is unreachable by
   construction, which is why Decision 3 can keep `run_id_required` as a hard
   error with no softening.

**This does not contradict C9.** C9 forbids a refused or lost IPC call from
killing a run **in flight**; refusing to _start_ a stage that could not be booked
is a different transaction. The failure is at t=0, it is loud, it costs no work,
and the alternative — dispatching a stage whose every progress and phase call
will be refused — is F16's silence with extra steps.

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

**Resolution order for every identity-bearing call** — and it is **entered with
the verb class already decided** (Decision 3 line 3), because the first draft's
ordering let a terminal or administrative verb fall into step 3 and be served
from a registry that has none of the claim invariants (F29):

```
1. closedRuns.has(runId)               → run_closed
2. ipcRegistry[runId]                  → serve from the IPC registry
3. scheduler.LookupRunByID(runId)      → run-progress: serve from the SCHEDULER's runtime; NEVER adopt
                                       → terminal / administrative: run_wrong_owner (Decision 3's table)
4. run-progress / terminal             → adopt into the IPC registry (singleflight, Decision 4)
   administrative                      → adopt an EXISTING snapshot via the same singleflight
                                          (LastSeen untouched); nothing on disk → run_not_found
```

Step 3's run-progress arm is what keeps "the two registries stay separate" true.
Without it,
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

`RuntimeState` owns the run's _content_ behind its own mutex; `runEntry` owns
the registry's _bookkeeping_ behind the server's. Two fields are spelled
`terminal` and two are spelled `abandoned`, so this section names **who sets
each one and who reads it** — the first draft left that unstated, and F27 walked
through the gap.

| Field                                               | Owner / lock                                 | Persisted | Written by                                                                                                                                               | Read by                                                                  |
| --------------------------------------------------- | -------------------------------------------- | --------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| `RuntimeState.RunID`                                | immutable after construction; no lock needed | yes       | `NewRuntimeState` only                                                                                                                                   | everything                                                               |
| `RuntimeState.Terminal` / `TerminalAt`              | `rs.mu`                                      | **yes**   | `markTerminalLocked`, claim step 1c                                                                                                                      | `persistLocked`/`snapshotLocked`, adoption, the reconciler, the gate CLI |
| `RuntimeState.Abandoned` / `AbandonedAt`            | `rs.mu`                                      | **yes**   | `markAbandonedLocked`, from `abandonRun` (7.1) — always on the LIVE object, because the administrative path adopts before it writes (F33); no clear path | `persistLocked`, the reconciler, the issue index                         |
| `RuntimeState.PID`                                  | `rs.mu`                                      | **yes**   | `SetProcess` (scheduler), `setStageChildLocked` (extension path, 7.2)                                                                                    | ladder arm 3, in a process with no registry                              |
| `RuntimeState.sealed`                               | `rs.mu`                                      | no        | `SealAndRemove`, claim step 4                                                                                                                            | `persistLocked` only, inside its own critical section                    |
| `runEntry.terminal`                                 | `runtimesMu`                                 | no        | claim step 1c                                                                                                                                            | resolution (admission control) only                                      |
| `runEntry.abandoned` / `AbandonedAt`                | `runtimesMu`                                 | no        | `abandonRun`, mirroring `rs`                                                                                                                             | the derived issue index's ranking (Decision 6)                           |
| `runEntry.FirstSeen` / `LastSeen` / `*RuntimeState` | `runtimesMu`                                 | no        | every accepted **run-progress or terminal** call; **never** an administrative one (Decision 4)                                                           | the lease (7.2), the index                                               |
| `adopting map[runID]*flight`                        | `runtimesMu`                                 | no        | the adoption singleflight (Decision 4)                                                                                                                   | the adoption singleflight                                                |
| `closedRuns` (FIFO ring, cap 1024)                  | `runtimesMu`                                 | no        | claim step 3; adoption on a terminal snapshot                                                                                                            | Decision 3 line 4                                                        |

The registry pair and the content pair are **not copies of one fact**: the
`runEntry` half is admission control for _new_ resolutions and dies with the
process; the `RuntimeState` half travels with the object, is marshalled into
every byte the run writes, and is what a fresh process, the gate CLI and the
reconciler read. That is why the claim sets both (step 1c) and why an in-flight
`Persist` cannot write a lie.

**Lock order is `runtimesMu` → `rs.mu`, or `schedulerMu` → `rs.mu`, and the two
registry locks are never held together.** `runEntry` fields are never read under
`rs.mu` — the rule that keeps the order acyclic and that also dissolves F20's
two-locks-one-comparator hazard, a torn read a sequential `-race` test would not
catch. The per-symbol form of this — which method takes which lock, and which
have `…Locked` twins — is Decision 5's
[lock-discipline table](#the-lock-discipline-as-a-table), and C16 makes naming
the twin a requirement of any normative sequence rather than an implementation
detail.

---

## Assumptions

Assertions this ADR relies on that **cannot be verified from this tree**. Each
carries a verification task and the design that applies if it turns out false —
so the ADR stays decidable either way rather than deferring to a discovery made
during implementation.

### A-1 — the platform's `pipeline_runs` row is idempotent by `run_id` and last-writer-wins on outcome

**Where it is load-bearing.** 7.1's `abandonRun` emits a terminal
`pipeline_done(success=false)` for a dispatch that may still be alive, on the
theory that the run's own later `notifyComplete` supersedes it and the row
converges on the truth. R-3's false-positive lease expiry leans on the same
property, as does the reconciler's emit-then-remove idempotency across
activations.

**Why it is unverified.** The platform is the closed-source companion service
and is not in this repository. `#1047` materialises a `running` row from the
first `stage_started` event keyed by `run_id`, which is consistent with — but
not proof of — last-writer-wins on a later terminal event.

**Verification task (blocks the step that ships `abandonRun`).** Confirm against
the platform's ingestion contract that (a) two `pipeline_done` events for one
`run_id` update one row rather than creating two, and (b) the **later** event's
outcome wins. Filed as a follow-up issue (see below) and named as a precondition
on **the plan step that ships `pipeline.abandonRun`** — step 6 in the current
[Implementation tracking](#implementation-tracking) order. The gate follows the
verb, not the number.

**If A-1 is false** — i.e. the platform treats the first terminal event as final
— the design changes as follows, and only here: **`abandonRun` stops emitting
`pipeline_done` at abandon time.** It still marks the dispatch abandoned locally,
still stamps the durable marker, still frees the bookkeeping, still returns
`no_run` where there is nothing to abandon. The run's platform row is then
closed by exactly one of two things: the run's own `notifyComplete` (the #307
case — a wedge that unwedges), or the reconciler at lease expiry (7.2), which is
the existing #44 mechanism and already the path for "the run went silent and
never came back". The cost of that variant is that a genuinely-dead abandoned
run's row stays `running` for up to `LIVENESS_WINDOW` instead of closing
immediately; the cost of guessing wrong in the other direction is a permanently
wrong row for every wedge that recovers. Nothing else in this ADR moves.

### A-2 — `rename(2)` within one `.nightgauge/pipeline/` directory is atomic and exclusive on every supported platform

**Where it is load-bearing.** Decision 9's consume-on-claim is the whole of C15.

**Status.** POSIX `rename(2)` is specified atomic within a filesystem, and a
rename whose **source** does not exist fails `ENOENT`; Node's `fs.rename` maps
to `MoveFileEx` on Windows with the same source-missing behaviour. The
assumption that can fail is not the primitive but the **premise that both files
are on one filesystem** — which holds because the claim artifact is written into
the same directory as its source. A workspace whose `.nightgauge/pipeline/`
straddles a mount point cannot exist by construction.

**What `rename(2)` does NOT do, stated here because a rule was built on the
opposite belief.** It updates the file's `st_ctime` and both directories'
`st_mtime`; it leaves the renamed file's own **`st_mtime` unchanged**. The claim
artifact therefore carries the mtime of the paused snapshot — typically hours or
days old — which is why Decision 9 ages a claim from the timestamp inside the
claim token and never from the file (F34, C17). This is a property of the
primitive, not an assumption: it needs no verification task, only a place in the
document where an implementer will meet it.

**If A-2 is false** for some future storage backend (a network filesystem
without atomic rename), the fallback is `open(O_CREAT|O_EXCL)` on a separate
`.claim` file — a weaker artifact, because the snapshot then still exists under
its canonical name and a reader that ignores the claim file is unprotected. That
is why rename is the primary design and not a convenience.

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

| Test                                                                                                                          | Must show                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| ----------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TestRunIdentity_AbandonedRunStillBooksItsOwnCompletion` (from `TestProbe_ForceClearedRunResurrectsAfterSuccessorCompletes`)  | After `abandonRun`, the same run's late `pr-create running` + `notifyComplete{prMerged:true}` are **accepted**, produce exactly **one** run record and **one** learning-corpus row under that run's own id, and its `pipeline_done` supersedes the abandon event. A **successor** run of the same issue is untouched. Covers F1, F4, F18.                                                                                                                                                                                                            |
| `TestRunIdentity_SuccessorWithoutInitializedRecordsNormally` (from `TestProbe_SuccessorWithoutInitializedIsLockedOutForever`) | A run whose first message the server never saw has **every** subsequent transition accepted by adoption, and its completion writes exactly one record, one outcome, and full telemetry. This is the test that fails against the refuted design (F16) and must never be deleted.                                                                                                                                                                                                                                                                      |
| `TestRunIdentity_AbandonRunNeverCreates`                                                                                      | `abandonRun` for an id with no entry **and** no snapshot returns `no_run`, writes nothing, adds nothing to `closedRuns`, emits no `pipeline_done`, and a subsequent first message from that id is served normally. Covers the `bookForceClearedReservation` case.                                                                                                                                                                                                                                                                                    |
| `TestRunIdentity_AbandonRunRequiresMatchingRepoAndIssue`                                                                      | A mismatched `repo` or `issueNumber` yields `run_not_found` and mutates nothing; no `pipeline_done` with `IssueNumber: 0` is ever emitted. Covers C8.                                                                                                                                                                                                                                                                                                                                                                                                |
| `TestRunIdentity_ZombieCannotMutateSuccessor`                                                                                 | Interleaving two run ids on one issue: the successor's `TotalCostUSD`, per-stage tokens, `StageErrors`, `PhaseHistory` and `RunRecord` are byte-identical to a solo run. Covers F2, F4, F10.                                                                                                                                                                                                                                                                                                                                                         |
| `TestRunIdentity_TerminalDeleteIsIdentityChecked`                                                                             | A successor's entry installed during `notifyComplete`'s unlocked window survives: its registry entry is intact and its snapshot file still exists on disk. Covers F5 and C7.                                                                                                                                                                                                                                                                                                                                                                         |
| `TestRunIdentity_TerminalSnapshotIsNeverRehydrated`                                                                           | A terminal-marked snapshot left on disk (removal failed) makes a later call `run_closed`, not an adoption; no record is written and the authoritative index entry for `run:<id>` is unchanged. Covers the R-4 interleaving.                                                                                                                                                                                                                                                                                                                          |
| `TestRunIdentity_TerminalRemovalUsesSnapshotRepo`                                                                             | A `notifyComplete` whose `repo` param differs from the run's persisted repo still removes the correct file and leaves no other repo's file touched.                                                                                                                                                                                                                                                                                                                                                                                                  |
| `TestRunIdentity_CrossRepoSameIssueNumberDoNotCollide`                                                                        | Repo A #42 and repo B #42, no force-clear involved, keep separate runtimes, snapshots and records. Covers F8.                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `TestRunIdentity_SetPausedNeverInventsARuntime`                                                                               | `setPaused` for a closed id errors `run_closed`; for an unknown id with **no** snapshot errors `run_not_found` and writes **no file**; for an unknown id **with** a snapshot adopts that snapshot through the singleflight and pauses it, leaving `LastSeen` at zero. Covers F9.                                                                                                                                                                                                                                                                     |
| `TestRunIdentity_InvalidRunIdIsRejectedBeforeUse`                                                                             | Table-driven over `../`, `/`, `%2e%2e`, uppercase, a UUIDv4, and a 36-char non-UUID: every one returns `run_id_invalid`; an **empty** id returns `run_id_required`. In no case is a file created, read or removed anywhere under the state dirs. Pins Decision 1's regex to Decision 8's discovery regex as the same constant.                                                                                                                                                                                                                       |
| `TestRunIdentity_ClosedRunIsRefusedOnEveryRunProgressMethod`                                                                  | Table-driven across the four run-progress methods: each returns `run_closed` as a JSON-RPC **error**, and mutates nothing.                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `TestRunIdentity_SchedulerRunIsServedNotAdopted`                                                                              | A `notifyStageProgress` / `notifyPhaseTransition` carrying a **live scheduler** run's id creates **no** entry in `activeRuntimes`, records onto the scheduler's runtime, and does not become "current" in the issue index. Covers the PipelineBridge path and Decision 11.                                                                                                                                                                                                                                                                           |
| `TestRunIdentity_PhaseTransitionSchedulerArmIsIdentityGated`                                                                  | A phase event whose `runId` does not match the scheduler's registered runtime for that issue records **nothing** in that runtime's `PhaseHistory`. Covers the second arm of F10.                                                                                                                                                                                                                                                                                                                                                                     |
| `TestOrphanReconcile_ClosesAbandonedRunAtRootSwitch`                                                                          | With an abandoned entry in the registry, the `workspace.setRoot` call site emits `pipeline_done` and removes the snapshot — the case that is dead on main (F11). The fresh-start case continues to pass unchanged.                                                                                                                                                                                                                                                                                                                                   |
| `TestOrphanReconcile_LiveSchedulerRunIsNotReconciled`                                                                         | A live Go-scheduler run's snapshot is **skipped** at `workspace.setRoot`: no `pipeline_done`, file intact. Verified failing against `main`, where it is reconciled. Covers F21.                                                                                                                                                                                                                                                                                                                                                                      |
| `TestOrphanReconcile_PausedAndIdentityLessSnapshotsStillSkipped`                                                              | C1/C5 preservation: paused snapshots and snapshots with no identity are skipped, not reconciled, not deleted — until the 14-day cap, which does remove a paused one.                                                                                                                                                                                                                                                                                                                                                                                 |
| `TestOrphanReconcile_LiveLeaseIsNotReconciled`                                                                                | A run whose `LastSeen` is inside the window is skipped; the same run outside the window is reconciled.                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `TestOrphanReconcile_RunsAndRemovesWithoutAnalytics`                                                                          | With `analyticsSvc == nil`, the scan still runs, every removal rule still fires, and no event is emitted. Covers F24.                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `TestOrphanReconcile_StartupDefersAndReEvaluates`                                                                             | A snapshot present at `Server.Run` whose run re-asserts during the grace window is **not** emitted for and **not** removed; one that stays silent is reconciled at expiry. Verified failing against `main`, where the sweep runs inline. Covers **F26**.                                                                                                                                                                                                                                                                                             |
| `TestOrphanReconcile_LivePidIsNotReconciled`                                                                                  | A snapshot whose recorded `PID` names a live process is skipped after the grace window with both registries empty; the same snapshot with a dead PID is reconciled. Run for **both** populations — a scheduler runtime (`SetProcess`) and an extension runtime whose PID arrived as `stagePid` on a `running` transition. Covers ladder arm 3.                                                                                                                                                                                                       |
| `TestOrphanReconcile_SilentExtensionRunInLongCiSurvivesGraceExpiry`                                                           | The surviving F26 trace end to end: an extension-path run whose last stage transition persisted more than `LIVENESS_WINDOW` ago, emitting no tokens for the whole grace window, with both registries empty at expiry. It is **not** emitted for and **not** removed, because its `stagePid` names a live process. Verified failing against a tree without the `stagePid` field, where every ladder arm is false. Covers **F32** and C18.                                                                                                             |
| `TestRunIdentity_StagePidIsClearedAtStageEnd`                                                                                 | A terminal stage transition carrying `stagePid: 0` clears the snapshot's `PID`, so a finished child's recycled PID cannot vouch for the run; the same transition still persists, so arm 4 is fresh. Bounds the PID-reuse window to one stage.                                                                                                                                                                                                                                                                                                        |
| `TestOrphanReconcile_FreshAbandonedSnapshotSurvives`                                                                          | An `abandoned` snapshot whose `abandoned_at` and mtime are inside the window is **neither** emitted for **nor** removed, and a stage transition arriving afterwards still finds its file. The same snapshot past the window is removed without emitting. Pins 7.4's two abandoned rows.                                                                                                                                                                                                                                                              |
| `TestOrphanReconcile_ResumingArtifactAgesFromTheClaimToken`                                                                   | **Constructed so the mtime and the claim token disagree**: an artifact whose file mtime is days old but whose `claimToken` was minted 2s ago is **not** released; the same artifact with a token older than the grace window **is**. Verified failing against an mtime-based implementation — this is the row that would have passed while the guard protected nothing. Covers **F34** and C17.                                                                                                                                                      |
| `TestOrphanReconcile_ReleaseNeverOverwritesAnOccupiedCanonicalName`                                                           | With both `runtime-{issue}-{runId}.json` (live, post-resume) and a stale-token `resuming-*` present, the reconciler removes the **artifact** and leaves the canonical file byte-identical; with canonical absent it renames back and the pause survives to the next activation. Covers Decision 9's release table and the crashed-claim path.                                                                                                                                                                                                        |
| `TestOrphanReconcile_UnparseableOrFutureClaimTokenIsTreatedAsLive`                                                            | A `resuming-*` whose token does not parse, and one whose embedded time is beyond `CLAIM_SKEW_TOLERANCE` in the future, are both left untouched and logged once. Pins the fail-safe direction (C13).                                                                                                                                                                                                                                                                                                                                                  |
| `TestRunIdentity_TerminalVerbAgainstSchedulerRunIsRefused`                                                                    | `notifyComplete`, `abandonRun` and `setPaused` carrying a **live scheduler** run's id each return `run_wrong_owner`, write **no** V2 record, **no** learning row, **no** `abandoned` stamp, and emit **no** `pipeline_done`; the scheduler's own `OnPipelineComplete` still writes exactly one record. Covers **F29** and C4.                                                                                                                                                                                                                        |
| `TestRunIdentity_ConcurrentAdoptionYieldsOneRuntime`                                                                          | N goroutines calling run-progress verbs for one unknown id concurrently produce exactly **one** `*RuntimeState`, one registry entry, and one snapshot containing **every** goroutine's stage; run under `-race`. Covers **F30** and C14.                                                                                                                                                                                                                                                                                                             |
| `TestRunIdentity_AdministrativeResolutionInstallsAnEntryWithoutVouching`                                                      | `abandonRun` (and `setPaused`) for an id with a snapshot and no entry installs **one** entry via the singleflight with `LastSeen` at the zero time; a concurrent `notifyStageTransition` for that id shares the same `*RuntimeState`, and no stage it booked is lost; `skipRun` is **false** for that snapshot immediately afterwards. Run under `-race`. Covers **F33** and the F9 pin it must not re-create.                                                                                                                                       |
| `TestRunIdentity_AdministrativeMarkerSurvivesTheRunsNextPersist`                                                              | The F33 interleaving: `abandonRun` resolves and stamps while the run unwedges and persists. Exactly one `abandoned: true` snapshot exists afterwards, the run's later stages are still present in it, and the reconciler emits **no** second `pipeline_done`. Verified failing against a detached-copy implementation.                                                                                                                                                                                                                               |
| `TestRunIdentity_InFlightPersistCannotResurrect`                                                                              | A `Persist` held mid-flight across the terminal claim either lands **before** the seal (overwritten and removed) or writes `terminal: true`; in no interleaving does a non-terminal `runtime-{issue}-{runId}.json` exist after the claim returns, and a post-seal `Persist` returns `ErrRunSealed`. Run under `-race`. Covers **F27**.                                                                                                                                                                                                               |
| `TestRunIdentity_ExecutionPathReplayIsInsideTheClaim`                                                                         | A `notifyComplete` carrying `StageExecutionPaths` / `StagePuntReasons` produces a V2 record with `execution_path` and `punt_reason` stamped on its stage records — the #309 replay is not refused by the latch. Pins claim step 1b.                                                                                                                                                                                                                                                                                                                  |
| `TestRunIdentity_ClaimSequenceIsDeadlockFree`                                                                                 | The whole of Decision 5 executed literally, with a watchdog: N concurrent `notifyComplete`s, run-progress calls, derived-index scans and reconciler passes over one repo, under `-race`, with a hard timeout that **fails the test rather than hanging CI**. A build-tag-guarded variant asserts that the claim path calls no exported `RuntimeState` method between `runtimesMu.Lock` and its `Unlock`. Verified failing against a literal transcription of the second revision's sequence, which wedges on the first call. Covers **F36** and C16. |
| `TestRunIdentity_ClosedRunsEvictionFallsBackToTheDurableMarker`                                                               | With the ring forced past its cap, a late duplicate whose terminal snapshot survives is `run_closed`; one whose snapshot was removed adopts empty, its skeleton record is dropped by the richer-upgrade rule and its learning row by the corpus dedup, leaving exactly one spurious `pipeline_done`. Pins Decision 4's late-duplicate table.                                                                                                                                                                                                         |

### Go — `internal/state`, `internal/learning`, `internal/orchestrator`

| Test                                                   | Must show                                                                                                                                                                                                                                                                                                   |
| ------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TestPersist_RefusesEmptyRunID`                        | No `runtime-42-.json` is ever created; the call errors.                                                                                                                                                                                                                                                     |
| `TestAdoption_RehydratesFromSnapshot`                  | An unknown id whose non-terminal snapshot exists on disk adopts with its full stage history, not an empty runtime.                                                                                                                                                                                          |
| `TestGateRecord_NeverCreatesASnapshot`                 | `AppendStageGateResultToDisk` with no existing file records nothing and errors loudly; with a `terminal` file it records nothing; `PersistExisting` fails on a removed file. Covers F22.                                                                                                                    |
| `TestHistory_TwoRunsOfOneIssueProduceTwoRecords`       | Covers F6 and F7, including two dispatches starting within one UTC second.                                                                                                                                                                                                                                  |
| `TestLearningRecorder_RecordIsIdempotentByRunID`       | Two `Record` calls for one run id append one row, **including across a process restart** (the corpus is the durable dedup). Explicit replacement for the deleted "the runtime is deleted so a repeat records nothing" guarantee (C2).                                                                       |
| `TestScheduler_MintsLocallyIgnoringRemoteRunID`        | A queue item carrying a `RemoteRunID` produces a locally-minted `RunID`; the remote id is carried as a correlation attribute only. Covers Decision 2's reversal.                                                                                                                                            |
| `TestSidecar_CarriesRunID`                             | `current-run.json` written by the scheduler contains `run_id` matching the runtime snapshot's filename component.                                                                                                                                                                                           |
| `TestPersist_HoldsTheRunMutexAcrossTheWrite`           | A concurrent mutation cannot interleave between `Persist`'s marshal and its `AtomicWriteFile`; a sealed runtime's `Persist` writes nothing and returns `ErrRunSealed`. Run under `-race`. Covers the F27 mechanism at its own layer.                                                                        |
| `TestRuntimeState_ExportedMethodsWrapTheirLockedTwins` | Every symbol in Decision 5's lock-discipline table has a `…Locked` body and a two-line exported wrapper; calling the `…Locked` form without the mutex is caught by `-race`, and calling the exported form from inside the mutex is caught by the watchdog above. Pins C16 at the layer that owns the mutex. |
| `TestIpcStageRunner_RefusesAnEmptyRunID`               | `RunStage` with an empty `RunID` returns an error **without** emitting `pipeline.runStage` and without spawning anything; the scheduler's ordinary path always supplies one. Pins Decision 10's step-0b assertion.                                                                                          |

### TypeScript — `packages/nightgauge-vscode/tests/`

| Test                                                                                  | Must show                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| ------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ConcurrentPipelineManager.terminalDoubleBook.test.ts` (PROBE-X / PROBE-Y, committed) | The pre-fix sequences `after force-clear: failed=1 completed=0 \| final: failed=1 completed=1` and `afterForceClear=1 final onSlotFailed=2` become `failed=1 completed=0` at **both** checkpoints, with `onSlotFailed` called exactly once. Covers F18 and pins C10.                                                                                                                                                                                                                                  |
| `ConcurrentPipelineManager.runIdentity.test.ts`                                       | The minted `runId` reaches the slot, the reservation, the `PipelineStateService`, and `pipeline.abandonRun` on force-clear; `forceClearedRunIds` holds run ids and has no release path; the funnel calls `abandonRun` and **never** `notifyComplete`.                                                                                                                                                                                                                                                 |
| `PipelineStateService.identityIsNotAmbient.test.ts`                                   | `beginRun` on a service holding a live identity throws and mutates nothing; `retryFailedIssue` against an in-flight **same** issue surfaces the error instead of relabelling the live run. Covers F23.                                                                                                                                                                                                                                                                                                |
| `PipelineStateService.stateChangedRouting.test.ts`                                    | A `stateChanged` for a **different** run id on the **same** issue is ignored; one with an **empty** run id is still applied via the issue pre-filter. Covers F19 and Decision 6's fallback.                                                                                                                                                                                                                                                                                                           |
| `PipelineBridge.identity.test.ts`                                                     | Both raw IPC calls carry `ipcParams.runId`; neither is rejected; the ≥5s progress cadence produces no rejection log.                                                                                                                                                                                                                                                                                                                                                                                  |
| `CliPipelineReconciliationService.test.ts` (extended)                                 | A sidecar carrying `run_id` discovers `runtime-{issue}-{runId}.json` and fires `onDiscovered`; a sidecar without `run_id` is skipped **with a log**, not silently. Covers F25.                                                                                                                                                                                                                                                                                                                        |
| `pauseRestore.test.ts`                                                                | Two paused snapshots for one issue produce **one** QuickPick listing both by run id and `started_at`; resuming threads the parsed `runId` into `beginRun` + `runPipeline`; the un-chosen file is discarded, not left to re-prompt. Covers C5.                                                                                                                                                                                                                                                         |
| `pauseRestore.exclusiveClaim.test.ts`                                                 | Two hosts racing one paused snapshot: exactly **one** rename succeeds and exactly **one** `runPipeline` is called; the loser gets `ENOENT`, logs once, deletes **nothing**, and never calls `beginRun`. The winner's artifact name carries a **freshly minted UUIDv7 claim token**, not the host's session id, and the claimant logs `(runId, claimToken, sessionId)` so the two remain joinable. A claim artifact left by a killed host is released and re-prompts. Covers **F28**, **F34** and C15. |
| `attentionRaise.identity.test.ts`                                                     | A run-scoped raise carries the **installed** identity (not a file read), corroborates, and keeps #305's raise-and-retry ceiling option; `readCurrentRunId` no longer exists; a run-scoped raise with an empty id errors instead of failing corroboration silently. Covers **F31**.                                                                                                                                                                                                                    |
| `runtimeStubSweep.test.ts` (extended)                                                 | Legacy `runtime-\d+.json` files are **left untouched** by the TS sweep (Go owns them); identity-less new-scheme files classify as `delete`; paused, identified files classify as `keep`.                                                                                                                                                                                                                                                                                                              |
| `IpcClient.protocolMismatch.test.ts`                                                  | A mismatched `ipc.ready` `protocolVersion` disconnects the client, raises the modal, and makes every subsequent `call()` reject with `protocol_mismatch` without touching the socket.                                                                                                                                                                                                                                                                                                                 |

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
dispatch that had not yet reached Go. Decision 3's verb classes are the answer —
four of them, resolved before any registry is consulted.

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

### J — Resume MINTS a fresh identity, carrying the paused one as a predecessor

The other structurally-sound answer to F28: never replay an id, so a redelivered
prompt cannot collide by construction.

**Rejected.** The paused snapshot's stage history, accumulated cost, phase
records, ADR-013 trace file and `run:<id>` ledger key are all keyed to the
existing identity. A fresh id either orphans every one of them or requires a
predecessor→successor mapping to stitch them back — and a mapping is a thing
that can be stale, lost or disagreed about, which is **Alternative E's own
refutation**. It would also make one logical run produce two learning-corpus
rows and two authoritative records, forcing #313's merge rules to arbitrate
something they were never designed for. Decision 9's consume-on-claim gets the
same exclusion from a primitive the OS already makes atomic, and keeps "one run,
one identity, for the run's whole life".

### K — Adoption as one check-load-insert critical section holding `runtimesMu`

The simplest fix for F30: hold the registry lock across the disk read so the
check, the load and the insert cannot interleave.

**Rejected on cost, not on correctness.** `runtimesMu` is server-global and is
also taken by Decision 6's derived index scan and by the reconciler, so this
serialises every unrelated run's handlers behind one run's disk latency — during
the burst of concurrent adoptions that a restart produces, which is exactly the
case the lock is being taken for. Decision 4's per-id singleflight gets the same
exactly-once property with O(map) hold time. Recorded because "just hold the
lock" is the reviewer's first instinct and the tradeoff should be visible.

### L — A durable `closedRuns` journal on disk

Persist the closed-run set so `run_closed` survives a restart and an eviction.

**Rejected.** It is a second writer over run state with its own retention,
corruption and compaction story — the #316 lesson ADR 015 Decision C already
paid for once. The durable fact already exists in the right place: the `terminal`
marker inside the run's own snapshot, written before removal. And the outcome
when neither exists is bounded and specified (Decision 4's late-duplicate table):
one spurious `pipeline_done`, no wrong record, no doubled outcome. Buying
durability for that would cost more than the noise it removes.

### M — A `pipeline.declareLiveRuns` handshake so the server learns which runs survived a restart

Have the reconnecting client send the run ids it believes are live, so the
startup sweep can skip them immediately instead of waiting out a grace window.

**Rejected.** It adds an unauthenticated wire verb whose only effect is to
**suppress reconciliation** — i.e. a primitive for stranding `running` platform
rows, which is the defect #44 exists to prevent (C8 says no new verb may widen
the socket's capability). And it buys almost nothing: the deferred sweep already
distinguishes restart from cold start **behaviourally**, because a live run
re-asserts inside the window and a dead one does not. The entire cost of not
having it is that a genuinely-dead run's row closes 120 seconds later.

### N — `utimes` the claim artifact at claim time instead of decoding the token

The other correct answer to F34: keep an opaque claim token, and have the
claimant explicitly stamp the artifact's mtime (or rewrite the snapshot under the
new name rather than renaming into it) so that "older than `STARTUP_GRACE`"
measures age-since-claim as the rule always intended.

**Rejected, narrowly, and it is the closest call in this ADR.** Three reasons,
in order of weight:

1. **It re-introduces a write where the design has an atomic move.** Rewriting
   under the new name is no longer one atomic operation — `open`/`write`/`close`
   is not exclusive, so C15's guarantee would come from the rename anyway and the
   write would be a second step that can partially fail. A `utimes` after the
   rename is a second syscall that can fail on its own, leaving a claim that is
   valid but reads as ancient — the F34 outcome, now intermittent rather than
   systematic, which is strictly harder to diagnose.
2. **mtime is not ours.** Backup agents, indexers, sync clients and `cp -p`
   rewrite mtimes; a rule that reads mtime is a rule other software can move. The
   token is in the filename, which nothing else in the system writes.
3. **The parse is trivial and total.** A UUIDv7's first 48 bits are big-endian
   Unix milliseconds; the ADR already mandates the format and validates it with a
   regex, so the "decode" is six bytes and a bounds check, with both failure
   modes specified to fail safe.

Recorded rather than dismissed because the reverse choice is defensible: it needs
no timestamp semantics anywhere and keeps Decision 1's "nothing decodes a UUID"
rule absolutely rather than with a carve-out. **If an implementer finds the
UUIDv7 parse unpalatable, this is the sanctioned substitute** — with the same
release table (canonical-occupied never renames back) and the same fail-safe
direction, which is the part that actually closes F34.

### O — A lease touch on the progress verbs, or a periodic keepalive, instead of the extension-side PID

Two variants of the same idea for closing F32: keep arm 4 fresh by touching the
snapshot on every `notifyStageProgress`, or add a periodic verb whose only job is
to say "still here".

**Rejected, and the first one is rejected on evidence rather than taste.** A
lease touch on the progress verbs does not close the surviving F26 trace at all:
that trace's run is in `pr-merge` polling CI and emits **no assistant tokens**,
so it makes no progress calls to touch anything. It would fix a case that was
never the problem while costing a filesystem write every five seconds per run.
The keepalive variant is Alternative M's `declareLiveRuns` at a different cadence
— an unauthenticated verb whose only effect is to suppress reconciliation (C8) —
and it puts the liveness signal on the socket that just restarted, which is the
one channel the F26 population has already lost. The stage child's PID is
evidence the reconciler can check **for itself**, from a fresh process, with no
cooperation from the thing whose liveness is in question. That asymmetry is the
whole argument.

### P — Guard the administrative disk path with an OS file lock instead of adopting

Close F33 by taking an exclusive lock on `runtime-{issue}-{runId}.json` around
the administrative read-modify-write, leaving "administrative verbs never install
an entry" intact.

**Rejected.** It excludes the wrong writers. The race is between a detached copy
and the **live `*RuntimeState`'s** `Persist`, which serialises on `rs.mu` and
knows nothing about a file lock; making `Persist` also take an advisory file lock
would put a filesystem syscall inside the mutex Decision 5 just made hold across
the write. It is also a second exclusion mechanism sitting beside one that
already exists and already has exactly-once semantics per id — and advisory
locking semantics differ across the platforms this product supports. Adoption
gets the property from the machinery already specified: one object per identity,
one mutex, every writer on the same side of it.

---

## Revision history — what design review changed

Recorded because a Decided ADR is the permanent record and several of these
reverse a sentence a reader may have seen. Rows 1–22 are the first review's
reversals; rows 23–32 are the second's; rows 33–38 are the third's.

Rows 33–38 differ in kind from what precedes them, and the difference is worth
naming for anyone reading this as a record of how the design was arrived at:
**every one of them is a defect introduced by a row above it.** The terminal
latch (row 24) produced the self-deadlock; the singleflight (row 28) and the
replay's placement (row 31) contributed to it; the consume-on-claim rename
(row 25) produced the stale-claim release; the deferred sweep and the new ladder
arms (row 23) produced the coverage gap. A fix that is described but not traced
back to the code it will run against is a new defect with better prose, and five
of the six were invisible in this document until someone re-read the cited lines.

| #   | Earlier draft                                                                                                                 | Decided                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| --- | ----------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | `abandonRun` is a full terminal claim, `closedRuns` insert included                                                           | `abandonRun` terminates the **dispatch**; the run stays adoptable and books its own honest completion (7.1, Alternative F)                                                                                                                                                                                                                                                                                                                                                                   |
| 2   | Universal "unknown → adopt"                                                                                                   | Two verb classes: run-progress adopts, administrative resolves-or-refuses (Decision 3, Alternative G)                                                                                                                                                                                                                                                                                                                                                                                        |
| 3   | `abandonRun {runId, reason}`                                                                                                  | `{runId, repo, issueNumber, reason, stage?}`, with all three corroborated against the resolved run (7.1)                                                                                                                                                                                                                                                                                                                                                                                     |
| 4   | Seed `runId` from `RemoteRunID`                                                                                               | Always mint locally; `remoteRunId` is a correlation attribute; the scheduler's `RemoteRunID` preference is deleted (Decision 2, Alternative H)                                                                                                                                                                                                                                                                                                                                               |
| 5   | "A terminal run has no snapshot, so the file's presence is evidence"                                                          | A **durable** `terminal` marker is stamped before removal; adoption refuses a terminal snapshot; removal uses the snapshot's own repo (Decision 4)                                                                                                                                                                                                                                                                                                                                           |
| 6   | Terminal latch guarantees no resurrection                                                                                     | Restated as in-process; the gate CLI's create-on-miss is **deleted** and it writes through `PersistExisting` (Decision 5)                                                                                                                                                                                                                                                                                                                                                                    |
| 7   | Index "current" ranked by `FirstSeen`, "self-correcting"                                                                      | Ranked by `LastSeen`; abandoned entries drop out (Decision 6)                                                                                                                                                                                                                                                                                                                                                                                                                                |
| 8   | `skipRun` consults the IPC registry                                                                                           | Consults **both** registries; `Scheduler.IsRunLive` added (7.2)                                                                                                                                                                                                                                                                                                                                                                                                                              |
| 9   | Paused snapshots exempt from all retention                                                                                    | Exempt from reconciliation while fresh; **subject to the 14-day cap** (7.4)                                                                                                                                                                                                                                                                                                                                                                                                                  |
| 10  | Retention/reconciliation as-is                                                                                                | Emission and removal split so both run on a local-only workspace (7.4)                                                                                                                                                                                                                                                                                                                                                                                                                       |
| 11  | `NIGHTGAUGE_RUN_ID` consumed, exporter unnamed                                                                                | Three exporters named and sequenced first; the gate CLI also gets `--run-id` explicitly from `HeadlessOrchestrator` (Decision 8)                                                                                                                                                                                                                                                                                                                                                             |
| 12  | Producer table omits `PipelineBridge`, the raw progress sites, and the CLI reconciler                                         | All enumerated; `PipelineBridge` uses the `ipcParams.runId` it already receives (Decision 10)                                                                                                                                                                                                                                                                                                                                                                                                |
| 13  | "Every producer mints at its own dispatch point"                                                                              | Plus: identity is not ambient — `beginRun` refuses to overwrite a live identity (Decision 10)                                                                                                                                                                                                                                                                                                                                                                                                |
| 14  | Pause-restore = a new regex                                                                                                   | Full migration: parsed id, one QuickPick per issue, id threaded into resume, `setPaused` resolvable from disk (Decision 9)                                                                                                                                                                                                                                                                                                                                                                   |
| 15  | Legacy files swept by both Go and TypeScript                                                                                  | Go owns legacy disposition exclusively; the TS sweep's filter is narrowed (Migration)                                                                                                                                                                                                                                                                                                                                                                                                        |
| 16  | "The TypeScript client already validates `protocolVersion` and rejects a mismatch"                                            | It does not — this ADR specifies the disconnect + modal + `protocol_mismatch` hard-fail (Migration)                                                                                                                                                                                                                                                                                                                                                                                          |
| 17  | "An error rejects the promise, so every existing try/catch at minimum logs it"                                                | False — five bare catches named; rejection observability is Go-side, and the catches gain a `warn` (Decision 3, Migration)                                                                                                                                                                                                                                                                                                                                                                   |
| 18  | #375 `IdempotencyKey` becomes run-scoped                                                                                      | Key unchanged; the run id rides as a payload field and trace ref (Decision 9, Alternative I)                                                                                                                                                                                                                                                                                                                                                                                                 |
| 19  | `notifyPhaseTransition` "structurally" fixed                                                                                  | Its second, scheduler-keyed arm is identity-gated (Decision 11)                                                                                                                                                                                                                                                                                                                                                                                                                              |
| 20  | `runId` format mandated but unvalidated                                                                                       | Validated at the wire boundary against the same constant the discovery regex uses (Decisions 1, 8)                                                                                                                                                                                                                                                                                                                                                                                           |
| 21  | #305 corroboration "unchanged"                                                                                                | Explicit multi-run selection rule: match run id + repo + issue, else corroboration fails (Decision 9)                                                                                                                                                                                                                                                                                                                                                                                        |
| 22  | Failure catalogue and constraints cited but undefined                                                                         | Both inlined as tables above                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| 23  | "Both call sites then work: `Server.Run` (fresh process, empty IPC registry…)"                                                | **False** — the client auto-restarts the backend under a surviving host, so the startup sweep closed live runs. `Server.Run` now **defers** 120s and re-evaluates; the ladder gains PID and file-age arms (F26, 7.2/7.3)                                                                                                                                                                                                                                                                     |
| 24  | `Terminal` on `runEntry` only, "the entry refuses every further `Persist`"                                                    | It could not — `Persist` is a `RuntimeState` method. `Terminal` is now a **persisted `RuntimeState` field**, `Persist` holds `rs.mu` across the write and refuses a **sealed** runtime (F27, Decisions 5 and 12)                                                                                                                                                                                                                                                                             |
| 25  | Resume "continues under the snapshot's own identity, so the snapshot is consumed"                                             | Never mechanised — two hosts could resume one id. Resume now requires winning an atomic **rename** (`resuming-{issue}-{runId}.{token}.json`) (F28, C15, Decision 9)                                                                                                                                                                                                                                                                                                                          |
| 26  | `abandoned` durable "in the file"; `skipRun` excluded abandoned runs                                                          | Ambiguous and destructive both ways. `abandoned` is a **`RuntimeState` field** re-stamped by every `Persist`, with no clear path, and it is **removed from the liveness predicate** (F4 of review 2, 7.1/7.2/7.4)                                                                                                                                                                                                                                                                            |
| 27  | Verb class branched at step 4 of Decision 11's order                                                                          | Class is decided **before** any registry is consulted; terminal and administrative verbs against a scheduler-resolved run are `run_wrong_owner` (F29, Decision 3's per-class table)                                                                                                                                                                                                                                                                                                          |
| 28  | Adoption "creates the entry" and separately "reads the snapshot"                                                              | Two concurrent adoptions could build two runtimes. Specified as a **per-id singleflight** with the I/O outside the lock (F30, C14, Decision 4; Alternative K records the rejected whole-lock form)                                                                                                                                                                                                                                                                                           |
| 29  | `PipelineBridge` "already receives `ipcParams.runId`"                                                                         | True, but the value is `omitempty` and empty when `Runtime` is nil. `StageRunParams.RunID` becomes explicit and **asserted at the emitter** in plan step **0b**, before the verbs require it (Decision 10)                                                                                                                                                                                                                                                                                   |
| 30  | `attention.raise`'s `RunID` becomes a required selector                                                                       | …with a guesser as its only producer. `readCurrentRunId` is **deleted** and the installed identity is threaded, preserving #305's ceiling option (F31, Decision 9)                                                                                                                                                                                                                                                                                                                           |
| 31  | #309's execution-path replay unplaced in the claim sequence                                                                   | It is **step 1b**, inside the critical section and before the latch — the only mutation the latch admits (Decision 5)                                                                                                                                                                                                                                                                                                                                                                        |
| 32  | `closedRuns` "LRU-capped with a durable on-disk backing"                                                                      | **Withdrawn.** It is a volatile **FIFO ring, cap 1024**; the durable authority is the snapshot's `terminal` marker, and the late-duplicate outcome under eviction is tabulated (Decision 4; Alternative L records the rejected journal)                                                                                                                                                                                                                                                      |
| 33  | Claim step 1 "under `runtimesMu`, and (for 1b–1d) `rs.mu`", naming `rt.RecordExecutionPath`, `rt.MarkTerminal`, `rt.Snapshot` | **Unimplementable — it self-deadlocks.** Those methods take `rs.mu` themselves and Go mutexes are not reentrant. The sequence now names `…Locked` variants, adoption runs as **step 0 outside every lock** with `entry.terminal` re-checked after re-acquiring, and Decision 5 carries a **lock-discipline table** covering all three mutexes (F36, C16)                                                                                                                                     |
| 34  | Administrative verbs "resolve read-only from the on-disk snapshot; never create an entry"                                     | The modal path was an **unsynchronised whole-snapshot read-modify-write** on a detached copy: a concurrent adoption erases `abandoned`, and `setPaused` rolls back stages booked between its read and its write. Administrative verbs now **adopt an existing snapshot through the same singleflight** — never adopt-empty — and Decision 4 states that administrative adoption leaves `LastSeen` untouched, so it never vouches for liveness (F33)                                          |
| 35  | The claim artifact is aged by file mtime — "`resuming-*` older than `STARTUP_GRACE`"                                          | `rename(2)` preserves mtime, so every claim is **born stale** and releasable under a live claimant, which reopens F28 through the rule added to close it. `claimToken` becomes a **UUIDv7 minted at claim time**; the release parses the claim time out of the filename, tolerates 60s of forward skew, fails safe in both directions, and **never renames back over an occupied canonical name** (F34, C17; Alternative N records the `utimes` variant)                                     |
| 36  | "`notifyStageProgress` refreshes arms 1 **and 4**" / "`SetProcess` records the stage child"                                   | Both false for the extension population: progress never persists (so it refreshes arm 1 only), the cited emitter was the scheduler's, and `SetProcess`' single production caller is the Go execution manager, so every `activeRuntimes` runtime has `PID` 0. The ladder gains a **per-population coverage table**, and `notifyStageTransition` gains **`stagePid`** so arm 3 covers the population it was added for (F32, C18; Alternative O records the rejected lease-touch and keepalive) |
| 37  | Plan: the wire flip (step 3) before the extension identity installation (step 4), "each step independently mergeable"         | The extension side holds **no** identity until `beginRun` exists, so between those merges every extension-path run would be refused `run_id_required` and swallowed — F16 inside the migration. Reordered: wire **shape** (2) → **both** emitter populations assert (0b, 3) → re-key and require (4), with a **per-verb flip gate** and "independently mergeable" restated as "merges green", not "orderable freely" (F35)                                                                   |
| 38  | `abandonRun` bundled into the extension unification step                                                                      | Split into its own step so **A-1's platform verification gates the verb rather than a step number**, and so the force-clear funnel change lands with the verb it calls rather than one merge ahead of it                                                                                                                                                                                                                                                                                     |

---

## Consequences

**The registry becomes bounded.** Entries are evicted at terminal and reaped by
the lease, and `closedRuns` is a **volatile FIFO ring capped at 1024 ids** whose
authority is the snapshot's durable `terminal` marker, not the ring itself
(Decision 4 — the first draft's "durable on-disk backing" is withdrawn, and
Alternative L says why). Today every force-cleared run and every `setPaused`
stub is retained for the life of the server, and `getState` serves the dead
run's snapshot for that issue indefinitely (F12).

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

**This "cross-process residual" claim was briefly false and is now true again.**
The second revision's administrative disk path was a whole-snapshot
read-modify-write between two goroutines of the IPC server itself (F33), so the
residual was not cross-process at all. Routing that path through the adoption
singleflight (Decision 4) puts every writer inside one process on the same
`rs.mu`, which is what makes the scoping sentence above hold. Any future path
that writes a `runtime-*.json` without going through a registered
`*RuntimeState` re-opens it, and the ADR states that as a rule rather than
leaving it to be rediscovered.
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
it will later contradict. The event is keyed by run id, so the platform side
converges **under Assumption A-1**; the local snapshot removal is identity-safe;
and the run re-adopts and re-persists on its next message. Bounded by the window,
by the four other arms of the liveness ladder (a live PID or a fresh file both
veto the expiry), and by `abandonRun` carrying the load in the common case.

**`closedRuns` does not survive a server restart or a ring eviction when the
snapshot was removed cleanly — residual risk R-4.** In that case a
genuinely-closed run's late call adopts rather than being refused, re-creates a
snapshot, and is closed again by the next reconcile — one spurious
`pipeline_done`. It **cannot** double-book: the adopted runtime starts empty
(rehydration from a terminal snapshot is refused, and there is no snapshot when
removal succeeded), so #313's richer-upgrade-only rule drops its skeleton record,
and Decision 9's corpus dedup — which reads the durable corpus, not memory —
drops the duplicate outcome. Only the noise remains, and Decision 4's
late-duplicate table enumerates every case rather than leaving a reader to
derive it.

**The scheduler registry is still issue-keyed — residual risk R-5.** Decision 11
guards its one externally-reachable write site on identity rather than re-keying
it. That is sound because it has a single in-process writer per issue, but it is
a compensating check, which this ADR elsewhere calls the signature of a wrong
key. Re-keying `Scheduler.activeRuntimes` on `RunID` is filed as a follow-up.

**Orphan reconciliation at startup is deferred by two minutes.** A genuinely
dead run's platform row closes 120 seconds later than it does today. That is the
price of C13's bias, it is paid once per activation, and it buys the difference
between "a backend restart is invisible" and "a backend restart destroys every
live run's record" (F26).

**`Persist` becomes a serialising operation on its own run.** Holding `rs.mu`
across the atomic write means one run's mutations queue behind that run's own
file write. Runs do not contend with each other (different mutexes), the write
is a few milliseconds, and the alternative is F27. Stated because it is a
behaviour change to a hot path that a profiler will notice before a reader does.

**An administrative verb can now install a registry entry, and deliberately
cannot make a run look alive.** `setPaused` and `abandonRun` resolving an on-disk
snapshot adopt it rather than rewriting a detached copy, so the entry count grows
by one per administrative call against a restarted server. Those entries carry a
zero `LastSeen`, so they satisfy no ladder arm, pin nothing against #44, and rank
last in the issue index. This is a behaviour change from "administrative verbs
never touch the registry" and is stated because the two rules — "may install
state" and "may never assert liveness" — must be read together or the second one
looks like an oversight.

**Scope. #370 is materially larger than its first draft implied**, and the
growth is recorded rather than discovered during implementation. Beyond the
re-keying itself the ADR now requires: `current-run.json` to carry `run_id` (a Go
sidecar change in `failure_handler.go`); the pause-restore prompt to become a
per-issue QuickPick **with an atomic claim whose token is a freshly minted
UUIDv7**; `RunOptions.RunID` threaded through eight adapter stage-env builders;
`StageRunParams.RunID` explicit and asserted at `IpcStageRunner`; a
`PersistExisting` primitive and a `SealAndRemove` primitive; `Persist` holding
`rs.mu` across its write; **a `…Locked` split across every `RuntimeState` method
the claim sequence names, and a lock-discipline table covering three mutexes**;
an adoption singleflight that now also serves the administrative verbs; **a
`stagePid` field on `notifyStageTransition` plus an `onStageChildSpawned`
callback in `skillRunner`, so the liveness ladder's PID arm exists for
extension-path runs at all**; a deferred startup reconcile; a real
`ProtocolVersion` hard-fail in `IpcClient`; and the deletion of
`readCurrentRunId`. Steps 0, 0b, 2 and 3 exist specifically to sequence the
producers ahead of the consumers that would otherwise be inert or refused.

**One deleted sentence is worth naming.** `server.go:2875-2880` currently reads:
_"Idempotency is inherited, not enforced here … the runtime is deleted at the end
of this handler, so a repeated `notifyComplete` finds none and records nothing."_
That sentence is the only thing standing between the calibration corpus and
silent doubling, and it is an emergent property of a bare-issue delete. This ADR
deletes the property and replaces it with an explicit, tested one (Decision 9).
Any future change that keeps a runtime resolvable past completion must supply
its own replacement, not assume this one.

---

## Follow-up issues (filed at merge)

This ADR names six pieces of work it deliberately does **not** do. They are
listed here with the scope each one carries, so the issues can be filed from
this section rather than reconstructed from prose. None of them gates #370; the
A-1 verification gates one **step** of it, as noted.

| Ref     | Title                                                                        | Scope                                                                                                                                                                                                                                                                                                                                  |
| ------- | ---------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **R-1** | Give the runtime snapshot a single authoritative writer                      | Five writers in three processes share `Persist`'s whole-file last-write-wins contract. After #370 the residual is same-run field loss between the IPC server and `nightgauge gate verify --record`, narrowed to a rename race. Fix: the gate CLI posts its result through IPC when a server is reachable, mirroring #316's discipline. |
| **R-2** | Authenticate the IPC socket                                                  | #370 closes the **addressing** half of ADR 015 §N's residual and explicitly not the **forgery** half: a writer that can reach the socket can still mint an id and drive a run of its own. §N is amended in #370's PR to say which half; this issue closes the other.                                                                   |
| **R-5** | Re-key `Scheduler.activeRuntimes` on `RunID`                                 | Decision 11 guards its one externally-reachable write site on identity instead of re-keying — a compensating check, which this ADR elsewhere calls the signature of a wrong key. Re-keying it also gives scheduler runs a real IPC abandon/pause route, which Decision 3 currently refuses with `run_wrong_owner`.                     |
| **A-1** | Verify the platform's `pipeline_runs` idempotency contract                   | Confirm two `pipeline_done` events for one `run_id` update one row and the later outcome wins. **Precondition on the plan step that ships `pipeline.abandonRun`** (step 6 in the current order); if it fails, apply the A-1-false variant in [Assumptions](#assumptions).                                                              |
| **L-1** | Remove the one-shot Go legacy `runtime-{N}.json` sweep                       | Ships in #370 to avoid stranding phantom `running` rows across the upgrade; lands one release later so it cannot become an open-ended compatibility path.                                                                                                                                                                              |
| **O-1** | Give the five bare `PipelineStateService` catches a shared rejection handler | #370 adds a `logger.warn` to each of `:717, 741, 787, 828, 966`. They should share one handler that classifies the JSON-RPC code, because five copies of a warn will drift. Observability follow-up, not a mechanism.                                                                                                                  |

---

## Implementation tracking

Tracked under issue #370.

**"Independently mergeable" means each step merges green and leaves the tree
working. It does not mean the steps can be ordered freely**, and the second
revision's order was wrong in a way that would have cost a run's entire record
(F35). One rule binds the whole sequence:

> **No step may make a verb hard-require an identity before every emitter
> population that calls that verb supplies one.**

The second revision flipped `run_id_required` in step 3 while the extension side
acquired its identity in step 4 — and `PipelineStateService` holds no run
identity field at all until `beginRun` exists, so between those two merges the
only value it could pass was `""`. Seven raw `notifyStageTransition` calls
(`PipelineStateService.ts:709, 736, 776, 814, 861, 877, 896`) plus
`notifyPipelineComplete` (`:921`) would each have been refused
`run_id_required` and swallowed by the five bare catches this ADR names, so
every extension-path run dispatched in that window writes zero history records,
zero learning outcomes and zero telemetry behind a healthy-looking UI. That is
F16's shape, reached inside the migration that exists to prevent it — and
nothing would have objected except the TypeScript compiler, which a `""` stub
satisfies. The order below puts **both** emitter populations ahead of the flip,
and the flip itself is gated per verb.

**The wire shape and the wire requirement are separated on purpose.** The params
gain `runId` in a step that requires nothing (step 2); the verbs begin refusing
calls without it in a later step (step 4), once every producer sends one. This
is sequencing **within one issue on an unreleased tree**, not a compatibility
shim: there is no version of the product in anyone's hands where `runId` is
optional, no config knob selects the behaviour, and the field is required on the
wire by the end of step 4. The pre-customer no-compat rule is about shipped
surfaces, and this is not one.

0. `NIGHTGAUGE_RUN_ID` exporters: `RunOptions.RunID` through the eight
   `internal/execution/adapters/*.go` stage env builders, and `SkillRunner`'s
   child env. No consumer changes yet — the variable simply starts existing.

   0b. **The SCHEDULER emitter's identity becomes non-optional.**
   `StageRunParams.RunID` explicit and populated from `runtime.RunID` at
   `scheduler.go:3620`; `Runtime` documented non-optional;
   `IpcStageRunner.RunStage` asserts a non-empty id and errors instead of
   emitting; `RunStageParams.RunID` loses `omitempty` on both sides of the wire.
   This covers one of the two emitter populations; step 3 covers the other, and
   **both** must land before the flip in step 4.

1. **The state layer.** `RunID` as a `NewRuntimeState` constructor argument,
   immutable; `Persist` refuses an empty identity, **holds `rs.mu` across marshal
   - write**, and refuses a sealed runtime; `MarkTerminal` / `MarkAbandoned` /
     `SetStageChild` / `SealAndRemove` / `PersistExisting`; **the `…Locked` split**
     for every symbol in Decision 5's
     [lock-discipline table](#the-lock-discipline-as-a-table) (a pure refactor —
     each exported method becomes a `Lock`/`defer Unlock` wrapper over its
     `…Locked` body, so the two forms cannot drift, C16); the shared identity regex
     and the `resuming-*` claim-artifact regex (both components identities); the
     new filename and the `LoadPersistedState` / `FindPersistedStatesForIssue`
     split; the durable `terminal` / `abandoned` `RuntimeState` fields; the gate
     CLI's create-on-miss deleted; `current-run.json` gains `run_id` (Go only).
2. **The wire gains the identity; nothing requires it.** `runId` added to all six
   `pipeline.*` param types, `repo` + `issueNumber` added to the two
   administrative verbs, `stagePid` added to `notifyStageTransition` (7.2),
   codegen and TypeScript client regen. The server **accepts and ignores**
   `runId`; resolution is unchanged and still issue-keyed; `ProtocolVersion`
   unchanged; **no verb refuses anything**. Zero behaviour change, and it is what
   lets step 3 send an identity before step 4 demands one.
3. **The EXTENSION emitter's identity becomes real** — the population step 0b
   does not cover. The generation becomes the run id; `remoteRunId` rename;
   **`beginRun`/`endRun` on `PipelineStateService`** with the not-ambient refusal;
   the four raw IPC call sites given identities; the seven
   `notifyStageTransition` sites and `notifyPipelineComplete` sending the
   installed id; `stateChanged` / phase-event routing by run id with the
   empty-id fallback; `onStageChildSpawned` → `stagePid` on the `running`
   transition; the five `catch` logs. The server still ignores everything sent.
4. **The server re-keys, and the verbs require — one transaction, and the
   largest step.** `runEntry` wrapper, the re-keyed registry,
   **verb-class-first resolution with the per-class × per-registry policy**
   (including `run_wrong_owner`), adoption via the **per-id singleflight** with
   terminal-refusal and the administrative rules of Decision 4, the two-half
   terminal latch with the #309 replay as claim step 1b, compare-and-delete, the
   `closedRuns` FIFO ring, the `LastSeen` index ranking, `Scheduler.IsRunLive` /
   `LookupRunByID` / `RecordPhaseStartForRun`, `run_id_required` /
   `run_id_invalid` as hard errors, `ProtocolVersion` 2 with the specified
   hard-fail — with the Go regression suite.

   The re-key and the requirement **cannot be separated**: a registry keyed on
   `runId` that accepts `""` is the issue-number collision under a new name. What
   _can_ be separated, and is, is the wire shape (step 2) and the emitters
   (0b, 3). **Per-verb flip gate — a verb may begin refusing only when every row
   in its "must already assert" column has landed:**

   | Verb                             | Emitter populations that must already assert                                                                                                                                                                           |
   | -------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
   | `pipeline.notifyStageTransition` | scheduler `IpcStageRunner` (0b); extension `PipelineStateService` ×7 raw sites (3)                                                                                                                                     |
   | `pipeline.notifyPhaseTransition` | scheduler `PipelineBridge:280` (0b); extension `PipelineStateService.{startPhase:1076,completePhase:1120}` — both fire-and-forget with silent catches, so their assertion must land with the beginRun installation (3) |
   | `pipeline.notifyStageProgress`   | scheduler `PipelineBridge:355` (0b); extension `HeadlessOrchestrator:13323` and `bootstrap/services.ts:3094` (3)                                                                                                       |
   | `pipeline.notifyComplete`        | extension `PipelineStateService.notifyPipelineComplete:921` (3)                                                                                                                                                        |
   | `pipeline.setPaused`             | extension `PipelineStateService.{pause,resume}Pipeline` (3)                                                                                                                                                            |
   | `pipeline.abandonRun`            | its only caller is the force-clear funnel, which lands **with the verb** in step 6                                                                                                                                     |

5. **The reconciler.** The **liveness ladder** including arm 3 consuming the
   `stagePid` step 3 began sending, the **deferred startup sweep**, the 7.4
   disposition table, the claim-token release rule, and removal split from
   emission. Split out of step 4 because it consumes the registry rather than
   defining it, and because it is the part with its own failure mode (F26/F32).
   Its `resuming-*` rows are inert until step 8 creates the first such file —
   inert, not wrong.
6. **`pipeline.abandonRun`.** The verb and its handler (administrative class,
   corroboration, `MarkAbandoned`, the dispatch-terminal `pipeline_done`), plus
   `abandonRun` in the force-clear funnel, the rule that the funnel never calls
   `notifyComplete`, and the fence re-pin. **Gated on A-1's verification** — if
   the platform is first-writer-wins on outcome, ship the A-1-false variant from
   [Assumptions](#assumptions).
7. `learning.Recorder` idempotency key; #305's multi-run corroboration rule with
   the raise's identity **threaded** and `readCurrentRunId` **deleted**; the
   `AttentionRaiseParams.RunID` doc-comment amendment; #375's run-id payload
   field and trace ref (key unchanged); `--run-id` on the gate CLI plus explicit
   `--run-id` at `HeadlessOrchestrator`'s four spawn sites; `NIGHTGAUGE_RUN_ID`
   in the SDK trace recorder with the `runtime-${issue}.json` read deleted.
8. `CliPipelineReconciliationService` reads `run_id` from the sidecar;
   pause-restore migration (parsed id, per-issue QuickPick, **consume-on-claim
   rename with the UUIDv7 claim token**, and the claim-token-aged release the
   reconciler gained in step 5); the TS stub sweep's filter narrowed to the new
   scheme.
9. The one-shot Go legacy sweep, plus L-1 for its own removal.
10. File every row of [Follow-up issues](#follow-up-issues-filed-at-merge) — R-1,
    R-2 (with the ADR 015 §N amendment landed in this PR), R-5, A-1, L-1, O-1.
