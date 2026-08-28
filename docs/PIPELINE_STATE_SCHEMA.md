# Pipeline State Schema

Catalog of every JSON schema under `.nightgauge/pipeline/`, the durable
write contract that produces them, and the migration semantics for upgrades.
Introduced by Issue #3238 (Gap 1 — graceful pipeline stop with durable,
resumable per-stage context state).

> **Single source of truth**: `run-state.json` is the canonical lifecycle
> record per repo. Every other file under `.nightgauge/pipeline/` is a
> per-stage context handoff (read-only after the producing stage's rename
> completes). The TypeScript SDK and the Go binary both write the same
> on-disk format using the same atomic+fsync contract.

## Table of contents

- [Atomic write contract](#atomic-write-contract)
- [Lifecycle state machine](#lifecycle-state-machine)
- [Schema catalog](#schema-catalog)
- [Schema versioning rules](#schema-versioning-rules)
- [First run after upgrade (pre-Gap-1 migration)](#first-run-after-upgrade-pre-gap-1-migration)
- [History archive layout](#history-archive-layout)
- [Recovery decision tree](#recovery-decision-tree)
- [References](#references)

## Atomic write contract

Every JSON file under `.nightgauge/pipeline/` is written via:

```
write-temp → fsync(file) → rename → fsync(parent dir)
```

A reader observes either the previous version or the new version — never
partial JSON, even on power loss between rename and the next disk flush.

| Runtime    | Helper                                              | File                                                    |
| ---------- | --------------------------------------------------- | ------------------------------------------------------- |
| TypeScript | `atomicWriteJSON(filePath, content)`                | `packages/nightgauge-sdk/src/context/ContextManager.ts` |
| Go         | `runstate.AtomicWriteFile(target, data, perm)`      | `internal/runstate/persist.go`                          |
| Go         | `state.AtomicWriteFile(target, data, perm)` (alias) | `internal/state/runtime_state.go`                       |

Directory fsync is best-effort: macOS treats it as a no-op and Windows /
some FUSE mounts disallow opening a directory as a file. EISDIR/EINVAL/
ENOTSUP/EPERM are swallowed; any other error fails the write.

See ADR-004 (`.nightgauge/knowledge/features/3238-graceful-pipeline-stop-with-durable/decisions.md`) for why this contract applies to every JSON write
under the directory rather than only to `run-state.json`.

## Lifecycle state machine

```
              ┌──────────┐
              │ (start)  │
              └────┬─────┘
                   ▼
              ┌──────────┐  stop      ┌──────────┐  resume
              │ running  │ ─────────▶ │  paused  │ ───────────────┐
              │          │ ◀───────── │          │                 │
              │          │            │          │ ─── discard ─┐  │
              │          │            └──────────┘              │  │
   pr-merge ──┼────▶ ┌──────────┐                               │  │
   succeeds   │      │completed │ (terminal)                    │  │
              │      └──────────┘                               │  │
              ▼                                                 ▼  │
         ┌─────────┐                                       ┌──────────┐
         │ aborted │ ─────────── discard ─────────────────▶│discarded │
         └─────────┘                                       │(terminal)│
                                                           └──────────┘
```

Allowed transitions:

| from        | to                                 |
| ----------- | ---------------------------------- |
| `running`   | `paused` · `completed` · `aborted` |
| `paused`    | `running` · `discarded`            |
| `aborted`   | `discarded`                        |
| `completed` | (terminal)                         |
| `discarded` | (terminal)                         |

**Stop NEVER deletes** branches, worktrees, or context files. Discard is
the only destructive transition.

## Schema catalog

| Filename                   | Schema (Zod)                   | Schema (Go)               | Owning stage / writer                     |
| -------------------------- | ------------------------------ | ------------------------- | ----------------------------------------- |
| `run-state.json`           | `RunStateSchema` (1.0)         | `runstate.RunState` (1.0) | `RunStateManager` (TS + Go)               |
| `issue-<N>.json`           | `IssueContextSchema`           | (read by Go scheduler)    | `/nightgauge-issue-pickup`                |
| `planning-<N>.json`        | `PlanningContextSchema`        | —                         | `/nightgauge-feature-planning`            |
| `dev-<N>.json`             | `DevContextSchema`             | —                         | `/nightgauge-feature-dev`                 |
| `validate-<N>.json`        | `ValidateContextSchema`        | —                         | `/nightgauge-feature-validate`            |
| `pr-<N>.json`              | `PRContextSchema`              | —                         | `/nightgauge-pr-create`                   |
| `feedback-<N>.json`        | `FeedbackContextSchema`        | —                         | `/nightgauge-feature-validate` (signals)  |
| `epic-context-<E>.json`    | `EpicContextSchema`            | —                         | wave orchestrator                         |
| `runtime-<N>-<runId>.json` | (Go only) `state.RuntimeState` | `state.RuntimeState`      | Go scheduler                              |
| `workflow-<runId>.jsonl`   | `JournalRecord` (append-only)  | —                         | `WorkflowExecutor` (orchestration engine) |

### Workflow journal (`workflow-<runId>.jsonl`)

When a stage runs through the multi-agent orchestration engine (see
[docs/WORKFLOW_ORCHESTRATION.md](WORKFLOW_ORCHESTRATION.md)), the
`WorkflowExecutor` writes one append-only journal per run alongside the context
files. It is **not** a context handoff file — it is a durable replay log:

- One `JournalRecord` (`{ event, heartbeatMs }`) per `WorkflowEvent` emission, in
  emission order. `event` is the canonical `schemaVersion: 4` node
  (`WorkflowRun` / `WorkflowPhase` / `SubAgentNode` / `JudgeVerdict`) verbatim;
  `heartbeatMs` is a node-level liveness signal so stale-slot recovery can tell a
  wedged run from one still making progress.
- `resume(runId)` replays the journal to rebuild the node tree, re-emits the
  historical events so a fresh consumer sees the whole run, then re-dispatches
  **only** the not-yet-terminal nodes; completed agents replay their (sanitized)
  `outputRef` instead of re-running. A torn final line from a crash is skipped,
  not fatal.
- The same node tree is mirrored server-side as `workflow_nodes` on the
  platform's `/v1/workflows` SSE stream.

### Run-state envelope (`run-state.json`)

| Field               | Type                                                             | Notes                                                                  |
| ------------------- | ---------------------------------------------------------------- | ---------------------------------------------------------------------- |
| `schema_version`    | `string` matching `^\d+\.\d+$`                                   | Currently `1.0`.                                                       |
| `issue_number`      | `number` ≥ 0                                                     |                                                                        |
| `state`             | `running` \| `paused` \| `completed` \| `discarded` \| `aborted` |                                                                        |
| `run_id`            | UUID v7                                                          | Time-ordered (sortable). Stable across pause/resume; new run = new id. |
| `attempt_number`    | `number` ≥ 1                                                     | Incremented on resume.                                                 |
| `completed_stages`  | `Stage[]`                                                        | Pure log of successful per-stage renames.                              |
| `resume_from_stage` | `Stage` \| `null`                                                | First stage NOT in `completed_stages`.                                 |
| `worktree_path`     | absolute `string` \| `null`                                      | Stop preserves; discard removes.                                       |
| `branch`            | `string`                                                         | Feature branch attached to the run.                                    |
| `created_at`        | RFC 3339                                                         |                                                                        |
| `updated_at`        | RFC 3339                                                         | Refreshed on every transition.                                         |
| `reason`            | `string` \| `null`                                               | Populated for paused/aborted/discarded.                                |
| `recoverable`       | `boolean` \| `null`                                              | Distinguishes recoverable failures from structural mismatches.         |
| `recovery_actions`  | `string[]` \| `null`                                             | Surface for the Gap 2 recovery UX.                                     |
| `attempts`          | `Attempt[]`                                                      | Per-attempt metadata (PID, host_id, last_stage).                       |

### Runtime snapshot (`runtime-<N>-<runId>.json`)

Written **only by the Go scheduler**. There is no TypeScript writer, so the
"both write the same on-disk format" note at the top of this document does not
apply to this one file — the extension has three readers and no writer. The same
bytes are also the `state` payload on the `pipeline.stateChanged` IPC event, so
this table describes the wire shape as well as the file.

`docs/decisions/017-runtime-identity-keying.md` § 8 documents the **filename**,
the discovery regex and the atomic-write contract. This section documents the
**field shape**, which nothing did before (#1012).

**Two things a reader assumes and gets wrong:**

1. **There is no `status` field.** Of the 56 top-level fields, none is named
   `status` — the lifecycle lives in `terminal` / `terminalAt` /
   `terminalOutcome` and `abandoned` / `abandonedAt` / `abandonedReason`.
   (`status` appears only _nested_, on `license` and on each `phaseHistory[]`
   entry.)
2. **On the happy path the file does not flip to a terminal value — it
   disappears.** `SealAndRemove` writes the terminal-stamped snapshot and
   `os.Remove`s that same path inside one critical section, so a watcher waiting
   to observe `terminal: true` on disk will usually observe a deletion instead.
   The durable record of a finished run is the **history record**
   (`history/<YYYY-MM-DD>.jsonl`, field `outcome`), not this file.

| Group          | Fields                                                                                                                                                                                                                                                                                                                              |
| -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Identity       | `repo`, `issueNumber`, `itemId`, `title`, `body`, `branch`, `runId`                                                                                                                                                                                                                                                                 |
| Lifecycle      | `terminal`, `terminalAt`, `terminalOutcome`, `abandoned`, `abandonedAt`, `abandonedReason`, `paused`                                                                                                                                                                                                                                |
| Position       | `stage`, `startedAt`, `stageStart`                                                                                                                                                                                                                                                                                                  |
| Process        | `pid`, `ownerPid`, `worktreeDir`                                                                                                                                                                                                                                                                                                    |
| Totals         | `inputTokens`, `outputTokens`, `totalCostUsd`, `authoritativeChangeClass`, `actualLinesChanged`                                                                                                                                                                                                                                     |
| Progress       | `completedStages`, `skippedStages`, `phaseHistory`, `stageErrors`, `retryCount`, `escalationHistory`, `ralphIterations`                                                                                                                                                                                                             |
| Per-stage maps | `stageModes`, `stageAdapters`, `stageModels`, `stageServedModels`, `stageEfforts`, `stageThinking`, `stageServedEfforts`, `stageServedThinking`, `stageModelSelectionModes`, `stageExecutionPaths`, `stagePuntReasons`, `stageGateResults`, `stageAnomalies`, `stageRecoveryAttempts`, `stageOutputTails`, `terminatingStageTokens` |
| Outcome        | `gateResults`, `prUrl`, `mergedCommitSha`, `mergedAt`, `license`, `licenseExpiredMidRun`, `toolCalls`, `modelRefusalFallbacks`                                                                                                                                                                                                      |

`terminalOutcome` has a closed vocabulary — `complete` | `cancelled` | `failed`
— shared deliberately with the extension path so the two cannot drift.

#### `phaseHistory[]` — `PhaseRecord`

| Field         | Type               | Meaning                                                                  |
| ------------- | ------------------ | ------------------------------------------------------------------------ |
| `stage`       | `string`           | The pipeline stage this phase belongs to.                                |
| `name`        | `string`           | Kebab-case phase id, matching `PHASE_REGISTRY`.                          |
| `index`       | `int`              | The phase's position **in the registry**, not its arrival order (#1008). |
| `total`       | `int`              | The stage's registry phase count.                                        |
| `status`      | `string`           | See below.                                                               |
| `startedAt`   | RFC 3339           | When the phase began.                                                    |
| `completedAt` | RFC 3339 \| absent | Absent while running.                                                    |

`status` values and who writes each:

| Value       | Writer                                                       |
| ----------- | ------------------------------------------------------------ |
| `running`   | `BeginPhase`                                                 |
| `complete`  | `CompletePhase`                                              |
| `skipped`   | `SkipPhase` — a phase the stage chose not to run (#1026)     |
| `failed`    | `FailPhase` — a phase that reported an error (#1026)         |
| `abandoned` | `CompleteStage` — still running when its stage ended (#1009) |

Until #1026 the first two were the only values any writer produced, even though
`skipped` was declared; and a phase that never completed stayed `running`
forever, which read as "the run is stuck here" long after the stage had moved
on. Both are now written, and the set above is pinned by a parity test — a value
added to the Go struct without being documented here fails the build.

## Schema versioning rules

`schema_version` is a `<major>.<minor>` string on every pipeline JSON file.

- Same major + file minor ≤ reader minor → **accept**.
- Same major + file minor > reader minor → readers tolerate extra optional
  fields via `.nullish()`. The `ContextManager` global gate is **major-only**;
  per-schema strict-minor enforcement lives on `run-state.json` via
  `RunStateManager`.
- Major mismatch → hard error: `SchemaVersionMismatch` (TypeScript) /
  `runstate.IsSchemaCompatible() == false` (Go). The error points the user
  at this document.
- Missing `schema_version` is treated as `1.0` for compatibility with
  pre-Gap-1 files.

The reader's expected version is kept in:

- TypeScript: `READER_SCHEMA_VERSION` in `ContextManager.ts`.
- Go: `runstate.SchemaVersion`.

## First run after upgrade (pre-Gap-1 migration)

When the new code runs against a checkout written by the old code (no
`run-state.json`, context files without `schema_version`), we **do not**
attempt implicit migration — see ADR-002.

Behavior:

1. `RunStateManager.detectResume()` returns `kind: "orphaned"` with
   `choices: ["restart", "manual-pickup"]`.
2. The orchestrator surfaces this via the IPC channel; the user (or the
   autonomous orchestrator's skip-and-log path) chooses how to proceed.
3. No on-disk content migration is performed — the user explicitly chooses
   `restart` (archive + new run) or `manual-pickup` (treat as a fresh
   pickup).

Rationale: the pre-Gap-1 state cannot reliably tell us which stage was last
completed (no atomic write guarantee), so any implicit migration is
guessing. A loud `orphaned` record is honest about the uncertainty and gives
the user agency.

## History archive layout

On terminal success (`pr-merge` finishes) or on `discarded`, every live
context file for the issue is moved into:

```
.nightgauge/pipeline/history/<run_id>/
├── issue-<N>.json
├── planning-<N>.json
├── dev-<N>.json
├── validate-<N>.json
├── pr-<N>.json
└── run-state.json   # final snapshot — terminal state
```

The `<run_id>` is the UUID v7 from `run-state.json` (sortable by start time).
The pipeline-history walker (`internal/cmd/batchfailures/extractor.go`)
reads both the daily JSONL files (`history/<YYYY-MM-DD>.jsonl`) and the per-run
directories. The daily files are **not** legacy: `state.WriteV2` documents itself
as the primary write path for the Go scheduler and writes exactly that filename
(#1012). Note the daily file is a LOCAL calendar day (`time.Now()`), while
`exit-records/` stamps a UTC day — the two directories do not line up.

## Recovery decision tree

When the orchestrator (Go scheduler or VSCode extension) starts, it runs
`detectResume(baseDir, branch, hasContextFiles)` and branches:

```
                       ┌─────────────────────────┐
                       │ run-state.json exists?  │
                       └─────────────┬───────────┘
                                     │
                  ┌──────────────────┼──────────────────┐
                  │ no               │                  │ yes
                  ▼                  │                  ▼
       ┌──────────────────┐          │          ┌─────────────────┐
       │ branch / context │          │          │ state == ?      │
       │ files present?   │          │          └────────┬────────┘
       └────────┬─────────┘          │                   │
                │                    │      ┌────────────┼────────────┐
       no       │       yes          │      │            │            │
       ▼        ▼        ▼           │      ▼            ▼            ▼
   ┌──────┐  ┌──────────────────┐   │  running       paused        aborted
   │fresh │  │orphaned          │   │      │            │            │
   └──────┘  │choices: restart, │   │      ▼            ▼            ▼
             │         manual-  │   │  refuse +     resume |    restart |
             │         pickup   │   │  PID check    restart |    discard
             └──────────────────┘   │               discard
                                    │
                                    ▼
                             completed/discarded
                                    │
                                    ▼
                                  fresh
```

## Concurrent-run detection

Detection uses an atomic CAS on `run-state.json` — no OS-level file
locking. See ADR-003.

1. Read `run-state.json`.
2. If `state == "running"` and the most recent attempt's PID is alive
   (signal 0 / `process.kill(pid, 0)`), refuse with `ConcurrentRunRefused`.
3. If the PID is dead, the existing record is treated as a stale writer and
   the new run takes over (`MarkRunning` with `force: false` succeeds).
4. The user-driven CLI may pass `--force-concurrent` to bypass step 2; the
   autonomous orchestrator never does.

## References

- ADR-001 / ADR-002 / ADR-003 / ADR-004 / ADR-005 — see
  `.nightgauge/knowledge/features/3238-graceful-pipeline-stop-with-durable/decisions.md`
- `docs/CONTEXT_ARCHITECTURE.md` — per-stage handoff schemas in detail
- `docs/PIPELINE_EXECUTION.md` — interactive vs headless execution
- `docs/GO_BINARY.md` — `nightgauge run state` subcommand reference
- Issue #3238 — this work
- Issue #3237 — the precipitating incident this fixes
