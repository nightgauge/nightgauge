# Cascading-Failure Circuit Breaker

> Sliding-window failure tracker that pauses the autonomous scheduler with
> `safety:cascading-failures` when N pipeline failures land inside a
> configurable window. NOT in any auto-resume / self-clear path — clearing
> requires explicit operator triage. Companion to the existing rate-limit
> circuit breaker; both now fan out to Discord. (Issue #3605 bullet C)

---

## Why

Recent retros (#3365, #3366, #3367, #3368, #3382, #3499, #3544, #3591) repeatedly
showed the same pathology: an environmental fault (GitHub GraphQL bucket
exhaustion, stop-hook regression, claude-CLI auth drift) fanned out across
several pipelines before anyone noticed. The autonomous scheduler dutifully
re-dispatched after each backoff, burning tokens and worsening the underlying
fault — even when the failure pattern was obviously systemic.

The pre-existing safeguards each handle a different shape of trouble:

- **Per-issue lifetime cap** (`MaxLifetimeFailuresPerIssue`) protects against
  one chronically-bad issue, but a cluster of failures across N different
  issues never trips it.
- **Per-session circuit breaker** (`SafetyRails.CircuitBreakerMax`) is
  **consecutive** failures — interleaved successes reset the counter, so a
  storm hitting 4-of-5 pipelines never fires.
- **Stall-kill / quota-exhausted carve-outs** intentionally don't count
  toward those caps because they're transient.

Net effect: a structural fault could fan out across 6+ failed pipelines
inside half an hour while every per-pipeline safeguard read "fine".

The cascading-failure breaker fills that gap with a sliding-window count
across pipelines and across repos.

---

## How It Trips

1. Every pipeline that ends with `success=false` calls
   `AutonomousScheduler.onPipelineComplete` → which calls
   `cascadeTracker.RecordFailure(repo, number, terminalKind, now)`.
2. The tracker prunes entries older than the configured window, appends the
   new one, and checks `len(entries) >= threshold`.
3. On the threshold-crossing failure, the breaker trips: `state.Status`
   transitions to `safety_tripped`, `PauseTriggeredBy` is set to the
   canonical tag `safety:cascading-failures`, and `PauseReason` carries a
   human-readable summary including every involved issue and its terminal
   kind.
4. The pre-existing `fireStatusChangeLocked` callback emits
   `autonomous.statusChanged` via IPC. The TS extension's status bar shows
   "Paused" and the new safety-notifier path fires a Discord webhook
   (see _Discord wiring_ below).

The breaker fires **exactly once per trip** — subsequent failures inside
the still-tripped window are recorded for forensic context but do NOT
re-emit the trip event. This keeps Discord from being spammed when a
cascade keeps going while the operator is investigating.

### Carve-outs

The following terminal kinds **do NOT** feed the cascade tracker because
they're environmental, not structural — counting them would burn down the
threshold during legitimate retries:

- `TerminalKindStallKill` — agent exceeded idle/hard-cap thresholds
- `TerminalKindRateLimitQuotaExhausted` — Anthropic 5-hour bucket
- `TerminalKindWorktreeUncommitted` — work auto-recovered by failure path
- `TerminalKindBudgetCeiling` — real spend, not a code defect

The `onPipelineComplete` short-circuit branches already `return` before
the cascade-feeding call, so the carve-outs are enforced by control flow
rather than an opt-in list.

---

## Configuration

| Knob                                       | Default            | Description                                                                                                                                                                                                                                           |
| ------------------------------------------ | ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `DefaultCascadeFailureThreshold`           | `3`                | Number of in-window failures that trip the breaker.                                                                                                                                                                                                   |
| `DefaultCascadeFailureWindow`              | `30 * time.Minute` | Sliding-window duration. Wide enough to catch clustered failures across N pipelines (#3499 / #3544 retros showed clusters land inside ~20 min), narrow enough that an early-morning failure plus an unrelated mid-day failure don't trip the breaker. |
| `NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD` env | (override)         | Runtime override for the threshold. Malformed values silently fall back to the default — a typo in a shell rc must never brick the autonomous loop.                                                                                                   |
| `NIGHTGAUGE_CASCADE_FAILURE_WINDOW` env    | (override)         | Runtime override for the window, parsed with `time.ParseDuration` (`30m`, `1h`, `90s`). Malformed values silently fall back.                                                                                                                          |

Env-var overrides are read once at `NewCascadeTracker` time. Subsequent
changes require an autonomous restart. The tracker has no persistent
on-disk state — operator restart implicitly resets it (same as the
existing `SafetyRails` consecutive-failure counter).

---

## Clearing a Trip

Cascade pauses are explicit-triage-only. The only path that clears them is
`AutonomousScheduler.Resume()` (i.e., the operator running
`nightgauge autonomous resume` or clicking Resume in VSCode). Inside
`Resume()`, the cascade tracker's `Reset()` clears both the recorded
failures and the tripped flag so a fresh cluster can re-trip the breaker.

Specifically, the cascade pause is **NOT** in any of these self-clear paths:

- `quota-cooldown auto-clear` (Anthropic 5-hour bucket reset)
- `rate-limit-circuit-breaker autoResumeAfterRecovery` (#3307 GitHub
  bucket recovery)
- The `haltQueueOnSlotFailure → return` defense-in-depth in `Pause()`

If you want the operator to triage and resume from scratch, the breaker
delivers exactly that.

---

## Discord Wiring

Pre-#3605, the rate-limit circuit-breaker pause (`#3577`) only fired a
VSCode toast — invisible if the editor wasn't focused. The cascade
breaker adds a second wire-eligible safety pause, and we deliberately
unify both onto a single Discord notifier so an operator who's set up
the webhook gets pinged for either trigger.

The allowlist of safety triggers that fan out to Discord lives in
`packages/nightgauge-vscode/src/commands/autonomousCommands.ts` as
`CASCADE_PAUSE_TRIGGERS`:

```ts
export const CASCADE_PAUSE_TRIGGERS = new Set<string>([
  "rate-limit-circuit-breaker", // GitHub GraphQL bucket exhausted (#3577)
  "safety:cascading-failures", // 3 failures in 30m window (#3605 C)
]);
```

Other safety triggers (lifetime-failure-cap, budget-ceiling,
health-gate) deliberately stay on the toast-only path because they're
per-issue events the operator is already inside the IDE for.

The notifier is registered at bootstrap (`bootstrap/services.ts`) and
calls `DiscordService.notifySafetyPause(triggeredBy, reason)` — a one-off
webhook POST distinct from the per-pipeline embed lifecycle, so safety
pauses don't interleave with stage updates in the channel.

---

## Operator Runbook

When you see a `safety:cascading-failures` pause:

1. Read the `PauseReason` on the Discord embed (or in the VSCode output
   channel) — it lists every involved issue + terminal kind. This is your
   first read of "what cluster tripped me".
2. Run `nightgauge exit-records tail --limit 20 --failures-only`
   (from PR B / #3605 bullet B) to get the full forensic record for each
   failure — signal source, idle time at exit, stderr tail, rate-limit
   reading at exit, concurrent sibling pipelines.
3. Decide whether the underlying fault is fixed. If yes:
   ```bash
   nightgauge autonomous resume
   ```
   The tracker resets on Resume so the breaker is ready to fire again on
   a fresh cluster.
4. If the underlying fault is NOT fixed (e.g. a GitHub-side outage you
   can't influence), leave autonomous paused. The breaker doing exactly
   what it's supposed to do is the win — burning tokens against an
   ongoing outage is the bug it's there to prevent.

---

## Related Files

- `internal/orchestrator/cascade_tracker.go` — sliding-window tracker
- `internal/orchestrator/cascade_tracker_test.go` — 11 unit tests
- `internal/orchestrator/autonomous.go` — wiring at onPipelineComplete +
  Resume + struct field
- `internal/orchestrator/autonomous_cascade_test.go` — 7 integration tests
- `packages/nightgauge-vscode/src/services/DiscordService.ts` —
  `notifySafetyPause` method
- `packages/nightgauge-vscode/src/commands/autonomousCommands.ts` —
  `CASCADE_PAUSE_TRIGGERS`, `setAutonomousSafetyNotifier`
- `packages/nightgauge-vscode/src/bootstrap/services.ts` — bootstrap
  wiring

---

## Author

nightgauge
