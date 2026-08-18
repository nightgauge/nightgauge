# Adapter Usage & Quota Model — one snapshot shape, local telemetry first, no invented limits

**Date:** 2026-08-17
**Author:** nightgauge
**Status:** Decided
**Issue:** #658 (epic #657)
**Consumed by:** #659 (status-bar meter), #661 (usage panel)

---

## Executive Summary

Nightgauge's only usage surface today is a single all-time dollar counter in
the status bar (`statusBar.showUsage()`, driven by `UsageLimitsService`
reading `DashboardState.getAggregates("all").totalCostUsd`). It is
adapter-blind, unbounded in time, and has no way to express a subscription
window.

This ADR defines one snapshot shape — `UsageSnapshot` — that every later usage
surface reads, a `UsageProvider` interface that makes per-adapter quota
acquisition pluggable, and `LocalTelemetryUsageProvider`, a first provider
built entirely from telemetry nightgauge already persists.

Three commitments, in priority order:

1. **Nothing reports a number it does not have.** Every `limit` _this PR_
   produces is a locally configured budget ceiling, because local telemetry is
   the only provider wired up — not because no provider signal exists. One
   does, for `claude`, and `UsageWindow.limit` is defined to carry either.
2. **"Cannot say" has exactly one representation** —
   `plan.kind: "unknown"` with an empty window list. Never a fabricated
   percentage, never a zeroed bar.
3. **The window list is open.** Later per-adapter providers add windows the
   local provider cannot produce without a type change.

## Context: what quota signal actually exists

There is **no quota HTTP API**. `UsageLimitsService` states this in its header
— _"No upstream API is available for Claude Code Max quota — tracking is local
and budget-based via user-configured monthly budget."_ — and it is accurate as
far as it goes: `internal/execution/adapters/` and `internal/models/` surface
no remaining/limit/reset field.

**It does not follow that no signal exists.** For the `claude` adapter, a
real, vendor-reported allowance already flows through this codebase on the CLI
stream:

- `utils/tokenParser.ts:433-447` parses a `rate_limit_event` message off
  `--output-format stream-json` into `RateLimitEventData`
  (`tokenParser.ts:135-160`): `resetsAt` (unix seconds), `rateLimitType` (the
  bucket — observed values `"five_hour"`, `"daily"`, `"seven_day"`),
  `utilization` (0-100), `status` (`allowed` / `allowed_warning` / `limited`),
  and overage fields.
- `utils/skillRunner.ts:6437-6446` is its only consumer, and it uses the event
  solely to pause or fast-fail a stage on quota exhaustion. Nothing surfaces it
  as usage.

So this ADR must **not** claim that every limit is necessarily local. It
claims something narrower and true: every limit _this PR produces_ is local,
because `LocalTelemetryUsageProvider` is the only provider wired up.
Surfacing the `rate_limit_event` channel is deliberately left to a follow-up
provider, and the types here are sized so that provider needs no type change
(see the fit check below).

### Why the `rate_limit_event` provider is a follow-up, not this ticket

Beyond #658's ACs naming local telemetry as the first provider, the channel
carries real caveats that deserve their own ticket:

- **Undocumented.** It is not in Anthropic's published CLI contract; it is
  observed behaviour.
- **The wire shape has already moved once.** #3386 found the fields nested
  under `rate_limit_info` where the parser had read them flat; both shapes are
  still accepted. #3448 further found `overageStatus: "rejected"` is emitted
  as a steady state and is not the kill signal it was assumed to be. A usage
  meter built on this needs the same defensive parsing the runner has.
- **Mid-run only.** The event arrives while a stage is streaming. A status bar
  must read something at rest, so the provider needs to persist the last event
  and attach an explicit staleness caveat — `capturedAt` must be the time the
  event was _observed_, not the time it was rendered.
- **No model-family breakdown.** The event names a bucket, not a family, so it
  cannot fill `modelFamily` on its own.
- **`utilization` is a percentage with no denominator.** See the unit fit
  below.

### Two look-alikes that are not adapter quota

| Surface                                                | What it actually measures               |
| ------------------------------------------------------ | --------------------------------------- |
| `PlatformQuotaService`                                 | nightgauge's own hosted run-count quota |
| `rateLimitCircuitBreaker` / `RateLimitRemainingAtExit` | GitHub API rate limits                  |

Neither is an AI-provider allowance. Conflating either into this model would
put a number in front of the user that answers a different question than the
one the meter appears to ask.

A `limit` of `null` means "no ceiling is known", never "no usage".

## Decision

### The shape

```ts
UsageSnapshot { adapter, plan: { kind }, capturedAt, windows: UsageWindow[] }
UsageWindow   { id, label, scope, modelFamily?, used, limit, unit, resetsAt, confidence }
UsageProvider { id, supports(adapter), getSnapshot(adapter) }
```

`getSnapshot` takes the adapter rather than closing over one, because a single
provider legitimately serves several adapters (local telemetry serves five).

`getSnapshot` returns `null` — not an empty snapshot — when a provider claims
an adapter in principle but has nothing to say about it. `AdapterUsageService`
converts that into the unknown snapshot, so the "cannot say" answer is
constructed in exactly one place (`unknownUsageSnapshot`).

### Provider resolution

`UsageProviderRegistry` holds an ordered list; `resolve(adapter)` returns the
first provider that claims it. Order is precedence: a future
`ClaudeCodeUsageProvider` registered ahead of `LocalTelemetryUsageProvider`
takes `claude` and leaves the rest to local telemetry. When nothing claims the
adapter the snapshot is `unknown` with no windows.

### `LocalTelemetryUsageProvider` deliberately does not claim every adapter

It claims `claude`, `codex`, `gemini`, `gemini-sdk`, `grok` and declines
`lm-studio`, `ollama`, `copilot`. This is not a stub — it is the point:

- `lm-studio` / `ollama` run locally against the user's own hardware. Their
  `cost_usd` is a genuine `$0`; the Go writer documents that it never marks
  those `cost_unstamped`. A budget bar for them would sit at 0% forever.
- `copilot` is a flat seat subscription whose real meter is premium requests
  per month — a number nothing in nightgauge's telemetry records.

For those three the honest answer is `unknown`, and the user gets no meter
rather than a meaningless one.

### Fit check against the next provider

The shape was checked against the concrete case — a provider fed by
`RateLimitEventData` — rather than against a hypothetical. Field by field:

| Signal                                    | Fits as                                                             | Type change? |
| ----------------------------------------- | ------------------------------------------------------------------- | ------------ |
| `resetsAt` (unix seconds)                 | `resetsAt: new Date(resetsAt * 1000)`                               | No           |
| `rateLimitType: "five_hour"`              | `scope: "rolling"`                                                  | No           |
| `rateLimitType: "daily" / "seven_day"`    | `scope: "daily"` / `scope: "weekly"`                                | No           |
| a provider-reported ceiling               | `limit` — defined as "budget **or** provider allowance"             | No           |
| the figure being vendor-stated            | `confidence: "measured"`                                            | No           |
| plan shape                                | `plan.kind: "subscription-window"`                                  | No           |
| `status: "limited"` / `allowed_warning`   | derivable from `used`/`limit`; severity is the consumer's to render | No           |
| `utilization` (0-100, **no denominator**) | `used: utilization, limit: 100, unit: "percent"`                    | **Yes**      |
| `isUsingOverage` / `overageStatus`        | not modelled; `used > limit` is legal, the _reason_ is not carried  | Deferred     |

Only one misfit, and it was real: the channel reports a percentage and never
an absolute allowance, so a `"percent"` unit is required for the number to be
stated honestly. It is added here, in the ticket that owns the vocabulary,
rather than left as a type change for the next author to discover — the point
of an open window list is that the next provider is additive. It is reserved,
not produced: local telemetry never emits it.

The distinction that matters: a **vendor-reported** percentage is data. A
percentage _this model_ computes for a window whose real usage it does not
know is a fabrication, and stays forbidden.

`isUsingOverage` is the one signal with no home. Rather than add a speculative
field, the provider that needs it should extend `UsageWindow` then, with a
real consumer in hand.

### Confidence and staleness contract (binding on later providers)

`confidence` is a property of the number, not of the provider:

| Value       | Meaning                                                                         |
| ----------- | ------------------------------------------------------------------------------- |
| `measured`  | Every contributing input carried a figure the provider itself reported.         |
| `estimated` | At least one input was derived (rate card, extrapolation) rather than reported. |
| `unknown`   | At least one input could not be priced/counted; `used` is a floor, not a total. |

**Confidence is never averaged — the weakest input decides.** A total that
folds in one unpriced record is not a total. A window with no contributing
inputs is `measured`: a measured zero ("you have not run this adapter today")
is a real answer, and is distinct from having nothing to say at all, which is
signalled by returning no snapshot.

**Staleness is the consumer's to judge, from `capturedAt`.** The service
re-derives when the cached snapshot has outlived one refresh interval;
providers must stamp `capturedAt` with the time the data was _observed_, not
the time it was requested, so a provider that caches upstream responses cannot
launder a stale figure as fresh.

### Refresh cadence

`AdapterUsageService` refreshes on `ui.limits.polling_interval_seconds`
(default 300s) — the cadence the existing usage surface already polls on. No
second usage-refresh setting is introduced for a user to reconcile. Concurrent
refreshes share one derivation.

The change event fires only when the derived usage actually differs
(`usageSnapshotsEquivalent` ignores `capturedAt`), so a consumer that
re-renders on the event is not woken by the clock alone.

## Finding: `getAggregates()` could not be reused as the reducer

#658's technical notes proposed reusing `DashboardState.getAggregates()`
rather than writing a second reducer, and instructed us to stop and document
if observed behaviour differed. It does, in two ways that decide the design:

1. **No adapter dimension.** `getAggregates()` reduces over
   `PipelineRunSummary`, which has no adapter field at all. Adapter identity
   exists only on `tokens.per_stage[*].adapter` inside the raw history record.
   An adapter-attributed figure is not recoverable from its output.
2. **No calendar windows.** It exposes all-time and "session" totals only —
   there is no daily or monthly bucket to read.

So the dollar reduction happens in the provider, over raw records read through
`ExecutionHistoryReader.readDateRange`. What _is_ reused is the dashboard's
notion of where a session begins: `UsageSessionClock` is satisfied
structurally by `DashboardState.getSessionStartTime()`, so the two surfaces
agree on the boundary instead of inventing a second one. Cost is bucketed on
the run's `started_at` — the same field `computeRecentActivityDelta` buckets
on.

`getAggregates()` remains the reducer for the adapter-blind budget alerts in
`UsageLimitsService`, which this ticket leaves untouched.

### Attribution rule

Only per-stage entries whose `adapter` field is present **and** names the
adapter are counted. Absence means adapter-unknown (the history schema is
explicit about this), and defaulting it would credit one adapter with
another's spend. A corpus whose stages all predate `adapter` therefore yields
no snapshot — correctly, because it contains no evidence about any specific
adapter.

### No new window constant

Every boundary is calendar-derived from the windows #658 asks for (session
start, local midnight, the 1st of the month), so no 7-day or 30-day constant
is introduced. The history read horizon is the earliest of those boundaries
minus one day of padding, which absorbs the skew between the writer's
local-calendar filenames and the reader's UTC day iteration.

## Produced today vs reserved

Every member below either has a producer in this PR or is named here with the
provider that will produce it. Nothing is a permanently-false flag.

| Member                             | Status                  | Producer / reason                                                                                                                                                                                                                                           |
| ---------------------------------- | ----------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `adapter`                          | Produced                | The configured adapter, on every snapshot.                                                                                                                                                                                                                  |
| `plan.kind: "pay-per-token"`       | Produced                | Every `LocalTelemetryUsageProvider` snapshot.                                                                                                                                                                                                               |
| `plan.kind: "unknown"`             | Produced                | Unclaimed adapter, no attributed data, or a failed derivation.                                                                                                                                                                                              |
| `plan.kind: "subscription-window"` | Produced (#709)         | `ClaudeRateLimitUsageProvider`, once a `rate_limit_event` has been observed. A Copilot provider would be the second. Local telemetry cannot observe a plan.                                                                                                 |
| `capturedAt`                       | Produced                | Derivation time.                                                                                                                                                                                                                                            |
| `windows[]`                        | Produced                | Three per local-telemetry snapshot, up to three per Claude subscription snapshot; empty on `unknown`.                                                                                                                                                       |
| `scope: session/daily/monthly`     | Produced                | One window each.                                                                                                                                                                                                                                            |
| `scope: "rolling"`                 | Produced (#709)         | `ClaudeRateLimitUsageProvider` — `rateLimitType: "five_hour"`. Local telemetry still cannot know where a provider's sliding window starts, and guessing is a fabricated percentage.                                                                         |
| `scope: "weekly"`                  | Produced (#709)         | Same provider — `rateLimitType: "seven_day"`.                                                                                                                                                                                                               |
| `modelFamily`                      | **Reserved** (optional) | A provider that buckets per family. Local telemetry could bucket by `per_stage[*].model`, but a per-family _limit_ is what makes the bucket useful and none exists locally; `rate_limit_event` names a bucket, not a family, so it cannot fill this either. |
| `used`                             | Produced                | Summed attributed `cost_usd`; an absolute figure, never a ratio this model derived.                                                                                                                                                                         |
| `limit` (non-null)                 | Produced                | Monthly window, when `monthly_budget_usd > 0`. Defined to carry a provider-reported allowance too; that path is the follow-up provider's.                                                                                                                   |
| `limit: null`                      | Produced                | Session and daily always; monthly when no budget is configured.                                                                                                                                                                                             |
| `unit: "usd"`                      | Produced                | Every local-telemetry window.                                                                                                                                                                                                                               |
| `unit: "percent"`                  | Produced (#709)         | `ClaudeRateLimitUsageProvider` — `utilization` is 0-100 with no denominator, so `used: utilization, limit: 100` is the only honest rendering. Vendor-reported, not model-computed.                                                                          |
| `unit: "tokens"`                   | **Reserved**            | A provider given an absolute token allowance — not derivable from what we persist.                                                                                                                                                                          |
| `unit: "requests"`                 | **Reserved**            | A Copilot provider — premium requests per month.                                                                                                                                                                                                            |
| `resetsAt` (non-null)              | Produced                | Next local midnight (daily), 1st of next month (monthly).                                                                                                                                                                                                   |
| `resetsAt: null`                   | Produced                | Session — no clock resets it, so claiming one would be an invention.                                                                                                                                                                                        |
| `confidence: "measured"`           | Produced                | Priced stages, and measured-zero windows. Also a same-run `rate_limit_event` reading (#709) — the vendor states utilization outright, and only while it is streaming.                                                                                       |
| `confidence: "unknown"`            | Produced                | `cost_unstamped: true` (written by the Go history writer) or `cost_source: "unknown"`.                                                                                                                                                                      |
| `confidence: "estimated"`          | Produced (#709)         | A cached `rate_limit_event` reading. Also mapped from `cost_source: "computed"` — see the gap below for why that input is not yet written.                                                                                                                  |
| `observedAt`                       | Produced (#709)         | Set only when the figure predates the snapshot: a cached vendor reading. Absent means "observed as the snapshot was derived", which is every local-telemetry window.                                                                                        |

### Gap: `cost_source` never reaches the JSONL history

`HistoryStageTokenUsageSchema` declares `cost_source`
(`native` \| `computed` \| `unknown`), `tokenParser.computeStageCost` produces
it, and `PipelineStateService` stores it on live run-state — but Go's
`state.V2StageTokens` has no corresponding field, so the history writer drops
it. `ExecutionHistoryReader` back-fills `"native"` whenever `cost_usd > 0`.

Net effect: on today's on-disk corpus `confidence` resolves to `measured` or
(via `cost_unstamped`) `unknown`; `estimated` is reachable from any
schema-valid record and is honoured by the provider, but nothing in production
writes the input that triggers it. This is worth closing on the Go side — it
is the difference between "$14.20 spent" and "$14.20 estimated from a rate
card" for every non-Claude adapter — and is tracked as a follow-up rather than
widened into this ticket.

## Consequences

- **This PR ships a service with no production consumer.** That is the epic's
  sequencing (#659 wires the status bar, #661 the panel), not an oversight.
  What it does _not_ ship is an unreachable branch or a field nothing can
  populate — the table above is the audit.
- `UsageLimitsService` and `statusBar.showUsage()` are untouched; the existing
  budget threshold alerts keep firing with unchanged behaviour. The mismatch
  between that meter's all-time cost and its monthly budget label is a
  pre-existing issue, out of scope here.
- Adding a provider is additive: implement `UsageProvider`, register it ahead
  of local telemetry, honour the confidence and staleness contract above.

---

## Amendment (#709): the `rate_limit_event` provider, and what it forced

`ClaudeRateLimitUsageProvider` landed as the first producer of
`plan.kind: "subscription-window"`. It reads the `rate_limit_event` envelope
`utils/tokenParser.ts` has always parsed and `utils/skillRunner.ts` has always
consumed for pause/fast-fail — a second reader, not a second data path, and no
new CLI invocation or flag. Three things in this ADR needed amending, and one
of them is a genuine correction rather than a status flip.

**1. `supports()` claiming an adapter can no longer mean "answers for it".**
`ExecutionAdapter` has a single `claude` value covering both the CLI/OAuth
subscription path and the API-key pay-per-token path, and only the first emits
the envelope. A `supports()` predicate that claimed `claude` only when a
reading existed would have to do I/O, breaking this ADR's "cheap, synchronous,
adapter-only" rule; one that claimed `claude` unconditionally would have
silenced the dollar windows for API-key users. So `UsageProviderRegistry`
now returns **every** claiming provider (`resolveAll`) and
`AdapterUsageService.derive` walks them in precedence order, taking the first
non-null snapshot. `null` from a provider is a fall-through, not a verdict;
only an exhausted list produces the unknown snapshot. A provider that throws
is skipped for the same reason.

**Plan kind follows the observed signal, never the adapter name.**

**2. The figure is only observable mid-stream, so it has to be persisted.**
The envelope arrives on a live `claude -p` stdout stream and nothing returns
it at rest, but the status bar samples between runs. `ClaudeRateLimitStore`
keeps the last reading per bucket in
`.nightgauge/usage/claude-rate-limits.json` (per-machine runtime state,
gitignored). `PipelineBridge` is the only writer: `record()` from its
`onRateLimitEvent` callback, `settle()` in the `finally` of `handleRunStage`.

That split is what makes `confidence` honest here. `measured` is reserved for a
reading from the run that is **still streaming**; the moment the stage ends,
`settle()` demotes it and the same number is served as `estimated`. A vendor's
confident statement about an hour ago is an estimate of now. `UsageWindow`
gained one optional field, `observedAt`, to carry the as-of — `capturedAt`
could not, because it says when the snapshot was assembled, not when the
number was true.

**3. A cached reading past its own `resetsAt` is known-wrong, not stale.**
That window has since refilled. Nightgauge cannot know the post-reset
utilization — the user may have spent it in the Claude Code app, outside
nightgauge entirely — so an expired reading is dropped rather than aged, and a
`0%` is never synthesised. When every bucket has expired the provider returns
`null` and the adapter falls through to the dollar windows behind it.

**Stability.** The wire format is unofficial and reverse-engineered; the spike
that recommended this rated it "reverse-engineered, unofficial; measured only
mid-run" (`docs/spikes/662-adapter-usage-quota-signals.md` §2.2). Unknown
bucket names produce no window, malformed persisted entries are discarded
rather than coerced, and a shape change degrades the meter to the pay-per-token
path instead of corrupting it.

**Unchanged:** `LocalTelemetryUsageProvider`, `monthlyBudgetUsd` and its
threshold alerts, and the fast-fail path's reading of the same event. This
added a provider; it removed nothing.
