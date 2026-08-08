# Runtime Identity Keying — the pipeline runtime registry keys on a run, not on an issue number

**Date:** 2026-08-08
**Author:** nightgauge
**Status:** Decided
**Issue:** #370
**Builds on:** #44 (orphan reconciliation), #215 (per-repo state roots),
#304 (outcome recording), #305 (`attention.raise` corroboration), #307
(force-clear terminal bookkeeping), #313/#316 (history record identity and the
single-authoritative-writer rule), #375 (run-scoped raise verb), ADR 013
"Run Lifecycle Trace Schema" (per-run trace keyed by `run_id` — indexed in
[README.md](README.md)), and
[ADR 015 §N](015-decision-requests.md) (which explicitly defers
caller-asserted run identity to this ADR)

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
   server;
2. **carried on every `pipeline.*` call**, not latched on one at-most-once
   message;
3. **adopted on sight** when the server has never seen it and it is not
   provably closed — so a restarted server, a lost `initialized`, or a slow
   socket never locks a live run out of its own records;
4. **refused** only for a run that is provably terminal;
5. **compared at every destructive write, under the lock that resolved it**,
   with the snapshot filename carrying the identity so the path _is_ the check.

The issue number is demoted to a **derived index** for lookup and UX. Two
dispatches of one issue coexist under distinct keys and corrupt nothing; the
index names one of them "current" for issue-addressed reads, and that choice is
cosmetic and self-correcting rather than load-bearing.

The #307 per-dispatch generation token and this run identity are **the same
concept minted in the same instant**, so they become one value rather than two
that must be kept in sync.

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
registry (`internal/orchestrator/scheduler.go:458-467`, `map[int]`) persists into
that same namespace, so any filename change is a two-registry change.

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

## Decisions

### 1. The run identity is a client-minted `run_id`, and it _is_ `RuntimeState.RunID`

There is **one** identity, not two bound together.

- **Format:** UUIDv7 — a time-ordered UUID. Uniqueness comes from the random
  tail; the leading millisecond timestamp makes ids sort by mint time in logs,
  ledger files and trace filenames. **No correctness rule decodes it.** Ordering
  is used only by the issue index (Decision 6), and even there the server's own
  observation timestamp is the comparator. UUIDv7 is chosen for readability and
  for a stable order over snapshots found on disk by a process with no registry;
  it is not a covert protocol.
- **Minted by the dispatcher, always client-side.** The server **never** mints
  a run identity. `notifyStageTransition`'s lazy
  `rt.RunID = uuid.NewString()` is deleted outright, and so is `setPaused`'s
  mint-on-miss. This single rule is what kills identity laundering (F1) and the
  token-less-producer steal (F18) together: a producer that must supply an
  identity it minted itself cannot address a run it did not start.
- **`state.NewRuntimeState` takes the run id as a constructor argument.**
  `RunID` becomes **immutable after construction**. There is no setter. An
  immutable field written once before the value is shared with any goroutine
  needs no lock, which dissolves the two-locks-one-comparator hazard (F26):
  today `RunID` is written under `Server.runtimesMu` and read under
  `RuntimeState.mu` in `snapshotLocked`/`Persist`.
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
identity — the lockout of F17 in a new costume. Minting on the client makes the
identity exist unconditionally, before any I/O that can fail.

### 2. The #307 generation token becomes the run identity (unify, do not bridge)

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

**#307's guarantees are preserved, not weakened.** The tombstone stays
permanent (a revocable tombstone expires exactly when the wedge is worst). The
check-and-claim pairs stay adjacent and `await`-free. `stillOwnsIssue` keeps
reading both `slots` and `reservedSlots`. The sha256-pinned fences in
`internal/orchestrator/testdata/terminal_behaviors.json` are re-pinned
deliberately, in the same commit that adds `abandonRun` to the force-clear
funnel, with the parity test's failure treated as the intended signal it is.

**`PipelineSlot.runId` is repurposed and its old meaning is renamed.** Today it
is "platform run ID from ack — used to route cancel commands to the right slot",
applied from `pendingRunIds` when the slot opens. That becomes `remoteRunId`,
which continues to do only cancel-command routing. When a dispatch **originates**
from a platform `PendingCommand` carrying `RemoteRunID`, the dispatcher seeds
`runId` from it rather than minting — the identity and the platform's run id
coincide, which is the ideal case. Otherwise they are separate values and the
platform materialises its row from the local `runId`, exactly as #1047 does
today with the Go-minted UUID.

### 3. Every `pipeline.*` call carries the identity; the handshake is re-asserted, never latched

| Method                           | `runId`                        | Notes                                                                          |
| -------------------------------- | ------------------------------ | ------------------------------------------------------------------------------ |
| `pipeline.notifyStageTransition` | **required**                   | `initialized` loses all special status — it is one transition among many       |
| `pipeline.notifyComplete`        | **required**                   | terminal claim (Decision 5)                                                    |
| `pipeline.notifyPhaseTransition` | **required**                   |                                                                                |
| `pipeline.notifyStageProgress`   | **required**                   |                                                                                |
| `pipeline.setPaused`             | **required**, and gains `repo` | never creates a runtime (Decision 8)                                           |
| `pipeline.abandonRun`            | **required**                   | new verb (Decision 7)                                                          |
| `pipeline.getState`              | _optional_                     | a lookup, not a run message; issue-addressed reads stay supported (Decision 6) |

There is no separate handshake message and no state machine over message types.
**The identity on the current message is the whole handshake.** Losing any one
message costs exactly that message's content and nothing else — which is the
property the latch-on-`initialized` design failed to have.

The server's rule on every identity-bearing call is three lines:

```
if runId == ""                      → error  run_id_required
if closedRuns.has(runId)            → error  run_closed
if !activeRuntimes.has(runId)       → ADOPT (Decision 4)
                                    → serve
```

**Rejection shape.** A rejection is a **JSON-RPC error** with a machine-readable
`code`, never a success response carrying a status field. Nothing in
`packages/` reads a non-error status field — a `{"status":"stale"}` object is
discarded today, which is how the earlier design's rejections became invisible.
An error rejects the promise, so every existing `try/catch` at minimum logs it,
and fail-open (C9) is preserved by construction: the run continues on its local
cache.

**No rejection is actionable, and none needs to be.** There is deliberately no
re-handshake instruction and no `reHandshakeRequired` flag, because there is no
actor on the TypeScript side to act on one — inventing a recipient for a
rejection is how the permanent lockout was designed in the first place. The
invariant that makes this safe: **a live run can never be rejected.** Unknown
identities adopt; the only errors reachable are for runs that are provably
closed, and a closed run has nothing left to say.

Rejections are **loud and counted** — a `log.Printf` per rejected run id, rate
limited to once per run id per minute on the high-frequency `notifyStageProgress`
path, plus an ADR-013 trace node. Silence on a refusal is how #304's corpus
stayed empty for the life of the product.

### 4. Adoption is the answer to "unknown identity", and it rehydrates

When the server sees a `runId` it has no entry for and that is not in
`closedRuns`, it **creates the entry and serves the call**. This is safe in a
way that mint-on-miss never was, because the identity came from the caller: an
adopting zombie re-creates **its own** run under **its own** key, where every
write it makes lands on its own record and touches no other run.

Adoption **rehydrates from disk when it can.** The snapshot path is fully
derivable from the call's own parameters (`repo` → repo root, plus `issue` and
`runId`), so adoption reads `runtime-{issue}-{runId}.json` and restores the run's
accumulated history rather than starting empty. This turns the ordinary case —
an IPC server restarted mid-run — from lossy into very nearly lossless. It is
also self-verifying: **a terminal run has no snapshot** (Decision 5 removes it),
so the file's presence is itself evidence the run was not closed.

Adoption without a snapshot is still allowed, because a run whose transitions
have not yet carried a repo has no snapshot yet (the `repo != ""` persist gate,
C6e). Such a run adopts with empty history; its eventual record is a skeleton,
and #313's "skeletons never overwrite / richer-upgrade-only" rule already does
the right thing with it — dropped if a richer record for that `run:<id>` exists,
written if it is all we have. That is the intended behaviour in both cases, and
it is an existing guarantee this ADR consumes rather than a new one it needs.

### 5. Terminal is a latch, and destructive writes are compare-and-delete

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
4. **Remove the snapshot** at `runtime-{issue}-{runId}.json`. **The path is the
   identity**, so this cannot take a successor's file even in principle — the
   strongest available form of an identity-checked destructive write.

Once `terminal` is set, the entry refuses every further mutation _and every
further `Persist`_, so a late write from the same run cannot resurrect the
snapshot after step 4.

The unlocked window is no longer a hazard: the only thing it can produce is
another call for the same `runId` — refused by the latch — or calls for other
run ids, which are on different keys. A failed compare in step 3 is not
expected to be reachable and is treated as an assertion: log loudly, **keep**
the record and the outcome (they were written under a valid claim at the time),
and do not delete.

**The `failed`-transition snapshot removal (`server.go:2592`) is deleted.** It
is a second, redundant terminal path — `notifyComplete` fires immediately after
with `Success=false`, as the adjacent comment already says — and it is what let
a zombie destroy a live run's crash snapshot (F3). Worse, it was wrong on its
own terms: if the host dies between the `failed` transition and
`notifyComplete`, the run genuinely never reached a terminal event and deserves
reconciliation, which the removal prevented. After this ADR a snapshot is
removed at exactly two places: a terminal claim (`notifyComplete` /
`abandonRun`), and the reconciler after it has emitted the run's terminal event.

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

**"Current" = the newest non-terminal entry for `repo#issue`,** ordered by the
entry's server-observed `FirstSeen`. Server-observed, so no caller clock is
trusted. Being wrong here is cosmetic and self-correcting: the next transition
from either run re-ranks it.

Repo is **an attribute of the run and a component of the index key, not part of
the identity.** The identity is globally unique on its own, which is what
actually fixes the cross-repo same-issue-number collision (F9) that needs no
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
  the envelope. `PipelineStateService` filters on
  `d.runId === this.runId` (it has the value — the dispatcher handed it over),
  keeping the issue-number comparison as a coarse pre-filter.
  `PipelineSlotsTracker` routes by `runId` → slot. These two filters are the
  migration surface named in C9, and they close F24: a zombie-driven
  `stateChanged` is no longer applied to a successor's slot UI.
- The tree and dashboard show the **current** run for an issue. A second,
  older run is reachable only through the slot card that owns it — which is the
  honest presentation, because a second run of one issue is an anomaly the
  operator should see as one.

Issue-addressed reads may now return **nothing** where they previously returned
a dead run's snapshot indefinitely (F13). That is an accepted improvement: no
answer is better than a confidently wrong one.

### 7. Liveness, abandonment, and reconciliation (#44)

Presence in the registry has never been evidence of liveness — which is why the
`workspace.setRoot` reconcile call site is dead today for exactly the runs that
need it (F12). Three changes fix it, in order of how often they carry the load:

1. **`pipeline.abandonRun {runId, reason}` — a new verb, called by the
   force-clear.** It is the explicit counterpart of `notifyComplete`: same
   terminal claim, same compare-and-delete, same snapshot removal — except that
   it emits the run's terminal `pipeline_done` from the claimed snapshot and
   **does not** write a learning-corpus row (an abandoned dispatch measures
   nothing about model routing). This is not the round-2 mistake of deleting an
   entry one did not create: `abandonRun` names its target by identity, and the
   identity it names is the one it minted.
2. **A lease.** Every accepted call stamps `entry.LastSeen`. The reconciler's
   skip predicate is re-derived from _"this issue has an entry"_ to **"this run
   is live"**: `skipRun(runId)` is true only when a **non-terminal** entry for
   that exact id exists **and** its `LastSeen` is within the liveness window
   (30 minutes; `notifyStageProgress` refreshes it continuously during a long
   stage, so the window is generous by an order of magnitude). The lease is a
   backstop for a lost `abandonRun`, not the primary mechanism — it can only
   fire for a run that both lost its abandon call and went silent for half an
   hour, which is indistinguishable from dead.
3. **`collectOrphanedRuns` keys on the run,** parsing the identity out of the
   filename. Its existing skips are preserved verbatim: **paused** snapshots
   (they power the #2008 pause-restore prompt) and **identity-less** snapshots.
   Emit-then-remove idempotency across activations is unchanged.

Both call sites then work: `Server.Run` (fresh process, empty registry —
unchanged) and `workspace.setRoot` (populated registry — now skips only runs
that are genuinely live). That is the #370 acceptance criterion.

**Retention.** Today there is none, which is why identity-less debris
accumulates. The rules: a terminal claim removes its own snapshot; the
reconciler removes each snapshot after emitting its event; and a startup pass
reconciles-then-removes any snapshot older than **14 days** with no live entry.
**Paused snapshots are exempt from all three** — they are removed only by an
explicit resume/discard or by the TypeScript stub sweep, which is what keeps
#2008 intact.

### 8. Snapshot layout: `runtime-{issue}-{runId}.json`

```
{repoRoot}/.nightgauge/pipeline/runtime-{issueNumber}-{runId}.json
```

Everything C6 requires survives, and the filename becomes the identity check for
destructive writes:

| Requirement                               | How                                                                                       |
| ----------------------------------------- | ----------------------------------------------------------------------------------------- |
| Discoverable by directory scan, no index  | `^runtime-(\d+)-([0-9a-f-]{36})\.json$`                                                   |
| Parseable with no registry                | unchanged — a full `RuntimeState` snapshot                                                |
| Atomic                                    | unchanged — `state.AtomicWriteFile`                                                       |
| Rooted in the run's target repo           | unchanged (#215/#307)                                                                     |
| Never written for an unattributed runtime | the `repo != ""` gates stay, and `Persist` now also refuses an empty `RunID` (Decision 1) |

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

**Third-process seams (C12) thread the identity instead of guessing it:**

- `nightgauge gate verify --record` gains `--run-id`, defaulting to
  `NIGHTGAUGE_RUN_ID` from the stage environment. Without an identity the
  **verdict still runs and is returned**; only the `--record` write is skipped,
  with a loud error. A skipped gate record is an annoyance; a gate record
  written into a guessed file is corruption.
- `packages/nightgauge-sdk/src/events/traceRecorder.ts` stops reading
  `runtime-${issue}.json` to discover a `RunID` and reads `NIGHTGAUGE_RUN_ID`
  from its environment. This deletes a cross-process file read that was wrong
  after any adoption or steal (F16) and that would have broken on the filename
  change anyway.
- The Go scheduler's three `Persist` sites (C11) need no code change —
  `Persist` derives the filename from the snapshot — but they **do** need the
  new filename, and the scheduler already threads `RunID` from runstate, so it
  satisfies the non-empty-identity rule. The two registries stay separate; they
  simply stop sharing a filename with each other's runs.

### 9. Interactions with the systems that consume run state

**#304 outcome recording (C2).** The current at-most-once guarantee is an
emergent property of a bare-issue delete, stated in a comment: _"the runtime is
deleted at the end of this handler, so a repeated `notifyComplete` finds none and
records nothing."_ Adoption makes that sentence false, so it is replaced with an
explicit one: **`learning.Recorder.Record` gains an idempotency key equal to the
run id and skips a row whose id is already present.** In-process, the terminal
latch already makes the handler body run at most once per run; the corpus dedup
is the cross-process/restart backstop. The corpus root stays `s.repoRoot(p.Repo)`
— the run's target repo, never `s.workspaceRoot` — and every loud log for a
missing predicted/actual model or a missing complexity/size keeps firing
unchanged.

**#305 corroboration (C3).** `recordedRunSpendUSD(repo, issue)` resolves the
issue index to the current run and reads that entry; both rules are unchanged —
exact repo match on **both** arms, and real progression (only `CompletedStages`
entries with a `BeginStage`-stamped `StartedAt` and a non-empty `Stage`).

**`attention.raise`'s caller-supplied `RunID` does _not_ become the identity.**
ADR 015 §N deferred that question here, and the answer is no. The safety
property of a client-minted identity is that it addresses only the caller's own
run; a raise is a different producer talking about _someone else's_ run, so
letting it name one by id would hand a caller a selector over runs it did not
start — a strictly worse position than today, where it can only name a
`(repo, issue)` pair the server resolves itself. The `runId` on a raise stays a
**label** carried onto the card, never a selector.

**#313/#316 history identity (C4).** The run identity maps 1:1 onto
`RunRecord.RunID` by construction — it is the same value. `runRecordKey` stays
`"run:" + RunID`; `appendAndIndex` is untouched (one serialized, idempotent
path; coordinator lock across the JSONL append and the index RMW;
first-write-wins; richer-upgrade-only; skeletons never overwrite). No second
writer is introduced anywhere. The **fallback key path** (`issue:{N}|{whole
UTC second}`, F8) becomes **unreachable** on the extension path, because the
identity now exists before the first transition — strictly better than today,
where two dispatches starting in the same second are one run to the ledger.

**Pause-restore (#2008, C5).** The discovery regex in
`bootstrap/services.ts` becomes `/^runtime-\d+-[0-9a-f-]{36}\.json$/`.
`classifyRuntimeStub`'s two existing rules are unchanged (empty repo/stage →
delete; repo mismatch against the containing repo → delete) and it gains a
third: **a file with no identity in its name or body → delete**, since an
identity-less snapshot cannot be restored to a run. Paused snapshots remain
exempt from reconcile-and-delete.

**#375 attention cards (F15).** `BuildAbandonedDispatch`'s `IdempotencyKey`
becomes run-scoped: `producer:repo#issue@runId` instead of
`producer:repo#issue`. Two dispatches of one issue now produce two cards
instead of collapsing into one, and the force-clear card carries the real run id
(it has it — that is the id it tombstoned), so `runTraceRef(runID)` resolves to
the ADR-013 trace instead of nothing. This is safe against ADR 015's
standing-condition semantics because the abandoned-dispatch producer is
run-scoped, not one of the repo-scoped sweep producers whose sticky identity and
mute-until-changed fingerprints depend on a stable key.

**Stage progress and phase transitions (F11).** Both carry the identity and are
resolved by it, so cross-run corruption is structurally gone rather than
guarded against. Neither is silently dropped: a closed run's progress is
refused with `run_closed` (rate-limited in the log), and a merely-unknown run
adopts and keeps streaming. Nothing legitimate goes dark — a refused stream
belongs to a run whose card is already terminal — and the noise on the busiest
path is bounded by log rate-limiting rather than by silence.

### 10. Producers with no dispatch

Every producer mints its own identity at its own dispatch point. None adopts
another's; none is rejected for lacking one, because none lacks one.

| Producer                                   | Decision                                                                       |
| ------------------------------------------ | ------------------------------------------------------------------------------ |
| `ConcurrentPipelineManager.startSlot`      | mints (Decision 2) and hands the id to the slot's `PipelineStateService`       |
| `PipelineStateService.setPaused`           | sends the service's `runId` **and** `repo`; never creates a runtime            |
| `commands/retryFailedIssue.ts`             | **mints.** It is a dispatch; it was simply never treated as one                |
| `bootstrap/services.ts` state service      | **mints** per dispatch, same reason                                            |
| `HeadlessOrchestrator` direct entry points | receive the slot's id when manager-driven; mint at their own entry otherwise   |
| Go scheduler                               | already threads `RunID` from runstate — unchanged, out of registry scope (C11) |
| `nightgauge gate verify --record`          | `--run-id` / `NIGHTGAUGE_RUN_ID`; records nothing without one (Decision 8)     |
| SDK `traceRecorder`                        | `NIGHTGAUGE_RUN_ID`; the file read is deleted (Decision 8)                     |

This is the structural answer to F18. The refuted design's guard was gated on
`token != ""`, so the token-less producers bypassed it, adopted a live run's
runtime, wrote the authoritative record and outcome under it, deleted the
runtime — after which the live run's own completion was refused and it recorded
nothing at all. When _everyone_ mints, there is no token-less producer to
bypass anything.

`setPaused` deserves its own line, because it is three defects in one call
today (F10): it mints a runtime with no repo and no `RunID`, so the next real
dispatch adopts an identity-less stub; it can call `SetPaused` + `Persist` on a
**live successor's** runtime, and `collectOrphanedRuns` skips paused snapshots,
so a hung run's `pipeline_done` is never emitted; and the unattributed entry
pins the issue against #44 forever. Requiring `runId` + `repo` and forbidding
creation kills all three at once.

### 11. Where the identity lives, and under which lock

- `RuntimeState.RunID` — the identity. **Immutable after construction**,
  persisted, needs no lock (Decision 1).
- `runEntry` — a new wrapper struct owned **exclusively** by `runtimesMu`,
  holding the registry's mutable bookkeeping: `Terminal bool`, `FirstSeen`,
  `LastSeen`, and the `*RuntimeState` pointer. **Never persisted, never read
  under `RuntimeState.mu`.**

`RuntimeState` owns the run's _content_ behind its own mutex; `runEntry` owns
the registry's _bookkeeping_ behind the server's. No field is written under one
lock and read under another, which is the specific hazard F26 raised — a
comparator whose torn read a sequential `-race` test would not catch.

---

## Migration

**Pre-customer, no compatibility.** The issue-keyed paths are deleted outright:
no dual-read, no legacy filename support in `LoadPersistedState`, no compat
knob, no optional-`runId` grace period.

**The wire change is required, so `ProtocolVersion` goes 1 → 2.** The
TypeScript client already validates `protocolVersion` in the `ipc.ready` event
and rejects a mismatch, so an old extension paired with a new binary hard-fails
visibly at startup. Lockstep is the _correct_ behaviour here rather than a
regrettable one: the degrade-instead-of-fail alternative is an extension sending
identity-less calls that are all rejected — a silent lockout with exactly F17's
shape, discovered hours later.

**In-flight runs across the upgrade: quiesce is required, and that is
acceptable — stated explicitly.** Upgrading the binary or the extension
terminates runs that are in flight. This ADR makes no attempt to carry a run
across the upgrade, and there is no hidden cost in saying so: an extension host
update already tears down the IPC server and every slot today. The worktree,
branch, and queue state survive; the run does not, and is re-queued.

**Legacy `runtime-{N}.json` on disk: reconcile-then-delete, once.** The
alternative — ignore-and-delete — creates phantom `running` platform rows for
runs that were live moments before the upgrade, which is precisely the defect
#44 exists to prevent. A one-shot startup pass matches the legacy pattern,
emits each snapshot's terminal `pipeline_done` through the existing #44 path,
and deletes the file. It **never** adopts a legacy file into a live run and
`LoadPersistedState` never reads one — it is a sweep, not a shim.

To keep it from becoming an open-ended compatibility path, the sweep ships in
the same PR as the re-keying and its **removal is filed as a follow-up issue at
merge time**, to land one release later. A legacy **paused** snapshot cannot be
restored (it has no identity), so it is reported once in the log with its issue
number and deleted; the operator re-queues the issue, and its worktree is
untouched.

---

## Testing Strategy

The #307 probes become the committed regression suite. Every case below must be
**verified failing against pre-fix code first**, as #307 round 1 required — a
regression test that has never been red is a test of the fix's spelling, not of
the defect. The two named probes lived in a scratchpad directory that no longer
exists and the extension probes were never committed; both are reconstructed
here as permanent tests.

### Go — `internal/ipc/`

| Test                                                                                                                          | Must show                                                                                                                                                                                                                                                                                              |
| ----------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `TestRunIdentity_ForceClearedRunDoesNotResurrect` (from `TestProbe_ForceClearedRunResurrectsAfterSuccessorCompletes`)         | An abandoned run's late `pr-create running` + `notifyComplete` produce **zero** additional run records and **zero** learning-corpus rows. Asserted on file contents: the pre-fix probe's `record[0] runID=436a…outcome=complete` / `record[1] runID=a443…outcome=failed` pair becomes a single record. |
| `TestRunIdentity_SuccessorWithoutInitializedRecordsNormally` (from `TestProbe_SuccessorWithoutInitializedIsLockedOutForever`) | A run whose first message the server never saw has **every** subsequent transition accepted by adoption, and its completion writes exactly one record, one outcome, and full telemetry. This is the test that fails against the refuted design and must never be deleted.                              |
| `TestRunIdentity_ZombieCannotMutateSuccessor`                                                                                 | Interleaving two run ids on one issue: the successor's `TotalCostUSD`, per-stage tokens, `StageErrors`, `PhaseHistory` and `RunRecord` are byte-identical to a solo run. Covers F2, F4, F11.                                                                                                           |
| `TestRunIdentity_TerminalDeleteIsIdentityChecked`                                                                             | A successor's entry installed during `notifyComplete`'s unlocked window survives: its registry entry is intact and its snapshot file still exists on disk. Covers F5 and C7.                                                                                                                           |
| `TestRunIdentity_CrossRepoSameIssueNumberDoNotCollide`                                                                        | Repo A #42 and repo B #42, no force-clear involved, keep separate runtimes, snapshots and records. Covers F9.                                                                                                                                                                                          |
| `TestRunIdentity_SetPausedNeverCreatesARuntime`                                                                               | `setPaused` for an unknown-and-closed id errors; for an unknown-live id adopts with the caller's repo; in no case does a repo-less or identity-less snapshot reach disk. Covers F10.                                                                                                                   |
| `TestRunIdentity_ClosedRunIsRefusedOnEveryMethod`                                                                             | Table-driven across all five identity-bearing methods: each returns `run_closed` as a JSON-RPC **error**, and mutates nothing.                                                                                                                                                                         |
| `TestOrphanReconcile_ClosesAbandonedRunAtRootSwitch`                                                                          | With an abandoned entry in the registry, the `workspace.setRoot` call site emits `pipeline_done` and removes the snapshot — the case that is dead on main (F12). The fresh-start case continues to pass unchanged.                                                                                     |
| `TestOrphanReconcile_PausedAndIdentityLessSnapshotsStillSkipped`                                                              | C1/C5 preservation: paused snapshots and snapshots with no identity are skipped, not reconciled, not deleted.                                                                                                                                                                                          |
| `TestOrphanReconcile_LiveLeaseIsNotReconciled`                                                                                | A run whose `LastSeen` is inside the window is skipped; the same run outside the window is reconciled.                                                                                                                                                                                                 |

### Go — `internal/state`, `internal/learning`

| Test                                             | Must show                                                                                                                                                               |
| ------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TestPersist_RefusesEmptyRunID`                  | No `runtime-42-.json` is ever created; the call errors.                                                                                                                 |
| `TestAdoption_RehydratesFromSnapshot`            | An unknown id whose snapshot exists on disk adopts with its full stage history, not an empty runtime.                                                                   |
| `TestHistory_TwoRunsOfOneIssueProduceTwoRecords` | Covers F7 and F8, including two dispatches starting within one UTC second.                                                                                              |
| `TestLearningRecorder_RecordIsIdempotentByRunID` | Two `Record` calls for one run id append one row. This is the explicit replacement for the deleted "the runtime is deleted so a repeat records nothing" guarantee (C2). |

### TypeScript — `packages/nightgauge-vscode/tests/`

| Test                                                                                  | Must show                                                                                                                                                                                                                                                            |
| ------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ConcurrentPipelineManager.terminalDoubleBook.test.ts` (PROBE-X / PROBE-Y, committed) | The pre-fix sequences `after force-clear: failed=1 completed=0 \| final: failed=1 completed=1` and `afterForceClear=1 final onSlotFailed=2` become `failed=1 completed=0` at **both** checkpoints, with `onSlotFailed` called exactly once. Covers F23 and pins C10. |
| `ConcurrentPipelineManager.runIdentity.test.ts`                                       | The minted `runId` reaches the slot, the reservation, the `PipelineStateService`, and `pipeline.abandonRun` on force-clear; `forceClearedRunIds` holds run ids and has no release path.                                                                              |
| `PipelineStateService.stateChangedRouting.test.ts`                                    | A `stateChanged` for a **different** run id on the **same** issue is ignored. Covers F24.                                                                                                                                                                            |
| `runtimeStubSweep.test.ts` (extended)                                                 | Legacy `runtime-\d+.json` and identity-less new-scheme files classify as `delete`; paused, identified files classify as `keep`.                                                                                                                                      |

### Fences

`internal/orchestrator/testdata/terminal_behaviors.json` holds sha256-pinned
`terminal-parity:begin/end runSlotPipeline-finally` and `force-clear-funnel`
fences. Adding `abandonRun` to the force-clear funnel breaks the parity test **on
purpose**; the re-pin is a reviewed line in the same commit, never a
mechanical refresh. The prose-enforced `accounting` row gains the run identity.

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

**Rejected as insufficient.** It closes F9 and nothing else. Two dispatches of
the _same_ repo's issue still collide, which is every force-clear/zombie window
(F1–F5, F11–F13) — the majority of the failure catalogue and the only reason
#370 exists.

### D — Server-minted identity via a `pipeline.openRun` request/response

An explicit open verb whose **response** carries a server-minted id, making the
handshake request/response rather than fire-and-forget.

**Rejected.** The identity would not exist until a round trip completed, and the
sole client wraps each IPC call in a swallowing `try/catch` behind a 30-second
timeout on a socket whose wedging is the condition being defended against. A
lost response yields a run with no identity — F17 in a new costume, with an
extra verb. Client-side minting (Decision 1) makes the identity exist
unconditionally, before any I/O that can fail.

### E — Two identities: an extension dispatch id plus a derived `RunID`

Keep the #307 generation as the wire identity and bind Go's `RunID` to it on
first sight.

**Rejected.** A binding table is a thing that can be stale, lost, or disagreed
about, which is the defect class this ADR deletes. It would also leave the
force-clear unable to name the Go-side run without a lookup that is exactly as
stale as the wedge that caused the force-clear. See Decision 2.

---

## Consequences

**The registry becomes bounded.** Entries are evicted at terminal, reaped by the
lease, and `closedRuns` is an LRU-capped set. Today every force-cleared run and
every `setPaused` stub is retained for the life of the server, and `getState`
serves the dead run's snapshot for that issue indefinitely (F13).

**Issue-addressed reads can now return nothing.** Accepted, and better than the
current confidently-wrong answer.

**Every `pipeline.*` param type changes.** That means IPC codegen regeneration
and the TypeScript client regen — both already steps in the pre-submission
validation sequence — plus `ProtocolVersion` 2 and a hard-fail on a mismatched
pair.

**In-flight runs do not survive the upgrade.** Stated as a decision, not
discovered as a symptom.

**F14 is narrowed, not closed — residual risk R-1.** Five writers in three
processes still share the `Persist` whole-file last-write-wins contract. After
re-keying they can no longer clobber _another run's_ file — the failure shrinks
from cross-run corruption to same-run field loss between the IPC server and
`nightgauge gate verify --record`. Closing it properly means giving the snapshot
a single authoritative writer, mirroring the #316 discipline (the gate CLI
posting its result through IPC when a server is reachable). That is a separate
change with its own blast radius and is filed as a follow-up, not silently
absorbed here.

**The IPC socket remains unauthenticated — residual risk R-2.** A writer that
can reach the socket can mint run ids and drive a forged run, exactly as it can
today with two calls (`running`, then `complete`). Re-keying does not close
that, and this ADR does not claim it does; #305's corroboration rules (exact
repo, real progression) remain the only mitigation, and socket authentication is
a named follow-up. Anyone who can write to the socket can drive the pipeline
anyway, which bounds but does not eliminate the concern.

**A false-positive lease expiry can re-emit a terminal event — residual risk
R-3.** A live-but-silent run reconciled after 30 minutes gets a `pipeline_done`
it will later contradict. The event is keyed by run id, so the platform side is
idempotent; the local snapshot removal is identity-safe; and the run re-adopts
and re-persists on its next message. Bounded by the window and by
`abandonRun` carrying the load in the common case.

**`closedRuns` does not survive a server restart — residual risk R-4.** After a
restart, a genuinely-closed run's late call adopts rather than being refused,
re-creates a snapshot, and is closed again by the next reconcile — one spurious
`pipeline_done`. It **cannot** double-book: #313's first-write-wins drops the
duplicate record, and Decision 9's corpus dedup drops the duplicate outcome. F1's
actual harm is closed even on this path; only the noise remains.

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
mergeable and independently testable:

1. `RunID` as a `NewRuntimeState` constructor argument, immutable; `Persist`
   refuses an empty identity; the new filename and the `LoadPersistedState` /
   `FindPersistedStatesForIssue` split (Go only; Go scheduler follows the same
   filename by construction).
2. `runEntry` wrapper, the re-keyed registry, the adopt/refuse rule, the
   terminal latch, and compare-and-delete — with the Go regression suite.
3. The wire change: `runId` on all six methods, `pipeline.abandonRun`,
   `ProtocolVersion` 2, codegen regen.
4. Extension unification: the generation becomes the run id, `remoteRunId`
   rename, `stateChanged` / phase-event routing by run id, `abandonRun` in the
   force-clear funnel, fence re-pin.
5. `learning.Recorder` idempotency key; #375 run-scoped `IdempotencyKey`;
   `--run-id` on the gate CLI and `NIGHTGAUGE_RUN_ID` in the SDK trace recorder.
6. The one-shot legacy sweep, plus the follow-up issue for its own removal.
7. Follow-ups filed at merge: R-1 (single authoritative snapshot writer) and
   R-2 (IPC socket authentication).
