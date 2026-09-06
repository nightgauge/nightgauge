### Declare every long external process you wait on (`NIGHTGAUGE_PROGRESS:`)

The progress-runaway monitor watches the agent's own message stream: commits,
new files, phase markers, `CI_PROGRESS:` lines, novel tool calls. A stage that
starts a Playwright/Docker/Flutter/emulator run and then waits for it produces
**none of those**, so every clock the monitor has goes cold precisely while the
stage is behaving correctly, and it is killed mid-suite (#1488 — this happened
twice on the same issue, then again on a stage whose own `git commit` was still
running).

The monitor cannot infer the child. **Declare it**, by echoing one line the
moment the process is spawned:

```bash
echo "NIGHTGAUGE_PROGRESS: {\"pid\": $SUITE_PID, \"log\": \"$SUITE_LOG\", \"label\": \"playwright e2e\"}"
```

| Field   | Meaning                                                                           |
| ------- | --------------------------------------------------------------------------------- |
| `pid`   | The child's pid, captured with `PID=$!` **at spawn**. Optional if `log` is given. |
| `log`   | Absolute path of a log the process appends to. Byte growth counts as activity.    |
| `label` | Short human name for the operator-facing "waiting, not stalled" line.             |

Both fields are optional individually but at least one must be present, and
giving both is strictly better — the pid covers a quiet process, the log covers
a process whose pid the shell cannot see (a `docker compose` child, a wrapper
script's grandchild).

While a declared pid is alive **or** a declared log is still growing, the
monitor defers the no-progress kill and suppresses the churn detector, so
polling the log is no longer a stall vector. Three rules make this safe rather
than a way to become immortal, and you should know all three:

1. **The declaration is not progress.** It defers a kill; it can never satisfy
   one. The stage-cost cap, the stage hard-cap and the catastrophic-cost
   backstop are untouched.
2. **A dead pid stops deferring immediately**, and a log that stops growing
   stops deferring at the next poll. Do not declare a pid you have already
   reaped, and never `touch` a log to keep a stage alive.
3. **Twenty minutes is the ceiling.** Past that the monitor stops looking, and a
   wedged child is reclaimed exactly as before
   (`pipeline.progress_runaway.external_progress_ceiling_ms`).

**Capture the pid at spawn and poll with a bounded loop.** `PID=$!` on the line
after the `&`, then `kill -0 "$PID"` with a fixed iteration count. Never
`pgrep -f <script name>` — it matches the polling shell itself and the loop
never exits.

A stage that loops on `ls`/`cat` with nothing declared is still killed at the
window, which is the whole point: the declaration is what separates "waiting on
real work" from "spinning".
