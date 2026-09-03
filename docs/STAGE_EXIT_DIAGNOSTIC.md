# Stage-Exit Diagnostic Records

> Per-stage forensic JSONL emitted by the scheduler on every pipeline stage
> exit (success or failure). The single most important change from issue
> #3605 — it makes the next failure debuggable in 30 seconds instead of an
> hour, and gives every healthy run an anchor for ratio-based health
> analysis.

---

## Why

Before #3605, when a pipeline stage exited unsuccessfully the persisted V3
RunRecord carried almost no information — just `success=false, exitCode=N,
terminalFailureKind=""`. We could not tell stall vs SIGKILL vs claude crash
vs network vs hook failure from disk. Every retro was a guess, and the
incidents kept compounding: #3365 (stop-hook drops commit), #3366 (pr-create
silently fails), #3367 (project fields fetch timeout), #3368 (GraphQL rate
limit during merge), #3382 / #3499 / #3544 (stall-kill), #3591 (mystery
exit, `terminalFailureKind=""`).

The pattern was always the same: by the time anyone read the daily JSONL,
the evidence was already gone. We were band-aiding one mode at a time
because we couldn't see across modes.

This subsystem closes that gap by persisting a **structured record on every
stage exit** — including healthy runs, so the file also anchors what
"normal" looks like.

---

## On-disk Format

```
<workspaceRoot>/.nightgauge/pipeline/exit-records/<UTC-day>.jsonl
```

One JSON object per line. The filename is always `YYYY-MM-DD.jsonl` so
lexicographic sort equals chronological sort — every reader (the CLI, future
retro tooling, ad-hoc `jq` pipelines) relies on this invariant.

Records are appended via `internal/history.AppendJSONL`, the same primitive
used by the V3 daily run-record writer and the knowledge telemetry emitter.
This means **single-line atomicity is guaranteed** (POSIX `O_APPEND` for
cross-process; an in-process mutex for goroutine interleaving).

### Schema

| Field                                                                | Type                   | Source         | Notes                                                                                                                                                                                       |
| -------------------------------------------------------------------- | ---------------------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ts`                                                                 | RFC3339Nano UTC string | Go scheduler   | Set at write time so concurrent stage exits keep monotonic ordering.                                                                                                                        |
| `repo`                                                               | string                 | Go scheduler   | Canonical `owner/name`.                                                                                                                                                                     |
| `issue`                                                              | int                    | Go scheduler   | GitHub issue number.                                                                                                                                                                        |
| `stage`                                                              | string                 | Go scheduler   | One of `issue-pickup`, `feature-planning`, `feature-dev`, `feature-validate`, `pr-create`, `pr-merge`.                                                                                      |
| `run_id`                                                             | string                 | Go scheduler   | UUID v7 from runstate (#3557). Joins this record to the matching V3 RunRecord row.                                                                                                          |
| `session_id`                                                         | string                 | TS SkillRunner | Claude CLI conversation id when captured before exit. Empty when the subprocess never produced a `result` envelope (the most common pathology this record exists to debug).                 |
| `success`                                                            | bool                   | Go scheduler   | The stage's **post-gate** outcome — never the skill's bare exit code. A skill that exits 0 and then fails its post-condition gate records `false` (#125).                                   |
| `exit_code`                                                          | int (ptr)              | Go scheduler   | Pointer-shaped so a real `0` is distinguishable from "never observed".                                                                                                                      |
| `signal`                                                             | string                 | TS SkillRunner | POSIX signal name (`SIGTERM` / `SIGKILL` / …). Empty when the process exited naturally.                                                                                                     |
| `signal_source`                                                      | string                 | TS SkillRunner | Names the in-binary code path that delivered `signal`. One of `stall-kill`, `hard-cap`, `quota-fast-fail`, `runaway-progress`, `processTree-reaper`, `external`. Empty when no signal.      |
| `kill_ceiling`                                                       | string                 | TS SkillRunner | Stable name of the **limit** that terminated the stage — see [Kill Ceilings](#kill-ceilings-161). Empty when the exit enforced no configured limit.                                         |
| `kill_ceiling_value`                                                 | string                 | TS SkillRunner | That ceiling's resolved limit plus how it was derived, e.g. `2400000ms (stall warn threshold 300s (source: static) × NX_RUNAWAY_KILL_MULTIPLE=8)`.                                          |
| `terminal_kind`                                                      | string                 | Go scheduler   | Post-classification terminal failure category from `ClassifyTerminalKind`. Empty on success.                                                                                                |
| `elapsed_ms`                                                         | int64                  | TS or Go       | Total wall time from stage start to exit. Prefers the TS-reported value when forwarded; falls back to the scheduler-measured stage duration.                                                |
| `idle_ms_at_exit`                                                    | int64                  | TS SkillRunner | Milliseconds since the last subprocess output chunk at the moment of exit. Distinguishes wedged-then-killed (large) from killed-mid-activity (small).                                       |
| `tokens.input / .output / .cache_read / .cache_creation / .cost_usd` | -                      | Go scheduler   | Per-stage token / cost snapshot.                                                                                                                                                            |
| `last_bash_command`                                                  | string                 | TS SkillRunner | Most recent `Bash` tool_use input, truncated to 500 chars. Many silent kills happen mid-Bash — this is the strongest single forensic anchor.                                                |
| `last_bash_exit`                                                     | int (ptr)              | TS SkillRunner | Exit code of the matching Bash tool_result. Pointer-shaped so `0` is distinguishable from "never observed".                                                                                 |
| `recent_bash`                                                        | []{cmd, exit}          | TS SkillRunner | The last 10 Bash commands, oldest first, each with its own exit code (`exit` omitted when the result never landed). Superset of `last_bash_command` — its tail is that same command (#156). |
| `stop_hook_errored`                                                  | bool                   | TS SkillRunner | `true` when the stream included a `notification.key == "stop-hook-error"` event before exit.                                                                                                |
| `stderr_tail`                                                        | string                 | TS SkillRunner | Last 4 KB of stderr from the SkillRunner ring buffer. Includes the `[skillRunner] …` kill markers so retro can reconstruct the chosen kill path from a single line.                         |
| `rate_limit_remaining_at_exit`                                       | int                    | Go scheduler   | GitHub GraphQL bucket reading at stage end (REST / GraphQL share a tracker on the Go side). `-1` means "unavailable"; `0+` is a real reading.                                               |
| `concurrent_pipelines_at_exit`                                       | []string               | Go scheduler   | Sibling pipelines that were running concurrently at exit (`owner/repo#number`). Empty when no siblings. Smoking gun for cross-pipeline interference (#3605 / #3591).                        |
| `gate_kind`                                                          | string                 | Go scheduler   | Post-condition gate outcome shape when a gate ran: `ok` \| `no_op` \| `fail` (#3863). Empty when no gate ran.                                                                               |
| `gate_reason`                                                        | string                 | Go scheduler   | Short human-readable reason from that gate. Populated on both dispatch paths since #125, so a retro sees _why_ a gate-caught failure failed without log archaeology.                        |
| `unreclaimed_stashes`                                                | []string               | Go scheduler   | This issue's pipeline stashes still on the stash stack at exit, as `<ref> #<issue> <stage>`. Omitted entirely on a clean exit (#330) — see below.                                           |

### Unreclaimed Stashes (#330)

A stage that stashes to measure against a clean tree is supposed to pop it back.
A killed stage never reaches that line — no `trap`, no `defer`, and no shell
cleanup survives a SIGKILL — so the stash outlives it with no owner and no
expiry. Five accumulated across three sibling repos before anyone looked, the
oldest five months old, one of them holding an entire issue's deliverable.

The reclaim itself cannot be made reliable. What can be guaranteed is that the
leak is never **silent**: the exit record is the one artifact written on every
terminal path, including the ones with no code of their own left to run, so it
is where a stranded stash has to appear.

### Pre-Dispatch Failures (#1329)

"Every terminal path" includes the one with no stage exit at all: a run that
latches terminal between issue pickup and the first stage exit (a spawn error,
a missing config, a dispatcher exception). No subprocess ran, so nothing called
`diagnostics.recordStageExit`, the runtime snapshot was removed at the latch,
and the history record carried only the generic `subagent_crash` fallback kind
with `stages: {}` — a red run with no reason anywhere on disk.

The `pipeline.notifyComplete` handler now writes a synthetic exit record with
`stage: "pre-dispatch"` for that path, and the reason lands in two places:

| Field                     | Where                                | Content                                                                                                                       |
| ------------------------- | ------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------- |
| `failure_detail`          | exit record, `stage == pre-dispatch` | The extension's `PipelineRunResult.error.message`, forwarded as `failureDetail`. Tail-truncated to 2048 runes, `…`-prefixed.  |
| `terminal_failure_detail` | history run record (V3)              | The same text — or, when a stage did fail, that stage's own error. Tail-truncated to 2048 runes. Present on every failed run. |

Only the pre-dispatch path gets a synthetic exit record; a stage that exited
already has a real one, and its reason is the stage's own error.

```bash
# Runs that died before any stage started, with their reason
jq -r 'select(.stage=="pre-dispatch") | "\(.issue) \(.terminal_kind) \(.failure_detail)"' \
  .nightgauge/pipeline/exit-records/$(date +%F).jsonl
```

Scoped to the record's own issue, so a concurrent run's stash is never
attributed to this stage, and omitted entirely when nothing leaked — a field
that is always present is a field readers learn to skip. Reclaim with
`nightgauge stash sweep` (see
[GO_BINARY.md § Stash Reclamation](GO_BINARY.md#stash-reclamation-issue-330)).

```bash
# Every stage exit that stranded a stash, today
jq -r 'select(.unreclaimed_stashes) | "\(.issue) \(.stage) \(.unreclaimed_stashes|join(", "))"' \
  .nightgauge/pipeline/exit-records/$(date +%F).jsonl
```

### Kill Ceilings (#161)

`signal_source` names the **closure** that delivered the signal; several
distinct limits funnel into each of those labels. `runaway-progress` alone
covers four unrelated ceilings, and one of them — the Nx stall multiple — is
computed at runtime and appears in no config file. Issue #161 lost three stages
(~$34, nothing merged) to a ceiling that could not be identified from a
complete exit record: reading `DEFAULT_STAGE_HARD_CAPS`, `stage_time_caps`,
`no_progress_window_ms`, and the cost ceiling ruled every one of them out and
left no candidate.

`kill_ceiling` closes that gap by naming the limit itself, and
`kill_ceiling_value` carries what it resolved to **and how**:

| `kill_ceiling`                | Fires when                                                              | `kill_ceiling_value` is derived from                                              |
| ----------------------------- | ----------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| `progress-no-progress-window` | No productive signal, and no novel tool call, for the window            | `pipeline.progress_runaway.no_progress_window_ms`                                 |
| `progress-churn-tools`        | `churn_tool_threshold` distinct tools with no productive signal         | `pipeline.progress_runaway.churn_tool_threshold`                                  |
| `progress-catastrophic-cost`  | `catastrophic_limit_usd` reached with no productive progress            | `pipeline.progress_runaway.catastrophic_limit_usd`                                |
| `nx-stall-multiple`           | Elapsed reached N× the stall **warn** threshold, no productive progress | `stall warn threshold × NX_RUNAWAY_KILL_MULTIPLE` — **derived, not configurable** |
| `stage-hard-cap`              | Elapsed reached the absolute per-stage cap                              | `pipeline.stage_hard_caps.<stage>`                                                |
| `stage-time-cap`              | Elapsed cap for zero-cost adapters (`provider_scale=0`)                 | `min(stage_hard_caps.<stage>, stage_time_caps.<stage>)`                           |
| `quota-fast-fail-idle`        | Idle past the quota budget after a rate-limit signal                    | `QUOTA_EXHAUSTED_FAST_FAIL_IDLE_MS` or `min(stallKillMs, quota_signal_idle_ms)`   |
| `stall-idle`                  | Idle past `stallKillMs` with no subprocess output                       | Calibrated or static idle-kill threshold                                          |

These names are a diagnostic contract — they are grepped in retros and appear
verbatim in historical records, so treat the set as append-mostly.

```bash
# Which ceiling is killing stages this week, and what was it set to?
cat .nightgauge/pipeline/exit-records/*.jsonl \
  | jq -r 'select(.kill_ceiling != null)
           | [.stage, .kill_ceiling, .kill_ceiling_value] | @tsv' \
  | sort | uniq -c | sort -rn
```

A kill that records no ceiling was not limit enforcement — an operator abort, an
external signal, or a crash.

### Schema Invariants

The JSON tags MUST stay stable once shipped. Additive fields are allowed
(always with `omitempty`), but renames or removals would break the
`nightgauge exit-records tail` reader and any external operator tooling
that `grep`s or `jq`s the daily file.

### `success` is the Post-Gate Outcome (#125)

`success` and the run record's `V2StageDetail.status` describe the same
event and must never disagree. A stage's skill exiting 0 is a **self-report**,
not an outcome: the deterministic post-condition gate that runs afterwards is
what decides the stage (see [STAGE_GATES.md](STAGE_GATES.md) — the
`skill-said-success` failure mode). Both write paths therefore record the
verdict _after_ the gate, with `gate_kind="fail"` and the gate's reason
attached.

Find gate-caught failures with:

```bash
nightgauge exit-records tail --limit 200 --json | jq 'select(.gate_kind == "fail")'
```

> **Reading historical data**: records written before #125 by the VSCode/IPC
> dispatch path carry `success=true` for stages the gate failed, and cannot be
> backfilled — the gate verdict was never persisted anywhere joinable. Treat
> pre-#125 success ratios from that path as an **upper bound**: they
> under-count exactly the failures gates exist to catch. Records with
> `exit_code=0` and no `gate_kind` are the ambiguous population.

---

## How to Read the File

### The CLI

```bash
# Last 20 records, all issues, today's file
nightgauge exit-records tail

# Last 50 records, newest first
nightgauge exit-records tail --limit 50

# Just #3591
nightgauge exit-records tail --issue 3591

# Pipe into jq for ad-hoc analysis
nightgauge exit-records tail --limit 200 --json | jq 'select(.success == false)'
```

The default reader walks daily files **newest-first** so failures land at
the top of the output without the operator having to guess which day's file
holds them. Multi-day walks stop scanning as soon as `--limit` is met.

### Direct `jq` Workflows

Because each line is one JSON object, ad-hoc retro analysis is trivial:

```bash
DAY=$(date -u +%Y-%m-%d)
FILE=.nightgauge/pipeline/exit-records/$DAY.jsonl

# All SIGKILL exits today
jq -c 'select(.signal == "SIGKILL")' "$FILE"

# Stall-kill rate today (% of all stages that ended in stall_kill)
TOTAL=$(wc -l < "$FILE")
STALL=$(jq -c 'select(.terminal_kind == "stall_kill")' "$FILE" | wc -l)
echo "scale=2; $STALL/$TOTAL*100" | bc

# Worst stages this week (by elapsed_ms p95)
cat .nightgauge/pipeline/exit-records/*.jsonl \
  | jq -s 'group_by(.stage) | map({stage: .[0].stage, p95: ([.[].elapsed_ms] | sort | .[(length*0.95|floor)])})'
```

---

## Where the Data Comes From

The record carries **layered evidence** — each field is sourced from the
layer with first-hand knowledge of it:

1. **Always populated by Go (deterministic):**
   - `ts`, `repo`, `issue`, `stage`, `success`, `exit_code`, `elapsed_ms`,
     `tokens.*`, `run_id`.

2. **Populated by Go when a provider fn is attached (production wiring):**
   - `rate_limit_remaining_at_exit` (via `SetRateLimitRemainingFn` — wired
     to the github client's `SharedRateLimitTracker`).
   - `concurrent_pipelines_at_exit` (via `SetRunningSiblingsFn` — wired to
     the autonomous scheduler's `RunningSiblings`).
   - When the provider fn is not attached (CLI-only paths, tests), the
     scheduler falls back to its in-process `activeStages` map for siblings
     (issue numbers only, no repo) and its own `*gh.Client` tracker for
     rate-limit.

3. **Populated by Go when the stage failed:**
   - `terminal_kind` (via `ClassifyTerminalKind` over the stage error text,
     same path the V3 record uses).

4. **Forwarded verbatim from TS SkillRunner via `pipeline.stageResult`:**
   - `session_id`, `signal`, `signal_source`, `idle_ms_at_exit`,
     `last_bash_command`, `last_bash_exit`, `recent_bash`,
     `stop_hook_errored`, `stderr_tail`, `cache_creation_tokens`.
   - These are zero / empty when the TS SkillRunner pre-dates the #3605
     update — the record is still valid, just terser. The schema is
     **forward-compatible**: once the TS side ships, the daily JSONL gains
     richer fields with no Go-side change required.

### Why `recent_bash` and not just `last_bash_command` (#156)

Stage subprocesses run with `--no-session-persistence`, so **no conversation
transcript survives the stage**. This record is not one forensic source among
several — it is the only durable evidence of what a stage did, which is more
weight than a single-value field can bear.

Observed in practice: a validate stage exited with `last_bash_command` = `true`.
That is equally consistent with a benign trailing `|| true` and with a stage
that ran no verification at all, and with no transcript the two are
indistinguishable after the fact. The field answered "what was the last thing it
typed" when the useful question was "what did it actually do".

`recent_bash` keeps the last 10 commands with their own exit codes, so that case
becomes self-answering — ten commands of context show whether a test suite ran
before the no-op tail, with no re-run required:

```bash
nightgauge exit-records tail --limit 200 --json \
  | jq 'select(.stage == "feature-validate") | {issue, recent_bash}'
```

Bounds: 10 entries × 500 chars per command, enforced at the point of persistence
by `diagnostics.BoundRecentBash` on **both** write paths, not merely trusted from
the producer. Commands and exit codes only — capturing per-command stdout/stderr
would reintroduce the size and secret-leakage problems the truncation rules
exist to avoid.

`last_bash_command` / `last_bash_exit` are unchanged and remain the fields
existing readers and retro tooling key on; `recent_bash`'s last entry is that
same command.

### Two Write Paths, One Schema

Records reach the daily file from either dispatch path, and both populate the
same fields:

- **Go scheduler** (`auto` / CLI mode) — `scheduler_exit_record.go`, which also
  fills the Go-only `rate_limit_remaining_at_exit` and
  `concurrent_pipelines_at_exit`.
- **IPC / VSCode mode** — `HeadlessOrchestrator.recordStageExitDiagnostic()`
  calls `diagnostics.recordStageExit`. Token/cost, `exit_code`, and the
  forensic anchors come from the `StageExitTelemetry` captured off the skill
  subprocess at exit (issue #109 — before that fix this path passed `undefined`
  for all of them, so every record it wrote carried `"tokens": {}`).

`tokens.*` is a **per-stage-attempt** figure on both paths, never a
pipeline-wide running total. When one stage attempt spawns more than one
subprocess (the API-error auto-retry), the record sums the attempts — their
accumulators are disjoint, so this cannot double-book. A stage that spawned no
subprocess at all (`pipeline-start`, a pre-stage ceiling block) legitimately
records zero; values are never synthesized.

---

## Why "All Exits, Not Just Failures"

The first instinct is to write only on failure. We deliberately don't.

Recording **healthy** exits too means:

- The daily file anchors what "normal" looks like. `p95(idle_ms_at_exit)`
  trended from 1.2s last week to 18.4s this week is signal — without the
  healthy baseline you can't see the regression.
- Ratio-based dashboards become trivial (stall-kill % of all exits, etc.).
- The on-disk write path is exercised every pipeline run, so a regression
  in the writer is caught the next day rather than the next failure.

The cost is one extra ≤ 1 KB JSONL append per stage — negligible compared
to the V3 record we already write.

---

## Limitations

- **Cross-process sibling discovery** uses the local `activeStages` map
  unless `SetRunningSiblingsFn` is wired by the caller. In single-process
  autonomous mode this captures everything; in a future multi-process
  deployment the workspace sidecar pattern will need to evolve. The CLI's
  in-process fallback writes `?#NUMBER` keys to make the limitation
  obvious.
- **Best-effort writes.** A filesystem failure logs at INFO and never
  blocks pipeline progress — we'd rather lose a diagnostic record than
  fail a stage on a disk error.

---

## Related Files

- `internal/diagnostics/exit_record.go` — schema + writer
- `internal/orchestrator/scheduler_exit_record.go` — scheduler-side
  population
- `internal/orchestrator/scheduler.go` — call site + injection points
  (`SetRunningSiblingsFn`, `SetRateLimitRemainingFn`)
- `internal/ipc/pipeline_messages.go` — IPC contract (TS → Go forwarding)
- `internal/ipc/diagnostics_stage_exit.go` — IPC-mode handler + record builder
- `packages/nightgauge-vscode/src/services/HeadlessOrchestrator.ts` —
  IPC-mode caller (`recordStageExitDiagnostic`)
- `cmd/nightgauge/exit_records.go` — CLI reader

---

## Author

nightgauge
