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

| Field                                                                | Type                   | Source         | Notes                                                                                                                                                                       |
| -------------------------------------------------------------------- | ---------------------- | -------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ts`                                                                 | RFC3339Nano UTC string | Go scheduler   | Set at write time so concurrent stage exits keep monotonic ordering.                                                                                                        |
| `repo`                                                               | string                 | Go scheduler   | Canonical `owner/name`.                                                                                                                                                     |
| `issue`                                                              | int                    | Go scheduler   | GitHub issue number.                                                                                                                                                        |
| `stage`                                                              | string                 | Go scheduler   | One of `issue-pickup`, `feature-planning`, `feature-dev`, `feature-validate`, `pr-create`, `pr-merge`.                                                                      |
| `run_id`                                                             | string                 | Go scheduler   | UUID v7 from runstate (#3557). Joins this record to the matching V3 RunRecord row.                                                                                          |
| `session_id`                                                         | string                 | TS SkillRunner | Claude CLI conversation id when captured before exit. Empty when the subprocess never produced a `result` envelope (the most common pathology this record exists to debug). |
| `success`                                                            | bool                   | Go scheduler   | Mirrors the scheduler's success flag. Healthy runs carry `true`.                                                                                                            |
| `exit_code`                                                          | int (ptr)              | Go scheduler   | Pointer-shaped so a real `0` is distinguishable from "never observed".                                                                                                      |
| `signal`                                                             | string                 | TS SkillRunner | POSIX signal name (`SIGTERM` / `SIGKILL` / …). Empty when the process exited naturally.                                                                                     |
| `signal_source`                                                      | string                 | TS SkillRunner | Names the in-binary code path that delivered `signal`. One of `stall-kill`, `hard-cap`, `quota-fast-fail`, `processTree-reaper`, `external`. Empty when no signal.          |
| `terminal_kind`                                                      | string                 | Go scheduler   | Post-classification terminal failure category from `ClassifyTerminalKind`. Empty on success.                                                                                |
| `elapsed_ms`                                                         | int64                  | TS or Go       | Total wall time from stage start to exit. Prefers the TS-reported value when forwarded; falls back to the scheduler-measured stage duration.                                |
| `idle_ms_at_exit`                                                    | int64                  | TS SkillRunner | Milliseconds since the last subprocess output chunk at the moment of exit. Distinguishes wedged-then-killed (large) from killed-mid-activity (small).                       |
| `tokens.input / .output / .cache_read / .cache_creation / .cost_usd` | -                      | Go scheduler   | Per-stage token / cost snapshot.                                                                                                                                            |
| `last_bash_command`                                                  | string                 | TS SkillRunner | Most recent `Bash` tool_use input, truncated to 500 chars. Many silent kills happen mid-Bash — this is the strongest single forensic anchor.                                |
| `last_bash_exit`                                                     | int (ptr)              | TS SkillRunner | Exit code of the matching Bash tool_result. Pointer-shaped so `0` is distinguishable from "never observed".                                                                 |
| `stop_hook_errored`                                                  | bool                   | TS SkillRunner | `true` when the stream included a `notification.key == "stop-hook-error"` event before exit.                                                                                |
| `stderr_tail`                                                        | string                 | TS SkillRunner | Last 4 KB of stderr from the SkillRunner ring buffer. Includes the `[skillRunner] …` kill markers so retro can reconstruct the chosen kill path from a single line.         |
| `rate_limit_remaining_at_exit`                                       | int                    | Go scheduler   | GitHub GraphQL bucket reading at stage end (REST / GraphQL share a tracker on the Go side). `-1` means "unavailable"; `0+` is a real reading.                               |
| `concurrent_pipelines_at_exit`                                       | []string               | Go scheduler   | Sibling pipelines that were running concurrently at exit (`owner/repo#number`). Empty when no siblings. Smoking gun for cross-pipeline interference (#3605 / #3591).        |

### Schema Invariants

The JSON tags MUST stay stable once shipped. Additive fields are allowed
(always with `omitempty`), but renames or removals would break the
`nightgauge exit-records tail` reader and any external operator tooling
that `grep`s or `jq`s the daily file.

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
     `last_bash_command`, `last_bash_exit`, `stop_hook_errored`,
     `stderr_tail`, `cache_creation_tokens`.
   - These are zero / empty when the TS SkillRunner pre-dates the #3605
     update — the record is still valid, just terser. The schema is
     **forward-compatible**: once the TS side ships, the daily JSONL gains
     richer fields with no Go-side change required.

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
- **TS-side fields land on a separate PR.** The Go-side reception is wired
  today; once the SkillRunner emits the fields the daily file becomes
  richer with no Go-side change.
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
- `cmd/nightgauge/exit_records.go` — CLI reader

---

## Author

nightgauge
